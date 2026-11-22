package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/terminal"
)

// maxExtensionExecs is how many commands extensions may run at once through
// extension.Host.Exec, across every cluster, extension and person; sixteen,
// like the terminals and the tunnels. perPersonExtensionExecs is one person's
// share of them under the server profile, four like the others.
const (
	maxExtensionExecs       = 16
	perPersonExtensionExecs = 4
)

// Exec runs one command for the person behind r, in the order Reveal reads:
// checked, counted, then reviewed and recorded by Gate.Perform with the
// extension and the argv on both records, and run inside it.
func (h host) Exec(r *http.Request, name string, q extension.ExecQuery) (extension.ExecResult, error) {
	if h.ext == "" {
		return extension.ExecResult{}, errors.New("Exec is answered only to a mounted extension, so the record can " +
			"name who asked; this door was handed to none")
	}
	timeout, err := execQueryProblems(q, extension.MaxExecTimeout)
	if err != nil {
		return extension.ExecResult{}, err
	}
	client, err := h.client(r, name)
	if err != nil {
		return extension.ExecResult{}, err
	}
	if err := h.refuseReadOnly(client.Context); err != nil {
		return extension.ExecResult{}, err
	}
	release, err := h.s.execShare().take(r.Context())
	if err != nil {
		return extension.ExecResult{}, err
	}
	defer release()

	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	ctx = access.WithNote(ctx, access.Note{Extension: h.ext, Command: quoteArgv(q.Command)})
	attrs := execAttributes(q.Namespace, q.Pod)

	var out terminal.Output
	_, err = h.s.gate.Perform(ctx, client.Context, attrs, func(ctx context.Context) error {
		var err error
		out, err = terminal.RunCommand(ctx, client, terminal.Command{
			Namespace: q.Namespace, Pod: q.Pod, Container: q.Container, Argv: q.Command,
		}, extension.ExecOutputLimit)
		return err
	})
	var refused *access.Refused
	switch {
	case err == nil:
		return extension.ExecResult{Stdout: out.Stdout, Stderr: out.Stderr, ExitCode: out.ExitCode, Truncated: out.Truncated}, nil
	case errors.As(err, &refused):
		// answers errors.Is for extension.ErrDenied and ErrUnreviewable.
		return extension.ExecResult{}, err
	case errors.Is(err, terminal.ErrNotInImage):
		return extension.ExecResult{}, fmt.Errorf("%w: %q in container %s of pod %s/%s",
			extension.ErrNoSuchCommand, q.Command[0], q.Container, q.Namespace, q.Pod)
	case errors.Is(ctx.Err(), context.DeadlineExceeded) && r.Context().Err() == nil:
		return extension.ExecResult{}, fmt.Errorf("%w (%s, in container %s of pod %s/%s)",
			extension.ErrExecTimedOut, timeout, q.Container, q.Namespace, q.Pod)
	}
	return extension.ExecResult{}, fmt.Errorf("running %q in container %s of pod %s/%s on cluster %q: %w",
		q.Command[0], q.Container, q.Namespace, q.Pod, client.Context, err)
}

// execQueryProblems refuses a query before anything is counted or reviewed,
// and answers its deadline.
func execQueryProblems(q extension.ExecQuery, longest time.Duration) (time.Duration, error) {
	for _, part := range []struct{ what, value string }{
		{"namespace", q.Namespace}, {"pod", q.Pod}, {"container", q.Container},
	} {
		if part.value == "" {
			return 0, fmt.Errorf("a command runs in one container of one pod in one namespace; the %s is missing", part.what)
		}
	}
	if problems := validation.IsDNS1123Label(q.Namespace); len(problems) > 0 {
		return 0, fmt.Errorf("the namespace %q is not one: %s", q.Namespace, strings.Join(problems, "; "))
	}
	if problems := validation.IsDNS1123Subdomain(q.Pod); len(problems) > 0 {
		return 0, fmt.Errorf("the pod name %q is not one: %s", q.Pod, strings.Join(problems, "; "))
	}
	if problems := validation.IsDNS1123Label(q.Container); len(problems) > 0 {
		return 0, fmt.Errorf("the container name %q is not one: %s", q.Container, strings.Join(problems, "; "))
	}
	if len(q.Command) == 0 || q.Command[0] == "" {
		return 0, errors.New("a command names the program it runs; this one names none")
	}
	for _, arg := range q.Command {
		if strings.ContainsRune(arg, 0) {
			return 0, errors.New("an argument of the command holds a NUL byte, which no program can be handed")
		}
	}
	switch {
	case q.Timeout < 0:
		return 0, fmt.Errorf("a command's deadline is not negative; asked for %s", q.Timeout)
	case q.Timeout == 0:
		return extension.ExecTimeout, nil
	case q.Timeout > longest:
		return 0, fmt.Errorf("a command's deadline is at most %s; asked for %s", longest, q.Timeout)
	}
	return q.Timeout, nil
}

// quoteArgv is the argv as the record carries it: each argument quoted, so the
// record says where one ends and the next begins.
func quoteArgv(argv []string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = strconv.Quote(arg)
	}
	return strings.Join(quoted, " ")
}

// execShare is the server's count of the commands extensions run, made on first
// use with the server profile's share. A server built without New (tests) still
// gets one.
func (s *Server) execShare() *countedExecs {
	s.execsOnce.Do(func() {
		s.execs = &countedExecs{limit: maxExtensionExecs, held: map[string]int{}}
		if s.inCluster {
			s.execs.perPerson = perPersonExtensionExecs
		}
	})
	return s.execs
}

// countedExecs is the ceiling and the per-person share, as countedTunnels is
// for tunnels: a slot is taken before anything is reviewed, so a refusal costs
// the cluster nothing.
type countedExecs struct {
	limit     int
	perPerson int

	mu   sync.Mutex
	open int
	held map[string]int
}

// take claims a slot for the person ctx carries and answers how to give it
// back. The shared password account has no share and counts against the
// ceiling alone; the ceiling is said first.
func (c *countedExecs) take(ctx context.Context) (func(), error) {
	who, _ := identity.From(ctx)
	key, share := who.Key(), !who.Shared() && c.perPerson > 0
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.open >= c.limit {
		return nil, fmt.Errorf("%w: %d at once is the console's limit; try again when one has finished",
			extension.ErrExecBusy, c.limit)
	}
	if share && c.held[key] >= c.perPerson {
		return nil, fmt.Errorf("%w: your pages already run %d, which is one person's share of this shared "+
			"console; try again when one has finished", extension.ErrExecBusy, c.perPerson)
	}
	c.open++
	if share {
		c.held[key]++
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.open--
			if share {
				if c.held[key]--; c.held[key] <= 0 {
					delete(c.held, key)
				}
			}
		})
	}, nil
}

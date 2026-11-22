package web

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/audit"
)

// Host.Exec is the console's exec door handed to an extension: checked before
// anything is counted, counted before anything is reviewed, reviewed for the
// person as `create pods/exec` on the pod, recorded with the extension and the
// argv, and only then sent. Driven against surfaceServer, which records every
// path it is asked and does not speak the exec protocol: what these tests pin
// is what reaches the cluster and what is written, not a command's output.

const execExt = "jvm"

// hostExecServer is a console over surfaceServer under the server profile,
// whose reviews answer the exec question with exec(q), and whose audit log
// writes to out.
func hostExecServer(t *testing.T, exec func(accesstest.Question) (bool, error), out io.Writer) (*Server, *surfaceServer) {
	t.Helper()
	manager, fake := surfaceManager(t)
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		if q.Attributes.Subresource == "exec" {
			return exec(q)
		}
		return true, nil
	}}
	s, err := New(Options{Clusters: manager, InCluster: true,
		Access: access.New(access.Options{Reviews: reviews.For, InCluster: true, Audit: audit.New(out)})})
	if err != nil {
		t.Fatal(err)
	}
	return s, fake
}

func jcmdQuery() extension.ExecQuery {
	return extension.ExecQuery{Namespace: "shop", Pod: "api-1", Container: "app",
		Command: []string{"jcmd", "1", "VM.version"}}
}

func askedToExec(fake *surfaceServer) bool {
	for _, path := range fake.asked() {
		if strings.Contains(path, "/exec") {
			return true
		}
	}
	return false
}

func TestAnExecThePersonMayNotMakeIsRefusedUnsentAndRecordedWithItsCommand(t *testing.T) {
	sink := &revealSink{}
	s, fake := hostExecServer(t, func(accesstest.Question) (bool, error) { return false, nil }, sink)
	_, err := s.hostFor(execExt).Exec(asGrace(), "prod", jcmdQuery())
	if !errors.Is(err, extension.ErrDenied) {
		t.Fatalf("a denied exec answered %v, want ErrDenied", err)
	}
	if askedToExec(fake) {
		t.Errorf("a denied exec reached the cluster: %v", fake.asked())
	}
	got := sink.records(t)
	if len(got) != 2 || got[0].Phase != audit.Attempt || got[1].Phase != audit.Outcome || got[1].Outcome != audit.Refused {
		t.Fatalf("a denied exec wrote %+v, want a refused attempt and outcome", got)
	}
	for _, r := range got {
		if r.Act != audit.Exec || r.Verb != "create" || r.Resource != "pods" || r.Subresource != "exec" ||
			r.Namespace != "shop" || r.Name != "api-1" || r.Extension != execExt ||
			r.Command != `"jcmd" "1" "VM.version"` || r.Who != "grace@example.com" || r.Decision != string(access.Denied) {
			t.Errorf("the %s record reads %+v", r.Phase, r)
		}
	}
}

func TestAnExecThatCannotBeReviewedIsRefusedUnderTheServerProfile(t *testing.T) {
	s, fake := hostExecServer(t, cannotAsk, io.Discard)
	_, err := s.hostFor(execExt).Exec(asGrace(), "prod", jcmdQuery())
	if !errors.Is(err, extension.ErrUnreviewable) {
		t.Fatalf("an unreviewable exec answered %v, want ErrUnreviewable", err)
	}
	if askedToExec(fake) {
		t.Errorf("an unreviewable exec reached the cluster: %v", fake.asked())
	}
}

// Allowed, the request goes to the pod's exec subresource, and both records
// say which extension ran which command. The fake does not speak the exec
// protocol, so the outcome is a failure: what is pinned is the request and the
// record, not the output.
func TestAnAllowedExecIsSentToThePodAndRecordedAsTheExtensions(t *testing.T) {
	sink := &revealSink{}
	s, fake := hostExecServer(t, func(accesstest.Question) (bool, error) { return true, nil }, sink)
	_, err := s.hostFor(execExt).Exec(asGrace(), "prod", jcmdQuery())
	if err == nil {
		t.Fatal("an exec against a server that does not speak exec answered no error")
	}
	if errors.Is(err, extension.ErrDenied) || errors.Is(err, extension.ErrUnreviewable) {
		t.Fatalf("an allowed exec read as a refusal: %v", err)
	}
	sent := false
	for _, path := range fake.asked() {
		if strings.HasSuffix(path, "/api/v1/namespaces/shop/pods/api-1/exec") {
			sent = true
		}
	}
	if !sent {
		t.Errorf("an allowed exec never reached the pod's exec subresource: %v", fake.asked())
	}
	got := sink.records(t)
	if len(got) != 2 || got[0].Decision != string(access.Allowed) || got[1].Outcome != audit.Failed {
		t.Fatalf("an allowed, failed exec wrote %+v", got)
	}
	for _, r := range got {
		if r.Extension != execExt || r.Command != `"jcmd" "1" "VM.version"` {
			t.Errorf("the %s record does not name the extension and its command: %+v", r.Phase, r)
		}
	}
}

// A door handed to no extension has nobody to record the command as.
func TestAnExecThroughAHostWithNoExtensionIsRefused(t *testing.T) {
	sink := &revealSink{}
	s, fake := hostExecServer(t, func(accesstest.Question) (bool, error) { return true, nil }, sink)
	if _, err := s.Host().Exec(asGrace(), "prod", jcmdQuery()); err == nil {
		t.Fatal("a host handed to no extension ran a command")
	}
	if askedToExec(fake) || len(sink.records(t)) != 0 {
		t.Errorf("a refused anonymous exec reached or recorded something: %v", fake.asked())
	}
}

func TestAnExecOnAClusterNotInTheKubeconfigSaysSo(t *testing.T) {
	s, _ := hostExecServer(t, func(accesstest.Question) (bool, error) { return true, nil }, io.Discard)
	_, err := s.hostFor(execExt).Exec(asGrace(), "nowhere", jcmdQuery())
	if !errors.Is(err, extension.ErrUnknownCluster) {
		t.Fatalf("an exec on an unknown cluster answered %v", err)
	}
}

// A query that cannot be one is refused before it is counted, reviewed,
// recorded or sent.
func TestAMalformedExecIsRefusedBeforeAnythingHappens(t *testing.T) {
	for name, change := range map[string]func(*extension.ExecQuery){
		"no container":       func(q *extension.ExecQuery) { q.Container = "" },
		"no pod":             func(q *extension.ExecQuery) { q.Pod = "" },
		"a namespace path":   func(q *extension.ExecQuery) { q.Namespace = "shop/../kube-system" },
		"a pod path":         func(q *extension.ExecQuery) { q.Pod = "api-1/exec" },
		"no program":         func(q *extension.ExecQuery) { q.Command = nil },
		"an empty program":   func(q *extension.ExecQuery) { q.Command = []string{"", "x"} },
		"a NUL byte":         func(q *extension.ExecQuery) { q.Command = []string{"jcmd", "1\x00rm"} },
		"a negative timeout": func(q *extension.ExecQuery) { q.Timeout = -time.Second },
		"too long a timeout": func(q *extension.ExecQuery) { q.Timeout = extension.MaxExecTimeout + time.Second },
	} {
		t.Run(name, func(t *testing.T) {
			sink := &revealSink{}
			s, fake := hostExecServer(t, func(accesstest.Question) (bool, error) { return true, nil }, sink)
			q := jcmdQuery()
			change(&q)
			if _, err := s.hostFor(execExt).Exec(asGrace(), "prod", q); err == nil {
				t.Fatal("a malformed exec was accepted")
			}
			if askedToExec(fake) || len(sink.records(t)) != 0 || s.execShare().open != 0 {
				t.Errorf("a malformed exec reached, recorded or held something: %v", fake.asked())
			}
		})
	}
}

// Under the server profile one person holds at most their share, and the next
// command is refused before it is reviewed or recorded; another person still
// runs theirs.
func TestOnePersonsExecsAreBoundedToTheirShare(t *testing.T) {
	sink := &revealSink{}
	s, fake := hostExecServer(t, func(accesstest.Question) (bool, error) { return true, nil }, sink)
	var gives []func()
	for range perPersonExtensionExecs {
		give, err := s.execShare().take(grace())
		if err != nil {
			t.Fatalf("taking one of grace's %d slots: %v", perPersonExtensionExecs, err)
		}
		gives = append(gives, give)
	}
	_, err := s.hostFor(execExt).Exec(asGrace(), "prod", jcmdQuery())
	if !errors.Is(err, extension.ErrExecBusy) {
		t.Fatalf("grace's fifth exec answered %v, want ErrExecBusy", err)
	}
	if askedToExec(fake) || len(sink.records(t)) != 0 {
		t.Errorf("an exec past the share reached or recorded something: %v", fake.asked())
	}
	for _, give := range gives {
		give()
		give() // a second give returns nothing more
	}
	if open := s.execShare().open; open != 0 {
		t.Errorf("%d slots still held after every one was given back", open)
	}
}

func TestTheConsoleRunsAtMostItsCeilingOfExecs(t *testing.T) {
	c := &countedExecs{limit: 2, held: map[string]int{}}
	first, err := c.take(grace())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.take(grace()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.take(grace()); !errors.Is(err, extension.ErrExecBusy) {
		t.Fatalf("a third exec at a ceiling of two answered %v", err)
	}
	first()
	if _, err := c.take(grace()); err != nil {
		t.Errorf("a slot given back was not taken again: %v", err)
	}
}

func TestTheRecordedCommandQuotesEachArgument(t *testing.T) {
	if got := quoteArgv([]string{"jcmd", "1", "VM.native_memory", "summary scale=MB"}); got !=
		`"jcmd" "1" "VM.native_memory" "summary scale=MB"` {
		t.Errorf("quoteArgv = %s", got)
	}
}

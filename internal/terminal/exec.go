package terminal

// A shell inside a pod, over the same socket protocol as the shell on the
// cluster, so the UI mounts one terminal component for both.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
	streamhttp "k8s.io/streaming/pkg/httpstream"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/safego"
	"github.com/thonicdev/kubetower/textutil"
)

// defaultExecCommand is what the operator gets when they ask for a shell
// without saying which. /bin/sh, not /bin/bash: distroless and Alpine images
// have no bash, and falling back would need a shell to run the fallback in.
var defaultExecCommand = []string{"/bin/sh"}

// nameRE matches Kubernetes object names, all that ever reaches the exec URL.
// client-go escapes path segments, so this is not about injection: a name
// refused here is one line in the terminal, where the API server's refusal
// would be an upgrade failure that looks like a terminal that connected and
// died.
var nameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

func validName(s string) bool {
	return s != "" && len(s) <= 253 && nameRE.MatchString(s)
}

// ExecHandler serves a shell inside a pod over a WebSocket.
//
// Parameters: cluster, namespace, pod, optionally container, and command
// repeated once per argument.
func (t *Terminal) ExecHandler() http.Handler {
	return t.execHandler(wireRaw)
}

// FramedExecHandler serves the same pod shell over api/websocket.md's frame
// vocabulary. See FramedShellHandler for why there are two wires and one
// session.
func (t *Terminal) FramedExecHandler() http.Handler {
	return t.execHandler(wireFramed)
}

func (t *Terminal) execHandler(speaks wire) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.serveExec(w, r, speaks)
	})
}

func (t *Terminal) serveExec(w http.ResponseWriter, r *http.Request, speaks wire) {
	name, ok := t.clusters.Resolve(clusterParam(r))
	if !ok {
		http.Error(w, "unknown cluster", http.StatusNotFound)
		return
	}

	// The read-only guardrail applies here too: `exec` is in the shell's
	// mutatingVerbs, and this endpoint would otherwise open the same shell through
	// client-go. A guardrail that refuses through one door and permits through the
	// next teaches people the marking is noise. Refused, not merely logged: this is
	// a root-equivalent prompt inside a running workload.
	if t.opts.ReadOnly != nil && t.opts.ReadOnly(name) {
		http.Error(w, name+" is marked read-only in KT_READONLY_CLUSTERS; exec is refused there",
			http.StatusForbidden)
		return
	}

	q := r.URL.Query()
	namespace := pathOrQuery(r, "namespace")
	pod := pathOrQuery(r, "pod")
	if !validName(namespace) || !validName(pod) {
		http.Error(w, "namespace and pod must be Kubernetes names", http.StatusBadRequest)
		return
	}
	container := q.Get("container")
	if container != "" && !validName(container) {
		http.Error(w, "container must be a Kubernetes name", http.StatusBadRequest)
		return
	}

	command := q["command"]
	if len(command) == 0 {
		command = defaultExecCommand
	}

	conn, err := t.upgrader.Upgrade(w, r, nil)
	if err != nil {
		t.logf("exec upgrade failed: %v", err)
		return
	}
	stream := t.openStream(conn, speaks)
	defer stream.close()

	// Whether this person may open it, asked before anything else is spent:
	// `create pods/exec` on this pod, in a review naming them. Under the server
	// profile the ServiceAccount may exec where the person may not, so the API
	// server's refusal cannot stand in. After the upgrade, since a refused
	// handshake reaches the browser with no reason. runExec asks again inside the
	// door, from the held answer.
	//
	// As for every other write (access.Gate.LetsThrough): on the desktop an
	// answer that could not be established lets the shell open, since the API
	// server checks the same identity; a denial refuses on every profile.
	attrs := execAttributes(namespace, pod)
	answer := t.opts.Access.Review(r.Context(), name, attrs)
	if !answer.Allowed() && !t.opts.Access.LetsThrough(answer) {
		refused := &access.Refused{Cluster: name, Attributes: attrs, Answer: answer}
		state := stateForbidden
		if answer.Decision != access.Denied {
			state = stateUnreadable
		}
		// The refusal's audit record, written here because Perform never sees this
		// attempt. A record that cannot be written changes nothing (the attempt is
		// refused already; the gate logs it). Written before the pane is told, so the
		// record is never behind the answer.
		pending, _ := t.opts.Access.Begin(r.Context(), t.opts.Access.Record(r.Context(), audit.Exec, name, attrs, answer.Decision))
		t.opts.Access.End(r.Context(), pending, audit.Refused, "")
		t.logf("%s", identity.Line(r.Context(), "exec refused on %q %s/%s: decision=%s", name, namespace, pod, answer.Decision))
		stream.refuse(state, codeVerbDenied, refused.Error())
		return
	}

	// The shell's ceiling, checked after the upgrade for the reason serveShell
	// gives. A pod exec and a shell are the same resource, so they share one count.
	release, err := t.hold(r.Context(), stream)
	if err != nil {
		stream.refuse(stateUnreadable, codeNotSupportedHere, err.Error())
		t.logf("%s", identity.Line(r.Context(), "exec refused on %q %s/%s: %v", name, namespace, pod, err))
		return
	}
	defer release()

	// Re-checked while open: the sign-in, the review, the idle bound (watch.go).
	// It ends with the stream. Through safego, and a panic closes the shell: a
	// shell nobody re-checks must not stay open.
	safego.Go("exec: re-checking an open shell", func() error {
		t.watch(r.Context(), stream, name, attrs)
		return nil
	}, func(err error) {
		if err != nil {
			stream.farewell(stateUnreadable, codeUnclassified,
				"this shell was closed: the console's re-check of it stopped on an internal error")
		}
	})

	// Who opened it, on this line and its closing twin: under the server profile
	// the API server's audit sees the ServiceAccount, not the person. Written from
	// inside the door (runExec's opened), so a shell refused there never reads as
	// opened.
	opened := false
	open := func() {
		opened = true
		t.logf("%s", identity.Line(r.Context(), "exec opened on %q %s/%s decision=%s", name, namespace, pod, answer.Decision))
	}
	// When the stream itself failed, runExec has already put the failure on
	// screen; the report below covers errors raised before anything opened, and on
	// the other path writes into a closed socket and is dropped, so it can stay
	// unconditional.
	err = t.runExec(r.Context(), stream, execTarget{
		cluster:   name,
		namespace: namespace,
		pod:       pod,
		container: container,
		command:   command,
	}, open)
	if !opened {
		// Refused inside the door, before anything opened (the audit record could not
		// be written, or the held answer changed): said as a refusal, not as a shell
		// that ended.
		t.logf("%s", identity.Line(r.Context(), "exec refused on %q %s/%s: %v", name, namespace, pod, err))
		stream.refuse(execRefusal(err))
		return
	}
	// `exec` terminates, so an exit is expected (api/websocket.md). streamExec says
	// it before the socket closes; here it would arrive after.
	if err != nil {
		stream.refuse(stateUnreachable, codeUnclassified, fmt.Sprintf("exec ended: %v", err))
	}
	t.logf("%s", identity.Line(r.Context(), "exec closed on %q %s/%s", name, namespace, pod))
}

// The line written into a terminal pane is textutil.FirstLine and deliberately
// uncapped. The cut is because a terminal returns to column 0 only on "\r", so
// a multi-line error would staircase off the pane. The length is not capped: a
// pane wraps and scrolls, and the sentence an admission webhook or a kubelet
// writes is what the operator needs whole.

// execRefusal is the framed refusal for a shell that was not opened: no_shell
// where the runtime said the program is not in the image, unclassified for
// anything else.
func execRefusal(err error) (state, code, message string) {
	if notInImage(err) {
		return stateUnreadable, codeNoShell, "the shell was not opened: " + textutil.FirstLine(err.Error())
	}
	return stateUnreadable, codeUnclassified, fmt.Sprintf("the shell was not opened: %v", err)
}

// noShellMessage is the sentence for a shell whose program is not in the
// image, naming the program and the container so the pane says which.
func noShellMessage(target execTarget, err error) string {
	program := "the shell"
	if len(target.command) > 0 {
		program = target.command[0]
	}
	where := "this container's image"
	if target.container != "" {
		where = "the image of container " + target.container
	}
	return program + " is not in " + where + ": " + textutil.FirstLine(err.Error())
}

type execTarget struct {
	cluster   string
	namespace string
	pod       string
	container string
	command   []string
}

// execAttributes is the review a shell inside a pod needs: `create` on the
// pod's `exec` subresource, on that pod.
func execAttributes(namespace, pod string) access.Attributes {
	return access.Attributes{Verb: "create", Version: "v1", Resource: "pods", Subresource: "exec", Namespace: namespace, Name: pod}
}

// runExec opens the stream through the access review: nothing reaches the
// kubelet unless the person behind ctx may `create pods/exec` on the pod. A pod
// exec and a node shell both come through here, and internal/access's walk holds
// streamExec to being reached from nowhere else.
//
// opened, when set, runs inside the door just before the stream opens, after
// the review and the attempt's audit record, so "opened" is logged only for a
// shell that did.
func (t *Terminal) runExec(ctx context.Context, s *stream, target execTarget, opened func()) error {
	_, err := t.opts.Access.Perform(ctx, target.cluster, execAttributes(target.namespace, target.pod),
		func(ctx context.Context) error {
			if opened != nil {
				opened()
			}
			return t.streamExec(ctx, s, target)
		})
	return err
}

func (t *Terminal) streamExec(ctx context.Context, s *stream, target execTarget) error {
	client, err := t.clusters.Client(target.cluster)
	if err != nil {
		return err
	}

	// Its own cancellation: the callers pass r.Context(), which the WebSocket
	// hijack never cancels (see below).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	req := client.Clients.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(target.namespace).
		Name(target.pod).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: target.container,
			Command:   target.command,
			Stdin:     true,
			Stdout:    true,
			// Stderr must stay false while TTY is true: a pseudo-terminal has one stream,
			// and the API server rejects a request for both with a 400 at upgrade time.
			Stderr: false,
			TTY:    true,
		}, scheme.ParameterCodec)

	// The streaming config, not the shared one: cluster.Manager's 30-second
	// timeout bounds body reads, and an exec session is a body open for as long as
	// the operator types. internal/logs avoids the same trap.
	open := t.openExec
	if open == nil {
		open = newExecutor
	}
	executor, err := open(cluster.StreamingConfig(client.Config), req.URL())
	if err != nil {
		return err
	}

	// remotecommand wants an io.Reader for stdin and the socket gives messages, so
	// the read loop writes into a pipe the executor drains.
	stdin, stdinWriter := io.Pipe()
	sizes := newSizeQueue()

	var streamErr error
	done := make(chan struct{})
	// Through safego: a panic in client-go's stream becomes the session's error,
	// and done is closed after it is set.
	safego.Go("exec: the stream to the pod", func() error {
		return executor.StreamWithContext(ctx, remotecommand.StreamOptions{
			Stdin:             stdin,
			Stdout:            s,
			Tty:               true,
			TerminalSizeQueue: sizes,
		})
	}, func(err error) {
		streamErr = err
		close(done)
	})

	// The browser leaving ends the session, whatever pump is doing. pump writes
	// each keystroke into stdinWriter, and a pipe write returns only when the other
	// end reads. client-go reads it only while its write to the remote stdin is not
	// blocked, so a paste into a command that does not read stdin parks pump in
	// that Write, where it never sees the browser go; when the command then ends,
	// client-go stops reading. Left parked, the session slot is never given back.
	//
	// Closing the read half with an error makes a parked Write return, and
	// canceling ends a remote command that is not reading. Fired whenever the
	// stream closes: pump's read failing, a ping or write that could not be
	// delivered, or the goroutine below once the command is over.
	go func() {
		<-s.closed()
		_ = stdin.CloseWithError(errBrowserGone)
		cancel()
	}()

	// The end of the remote command must close the socket, the only thing that
	// unblocks pump's read loop; otherwise an exited pod leaves the loop parked
	// until the browser goes.
	//
	// The failure is written here, before the close: the caller reports what this
	// function returns only after pump returns, which is after this goroutine has
	// closed the socket, so its message would go nowhere. Reading streamErr is safe:
	// close(done) happens after the write to it.
	//
	// The end is said here too, for the same reason. A command that ended with a
	// status is not a failed session: client-go reports a CodeExitError, which
	// becomes an `exit` carrying the code.
	go func() {
		<-done
		var status utilexec.CodeExitError
		switch {
		case streamErr == nil:
			s.ended(nil)
		case notInImage(streamErr):
			// The shell program is not in the image. It surfaces here, after the
			// door opened, since the runtime only says so once the stream has been
			// asked for: its own branchable code, because the remedy is another
			// container rather than another try. Ahead of the exit status, which a
			// runtime may also report (126, 127) for the same failure: the sentence
			// is the more precise of the two.
			s.refuse(stateUnreadable, codeNoShell, noShellMessage(target, streamErr))
		case errors.As(streamErr, &status):
			code := status.ExitStatus()
			s.ended(&code)
		case !errors.Is(streamErr, context.Canceled):
			s.fail("exec ended: " + textutil.FirstLine(streamErr.Error()))
		}
		s.close()
	}()

	s.pump(stdinWriter, sizes.push)

	// The browser went away or the command ended. Closing the write half sends EOF
	// to the remote stdin; the size queue must be closed too, or remotecommand's
	// goroutine draining it never returns.
	_ = stdinWriter.Close()
	sizes.close()

	// The stream must be able to end the command too: EOF only stops something
	// reading stdin, and a silent, busy command (`sleep 3600`, `helm upgrade
	// --wait`) would park `<-done` forever. r.Context() is not canceled when the
	// browser goes (the connection was hijacked, see stream.go), so nothing else
	// unblocks it, and the session slot, its kubeconfig and, for a node shell, a
	// privileged pod on the node would leak until the pod's own deadline.
	//
	// Canceling makes StreamWithContext return, done close and the callers'
	// cleanup run, so the stream's deadlines bound the command as well.
	cancel()
	<-done

	// A reported exit status is an ordinary end, already said above: returned as
	// an error it would be recorded as a failed act and refused a second time.
	var status utilexec.CodeExitError
	if streamErr == nil || errors.Is(streamErr, context.Canceled) || errors.As(streamErr, &status) {
		return nil
	}
	return streamErr
}

// errBrowserGone is what a write parked in the exec's stdin pipe returns once
// the stream has ended. Never shown: pump returns on it without a word.
var errBrowserGone = errors.New("the browser side of this session has closed")

// newExecutor prefers the WebSocket transport and falls back to SPDY, as
// kubectl does: SPDY is being retired from the API server, and WebSocket
// streaming needs an API server that speaks it. Only the fallback works across
// the cluster versions one kubeconfig holds.
func newExecutor(config *rest.Config, target *url.URL) (remotecommand.Executor, error) {
	if config == nil {
		return nil, errors.New("no rest config for this cluster")
	}
	// The verbs differ: a WebSocket upgrade is a GET, a SPDY one a POST. POST to
	// the WebSocket executor gets a 405 that looks like the cluster refusing exec.
	websocketExec, err := remotecommand.NewWebSocketExecutor(config, "GET", target.String())
	if err != nil {
		return nil, fmt.Errorf("preparing the exec stream: %w", err)
	}
	spdyExec, err := remotecommand.NewSPDYExecutor(config, "POST", target)
	if err != nil {
		return nil, fmt.Errorf("preparing the exec stream: %w", err)
	}
	return remotecommand.NewFallbackExecutor(websocketExec, spdyExec, upgradeRefused)
}

// upgradeRefused is whether a WebSocket upgrade failed in the way that falls
// back to SPDY, for the exec and the tunnel alike. client-go returns
// k8s.io/streaming's UpgradeFailureError, while k8s.io/apimachinery has an
// unrelated type of the same name; both are asked, so either falls back.
func upgradeRefused(err error) bool {
	return streamhttp.IsUpgradeFailure(err) || httpstream.IsUpgradeFailure(err)
}

// pathOrQuery reads a parameter from the route when the route names it, and
// from the query otherwise, as clusterParam does.
func pathOrQuery(r *http.Request, key string) string {
	if v := r.PathValue(key); v != "" {
		return v
	}
	return r.URL.Query().Get(key)
}

// --- terminal size queue -----------------------------------------------------

type sizeQueue struct {
	ch   chan remotecommand.TerminalSize
	done chan struct{}
	once sync.Once
}

func newSizeQueue() *sizeQueue {
	return &sizeQueue{ch: make(chan remotecommand.TerminalSize, 1), done: make(chan struct{})}
}

// Next blocks until a resize arrives, and returns nil once the session is over:
// nil tells remotecommand to stop watching; anything else, or never returning,
// leaks its goroutine.
func (q *sizeQueue) Next() *remotecommand.TerminalSize {
	select {
	case size := <-q.ch:
		return &size
	case <-q.done:
		return nil
	}
}

// push records a new size without ever blocking. Dragging a window emits dozens
// of resizes and only the last matters, so a pending one is replaced. Blocking
// would stall the read loop, which is the keyboard.
func (q *sizeQueue) push(cols, rows uint16) {
	select {
	case <-q.done:
		return
	default:
	}
	select {
	case <-q.ch:
	default:
	}
	select {
	case q.ch <- remotecommand.TerminalSize{Width: cols, Height: rows}:
	default:
	}
}

func (q *sizeQueue) close() { q.once.Do(func() { close(q.done) }) }

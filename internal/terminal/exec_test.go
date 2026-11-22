package terminal

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/identity"
)

func TestValidNameAcceptsKubernetesNamesAndRefusesTheRest(t *testing.T) {
	valid := []string{
		"kube-system",
		"prometheus-server-7d9f8b4c5-x2vqp",
		"a",
		"my.pod.0",
	}
	for _, name := range valid {
		if !validName(name) {
			t.Errorf("%q was refused but is a Kubernetes name", name)
		}
	}

	// Not about injection — client-go escapes path segments — but about where
	// the error appears: refused here it is a line in the terminal, refused by
	// the API server it is an upgrade failure that the UI shows as a terminal
	// which connected and then died.
	invalid := []string{
		"",
		"../etc/passwd",
		"UPPER",
		"has space",
		"-leading-dash",
		"trailing-dash-",
		"pod/../other",
	}
	for _, name := range invalid {
		if validName(name) {
			t.Errorf("%q was accepted", name)
		}
	}
}

// /bin/sh and not /bin/bash: a distroless or Alpine image has no bash, and a
// terminal that opens and immediately dies with "executable file not found" is
// a worse first impression than a plainer prompt.
func TestDefaultExecCommandIsTheShellEveryImageHas(t *testing.T) {
	if len(defaultExecCommand) != 1 || defaultExecCommand[0] != "/bin/sh" {
		t.Errorf("default exec command is %v", defaultExecCommand)
	}
}

// --- the terminal size queue --------------------------------------------------

// Dragging a window emits dozens of resizes. If push blocked, the read loop
// would stall — and the read loop is the keyboard, so the terminal would stop
// accepting input because it was being resized.
func TestSizeQueuePushNeverBlocks(t *testing.T) {
	q := newSizeQueue()
	defer q.close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 10000 {
			q.push(uint16(80+i%40), 24)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("push blocked; a resize storm would wedge the keyboard")
	}
}

// Only the last size is true. A queue that delivered them in order would resize
// the remote terminal dozens of times and finish on a stale one if any were
// dropped from the wrong end.
func TestSizeQueueKeepsTheLatestSize(t *testing.T) {
	q := newSizeQueue()
	defer q.close()

	q.push(80, 24)
	q.push(100, 30)
	q.push(120, 40)

	got := q.Next()
	if got == nil {
		t.Fatal("Next returned nil while the queue was open")
	}
	if got.Width != 120 || got.Height != 40 {
		t.Errorf("Next returned %dx%d, expected 120x40", got.Width, got.Height)
	}
}

// A resize frame that arrives before the exec has asked for a size is the size
// the exec starts with.
//
// `remotecommand` asks the queue once the streams to the API server are up,
// which is a round trip after the browser's socket opened; the browser sends
// its size the instant its socket opens. So the ordinary case is the frame
// **ahead** of the first `Next()`, and this pins the wire
// half of it: a frame read by `pump` before anyone has asked is held, and the
// first `Next()` answers it rather than blocking for a second resize that only
// a drag of the dock's grip would send. Without it `stty size` in a pod
// answers `0 0` and `tput cols` `80` while xterm draws 177 columns.
func TestAResizeFrameAheadOfTheFirstReadIsTheSizeTheExecStartsWith(t *testing.T) {
	var input safeBuffer
	sizes := newSizeQueue()
	defer sizes.close()

	pumping := make(chan struct{})
	client, wait := dialFramedStream(t, func(s *stream) {
		close(pumping)
		s.pump(&input, sizes.push)
	})
	<-pumping

	// The browser's first frame on open: its size, before any keystroke.
	if err := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"resize","cols":177,"rows":17}`)); err != nil {
		t.Fatalf("writing the resize: %v", err)
	}
	// Nobody has asked yet. Wait until the frame has been read and pushed, which
	// is observable only through the queue, so poll it without consuming.
	deadline := time.Now().Add(5 * time.Second)
	for len(sizes.ch) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the resize frame never reached the size queue")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Now the exec starts and asks once, the way remotecommand does.
	got := sizes.Next()
	if got == nil {
		t.Fatal("Next returned nil while the queue was open")
	}
	if got.Width != 177 || got.Height != 17 {
		t.Errorf("the exec would start at %dx%d, expected the browser's 177x17", got.Width, got.Height)
	}

	_ = client.Close()
	wait()
	if got := input.String(); got != "" {
		t.Errorf("a resize frame reached stdin as %q", got)
	}
}

// nil is the documented way to tell remotecommand to stop watching. Anything
// else — including never returning — leaks its goroutine for the life of the
// process, once per pod anyone ever opened a shell in.
func TestSizeQueueNextReturnsNilOnceClosed(t *testing.T) {
	q := newSizeQueue()

	next := make(chan *remotecommand.TerminalSize, 1)
	go func() { next <- q.Next() }()

	q.close()

	select {
	case got := <-next:
		if got != nil {
			t.Errorf("Next returned %v after close, expected nil", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Next never returned after close")
	}
}

func TestSizeQueuePushAfterCloseIsHarmless(t *testing.T) {
	q := newSizeQueue()
	q.close()
	q.close() // idempotent, because both the read loop and the caller close it
	q.push(80, 24)

	if got := q.Next(); got != nil {
		t.Errorf("Next returned %v after close", got)
	}
}

// An exec that cannot reach its cluster has to say so *in the pane*.
//
// serveExec reports whatever runExec returns — but it only reaches that line
// once `pump` has returned, and `pump` returns because runExec's own goroutine
// closed the socket the moment the remote stream failed. The report would
// therefore be written into a connection that had already gone: every exec
// against a cluster that stopped answering would show a pane that opened, went
// black and said nothing at all, which reads as a pod with no shell in it
// rather than as a cluster that is down.
//
// The address in this kubeconfig answers nothing, on purpose. That is exactly
// the case being tested.
func TestAnExecThatCannotReachItsClusterSaysSoInThePane(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")
	kubeconfig := execKubeconfig(t, "unreachable")

	term, err := New(managerFor(t, kubeconfig), Options{Kubeconfig: cluster.Kubeconfig{kubeconfig}, Access: accesstest.Allow()})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(term.ExecHandler())
	t.Cleanup(srv.Close)

	got := dialAndRead(t, "ws"+strings.TrimPrefix(srv.URL, "http")+
		"/kt-api/exec?cluster=unreachable&namespace=default&pod=api")

	if strings.TrimSpace(got) == "" {
		t.Fatal("the pane was told nothing. An exec that fails silently is indistinguishable " +
			"from a pod with no /bin/sh in it, and the operator goes looking in the wrong place.")
	}
	if !strings.Contains(got, "exec ended") {
		t.Errorf("the failure does not say what happened: %q", got)
	}
	// One line, because a terminal only returns to column 0 on a carriage
	// return: a multi-line client-go error comes out as a staircase.
	if strings.Count(got, "\n") > 3 {
		t.Errorf("the failure was written as several lines and will draw as a staircase: %q", got)
	}
}

// TestAnExecLineNamesWhoOpenedIt: a shell inside a pod is the most dangerous
// thing the server profile hands out, and the API server's audit sees the
// ServiceAccount for it. The guard puts the person on the request; the exec's
// log lines must carry them.
func TestAnExecLineNamesWhoOpenedIt(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")
	kubeconfig := execKubeconfig(t, "unreachable")
	term, err := New(managerFor(t, kubeconfig), Options{Kubeconfig: cluster.Kubeconfig{kubeconfig}, Access: accesstest.Allow()})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var lines []string
	term.logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	// What the guard does, and nothing more.
	guarded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := identity.Who{Name: "alice@example.com", Method: identity.SSO}
		term.ExecHandler().ServeHTTP(w, r.WithContext(identity.With(r.Context(), who)))
	})
	srv := httptest.NewServer(guarded)
	t.Cleanup(srv.Close)
	_ = dialAndRead(t, "ws"+strings.TrimPrefix(srv.URL, "http")+
		"/kt-api/exec?cluster=unreachable&namespace=default&pod=api")

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got := append([]string(nil), lines...)
		mu.Unlock()
		var opened, closed bool
		for _, l := range got {
			if strings.HasPrefix(l, "exec opened") {
				opened = strings.HasSuffix(l, `who="alice@example.com" via=sso`)
			}
			if strings.HasPrefix(l, "exec closed") {
				closed = strings.HasSuffix(l, `who="alice@example.com" via=sso`)
			}
		}
		if opened && closed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the exec's lines do not both name who opened it: %q", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

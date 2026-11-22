package terminal

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// --- the browser leaving ends the session --------------------------------------

// parkingExecutor stands in for client-go's exec stream, in the one way that
// matters here: its stdin copy reads one chunk and then stops reading, as
// client-go's `copyStdin` does while its write to a remote command that does not
// read stdin is blocked. The next keystroke then parks pump in the pipe's Write.
// It returns when its context is canceled - a remote command that never ends by
// itself, the worst case.
type parkingExecutor struct {
	firstRead chan struct{}
}

func (e parkingExecutor) Stream(remotecommand.StreamOptions) error {
	return errors.New("not used")
}

func (e parkingExecutor) StreamWithContext(ctx context.Context, opts remotecommand.StreamOptions) error {
	buf := make([]byte, 64)
	if _, err := opts.Stdin.Read(buf); err == nil {
		close(e.firstRead)
	}
	<-ctx.Done()
	return ctx.Err()
}

// execTerminal is a Terminal whose pod exec runs over executor, with a short
// keep-alive so a test can watch a browser leave, behind a server that records
// when the handler returns.
func execTerminal(t *testing.T, executor remotecommand.Executor) (*Terminal, string, <-chan struct{}) {
	t.Helper()
	t.Setenv("KT_IN_CONTAINER", "0")
	kubeconfig := execKubeconfig(t, "somewhere")
	term, err := New(managerFor(t, kubeconfig), Options{Kubeconfig: cluster.Kubeconfig{kubeconfig}, Access: accesstest.Allow()})
	if err != nil {
		t.Fatal(err)
	}
	term.openExec = func(*rest.Config, *url.URL) (remotecommand.Executor, error) { return executor, nil }
	term.pingEvery = 20 * time.Millisecond

	returned := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(returned)
		term.ExecHandler().ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return term, "ws" + strings.TrimPrefix(srv.URL, "http") +
		"/kt-api/exec?cluster=somewhere&namespace=default&pod=api", returned
}

// settleGoroutines waits for the goroutine count to come back to baseline.
func settleGoroutines(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := runtime.NumGoroutine()
		if n <= baseline {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("%d goroutines are still running against a baseline of %d:\n%s", n, baseline, buf)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestAnExecParkedOnItsStdinEndsWhenTheBrowserLeaves: two pastes into a pod
// whose command does not read stdin: the first is taken, the second parks pump
// in the pipe's Write, where it stops reading the socket. The browser then
// closes, and the handler must still return, or it keeps one of the sixteen
// session slots every person shares under the server profile.
func TestAnExecParkedOnItsStdinEndsWhenTheBrowserLeaves(t *testing.T) {
	executor := parkingExecutor{firstRead: make(chan struct{})}
	term, target, returned := execTerminal(t, executor)
	baseline := runtime.NumGoroutine()

	conn, _, err := websocket.DefaultDialer.Dial(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(clientMessage{Type: "in", Data: "first paste"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-executor.firstRead:
	case <-time.After(5 * time.Second):
		t.Fatal("the exec never read its first input")
	}
	// Taken by nobody: pump is now parked writing it into the pipe.
	if err := conn.WriteJSON(clientMessage{Type: "in", Data: "second paste"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if term.sessions.live() != 1 {
		t.Fatalf("the exec does not hold its slot while open: %d", term.sessions.live())
	}
	// What the macOS window's ⌘Q question counts: the same session, seen from
	// the registry quitting would close.
	if term.Open() != 1 {
		t.Fatalf("Open() = %d while one exec is open, want 1", term.Open())
	}

	_ = conn.Close()

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the browser left and the exec handler never returned: pump is parked in a write " +
			"to a stdin nobody drains, and the session slot is held for the life of the process")
	}
	waitFor(t, func() bool { return term.sessions.live() == 0 }, "the session slot was never given back")
	waitFor(t, func() bool { return term.Open() == 0 }, "Open() still counts an exec that has ended")
	settleGoroutines(t, baseline)
}

// fullPTY is a pseudo-terminal whose input buffer is full: a write blocks until
// the terminal is closed, as one does while a foreground program that does not
// read stdin runs. Its output is silent until then.
type fullPTY struct {
	closed    chan struct{}
	once      sync.Once
	writing   chan struct{}
	wroteOnce sync.Once
}

func newFullPTY() *fullPTY {
	return &fullPTY{closed: make(chan struct{}), writing: make(chan struct{})}
}

func (p *fullPTY) Read([]byte) (int, error) {
	<-p.closed
	return 0, io.EOF
}

func (p *fullPTY) Write([]byte) (int, error) {
	p.wroteOnce.Do(func() { close(p.writing) })
	<-p.closed
	return 0, os.ErrClosed
}

func (p *fullPTY) Close() error {
	p.once.Do(func() { close(p.closed) })
	return nil
}

func (p *fullPTY) Resize(uint16, uint16) error { return nil }
func (p *fullPTY) Kill() error                 { return nil }
func (p *fullPTY) Wait() error                 { return nil }

// TestAShellParkedOnAFullTerminalEndsWhenTheBrowserLeaves is the PTY half,
// for the shell and the assistant, which share relay. pump is parked writing
// into a PTY whose input is full; the browser goes. The session - the PTY, the
// process behind it and the directory holding a kubeconfig - must end with it.
func TestAShellParkedOnAFullTerminalEndsWhenTheBrowserLeaves(t *testing.T) {
	workdir := filepath.Join(t.TempDir(), "term-x")
	if err := os.Mkdir(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	pty := newFullPTY()
	sess := &session{pty: pty, workdir: workdir, exited: make(chan struct{})}

	returned := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: sameOrigin}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(returned)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		s := newTimedStream(conn, wireRaw, pongWait, 20*time.Millisecond, writeWait)
		defer s.close()
		defer sess.close()
		relay(s, sess)
	}))
	t.Cleanup(srv.Close)
	baseline := runtime.NumGoroutine()

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.WriteJSON(clientMessage{Type: "in", Data: "a paste the terminal cannot take"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pty.writing:
	case <-time.After(5 * time.Second):
		t.Fatal("pump never wrote into the terminal")
	}

	_ = client.Close()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the browser left and the shell's handler never returned: pump is parked in a " +
			"write to a full terminal, and the shell, its slot and its kubeconfig stay")
	}

	select {
	case <-pty.closed:
	default:
		t.Error("the terminal was not closed when the browser left")
	}
	if _, err := os.Stat(workdir); !os.IsNotExist(err) {
		t.Errorf("the session directory, which holds a kubeconfig, survived the session: %v", err)
	}
	settleGoroutines(t, baseline)
}

// --- one frame cannot take the process's memory --------------------------------

// writeOversizedHeader writes the header of a masked text frame announcing size
// bytes, and none of the payload. A server that buffers the frame waits for
// bytes that never come; one that enforces a read limit answers from the header.
func writeOversizedHeader(t *testing.T, conn *websocket.Conn, size uint64) {
	t.Helper()
	header := []byte{0x81, 0x80 | 127}
	header = binary.BigEndian.AppendUint64(header, size)
	header = append(header, 1, 2, 3, 4) // the mask key a client frame carries
	if _, err := conn.UnderlyingConn().Write(header); err != nil {
		t.Fatal(err)
	}
}

// TestAFramePastTheLimitClosesTheSocketWithNothingBuffered: a frame of
// 2 MiB is refused with 1009 from its header alone, pump returns without writing
// a byte into the session, and the exec gives its slot back.
func TestAFramePastTheLimitClosesTheSocketWithNothingBuffered(t *testing.T) {
	executor := parkingExecutor{firstRead: make(chan struct{})}
	term, target, returned := execTerminal(t, executor)

	conn, _, err := websocket.DefaultDialer.Dial(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	writeOversizedHeader(t, conn, 2<<20)

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			continue
		}
		var closeErr *websocket.CloseError
		if !errors.As(err, &closeErr) || closeErr.Code != websocket.CloseMessageTooBig {
			t.Fatalf("a 2 MiB frame was not refused with 1009 (message too big): %v", err)
		}
		break
	}
	select {
	case <-executor.firstRead:
		t.Error("bytes of the oversized frame reached the session")
	default:
	}
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the exec handler did not return after the oversized frame")
	}
	if n := term.sessions.live(); n != 0 {
		t.Errorf("the session slot was not given back: %d live", n)
	}
}

// TestAFrameUnderTheLimitIsDelivered pins the other side of the limit: a large
// paste below it reaches the session whole.
func TestAFrameUnderTheLimitIsDelivered(t *testing.T) {
	var got safeBuffer
	client, served := dialStream(t, func(s *stream) { s.pump(&got, nil) })
	paste := strings.Repeat("x", 512<<10)
	if err := client.WriteJSON(clientMessage{Type: "in", Data: paste}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(got.String()) == len(paste) }, "a 512 KiB paste did not arrive whole")
	_ = client.Close()
	served()
}

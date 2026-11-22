package terminal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// --- an ended session says so before it closes ----------------------------------
//
// A shell's normal exit must not read, in the dock, as "The connection to the
// session went away." If relay closed the socket the moment the shell exited,
// an `exit` frame written afterwards would go into a closed connection; the
// browser would see a bare close (1006) and draw the sentence it keeps for a
// socket the network took - which a front end retries. Every test here reads the frames up
// to the close and asserts on the **last one before it**, because that is the
// order the pane depends on: `exit` first, then the close, which then finds the
// sentence already set.

// framesUntilClose reads every frame of the framed wire until the socket closes,
// and fails the test if it does not close in time.
func framesUntilClose(t *testing.T, client *websocket.Conn) []serverFrame {
	t.Helper()
	_ = client.SetReadDeadline(time.Now().Add(15 * time.Second))
	var frames []serverFrame
	for {
		kind, data, err := client.ReadMessage()
		if err != nil {
			var netErr interface{ Timeout() bool }
			if errors.As(err, &netErr) && netErr.Timeout() {
				t.Fatalf("the socket was still open after the session ended; frames so far: %+v", frames)
			}
			return frames
		}
		if kind != websocket.TextMessage {
			t.Fatalf("frame sent as type %d on the framed wire", kind)
		}
		var frame serverFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("a frame is not JSON: %v (%s)", err, data)
		}
		frames = append(frames, frame)
	}
}

// lastFrame is the frame the socket closed behind, or a failure naming what did
// arrive.
func lastFrame(t *testing.T, frames []serverFrame) serverFrame {
	t.Helper()
	if len(frames) == 0 {
		t.Fatal("the socket closed with no frame at all: the pane reads that as the connection going away")
	}
	return frames[len(frames)-1]
}

// TestAShellThatExitsSaysSoBeforeItsSocketCloses is the handler-level case: a real
// shell behind FramedShellHandler, `exit` typed into it, and an `exit` frame as
// the last thing before the close.
func TestAShellThatExitsSaysSoBeforeItsSocketCloses(t *testing.T) {
	if !PTYSupported() {
		t.Skip("no pseudo-terminal on this host")
	}
	t.Setenv("KT_IN_CONTAINER", "0")
	kubeconfig := execKubeconfig(t, "somewhere")
	opts := Options{Kubeconfig: cluster.Kubeconfig{kubeconfig}, Access: accesstest.Allow(),
		WorkRoot: filepath.Join(t.TempDir(), "work")}
	// /bin/sh rather than whatever the host's default is, so an rc file cannot
	// hold the shell open or print something that looks like a frame. Windows
	// keeps its default: PowerShell answers `exit` the same way.
	if runtime.GOOS != "windows" {
		opts.Shell = []string{"/bin/sh"}
	}
	term, err := New(managerFor(t, kubeconfig), opts)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(term.FramedShellHandler())
	t.Cleanup(srv.Close)

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/?cluster=somewhere", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.WriteJSON(clientMessage{Type: "data", Data: base64.StdEncoding.EncodeToString([]byte("exit\r"))}); err != nil {
		t.Fatal(err)
	}

	frames := framesUntilClose(t, client)
	if last := lastFrame(t, frames); last.Type != "exit" {
		t.Fatalf("the shell exited and the last frame before the close was %q, not exit: %+v", last.Type, frames)
	}
	// The PTY seam still knows no status, so none is claimed.
	if code := lastFrame(t, frames).Code; code != nil {
		t.Errorf("the shell's exit frame claims code %d; the pseudo-terminal seam cannot know one", *code)
	}
}

// endingPTY prints one line and then ends, as a session whose process exited
// does: the read after the last output fails.
type endingPTY struct {
	once   sync.Once
	closed chan struct{}
	said   bool
}

func (p *endingPTY) Read(b []byte) (int, error) {
	if !p.said {
		p.said = true
		return copy(b, "goodbye\r\n"), nil
	}
	return 0, io.EOF
}

func (p *endingPTY) Write(b []byte) (int, error) { return len(b), nil }
func (p *endingPTY) Close() error {
	p.once.Do(func() { close(p.closed) })
	return nil
}
func (p *endingPTY) Resize(uint16, uint16) error { return nil }
func (p *endingPTY) Kill() error                 { return nil }
func (p *endingPTY) Wait() error                 { return nil }

// TestARelayedSessionThatEndsSaysSo is the assistant's half. serveClaude writes
// no `exit` of its own and ends through the same relay as the shell, so the
// frame has to come from relay itself for both of them to have it. Nothing past
// relay is in this test, on purpose: that is the whole of what the two channels
// share.
func TestARelayedSessionThatEndsSaysSo(t *testing.T) {
	pty := &endingPTY{closed: make(chan struct{})}
	sess := &session{pty: pty, workdir: t.TempDir(), exited: make(chan struct{})}

	client, wait := dialFramedStream(t, func(s *stream) {
		defer sess.close()
		relay(s, sess)
	})
	frames := framesUntilClose(t, client)
	wait()

	if len(frames) < 2 || frames[0].Type != "data" {
		t.Fatalf("the output did not arrive ahead of the end: %+v", frames)
	}
	if last := lastFrame(t, frames); last.Type != "exit" {
		t.Fatalf("the session ended and the last frame before the close was %q: %+v", last.Type, frames)
	}
}

// exitingExecutor is a remote command that prints one line and ends with err,
// which is nil for a command that exited 0 and a CodeExitError for one that did
// not - exactly what client-go's remotecommand returns for each.
type exitingExecutor struct{ err error }

func (e exitingExecutor) Stream(remotecommand.StreamOptions) error { return errors.New("not used") }

func (e exitingExecutor) StreamWithContext(_ context.Context, opts remotecommand.StreamOptions) error {
	_, _ = opts.Stdout.Write([]byte("last words\r\n"))
	return e.err
}

// framedExec is a Terminal whose pod exec runs over executor, served on the
// framed wire.
func framedExec(t *testing.T, executor remotecommand.Executor) string {
	t.Helper()
	t.Setenv("KT_IN_CONTAINER", "0")
	kubeconfig := execKubeconfig(t, "somewhere")
	term, err := New(managerFor(t, kubeconfig), Options{Kubeconfig: cluster.Kubeconfig{kubeconfig}, Access: accesstest.Allow()})
	if err != nil {
		t.Fatal(err)
	}
	term.openExec = func(*rest.Config, *url.URL) (remotecommand.Executor, error) { return executor, nil }
	srv := httptest.NewServer(term.FramedExecHandler())
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/?cluster=somewhere&namespace=default&pod=api"
}

func dialFramed(t *testing.T, target string) *websocket.Conn {
	t.Helper()
	client, _, err := websocket.DefaultDialer.Dial(target, http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// TestAPodExecThatEndsSaysSo: the same drop on the exec path. streamExec's own
// goroutine closes the socket the moment the remote command returns, so
// serveExec's `exit` would go into a closed connection there too - for a pod exec
// and for a node shell, which share runExec.
func TestAPodExecThatEndsSaysSo(t *testing.T) {
	client := dialFramed(t, framedExec(t, exitingExecutor{}))

	frames := framesUntilClose(t, client)
	last := lastFrame(t, frames)
	if last.Type != "exit" {
		t.Fatalf("the command ended and the last frame before the close was %q: %+v", last.Type, frames)
	}
	if last.Code != nil {
		t.Errorf("a command that returned no error was given code %d; nothing reported one", *last.Code)
	}
}

// TestANonZeroExitIsAnExitFrameCarryingItsCode: a command that ended with a
// status is a command that ended, not a connection that failed, so not a red
// `exec ended: command terminated with exit code 3` and a bare close. The API
// server *did* report the status, so the frame carries it -
// the one case where `code` is known rather than invented.
func TestANonZeroExitIsAnExitFrameCarryingItsCode(t *testing.T) {
	client := dialFramed(t, framedExec(t, exitingExecutor{err: utilexec.CodeExitError{
		Err: errors.New("command terminated with non-zero exit code: error executing command [sh], exit code 3"), Code: 3,
	}}))

	frames := framesUntilClose(t, client)
	last := lastFrame(t, frames)
	if last.Type != "exit" {
		t.Fatalf("a non-zero exit ended with %q rather than exit: %+v", last.Type, frames)
	}
	if last.Code == nil || *last.Code != 3 {
		t.Fatalf("the exit frame does not carry the status the API server reported: %+v", last)
	}
	for _, f := range frames {
		if f.Type == "state" {
			t.Errorf("a command's own exit status was reported as a failure of the session: %+v", f)
		}
		if f.Type == "data" {
			raw, _ := base64.StdEncoding.DecodeString(f.Data)
			if strings.Contains(string(raw), "exec ended") {
				t.Errorf("a command's own exit status was written into the pane as a failure: %q", raw)
			}
		}
	}
}

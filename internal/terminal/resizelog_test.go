package terminal

import (
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// resizeFailingPTY is a terminal whose resize always fails, as one does once
// the shell behind it is gone.
type resizeFailingPTY struct{ endingPTY }

func (*resizeFailingPTY) Resize(uint16, uint16) error { return errors.New("resize refused") }

// TestAFailedResizeIsLoggedOnce: the pane keeps its old size and the operator
// has nothing to say why, so the failure is said with its size and its error;
// and said once, so a pane being dragged cannot flood the log.
func TestAFailedResizeIsLoggedOnce(t *testing.T) {
	log := &recordingLog{}
	sess := &session{pty: &resizeFailingPTY{}, workdir: t.TempDir(), logf: log.logf}

	for i := 0; i < 50; i++ {
		sess.resize(120, 40)
	}

	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.lines) != 1 {
		t.Fatalf("want one log line for 50 failed resizes, got %d: %q", len(log.lines), log.lines)
	}
	for _, want := range []string{"120x40", "resize refused"} {
		if !strings.Contains(log.lines[0], want) {
			t.Fatalf("log line %q lacks %q", log.lines[0], want)
		}
	}
}

// TestASuccessfulResizeSaysNothing keeps the log line from becoming noise.
func TestASuccessfulResizeSaysNothing(t *testing.T) {
	log := &recordingLog{}
	sess := &session{pty: &endingPTY{}, workdir: t.TempDir(), logf: log.logf}
	sess.resize(80, 24)
	if len(log.lines) != 0 {
		t.Fatalf("unexpected log: %q", log.lines)
	}
}

// heldFailingPTY is a terminal that stays open until closed and whose resize
// always fails, so the relay is still reading when the browser's frame arrives.
type heldFailingPTY struct {
	once   sync.Once
	closed chan struct{}
}

func (p *heldFailingPTY) Read([]byte) (int, error)    { <-p.closed; return 0, io.EOF }
func (p *heldFailingPTY) Write(b []byte) (int, error) { return len(b), nil }
func (p *heldFailingPTY) Close() error                { p.once.Do(func() { close(p.closed) }); return nil }
func (*heldFailingPTY) Resize(uint16, uint16) error   { return errors.New("resize refused") }
func (*heldFailingPTY) Kill() error                   { return nil }
func (*heldFailingPTY) Wait() error                   { return nil }

// TestAResizeFrameThatFailsIsLoggedThroughTheRelay drives the real path: a
// resize frame on the websocket, through relay and pump, to the terminal. The
// tests above call session.resize directly, so on their own they would stay
// green if relay dropped the error.
func TestAResizeFrameThatFailsIsLoggedThroughTheRelay(t *testing.T) {
	log := &recordingLog{}
	sess := &session{pty: &heldFailingPTY{closed: make(chan struct{})}, workdir: t.TempDir(), exited: make(chan struct{}), logf: log.logf}

	client, wait := dialStream(t, func(s *stream) {
		defer sess.close()
		relay(s, sess)
	})
	if err := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"resize","cols":120,"rows":40}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !log.has("120x40", "resize refused") {
		if time.Now().After(deadline) {
			t.Fatal("a failed resize frame left no log line")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = client.Close()
	wait()
}

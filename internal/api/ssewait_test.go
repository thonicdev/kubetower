package api

import (
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
)

// stoppedReader is a browser that stopped reading: every write and flush
// blocks until the deadline set through http.ResponseController, and for ever
// with none.
type stoppedReader struct {
	mu       sync.Mutex
	deadline time.Time
	gone     chan struct{}
}

func (b *stoppedReader) Header() http.Header         { return http.Header{} }
func (b *stoppedReader) WriteHeader(int)             {}
func (b *stoppedReader) Write(p []byte) (int, error) { return 0, b.block() }
func (b *stoppedReader) FlushError() error           { return b.block() }
func (b *stoppedReader) SetWriteDeadline(d time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deadline = d
	return nil
}

func (b *stoppedReader) block() error {
	b.mu.Lock()
	deadline := b.deadline
	b.mu.Unlock()
	if deadline.IsZero() {
		<-b.gone
		return os.ErrClosed
	}
	select {
	case <-time.After(time.Until(deadline)):
		return os.ErrDeadlineExceeded
	case <-b.gone:
		return os.ErrClosed
	}
}

// TestAnEventToABrowserThatStoppedReadingFailsWithinTheBound: on the
// framed log stream, emit must not block for ever on a browser that no longer
// reads, or the follow behind it never sees an error to stop on. It fails at the
// bound, and the reader stops on emit's error.
func TestAnEventToABrowserThatStoppedReadingFailsWithinTheBound(t *testing.T) {
	browser := &stoppedReader{gone: make(chan struct{})}
	t.Cleanup(func() { close(browser.gone) })
	emit := sseEmitter(browser, 100*time.Millisecond)

	failed := make(chan error, 1)
	go func() { failed <- emit(LogEvent{Type: "line", Text: "listening on :8080"}) }()
	select {
	case err := <-failed:
		if err == nil {
			t.Fatal("an event to a browser that stopped reading was reported as written")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an event to a browser that stopped reading was still being written after 5s, against a bound of 100ms")
	}
}

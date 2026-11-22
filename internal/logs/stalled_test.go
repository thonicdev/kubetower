package logs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

// stalledBrowser is a response whose far end stopped reading: it takes the
// first few writes, as a socket's buffers do, and then blocks every write and
// flush until the deadline set through http.ResponseController passes - for
// ever, with none set, as a real connection does.
type stalledBrowser struct {
	header http.Header
	free   int

	mu       sync.Mutex
	deadline time.Time
	unblock  chan struct{}
}

func newStalledBrowser(free int) *stalledBrowser {
	return &stalledBrowser{header: http.Header{}, free: free, unblock: make(chan struct{})}
}

func (b *stalledBrowser) Header() http.Header { return b.header }
func (b *stalledBrowser) WriteHeader(int)     {}

func (b *stalledBrowser) Write(p []byte) (int, error) {
	if err := b.stall(); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (b *stalledBrowser) Flush() { _ = b.stall() }

func (b *stalledBrowser) FlushError() error { return b.stall() }

func (b *stalledBrowser) SetWriteDeadline(d time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deadline = d
	return nil
}

func (b *stalledBrowser) stall() error {
	b.mu.Lock()
	if b.free > 0 {
		b.free--
		b.mu.Unlock()
		return nil
	}
	deadline := b.deadline
	b.mu.Unlock()
	if deadline.IsZero() {
		<-b.unblock
		return os.ErrClosed
	}
	select {
	case <-time.After(time.Until(deadline)):
		return os.ErrDeadlineExceeded
	case <-b.unblock:
		return os.ErrClosed
	}
}

// TestABrowserThatStopsReadingGivesItsFollowBack: a follow's thirty-minute
// ceiling is a context, looked at between lines; a write to a browser that
// stopped reading blocks inside a line, so without a bound the follow would
// hold its slot past the ceiling, for as long as the socket stayed open. Each
// write is bounded: the handler returns and the slot is given back.
func TestABrowserThatStopsReadingGivesItsFollowBack(t *testing.T) {
	server := fakeAPI(t, time.Millisecond, 0) // talks until it is stopped
	s := New(newFakeClients(t, server.URL, 0), accesstest.Allow(), nil, nil)
	s.writeWait = 100 * time.Millisecond

	browser := newStalledBrowser(4)
	t.Cleanup(func() { close(browser.unblock) })
	r := httptest.NewRequest(http.MethodGet,
		"/kt-api/logs?cluster=docker-desktop&namespace=n&pod=p&container=app", nil).WithContext(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Handler().ServeHTTP(browser, r)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("the handler was still writing to a browser that stopped reading after 5s, against a write bound of 100ms; %d follow(s) open", s.live.open())
	}
	if n := s.live.open(); n != 0 {
		t.Errorf("%d follow(s) still open after the handler returned, want 0", n)
	}
}

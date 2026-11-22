package logs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/identity"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(by time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(by)
	c.mu.Unlock()
}

// asAlice is ctx carrying alice, signed in over single sign-on, whose sign-in
// stands while stands says so.
func asAlice(ctx context.Context, stands *atomic.Bool) context.Context {
	ctx = identity.With(ctx, identity.Who{Name: "alice", Method: identity.SSO, Session: "s1"})
	return identity.WithStanding(ctx, func(context.Context) (bool, error) { return stands.Load(), nil })
}

// followFor runs the SSE handler on a follow of a pod that never stops talking,
// as alice, and answers once it has returned with what the browser received.
func followFor(t *testing.T, s *Streamer, stands *atomic.Bool, meanwhile func()) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet,
		"/kt-api/logs?cluster=docker-desktop&namespace=n&pod=p&container=app", nil)
	ctx, abandon := context.WithCancel(asAlice(r.Context(), stands))
	defer abandon()
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Handler().ServeHTTP(rec, r.WithContext(ctx))
	}()
	time.Sleep(50 * time.Millisecond) // several intervals of a follow that may go on
	select {
	case <-done:
		t.Fatalf("the follow ended before anything changed:\n%s", tailOf(rec.Body.String()))
	default:
	}
	meanwhile()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		abandon()
		<-done
		t.Fatal("the follow was still open 5s after it should have been ended")
	}
	return rec.Body.String()
}

// TestAFollowEndsWhenItsSignInEnds: a follow must not run to its thirty-minute
// ceiling after the person was signed out on another replica. It ends within an
// interval, saying why, and its slot is given back.
func TestAFollowEndsWhenItsSignInEnds(t *testing.T) {
	server := fakeAPI(t, 5*time.Millisecond, 0)
	s := New(newFakeClients(t, server.URL, 0), accesstest.Allow(), nil, nil)
	s.recheckEvery = 10 * time.Millisecond
	stands := standing(true)

	body := followFor(t, s, stands, func() { stands.Store(false) })
	if !strings.Contains(body, "event: error\ndata: your sign-in to the console has ended") {
		t.Errorf("the follow ended without saying the sign-in had ended:\n%s", tailOf(body))
	}
	if n := s.live.open(); n != 0 {
		t.Errorf("%d follow(s) still open, want 0", n)
	}
}

// TestAFollowEndsWhenTheReviewNowSaysNo: the RoleBinding goes while the pane
// follows. Once the gate's held answer expires, the next interval is told no,
// and the follow ends saying so.
func TestAFollowEndsWhenTheReviewNowSaysNo(t *testing.T) {
	server := fakeAPI(t, 5*time.Millisecond, 0)
	clock := &testClock{now: time.Unix(1_800_000_000, 0)}
	var denied atomic.Bool
	reviews := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return !denied.Load(), nil }}
	s := New(newFakeClients(t, server.URL, 0), access.New(access.Options{Reviews: reviews.For, Now: clock.Now}), nil, nil)
	s.recheckEvery = 10 * time.Millisecond

	body := followFor(t, s, standing(true), func() {
		denied.Store(true)
		clock.advance(access.TTL)
	})
	if !strings.Contains(body, "event: error\ndata: this log stream was closed") ||
		!strings.Contains(body, "pods/log") {
		t.Errorf("the follow ended without the review's refusal:\n%s", tailOf(body))
	}
	if n := s.live.open(); n != 0 {
		t.Errorf("%d follow(s) still open, want 0", n)
	}
}

// The framed reader ends the same way, on an event of its own kind, which
// internal/web frames as a refusal rather than a failure of the cluster.
func TestTheFramedFollowEndsOnARefusedEvent(t *testing.T) {
	server := fakeAPI(t, 5*time.Millisecond, 0)
	s := New(newFakeClients(t, server.URL, 0), accesstest.Allow(), nil, nil)
	s.recheckEvery = 10 * time.Millisecond
	stands := standing(true)
	go func() {
		time.Sleep(50 * time.Millisecond)
		stands.Store(false)
	}()

	// Bounded, so that without the watch the test fails rather than hangs.
	ctx, cancel := context.WithTimeout(asAlice(context.Background(), stands), 5*time.Second)
	defer cancel()
	var last Event
	err := s.Follow(ctx,
		Request{Cluster: "docker-desktop", Namespace: "n", Pod: "p", Container: "app", Follow: true},
		func(e Event) error { last = e; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if last.Kind != EventRefused || !strings.Contains(last.Text, "sign-in") {
		t.Errorf("the framed follow ended on %+v, want a refused event naming the sign-in", last)
	}
}

func standing(ok bool) *atomic.Bool {
	var b atomic.Bool
	b.Store(ok)
	return &b
}

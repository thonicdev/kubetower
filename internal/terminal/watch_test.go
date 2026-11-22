package terminal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/identity"
)

// testClock is a clock a test moves by hand.
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

// watchedExec is a framed pod exec behind a Terminal whose watcher asks every
// 10 ms on clock, opened by alice over single sign-on, whose sign-in stands
// while stands says so. The pod's command runs until the session ends.
func watchedExec(t *testing.T, gate *access.Gate, clock *testClock, idle time.Duration, stands *atomic.Bool) (*Terminal, *websocket.Conn) {
	t.Helper()
	t.Setenv("KT_IN_CONTAINER", "0")
	kubeconfig := execKubeconfig(t, "somewhere")
	term, err := New(managerFor(t, kubeconfig), Options{
		Kubeconfig: cluster.Kubeconfig{kubeconfig},
		Access:     gate,
		IdleExec:   idle,
	})
	if err != nil {
		t.Fatal(err)
	}
	executor := parkingExecutor{firstRead: make(chan struct{})}
	term.openExec = func(*rest.Config, *url.URL) (remotecommand.Executor, error) { return executor, nil }
	term.recheckEvery = 10 * time.Millisecond
	term.now = clock.Now

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := identity.With(r.Context(), identity.Who{Name: "alice", Method: identity.SSO, Session: "s1"})
		ctx = identity.WithStanding(ctx, func(context.Context) (bool, error) { return stands.Load(), nil })
		term.FramedExecHandler().ServeHTTP(w, r.WithContext(ctx))
	}))
	t.Cleanup(srv.Close)
	client := dialFramed(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/?cluster=somewhere&namespace=default&pod=api")
	waitFor(t, func() bool { return term.sessions.live() == 1 }, "the exec did not open")
	// Open means past the review inside the door and streaming: a keystroke
	// has reached the pod. Before that, a test that moves the gate's clock is
	// moving it under the opening, not under an open shell.
	if err := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"data","data":"aWQK"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-executor.firstRead:
	case <-time.After(5 * time.Second):
		t.Fatal("the exec never reached the pod")
	}
	return term, client
}

func standing(ok bool) *atomic.Bool {
	var b atomic.Bool
	b.Store(ok)
	return &b
}

// TestAnOpenExecEndsWhenItsSignInEnds: a session signed out on another
// replica, past its cap - the front door's answer is no - and the open exec
// receives the refusal frame within one interval, and gives its slot back,
// rather than running until its tab is closed.
func TestAnOpenExecEndsWhenItsSignInEnds(t *testing.T) {
	stands := standing(true)
	clock := &testClock{now: time.Unix(1_800_000_000, 0)}
	term, client := watchedExec(t, accesstest.Allow(), clock, 0, stands)

	time.Sleep(50 * time.Millisecond) // several intervals of a sign-in that stands
	if term.sessions.live() != 1 {
		t.Fatal("the exec ended while its sign-in still stood")
	}
	stands.Store(false)

	last := lastFrame(t, framesUntilClose(t, client))
	if last.Type != "state" || last.State != stateForbidden || last.Reason == nil ||
		last.Reason.Code != codeVerbDenied || !strings.Contains(last.Reason.Message, "sign-in") {
		t.Errorf("the exec ended without the refusal frame naming the sign-in: %+v", last)
	}
	waitFor(t, func() bool { return term.sessions.live() == 0 }, "the exec's slot was not given back")
}

// TestAnOpenExecEndsWhenTheReviewNowSaysNo is the other half: the
// RoleBinding is removed while the shell is open. Once the gate's held answer
// has expired - access.TTL on its clock - the next interval asks again, is
// told no, and the shell ends in the refusal frame.
func TestAnOpenExecEndsWhenTheReviewNowSaysNo(t *testing.T) {
	clock := &testClock{now: time.Unix(1_800_000_000, 0)}
	var denied atomic.Bool
	reviews := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return !denied.Load(), nil }}
	gate := access.New(access.Options{Reviews: reviews.For, Now: clock.Now})
	term, client := watchedExec(t, gate, clock, 0, standing(true))

	denied.Store(true) // the RoleBinding goes
	time.Sleep(50 * time.Millisecond)
	if term.sessions.live() != 1 {
		t.Fatal("the exec ended before the gate's held answer expired: the revocation delay is access.TTL, not less")
	}
	clock.advance(access.TTL)

	last := lastFrame(t, framesUntilClose(t, client))
	if last.Type != "state" || last.State != stateForbidden || last.Reason == nil ||
		last.Reason.Code != codeVerbDenied || !strings.Contains(last.Reason.Message, "pods/exec") {
		t.Errorf("the exec ended without the refusal frame naming the review: %+v", last)
	}
	waitFor(t, func() bool { return term.sessions.live() == 0 }, "the exec's slot was not given back")
}

// A review that could not be made is not a no: an API server that blips must
// not end every open shell.
func TestAnOpenExecSurvivesAReviewThatCouldNotBeMade(t *testing.T) {
	clock := &testClock{now: time.Unix(1_800_000_000, 0)}
	var broken atomic.Bool
	reviews := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) {
		if broken.Load() {
			return false, context.DeadlineExceeded
		}
		return true, nil
	}}
	gate := access.New(access.Options{Reviews: reviews.For, Now: clock.Now})
	term, client := watchedExec(t, gate, clock, 0, standing(true))

	broken.Store(true)
	clock.advance(access.TTL)
	time.Sleep(80 * time.Millisecond)
	if term.sessions.live() != 1 {
		t.Fatal("an exec ended because a review could not be made")
	}
	_ = client.Close()
	waitFor(t, func() bool { return term.sessions.live() == 0 }, "the exec's slot was not given back")
}

// TestAnIdleExecEndsAtTheBound, on a test clock: pongs alone would keep an
// idle exec alive for ever. Under the server profile an exec that
// receives nothing from the person for IdleExec ends in a framed state reason
// and gives its slot back; a keystroke starts the wait again.
func TestAnIdleExecEndsAtTheBound(t *testing.T) {
	clock := &testClock{now: time.Unix(1_800_000_000, 0)}
	term, client := watchedExec(t, accesstest.Allow(), clock, IdleExecBound, standing(true))

	clock.advance(IdleExecBound - time.Minute)
	if err := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"data","data":"bHMK"}`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // the watcher hears it
	clock.advance(IdleExecBound - time.Minute)
	time.Sleep(50 * time.Millisecond)
	if term.sessions.live() != 1 {
		t.Fatal("an exec that received a keystroke inside the bound was ended as idle")
	}

	clock.advance(2 * time.Minute)
	last := lastFrame(t, framesUntilClose(t, client))
	if last.Type != "state" || last.State != stateUnreadable || last.Reason == nil ||
		last.Reason.Code != codeNotSupportedHere || !strings.Contains(last.Reason.Message, IdleExecBound.String()) {
		t.Errorf("the idle exec ended without a state frame naming the bound: %+v", last)
	}
	waitFor(t, func() bool { return term.sessions.live() == 0 }, "the idle exec's slot was not given back")
}

// On the desktop there is no bound: Options leaves IdleExec at zero.
func TestWithoutABoundAnIdleExecStaysOpen(t *testing.T) {
	clock := &testClock{now: time.Unix(1_800_000_000, 0)}
	term, client := watchedExec(t, accesstest.Allow(), clock, 0, standing(true))
	clock.advance(24 * time.Hour)
	time.Sleep(50 * time.Millisecond)
	if term.sessions.live() != 1 {
		t.Fatal("an idle exec ended with no bound set")
	}
	_ = client.Close()
	waitFor(t, func() bool { return term.sessions.live() == 0 }, "the exec's slot was not given back")
}

package auth

// The client's retry. A flood that evicts a
// launch's challenge between its hello and its prove - after the instance has
// proved itself - is "busy", and the launch runs the exchange again.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// evictingInstance is the real instance's route, with a flood of hellos sent
// to it just before each of the first evictions proves arrives: what something
// local hammering the route at that moment does.
func evictingInstance(t *testing.T, evictions int32) (*httptest.Server, *Auth, *atomic.Int32) {
	t.Helper()
	a := testAuth(t)
	a.OfferLaunches()
	var proves atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get(LauncherHeader), ".prove.") && proves.Add(1) <= evictions {
			for i := 0; i < maxChallenges; i++ {
				req := httptest.NewRequest(http.MethodPost, HandoffPath, nil)
				req.RemoteAddr = loopback
				req.Header.Set(LauncherHeader, hello(clientNonce))
				a.Handoff(httptest.NewRecorder(), req)
			}
		}
		a.Handoff(w, r)
	}))
	a.AcceptHandoffs(testLauncher, srv.Listener.Addr().String())
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, a, &proves
}

func TestALaunchWhoseChallengeIsEvictedTriesAgain(t *testing.T) {
	// Twice, so the third attempt - the design's - is the one that gets through.
	srv, a, proves := evictingInstance(t, 2)
	code, err := RequestHandoff(context.Background(), testLauncher, addrOf(srv), true, 5*time.Second)
	if err != nil {
		t.Fatalf("a launch whose challenge was evicted twice did not get through: %v", err)
	}
	if n := proves.Load(); n != 3 {
		t.Errorf("the launch sent %d proves; want 3", n)
	}
	if sessionCookieOf(a, arrive(t, a, code, loopback)) == nil {
		t.Error("the code it got opened no session")
	}
}

func TestALaunchEvictedEveryTimeIsToldBusyNotImpostor(t *testing.T) {
	srv, _, proves := evictingInstance(t, 1<<20)
	_, err := RequestHandoff(context.Background(), testLauncher, addrOf(srv), true, 5*time.Second)
	if !errors.Is(err, ErrInstanceBusy) || errors.Is(err, ErrNotTheInstance) {
		t.Errorf("a launch evicted every time was told %v; want ErrInstanceBusy, not ErrNotTheInstance", err)
	}
	if n := proves.Load(); n != handoffAttempts {
		t.Errorf("the launch sent %d proves; want %d before giving up", n, handoffAttempts)
	}
}

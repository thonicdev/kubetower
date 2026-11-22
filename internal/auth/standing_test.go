package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/identity"
)

// standingOf runs a request carrying cookie through Standing and hands back
// what it put on the request.
func standingOf(t *testing.T, a *Auth, cookie *http.Cookie) identity.Standing {
	t.Helper()
	var got identity.Standing
	req := httptest.NewRequest(http.MethodGet, consoleOrigin+"/api/ws/exec", nil)
	req.AddCookie(cookie)
	a.Standing(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = identity.StandingOf(r.Context())
	})).ServeHTTP(httptest.NewRecorder(), req)
	if got == nil {
		t.Fatal("a request carrying a session was given no way to ask whether it stands")
	}
	return got
}

// TestASignedOutSessionNoLongerStands is the first half of the in-stream
// re-check of a sign-in, at the front door: what a stream asks while it is open answers yes
// while the session is live and no once it has been signed out - wherever the
// sign-out happened, since it is the store's record that is asked. Bob's
// session is untouched.
func TestASignedOutSessionNoLongerStands(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, nil)
	ready(t, a)
	alice := signInAs(t, a, f, "code-alice", func(c map[string]any) { c["email"] = "alice@example.com" })
	bob := signInAs(t, a, f, "code-bob", func(c map[string]any) { c["email"] = "bob@example.com" })
	ctx := context.Background()

	alicesStream, bobsStream := standingOf(t, a, alice), standingOf(t, a, bob)
	if ok, err := alicesStream(ctx); !ok || err != nil {
		t.Fatalf("alice's live session does not stand: %v, %v", ok, err)
	}

	req := httptest.NewRequest(http.MethodPost, consoleOrigin+"/api/auth/logout", nil)
	req.AddCookie(alice)
	a.LogoutJSON(httptest.NewRecorder(), req)

	if ok, err := alicesStream(ctx); ok || err != nil {
		t.Errorf("alice's session still stands after she signed out: %v, %v", ok, err)
	}
	if ok, err := bobsStream(ctx); !ok || err != nil {
		t.Errorf("bob's session stopped standing when alice signed out: %v, %v", ok, err)
	}
}

// TestASessionInUseStandsPastTheIdleDeadlineOfTheCookieItOpenedWith: the
// cookie a stream was opened with is not refreshed - the browser's other
// requests refresh a newer one - so its idle deadline says nothing about the
// session. Past it, the session stands; past its cap, it does not.
func TestASessionInUseStandsPastTheIdleDeadlineOfTheCookieItOpenedWith(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, nil)
	ready(t, a)
	alice := signInAs(t, a, f, "code-alice", nil)
	c, ok := a.parseToken(alice.Value)
	if !ok {
		t.Fatal("the session cookie does not parse")
	}
	ctx := context.Background()

	if ok, err := a.stands(ctx, alice.Value, c.expires.Add(time.Minute)); !ok || err != nil {
		t.Errorf("a session in use stopped standing at its first cookie's idle deadline: %v, %v", ok, err)
	}
	if ok, _ := a.stands(ctx, alice.Value, c.issued.Add(a.ssoMax)); ok {
		t.Error("a session past its cap still stands")
	}
}

// A request with no session is given nothing to ask: nil, which a stream reads
// as nothing to re-check, never as a no.
func TestARequestWithNoSessionCarriesNoStanding(t *testing.T) {
	a := testAuth(t)
	var got identity.Standing
	a.Standing(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = identity.StandingOf(r.Context())
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if got != nil {
		t.Error("a request with no session cookie was given a standing to ask")
	}
}

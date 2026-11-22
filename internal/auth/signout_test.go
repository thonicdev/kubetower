package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thonicdev/kubetower/internal/identity"
)

// TestSigningOutNamesThePersonToTheHook is the sign-out half, at the front
// door, of closing what a session left open: once the store has recorded a single sign-on sign-out, the hook is told
// who it was, so internal/launch can close what they had open. Bob is not named.
func TestSigningOutNamesThePersonToTheHook(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, nil)
	ready(t, a)
	var told []identity.Who
	a.OnSignOut(func(who identity.Who) { told = append(told, who) })

	alice := signInAs(t, a, f, "code-alice", func(c map[string]any) { c["email"] = "alice@example.com" })
	_ = signInAs(t, a, f, "code-bob", func(c map[string]any) { c["email"] = "bob@example.com" })

	req := httptest.NewRequest(http.MethodPost, consoleOrigin+"/api/auth/logout", nil)
	req.AddCookie(alice)
	a.LogoutJSON(httptest.NewRecorder(), req)

	if len(told) != 1 || told[0].Name != "alice@example.com" || told[0].Method != identity.SSO {
		t.Fatalf("the hook was told %+v, want alice once", told)
	}
	// And which of alice's sign-ins it was: the tag her requests carried.
	c, ok := a.parseToken(alice.Value)
	if !ok || told[0].Session == "" || told[0].Session != sessionTag(c.sid) {
		t.Errorf("the hook was told session %q, want the signed-out session's own tag", told[0].Session)
	}
}

// A password session names the shared account - everybody who holds the
// password - so its sign-out tells the hook nothing: closing "admin"'s shells
// would close every other holder's.
func TestASharedPasswordSignOutClosesNobodysSessions(t *testing.T) {
	a := testAuth(t)
	called := false
	a.OnSignOut(func(identity.Who) { called = true })

	now := a.now()
	token, _ := a.mint(now, now, "", byPassword)
	rec := httptest.NewRecorder()
	a.Logout(rec, logoutForm(&http.Cookie{Name: a.cfg.CookieName, Value: token}))
	if rec.Code != http.StatusFound {
		t.Fatalf("the sign-out answered %d, want 302: the test measures nothing", rec.Code)
	}

	if called {
		t.Error("a shared-password sign-out told the hook a person, and would close every holder's sessions")
	}
}

// A hook that panics leaves the sign-out standing.
func TestAPanickingSignOutHookDoesNotUndoTheSignOut(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, nil)
	ready(t, a)
	a.OnSignOut(func(identity.Who) { panic("boom") })
	alice := signInAs(t, a, f, "code-alice", nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, consoleOrigin+"/api/auth/logout", nil)
	req.AddCookie(alice)
	a.LogoutJSON(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("the sign-out answered %d after its hook panicked, want 204", rec.Code)
	}
	if _, ok := currentWith(a, alice); ok {
		t.Error("alice is still signed in after a sign-out whose hook panicked")
	}
}

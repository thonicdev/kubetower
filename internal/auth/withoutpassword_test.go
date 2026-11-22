package auth

// On the loopback desktop the password is withdrawn and the shared account
// stays.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// passwordless is testAuth with its password withdrawn, signing with the same
// key - the same credentials file, after the update.
func passwordless(t *testing.T) *Auth {
	t.Helper()
	a, err := New(Config{User: "operator", SessionKey: []byte("a-thirty-two-byte-key-for-tests!"),
		SessionIdle: time.Hour, SessionMax: 12 * time.Hour, CookieName: "kubetower_session",
		WithoutPassword: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.delay = 0
	a.OfferLaunches()
	return a
}

// The upgrade as internal/launch makes it: the credentials file keeps the hash
// an earlier version drew (localCredentialsFor, "an upgraded install on the
// loopback desktop"), and the front door is handed it beside WithoutPassword.
// The shared account's sessions are signed with a key bound to that
// hash, so it is what carries them through the update.
func TestAPasswordCookieFromBeforeTheUpdateStillWorks(t *testing.T) {
	before := testAuth(t)
	cookie := signIn(t, before)

	after, err := New(Config{User: "operator", PasswordHash: before.cfg.PasswordHash,
		SessionKey:  []byte("a-thirty-two-byte-key-for-tests!"),
		SessionIdle: time.Hour, SessionMax: 12 * time.Hour, CookieName: "kubetower_session",
		WithoutPassword: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, reached := whoBehind(t, after, cookie); !reached {
		t.Error("a password session issued before the update was refused after it; the operator's " +
			"session is meant to last until its cap")
	}
	// And the other side of that binding: had the update drawn a new password instead,
	// the same cookie would have ended.
	if _, reached := whoBehind(t, passwordless(t), cookie); reached {
		t.Error("a password session survived into a front door whose password hash is not the one it was opened under")
	}
}

func TestNoPasswordIsCheckedWhereItWasWithdrawn(t *testing.T) {
	a := passwordless(t)
	if a.OffersPassword() || a.Methods().Password {
		t.Fatal("a front door without a password offers one")
	}
	rec := httptest.NewRecorder()
	a.Login(rec, postForm(map[string][]string{"user": {"operator"}, "password": {testPassword}}))
	if rec.Code != http.StatusNotFound {
		t.Errorf("a password login answered %d where the password is withdrawn; want 404", rec.Code)
	}

	code, err := a.IssueLaunchCode(true)
	if err != nil {
		t.Fatalf("no launch code where the password is withdrawn: %v", err)
	}
	if sessionCookieOf(a, arrive(t, a, code, loopback)) == nil {
		t.Error("a launch code opened no session where the password is withdrawn")
	}
}

func TestTheSignInPageSaysHowToOpenTheConsole(t *testing.T) {
	a := passwordless(t)
	rec := httptest.NewRecorder()
	a.LoginPage(rec, httptest.NewRequest(http.MethodGet, LoginPath, nil))
	body := rec.Body.String()
	if !strings.Contains(body, "Open KubeTower again, or run") || !strings.Contains(body, "kubetower open") {
		t.Errorf("the sign-in page does not say how to open the console:\n%s", body)
	}
	if strings.Contains(body, `type="password"`) {
		t.Error("the sign-in page draws a password field where none is offered")
	}
	if strings.Contains(body, "Your session has expired") {
		t.Error("the sign-in page asks to sign in where there is nothing to sign in with")
	}
}

func TestNoPasswordToWithdrawBesideAnIdentityProvider(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	if a := ssoAuth(t, f, nil); a == nil {
		t.Fatal("ssoAuth")
	}
	_, err := New(Config{SessionKey: []byte("a-thirty-two-byte-key-for-tests!"), WithoutPassword: true,
		OIDC: &OIDCConfig{Issuer: f.issuer, ClientID: testClientID, RedirectURL: consoleOrigin + CallbackPath}})
	if err == nil {
		t.Error("WithoutPassword was accepted beside an identity provider, where KeepLocalAccount is the only rule")
	}
}

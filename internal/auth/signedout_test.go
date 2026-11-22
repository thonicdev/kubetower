package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// cookieNamed is the cookie of that name a response set, or nil.
func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// signInPage renders the sign-in page for a request carrying these cookies.
func signInPage(a *Auth, target string, cookies ...*http.Cookie) string {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	a.LoginPage(rec, req)
	return rec.Body.String()
}

// After a deliberate log-out, opening a page lands on the sign-in page with a
// destination in hand - the same request a lapsed session makes. The page says
// signed out, not expired.
func TestTheSignInPageSaysSignedOutAfterALogOut(t *testing.T) {
	a := testAuth(t)
	name := a.cfg.CookieName + signedOutSuffix

	for _, logOut := range []struct {
		what string
		do   func(*httptest.ResponseRecorder, *http.Cookie)
	}{
		{"the console's own Log out", func(rec *httptest.ResponseRecorder, c *http.Cookie) {
			req := httptest.NewRequest(http.MethodPost, consoleOrigin+"/api/auth/logout", nil)
			req.AddCookie(c)
			a.LogoutJSON(rec, req)
		}},
		{"the front door's log-out route", func(rec *httptest.ResponseRecorder, c *http.Cookie) {
			a.Logout(rec, logoutForm(c))
		}},
	} {
		rec := httptest.NewRecorder()
		logOut.do(rec, signedIn(t, a))
		marker := cookieNamed(rec, name)
		if marker == nil || marker.Value != "1" || !marker.HttpOnly {
			t.Fatalf("%s set no log-out record (%+v)", logOut.what, marker)
		}

		body := signInPage(a, LoginPath+"?next=%2Fc%2Fprod", marker)
		if !strings.Contains(body, "You are signed out. Sign in to open that page.") {
			t.Errorf("after %s, the sign-in page does not say signed out:\n%s", logOut.what, body)
		}
		if strings.Contains(body, "expired") {
			t.Errorf("after %s, the sign-in page says the session expired", logOut.what)
		}
		if body := signInPage(a, LoginPath, marker); !strings.Contains(body, "You are signed out.") {
			t.Errorf("after %s, the page it lands on does not say signed out", logOut.what)
		}
	}
}

// Without the record, a destination in hand still means a session ran out.
func TestTheSignInPageStillSaysExpiredForALapsedSession(t *testing.T) {
	a := testAuth(t)
	body := signInPage(a, LoginPath+"?next=%2Fc%2Fprod")
	if !strings.Contains(body, "Your session has expired.") {
		t.Errorf("a lapsed session is not told it expired:\n%s", body)
	}
	if strings.Contains(body, "signed out") {
		t.Error("a lapsed session is told it signed out")
	}
}

// Signed in again, the first request deletes the record, so a session that
// later lapses is told so.
func TestSigningInAgainForgetsTheLogOut(t *testing.T) {
	a := testAuth(t)
	name := a.cfg.CookieName + signedOutSuffix

	req := httptest.NewRequest(http.MethodGet, "/c/prod", nil)
	req.AddCookie(signedIn(t, a))
	req.AddCookie(&http.Cookie{Name: name, Value: "1"})
	rec := httptest.NewRecorder()
	a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, req)

	cleared := cookieNamed(rec, name)
	if cleared == nil || cleared.MaxAge >= 0 {
		t.Errorf("a signed-in request kept the log-out record (%+v)", cleared)
	}
}

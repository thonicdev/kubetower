package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// logoutForm is the sign-out a client of the front door's own route sends:
// a POST carrying the mutation marker, from the console's own origin.
func logoutForm(cookie *http.Cookie) *http.Request {
	req := httptest.NewRequest(http.MethodPost, consoleOrigin+LogoutPath, nil)
	req.Header.Set("X-KubeTower-Request", "1")
	req.Header.Set("Origin", consoleOrigin)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return req
}

// signedIn is a live shared-account session and its cookie.
func signedIn(t *testing.T, a *Auth) *http.Cookie {
	t.Helper()
	now := a.now()
	token, c := a.mint(now, now, "", byPassword)
	return a.sessionCookie(token, c, now)
}

// TestFollowingALinkToLogoutEndsNoSession: if `/auth/logout` answered any
// method outside the guard, a link - which SameSite=Lax sends the cookie with -
// would sign a person out. A GET is 405 and clears nothing; so is a POST
// without the marker, or from another site.
func TestFollowingALinkToLogoutEndsNoSession(t *testing.T) {
	a := testAuth(t)
	cookie := signedIn(t, a)

	get := httptest.NewRequest(http.MethodGet, consoleOrigin+LogoutPath, nil)
	get.AddCookie(cookie)
	unmarked := httptest.NewRequest(http.MethodPost, consoleOrigin+LogoutPath, nil)
	unmarked.AddCookie(cookie)
	foreign := logoutForm(cookie)
	foreign.Header.Set("Origin", "https://elsewhere.example")

	for name, req := range map[string]*http.Request{"GET": get, "POST without the marker": unmarked, "POST from another site": foreign} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			a.Logout(rec, req)
			want := http.StatusForbidden
			if req.Method == http.MethodGet {
				want = http.StatusMethodNotAllowed
				if allow := rec.Header().Get("Allow"); allow != http.MethodPost {
					t.Errorf("a GET was answered Allow %q, want POST", allow)
				}
			}
			if rec.Code != want {
				t.Errorf("answered %d, want %d", rec.Code, want)
			}
			for _, c := range rec.Result().Cookies() {
				if c.Name == a.cfg.CookieName {
					t.Errorf("the session cookie was touched (%q, MaxAge %d)", c.Value, c.MaxAge)
				}
			}
		})
	}
}

// And through the whole front door, the way a browser reaches it: the route
// is public (a sign-out needs no session to be refused), so the method is the
// handler's to refuse - and a POST with the guard still signs out.
func TestAGuardedPostToLogoutSignsOut(t *testing.T) {
	a := testAuth(t)
	cookie := signedIn(t, a)
	mux := http.NewServeMux()
	mux.HandleFunc(LogoutPath, a.Logout)
	front := a.Middleware(mux)

	get := httptest.NewRequest(http.MethodGet, consoleOrigin+LogoutPath, nil)
	get.AddCookie(cookie)
	rec := httptest.NewRecorder()
	front.ServeHTTP(rec, get)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET %s through the front door answered %d, want 405", LogoutPath, rec.Code)
	}

	rec = httptest.NewRecorder()
	front.ServeHTTP(rec, logoutForm(cookie))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != LoginPath {
		t.Fatalf("a guarded POST answered %d to %q, want a 302 to the sign-in page", rec.Code, rec.Header().Get("Location"))
	}
	c := sessionCookie(t, rec, a.cfg.CookieName)
	if c.Value != "" || c.MaxAge >= 0 {
		t.Errorf("a guarded POST did not clear the session cookie (value %q, MaxAge %d)", c.Value, c.MaxAge)
	}
}

// TestAPasswordOnlyServerProfileSessionCookieIsSecure: behind the
// server profile's TLS ingress the session cookie is Secure with or without
// single sign-on - its sign-in, its refresh and its deletion - and the
// known-browser cookie beside it too. The desktop, on plain HTTP over
// loopback, keeps a cookie without Secure: one with it would never come back.
func TestAPasswordOnlyServerProfileSessionCookieIsSecure(t *testing.T) {
	for _, secure := range []bool{true, false} {
		profile := "desktop"
		if secure {
			profile = "server profile"
		}
		t.Run(profile, func(t *testing.T) {
			a := testAuth(t)
			a.cfg.SecureCookies = secure // as internal/launch sets it, before anything is served

			rec := postLogin(t, a, "operator", testPassword)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("the sign-in answered %d", rec.Code)
			}
			for _, name := range []string{a.cfg.CookieName, knownCookie} {
				c := sessionCookie(t, rec, name)
				if c.Secure != secure {
					t.Errorf("%s at sign-in: Secure is %t, want %t", name, c.Secure, secure)
				}
				if name == a.cfg.CookieName && (!c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/") {
					t.Errorf("the session cookie lost a flag beside Secure: %+v", c)
				}
			}

			out := httptest.NewRecorder()
			a.Logout(out, logoutForm(sessionCookie(t, rec, a.cfg.CookieName)))
			if c := sessionCookie(t, out, a.cfg.CookieName); c.Secure != secure || c.MaxAge >= 0 {
				t.Errorf("the deletion: Secure %t MaxAge %d, want Secure %t and expired", c.Secure, c.MaxAge, secure)
			}
		})
	}
}

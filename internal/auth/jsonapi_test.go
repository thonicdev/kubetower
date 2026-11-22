package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An expired session under the JSON contract answers 401 and never a page.
//
// # The failure this is about
//
// The guard answers a *navigation* with an HTML login page, which is right and
// stays. A `fetch` renders that as nothing at all: no error thrown, no unusual
// status in a log, a blank screen. The front end routes to its own login screen
// on the **status**, which only works if the status arrives — and this is the
// only thing that establishes it does.
//
// It is asserted three ways because each is a separate way to get it wrong: the
// status, the media type, and the fact that the body is a problem object rather
// than markup. A 401 carrying HTML would satisfy the first and fail a client just
// as completely.
func TestAnExpiredSessionUnderTheAPIPrefixAnswersJSONAndNeverAPage(t *testing.T) {
	guard := testAuth(t)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("%s reached the handler with no session", r.URL.Path)
	})

	for _, address := range []string{
		"/api/clusters",
		"/api/c/prod/r/pods",
		"/api/preferences",
	} {
		recorder := httptest.NewRecorder()
		// No Accept header at all, which is what a bare `fetch` sends and is the
		// case the page-or-JSON rule below this one reads as "background".
		guard.Middleware(next).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, address, nil))

		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s answered %d, expected 401", address, recorder.Code)
		}
		if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
			t.Errorf("%s answered %q, expected application/problem+json", address, got)
		}
		body := recorder.Body.String()
		if strings.Contains(strings.ToLower(body), "<html") || strings.Contains(body, "<form") {
			t.Errorf("%s answered markup, which a fetch renders as nothing at all: %s", address, body)
		}
		var problem struct {
			Status int    `json:"status"`
			Code   string `json:"code"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
			t.Errorf("%s answered a body that is not JSON: %v", address, err)
			continue
		}
		if problem.Status != http.StatusUnauthorized || problem.Code != "session_expired" {
			t.Errorf("%s answered status %d code %q, expected 401 session_expired",
				address, problem.Status, problem.Code)
		}
	}
}

// A navigation outside the contract still gets its page, because that is what a
// person in a browser needs and removing it would trade one silent failure for
// another.
func TestANavigationOutsideTheContractStillGetsItsLoginPage(t *testing.T) {
	guard := testAuth(t)
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/c/prod/pods", nil)
	request.Header.Set("Accept", "text/html")
	guard.Middleware(next).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusFound {
		t.Fatalf("a navigation answered %d, expected a redirect to the login page", recorder.Code)
	}
	if location := recorder.Header().Get("Location"); !strings.HasPrefix(location, LoginPath) {
		t.Errorf("a navigation was sent to %q, expected %q", location, LoginPath)
	}
}

// The contract's four public paths answer before there is a session. A probe
// carries no cookie, and one answering 401 would report the console as dead
// whenever nobody is signed in.
func TestTheContractsPublicPathsAnswerWithNoSession(t *testing.T) {
	guard := testAuth(t)
	reached := map[string]bool{}
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		reached[r.URL.Path] = true
	})

	for _, address := range []string{"/healthz", "/readyz", "/api/auth/login", "/api/auth/logout"} {
		recorder := httptest.NewRecorder()
		guard.Middleware(next).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, address, nil))
		if !reached[address] {
			t.Errorf("%s was refused by the guard; it has to answer before there is a session", address)
		}
	}
	// And the one that must not be: static assets are behind the guard, which is
	// the assumption most likely to be made the other way during a port.
	recorder := httptest.NewRecorder()
	guard.Middleware(next).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	if reached["/static/app.css"] {
		t.Error("a static asset reached the handler with no session")
	}
}

// LoginJSON never returns a credential in the body, and never says which half of
// a bad login was wrong.
func TestLoginJSONSetsTheCookieAndSaysNothingElse(t *testing.T) {
	guard := testAuth(t)

	recorder := httptest.NewRecorder()
	guard.LoginJSON(recorder, httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"user":"operator","password":"correct-horse-battery-staple"}`)))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("a correct login answered %d, expected 204: %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.Len() != 0 {
		t.Errorf("a login answered with a body: %s", recorder.Body.String())
	}
	cookie := recorder.Header().Get("Set-Cookie")
	if cookie == "" {
		t.Fatal("a correct login set no cookie")
	}
	if !strings.Contains(cookie, "HttpOnly") {
		t.Errorf("the session cookie is not HttpOnly: %s", cookie)
	}

	refused := httptest.NewRecorder()
	guard.LoginJSON(refused, httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"user":"operator","password":"wrong"}`)))
	if refused.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong password answered %d, expected 401", refused.Code)
	}
	if got := refused.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("a refused login answered %q, expected application/problem+json", got)
	}
	lower := strings.ToLower(refused.Body.String())
	if strings.Contains(lower, "password") || strings.Contains(lower, "user") {
		t.Errorf("the refusal names which half was wrong, which is an account-existence "+
			"oracle: %s", refused.Body.String())
	}
}

// LogoutJSON answers 204 whether or not a session was live. A logout that fails
// on an already-expired session leaves a front end unable to reach the login
// screen, which is the state it is most likely to be in when it calls this.
func TestLogoutJSONAnswersWithNoSession(t *testing.T) {
	recorder := httptest.NewRecorder()
	testAuth(t).LogoutJSON(recorder, httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("logging out with no session answered %d, expected 204", recorder.Code)
	}
}

package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const testPassword = "correct-horse-battery-staple"

func testAuth(t *testing.T) *Auth {
	t.Helper()

	// MinCost, not the production cost: these tests compare a password often
	// enough that cost 14 would make the package take a minute to test, and what
	// is under test is the decision around bcrypt, never bcrypt itself.
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("preparing the test hash: %v", err)
	}

	a, err := New(Config{
		User:         "operator",
		PasswordHash: string(hash),
		SessionKey:   []byte("a-thirty-two-byte-key-for-tests!"),
		// An hour idle inside a twelve-hour ceiling: short enough that a test
		// can name a boundary, wide enough apart that the two are visibly
		// different bounds. Every test that cares moves the clock rather than
		// these.
		SessionIdle: time.Hour,
		SessionMax:  12 * time.Hour,
		CookieName:  "kubetower_session",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The rejection delay is real behavior but it is wall-clock time, and
	// paying it in six tests is three quarters of a minute of nothing.
	a.delay = 0
	// The desktop's launches, as internal/launch turns them on outside
	// --incluster: the window tests mint codes on this front door.
	a.OfferLaunches()
	return a
}

func (a *Auth) testCookie(t *testing.T) *http.Cookie {
	t.Helper()
	return &http.Cookie{Name: a.cfg.CookieName, Value: a.issueToken()}
}

// issueToken is what a login writes, for the tests that only care that a token
// is ours and current.
//
// It lives in the test file and not beside `mint` on purpose: production has two
// callers and each of them has an issue time to carry — the login's own instant,
// or the one already in the cookie it is refreshing — so a no-argument version in
// auth.go would be a third way to stamp a session that nothing calls.
func (a *Auth) issueToken() string {
	now := a.now()
	token, _ := a.mint(now, now, "", byPassword)
	return token
}

// frozen pins the clock so a test can say what happens after thirty days
// without waiting or shortening a window into wall-clock noise. Written before
// the Auth serves anything, which is what keeps it out of the race detector's
// way — see the comment on Auth.now.
func frozen(a *Auth, at time.Time) *time.Time {
	clock := &at
	a.now = func() time.Time { return *clock }
	return clock
}

// --- Open redirect ----------------------------------------------------------

// The login page sends the browser wherever `next` says. Accepting anything
// that resolves off this host turns the console into a redirector that lends a
// trusted address to somebody else's page — and it is reached by a plain link,
// with no session needed to fire it.
//
// The backslash entries are the hole a check that rejects only "//host" leaves:
// "/\host" passes it, because Go's URL parser reads "\" as an ordinary path
// character while every browser's reads it as "/" for http and https. They are
// pinned here so the check cannot be simplified back into the hole.
func TestSafeNextRejectsExternalTargets(t *testing.T) {
	hostile := map[string]string{
		"absolute URL":                 "https://evil.example",
		"absolute URL with a path":     "http://evil.example/path",
		"protocol-relative":            "//evil.example",
		"protocol-relative with path":  "//evil.example/c/prod",
		"backslash authority":          `/\evil.example`,
		"double backslash authority":   `/\\evil.example`,
		"backslash authority and path": `/\evil.example/c/prod`,
		"leading backslashes":          `\\evil.example`,
		"javascript scheme":            "javascript:alert(1)",
		"scheme with no slashes":       "data:text/html,<script>alert(1)</script>",
		// Browsers strip tab, CR and LF from a URL before parsing it, so each of
		// these arrives at the network as "//evil.example".
		"tab inside the authority": "/\t/evil.example",
		"newline before the slash": "/\n/evil.example",
		"carriage return":          "/\r/evil.example",
		"empty":                    "",
	}
	for name, next := range hostile {
		t.Run(name, func(t *testing.T) {
			if got := safeNext(next); got != "/" {
				t.Errorf("safeNext(%q) = %q — it must fall back to %q", next, got, "/")
			}
		})
	}
}

// The other half of the same guarantee: tightening the check must not start
// rejecting the paths this console is actually made of, or an expired session
// silently forgets where the operator was.
func TestSafeNextKeepsInternalPaths(t *testing.T) {
	for _, next := range []string{
		"/",
		"/c/docker-desktop/",
		"/c/SHOP-EKS-1/pods",
		"/c/SHOP-EKS-1/pods?q=nginx&ns=kube-system",
		// A context name in the shape Headlamp renames EKS contexts to.
		"/c/arn:aws:eks:eu-central-1:123456789012:cluster--example-prod-eks/",
	} {
		if got := safeNext(next); got != next {
			t.Errorf("safeNext(%q) = %q — an internal path must survive unchanged", next, got)
		}
	}
}

// The redirect target reaches the browser through the login form as well as
// through the Location header, so the same hostile value must not come back out
// of the page as a usable link.
func TestLoginPageDoesNotReflectAHostileNext(t *testing.T) {
	a := testAuth(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, LoginPath+"?next="+url.QueryEscape(`/\evil.example`), nil)

	a.LoginPage(rec, req)

	if body := rec.Body.String(); strings.Contains(body, "evil.example") {
		t.Error("the login page echoed a hostile next value back into its form")
	}
}

// --- Session tokens ---------------------------------------------------------

func TestValidTokenAcceptsAFreshToken(t *testing.T) {
	a := testAuth(t)
	if !a.validToken(a.issueToken()) {
		t.Error("a freshly issued token was rejected — nobody can stay signed in")
	}
}

func TestValidTokenRejectsExpiry(t *testing.T) {
	a := testAuth(t)
	a.cfg.SessionIdle = -time.Minute // issued already in the past
	if a.validToken(a.issueToken()) {
		t.Error("an expired token was accepted — the session never actually ends")
	}
}

// Signing the token buys exactly one thing: neither of its stamps can be moved
// by the only party that ever holds it. The second case is the issue time —
// rewriting it is how a holder would push the cap
// out for ever while the deadline slides along behind it.
func TestValidTokenRejectsRewrittenStamps(t *testing.T) {
	a := testAuth(t)
	payload, signature, _ := strings.Cut(a.issueToken(), "|")
	version, stamps, _ := strings.Cut(payload, ":")
	issued, expires, _ := strings.Cut(stamps, ":")
	far := strconv.FormatInt(time.Now().Add(100*time.Hour).Unix(), 10)

	cases := map[string]string{
		"expiry pushed back":        version + ":" + issued + ":" + far,
		"issue time pushed forward": version + ":" + far + ":" + expires,
	}
	for name, forged := range cases {
		t.Run(name, func(t *testing.T) {
			if a.validToken(forged + "|" + signature) {
				t.Error("a token whose stamps were rewritten was accepted — sessions are unbounded")
			}
		})
	}
}

func TestValidTokenRejectsTamperedAndMalformed(t *testing.T) {
	a := testAuth(t)
	payload, signature, _ := strings.Cut(a.issueToken(), "|")

	cases := map[string]string{
		"no separator":         payload + signature,
		"empty signature":      payload + "|",
		"altered signature":    payload + "|" + strings.Repeat("A", len(signature)),
		"empty string":         "",
		"junk":                 "not-a-token",
		"payload not a number": "tomorrow|" + signature,
		"extra separator":      payload + "|" + signature + "|" + signature,
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if a.validToken(token) {
				t.Errorf("an invalid token was accepted (%q)", token)
			}
		})
	}
}

// A token signed with another key must not be honored — that is what makes
// rotating the session secret the one way to end every session at once, and the
// only answer this stateless design has to a stolen cookie.
func TestValidTokenRejectsAnotherKeysSignature(t *testing.T) {
	a := testAuth(t)
	other := testAuth(t)
	other.cfg.SessionKey = []byte("a-completely-different-key-32b!!")

	if a.validToken(other.issueToken()) {
		t.Error("a token signed with another key was accepted — rotating the secret would evict nobody")
	}
}

// --- Configuration ----------------------------------------------------------

// A front door that starts with no password looks exactly like one that starts
// with a good password, right up to the moment somebody walks through it. Every
// one of these must be a refusal to start, not a default.
func TestNewRefusesAnIncompleteConfiguration(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("preparing the test hash: %v", err)
	}
	full := Config{
		User:         "operator",
		PasswordHash: string(hash),
		SessionKey:   []byte("a-thirty-two-byte-key-for-tests!"),
	}

	cases := map[string]func(*Config){
		"no user":                              func(c *Config) { c.User = "" },
		"no password hash":                     func(c *Config) { c.PasswordHash = "" },
		"no session key":                       func(c *Config) { c.SessionKey = nil },
		"session key a human could have typed": func(c *Config) { c.SessionKey = []byte("hunter2") },
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := full
			spoil(&cfg)
			if _, err := New(cfg); err == nil {
				t.Error("New accepted it — the console would come up unguarded")
			}
		})
	}
}

// The two optional fields must have working defaults, or a caller that omits
// them gets a cookie with no name and a session that expires on issue.
func TestNewFillsInTheOptionalFields(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("preparing the test hash: %v", err)
	}
	a, err := New(Config{
		User:         "operator",
		PasswordHash: string(hash),
		SessionKey:   []byte("a-thirty-two-byte-key-for-tests!"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.cfg.CookieName != defaultCookieName {
		t.Errorf("cookie name = %q, expected %q", a.cfg.CookieName, defaultCookieName)
	}
	if a.cfg.SessionIdle != DefaultSessionIdle {
		t.Errorf("idle window = %v, expected %v", a.cfg.SessionIdle, DefaultSessionIdle)
	}
	if a.cfg.SessionMax != DefaultSessionMax {
		t.Errorf("maximum lifetime = %v, expected %v", a.cfg.SessionMax, DefaultSessionMax)
	}
}

// --- Signing in -------------------------------------------------------------

func TestLoginIssuesASessionAndHonoursNext(t *testing.T) {
	a := testAuth(t)
	rec := httptest.NewRecorder()

	a.Login(rec, postForm(url.Values{
		"user":     {"operator"},
		"password": {testPassword},
		"next":     {"/c/SHOP-EKS-1/pods"},
	}))

	// 303 and not 302: a POST answered with 302 is followed as a POST by clients
	// that read the specification strictly, and the login page would be asked to
	// render the pod list.
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, expected %d", rec.Code, http.StatusSeeOther)
	}
	if loc := rec.Header().Get("Location"); loc != "/c/SHOP-EKS-1/pods" {
		t.Errorf("Location = %q — the operator was not returned to where they were", loc)
	}

	c := sessionCookie(t, rec, a.cfg.CookieName)
	if !a.validToken(c.Value) {
		t.Error("the cookie does not carry a token this server will accept")
	}
	// HttpOnly is the whole defense against a script reading the session: this
	// process serves a terminal and renders cluster data, so an XSS anywhere in
	// the console would otherwise be a stolen kubeconfig.
	if !c.HttpOnly {
		t.Error("the session cookie is readable from JavaScript")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Error("the session cookie is not SameSite=Lax — a cross-site POST could carry it")
	}
}

// A successful login is a redirect the operator's browser follows without
// asking, so it is the one place an open redirect is worth the most.
func TestLoginWillNotRedirectOffHost(t *testing.T) {
	a := testAuth(t)
	rec := httptest.NewRecorder()

	a.Login(rec, postForm(url.Values{
		"user":     {"operator"},
		"password": {testPassword},
		"next":     {`/\evil.example`},
	}))

	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q — a successful login redirected off this host", loc)
	}
}

func TestLoginRefusesBadCredentials(t *testing.T) {
	cases := map[string]url.Values{
		"wrong password": {"user": {"operator"}, "password": {"nope"}},
		"unknown user":   {"user": {"intruder"}, "password": {testPassword}},
		"empty form":     {},
		// bcrypt truncates at 72 bytes; a longer string must not be accepted for
		// sharing its first 72 with nothing in particular.
		"very long password": {"user": {"operator"}, "password": {strings.Repeat("x", 200)}},
	}
	for name, form := range cases {
		t.Run(name, func(t *testing.T) {
			a := testAuth(t)
			rec := httptest.NewRecorder()

			a.Login(rec, postForm(form))

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, expected %d", rec.Code, http.StatusUnauthorized)
			}
			if len(rec.Result().Cookies()) != 0 {
				t.Error("a refused login still set a cookie")
			}
		})
	}
}

// The message must be the same whichever half was wrong, or it answers "does
// this account exist" for anyone who cares to ask.
func TestLoginSaysTheSameThingForEitherFailure(t *testing.T) {
	a := testAuth(t)

	badPass := httptest.NewRecorder()
	a.Login(badPass, postForm(url.Values{"user": {"operator"}, "password": {"nope"}}))

	badUser := httptest.NewRecorder()
	a.Login(badUser, postForm(url.Values{"user": {"intruder"}, "password": {testPassword}}))

	if badPass.Body.String() != badUser.Body.String() {
		t.Error("a wrong user and a wrong password produce different pages — account names are enumerable")
	}
}

func TestLogoutClearsTheCookie(t *testing.T) {
	a := testAuth(t)
	rec := httptest.NewRecorder()

	a.Logout(rec, logoutForm(nil))

	c := sessionCookie(t, rec, a.cfg.CookieName)
	if c.Value != "" || c.MaxAge >= 0 {
		t.Errorf("the session cookie was not expired (value %q, MaxAge %d)", c.Value, c.MaxAge)
	}
}

// --- Middleware -------------------------------------------------------------

// A page navigation gets the login form and keeps its destination; a background
// request gets a plain 401. Sending an HTML page to a fetch is how a logged-out
// console renders a login form inside a pod table instead of saying it is logged
// out.
func TestMiddlewareAnswersNavigationsAndFetchesDifferently(t *testing.T) {
	a := testAuth(t)
	guarded := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the request reached the console without a session")
	}))

	t.Run("navigation", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/c/SHOP-EKS-1/pods?q=nginx", nil)
		req.Header.Set("Accept", "text/html,application/xhtml+xml")

		guarded.ServeHTTP(rec, req)

		if rec.Code != http.StatusFound {
			t.Fatalf("status = %d, expected %d", rec.Code, http.StatusFound)
		}
		loc, err := url.Parse(rec.Header().Get("Location"))
		if err != nil {
			t.Fatalf("unparseable Location: %v", err)
		}
		if loc.Path != LoginPath {
			t.Errorf("Location path = %q, expected %q", loc.Path, LoginPath)
		}
		if next := loc.Query().Get("next"); next != "/c/SHOP-EKS-1/pods?q=nginx" {
			t.Errorf("next = %q — the query string was lost, so the filter is lost with it", next)
		}
	})

	t.Run("fetch", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/fragments/pods", nil)
		req.Header.Set("Accept", "application/json")

		guarded.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, expected %d", rec.Code, http.StatusUnauthorized)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %q, expected JSON", ct)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("the 401 body is not JSON: %v (%q)", err, rec.Body.String())
		}
		if body["error"] == "" {
			t.Error("the 401 body says nothing about why")
		}
	})
}

func TestMiddlewareLetsAValidSessionThrough(t *testing.T) {
	a := testAuth(t)
	reached := false
	guarded := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))

	req := httptest.NewRequest(http.MethodGet, "/c/SHOP-EKS-1/pods", nil)
	req.AddCookie(a.testCookie(t))
	guarded.ServeHTTP(httptest.NewRecorder(), req)

	if !reached {
		t.Error("a valid session was turned away at the door")
	}
}

// Each of these has to answer before a session exists. The login page because
// guarding it is a redirect loop; the manifest and the icons because the browser
// fetches them without the cookie and, handed a login page, quietly withdraws
// the install offer; the health path because the container's own probe would
// otherwise restart a process that is working.
func TestMiddlewareLetsThePublicPathsThrough(t *testing.T) {
	a := testAuth(t)
	for _, path := range []string{
		LoginPath,
		LogoutPath,
		HealthPath,
		"/kt-pwa/manifest.webmanifest",
		"/kt-pwa/sw.js",
		"/kt-pwa/icon.svg",
	} {
		t.Run(path, func(t *testing.T) {
			reached := false
			guarded := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
			guarded.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
			if !reached {
				t.Error("this path is guarded, and it must not be")
			}
		})
	}
}

// Everything that is not on the public list is guarded — including anything that
// merely starts with the same characters, which is how a "/kt-pwa" prefix check
// written one character short becomes a way past the front door.
func TestMiddlewareGuardsEverythingElse(t *testing.T) {
	a := testAuth(t)
	for _, path := range []string{
		"/",
		"/c/SHOP-EKS-1/pods",
		"/fragments/pods",
		"/static/anything-at-all",
		"/kt-pwa-not-really/secrets",
		// The guard does not exempt the notices: on the desktop they are
		// mounted outside it (internal/launch, the outer router), GET only.
		"/THIRD_PARTY_NOTICES.txt",
		"/THIRD_PARTY_NOTICES.txt/x",
		"/assets/index-Chmc5KSx.js",
		"/auth/login/../../c/SHOP-EKS-1/pods",
	} {
		t.Run(path, func(t *testing.T) {
			guarded := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("served without a session")
			}))
			rec := httptest.NewRecorder()
			// Raw target, unnormalized: httptest.NewRequest parses it the way the
			// server would.
			guarded.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusFound && rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d — neither a redirect nor a refusal", rec.Code)
			}
		})
	}
}

// --- Request classification -------------------------------------------------

// Only a page navigation is worth a redirect; everything else must get a 401 it
// can read.
func TestIsBackgroundRequest(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"page navigation", map[string]string{"Accept": "text/html,application/xhtml+xml"}, false},
		{"fetch asking for JSON", map[string]string{"Accept": "application/json"}, true},
		{"no preference stated", nil, true},
		{"websocket upgrade", map[string]string{"Connection": "Upgrade", "Accept": "text/html"}, true},
		{"XMLHttpRequest", map[string]string{"X-Requested-With": "XMLHttpRequest", "Accept": "text/html"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			if got := isBackgroundRequest(r); got != tc.want {
				t.Errorf("isBackgroundRequest = %t, expected %t", got, tc.want)
			}
		})
	}
}

// --- Installable application ------------------------------------------------

// The login page carries the same manifest and the same service worker as the
// console behind it. Without them an expired session breaks out of the installed
// window into a browser tab, and a first visit — which never sees anything but
// this page — is offered no install at all.
//
// The two paths are written out rather than derived from a constant on purpose:
// the other place they exist is whatever serves /kt-pwa/, and nothing in the
// build would notice the day the two drift apart.
func TestLoginPageIsInstallable(t *testing.T) {
	rec := httptest.NewRecorder()
	testAuth(t).LoginPage(rec, httptest.NewRequest(http.MethodGet, LoginPath, nil))

	body := rec.Body.String()
	for _, want := range []string{
		`<link rel="manifest" href="/kt-pwa/manifest.webmanifest">`,
		`navigator.serviceWorker.register("/kt-pwa/sw.js"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the login page no longer carries %q", want)
		}
	}
}

// The page must reference nothing it would have to fetch from elsewhere: it is
// served before anything is trusted, and it is the page an offline installed
// application falls back to.
func TestLoginPageIsSelfContained(t *testing.T) {
	rec := httptest.NewRecorder()
	testAuth(t).LoginPage(rec, httptest.NewRequest(http.MethodGet, LoginPath, nil))

	for _, external := range []string{"http://", "https://", "//cdn", "fonts.googleapis"} {
		if strings.Contains(rec.Body.String(), external) {
			t.Errorf("the login page reaches outside for %q", external)
		}
	}
}

// The form must post where the router serves. Nothing else in the build ties
// them together, and the failure mode is a Sign in button that 404s.
func TestLoginPagePostsToTheLoginPath(t *testing.T) {
	rec := httptest.NewRecorder()
	testAuth(t).LoginPage(rec, httptest.NewRequest(http.MethodGet, LoginPath, nil))

	if want := `action="` + LoginPath + `"`; !strings.Contains(rec.Body.String(), want) {
		t.Errorf("the form does not carry %s", want)
	}
}

// A page that reflects session state must never be held by a cache: served back
// after signing in, it is a login form the operator cannot get past.
func TestLoginPageIsNotCached(t *testing.T) {
	rec := httptest.NewRecorder()
	testAuth(t).LoginPage(rec, httptest.NewRequest(http.MethodGet, LoginPath, nil))

	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, expected no-store", cc)
	}
}

// --- helpers ----------------------------------------------------------------

func postForm(form url.Values) *http.Request {
	r := httptest.NewRequest(http.MethodPost, LoginPath, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q cookie was set", name)
	return nil
}

// --- The after-login hook ---------------------------------------------------
//
// Config.OnLogin exists so that internal/launch can delete the one-time file
// holding the password drawn on a first run, in clear, the moment somebody has
// used it. This package deliberately knows nothing about that file, the state
// directory, or what a first run is — it declares the smallest true statement
// instead, which is that something wants to know a login succeeded.
//
// Three properties, and each of them is a way the wiring can fail.

// TestOnlyASuccessfulLoginRunsTheAfterLoginHook.
//
// The refused half is the one worth having. A hook called before the credential
// check, or called on every attempt, deletes the operator's only copy of the
// password on a mistyped character — and the console is then shut with the file
// gone and nothing saying why.
func TestOnlyASuccessfulLoginRunsTheAfterLoginHook(t *testing.T) {
	for _, tc := range []struct {
		name     string
		user     string
		password string
		status   int
		want     int
	}{
		{"a correct login", "operator", testPassword, http.StatusSeeOther, 1},
		{"the wrong password", "operator", "nearly-" + testPassword, http.StatusUnauthorized, 0},
		{"the wrong user", "root", testPassword, http.StatusUnauthorized, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testAuth(t)
			ran := 0
			a.cfg.OnLogin = func() { ran++ }

			w := postLogin(t, a, tc.user, tc.password)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d", w.Code, tc.status)
			}
			if ran != tc.want {
				t.Errorf("the after-login hook ran %d time(s), want %d", ran, tc.want)
			}
		})
	}
}

// TestAPanickingAfterLoginHookDoesNotFailTheLogin.
//
// Login runs in net/http's handler goroutine, where an unrecovered panic aborts
// the response: the browser reports a connection reset for a password that was
// correct, every time, with the cause in another package entirely. The hook's
// contract says it cannot fail a login; this is what makes that true rather than
// asking for it.
func TestAPanickingAfterLoginHookDoesNotFailTheLogin(t *testing.T) {
	a := testAuth(t)
	a.cfg.OnLogin = func() { panic("the state directory went away") }

	w := postLogin(t, a, "operator", testPassword)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want %d: a panicking hook failed a correct login", w.Code, http.StatusSeeOther)
	}
	// And the session it was granted has to be usable, or the login only looked
	// like it succeeded.
	if cookie := sessionCookie(t, w, a.cfg.CookieName); !a.validToken(cookie.Value) {
		t.Error("the session cookie does not validate")
	}
}

// TestNoAfterLoginHookIsTheOrdinaryCase. Nil is what every caller that does not
// need this passes, and it must not be a nil dereference on the one path that
// matters most.
func TestNoAfterLoginHookIsTheOrdinaryCase(t *testing.T) {
	a := testAuth(t)
	if a.cfg.OnLogin != nil {
		t.Fatal("testAuth set a hook, so this asserts nothing")
	}
	if w := postLogin(t, a, "operator", testPassword); w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want %d", w.Code, http.StatusSeeOther)
	}
}

// postLogin drives one login through the real handler. It reuses `postForm`
// above rather than building its own request, so that a change to how a login is
// submitted cannot leave these three tests exercising a shape nothing sends.
func postLogin(t *testing.T, a *Auth, user, password string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	a.Login(w, postForm(url.Values{"user": {user}, "password": {password}}))
	return w
}

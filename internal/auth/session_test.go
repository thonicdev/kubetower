package auth

// The sliding session: a session in use does not end, and one left unused
// does, whatever time has passed since signing in.
//
// Every test here moves a frozen clock rather than the wall clock. A test that
// establishes "thirty days unused ends the session" by sleeping is a test nobody
// runs; one that shortens the window to a second measures the build machine's
// scheduler instead of the rule.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// midday is an arbitrary fixed instant. Not `time.Now()`: two runs of the same
// test on different days must exercise the same arithmetic.
var midday = time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)

// use drives one ordinary console request through the guard and reports whether
// it got through, plus the session cookie the guard wrote if it wrote one.
//
// Through `Middleware` and not through a helper of its own, because the whole
// subject is a header the middleware sets on its way past: a unit test on
// `refreshDue` would pass on the day the middleware stopped calling it.
func use(t *testing.T, a *Auth, cookie *http.Cookie) (reached bool, refreshed *http.Cookie) {
	t.Helper()
	guarded := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	req := httptest.NewRequest(http.MethodGet, "/c/SHOP-EKS-1/pods", nil)
	req.Header.Set("Accept", "text/html")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.Name == a.cfg.CookieName {
			refreshed = c
		}
	}
	return reached, refreshed
}

// signIn posts a correct login and returns the cookie it issued.
func signIn(t *testing.T, a *Auth) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	a.Login(rec, postForm(map[string][]string{"user": {"operator"}, "password": {testPassword}}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login status %d, want %d", rec.Code, http.StatusSeeOther)
	}
	return sessionCookie(t, rec, a.cfg.CookieName)
}

// TestASessionInUseDoesNotExpire states as a property that a session in use
// stays alive.
//
// The idle window here is an hour and the console is used every forty-five
// minutes for a day. An absolute deadline would fail it at minute 61,
// whatever the operator was doing.
func TestASessionInUseDoesNotExpire(t *testing.T) {
	a := testAuth(t)
	a.cfg.SessionMax = 90 * 24 * time.Hour // out of the way; the cap has its own test
	clock := frozen(a, midday)

	cookie := signIn(t, a)
	for elapsed := time.Duration(0); elapsed < 24*time.Hour; elapsed += 45 * time.Minute {
		*clock = clock.Add(45 * time.Minute)
		reached, refreshed := use(t, a, cookie)
		if !reached {
			t.Fatalf("the session ended after %s of continuous use", elapsed+45*time.Minute)
		}
		if refreshed != nil {
			// What a browser does with the Set-Cookie it was handed. Carrying it
			// forward is the whole mechanism; a test that kept the login's
			// original cookie would be testing something no browser does.
			cookie = refreshed
		}
	}
}

// TestASessionLeftAloneEndsAtTheIdleWindow is the other half, and it is the half
// that makes the first one mean something: a session that slides for ever is not
// an idle window, it is no expiry at all.
func TestASessionLeftAloneEndsAtTheIdleWindow(t *testing.T) {
	a := testAuth(t)
	clock := frozen(a, midday)
	cookie := signIn(t, a)

	*clock = clock.Add(59 * time.Minute)
	if reached, _ := use(t, a, cookie); !reached {
		t.Fatal("the session ended a minute inside its idle window")
	}

	// From that request, not from the login: the deadline moved when it was
	// served, which is the point.
	*clock = clock.Add(61 * time.Minute)
	reached, _ := use(t, a, cookie)
	if reached {
		t.Error("an hour and a minute unused, and the session was still accepted")
	}
}

// TestTheSessionCookieIsNotRewrittenOnEveryRequest.
//
// The pod table refreshes every two seconds, so a cookie re-signed on every
// request is 1 800 `Set-Cookie` headers an hour on responses whose body is a
// table fragment. The rate limit is `refreshEvery`, capped at half the idle
// window — half an hour here.
func TestTheSessionCookieIsNotRewrittenOnEveryRequest(t *testing.T) {
	a := testAuth(t)
	clock := frozen(a, midday)
	cookie := signIn(t, a)

	*clock = clock.Add(time.Minute)
	if _, refreshed := use(t, a, cookie); refreshed != nil {
		t.Error("a request a minute after the login rewrote the cookie — this lands on the two-second refresh")
	}

	*clock = clock.Add(31 * time.Minute)
	_, refreshed := use(t, a, cookie)
	if refreshed == nil {
		t.Fatal("the cookie was never refreshed, so the window does not slide at all")
	}
	if !a.validToken(refreshed.Value) {
		t.Error("the refreshed cookie is not one this server will accept")
	}
}

// TestARefreshedCookieKeepsEveryFlag.
//
// A refresh writes the same credential an hour after a correct login, on a
// cookie that now lives for weeks rather than for twelve hours. Dropping
// HttpOnly there would be a downgrade nothing announces and no login-time test
// would see. `sessionCookie` is one function for exactly this, and this test is
// what stops a second cookie literal appearing beside it.
func TestARefreshedCookieKeepsEveryFlag(t *testing.T) {
	a := testAuth(t)
	clock := frozen(a, midday)
	cookie := signIn(t, a)

	*clock = clock.Add(31 * time.Minute)
	_, refreshed := use(t, a, cookie)
	if refreshed == nil {
		t.Fatal("no refreshed cookie to check")
	}
	if !refreshed.HttpOnly {
		t.Error("the refreshed session cookie is readable from JavaScript")
	}
	if refreshed.SameSite != http.SameSiteLaxMode {
		t.Error("the refreshed session cookie is not SameSite=Lax — a cross-site POST could carry it")
	}
	if refreshed.Path != "/" {
		t.Errorf("the refreshed cookie's path is %q, so half the console would stop sending it", refreshed.Path)
	}
	if refreshed.MaxAge <= 0 {
		t.Errorf("MaxAge = %d — the browser would drop this at the end of the session", refreshed.MaxAge)
	}
}

// TestNoRefreshOutlivesTheAbsoluteCap.
//
// The cap is the only bound in this design on a session that is *being* used,
// and a stolen cookie is used. So the arithmetic that keeps a working session
// alive must be unable to push past it — which is why `mint` takes the issue
// time as an argument rather than reading the clock a second time.
func TestNoRefreshOutlivesTheAbsoluteCap(t *testing.T) {
	a := testAuth(t) // idle 1h, cap 12h
	clock := frozen(a, midday)
	cookie := signIn(t, a)

	// Twelve hours of continuous use, in steps well inside the idle window.
	for elapsed := 45 * time.Minute; elapsed <= 11*time.Hour+30*time.Minute; elapsed += 45 * time.Minute {
		*clock = midday.Add(elapsed)
		reached, refreshed := use(t, a, cookie)
		if !reached {
			t.Fatalf("the session ended after %s, inside the twelve-hour cap", elapsed)
		}
		if refreshed != nil {
			cookie = refreshed
		}
	}

	// At the cap the deadline stops moving, so the cookie the browser holds says
	// less than an idle window and the last refresh wrote it that way.
	claims, ok := a.parseToken(cookie.Value)
	if !ok {
		t.Fatal("the cookie in hand is not a token this server signed")
	}
	if want := midday.Add(12 * time.Hour); !claims.expires.Equal(want) {
		t.Errorf("expiry = %s, want the cap at %s — a refresh moved the ceiling", claims.expires.UTC(), want.UTC())
	}

	*clock = midday.Add(12*time.Hour + time.Minute)
	if reached, _ := use(t, a, cookie); reached {
		t.Error("a session in continuous use outlived its absolute cap — nothing would ever end it")
	}
}

// TestShorteningTheCapEndsSessionsAlreadyIssued.
//
// `live` re-derives the ceiling from the configuration instead of trusting the
// expiry in the token. Without that, cutting KT_SESSION_MAX would leave every
// cookie already in a browser running to the old ceiling, and the only remedy
// would be rotating the signing key — which this project already calls the last
// resort rather than the dial.
func TestShorteningTheCapEndsSessionsAlreadyIssued(t *testing.T) {
	a := testAuth(t)
	clock := frozen(a, midday)
	cookie := signIn(t, a)

	*clock = clock.Add(30 * time.Minute)
	if reached, _ := use(t, a, cookie); !reached {
		t.Fatal("the session was refused half an hour in")
	}

	a.cfg.SessionMax = 10 * time.Minute
	if reached, _ := use(t, a, cookie); reached {
		t.Error("the session survived a cap shorter than its own age")
	}
}

// TestASessionCookieFrom051IsRefusedRatherThanTrusted.
//
// 0.5.1's payload is a bare expiry, and the signing key is read from
// credentials.json — so after an upgrade that cookie arrives **correctly
// signed**. The version prefix is the only thing that tells the two formats
// apart. Refusing it costs one login; reading it as "issued now" would start the
// ninety-day ceiling at an upgrade rather than at a login, and accepting it as
// v2 without a version would read one field where three are expected.
//
// The failure to guard against is not acceptance, it is a panic or a silent
// pass: an index into a two-element split of a one-element string is how this
// would have gone wrong.
func TestASessionCookieFrom051IsRefusedRatherThanTrusted(t *testing.T) {
	a := testAuth(t)
	frozen(a, midday)

	// Exactly what 0.5.1 wrote: strconv.FormatInt(expiry.Unix()) + "|" + sign.
	payload := strconv.FormatInt(midday.Add(11*time.Hour).Unix(), 10)
	old := payload + "|" + a.sign(payload)

	// And the version arm on its own, because the bare payload above is refused
	// by the "one field where three are expected" check as well — two guards over
	// one case, and neither is tested until one is tested where the other cannot
	// reach. This is a payload of the right *shape* wearing
	// another version, which only the version comparison can refuse.
	future := "3:" + strconv.FormatInt(midday.Unix(), 10) + ":" +
		strconv.FormatInt(midday.Add(time.Hour).Unix(), 10)
	if a.validToken(future + "|" + a.sign(future)) {
		t.Error("a token from another format version was accepted on the strength of being well-shaped")
	}

	if a.validToken(old) {
		t.Error("a 0.5.1 token was accepted — there are two token formats in the tree and only one is documented")
	}
	reached, refreshed := use(t, a, &http.Cookie{Name: a.cfg.CookieName, Value: old})
	if reached {
		t.Error("a 0.5.1 cookie got through the guard")
	}
	if refreshed != nil {
		t.Error("a cookie this server will not accept was refreshed rather than refused")
	}
}

// TestNoCookieIsWrittenOnAProtocolUpgrade.
//
// gorilla's Upgrader hijacks the connection and writes the 101 response itself,
// discarding whatever was set on the ResponseWriter. A `Set-Cookie` there is a
// refresh that appears in a log and in the code and never reaches a browser —
// which is worse than none, because it is what somebody debugging "my session
// ended while a terminal was open" would find and believe.
func TestNoCookieIsWrittenOnAProtocolUpgrade(t *testing.T) {
	a := testAuth(t)
	clock := frozen(a, midday)
	cookie := signIn(t, a)
	*clock = clock.Add(45 * time.Minute) // well past the refresh step

	reached := false
	guarded := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	req := httptest.NewRequest(http.MethodGet, "/kt-api/terminal", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)

	if !reached {
		t.Fatal("a live session was refused a terminal")
	}
	if got := rec.Header().Get("Set-Cookie"); got != "" {
		t.Errorf("Set-Cookie on an upgrade: %q — the hijacker discards it", got)
	}
}

// TestNothingRefreshesASessionThatIsNotThere. A public path and a request with
// no cookie must not acquire one on the way past: a front door that hands out
// sessions to anonymous requests is not a front door.
func TestNothingRefreshesASessionThatIsNotThere(t *testing.T) {
	a := testAuth(t)
	frozen(a, midday)

	if _, refreshed := use(t, a, nil); refreshed != nil {
		t.Error("a request with no session was handed one")
	}

	guarded := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/kt-pwa/manifest.webmanifest", nil))
	if got := rec.Header().Get("Set-Cookie"); got != "" {
		t.Errorf("the manifest was answered with %q", got)
	}
}

// TestTheTokenPayloadHasOneWriter.
//
// The trap to avoid: a verification path and a reissue path that disagree
// about the payload format. They cannot disagree if there is
// one function that writes it, so that is asserted about the source rather than
// about a round trip — a round trip passes the day somebody adds a second
// writer that happens to agree today.
func TestTheTokenPayloadHasOneWriter(t *testing.T) {
	src, err := os.ReadFile("auth.go")
	if err != nil {
		t.Fatalf("reading auth.go: %v", err)
	}
	if n := strings.Count(string(src), "tokenVersion + \":\""); n != 1 {
		t.Errorf("%d places build a token payload, want 1 (mint) — a second one is a format that can drift", n)
	}
	// Three: mint writes under the key its kind is signed with, and
	// parseToken verifies under each of the two keys before it reads anything.
	if n := strings.Count(string(src), "a.signToken("); n != 3 {
		t.Errorf("%d calls to signToken, want 3 (mint writes, parseToken verifies under both keys)", n)
	}
	// And no token is signed with the bare session key: sign is the
	// known-browser cookie's (known.go), not a session's.
	if n := strings.Count(string(src), "a.sign("); n != 0 {
		t.Errorf("%d calls to sign in auth.go, want 0: a session token is signed by signToken", n)
	}
}

package auth

// The native window arrives signed in through a one-time code bound to the
// launch. Each test names one property of that code.

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/identity"
)

// arrive is the window's GET, from addr.
func arrive(t *testing.T, a *Auth, code, addr string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, LaunchPath+"?"+LaunchCodeParam+"="+code, nil)
	req.RemoteAddr = addr
	rec := httptest.NewRecorder()
	a.LaunchSignIn(rec, req)
	return rec
}

const loopback = "127.0.0.1:52011"

// sessionCookieOf is the session cookie a response set, or nil.
func sessionCookieOf(a *Auth, rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == a.cfg.CookieName {
			return c
		}
	}
	return nil
}

// whoBehind is who the guard says a request carrying cookie is from, or false
// when it does not let the request through.
func whoBehind(t *testing.T, a *Auth, cookie *http.Cookie) (identity.Who, bool) {
	t.Helper()
	var who identity.Who
	var reached bool
	guarded := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		who, _ = identity.From(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/c/SHOP-EKS-1/pods", nil)
	req.Header.Set("Accept", "text/html")
	req.AddCookie(cookie)
	guarded.ServeHTTP(httptest.NewRecorder(), req)
	return who, reached
}

// refusedToSignIn asserts the answer a copied address gets: the sign-in page,
// and no session.
func refusedToSignIn(t *testing.T, a *Auth, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != LoginPath {
		t.Errorf("%s: answered %d to %q; want 302 to %s, the sign-in page",
			what, rec.Code, rec.Header().Get("Location"), LoginPath)
	}
	if c := sessionCookieOf(a, rec); c != nil {
		t.Errorf("%s: a session cookie was set", what)
	}
}

func TestALaunchCodeSignsInOnceAndTheSessionIsTheWindows(t *testing.T) {
	a := testAuth(t)
	code, err := a.IssueLaunchCode(true)
	if err != nil {
		t.Fatalf("IssueWindowPass: %v", err)
	}

	rec := arrive(t, a, code, loopback)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("the window's arrival answered %d to %q; want 302 to /, an address with no code in it",
			rec.Code, rec.Header().Get("Location"))
	}
	cookie := sessionCookieOf(a, rec)
	if cookie == nil {
		t.Fatal("the window's arrival set no session cookie")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control is %q; want no-store", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy is %q; want no-referrer", got)
	}

	who, reached := whoBehind(t, a, cookie)
	if !reached {
		t.Fatal("the guard refused the session the window's code opened")
	}
	if !who.Window || who.Method != identity.Launch || who.Name != "operator" || !who.Shared() {
		t.Errorf("the window's session reads as %+v; want the shared account, via launch, marked Window", who)
	}

	// A second use is refused, whatever it carries now.
	refusedToSignIn(t, a, arrive(t, a, code, loopback), "the same code a second time")
}

func TestAPasswordSessionIsNotTheWindows(t *testing.T) {
	a := testAuth(t)
	who, reached := whoBehind(t, a, signIn(t, a))
	if !reached {
		t.Fatal("the guard refused a password session")
	}
	if who.Window {
		t.Error("a session opened by typing the password is marked as the window's")
	}
}

func TestACodeFromAnotherLaunchIsRefused(t *testing.T) {
	// Two processes with the same credentials file: the same session key, each
	// with its own launch.
	first, second := testAuth(t), testAuth(t)
	old, err := first.IssueLaunchCode(true)
	if err != nil {
		t.Fatalf("IssueWindowPass: %v", err)
	}
	if _, err := second.IssueLaunchCode(true); err != nil {
		t.Fatalf("IssueWindowPass: %v", err)
	}
	refusedToSignIn(t, second, arrive(t, second, old, loopback), "the previous launch's code")

	// And a launch with no code drawn refuses every one.
	none := testAuth(t)
	refusedToSignIn(t, none, arrive(t, none, old, loopback), "a code presented to a launch that minted none")
	refusedToSignIn(t, none, arrive(t, none, "", loopback), "no code at all")
}

func TestEachWindowOpeningHasItsOwnCode(t *testing.T) {
	// A code is minted for every window opening, and one for a
	// second launch handed off to this process: two codes are two keys, each
	// opening one session.
	a := testAuth(t)
	first, err := a.IssueLaunchCode(true)
	if err != nil {
		t.Fatalf("IssueLaunchCode: %v", err)
	}
	second, err := a.IssueLaunchCode(true)
	if err != nil {
		t.Fatalf("IssueLaunchCode: %v", err)
	}
	if first == second {
		t.Fatal("two mints returned the same code")
	}
	for _, code := range []string{second, first} {
		if sessionCookieOf(a, arrive(t, a, code, loopback)) == nil {
			t.Errorf("a code this process minted opened no session")
		}
		refusedToSignIn(t, a, arrive(t, a, code, loopback), "a code used a second time")
	}
}

func TestACodeNobodyPresentsLapsesAfterAMinute(t *testing.T) {
	a := testAuth(t)
	a.now = func() time.Time { return midday }
	code, _ := a.IssueLaunchCode(true)
	a.now = func() time.Time { return midday.Add(launchCodeLife) }
	refusedToSignIn(t, a, arrive(t, a, code, loopback), "a code presented a minute after it was minted")

	fresh, _ := a.IssueLaunchCode(true)
	a.now = func() time.Time { return midday.Add(launchCodeLife + launchCodeLife - time.Second) }
	if sessionCookieOf(a, arrive(t, a, fresh, loopback)) == nil {
		t.Error("a code presented within its minute opened no session")
	}
}

func TestTheUnspentCodesAreBounded(t *testing.T) {
	a := testAuth(t)
	for i := 0; i < maxLaunchCodes; i++ {
		if _, err := a.IssueLaunchCode(true); err != nil {
			t.Fatalf("mint %d of %d: %v", i+1, maxLaunchCodes, err)
		}
	}
	if _, err := a.IssueLaunchCode(true); err == nil {
		t.Errorf("a code was minted past the bound of %d unspent ones", maxLaunchCodes)
	}
	// Lapsed codes stop counting.
	a.now = func() time.Time { return time.Now().Add(launchCodeLife) }
	if _, err := a.IssueLaunchCode(true); err != nil {
		t.Errorf("once every code had lapsed, minting was still refused: %v", err)
	}
}

func TestALaunchCodeIsRefusedFromAnotherMachineAndSpent(t *testing.T) {
	for _, addr := range []string{"192.0.2.7:40000", "[2001:db8::1]:40000", "not an address"} {
		a := testAuth(t)
		code, err := a.IssueLaunchCode(true)
		if err != nil {
			t.Fatalf("IssueWindowPass: %v", err)
		}
		refusedToSignIn(t, a, arrive(t, a, code, addr), "from "+addr)
		// Whoever sent it from there held the window's secret: it is gone.
		refusedToSignIn(t, a, arrive(t, a, code, loopback), "from loopback after "+addr+" presented it")
	}

	// IPv6 loopback, and IPv4 loopback written as IPv6, are this machine.
	for _, addr := range []string{"[::1]:40000", "[::ffff:127.0.0.1]:40000"} {
		a := testAuth(t)
		code, _ := a.IssueLaunchCode(true)
		if rec := arrive(t, a, code, addr); sessionCookieOf(a, rec) == nil {
			t.Errorf("from %s, which is loopback, the window's code opened no session", addr)
		}
	}
}

func TestNoLaunchCodeWhereNoPasswordIsOffered(t *testing.T) {
	// The server profile with an identity provider and no auth.localAccount:
	// the provider has removed the shared account the window would sign in as.
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, nil)
	if _, err := a.IssueLaunchCode(true); err == nil {
		t.Error("a launch code was minted where no password is offered")
	}
	rec := arrive(t, a, "anything", loopback)
	if rec.Code != http.StatusNotFound {
		t.Errorf("the window's route answered %d where no password is offered; want 404", rec.Code)
	}
}

func TestTheWindowMarkSurvivesTheSlidingRefresh(t *testing.T) {
	a := testAuth(t)
	a.now = func() time.Time { return midday }
	code, _ := a.IssueLaunchCode(true)
	cookie := sessionCookieOf(a, arrive(t, a, code, loopback))
	if cookie == nil {
		t.Fatal("no session cookie")
	}

	// testAuth's idle window is an hour, so the refresh step is half of it:
	// forty-five minutes on, the session is live and its cookie is due.
	a.now = func() time.Time { return midday.Add(45 * time.Minute) }
	_, refreshed := use(t, a, cookie)
	if refreshed == nil {
		t.Fatal("the session was not refreshed forty-five minutes on; the test is not exercising the refresh")
	}
	who, reached := whoBehind(t, a, refreshed)
	if !reached || !who.Window {
		t.Errorf("after the sliding refresh the session reads as %+v (reached %v); the window's mark was lost",
			who, reached)
	}
}

func TestTheWindowMarkCannotBeWrittenIntoABrowsersCookie(t *testing.T) {
	a := testAuth(t)
	cookie := signIn(t, a)
	payload, signature, _ := strings.Cut(cookie.Value, "|")
	forged := *cookie
	forged.Value = payload + ":" + windowMark + "|" + signature
	if _, reached := whoBehind(t, a, &forged); reached {
		t.Error("a password cookie with the window's mark appended, and its old signature, was accepted")
	}
}

func TestALaunchCodeNeverReachesTheLog(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	a := testAuth(t)
	code, _ := a.IssueLaunchCode(true)
	arrive(t, a, code, "192.0.2.7:40000") // refused from elsewhere, and spent
	b := testAuth(t)
	other, _ := b.IssueLaunchCode(true)
	arrive(t, b, other, loopback) // signs in
	arrive(t, b, other, loopback) // refused, spent

	if out.Len() == 0 {
		t.Fatal("nothing was logged; the test would pass on a handler that says nothing")
	}
	for _, tok := range []string{code, other} {
		if strings.Contains(out.String(), tok) {
			t.Errorf("the log carries the window's code:\n%s", out.String())
		}
	}
}

func TestABrowserWindowsCodeOpensAnUnmarkedSession(t *testing.T) {
	// Every window tier takes a code, and only the
	// native window's session is the window's - a browser is not this process's.
	a := testAuth(t)
	code, err := a.IssueLaunchCode(false)
	if err != nil {
		t.Fatalf("IssueLaunchCode: %v", err)
	}
	cookie := sessionCookieOf(a, arrive(t, a, code, loopback))
	if cookie == nil {
		t.Fatal("a browser window's code opened no session")
	}
	who, reached := whoBehind(t, a, cookie)
	if !reached || who.Window {
		t.Errorf("a browser window's session reads as %+v (reached %v); want an unmarked one", who, reached)
	}
}

func TestALaunchSessionSaysSoInTheSessionAndThePasswordOneDoesNot(t *testing.T) {
	a := testAuth(t)
	code, _ := a.IssueLaunchCode(false)
	launched := sessionCookieOf(a, arrive(t, a, code, loopback))
	for name, c := range map[string]struct {
		cookie *http.Cookie
		want   string
	}{
		"a launch code's session": {launched, "launch"},
		"a password session":      {signIn(t, a), "password"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
		req.AddCookie(c.cookie)
		session, ok := a.Current(req)
		if !ok || session.Method != c.want {
			t.Errorf("%s: Current = %+v (%v); want method %q", name, session, ok, c.want)
		}
	}
}

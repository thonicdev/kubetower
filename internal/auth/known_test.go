package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// loginAs is a JSON sign-in from remote carrying cookies, answering the
// recorder.
func loginAs(a *Auth, remote, password string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"user":"operator","password":"`+password+`"}`))
	request.RemoteAddr = remote
	for _, c := range cookies {
		request.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	a.LoginJSON(rec, request)
	return rec
}

func knownCookieOf(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == knownCookie {
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Errorf("the known-browser cookie is not HttpOnly and SameSite=Strict: %+v", c)
			}
			return c
		}
	}
	t.Fatal("a successful password sign-in set no known-browser cookie")
	return nil
}

// TestAKnownBrowserSignsInDuringAFlood: behind an ingress nobody named, one
// host flooding the anonymous queue at 500 requests a second refuses nearly
// every sign-in. A browser that signed in
// before carries a signed cookie that moves it to a lane of its own: during
// the flood it is admitted, within the queue's bound, every time; an anonymous
// attempt still faces the queue and is refused nearly every time.
func TestAKnownBrowserSignsInDuringAFlood(t *testing.T) {
	a := testAuth(t)
	a.delay = 0
	a.cfg.BehindProxy = true
	clock := time.Now()
	a.now = func() time.Time { return clock }
	var waited time.Duration
	a.sleep = func(d time.Duration) { waited = d }
	const ingress = "10.0.0.3:5000"

	known := knownCookieOf(t, loginAs(a, ingress, testPassword))

	knownIn, anonymousIn, probes := 0, 0, 0
	for step := 1; step <= 5000; step++ { // ten simulated seconds at 500 requests a second
		clock = clock.Add(2 * time.Millisecond)
		loginAs(a, ingress, "wrong") // the flood
		if step%500 != 0 {
			continue
		}
		probes++
		waited = 0
		if rec := loginAs(a, ingress, testPassword, known); rec.Code == http.StatusNoContent {
			knownIn++
			if waited > globalQueue {
				t.Errorf("a known browser waited %s, past the bound %s", waited, globalQueue)
			}
		} else {
			t.Errorf("a known browser was refused during the flood: %d %s", rec.Code, rec.Body.String())
		}
		if rec := loginAs(a, ingress, testPassword); rec.Code == http.StatusNoContent {
			anonymousIn++
		}
	}
	if knownIn != probes {
		t.Errorf("a known browser signed in %d of %d times during the flood", knownIn, probes)
	}
	if anonymousIn > probes/5 {
		t.Errorf("an anonymous attempt was admitted %d of %d times: it is not facing the flooded queue", anonymousIn, probes)
	}
}

// The cookie chooses a lane and nothing else: forged, expired or absent, the
// attempt is anonymous; and on its own it signs nobody in.
func TestAKnownBrowserCookieOnlyChoosesALane(t *testing.T) {
	a := testAuth(t)
	a.delay = 0
	known := knownCookieOf(t, loginAs(a, "198.51.100.7:4000", testPassword))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(known)
	if _, ok := a.knownBrowser(request); !ok {
		t.Fatal("the cookie the sign-in set is not read back")
	}
	forged := *known
	forged.Value = strings.Replace(known.Value, "|", "x|", 1)
	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&forged)
	if _, ok := a.knownBrowser(request); ok {
		t.Error("a tampered known-browser cookie was accepted")
	}
	later := time.Now().Add(knownTTL + time.Hour)
	a.now = func() time.Time { return later }
	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(known)
	if _, ok := a.knownBrowser(request); ok {
		t.Error("an expired known-browser cookie was accepted")
	}
	// It is not a session.
	if reached, _ := use(t, a, known); reached {
		t.Error("the known-browser cookie got through the guard on its own")
	}
}

// TestABracketedIPv6HopIsRead: `[2001:db8::1]` with no port is a form some
// proxies write, and neither address parser reads it.
func TestABracketedIPv6HopIsRead(t *testing.T) {
	for _, hop := range []string{"[2001:db8::1]", "[2001:db8::1]:443", "2001:db8::1"} {
		ip, ok := parseHop(hop)
		if !ok || ip.String() != "2001:db8::1" {
			t.Errorf("%q read as %v %t, want 2001:db8::1", hop, ip, ok)
		}
	}
}

// TestAQueueRefusalDoesNotChargeTheClient: when the queue refuses, the
// client's own budget is given back - a direct client retrying during a flood
// it did not cause is not left to meet its own refusal afterwards.
func TestAQueueRefusalDoesNotChargeTheClient(t *testing.T) {
	a := testAuth(t)
	a.delay = 0
	clock := time.Now()
	a.now = func() time.Time { return clock }
	a.sleep = func(time.Duration) {}

	for i := 0; i < 40; i++ { // fill the queue from forty other addresses
		loginAs(a, fmt.Sprintf("198.51.100.%d:4000", i+1), "wrong")
	}
	const person = "203.0.113.9:4000"
	for i := 0; i < attemptBurst; i++ {
		if rec := loginAs(a, person, "wrong"); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("attempt %d into a full queue answered %d, want 429", i+1, rec.Code)
		}
	}
	clock = clock.Add(globalQueue + time.Second) // the queue drains; the person's budget barely moves
	if rec := loginAs(a, person, testPassword); rec.Code != http.StatusNoContent {
		t.Errorf("after the flood the person's right password answered %d: the queue's refusals were charged to them", rec.Code)
	}
}

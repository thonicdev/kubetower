package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func jsonLogin(a *Auth, remote, forwarded, password string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"user":"operator","password":"`+password+`"}`))
	request.RemoteAddr = remote
	if forwarded != "" {
		request.Header.Set("X-Forwarded-For", forwarded)
	}
	rec := httptest.NewRecorder()
	a.LoginJSON(rec, request)
	return rec
}

// TestPasswordGuessesPastTheBudgetAreRefusedBeforeThePassword: each refusal
// costs a bcrypt, and uncounted, how many run at once would be the guesser's
// choice, on the process serving every person. Past a client's
// budget the answer is the contract's 429 `rate_limited`, with Retry-After, and
// it is given before the password is looked at: the right password is refused
// too, so the comparison never ran.
func TestPasswordGuessesPastTheBudgetAreRefusedBeforeThePassword(t *testing.T) {
	a := testAuth(t)
	a.delay = 0
	const guesser = "198.51.100.7:4000"

	for i := 0; i < attemptBurst; i++ {
		if rec := jsonLogin(a, guesser, "", "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("guess %d answered %d inside the budget, want 401", i+1, rec.Code)
		}
	}
	rec := jsonLogin(a, guesser, "", testPassword)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), `"rate_limited"`) {
		t.Fatalf("an attempt past the budget answered %d %s, want 429 rate_limited", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("the 429 says nothing about when to try again")
	}
	// Another client is not this one's budget.
	if rec := jsonLogin(a, "203.0.113.9:4000", "", testPassword); rec.Code != http.StatusNoContent {
		t.Errorf("another address was refused because of the guesser: %d", rec.Code)
	}

	// The form route spends the same budget.
	form := httptest.NewRequest(http.MethodPost, LoginPath, strings.NewReader(url.Values{
		"user": {"operator"}, "password": {testPassword}}.Encode()))
	form.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	form.RemoteAddr = guesser
	formRec := httptest.NewRecorder()
	a.Login(formRec, form)
	if formRec.Code != http.StatusTooManyRequests {
		t.Errorf("the form route answered %d past the budget, want 429", formRec.Code)
	}

	// And the budget refills.
	a.now = func() time.Time { return time.Now().Add(attemptEvery * attemptBurst) }
	if rec := jsonLogin(a, guesser, "", testPassword); rec.Code != http.StatusNoContent {
		t.Errorf("the budget never refilled: %d", rec.Code)
	}
}

// TestTheForwardedClientIsBelievedOnlyFromANamedProxy: behind an ingress every
// attempt comes from the controller's pod, so one guesser would spend everybody's
// budget and the log would name the ingress. X-Forwarded-For is read, but only
// when the connecting address is a proxy the deployment named.
func TestTheForwardedClientIsBelievedOnlyFromANamedProxy(t *testing.T) {
	a := testAuth(t)
	a.delay = 0
	proxies, err := ParseTrustedProxies("10.0.0.0/8, 192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.TrustedProxies = proxies
	const ingress = "10.0.0.3:5000"

	for i := 0; i < attemptBurst; i++ {
		jsonLogin(a, ingress, "198.51.100.7, 10.0.0.9", "wrong")
	}
	if rec := jsonLogin(a, ingress, "198.51.100.7, 10.0.0.9", "wrong"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("the forwarded guesser was not counted as itself: %d", rec.Code)
	}
	if rec := jsonLogin(a, ingress, "203.0.113.9", testPassword); rec.Code != http.StatusNoContent {
		t.Errorf("a person behind the same ingress was refused for the guesser: %d", rec.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.RemoteAddr = ingress
	request.Header.Set("X-Forwarded-For", "198.51.100.7")
	if line := a.clientLine(request); line != "198.51.100.7 (X-Forwarded-For, through trusted proxy 10.0.0.3)" {
		t.Errorf("the log names %q", line)
	}

	// From an address nobody named, the header is the guesser's own words.
	request.RemoteAddr = "198.51.100.99:5000"
	request.Header.Set("X-Forwarded-For", "203.0.113.1")
	if _, _, addr, via := a.client(request); addr != "198.51.100.99" || via != "" {
		t.Errorf("X-Forwarded-For was believed from an address nobody named: %s via %q", addr, via)
	}
}

func TestTrustedProxiesAreAddressesOrPrefixes(t *testing.T) {
	got, err := ParseTrustedProxies("10.1.2.3, fd00::/8 192.168.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("10.1.2.3/32"),
		netip.MustParsePrefix("fd00::/8"),
		netip.MustParsePrefix("192.168.0.0/16"),
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got[i], want[i])
		}
	}
	if _, err := ParseTrustedProxies("ingress.local"); err == nil {
		t.Error("a name was accepted as a proxy address")
	}
}

// The remembered clients are bounded.
func TestTheAttemptBudgetsAreBounded(t *testing.T) {
	var l attempts
	now := time.Now()
	for i := 0; i < attemptClients+500; i++ {
		l.spend(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}).String(), now)
	}
	if n := len(l.clients); n > attemptClients {
		t.Errorf("%d clients remembered, past the bound of %d", n, attemptClients)
	}
}

// TestOneGuesserBehindAnUnnamedIngressLocksNobodyOut: behind an ingress nobody
// named, every attempt comes from its address; a budget keyed on it would let
// ten wrong guesses refuse the next person's right password. That address
// names nobody: the right password signs in.
func TestOneGuesserBehindAnUnnamedIngressLocksNobodyOut(t *testing.T) {
	a := testAuth(t)
	a.delay = 0
	a.cfg.BehindProxy = true
	const ingress = "10.0.0.3:5000"

	for i := 0; i < attemptBurst+5; i++ {
		if rec := jsonLogin(a, ingress, "", "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("guess %d answered %d through the ingress, want 401", i+1, rec.Code)
		}
	}
	if rec := jsonLogin(a, ingress, "", testPassword); rec.Code != http.StatusNoContent {
		t.Errorf("the right password through the same ingress answered %d after another client's guesses, want 204", rec.Code)
	}
	if n := len(a.tries.clients); n != 0 {
		t.Errorf("%d per-client budgets kept for an address that names nobody", n)
	}
}

// TestFiftyAddressesInOneIPv6PrefixShareOneBudget: every IPv6 host holds a
// whole /64, so a budget keyed by the full address would be one per guess.
func TestFiftyAddressesInOneIPv6PrefixShareOneBudget(t *testing.T) {
	a := testAuth(t)
	a.delay = 0
	a.sleep = func(time.Duration) {}
	refused := 0
	for i := 1; i <= 50; i++ {
		remote := fmt.Sprintf("[2001:db8:1:2::%x]:4000", i)
		if rec := jsonLogin(a, remote, "", "wrong"); rec.Code == http.StatusTooManyRequests {
			refused++
		}
	}
	if refused != 50-attemptBurst {
		t.Errorf("%d of 50 guesses from one /64 were refused, want %d", refused, 50-attemptBurst)
	}
}

// TestTheProcessQueueBoundsAFloodAndNeverLocks: whoever is guessing, the
// process checks at most globalBurst at once and then globalEvery apart, an
// attempt waiting its turn up to globalQueue - and refused past it, never for
// longer: once the flood stops the next attempt is checked.
func TestTheProcessQueueBoundsAFloodAndNeverLocks(t *testing.T) {
	a := testAuth(t)
	a.delay = 0
	a.cfg.BehindProxy = true
	clock := time.Now()
	a.now = func() time.Time { return clock }
	var waited time.Duration
	a.sleep = func(d time.Duration) { waited += d }

	refused := 0
	for i := 0; i < 40; i++ {
		if rec := jsonLogin(a, "10.0.0.3:5000", "", "wrong"); rec.Code == http.StatusTooManyRequests {
			refused++
			if rec.Header().Get("Retry-After") == "" {
				t.Fatal("a queued-out attempt says nothing about when to retry")
			}
		}
	}
	admitted := 40 - refused
	if want := globalBurst + int(globalQueue/globalEvery); admitted != want {
		t.Errorf("%d attempts were checked in an instant, want the burst and the queue: %d", admitted, want)
	}
	if waited == 0 {
		t.Error("nothing waited its turn: the queue is a refusal, not a queue")
	}
	clock = clock.Add(time.Second)
	if rec := jsonLogin(a, "10.0.0.3:5000", "", testPassword); rec.Code != http.StatusNoContent {
		t.Errorf("a second after the flood the right password answered %d", rec.Code)
	}
}

// TestASignInForgivesOnlyItsOwnClient: a correct password gives back the budget
// of the client that sent it, and nobody else's.
func TestASignInForgivesOnlyItsOwnClient(t *testing.T) {
	a := testAuth(t)
	a.delay = 0
	a.sleep = func(time.Duration) {}
	const guesser = "198.51.100.7:4000"
	for i := 0; i < attemptBurst; i++ {
		jsonLogin(a, guesser, "", "wrong")
	}
	if rec := jsonLogin(a, "203.0.113.9:4000", "", testPassword); rec.Code != http.StatusNoContent {
		t.Fatalf("another client's sign-in answered %d", rec.Code)
	}
	if rec := jsonLogin(a, guesser, "", "wrong"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("another client's sign-in gave the guesser more tries: %d", rec.Code)
	}
}

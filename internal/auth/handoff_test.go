package auth

// A second launch's handoff is a mutual exchange. The instance proves the launcher secret before
// the client sends anything reusable, the client's proof names a server nonce
// only the instance issued and spends, and the code comes back sealed.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testLauncher = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testAddr     = "127.0.0.1:5823"
	clientNonce  = "00112233445566778899aabbccddeeff"
)

// step posts one step of the exchange, header as given, from addr.
func step(t *testing.T, a *Auth, method, header, addr, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, HandoffPath+query, nil)
	req.RemoteAddr = addr
	if header != "" {
		req.Header.Set(LauncherHeader, header)
	}
	rec := httptest.NewRecorder()
	a.Handoff(rec, req)
	return rec
}

func answerOf(t *testing.T, rec *httptest.ResponseRecorder) handoffAnswer {
	t.Helper()
	var got handoffAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("the answer %q is not the exchange's JSON: %v", rec.Body.String(), err)
	}
	return got
}

func hello(client string) string { return "v2.hello." + client }

func prove(secret, addr, client, server string, native bool) string {
	return "v2.prove." + client + "." + server + "." +
		handoffMAC(secret, "client", addr, client, server, nativeField(native))
}

func handingOff(t *testing.T) *Auth {
	t.Helper()
	a := testAuth(t)
	a.now = func() time.Time { return midday }
	a.OfferLaunches()
	a.AcceptHandoffs(testLauncher, testAddr)
	return a
}

// challenge runs hello and returns the server nonce, after checking the
// instance's proof the way a client does.
func challenge(t *testing.T, a *Auth, client string) string {
	t.Helper()
	rec := step(t, a, http.MethodPost, hello(client), loopback, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("hello answered %d; want 200", rec.Code)
	}
	got := answerOf(t, rec)
	if !isHandoffNonce(got.Challenge) {
		t.Fatalf("hello's challenge %q is not a nonce", got.Challenge)
	}
	if got.Proof != handoffMAC(testLauncher, "instance", testAddr, client, got.Challenge) {
		t.Fatal("the instance's proof is not the MAC of its address and both nonces")
	}
	if got.Sealed != "" {
		t.Fatal("hello was answered with a code")
	}
	return got.Challenge
}

func TestTheExchangeHandsASealedWorkingCode(t *testing.T) {
	a := handingOff(t)
	server := challenge(t, a, clientNonce)

	rec := step(t, a, http.MethodPost, prove(testLauncher, testAddr, clientNonce, server, true), loopback, "?native=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("a good prove answered %d; want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control is %q; want no-store", got)
	}
	got := answerOf(t, rec)
	code, ok := openCode(testLauncher, testAddr, clientNonce, server, "1", got.Sealed)
	if !ok {
		t.Fatal("the sealed code does not open with the secret and both nonces")
	}
	if strings.Contains(rec.Body.String(), code) {
		t.Fatal("the code is in the answer in the clear")
	}
	if _, ok := openCode(testLauncher, testAddr, clientNonce, server, "0", got.Sealed); ok {
		t.Error("a code sealed for a native window opens as a browser one")
	}
	cookie := sessionCookieOf(a, arrive(t, a, code, loopback))
	if cookie == nil {
		t.Fatal("the code the handoff returned opened no session")
	}
	if who, _ := whoBehind(t, a, cookie); !who.Window {
		t.Error("a code handed off for a native window opened an unmarked session")
	}
}

func TestAProveIsAnsweredOnce(t *testing.T) {
	a := handingOff(t)
	server := challenge(t, a, clientNonce)
	header := prove(testLauncher, testAddr, clientNonce, server, false)
	if rec := step(t, a, http.MethodPost, header, loopback, ""); rec.Code != http.StatusOK {
		t.Fatalf("the first prove answered %d; want 200", rec.Code)
	}
	if rec := step(t, a, http.MethodPost, header, loopback, ""); rec.Code != http.StatusForbidden {
		t.Errorf("the same prove again answered %d; want 403", rec.Code)
	}
}

// A relayed proof, at the level of the instance: a proof made with
// the right secret for this address, but for a server nonce this instance
// never issued - all a squatter could ever hold - is refused, however fresh.
func TestAProofForAChallengeThisInstanceNeverIssuedIsRefused(t *testing.T) {
	a := handingOff(t)
	foreign := "ffeeddccbbaa99887766554433221100"
	rec := step(t, a, http.MethodPost, prove(testLauncher, testAddr, clientNonce, foreign, true), loopback, "?native=1")
	if rec.Code != http.StatusForbidden {
		t.Errorf("a prove for a server nonce never issued answered %d; want 403", rec.Code)
	}
}

func TestAWrongProveSpendsItsChallenge(t *testing.T) {
	a := handingOff(t)
	server := challenge(t, a, clientNonce)
	wrong := strings.Repeat("f", len(testLauncher))
	if rec := step(t, a, http.MethodPost, prove(wrong, testAddr, clientNonce, server, false), loopback, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("a prove with another secret answered %d; want 403", rec.Code)
	}
	if rec := step(t, a, http.MethodPost, prove(testLauncher, testAddr, clientNonce, server, false), loopback, ""); rec.Code != http.StatusForbidden {
		t.Errorf("a challenge answered wrongly once could still be answered; got %d, want 403", rec.Code)
	}
}

func TestTheHandoffRefusesEverythingElse(t *testing.T) {
	wrong := strings.Repeat("f", len(testLauncher))
	for name, c := range map[string]struct {
		method string
		header func(a *Auth) string
		addr   string
		at     time.Duration
		want   int
	}{
		"a prove with another secret": {http.MethodPost, func(a *Auth) string {
			return prove(wrong, testAddr, clientNonce, challenge(t, a, clientNonce), false)
		}, loopback, 0, http.StatusForbidden},
		"a prove for another address": {http.MethodPost, func(a *Auth) string {
			return prove(testLauncher, "127.0.0.1:6000", clientNonce, challenge(t, a, clientNonce), false)
		}, loopback, 0, http.StatusForbidden},
		"a prove by another client nonce": {http.MethodPost, func(a *Auth) string {
			return prove(testLauncher, testAddr, "ffffffffffffffffffffffffffffffff", challenge(t, a, clientNonce), false)
		}, loopback, 0, http.StatusForbidden},
		"a prove after its challenge lapsed": {http.MethodPost, func(a *Auth) string {
			return prove(testLauncher, testAddr, clientNonce, challenge(t, a, clientNonce), false)
		}, loopback, challengeLife, http.StatusForbidden},
		"a hello whose nonce is not hex": {http.MethodPost, func(*Auth) string {
			return hello(strings.Repeat("z", 32))
		}, loopback, 0, http.StatusForbidden},
		"a hello whose nonce is upper-case": {http.MethodPost, func(*Auth) string {
			return hello(strings.ToUpper("aabbccddeeff00112233445566778899"))
		}, loopback, 0, http.StatusForbidden},
		"the v1 one-way proof": {http.MethodPost, func(*Auth) string {
			return "v1.1792002632." + clientNonce + "." + strings.Repeat("a", 64)
		}, loopback, 0, http.StatusForbidden},
		"the secret itself": {http.MethodPost, func(*Auth) string { return testLauncher }, loopback, 0, http.StatusForbidden},
		"no header":         {http.MethodPost, func(*Auth) string { return "" }, loopback, 0, http.StatusForbidden},
		"a hello from elsewhere": {http.MethodPost, func(*Auth) string {
			return hello(clientNonce)
		}, "192.0.2.7:40000", 0, http.StatusForbidden},
		"a GET": {http.MethodGet, func(*Auth) string { return hello(clientNonce) }, loopback, 0, http.StatusMethodNotAllowed},
	} {
		a := handingOff(t)
		header := c.header(a)
		a.now = func() time.Time { return midday.Add(c.at) }
		rec := step(t, a, c.method, header, c.addr, "")
		if rec.Code != c.want {
			t.Errorf("%s: answered %d; want %d", name, rec.Code, c.want)
		}
		if strings.Contains(rec.Body.String(), "sealed") || strings.Contains(rec.Body.String(), "challenge") {
			t.Errorf("%s: the refusal's body carries part of the exchange: %q", name, rec.Body.String())
		}
	}
}

func TestTheOpenChallengesAreBoundedByEvictingTheOldest(t *testing.T) {
	a := handingOff(t)
	said := capturedLog(t)
	oldest := challenge(t, a, clientNonce)
	for i := 1; i < maxChallenges; i++ {
		challenge(t, a, clientNonce)
	}
	newest := challenge(t, a, clientNonce) // one past the bound: answered, not refused
	if n := len(a.challenges.open); n != maxChallenges {
		t.Errorf("%d challenges are open; want the bound, %d", n, maxChallenges)
	}
	if rec := step(t, a, http.MethodPost, prove(testLauncher, testAddr, clientNonce, oldest, false), loopback, ""); rec.Code != http.StatusForbidden {
		t.Errorf("the evicted oldest challenge was still answered: %d", rec.Code)
	}
	if rec := step(t, a, http.MethodPost, prove(testLauncher, testAddr, clientNonce, newest, false), loopback, ""); rec.Code != http.StatusOK {
		t.Errorf("the newest challenge was not answered: %d", rec.Code)
	}
	if strings.Contains(said.String(), "<nil>") {
		t.Errorf("a log line prints a nil error:\n%s", said.String())
	}
	if !strings.Contains(said.String(), "the oldest was dropped") {
		t.Errorf("the eviction was not said:\n%s", said.String())
	}
}

// Seventy junk hellos from something local must not make the next launch's
// answer a 503, which the client would report as an impostor holding the port.
func TestAGenuineLaunchSucceedsAfterAFloodOfHellos(t *testing.T) {
	srv, a := liveInstance(t, testLauncher)
	for i := 0; i < 70; i++ {
		req, err := http.NewRequest(http.MethodPost, srv.URL+HandoffPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(LauncherHeader, hello(clientNonce))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("junk hello %d answered %d; a hello is never refused for being one too many", i+1, resp.StatusCode)
		}
	}
	code, err := RequestHandoff(context.Background(), testLauncher, addrOf(srv), true, 5*time.Second)
	if err != nil {
		t.Fatalf("a genuine launch after 70 junk hellos failed: %v", err)
	}
	if sessionCookieOf(a, arrive(t, a, code, loopback)) == nil {
		t.Error("its code opened no session")
	}
}

func TestABusyInstanceIsReportedAsBusyNotAsAnImpostor(t *testing.T) {
	srv, _ := squatter(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	})
	_, err := RequestHandoff(context.Background(), testLauncher, addrOf(srv), true, 5*time.Second)
	if !errors.Is(err, ErrInstanceBusy) || errors.Is(err, ErrNotTheInstance) {
		t.Errorf("a 503 was reported as %v; want ErrInstanceBusy and not ErrNotTheInstance", err)
	}
}

func TestNoHandoffWithoutALauncherSecret(t *testing.T) {
	a := testAuth(t)
	a.OfferLaunches()
	if a.AcceptsHandoffs() {
		t.Fatal("a front door with no launcher secret accepts handoffs")
	}
	if rec := step(t, a, http.MethodPost, hello(clientNonce), loopback, ""); rec.Code != http.StatusNotFound {
		t.Errorf("answered %d with no launcher secret installed; want 404", rec.Code)
	}
	a.AcceptHandoffs("short", testAddr)
	if a.AcceptsHandoffs() {
		t.Error("a launcher secret shorter than 32 characters was accepted")
	}
}

// --- the client half, against real listeners ---------------------------------

// liveInstance is the instance's route on a real loopback listener, the
// address every MAC names being the one it answers on.
func liveInstance(t *testing.T, secret string) (*httptest.Server, *Auth) {
	t.Helper()
	a := testAuth(t)
	a.OfferLaunches()
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+HandoffPath, a.Handoff)
	srv := httptest.NewUnstartedServer(mux)
	a.AcceptHandoffs(secret, srv.Listener.Addr().String())
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, a
}

func addrOf(srv *httptest.Server) string { return strings.TrimPrefix(srv.URL, "http://") }

func TestTheClientIsHandedAWorkingCodeByTheRealInstance(t *testing.T) {
	srv, a := liveInstance(t, testLauncher)
	code, err := RequestHandoff(context.Background(), testLauncher, addrOf(srv), true, 5*time.Second)
	if err != nil {
		t.Fatalf("the exchange with the real instance failed: %v", err)
	}
	if sessionCookieOf(a, arrive(t, a, code, loopback)) == nil {
		t.Error("the code the client was handed opened no session")
	}
}

func TestTheClientRefusesAnInstanceWithAnotherSecret(t *testing.T) {
	srv, _ := liveInstance(t, strings.Repeat("e", len(testLauncher)))
	_, err := RequestHandoff(context.Background(), testLauncher, addrOf(srv), true, 5*time.Second)
	if !errors.Is(err, ErrNotTheInstance) {
		t.Errorf("an instance holding another secret was not refused: %v", err)
	}
}

// squatter answers every request with answer and counts the requests that
// carried a prove: the step that must never reach anything unproven.
func squatter(t *testing.T, answer http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var proves atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get(LauncherHeader), ".prove.") {
			proves.Add(1)
		}
		answer(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &proves
}

func TestTheClientRefusesASquatter(t *testing.T) {
	jsonAnswer := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}
	}
	for name, answer := range map[string]http.HandlerFunc{
		"200 with a code of its own":      jsonAnswer(`{"code":"c2VjcmV0LWNvZGUtZm9yLWEtd2luZG93LXRoaXMtcHJvY2Vzcy1vcGVucw"}`),
		"a challenge and a made-up proof": jsonAnswer(`{"challenge":"00112233445566778899aabbccddeeff","proof":"` + strings.Repeat("a", 64) + `"}`),
		"a sealed code and nothing else":  jsonAnswer(`{"sealed":"AAAA"}`),
		"403":                             func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "refused", http.StatusForbidden) },
	} {
		srv, proves := squatter(t, answer)
		code, err := RequestHandoff(context.Background(), testLauncher, addrOf(srv), true, 5*time.Second)
		if !errors.Is(err, ErrNotTheInstance) || code != "" {
			t.Errorf("%s: the client took %q (%v); want ErrNotTheInstance and no code", name, code, err)
		}
		if n := proves.Load(); n != 0 {
			t.Errorf("%s: the squatter was sent %d proves; it must be sent none", name, n)
		}
	}
}

func TestTheClientNeverFollowsARedirect(t *testing.T) {
	var followed atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Add(1) }))
	t.Cleanup(elsewhere.Close)
	srv, _ := squatter(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+HandoffPath, http.StatusTemporaryRedirect)
	})
	_, err := RequestHandoff(context.Background(), testLauncher, addrOf(srv), true, 5*time.Second)
	if !errors.Is(err, ErrNotTheInstance) {
		t.Errorf("a redirect was not refused: %v", err)
	}
	if n := followed.Load(); n != 0 {
		t.Errorf("the redirect was followed %d times", n)
	}
}

// A relay in the middle, forwarding every byte to the real instance on another
// address: the instance's proof names its own address, so the client stops at
// the first step and never proves anything to the relay.
func TestARelayToAnInstanceOnAnotherAddressIsRefused(t *testing.T) {
	real, _ := liveInstance(t, testLauncher)
	target, err := url.Parse(real.URL)
	if err != nil {
		t.Fatal(err)
	}
	relay := httputil.NewSingleHostReverseProxy(target)
	var proves atomic.Int32
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get(LauncherHeader), ".prove.") {
			proves.Add(1)
		}
		relay.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)

	_, err = RequestHandoff(context.Background(), testLauncher, addrOf(front), true, 5*time.Second)
	if !errors.Is(err, ErrNotTheInstance) {
		t.Errorf("a relay to an instance on another address was not refused: %v", err)
	}
	if n := proves.Load(); n != 0 {
		t.Errorf("the relay was sent %d proves", n)
	}
}

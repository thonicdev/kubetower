package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/sharedstate"
)

// Several replicas behind one address, as far as the front door is concerned.
//
// Two Auths over one store are two pods behind one Service. Every property below
// has the same shape - begun on one, finished or read on the other - and each is
// paired with the control that shows the test measures the store rather than a
// coincidence: two Auths with their own memory, which is what two pods with
// unshared state are, fail it.

// twoReplicas builds two front doors over kv. Nil kv gives each its own memory.
func twoReplicas(t *testing.T, f *fakeIdP, kv sharedstate.Store) (*Auth, *Auth) {
	t.Helper()
	with := func(cfg *Config) { cfg.Store = kv }
	a, b := ssoAuth(t, f, with), ssoAuth(t, f, with)
	ready(t, a)
	ready(t, b)
	return a, b
}

func sealedMemory(t *testing.T, now func() time.Time) sharedstate.Store {
	t.Helper()
	memory := sharedstate.NewMemory(now)
	sealed, err := sharedstate.Seal(memory, []byte("a-thirty-two-byte-key-for-tests!"))
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func TestASignInStartedOnOneReplicaFinishesOnTheOther(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a, b := twoReplicas(t, f, sealedMemory(t, time.Now))

	// The click lands on A: A draws the state, the verifier and the nonce.
	s := start(t, a, "/c/prod/overview")
	f.grant("code-1", grant{challenge: s.challenge, claims: f.claimsFor(s.nonce, time.Now())})

	// The provider sends the browser back and the Service picks B. B has never
	// seen this flow, and must finish it anyway: the verifier it exchanges the
	// code with, and the nonce it checks the token against, come from the sealed
	// flow cookie, which any replica holding the shared session key can open.
	// The session it then opens is in the store.
	rec := finish(t, b, s, "code-1", s.state, true)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/c/prod/overview" {
		t.Fatalf("a sign-in started on one replica and finished on the other answered %d to %q", rec.Code,
			rec.Header().Get("Location"))
	}
	cookie := sessionFrom(rec, "kubetower_session")
	if cookie == nil {
		t.Fatal("the finishing replica set no session cookie")
	}

	// The session B opened is a session on A too, for the same person.
	want, ok := currentWith(b, cookie)
	if !ok || want.Identity == "" {
		t.Fatalf("the replica that opened the session does not recognize it: %+v, %v", want, ok)
	}
	if got, ok := currentWith(a, cookie); !ok || got.Identity != want.Identity {
		t.Errorf("the other replica answers %+v, %v for a session opened as %q", got, ok, want.Identity)
	}
}

// The control. A sign-in in progress is a sealed cookie rather than a store
// record, so what lets it cross replicas is **the shared session
// key**, not the store - which is why replicas.go refuses several replicas
// without KT_SESSION_SECRET. Two replicas with their own keys cannot finish each
// other's sign-in, whatever store they share.
func TestWithoutASharedKeyASignInDoesNotCrossReplicas(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	kv := sealedMemory(t, time.Now)
	a := ssoAuth(t, f, func(cfg *Config) { cfg.Store = kv })
	b := ssoAuth(t, f, func(cfg *Config) {
		cfg.Store = kv
		cfg.SessionKey = []byte("another-thirty-two-byte-test-key")
	})
	ready(t, a)
	ready(t, b)

	s := start(t, a, "/c/prod/overview")
	f.grant("code-1", grant{challenge: s.challenge, claims: f.claimsFor(s.nonce, time.Now())})
	rec := finish(t, b, s, "code-1", s.state, true)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "sso="+ssoExpired) {
		t.Fatalf("two replicas with different session keys finished each other's sign-in (%d to %q); the "+
			"control is not measuring what the test above claims", rec.Code, loc)
	}
}

func TestASignOutOnOneReplicaEndsTheSessionOnBoth(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a, b := twoReplicas(t, f, sealedMemory(t, time.Now))
	cookie := signInAs(t, a, f, "code-1", nil)
	if _, ok := currentWith(b, cookie); !ok {
		t.Fatal("the other replica does not see the session, so a sign-out there proves nothing")
	}

	req := httptest.NewRequest(http.MethodPost, consoleOrigin+"/api/auth/logout", nil)
	req.AddCookie(cookie)
	b.LogoutJSON(httptest.NewRecorder(), req)

	for name, replica := range map[string]*Auth{"the replica that signed it out": b, "the one that opened it": a} {
		if _, ok := currentWith(replica, cookie); ok {
			t.Errorf("%s still honors a copy of the cookie after the sign-out", name)
		}
	}
}

// A callback replayed on the other replica opens nothing.
//
// **Single use is the console's own record, in the shared store.** The flow is
// a sealed cookie (oidc.go, `flow`), so the server holds nothing between the
// click and the return; once a callback has been verified, a hash of its state
// is written set-if-absent into the store every replica shares, and a second
// callback with the same state - on either replica - finds it there. The first
// row is the provider refusing a redeemed code as well; the second is a provider
// that honors a code twice, where only the shared record refuses the replay;
// the third is the control, two replicas with their own memory, where the
// replay on the other one gets through - so the second row measures the store.
func TestAReplayedCallbackOnTheOtherReplicaOpensNothing(t *testing.T) {
	replay := func(t *testing.T, kv sharedstate.Store, redeemTwice bool) (*Auth, *httptest.ResponseRecorder) {
		t.Helper()
		f := newFakeIdP(t, "/dex")
		f.redeemTwice.Store(redeemTwice)
		a, b := twoReplicas(t, f, kv)
		s := start(t, a, "/")
		f.grant("code-1", grant{challenge: s.challenge, claims: f.claimsFor(s.nonce, time.Now())})
		if rec := finish(t, a, s, "code-1", s.state, true); sessionFrom(rec, "kubetower_session") == nil {
			t.Fatalf("the first callback did not sign in: %d %s", rec.Code, rec.Header().Get("Location"))
		}
		return b, finish(t, b, s, "code-1", s.state, true)
	}
	t.Run("the provider refuses a redeemed code", func(t *testing.T) {
		b, rec := replay(t, sealedMemory(t, time.Now), false)
		assertRefused(t, b, rec, ssoFailed)
	})
	t.Run("the provider honors a code twice, and the shared record refuses it", func(t *testing.T) {
		b, rec := replay(t, sealedMemory(t, time.Now), true)
		assertRefused(t, b, rec, ssoExpired)
	})
	t.Run("the control: without a shared store the replay gets through", func(t *testing.T) {
		if _, rec := replay(t, nil, true); sessionFrom(rec, "kubetower_session") == nil {
			t.Errorf("two replicas with their own memory refused the replay (%d %s); the row above "+
				"would not be measuring the shared store", rec.Code, rec.Header().Get("Location"))
		}
	})
}

// TestASessionRecordExpiresWithItsCookie is "the same idle and absolute limits
// the console already enforces", in the store: the record goes when the cookie
// would, a session in use keeps its record alive, and the cap ends both.
func TestASessionRecordExpiresWithItsCookie(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	var mu sync.Mutex
	clock := time.Now()
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	advance := func(d time.Duration) { mu.Lock(); clock = clock.Add(d); mu.Unlock() }

	memory := sharedstate.NewMemory(now)
	a := ssoAuth(t, f, func(cfg *Config) {
		cfg.Store = memory
		cfg.SessionIdle = 3 * time.Hour
		cfg.SessionMax = 24 * time.Hour
		cfg.OIDC.SessionMax = 10 * time.Hour
	})
	a.now = now
	ready(t, a)
	cookie := signInAs(t, a, f, "code-1", nil)

	guarded := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	use := func() int {
		req := httptest.NewRequest(http.MethodGet, consoleOrigin+"/api/clusters", nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		guarded.ServeHTTP(rec, req)
		if refreshed := sessionFrom(rec, "kubetower_session"); refreshed != nil {
			cookie = refreshed
		}
		return rec.Code
	}

	// Used every two hours, the session outlives its three-hour idle window
	// several times over: the record moves with the cookie.
	for i := 0; i < 4; i++ {
		advance(2 * time.Hour)
		if code := use(); code != http.StatusNoContent {
			t.Fatalf("a session in use was refused after %d hours (%d): the record did not move with the "+
				"cookie", 2*(i+1), code)
		}
	}
	if memory.Len(sessionPrefix) != 1 {
		t.Fatalf("the store holds %d sessions, expected 1", memory.Len(sessionPrefix))
	}

	// Eight hours in. Two more is past the single sign-on cap of ten, and the
	// store has let go of the record by then whatever the cookie says.
	advance(2*time.Hour + time.Minute)
	if code := use(); code == http.StatusNoContent {
		t.Error("a session past its cap was honored")
	}
	if n := memory.Len(sessionPrefix); n != 0 {
		t.Errorf("the store still holds %d session records past the cap", n)
	}
}

// Left alone, the record goes at the idle deadline, not at the cap.
func TestAnIdleSessionRecordGoesAtTheIdleDeadline(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	var mu sync.Mutex
	clock := time.Now()
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	memory := sharedstate.NewMemory(now)
	a := ssoAuth(t, f, func(cfg *Config) { cfg.Store = memory; cfg.SessionIdle = time.Hour })
	a.now = now
	ready(t, a)
	signInAs(t, a, f, "code-1", nil)
	if memory.Len(sessionPrefix) != 1 {
		t.Fatal("no record after a sign-in")
	}
	mu.Lock()
	clock = clock.Add(time.Hour + time.Second)
	mu.Unlock()
	if n := memory.Len(sessionPrefix); n != 0 {
		t.Errorf("the store keeps an idle session's record past its idle deadline (%d left)", n)
	}
}

// unanswering is a store that never answers.
type unanswering struct{}

func (unanswering) Put(context.Context, string, []byte, time.Time) error {
	return sharedstate.ErrUnreachable
}
func (unanswering) PutNew(context.Context, string, []byte, time.Time) (bool, error) {
	return false, sharedstate.ErrUnreachable
}
func (unanswering) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, sharedstate.ErrUnreachable
}
func (unanswering) Take(context.Context, string) ([]byte, bool, error) {
	return nil, false, sharedstate.ErrUnreachable
}
func (unanswering) Extend(context.Context, string, time.Time) (bool, error) {
	return false, sharedstate.ErrUnreachable
}
func (unanswering) Delete(context.Context, string) error { return sharedstate.ErrUnreachable }
func (unanswering) Describe() string                     { return "nothing" }

// TestAStoreThatDoesNotAnswerIsNotAnExpiredSession is "an empty answer and a
// failed answer are different answers" at the front door. A store that did not
// answer must not send the person to sign in - signing in needs the same store -
// and must not be drawn as the identity provider's fault.
func TestAStoreThatDoesNotAnswerIsNotAnExpiredSession(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, func(cfg *Config) { cfg.Store = unanswering{} })
	ready(t, a)

	// A correctly signed cookie naming a session: only the store can say
	// whether it is live, and the store says nothing.
	now := a.now()
	token, _ := a.mint(now, now, strings.Repeat("ab", sessionIDBytes), byPassword)
	guarded := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a request whose session could not be read reached the console")
	}))

	req := httptest.NewRequest(http.MethodGet, consoleOrigin+"/api/clusters", nil)
	req.AddCookie(&http.Cookie{Name: "kubetower_session", Value: token})
	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	var problem api.Problem
	_ = json.Unmarshal(rec.Body.Bytes(), &problem)
	if rec.Code != http.StatusServiceUnavailable || problem.Code != api.ProblemStoreUnreachable {
		t.Errorf("an API request answered %d %q, expected 503 %q", rec.Code, problem.Code,
			api.ProblemStoreUnreachable)
	}

	req = httptest.NewRequest(http.MethodGet, consoleOrigin+"/c/prod/overview", nil)
	req.Header.Set("Accept", "text/html")
	req.AddCookie(&http.Cookie{Name: "kubetower_session", Value: token})
	rec = httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Location") != "" {
		t.Errorf("a navigation answered %d to %q, expected a 503 and no redirect to sign in", rec.Code,
			rec.Header().Get("Location"))
	}

	// And a sign-in says the store, not the provider. The start records
	// nothing since the flow is a sealed cookie, so it reaches the provider;
	// the callback is where a session is opened, and where the store is asked.
	s := start(t, a, "/")
	f.grant("code-store", grant{challenge: s.challenge, claims: f.claimsFor(s.nonce, time.Now())})
	rec = finish(t, a, s, "code-store", s.state, true)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc == nil || loc.Query().Get("sso") != ssoStore {
		t.Errorf("a sign-in the store could not record was sent to %q, expected sso=%s", rec.Header().Get("Location"),
			ssoStore)
	}
}

// TestARecordRewrittenInTheStoreIsNoSession is why the store is sealed: whoever
// can write to it - its password, a shell in its pod - must not be able to
// rewrite their own record's groups and have their own cookie assert them.
func TestARecordRewrittenInTheStoreIsNoSession(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	memory := sharedstate.NewMemory(time.Now)
	sealed, err := sharedstate.Seal(memory, []byte("a-thirty-two-byte-key-for-tests!"))
	if err != nil {
		t.Fatal(err)
	}
	a := ssoAuth(t, f, func(cfg *Config) { cfg.Store = sealed })
	ready(t, a)
	cookie := signInAs(t, a, f, "code-1", nil)
	c, _ := a.parseToken(cookie.Value)

	// What somebody holding only the store can write: a record of their own
	// making, in plain JSON, under the id their own cookie names.
	forged, _ := json.Marshal(storedSession{Name: "kilgore@kilgore.trout", Groups: []string{"system:masters"},
		Issued: time.Now()})
	if err := memory.Put(context.Background(), sessionPrefix+c.sid, forged, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, ok := currentWith(a, cookie); ok {
		t.Errorf("a record rewritten in the store was honored, with groups %v", got.Groups)
	}
}

// deleteRefusing is a working store whose Delete does not answer: a sign-out
// that reaches Valkey at the moment it blinks.
type deleteRefusing struct{ sharedstate.Store }

func (deleteRefusing) Delete(context.Context, string) error { return sharedstate.ErrUnreachable }

// TestASignOutTheStoreDidNotRecordDoesNotAnswerSuccess: the store did not
// record the sign-out, so the session is still live. Neither route may answer
// as though it had worked, nor answer 503 and still clear the cookie, which
// would leave a browser that looked signed out and a session that was not,
// with the cookie a retry needs deleted. **The cookie is kept**, and a retry
// once the store answers signs the session out.
func TestASignOutTheStoreDidNotRecordDoesNotAnswerSuccess(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, func(cfg *Config) { cfg.Store = deleteRefusing{sealedMemory(t, time.Now)} })
	ready(t, a)
	cookie := signInAs(t, a, f, "code-1", nil)

	req := httptest.NewRequest(http.MethodPost, consoleOrigin+"/api/auth/logout", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	a.LogoutJSON(rec, req)
	var problem api.Problem
	_ = json.Unmarshal(rec.Body.Bytes(), &problem)
	if rec.Code != http.StatusServiceUnavailable || problem.Code != api.ProblemStoreUnreachable {
		t.Errorf("a sign-out the store did not record answered %d %q, expected 503 %q", rec.Code, problem.Code,
			api.ProblemStoreUnreachable)
	}
	// What a person reads calls the act Log out, as the console's control does;
	// the route and the code keep their names.
	if problem.Title != "Log-out not recorded" || !strings.Contains(problem.Detail, "Log out again") ||
		strings.Contains(strings.ToLower(problem.Title+problem.Detail), "sign out") {
		t.Errorf("the problem says %q / %q, expected it to name the act Log out", problem.Title, problem.Detail)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "kubetower_session" && c.MaxAge < 0 {
			t.Error("a sign-out the store did not record cleared the cookie; the browser can no longer " +
				"retry it, and the session it names is still live")
		}
	}
	// The truth the 503 tells: the session is still live.
	if _, ok := currentWith(a, cookie); !ok {
		t.Fatal("the session ended although its record was never deleted; the test measures nothing")
	}

	// The form route says it in a page, not a redirect to sign in.
	form := logoutForm(cookie)
	page := httptest.NewRecorder()
	a.Logout(page, form)
	if page.Code != http.StatusServiceUnavailable || !strings.Contains(page.Body.String(), "could not log this session out") ||
		strings.Contains(strings.ToLower(page.Body.String()), "sign out") {
		t.Errorf("the form's sign-out answered %d to %q, expected a 503 page saying it was not recorded",
			page.Code, page.Header().Get("Location"))
	}
	for _, c := range page.Result().Cookies() {
		if c.Name == "kubetower_session" && c.MaxAge < 0 {
			t.Error("the form's sign-out cleared the cookie although the store did not record it")
		}
	}

	// **The retry is what keeping the cookie buys.** The store answers again,
	// the same cookie signs out, and then it opens nothing.
	a.store = newSessionStore(a.store.kv.(deleteRefusing).Store)
	retry := httptest.NewRequest(http.MethodPost, consoleOrigin+"/api/auth/logout", nil)
	retry.AddCookie(cookie)
	again := httptest.NewRecorder()
	a.LogoutJSON(again, retry)
	if again.Code != http.StatusNoContent {
		t.Fatalf("the retried sign-out answered %d, want 204", again.Code)
	}
	if _, ok := currentWith(a, cookie); ok {
		t.Error("the retried sign-out left the session live")
	}
}

package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc/oidctest"
	"golang.org/x/crypto/bcrypt"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/sharedstate"
)

// The single sign-on flow, against an identity provider that is real HTTP and
// real signatures: a discovery document, a key set, a token endpoint, and ID
// tokens signed with a key the console only learns through the key set.
//
// **Nothing here is mocked below the network.** The console discovers, fetches
// keys, exchanges a code with its PKCE verifier and verifies a JWS, exactly as it
// does against Dex; what the fake decides is only what the token *says*, which is
// the thing every refusal row below varies.

const (
	testClientID     = "kubetower"
	testClientSecret = "kubetower-test-secret"
	consoleOrigin    = "http://console.test"
)

// fakeIdP is an OpenID provider under `prefix` on one httptest server.
type fakeIdP struct {
	t      *testing.T
	server *httptest.Server
	prefix string
	// issuer is what the discovery document and every token say. Normally the
	// server's own URL plus prefix; the split test sets another.
	issuer string
	// discoveredIssuer overrides what the discovery document names, for the
	// test of a document naming another issuer.
	discoveredIssuer string

	key    *ecdsa.PrivateKey
	keyID  string
	keys   *oidctest.Server
	down   atomic.Bool
	tokens atomic.Int32
	// redeemTwice makes the token endpoint honor a code more than once - a
	// provider breaking RFC 6749 §4.1.2, which the console must not rely on.
	redeemTwice atomic.Bool

	mu sync.Mutex
	// codes maps an issued code to what its token will say, and the PKCE
	// challenge it must be exchanged with.
	codes map[string]grant
	// lastVerifier is the code_verifier the console last sent.
	lastVerifier string
}

type grant struct {
	challenge string
	claims    map[string]any
	// signWith, where set, signs the token with a key the key set does not
	// carry.
	signWith *ecdsa.PrivateKey
}

func newFakeIdP(t *testing.T, prefix string) *fakeIdP {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{t: t, prefix: prefix, key: key, keyID: "test-key", codes: map[string]grant{}}
	f.keys = &oidctest.Server{
		PublicKeys: []oidctest.PublicKey{{PublicKey: key.Public(), KeyID: f.keyID, Algorithm: "ES256"}},
		Algorithms: []string{"ES256"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc(prefix+"/.well-known/openid-configuration", f.discovery)
	mux.HandleFunc(prefix+"/keys", func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/keys"
		f.keys.ServeHTTP(w, r)
	})
	mux.HandleFunc(prefix+"/token", f.token)
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	f.issuer = f.server.URL + prefix
	f.keys.SetIssuer(f.issuer)
	return f
}

func (f *fakeIdP) discovery(w http.ResponseWriter, r *http.Request) {
	if f.down.Load() {
		http.Error(w, "the provider is down for the test", http.StatusServiceUnavailable)
		return
	}
	named := f.issuer
	if f.discoveredIssuer != "" {
		named = f.discoveredIssuer
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                named,
		"authorization_endpoint":                f.issuer + "/auth",
		"token_endpoint":                        f.issuer + "/token",
		"jwks_uri":                              f.issuer + "/keys",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"ES256", "none"},
	})
}

func (f *fakeIdP) token(w http.ResponseWriter, r *http.Request) {
	f.tokens.Add(1)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	user, pass, _ := r.BasicAuth()
	if user == "" {
		user, pass = r.PostFormValue("client_id"), r.PostFormValue("client_secret")
	}
	if user != testClientID || pass != testClientSecret {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	g, ok := f.codes[r.PostFormValue("code")]
	if !f.redeemTwice.Load() {
		delete(f.codes, r.PostFormValue("code"))
	}
	f.lastVerifier = r.PostFormValue("code_verifier")
	f.mu.Unlock()
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	// PKCE, checked the way a provider checks it: S256 of the verifier must be
	// the challenge the authorization request carried.
	sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"PKCE verification failed"}`))
		return
	}
	payload, _ := json.Marshal(g.claims)
	signer := f.key
	if g.signWith != nil {
		signer = g.signWith
	}
	idToken := oidctest.SignIDToken(signer, f.keyID, "ES256", string(payload))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "an-access-token", "token_type": "Bearer", "expires_in": 3600, "id_token": idToken,
	})
}

// claimsFor is a token the console should accept, for this nonce.
func (f *fakeIdP) claimsFor(nonce string, now time.Time) map[string]any {
	return map[string]any{
		"iss":            f.issuer,
		"sub":            "CiQwOGE4Njg0Yi1kYjg4LTRiNzMtOTBhOS0zY2QxNjYxZjU0NjYSBWxvY2Fs",
		"aud":            testClientID,
		"exp":            now.Add(time.Hour).Unix(),
		"iat":            now.Unix(),
		"nonce":          nonce,
		"email":          "kilgore@kilgore.trout",
		"email_verified": true,
		"groups":         []any{"authors"},
		"name":           "Kilgore Trout",
	}
}

// ssoAuth is an Auth with single sign-on against f, and no shared password.
func ssoAuth(t *testing.T, f *fakeIdP, tweak func(*Config)) *Auth {
	t.Helper()
	cfg := Config{
		SessionKey:  []byte("a-thirty-two-byte-key-for-tests!"),
		SessionIdle: time.Hour,
		SessionMax:  24 * time.Hour,
		CookieName:  "kubetower_session",
		OIDC: &OIDCConfig{
			Issuer:       f.issuer,
			ClientID:     testClientID,
			ClientSecret: testClientSecret,
			RedirectURL:  consoleOrigin + CallbackPath,
			Scopes:       []string{"openid", "email", "groups"},
			GroupsPrefix: "oidc:",
			HTTPClient:   f.server.Client(),
		},
	}
	if tweak != nil {
		tweak(&cfg)
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.delay = 0
	return a
}

// ready starts discovery and waits for it to answer.
func ready(t *testing.T, a *Auth) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a.Start(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if a.Methods().SSO.State == api.SSOReady {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("single sign-on never became ready: %+v", a.Methods().SSO)
}

// started is one click on "Sign in with SSO", as far as the provider.
type started struct {
	flowCookie *http.Cookie
	state      string
	nonce      string
	challenge  string
	authorize  *url.URL
}

func start(t *testing.T, a *Auth, next string) started {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, consoleOrigin+StartPath+"?next="+url.QueryEscape(next), nil)
	rec := httptest.NewRecorder()
	a.StartSSO(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("start answered %d, want 302: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("start answered with a body the contract does not declare: %q", rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	var flowCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == flowCookieName {
			flowCookie = c
		}
	}
	if flowCookie == nil {
		t.Fatal("start set no flow cookie, so nothing binds the state to this browser")
	}
	q := loc.Query()
	return started{flowCookie: flowCookie, state: q.Get("state"), nonce: q.Get("nonce"),
		challenge: q.Get("code_challenge"), authorize: loc}
}

// finish is the provider's redirect back, carrying `code` and the flow's state.
func finish(t *testing.T, a *Auth, s started, code, state string, withCookie bool) *httptest.ResponseRecorder {
	t.Helper()
	target := consoleOrigin + CallbackPath + "?" + url.Values{"code": {code}, "state": {state}}.Encode()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if withCookie {
		req.AddCookie(&http.Cookie{Name: flowCookieName, Value: s.flowCookie.Value})
	}
	rec := httptest.NewRecorder()
	a.FinishSSO(rec, req)
	return rec
}

func (f *fakeIdP) grant(code string, g grant) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.codes[code] = g
}

func sessionFrom(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name && c.Value != "" {
			return c
		}
	}
	return nil
}

func currentWith(a *Auth, cookie *http.Cookie) (api.Session, bool) {
	req := httptest.NewRequest(http.MethodGet, consoleOrigin+"/api/session", nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return a.Current(req)
}

// signInAs runs the whole flow and returns the session cookie.
func signInAs(t *testing.T, a *Auth, f *fakeIdP, code string, edit func(map[string]any)) *http.Cookie {
	t.Helper()
	s := start(t, a, "/c/prod/overview")
	claims := f.claimsFor(s.nonce, time.Now())
	if edit != nil {
		edit(claims)
	}
	f.grant(code, grant{challenge: s.challenge, claims: claims})
	rec := finish(t, a, s, code, s.state, true)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/c/prod/overview" {
		t.Fatalf("the callback answered %d to %q, want 303 to /c/prod/overview", rec.Code,
			rec.Header().Get("Location"))
	}
	cookie := sessionFrom(rec, "kubetower_session")
	if cookie == nil {
		t.Fatal("a verified sign-in set no session cookie")
	}
	return cookie
}

// --- The round trip -----------------------------------------------------------

func TestASignInThroughTheProviderOpensASessionForThatPerson(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, nil)
	ready(t, a)

	s := start(t, a, "/c/prod/overview")

	// What the browser was sent to carries everything the flow rests on.
	q := s.authorize.Query()
	for name, want := range map[string]string{
		"client_id":             testClientID,
		"redirect_uri":          consoleOrigin + CallbackPath,
		"response_type":         "code",
		"code_challenge_method": "S256",
	} {
		if got := q.Get(name); got != want {
			t.Errorf("the authorization request's %s is %q, want %q", name, got, want)
		}
	}
	if s.state == "" || s.nonce == "" || s.challenge == "" {
		t.Fatalf("state %q, nonce %q, challenge %q: one of the three is missing", s.state, s.nonce, s.challenge)
	}
	if !strings.HasPrefix(s.authorize.String(), f.issuer+"/auth?") {
		t.Errorf("the browser was sent to %s, not to the provider's authorization endpoint", s.authorize)
	}
	if !s.flowCookie.HttpOnly || s.flowCookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("the flow cookie is HttpOnly=%v SameSite=%v; it must be HttpOnly and Lax", s.flowCookie.HttpOnly,
			s.flowCookie.SameSite)
	}

	f.grant("code-1", grant{challenge: s.challenge, claims: f.claimsFor(s.nonce, time.Now())})
	rec := finish(t, a, s, "code-1", s.state, true)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/c/prod/overview" {
		t.Fatalf("the callback answered %d to %q, want 303 to /c/prod/overview", rec.Code, rec.Header().Get("Location"))
	}
	if rec.Body.Len() != 0 {
		t.Errorf("the callback answered with a body the contract does not declare: %q", rec.Body.String())
	}
	// PKCE was exercised rather than merely requested: the verifier the console
	// sent is the one whose S256 is the challenge (the fake refuses otherwise).
	f.mu.Lock()
	verifier := f.lastVerifier
	f.mu.Unlock()
	if verifier == "" {
		t.Error("the code exchange carried no code_verifier")
	}

	cookie := sessionFrom(rec, "kubetower_session")
	if cookie == nil || !cookie.HttpOnly {
		t.Fatalf("no HttpOnly session cookie was set: %+v", cookie)
	}
	session, ok := currentWith(a, cookie)
	if !ok {
		t.Fatal("the cookie a verified sign-in set is not a session")
	}
	if session.Identity != "kilgore@kilgore.trout" || session.Method != api.SessionMethodSSO {
		t.Errorf("the session is %q by %q, want kilgore@kilgore.trout by sso", session.Identity, session.Method)
	}
	if len(session.Groups) != 1 || session.Groups[0] != "oidc:authors" {
		t.Errorf("the session's groups are %v, want [oidc:authors] - the provider's group with the prefix", session.Groups)
	}
	if want := time.Now().Add(DefaultSSOSessionMax); session.AbsoluteExpiresAt.After(want.Add(time.Minute)) {
		t.Errorf("a single sign-on session is capped at %s, past the %s cap", session.AbsoluteExpiresAt, DefaultSSOSessionMax)
	}
}

// --- Every claim that matters, refused one at a time --------------------------

// TestEveryWrongClaimIsARefusalAndNeverAFallback is the table
// docs/SERVER-MODE-THREAT-MODEL.md §4.4 asks for: one row per claim, each
// asserting a refusal **and** that no identity came out of it. A refusal that
// fell back to another identity - the subject where the email was missing, "no
// groups" where the claim was malformed - would be a person asserted to the API
// server on a guess.
func TestEveryWrongClaimIsARefusalAndNeverAFallback(t *testing.T) {
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		name string
		edit func(c map[string]any)
		sign *ecdsa.PrivateKey
		code string
	}{
		{"another issuer", func(c map[string]any) { c["iss"] = "https://evil.example/dex" }, nil, ssoFailed},
		{"another audience", func(c map[string]any) { c["aud"] = "somebody-else" }, nil, ssoFailed},
		{"expired", func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }, nil, ssoFailed},
		{"not yet valid", func(c map[string]any) { c["nbf"] = time.Now().Add(time.Hour).Unix() }, nil, ssoFailed},
		{"another flow's nonce", func(c map[string]any) { c["nonce"] = "not-this-flow" }, nil, ssoFailed},
		{"no nonce at all", func(c map[string]any) { delete(c, "nonce") }, nil, ssoFailed},
		{"signed with a key the key set does not carry", nil, otherKey, ssoFailed},
		{"several audiences and no authorized party", func(c map[string]any) { c["aud"] = []any{testClientID, "another"} },
			nil, ssoFailed},
		{"an authorized party that is another client", func(c map[string]any) { c["azp"] = "another" }, nil, ssoFailed},
		{"no email", func(c map[string]any) { delete(c, "email") }, nil, ssoUnnamed},
		{"an unverified email", func(c map[string]any) { c["email_verified"] = false }, nil, ssoUnnamed},
		{"groups that are not names", func(c map[string]any) { c["groups"] = []any{"ok", 42} }, nil, ssoUnnamed},
		{"groups that are an object", func(c map[string]any) { c["groups"] = map[string]any{"a": 1} }, nil, ssoUnnamed},
		// The scalar form of the list form's empty element: not "no groups",
		// which is the claim being absent - and with the prefix it would be the
		// group "oidc:".
		{"groups that are an empty string", func(c map[string]any) { c["groups"] = "" }, nil, ssoUnnamed},
	}
	for i, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := newFakeIdP(t, "/dex")
			a := ssoAuth(t, f, nil)
			ready(t, a)
			s := start(t, a, "/")
			claims := f.claimsFor(s.nonce, time.Now())
			if row.edit != nil {
				row.edit(claims)
			}
			code := "code-" + string(rune('a'+i))
			f.grant(code, grant{challenge: s.challenge, claims: claims, signWith: row.sign})
			rec := finish(t, a, s, code, s.state, true)
			assertRefused(t, a, rec, row.code)
		})
	}
}

// The three that are about the flow rather than the token.
func TestAForgedOrReplayedCallbackIsRefused(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, nil)
	ready(t, a)

	t.Run("a state this browser was not given", func(t *testing.T) {
		s := start(t, a, "/")
		f.grant("code-x", grant{challenge: s.challenge, claims: f.claimsFor(s.nonce, time.Now())})
		assertRefused(t, a, finish(t, a, s, "code-x", "somebody-elses-state", true), ssoExpired)
	})
	t.Run("no flow cookie - the callback did not come from this browser's click", func(t *testing.T) {
		s := start(t, a, "/")
		f.grant("code-y", grant{challenge: s.challenge, claims: f.claimsFor(s.nonce, time.Now())})
		assertRefused(t, a, finish(t, a, s, "code-y", s.state, false), ssoExpired)
	})
	// **Single use has three layers** (see flow): the callback deletes the flow
	// cookie, a code is redeemed once, and the console records the state. The
	// first two are asserted here, the third in the row after it.
	t.Run("the same callback twice", func(t *testing.T) {
		s := start(t, a, "/")
		f.grant("code-z", grant{challenge: s.challenge, claims: f.claimsFor(s.nonce, time.Now())})
		rec := finish(t, a, s, "code-z", s.state, true)
		if rec.Code != http.StatusSeeOther || sessionFrom(rec, "kubetower_session") == nil {
			t.Fatalf("the first callback did not sign in: %d %s", rec.Code, rec.Header().Get("Location"))
		}
		cleared := false
		for _, c := range rec.Result().Cookies() {
			if c.Name == flowCookieName && c.MaxAge < 0 {
				cleared = true
			}
		}
		if !cleared {
			t.Error("the callback left the flow cookie in the browser; it is what a replay would carry")
		}
		// A replay that kept a copy of the cookie still carries a code the
		// provider has redeemed.
		assertRefused(t, a, finish(t, a, s, "code-z", s.state, true), ssoFailed)
	})
	// **And single use does not rest on the provider.** Against one that
	// redeems a code twice, the replay above carries a code that still works,
	// and only the console's own record of the state refuses it.
	t.Run("the same callback twice, against a provider that redeems a code twice", func(t *testing.T) {
		f.redeemTwice.Store(true)
		defer f.redeemTwice.Store(false)
		s := start(t, a, "/")
		f.grant("code-twice", grant{challenge: s.challenge, claims: f.claimsFor(s.nonce, time.Now())})
		rec := finish(t, a, s, "code-twice", s.state, true)
		if rec.Code != http.StatusSeeOther || sessionFrom(rec, "kubetower_session") == nil {
			t.Fatalf("the first callback did not sign in: %d %s", rec.Code, rec.Header().Get("Location"))
		}
		assertRefused(t, a, finish(t, a, s, "code-twice", s.state, true), ssoExpired)
	})
	t.Run("a flow cookie changed by one byte", func(t *testing.T) {
		s := start(t, a, "/")
		f.grant("code-t", grant{challenge: s.challenge, claims: f.claimsFor(s.nonce, time.Now())})
		raw := []byte(s.flowCookie.Value)
		if raw[10] == 'A' {
			raw[10] = 'B'
		} else {
			raw[10] = 'A'
		}
		s.flowCookie = &http.Cookie{Name: flowCookieName, Value: string(raw)}
		assertRefused(t, a, finish(t, a, s, "code-t", s.state, true), ssoExpired)
	})
	t.Run("a flow sealed by another console", func(t *testing.T) {
		other := ssoAuth(t, f, func(c *Config) { c.SessionKey = []byte("another-thirty-two-byte-test-key") })
		ready(t, other)
		s := start(t, other, "/")
		f.grant("code-o", grant{challenge: s.challenge, claims: f.claimsFor(s.nonce, time.Now())})
		assertRefused(t, a, finish(t, a, s, "code-o", s.state, true), ssoExpired)
	})
	t.Run("a flow older than its lifetime", func(t *testing.T) {
		s := start(t, a, "/")
		f.grant("code-late", grant{challenge: s.challenge, claims: f.claimsFor(s.nonce, time.Now())})
		a.now = func() time.Time { return time.Now().Add(flowLifetime + time.Second) }
		defer func() { a.now = time.Now }()
		assertRefused(t, a, finish(t, a, s, "code-late", s.state, true), ssoExpired)
	})
	t.Run("the provider said no", func(t *testing.T) {
		s := start(t, a, "/")
		req := httptest.NewRequest(http.MethodGet, consoleOrigin+CallbackPath+"?"+url.Values{
			"error": {"access_denied"}, "error_description": {"<script>alert(1)</script>"},
			"state": {s.state}}.Encode(), nil)
		req.AddCookie(&http.Cookie{Name: flowCookieName, Value: s.flowCookie.Value})
		rec := httptest.NewRecorder()
		a.FinishSSO(rec, req)
		assertRefused(t, a, rec, ssoRefused)
		if strings.Contains(rec.Header().Get("Location"), "script") {
			t.Errorf("the provider's own words reached the redirect: %s", rec.Header().Get("Location"))
		}
	})
}

func assertRefused(t *testing.T, a *Auth, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		t.Errorf("a refused sign-in answered %d, want 303 to the sign-in page", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc == nil || loc.Path != LoginPath || loc.Query().Get("sso") != code {
		t.Errorf("a refused sign-in went to %q, want %s?sso=%s", rec.Header().Get("Location"), LoginPath, code)
	}
	if c := sessionFrom(rec, "kubetower_session"); c != nil {
		if _, ok := currentWith(a, c); ok {
			t.Fatal("a refused sign-in produced a session - a fallback identity")
		}
		t.Error("a refused sign-in set a session cookie at all")
	}
	if _, ok := ssoSentences[code]; !ok {
		t.Errorf("the code %q has no sentence on the sign-in page", code)
	}
}

// --- One person's sign-out is theirs alone ------------------------------------

func TestSigningOnePersonOutLeavesTheOtherSignedIn(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, nil)
	ready(t, a)

	alice := signInAs(t, a, f, "code-alice", func(c map[string]any) { c["email"] = "alice@example.com" })
	bob := signInAs(t, a, f, "code-bob", func(c map[string]any) { c["email"] = "bob@example.com" })

	req := httptest.NewRequest(http.MethodPost, consoleOrigin+"/api/auth/logout", nil)
	req.AddCookie(alice)
	a.LogoutJSON(httptest.NewRecorder(), req)

	if _, ok := currentWith(a, alice); ok {
		t.Error("alice signed out and her cookie still opens a session - a copy of it would too")
	}
	if s, ok := currentWith(a, bob); !ok || s.Identity != "bob@example.com" {
		t.Errorf("alice signing out ended bob's session too (%v, %q)", ok, s.Identity)
	}
	// And through the guard, which is what every other request meets.
	if reached, _ := use(t, a, alice); reached {
		t.Error("alice's cookie got through the guard after she signed out")
	}
	if reached, _ := use(t, a, bob); !reached {
		t.Error("bob's cookie was refused by the guard after alice signed out")
	}
}

// A forged cookie naming somebody's session id cannot end it: the id is inside
// the signed payload, so ending a session needs a cookie this process signed.
func TestAForgedCookieCannotSignSomebodyElseOut(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, nil)
	ready(t, a)
	bob := signInAs(t, a, f, "code-bob", nil)

	payload, _, _ := strings.Cut(bob.Value, "|")
	forged := &http.Cookie{Name: "kubetower_session", Value: payload + "|" + strings.Repeat("A", 43)}
	req := httptest.NewRequest(http.MethodPost, consoleOrigin+"/api/auth/logout", nil)
	req.AddCookie(forged)
	a.LogoutJSON(httptest.NewRecorder(), req)
	if _, ok := currentWith(a, bob); !ok {
		t.Error("a cookie with bob's session id and a forged signature signed bob out")
	}
}

// --- Configuring a provider removes the shared password ------------------------

func TestAProviderRemovesTheSharedPasswordUnlessItIsKept(t *testing.T) {
	f := newFakeIdP(t, "/dex")

	removed := ssoAuth(t, f, nil)
	if removed.OffersPassword() {
		t.Fatal("a provider is configured and the shared password is still offered - removing it is the default")
	}
	if removed.Methods().Password {
		t.Error("the sign-in methods offer a password the console does not have")
	}
	// A password cookie minted before the provider was configured - the signing
	// key survives that change in the chart's Secret - must not outlive it.
	now := removed.now()
	old, _ := removed.mint(now, now, "", byPassword)
	if reached, _ := use(t, removed, &http.Cookie{Name: "kubetower_session", Value: old}); reached {
		t.Error("a shared-password cookie got through a console that no longer offers the password")
	}
	if page := renderPage(t, removed, ""); strings.Contains(page, `type="password"`) {
		t.Error("the sign-in page draws a password form for a route that does not exist")
	}

	hash := testHash(t)
	kept := ssoAuth(t, f, func(c *Config) {
		c.KeepLocalAccount = true
		c.User = "admin"
		c.PasswordHash = hash
	})
	if !kept.OffersPassword() {
		t.Fatal("KeepLocalAccount is set and the password is not offered")
	}
	now = kept.now()
	cookie, _ := kept.mint(now, now, "", byPassword)
	if reached, _ := use(t, kept, &http.Cookie{Name: "kubetower_session", Value: cookie}); !reached {
		t.Error("with the account kept, its cookie was refused")
	}
	session, ok := currentWith(kept, &http.Cookie{Name: "kubetower_session", Value: cookie})
	if !ok || session.Method != api.SessionMethodPassword || session.Identity != "admin" {
		t.Errorf("the shared account's session reads %q by %q, want admin by password", session.Identity, session.Method)
	}
}

func TestNewRefusesAProviderThatCouldNeverWork(t *testing.T) {
	base := func() *OIDCConfig {
		return &OIDCConfig{Issuer: "https://idp.example", ClientID: "c",
			RedirectURL: "https://console.example" + CallbackPath}
	}
	cases := map[string]func(*OIDCConfig){
		"no client id":                 func(c *OIDCConfig) { c.ClientID = "" },
		"a relative issuer":            func(c *OIDCConfig) { c.Issuer = "idp.example" },
		"a relative redirect URL":      func(c *OIDCConfig) { c.RedirectURL = CallbackPath },
		"a redirect URL elsewhere":     func(c *OIDCConfig) { c.RedirectURL = "https://console.example/callback" },
		"a redirect URL with a query":  func(c *OIDCConfig) { c.RedirectURL += "?x=1" },
		"a discovery URL with no host": func(c *OIDCConfig) { c.DiscoveryURL = "/dex" },
		// A prefix that is itself Kubernetes' own, or that carries a line
		// ending pasted with it: a console that would refuse, or misname,
		// every person it signs in.
		"a username prefix system:":            func(c *OIDCConfig) { c.UsernamePrefix = "system:" },
		"a username prefix with a line ending": func(c *OIDCConfig) { c.UsernamePrefix = "corp:\n" },
		"a username prefix that is not UTF-8":  func(c *OIDCConfig) { c.UsernamePrefix = "corp\xff:" },
		"a groups prefix with a line ending":   func(c *OIDCConfig) { c.GroupsPrefix = "oidc:\r\n" },
		// The default, resolved: the issuer is the prefix where the claim is
		// not `email`, so an issuer kube-apiserver would make reserved is too.
		"an issuer that would prefix every name with system:": func(c *OIDCConfig) {
			c.Issuer = "system://idp.example"
			c.UsernameClaim = "sub"
		},
	}
	for name, breakIt := range cases {
		cfg := base()
		breakIt(cfg)
		if _, err := New(Config{SessionKey: []byte("a-thirty-two-byte-key-for-tests!"), OIDC: cfg}); err == nil {
			t.Errorf("%s: New accepted it", name)
		}
	}
	if _, err := New(Config{SessionKey: []byte("a-thirty-two-byte-key-for-tests!"), OIDC: base()}); err != nil {
		t.Errorf("a complete configuration was refused: %v", err)
	}
}

// --- An unreachable provider does not take the console down --------------------

func TestAnUnreachableProviderIsAStateAndNotAFailure(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	f.down.Store(true)
	a := ssoAuth(t, f, nil)

	if got := a.Methods().SSO.State; got != api.SSOPending {
		t.Errorf("before the first attempt the provider reads %q, want pending", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)
	waitFor(t, func() bool { return a.Methods().SSO.State == api.SSOUnavailable })
	sso := a.Methods().SSO
	if !strings.Contains(sso.Detail, "HTTP 503") {
		t.Errorf("the unavailable state does not say why: %q", sso.Detail)
	}
	if strings.Contains(sso.Detail, f.server.URL) || strings.Contains(sso.Detail, "down for the test") {
		t.Errorf("the unavailable state publishes the provider's address or its words: %q", sso.Detail)
	}
	if strings.Contains(sso.Detail, testClientSecret) {
		t.Error("the client secret reached the sign-in page")
	}

	// A click while it is down goes back to the sign-in page, saying so.
	rec := httptest.NewRecorder()
	a.StartSSO(rec, httptest.NewRequest(http.MethodGet, consoleOrigin+StartPath, nil))
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || !strings.Contains(loc, "sso=unavailable") {
		t.Errorf("a click while the provider is down answered %d to %q", rec.Code, loc)
	}
	if page := renderPage(t, a, ""); !strings.Contains(page, "Single sign-on is unavailable") {
		t.Error("the sign-in page does not say the provider is unavailable")
	}

	// It comes back, and the console notices without a restart - the click
	// above asked for an immediate retry.
	f.down.Store(false)
	a.sso.retryNow()
	waitFor(t, func() bool { return a.Methods().SSO.State == api.SSOReady })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the condition never held")
}

// --- The issuer trap ------------------------------------------------------------

// TestTheIssuerAndTheDiscoveryAddressAreTwoSettings is the measured requirement:
// the browser and the pod reach the provider at different addresses, and the
// token's `iss` is the browser's. Here the issuer is a name nothing resolves, and
// the console discovers, fetches keys and exchanges the code through the other
// address - while every token is still verified against the issuer string.
func TestTheIssuerAndTheDiscoveryAddressAreTwoSettings(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	internal := f.issuer
	f.issuer = "http://idp.public.invalid/dex"
	a := ssoAuth(t, f, func(c *Config) {
		c.OIDC.Issuer = f.issuer
		c.OIDC.DiscoveryURL = internal
	})
	ready(t, a)

	s := start(t, a, "/c/prod/overview")
	if !strings.HasPrefix(s.authorize.String(), "http://idp.public.invalid/dex/auth?") {
		t.Errorf("the browser was sent to %s; the authorization endpoint is the browser's and is left as "+
			"the document names it", s.authorize)
	}
	cookie := func() *http.Cookie {
		f.grant("code-split", grant{challenge: s.challenge, claims: f.claimsFor(s.nonce, time.Now())})
		rec := finish(t, a, s, "code-split", s.state, true)
		return sessionFrom(rec, "kubetower_session")
	}()
	if cookie == nil {
		t.Fatal("a sign-in through a separate discovery address did not complete")
	}
	if f.tokens.Load() == 0 {
		t.Error("the token endpoint was never called through the discovery address")
	}
}

// A discovery document naming another issuer is refused rather than trusted,
// although go-oidc would accept it once told the addresses differ.
func TestADiscoveryDocumentNamingAnotherIssuerIsRefused(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	f.discoveredIssuer = "https://evil.example/dex"
	internal := f.issuer
	// Two addresses, so go-oidc's own issuer check is the one switched off and
	// this console's is the only one left.
	a := ssoAuth(t, f, func(c *Config) {
		c.OIDC.Issuer = "http://idp.public.invalid/dex"
		c.OIDC.DiscoveryURL = internal
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)
	waitFor(t, func() bool { return a.Methods().SSO.State == api.SSOUnavailable })
	// The page says which kind of failure it is; the log names the issuer.
	d := a.Methods().SSO.Detail
	if !strings.Contains(d, "another issuer") {
		t.Errorf("the refusal does not say the document names another issuer: %q", d)
	}
	if strings.Contains(d, "evil.example") || strings.Contains(d, "127.0.0.1") {
		t.Errorf("an anonymous reader of the sign-in page is told what the document and its address say: %q", d)
	}
	a.sso.mu.RLock()
	logged := a.sso.lastErr
	a.sso.mu.RUnlock()
	if !strings.Contains(logged, "evil.example") {
		t.Errorf("the log's error does not name the issuer the document claimed: %q", logged)
	}
}

// TestTheSignInPageNeverPublishesTheProvidersAddress: /api/auth/methods and the
// sign-in page are public, and the discovery error names the address the pod
// reaches the provider at and the IP it resolved to. A category, and an HTTP
// status where there is one, is what an anonymous caller gets.
func TestTheSignInPageNeverPublishesTheProvidersAddress(t *testing.T) {
	// A port nothing listens on: the error is a refused connection, naming it.
	a, err := New(Config{
		SessionKey: []byte("a-thirty-two-byte-key-for-tests!"),
		OIDC: &OIDCConfig{Issuer: "http://127.0.0.1:1/dex", ClientID: testClientID,
			RedirectURL: consoleOrigin + CallbackPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)
	waitFor(t, func() bool { return a.Methods().SSO.State == api.SSOUnavailable })

	d := a.Methods().SSO.Detail
	for _, leak := range []string{"127.0.0.1", ":1/", "dial tcp", "refused", "well-known"} {
		if strings.Contains(d, leak) {
			t.Errorf("the public detail carries %q: %q", leak, d)
		}
	}
	if !strings.Contains(d, "did not answer") {
		t.Errorf("the public detail does not say the provider did not answer: %q", d)
	}
	if page := renderPage(t, a, ""); strings.Contains(page, "127.0.0.1") {
		t.Error("the sign-in page carries the provider's address")
	}
}

// --- Tokens and secrets stay where they are -------------------------------------

// TestAStoredSessionCarriesNoToken is docs/DEFINITION-OF-DONE.md §16 on the one
// record this package keeps: built from a verified token, marshalled, and
// searched for the token.
func TestAStoredSessionCarriesNoToken(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, nil)
	ready(t, a)
	cookie := signInAs(t, a, f, "code-1", nil)

	c, ok := a.parseToken(cookie.Value)
	if !ok || c.sid == "" {
		t.Fatal("the session cookie does not name a stored session")
	}
	rec, _, _ := a.store.get(context.Background(), c.sid)
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"an-access-token", "eyJ", testClientSecret} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("the stored session carries %q: %s", forbidden, raw)
		}
	}
	cfg, _ := json.Marshal(a.sso.cfg)
	if strings.Contains(string(cfg), testClientSecret) {
		t.Errorf("the provider's configuration serializes its client secret: %s", cfg)
	}
}

// A click that arrived on another origin than the redirect URL names is sent
// there first, so the flow cookie lands where the provider will return - and
// only once, so a proxy that rewrites Host cannot loop the browser.
func TestAClickOnAnotherOriginStartsOnTheConfiguredOne(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, nil)
	ready(t, a)

	rec := httptest.NewRecorder()
	a.StartSSO(rec, httptest.NewRequest(http.MethodGet, "http://localhost:5823"+StartPath+"?next=%2Fx", nil))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || loc == nil || loc.Host != "console.test" || loc.Path != StartPath ||
		loc.Query().Get("next") != "/x" {
		t.Fatalf("a click on another origin went to %q", rec.Header().Get("Location"))
	}
	rec = httptest.NewRecorder()
	a.StartSSO(rec, httptest.NewRequest(http.MethodGet, "http://localhost:5823"+StartPath+"?next=%2Fx&hop=1", nil))
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, f.issuer+"/auth?") {
		t.Errorf("the second arrival was sent round again to %q", loc)
	}
}

// The page says what came back, in its own words, for every code.
func TestTheSignInPageSaysWhySingleSignOnFailedInItsOwnWords(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, nil)
	ready(t, a)
	for code, sentence := range ssoSentences {
		page := renderPage(t, a, "?sso="+code)
		if !strings.Contains(page, htmlEscaped(sentence)) {
			t.Errorf("?sso=%s does not draw %q", code, sentence)
		}
	}
	if page := renderPage(t, a, "?sso=%3Cb%3Eowned%3C%2Fb%3E"); strings.Contains(page, "owned") {
		t.Error("an unknown code was echoed onto the page")
	}
	if page := renderPage(t, a, ""); !strings.Contains(page, `href="`+StartPath+`?next=%2F"`) {
		t.Error("the sign-in page has no link to start single sign-on")
	}
}

func testHash(t *testing.T) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(hash)
}

func renderPage(t *testing.T, a *Auth, query string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	a.LoginPage(rec, httptest.NewRequest(http.MethodGet, LoginPath+query, nil))
	return rec.Body.String()
}

func htmlEscaped(s string) string {
	return strings.NewReplacer("'", "&#39;", `"`, "&#34;").Replace(s)
}

// A name or a group carrying a newline cannot forge a second log line: the
// sign-in line quotes both, because both come from the provider.
func TestTheSignInLineQuotesWhatTheProviderWrote(t *testing.T) {
	if got := groupList([]string{"ok", "evil\n[auth] signed in with single sign-on: \"root\""}); strings.Contains(got, "\n") {
		t.Errorf("the group list carries a raw newline: %q", got)
	}
	if got := groupList(nil); got != "none" {
		t.Errorf("no groups reads %q, want none", got)
	}
}

// The guard hands the handler who the request is from, for the write log: the
// person for a single sign-on session, the shared account for a password one.
func TestTheGuardPassesWhoTheRequestIsFromToTheHandler(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	hash := testHash(t)
	a := ssoAuth(t, f, func(c *Config) {
		c.KeepLocalAccount = true
		c.User = "admin"
		c.PasswordHash = hash
	})
	ready(t, a)
	person := signInAs(t, a, f, "code-who", func(c map[string]any) {
		c["email"] = "carol@example.com"
		c["groups"] = []any{"platform", "oncall"}
	})
	now := a.now()
	shared, _ := a.mint(now, now, "", byPassword)

	for _, tc := range []struct {
		cookie *http.Cookie
		want   identity.Who
	}{
		// **With the groups**, prefixed as they will be asserted: the access
		// review names both, and a person reviewed without their groups is
		// reviewed as somebody with fewer rights than they have.
		{person, identity.Who{Name: "carol@example.com", Method: identity.SSO, Groups: []string{"oidc:platform", "oidc:oncall"}}},
		{&http.Cookie{Name: "kubetower_session", Value: shared}, identity.Who{Name: "admin", Method: identity.Password}},
	} {
		var got identity.Who
		guarded := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, _ = identity.From(r.Context())
		}))
		req := httptest.NewRequest(http.MethodGet, consoleOrigin+"/api/clusters", nil)
		req.AddCookie(tc.cookie)
		guarded.ServeHTTP(httptest.NewRecorder(), req)
		// The session tag is this sign-in's own; the password account has none.
		if (got.Session != "") != (tc.want.Method == identity.SSO) {
			t.Errorf("the session tag is %q for a %s session", got.Session, got.Method)
		}
		got.Session = ""
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("the handler was told %+v, want %+v", got, tc.want)
		}
	}
}

// --- A public start route holds nothing a flood can evict ----------------------

// TestAFloodOfStartsEvictsNobodysSignIn is the denial of service the flow
// cookie removes. With flows kept server-side in a map bounded at 1 024,
// anybody could open 1 024 flows through the public start route and every
// sign-in in progress would be dropped; a person back from the provider would
// read "that sign-in took too long".
func TestAFloodOfStartsEvictsNobodysSignIn(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, nil)
	ready(t, a)

	victim := start(t, a, "/c/prod/overview")
	for i := 0; i < 2048; i++ {
		rec := httptest.NewRecorder()
		a.StartSSO(rec, httptest.NewRequest(http.MethodGet, consoleOrigin+StartPath, nil))
		if rec.Code != http.StatusFound {
			t.Fatalf("start %d answered %d", i, rec.Code)
		}
	}
	f.grant("code-victim", grant{challenge: victim.challenge, claims: f.claimsFor(victim.nonce, time.Now())})
	rec := finish(t, a, victim, "code-victim", victim.state, true)
	if rec.Code != http.StatusSeeOther || sessionFrom(rec, "kubetower_session") == nil {
		t.Fatalf("after 2 048 anonymous starts the person's own sign-in answered %d to %q; a flood evicted it",
			rec.Code, rec.Header().Get("Location"))
	}
}

// --- Kubernetes' own names are never the provider's to give --------------------

// TestKubernetesReservedNamesAreRefused: the console asserts the
// name and groups to the API server, and an IdP group `system:masters` with the
// default empty prefix would be cluster-admin granted at the provider.
func TestKubernetesReservedNamesAreRefused(t *testing.T) {
	rows := []struct {
		name   string
		prefix string
		edit   func(c map[string]any)
	}{
		{"a group system:masters with no prefix", "", func(c map[string]any) {
			c["groups"] = []any{"authors", "system:masters"}
		}},
		{"a single group system:nodes with no prefix", "", func(c map[string]any) { c["groups"] = "system:nodes" }},
		{"a person named system:admin", "oidc:", func(c map[string]any) { c["email"] = "system:admin" }},
		{"a service account's name", "oidc:", func(c map[string]any) {
			c["email"] = "system:serviceaccount:kube-system:default"
		}},
		// One normalization away: a space in front, or another case.
		{"a group system:masters behind a space", "", func(c map[string]any) {
			c["groups"] = []any{" system:masters"}
		}},
		{"a group SYSTEM:masters", "", func(c map[string]any) { c["groups"] = []any{"SYSTEM:masters"} }},
		{"a person named System:admin behind a space", "oidc:", func(c map[string]any) { c["email"] = " System:admin" }},
	}
	for i, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := newFakeIdP(t, "/dex")
			a := ssoAuth(t, f, func(c *Config) { c.OIDC.GroupsPrefix = row.prefix })
			ready(t, a)
			s := start(t, a, "/")
			claims := f.claimsFor(s.nonce, time.Now())
			row.edit(claims)
			code := "code-r" + string(rune('a'+i))
			f.grant(code, grant{challenge: s.challenge, claims: claims})
			assertRefused(t, a, finish(t, a, s, code, s.state, true), ssoUnnamed)
		})
	}

	// A prefix makes the same group an ordinary name, which is what it is for.
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, nil) // prefix oidc:
	ready(t, a)
	cookie := signInAs(t, a, f, "code-prefixed", func(c map[string]any) { c["groups"] = []any{"system:masters"} })
	if session, ok := currentWith(a, cookie); !ok || len(session.Groups) != 1 ||
		session.Groups[0] != "oidc:system:masters" {
		t.Errorf("with the prefix oidc: the group reads %v, want [oidc:system:masters]", session.Groups)
	}

	// And a prefix that is itself reserved is a configuration that could only
	// ever assert Kubernetes' groups.
	if _, err := New(Config{SessionKey: []byte("a-thirty-two-byte-key-for-tests!"), OIDC: &OIDCConfig{
		Issuer: "https://idp.example", ClientID: "c", RedirectURL: "https://console.example" + CallbackPath,
		GroupsPrefix: "system:"}}); err == nil {
		t.Error("New accepted the groups prefix system:")
	}
}

// --- The name the console asserts is the prefixed one ------------------------

// TestTheUsernamePrefixFollowsKubeApiserversRule: the default, the spelling of
// "none" and an explicit value, each read back from the session a real sign-in
// opened - which is the string the review and every write line will carry.
func TestTheUsernamePrefixFollowsKubeApiserversRule(t *testing.T) {
	const sub = "CiQwOGE4Njg0Yi1kYjg4LTRiNzMtOTBhOS0zY2QxNjYxZjU0NjYSBWxvY2Fs" // claimsFor's
	for i, row := range []struct {
		name   string
		claim  string
		prefix string
		want   func(issuer string) string
	}{
		{"email with the default stays bare", "", "", func(string) string { return "kilgore@kilgore.trout" }},
		{"sub with the default is behind the issuer", "sub", "",
			func(issuer string) string { return issuer + "#" + sub }},
		{"sub with - has no prefix", "sub", NoUsernamePrefix, func(string) string { return sub }},
		{"email with an explicit prefix carries it", "email", "corp:",
			func(string) string { return "corp:kilgore@kilgore.trout" }},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newFakeIdP(t, "/dex")
			a := ssoAuth(t, f, func(c *Config) {
				c.OIDC.UsernameClaim = row.claim
				c.OIDC.UsernamePrefix = row.prefix
			})
			ready(t, a)
			cookie := signInAs(t, a, f, fmt.Sprintf("code-p%d", i), nil)
			session, ok := currentWith(a, cookie)
			if want := row.want(f.issuer); !ok || session.Identity != want {
				t.Errorf("the session is %q (ok %v), want %q", session.Identity, ok, want)
			}
		})
	}
}

// TestThePrefixedNameIsWhatIsReviewedAndWhatTheWriteLineSays is the first
// check on a prefix, end to end: a real sign-in with a prefix, through the
// guard, to the access review and to the `who=` a write line ends with. Both
// must be `<prefix><claim>` - a review naming one string and a log naming
// another would be an audit record about somebody the API server was never
// asked about.
func TestThePrefixedNameIsWhatIsReviewedAndWhatTheWriteLineSays(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, func(c *Config) { c.OIDC.UsernamePrefix = "corp:" })
	ready(t, a)
	cookie := signInAs(t, a, f, "code-review", nil)
	const want = "corp:kilgore@kilgore.trout"

	var ctx context.Context
	guarded := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ctx = r.Context() }))
	req := httptest.NewRequest(http.MethodGet, consoleOrigin+"/api/clusters", nil)
	req.AddCookie(cookie)
	guarded.ServeHTTP(httptest.NewRecorder(), req)
	if ctx == nil {
		t.Fatal("the guard did not let a signed-in request through")
	}

	reviews := &accesstest.Reviews{}
	gate := access.New(access.Options{Reviews: reviews.For, InCluster: true})
	gate.Review(ctx, "prod", access.Attributes{Verb: "delete", Version: "v1", Resource: "pods",
		Namespace: "shop", Name: "api-1"})
	asked := reviews.Asked()
	if len(asked) != 1 || asked[0].Self || asked[0].User != want {
		t.Errorf("the review asked %+v, want one SubjectAccessReview for %q", asked, want)
	}
	if got, line := identity.Fields(ctx), fmt.Sprintf("who=%q via=sso", want); got != line {
		t.Errorf("the write line ends %q, want %q", got, line)
	}
}

// TestANameIsCheckedAfterItsPrefix is the second check: a name with a
// control or invisible character, or one that is Kubernetes' own once
// prefixed, is refused at sign-in and opens no session. And the same claim a
// prefix makes harmless is accepted, which is what the prefix is for.
func TestANameIsCheckedAfterItsPrefix(t *testing.T) {
	rows := []struct {
		name   string
		claim  string
		prefix string
		edit   func(c map[string]any)
	}{
		{"a name with a newline", "", "", func(c map[string]any) {
			c["email"] = "kilgore@kilgore.trout\n[auth] signed in with single sign-on: \"root\""
		}},
		{"a name with a NUL", "", "", func(c map[string]any) { c["email"] = "kilgore\x00@kilgore.trout" }},
		{"a name with a right-to-left override", "", "", func(c map[string]any) {
			c["email"] = "\u202enimda@kilgore.trout"
		}},
		{"a name with a zero-width space", "", "", func(c map[string]any) { c["email"] = "admin\u200b@corp.example" }},
		{"a name with a line separator", "", "", func(c map[string]any) { c["email"] = "a\u2028b@corp.example" }},
		{"a group with a newline", "", "", func(c map[string]any) { c["groups"] = []any{"authors\nadmins"} }},
		// The prefix and the claim together are Kubernetes' own, neither alone:
		// the check has to be on what is asserted.
		{"a name that is system: once prefixed", "sub", "sys", func(c map[string]any) { c["sub"] = "tem:masters" }},
		{"a sub that is system: with no prefix", "sub", NoUsernamePrefix, func(c map[string]any) {
			c["sub"] = "system:kube-controller-manager"
		}},
	}
	for i, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := newFakeIdP(t, "/dex")
			a := ssoAuth(t, f, func(c *Config) {
				c.OIDC.UsernameClaim = row.claim
				c.OIDC.UsernamePrefix = row.prefix
			})
			ready(t, a)
			s := start(t, a, "/")
			claims := f.claimsFor(s.nonce, time.Now())
			row.edit(claims)
			code := fmt.Sprintf("code-n%d", i)
			f.grant(code, grant{challenge: s.challenge, claims: claims})
			assertRefused(t, a, finish(t, a, s, code, s.state, true), ssoUnnamed)
		})
	}

	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, func(c *Config) { c.OIDC.UsernamePrefix = "corp:" })
	ready(t, a)
	cookie := signInAs(t, a, f, "code-harmless", func(c map[string]any) { c["email"] = "system:admin" })
	if session, ok := currentWith(a, cookie); !ok || session.Identity != "corp:system:admin" {
		t.Errorf("with the prefix corp: the name reads %q, want corp:system:admin", session.Identity)
	}
}

// TestAnEmailNobodyVouchedForIsAcceptedAndSaidOnce: a token naming its person
// by an email with no `email_verified` claim at all is accepted, as
// kube-apiserver accepts it, and the log says what that trusts - once, because
// it is the provider's property and not one sign-in's.
func TestAnEmailNobodyVouchedForIsAcceptedAndSaidOnce(t *testing.T) {
	var out strings.Builder
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, nil)
	ready(t, a)
	unvouched := func(c map[string]any) { delete(c, "email_verified") }
	signInAs(t, a, f, "code-u1", unvouched)
	signInAs(t, a, f, "code-u2", unvouched)
	if n := strings.Count(out.String(), "carried no email_verified claim"); n != 1 {
		t.Errorf("the warning was logged %d times over two sign-ins, want once:\n%s", n, out.String())
	}
}

// TestASessionFromAnotherMappingIsNoSession: the session store outlives a
// rollout. A person signed in before the prefix or the claim changed holds a
// record naming them as the old configuration did, and until it is refused
// they are reviewed under that name for the rest of the cap - twelve hours.
// So a record is live only under the mapping that wrote it, and a record from
// before the field existed (no mapping) is not live under any.
func TestASessionFromAnotherMappingIsNoSession(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	memory := sharedstate.NewMemory(time.Now)
	sharing := func(edit func(*OIDCConfig)) func(*Config) {
		return func(c *Config) {
			c.Store = memory
			if edit != nil {
				edit(c.OIDC)
			}
		}
	}
	before := ssoAuth(t, f, sharing(nil))
	ready(t, before)
	cookie := signInAs(t, before, f, "code-m1", nil)

	// The control: the same configuration in another process - a second
	// replica, or the same one restarted - still honors it.
	if got, ok := currentWith(ssoAuth(t, f, sharing(nil)), cookie); !ok || got.Identity != "kilgore@kilgore.trout" {
		t.Fatalf("an unchanged configuration did not honor its own session: %+v (ok %v)", got, ok)
	}

	for name, edit := range map[string]func(*OIDCConfig){
		"a username prefix set":  func(c *OIDCConfig) { c.UsernamePrefix = "corp:" },
		"the claim moved to sub": func(c *OIDCConfig) { c.UsernameClaim = "sub" },
		"another groups prefix":  func(c *OIDCConfig) { c.GroupsPrefix = "idp:" },
		"another groups claim":   func(c *OIDCConfig) { c.GroupsClaim = "roles" },
		"another issuer":         func(c *OIDCConfig) { c.Issuer = "https://elsewhere.example/dex" },
	} {
		if got, ok := currentWith(ssoAuth(t, f, sharing(edit)), cookie); ok {
			t.Errorf("%s: a session signed in under the old configuration is still %q", name, got.Identity)
		}
	}

	// A record written before the mapping existed, as a shared store still
	// holds after a rollout.
	c, _ := before.parseToken(cookie.Value)
	raw, ok, err := memory.Get(context.Background(), sessionPrefix+c.sid)
	if err != nil || !ok {
		t.Fatalf("the record is not in the store: ok %v, %v", ok, err)
	}
	var rec storedSession
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Mapping == "" {
		t.Fatal("a sign-in wrote a record with no mapping")
	}
	rec.Mapping = ""
	old, _ := json.Marshal(rec)
	if err := memory.Put(context.Background(), sessionPrefix+c.sid, old, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, ok := currentWith(before, cookie); ok {
		t.Errorf("a record with no mapping, from before this check, is still %q", got.Identity)
	}
}

// TestARefusedNameIsLoggedByItsClaim: an issuer prefix can be as long as the
// clip, and a refusal that clipped the joined string would log the issuer and
// none of the person.
func TestARefusedNameIsLoggedByItsClaim(t *testing.T) {
	var out strings.Builder
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	f := newFakeIdP(t, "/dex")
	long := "https://login.microsoftonline.com/0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0/v2.0#"
	a := ssoAuth(t, f, func(c *Config) { c.OIDC.UsernamePrefix = long })
	ready(t, a)
	s := start(t, a, "/")
	claims := f.claimsFor(s.nonce, time.Now())
	claims["email"] = "mallory\u200b@corp.example"
	f.grant("code-long", grant{challenge: s.challenge, claims: claims})
	assertRefused(t, a, finish(t, a, s, "code-long", s.state, true), ssoUnnamed)
	if !strings.Contains(out.String(), "\"mallory\\u200b@corp.example\"") {
		t.Errorf("the refusal does not name the claim:\n%s", out.String())
	}
}

// TestTheStartUpLineNamesThePrefixInForce: the line is the one place an
// operator writing a RoleBinding learns the string to write, so it names the
// prefix as resolved - the issuer, under the default and a claim that is not
// `email` - and says nothing with no provider.
func TestTheStartUpLineNamesThePrefixInForce(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, func(c *Config) { c.OIDC.UsernameClaim = "sub" })
	if got, want := a.NamePolicy(), fmt.Sprintf("%q followed by their \"sub\" claim", f.issuer+"#"); !strings.Contains(got, want) {
		t.Errorf("the start-up line reads %q, want it to say %s", got, want)
	}
	a = ssoAuth(t, f, nil)
	if got := a.NamePolicy(); !strings.Contains(got, "by their \"email\" claim, unprefixed") {
		t.Errorf("the start-up line reads %q, want the email claim, unprefixed", got)
	}
	password, err := New(Config{User: "admin", PasswordHash: testHash(t), SessionKey: []byte("a-thirty-two-byte-key-for-tests!")})
	if err != nil {
		t.Fatal(err)
	}
	if got := password.NamePolicy(); got != "" {
		t.Errorf("with no provider the start-up line reads %q, want nothing", got)
	}
}

// TestWhatANameMayHold is unprintable's own table: what a real name carries is
// accepted, what draws as nothing is refused.
func TestWhatANameMayHold(t *testing.T) {
	for _, s := range []string{
		"kilgore@kilgore.trout",
		"Kilgore Trout",
		"\u0645\u06cc\u200c\u062e\u0648\u0627\u0647\u0645", // Persian: a non-joiner between two Arabic letters
		"\u0d28\u0d4d\u200d",       // Malayalam: a joiner after a virama
		"\u05d3\u05e0\u05d4\u200f", // Hebrew with a right-to-left mark
		"Ma\u00adria",              // a soft hyphen
		"\u200eauthors",            // a group with a stray left-to-right mark
	} {
		if unprintable(s) {
			t.Errorf("%q was refused, and a real name carries it", s)
		}
	}
	for _, s := range []string{
		"admin\u200d@corp.example", // a joiner after a Latin letter draws as nothing
		"\u200cadmin",              // a non-joiner with nothing before it
		"admin\u200b",              // a zero-width space
		"\u202eadmin",              // a right-to-left override
		"\u3164admin",              // a Hangul filler
		"ad\u115fmin",              // the other one
		"ad\u034fmin",              // a combining grapheme joiner
		"admin\ufe0f",              // a variation selector
		"admin\u2029",              // a paragraph separator
		"admin\x7f",                // DEL
		"admin\xff",                // not UTF-8
	} {
		if !unprintable(s) {
			t.Errorf("%q was accepted, and it holds something that draws as nothing", s)
		}
	}
}

// --- Where the browser goes, and what its cookies say ---------------------------

// TestTheReturnAddressIsAlwaysThisConsole: `next` is chosen by whoever wrote
// the link, and after a sign-in the browser goes there with a fresh session.
// Every spelling of "another site" comes back as "/".
func TestTheReturnAddressIsAlwaysThisConsole(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, nil)
	ready(t, a)
	for i, next := range []string{"//evil.example", "/\\evil.example", "https://evil.example/", "evil.example",
		"/ok\n//evil.example", "/" + strings.Repeat("a", maxNextBytes+1)} {
		code := "code-n" + string(rune('a'+i))
		s := start(t, a, next)
		f.grant(code, grant{challenge: s.challenge, claims: f.claimsFor(s.nonce, time.Now())})
		rec := finish(t, a, s, code, s.state, true)
		if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || loc != "/" {
			t.Errorf("next=%.40q: the sign-in returned %d to %q, want 303 to /", next, rec.Code, loc)
		}
	}
}

// TestCookiesAreSecureWhereTheBrowserArrivesOverTLS: the redirect URL is the one
// address this console knows the browser uses. Behind https both cookies say
// Secure; on a plain-http address neither can.
//
// **And behind https the flow cookie is `__Host-`-prefixed, Path=/, no Domain**,
// which is what stops a cookie planted from a sibling host or over plain http
// from passing for this browser's own flow (login CSRF by cookie planting). A
// browser refuses a `__Host-` cookie with any other attributes, so the test
// asserts every one of them - and that the callback clears the same cookie.
func TestCookiesAreSecureWhereTheBrowserArrivesOverTLS(t *testing.T) {
	for _, origin := range []string{"https://console.test", "http://console.test"} {
		want := strings.HasPrefix(origin, "https")
		wantName, wantPath := flowCookieName, "/api/auth/oidc/"
		if want {
			wantName, wantPath = "__Host-kubetower_sso", "/"
		}
		f := newFakeIdP(t, "/dex")
		a := ssoAuth(t, f, func(c *Config) { c.OIDC.RedirectURL = origin + CallbackPath })
		ready(t, a)

		rec := httptest.NewRecorder()
		a.StartSSO(rec, httptest.NewRequest(http.MethodGet, origin+StartPath+"?next=%2F", nil))
		var flowCookie *http.Cookie
		for _, c := range rec.Result().Cookies() {
			if c.Name == wantName {
				flowCookie = c
			}
		}
		if flowCookie == nil {
			t.Fatalf("%s: start set no flow cookie named %s (%d, cookies %v)", origin, wantName, rec.Code,
				rec.Header().Values("Set-Cookie"))
		}
		if flowCookie.Secure != want {
			t.Errorf("%s: the flow cookie's Secure is %v, want %v", origin, flowCookie.Secure, want)
		}
		if flowCookie.Path != wantPath || flowCookie.Domain != "" || !flowCookie.HttpOnly ||
			flowCookie.SameSite != http.SameSiteLaxMode {
			t.Errorf("%s: the flow cookie is Path=%q Domain=%q HttpOnly=%v SameSite=%v, want Path=%q, no Domain, "+
				"HttpOnly, Lax", origin, flowCookie.Path, flowCookie.Domain, flowCookie.HttpOnly, flowCookie.SameSite,
				wantPath)
		}
		loc, _ := url.Parse(rec.Header().Get("Location"))
		q := loc.Query()
		f.grant("code-s", grant{challenge: q.Get("code_challenge"), claims: f.claimsFor(q.Get("nonce"), time.Now())})
		back := httptest.NewRequest(http.MethodGet, origin+CallbackPath+"?"+url.Values{"code": {"code-s"},
			"state": {q.Get("state")}}.Encode(), nil)
		back.AddCookie(&http.Cookie{Name: wantName, Value: flowCookie.Value})
		done := httptest.NewRecorder()
		a.FinishSSO(done, back)
		cleared := false
		for _, c := range done.Result().Cookies() {
			if c.Name == wantName && c.Path == wantPath && c.MaxAge < 0 {
				cleared = true
			}
		}
		if !cleared {
			t.Errorf("%s: the callback did not clear %s at Path=%s; a browser keeps a cookie whose clearing "+
				"names another path", origin, wantName, wantPath)
		}
		session := sessionFrom(done, "kubetower_session")
		if session == nil {
			t.Fatalf("%s: the sign-in set no session cookie (%d %s)", origin, done.Code, done.Header().Get("Location"))
		}
		if session.Secure != want {
			t.Errorf("%s: the session cookie's Secure is %v, want %v", origin, session.Secure, want)
		}
	}
}

// TestAFrontDoorPrintsNoKey: a test failure or a log line that formats an
// *Auth must not carry the session key into CI output, in any of the three
// spellings fmt has for a struct.
func TestAFrontDoorPrintsNoKey(t *testing.T) {
	f := newFakeIdP(t, "/dex")
	a := ssoAuth(t, f, nil)
	key := "a-thirty-two-byte-key-for-tests!"
	// The key as a string, as %v prints bytes, and as %#v does.
	spellings := []string{key, strings.Trim(fmt.Sprint([]byte(key)), "[]"),
		strings.TrimSuffix(strings.TrimPrefix(fmt.Sprintf("%#v", []byte(key)), "[]byte{"), "}")}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		out := fmt.Sprintf(verb, a)
		leaked := false
		for _, k := range spellings {
			leaked = leaked || strings.Contains(out, k)
		}
		if leaked {
			t.Errorf("fmt.Sprintf(%q, auth) prints the session key: %.80s...", verb, out)
		}
	}
}

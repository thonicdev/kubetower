package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/thonicdev/kubetower/internal/api"
)

// Single sign-on: the OpenID Connect authorization-code flow, server-side.
//
// It answers who the request is from, never what they may do: that is the
// cluster's RBAC, asked through a SubjectAccessReview. Nothing here maps a
// group to a permission.
//
// It is still the part to fear (docs/SERVER-MODE-THREAT-MODEL.md §4.2): the
// console tells the API server "alice, in these groups, is asking", and a bug
// in token verification, in the name claim or in the groups claim does not
// produce a denied request but an allowed one about the wrong person. So
// every claim that matters is verified here, none taken from a header an
// ingress might set:
//
//   - the signature, against the provider's JWKS (go-oidc's RemoteKeySet,
//     which refetches the set when a token names an unseen key);
//   - `iss`, equal to the configured issuer to the byte;
//   - `aud`, containing this console's client id;
//   - `exp`, and `nbf` where present;
//   - the `nonce` this console issued for this flow, in constant time;
//   - `state`, bound to this browser by the sealed, HttpOnly flow cookie that
//     also carries the nonce and the PKCE verifier (see flow);
//   - PKCE (S256), so an intercepted code cannot be exchanged by anybody but
//     the one who asked for it.
//
// Verifying a JWS against a rotating key set is not written by hand:
// go-oidc does it (kube-apiserver's own OIDC authenticator is built on it),
// and golang.org/x/oauth2 does the exchange and PKCE.
//
// The issuer is the trap: `iss` must be the exact expected string, while the
// browser and the pod may reach the provider at different addresses. See
// OIDCConfig.DiscoveryURL.

// OIDCConfig is one identity provider.
type OIDCConfig struct {
	// Issuer is the provider's identity, and the string every token's `iss`
	// must equal.
	Issuer string

	// DiscoveryURL is where this process fetches the discovery document from,
	// when that is not the issuer (the pod reaching the provider by a
	// cluster-internal name while browsers use a public one). Empty means the
	// issuer.
	//
	// The document must still name the configured issuer; one naming another
	// is refused, a check made here because go-oidc's InsecureIssuerURLContext
	// would accept it. The back-channel endpoints (token, JWKS, userinfo) are
	// rewritten from the issuer's prefix to this one, since the pod cannot
	// reach the issuer's address; the authorization endpoint, which the browser
	// follows, is left alone. An endpoint outside the issuer's prefix is left
	// alone too, and must be reachable from the pod.
	DiscoveryURL string

	ClientID string
	// ClientSecret is the client's credential: never logged, never
	// serialized. It may be empty for a public client, where PKCE protects
	// the exchange.
	ClientSecret string `json:"-"`

	// RedirectURL is this console's callback as the browser reaches it, ending
	// in CallbackPath, registered at the provider byte for byte. Not derived
	// from a request: behind an ingress the Host header is the client's, and a
	// redirect URI built from it is an open redirect with a provider's
	// signature on it.
	RedirectURL string

	// Scopes requested. Empty means `openid email profile`. A provider may
	// withhold the groups claim without a `groups` scope (Dex does); the log
	// says so the first time a token arrives without it.
	Scopes []string

	// UsernameClaim names the person. Empty means `email`. Where it is
	// `email`, a token whose `email_verified` is false is refused, as
	// kube-apiserver does; one with no `email_verified` at all is accepted, as
	// kube-apiserver accepts it, and logged once.
	//
	// The claim is the person's rights, not a label: the access review names
	// it, so it must be a claim the person cannot edit at the provider. `sub`
	// and a verified `email` are safe; `preferred_username`, `name`, `upn` and
	// a user-settable email are not (the Azure AD "nOAuth" shape).
	UsernameClaim string

	// UsernamePrefix is prepended to the name before the console reviews, logs
	// or stores it: kube-apiserver's `--oidc-username-prefix`, with the same
	// default and the same spelling of "none":
	//
	//   - empty: the issuer and `#` (`https://dex.example/dex#CiQw...`), except
	//     where the claim is `email`, which is left bare;
	//   - `-`: no prefix, typed out deliberately;
	//   - anything else: that string.
	//
	// Without a prefix, the provider's `alice` is a client certificate's
	// `alice`, and a RoleBinding for one grants the other. The apiserver's
	// default, so one RoleBinding means the same person to `kubectl` and to the
	// console (an email's domain already says whose it is). A cluster using
	// structured authentication has no default prefix; set this to its
	// `claimMappings.username.prefix`. `-` rather than empty for "none",
	// because empty is what not setting it gives.
	UsernamePrefix string

	// GroupsClaim carries the groups. Empty means `groups`. Per provider: Dex
	// and Okta say `groups`, Keycloak commonly something else, and a hard-coded
	// name would make everyone on another provider groupless.
	GroupsClaim string

	// GroupsPrefix is prepended to every group before the console asserts it,
	// kube-apiserver's `--oidc-groups-prefix`: a transformation of a name, not
	// of a permission, so acceptable as a deployment value.
	GroupsPrefix string

	// SessionMax caps a single sign-on session, counted from the sign-in.
	// Zero means DefaultSSOSessionMax, and the password session's own cap
	// applies as well where it is shorter.
	SessionMax time.Duration

	// HTTPClient is used for discovery, the key set and the token exchange.
	// Nil means one with httpTimeout.
	HTTPClient *http.Client
}

const (
	// CallbackPath and StartPath are the two single sign-on routes, which
	// internal/api registers. Constants because the redirect URI is validated
	// against this path at start-up.
	StartPath    = api.Prefix + "auth/oidc/start"
	CallbackPath = api.Prefix + "auth/oidc/callback"

	// DefaultSSOSessionMax bounds a single sign-on session however much it is
	// used. It is also how stale a person's groups may get: a group removed at
	// the provider keeps working until the session ends. Twelve hours is a
	// working day; longer is a longer revocation delay
	// (docs/SERVER-MODE-THREAT-MODEL.md §8.3).
	DefaultSSOSessionMax = 12 * time.Hour

	defaultUsernameClaim = "email"
	defaultGroupsClaim   = "groups"

	// NoUsernamePrefix is the UsernamePrefix that means "none", as
	// kube-apiserver spells it.
	NoUsernamePrefix = "-"

	// flowCookieName carries a sign-in in progress, bound to the browser that
	// started it, sealed (see flow). Without it a callback carrying somebody
	// else's code and state would sign this browser in as them: login CSRF.
	// This is the name over plain http; behind TLS it is hostFlowCookieName.
	flowCookieName = "kubetower_sso"

	// hostFlowCookieName is the flow cookie behind TLS. The `__Host-` prefix
	// keeps the binding above against a planted cookie: someone able to set a
	// cookie for this host (a sibling subdomain, plain http on the same name)
	// could plant their own sealed flow and send the victim to the callback
	// with their own code, signing the victim in as the attacker. A browser
	// accepts `__Host-` only from a secure origin, with Path=/ and no Domain.
	// Plain http cannot have it, as the threat model says.
	hostFlowCookieName = "__Host-kubetower_sso"

	// flowLifetime is how long a sign-in may take between the click and the
	// return; longer is an unused state waiting to be replayed.
	flowLifetime = 10 * time.Minute

	// maxNextBytes bounds the `next` a flow carries back: a browser silently
	// drops a cookie past about 4 KiB, which would read as an expired sign-in.
	// A longer one is sent to "/".
	maxNextBytes = 1024

	// httpTimeout is per request to the provider.
	httpTimeout = 10 * time.Second

	// discoveryFirstRetry and discoveryMaxRetry bound the background
	// discovery's backoff; a click on "Sign in with SSO" retries at once.
	discoveryFirstRetry = time.Second
	discoveryMaxRetry   = 30 * time.Second
)

// The codes a failed sign-in is sent back to the sign-in page with: a fixed
// vocabulary, never the provider's words, so nothing a provider or a forged
// callback writes into a query string reaches a page this console serves.
const (
	ssoUnavailable = "unavailable"
	ssoRefused     = "refused"
	ssoExpired     = "expired"
	ssoFailed      = "failed"
	ssoUnnamed     = "unnamed"
	// ssoStore is the console's own session store not answering, its own
	// code so the operator does not look at the provider.
	ssoStore = "store"
)

// ssoSentences is what the sign-in pages say for each code.
var ssoSentences = map[string]string{
	ssoUnavailable: "The identity provider could not be reached. The console keeps trying, so try again in a moment.",
	ssoRefused:     "The identity provider did not sign you in.",
	ssoExpired:     "That sign-in took too long, or was started in another window. Start it again.",
	ssoFailed:      "The identity provider's answer could not be verified, so nobody was signed in.",
	ssoUnnamed:     "The identity provider signed you in, but its token does not say who you are in a way this console accepts.",
	ssoStore:       "The console could not reach its session store, so nobody was signed in. Try again in a moment.",
}

// signingAlgorithms is every JWS algorithm a token may be signed with. A
// discovery document's list is intersected with this, so a provider that
// advertises `none` cannot widen it.
var signingAlgorithms = map[string]bool{
	oidc.RS256: true, oidc.RS384: true, oidc.RS512: true,
	oidc.ES256: true, oidc.ES384: true, oidc.ES512: true,
	oidc.PS256: true, oidc.PS384: true, oidc.PS512: true,
	oidc.EdDSA: true,
}

// provider is one identity provider and everything this process knows about it.
type provider struct {
	cfg    OIDCConfig
	client *http.Client
	now    func() time.Time
	secure bool

	mu      sync.RWMutex
	ready   *discovered
	lastErr string
	// lastPublic is lastErr as the sign-in page may say it: never the
	// address or what the provider wrote.
	lastPublic string
	tries      int
	retryAt    time.Time
	// warnedNoGroups is set the first time a token lacks the groups claim,
	// so the log says so once.
	warnedNoGroups bool
	// warnedUnverified is the same, for an `email` name with no
	// `email_verified` claim at all.
	warnedUnverified bool
	// mapping fingerprints how this configuration turns a token into a name
	// and groups (mappingOf). Every record it writes carries it, and a record
	// carrying another is no session (Auth.live).
	mapping string

	poke chan struct{}

	// seal encrypts and authenticates the flow cookie under a key derived from
	// the session key, so any replica can finish a sign-in another started,
	// and nothing else can.
	seal cipher.AEAD
}

// discovered is what a successful discovery produced.
type discovered struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// flow is one sign-in between the click and the return.
//
// The start route is public, so a bounded server-side table of flows would
// let anybody open flows in a loop and evict every sign-in in progress. So
// the flow is the cookie, sealed with AES-256-GCM under a key derived from
// the session key (newFlowSeal): the browser holds the PKCE verifier and the
// nonce without reading them, a cookie this process did not seal opens
// nothing, and a flood of starts holds nothing on the server.
//
// Single use is the console's own, not the provider's: a provider that
// redeems a code twice (RFC 6749 §4.1.2 says it must not) would otherwise let
// a copied cookie and callback URL sign in again. After the code exchange and
// ID token verification, before a session is opened, the callback records a
// hash of `state` set-if-absent until the flow would expire
// (sessionStore.once); a second callback with that state is refused. Only
// after the exchange, so the public start route writes nothing to the store.
// TestAForgedOrReplayedCallbackIsRefused holds both.
type flow struct {
	State    string    `json:"state"`
	Nonce    string    `json:"nonce"`
	Verifier string    `json:"verifier"`
	Next     string    `json:"next"`
	Expires  time.Time `json:"expires"`
}

// flowSealLabel separates the flow cookie's key from the session cookie's
// HMAC key (the same secret), and is the additional data of every seal, so a
// value sealed as one can never be read as the other.
const flowSealLabel = "kubetower sso flow cookie v1"

// newFlowSeal derives the flow cookie's AEAD from the session key.
func newFlowSeal(sessionKey []byte) (cipher.AEAD, error) {
	mac := hmac.New(sha256.New, sessionKey)
	mac.Write([]byte(flowSealLabel))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealFlow writes a flow as a cookie value: base64url(nonce || ciphertext).
func (p *provider) sealFlow(f flow) (string, error) {
	plain, err := json.Marshal(f)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, p.seal.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(p.seal.Seal(nonce, nonce, plain, []byte(flowSealLabel))), nil
}

// openFlow reads a cookie value back. Anything but a value this process
// sealed, unmodified, is no flow, and is not read further. The expiry is
// checked on this pod's clock against the instant sealed in at the start.
func (p *provider) openFlow(value string) (flow, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) < p.seal.NonceSize() {
		return flow{}, false
	}
	size := p.seal.NonceSize()
	plain, err := p.seal.Open(nil, raw[:size], raw[size:], []byte(flowSealLabel))
	if err != nil {
		return flow{}, false
	}
	var f flow
	if err := json.Unmarshal(plain, &f); err != nil || f.State == "" || !p.now().Before(f.Expires) {
		return flow{}, false
	}
	return f, true
}

// flowCookie is the flow cookie's name and path. Behind TLS it is
// `__Host-`-prefixed, which the browser only accepts with Path=/ and no Domain
// (hostFlowCookieName says why that matters); over plain http it keeps the plain
// name, scoped to the two single sign-on routes.
func (p *provider) flowCookie() (name, path string) {
	if p.secure {
		return hostFlowCookieName, "/"
	}
	return flowCookieName, api.Prefix + "auth/oidc/"
}

// validateOIDC refuses at start-up a configuration that could never work. An
// unreachable provider is a state the console runs in, not this; a missing
// client id would be a console failing every sign-in forever.
func validateOIDC(cfg *OIDCConfig) error {
	issuer, err := url.Parse(cfg.Issuer)
	if err != nil || issuer.Scheme == "" || issuer.Host == "" {
		return fmt.Errorf("auth: the OIDC issuer %q is not an absolute URL", cfg.Issuer)
	}
	if cfg.DiscoveryURL != "" {
		d, err := url.Parse(cfg.DiscoveryURL)
		if err != nil || d.Scheme == "" || d.Host == "" {
			return fmt.Errorf("auth: the OIDC discovery URL %q is not an absolute URL", cfg.DiscoveryURL)
		}
	}
	if cfg.ClientID == "" {
		return errors.New("auth: an OIDC issuer is configured and no client id")
	}
	redirect, err := url.Parse(cfg.RedirectURL)
	if err != nil || redirect.Scheme == "" || redirect.Host == "" {
		return fmt.Errorf("auth: the OIDC redirect URL %q is not an absolute URL; it has to be this "+
			"console's address as the browser reaches it, ending in %s", cfg.RedirectURL, CallbackPath)
	}
	if redirect.Path != CallbackPath || redirect.RawQuery != "" {
		return fmt.Errorf("auth: the OIDC redirect URL %q must end in %s and carry no query, "+
			"because that is the one address this console answers a provider on", cfg.RedirectURL, CallbackPath)
	}
	if reserved(cfg.GroupsPrefix) {
		return fmt.Errorf("auth: the OIDC groups prefix %q starts with %q, which would make every group the "+
			"provider sends one of Kubernetes' own", cfg.GroupsPrefix, reservedPrefix)
	}
	// The prefix as applied, default included.
	if prefix := cfg.NamePrefix(); reserved(prefix) {
		return fmt.Errorf("auth: the OIDC username prefix %q starts with %q, which would make every person "+
			"the provider signs in one of Kubernetes' own users", prefix, reservedPrefix)
	}
	// Refused here as well as at sign-in: a prefix with a stray newline (a
	// value pasted into a Secret) would otherwise start a console that
	// refuses every person.
	for _, p := range []struct{ which, value string }{
		{"username", cfg.NamePrefix()}, {"groups", cfg.GroupsPrefix},
	} {
		if unprintable(p.value) {
			return fmt.Errorf("auth: the OIDC %s prefix %q carries a control or invisible character", p.which, p.value)
		}
	}
	return nil
}

// NamePrefix is the UsernamePrefix the console applies, default resolved: see
// UsernamePrefix for the rule and why it is kube-apiserver's.
func (c *OIDCConfig) NamePrefix() string {
	switch c.UsernamePrefix {
	case NoUsernamePrefix:
		return ""
	case "":
		claim := c.UsernameClaim
		if claim == "" {
			claim = defaultUsernameClaim
		}
		if claim == "email" {
			return ""
		}
		return c.Issuer + "#"
	}
	return c.UsernamePrefix
}

// unprintable reports a string that is not UTF-8, or that holds a character
// which draws as nothing or breaks a line:
//
//   - a control character (Cc);
//   - a format character (Cf) - the bidirectional overrides that make a log
//     viewer draw `eve` where `admin` was written, a zero-width space that
//     makes two names look like one - with the four exceptions below;
//   - a default-ignorable code point that is not Cf - the Hangul fillers
//     U+115F and U+3164, the combining grapheme joiner U+034F, the variation
//     selectors (U+FE0F among them, so an emoji's presentation selector is
//     refused too);
//   - a Unicode line or paragraph separator, which a log viewer may honor as
//     a line break.
//
// Allowed, because real names carry them: the soft hyphen U+00AD and the
// direction marks U+200E and U+200F (Hebrew and Arabic insert them, and they
// reorder nothing as the overrides do), and the zero-width non-joiner and
// joiner U+200C and U+200D inside a word of a script that shapes with them
// (IDNA's CONTEXTJ, approximated by the script of the preceding character).
// Anywhere else they draw as nothing, and are refused.
//
// It does not catch spaces of any kind, the braille blank U+2800 or
// homoglyphs (`admin` with a Cyrillic `a`). "Looks like somebody else" is
// settled by the prefix, which keeps the provider's names apart, and by a
// claim the person cannot edit (UsernameClaim).
//
// Refused rather than escaped: the log escapes what it prints, but the API
// server is reviewed on the string as it is, and a RoleBinding would have to
// spell it invisibly too. Nobody is legitimately given an unreadable name.
func unprintable(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	var before rune // the last character that was not a combining mark
	for _, r := range s {
		switch {
		case r == '\u00ad' || r == '\u200e' || r == '\u200f':
		case r == '\u200c' || r == '\u200d':
			if !unicode.In(before, joiningScripts...) {
				return true
			}
		case unicode.IsControl(r),
			unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Other_Default_Ignorable_Code_Point,
				unicode.Variation_Selector):
			return true
		}
		// A virama or an Arabic vowel sign marks the letter before it: a
		// joiner after it joins that letter's script.
		if !unicode.In(r, unicode.Mn, unicode.Mc) || before == 0 {
			before = r
		}
	}
	return false
}

// joiningScripts are the scripts whose shaping the zero-width joiner and
// non-joiner change (Arabic-type joining, and Brahmic scripts with a
// virama). A script missing here refuses a joiner, the safe direction.
var joiningScripts = []*unicode.RangeTable{
	unicode.Arabic, unicode.Syriac, unicode.Nko, unicode.Mongolian, unicode.Mandaic, unicode.Adlam,
	unicode.Devanagari, unicode.Bengali, unicode.Gurmukhi, unicode.Gujarati, unicode.Oriya,
	unicode.Tamil, unicode.Telugu, unicode.Kannada, unicode.Malayalam, unicode.Sinhala,
	unicode.Myanmar, unicode.Khmer, unicode.Tibetan,
}

// reservedPrefix is Kubernetes' own namespace of users and groups
// (`system:masters`, `system:serviceaccount:...`). No name or group from an
// identity provider may start with it, after its prefix: an IdP group named
// `system:masters` asserted as is would be cluster-admin granted by whoever
// can edit a group at the provider. kube-apiserver does not refuse it by
// default, so the console does, before a session holds it.
const reservedPrefix = "system:"

// reserved compares trimmed and case-folded, stricter than Kubernetes:
// ` system:masters` and `System:masters` are one normalization away from
// Kubernetes' own, and nobody legitimately has either name.
func reserved(name string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), reservedPrefix)
}

func newProvider(cfg OIDCConfig, now func() time.Time, seal cipher.AEAD) *provider {
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	}
	if cfg.UsernameClaim == "" {
		cfg.UsernameClaim = defaultUsernameClaim
	}
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = defaultGroupsClaim
	}
	if cfg.SessionMax <= 0 {
		cfg.SessionMax = DefaultSSOSessionMax
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: httpTimeout}
	}
	redirect, _ := url.Parse(cfg.RedirectURL)
	return &provider{
		cfg:     cfg,
		mapping: mappingOf(cfg),
		client:  client,
		now:     now,
		// Behind TLS the cookies say so; the redirect URL is the one address
		// this console knows the browser uses.
		secure: redirect != nil && redirect.Scheme == "https",
		poke:   make(chan struct{}, 1),
		seal:   seal,
	}
}

// mappingOf fingerprints everything that decides which strings a token
// becomes: the issuer, the username claim and its applied prefix, the groups
// claim and its prefix (another issuer's `alice@corp.example` is not this
// one's). Hashed, so a record carries no configuration; length-prefixed, so
// no two configurations join into the same bytes.
func mappingOf(cfg OIDCConfig) string {
	h := sha256.New()
	for _, part := range []string{cfg.Issuer, cfg.UsernameClaim, cfg.NamePrefix(), cfg.GroupsClaim, cfg.GroupsPrefix} {
		fmt.Fprintf(h, "%d:%s;", len(part), part)
	}
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:16])
}

// --- Discovery, retried behind the caller -----------------------------------

// run discovers the provider, and keeps trying until it answers.
// An unreachable provider does not take the console down: this runs beside
// the serving process, and the sign-in page reads its state. It stops on
// success; go-oidc refetches the key set itself when a token names an
// unseen key.
func (p *provider) run(ctx context.Context) {
	delay := discoveryFirstRetry
	for {
		attempt, cancel := context.WithTimeout(ctx, httpTimeout)
		found, err := p.discover(attempt)
		cancel()

		p.mu.Lock()
		p.tries++
		if err == nil {
			p.ready = found
			p.lastErr = ""
			tries := p.tries
			p.mu.Unlock()
			log.Printf("[auth] single sign-on is ready: %s answered discovery (attempt %d)", p.cfg.Issuer, tries)
			return
		}
		changed := p.lastErr != err.Error()
		p.lastErr = err.Error()
		p.lastPublic = publicReason(err)
		p.retryAt = p.now().Add(delay)
		tries := p.tries
		p.mu.Unlock()

		// Said when the reason changes, not on every attempt: an hour's outage
		// at a thirty-second ceiling would be 120 identical lines.
		if changed {
			log.Printf("[auth] single sign-on is unavailable: %v. The console is serving; "+
				"discovery is retried in the background (attempt %d)", err, tries)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		case <-p.poke:
		}
		if delay *= 2; delay > discoveryMaxRetry {
			delay = discoveryMaxRetry
		}
	}
}

// retryNow wakes the discovery loop, for a person who has just clicked.
func (p *provider) retryNow() {
	select {
	case p.poke <- struct{}{}:
	default:
	}
}

// discover asks the provider for its configuration and builds what a flow
// needs from it.
func (p *provider) discover(ctx context.Context) (*discovered, error) {
	ctx = oidc.ClientContext(ctx, p.client)
	base := p.cfg.Issuer
	split := p.cfg.DiscoveryURL != "" && trimSlash(p.cfg.DiscoveryURL) != trimSlash(p.cfg.Issuer)
	if split {
		base = p.cfg.DiscoveryURL
		// Lets go-oidc accept a document fetched from one address naming
		// another. Its issuer check is replaced below, not dropped.
		ctx = oidc.InsecureIssuerURLContext(ctx, p.cfg.Issuer)
	}
	found, err := oidc.NewProvider(ctx, base)
	if err != nil {
		return nil, fmt.Errorf("discovery at %s: %w", base, err)
	}

	var doc struct {
		Issuer   string   `json:"issuer"`
		Auth     string   `json:"authorization_endpoint"`
		Token    string   `json:"token_endpoint"`
		JWKS     string   `json:"jwks_uri"`
		UserInfo string   `json:"userinfo_endpoint"`
		Algs     []string `json:"id_token_signing_alg_values_supported"`
	}
	if err := found.Claims(&doc); err != nil {
		return nil, fmt.Errorf("discovery at %s: the document could not be read: %w", base, err)
	}
	// The issuer equality, checked here whether or not go-oidc did: a
	// document naming another issuer is a provider whose every token would be
	// refused, or a document somebody else serves at the discovery address.
	if doc.Issuer != p.cfg.Issuer {
		return nil, &discoveryError{
			public: "published a discovery document naming another issuer than the one this console is configured for",
			err: fmt.Errorf("the discovery document at %s names the issuer %q, and this console "+
				"is configured for %q; a token from it would be refused, so nothing is signed in "+
				"against it", base, doc.Issuer, p.cfg.Issuer)}
	}
	if doc.Auth == "" || doc.Token == "" || doc.JWKS == "" {
		return nil, &discoveryError{
			public: "published a discovery document with no authorization, token or key set endpoint",
			err: fmt.Errorf("the discovery document at %s names no authorization, token or key "+
				"set endpoint", base)}
	}

	var algs []string
	for _, a := range doc.Algs {
		if signingAlgorithms[a] {
			algs = append(algs, a)
		}
	}
	if len(algs) == 0 {
		// The OIDC default when a provider says nothing.
		algs = []string{oidc.RS256}
	}

	back := func(endpoint string) string {
		if !split || !strings.HasPrefix(endpoint, trimSlash(p.cfg.Issuer)) {
			return endpoint
		}
		return trimSlash(p.cfg.DiscoveryURL) + strings.TrimPrefix(endpoint, trimSlash(p.cfg.Issuer))
	}
	config := oidc.ProviderConfig{
		IssuerURL:   p.cfg.Issuer,
		AuthURL:     doc.Auth,
		TokenURL:    back(doc.Token),
		JWKSURL:     back(doc.JWKS),
		UserInfoURL: back(doc.UserInfo),
		Algorithms:  algs,
	}
	built := config.NewProvider(ctx)

	return &discovered{
		oauth: oauth2.Config{
			ClientID:     p.cfg.ClientID,
			ClientSecret: p.cfg.ClientSecret,
			Endpoint:     built.Endpoint(),
			RedirectURL:  p.cfg.RedirectURL,
			Scopes:       p.cfg.Scopes,
		},
		verifier: built.VerifierContext(oidc.ClientContext(context.Background(), p.client), &oidc.Config{
			ClientID:             p.cfg.ClientID,
			SupportedSigningAlgs: algs,
			Now:                  p.now,
		}),
	}, nil
}

func trimSlash(s string) string { return strings.TrimSuffix(s, "/") }

// state reads what the sign-in page must say about the provider.
func (p *provider) state() api.SSO {
	p.mu.RLock()
	defer p.mu.RUnlock()
	switch {
	case p.ready != nil:
		return api.SSO{State: api.SSOReady}
	case p.tries == 0:
		return api.SSO{State: api.SSOPending,
			Detail: "The console is asking the identity provider for its configuration for the first time."}
	}
	wait := p.retryAt.Sub(p.now()).Round(time.Second)
	if wait < 0 {
		wait = 0
	}
	// A sentence of this console's, never the error: this is read by anyone
	// who can reach the sign-in page, and the error names the provider's
	// internal address, its resolved IP and whatever it wrote back. The log
	// carries that, once per change of reason.
	return api.SSO{State: api.SSOUnavailable, Detail: fmt.Sprintf(
		"The identity provider %s. Tried %d times; the next attempt is in %s, and signing in tries "+
			"again at once. The console's log says more.", p.lastPublic, p.tries, wait)}
}

// discoveryError is a failed discovery with the reason the sign-in page may
// give. Error() is the whole story, for the log.
type discoveryError struct {
	public string
	err    error
}

func (e *discoveryError) Error() string { return e.err.Error() }
func (e *discoveryError) Unwrap() error { return e.err }

// httpStatus finds the status go-oidc puts at the front of its error for a
// discovery document that did not come back 200 ("503 Service Unavailable: ...").
var httpStatus = regexp.MustCompile(`^([1-5][0-9][0-9]) `)

// publicReason is what an anonymous reader of the sign-in page is told about
// a failed discovery: a category and an HTTP status where there was one;
// never the address, a resolved IP or the body.
func publicReason(err error) string {
	var d *discoveryError
	if errors.As(err, &d) && d.public != "" {
		return d.public
	}
	var netErr *url.Error
	if errors.As(err, &netErr) {
		return "did not answer: the connection failed or timed out"
	}
	// discover wraps go-oidc's error as "discovery at <address>: <error>".
	msg := err.Error()
	if _, rest, ok := strings.Cut(msg, ": "); ok {
		msg = rest
	}
	if m := httpStatus.FindStringSubmatch(msg); m != nil {
		return "answered its discovery request with HTTP " + m[1]
	}
	return "answered its discovery request with something this console could not use"
}

func (p *provider) current() (*discovered, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.ready, p.ready != nil
}

// clip shortens an error for a page or a log: a provider's HTML error page
// ends up whole in go-oidc's error.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// stateKey is what the session store records for a used `state`: a hash, so
// the store never holds a value that was in a URL.
func stateKey(state string) string {
	sum := sha256.Sum256([]byte(state))
	return "sso-state:" + base64.RawURLEncoding.EncodeToString(sum[:])
}

// randomToken is an opaque value for `state` and `nonce`.
func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// authorisedParty applies OIDC Core 3.1.3.7's rules 4 and 5 to a verified
// token: several audiences need an `azp`, and an `azp` must be this client.
func (p *provider) authorisedParty(token *oidc.IDToken) error {
	var claims struct {
		AZP *string `json:"azp"`
	}
	if err := token.Claims(&claims); err != nil {
		return fmt.Errorf("the token's claims could not be read: %w", err)
	}
	if claims.AZP == nil {
		if len(token.Audience) > 1 {
			return fmt.Errorf("the token names %d audiences and no authorized party", len(token.Audience))
		}
		return nil
	}
	if *claims.AZP != p.cfg.ClientID {
		return fmt.Errorf("the token was issued to another client (azp %q)", clip(*claims.AZP, 64))
	}
	return nil
}

// --- Who the token says the person is ---------------------------------------

// person reads the name and the groups out of a verified token.
//
// Every refusal is a refusal, never a fallback: a missing username claim
// does not become `sub`, a malformed groups claim does not become "no
// groups". Either would assert a person to the API server on a guess
// (docs/SERVER-MODE-THREAT-MODEL.md §4.2).
func (p *provider) person(token *oidc.IDToken) (storedSession, error) {
	var claims map[string]any
	if err := token.Claims(&claims); err != nil {
		return storedSession{}, fmt.Errorf("the token's claims could not be read: %w", err)
	}

	claimed, _ := claims[p.cfg.UsernameClaim].(string)
	if claimed == "" {
		return storedSession{}, fmt.Errorf("the token carries no %q claim, which is the one "+
			"configured to name the person", p.cfg.UsernameClaim)
	}
	// The prefix first, every check after it: the prefixed string is the
	// one asserted, reviewed, logged and stored, and the empty prefix (the
	// `email` default) is exactly the case the `system:` check exists for.
	//
	// The refusals name the prefix and the claim apart, clipping only the
	// claim: an issuer prefix alone can be 75 characters.
	prefix := p.cfg.NamePrefix()
	name := prefix + claimed
	if unprintable(name) {
		return storedSession{}, fmt.Errorf("the token names the person %q (behind the prefix %q), which "+
			"carries a control or invisible character", clip(claimed, 64), prefix)
	}
	if reserved(name) {
		return storedSession{}, fmt.Errorf("the token names the person %q (behind the prefix %q), which "+
			"starts with %q and would be one of Kubernetes' own users; set KT_OIDC_USERNAME_PREFIX, or "+
			"rename them at the provider", clip(claimed, 64), prefix, reservedPrefix)
	}
	if p.cfg.UsernameClaim == "email" {
		raw, present := claims["email_verified"]
		if present {
			if verified, isBool := raw.(bool); !isBool || !verified {
				return storedSession{}, errors.New("the token's email is not verified, and an " +
					"unverified address names nobody")
			}
		} else {
			// Accepted, as kube-apiserver accepts it: some providers never send
			// the claim (Azure AD by default), and refusing would make `email`
			// unusable there. That is the nOAuth case, so it is logged, once,
			// since it is a property of the provider.
			p.mu.Lock()
			warn := !p.warnedUnverified
			p.warnedUnverified = true
			p.mu.Unlock()
			if warn {
				log.Printf("[auth] a token named its person by email and carried no email_verified claim, " +
					"so nothing says the provider checked the address. If its users can set their own " +
					"email, any of them can be asserted as anybody; name people by \"sub\" " +
					"(KT_OIDC_USERNAME_CLAIM) unless the provider is known to verify it. Said once")
			}
		}
	}

	var groups []string
	switch raw := claims[p.cfg.GroupsClaim].(type) {
	case nil:
		p.mu.Lock()
		warn := !p.warnedNoGroups
		p.warnedNoGroups = true
		p.mu.Unlock()
		if warn {
			log.Printf("[auth] a token arrived with no %q claim, so its person has no groups. Some "+
				"providers emit it only when a %q scope is asked for (KT_OIDC_SCOPES); Dex's "+
				"static passwords never emit one. Said once", p.cfg.GroupsClaim, "groups")
		}
	case string:
		// An empty string is refused, like an empty list element: it is
		// not "no groups", which is the claim being absent.
		if raw == "" {
			return storedSession{}, fmt.Errorf("the %q claim is an empty string, which is not a group name",
				p.cfg.GroupsClaim)
		}
		groups = []string{raw}
	case []any:
		for _, g := range raw {
			s, ok := g.(string)
			if !ok || s == "" {
				return storedSession{}, fmt.Errorf("the %q claim holds something that is not a group name",
					p.cfg.GroupsClaim)
			}
			groups = append(groups, s)
		}
	default:
		return storedSession{}, fmt.Errorf("the %q claim is neither a string nor a list of strings",
			p.cfg.GroupsClaim)
	}
	for i := range groups {
		groups[i] = p.cfg.GroupsPrefix + groups[i]
		// After the prefix, which is what is asserted; the empty default
		// prefix is exactly the case this exists for.
		if unprintable(groups[i]) {
			return storedSession{}, fmt.Errorf("the %q claim holds the group %q, which carries a control "+
				"or invisible character", p.cfg.GroupsClaim, clip(groups[i], 64))
		}
		if reserved(groups[i]) {
			return storedSession{}, fmt.Errorf("the %q claim holds the group %q, which starts with %q and "+
				"would be one of Kubernetes' own groups; set KT_OIDC_GROUPS_PREFIX, or remove it at the "+
				"provider", p.cfg.GroupsClaim, clip(groups[i], 64), reservedPrefix)
		}
	}
	if groups == nil {
		groups = []string{}
	}
	return storedSession{Name: name, Groups: groups, Mapping: p.mapping}, nil
}

// --- The two routes ---------------------------------------------------------

// StartSSO sends the browser to the provider.
func (a *Auth) StartSSO(w http.ResponseWriter, r *http.Request) {
	p := a.sso
	// The one place `next` is made safe: it is sealed into the flow and read
	// back from the seal, which only this process can write.
	next := safeNext(r.URL.Query().Get("next"))
	if len(next) > maxNextBytes {
		next = "/"
	}
	if p == nil {
		// Unreachable through the router, which registers this route only
		// with a provider; answered rather than panicking.
		a.ssoFailure(w, r, ssoUnavailable, next)
		return
	}

	// The flow cookie must be set on the origin the provider returns to, or
	// every sign-in reads as expired. A click on another address (`localhost`
	// for `127.0.0.1`, the pod's address for the ingress) is first sent to the
	// configured origin, never the request's Host, so nobody else chooses
	// this redirect.
	//
	// Once only, marked by `hop`: a proxy rewriting Host would otherwise loop
	// the browser forever.
	if callback, err := url.Parse(p.cfg.RedirectURL); err == nil && r.Host != "" && r.Host != callback.Host &&
		r.URL.Query().Get("hop") == "" {
		there := *callback
		there.Path = StartPath
		there.RawQuery = url.Values{"next": {next}, "hop": {"1"}}.Encode()
		w.Header().Set("Cache-Control", "no-store")
		redirect(w, there.String(), http.StatusFound)
		return
	}

	found, ok := p.current()
	if !ok {
		p.retryNow()
		a.ssoFailure(w, r, ssoUnavailable, next)
		return
	}

	state, err1 := randomToken()
	nonce, err2 := randomToken()
	if err1 != nil || err2 != nil {
		log.Printf("[auth] single sign-on could not draw a random value, so no flow was started")
		a.ssoFailure(w, r, ssoFailed, next)
		return
	}
	verifier := oauth2.GenerateVerifier()
	sealed, err := p.sealFlow(flow{State: state, Nonce: nonce, Verifier: verifier, Next: next,
		Expires: p.now().Add(flowLifetime)})
	if err != nil {
		log.Printf("[auth] single sign-on could not seal the flow, so none was started: %v", err)
		a.ssoFailure(w, r, ssoFailed, next)
		return
	}

	name, path := p.flowCookie()
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    sealed,
		Path:     path,
		HttpOnly: true,
		// Lax lets it ride the provider's redirect back, a cross-site
		// top-level GET, which Strict would drop.
		SameSite: http.SameSiteLaxMode,
		Secure:   p.secure,
		MaxAge:   int(flowLifetime.Seconds()),
	})
	w.Header().Set("Cache-Control", "no-store")
	redirect(w, found.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)),
		http.StatusFound)
}

// FinishSSO is the redirect URI.
func (a *Auth) FinishSSO(w http.ResponseWriter, r *http.Request) {
	p := a.sso
	if p == nil {
		a.ssoFailure(w, r, ssoUnavailable, "/")
		return
	}
	query := r.URL.Query()

	// The flow cookie goes whatever happens next.
	name, path := p.flowCookie()
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: path,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: p.secure, MaxAge: -1})

	state := query.Get("state")
	cookie, err := r.Cookie(name)
	if state == "" || err != nil {
		// This browser did not start the flow, or started it on another
		// origin; a callback not from this browser's own click is precisely
		// the one to refuse.
		a.refused(r, "sso-no-flow", "[auth] single sign-on refused: the callback arrived without this browser's flow")
		a.ssoFailure(w, r, ssoExpired, "/")
		return
	}
	pending, ok := p.openFlow(cookie.Value)
	if !ok {
		a.refused(r, "sso-bad-flow", "[auth] single sign-on refused: the flow cookie is not one this console sealed, or it "+
			"is older than %s", flowLifetime)
		a.ssoFailure(w, r, ssoExpired, "/")
		return
	}
	if subtle.ConstantTimeCompare([]byte(pending.State), []byte(state)) != 1 {
		a.refused(r, "sso-state", "[auth] single sign-on refused: the callback's state does not match this browser's")
		a.ssoFailure(w, r, ssoExpired, "/")
		return
	}

	if code := query.Get("error"); code != "" {
		// The provider's error code is from a fixed vocabulary (RFC 6749
		// §4.1.2.1) and worth a log line; its free-text description is not
		// repeated anywhere.
		a.refused(r, "sso-idp-error", "[auth] single sign-on: the identity provider answered %q", clip(code, 64))
		a.ssoFailure(w, r, ssoRefused, pending.Next)
		return
	}

	found, ok := p.current()
	if !ok {
		a.ssoFailure(w, r, ssoUnavailable, pending.Next)
		return
	}

	ctx, cancel := context.WithTimeout(oidc.ClientContext(r.Context(), p.client), httpTimeout)
	defer cancel()
	token, err := found.oauth.Exchange(ctx, query.Get("code"), oauth2.VerifierOption(pending.Verifier))
	if err != nil {
		// oauth2's error can quote the provider's response body: the
		// provider's words about a code this console sent, not a secret it
		// holds, and the one thing that says why.
		a.refused(r, "sso-exchange", "[auth] single sign-on refused: the code exchange failed: %s", clip(err.Error(), 300))
		a.ssoFailure(w, r, ssoFailed, pending.Next)
		return
	}
	raw, _ := token.Extra("id_token").(string)
	if raw == "" {
		a.refused(r, "sso-no-id-token", "[auth] single sign-on refused: the token response carries no ID token")
		a.ssoFailure(w, r, ssoFailed, pending.Next)
		return
	}
	verified, err := found.verifier.Verify(ctx, raw)
	if err != nil {
		a.refused(r, "sso-verify", "[auth] single sign-on refused: the ID token did not verify: %s", clip(err.Error(), 300))
		a.ssoFailure(w, r, ssoFailed, pending.Next)
		return
	}
	// go-oidc checks the nonce only through a context it does not hold
	// here, so it is compared here, in constant time, before anything in
	// the token is believed.
	if subtle.ConstantTimeCompare([]byte(verified.Nonce), []byte(pending.Nonce)) != 1 {
		a.refused(r, "sso-nonce", "[auth] single sign-on refused: the ID token carries a nonce this flow did not issue")
		a.ssoFailure(w, r, ssoFailed, pending.Next)
		return
	}
	// go-oidc checks that `aud` contains this client but not the
	// authorized party. OIDC Core 3.1.3.7 does: several audiences need this
	// client in `azp`, and an `azp` naming another client is a token issued
	// to somebody else.
	if err := p.authorisedParty(verified); err != nil {
		a.refused(r, "sso-azp", "[auth] single sign-on refused: %v", err)
		a.ssoFailure(w, r, ssoFailed, pending.Next)
		return
	}
	who, err := p.person(verified)
	if err != nil {
		a.refused(r, "sso-person", "[auth] single sign-on refused: %v", err)
		a.ssoFailure(w, r, ssoUnnamed, pending.Next)
		return
	}

	now := a.now()
	// Single use, on this console's authority (see flow). A store that did
	// not answer is the store being down, not a replay.
	first, err := a.store.once(r.Context(), stateKey(pending.State), pending.Expires)
	if err != nil {
		log.Printf("[auth] single sign-on could not record this sign-in's state, so no session was opened: %v", err)
		a.ssoFailure(w, r, ssoStore, pending.Next)
		return
	}
	if !first {
		a.refused(r, "sso-replay", "[auth] single sign-on refused: this sign-in's state has already opened a session, "+
			"so the callback is a replay")
		a.ssoFailure(w, r, ssoExpired, pending.Next)
		return
	}
	who.Issued = now
	// The record lives as long as the cookie carrying its id: the idle
	// window from now, never past the cap.
	id, err := a.store.open(r.Context(), who, a.expiryFor(now, now, anySSOSession))
	if err != nil {
		log.Printf("[auth] single sign-on could not open a session: %v", err)
		a.ssoFailure(w, r, ssoStore, pending.Next)
		return
	}
	cookieToken, c := a.mint(now, now, id, byPassword)
	http.SetCookie(w, a.sessionCookie(cookieToken, c, now))
	// Quoted, because both come from the provider: a newline in a name
	// would let the rest pose as another line.
	log.Printf("[auth] signed in with single sign-on: %q (groups: %s)", who.Name, groupList(who.Groups))

	w.Header().Set("Cache-Control", "no-store")
	redirect(w, pending.Next, http.StatusSeeOther)
}

// ssoFailure sends the browser back to the sign-in page with a fixed code.
func (a *Auth) ssoFailure(w http.ResponseWriter, r *http.Request, code, next string) {
	q := url.Values{"sso": {code}}
	if next != "" && next != "/" {
		q.Set("next", next)
	}
	// 303 from the callback, which acted on a request; 302 from the start,
	// which acted on nothing.
	status := http.StatusFound
	if r.URL.Path == CallbackPath {
		status = http.StatusSeeOther
	}
	w.Header().Set("Cache-Control", "no-store")
	redirect(w, LoginPath+"?"+q.Encode(), status)
}

// redirect answers with a Location and no body.
//
// Not http.Redirect, which writes an HTML body on a GET that
// `api/openapi.yaml` does not declare for these routes.
func redirect(w http.ResponseWriter, target string, code int) {
	w.Header().Set("Location", target)
	w.WriteHeader(code)
}

func groupList(groups []string) string {
	if len(groups) == 0 {
		return "none"
	}
	quoted := make([]string, len(groups))
	for i, g := range groups {
		quoted[i] = strconv.Quote(g)
	}
	return strings.Join(quoted, ", ")
}

// --- What the sign-in page offers -------------------------------------------

// OffersPassword is whether the shared account can be signed into with its
// password: it exists (always without a provider, with one only where kept)
// and its password was not withdrawn (Config.WithoutPassword).
func (a *Auth) OffersPassword() bool { return a.localAccount() && !a.cfg.WithoutPassword }

// localAccount is whether the shared account exists at all: no identity
// provider, or one beside which KT_AUTH_LOCAL_ACCOUNT keeps it. Launch codes
// sign in as it.
func (a *Auth) localAccount() bool { return a.sso == nil || a.cfg.KeepLocalAccount }

// OffersSSO is whether an identity provider is configured.
func (a *Auth) OffersSSO() bool { return a.sso != nil }

// Methods is the sign-in page's state now.
func (a *Auth) Methods() api.SignInMethods {
	m := api.SignInMethods{Password: a.OffersPassword(), SSO: api.SSO{State: api.SSOOff}}
	if a.sso != nil {
		m.SSO = a.sso.state()
	}
	return m
}

// Start runs single sign-on's discovery in the background, until ctx ends or
// the provider answers. A no-op without a provider. Called once by the process;
// the sign-in page reads `pending` until the first attempt has answered.
func (a *Auth) Start(ctx context.Context) {
	if a.sso == nil {
		return
	}
	go a.sso.run(ctx)
}

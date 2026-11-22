// Package auth is KubeTower's front door: a shared account behind a password
// or a one-time launch code, single sign-on, and one signed session cookie.
//
// This process holds credentials for production clusters, so whatever reaches
// the socket can use them. The decision is a Middleware around the router,
// which sees the request itself rather than reconstructing it from
// X-Forwarded-* headers.
//
// The token carries its issue time as well as its expiry, and the expiry
// slides, so a console in use all day is not asked to sign in mid-day (see
// DefaultSessionIdle and tokenVersion).
//
// Mounting it, in the package that owns the router:
//
//	mux.HandleFunc("GET "+auth.LoginPath, a.LoginPage)
//	mux.HandleFunc("POST "+auth.LoginPath, a.Login)
//	mux.HandleFunc(auth.LogoutPath, a.Logout)
//	srv.Handler = a.Middleware(mux)
//
// The middleware lets those paths through itself, so a login page cannot end
// up guarded by the session it exists to create.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/sharedstate"
	"golang.org/x/crypto/bcrypt"
)

// The paths the front door owns. Constants, because the login page names them
// in its own markup, and a form posting to a path the router does not serve
// would be a login screen that silently 404s.
const (
	LoginPath  = "/auth/login"
	LogoutPath = "/auth/logout"

	// HealthPath answers without a session, or a container health check
	// would read the login page as a failure and restart a working process
	// forever.
	HealthPath = "/kt-healthz"

	// PWAPrefix is where the manifest, the icons and the service worker live.
	// The browser fetches them without the session cookie; guarded, it would
	// get the login page instead and the install offer would silently vanish.
	// Exported so the serving and the exempting packages share one string.
	PWAPrefix = "/kt-pwa/"
)

const (
	defaultCookieName = "kubetower_session"

	// DefaultSessionIdle and DefaultSessionMax are how long a session lasts:
	// the idle window is how long the console may go unused before it asks
	// again; the maximum is how long a session lives however much it is used.
	//
	// One absolute lifetime fails both ways: it ends a working day mid-way, or
	// makes every morning a login. An idle window never ends a session in use,
	// and thirty days is longer than any break that keeps the same browser and
	// state directory (a fortnight would be the length of a holiday).
	//
	// The cap is the load-bearing one. The token is stateless, so the idle
	// window only expires a session nobody uses: a stolen cookie is used, which
	// refreshes it, so the idle window alone would be an unbounded session for
	// whoever took it. The cap is the only thing that ends a session in use,
	// short of rotating the signing key. Ninety days: one unrequested login a
	// quarter, and a leaked cookie is not permanent.
	//
	// Settable as `KT_SESSION_TTL` and `KT_SESSION_MAX`.
	DefaultSessionIdle = 30 * 24 * time.Hour
	DefaultSessionMax  = 90 * 24 * time.Hour

	// refreshEvery is how often a session in continuous use may cost a
	// `Set-Cookie`: re-signing on every request would put that header on every
	// two-second refresh. It is a rate limit on a header, not a policy; the
	// window that can be relied on is the configured one minus this. An hour off
	// thirty days goes unnoticed, where a "refresh below half" rule would cost
	// fifteen days.
	refreshEvery = time.Hour

	// minSessionKey is the shortest HMAC key accepted. The generated secret is
	// 64 hex characters, so this only bites on a hand-typed one, which is worth
	// refusing: every session rests on it being unguessable.
	minSessionKey = 32

	// failureDelay is how long a rejected login is held before it answers:
	// friction on top of the rate limiter (attempts.go), not the limiter.
	failureDelay = 750 * time.Millisecond
)

// Config is everything the front door needs. There is one shared account,
// not a user table: a user table would be a second thing to keep secure for
// nobody's benefit; people are told apart through single sign-on.
type Config struct {
	// User is the login name, compared in constant time all the same.
	User string

	// PasswordHash is a bcrypt hash, never a password. It arrives through the
	// environment, beside the kubeconfig, so a reversible form would buy
	// nothing.
	PasswordHash string

	// SessionKey signs the session tokens. Rotating it ends every session, the
	// only logout-everywhere this design has. Changing PasswordHash ends every
	// shared-account session as well (signToken), not single sign-on ones.
	SessionKey []byte

	// SessionIdle is how long the console may go unused before the session ends.
	// It slides: every request with a live session pushes the deadline out.
	// Zero or negative means DefaultSessionIdle.
	//
	// Not called SessionTTL, although the variable is `KT_SESSION_TTL`: a "TTL"
	// reads as a lifetime from issue, which this is not. The variable keeps its
	// name because renaming it would silently widen every installation that
	// shortened it; internal/launch is where both names meet.
	SessionIdle time.Duration

	// SessionMax is the longest a session may live counted from the login that
	// created it, however much it is used. Zero or negative means
	// DefaultSessionMax.
	//
	// Re-checked on every request rather than trusted from the token, so
	// shortening it affects cookies already in browsers. Lengthening it does
	// not extend an existing cookie past its MaxAge.
	SessionMax time.Duration

	// CookieName is the session cookie. Zero value means defaultCookieName.
	CookieName string

	// OnLogin is run after every successful login. Optional.
	//
	// internal/launch uses it to delete the one-time file holding a first-run
	// password once somebody has used it. The deletion belongs where a login is
	// decided; the file, the state directory and what a first run is do not, so
	// the package declares only what it needs: to know a login succeeded.
	//
	// It cannot fail a login: it returns nothing, and a panic is recovered, since
	// an operator shut out of their console because a file could not be unlinked
	// is far worse than the file lingering. Called on every login, so it must be
	// idempotent and cheap.
	OnLogin func()

	// PasswordNote reports where a drawn password can still be read (a path), or
	// "" when there is nothing to point at. Optional.
	//
	// A first-run password written to a file is obtainable but not discoverable:
	// an application opened from a desktop menu has no visible stderr, and the
	// browser opens on this page with nothing saying a file exists. So the page
	// says it. A predicate handed in, as OnLogin is, so the state directory stays
	// none of this package's business.
	//
	// A path, never the value: the page is served before anything is trusted.
	// Nothing here opens the file, so there is no reader to leak.
	//
	// Called on every render, because the file lives only until somebody signs
	// in; a stat per render is nothing beside the bcrypt the POST pays. A panic
	// is recovered, because a login page that 500s is a lock-out.
	PasswordNote func() string

	// OIDC is the identity provider, or nil for a console with no single
	// sign-on. See oidc.go.
	OIDC *OIDCConfig

	// Store is where single sign-on keeps its sessions and its sign-ins in
	// progress. Nil means this process's memory, which suits one replica;
	// several replicas need a shared one (internal/launch builds a Valkey one
	// from `KT_VALKEY_ADDR`). Wrap it in sharedstate.Seal first: see that type
	// for why a store's password is not enough.
	Store sharedstate.Store

	// KeepLocalAccount keeps the shared password alongside an identity
	// provider. Meaningless without OIDC and false by default with it, so
	// configuring a provider removes the shared password unless somebody wrote
	// this out where a release reviewer reads it: a shared password beside a
	// provider is a permanent bypass nobody remembers to turn off.
	//
	// The cost: a deployment whose provider never comes back, with this unset,
	// has no way in but a redeploy, a deliberate act with an audit trail rather
	// than a standing back door.
	KeepLocalAccount bool

	// WithoutPassword keeps the shared account and withdraws its password: the
	// loopback desktop, where a person arrives through a one-time launch code.
	// The form and `POST /api/auth/login` are then not registered, and the
	// sign-in page says how to open the console instead. The account still
	// exists (launch codes sign in as it, and existing sessions last until their
	// cap). Refused beside an identity provider, where KeepLocalAccount rules.
	WithoutPassword bool

	// TrustedProxies are the proxies whose X-Forwarded-For is believed when a
	// password attempt is counted and logged (KT_TRUSTED_PROXIES). Empty
	// believes nobody: the connecting address is the client.
	TrustedProxies []netip.Prefix

	// BehindProxy says the connecting address may be a proxy every person
	// shares (an ingress), so unless named in TrustedProxies it is not taken
	// for one client and gets no per-client budget. Set under --incluster.
	BehindProxy bool

	// SecureCookies says the browser reaches this console over TLS whatever
	// this process listens on: the server profile behind its TLS ingress. The
	// session and known-browser cookies are then `Secure` with or without single
	// sign-on; otherwise a password-only server would send a cookie that opens a
	// cluster in the clear on any plain-HTTP request to the host (a redirected
	// `http://` link, a captive portal). False on the desktop, which listens on
	// plain loopback HTTP and could not keep a Secure cookie.
	SecureCookies bool
}

// Auth guards the front door.
type Auth struct {
	cfg Config

	// accountKey signs the shared account's session tokens: derived from the
	// session key and the password hash, so changing either ends every
	// shared-account session. A single sign-on token is signed with the
	// session key itself (deriveAccountKey).
	accountKey []byte

	// delay is failureDelay, on the struct so tests need not pay it.
	delay time.Duration

	// now is time.Now, held here so the sliding expiry can be tested at its
	// boundaries. Written once in New, or in a test's setup before serving;
	// never while a request is in flight.
	now func() time.Time

	// sso is the identity provider, nil without one. store holds its sessions,
	// and ssoMax is their cap: the shorter of the provider's and the password
	// session's own.
	sso    *provider
	store  *sessionStore
	ssoMax time.Duration

	// tries is each client's budget of password attempts and queue the
	// anonymous attempts' share of hashing capacity (attempts.go); known and
	// knownQueue are the same for a known browser (known.go). sleep is
	// time.Sleep, so a test does not wait out a queue.
	tries      attempts
	queue      queue
	known      attempts
	knownQueue queue
	sleep      func(time.Duration)

	// signedOut is told who signed out, once the store has recorded it. Set by
	// OnSignOut before anything is served; nil does nothing.
	signedOut func(identity.Who)

	// launch holds the unspent one-time codes this process minted for its
	// native window (window.go).
	launch launchCodes

	// launchOffered is set by OfferLaunches, on the desktop profile only.
	launchOffered bool

	// launcherAddr is the address this instance answers on, which every
	// handoff MAC names; challenges are its issued, unanswered server nonces
	// (handoff.go).
	launcherAddr string
	challenges   handoffChallenges

	// refusals coalesces the refusal lines of the launch and handoff routes,
	// which anything on loopback can provoke, and of refused sign-ins and
	// single sign-on callbacks, per client (refusallog.go).
	refusals refusalLog

	// launcher is the launcher secret a second launch must present to be
	// handed a code (handoff.go). Set by AcceptHandoffs before anything is
	// served, on the loopback desktop only; empty, the handoff answers 404.
	launcher []byte
}

// OnSignOut installs what runs once a single sign-on sign-out is recorded:
// internal/launch closes the person's open shells and log streams, which a
// sign-out would otherwise leave running. Called once, before serving.
//
// Single sign-on only: a password session names the shared account, so
// ending "admin"'s sessions would close every other holder's too.
func (a *Auth) OnSignOut(fn func(identity.Who)) { a.signedOut = fn }

// String and GoString print an *Auth as its name only: its fields hold the
// session key, which a formatted value in a test failure or log line would
// otherwise print. TestAFrontDoorPrintsNoKey holds it.
func (a *Auth) String() string   { return "auth.Auth{redacted}" }
func (a *Auth) GoString() string { return a.String() }

// New validates the configuration and fills in the optional parts. It refuses
// rather than falling back: a front door started with no password looks,
// from outside, like one started with a good one.
func New(cfg Config) (*Auth, error) {
	// The password is required exactly where it is offered.
	if cfg.WithoutPassword && cfg.OIDC != nil {
		return nil, errors.New("auth: a console without a password has no identity provider; " +
			"beside one, the password is kept only by KeepLocalAccount")
	}
	if cfg.OIDC == nil || cfg.KeepLocalAccount {
		if cfg.User == "" {
			return nil, errors.New("auth: no user configured")
		}
		// Without a password there is no hash to check one against, and
		// demanding it would make somebody draw one.
		if cfg.PasswordHash == "" && !cfg.WithoutPassword {
			return nil, errors.New("auth: no password hash configured")
		}
	}
	if cfg.OIDC != nil {
		if err := validateOIDC(cfg.OIDC); err != nil {
			return nil, err
		}
	}
	if len(cfg.SessionKey) < minSessionKey {
		return nil, fmt.Errorf("auth: session key is %d bytes, minimum %d", len(cfg.SessionKey), minSessionKey)
	}
	if cfg.SessionIdle <= 0 {
		cfg.SessionIdle = DefaultSessionIdle
	}
	if cfg.SessionMax <= 0 {
		cfg.SessionMax = DefaultSessionMax
	}
	if cfg.CookieName == "" {
		cfg.CookieName = defaultCookieName
	}
	a := &Auth{cfg: cfg, accountKey: deriveAccountKey(cfg.SessionKey, cfg.PasswordHash),
		delay: failureDelay, now: time.Now, sleep: time.Sleep,
		known:      attempts{burst: knownBurst, every: knownEvery},
		knownQueue: queue{burst: knownQueueBurst, every: knownQueueEvery}}
	// Read through a.now, not captured, so a test that freezes the clock
	// after New freezes the provider's token checks and the in-memory store.
	clock := func() time.Time { return a.now() }
	kv := cfg.Store
	if kv == nil {
		kv = sharedstate.NewMemory(clock)
	}
	a.store = newSessionStore(kv)
	if cfg.OIDC != nil {
		seal, err := newFlowSeal(cfg.SessionKey)
		if err != nil {
			return nil, err
		}
		a.sso = newProvider(*cfg.OIDC, clock, seal)
		a.ssoMax = a.sso.cfg.SessionMax
		if cfg.SessionMax < a.ssoMax {
			a.ssoMax = cfg.SessionMax
		}
	}
	return a, nil
}

// SessionPolicy says in one line how long a session lasts, for the startup
// log: both numbers have defaults and are settable, so "why am I asked to
// sign in" is otherwise invisible from outside.
func (a *Auth) SessionPolicy() string {
	policy := fmt.Sprintf("a session ends after %s unused, and %s after signing in whatever happens",
		a.cfg.SessionIdle, a.cfg.SessionMax)
	if a.sso != nil {
		// The single sign-on cap is also how stale a person's groups may get,
		// which an operator revoking access needs to know.
		policy += fmt.Sprintf("; a single sign-on session ends %s after signing in, which is also "+
			"how long a group removed at the identity provider keeps being asserted", a.ssoMax)
	}
	return policy
}

// NamePolicy says in one line which string a person signed in through the
// provider is reviewed as, for the startup log, or "" with no provider. It
// is the string every RoleBinding must name: with the default a claim other
// than `email` arrives behind the issuer, and a binding for the bare claim
// matches nobody.
func (a *Auth) NamePolicy() string {
	if a.sso == nil {
		return ""
	}
	claim := a.sso.cfg.UsernameClaim // defaulted by newProvider
	if prefix := a.sso.cfg.NamePrefix(); prefix != "" {
		return fmt.Sprintf("people signed in through the identity provider are reviewed as %q followed by "+
			"their %q claim (KT_OIDC_USERNAME_PREFIX)", prefix, claim)
	}
	return fmt.Sprintf("people signed in through the identity provider are reviewed by their %q claim, "+
		"unprefixed", claim)
}

// --- Session token ----------------------------------------------------------
//
// The token is self-contained: "2:issued:expiry|signature". No server-side
// table means nothing to expire, synchronize or leak on restart; the
// signature stops the holder rewriting either stamp.
//
// "2:issued:expiry:id|signature" is a single sign-on session, naming its
// record in the session store (store.go): the same cookie shape, guard and
// writer, with one more field. "2:issued:expiry:window|signature" and
// "...:launch|signature" are shared-account sessions opened by a one-time
// launch code for a native or a browser window (window.go); the literal word
// can never be a store id.
//
// Two stamps, because a sliding window needs the issue time: without it a
// refresh is indistinguishable from an issue and nothing caps anything.
//
// The `2:` makes an older expiry-only cookie a refusal rather than a guess:
// it is signed with the same key, so only the version tells them apart, and
// read as "issued now" its cap would count from an upgrade instead of a login.
//
// ":" separates the payload's fields, not "|", so the cut between payload
// and signature can never read a stamp as a signature.
const tokenVersion = "2"

// claims is what a verified token says. Never construct one from an
// unverified token: everything in it is what the cookie's holder would like.
type claims struct {
	issued  time.Time
	expires time.Time
	// sid is the session store's id for a single sign-on session, "" for a
	// stateless shared-account one.
	sid string
	// opened is how a session without a sid was opened: the password, or a
	// launch code for a browser or a native window. A launch session is
	// otherwise the password's (shared account, no record, the same cap).
	// Always byPassword with a sid.
	opened openedBy
}

// openedBy is how a shared-account session was opened.
type openedBy uint8

const (
	byPassword openedBy = iota
	// byLaunch is a one-time code minted for a browser window this process
	// (or a second launch of it) opened.
	byLaunch
	// byWindow is a one-time code minted for a native window: the session
	// identity.Who.Window marks.
	byWindow
)

// window is whether the session is a native window's (identity.Who.Window).
func (c claims) window() bool { return c.opened == byWindow }

// sign is the session key's MAC over payload: the known-browser cookie's
// (known.go). A session token is signed by signToken.
func (a *Auth) sign(payload string) string { return macOf(a.cfg.SessionKey, payload) }

// signToken is the signature of a session token's payload: under the shared
// account's key for a session with no store id, under the session key for a
// single sign-on session.
//
// The shared account's key is bound to the password: the token is stateless,
// so its signature is the only revocation, and under KT_SESSION_SECRET alone
// a session opened with a since-leaked, since-changed password would live on
// up to its cap unless the secret were rotated too. A single sign-on session
// is ended by its store record instead.
func (a *Auth) signToken(payload, sid string) string {
	if sid == "" {
		return macOf(a.accountKey, payload)
	}
	return macOf(a.cfg.SessionKey, payload)
}

// deriveAccountKey is the shared account's token key: HMAC-SHA256 under the
// deriveAccountKey is the shared account's token key: HMAC-SHA256 under the
// session key over a label and the password hash. The hash, because that is
// all this process holds; bcrypt is salted, so resetting the same password
// still ends the sessions.
//
// With no password (WithoutPassword) the hash is empty, and the key is still
// distinct from the session key and stable across restarts, so a window's
// session survives one. An installation that once had a password keeps its
// hash (internal/launch), so its sessions keep their key.
func deriveAccountKey(sessionKey []byte, passwordHash string) []byte {
	mac := hmac.New(sha256.New, sessionKey)
	mac.Write([]byte("kubetower shared-account sessions\x00"))
	mac.Write([]byte(passwordHash))
	return mac.Sum(nil)
}

func macOf(key []byte, payload string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// mint writes a token for a session that began at `issued`, seen at `now`, with
// a single sign-on store id or "" for the shared account, and how a
// shared-account session was opened.
//
// Issue and refresh both come here, and `issued` is an argument: a refresh
// that re-stamped the issue time would slide the cap with the deadline and
// make it unreachable. There is exactly one writer.
func (a *Auth) mint(issued, now time.Time, sid string, opened openedBy) (string, claims) {
	if sid != "" {
		opened = byPassword
	}
	c := claims{issued: issued, expires: a.expiryFor(issued, now, sid), sid: sid, opened: opened}
	payload := tokenVersion + ":" +
		strconv.FormatInt(c.issued.Unix(), 10) + ":" +
		strconv.FormatInt(c.expires.Unix(), 10)
	switch {
	case sid != "":
		payload += ":" + sid
	case opened == byWindow:
		payload += ":" + windowMark
	case opened == byLaunch:
		payload += ":" + launchMark
	}
	return payload + "|" + a.signToken(payload, sid), c
}

// expiryFor is the deadline a session begun at `issued` has when last seen at
// `now`: the idle window from now, never past the cap from the login.
func (a *Auth) expiryFor(issued, now time.Time, sid string) time.Time {
	expires := now.Add(a.cfg.SessionIdle)
	if hard := issued.Add(a.maxFor(sid)); expires.After(hard) {
		return hard
	}
	return expires
}

// anySSOSession is a store id for expiryFor and maxFor to read as "a single
// sign-on session" before the real id is drawn: both only ask whether
// there is one.
const anySSOSession = "sso"

// maxFor is the cap of a session: the password's, or single sign-on's.
func (a *Auth) maxFor(sid string) time.Duration {
	if sid != "" {
		return a.ssoMax
	}
	return a.cfg.SessionMax
}

// parseToken verifies the signature and reads the stamps. It says nothing about
// time (live does), so the refresh path can read a token it is about to
// replace.
func (a *Auth) parseToken(token string) (claims, bool) {
	payload, signature, found := strings.Cut(token, "|")
	if !found {
		return claims{}, false
	}
	// Constant time: == returns at the first differing byte, which leaks the
	// expected signature byte by byte.
	//
	// Against both keys before the payload is read, since which key signed
	// it is the payload's to say and the payload is not trusted until a key
	// verified it. Below, each kind is held to its own key.
	byAccount := subtle.ConstantTimeCompare([]byte(signature), []byte(a.signToken(payload, ""))) == 1
	bySession := subtle.ConstantTimeCompare([]byte(signature), []byte(a.signToken(payload, anySSOSession))) == 1
	if !byAccount && !bySession {
		return claims{}, false
	}

	// Only now is the payload worth reading.
	version, stamps, found := strings.Cut(payload, ":")
	if !found || version != tokenVersion {
		// An older token lands here: correctly signed, one field short.
		return claims{}, false
	}
	issuedRaw, expiresRaw, found := strings.Cut(stamps, ":")
	if !found {
		return claims{}, false
	}
	// A fourth field is a store id, in exactly the shape the store writes,
	// or a launch mark (window.go); anything else is in no format this build
	// knows.
	expiresRaw, sid, _ := strings.Cut(expiresRaw, ":")
	opened := byPassword
	switch {
	case sid == windowMark:
		opened, sid = byWindow, ""
	case sid == launchMark:
		opened, sid = byLaunch, ""
	case sid != "" && !isSessionID(sid):
		return claims{}, false
	}
	// The key that verified it must be the one its kind is signed with: a
	// shared-account payload under the bare session key is refused.
	if (sid == "" && !byAccount) || (sid != "" && !bySession) {
		return claims{}, false
	}
	issued, err := strconv.ParseInt(issuedRaw, 10, 64)
	if err != nil {
		return claims{}, false
	}
	expires, err := strconv.ParseInt(expiresRaw, 10, 64)
	if err != nil {
		return claims{}, false
	}
	return claims{issued: time.Unix(issued, 0), expires: time.Unix(expires, 0), sid: sid, opened: opened}, true
}

// live reports whether these claims are still a session at `now`, and for a
// single sign-on session the record behind it.
//
// The cap is re-derived here, not trusted from the token, so shortening
// `KT_SESSION_MAX` affects existing cookies; otherwise rotating the signing
// key, the last resort, would be the only way to end them.
//
// An error is the store not answering, not a "no": the caller answers it as
// the store's failure, never as a lapsed session.
func (a *Auth) live(ctx context.Context, c claims, now time.Time) (storedSession, bool, error) {
	if !now.Before(c.expires) {
		return storedSession{}, false, nil
	}
	if !now.Before(c.issued.Add(a.maxFor(c.sid))) {
		return storedSession{}, false, nil
	}
	if c.sid == "" {
		// A password session is refused where the shared account does not
		// exist: the signing key usually survives configuring a provider (kept
		// in a Secret across upgrades), so every shared-password cookie would
		// otherwise outlive the removal of the account by up to its cap.
		//
		// The account, not its password: on the loopback desktop the password
		// is withdrawn and the account stays, so existing sessions keep
		// working until their cap.
		return storedSession{}, a.localAccount(), nil
	}
	// A single sign-on session is live only while its record is, which is
	// what makes a sign-out (on every replica, with a shared store) end it.
	rec, ok, err := a.store.get(ctx, c.sid)
	if err != nil || !ok {
		return rec, ok, err
	}
	// And only while it was derived as this process would derive it: a
	// rollout that changes the prefix or the claim changes who is reviewed
	// now, not at the cap. A mismatch is a lapsed session. With no provider,
	// no record is one this process could have written.
	if a.sso == nil || rec.Mapping != a.sso.mapping {
		return storedSession{}, false, nil
	}
	return rec, true, nil
}

func (a *Auth) validToken(token string) bool {
	c, ok := a.parseToken(token)
	if !ok {
		return false
	}
	_, live, err := a.live(context.Background(), c, a.now())
	return live && err == nil
}

// session is what a request's cookie amounts to: its claims and, for a single
// sign-on session, its record, read once per request because each read is a
// store round trip on several replicas.
type session struct {
	claims
	rec storedSession
}

// session reads the cookie and answers what it claims, that there is none, or
// that the store could not say.
func (a *Auth) session(r *http.Request) (session, bool, error) {
	cookie, err := r.Cookie(a.cfg.CookieName)
	if err != nil {
		return session{}, false, nil
	}
	c, ok := a.parseToken(cookie.Value)
	if !ok {
		return session{}, false, nil
	}
	rec, ok, err := a.live(r.Context(), c, a.now())
	if err != nil || !ok {
		return session{}, false, err
	}
	return session{claims: c, rec: rec}, true, nil
}

// sessionValid is whether a request carries a live session. A store that could
// not say is "no", right for its one caller: the sign-in page deciding
// whether to show itself.
func (a *Auth) sessionValid(r *http.Request) bool {
	_, ok, _ := a.session(r)
	return ok
}

// --- Keeping a session alive ------------------------------------------------

// refreshDue reports whether this session's cookie is far enough behind to be
// worth rewriting: the deadline a token minted now would have, against the
// one it has, compared to refreshEvery.
//
//   - At the cap it stops on its own: expiryFor returns the same instant once
//     the ceiling is reached, so the difference is zero.
//   - The limit is in the unit limited: one `Set-Cookie` an hour per browser
//     whatever the idle window.
//
// The step is capped at half the idle window, so a short `KT_SESSION_TTL=10m`
// is still refreshed in time rather than ending at ten minutes regardless of
// use.
func (a *Auth) refreshDue(c claims, now time.Time) bool {
	step := refreshEvery
	if half := a.cfg.SessionIdle / 2; half < step {
		step = half
	}
	return a.expiryFor(c.issued, now, c.sid).Sub(c.expires) >= step
}

// sessionCookie is the one place the session cookie's flags are written.
// Login and the sliding refresh both come here: a refresh that dropped
// HttpOnly or relaxed SameSite would silently downgrade a credential that
// lives for weeks.
func (a *Auth) sessionCookie(token string, c claims, now time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     a.cfg.CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		// Lax, not Strict: Strict drops the cookie on a top-level GET from a
		// bookmark or a link, showing a login page to someone with a session.
		// Lax still withholds it from a cross-site POST, which is what matters.
		SameSite: http.SameSiteLaxMode,
		// The remaining life, not the configured window, so browser and server
		// agree when it stops working (they differ near the cap).
		MaxAge: int(c.expires.Sub(now).Seconds()),
		// No Secure on loopback: the desktop listens on plain HTTP. See
		// secureCookies.
		Secure: a.secureCookies(),
	}
}

// secureCookies is whether the session's cookies carry Secure: where the
// browser is known to arrive over TLS (the server profile behind its
// ingress, or a single sign-on redirect URL that is https).
func (a *Auth) secureCookies() bool {
	return a.cfg.SecureCookies || (a.sso != nil && a.sso.secure)
}

// --- Middleware -------------------------------------------------------------

// Middleware refuses everything that arrives without a valid session, except
// the paths isPublic lists, and keeps a session in use alive (keepAlive).
//
// A person navigating gets the login page; a background request gets a plain
// 401, because an HTML login page handed to code expecting JSON makes the
// screen look broken rather than signed out.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.isPublic(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		s, ok, err := a.session(r)
		if err != nil {
			a.storeUnavailable(w, r, err)
			return
		}
		if ok {
			// Signed in again: the record of having logged out no longer
			// describes this browser.
			if a.loggedOut(r) {
				a.markSignedOut(w, false)
			}
			a.keepAlive(w, r, s.claims)
			// **Who the request is from travels with it**, to the one line
			// Who the request is from travels with it to the write paths' log
			// line, set here only, from the verified session, never from a header.
			if who, ok := a.who(s); ok {
				r = r.WithContext(identity.With(r.Context(), who))
			}
			next.ServeHTTP(w, r)
			return
		}

		// Under /api/ a lapsed session is a problem object and nothing else:
		// every 4xx there is `application/problem+json`, and the front end
		// routes to its login screen on the status; an HTML page in a fetch
		// would be a blank screen with no error.
		if strings.HasPrefix(r.URL.Path, api.Prefix) {
			api.WriteProblem(w, http.StatusUnauthorized, "session_expired",
				"Not signed in", "no session, or it has lapsed; sign in at "+LoginPath)
			return
		}

		if isBackgroundRequest(r) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusUnauthorized)
			// The login path is in the body, so a client need not hardcode it.
			_, _ = io.WriteString(w, `{"error":"session missing or expired","login":"`+LoginPath+`"}`)
			return
		}

		target := LoginPath
		// The request in hand is the one to return to.
		if orig := r.URL.RequestURI(); orig != "" && orig != "/" {
			target += "?next=" + url.QueryEscape(orig)
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, target, http.StatusFound)
	})
}

// storeUnavailable answers a request whose session could not be read because
// the store did not answer.
// A 503, never a 401 or the sign-in page: signing in needs the same store,
// so the person would loop through the provider, told each time their
// session had expired when it had not. The log line carries the store's
// address, never anything in it.
func (a *Auth) storeUnavailable(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("[auth] a session could not be read, so the request was answered 503: %v", err)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", "5")
	const detail = "the console's session store did not answer, so no session can be read; " +
		"it is asked again on every request"
	if strings.HasPrefix(r.URL.Path, api.Prefix) {
		api.WriteProblem(w, http.StatusServiceUnavailable, "session_store_unreachable",
			"Session store unreachable", detail)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = io.WriteString(w, "KubeTower: "+detail+". Try again in a moment.\n")
}

// keepAlive pushes the session's deadline out, by re-issuing the cookie, when it
// has fallen far enough behind to be worth a header.
//
// A session in use must not end, so its deadline moves.
//
// An upgrade is skipped: gorilla's Upgrader writes the 101 response from
// scratch, discarding headers set here, so a refresh there would only appear
// to happen. So a long-lived stream is not evidence anybody is present: a
// terminal or log stream left open does not keep the session alive by
// itself. An expiry mid-stream is not acted on here either, since tearing a
// WebSocket down from the middleware gives the browser no status to report;
// a pod exec and a log follow ask whether their sign-in still stands
// (standing.go) and end in a frame of their own.
func (a *Auth) keepAlive(w http.ResponseWriter, r *http.Request, c claims) {
	if wantsUpgrade(r) {
		return
	}
	now := a.now()
	if !a.refreshDue(c, now) {
		return
	}
	// The window's mark travels with the refresh, or the window's session
	// would become an ordinary password one.
	token, refreshed := a.mint(c.issued, now, c.sid, c.opened)
	if c.sid != "" {
		// The record's expiry moves with the cookie's, or a session in use
		// would end at its last refresh's deadline. Extended first; if the
		// store did not answer the cookie is left alone (still good until its
		// own deadline) and the next request tries again.
		ok, err := a.store.extend(r.Context(), c.sid, refreshed.expires)
		if err != nil {
			log.Printf("[auth] a session's deadline could not be moved; the cookie keeps its old one: %v", err)
			return
		}
		if !ok {
			// Ended on another replica between the read and now.
			return
		}
	}
	http.SetCookie(w, a.sessionCookie(token, refreshed, now))
}

// isPublic lists what has to answer before a session exists. Every entry is a
// path this package or the process owns; nothing that reads a cluster. The
// JSON contract's public paths are listed, not derived: a prefix match on
// api.Prefix would make every operation public.
//
// `/healthz` and `/readyz` answer a kubelet, whose probes carry no cookie; a
// 401 would report the console dead whenever nobody is signed in.
// HealthPath stays for the container's own health check.
func (a *Auth) isPublic(path string) bool {
	switch path {
	case LoginPath, LogoutPath, HealthPath:
		return true
	case LaunchPath:
		// Public because the arriving window has no session yet, and exactly
		// where the route is registered (both read OffersLaunch, set before
		// the router is built and never after). Elsewhere the path is guarded
		// like any other: public and absent, it would fall through to the
		// front end's catch-all and answer 200 with a shell whose asset names
		// fingerprint the build.
		return a.OffersLaunch()
	case HandoffPath:
		// A second launch has no session; the exchange is its proof. Public
		// only where it is registered: the loopback desktop.
		return a.AcceptsHandoffs()
	case api.Prefix + "auth/login", api.Prefix + "auth/logout", api.Prefix + "auth/methods":
		return true
	case StartPath, CallbackPath:
		// Public because the person arriving has no session yet; the router
		// registers both only where a provider is configured.
		return true
	case api.LivenessPath, api.ReadinessPath:
		return true
	}
	return strings.HasPrefix(path, PWAPrefix)
}

// isBackgroundRequest reports whether nobody is looking at a page right now.
//
// No header says "this is a navigation", so this reads the signals present
// and defaults to background: an unannounced request is more likely a fetch
// than a browser bar. The console's own fetches set `Accept` and
// `X-Requested-With`.
func isBackgroundRequest(r *http.Request) bool {
	if r.Header.Get("X-Requested-With") != "" {
		return true
	}
	if wantsUpgrade(r) {
		// A WebSocket: a 302 would be a failed handshake, not a redirect.
		return true
	}
	// A navigation announces that it wants HTML. API calls ask for JSON, or state
	// no preference at all.
	return !strings.Contains(r.Header.Get("Accept"), "text/html")
}

// wantsUpgrade reports whether this is a protocol upgrade (the dock's
// terminals and the log stream). One spelling for two decisions: such a
// request is refused with a status rather than a redirect, and is not handed
// a refreshed cookie (keepAlive).
func wantsUpgrade(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// --- Handlers ---------------------------------------------------------------

// LoginPage serves the form. Already signed in, it sends the browser on rather
// than showing a login screen to someone who is logged in.
func (a *Auth) LoginPage(w http.ResponseWriter, r *http.Request) {
	if a.sessionValid(r) {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusFound)
		return
	}
	a.render(w, r, "", http.StatusOK)
}

// Login checks the credentials and issues the session cookie.
func (a *Auth) Login(w http.ResponseWriter, r *http.Request) {
	// Defense in depth: where no password is offered this route is not
	// registered (internal/launch). This only stops a later mount from
	// bringing the account back; it is not what removes the password.
	if !a.OffersPassword() {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		a.render(w, r, "Unreadable form.", http.StatusBadRequest)
		return
	}

	// The budget before the bcrypt: a guess past it costs nothing.
	attempt, ok, wait := a.admit(r)
	if !ok {
		a.refused(r, "login-throttled", "[auth] login refused, too many attempts")
		w.Header().Set("Retry-After", retryAfter(wait))
		a.render(w, r, "Too many attempts. Wait a minute and try again.", http.StatusTooManyRequests)
		return
	}

	// Both comparisons run before either is looked at: a short-circuiting
	// `&&` would skip bcrypt for a wrong user name, a timing difference that
	// enumerates accounts.
	userOK := subtle.ConstantTimeCompare([]byte(r.PostFormValue("user")), []byte(a.cfg.User)) == 1
	passOK := bcrypt.CompareHashAndPassword([]byte(a.cfg.PasswordHash), []byte(r.PostFormValue("password"))) == nil

	if !userOK || !passOK {
		// One message for both failures, so an attacker cannot tell which
		// half to keep guessing.
		a.refused(r, "login-failed", "[auth] failed login")
		time.Sleep(a.delay)
		a.render(w, r, "Incorrect credentials.", http.StatusUnauthorized)
		return
	}
	a.admitted(w, r, attempt)

	// A sign-in starts the session's clock: issued is now, and every later
	// write of the cookie carries it forward. That is what the cap counts from.
	now := a.now()
	token, c := a.mint(now, now, "", byPassword)
	http.SetCookie(w, a.sessionCookie(token, c, now))
	log.Printf("[auth] successful login (%s)", a.cfg.User)

	// After the cookie is set, before the redirect: the login is granted by
	// now, so even a recovered panic leaves a usable session.
	a.afterLogin()

	// 303: a POST, followed by a GET.
	http.Redirect(w, r, safeNext(r.PostFormValue("next")), http.StatusSeeOther)
}

// afterLogin runs the caller's hook and swallows whatever it does.
//
// Login runs on net/http's handler goroutine, where a panic aborts the
// response: a panicking hook would turn every correct password into a reset
// connection. This makes OnLogin's "cannot fail a login" true.
func (a *Auth) afterLogin() {
	if a.cfg.OnLogin == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			log.Printf("[auth] the after-login hook panicked (%v). The login stands", p)
		}
	}()
	a.cfg.OnLogin()
}

// Logout ends a single sign-on session's record, then clears the cookie. A
// password session has nothing server-side to forget: a stolen one stays
// valid until its expiry or a key rotation. After this, a copy of a single
// sign-on cookie opens nothing.
//
// POST only, from this console only: SameSite=Lax sends the cookie on a
// top-level GET, so any link (an `<img>`, a chat message) could sign a person
// out. A POST meets api.RefuseForged (the X-KubeTower-Request marker and this
// console's Origin), as `POST /api/auth/logout` does. The console itself
// does not call this route.
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Log out with a POST: following a link does not end a session.", http.StatusMethodNotAllowed)
		return
	}
	if api.RefuseForged(w, r) {
		return
	}
	// The cookie is cleared only once the store has recorded the sign-out:
	// on a 503, clearing it would look signed out while the session is not,
	// and delete the cookie a retry needs.
	if err := a.endSession(r); err != nil {
		// Not the sign-in page as though it worked: it did not.
		a.render(w, r, signOutUnrecorded, http.StatusServiceUnavailable)
		return
	}
	a.clearSessionCookie(w)
	a.markSignedOut(w, true)
	http.Redirect(w, r, LoginPath, http.StatusFound)
}

// signedOutSuffix names the cookie that records a deliberate log-out, beside
// the session cookie's own name.
const signedOutSuffix = "_signed_out"

// markSignedOut sets, or with on false deletes, the cookie saying this browser
// logged out, so the sign-in page says "signed out" rather than "expired"
// (without the session cookie the two arrivals are the same request). It
// carries no identity and grants nothing; it lasts the browser session, and
// the first signed-in request deletes it.
func (a *Auth) markSignedOut(w http.ResponseWriter, on bool) {
	cookie := &http.Cookie{
		Name:     a.cfg.CookieName + signedOutSuffix,
		Value:    "1",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.secureCookies(),
	}
	if !on {
		cookie.Value = ""
		cookie.MaxAge = -1
	}
	http.SetCookie(w, cookie)
}

// loggedOut is whether this request carries the log-out record markSignedOut
// sets.
func (a *Auth) loggedOut(r *http.Request) bool {
	cookie, err := r.Cookie(a.cfg.CookieName + signedOutSuffix)
	return err == nil && cookie.Value == "1"
}

// clearSessionCookie deletes the session cookie from the browser.
func (a *Auth) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.cfg.CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		// The flags it was set with, so the deletion is the same cookie's.
		Secure: a.secureCookies(),
	})
}

// signOutUnrecorded is what a person is told when their sign-out reached a
// store that did not answer: plainly, because nothing was signed out and the
// browser still holds the cookie a retry needs.
const signOutUnrecorded = "The console could not log this session out: its session store did not " +
	"answer, so the session is still live and this browser is still signed in. Log out again in a moment."

// who is the person a live session names: the store record's for a single
// sign-on session, the shared account's for a password one.
func (a *Auth) who(s session) (identity.Who, bool) {
	if s.sid == "" {
		method := identity.Password
		if s.opened != byPassword {
			method = identity.Launch
		}
		return identity.Who{Name: a.cfg.User, Method: method, Window: s.window()}, a.cfg.User != ""
	}
	// The groups travel with the name because the review asserts both: a
	// person reviewed without their groups, or with another session's, is
	// reviewed as somebody else. Copied, so nothing downstream edits the record.
	groups := append([]string(nil), s.rec.Groups...)
	return identity.Who{Name: s.rec.Name, Method: identity.SSO, Groups: groups, Session: sessionTag(s.sid)},
		s.rec.Name != ""
}

// sessionTag is identity.Who.Session for a single sign-on session id: the
// first sixteen bytes of a labeled SHA-256, in hex. One-way, so no Who
// reveals the id.
func sessionTag(sid string) string {
	sum := sha256.Sum256([]byte("kubetower-session-tag\x00" + sid))
	return hex.EncodeToString(sum[:16])
}

// endSession forgets the store record a request's cookie names, if any. The
// signature is checked first, so a forged cookie cannot end somebody else's
// session by naming its id.
//
// It answers the store's failure so both sign-out routes can say the
// sign-out was not recorded, instead of a 204 while a copy of the cookie
// kept working. Nil where there was nothing to end.
func (a *Auth) endSession(r *http.Request) error {
	cookie, err := r.Cookie(a.cfg.CookieName)
	if err != nil {
		return nil
	}
	if c, ok := a.parseToken(cookie.Value); ok && c.sid != "" {
		// Who it was, read before the record is gone, for the hook below.
		rec, live, _ := a.live(r.Context(), c, a.now())
		if err := a.store.end(r.Context(), c.sid); err != nil {
			log.Printf("[auth] a single sign-on session could NOT be signed out in the store; its cookie "+
				"is kept so the browser can try again, and the sign-out answered 503: %v", err)
			return err
		}
		log.Printf("[auth] a single sign-on session was signed out")
		if live && rec.Name != "" {
			a.afterSignOut(identity.Who{Name: rec.Name, Method: identity.SSO, Session: sessionTag(c.sid)})
		}
	}
	return nil
}

// afterSignOut runs the sign-out hook and swallows whatever it does, for the
// reason afterLogin gives: the sign-out has been recorded by then, and a hook
// that panicked must not turn it into a reset connection.
func (a *Auth) afterSignOut(who identity.Who) {
	if a.signedOut == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			log.Printf("[auth] the sign-out hook panicked (%v). The sign-out stands", p)
		}
	}()
	a.signedOut(who)
}

// --- Redirect target --------------------------------------------------------

// safeNext reduces the ?next= parameter to somewhere on this server, or to "/".
// The parameter returns an expired session to the page it was on. It is also
// a string an attacker chooses, put in a Location header of a trusted host:
// the shape of an open redirect.
//
// `HasPrefix(next, "/") && !HasPrefix(next, "//")` has a hole: Go's URL
// parser reads `\` as a path character, while browsers (WHATWG) treat it as
// `/`, so `/\evil.example` resolves to `http://evil.example/`. So the check
// is made of bytes, not of a parse:
//
//   - a single leading "/" (rules out "https://…" and "javascript:…");
//   - a second byte that is neither "/" nor "\", the forms that turn a path
//     into an authority;
//   - no control characters: browsers strip tab, CR and LF before parsing,
//     so "/\t/evil.example" would reach the network as "//evil.example".
func safeNext(next string) string {
	if next == "" || next[0] != '/' {
		return "/"
	}
	if len(next) > 1 && (next[1] == '/' || next[1] == '\\') {
		return "/"
	}
	for i := 0; i < len(next); i++ {
		if next[i] < 0x20 || next[i] == 0x7f {
			return "/"
		}
	}
	return next
}

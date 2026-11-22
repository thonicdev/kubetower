package auth

// The native window's one-time sign-in.
//
// On the desktop the native window arrives signed in, through a code in the
// address it opens. Trusting loopback alone is not enough: without a session,
// DNS rebinding and CSRF are open again. On the loopback desktop there is no
// password, and a second launch obtains a fresh code from the running
// instance (handoff.go), so a process mints as many codes as it is asked for.
//
//   - A code per window opening (IssueLaunchCode), put in that window's
//     address and nowhere else; only its digest is kept.
//   - Bound to the launch: codes live in this process's memory.
//   - Single use and short-lived: the check and the spending are one step
//     under one lock, so two racing requests open one session; an unpresented
//     code lapses after launchCodeLife. With 256 bits, guessing gains nothing.
//   - From loopback only, by the connecting address, never X-Forwarded-For:
//     the desktop has no proxy, and a header is the client's to choose.
//   - Only where the shared account exists, since the session is that
//     account's. The route is not registered under `--incluster`.
//
// The session cookie of a native window carries a fourth field, `window`, in
// its signed payload (windowMark), which becomes identity.Who.Window for a
// check that wants a factor the password does not give. It survives the
// sliding refresh and cannot be added without the session key.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
)

const (
	// LaunchPath is where a launched window arrives, with its code in the
	// query under LaunchCodeParam.
	//
	// In the query, not the path: internal/launch logs the window's address
	// with the query dropped, so a code in the path would be in
	// `kubetower.log`.
	LaunchPath = api.LaunchPath

	// LaunchCodeParam is the query parameter carrying the code.
	LaunchCodeParam = "code"

	// launchCodeLife is how long an unpresented code stays good: the window
	// opens right after minting, and a minute covers a slow WebView2 or
	// WKWebView first start.
	launchCodeLife = time.Minute

	// maxLaunchCodes bounds the unspent codes a process holds, so a caller
	// minting in a loop cannot grow the map without limit.
	maxLaunchCodes = 64

	// windowMark is the fourth field of a window session's token payload,
	// where an SSO session carries its store id; a store id is exactly 32 hex
	// characters (isSessionID), so the two cannot be confused.
	windowMark = "window"

	// launchMark is the same field for a session a browser window's launch
	// code opened: the shared account, reached through the application.
	launchMark = "launch"

	// launchCodeBytes is the code's entropy: 256 bits, the size of every other
	// secret this package draws.
	launchCodeBytes = 32
)

// launchCodes are the unspent codes this process has minted, by digest.
type launchCodes struct {
	mu    sync.Mutex
	codes map[[sha256.Size]byte]launchCode
}

// launchCode is what is known about one unspent code: when it lapses, and
// whether it was minted for a native window rather than a browser.
type launchCode struct {
	lapses time.Time
	native bool
}

// IssueLaunchCode mints a single-use code for one window opening, which
// lapses after launchCodeLife. native marks the session it opens as the
// window's (identity.Who.Window). Refused where the shared account does not
// exist, since the session would be that account's.
func (a *Auth) IssueLaunchCode(native bool) (string, error) {
	if !a.OffersLaunch() {
		return "", errors.New("auth: no launch code here: not the desktop, or the shared account does not exist")
	}
	raw := make([]byte, launchCodeBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	code := base64.RawURLEncoding.EncodeToString(raw)

	now := a.now()
	a.launch.mu.Lock()
	defer a.launch.mu.Unlock()
	a.launch.prune(now)
	if len(a.launch.codes) >= maxLaunchCodes {
		return "", errors.New("auth: too many launch codes waiting to be used")
	}
	if a.launch.codes == nil {
		a.launch.codes = map[[sha256.Size]byte]launchCode{}
	}
	a.launch.codes[sha256.Sum256([]byte(code))] = launchCode{lapses: now.Add(launchCodeLife), native: native}
	return code, nil
}

// prune forgets every code that has lapsed. Called with the lock held.
func (l *launchCodes) prune(now time.Time) {
	for digest, c := range l.codes {
		if !now.Before(c.lapses) {
			delete(l.codes, digest)
		}
	}
}

// spendLaunchCode reports whether code is one this process minted, unspent and
// not lapsed, and spends it if so; and whether it was minted for a native
// window.
//
// The lookup is by SHA-256 digest, so what an attacker could time is the
// comparison of a hash they cannot steer towards a match.
func (a *Auth) spendLaunchCode(code string) (ok, native bool) {
	if code == "" {
		return false, false
	}
	digest := sha256.Sum256([]byte(code))
	now := a.now()
	a.launch.mu.Lock()
	defer a.launch.mu.Unlock()
	a.launch.prune(now)
	c, ok := a.launch.codes[digest]
	if !ok {
		return false, false
	}
	delete(a.launch.codes, digest)
	return true, c.native
}

// LaunchSignIn is `GET /auth/launch?code=…`: the native window's arrival.
//
// A valid code from loopback opens a session and redirects to the root, so
// the window's history carries no code. Anything else lands on the sign-in
// page with the same response whichever check failed (spent, lapsed, from
// another launch, guessed, not from this machine).
//
// The log never carries the code, nor the query it arrived in.
func (a *Auth) LaunchSignIn(w http.ResponseWriter, r *http.Request) {
	// No cache may keep either answer, and no page reached from here may be
	// told the code's address. Set here too, so it holds without
	// internal/web in front.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")

	// Defense in depth: where launches are not offered the route is not
	// registered; this stops a later mount bringing the account back.
	if !a.OffersLaunch() {
		http.NotFound(w, r)
		return
	}

	// Spent before the address is checked, so a code presented from the
	// wrong place is gone too: the window lands on the sign-in page and says
	// so, rather than racing whoever held its secret.
	ok, native := a.spendLaunchCode(r.URL.Query().Get(LaunchCodeParam))
	if ok && !fromLoopback(r) {
		a.refusals.say(a.now(), "launch-remote", "[auth] a window's one-time code was presented from %s, "+
			"which is not this machine; it is spent and no session was opened", r.RemoteAddr)
		ok = false
	}
	if !ok {
		a.refusals.say(a.now(), "launch", "[auth] a window sign-in was refused (a spent or lapsed code, "+
			"one from another launch, or none) from %s; sent to the sign-in page", a.clientLine(r))
		http.Redirect(w, r, LoginPath, http.StatusFound)
		return
	}

	now := a.now()
	opened := byLaunch
	if native {
		opened = byWindow
	}
	token, c := a.mint(now, now, "", opened)
	http.SetCookie(w, a.sessionCookie(token, c, now))
	if native {
		log.Printf("[auth] a native window signed in with a one-time code (%s)", a.cfg.User)
	} else {
		log.Printf("[auth] a browser window this process opened signed in with a one-time code (%s)", a.cfg.User)
	}
	// Not afterLogin: its hook deletes the drawn first-run password once
	// somebody used it, and nobody has; a person who never saw it still needs
	// the file to open the console in a browser.
	http.Redirect(w, r, "/", http.StatusFound)
}

// OfferLaunches says this console opens its own windows: the desktop profile.
// Called once, before the front door serves, never under --incluster. Off
// until called, so a profile that forgets has no launch route.
func (a *Auth) OfferLaunches() { a.launchOffered = true }

// OffersLaunch is whether a one-time launch code can be minted and spent here:
// launches offered, and the shared account they sign in as exists.
func (a *Auth) OffersLaunch() bool { return a.launchOffered && a.localAccount() }

// fromLoopback reports whether the request's connecting address is this
// machine's. RemoteAddr only: a forwarded header is chosen by the client.
func fromLoopback(r *http.Request) bool {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	return ap.Addr().Unmap().IsLoopback()
}

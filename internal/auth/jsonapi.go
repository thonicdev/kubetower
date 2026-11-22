package auth

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"golang.org/x/crypto/bcrypt"
)

// The front door, answered in JSON for the single-page front end.
//
// The session is this package's: the cookie's flags (sessionCookie), the
// sliding expiry and cap, the constant-time comparison, the failure delay,
// and the rule that a rejected login never says which half was wrong. A
// second login handler elsewhere would be a second set of those decisions.
// internal/api owns the contract (address, mutation guard, refusal shape);
// these methods satisfy api.Sessions and nothing more.

// LoginJSON exchanges a credential for the session cookie and answers 204.
//
// No token is ever returned in the body: the cookie is HttpOnly so one XSS
// flaw cannot exfiltrate a credential reaching a production cluster, and a
// body carrying it would undo that.
func (a *Auth) LoginJSON(w http.ResponseWriter, r *http.Request) {
	// Defense in depth: internal/api does not register this route where no
	// password is offered.
	if !a.OffersPassword() {
		api.WriteProblem(w, http.StatusNotFound, "not_found", "Not found",
			"no operation at "+r.Method+" "+r.URL.Path)
		return
	}
	var body struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		api.WriteProblem(w, http.StatusBadRequest, "invalid_request",
			"Bad request", "the credential could not be read: "+err.Error())
		return
	}

	// The budget before the bcrypt, answered 429 `rate_limited`.
	attempt, ok, wait := a.admit(r)
	if !ok {
		a.refused(r, "login-throttled", "[auth] login refused, too many attempts")
		w.Header().Set("Retry-After", retryAfter(wait))
		api.WriteProblem(w, http.StatusTooManyRequests, api.ProblemRateLimited,
			"Too many attempts", "too many sign-in attempts from this address; wait and try again")
		return
	}

	// Both comparisons run before either is looked at: a short-circuiting
	// `&&` would skip bcrypt for a wrong user name, a timing difference that
	// enumerates accounts.
	userOK := subtle.ConstantTimeCompare([]byte(body.User), []byte(a.cfg.User)) == 1
	passOK := bcrypt.CompareHashAndPassword([]byte(a.cfg.PasswordHash), []byte(body.Password)) == nil

	if !userOK || !passOK {
		a.refused(r, "login-failed", "[auth] failed login")
		time.Sleep(a.delay)
		// One message for both failures, or the login becomes an
		// account-existence oracle.
		api.WriteProblem(w, http.StatusUnauthorized, "not_authenticated",
			"Refused", "the credentials were not accepted")
		return
	}

	now := a.now()
	token, c := a.mint(now, now, "", byPassword)
	http.SetCookie(w, a.sessionCookie(token, c, now))
	log.Printf("[auth] successful login (%s)", a.cfg.User)
	a.admitted(w, r, attempt)
	a.afterLogin()
	w.WriteHeader(http.StatusNoContent)
}

// LogoutJSON clears the cookie and answers 204 whether or not a session was
// live - with one exception, below.
//
// A logout failing on an expired session would leave a front end unable to
// reach its login screen.
//
// The exception: a sign-out the store did not record answers 503
// `session_store_unreachable` and keeps the cookie, because the session is
// still live and a retry needs it; clearing it would look signed out while
// the session is not.
func (a *Auth) LogoutJSON(w http.ResponseWriter, r *http.Request) {
	// The cookie is cleared only once the store has recorded the sign-out.
	if err := a.endSession(r); err != nil {
		w.Header().Set("Retry-After", "5")
		api.WriteProblem(w, http.StatusServiceUnavailable, api.ProblemStoreUnreachable,
			"Log-out not recorded", signOutUnrecorded)
		return
	}
	a.clearSessionCookie(w)
	a.markSignedOut(w, true)
	w.WriteHeader(http.StatusNoContent)
}

// Current is the live session, with both of its bounds.
//
// The front end needs both: an idle one it can push back by acting, and an
// absolute one it cannot, or the operator is signed out mid-sentence.
func (a *Auth) Current(r *http.Request) (api.Session, bool) {
	c, ok, _ := a.session(r)
	if !ok {
		// A store that could not answer never reaches here: the guard in front
		// has already answered 503.
		return api.Session{}, false
	}
	if c.sid != "" {
		rec := c.rec
		groups := append([]string{}, rec.Groups...)
		return api.Session{
			Identity:          rec.Name,
			Method:            api.SessionMethodSSO,
			Groups:            groups,
			IdleExpiresAt:     c.expires,
			AbsoluteExpiresAt: c.issued.Add(a.ssoMax),
		}, true
	}
	method := api.SessionMethodPassword
	if c.opened != byPassword {
		method = api.SessionMethodLaunch
	}
	return api.Session{
		Identity:          a.cfg.User,
		Method:            method,
		Groups:            []string{},
		IdleExpiresAt:     c.expires,
		AbsoluteExpiresAt: c.issued.Add(a.cfg.SessionMax),
	}, true
}

package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The known-browser lane.
//
// Behind an anonymous ingress every password attempt shares one process
// queue, and a single host flooding it at a hundred requests a second refuses
// about 94 % of everybody's sign-ins. A browser that has signed in with the
// password before gets a lane the flood cannot fill (OWASP's device-cookie
// pattern):
//
//   - a successful password sign-in sets the cookie: a random id and its issue
//     time, signed with the session key, HttpOnly, SameSite=Strict, Secure
//     where the session cookie is or over TLS. It holds no secret and signs
//     nobody in; it only decides which budget an attempt counts against;
//   - an attempt carrying a valid one takes that browser's own budget and a
//     reserved share of the hashing capacity, never the anonymous queue;
//   - anything else (none, forged, expired, foreign) is anonymous.
//
// A guesser never had the cookie; one stolen with its browser is worth five
// guesses a minute at most.

const (
	// knownCookie is the cookie's name.
	knownCookie = "kubetower_known_browser"
	// knownVersion opens its signed payload, so a session token and a
	// known-browser cookie can never be taken for each other.
	knownVersion = "kb1"
	// knownTTL is how long a browser stays known after its last sign-in.
	knownTTL = 180 * 24 * time.Hour

	// knownBurst attempts at once from one known browser, then one more every
	// knownEvery.
	knownBurst = 5
	knownEvery = 12 * time.Second
	// knownQueueBurst attempts at once from every known browser together, then
	// one more every knownQueueEvery (two a second): their reserved share of
	// the hashing capacity, beside the anonymous queue's five a second.
	knownQueueBurst = 4
	knownQueueEvery = 500 * time.Millisecond
)

// knownBrowser reads a valid known-browser cookie off the request, and answers
// its id.
func (a *Auth) knownBrowser(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(knownCookie)
	if err != nil {
		return "", false
	}
	payload, signature, found := strings.Cut(cookie.Value, "|")
	if !found {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(signature), []byte(a.sign(knownVersion+"\x00"+payload))) != 1 {
		return "", false
	}
	fields := strings.Split(payload, ".")
	if len(fields) != 3 || fields[0] != knownVersion || len(fields[1]) != 32 {
		return "", false
	}
	issued, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || a.now().Sub(time.Unix(issued, 0)) > knownTTL {
		return "", false
	}
	return fields[1], true
}

// rememberBrowser sets, or renews, the known-browser cookie after a correct
// password. A browser that already carries a valid one keeps its id, so its
// budget follows it.
func (a *Auth) rememberBrowser(w http.ResponseWriter, r *http.Request) {
	id, ok := a.knownBrowser(r)
	if !ok {
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			return
		}
		id = hex.EncodeToString(raw)
	}
	payload := knownVersion + "." + id + "." + strconv.FormatInt(a.now().Unix(), 10)
	http.SetCookie(w, &http.Cookie{
		Name:     knownCookie,
		Value:    payload + "|" + a.sign(knownVersion+"\x00"+payload),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(knownTTL.Seconds()),
		Secure:   r.TLS != nil || a.secureCookies(),
	})
}

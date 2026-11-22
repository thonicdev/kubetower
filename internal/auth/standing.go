package auth

import (
	"context"
	"net/http"
	"time"

	"github.com/thonicdev/kubetower/internal/identity"
)

// Standing puts on each request a way to ask, later, whether the sign-in it
// was made under still stands (identity.Standing). It wraps what Middleware
// lets through, so the cookie it reads has already been verified once.
//
// For what outlives its request: a pod exec or a log follow is authenticated
// at the upgrade and never passes the front door again, so a sign-out on
// another replica, a session past its cap or a changed name mapping would
// leave the socket open. The handlers ask this while they run and close in
// their own frame (docs/SERVER-MODE-THREAT-MODEL.md §8.2), with a status
// the browser can report.
func (a *Auth) Standing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(a.cfg.CookieName)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		token := cookie.Value
		next.ServeHTTP(w, r.WithContext(identity.WithStanding(r.Context(),
			func(ctx context.Context) (bool, error) { return a.stands(ctx, token, a.now()) })))
	})
}

// stands is live, asked about a session that is in use somewhere else.
//
// Everything but the cookie's idle deadline: the token in hand is the one the
// stream opened with, while the browser's other requests have been
// refreshing a newer one. What is asked is the cap from the issue time and,
// for a single sign-on session, the store record (deleted by a sign-out on
// every replica). A shared password session has no record and stands until
// its cap or a change of signing key.
func (a *Auth) stands(ctx context.Context, token string, now time.Time) (bool, error) {
	c, ok := a.parseToken(token)
	if !ok {
		return false, nil
	}
	c.expires = now.Add(time.Second)
	_, live, err := a.live(ctx, c, now)
	if err != nil {
		return false, err
	}
	return live, nil
}

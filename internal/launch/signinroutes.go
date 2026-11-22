package launch

import (
	"net/http"

	"github.com/thonicdev/kubetower/internal/auth"
)

// passwordForm is the password sign-in form, reduced to what registering it
// needs: whether a password is offered, and the handler that checks one.
//
// An interface rather than *auth.Auth so the decision below is tested by
// walking a router.
type passwordForm interface {
	OffersPassword() bool
	Login(w http.ResponseWriter, r *http.Request)
}

// mountPasswordForm registers `POST /auth/login`, and registers nothing where
// no password is offered (an identity provider and no kept local account).
//
// An unregistered route is a property of the router, true for every method and
// visible to mux.Handler, where a refusing handler is one code path.
// internal/api decides `POST /api/auth/login` from the same OffersPassword, so
// the two cannot disagree.
//
// `GET /auth/login` stays unconditional: it is the sign-in page, which offers
// single sign-on when there is no password.
func mountPasswordForm(mux *http.ServeMux, front passwordForm) {
	if !front.OffersPassword() {
		return
	}
	mux.HandleFunc("POST "+auth.LoginPath, front.Login)
}

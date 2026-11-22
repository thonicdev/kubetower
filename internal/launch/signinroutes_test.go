package launch

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thonicdev/kubetower/internal/auth"
)

// An identity provider removes the shared password unless it is kept - on the
// password form, walked the way profileroutes_test.go walks the shell upgrades.
// `internal/api/signinroutes_test.go` holds the same decision for `POST
// /api/auth/login`; both spellings of the password route follow the one
// `OffersPassword`, so they cannot disagree about whether the account exists.

type formFront struct{ offers bool }

func (f formFront) OffersPassword() bool                       { return f.offers }
func (formFront) Login(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }

func TestThePasswordFormIsNotRegisteredWhereNoPasswordIsOffered(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, auth.LoginPath, nil)

	removed := http.NewServeMux()
	mountPasswordForm(removed, formFront{offers: false})
	if _, pattern := removed.Handler(req); pattern != "" {
		t.Errorf("POST %s is registered as %q with no password offered. The console removes the shared "+
			"account by not registering its route, never by a handler that refuses", auth.LoginPath, pattern)
	}

	kept := http.NewServeMux()
	mountPasswordForm(kept, formFront{offers: true})
	if _, pattern := kept.Handler(req); pattern != "POST "+auth.LoginPath {
		t.Errorf("POST %s is registered as %q with the password offered; a desktop, and a "+
			"deployment that kept the account, must keep it", auth.LoginPath, pattern)
	}
}

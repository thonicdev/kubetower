package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Sign-in routes, walked rather than served, for the reason profileroutes_test.go gives:
// an address that is not registered is a property of the router, and a handler
// that refuses is a property of one code path.
//
// The three shapes a console can be in, and what each registers:
//
//	no provider                  -> the password route, no single sign-on pair
//	provider, account not kept   -> the pair, and no password route (the default)
//	provider, account kept       -> both
func TestTheSessionRoutesFollowWhatIsOffered(t *testing.T) {
	password := route{http.MethodPost, "/api/auth/login"}
	start := route{http.MethodGet, "/api/auth/oidc/start"}
	callback := route{http.MethodGet, "/api/auth/oidc/callback"}
	// These two are registered whatever is offered: a logout must work from any
	// session, and the sign-in page has to be able to ask what it may draw.
	always := []route{{http.MethodPost, "/api/auth/logout"}, {http.MethodGet, "/api/auth/methods"}}

	cases := []struct {
		name    string
		signIn  SignIn
		present []route
		absent  []route
	}{
		{"a build with no SignIn at all is the password alone", nil,
			[]route{password}, []route{start, callback}},
		{"no provider", fakeSignIn{password: true},
			[]route{password}, []route{start, callback}},
		{"a provider, and the shared account not kept", fakeSignIn{sso: true},
			[]route{start, callback}, []route{password}},
		{"a provider, and the shared account kept on purpose", fakeSignIn{password: true, sso: true},
			[]route{password, start, callback}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := New(Deps{SignIn: tc.signIn})
			for _, r := range append(append([]route{}, always...), tc.present...) {
				if pattern := r.pattern(a); pattern == "" {
					t.Errorf("%s %s is not registered, and it should be", r.method, r.path)
				}
			}
			for _, r := range tc.absent {
				if pattern := r.pattern(a); pattern != "" {
					t.Errorf("%s %s is registered as %q, and it must not be: the sign-in rule removes it by "+
						"not registering it, never by a handler that refuses", r.method, r.path, pattern)
				}
			}
		})
	}
}

// And, at this router, the removed password route answers exactly what an
// address that never existed answers: the contract's JSON 404, byte for byte
// apart from the path it names. It is not a refusal written for the occasion.
//
// **Through the session guard the two differ, and that is not a leak**: the
// guard lets `/api/auth/login` through unauthenticated, as a public path, so it
// reaches this 404, while an unknown path gets the guard's 401. What the 404
// says, that no password is offered, is what `GET /api/auth/methods` already
// tells anybody who asks.
func TestARemovedPasswordRouteIsIndistinguishableFromNoRoute(t *testing.T) {
	a := New(Deps{SignIn: fakeSignIn{sso: true}})
	answer := func(path string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"user":"admin","password":"x"}`))
		req.Header.Set("X-KubeTower-Request", "1")
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, req)
		return rec.Code, strings.Replace(rec.Body.String(), path, "PATH", 1)
	}
	gotStatus, gotBody := answer("/api/auth/login")
	wantStatus, wantBody := answer("/api/auth/never-existed")
	if gotStatus != wantStatus || gotBody != wantBody {
		t.Errorf("the removed password route answers %d %s, and an address that never existed answers %d %s",
			gotStatus, gotBody, wantStatus, wantBody)
	}
	if gotStatus != http.StatusNotFound {
		t.Errorf("the removed password route answered %d, want 404", gotStatus)
	}
}

type route struct{ method, path string }

func (r route) pattern(a *API) string {
	_, pattern := a.mux.Handler(httptest.NewRequest(r.method, r.path, nil))
	return pattern
}

// The desktop's launch routes,
// walked at the router Mount fills: present only where SignIn offers them, and
// never under --incluster, where internal/launch offers neither.
func TestTheLaunchRoutesExistOnlyWhereOffered(t *testing.T) {
	launch := httptest.NewRequest(http.MethodGet, LaunchPath+"?code=x", nil)
	handoff := httptest.NewRequest(http.MethodPost, HandoffPath, nil)
	for _, c := range []struct {
		name                    string
		signIn                  SignIn
		wantLaunch, wantHandoff bool
	}{
		{"the loopback desktop", fakeSignIn{launch: true, handoffs: true}, true, true},
		{"a desktop that is not loopback", fakeSignIn{password: true, launch: true}, true, false},
		{"the server profile", fakeSignIn{password: true, sso: true}, false, false},
		{"no SignIn at all", nil, false, false},
	} {
		mux := http.NewServeMux()
		New(Deps{SignIn: c.signIn}).Mount(mux)
		if _, p := mux.Handler(launch); (p != "") != c.wantLaunch {
			t.Errorf("%s: GET %s is registered as %q; want present %v", c.name, LaunchPath, p, c.wantLaunch)
		}
		if _, p := mux.Handler(handoff); (p != "") != c.wantHandoff {
			t.Errorf("%s: POST %s is registered as %q; want present %v", c.name, HandoffPath, p, c.wantHandoff)
		}
	}
}

// A GET on the handoff reaches the
// handler, whose 405 the contract promises, and not the front end's
// catch-all, which would answer it with the single-page shell.
func TestEveryMethodOnTheHandoffReachesTheHandoff(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) {}) // the front end's catch-all
	New(Deps{SignIn: fakeSignIn{launch: true, handoffs: true}}).Mount(mux)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodHead} {
		if _, p := mux.Handler(httptest.NewRequest(method, HandoffPath, nil)); p != HandoffPath {
			t.Errorf("%s %s is answered by %q; want the handoff, %q", method, HandoffPath, p, HandoffPath)
		}
	}
}

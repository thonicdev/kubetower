package auth

// The launch and handoff paths are public only where they exist. Under
// --incluster they are guarded like any other path, rather than public and
// absent - which would answer an anonymous request with the single-page
// shell.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
)

func reachesUnsigned(a *Auth, method, path string) bool {
	reached := false
	guarded := a.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Accept", "text/html")
	guarded.ServeHTTP(httptest.NewRecorder(), req)
	return reached
}

func TestTheLaunchPathsArePublicOnlyWhereTheyExist(t *testing.T) {
	server := testAuth(t)
	server.launchOffered = false // as under --incluster, where OfferLaunches is never called
	if reachesUnsigned(server, http.MethodGet, LaunchPath) {
		t.Errorf("%s is public where launches are not offered", LaunchPath)
	}
	if reachesUnsigned(server, http.MethodPost, HandoffPath) {
		t.Errorf("%s is public where handoffs are not accepted", HandoffPath)
	}

	desktop := handingOff(t)
	if !reachesUnsigned(desktop, http.MethodGet, LaunchPath) {
		t.Errorf("%s is not public on the desktop, so a window with no session cannot arrive", LaunchPath)
	}
	if !reachesUnsigned(desktop, http.MethodPost, HandoffPath) {
		t.Errorf("%s is not public on the loopback desktop, so a second launch cannot reach it", HandoffPath)
	}
}

// shellMarker is what the stand-in for the front end's catch-all writes, so a
// response it wrote can be told apart from one the guard wrote.
const shellMarker = "<!-- the single-page shell -->"

// console is the process's router as internal/launch composes it, reduced to
// what this question turns on: the launch routes api.Mount registers from this
// front door's own answer, the front end's catch-all behind them - which
// answers every path nothing else claims with index.html - and this front
// door's guard around both, as `front.Middleware(mux)` does in run.go.
//
// **The router and the guard are asked of the same Auth.** A test through this
// therefore fails if the route's registration and the guard's exemption stop
// reading the same answer.
func console(a *Auth) (http.Handler, *http.ServeMux) {
	mux := http.NewServeMux()
	api.New(api.Deps{SignIn: a}).Mount(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, shellMarker)
	})
	return a.Middleware(mux), mux
}

// navigate is a browser with no session asking for path, from this machine.
func navigate(h http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Accept", "text/html")
	req.RemoteAddr = loopback
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Where `/auth/launch` is not mounted, an anonymous request for it is answered
// exactly as an unregistered path is - a 302 to the sign-in page carrying
// `next` - and never with the single-page shell.
//
// A path public on every profile would be let through by the guard with
// nothing mounted there, and the catch-all would answer 200 with index.html,
// whose asset names fingerprint the build. The test above asks
// the guard alone whether a request gets past it; this one asks the whole
// composition what the person receives, with and without a code, beside two
// paths that never existed - so the launch path is compared with the answer it
// must equal rather than with a constant somebody could update to match.
//
// The three front doors without the route:
//
//	the server profile, shared account    launches never offered (--incluster)
//	the server profile, single sign-on    neither offered nor an account to open
//	single sign-on, launches offered      offered, but no shared account for a code to open
func TestAnUnmountedLaunchRouteAnswersAsAnUnregisteredPath(t *testing.T) {
	serverShared := testAuth(t)
	serverShared.launchOffered = false // as under --incluster, where OfferLaunches is never called
	ssoOnly := func(offered bool) *Auth {
		a := ssoAuth(t, newFakeIdP(t, "/dex"), nil)
		if offered {
			a.OfferLaunches()
		}
		return a
	}
	for _, c := range []struct {
		name  string
		front *Auth
	}{
		{"the server profile, with the shared account", serverShared},
		{"the server profile, with single sign-on and no shared account", ssoOnly(false)},
		{"single sign-on with no shared account, launches offered", ssoOnly(true)},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, mux := console(c.front)
			// The premise, checked rather than assumed: only the catch-all is
			// at the address, so what follows is about an unmounted route and
			// not about a mounted one that refuses.
			if _, p := mux.Handler(httptest.NewRequest(http.MethodGet, LaunchPath, nil)); p != "/" {
				t.Fatalf("GET %s is answered by %q here; this test is about where it is not mounted", LaunchPath, p)
			}
			for _, path := range []string{
				LaunchPath,
				LaunchPath + "?" + LaunchCodeParam + "=x",
				"/never-existed",
				LaunchPath + "x",
			} {
				rec := navigate(h, path)
				want := LoginPath + "?next=" + url.QueryEscape(path)
				if got := rec.Header().Get("Location"); rec.Code != http.StatusFound || got != want {
					t.Errorf("GET %s answered %d, Location %q; want 302 to %q, as any unregistered path",
						path, rec.Code, got, want)
				}
				if strings.Contains(rec.Body.String(), shellMarker) {
					t.Errorf("GET %s was answered with the single-page shell", path)
				}
			}
		})
	}
}

// And where the route is mounted, the same composition still signs the window
// in. Without this, the test above would pass just as
// well on a console that guarded the launch path everywhere - where no window
// could ever arrive.
func TestAMountedLaunchRouteStillSignsTheWindowIn(t *testing.T) {
	a := testAuth(t) // the desktop: launches offered, and the shared account exists
	h, mux := console(a)
	if _, p := mux.Handler(httptest.NewRequest(http.MethodGet, LaunchPath, nil)); p != "GET "+LaunchPath {
		t.Fatalf("GET %s is answered by %q on the desktop; want its own route", LaunchPath, p)
	}

	code, err := a.IssueLaunchCode(true)
	if err != nil {
		t.Fatalf("IssueLaunchCode: %v", err)
	}
	rec := navigate(h, LaunchPath+"?"+LaunchCodeParam+"="+code)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" || sessionCookieOf(a, rec) == nil {
		t.Errorf("a window's code answered %d, Location %q, session cookie %v; want 302 to / with a session",
			rec.Code, rec.Header().Get("Location"), sessionCookieOf(a, rec) != nil)
	}

	// A code that is no code is the handler's refusal, not the guard's: the
	// sign-in page with no `next`, which is how the two answers differ.
	rec = navigate(h, LaunchPath+"?"+LaunchCodeParam+"=x")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != LoginPath {
		t.Errorf("a refused code answered %d, Location %q; want 302 to %q from the launch handler",
			rec.Code, rec.Header().Get("Location"), LoginPath)
	}
}

// The menu's reads ask the console's own access reviews in the in-cluster
// profile (internal/web/listable.go), so they must sit
// behind a session: an anonymous request reaching them would make the console
// spend its ServiceAccount's reviews for nobody.
func TestTheMenuReadsAreNotPublicInTheServerProfile(t *testing.T) {
	server := testAuth(t)
	server.launchOffered = false // as under --incluster
	for _, path := range []string{"/api/c/in-cluster/nav", "/api/c/in-cluster/nav/custom-resources"} {
		if reachesUnsigned(server, http.MethodGet, path) {
			t.Errorf("%s reached its handler with no session", path)
		}
	}
}

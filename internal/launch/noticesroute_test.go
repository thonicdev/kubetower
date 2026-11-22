package launch

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thonicdev/kubetower/internal/web"
)

// outerFor builds the router in front of the guard with stand-ins: a guard
// that refuses everything, and notices that answer one word.
func outerFor(inCluster bool) *http.ServeMux {
	outer := http.NewServeMux()
	guard := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "session missing", http.StatusUnauthorized)
	})
	notices := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("notices")) })
	mountOuter(outer, guard, notices, inCluster)
	return outer
}

func status(mux http.Handler, method, path string) int {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Code
}

// On the desktop the notices need no session, for GET and HEAD only. Every
// other method on the path, and every neighbor of it, meets the guard.
func TestTheDesktopServesTheNoticesWithoutASession(t *testing.T) {
	outer := outerFor(false)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if got := status(outer, method, web.NoticesPath); got != http.StatusOK {
			t.Errorf("%s %s = %d, want 200 without a session", method, web.NoticesPath, got)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodOptions, http.MethodDelete} {
		if got := status(outer, method, web.NoticesPath); got != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want the guard's 401", method, web.NoticesPath, got)
		}
	}
	for _, path := range []string{web.NoticesPath + "/", web.NoticesPath + "x", "/", "/assets/index-Chmc5KSx.js"} {
		if got := status(outer, http.MethodGet, path); got != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, want the guard's 401", path, got)
		}
	}
}

// Under --incluster nothing is in front of the guard: the notices take a
// session like every page.
func TestTheServerProfileKeepsTheNoticesBehindSignIn(t *testing.T) {
	outer := outerFor(true)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
		if got := status(outer, method, web.NoticesPath); got != http.StatusUnauthorized {
			t.Errorf("%s %s = %d under --incluster, want the guard's 401", method, web.NoticesPath, got)
		}
	}
	req := httptest.NewRequest(http.MethodGet, web.NoticesPath, nil)
	if _, pattern := outer.Handler(req); pattern != "/" {
		t.Errorf("under --incluster %s is routed by %q, want only the guard's \"/\"", web.NoticesPath, pattern)
	}
}

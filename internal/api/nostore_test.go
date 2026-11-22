package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// **No answer under /api/ may be kept in the browser's disk cache**. Helm values, ConfigMap data and plain
// environment values are answers to a GET, and a GET with no Cache-Control is
// one a browser may store and keep after sign-out, readable by the next person
// at that profile or by anything that reads the profile's cache directory.
//
// Walked over the contract itself - every operation it declares, in the
// conformance test's own order - so an operation added to the contract is held
// to it without anybody remembering to add it here.
func TestEveryAnswerUnderTheAPIPrefixIsNotStored(t *testing.T) {
	spec := loadSpec(t)
	checked, outside := 0, 0
	walkContract(t, spec, func(op operation, _ exercise, response *http.Response, _ []byte) {
		if !strings.HasPrefix(op.path, Prefix) {
			// The probes and the desktop's launch routes, which are not under
			// the API's mount; the launch routes set their own no-store.
			outside++
			return
		}
		checked++
		if got := response.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s (%s %s) answered %d with Cache-Control %q, want no-store",
				op.id, op.method, op.path, response.StatusCode, got)
		}
	})
	if checked == 0 {
		t.Fatal("no operation under /api/ was walked; the check above checked nothing")
	}
	t.Logf("%d operations under %s checked; %d outside it were not asked", checked, Prefix, outside)
}

// And the answers that are not an operation's: a miss, and a mutation refused
// at the door. Both are written by this package before any handler runs.
func TestARefusalAndAMissUnderTheAPIPrefixAreNotStoredEither(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{}).Mount(mux)

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/nothing-here", nil),
		// No X-KubeTower-Request: refused by guard.
		httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil),
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s %s answered %d with Cache-Control %q, want no-store",
				request.Method, request.URL.Path, recorder.Code, got)
		}
	}
}

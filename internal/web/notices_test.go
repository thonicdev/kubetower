package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// noticesMux mounts the notices the way Mount does, beside a catch-all that
// records whatever reaches it, so a request the notices route must not answer
// is seen to go elsewhere.
func noticesMux(t *testing.T, ui fstest.MapFS) (*http.ServeMux, *string) {
	t.Helper()
	s := &Server{ui: ui}
	mux := http.NewServeMux()
	mux.Handle("GET "+NoticesPath, s.NoticesHandler())
	fell := ""
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fell = r.URL.Path
		http.NotFound(w, r)
	}))
	return mux, &fell
}

func TestNoticesAreServedFromTheBundle(t *testing.T) {
	mux, _ := noticesMux(t, fstest.MapFS{
		"index.html":              &fstest.MapFile{Data: []byte("<!doctype html>")},
		"THIRD_PARTY_NOTICES.txt": &fstest.MapFile{Data: []byte("KubeTower - third-party notices\n")},
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, NoticesPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type %q, want text/plain", got)
	}
	if !strings.HasPrefix(rec.Body.String(), "KubeTower - third-party notices") {
		t.Errorf("body %q is not the notices", rec.Body.String())
	}
}

// A build without the file says so, rather than answering the console's page
// on a path the front door lets through without a session.
func TestNoticesAbsentAnswerNotFoundInWords(t *testing.T) {
	mux, fell := noticesMux(t, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>")}})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, NoticesPath, nil))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no third-party notices") {
		t.Errorf("status %d, body %q: want a 404 that says the build carries no notices", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "<!doctype") {
		t.Error("the console's page was served on the public notices path")
	}
	if *fell != "" {
		t.Errorf("the request fell through to %q", *fell)
	}
}

// The route is one exact path. Anything beside it, below it or with another
// method is not answered by it, so the public exemption reads nothing else.
func TestNoticesRouteIsOneExactPath(t *testing.T) {
	mux, fell := noticesMux(t, fstest.MapFS{
		"index.html":                &fstest.MapFile{Data: []byte("<!doctype html>")},
		"THIRD_PARTY_NOTICES.txt":   &fstest.MapFile{Data: []byte("notices")},
		"assets/secret-AAAABBBB.js": &fstest.MapFile{Data: []byte("secret")},
	})
	for _, path := range []string{
		NoticesPath + "/",
		NoticesPath + "/assets/secret-AAAABBBB.js",
		NoticesPath + "x",
		"/THIRD_PARTY_NOTICES",
	} {
		*fell = ""
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if strings.Contains(rec.Body.String(), "notices") || strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("%s answered %q from the bundle", path, rec.Body.String())
		}
		if *fell == "" {
			t.Errorf("%s was answered by the notices route", path)
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, NoticesPath, nil))
	if strings.Contains(rec.Body.String(), "notices") {
		t.Error("a POST was answered with the notices")
	}
}

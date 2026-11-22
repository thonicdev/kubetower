package web

import (
	"bytes"
	"io/fs"
	"net/http"
	"time"
)

// NoticesPath is where the console serves THIRD_PARTY_NOTICES.txt.
const NoticesPath = "/THIRD_PARTY_NOTICES.txt"

// noticesFile is where a release build writes THIRD_PARTY_NOTICES.txt into the
// bundle: the licenses of the linked Go modules and the bundled npm packages.
const noticesFile = bundleRoot + "/THIRD_PARTY_NOTICES.txt"

// Notices returns the third-party notices the bundle carries (ui as Run receives
// it, nil for the embedded tree). A build without them answers fs.ErrNotExist.
func Notices(ui fs.FS) ([]byte, error) {
	return fs.ReadFile(bundleFS(ui), noticesFile)
}

// NoticesHandler serves THIRD_PARTY_NOTICES.txt and nothing else: it reads one
// constant name once and takes nothing from the request, so no mount of it can
// read the rest of the bundle. A build without the file answers 404 in words.
//
// The router decides who may read it. Under `--incluster` it sits behind the
// session like every page, so an anonymous visitor does not learn every
// dependency's version; on the loopback desktop internal/launch also mounts it,
// GET only, in front of the session guard.
func (s *Server) NoticesHandler() http.Handler {
	text, err := Notices(s.ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if err != nil {
			http.Error(w, "this build carries no third-party notices: it was not built by a release "+
				"(release.yml, the Dockerfile, or `task build`)", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		http.ServeContent(w, r, "THIRD_PARTY_NOTICES.txt", time.Time{}, bytes.NewReader(text))
	})
}

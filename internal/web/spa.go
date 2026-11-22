package web

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Serving the compiled console, on two addresses:
//
//	GET /assets/   the bundle's own files, at the URL the bundle names
//	GET /          the shell: index.html for a navigation, 404 for a missing file
//
// `web/vite.config.ts` sets no `base`, so the built `index.html` points at
// absolute `/assets/...` URLs; serving them anywhere else would mean rewriting
// the HTML. The client router's base (`web/ui/src/router/base.ts`) must match the
// shell's prefix, or signing in lands on a router error; `basename_test.go`
// checks the two agree.
const (
	// spaPrefix is where the compiled console's shell answers;
	// `web/ui/src/router/base.ts` is its other half.
	spaPrefix = "/"
	// spaAssetPrefix is where the bundle's own <script> and <link> point.
	spaAssetPrefix = "/assets/"

	// bundleRoot is Vite's `outDir` inside the embedded tree, bundleIndex the file
	// whose presence means a build happened, and bundleDir the one directory under
	// it Vite fills.
	bundleRoot  = "dist/ui"
	bundleIndex = bundleRoot + "/index.html"
	bundleDir   = "assets"
)

// mountSPA registers the compiled console, and says at startup when there is
// none. The check runs once: an embed.FS cannot change under a running process.
func (s *Server) mountSPA(mux *http.ServeMux) {
	bundle := newSPAFiles(s.Bundle())
	if !bundle.built {
		// At startup: a build with no front end is a green `go build` and a healthy
		// process with no console (docs/DEFINITION-OF-DONE.md §14), and this line is the
		// only thing that says so before a browser opens.
		s.log("no front-end bundle in this binary: %s answers a \"UI not built\" page. "+
			"Run `task build:web` (or `corepack pnpm -C web run build`) and rebuild. "+
			"There is no other interface: this is the console", spaPrefix)
	}
	// No method on the shell's pattern: ServeMux panics on `GET /` beside `/api/`
	// ("matches fewer methods ... but has a more general path pattern"). So the
	// shell takes every method and methodGuard answers 405 to the rest.
	mux.Handle(spaPrefix, methodGuard(bundle.shell()))
	mux.Handle("GET "+spaAssetPrefix, bundle.asset())
}

// spaFiles is the embedded bundle. The fs.FS is a field so a test can serve both
// a built and an unbuilt bundle: an empty answer and a failed one must both be
// producible to be asserted.
type spaFiles struct {
	fsys fs.FS
	// built is computed once at construction.
	built bool
}

func newSPAFiles(fsys fs.FS) spaFiles {
	return spaFiles{fsys: fsys, built: spaBundlePresent(fsys)}
}

// spaBundlePresent reports whether a real build is embedded. `//go:embed
// all:dist` is satisfied by the committed `.gitkeep` alone, so it asks for a
// non-empty `index.html` and at least one file under `assets/`: an index naming
// scripts that are not in the binary renders nothing.
func spaBundlePresent(fsys fs.FS) bool {
	info, err := fs.Stat(fsys, bundleIndex)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return false
	}
	entries, err := fs.ReadDir(fsys, path.Join(bundleRoot, bundleDir))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			return true
		}
	}
	return false
}

// spaShell serves the single-page application's own addresses. An unknown path
// answers `index.html`, so a deep link reaches the client router; but a missing
// file under the bundle, or a request for an asset, answers 404, never HTML, or a
// missing script would surface as a MIME type error that reads like a corrupt
// build.
func (b spaFiles) shell() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !b.built {
			spaNotBuilt(w)
			return
		}
		// The decision reads these three headers, so a shared cache must key on all of
		// them.
		w.Header().Add("Vary", "Sec-Fetch-Dest, Sec-Fetch-Mode, Accept")

		rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, spaPrefix), "/")
		if rest == "" {
			b.index(w, r)
			return
		}
		if !fs.ValidPath(rest) {
			// ServeMux has already cleaned the path; defense in depth, since this builds a
			// file name out of a URL.
			spaMissing(w)
			return
		}
		if name := path.Join(bundleRoot, rest); b.exists(name) {
			b.serve(w, r, name)
			return
		}
		// Nothing there: which answer depends on what was asked for.
		if strings.HasPrefix(rest, bundleDir+"/") || spaWantsAsset(r, rest) {
			spaMissing(w)
			return
		}
		b.index(w, r)
	}
}

// spaAsset serves `/assets/`, with no fallback: every URL under it was written by
// Vite, so a miss is a file that is absent.
func (b spaFiles) asset() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !b.built {
			spaNotBuilt(w)
			return
		}
		rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, spaAssetPrefix), "/")
		if rest == "" || !fs.ValidPath(rest) {
			spaMissing(w)
			return
		}
		name := path.Join(bundleRoot, bundleDir, rest)
		if !b.exists(name) {
			spaMissing(w)
			return
		}
		b.serve(w, r, name)
	}
}

// spaIndex serves the shell document, 200 and not a redirect, so a deep link
// reaches the client router with its path intact.
func (b spaFiles) index(w http.ResponseWriter, r *http.Request) {
	raw, err := fs.ReadFile(b.fsys, bundleIndex)
	if err != nil {
		// Unreachable while `built` is true; an answer rather than a panic.
		spaNotBuilt(w)
		return
	}
	if tag := etagFor(b.fsys, "/"+bundleIndex); tag != "" {
		w.Header().Set("ETag", tag)
	}
	w.Header().Set("Cache-Control", spaCacheControl(bundleIndex))
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(raw))
}

// spaWantsAsset separates a navigation from a subresource, using three signals in
// order:
//
//   - `Sec-Fetch-Mode` and `Sec-Fetch-Dest`: `navigate` or `document` is an
//     address in the URL bar. A service worker rewrites both, so they can say
//     "page" and be believed, but not the reverse.
//   - `Accept` naming `text/html`, which only a document request does, and
//     which survives a service worker in every engine (internal/auth's
//     `isBackgroundRequest` reads it too).
//   - any other `Sec-Fetch-Dest`, then the extension, from a closed list: object
//     names contain dots (`/c/prod/pods/my-app.v1` is a navigation).
func spaWantsAsset(r *http.Request, rest string) bool {
	// The console's service worker (scope `/`) re-issues navigations, and Chrome
	// stamps them `Sec-Fetch-Dest: empty` while keeping `Sec-Fetch-Mode: navigate`.
	// Reading only Dest would 404 every reload and bookmark.
	if r.Header.Get("Sec-Fetch-Mode") == "navigate" {
		return false
	}
	// Firefox stamps the same re-issued navigation `Sec-Fetch-Mode: same-origin`,
	// the shape of a script's own `fetch()`; `Accept: text/html` survives in every
	// engine. A missing chunk still 404s: `/assets/` never reaches here, and no
	// engine asks for a script with `text/html`.
	if spaAcceptsHTML(r) {
		return false
	}
	switch r.Header.Get("Sec-Fetch-Dest") {
	case "":
		// Fall through to the extension test.
	case "document", "iframe", "frame":
		return false
	default:
		return true
	}
	return spaAssetExtensions[strings.ToLower(path.Ext(rest))]
}

// spaAcceptsHTML reports whether the request names HTML as acceptable, as a
// browser asks for a page. A range with `q=0` is a refusal and is skipped; `*/*`
// is not HTML, or every `fetch()` would be a page.
func spaAcceptsHTML(r *http.Request) bool {
	for _, value := range r.Header.Values("Accept") {
		for _, item := range strings.Split(value, ",") {
			mediaType, params, _ := strings.Cut(item, ";")
			switch strings.ToLower(strings.TrimSpace(mediaType)) {
			case "text/html", "application/xhtml+xml":
			default:
				continue
			}
			if !spaRefused(params) {
				return true
			}
		}
	}
	return false
}

// spaRefused reports whether a media range's parameters carry a quality of zero.
func spaRefused(params string) bool {
	for _, param := range strings.Split(params, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(param), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return err == nil && q == 0
	}
	return false
}

// spaAssetExtensions is what a browser fetches for a page rather than navigates
// to. Anything else is a navigation, the safe direction: HTML where a 404
// belonged, rather than a blank screen where the application belonged.
var spaAssetExtensions = map[string]bool{
	".js": true, ".mjs": true, ".cjs": true, ".css": true, ".map": true,
	".json": true, ".webmanifest": true, ".wasm": true,
	".svg": true, ".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".ico": true, ".webp": true, ".avif": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".txt": true, ".xml": true, ".mp4": true, ".webm": true,
}

// spaHashedName recognizes a content-addressed file name: Vite's stem, a hyphen
// and a base64url digest (`index-Chmc5-KS.js`). Such a name never means two
// files, which is what licenses `immutable`; anything else is mutable.
var spaHashedName = regexp.MustCompile(`-[A-Za-z0-9_-]{8,}\.`)

// spaCacheControl is the caching rule per class of file:
//
//   - `index.html` is `no-cache`: its URL never changes and it names the hashed
//     scripts, so a cached copy would pin an old build. With the ETag a check
//     costs a 304.
//   - A hashed asset is immutable for a year: changed content arrives under a
//     new URL.
//   - Everything else revalidates.
//
// Written by hand because an embedded file has no modification time (see
// staticcache.go).
func spaCacheControl(name string) string {
	base := path.Base(name)
	if base == "index.html" {
		return "no-cache"
	}
	if spaHashedName.MatchString(base) {
		return "public, max-age=31536000, immutable"
	}
	return "no-cache"
}

// spaServeFile writes one file out of the embedded bundle. The ETag is set
// before delegating, so `http.ServeContent` answers `If-None-Match` with a 304.
// etagFor is keyed by the path inside the embedded tree (`dist/ui/...`), so a
// bundle file cannot collide with a same-named file under `static/`.
func (b spaFiles) serve(w http.ResponseWriter, r *http.Request, name string) {
	raw, err := fs.ReadFile(b.fsys, name)
	if err != nil {
		spaMissing(w)
		return
	}
	if tag := etagFor(b.fsys, "/"+name); tag != "" {
		w.Header().Set("ETag", tag)
	}
	w.Header().Set("Cache-Control", spaCacheControl(name))
	if strings.HasSuffix(name, ".map") {
		// Go's type table has no `.map`: a source map is JSON.
		w.Header().Set("Content-Type", "application/json")
	}
	http.ServeContent(w, r, path.Base(name), time.Time{}, bytes.NewReader(raw))
}

func (b spaFiles) exists(name string) bool {
	info, err := fs.Stat(b.fsys, name)
	return err == nil && !info.IsDir()
}

// spaMissing is the 404 for a file the bundle does not contain. It names the
// bundle, so it is not mistaken for the router's 404 for an address nothing
// registered.
func spaMissing(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "not in the front-end bundle", http.StatusNotFound)
}

// spaNotBuilt is what a binary with no front end says on every address the front
// end owns: 503, since a 404 reads as a routing mistake and a 200 is a page a
// monitor calls healthy. No inline `<script>`: the Content-Security-Policy names
// every inline block by hash (headers.go), so one here would be blocked. The
// inline `<style>` is allowed by `style-src 'unsafe-inline'`.
func spaNotBuilt(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write(spaNotBuiltPage)
}

var spaNotBuiltPage = []byte(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>KubeTower — UI not built</title>
<style>
body { margin: 0; padding: 3rem 1.5rem; background: #14161a; color: #e6e8eb;
       font: 16px/1.6 system-ui, -apple-system, Segoe UI, sans-serif; }
main { max-width: 40rem; margin: 0 auto; }
h1 { font-size: 1.4rem; margin: 0 0 1rem; }
p { margin: 0 0 1rem; }
code { background: #1f232a; padding: .15rem .4rem; border-radius: .25rem; }
a { color: #7cc4ff; }
</style>
</head>
<body>
<main>
<h1>The front end is not in this binary</h1>
<p>This build carries no compiled console: <code>web/dist/ui/index.html</code>
was not embedded. That is the ordinary state of a checkout on which the front end
has never been built — the committed placeholder <code>web/dist/.gitkeep</code>
is what lets <code>go build</code> and <code>go test</code> work with no Node
installed.</p>
<p>Build it, then rebuild the binary:</p>
<p><code>task build:web</code> &nbsp;or&nbsp;
<code>corepack pnpm -C web run build</code></p>
<p>This is the whole console: there is no other interface to fall back to.</p>
</main>
</body>
</html>
`)

// methodGuard answers 405 for anything but a read. The shell is registered
// without a method, so it receives writes to unclaimed addresses; `Allow` tells a
// client which methods the address has.
func methodGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "this address serves the console and answers GET", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

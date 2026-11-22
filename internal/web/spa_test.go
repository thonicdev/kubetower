package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	webassets "github.com/thonicdev/kubetower/web"
)

// fakeBundle is a build that happened: an index naming one hashed script, that
// script, its map, its stylesheet, and one file with no digest in its name.
//
// Built here rather than read from `web/dist` so that these tests answer the
// same on a machine that has never run `pnpm build` — which is most of them, and
// is the state `web/dist/.gitkeep` exists to keep working.
func fakeBundle() fstest.MapFS {
	return fstest.MapFS{
		"dist/.gitkeep":                            &fstest.MapFile{},
		"dist/ui/index.html":                       &fstest.MapFile{Data: []byte(`<!doctype html><html><head><script type="module" crossorigin src="/assets/index-Chmc5KSx.js"></script></head><body><div id="root"></div></body></html>`)},
		"dist/ui/assets/index-Chmc5KSx.js":         &fstest.MapFile{Data: []byte("export const a = 1;\n")},
		"dist/ui/assets/index-Chmc5KSx.js.map":     &fstest.MapFile{Data: []byte(`{"version":3}`)},
		"dist/ui/assets/index-Daq3QabZ.css":        &fstest.MapFile{Data: []byte(":root{}\n")},
		"dist/ui/favicon.svg":                      &fstest.MapFile{Data: []byte("<svg/>")},
		"dist/ui/assets/nested/deep-AAAABBBBCC.js": &fstest.MapFile{Data: []byte("//\n")},
	}
}

// muxFor mounts one bundle on a router the same way Mount does, so the tests
// exercise the registration and not only the handlers.
func muxFor(bundle spaFiles) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET "+spaPrefix, bundle.shell())
	mux.Handle("GET "+spaAssetPrefix, bundle.asset())
	return mux
}

func get(t *testing.T, mux *http.ServeMux, target string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// **An empty `dist` is not a bundle, and the embed directive cannot say so.**
//
// docs/DEFINITION-OF-DONE.md §14: `//go:embed all:dist` is satisfied by the
// lone committed `.gitkeep`, so a binary carrying no front end compiles, links
// and starts. This is the assertion the build cannot make.
func TestABundleIsOnlyPresentWhenSomethingWasActuallyBuilt(t *testing.T) {
	for _, tc := range []struct {
		name string
		fsys fstest.MapFS
		want bool
	}{
		{
			name: "nothing at all",
			fsys: fstest.MapFS{},
		},
		{
			// The ordinary state of a fresh clone, and the one that must not
			// read as a working console.
			name: "the placeholder alone",
			fsys: fstest.MapFS{"dist/.gitkeep": &fstest.MapFile{}},
		},
		{
			name: "an index with no code behind it",
			fsys: fstest.MapFS{"dist/ui/index.html": &fstest.MapFile{Data: []byte("<html></html>")}},
		},
		{
			// The convincing one: a page that renders, and renders nothing. A
			// directory under `assets/` is not code, so this stays false.
			name: "an index and an assets directory holding no file",
			fsys: fstest.MapFS{
				"dist/ui/index.html":            &fstest.MapFile{Data: []byte("<html></html>")},
				"dist/ui/assets/sub/buried.txt": &fstest.MapFile{Data: []byte("x")},
			},
		},
		{
			name: "an empty index beside real code",
			fsys: fstest.MapFS{
				"dist/ui/index.html":        &fstest.MapFile{},
				"dist/ui/assets/index-A.js": &fstest.MapFile{Data: []byte("//")},
			},
		},
		{
			name: "a real build",
			fsys: fakeBundle(),
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := spaBundlePresent(tc.fsys); got != tc.want {
				t.Errorf("spaBundlePresent = %v, want %v", got, tc.want)
			}
		})
	}
}

// **A binary with no front end says so, and says it as 503.**
//
// Not 404, which reads as a routing mistake and sends the reader to the router.
// Not 200, which is the green build and broken release of
// docs/DEFINITION-OF-DONE.md §14. And never a blank page.
func TestWithNoBundleEveryFrontEndAddressSaysSoLoudly(t *testing.T) {
	mux := muxFor(newSPAFiles(fstest.MapFS{"dist/.gitkeep": &fstest.MapFile{}}))

	for _, target := range []string{
		"/",
		"/c/prod",
		"/assets/index-Chmc5KSx.js",
	} {
		rec := get(t, mux, target)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s answered %d, want 503: a build with no front end must not answer "+
				"like a working one", target, rec.Code)
		}
		body := rec.Body.String()
		if body == "" {
			t.Errorf("%s answered with an empty body, which is the blank page this exists to "+
				"prevent", target)
		}
		if !strings.Contains(body, "build:web") && !strings.Contains(body, "pnpm") {
			t.Errorf("%s does not say how to fix it; body = %q", target, body)
		}
		// A page explaining that something is missing must not itself be blocked
		// by the Content-Security-Policy, which names every inline script by
		// hash (headers.go).
		if strings.Contains(body, "<script") {
			t.Errorf("%s carries an inline script, which this process's CSP blocks", target)
		}
	}
}

// **The fallback is not a catch-all.** A navigation gets index.html so the
// client router can handle it; a subresource that is not there gets a 404.
//
// Answering a missing `assets/index-<hash>.js` with index.html produces a MIME
// type error on the `<script>` tag, which reads as a corrupt build rather than
// as a file that is absent (docs/DEFINITION-OF-DONE.md §3).
func TestANavigationGetsTheShellAndAMissingAssetGetsA404(t *testing.T) {
	mux := muxFor(newSPAFiles(fakeBundle()))
	const marker = `id="root"`

	navigations := []struct {
		target string
		header []string
	}{
		{target: "/"},
		{target: "/c/prod"},
		// A dot in a Kubernetes object name is not a file extension. A
		// dot-based rule answers this 404 and the client router never sees it.
		{target: "/c/prod/pods/my-app.v1"},
		// The browser says what it is doing with the answer, and it outranks
		// the extension.
		{target: "/c/prod/configmaps/nginx.conf", header: []string{"Sec-Fetch-Dest", "document"}},
		{target: "/report.js", header: []string{"Sec-Fetch-Dest", "document"}},
	}
	for _, nav := range navigations {
		rec := get(t, mux, nav.target, nav.header...)
		if rec.Code != http.StatusOK {
			t.Errorf("navigation %s answered %d, want 200 with the shell", nav.target, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), marker) {
			t.Errorf("navigation %s did not answer with index.html", nav.target)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("navigation %s answered Content-Type %q", nav.target, ct)
		}
	}

	subresources := []struct {
		target string
		header []string
	}{
		// The address the built index.html actually names, with one character
		// of the digest changed — a stale tab after a rebuild.
		{target: "/assets/index-Chmc5KSy.js"},
		{target: "/assets/nothing.css"},
		// The same file asked for through the shell prefix.
		{target: "/assets/index-Chmc5KSy.js"},
		{target: "/assets/index-Chmc5KSy.js", header: []string{"Sec-Fetch-Dest", "document"}},
		// No Sec-Fetch-Dest, so the extension decides.
		{target: "/vendor/thing.js"},
		{target: "/theme.css"},
		// And the browser saying it is fetching, not navigating.
		{target: "/c/prod", header: []string{"Sec-Fetch-Dest", "script"}},
		{target: "/c/prod", header: []string{"Sec-Fetch-Dest", "empty"}},
	}
	for _, sub := range subresources {
		rec := get(t, mux, sub.target, sub.header...)
		if rec.Code != http.StatusNotFound {
			t.Errorf("subresource %s %v answered %d, want 404. Answering it with index.html is "+
				"a MIME error in the browser, not something the SPA can recover from",
				sub.target, sub.header, rec.Code)
		}
		if strings.Contains(rec.Body.String(), marker) {
			t.Errorf("subresource %s came back as index.html", sub.target)
		}
	}
}

// A file that is really there is served, from either prefix, with its own type.
func TestTheBundlesOwnFilesAreServedWhereTheBundleNamesThem(t *testing.T) {
	mux := muxFor(newSPAFiles(fakeBundle()))

	for _, tc := range []struct {
		target      string
		wantType    string
		wantCaching string
	}{
		// `/assets/` is where the built index.html points, with a leading
		// slash. If this 404s, the page loads and renders nothing.
		{"/assets/index-Chmc5KSx.js", "text/javascript", "immutable"},
		{"/assets/index-Daq3QabZ.css", "text/css", "immutable"},
		{"/assets/index-Chmc5KSx.js.map", "application/json", "immutable"},
		{"/assets/nested/deep-AAAABBBBCC.js", "text/javascript", "immutable"},
		{"/assets/index-Chmc5KSx.js", "text/javascript", "immutable"},
		// No digest in the name, so it is an ordinary mutable URL.
		{"/favicon.svg", "image/svg+xml", "no-cache"},
		{"/index.html", "text/html", "no-cache"},
		{"/", "text/html", "no-cache"},
	} {
		rec := get(t, mux, tc.target)
		if rec.Code != http.StatusOK {
			t.Errorf("%s answered %d, want 200", tc.target, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, tc.wantType) {
			t.Errorf("%s answered Content-Type %q, want %q", tc.target, ct, tc.wantType)
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, tc.wantCaching) {
			t.Errorf("%s answered Cache-Control %q, want %q in it", tc.target, cc, tc.wantCaching)
		}
		if rec.Header().Get("ETag") == "" {
			t.Errorf("%s carries no ETag, so a browser has nothing to revalidate against",
				tc.target)
		}
	}
}

// **index.html is the one file that must never be cached immutably**, and a
// hashed asset is the one that must be.
//
// The index names the hashed scripts, so a cached copy pins a browser to a build
// that no longer exists, as staticcache.go records, in the
// form the bundle takes.
func TestTheIndexIsNeverImmutableAndAHashedAssetAlwaysIs(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{"dist/ui/index.html", "no-cache"},
		{"dist/ui/assets/index-Chmc5KSx.js", "immutable"},
		{"dist/ui/assets/index-Chmc5KSx.js.map", "immutable"},
		{"dist/ui/favicon.svg", "no-cache"},
		{"dist/ui/robots.txt", "no-cache"},
		// Short suffixes are not digests: a year-long cache of the wrong file is
		// unrecoverable, a needless revalidation is a 304.
		{"dist/ui/assets/index-ab.js", "no-cache"},
	} {
		if got := spaCacheControl(tc.name); !strings.Contains(got, tc.want) {
			t.Errorf("spaCacheControl(%q) = %q, want %q in it", tc.name, got, tc.want)
		}
	}
	if got := spaCacheControl("dist/ui/index.html"); strings.Contains(got, "immutable") {
		t.Errorf("index.html is cached immutably (%q): every future build would be invisible "+
			"to a browser that has it", got)
	}
}

// A validator that the handler does not honor costs a full body on every load
// and buys nothing — the second half of staticcache.go's argument.
func TestRevalidatingTheShellCostsA304AndNoBody(t *testing.T) {
	mux := muxFor(newSPAFiles(fakeBundle()))

	first := get(t, mux, "/")
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("the shell carries no ETag, so `no-cache` means a full download on every load")
	}

	again := get(t, mux, "/", "If-None-Match", tag)
	if again.Code != http.StatusNotModified {
		t.Errorf("re-requesting the shell with its own ETag answered %d, want 304", again.Code)
	}
	if again.Body.Len() != 0 {
		t.Errorf("a 304 carried %d bytes of body", again.Body.Len())
	}

	// And two different files must not share a digest.
	asset := get(t, mux, "/assets/index-Chmc5KSx.js")
	if asset.Header().Get("ETag") == tag {
		t.Error("the shell and a script carry the same ETag, so changing one reads as no " +
			"change to the other")
	}
}

// Nothing under the front end's prefixes can name a file outside the bundle.
func TestTheFrontEndPrefixesCannotReachOutOfTheBundle(t *testing.T) {
	mux := muxFor(newSPAFiles(fakeBundle()))
	for _, target := range []string{
		"/assets/../../../go.mod",
		"/assets/../index.html",
		"/assets/",
		"/assets/",
	} {
		rec := get(t, mux, target)
		if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "module ") {
			t.Fatalf("%s served something outside the bundle", target)
		}
		// 404, or ServeMux's own cleaning redirect — anything else means the
		// path reached a handler that made something of it.
		switch rec.Code {
		case http.StatusNotFound, http.StatusMovedPermanently, http.StatusTemporaryRedirect:
		default:
			t.Errorf("%s answered %d", target, rec.Code)
		}
	}
}

// **What is embedded and what is served cannot disagree.**
//
// This is the one test that reads the real `web/dist`, and it asserts a
// relationship rather than a state — so it runs, and means something, both on a
// machine that has built the front end and on one that never will. A test that
// skipped on the second would be docs/DEFINITION-OF-DONE.md §3's empty answer
// rendered as a passing one.
func TestTheEmbeddedTreeAndTheServedAnswerAgree(t *testing.T) {
	bundle := newSPAFiles(webassets.Assets)
	mux := muxFor(bundle)

	rec := get(t, mux, "/")
	if bundle.built {
		if rec.Code != http.StatusOK {
			t.Fatalf("a bundle is embedded but /ui/ answered %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "<script") {
			t.Error("the embedded index.html loads no script, which is the placeholder page " +
				"docs/DEFINITION-OF-DONE.md §14 warns about")
		}
		// Every file the shell names must be in the binary, or the page renders
		// nothing while answering 200.
		for _, src := range bundleScriptSources(body) {
			if got := get(t, mux, src); got.Code != http.StatusOK {
				t.Errorf("the embedded index.html loads %s and the binary answers %d for it",
					src, got.Code)
			}
		}
		t.Logf("a front-end bundle is embedded; /ui/ answered %d and every file it names is present",
			rec.Code)
		return
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no bundle is embedded but /ui/ answered %d, want 503", rec.Code)
	}
	t.Log("no front-end bundle in this build — the ordinary state of a checkout that has " +
		"never run `task build:web`, and /ui/ says so with a 503")
}

// bundleScriptSources pulls the same-origin URLs out of the shell's own markup.
// Crude on purpose: it reads what the build wrote rather than assuming Vite's
// naming, which is the thing that would silently change.
func bundleScriptSources(html string) []string {
	var out []string
	for _, attr := range []string{`src="`, `href="`} {
		rest := html
		for {
			i := strings.Index(rest, attr)
			if i < 0 {
				break
			}
			rest = rest[i+len(attr):]
			j := strings.Index(rest, `"`)
			if j < 0 {
				break
			}
			if value := rest[:j]; strings.HasPrefix(value, spaAssetPrefix) {
				out = append(out, value)
			}
			rest = rest[j:]
		}
	}
	return out
}

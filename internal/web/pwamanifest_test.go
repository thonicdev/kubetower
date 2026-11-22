package web

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/thonicdev/kubetower/internal/auth"
)

// srcsOf reads the icon addresses out of a manifest.
func srcsOf(t *testing.T, raw []byte) []string {
	t.Helper()
	var doc struct {
		Icons []struct {
			Src string `json:"src"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the manifest is not JSON: %v\n%s", err, raw)
	}
	out := make([]string, 0, len(doc.Icons))
	for _, icon := range doc.Icons {
		out = append(out, icon.Src)
	}
	return out
}

// TestTheManifestNamesOnlyIconsThisBuildCarries guards the manifest's icons.
// The manifest names four icons and three of them are produced by the
// Dockerfile's `icons` stage, so a build made any other way — `go build`,
// `go run ./cmd/kubetower`, `cmd/ktbuild`, every downloadable binary — served a
// manifest pointing at three 404s. Nothing failed: a manifest is fetched by the
// browser, its icons are fetched by the browser, and a browser that cannot find
// them simply does not offer the install. There is no error anywhere.
//
// This asserts the property rather than the file list, so it holds in both
// builds: whatever the manifest names, the tree has.
func TestTheManifestNamesOnlyIconsThisBuildCarries(t *testing.T) {
	files, err := fs.Sub(assets, "static/pwa")
	if err != nil {
		t.Fatalf("the PWA assets are not embedded: %v", err)
	}
	server := (&Server{}).pwa()

	request := httptest.NewRequest(http.MethodGet, auth.PWAPrefix+"manifest.webmanifest", nil)
	request.URL.Path = "/manifest.webmanifest" // the route strips the prefix
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("the manifest answered %d", recorder.Code)
	}
	srcs := srcsOf(t, recorder.Body.Bytes())
	if len(srcs) == 0 {
		t.Fatal("the manifest names no icons at all, so nothing can install it. " +
			"An empty icon list is not a safer answer than a wrong one")
	}
	for _, src := range srcs {
		name := strings.TrimPrefix(src, auth.PWAPrefix)
		if name == src {
			continue // somewhere else entirely; not this tree's business
		}
		if _, err := fs.Stat(files, name); err != nil {
			t.Errorf("the manifest names %s and this build does not carry it, so a browser "+
				"asking for it gets a 404 and the install offer may never appear: %v", src, err)
		}
	}
}

// TestAnIconThatIsThereSurvivesTheFilter guards the other direction. A filter
// that drops everything satisfies the test above perfectly.
func TestAnIconThatIsThereSurvivesTheFilter(t *testing.T) {
	raw := []byte(`{"name":"x","icons":[
		{"src":"/kt-pwa/here.svg","sizes":"any"},
		{"src":"/kt-pwa/gone.png","sizes":"192x192"},
		{"src":"https://example.invalid/elsewhere.png","sizes":"512x512"}
	]}`)
	files := fstest.MapFS{"here.svg": {Data: []byte("<svg/>")}}

	out, dropped := manifestNamingWhatExists(files, raw)

	if want := []string{"/kt-pwa/gone.png"}; len(dropped) != 1 || dropped[0] != want[0] {
		t.Errorf("dropped = %v, want %v", dropped, want)
	}
	got := srcsOf(t, out)
	// The absent one goes; the present one stays; and one addressed somewhere
	// else is left alone, because this cannot check it and a filter that
	// silently removes what it does not understand decides more than it knows.
	want := []string{"/kt-pwa/here.svg", "https://example.invalid/elsewhere.png"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("icons = %v, want %v", got, want)
	}
	if name, ok := nameOf(out); !ok || name != "x" {
		t.Errorf("the rest of the manifest did not survive the rewrite: name = %q", name)
	}
}

// TestAManifestWithNothingLeftIsServedUnchanged: an empty icon list is not
// installable at all, which is worse than one naming a 404 beside something
// valid. Cannot happen while icon.svg is committed; asserted so that deleting it
// degrades loudly.
func TestAManifestWithNothingLeftIsServedUnchanged(t *testing.T) {
	raw := []byte(`{"name":"x","icons":[{"src":"/kt-pwa/gone.png"}]}`)
	out, dropped := manifestNamingWhatExists(fstest.MapFS{}, raw)
	if len(dropped) != 1 {
		t.Errorf("dropped = %v, want the one missing icon reported", dropped)
	}
	if string(out) != string(raw) {
		t.Errorf("the manifest was rewritten to an empty icon list:\n%s", out)
	}
}

// TestAManifestThisCannotReadIsPassedThrough. Repairing a file it cannot parse
// is not this function's job, and returning nothing would replace a manifest
// that at least a lenient browser might read with no manifest at all.
func TestAManifestThisCannotReadIsPassedThrough(t *testing.T) {
	for _, raw := range []string{`not json`, `{"icons": "not a list"}`, `{}`} {
		out, dropped := manifestNamingWhatExists(fstest.MapFS{}, []byte(raw))
		if string(out) != raw || dropped != nil {
			t.Errorf("%s → %s, dropped %v; want it handed back untouched", raw, out, dropped)
		}
	}
}

func nameOf(raw []byte) (string, bool) {
	var doc struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", false
	}
	return doc.Name, true
}

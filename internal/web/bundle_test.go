package web

import (
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"

	webassets "github.com/thonicdev/kubetower/web"
)

// TestNoFrontEndMeansTheEmbeddedOne holds "nil is today's behavior" as a fact
// rather than a sentence: a Server with no Options.UI, and a Server built as a
// literal, read the very embed.FS they read before a caller could bring one.
func TestNoFrontEndMeansTheEmbeddedOne(t *testing.T) {
	if _, ok := bundleFS(nil).(interface{ ReadFile(string) ([]byte, error) }); !ok {
		t.Fatal("bundleFS(nil) is not the embedded tree")
	}
	if got, want := (&Server{}).Bundle(), fs.FS(webassets.Assets); got != want {
		t.Fatalf("a Server with no UI serves %T, want the embedded bundle", got)
	}
}

// TestACallersFrontEndIsReadAtTheEmbeddedPaths is the layout half: a caller's
// Vite root appears where this package reads its own, and nowhere else.
func TestACallersFrontEndIsReadAtTheEmbeddedPaths(t *testing.T) {
	ui := fstest.MapFS{
		"index.html":             &fstest.MapFile{Data: []byte("<!doctype html><title>caller</title>")},
		"assets/app-AAAABBBB.js": &fstest.MapFile{Data: []byte("export {};")},
	}
	b := bundleFS(ui)

	if raw, err := fs.ReadFile(b, bundleIndex); err != nil || string(raw) != "<!doctype html><title>caller</title>" {
		t.Errorf("%s: %q, %v - want the caller's index", bundleIndex, raw, err)
	}
	if !spaBundlePresent(b) {
		t.Error("a caller's bundle with an index and an asset reads as not built")
	}
	for _, name := range []string{"index.html", "static/pwa/manifest.json", "dist/other/index.html"} {
		if _, err := fs.Stat(b, name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s outside %s answered %v, want not-exist", name, bundleRoot, err)
		}
	}
	if _, err := fs.Stat(b, "../index.html"); !errors.Is(err, fs.ErrInvalid) {
		t.Errorf("an invalid path answered %v, want ErrInvalid", err)
	}
}

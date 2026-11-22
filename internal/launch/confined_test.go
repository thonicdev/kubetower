package launch

// A snap or Flatpak browser cannot read the state directory, so its launch
// page goes in its own data directory (confined.go).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// confineNothing points every confinement lookup at empty directories, so a
// test that opens a window never writes into the real home of the machine
// running it, whatever browsers that machine has.
func confineNothing(t *testing.T) {
	t.Helper()
	empty := t.TempDir()
	prevBin, prevSnap, prevFlat, prevHandler := snapBin, snapDesktopDirs, flatpakDesktopDirs, htmlHandler
	t.Cleanup(func() {
		snapBin, snapDesktopDirs, flatpakDesktopDirs, htmlHandler = prevBin, prevSnap, prevFlat, prevHandler
	})
	snapBin = filepath.Join(empty, "snap-bin")
	snapDesktopDirs = []string{filepath.Join(empty, "snap-apps")}
	flatpakDesktopDirs = func(string) []string { return []string{filepath.Join(empty, "flatpak-apps")} }
	htmlHandler = func() string { return "" }
}

// export writes an empty desktop entry id into dir.
func export(t *testing.T, dir, id string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAConfinedHandlerGetsItsOwnDirectory(t *testing.T) {
	confineNothing(t)
	home := t.TempDir()
	export(t, snapDesktopDirs[0], "firefox_firefox.desktop")
	export(t, flatpakDesktopDirs(home)[0], "org.mozilla.firefox.desktop")
	export(t, filepath.Join(home, "plain"), "google-chrome.desktop")

	cases := []struct{ id, want string }{
		{"firefox_firefox.desktop", filepath.Join(home, "snap", "firefox", "common")},
		{"org.mozilla.firefox.desktop", filepath.Join(home, ".var", "app", "org.mozilla.firefox")},
		{"google-chrome.desktop", ""},     // a deb: the state directory
		{"chromium_chromium.desktop", ""}, // named like a snap, but no snap exports it
		{"", ""},                          // no answer from xdg-mime
		{"../firefox_firefox.desktop", ""},
	}
	for _, tc := range cases {
		if got := confinedDesktopDir(home, tc.id); got != tc.want {
			t.Errorf("confinedDesktopDir(%q) = %q; want %q", tc.id, got, tc.want)
		}
	}
}

func TestAChromiumSnapGetsItsOwnDirectory(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("snaps are Linux's")
	}
	confineNothing(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got, want := chromiumPageDir(filepath.Join(snapBin, "chromium")), filepath.Join(home, "snap", "chromium", "common"); got != want {
		t.Errorf("a Chromium snap's page goes in %q; want %q", got, want)
	}
	if got, want := chromiumPageDir(filepath.Join(snapBin, "brave.browser")), filepath.Join(home, "snap", "brave", "common"); got != want {
		t.Errorf("a snap app's page goes in %q; want its snap's %q", got, want)
	}
	if got := chromiumPageDir("/usr/bin/google-chrome"); got != "" {
		t.Errorf("a deb Chrome's page goes in %q; want the state directory", got)
	}
}

// The rung a confined default browser gets: with no Chromium installed, the
// default browser is handed a page in its snap's own directory, and the state
// directory gets none.
func TestASnapDefaultBrowserIsHandedAPageItCanRead(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("snaps are Linux's")
	}
	confineNothing(t)
	home, state := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KT_STATE_DIR", state)
	t.Setenv("PATH", t.TempDir()) // no Chromium-based browser to find
	export(t, snapDesktopDirs[0], "firefox_firefox.desktop")
	htmlHandler = func() string { return "firefox_firefox.desktop" }

	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	listen := strings.TrimPrefix(srv.URL, "http://")

	prevNative, prevStart := drawNative, startProcess
	t.Cleanup(func() { drawNative, startProcess = prevNative, prevStart })
	drawNative = func(string, windowSources) bool { return false }
	var argvs [][]string
	startProcess = func(cmd string, args ...string) error {
		argvs = append(argvs, append([]string{cmd}, args...))
		return nil
	}

	openWindow(context.Background(), listen, fixedMint, windowSources{})

	if len(argvs) != 1 || argvs[0][0] != "xdg-open" || len(argvs[0]) != 2 {
		t.Fatalf("started %q; want one xdg-open of a page", argvs)
	}
	page := pagePath(t, argvs[0][1])
	if want := filepath.Join(home, "snap", "firefox", "common"); filepath.Dir(page) != want {
		t.Errorf("the page is in %s; want the snap's own %s", filepath.Dir(page), want)
	}
	if strings.Contains(strings.Join(argvs[0], " "), pageCode) {
		t.Errorf("the command line carries the code: %q", argvs[0])
	}
	raw, err := os.ReadFile(page)
	if err != nil || !strings.Contains(string(raw), windowURL(listen, pageCode)) {
		t.Errorf("the page does not forward to the window's launch address (%v):\n%s", err, raw)
	}
	if info, err := os.Stat(page); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the page is not 0600: %v %v", info, err)
	}
	if entries, _ := os.ReadDir(state); len(entries) != 0 {
		t.Errorf("a page was also written in the state directory: %v", entries)
	}
}

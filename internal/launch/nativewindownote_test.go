package launch

import (
	"strings"
	"testing"
)

// `--app` on a platform with no native window opens a browser window, and the
// operator has to be told that rather than left to conclude the flag did not
// work.
//
// A Go comment is not where an operator reads; this note is. macOS and Windows
// are not in this list: they draw their own window.
func TestEveryPlatformWithoutANativeWindowSaysSo(t *testing.T) {
	for _, goos := range []string{"linux", "freebsd"} {
		note := nativeWindowAbsentNote(goos)
		if note == "" {
			t.Fatalf("%s: no native window and nothing said about it", goos)
		}
		// The two things it has to convey: that this is a browser window, and
		// that a permanent entry is still available. A note that only apologized
		// would leave the operator where they started.
		for _, want := range []string{"browser window", "Install app"} {
			if !strings.Contains(note, want) {
				t.Errorf("%s: note = %q and does not mention %q", goos, note, want)
			}
		}
	}
}

// The engine is named per platform, because "the engine exists here" with the
// wrong name attached is worse than not naming one.
func TestTheNoteNamesThePlatformsOwnEngine(t *testing.T) {
	if note := nativeWindowAbsentNote("linux"); !strings.Contains(note, "WebKitGTK") {
		t.Errorf("linux note = %q, want WebKitGTK", note)
	}
	// And an unknown Unix names neither rather than guessing.
	other := nativeWindowAbsentNote("freebsd")
	for _, engine := range []string{"WKWebView", "WebKitGTK"} {
		if strings.Contains(other, engine) {
			t.Errorf("freebsd note = %q and claims %q, which it cannot know", other, engine)
		}
	}
}

// The two platforms that draw their own window must stay silent.
//
// **This is the assertion that pairs the note with the window**, and it is the
// reason the note has to change together with `nativewindow_darwin.go` rather
// than afterwards. `nativeWindowAbsentNote` feeds two surfaces — the start-up
// log through `openWindow`, and the `--install` banner through
// `installation.Notes` — so a macOS that kept the note would print "there is no
// native window on this platform yet" underneath the native window it had just
// opened, twice, with every test still green. A note that contradicts what the
// operator is looking at is the defect this note exists to fix, inverted, and
// nothing but this test stands between the two.
//
// Each also has a path that falls back to a browser — a missing WebView2 runtime
// on Windows, an AppKit or WKWebView failure on macOS — and each logs its own,
// more specific reason there. Two sentences would contradict each other.
func TestThePlatformsThatDrawTheirOwnWindowSayNothing(t *testing.T) {
	for _, goos := range []string{"windows", "darwin"} {
		if note := nativeWindowAbsentNote(goos); note != "" {
			t.Errorf("%s note = %q, want silence — it draws the window itself, and its "+
				"browser fallback has its own more specific line", goos, note)
		}
	}
}

// The note has to reach the banner, not merely exist.
//
// This assertion is cheap and the failure it guards against is not: built,
// tested and not reachable. A note appended to `installation.Notes` that
// `installedHelp` did not print would pass every other test.
//
// **It asks Linux**, which has a note: asking a platform whose note is the empty
// string would assert that a banner contains "" — true of every string. A test
// that cannot fail is worse than no test, because it is counted.
func TestTheInstallBannerPrintsANoteItIsGiven(t *testing.T) {
	note := nativeWindowAbsentNote("linux")
	if note == "" {
		t.Fatal("the note for linux is empty, so this test asserts nothing")
	}
	banner := installedHelp(installation{
		Binary: "/home/operator/.local/bin/kubetower",
		Notes:  []string{note},
	})
	if !strings.Contains(banner, note) {
		t.Errorf("the install banner does not carry the note it was given.\nbanner:\n%s", banner)
	}
	if !strings.Contains(banner, "note:") {
		t.Errorf("the banner has no notes section at all:\n%s", banner)
	}
}

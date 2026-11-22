package launch

import (
	"net/url"
	"strings"
	"testing"
)

// The decisions behind the macOS window's title bar, About box, Help item and
// Web Inspector. They are in `nativewindowtext.go`, with no build tag, so that
// these run on the Linux CI that cannot compile the window itself.

// The window carries the page's own title, and never an empty one.
func TestTheWindowTitleIsThePagesOwnOrKubeTower(t *testing.T) {
	for page, want := range map[string]string{
		// What the console sets (`useDocumentTitle`, Topbar.tsx): passed through
		// whole, never recomposed.
		"KubeTower · Production · Pod / default / api-0": "KubeTower · Production · Pod / default / api-0",
		"KubeTower":    "KubeTower",
		"  Settings  ": "Settings",
		"":             windowTitleFallback,
		" \t\n":        windowTitleFallback,
	} {
		if got := windowTitle(page); got != want {
			t.Errorf("windowTitle(%q) = %q, want %q", page, got, want)
		}
	}
	if windowTitleFallback != "KubeTower" {
		t.Errorf("the fallback is %q; a window with no page names the application", windowTitleFallback)
	}
}

// About KubeTower says what `--version` says, and under which license.
func TestTheAboutBoxCarriesTheVersionLineAndTheLicence(t *testing.T) {
	restoreVersion := version
	t.Cleanup(func() { version = restoreVersion })

	version = "0.4.1"
	about := aboutPanel()
	if about.Name != "KubeTower" {
		t.Errorf("Name = %q, want KubeTower: a bare binary has no bundle to name it", about.Name)
	}
	if about.Version != "0.4.1" {
		t.Errorf("Version = %q, want the release, 0.4.1", about.Version)
	}
	if about.Build != "built "+built {
		t.Errorf("Build = %q, want the build date; empty would let AppKit repeat CFBundleVersion", about.Build)
	}
	if strings.Contains(about.Build+about.Credits, "edition") {
		t.Errorf("the About box names an edition, and there is none:\n%s\n%s", about.Build, about.Credits)
	}
	// The same function `--version` prints, so the two cannot drift.
	if !strings.Contains(about.Credits, versionLine()) {
		t.Errorf("Credits do not carry the --version line %q:\n%s", versionLine(), about.Credits)
	}
	if !strings.Contains(about.Credits, "AGPL-3.0-only") {
		t.Errorf("Credits do not name the license:\n%s", about.Credits)
	}
	// Nothing a release carries is licensed separately, so no build
	// may say otherwise.
	if strings.Contains(about.Credits, "licensed separately") {
		t.Errorf("the credits claim separately licensed modules, which no build carries:\n%s", about.Credits)
	}
}

// The Web Inspector follows `developmentBuild()`, the same reading the update
// check is handed — on for an unstamped build, off for anything stamped.
func TestTheWebInspectorIsOnlyForDevelopmentBuilds(t *testing.T) {
	restore := version
	t.Cleanup(func() { version = restore })

	version = devVersion
	if !webInspectorAllowed() {
		t.Error("an unstamped build has no inspector; the development loop loses its debugger")
	}
	for _, release := range []string{"0.1.0", "1.2.3", "0.0.0-rc.1"} {
		version = release
		if webInspectorAllowed() {
			t.Errorf("release %s turns the Web Inspector on", release)
		}
		if webInspectorAllowed() != developmentBuild() {
			t.Errorf("release %s: the inspector and developmentBuild() disagree", release)
		}
	}
}

// NSTextAlignmentCenter is 1 on Apple silicon and 2 on Intel (`NSText.h`,
// `TARGET_ABI_USES_IOS_VALUES`); on Intel 1 is right-aligned.
func TestCentredTextIsSpeltPerProcessor(t *testing.T) {
	if got := textAlignmentCenter("arm64"); got != 1 {
		t.Errorf("arm64: center = %d, want 1", got)
	}
	if got := textAlignmentCenter("amd64"); got != 2 {
		t.Errorf("amd64: center = %d, want 2 (1 is right-aligned there)", got)
	}
}

// Help opens an address `openOutside` will hand to a browser: https, absolute,
// and the documentation rather than the site's front page.
func TestHelpOpensThePublicDocumentation(t *testing.T) {
	u, err := url.Parse(helpURL)
	if err != nil {
		t.Fatalf("helpURL %q does not parse: %v", helpURL, err)
	}
	if u.Scheme != "https" || u.Host == "" {
		t.Errorf("helpURL %q is not an absolute https address; openOutside would refuse it", helpURL)
	}
	if !strings.HasSuffix(strings.TrimSuffix(u.Path, "/"), "/docs") {
		t.Errorf("helpURL %q is not the documentation index", helpURL)
	}
}

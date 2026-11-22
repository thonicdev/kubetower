package launch

import (
	"strings"

	"github.com/thonicdev/kubetower/internal/update"
)

// The decisions the macOS window takes that are not AppKit calls: the title,
// About KubeTower, where Help goes, whether the Web Inspector is on, and one
// number AppKit spells differently per processor. No build tag, so they are
// tested on Linux; nativewindow_darwin.go only sends the answers to AppKit.

// windowTitleFallback is the window's title when the page has none: before the
// first page has loaded, and on a page that sets an empty one.
const windowTitleFallback = "KubeTower"

// helpURL is the public documentation, which Help ▸ KubeTower Help opens in the
// operator's browser. The GitHub Pages address, not the site's canonical
// domain, which does not resolve: a Help item that opens an error page is worse
// than none.
const helpURL = "https://thonicdev.github.io/kubetower-site/docs/"

// windowTitle is what the window's title bar says for a page titled `page`.
//
// The console names every page itself (`KubeTower · <cluster> · <page>`,
// useDocumentTitle in web/ui/src/shell/Topbar.tsx), so this passes it through.
// Only an empty title is replaced: a window titled nothing is a blank entry in
// the Window menu, ⌘` and Mission Control.
func windowTitle(page string) string {
	if title := strings.TrimSpace(page); title != "" {
		return title
	}
	return windowTitleFallback
}

// aboutPanelText is what About KubeTower shows, field by field, in the terms of
// `orderFrontStandardAboutPanelWithOptions:`.
type aboutPanelText struct {
	// Name is NSAboutPanelOptionApplicationName. Given, because the bare
	// binary with `--app` has no bundle and AppKit would show `kubetower`.
	Name string
	// Version is NSAboutPanelOptionApplicationVersion, the release.
	Version string
	// Build is NSAboutPanelOptionVersion, shown in brackets after Version: the
	// build date. Left out, AppKit would fill it from CFBundleVersion, the
	// release, and read `0.1.0 (0.1.0)`.
	Build string
	// Credits is NSAboutPanelOptionCredits: the line `--version` prints, and the
	// license.
	Credits string
}

// aboutPanel is the About box's text.
//
// The version line is versionLine itself, as `--version`, the log and the
// Infos tab print it, and it is right outside a bundle, where the standard
// panel's CFBundleShortVersionString is missing. Every release is
// AGPL-3.0-only, so there is one license sentence.
func aboutPanel() aboutPanelText {
	const licence = "Free software under the GNU Affero General Public License, version 3 only (AGPL-3.0-only)."
	return aboutPanelText{
		Name:    "KubeTower",
		Version: version,
		Build:   "built " + built,
		Credits: versionLine() + "\n\n" + licence + "\n" + update.Repository,
	}
}

// webInspectorAllowed reports whether the window's Web Inspector is turned on.
//
// On a development build only, decided by developmentBuild, the same reading
// the update check and the Settings page use. Never on a release: the inspector
// reads every response and runs script in the page holding the session, a door
// for anyone at the keyboard.
func webInspectorAllowed() bool { return developmentBuild() }

// textAlignmentCenter is AppKit's NSTextAlignmentCenter on the given processor.
//
// It is not one number: NSText.h defines NSTextAlignment under
// TARGET_ABI_USES_IOS_VALUES, set for every processor except x86_64, so center
// is 1 on Apple silicon and 2 on Intel, where 1 means right-aligned.
func textAlignmentCenter(goarch string) int {
	if goarch == "amd64" {
		return 2
	}
	return 1
}

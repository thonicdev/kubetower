package launch

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// The bytes the Linux and macOS installers write, generated here rather than in
// their own files.
//
// This file has no build tag on purpose: the desktop entry, the property list
// and the icon container are the only parts of those installers with syntax or
// arithmetic, and code behind a darwin or linux build tag is not compiled by
// `go test` on other hosts. What stays platform-specific is a handful of paths.
// Whether the desktop then draws the launcher or opens the bundle is not tested.

// desktopEntry is the freedesktop `.desktop` file for a Linux installation.
//
// `Exec` carries `--app`, like the Windows shortcut: a launch from a menu wants
// the application window, not a bare server (see appwindow.go for the ladder on
// Linux).
//
// `Terminal=false`, or the launcher would hold a terminal open for the life of
// the console.
//
// No `StartupWMClass`: the window belongs to the operator's browser, and
// claiming it would make the browser's own windows adopt this icon.
func desktopEntry(exec, iconName string) []byte {
	// Exec reserves characters a path may contain, so the path is quoted per
	// the spec: a backslash or a quote inside is escaped with a backslash.
	quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(exec)

	// The Icon key is omitted rather than written empty when there is no
	// bitmap: `Icon=` looks up a theme entry named "", which some desktops cache
	// as icon-less past a later fix. No key falls back to the generic icon.
	icon := ""
	if iconName != "" {
		icon = "Icon=" + iconName + "\n"
	}

	return []byte(fmt.Sprintf(`[Desktop Entry]
Type=Application
Version=1.0
Name=KubeTower
GenericName=Kubernetes console
Comment=A Kubernetes console that runs on your own machine
Exec="%s" --app
%sTerminal=false
Categories=Development;Monitor;
Keywords=kubernetes;kubectl;cluster;
StartupNotify=true
`, quoted, icon))
}

// infoPlist is the property list at the root of a macOS application bundle.
//
// macOS's unit of installation is a bundle; a bare executable in ~/Applications
// is not in Spotlight or Launchpad and opens Terminal when double-clicked.
//
// `LSUIElement` is not set: the Dock icon is how the operator sees it is
// running and how they quit it.
//
// `CFBundleVersion` and `CFBundleShortVersionString` are both the release: the
// project has one number for both questions.
//
// `LSApplicationCategoryType` must be the exact identifier from Apple's list:
// Spotlight derives "Developer Tools" from it, while a free-form value yields no
// category at all.
//
// `NSHumanReadableCopyright`, printed in the About panel, names the license
// (AGPL-3.0-only for everything a release carries) and no holder: naming the
// holder is not this file's question, and "Copyright" followed by nobody is not
// a notice.
//
// `NSSupportsAutomaticGraphicsSwitching` lets the web view run on the
// integrated GPU instead of pinning the discrete one on a dual-GPU Mac; this is
// Apple's documented behavior, not a measured saving.
//
// `NSAppTransportSecurity` is absent on purpose: the WKWebView window loads
// plain HTTP on 127.0.0.1 without any ATS exception, and an exception would be
// a relaxation every installed copy carries for nothing. Should a later macOS
// refuse the loopback (a blank window), the narrow key is
// `NSAllowsLocalNetworking`, never `NSAllowsArbitraryLoads`.
//
// `CFBundleIdentifier` is chosen once: macOS files preferences, privacy
// permissions and keychain items under it, so changing it makes a different
// application. The Linux desktop entry carries the same name.
//
// `CFBundleURLTypes` declares the `kubetower://` scheme (urlScheme, which the
// validator checks against), which is what makes macOS hand such a link to this
// application, starting it if needed, as the get-URL Apple Event
// nativewindow_darwin.go answers. The role is `Viewer`: a link only shows a
// page, never acts.
func infoPlist(release string) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key>
	<string>KubeTower</string>
	<key>CFBundleDisplayName</key>
	<string>KubeTower</string>
	<key>CFBundleIdentifier</key>
	<string>dev.thonicdev.kubetower</string>
	<key>CFBundleExecutable</key>
	<string>kubetower</string>
	<key>CFBundleIconFile</key>
	<string>kubetower</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleInfoDictionaryVersion</key>
	<string>6.0</string>
	<key>CFBundleVersion</key>
	<string>%s</string>
	<key>CFBundleShortVersionString</key>
	<string>%s</string>
	<key>NSHighResolutionCapable</key>
	<true/>
	<key>LSMinimumSystemVersion</key>
	<string>11.0</string>
	<key>LSApplicationCategoryType</key>
	<string>public.app-category.developer-tools</string>
	<key>NSHumanReadableCopyright</key>
	<string>KubeTower: GNU AGPL-3.0-only</string>
	<key>NSSupportsAutomaticGraphicsSwitching</key>
	<true/>
	<key>CFBundleURLTypes</key>
	<array>
		<dict>
			<key>CFBundleURLName</key>
			<string>dev.thonicdev.kubetower</string>
			<key>CFBundleTypeRole</key>
			<string>Viewer</string>
			<key>CFBundleURLSchemes</key>
			<array>
				<string>%s</string>
			</array>
		</dict>
	</array>
</dict>
</plist>
`, release, release, urlScheme))
}

// icnsMagic and the type of the one entry written below.
const (
	icnsMagic = "icns"
	// ic09 is the 512×512 slot. macOS accepts PNG data in the ic07–ic10 slots,
	// so no icon encoder is needed.
	icnsType512 = "ic09"
	// The file header and each entry header are a four-byte tag and a
	// four-byte big-endian length that includes the header itself. A length of
	// the payload alone makes a file macOS rejects without a diagnostic.
	icnsHeaderLen = 8
)

// icnsFromPNG wraps a PNG in an Apple icon container.
//
// The 512×512 bitmap goes in as-is and no other size is written: macOS scales
// down from the largest slot, and re-encoding would need a decoder and a
// resampler.
func icnsFromPNG(png []byte) ([]byte, error) {
	if len(png) == 0 {
		return nil, fmt.Errorf("no icon data")
	}
	// The slot promises PNG; anything else renders as nothing, with no error.
	if len(png) < 8 || string(png[1:4]) != "PNG" {
		return nil, fmt.Errorf("the icon is not a PNG")
	}

	entryLen := icnsHeaderLen + len(png)
	total := icnsHeaderLen + entryLen

	out := make([]byte, 0, total)
	out = append(out, icnsMagic...)
	out = binary.BigEndian.AppendUint32(out, uint32(total))
	out = append(out, icnsType512...)
	out = binary.BigEndian.AppendUint32(out, uint32(entryLen))
	out = append(out, png...)
	return out, nil
}

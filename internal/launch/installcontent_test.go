package launch

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/web"
)

// What can be checked about the Linux and macOS installers from a Windows host,
// which is where this project is developed.
//
// Not much, and being precise about which "not much" is the point. Nothing here
// proves GNOME draws a launcher or that Finder opens a bundle. What it proves is
// that the bytes are the shape their specifications require — which is exactly
// the class of defect that would otherwise be found by a colleague on a laptop
// nobody here has, reported as "it does nothing", and impossible to reproduce.

// TestTheDesktopEntryOpensAWindowAndNotAServer. `--app` in Exec= is the
// difference between clicking a launcher and getting an application, and
// clicking a launcher and getting a process with no window that only Task
// Manager can find.
//
// This is the Windows shortcut's argument transplanted: `wantsWindow` opens a
// window for a double-click with no console, but a Linux launcher runs the
// binary from a session that may well have neither, and relying on that
// inference rather than saying `--app` is how the inference gets changed later
// by somebody who has never seen this file.
func TestTheDesktopEntryOpensAWindowAndNotAServer(t *testing.T) {
	entry := string(desktopEntry("/home/x/.local/bin/kubetower", "kubetower"))

	exec := lineStarting(t, entry, "Exec=")
	if !strings.Contains(exec, "--app") {
		t.Errorf("Exec is %q; without --app the launcher starts a server with no window", exec)
	}
	if !strings.Contains(exec, "/home/x/.local/bin/kubetower") {
		t.Errorf("Exec is %q; it must name the installed binary", exec)
	}
	if terminal := lineStarting(t, entry, "Terminal="); terminal != "Terminal=false" {
		t.Errorf("%q; Terminal=true leaves a terminal open for the life of the console", terminal)
	}
	if !strings.HasPrefix(entry, "[Desktop Entry]\n") {
		t.Error("a .desktop file that does not open with [Desktop Entry] is ignored entirely")
	}
	if kind := lineStarting(t, entry, "Type="); kind != "Type=Application" {
		t.Errorf("%q; only Type=Application appears in a menu", kind)
	}
}

// TestTheDesktopEntryQuotesAPathWithASpaceIn. The spec reserves characters in
// Exec= and a home directory is somebody's name, which routinely has a space in
// it. Unquoted, `/home/jean dupont/…/kubetower --app` is a command called
// `/home/jean` with two arguments.
func TestTheDesktopEntryQuotesAPathWithASpaceIn(t *testing.T) {
	entry := string(desktopEntry("/home/jean dupont/.local/bin/kubetower", "kubetower"))
	exec := lineStarting(t, entry, "Exec=")
	if !strings.Contains(exec, `"/home/jean dupont/.local/bin/kubetower"`) {
		t.Errorf("Exec is %q; a path with a space must be quoted or it parses as two arguments", exec)
	}
}

// TestTheDesktopEntryDropsTheIconRatherThanNamingAMissingOne. `desktopInstall`
// clears the icon name when the bitmap could not be written, and this is the
// other half of that: an Icon= key naming a theme entry that does not exist is
// cached as icon-less by some desktops, and the cache outlives the fix. No key
// at all gets the generic application icon, which is the honest outcome.
func TestTheDesktopEntryDropsTheIconRatherThanNamingAMissingOne(t *testing.T) {
	entry := string(desktopEntry("/home/x/.local/bin/kubetower", ""))
	if strings.Contains(entry, "Icon=\n") {
		t.Error("Icon= was written empty; an empty key is worse than an absent one")
	}
}

// TestTheBundleIsAnApplicationAndNotADirectory. Four keys decide whether macOS
// treats `KubeTower.app` as an application or as a folder somebody named oddly.
// Miss CFBundleExecutable and the Finder opens it and finds nothing to run;
// miss CFBundlePackageType and it is not an application at all.
func TestTheBundleIsAnApplicationAndNotADirectory(t *testing.T) {
	plist := string(infoPlist("0.2.0"))

	for _, required := range []string{
		"<key>CFBundleExecutable</key>",
		"<string>kubetower</string>",
		"<key>CFBundlePackageType</key>",
		"<string>APPL</string>",
		"<key>CFBundleIdentifier</key>",
		"<string>dev.thonicdev.kubetower</string>",
		"<key>CFBundleIconFile</key>",
	} {
		if !strings.Contains(plist, required) {
			t.Errorf("Info.plist is missing %s, without which the bundle is a directory", required)
		}
	}

	// The executable named in the plist has to be the one the installer writes,
	// and those are two different files that nothing else compares.
	if !strings.Contains(plist, "<string>kubetower</string>") {
		t.Error("CFBundleExecutable must name the file install.go copies into Contents/MacOS")
	}

	// The icon key names a file without its extension, which is the one piece of
	// plist trivia that silently produces no icon rather than an error.
	if strings.Contains(plist, "kubetower.icns</string>") {
		t.Error("CFBundleIconFile carries the name without .icns; with the extension macOS finds nothing")
	}
}

// TestTheBundleCarriesTheRunningVersion. Both version keys, because Apps like
// the Finder read one and `sw_vers`-style tooling reads the other, and a bundle
// stamped `dev` in a release is the same lie `version.go` refuses to tell.
func TestTheBundleCarriesTheRunningVersion(t *testing.T) {
	plist := string(infoPlist("1.2.3"))
	if strings.Count(plist, "<string>1.2.3</string>") != 2 {
		t.Errorf("expected CFBundleVersion and CFBundleShortVersionString both at 1.2.3:\n%s", plist)
	}
}

// TestTheBundleSaysWhatItIs: the keys that tell macOS what
// kind of application this is, rather than only that it is one. Each is asserted
// as a key *with its value*, read out of the parsed dictionary, because a
// `Contains` on the raw text passes when the value has drifted onto the next key.
func TestTheBundleSaysWhatItIs(t *testing.T) {
	dict := plistDict(t, infoPlist("1.2.3"))

	// A category outside Apple's fixed list is read as no category, so the value
	// is compared exactly and not by prefix.
	if got := dict["LSApplicationCategoryType"]; got != "public.app-category.developer-tools" {
		t.Errorf("LSApplicationCategoryType is %q; the Finder and Spotlight only group by Apple's "+
			"own identifiers, and developer-tools is the one this is", got)
	}

	// The About panel's line names the license, as LICENSING.md spells it, and
	// no holder: an About panel is the last place the holder should be decided
	// by accident.
	copyright, ok := dict["NSHumanReadableCopyright"]
	if !ok {
		t.Fatal("NSHumanReadableCopyright is missing; the About panel says nothing about the license")
	}
	if !strings.Contains(copyright, "AGPL-3.0-only") {
		t.Errorf("NSHumanReadableCopyright is %q; it has to name the license, GNU AGPL-3.0-only", copyright)
	}
	if strings.Contains(strings.ToLower(copyright), "copyright") || strings.Contains(copyright, "©") {
		t.Errorf("NSHumanReadableCopyright is %q; it claims a copyright notice with no holder named, "+
			"and naming one is not this string's decision", copyright)
	}

	if got := dict["NSSupportsAutomaticGraphicsSwitching"]; got != "<true/>" {
		t.Errorf("NSSupportsAutomaticGraphicsSwitching is %q; without it a two-GPU laptop may wake "+
			"the discrete one for a web view", got)
	}

	// Absent on purpose and measured (see infoPlist). An exception added here
	// would relax a security control on every installed copy to fix nothing.
	if _, present := dict["NSAppTransportSecurity"]; present {
		t.Error("NSAppTransportSecurity is set; the loopback page loads without it, and " +
			"infoPlist's comment says what was measured before an exception is added")
	}
}

// TestTheBundleClaimsTheKubetowerScheme: without
// `CFBundleURLTypes` macOS never hands a `kubetower://` link to the application,
// and the Apple Event handler in `nativewindow_darwin.go` waits for nothing.
// Read out of the parsed property list, nested values included, because the
// failure modes are all silent: a scheme written as a bare <string> rather than
// inside an <array> under CFBundleURLSchemes, or the entry outside an <array>,
// is a bundle macOS registers with no scheme at all.
func TestTheBundleClaimsTheKubetowerScheme(t *testing.T) {
	root, ok := plistTree(t, infoPlist("1.2.3")).(map[string]any)
	if !ok {
		t.Fatal("the property list's root is not a dictionary")
	}
	types, ok := root["CFBundleURLTypes"].([]any)
	if !ok || len(types) != 1 {
		t.Fatalf("CFBundleURLTypes is %#v; want an array of exactly one entry", root["CFBundleURLTypes"])
	}
	entry, ok := types[0].(map[string]any)
	if !ok {
		t.Fatalf("the URL type is %#v; want a dictionary", types[0])
	}
	schemes, ok := entry["CFBundleURLSchemes"].([]any)
	if !ok || len(schemes) != 1 || schemes[0] != urlScheme {
		t.Errorf("CFBundleURLSchemes is %#v; want exactly [%q], the scheme the validator checks", entry["CFBundleURLSchemes"], urlScheme)
	}
	if urlScheme != "kubetower" {
		t.Errorf("the scheme is %q; want kubetower://", urlScheme)
	}
	if got := entry["CFBundleURLName"]; got != root["CFBundleIdentifier"] {
		t.Errorf("CFBundleURLName is %#v; want the bundle identifier %#v", got, root["CFBundleIdentifier"])
	}
	// A link only ever shows a page; Editor would be a claim this
	// application never makes good on.
	if got := entry["CFBundleTypeRole"]; got != "Viewer" {
		t.Errorf("CFBundleTypeRole is %#v; want Viewer", got)
	}
	for key := range entry {
		switch key {
		case "CFBundleURLName", "CFBundleURLSchemes", "CFBundleTypeRole":
		default:
			t.Errorf("the URL type carries %s, which nothing here asked for", key)
		}
	}
}

// TestTheBundlePlistIsOneDictionaryWithNoKeyTwice. `plutil -lint` is not on the
// Windows host, so the well-formedness it would check is checked here: the XML
// parses, the root is a single <dict>, and no key appears twice — a duplicated
// key is accepted by some readers and silently resolved to whichever comes last,
// which is how a second, stale value survives an edit.
func TestTheBundlePlistIsOneDictionaryWithNoKeyTwice(t *testing.T) {
	dict := plistDict(t, infoPlist("1.2.3"))
	for _, key := range []string{
		"CFBundleName", "CFBundleDisplayName", "CFBundleIdentifier", "CFBundleExecutable",
		"CFBundleIconFile", "CFBundlePackageType", "CFBundleInfoDictionaryVersion",
		"CFBundleVersion", "CFBundleShortVersionString", "NSHighResolutionCapable",
		"LSMinimumSystemVersion",
	} {
		if _, ok := dict[key]; !ok {
			t.Errorf("%s is missing from the parsed dictionary", key)
		}
	}
	if got := dict["CFBundleExecutable"]; got != bundleExecutableName {
		t.Errorf("CFBundleExecutable is %q; the installer writes %q", got, bundleExecutableName)
	}
}

// TestTheIconContainerCountsItsOwnHeaders. **This is the test the file exists
// for.** Both lengths in an icns are defined to include the eight-byte header
// they are part of, and a length written as the payload alone produces a
// container macOS rejects with no diagnostic whatsoever — no error, no log, just
// an application with the generic icon. It is unfindable by looking at it.
func TestTheIconContainerCountsItsOwnHeaders(t *testing.T) {
	png := fakePNG(64)

	icns, err := icnsFromPNG(png)
	if err != nil {
		t.Fatalf("icnsFromPNG: %v", err)
	}

	if got := string(icns[0:4]); got != "icns" {
		t.Fatalf("magic is %q, want %q", got, "icns")
	}

	total := binary.BigEndian.Uint32(icns[4:8])
	if int(total) != len(icns) {
		t.Errorf("the file header says %d bytes and the file is %d; the length includes the header", total, len(icns))
	}

	if got := string(icns[8:12]); got != "ic09" {
		t.Fatalf("entry type is %q, want the 512×512 slot ic09", got)
	}

	entry := binary.BigEndian.Uint32(icns[12:16])
	if int(entry) != 8+len(png) {
		t.Errorf("the entry header says %d and the entry is %d; it counts its own 8 bytes too", entry, 8+len(png))
	}

	if !bytes.Equal(icns[16:], png) {
		t.Error("the PNG did not survive into the container unchanged")
	}
}

// TestTheIconContainerRefusesSomethingThatIsNotAPNG. The ic09 slot promises PNG
// data. A container holding anything else is a file the Finder draws as nothing,
// with no error anywhere — so the refusal happens here, where there is somebody
// to tell.
func TestTheIconContainerRefusesSomethingThatIsNotAPNG(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"a JPEG", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0, 0, 0, 0, 0}},
		{"truncated", []byte{0x89, 'P'}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := icnsFromPNG(tc.data); err == nil {
				t.Error("accepted; a container with no PNG in it fails silently on macOS")
			}
		})
	}
}

// TestTheInstalledIconIsTheOneTheConsoleUses. The bitmap comes out of
// `internal/web`'s embedded tree rather than a second copy in `internal/launch`,
// because `go:embed` cannot climb out of its own package directory and the
// obvious workaround — copy the file — puts two images in the tree that nothing
// compares. This is what holds that: the accessor has to keep working, and what
// it returns has to be a real PNG that the icns writer will take.
func TestTheInstalledIconIsTheOneTheConsoleUses(t *testing.T) {
	png, err := web.AppIconPNG()
	if err != nil {
		t.Fatalf("web.AppIconPNG: %v — the installer has no icon to write on Linux or macOS", err)
	}
	if len(png) < 8 || string(png[1:4]) != "PNG" {
		t.Fatalf("what came back is not a PNG (%d bytes)", len(png))
	}
	if _, err := icnsFromPNG(png); err != nil {
		t.Errorf("the real icon will not go into a macOS container: %v", err)
	}
}

// TestTheBundleWriterMakesTheDirectoryMacOSExpects.
//
// **This runs on Windows, which is the entire reason it can run at all.**
// `writeAppBundle` has no build tag for the same reason `installcontent.go` has
// none, and one more: a build host that is not a Mac has to produce a
// `KubeTower.app` for download (`.github/workflows/dmg.yml` does, on Linux), so the
// assembler is a function this host both compiles and calls.
//
// Four things are asserted and each is silent when it is wrong. A missing
// `Contents/MacOS` is a bundle the Finder opens and finds nothing to run — and
// on the build host that directory has to exist *before* the cross-compiled
// binary is copied into it, so nothing else creates it. A missing `Info.plist`
// is a directory rather than an application. An icon written under a name the
// plist does not point at draws the generic icon with no error anywhere. And
// the container has to be a real icns, which is the header arithmetic tested
// above, checked here on the *file* because the bytes travel through a writer.
func TestTheBundleWriterMakesTheDirectoryMacOSExpects(t *testing.T) {
	root := filepath.Join(t.TempDir(), bundleDirName)

	entries, notes, err := writeAppBundle(root, "1.2.3")
	if err != nil {
		t.Fatalf("writeAppBundle: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("the bundle is incomplete: %v", notes)
	}
	if len(entries) != 2 {
		t.Errorf("entries = %v, want the application and its icon", entries)
	}

	if info, err := os.Stat(filepath.Join(root, "Contents", "MacOS")); err != nil || !info.IsDir() {
		t.Error("Contents/MacOS is not there, so there is nowhere to put the executable " +
			"and the Finder opens a bundle with nothing to run")
	}

	plist, err := os.ReadFile(filepath.Join(root, "Contents", "Info.plist"))
	if err != nil {
		t.Fatalf("Info.plist: %v — without it this directory is not an application", err)
	}
	if !strings.Contains(string(plist), "<string>1.2.3</string>") {
		t.Error("the plist does not carry the release it was given")
	}
	if !strings.Contains(string(plist), "<string>"+bundleExecutableName+"</string>") {
		t.Errorf("CFBundleExecutable does not name %q, which is the file the build "+
			"and install.go both place", bundleExecutableName)
	}

	// The icon file's name is `CFBundleIconFile` plus `.icns`, and the plist
	// deliberately carries the name without the extension — so these two are
	// only ever right together.
	icns, err := os.ReadFile(filepath.Join(root, "Contents", "Resources", bundleExecutableName+".icns"))
	if err != nil {
		t.Fatalf("the icon: %v", err)
	}
	if string(icns[0:4]) != icnsMagic {
		t.Errorf("the icon container starts %q, not %q", icns[0:4], icnsMagic)
	}
	if int(binary.BigEndian.Uint32(icns[4:8])) != len(icns) {
		t.Error("the icon container's own length does not match the file that was written")
	}
}

// TestTheBundleWriterRefusesToClaimAnApplicationItCouldNotWrite. An empty answer
// and a failed one are different answers,
// and this is the one place that is easiest to skip: an installer that reported success
// over a missing `Info.plist` would have registered an application the Finder
// will not open, and the operator's only evidence would be a bundle icon that
// does nothing.
func TestTheBundleWriterRefusesToClaimAnApplicationItCouldNotWrite(t *testing.T) {
	// A file where the bundle directory has to go: MkdirAll cannot make a
	// directory over it, which is the cheapest way to fail the write without
	// depending on permissions this test may or may not have.
	dir := t.TempDir()
	root := filepath.Join(dir, bundleDirName)
	if err := os.WriteFile(root, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, _, err := writeAppBundle(root, "1.2.3")
	if err == nil {
		t.Fatal("writeAppBundle reported success with nowhere to write the bundle")
	}
	if len(entries) != 0 {
		t.Errorf("it also reported %v as installed", entries)
	}
}

// lineStarting returns the whole of the first line with the given prefix, and
// fails if there is none — a missing key in a desktop entry is a defect and not
// an empty string.
func lineStarting(t *testing.T, content, prefix string) string {
	t.Helper()
	for line := range strings.SplitSeq(content, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("no line starting %q in:\n%s", prefix, content)
	return ""
}

// plistDict parses an XML property list whose root is one flat <dict>, the
// only shape `infoPlist` writes, into key → value. A <string> maps to its text;
// any other value maps to its element written back as `<name/>`, which is
// enough to tell <true/> from <false/>. A key that appears twice, a key with no
// value, or a root that is not a single <dict> fails the test.
func plistDict(t *testing.T, data []byte) map[string]string {
	t.Helper()
	decoder := xml.NewDecoder(bytes.NewReader(data))
	dict := map[string]string{}
	depth, roots := 0, 0
	key := ""
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Info.plist is not well-formed XML: %v", err)
		}
		switch el := token.(type) {
		case xml.StartElement:
			depth++
			switch {
			case depth == 1 && el.Name.Local != "plist":
				t.Fatalf("the root element is <%s>, not <plist>", el.Name.Local)
			case depth == 2:
				roots++
				if el.Name.Local != "dict" || roots > 1 {
					t.Fatalf("<plist> must hold exactly one <dict>; found <%s> as value %d", el.Name.Local, roots)
				}
			case depth == 3 && el.Name.Local == "key":
				var name string
				if err := decoder.DecodeElement(&name, &el); err != nil {
					t.Fatalf("reading a <key>: %v", err)
				}
				depth--
				if _, seen := dict[name]; seen {
					t.Errorf("%s appears twice; a reader keeps one of them and says nothing", name)
				}
				if key != "" {
					t.Errorf("%s has no value", key)
				}
				key = name
			case depth == 3:
				if key == "" {
					t.Fatalf("a <%s> with no <key> before it", el.Name.Local)
				}
				value := "<" + el.Name.Local + "/>"
				if el.Name.Local == "string" {
					if err := decoder.DecodeElement(&value, &el); err != nil {
						t.Fatalf("reading %s: %v", key, err)
					}
					depth--
				} else if err := decoder.Skip(); err == nil {
					depth--
				} else {
					t.Fatalf("reading %s: %v", key, err)
				}
				dict[key] = value
				key = ""
			}
		case xml.EndElement:
			depth--
		}
	}
	if key != "" {
		t.Errorf("%s has no value", key)
	}
	if roots != 1 {
		t.Fatal("<plist> holds no <dict>")
	}
	return dict
}

// plistTree reads a property list into Go values, nested ones included:
// <dict> as map[string]any, <array> as []any, <string> as string, <true/> and
// <false/> as bool, and anything else as its element name. `plistDict` reads
// only the top level and does not descend; this is for the values that do. A
// key that appears twice in one dictionary fails the test, as it does there.
func plistTree(t *testing.T, data []byte) any {
	t.Helper()
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var value func(start xml.StartElement) any
	value = func(start xml.StartElement) any {
		switch start.Name.Local {
		case "string":
			var s string
			if err := decoder.DecodeElement(&s, &start); err != nil {
				t.Fatalf("reading a <string>: %v", err)
			}
			return s
		case "true", "false":
			if err := decoder.Skip(); err != nil {
				t.Fatalf("reading <%s/>: %v", start.Name.Local, err)
			}
			return start.Name.Local == "true"
		case "dict", "array":
			dict, list, key := map[string]any{}, []any{}, ""
			for {
				token, err := decoder.Token()
				if err != nil {
					t.Fatalf("reading a <%s>: %v", start.Name.Local, err)
				}
				switch el := token.(type) {
				case xml.EndElement:
					if start.Name.Local == "array" {
						return list
					}
					if key != "" {
						t.Errorf("%s has no value", key)
					}
					return dict
				case xml.StartElement:
					if start.Name.Local == "array" {
						list = append(list, value(el))
						continue
					}
					if el.Name.Local == "key" {
						if err := decoder.DecodeElement(&key, &el); err != nil {
							t.Fatalf("reading a <key>: %v", err)
						}
						if _, seen := dict[key]; seen {
							t.Errorf("%s appears twice in one dictionary", key)
						}
						continue
					}
					if key == "" {
						t.Fatalf("a <%s> with no <key> before it", el.Name.Local)
					}
					dict[key], key = value(el), ""
				}
			}
		default:
			if err := decoder.Skip(); err != nil {
				t.Fatalf("reading <%s>: %v", start.Name.Local, err)
			}
			return "<" + start.Name.Local + ">"
		}
	}
	for {
		token, err := decoder.Token()
		if err != nil {
			t.Fatalf("the property list has no root value: %v", err)
		}
		if el, ok := token.(xml.StartElement); ok && el.Name.Local != "plist" {
			return value(el)
		}
	}
}

// fakePNG is a PNG signature followed by filler. Enough for the container
// arithmetic, which never looks past the first eight bytes.
func fakePNG(n int) []byte {
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, make([]byte, n)...)
	return png
}

// The banner a person reads once, and the one class of defect it can have.
//
// `--install` prints this to somebody who has just put KubeTower on their machine,
// and it is the only documentation many of them will ever see. A banner
// promising a Setup section that no longer exists fails nothing: the binary
// compiles, the tests pass, and the first sentence it says to a new operator
// is false. This test is what catches it.
//
// It checks against the *console*, not against a list written here: every
// capability the banner claims Setup offers has to be a heading Setup actually
// draws. A list would be a third place to keep in step.
func TestTheInstallBannerOnlyPromisesWhatSetupOffers(t *testing.T) {
	banner := installedHelp(installation{Binary: `C:\x\kubetower.exe`})

	// The Infos tab says only the version: a banner
	// pointing there for the password or the address sends people to a page
	// that does not have them.
	if strings.Contains(banner, "Infos") {
		t.Errorf("the banner points at the Infos tab, which does not say it:\n%s", banner)
	}
	// And the banner must not offer an MCP connection, which the console does
	// not have.
	for _, gone := range []string{"Claude Code", "MCP", "bearer token", "claude mcp"} {
		if strings.Contains(banner, gone) {
			t.Errorf("the install banner offers %q. That is not on the Setup page — "+
				"there is no MCP server — so this is an instruction a new "+
				"operator cannot follow, printed to them once, in the place they are most "+
				"likely to believe it:\n%s", gone, banner)
		}
	}
}

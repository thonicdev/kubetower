package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// TestTheCommittedFilePropertiesSayDev.
//
// `rsrc_windows_amd64.syso` carries the RT_VERSION resource Explorer shows in
// **Properties → Details**, and it is a generated artifact checked in — the same
// bargain as the PWA bitmaps, made for the same reason: an unstamped build has to
// work offline, and regenerating the object needs a tool download. The cost of
// that bargain is that it is free to drift: a committed version number ends up
// in Properties while `--version` says something else.
//
// So the version does not live in the committed object. **It says `dev`**, the
// answer `--version` gives for a build nobody stamped, and a release build gets
// its own: `cmd/ktbuild -version X` regenerates the object from the tag before it
// links windows/amd64, reads the version back out of the `.exe` it produced, and
// puts this one back. What is asserted here is the half that stays in the tree:
// the object and the JSON beside it both say `dev`, so a release number cannot
// be committed back by accident.
//
// Not build-tagged for Windows: it reads two files, and it has to run on the
// Linux pool where every other gate runs.
//
// The .syso is a COFF object, not a PE, so this looks for the version as the
// UTF-16 string a VERSIONINFO block stores rather than parsing the resource
// directory. Cheap, and it fails on exactly the drift it is written for.
//
// Regenerating, when this fails — from `internal/launch`:
//
//	go run github.com/tc-hib/go-winres@v0.3.3 make --in winres/winres.json \
//	    --arch amd64 --product-version dev --file-version dev
func TestTheCommittedFilePropertiesSayDev(t *testing.T) {
	const want = devVersion

	syso, err := os.ReadFile("rsrc_windows_amd64.syso")
	if err != nil {
		t.Fatalf("the resource object is missing, so the binary has no icon and no version: %v", err)
	}
	if !strings.Contains(string(syso), utf16String(want)) {
		t.Errorf("rsrc_windows_amd64.syso does not carry %q. A release's version is stamped by "+
			"cmd/ktbuild at build time and never committed; regenerate this one with the "+
			"command in the doc comment above", want)
	}

	// And the JSON beside it, which is what a person edits and reads. It is not
	// what the generator uses for the version — that comes from the flags — so it
	// can be right while the object is wrong, or the reverse. Both are checked
	// because neither implies the other.
	manifest, err := os.ReadFile(filepath.Join("winres", "winres.json"))
	if err != nil {
		t.Fatalf("winres.json is missing: %v", err)
	}
	if !strings.Contains(string(manifest), `"ProductVersion": "`+want+`"`) {
		t.Errorf("winres/winres.json names another version than %q. The generator takes "+
			"the version from its flags, so this file being wrong does not break the build — it "+
			"just misleads the next person to open it", want)
	}
}

// utf16String is how a VERSIONINFO block stores a string: UTF-16, little-endian,
// which is why a plain substring search over the object finds nothing.
func utf16String(s string) string {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 0, len(units)*2)
	for _, unit := range units {
		out = append(out, byte(unit), byte(unit>>8))
	}
	return string(out)
}

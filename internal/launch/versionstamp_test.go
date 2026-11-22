package launch

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
)

// TestEveryReleaseBuildStampsThisPackage holds the one thing the move out of
// `main` could break without a single failing build.
//
// `-X` names a variable by its package path, and **a path that names nothing is
// not an error**: the linker ignores it. A `-X main.version=…` compiles, links,
// and ships a binary that calls itself `dev` — `developmentBuild` true, the
// update check off, the Settings page saying no release was stamped. Nothing
// red anywhere. The two
// producers of a release are read here, and each must stamp this package and
// must not stamp `main`: the image's Dockerfile, and `cmd/ktbuild`, which builds
// every archive and the installer.
//
// **And none stamps an edition.** There is no edition, and `-X` on a name that
// does not exist is silently ignored, so a stamp left behind would be a
// setting that appears to work.
func TestEveryReleaseBuildStampsThisPackage(t *testing.T) {
	const here = "github.com/thonicdev/kubetower/internal/launch"

	// The image's own build. Always present: the image's build stage runs this
	// suite on its own context, and the Dockerfile is in that context.
	t.Run("Dockerfile", func(t *testing.T) {
		assertStamps(t, "../../Dockerfile", false, []string{
			"-X " + here + ".version=",
			"-X " + here + ".built=",
		})
		refuses(t, "../../Dockerfile", false, []string{".edition=", "KT_EDITION"})
	})

	// The build tool every archive goes through. Always present, the image's
	// build context included.
	t.Run("cmd/ktbuild", func(t *testing.T) {
		assertStamps(t, "../../cmd/ktbuild/main.go", false, []string{
			"-X " + here + ".version=",
			"-X " + here + ".built=",
		})
		refuses(t, "../../cmd/ktbuild/main.go", false, []string{".edition="})
	})

	// The release workflow builds through the tool and feeds the image the
	// same three values, so what is asserted is that it does both.
	//
	// **Skipped where the file is absent, and only there.** `.dockerignore`
	// keeps `.github` out of the image's build context, and that build stage
	// runs `go test ./...` - so inside the image this file does not exist, and
	// failing there would fail every image build for a file the image never
	// sees. A developer's tree and CI both have it, which is where the check
	// is worth something.
	t.Run("release workflow", func(t *testing.T) {
		assertStamps(t, "../../.github/workflows/release.yml", true, []string{
			`go run ./cmd/ktbuild -os "$os" -arch "$arch" -version "$VERSION" -built "$BUILT"`,
			"KT_VERSION=${{ needs.verify.outputs.version }}",
			"KT_BUILT=${{ needs.verify.outputs.built }}",
		})
		refuses(t, "../../.github/workflows/release.yml", true, []string{"KT_EDITION"})
	})
}

func assertStamps(t *testing.T, file string, skipIfAbsent bool, want []string) {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		if skipIfAbsent && errors.Is(err, fs.ErrNotExist) {
			t.Skipf("%s is not in this tree (the image's build context excludes it), so what it "+
				"stamps is UNCHECKED here: %v", file, err)
		}
		t.Fatalf("%s: %v. It is a release build, and nothing else checks what it stamps", file, err)
	}
	text := string(raw)
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("%s does not carry %q, so its binaries are unstamped", file, w)
		}
	}
	if strings.Contains(text, "-X main.") {
		t.Errorf("%s still stamps `main`, which holds none of these variables since the start-up "+
			"moved to github.com/thonicdev/kubetower/internal/launch: the linker ignores it and "+
			"the release calls itself dev", file)
	}
}

// refuses fails when file carries any of unwanted: a stamp of a name that does
// not exist, which the linker ignores without a word.
func refuses(t *testing.T, file string, skipIfAbsent bool, unwanted []string) {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		if skipIfAbsent && errors.Is(err, fs.ErrNotExist) {
			t.Skipf("%s is not in this tree, so it is UNCHECKED here: %v", file, err)
		}
		t.Fatalf("%s: %v", file, err)
	}
	for _, u := range unwanted {
		if strings.Contains(string(raw), u) {
			t.Errorf("%s carries %q: there is no edition, and a stamp of one "+
				"is silently ignored", file, u)
		}
	}
}

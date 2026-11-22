package boundary_test

import (
	"os"
	"strings"
	"testing"
)

// TestThisModuleIsOutsideTheCoresPath is the assertion the rest of this module
// rests on, and it is not decoration.
//
// Go's internal rule is **path-based**: an import of a path containing the
// element `internal` is refused unless the importing code is inside the tree
// rooted at the parent of that directory, and the tree is decided by the module
// path rather than by where the directory sits on disk. This module lives in the
// core's own working copy, one directory down. If its module path were
// `github.com/thonicdev/kubetower/boundary`, it would be **inside** that tree
// and allowed to import `internal/web` exactly as the fixture does - it would
// compile, every test here would pass, and the file would be proving nothing
// while reading as though it proved everything.
//
// That is a one-word mistake in go.mod with no other symptom, which is the kind
// this repository writes a test for rather than a comment.
//
// What the compiler answers when the path is right:
//
//	zz_probe.go:3:8: use of internal package
//	github.com/thonicdev/kubetower/internal/extras not allowed
//
// **`internal/extras` and not `internal/web`, and the reason is worth
// keeping.** A probe importing `internal/web` gets `go: updates to go.mod
// needed` instead - the resolver stops on the packages that import pulls in
// before it ever reaches the internal rule. A reader repeating this check with
// the obvious package would conclude the rule does not fire. `internal/extras`
// depends on `internal/statefile` and the standard library and nothing else, so
// the module graph is already satisfied and the refusal is the only thing left
// to say.
//
// That check cannot live here as compiled code - a file that must not compile
// cannot sit in a package that must - so what is permanent is the property that
// makes the refusal happen, which is the module path below.
func TestThisModuleIsOutsideTheCoresPath(t *testing.T) {
	const core = "github.com/thonicdev/kubetower"

	raw, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("reading this module's go.mod: %v", err)
	}
	path := modulePath(t, string(raw))

	if path == core || strings.HasPrefix(path, core+"/") {
		t.Fatalf("this module's path is %q, which is inside %q.\n"+
			"Go's internal rule is path-based, so from there `internal/` is importable and "+
			"nothing in this module proves anything about the extension contract.", path, core)
	}
}

// TestTheCoreIsTheWorkingCopyAndNotAPublishedVersion guards the other half of
// the arrangement: the `replace` is what makes this module test the tree it
// ships with. Without it the module would resolve some published version of the
// core - and there is none, so it would fail to build for a reason that has
// nothing to do with the contract.
func TestTheCoreIsTheWorkingCopyAndNotAPublishedVersion(t *testing.T) {
	raw, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("reading this module's go.mod: %v", err)
	}
	if !strings.Contains(string(raw), "replace github.com/thonicdev/kubetower => ../") {
		t.Fatal("go.mod no longer replaces the core with the working copy, so this module is " +
			"testing something other than the tree it sits in")
	}
}

// modulePath is the argument of the `module` directive.
//
// A scan rather than a parse: `golang.org/x/mod/modfile` would be a dependency
// added to a module whose dependency list is itself evidence, and the directive
// is one line.
func modulePath(t *testing.T, gomod string) string {
	t.Helper()
	for line := range strings.SplitSeq(gomod, "\n") {
		line = strings.TrimSpace(line)
		if after, found := strings.CutPrefix(line, "module "); found {
			return strings.TrimSpace(after)
		}
	}
	t.Fatal("no `module` directive in go.mod")
	return ""
}

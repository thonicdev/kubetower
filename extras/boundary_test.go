// Package extras_test holds the boundary the extras are kept behind, which
// the compiler does not hold for them.
//
// An extra reaches the console through `extension.Host` and the public
// packages, never `internal/`. Go's `internal/` rule is path-based, so an
// import of `github.com/thonicdev/kubetower/internal/...` from `extras/` -
// inside this module - compiles. And an extra must never reach the console's
// own write door (`internal/access`'s Gate.Perform, `internal/edit`): every
// write goes through a reviewed Host method. This test is what refuses the
// import that would reach it.
package extras_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/thonicdev/kubetower"

// allowed is every package of this module, outside its own tree, an extra may
// import: the contract and the public leaves - svcgroup for the Redis and
// Database viewers, and safego for starting goroutines. A new entry is a
// decision about the boundary, made in review, never a way to make this test
// pass.
var allowed = map[string]string{
	module + "/extension": "the contract: Extension, Route and Host",
	module + "/schedule":  "public leaf: the CronJob controller's own schedule parser",
	module + "/textutil":  "public leaf: first lines and clipping",
	module + "/svcgroup":  "public leaf: Services folded onto their pods, from objects the caller listed",
	// The one way a goroutine is started in this module,
	// the extras included (safego's guard test walks extras/ too). Standard
	// library only, and it reaches nothing of the console.
	module + "/safego": "public leaf: a goroutine whose panic is recovered, logged and returned as an error",
}

// clientGo is Kubernetes' client library. A file of an extra that is not a
// test never imports it: a client built there would read the cluster with the
// console's credentials, reviewed for nobody.
const clientGo = "k8s.io/client-go"

// allowedInTests is what a test file of an extra may import beyond allowed:
// `app`, to register the extra as `cmd/kubetower` does. It is the public door
// onto the start-up, not a way past it.
var allowedInTests = map[string]string{
	module + "/app": "register the extra the way cmd/kubetower does",
}

// TestTheExtrasReachTheCoreThroughItsPublicPackagesOnly reads the import block
// of every .go file under extras/ with go/parser - **whatever its build
// constraints**, so a `//go:build linux` file is checked on Windows too, which
// `go list` would not do - and refuses:
//
//   - any package of this module that is not in allowed (allowedInTests for a
//     _test.go file), internal/ above all;
//   - k8s.io/client-go, outside a test: an extra reads a cluster through Host
//     alone;
//   - another extra's packages: an extra may import its own tree
//     (`extras/<same extra>/...`) and nothing else under `extras/`, so two
//     extras stay separable from each other.
func TestTheExtrasReachTheCoreThroughItsPublicPackagesOnly(t *testing.T) {
	fset := token.NewFileSet()
	files := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		files++
		slash := filepath.ToSlash(path)
		owner, _, nested := strings.Cut(slash, "/")
		if !nested {
			owner = "" // a file at the top of extras/, this one: no extra of its own
		}
		for _, spec := range file.Imports {
			imp, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if why := refusal(imp, owner, strings.HasSuffix(path, "_test.go")); why != "" {
				t.Errorf("extras/%s imports %s: %s", slash, imp, why)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading extras/: %v", err)
	}
	// The floor: `find extras -name '*.go' | wc -l` counts well above it. Far
	// fewer means the walk stopped seeing the extras, and an
	// empty walk would pass every rule above.
	if files < 30 {
		t.Fatalf("read %d .go files under extras/; at least 30 exist, so the walk is not seeing the extras", files)
	}
	t.Logf("%d .go files under extras/, every build constraint included, each importing only %d "+
		"public packages of the module and its own extra's", files, len(allowed))
}

// refusal says why imp may not be imported from a file of the extra owner, or
// "" when it may.
func refusal(imp, owner string, inTest bool) string {
	switch {
	case !inTest && (imp == clientGo || strings.HasPrefix(imp, clientGo+"/")):
		return "an extra holds no Kubernetes client of its own: what it reads of a cluster goes " +
			"through extension.Host, whose doors review it for the person (Host.Client answers " +
			"discovery only, and its type is in extension)"
	case imp != module && !strings.HasPrefix(imp, module+"/"):
		return "" // the standard library and third-party modules are go.mod's question
	case owner != "" && (imp == module+"/extras/"+owner || strings.HasPrefix(imp, module+"/extras/"+owner+"/")):
		return "" // the extra's own packages
	case owner == "" && inTest && strings.HasPrefix(imp, module+"/extras/"):
		return "" // a test at the top of extras/ drives every extra together
	case imp == module+"/extras" || strings.HasPrefix(imp, module+"/extras/"):
		return "another extra's package; an extra imports only its own tree, so two extras " +
			"stay separable from each other"
	case allowed[imp] != "":
		return ""
	case inTest && allowedInTests[imp] != "":
		return ""
	case imp == module+"/internal" || strings.HasPrefix(imp, module+"/internal/"):
		return "an extra reaches the console through extension.Host, never internal/ " +
			"(the extension contract keeps the write door out of an extension's reach)"
	default:
		return "not one of the public packages an extra may use; add it to `allowed` only as a " +
			"decision about the boundary"
	}
}

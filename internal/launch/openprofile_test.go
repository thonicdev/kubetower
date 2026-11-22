package launch

import (
	"bytes"
	"go/ast"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/extras"
	"github.com/thonicdev/kubetower/internal/statefile"
	"github.com/thonicdev/kubetower/internal/update"
)

// **A malformed KT_STATE_PERSISTENT is warned about once.** The variable is
// read once, in openProfile, and the boolean handed to every use: the archive,
// the extras line, the kinds column and the web server. A second read is a
// second warning in the log, and two reads are free to disagree.
//
// openProfile itself, on the desktop profile, against a kubeconfig of one
// context in a temporary directory - the profile phase stops short of
// listening, so it can run here. The server profile cannot: it needs a pod's
// ServiceAccount.
func TestOpenProfileWarnsOnceAboutAMalformedStatePersistent(t *testing.T) {
	extras.Register(extras.Extra{Key: "alfa", Label: "Alfa page", Summary: "A fixture page.", Needs: "nothing"})
	t.Cleanup(extras.Reset)
	t.Setenv(extras.EnvExtras, "")

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv(statefile.EnvDir, filepath.Join(dir, "state"))
	t.Setenv(update.EnvEnabled, "off")
	t.Setenv(cluster.EnvKubeconfig, writeContextKubeconfig(t, filepath.Join(dir, "kubeconfig"), "alpha"))
	t.Setenv(envStatePersistent, "maybe")

	var said bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&said)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags) })

	p, err := openProfile(false)
	if err != nil {
		t.Fatalf("openProfile(false): %v", err)
	}
	log.SetOutput(prev)

	warning := envStatePersistent + " is not a yes/no value"
	if n := strings.Count(said.String(), warning); n != 1 {
		t.Errorf("a malformed %s is warned about %d times, want once. The log:\n%s",
			envStatePersistent, n, said.String())
	}
	if !p.statePersistent {
		t.Errorf("a malformed %s reads as not persistent, want the fallback (persistent)", envStatePersistent)
	}
	if n := strings.Count(said.String(), "extras enabled: "); n != 1 {
		t.Errorf("the extras line is logged %d times, want once. The log:\n%s", n, said.String())
	}
}

// **openProfile logs the extras line through extrasStartupLine**, handing it
// the statePersistent it read once - and the same one the kinds column gets.
//
// extrasStartupLine's own tests hold the sentence and the profile's polarity;
// this holds the call site, which no test runs under the server profile (it
// needs a pod). Rewriting the call as extrasSummary(xtra, ..., extras.ProfileOf(...))
// prints the same line on the desktop and a different one in a cluster, and
// fails here.
func TestOpenProfileLogsTheExtrasLineThroughExtrasStartupLine(t *testing.T) {
	_, funcs := runSource(t)
	open := funcs["openProfile"]
	if open == nil {
		t.Fatal("no func openProfile in run.go")
	}

	// The line is built by extrasStartupLine, once, and by nothing beside it.
	calls := callsTo(open, "extrasStartupLine")
	if len(calls) != 1 {
		t.Fatalf("openProfile calls extrasStartupLine %d times, want once: the extras line "+
			"is that function's, whose tests hold the profile it names", len(calls))
	}
	for _, name := range []string{"extrasSummary", "extrasStartupProfile", "ProfileOf"} {
		if n := len(callsTo(open, name)); n != 0 {
			t.Errorf("openProfile calls %s %d times: the extras line is built by "+
				"extrasStartupLine, under its test, and not beside it", name, n)
		}
	}
	if callers := callersOf(funcs, "extrasSummary"); !equalNames(callers, "extrasStartupLine") {
		t.Errorf("extrasSummary is called by %v, want extrasStartupLine alone", callers)
	}

	// The persistence it is handed is the variable openProfile read once.
	persistent := boundTo(open, "readStatePersistent")
	if persistent == "" {
		t.Fatal("openProfile does not bind readStatePersistent(...) to a variable of its own")
	}
	if got := lastArg(calls[0]); got != persistent {
		t.Errorf("extrasStartupLine is handed %q as the state's persistence, want %q, "+
			"the variable readStatePersistent was read into", got, persistent)
	}
	kinds := callsTo(open, "openKindPrefs")
	if len(kinds) != 1 || lastArg(kinds[0]) != persistent {
		t.Errorf("openKindPrefs is not handed %q: the extras line and the kinds column "+
			"would read the state's persistence differently", persistent)
	}

	// Read once: readStatePersistent is the one reader of the variable in
	// run.go, and openProfile its one caller, once.
	if readers := readersOf(funcs, "envStatePersistent"); !equalNames(readers, "readStatePersistent") {
		t.Errorf("%s is parsed by %v, want readStatePersistent alone", envStatePersistent, readers)
	}
	if callers := callersOf(funcs, "readStatePersistent"); !equalNames(callers, "openProfile") {
		t.Errorf("readStatePersistent is called by %v, want openProfile alone", callers)
	}
	if n := len(callsTo(open, "readStatePersistent")); n != 1 {
		t.Errorf("openProfile calls readStatePersistent %d times, want once", n)
	}
}

// callsTo is every call in fn's body made through name.
func callsTo(fn *ast.FuncDecl, name string) []*ast.CallExpr {
	var calls []*ast.CallExpr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && calledName(call) == name {
			calls = append(calls, call)
		}
		return true
	})
	return calls
}

// boundTo is the variable fn assigns the one result of a call to name to
// (x := name(...)), or "".
func boundTo(fn *ast.FuncDecl, name string) string {
	bound := ""
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		if call, ok := assign.Rhs[0].(*ast.CallExpr); ok && calledName(call) == name {
			if id, ok := assign.Lhs[0].(*ast.Ident); ok {
				bound = id.Name
			}
		}
		return true
	})
	return bound
}

// lastArg is the name of a call's last argument when it is a plain
// identifier, or "".
func lastArg(call *ast.CallExpr) string {
	if len(call.Args) == 0 {
		return ""
	}
	if id, ok := call.Args[len(call.Args)-1].(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// readersOf names every function in funcs that hands the identifier key to
// envBool or envBoolFrom.
func readersOf(funcs map[string]*ast.FuncDecl, key string) []string {
	var readers []string
	for fnName, fn := range funcs {
		reads := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || (calledName(call) != "envBool" && calledName(call) != "envBoolFrom") {
				return true
			}
			for _, arg := range call.Args {
				if id, ok := arg.(*ast.Ident); ok && id.Name == key {
					reads = true
				}
			}
			return true
		})
		if reads {
			readers = append(readers, fnName)
		}
	}
	return readers
}

// equalNames reports whether got holds exactly the names in want, in any order.
func equalNames(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}
	return true
}

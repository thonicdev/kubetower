package safego

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestNoBareGoStatement: every `go` statement in the module's production code
// is inside this package, or is listed in bareGo with the reason it is not
// cluster-derived work, so no new goroutine runs a cluster's answers with
// nothing to recover a panic.
//
// **What it reads.** Every .go file under the module root that is not a test,
// parsed with go/parser **whatever its build constraints**, so a
// `_windows.go` file is checked on Linux too - `go list` would skip it. web/'s
// node_modules, testdata and .git are not walked.
//
// **How a site is named.** `<file>:<enclosing function>`, a method as
// `Type.Method`, with how many `go` statements that function holds. A line
// number would make every unrelated edit above a listed site a red test; a
// count makes a second bare `go` added to an allowlisted function one.
//
// **The list is held to the code in both directions**: a listed site that no
// longer has its `go` statement fails too, so the list is never longer than
// the truth.
func TestNoBareGoStatement(t *testing.T) {
	found := bareGoSites(t, "..")
	var names []string
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		allowed, listed := bareGo[name]
		switch {
		case !listed:
			t.Errorf("%s starts %d goroutine(s) with a bare `go` statement. Start it with safego.Go, "+
				"so a panic is that task's error rather than the end of the process; "+
				"or, if it runs nothing derived from a cluster, add it to bareGo with the reason", name, found[name])
		case found[name] > allowed.n:
			t.Errorf("%s now holds %d bare `go` statements and bareGo allows %d: the new one goes through safego.Go",
				name, found[name], allowed.n)
		}
	}
	for name, allowed := range bareGo {
		if found[name] < allowed.n {
			t.Errorf("bareGo lists %d bare `go` statement(s) in %s and the code holds %d: shorten the list", allowed.n, name, found[name])
		}
		if strings.TrimSpace(allowed.why) == "" {
			t.Errorf("bareGo lists %s with no reason", name)
		}
	}
	total := 0
	for _, n := range found {
		total += n
	}
	// The floor: a walk that found nothing would pass every rule above. The
	// list holds well above it (`go test ./safego -run TestNoBareGoStatement -v`),
	// this package's own statement not counted.
	if total < 15 {
		t.Fatalf("found %d bare `go` statements in the module; at least 15 are listed, so the walk is not seeing the code", total)
	}
	t.Logf("%d bare `go` statements in %d functions, each listed with its reason", total, len(found))
}

// bareGoSites walks root and counts the `go` statements of each function
// outside this package and outside tests.
func bareGoSites(t *testing.T, root string) map[string]int {
	t.Helper()
	found := map[string]int{}
	walkModule(t, root, func(rel string, file *ast.File) { countGoStatements(rel, file, found) })
	return found
}

// unrecoveredCallSites walks root and counts, per function and per form, the
// calls that start a goroutine without a `go` statement.
func unrecoveredCallSites(t *testing.T, root string) map[string]int {
	t.Helper()
	found := map[string]int{}
	walkModule(t, root, func(rel string, file *ast.File) { countUnrecoveredCalls(rel, file, found) })
	return found
}

// walkModule parses every non-test .go file under root, outside this package,
// and hands it to visit with its slash-separated path relative to root.
func walkModule(t *testing.T, root string, visit func(rel string, file *ast.File)) {
	t.Helper()
	fset := token.NewFileSet()
	files := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "safego/") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		files++
		visit(rel, file)
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module: %v", err)
	}
	// The module holds hundreds of non-test .go files; far fewer means the walk
	// started in the wrong place.
	if files < 200 {
		t.Fatalf("read %d non-test .go files; the module holds hundreds, so the walk is not seeing it", files)
	}
}

// eachDecl calls visit on every node of every declaration of file, with the
// name of the function it is in. Every declaration and not only functions: a
// function literal held in a package-level variable starts goroutines too.
func eachDecl(file *ast.File, visit func(where string, n ast.Node)) {
	for _, decl := range file.Decls {
		where := "(package level)"
		if fn, ok := decl.(*ast.FuncDecl); ok {
			where = funcName(fn)
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			if n != nil {
				visit(where, n)
			}
			return true
		})
	}
}

// countGoStatements adds file's `go` statements to found, per function.
func countGoStatements(rel string, file *ast.File, found map[string]int) {
	eachDecl(file, func(where string, n ast.Node) {
		if _, ok := n.(*ast.GoStmt); ok {
			found[rel+":"+where]++
		}
	})
}

// countUnrecoveredCalls adds to found, as `<file>:<function> <form>`, every
// call in file that runs a function whose panic nobody recovers, or whose
// recover re-panics, and that the go-statement guard never sees:
//
//   - `time.AfterFunc` and `context.AfterFunc`, under whatever name the file
//     imports the package;
//   - `.Go` and `.TryGo` called on a value rather than a package - errgroup's
//     Group, and any group shaped like it. Without type information this
//     cannot tell an errgroup from another type with a Go method; none other
//     exists in the module, and one that appears is a goroutine starter too,
//     or deserves the one line in the allow-list that says it is not;
//   - any reference to k8s.io/apimachinery's `wait.Group`, whose Start,
//     StartWithContext and StartWithChannel start goroutines with no recover.
//     The type is matched rather than the methods: `.Start` on a value names
//     too many unrelated things to be counted on its name alone. So the count
//     is of mentions of the type, per function, not of starts: a second
//     `g.Start` in a function that already names `wait.Group` adds nothing,
//     and `t.g.Start(...)` on a field whose type is named elsewhere (a struct
//     declared at package level) is counted where the type is named, not
//     where the goroutine starts. The allow-list line is for the type's use;
//     its reason has to cover every start through it;
//   - from the same wait package, every function whose name holds `Until`
//     but not `Poll`, and `Forever`: the loop that runs a function on a
//     period. It runs on the caller's goroutine, not one of its own, and
//     recovers through HandleCrash, which logs and re-panics; it is flagged as
//     a policy, because such a loop runs for the life of the process and its
//     caller is almost always a goroutine started for it. The Poll* functions
//     run their condition under HandleCrash too (apimachinery v0.37.1,
//     loop.go and wait.go), so a panic there re-panics as well; they are left
//     alone because they are a wait the caller blocks on: on a request's
//     goroutine net/http recovers the re-panic, and on a goroutine of its own
//     the `go` statement guard already sees the `go`.
//
// safego's own `safego.Go` is a call on a package, so it is not counted.
//
// **What it does not see**, because it reads syntax without type information:
// a dot import (`import . "time"`, then a bare `AfterFunc(...)`); a starter
// held in a function value (`after := time.AfterFunc; after(...)`), or passed
// as one; a method value (`start := g.Go`); and a goroutine started inside a
// dependency on our behalf. None of these is in the module; the guard is a
// net for the usual spellings, not a proof.
func countUnrecoveredCalls(rel string, file *ast.File, found map[string]int) {
	imported := map[string]string{} // local name -> import path
	for _, spec := range file.Imports {
		path := strings.Trim(spec.Path.Value, `"`)
		name := path[strings.LastIndex(path, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imported[name] = path
	}
	eachDecl(file, func(where string, n ast.Node) {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Group" {
			if id, ok := sel.X.(*ast.Ident); ok && imported[id.Name] == "k8s.io/apimachinery/pkg/util/wait" {
				found[rel+":"+where+" wait.Group"]++
			}
			return
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		fn := sel.Sel.Name
		pkg := ""
		if id, ok := sel.X.(*ast.Ident); ok {
			pkg = imported[id.Name]
		}
		form := ""
		switch {
		case fn == "AfterFunc" && (pkg == "time" || pkg == "context"):
			form = pkg + ".AfterFunc"
		case pkg == "k8s.io/apimachinery/pkg/util/wait" &&
			((strings.Contains(fn, "Until") && !strings.Contains(fn, "Poll")) || fn == "Forever"):
			form = "wait." + fn
		case pkg == "" && (fn == "Go" || fn == "TryGo"):
			form = "." + fn
		}
		if form != "" {
			found[rel+":"+where+" "+form]++
		}
	})
}

// funcName is `Name`, or `Type.Name` for a method.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	typ := fn.Recv.List[0].Type
	for {
		switch x := typ.(type) {
		case *ast.StarExpr:
			typ = x.X
			continue
		case *ast.IndexExpr:
			typ = x.X
			continue
		case *ast.IndexListExpr:
			typ = x.X
			continue
		case *ast.Ident:
			return x.Name + "." + fn.Name.Name
		}
		return fn.Name.Name
	}
}

type allowance struct {
	n   int
	why string
}

// bareGo is every `go` statement left bare, with why it is not cluster-derived
// work. **A new entry is a decision made in review**, never a way to make this
// test pass: the default answer is safego.Go.
var bareGo = map[string]allowance{
	// Recover already, each in its own place, and turn the panic into that
	// task's answer - the shape safego generalizes.
	"extras/redis/keys.go:Conn.describe":      {1, "describeOne recovers a panic into its own row (\"unreadable\")"},
	"internal/actions/bulk.go:Runner.RunBulk": {1, "runs oneConfined, which recovers a panic into that object's outcome"},
	"internal/overview/overview.go:gather":    {1, "gather recovers each task's panic into its panel, the shape safego generalizes"},

	// The process's own plumbing: no cluster's answer is read on them.
	"internal/launch/run.go:Run": {2, "the update check (a GitHub release, not a cluster) and the macOS window's " +
		"fact refresh - process plumbing"},
	"internal/launch/run.go:serve":                 {1, "the HTTP server's Serve - process plumbing; a panic in Serve is fatal by design"},
	"internal/launch/run.go:watchWindowStop":       {1, "the signal watcher - process plumbing; no cluster is asked"},
	"internal/launch/appwindow.go:(package level)": {1, "waits on the browser process it started, nothing else"},
	"internal/audit/audit.go:Log.write":            {1, "one Write to the audit sink, bounded by a timer; the bytes are the console's own record"},
	"internal/auth/oidc.go:Auth.Start":             {1, "single sign-on discovery against the identity provider, not a cluster"},
	"internal/archive/archive.go:Archive.Start":    {1, "the pruner of the archive's own files on this disk; no cluster is asked"},
	"internal/forward/forward.go:Manager.Open":     {1, "waits on a tunnel's Done and frees its local port; reads nothing from the cluster"},
	"internal/web/tunnelcap.go:counted":            {1, "waits on a tunnel's Done and gives its slot back"},

	// The terminal's byte relays: they copy a socket to a PTY or a pipe and
	// back, and decode nothing a cluster sent. The cluster-facing ones - the
	// exec stream, the tunnel, the re-check, the skew notice - go through
	// safego.
	"internal/terminal/exec.go:Terminal.streamExec": {2, "closes stdin when the browser goes, and says how the stream ended once it has: " +
		"waits on channels; the stream itself goes through safego"},
	"internal/terminal/limit.go:Terminal.Close":             {1, "waits for every session's goroutines at shutdown"},
	"internal/terminal/limit.go:farewellAll":                {1, "writes one closing frame to each socket at shutdown"},
	"internal/terminal/stream.go:newTimedStream":            {1, "the socket's keep-alive pings"},
	"internal/terminal/stream.go:stream.farewell":           {1, "writes one closing frame, bounded by farewellWait"},
	"internal/terminal/terminal.go:relay":                   {2, "copies bytes between the browser's socket and a local PTY"},
	"internal/terminal/localterm_windows.go:launchTerminal": {2, "waits on a local terminal window's process"},
	"internal/terminal/pty_windows.go:startPTYSession":      {1, "reaps a local ConPTY process"},

	// A development tool, not shipped in the binary.
	"cmd/devforward/main.go:supervise": {3, "the development port-forward supervisor (`task dev`): kubectl's own output, scanned for a log"},
}

// TestNoUnrecoveredGoroutineCall is TestNoBareGoStatement's other half: a timer, a context's AfterFunc, a group's Go and
// apimachinery's wait loops run a function on a goroutine of their own, and a
// panic there ends the process exactly as one under a bare `go` does - but
// there is no `go` statement for that guard to see. Each is started through
// safego (safego.AfterFunc for a timer) or listed in unrecoveredCalls with its
// reason, and the list is held to the code in both directions.
func TestNoUnrecoveredGoroutineCall(t *testing.T) {
	found := unrecoveredCallSites(t, "..")
	var names []string
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		allowed, listed := unrecoveredCalls[name]
		switch {
		case !listed:
			t.Errorf("%s runs a function on a goroutine of its own %d time(s), with nothing to recover a panic. "+
				"Use safego.AfterFunc for a timer, or wrap the function in safego.Call, so a panic is not the end of the "+
				"process; or, if it runs nothing derived from a cluster, add it to unrecoveredCalls "+
				"with the reason", name, found[name])
		case found[name] > allowed.n:
			t.Errorf("%s now holds %d such calls and unrecoveredCalls allows %d: the new one goes through safego",
				name, found[name], allowed.n)
		}
	}
	for name, allowed := range unrecoveredCalls {
		if found[name] < allowed.n {
			t.Errorf("unrecoveredCalls lists %d call(s) at %s and the code holds %d: shorten the list", allowed.n, name, found[name])
		}
		if strings.TrimSpace(allowed.why) == "" {
			t.Errorf("unrecoveredCalls lists %s with no reason", name)
		}
	}
	t.Logf("%d sites found: %v", len(names), names)
}

// TestTheGuardSeesEveryForm holds one fixture per form the guard names, and the
// counter-proof for each: the go-statement guard alone finds nothing in it,
// which is the hole; the call guard finds exactly that form. A fixture with
// safego's own calls, and a method named Go on a package, is the control.
func TestTheGuardSeesEveryForm(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"time.AfterFunc", `package p
import "time"
func f() { time.AfterFunc(time.Second, func() { panic("x") }) }`, "p.go:f time.AfterFunc"},
		{"time.AfterFunc under another name", `package p
import clock "time"
func f() { clock.AfterFunc(clock.Second, func() {}) }`, "p.go:f time.AfterFunc"},
		{"context.AfterFunc", `package p
import "context"
func f(ctx context.Context) { context.AfterFunc(ctx, func() {}) }`, "p.go:f context.AfterFunc"},
		{"errgroup Go", `package p
import "golang.org/x/sync/errgroup"
func f() { var g errgroup.Group; g.Go(func() error { return nil }); _ = g.Wait() }`, "p.go:f .Go"},
		{"errgroup TryGo", `package p
import "golang.org/x/sync/errgroup"
type T struct{ g *errgroup.Group }
func (t *T) f() { t.g.TryGo(func() error { return nil }) }`, "p.go:T.f .TryGo"},
		{"wait.Until", `package p
import "k8s.io/apimachinery/pkg/util/wait"
func f(stop <-chan struct{}) { wait.Until(func() {}, 1, stop) }`, "p.go:f wait.Until"},
		{"wait.JitterUntilWithContext", `package p
import "k8s.io/apimachinery/pkg/util/wait"
var start = func() { wait.JitterUntilWithContext(nil, nil, 1, 0, true) }`, "p.go:(package level) wait.JitterUntilWithContext"},
		{"wait.Forever", `package p
import "k8s.io/apimachinery/pkg/util/wait"
func f() { wait.Forever(func() {}, 1) }`, "p.go:f wait.Forever"},
		{"wait.Group", `package p
import "k8s.io/apimachinery/pkg/util/wait"
func f() { var g wait.Group; g.Start(func() {}); g.Wait() }`, "p.go:f wait.Group"},
		{"wait.Group as a field", `package p
import kwait "k8s.io/apimachinery/pkg/util/wait"
type T struct{ g kwait.Group }`, "p.go:(package level) wait.Group"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file := parseFixture(t, c.src)
			goStmts := map[string]int{}
			countGoStatements("p.go", file, goStmts)
			if len(goStmts) != 0 {
				t.Fatalf("the fixture holds a `go` statement (%v), so it proves nothing about the hole", goStmts)
			}
			calls := map[string]int{}
			countUnrecoveredCalls("p.go", file, calls)
			if len(calls) != 1 || calls[c.want] != 1 {
				t.Errorf("the call guard found %v, want exactly %q once", calls, c.want)
			}
		})
	}

	control := parseFixture(t, `package p
import (
	"time"
	"github.com/thonicdev/kubetower/safego"
	other "example.com/group"
	"k8s.io/apimachinery/pkg/util/wait"
)
func f() {
	safego.Go("x", func() error { return nil }, nil)
	safego.AfterFunc("x", time.Second, func() {})
	other.Go()
	other.Group()
	time.Sleep(0)
	_ = wait.PollUntilContextCancel(nil, time.Second, true, nil)
	_ = wait.PollUntilContextTimeout(nil, time.Second, time.Minute, true, nil)
}`)
	calls := map[string]int{}
	countUnrecoveredCalls("p.go", control, calls)
	if len(calls) != 0 {
		t.Errorf("the control - safego's own calls, a package's Go and Group, wait's Poll loops - was flagged: %v", calls)
	}
}

func parseFixture(t *testing.T, src string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "p.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("the fixture does not parse: %v", err)
	}
	return file
}

// unrecoveredCalls is every call that starts a goroutine without a `go`
// statement and without safego, with why a panic there cannot happen or
// cannot matter. The same rule as bareGo: **a new entry is a decision made in
// review**, and the default answer is safego.AfterFunc or safego.Call.
var unrecoveredCalls = map[string]allowance{
	"internal/terminal/portforward.go:boundUpgrade context.AfterFunc": {1, "calls the context's own CancelFunc " +
		"once its deadline has passed; a CancelFunc reads nothing and does not panic"},
	"internal/launch/launchpage.go:browserTarget time.AfterFunc": {1, "removes the launch page this process wrote " +
		"on this disk; no cluster is involved, and os.Remove reports rather than panics"},
	"extras/runtimeinspect/node.go:cdp.call context.AfterFunc": {1, "ends a DevTools read on the Node.js " +
		"inspector when its request ends; the function it runs is wrapped in safego.Call"},
	"internal/web/hostconsole.go:endsWithRequest context.AfterFunc": {1, "closes a console or pod tunnel when " +
		"its request ends; the function it runs is wrapped in safego.Call, which recovers a panic in the teardown"},
}

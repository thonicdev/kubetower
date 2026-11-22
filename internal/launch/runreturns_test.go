package launch

// Run is the start-up a binary embeds (app.Run), so it hands its failures back
// and leaves nothing of the console running once it has returned. These read
// run.go: Run itself serves on a real port, and the test that starts and stops
// it is boundary/'s.

import (
	"go/ast"
	"go/token"
	"strings"
	"testing"
)

// No path in run.go ends the process under its caller: a failure is an error
// Run returns, and the caller decides how the process ends.
func TestRunNeverExitsTheProcess(t *testing.T) {
	fset, funcs := runSource(t)
	for name, fn := range funcs {
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if (pkg.Name == "os" && sel.Sel.Name == "Exit") ||
				(pkg.Name == "log" && strings.HasPrefix(sel.Sel.Name, "Fatal")) {
				t.Errorf("%s: %s calls %s.%s, which ends the process under app.Run's caller; return the error",
					fset.Position(call.Pos()), name, pkg.Name, sel.Sel.Name)
			}
			return true
		})
	}
}

// What Run starts runs on Run's own context - which ends when Run returns -
// and never on context.Background(), which never ends. The phases named here
// are the ones that start something that keeps running.
func TestWhatRunStartsEndsWithRun(t *testing.T) {
	fset, funcs := runSource(t)
	for _, name := range []string{"Run", "startWorkers", "openArchive", "wireEntryGate"} {
		fn := funcs[name]
		if fn == nil {
			t.Fatalf("no func %s in run.go", name)
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Background" {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "context" {
					t.Errorf("%s: %s uses context.Background(), which outlives Run; use Run's context",
						fset.Position(call.Pos()), name)
				}
			}
			return true
		})
	}
}

// The metrics pool is released on the way out: its live sessions each hold a
// tunnel into a cluster.
func TestRunReleasesTheMetricsPool(t *testing.T) {
	_, funcs := runSource(t)
	found := token.NoPos
	ast.Inspect(funcs["Run"].Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || calledName(call) != "add" || len(call.Args) != 1 {
			return true
		}
		if sel, ok := call.Args[0].(*ast.SelectorExpr); ok && sel.Sel.Name == "Close" {
			if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "measure" {
				found = call.Pos()
			}
		}
		return true
	})
	if found == token.NoPos {
		t.Error("Run does not add the metrics pool's Close to what it undoes on the way out")
	}
}

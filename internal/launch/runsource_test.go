package launch

// Helpers for the tests that read run.go rather than run it. Run is not started
// by a test - it would serve on a real port with a real kubeconfig - and Run is
// split into phases, so a property it holds can sit in Run's own body (the
// order it calls the phases in) or inside one phase. These read both.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// runSource parses run.go and returns its top-level functions by name.
func runSource(t *testing.T) (*token.FileSet, map[string]*ast.FuncDecl) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "run.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	funcs := map[string]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
			funcs[fn.Name.Name] = fn
		}
	}
	if funcs["Run"] == nil {
		t.Fatal("no func Run in run.go")
	}
	return fset, funcs
}

// calledName is the name a call is made through: f for f(...), Sel for x.Sel(...).
func calledName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

// firstCall is the position of the first call to name in fn's body, or NoPos.
func firstCall(fn *ast.FuncDecl, name string) token.Pos {
	at := token.NoPos
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && at == token.NoPos && calledName(call) == name {
			at = call.Pos()
		}
		return true
	})
	return at
}

// callersOf names every function in funcs whose body calls name.
func callersOf(funcs map[string]*ast.FuncDecl, name string) []string {
	var callers []string
	for fnName, fn := range funcs {
		if firstCall(fn, name) != token.NoPos {
			callers = append(callers, fnName)
		}
	}
	return callers
}

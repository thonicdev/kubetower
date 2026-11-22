package launch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// The native window's address is never written to the log as it is.
//
// The address a native window is handed carries a one-time sign-in
// code in its query (windowURL), and `kubetower.log` is a file a bug report
// receives whole. Both windows name that address `url` and write it through
// loggableURL, which drops the query. A new log line that forgot to would put
// a live code in the log, and nothing at run time would say so - so the rule
// is read out of the source, as TestNoWindowCallbackResolvesAnything does,
// which is also why it runs on every platform.
//
// **What it covers**: every argument of a `log.*` or `fmt.*` call in the two
// native window files that mentions the identifier `url` outside a
// `loggableURL(...)` call. **What it does not**: the address under another
// name, or carried through a variable first - a change that does either should
// be read by hand.
func TestTheNativeWindowNeverLogsItsAddressWhole(t *testing.T) {
	for _, name := range []string{"nativewindow_darwin.go", "nativewindow_windows.go"} {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		calls := 0
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || (pkg.Name != "log" && pkg.Name != "fmt") {
				return true
			}
			calls++
			for _, arg := range call.Args {
				ast.Inspect(arg, func(m ast.Node) bool {
					switch m := m.(type) {
					case *ast.CallExpr:
						if fn, ok := m.Fun.(*ast.Ident); ok && fn.Name == "loggableURL" {
							return false
						}
					case *ast.SelectorExpr:
						// url.Parse and the like: the package, not the address.
						if x, ok := m.X.(*ast.Ident); ok && x.Name == "url" {
							return false
						}
					case *ast.Ident:
						if m.Name == "url" {
							t.Errorf("%s: %s.%s names the window's address without loggableURL; "+
								"it carries a one-time sign-in code",
								fset.Position(m.Pos()), pkg.Name, sel.Sel.Name)
						}
					}
					return true
				})
			}
			return true
		})
		if calls == 0 {
			t.Errorf("%s: no log or fmt call found; the walk is reading the wrong file", name)
		}
	}
}

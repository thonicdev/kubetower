package launch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"testing"
)

// No Objective-C callback in the macOS window resolves a selector, a class or a
// string — it reads `hot`, which was filled before the window opened.
//
// The reason is in `nativewindow_darwin.go` at `hot`: resolving is a Go→C
// transition, a callback is already a C→Go one, and nesting the two on AppKit's
// stack overflows it. A comment stating the rule can sit a few lines above code
// breaking it through a helper (`navigate`, `setPageZoom`), so the rule is read
// out of the source here instead of out of a comment.
//
// It parses the file rather than compiling it, which is why it runs on every
// platform and not only on a Mac: what it checks is the shape of the code, not
// its behavior.
//
// **What it covers**: every `Fn:` literal in a `MethodDef`, every function
// handed to `objc.NewBlock` (a block is called by AppKit exactly as a method
// is — the ⌘Q sheet's answer), and every function of
// the file they reach by name, transitively. **What it does not**: a call
// through a variable or a method value, which nothing here uses — and which a
// change adding one should be made to check by hand.
func TestNoWindowCallbackResolvesAnything(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "nativewindow_darwin.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing the macOS window: %v", err)
	}

	decls := map[string]*ast.FuncDecl{}
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil {
			decls[fn.Name.Name] = fn
		}
	}
	// Plain names are this file's helpers; qualified ones are purego's.
	forbidden := map[string]bool{
		"sel": true, "class": true, "nsString": true,
		"objc.RegisterName": true, "objc.GetClass": true,
	}

	var roots []ast.Node
	blocks := 0
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if fun, ok := call.Fun.(*ast.SelectorExpr); ok && fun.Sel.Name == "NewBlock" && len(call.Args) == 1 {
				switch arg := call.Args[0].(type) {
				case *ast.FuncLit:
					roots, blocks = append(roots, arg.Body), blocks+1
				case *ast.Ident:
					if decl, ok := decls[arg.Name]; ok {
						roots, blocks = append(roots, decl.Body), blocks+1
					}
				}
			}
			return true
		}
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Fn" {
			if lit, ok := kv.Value.(*ast.FuncLit); ok {
				roots = append(roots, lit.Body)
			}
		}
		return true
	})
	// At least one block, the ⌘Q sheet's answer. None found means the walk
	// stopped recognizing how blocks are made, not that there are none.
	if blocks < 1 {
		t.Fatalf("found no function handed to objc.NewBlock in nativewindow_darwin.go: the test no " +
			"longer recognizes how blocks are made")
	}
	// At least twenty-eight callbacks with the block; fewer than that means the
	// walk found the wrong thing rather than that the file got simpler.
	if len(roots) < 28 {
		t.Fatalf("found %d callbacks in nativewindow_darwin.go, want at least 28: the test no longer "+
			"recognizes how they are declared", len(roots))
	}

	visited := map[string]bool{}
	var violations []string
	var walk func(where string, body ast.Node)
	walk = func(where string, body ast.Node) {
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := ""
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				name = fun.Name
			case *ast.SelectorExpr:
				if pkg, ok := fun.X.(*ast.Ident); ok {
					name = pkg.Name + "." + fun.Sel.Name
				}
			}
			if forbidden[name] {
				violations = append(violations, where+" calls "+name+" at "+fset.Position(call.Pos()).String())
				return true
			}
			if decl, ok := decls[name]; ok && !visited[name] {
				visited[name] = true
				walk(name, decl.Body)
			}
			return true
		})
	}
	for _, root := range roots {
		walk("a callback at "+fset.Position(root.Pos()).String(), root)
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Error(v)
	}
}

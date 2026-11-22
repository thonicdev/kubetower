package launch

// The port is bound before any window can open,
// so a port somebody else holds stops the start with a sentence and never has
// a window opened on its page.

import (
	"go/ast"
	"go/token"
	"net"
	"strings"
	"testing"
)

func TestAnAddressSomethingElseHoldsStopsTheStart(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	ln, err := listenFirst(held.Addr().String())
	if err == nil {
		ln.Close()
		t.Fatal("an address another listener holds was bound")
	}
	for _, want := range []string{held.Addr().String(), "no window was opened", "KT_LISTEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not say %q", err, want)
		}
	}

	free, err := listenFirst("127.0.0.1:0")
	if err != nil {
		t.Fatalf("a free address could not be bound: %v", err)
	}
	free.Close()
}

// Run is not started by a test - it would serve on a real port with a real
// kubeconfig - so the order is read from its source: listenFirst is called, and
// before the only openWindow call in Run; Run hands the bound listener to
// serve, which serves on it; and nothing in run.go binds behind a goroutine
// with ListenAndServe. The last two read the phases as well as Run: the
// goroutine that serves lives in serve.
func TestRunBindsThePortBeforeAnyWindow(t *testing.T) {
	fset, funcs := runSource(t)

	for name, fn := range funcs {
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "ListenAndServe" {
					t.Errorf("%s binds through ListenAndServe at %s, which a window can race",
						name, fset.Position(call.Pos()))
				}
			}
			return true
		})
	}

	run := funcs["Run"]
	bind, window := firstCall(run, "listenFirst"), firstCall(run, "openWindow")
	if bind == token.NoPos {
		t.Fatal("Run never calls listenFirst")
	}
	if window == token.NoPos {
		t.Fatal("Run never calls openWindow, so this test reads the wrong function")
	}
	if bind > window {
		t.Errorf("Run opens a window (%s) before it binds the port (%s)", fset.Position(window), fset.Position(bind))
	}

	// What listenFirst bound is what serve is handed, and serve serves on it.
	var bound string
	var served *ast.CallExpr
	ast.Inspect(run.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if call, ok := n.Rhs[0].(*ast.CallExpr); ok && calledName(call) == "listenFirst" {
				if id, ok := n.Lhs[0].(*ast.Ident); ok {
					bound = id.Name
				}
			}
		case *ast.CallExpr:
			if calledName(n) == "serve" && served == nil {
				served = n
			}
		}
		return true
	})
	if served == nil {
		t.Fatal("Run never calls serve, so nothing says what it serves on")
	}
	if id, ok := served.Args[0].(*ast.Ident); !ok || bound == "" || id.Name != bound {
		t.Errorf("Run does not hand serve the listener listenFirst bound (%s)", fset.Position(served.Pos()))
	}
	if served.Pos() < bind {
		t.Errorf("Run serves (%s) before it binds the port (%s)", fset.Position(served.Pos()), fset.Position(bind))
	}

	serveFn := funcs["serve"]
	if serveFn == nil {
		t.Fatal("serve is gone from run.go, so this test reads the wrong function")
	}
	listener := serveFn.Type.Params.List[0].Names[0].Name
	servesOnIt := false
	ast.Inspect(serveFn.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && calledName(call) == "Serve" && len(call.Args) == 1 {
			if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == listener {
				servesOnIt = true
			}
		}
		return true
	})
	if !servesOnIt {
		t.Errorf("serve never calls Serve(%s), so the port it was handed is not the one it serves on", listener)
	}
}

package launch

// What Run undoes on its way out: the forwards' Shutdown, then the archive's
// stop, then the store's Close - last opened, first closed - each one whatever
// the others do, as the separate defers cleanups replaced did.

import (
	"go/ast"
	"go/token"
	"reflect"
	"testing"
)

func TestCleanupsRunLastAddedFirst(t *testing.T) {
	var ran []string
	var undo cleanups
	for _, name := range []string{"close the store", "stop the archive", "shut the forwards down"} {
		undo.add(func() { ran = append(ran, name) })
	}
	undo.run()

	want := []string{"shut the forwards down", "stop the archive", "close the store"}
	if !reflect.DeepEqual(ran, want) {
		t.Errorf("cleanups ran %q, want last added first: %q", ran, want)
	}
}

func TestACleanupThatPanicsDoesNotStopTheOthers(t *testing.T) {
	var ran []string
	var undo cleanups
	undo.add(func() { ran = append(ran, "close the store") })
	undo.add(func() { ran = append(ran, "stop the archive") })
	undo.add(func() { panic("the forwards' Shutdown") })

	func() {
		defer func() {
			if r := recover(); r != "the forwards' Shutdown" {
				t.Errorf("the panic arrived as %v, want the cleanup's own", r)
			}
		}()
		undo.run()
	}()

	want := []string{"stop the archive", "close the store"}
	if !reflect.DeepEqual(ran, want) {
		t.Errorf("after a cleanup panicked, the others ran %q, want %q", ran, want)
	}
}

// Run defers undo.run() before it hands undo to startWorkers, so that what
// startWorkers opens is undone on every way out, an error from a later phase
// included. Read from the source: Run is not started by a test.
func TestRunUndoesWhatItsWorkersOpened(t *testing.T) {
	fset, funcs := runSource(t)
	run := funcs["Run"]

	deferred := token.NoPos
	ast.Inspect(run.Body, func(n ast.Node) bool {
		d, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}
		if sel, ok := d.Call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "run" {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "undo" && deferred == token.NoPos {
				deferred = d.Pos()
			}
		}
		return true
	})
	started := firstCall(run, "startWorkers")
	if started == token.NoPos {
		t.Fatal("Run never calls startWorkers, so this test reads the wrong function")
	}
	if deferred == token.NoPos {
		t.Fatal("Run never defers undo.run(), so nothing startWorkers opened is closed on the way out")
	}
	if deferred > started {
		t.Errorf("Run defers undo.run() (%s) after startWorkers (%s), so a failure inside it leaks what it opened",
			fset.Position(deferred), fset.Position(started))
	}
}

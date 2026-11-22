package columns

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thonicdev/kubetower/internal/statefile"
)

// Widths, order and hidden columns are three arrangements made by three
// gestures, and resetting one must not undo the others: an operator who wants
// the default widths back must not pay their column order for them.
//
// # Read back from the file, and from a second kind
//
// The assertions below open a *fresh* store on the same directory rather than
// reading the one that did the reset. A scope that cleared the map in memory and
// wrote the whole arrangement back would pass every in-memory assertion and lose
// the kind at the next restart. The file is the witness.
//
// And there are two kinds throughout, because "reset the widths" that quietly
// meant "reset every kind's widths" is the whole-console reset arriving by
// accident — exactly what naming the kind on the request is there to prevent.

// arrangedStore writes one kind: a width, a moved column and a hidden one.
func arrangedStore(t *testing.T, dir string) *Store {
	t.Helper()
	store := New(dir, func(string, ...any) {})
	for _, kind := range []string{"pods", "nodes"} {
		titles := podColumns()
		if _, err := store.Resize(kind, []Column{{Title: "Status", Width: 300}}, titles); err != nil {
			t.Fatalf("Resize %s: %v", kind, err)
		}
		if _, err := store.Place(kind, "Age", "Name", false, titles); err != nil {
			t.Fatalf("Place %s: %v", kind, err)
		}
		if _, err := store.SetVisible(kind, "Namespace", false, false, titles); err != nil {
			t.Fatalf("SetVisible %s: %v", kind, err)
		}
	}
	return store
}

// arrangementFacts is the three things a scope can forget, read off a layout.
type arrangementFacts struct {
	statusWidth int
	order       []string
	hidden      bool
}

func factsOf(t *testing.T, store *Store, kind string) arrangementFacts {
	t.Helper()
	layout := store.Arrangement(kind, podColumns())
	return arrangementFacts{
		statusWidth: widthOf(t, layout, "Status"),
		order:       titlesOf(layout),
		hidden:      columnNamed(t, layout, "Namespace").Hidden,
	}
}

func TestEachResetScopeForgetsOneArrangementAndKeepsTheOtherTwo(t *testing.T) {
	// The arrangement every case starts from, asserted once so a case that
	// "passes" because nothing was ever stored cannot.
	//
	// Age is dropped in front of Name, so the order below is the moved one.
	const movedOrder = "Age Name Namespace Status"

	for _, each := range []struct {
		scope Scope
		// What this scope is expected to have forgotten, read back from the file.
		wantWidth  int
		wantOrder  string
		wantHidden bool
	}{
		{ScopeWidths, 0, movedOrder, true},
		{ScopeOrder, 300, "Name Namespace Status Age", true},
		{ScopeHidden, 300, movedOrder, false},
	} {
		t.Run(string(each.scope), func(t *testing.T) {
			dir := t.TempDir()
			store := arrangedStore(t, dir)

			before := factsOf(t, store, "pods")
			if before.statusWidth != 300 || before.hidden != true ||
				!same(before.order, []string{"Age", "Name", "Namespace", "Status"}) {
				t.Fatalf("the fixture stored %+v, so this case would prove nothing", before)
			}

			if _, err := store.Reset("pods", each.scope, podColumns()); err != nil {
				t.Fatalf("Reset %s: %v", each.scope, err)
			}

			// The file, through a store that has never seen the gesture.
			reopened := New(dir, func(string, ...any) {})
			got := factsOf(t, reopened, "pods")

			if got.statusWidth != each.wantWidth {
				t.Errorf("after resetting %s, Status is %dpx on disk, want %dpx",
					each.scope, got.statusWidth, each.wantWidth)
			}
			if drawnOrder := join(got.order); drawnOrder != each.wantOrder {
				t.Errorf("after resetting %s, the order on disk is %q, want %q",
					each.scope, drawnOrder, each.wantOrder)
			}
			if got.hidden != each.wantHidden {
				t.Errorf("after resetting %s, Namespace hidden = %v on disk, want %v",
					each.scope, got.hidden, each.wantHidden)
			}

			// The other kind is untouched, in all three respects. A scope that
			// reached every kind would be a whole-console reset arriving without
			// anybody asking for one.
			other := factsOf(t, reopened, "nodes")
			if other.statusWidth != 300 || !same(other.order, before.order) || !other.hidden {
				t.Errorf("resetting %s on pods changed nodes as well: %+v", each.scope, other)
			}
		})
	}
}

// The default is still everything, because that is what the one-word menu item
// has always done and what an absent scope on the request means.
func TestResetAllStillForgetsTheWholeKind(t *testing.T) {
	dir := t.TempDir()
	store := arrangedStore(t, dir)

	layout, err := store.Reset("pods", ScopeAll, podColumns())
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if layout.Custom {
		t.Error("the kind still reports an arrangement of the operator's own")
	}

	reopened := New(dir, func(string, ...any) {})
	got := factsOf(t, reopened, "pods")
	if got.statusWidth != 0 || got.hidden || !same(got.order, podColumns()) {
		t.Errorf("something survived the whole-kind reset on disk: %+v", got)
	}
	if kinds := reopened.Kinds(); len(kinds) != 1 || kinds[0] != "nodes" {
		t.Errorf("the file holds %v, want nodes alone", kinds)
	}
}

// A scope nobody serves forgets nothing, and says so.
//
// **The empty string is in this list deliberately.** It is the zero value of the
// field on the wire, so it is what a misspelt, dropped or half-written scope
// arrives as — and the safe reading of "I could not tell what you meant" cannot
// be "forget everything", which is the most destructive of the four and the one
// scoped resets exist to avoid. The default lives at the boundary that
// knows the contract (internal/api/handlers_prefs.go), once.
func TestAnUnknownScopeForgetsNothing(t *testing.T) {
	for _, scope := range []Scope{"", "widths ", "Widths", "everything", "all!"} {
		t.Run(string(scope), func(t *testing.T) {
			dir := t.TempDir()
			store := arrangedStore(t, dir)

			if _, err := store.Reset("pods", scope, podColumns()); err == nil {
				t.Fatalf("Reset(%q) was accepted", string(scope))
			}

			got := factsOf(t, New(dir, func(string, ...any) {}), "pods")
			if got.statusWidth != 300 || !got.hidden ||
				!same(got.order, []string{"Age", "Name", "Namespace", "Status"}) {
				t.Errorf("a refused scope changed the file: %+v", got)
			}
		})
	}
}

// A scope with nothing to forget is not an error and not a write.
//
// Measured over a directory that cannot be written: had the reset saved anyway,
// this would come back wrapping statefile.ErrNotSaved. It is the same arm a kind
// that was never arranged has always taken, and it keeps the one log line a
// standing write failure is worth attached to a gesture that had something to do.
func TestAScopeWithNothingToForgetWritesNothing(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("%v", err)
	}
	store := New(filepath.Join(blocked, "state"), func(string, ...any) {})
	titles := podColumns()

	// The control: this one has something to forget, so it does try to write and
	// does report that it could not.
	if _, err := store.SetVisible("pods", "Namespace", false, false, titles); !errors.Is(err, statefile.ErrNotSaved) {
		t.Fatalf("SetVisible returned %v", err)
	}
	if _, err := store.Reset("pods", ScopeHidden, titles); !errors.Is(err, statefile.ErrNotSaved) {
		t.Fatalf("resetting the scope that had something in it returned %v, want ErrNotSaved", err)
	}

	// And these two hold nothing, so nothing is written and nothing is reported.
	for _, scope := range []Scope{ScopeWidths, ScopeOrder} {
		if _, err := store.Reset("pods", scope, titles); err != nil {
			t.Errorf("resetting %s, which held nothing, tried to write: %v", scope, err)
		}
	}
	if _, err := store.Reset("services", ScopeAll, titles); err != nil {
		t.Errorf("resetting a kind that was never arranged tried to write: %v", err)
	}
}

// join is the order as one string, so a failure prints the table rather than a
// Go slice literal.
func join(titles []string) string {
	out := ""
	for i, title := range titles {
		if i > 0 {
			out += " "
		}
		out += title
	}
	return out
}

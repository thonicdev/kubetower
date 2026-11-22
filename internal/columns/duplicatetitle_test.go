package columns

import (
	"strings"
	"testing"
)

// A CRD may serve two printer columns of the same name, and the arrangement must
// survive a restart.
//
// Nothing makes `additionalPrinterColumns` names unique, so `Status` twice with
// two different jsonPaths is a table a cluster can genuinely serve. The stored
// order is a list of titles; a load path that dedupes it reads two stored
// `Status` back as one, and `reconcile` then has nothing to place the second
// against so it drops to the end. **On every restart, with no operator action
// at all.**
//
// `Place` pairing `statefile.Remove` — which strips *every* occurrence — with
// `Insert` would produce no wrong table on its own: `reconcile` is
// duplicate-aware (`byTitle` holds every index for a title and `next` hands
// them out in order), so it re-places the column before anything is stored.
// Deduping on load alone reproduces the jump. The assertions are on positions,
// not on the column *count*, which stays right under both mistakes.
func TestTwoColumnsOfOneTitleKeepTheirPlacesAcrossARestart(t *testing.T) {
	dir := t.TempDir()
	titles := []string{"Name", "Status", "Replicas", "Status", "Age"}

	first := New(dir, nil)
	arranged, err := first.Place("widgets.example.io", "Replicas", "Name", true, titles)
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	const want = "Name, Replicas, Status, Status, Age"
	if got := drawn(arranged); got != want {
		t.Fatalf("the drag itself produced %q, want %q", got, want)
	}

	// A second store reading the file the first wrote — a restart.
	reloaded := drawn(New(dir, nil).Arrangement("widgets.example.io", titles))
	if reloaded != want {
		t.Errorf("after a restart the table reads %q, want %q.\n"+
			"The stored order was collapsed on the way in, so the second column of that "+
			"title has nothing to be placed against and drops to the end — on every "+
			"process this console runs, with nobody having touched anything.",
			reloaded, want)
	}
}

// And a table with no duplicates is unaffected, so keeping repeats is not a license for
// the stored order to grow.
func TestAnOrdinaryTableStillStoresEachTitleOnce(t *testing.T) {
	dir := t.TempDir()
	titles := []string{"Name", "Namespace", "Status", "Age"}

	s := New(dir, nil)
	for i := 0; i < 3; i++ {
		if _, err := s.Place("pods", "Status", "Name", true, titles); err != nil {
			t.Fatalf("Place: %v", err)
		}
	}

	const want = "Name, Status, Namespace, Age"
	if got := drawn(New(dir, nil).Arrangement("pods", titles)); got != want {
		t.Errorf("after three drags and a restart the table reads %q, want %q — repeats "+
			"are being accumulated rather than kept", got, want)
	}
}

func drawn(layout Layout) string {
	out := make([]string, 0, len(layout.Columns))
	for _, c := range layout.Columns {
		out = append(out, c.Title)
	}
	return strings.Join(out, ", ")
}

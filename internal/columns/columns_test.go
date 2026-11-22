package columns

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/statefile"
)

// titlesOf is what a test asserts against: the order a table would be drawn in.
func titlesOf(layout Layout) []string {
	out := make([]string, 0, len(layout.Columns))
	for _, c := range layout.Columns {
		out = append(out, c.Title)
	}
	return out
}

func same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func widthOf(t *testing.T, layout Layout, title string) int {
	t.Helper()
	for _, c := range layout.Columns {
		if c.Title == title {
			return c.Width
		}
	}
	t.Fatalf("no %q column in %v", title, titlesOf(layout))
	return 0
}

// The failure mode that will actually happen, and the reason this package
// reconciles on every read rather than at load time.
//
// A layout is written against the columns a kind had on the day it was arranged.
// Both halves of what happens next are ordinary — `internal/resources` gains a
// column in a release, and a CRD's printer columns change under a chart upgrade
// — and both are silent: a ghost heading draws a column of nothing over a table
// whose cells have all shifted one across, and a column missing from the stored
// order is a feature that shipped and that nobody can see.
func TestAStoredLayoutSurvivesTheColumnsChangingUnderIt(t *testing.T) {
	store := New(t.TempDir(), t.Logf)

	// Arranged when the table had these four.
	if _, err := store.Place("pods", "Restarts", "Name", true, []string{"Name", "Namespace", "Restarts", "Age"}); err != nil {
		t.Fatalf("Place: %v", err)
	}
	if _, err := store.Resize("pods", []Column{{Title: "Namespace", Width: 200}}, []string{"Name", "Namespace", "Restarts", "Age"}); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	// A release later: Restarts is gone and Node has appeared.
	now := []string{"Name", "Namespace", "Age", "Node"}
	layout := store.Arrangement("pods", now)

	got := titlesOf(layout)
	for _, ghost := range got {
		if ghost == "Restarts" {
			t.Errorf("the layout still draws Restarts: %v\n"+
				"A heading with no cells under it shifts every value one column across "+
				"and still renders perfectly, which is the worst way for this to fail.", got)
		}
	}
	if want := []string{"Name", "Namespace", "Age", "Node"}; !same(got, want) {
		t.Errorf("order = %v, want %v — Node is new, so it goes at the end; nothing else moved", got, want)
	}
	if len(got) != len(now) {
		t.Fatalf("the layout is not a permutation of the table: %v against %v", got, now)
	}
	// The arrangement of what is still there is kept, which is the whole point
	// of not simply throwing the entry away when one column goes.
	if w := widthOf(t, layout, "Namespace"); w != 200 {
		t.Errorf("Namespace kept width %d, want 200", w)
	}

	// And every column carries where it was, so a caller can permute its rows by
	// it. Off-by-one here would draw one column's values under another's heading.
	for i, c := range layout.Columns {
		if c.At < 0 || c.At >= len(now) || now[c.At] != c.Title {
			t.Errorf("column %d (%s) points at index %d, which is %v", i, c.Title, c.At, now)
		}
	}
}

// The other half of the same problem, one release further on: a stored order
// that names nothing the table has any more. It must degrade to the default
// table rather than to an empty one.
func TestAnEntirelyStaleOrderLeavesTheDefaultTable(t *testing.T) {
	store := New(t.TempDir(), t.Logf)
	if _, err := store.Place("certificates.cert-manager.io", "Status", "Ready", true,
		[]string{"Ready", "Status", "Secret"}); err != nil {
		t.Fatalf("Place: %v", err)
	}

	now := []string{"Name", "Issuer", "Age"}
	got := titlesOf(store.Arrangement("certificates.cert-manager.io", now))
	if !same(got, now) {
		t.Errorf("order = %v, want the table's own order %v", got, now)
	}
}

// A width comes back from a store, and a store is not truth: the file sits in a
// directory the operator can open, and this process is not the only thing that
// has ever written a JSON file by hand.
func TestAWidthFromTheFileIsClampedLikeAnyOtherInput(t *testing.T) {
	dir := t.TempDir()
	// `widthsAreDeliberate` set, because otherwise the one-time migration drops
	// the widths before the clamp ever sees them and this test passes for the
	// wrong reason.
	raw := `{"widthsAreDeliberate":true,"kinds":{"pods":{"order":["Name","Age"],"widths":{"Name":4,"Age":100000}}}}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(raw), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	layout := New(dir, t.Logf).Arrangement("pods", []string{"Name", "Age"})
	if w := widthOf(t, layout, "Name"); w != MinWidth {
		t.Errorf("Name is %dpx wide, want %d — a column nobody can grab is a column nobody gets back", w, MinWidth)
	}
	if w := widthOf(t, layout, "Age"); w != MaxWidth {
		t.Errorf("Age is %dpx wide, want %d", w, MaxWidth)
	}
}

// The same clamp on the way in, because that is where a drag arrives.
func TestADragIsClampedBeforeItIsStored(t *testing.T) {
	store := New(t.TempDir(), t.Logf)
	titles := []string{"Name", "Age"}

	layout, err := store.Resize("pods", []Column{{Title: "Name", Width: 1}, {Title: "Age", Width: 5000}}, titles)
	if err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if w := widthOf(t, layout, "Name"); w != MinWidth {
		t.Errorf("Name = %d, want %d", w, MinWidth)
	}
	if w := widthOf(t, layout, "Age"); w != MaxWidth {
		t.Errorf("Age = %d, want %d", w, MaxWidth)
	}
}

func TestAColumnIsMovedBesideAnotherAndNotToAnIndex(t *testing.T) {
	store := New(t.TempDir(), t.Logf)
	titles := []string{"Name", "Namespace", "Status", "Age"}

	layout, err := store.Place("pods", "Age", "Name", false, titles)
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if want := []string{"Age", "Name", "Namespace", "Status"}; !same(titlesOf(layout), want) {
		t.Fatalf("order = %v, want %v", titlesOf(layout), want)
	}

	// And back, to the other side of Status.
	layout, err = store.Place("pods", "Age", "Status", true, titles)
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if want := []string{"Name", "Namespace", "Status", "Age"}; !same(titlesOf(layout), want) {
		t.Errorf("order = %v, want %v", titlesOf(layout), want)
	}
}

// Dropping a heading on itself is what a drag that changed its mind produces.
// The answer to it is the table redrawn, not an error.
func TestDroppingAColumnOnItselfChangesNothing(t *testing.T) {
	store := New(t.TempDir(), t.Logf)
	titles := []string{"Name", "Age"}

	layout, err := store.Place("pods", "Name", "Name", true, titles)
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if !same(titlesOf(layout), titles) {
		t.Errorf("order = %v, want %v", titlesOf(layout), titles)
	}
}

func TestAColumnThisTableDoesNotHaveIsRefused(t *testing.T) {
	store := New(t.TempDir(), t.Logf)
	titles := []string{"Name", "Age"}

	if _, err := store.Place("pods", "Ready", "Name", true, titles); err == nil {
		t.Error("a column that is not in the table was accepted")
	}
	if _, err := store.Place("pods", "Name", "Ready", true, titles); err == nil {
		t.Error("a target that is not in the table was accepted")
	}
	if _, err := store.Resize("pods", []Column{{Title: "Ready", Width: 100}}, titles); err == nil {
		t.Error("a resize of a column that is not in the table was accepted")
	}
	if store.Arrangement("pods", titles).Custom {
		t.Error("a refused request still left something stored")
	}
}

// Two kinds, two arrangements. A width dragged on pods must not follow the
// operator to nodes — which is the whole reason this is keyed per kind and the
// first thing anybody would notice if it were not.
func TestOneKindsArrangementDoesNotReachAnother(t *testing.T) {
	store := New(t.TempDir(), t.Logf)
	titles := []string{"Name", "Age"}

	if _, err := store.Resize("pods", []Column{{Title: "Name", Width: 300}}, titles); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if _, err := store.Place("pods", "Age", "Name", false, titles); err != nil {
		t.Fatalf("Place: %v", err)
	}

	nodes := store.Arrangement("nodes", titles)
	if nodes.Custom {
		t.Error("nodes inherited an arrangement that was made on pods")
	}
	if !same(titlesOf(nodes), titles) {
		t.Errorf("nodes order = %v, want the default %v", titlesOf(nodes), titles)
	}
	if w := widthOf(t, nodes, "Name"); w != 0 {
		t.Errorf("nodes Name is %dpx wide; the width was set on pods", w)
	}
}

// The way back. A column dragged to its minimum is hard to find and harder to
// grab; without this the operator has taken a table away from themselves.
func TestResetPutsAKindBackToTheDefault(t *testing.T) {
	store := New(t.TempDir(), t.Logf)
	titles := []string{"Name", "Namespace", "Age"}

	if _, err := store.Resize("pods", []Column{{Title: "Name", Width: MinWidth}}, titles); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if _, err := store.Place("pods", "Age", "Name", false, titles); err != nil {
		t.Fatalf("Place: %v", err)
	}
	if !store.Arrangement("pods", titles).Custom {
		t.Fatal("nothing was stored, so this test proves nothing")
	}

	layout, err := store.Reset("pods", ScopeAll, titles)
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if layout.Custom {
		t.Error("the kind still reports an arrangement of the operator's own")
	}
	if !same(titlesOf(layout), titles) {
		t.Errorf("order = %v, want the default %v", titlesOf(layout), titles)
	}
	if layout.Sized() {
		t.Error("a width survived the reset")
	}
	if got := store.Kinds(); len(got) != 0 {
		t.Errorf("the file still holds %v", got)
	}
}

// A CRD may declare two printer columns with the same name. Matching by title
// alone would emit the first of them twice and drop the second, which draws a
// table where one column's values are quietly another's.
func TestTwoColumnsWithTheSameTitleStayTwoColumns(t *testing.T) {
	store := New(t.TempDir(), t.Logf)
	titles := []string{"Name", "Status", "Status", "Age"}

	if _, err := store.Place("widgets.example.com", "Age", "Name", true, titles); err != nil {
		t.Fatalf("Place: %v", err)
	}
	layout := store.Arrangement("widgets.example.com", titles)

	if len(layout.Columns) != len(titles) {
		t.Fatalf("%d columns out of %d in: %v", len(layout.Columns), len(titles), titlesOf(layout))
	}
	seen := map[int]bool{}
	for _, c := range layout.Columns {
		if seen[c.At] {
			t.Fatalf("index %d is drawn twice: %v", c.At, titlesOf(layout))
		}
		seen[c.At] = true
	}
}

// It has to survive a restart, or none of the above is a preference — it is a
// thing that happens until the next `docker compose up --build`.
func TestTheArrangementIsOnDiskAndComesBack(t *testing.T) {
	dir := t.TempDir()
	titles := []string{"Name", "Namespace", "Age"}

	store := New(dir, t.Logf)
	if _, err := store.Place("pods", "Age", "Name", false, titles); err != nil {
		t.Fatalf("Place: %v", err)
	}
	if _, err := store.Resize("pods", []Column{{Title: "Name", Width: 240}}, titles); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("reading the file back: %v", err)
	}
	var file persisted
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("the file is not JSON: %v\n%s", err, raw)
	}

	again := New(dir, t.Logf).Arrangement("pods", titles)
	if want := []string{"Age", "Name", "Namespace"}; !same(titlesOf(again), want) {
		t.Errorf("after a restart the order is %v, want %v", titlesOf(again), want)
	}
	if w := widthOf(t, again, "Name"); w != 240 {
		t.Errorf("after a restart Name is %dpx, want 240", w)
	}
}

// Nowhere to write is a supported state — a test, and a container whose state
// volume is missing. It must not be a reason to refuse to draw a table.
func TestAStoreWithNoFileStillArranges(t *testing.T) {
	store := New("", nil)
	titles := []string{"Name", "Age"}

	if _, err := store.Place("pods", "Age", "Name", false, titles); err != nil {
		t.Fatalf("Place: %v", err)
	}
	if want := []string{"Age", "Name"}; !same(titlesOf(store.Arrangement("pods", titles)), want) {
		t.Errorf("order = %v, want %v", titlesOf(store.Arrangement("pods", titles)), want)
	}
	if store.Path() != "" {
		t.Errorf("Path = %q, want empty", store.Path())
	}
}

// A file that is not JSON is a start from the default tables and one line in the
// log, not a console that will not start.
func TestAnUnreadableFileIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	var said []string
	store := New(dir, func(format string, args ...any) { said = append(said, format) })
	if got := titlesOf(store.Arrangement("pods", []string{"Name", "Age"})); !same(got, []string{"Name", "Age"}) {
		t.Errorf("order = %v, want the default", got)
	}
	if len(said) == 0 {
		t.Error("nothing was logged, so a lost arrangement would be diagnosed by guesswork")
	}
	for _, line := range said {
		if !strings.Contains(line, "columns") {
			t.Errorf("the log line does not name the subsystem: %q", line)
		}
	}
}

// The row height outlives the process, and an older file reads as the default.
func TestTheRowHeightIsRememberedAndDefaultsToRoomy(t *testing.T) {
	dir := t.TempDir()
	store := New(dir, func(string, ...any) {})

	if got := store.Density(); got != "" {
		t.Errorf("a fresh store reports %q; the default is the table as it has always been drawn", got)
	}

	got, err := store.SetDensity(Compact)
	if err != nil {
		t.Fatalf("SetDensity: %v", err)
	}
	if got != Compact {
		t.Fatalf("SetDensity returned %q", got)
	}

	// A second store over the same directory is the next start of the process.
	if got := New(dir, func(string, ...any) {}).Density(); got != Compact {
		t.Errorf("the choice did not survive a restart: %q. It is written to the same file as "+
			"the column arrangement, which is the only other thing this package keeps", got)
	}

	// Anything else is the default rather than a refusal: the value arrives from
	// a form, and a table drawn at the wrong height beats a page that will not
	// render because a string was unexpected.
	if got, err := store.SetDensity("enormous"); err != nil || got != "" {
		t.Errorf("an unknown density was stored as %q (err %v), want the default", got, err)
	}
	if got := New(dir, func(string, ...any) {}).Density(); got != "" {
		t.Errorf("the unknown density reached the file: %q", got)
	}
}

const podTitles = "Name Namespace Status Age"

// podColumns is the shape a pod table has: a name first, then the rest.
func podColumns() []string { return strings.Fields(podTitles) }

// Hiding a column keeps it in the layout, marked. The caller needs to know it
// exists in order to offer it back, and the store keeps its width and its place
// for when it returns.
//
// Note what this does *not* cover: it resizes Status before hiding it, so the
// stored arrangement has a width in it as well as a hidden title. The case of
// hidden and nothing else is the two tests below.
func TestAHiddenColumnIsRememberedRatherThanForgotten(t *testing.T) {
	dir := t.TempDir()
	store := New(dir, func(string, ...any) {})
	titles := podColumns()

	if _, err := store.Resize("pods", []Column{{Title: "Status", Width: 140}}, titles); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	layout, err := store.SetVisible("pods", "Status", false, false, titles)
	if err != nil {
		t.Fatalf("SetVisible: %v", err)
	}

	var found *Column
	for i := range layout.Columns {
		if layout.Columns[i].Title == "Status" {
			found = &layout.Columns[i]
		}
	}
	if found == nil {
		t.Fatal("a hidden column vanished from the layout; nothing can offer it back")
	}
	if !found.Hidden {
		t.Error("the column is in the layout and not marked hidden")
	}
	if found.Width != 140 {
		t.Errorf("its width is %d; hiding a column must not throw away what it was set to, or "+
			"putting it back is a second piece of work", found.Width)
	}

	// And it survives a restart, like the rest of the arrangement.
	again := New(dir, func(string, ...any) {}).Arrangement("pods", titles)
	for _, c := range again.Columns {
		if c.Title == "Status" && !c.Hidden {
			t.Error("the column came back visible after a restart")
		}
	}
}

// The name column cannot be hidden. It carries the link to the object, the
// checkbox that selects it and the way back from anything done here.
func TestTheColumnThatSaysWhichObjectItIsCannotBeHidden(t *testing.T) {
	store := New(t.TempDir(), func(string, ...any) {})
	titles := podColumns()

	_, err := store.SetVisible("pods", "Name", false, false, titles)
	if err == nil {
		t.Fatal("the name column was hidden. The table would render perfectly and be useless: " +
			"no row would say which object it is, and the menu to put it back would be over a " +
			"table with nothing identifiable in it")
	}
	if !strings.Contains(err.Error(), "Name") {
		t.Errorf("the refusal does not name the column: %q", err)
	}

	// Dragging another column to the left does not make it the name.
	if _, err := store.Place("pods", "Age", "Name", false, titles); err != nil {
		t.Fatalf("Place: %v", err)
	}
	if _, err := store.SetVisible("pods", "Name", false, false, titles); err == nil {
		t.Error("moving a column made the name hideable; the descriptor decides which column " +
			"identifies a row, not where the operator dragged it")
	}
	if _, err := store.SetVisible("pods", "Age", false, false, titles); err != nil {
		t.Errorf("a column dragged to the front became unhideable: %v", err)
	}
}

// The last column standing cannot be hidden either.
func TestTheLastVisibleColumnCannotBeHidden(t *testing.T) {
	store := New(t.TempDir(), func(string, ...any) {})
	titles := podColumns()

	for _, title := range []string{"Namespace", "Status"} {
		if _, err := store.SetVisible("pods", title, false, false, titles); err != nil {
			t.Fatalf("hiding %s: %v", title, err)
		}
	}
	// Name and Age remain, and Age may still go: a table of nothing but the
	// object's name is thin but honest — you can still see what exists and open
	// it. That is the floor, and it is reached rather than refused.
	if _, err := store.SetVisible("pods", "Age", false, false, titles); err != nil {
		t.Fatalf("hiding down to the name alone was refused: %v", err)
	}

	// From there nothing more can go, by either guard.
	if _, err := store.SetVisible("pods", "Name", false, false, titles); err == nil {
		t.Error("the last column was hidden; the panel would be a heading over nothing, and " +
			"the menu to undo it would be over a table with no rows to hang off")
	}
}

// Showing it again leaves the file as it was found, so a kind put back reads as
// untouched and loses its reset button.
func TestPuttingAColumnBackLeavesNothingBehind(t *testing.T) {
	store := New(t.TempDir(), func(string, ...any) {})
	titles := podColumns()

	if _, err := store.SetVisible("pods", "Age", false, false, titles); err != nil {
		t.Fatalf("hide: %v", err)
	}
	if !store.Arrangement("pods", titles).Custom {
		t.Error("hiding a column did not mark the kind as arranged, so no reset button is drawn " +
			"and the operator has no way back")
	}

	if _, err := store.SetVisible("pods", "Age", true, false, titles); err != nil {
		t.Fatalf("show: %v", err)
	}
	if store.Arrangement("pods", titles).Custom {
		t.Error("a kind whose only change has been undone still reports itself arranged")
	}
}

// And Reset brings everything back, not only the widths and the order.
func TestResetShowsHiddenColumnsAgain(t *testing.T) {
	store := New(t.TempDir(), func(string, ...any) {})
	titles := podColumns()

	if _, err := store.SetVisible("pods", "Status", false, false, titles); err != nil {
		t.Fatalf("hide: %v", err)
	}
	reset, err := store.Reset("pods", ScopeAll, titles)
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	for _, c := range reset.Columns {
		if c.Hidden {
			t.Errorf("%s is still hidden after a reset; the button says it puts the table back "+
				"and it left a column off it", c.Title)
		}
	}
}

// Hiding a column without ever resizing or reordering it is the whole of what
// is stored about that kind, and load() must keep it: a "this entry says
// nothing" test of `len(order) == 0 && len(widths) == 0` on the read path alone
// would drop it, and the column would come back visible at every restart.
//
// The second symptom is the one that makes it hard to report: the kind is absent
// from the map, so Layout.Custom is false and the *reset control is not offered
// either* — there is no way back and no sign anything was ever stored.
func TestAColumnHiddenAndNothingElseSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	titles := podColumns()

	// Hidden, and deliberately nothing else: no Resize, no Place.
	if _, err := New(dir, t.Logf).SetVisible("pods", "Status", false, false, titles); err != nil {
		t.Fatalf("SetVisible: %v", err)
	}

	again := New(dir, t.Logf).Arrangement("pods", titles)
	status := columnNamed(t, again, "Status")
	if !status.Hidden {
		t.Error("the column came back visible after a restart. Hiding it was the only thing " +
			"stored about this kind, and the read path dropped any kind whose order and " +
			"widths were both empty")
	}
	if !status.Decided {
		t.Error("the column came back with no decision recorded, so the descriptor's own " +
			"default would win over what the operator asked for")
	}
	if !again.Custom {
		t.Error("the kind reports itself unarranged, so no reset control is drawn: the column " +
			"is gone from the table and there is nothing on the page that puts it back")
	}
}

// The same state arriving as a file, which is how it reaches a running process
// that did not write it — a state volume from an older build, or a hand edit.
func TestAFileThatOnlyHidesAColumnIsRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName),
		[]byte(`{"kinds":{"pods":{"hidden":["Status"]}}}`), 0o600); err != nil {
		t.Fatalf("%v", err)
	}
	titles := podColumns()

	layout := New(dir, t.Logf).Arrangement("pods", titles)
	if !columnNamed(t, layout, "Status").Hidden {
		t.Errorf("the file hides Status and the table draws it: %v", titlesOf(layout))
	}
	if !layout.Custom {
		t.Error("a file that says something about this kind was read as saying nothing")
	}

	// And the same file with an empty entry in it still says nothing, or the
	// reset control would appear on a table nobody has touched.
	blank := t.TempDir()
	if err := os.WriteFile(filepath.Join(blank, FileName),
		[]byte(`{"kinds":{"pods":{}}}`), 0o600); err != nil {
		t.Fatalf("%v", err)
	}
	if New(blank, t.Logf).Arrangement("pods", titles).Custom {
		t.Error("an entry with nothing in it was read as an arrangement")
	}
}

// columnNamed is the one column with this title, or a failed test.
func columnNamed(t *testing.T, layout Layout, title string) Column {
	t.Helper()
	for _, c := range layout.Columns {
		if c.Title == title {
			return c
		}
	}
	t.Fatalf("%s is not in the layout at all: %v", title, titlesOf(layout))
	return Column{}
}

// A state directory that cannot be written must not stop a table being drawn —
// and must not pretend the arrangement was saved.
//
// Every one of the ways this store changes something returns the error: on a
// full disk a column dragged to 300px must not stay at 300px until the next
// restart and then silently not, with the only trace a log line in a container
// nobody is tailing.
func TestAnUnwritableDirectoryIsReportedByEveryChange(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("%v", err)
	}
	var lines int
	store := New(filepath.Join(blocked, "state"), func(string, ...any) { lines++ })
	lines = 0
	titles := podColumns()

	layout, err := store.Resize("pods", []Column{{Title: "Status", Width: 300}}, titles)
	if !errors.Is(err, statefile.ErrNotSaved) {
		t.Fatalf("Resize over an unwritable directory returned %v", err)
	}
	if widthOf(t, layout, "Status") != 300 {
		t.Error("the layout returned with the error does not carry the change; a caller " +
			"drawing it would undo the operator's own drag in front of them")
	}

	if _, err := store.Place("pods", "Age", "Name", false, titles); !errors.Is(err, statefile.ErrNotSaved) {
		t.Errorf("Place returned %v", err)
	}
	if _, err := store.SetVisible("pods", "Status", false, false, titles); !errors.Is(err, statefile.ErrNotSaved) {
		t.Errorf("SetVisible returned %v", err)
	}
	if _, err := store.Reset("pods", ScopeAll, titles); !errors.Is(err, statefile.ErrNotSaved) {
		t.Errorf("Reset returned %v; the one gesture whose whole purpose is undoing a change "+
			"reported success without undoing it on disk: %v", err, err)
	}
	if _, err := store.SetDensity(Compact); !errors.Is(err, statefile.ErrNotSaved) {
		t.Errorf("SetDensity returned %v", err)
	}

	// Still one line for the whole standing condition, not one per drag.
	if lines != 1 {
		t.Errorf("%d log lines for one broken directory, want exactly 1", lines)
	}
}

// A refusal and a failed write are different answers, and a caller that draws
// them differently has to be able to tell them apart. A refusal changed nothing;
// a failed write changed everything except the file.
func TestARefusalDoesNotLookLikeAFailedWrite(t *testing.T) {
	store := New(t.TempDir(), t.Logf)
	titles := podColumns()

	_, err := store.SetVisible("pods", "Name", false, false, titles)
	if err == nil {
		t.Fatal("the name column was hidden")
	}
	if errors.Is(err, statefile.ErrNotSaved) {
		t.Error("a refusal claims the change is in effect and merely unsaved. It is not in " +
			"effect at all, and a handler reading this would tell the operator their column " +
			"is hidden until the next restart")
	}
}

// An interrupted save leaves a `columns.json.NNNN` behind, and nothing else in
// this project would ever remove it.
func TestAStrandedTemporaryFileIsSweptUp(t *testing.T) {
	dir := t.TempDir()
	stranded := filepath.Join(dir, FileName+".9999999")
	if err := os.WriteFile(stranded, []byte("half a file"), 0o600); err != nil {
		t.Fatalf("%v", err)
	}

	if _, err := New(dir, t.Logf).SetVisible("pods", "Status", false, false, podColumns()); err != nil {
		t.Fatalf("SetVisible: %v", err)
	}
	if _, err := os.Stat(stranded); !os.IsNotExist(err) {
		t.Errorf("%s survived a save", stranded)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
		t.Errorf("the sweep took the arrangement with it: %v", err)
	}
}

// The stored widths are dropped once, and only once, and nothing else goes with
// them.
//
// Those numbers are not choices: the build that wrote a file without the flag
// posted every column's width on one drag, so a single resize froze the whole
// table at whatever the browser's layout algorithm had it at — RESTARTS 118px
// over a column of `883` while STATUS clipped `CrashLoopBackOff` beside it.
//
// Three things this has to get right, and each is a way of doing real damage to
// somebody's arrangement:
func TestTheStoredWidthsAreDroppedOnceAndTheOrderIsKept(t *testing.T) {
	dir := t.TempDir()
	// A file as an older build wrote it: no flag, widths for everything, an order
	// that was dragged and a column that was hidden.
	raw := `{"kinds":{"pods":{"order":["Age","Name"],"hidden":["Node"],` +
		`"widths":{"Name":349,"Age":118}}}}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(raw), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	// One: the widths go.
	store := New(dir, t.Logf)
	layout := store.Arrangement("pods", []string{"Name", "Age", "Node"})
	for _, title := range []string{"Name", "Age"} {
		if w := widthOf(t, layout, title); w != 0 {
			t.Errorf("%s came back at %dpx. The stored widths are a side effect of a drag "+
				"posting all of them, and keeping them means the table the operator "+
				"complained about is unchanged", title, w)
		}
	}

	// Two: the order and the hidden column stay. They were always one deliberate
	// act each, and taking them away would be this migration doing more damage
	// than the defect it repairs.
	var drawn []string
	for _, c := range layout.Columns {
		if !c.Hidden {
			drawn = append(drawn, c.Title)
		}
	}
	if want := "Age,Name"; strings.Join(drawn, ",") != want {
		t.Errorf("the table is drawn as %v, want [%s] — the migration took the operator's own "+
			"order with it", drawn, want)
	}
	for _, c := range layout.Columns {
		if c.Title == "Node" && !c.Hidden {
			t.Error("the hidden column came back. One tick hid it, and a widths migration has " +
				"no business undoing that")
		}
	}

	// Three: once. A width set after the migration has to survive a restart, or
	// the operator can never arrange a table again — a far worse bug than the
	// side-effect widths the migration drops.
	if _, err := store.Resize("pods", []Column{{Title: "Name", Width: 240}},
		[]string{"Name", "Age", "Node"}); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	again := New(dir, t.Logf).Arrangement("pods", []string{"Name", "Age", "Node"})
	if w := widthOf(t, again, "Name"); w != 240 {
		t.Errorf("Name is %dpx after a restart, want 240. The migration is running every start, "+
			"so no column can ever be resized", w)
	}
	// And the one nobody touched is still free to size itself to its contents,
	// which is the whole point of the change.
	if w := widthOf(t, again, "Age"); w != 0 {
		t.Errorf("Age came back at %dpx from a drag on Name: one resize is freezing the table "+
			"again", w)
	}
}

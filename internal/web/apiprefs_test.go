package web

import (
	"errors"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
)

// The half of the preference writes that `internal/api` cannot see.
//
// `internal/api` owns the shape of a request and is tested against the contract
// with a fake store, which is the right split: it knows nothing about hotbars or
// column arrangements and must not learn. What that leaves untested is this
// file — the translation from the contract's vocabulary into the store calls.
// `SetRailBar` (which selects which of several hotbars is drawn) and
// `SetRailPlace` must be store calls and not refusals, and a refusal would be
// invisible from `internal/api`: its fake returns nil for everything. So these
// read the arrangement back afterwards rather than checking an error — the store
// having been *told* is not the same as the rail having moved.

func prefsUnder(t *testing.T) (apiPrefs, *Server) {
	t.Helper()
	server, _, _ := railServerOn(t)
	return apiPrefs{server}, server
}

// barOf is which hotbar a context sits on, 1-based, or 0 for none.
func barOf(t *testing.T, server *Server, context string) int {
	t.Helper()
	return server.bars.Arrangement(server.clusters.Contexts()).BarOf(context)
}

// tilesOn is one hotbar's contexts, in the order they are drawn.
//
// Copied, because Arrangement hands back the store's own slices and the store
// edits them in place — an expectation captured before a move would otherwise be
// the same backing array afterwards, and every assertion would pass.
func tilesOn(t *testing.T, server *Server, bar int) []string {
	t.Helper()
	layout := server.bars.Arrangement(server.clusters.Contexts())
	return append([]string(nil), layout.Bars[bar-1]...)
}

// TestSetRailBarPutsAHotbarOnScreen pins the correction: the route selects which
// hotbar is drawn, and the choice is remembered so a reload does not send the
// operator back to hotbar 1.
func TestSetRailBarPutsAHotbarOnScreen(t *testing.T) {
	prefs, server := prefsUnder(t)

	if err := prefs.SetRailBar(2); err != nil {
		t.Fatalf("SetRailBar(2): %v", err)
	}
	if got := server.bars.Arrangement(server.clusters.Contexts()).Active; got != 2 {
		t.Errorf("hotbar %d is on screen, expected 2", got)
	}

	// The bound is the store's and the message says how many there are, which is
	// the number `preferences.rail.barCount` reports. A handler-side ceiling
	// would be a second answer to the same question, and the one that goes stale.
	err := prefs.SetRailBar(99)
	if err == nil {
		t.Fatal("hotbar 99 was accepted and there are not 99 hotbars")
	}
	if !errors.Is(err, api.ErrInvalid) {
		t.Errorf("%v is not an ErrInvalid, so it would reach the client as a 500", err)
	}
	if got := server.bars.Arrangement(server.clusters.Contexts()).Active; got != 2 {
		t.Errorf("a refused bar changed the arrangement anyway: now %d", got)
	}
}

// TestSetRailAssignSendsATileToAHotbar pins the `slot` member reaching the
// store as "which bar", not as a position within a bar, which is what place
// does.
func TestSetRailAssignSendsATileToAHotbar(t *testing.T) {
	prefs, server := prefsUnder(t)

	if got := barOf(t, server, "docker-desktop"); got != 1 {
		t.Fatalf("the fixture starts docker-desktop on hotbar %d, expected 1", got)
	}
	if err := prefs.SetRailAssign("docker-desktop", 2); err != nil {
		t.Fatalf("SetRailAssign: %v", err)
	}
	if got := barOf(t, server, "docker-desktop"); got != 2 {
		t.Errorf("docker-desktop is on hotbar %d, expected 2", got)
	}
}

// TestSetRailPlaceDropsATileBesideAnother pins the other correction. The rail
// has no groups; a drop lands beside a named tile, and which side it lands on is
// the `edge` the body carries.
//
// The expectation is derived from the arrangement the fixture starts in rather
// than written out. Which order four contexts arrive in is the kubeconfig
// reader's business and not this test's, and a literal here would fail the day
// somebody adds a context to `railServerOn` — failing in a file about placing
// tiles, over something that has nothing to do with placing tiles.
func TestSetRailPlaceDropsATileBesideAnother(t *testing.T) {
	prefs, server := prefsUnder(t)

	start := tilesOn(t, server, 1)
	if len(start) < 3 {
		t.Fatalf("the fixture's hotbar 1 holds %d tiles, too few to place one beside another", len(start))
	}
	anchor, dragged := start[0], start[2]

	// Before the first tile is the front of the bar.
	if err := prefs.SetRailPlace(dragged, anchor, false); err != nil {
		t.Fatalf("SetRailPlace before: %v", err)
	}
	now := tilesOn(t, server, 1)
	if now[0] != dragged || now[1] != anchor {
		t.Errorf("after a drop before %q the bar starts %v, expected %q then %q",
			anchor, now, dragged, anchor)
	}

	// And after it is one place further along, which is the assertion that would
	// fail if `edge` were dropped on the way through.
	if err := prefs.SetRailPlace(dragged, anchor, true); err != nil {
		t.Fatalf("SetRailPlace after: %v", err)
	}
	now = tilesOn(t, server, 1)
	if now[0] != anchor || now[1] != dragged {
		t.Errorf("after a drop after %q the bar starts %v, expected %q then %q",
			anchor, now, anchor, dragged)
	}
}

// TestSetRailStepNudgesATileOnePlace pins the arm the two arrows send, which had
// no path to the store at all: the API could only say "position 3", and the
// store has always moved a tile by a delta.
func TestSetRailStepNudgesATileOnePlace(t *testing.T) {
	prefs, server := prefsUnder(t)

	start := tilesOn(t, server, 1)
	if len(start) < 2 {
		t.Fatalf("the fixture's hotbar 1 holds %d tiles, too few to nudge one", len(start))
	}
	first, second := start[0], start[1]

	if err := prefs.SetRailStep(second, -1); err != nil {
		t.Fatalf("SetRailStep up: %v", err)
	}
	if now := tilesOn(t, server, 1); now[0] != second || now[1] != first {
		t.Errorf("after one step up the bar starts %v, expected %q then %q", now, second, first)
	}
	if err := prefs.SetRailStep(second, 1); err != nil {
		t.Fatalf("SetRailStep down: %v", err)
	}
	if now := tilesOn(t, server, 1); now[0] != first || now[1] != second {
		t.Errorf("after one step back down the bar starts %v, expected %q then %q", now, first, second)
	}
}

// TestTheNameplateWriteIsPartial pins the difference this adapter exists to
// keep: a member that was not said leaves that part of the nameplate alone.
//
// The failure it guards against is silent and cumulative. A handler that filled
// the unsaid members with "" would clear the initials of every cluster somebody
// recolored, one recolor at a time, answering 204 each time.
func TestTheNameplateWriteIsPartial(t *testing.T) {
	prefs, server := prefsUnder(t)

	label, initials, colour := "Production", "PR", "amber"
	if err := prefs.SetRailNameplate(api.RailNameplateWrite{
		Cluster: "mi-k3s", Label: &label, Initials: &initials, Colour: &colour,
	}); err != nil {
		t.Fatalf("the first write: %v", err)
	}
	plate := server.plates.Get("mi-k3s")
	if plate.Name != "Production" || plate.Initials != "PR" || plate.Colour != "amber" {
		t.Fatalf("all three were said and the plate is %+v", plate)
	}

	// Only the color. The other two must survive.
	teal := "teal"
	if err := prefs.SetRailNameplate(api.RailNameplateWrite{
		Cluster: "mi-k3s", Colour: &teal,
	}); err != nil {
		t.Fatalf("the color alone: %v", err)
	}
	plate = server.plates.Get("mi-k3s")
	if plate.Name != "Production" || plate.Initials != "PR" {
		t.Errorf("naming only the color lost the rest: %+v", plate)
	}
	if plate.Colour != "teal" {
		t.Errorf("the color is %q, expected teal", plate.Colour)
	}

	// An empty member is said, and says "derive it" — which is a different
	// answer from not saying it, and the reason every member is a pointer.
	empty := ""
	if err := prefs.SetRailNameplate(api.RailNameplateWrite{
		Cluster: "mi-k3s", Label: &empty,
	}); err != nil {
		t.Fatalf("the empty label: %v", err)
	}
	if plate = server.plates.Get("mi-k3s"); plate.Name != "" || plate.Initials != "PR" {
		t.Errorf("an empty label should clear the name and nothing else: %+v", plate)
	}

	// And reset clears the lot in one gesture.
	yes := true
	if err := prefs.SetRailNameplate(api.RailNameplateWrite{
		Cluster: "mi-k3s", Reset: &yes,
	}); err != nil {
		t.Fatalf("the reset: %v", err)
	}
	if plate = server.plates.Get("mi-k3s"); !plate.IsZero() {
		t.Errorf("after a reset the plate is %+v, expected the derived form", plate)
	}
}

// TestTheIconIsAFourthPartialMember pins the icon recipe into
// the same partial write: said alone it leaves the color, which is what the
// tile falls back to; said empty it clears only itself; reset clears it too.
func TestTheIconIsAFourthPartialMember(t *testing.T) {
	prefs, server := prefsUnder(t)

	colour, icon := "amber", `{"v":1,"style":"rings","seed":"mi-k3s"}`
	if err := prefs.SetRailNameplate(api.RailNameplateWrite{Cluster: "mi-k3s", Colour: &colour}); err != nil {
		t.Fatalf("the color: %v", err)
	}
	if err := prefs.SetRailNameplate(api.RailNameplateWrite{Cluster: "mi-k3s", Icon: &icon}); err != nil {
		t.Fatalf("the icon alone: %v", err)
	}
	if plate := server.plates.Get("mi-k3s"); plate.Icon != icon || plate.Colour != "amber" {
		t.Errorf("naming only the icon changed the rest: %+v", plate)
	}
	if got := prefs.Preferences().Rail.Nameplates["mi-k3s"].Icon; got != icon {
		t.Errorf("the preferences read back icon %q, want %q", got, icon)
	}

	empty := ""
	if err := prefs.SetRailNameplate(api.RailNameplateWrite{Cluster: "mi-k3s", Icon: &empty}); err != nil {
		t.Fatalf("the empty icon: %v", err)
	}
	if plate := server.plates.Get("mi-k3s"); plate.Icon != "" || plate.Colour != "amber" {
		t.Errorf("an empty icon should clear the icon and nothing else: %+v", plate)
	}

	notJSON := "<svg/>"
	if err := prefs.SetRailNameplate(api.RailNameplateWrite{Cluster: "mi-k3s", Icon: &notJSON}); !errors.Is(err, api.ErrInvalid) {
		t.Errorf("a recipe that is not JSON answered %v, want an ErrInvalid", err)
	}

	yes := true
	if err := prefs.SetRailNameplate(api.RailNameplateWrite{Cluster: "mi-k3s", Icon: &icon}); err != nil {
		t.Fatalf("the icon again: %v", err)
	}
	if err := prefs.SetRailNameplate(api.RailNameplateWrite{Cluster: "mi-k3s", Reset: &yes}); err != nil {
		t.Fatalf("the reset: %v", err)
	}
	if plate := server.plates.Get("mi-k3s"); !plate.IsZero() {
		t.Errorf("after a reset the plate is %+v, expected the derived form", plate)
	}
}

// TestAColourOffThePaletteIsRefused pins that the store's refusal survives the
// trip, as an ErrInvalid rather than a 500.
//
// The palette is a closed list because a nameplate color ends up in a style
// attribute. A value that is not on it is refused rather than substituted: a
// console that silently picks a different color from the one asked for is one
// nobody can debug.
func TestAColourOffThePaletteIsRefused(t *testing.T) {
	prefs, server := prefsUnder(t)

	rebeccapurple := "rebeccapurple"
	err := prefs.SetRailNameplate(api.RailNameplateWrite{
		Cluster: "mi-k3s", Colour: &rebeccapurple,
	})
	if err == nil {
		t.Fatal("a color that is not on the palette was accepted")
	}
	if !errors.Is(err, api.ErrInvalid) {
		t.Errorf("%v is not an ErrInvalid, so it would reach the client as a 500", err)
	}
	if plate := server.plates.Get("mi-k3s"); !plate.IsZero() {
		t.Errorf("a refused color was written anyway: %+v", plate)
	}
}

// TestSetColumnPlaceMovesAColumnBesideAnother pins the arm the API could not
// reach. `SetColumnOrder` translates a position into this call; this one is the
// call itself, so nothing depends on the list the browser last drew.
func TestSetColumnPlaceMovesAColumnBesideAnother(t *testing.T) {
	prefs, server := prefsUnder(t)

	titles, keys := titlesOf("pods"), keysOf("pods")
	if len(titles) < 3 {
		t.Fatalf("pods declares %d columns, too few to move one between two others", len(titles))
	}
	// The API is spoken to in keys - what the browser posts - and the store is
	// read back in titles - what it files by. The seam between the two is
	// `columnTitle`, and this is the test that crosses it.
	first, third := titles[0], titles[2]
	firstKey, thirdKey := keys[0], keys[2]

	if err := prefs.SetColumnPlace("pods", thirdKey, firstKey, false); err != nil {
		t.Fatalf("SetColumnPlace before: %v", err)
	}
	got := server.columns.Arrangement("pods", titles).Columns
	if got[0].Title != third || got[1].Title != first {
		t.Errorf("after a drop before %q the order starts %q, %q", first, got[0].Title, got[1].Title)
	}

	if err := prefs.SetColumnPlace("pods", thirdKey, firstKey, true); err != nil {
		t.Fatalf("SetColumnPlace after: %v", err)
	}
	got = server.columns.Arrangement("pods", titles).Columns
	if got[0].Title != first || got[1].Title != third {
		t.Errorf("after a drop after %q the order starts %q, %q", first, got[0].Title, got[1].Title)
	}

	// A column that is not on the table is a refusal and not a silent no-op: the
	// operator dragged something, and nothing having happened needs a sentence.
	err := prefs.SetColumnPlace("pods", thirdKey, "Not A Column", false)
	if err == nil {
		t.Fatal("a drop onto a column that does not exist was accepted")
	}
	if !errors.Is(err, api.ErrInvalid) {
		t.Errorf("%v is not an ErrInvalid, so it would reach the client as a 500", err)
	}
}

// TestDensityIsOneSettingForEveryTable pins the member that left the contract.
//
// It is the console's row height and not a table's, which is why the write
// carries no kind: a grid comfortable on pods and compact on services is two
// grids. The store has nowhere to put a kind.
func TestDensityIsOneSettingForEveryTable(t *testing.T) {
	prefs, server := prefsUnder(t)

	if err := prefs.SetColumnDensity("compact"); err != nil {
		t.Fatalf("SetColumnDensity(compact): %v", err)
	}
	if got := server.columns.Density(); got != "compact" {
		t.Errorf("the density is %q, expected compact", got)
	}
	// "comfortable" is the default, which the store spells as the empty string.
	if err := prefs.SetColumnDensity("comfortable"); err != nil {
		t.Fatalf("SetColumnDensity(comfortable): %v", err)
	}
	if got := server.columns.Density(); got != "" {
		t.Errorf("the density is %q, expected the default", got)
	}
}

// keysOf is the vocabulary the JSON API speaks about columns: the `key` each
// entry of `ListPage.columns` carries, which is what the column menu and the
// resize grip post back. It is `columnKey(title, index)`, the same function the
// list payload uses, so a test here cannot drift from what the browser sends.
func keysOf(kind string) []string {
	titles := titlesOf(kind)
	keys := make([]string, 0, len(titles))
	for i, title := range titles {
		keys = append(keys, columnKey(title, i))
	}
	return keys
}

// TestHidingAColumnByItsKeyHidesIt is the read-back the seam never had.
//
// The column menu posts `column.key` - `cpu-7` - and the store files hidden
// columns by title - `CPU`. `SetVisible` accepts any non-empty string, so a
// write by key would answer 204, store a name no column has, and the next page
// read would draw the column visible. The conformance test that checks the
// route answers 204 cannot see that.
//
// The test speaks the browser's vocabulary end to end: a key in, the
// arrangement read back and asserted on the title.
func TestHidingAColumnByItsKeyHidesIt(t *testing.T) {
	prefs, server := prefsUnder(t)

	titles, keys := titlesOf("pods"), keysOf("pods")
	if len(titles) < 3 {
		t.Fatalf("pods declares %d columns, too few to hide one that is not the name", len(titles))
	}
	// Not the first: the store refuses to hide the column that names the row.
	title, key := titles[2], keys[2]

	if err := prefs.SetColumnVisible("pods", key, false); err != nil {
		t.Fatalf("SetColumnVisible(%q): %v", key, err)
	}
	var seen bool
	for _, c := range server.columns.Arrangement("pods", titles).Columns {
		if c.Title == title {
			seen = true
			if !c.Decided || !c.Hidden {
				t.Errorf("after hiding %q by its key %q the store reads decided=%v hidden=%v", title, key, c.Decided, c.Hidden)
			}
		}
		if c.Title == key {
			t.Errorf("the key %q was stored as if it were a title", key)
		}
	}
	if !seen {
		t.Fatalf("%q is not in the arrangement at all", title)
	}

	if err := prefs.SetColumnVisible("pods", key, true); err != nil {
		t.Fatalf("SetColumnVisible(%q, true): %v", key, err)
	}
	for _, c := range server.columns.Arrangement("pods", titles).Columns {
		if c.Title == title && c.Hidden {
			t.Errorf("%q is still hidden after being shown again", title)
		}
	}

	// A name that is neither a key nor a title is a refusal, not a stored
	// entry: the operator ticked something, and nothing having happened needs a
	// sentence rather than a 204.
	err := prefs.SetColumnVisible("pods", "Not A Column", false)
	if err == nil {
		t.Fatal("a column name the table does not have was accepted")
	}
	if !errors.Is(err, api.ErrInvalid) {
		t.Errorf("%v is not an ErrInvalid, so it would reach the client as a 500", err)
	}
}

// TestResizingAColumnByItsKeyStoresTheWidth is the same seam on the other
// write. `Resize` refuses an unknown title, so a dragged width posted under its
// key must not be refused with a 400 the grip has no sentence for.
func TestResizingAColumnByItsKeyStoresTheWidth(t *testing.T) {
	prefs, server := prefsUnder(t)

	titles, keys := titlesOf("pods"), keysOf("pods")
	title, key := titles[1], keys[1]

	width := 240
	if err := prefs.SetColumnWidth("pods", key, &width); err != nil {
		t.Fatalf("SetColumnWidth(%q): %v", key, err)
	}
	var got int
	for _, c := range server.columns.Arrangement("pods", titles).Columns {
		if c.Title == title {
			got = c.Width
		}
	}
	if got != width {
		t.Errorf("after resizing %q by its key %q the store holds width %d, want %d", title, key, got, width)
	}

	if err := prefs.SetColumnWidth("pods", key, nil); err != nil {
		t.Fatalf("SetColumnWidth(%q, nil): %v", key, err)
	}
	for _, c := range server.columns.Arrangement("pods", titles).Columns {
		if c.Title == title && c.Width != 0 {
			t.Errorf("%q still holds width %d after being reset to automatic", title, c.Width)
		}
	}
}

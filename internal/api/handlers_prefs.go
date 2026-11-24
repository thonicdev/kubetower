package api

import (
	"net/http"
	"regexp"
	"strconv"
	"unicode/utf8"
)

// Twelve writes, one cell each, on purpose: a write posting the whole grid
// would make unticking one column re-assert every other from whatever the page
// held. Each answers 204; the front end already knows what it asked for.

func (a *API) prefsOrMissing(w http.ResponseWriter) bool {
	if a.deps.Prefs == nil {
		missing(w, "a preference store")
		return false
	}
	return true
}

func (a *API) getPreferences(w http.ResponseWriter, r *http.Request) {
	if !a.prefsOrMissing(w) {
		return
	}
	// Not enveloped: the console's own file cannot be unreachable.
	writeOK(w, a.deps.Prefs.Preferences().normalised())
}

// wrote is the shared tail of all twelve: the error map, then 204.
func wrote(w http.ResponseWriter, err error) {
	if err != nil {
		fail(w, err)
		return
	}
	writeNoContent(w)
}

func (a *API) setRailBar(w http.ResponseWriter, r *http.Request) {
	var body RailBarWrite
	if !decode(w, r, maxBody, &body) || !requireAll(w, "bar", body.Bar) || !a.prefsOrMissing(w) {
		return
	}
	if *body.Bar < 1 {
		badRequest(w, "hotbars are numbered from 1")
		return
	}
	// The upper bound is the store's (`preferences.rail.barCount`), not
	// repeated here.
	wrote(w, a.deps.Prefs.SetRailBar(*body.Bar))
}

func (a *API) setRailAssign(w http.ResponseWriter, r *http.Request) {
	var body RailAssignWrite
	if !decode(w, r, maxBody, &body) ||
		!requireAll(w, "cluster", body.Cluster, "bar", body.Bar) || !a.prefsOrMissing(w) {
		return
	}
	if *body.Bar < 1 {
		badRequest(w, "hotbars are numbered from 1")
		return
	}
	wrote(w, a.deps.Prefs.SetRailAssign(body.Cluster, *body.Bar))
}

// setRailMove takes one of two arms, and refuses both and neither. The schema
// is a `oneOf` over `direction` and `order`; a hand-written body saying "up" and
// "position 4" has not said what it wants, and this handler will not choose.
func (a *API) setRailMove(w http.ResponseWriter, r *http.Request) {
	var body RailMoveWrite
	if !decode(w, r, maxBody, &body) ||
		!requireAll(w, "cluster", body.Cluster) || !a.prefsOrMissing(w) {
		return
	}
	if (body.Direction == nil) == (body.Order == nil) {
		badRequest(w, "exactly one of direction and order")
		return
	}
	if body.Direction != nil {
		delta, ok := step(w, *body.Direction)
		if !ok {
			return
		}
		wrote(w, a.deps.Prefs.SetRailStep(body.Cluster, delta))
		return
	}
	if *body.Order < 0 || *body.Order > 9999 {
		badRequest(w, "order is between 0 and 9999")
		return
	}
	wrote(w, a.deps.Prefs.SetRailMove(body.Cluster, *body.Order))
}

// step turns the direction the two arrows send into the delta the store takes.
func step(w http.ResponseWriter, direction string) (int, bool) {
	switch direction {
	case "up":
		return -1, true
	case "down":
		return 1, true
	}
	badRequest(w, "direction is up or down")
	return 0, false
}

// edgeAfter reads the `edge` that both place writes carry. Absent is `before`,
// the schema's default; any other value is refused rather than read as
// `before`, which would answer a mistyped `after` with the opposite and a 204.
func edgeAfter(w http.ResponseWriter, edge *string) (bool, bool) {
	if edge == nil {
		return false, true
	}
	switch *edge {
	case "before":
		return false, true
	case "after":
		return true, true
	}
	badRequest(w, "edge is before or after")
	return false, false
}

// setRailPlace drops a tile beside another tile, never at an index: a
// neighbor is a fact about the server's arrangement, an index only about the
// list the browser last saw (see `internal/hotbars`.Place).
func (a *API) setRailPlace(w http.ResponseWriter, r *http.Request) {
	var body RailPlaceWrite
	if !decode(w, r, maxBody, &body) ||
		!requireAll(w, "cluster", body.Cluster, "target", body.Target) || !a.prefsOrMissing(w) {
		return
	}
	after, ok := edgeAfter(w, body.Edge)
	if !ok {
		return
	}
	wrote(w, a.deps.Prefs.SetRailPlace(body.Cluster, *body.Target, after))
}

// setRailHide and setRailReveal are two routes rather than one toggle, so a
// client with a stale view of what is hidden cannot flip the wrong way.
func (a *API) setRailHide(w http.ResponseWriter, r *http.Request) {
	a.railTile(w, r, true)
}

func (a *API) setRailReveal(w http.ResponseWriter, r *http.Request) {
	a.railTile(w, r, false)
}

func (a *API) railTile(w http.ResponseWriter, r *http.Request, hidden bool) {
	var body RailTileWrite
	if !decode(w, r, maxBody, &body) || !requireAll(w, "cluster", body.Cluster) || !a.prefsOrMissing(w) {
		return
	}
	wrote(w, a.deps.Prefs.SetRailHidden(body.Cluster, hidden))
}

// setRailNameplate sets one tile's appearance (label, short form, color) or
// returns parts of it to the derived form.
//
// Every member is a pointer because absent and empty differ: absent leaves
// that part alone, empty returns it to what the context name derives. The
// label never replaces the context name in an address, which the router keys
// on.
func (a *API) setRailNameplate(w http.ResponseWriter, r *http.Request) {
	var body RailNameplateWrite
	if !decode(w, r, maxBody, &body) ||
		!requireAll(w, "cluster", body.Cluster) || !a.prefsOrMissing(w) {
		return
	}
	// Characters, not bytes: the schema's maxLength counts characters.
	for _, limit := range []struct {
		what  string
		value *string
		max   int
	}{
		{"a label", body.Label, 128},
		{"initials", body.Initials, 8},
		{"a color", body.Colour, 64},
		{"an icon recipe", body.Icon, 1024},
	} {
		if limit.value != nil && utf8.RuneCountInString(*limit.value) > limit.max {
			badRequest(w, limit.what+" is at most "+strconv.Itoa(limit.max)+" characters")
			return
		}
	}
	wrote(w, a.deps.Prefs.SetRailNameplate(body))
}

func (a *API) setColumnSize(w http.ResponseWriter, r *http.Request) {
	var body ColumnSizeWrite
	if !decode(w, r, maxBody, &body) ||
		!requireAll(w, "kind", body.Kind, "column", body.Column) || !a.prefsOrMissing(w) {
		return
	}
	// Width is nullable and null means automatic, so absent and null agree
	// here and only here.
	if body.Width != nil && (*body.Width < 16 || *body.Width > 4096) {
		badRequest(w, "width is between 16 and 4096 pixels, or null for automatic")
		return
	}
	wrote(w, a.deps.Prefs.SetColumnWidth(body.Kind, body.Column, body.Width))
}

// setColumnPlace takes the same two arms as setRailMove, refusing both and
// neither.
func (a *API) setColumnPlace(w http.ResponseWriter, r *http.Request) {
	var body ColumnPlaceWrite
	if !decode(w, r, maxBody, &body) ||
		!requireAll(w, "kind", body.Kind, "column", body.Column) ||
		!a.prefsOrMissing(w) {
		return
	}
	if (body.Target == nil) == (body.Order == nil) {
		badRequest(w, "exactly one of target and order")
		return
	}
	if body.Target != nil {
		after, ok := edgeAfter(w, body.Edge)
		if !ok {
			return
		}
		wrote(w, a.deps.Prefs.SetColumnPlace(body.Kind, body.Column, *body.Target, after))
		return
	}
	// `edge` names a side of `target`, so with no target it is refused rather
	// than silently dropped.
	if body.Edge != nil {
		badRequest(w, "edge names which side of target, so it belongs with target")
		return
	}
	if *body.Order < 0 || *body.Order > 999 {
		badRequest(w, "order is between 0 and 999")
		return
	}
	wrote(w, a.deps.Prefs.SetColumnOrder(body.Kind, body.Column, *body.Order))
}

// setColumnShow writes one column's visibility; the decoder refuses a second
// field, so a stale page cannot re-assert the rest.
func (a *API) setColumnShow(w http.ResponseWriter, r *http.Request) {
	var body ColumnShowWrite
	if !decode(w, r, maxBody, &body) ||
		!requireAll(w, "kind", body.Kind, "column", body.Column, "visible", body.Visible) ||
		!a.prefsOrMissing(w) {
		return
	}
	wrote(w, a.deps.Prefs.SetColumnVisible(body.Kind, body.Column, *body.Visible))
}

// setColumnDensity is the one preference write whose subject is the console
// rather than a table, so it carries no kind.
func (a *API) setColumnDensity(w http.ResponseWriter, r *http.Request) {
	var body ColumnDensityWrite
	if !decode(w, r, maxBody, &body) || !requireAll(w, "density", body.Density) ||
		!a.prefsOrMissing(w) {
		return
	}
	if body.Density != "comfortable" && body.Density != "compact" {
		badRequest(w, "density is comfortable or compact")
		return
	}
	wrote(w, a.deps.Prefs.SetColumnDensity(body.Density))
}

// release is a bare semver, as the tags and CHANGELOG.md spell it.
var release = regexp.MustCompile(`^[0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6}(-[0-9A-Za-z.-]{1,32})?$`)

// setSeenVersion records that this release's "What's new" was seen (#80).
func (a *API) setSeenVersion(w http.ResponseWriter, r *http.Request) {
	var body SeenVersionWrite
	if !decode(w, r, maxBody, &body) || !requireAll(w, "version", body.Version) ||
		!a.prefsOrMissing(w) {
		return
	}
	if !release.MatchString(body.Version) {
		badRequest(w, "version is a release version, such as 0.2.0")
		return
	}
	wrote(w, a.deps.Prefs.SetSeenVersion(body.Version))
}

// resetColumns is the one write that is not a single cell, because forgetting
// is the operation. It names the kind it forgets, so it is never a
// whole-console reset.
//
// `scope` is defaulted and validated here only. Absent means "all"; anything
// unknown is refused with the vocabulary rather than widened, since "all" is the
// most destructive and a misspelling must not fall through to it. It is a
// pointer because `""` is not absent: a client that sent an empty scope must not
// get "forget everything" (`internal/columns.Scope.Valid` refuses "" too).
func (a *API) resetColumns(w http.ResponseWriter, r *http.Request) {
	var body ColumnResetWrite
	if !decode(w, r, maxBody, &body) || !requireAll(w, "kind", body.Kind) || !a.prefsOrMissing(w) {
		return
	}
	scope := "all"
	if body.Scope != nil {
		scope = *body.Scope
	}
	switch scope {
	case "all", "widths", "order", "hidden":
	default:
		badRequest(w, "scope is all, widths, order or hidden")
		return
	}
	wrote(w, a.deps.Prefs.ResetColumns(body.Kind, scope))
}

// --- The kinds column, per person -----------------------

func (a *API) kindPrefsOrMissing(w http.ResponseWriter) bool {
	if a.deps.KindPrefs == nil {
		missing(w, "a kind preference store")
		return false
	}
	return true
}

func (a *API) getKindPreferences(w http.ResponseWriter, r *http.Request) {
	if !a.kindPrefsOrMissing(w) {
		return
	}
	prefs, err := a.deps.KindPrefs.KindPreferences(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeOK(w, prefs.normalised())
}

// setKindPreferences replaces both members at once: they are few and the
// client holds them whole. Bounds are checked here so a store never gets a
// body the contract refuses.
func (a *API) setKindPreferences(w http.ResponseWriter, r *http.Request) {
	var body KindPreferencesWrite
	if !decode(w, r, maxBody, &body) || !requireAll(w, "showUnused", body.ShowUnused) || !a.kindPrefsOrMissing(w) {
		return
	}
	if len(body.Favourites) > MaxFavouriteClusters {
		badRequest(w, "favorites names more than "+strconv.Itoa(MaxFavouriteClusters)+" clusters")
		return
	}
	for cluster, kinds := range body.Favourites {
		if cluster == "" {
			badRequest(w, "a favorites key is a cluster name, and it is empty")
			return
		}
		if len(kinds) > MaxFavouritesPerCluster {
			badRequest(w, "favorites holds more than "+strconv.Itoa(MaxFavouritesPerCluster)+" kinds for one cluster")
			return
		}
		for _, kind := range kinds {
			if kind == "" || utf8.RuneCountInString(kind) > MaxKindSlug {
				badRequest(w, "a favorite kind is a slug of 1 to "+strconv.Itoa(MaxKindSlug)+" characters")
				return
			}
		}
	}
	kept, err := a.deps.KindPrefs.SetKindPreferences(r.Context(),
		KindPreferences{ShowUnused: *body.ShowUnused, Favourites: body.Favourites}.normalised())
	if err != nil {
		fail(w, err)
		return
	}
	writeOK(w, kept.normalised())
}

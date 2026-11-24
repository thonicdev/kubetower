package api

// --- Preferences ------------------------------------------------------------

// RailNameplate is one cluster's appearance, as the user set it. Every
// member is optional and empty means "derive that one from the context name".
type RailNameplate struct {
	Name     string `json:"name,omitempty"`
	Initials string `json:"initials,omitempty"`
	// Color is a palette key, never a CSS value.
	Colour string `json:"colour,omitempty"`
	// Icon is the tile's icon recipe, one JSON object the browser writes and
	// reads. Empty means none: the default icon is drawn.
	Icon string `json:"icon,omitempty"`
}

// RailPreferences is the rail's stored arrangement: clusters on numbered
// hotbars, each an ordered list.
type RailPreferences struct {
	// BarCount is how many hotbars this build offers, a server constant sent so
	// the client never assumes a number and hides a bar.
	BarCount int `json:"barCount,omitempty"`
	// ActiveBar is which hotbar is on screen, 1-based.
	ActiveBar int `json:"activeBar,omitempty"`
	// Bars is one entry per hotbar, in bar order, each that bar's clusters in
	// drawing order. A bar may be empty; hidden clusters are in none. Required,
	// so no `omitempty` (see normalized).
	Bars [][]string `json:"bars"`
	// Hidden are the clusters taken off the rail, still reachable by address.
	// The cluster whose page is open may be here and on a bar at once, so the
	// rail never misstates where the user is.
	Hidden []string `json:"hidden,omitempty"`
	// Nameplates are the user's own names, keyed by context name. A cluster
	// with no entry is drawn from its context name.
	Nameplates map[string]RailNameplate `json:"nameplates,omitempty"`
}

// ColumnCell is one column's stored arrangement for one kind.
type ColumnCell struct {
	Column  string `json:"column"`
	Width   *int   `json:"width,omitempty"`
	Order   int    `json:"order,omitempty"`
	Visible *bool  `json:"visible,omitempty"`
}

// KindColumns is one kind's stored cells. Density is not here: it is one
// setting for every table.
type KindColumns struct {
	Cells []ColumnCell `json:"cells,omitempty"`
}

// ColumnPreferences is every table's arrangement, plus two facts about all of
// them.
type ColumnPreferences struct {
	// Density is comfortable or compact, for every table.
	Density string `json:"density,omitempty"`
	// WidthsAreDeliberate says at least one width was set by hand, which puts
	// tables on a fixed layout to the stored shares; an unresized table lays out
	// to its contents. It is derived from the arrangement, and is not
	// `columns.json`'s migration flag of the same name.
	WidthsAreDeliberate bool `json:"widthsAreDeliberate,omitempty"`
	// Kinds is keyed by kind. Required, so never nil (see normalized).
	Kinds map[string]KindColumns `json:"kinds"`
}

// Preferences is the read half of all twelve writes, in one payload: a client
// that has just started needs all of it.
type Preferences struct {
	Rail    RailPreferences   `json:"rail"`
	Columns ColumnPreferences `json:"columns"`
	// SeenVersion is the last release whose "What's new" was seen on this
	// desktop (#80). Absent where nothing was seen, and under the server
	// profile, where each browser keeps its own.
	SeenVersion string `json:"seenVersion,omitempty"`
}

// The twelve write bodies. Each is one cell, with `additionalProperties: false`:
// a body carrying the whole grid would re-assert every other cell from whatever
// the page last held.

// RailBarWrite selects which hotbar is on screen.
type RailBarWrite struct {
	Bar *int `json:"bar"`
}

// RailAssignWrite sends one cluster's tile to one hotbar. Placing it within a
// bar is `/rail/place`.
type RailAssignWrite struct {
	Cluster string `json:"cluster"`
	Bar     *int   `json:"bar"`
}

// RailMoveWrite nudges one tile one place along its hotbar. Exactly one of
// `direction` (what the buttons send) and `order` (an absolute position, weaker
// because it describes the list the browser last saw) is set.
type RailMoveWrite struct {
	Cluster   string  `json:"cluster"`
	Direction *string `json:"direction"`
	Order     *int    `json:"order"`
}

// RailPlaceWrite drops one cluster's tile beside another, moving it to that
// tile's hotbar if needed. A neighbor, never an index (see hotbars.Place).
type RailPlaceWrite struct {
	Cluster string  `json:"cluster"`
	Target  *string `json:"target"`
	// Edge is before or after; absent is before.
	Edge *string `json:"edge"`
}

// RailTileWrite names one cluster's tile. Hide and reveal are separate routes
// because a toggle sent from a stale view flips the wrong way.
type RailTileWrite struct {
	Cluster string `json:"cluster"`
}

// RailNameplateWrite sets one cluster's appearance. Every member is a pointer
// because absent and empty differ: absent leaves that part alone, empty returns
// it to the derived form. `Reset` returns every part at once.
type RailNameplateWrite struct {
	Cluster string `json:"cluster"`
	// Label never replaces the context name in an address.
	Label    *string `json:"label"`
	Initials *string `json:"initials"`
	// Color is a palette key, never a CSS value.
	Colour *string `json:"colour"`
	// Icon is the tile's icon recipe, or empty for none.
	Icon  *string `json:"icon"`
	Reset *bool   `json:"reset"`
}

// ColumnSizeWrite sets one column's width.
type ColumnSizeWrite struct {
	Kind   string `json:"kind"`
	Column string `json:"column"`
	Width  *int   `json:"width"`
}

// ColumnPlaceWrite moves one column. Exactly one of `target` (with `edge`) and
// the weaker absolute `order` is set, as in RailMoveWrite.
type ColumnPlaceWrite struct {
	Kind   string  `json:"kind"`
	Column string  `json:"column"`
	Target *string `json:"target"`
	// Edge is before or after, and belongs with Target.
	Edge  *string `json:"edge"`
	Order *int    `json:"order"`
}

// ColumnShowWrite shows or hides one column.
type ColumnShowWrite struct {
	Kind    string `json:"kind"`
	Column  string `json:"column"`
	Visible *bool  `json:"visible"`
}

// ColumnDensityWrite sets the row density, for every table. It has no `kind`:
// there is no per-kind density to write.
type ColumnDensityWrite struct {
	Density string `json:"density"`
}

// SeenVersionWrite records that this release's "What's new" was seen (#80).
type SeenVersionWrite struct {
	Version string `json:"version"`
}

// ColumnResetWrite forgets one kind's arrangement. It is not a single cell, but
// it names the kind, so it is never a whole-console reset.
type ColumnResetWrite struct {
	Kind string `json:"kind"`
	// Scope is which arrangement to forget: "widths", "order", "hidden", or
	// "all". Absent is "all". A pointer so that `""` is refused rather than read
	// as "all", which would forget what the client meant to keep. Separate scopes
	// so resetting widths does not also cost the column order (columns.Scope).
	Scope *string `json:"scope,omitempty"`
}

// ExtensionEnabledWrite is one extension on one cluster.
type ExtensionEnabledWrite struct {
	Enabled *bool `json:"enabled"`
}

// ExtensionSettingWrite is one setting of one extension on one cluster.
type ExtensionSettingWrite struct {
	// Exactly one of them, by the setting's kind.
	Value *bool   `json:"value"`
	Text  *string `json:"text"`
}

// --- The kinds column, per person -----------------------

// KindPreferences is one person's choices about the kinds column: whether it
// shows the kinds with no object they may see, and their favorite kinds per
// cluster, by slug.
type KindPreferences struct {
	ShowUnused bool                `json:"showUnused"`
	Favourites map[string][]string `json:"favourites"`
}

// KindPreferencesWrite replaces them. ShowUnused is a pointer so a body that
// left it out is refused rather than read as false.
type KindPreferencesWrite struct {
	ShowUnused *bool               `json:"showUnused"`
	Favourites map[string][]string `json:"favourites"`
}

// The bounds a kinds write is held to.
const (
	MaxFavouriteClusters    = 256
	MaxFavouritesPerCluster = 512
	MaxKindSlug             = 317
)

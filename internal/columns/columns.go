// Package columns is the operator's own arrangement of a resource table: the
// order the columns are drawn in and how wide each one is, per kind, plus the
// small JSON file that remembers both across a restart.
//
// Like `internal/hotbars`, it is the operator's preference, kept apart from
// `internal/web`, and knows nothing about Kubernetes: a column is a title and a
// number.
//
// A stored layout is not authority over what a table has: the columns come
// from `internal/resources` or the cluster's CRD printer columns, which change
// with a chart upgrade. Everything is reconciled against the titles the caller
// hands in, on every read (see Arrangement).
package columns

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/thonicdev/kubetower/internal/statefile"
)

// FileName is the arrangement, inside the state directory. The directory, the
// write and the once-only logging are `internal/statefile`'s.
const FileName = "columns.json"

// The bounds a width is held to, in CSS pixels.
//
// Clamped here and not only in the browser, because a drag and the file are
// both input: a stored 4 would draw a column nobody can grab again. MinWidth
// keeps the grip and a few characters; MaxWidth fits an image reference without
// pushing the other columns off screen.
//
// A stored width is a share (Column.Width), so the pair bounds a ratio, while
// MinWidth is still exact in pixels: a fixed-layout table is never drawn
// narrower than the sum of its columns. The browser scales a whole set before
// posting it, since clamping one number would change the ratios (`postable` in
// `DataGrid.tsx`).
const (
	MinWidth = 56
	MaxWidth = 720
)

// Column is one heading in the arrangement.
type Column struct {
	Title string
	// Width is this column's share of the table, or 0 for "lay out to the
	// contents".
	//
	// The unit is the CSS pixel, what a pointer produces, but the number is read
	// against the table's other columns: the client lays the table out to the
	// ratios, so a set summing to 1266 fills any panel width in proportion.
	Width int
	// Hidden is a column the operator has taken off the table. It stays in the
	// list, so the caller can offer it back with its width and place.
	Hidden bool
	// Decided says Hidden is the operator's answer rather than the absence of
	// one, so a caller applying a descriptor's default must not override it.
	Decided bool
	// At is this column's index in the list the caller handed in, what the
	// caller permutes its cells by. Not a title: a CRD can repeat a title.
	At int
}

// Layout is one kind's arrangement, a permutation of the columns the caller
// handed in: every column once, and nothing else.
type Layout struct {
	Kind    string
	Columns []Column
	// Custom says something is stored for this kind, so the page offers a reset.
	Custom bool
}

// Sized reports whether any column has a width of the operator's choosing, so
// the table lays out to those shares rather than its contents and clips a value
// too long for its column.
func (l Layout) Sized() bool {
	for _, c := range l.Columns {
		if c.Width > 0 {
			return true
		}
	}
	return false
}

// Store is the arrangement and the file under it.
// Everything goes through the mutex, the file write included, so two
// concurrent writes cannot interleave into a file that describes neither.
type Store struct {
	// path is empty when there is nowhere to persist to; the arrangement then
	// lives as long as the process.
	path string
	// say logs a standing write failure once. See statefile.Complainer.
	say *statefile.Complainer

	mu    sync.RWMutex
	kinds map[string]*arrangement
	// density is how tightly every table is drawn: "" for the default or
	// "compact". One value for the console, not per kind: it is about the
	// operator's screen.
	density string
}

// arrangement is what is remembered about one kind.
//
// It keeps entries for titles the current table does not have: one kind slug
// can have different printer columns on two clusters, and dropping unseen
// titles would lose every cluster's arrangement but the last one looked at.
type arrangement struct {
	order []string
	// widths is by title, each a share of the table (Column.Width).
	widths map[string]int
	// hidden is by title, with three states: absent is no opinion (the
	// descriptor's default applies, and some columns default to hidden,
	// `resources.Column.Secondary`), true is hidden on purpose, false is shown
	// on purpose.
	hidden map[string]bool
}

// empty says this kind holds nothing of the operator's: no order, no widths and
// nothing hidden or explicitly shown.
// A nil arrangement is empty. It is the one "nothing stored" predicate for
// every caller.
func (a *arrangement) empty() bool {
	return a == nil || (len(a.order) == 0 && len(a.widths) == 0 && len(a.hidden) == 0)
}

// Dir is where the arrangement is kept: KT_STATE_DIR, or a per-user path when
// it is not set. In a container it must be a volume.
func Dir() string { return statefile.Dir() }

// New opens the store, reading the arrangement if there is one.
// A file that cannot be read is logged, and the default tables used.
func New(dir string, logf func(string, ...any)) *Store {
	s := &Store{say: statefile.NewComplainer(logf), kinds: map[string]*arrangement{}}
	if dir = strings.TrimSpace(dir); dir != "" {
		s.path = filepath.Join(dir, FileName)
	}
	s.load()
	return s
}

// Path is the file the arrangement is written to, or "" when it is only in
// memory. Reported at startup.
func (s *Store) Path() string { return s.path }

type persisted struct {
	Kinds map[string]persistedKind `json:"kinds,omitempty"`
	// Density is "compact" or absent. Absent is the default, the roomy table.
	Density string `json:"density,omitempty"`
	// WidthsAreDeliberate marks a file whose stored widths were each asked for.
	// A file without it has its widths dropped once on load, because an older
	// format stored every column's width on one drag, as a side effect rather
	// than a choice. The order and the hidden columns are kept.
	WidthsAreDeliberate bool `json:"widthsAreDeliberate,omitempty"`
	// WidthsAreShares records that the widths are read as shares of the table.
	// It converts nothing: pixel widths measured at one panel width already are
	// shares. Every save writes it true and nothing reads it back; it says what
	// the data means. It is a separate field because WidthsAreDeliberate is
	// already true in every file and so can no longer tell anything apart.
	WidthsAreShares bool `json:"widthsAreShares,omitempty"`
}

type persistedKind struct {
	// Hidden is the titles taken off the table; absent means none.
	Hidden []string `json:"hidden,omitempty"`
	// Shown is the titles the operator asked for explicitly, the ones that
	// would otherwise default to hidden.
	Shown []string `json:"shown,omitempty"`
	// Order is titles, and is absent for a kind that has only been resized.
	Order []string `json:"order,omitempty"`
	// Widths is keyed by title, so a repeated title shares one width; an index
	// would follow whatever moved into that position.
	//
	// The shares are not normalized: the bounds are in pixels, so a normalized
	// 3 would be clamped to 56 and the arrangement lost.
	Widths map[string]int `json:"widths,omitempty"`
}

func (s *Store) load() {
	if s.path == "" {
		return
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			s.say.Log("columns: %s could not be read (%v); starting from the default tables", s.path, err)
		}
		return
	}
	var file persisted
	if err := json.Unmarshal(raw, &file); err != nil {
		s.say.Log("columns: %s is not readable JSON (%v); starting from the default tables", s.path, err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if file.Density == Compact {
		s.density = Compact
	}
	// See persisted.WidthsAreDeliberate: such widths are never read at all.
	keepWidths := file.WidthsAreDeliberate
	if !keepWidths {
		s.say.Log("columns: dropping the stored column widths once - the build that wrote them " +
			"stored all of them on one resize, so they are a side effect rather than a choice. " +
			"The order and the hidden columns are kept, and widths set from now on stick")
	}
	for kind, stored := range file.Kinds {
		// The order is read as written, repeats included: a CRD with two
		// `Status` columns stores two, and `reconcile` places each. Deduping
		// would move the second to the end on every restart.
		entry := &arrangement{order: append([]string(nil), stored.Order...), widths: map[string]int{}, hidden: map[string]bool{}}
		for _, title := range stored.Hidden {
			entry.hidden[title] = true
		}
		for _, title := range stored.Shown {
			// A title in both lists (a hand edit) stays hidden.
			if !entry.hidden[title] {
				entry.hidden[title] = false
			}
		}
		if keepWidths {
			for title, width := range stored.Widths {
				// Clamped on the way in too: the file is input.
				if w := clampWidth(width); w > 0 {
					entry.widths[title] = w
				}
			}
		}
		if entry.empty() {
			continue
		}
		s.kinds[kind] = entry
	}
}

// Arrangement is one kind's layout, reconciled against the titles that exist
// right now.
//
// `titles` is the authority; the stored names only say order and width. A
// column the file names that is gone is dropped, so no heading is drawn over
// cells that do not exist; a column the file has never heard of is drawn at
// the end.
func (s *Store) Arrangement(kind string, titles []string) Layout {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reconcile(kind, titles)
}

// Resize records the widths of one kind's columns.
//
// Plural, because widening one column takes width from the others: widths are
// shares (Column.Width), so the browser sends the whole arrangement it draws.
//
// Widths are clamped, and a title this table does not have is refused rather
// than stored: it is a stale page or a bug.
func (s *Store) Resize(kind string, sizes []Column, titles []string) (Layout, error) {
	if strings.TrimSpace(kind) == "" {
		return Layout{}, fmt.Errorf("which kind's columns?")
	}
	live := set(titles)
	for _, c := range sizes {
		if !live[c.Title] {
			return Layout{}, fmt.Errorf("%q is not a column of %s", c.Title, kind)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry := s.entry(kind)
	for _, c := range sizes {
		if w := clampWidth(c.Width); w > 0 {
			entry.widths[c.Title] = w
		} else {
			delete(entry.widths, c.Title)
		}
	}
	s.forget(kind)
	// The layout goes back even on a failed write: the widths hold in memory.
	// errors.Is(err, statefile.ErrNotSaved) tells this from a refusal above.
	err := s.save()
	return s.reconcile(kind, titles), err
}

// Place puts a column immediately before or after another one. It is what a drag
// on a heading ends in: the heading that was dropped on, and which side of it
// the pointer was.
// A neighbor rather than an index, as in hotbars.Place: the server resolves it
// against what it holds, not the table the browser last drew.
func (s *Store) Place(kind, title, target string, after bool, titles []string) (Layout, error) {
	if strings.TrimSpace(kind) == "" {
		return Layout{}, fmt.Errorf("which kind's columns?")
	}
	live := set(titles)
	if !live[title] {
		return Layout{}, fmt.Errorf("%q is not a column of %s", title, kind)
	}
	if !live[target] {
		return Layout{}, fmt.Errorf("%q is not a column of %s", target, kind)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Reconciled first, so the order edited is the one on screen, new columns
	// included.
	current := s.reconcile(kind, titles)
	order := make([]string, 0, len(current.Columns))
	for _, c := range current.Columns {
		order = append(order, c.Title)
	}

	if title != target {
		// RemoveOne and not Remove: with two columns of one title (a CRD's
		// printer columns need not be unique), Remove would take both out and
		// lose one. With duplicates, which of the two moves is ambiguous; what
		// this guarantees is that neither is lost.
		order = statefile.RemoveOne(order, title)
		at := statefile.Index(order, target)
		if at < 0 {
			// Unreachable: target is live and only title was taken out.
			at = len(order)
		} else if after {
			at++
		}
		order = statefile.Insert(order, at, title)
	}

	entry := s.entry(kind)
	// The ghosts the file already knew are kept at the end, so arranging a
	// CRD's table on one cluster does not forget another cluster's columns.
	entry.order = append(order, held(entry.order, live)...)
	s.forget(kind)
	err := s.save()
	return s.reconcile(kind, titles), err
}

// SetVisible shows or hides one column.
//
// It refuses, with a sentence, to hide the descriptor's first column (the
// object's name, which carries the row's link and selection, wherever it has
// been dragged) and the last visible column (an empty table reads as a cluster
// that answered nothing).
func (s *Store) SetVisible(kind, title string, visible, defaultHidden bool, titles []string) (Layout, error) {
	if s == nil {
		return Layout{}, nil
	}
	if kind == "" || title == "" {
		return Layout{}, fmt.Errorf("which column, on which kind?")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !visible {
		if len(titles) > 0 && titles[0] == title {
			return s.reconcile(kind, titles), fmt.Errorf(
				"%s is what says which object each row is — it carries the link to the object, "+
					"the checkbox that selects it, and the way back from anything you do here. "+
					"It cannot be hidden", title)
		}
		if s.visibleCount(kind, titles) <= 1 {
			return s.reconcile(kind, titles), fmt.Errorf(
				"%s is the only column left; a table with none is a heading over nothing", title)
		}
	}

	entry := s.entry(kind)
	if entry.hidden == nil {
		entry.hidden = map[string]bool{}
	}
	// Recorded only when it differs from the column's default and deleted when
	// it returns to it, so showing a secondary column survives a restart and
	// undoing a change leaves the file as it was.
	if visible == defaultHidden {
		entry.hidden[title] = !visible
	} else {
		delete(entry.hidden, title)
	}
	s.forget(kind)
	err := s.save()
	return s.reconcile(kind, titles), err
}

// visibleCount is how many of these titles are still drawn. The caller holds the
// lock.
func (s *Store) visibleCount(kind string, titles []string) int {
	stored := s.kinds[kind]
	if stored == nil {
		return len(titles)
	}
	n := 0
	for _, title := range titles {
		if !stored.hidden[title] {
			n++
		}
	}
	return n
}

// Scope names which of a kind's three arrangements a reset forgets.
//
// Three, because three gestures make them (a drag on a grip, a drag on a
// heading, a tick), and resetting the widths should not cost the order and the
// hidden columns. ScopeWidths matters most: no drag can produce the width 0
// that means "no opinion", so it is the only way back.
type Scope string

const (
	// ScopeAll is everything this kind holds; the API's default.
	ScopeAll Scope = "all"
	// ScopeWidths forgets how wide, keeping the order and what is hidden.
	ScopeWidths Scope = "widths"
	// ScopeOrder forgets the column order, keeping the widths and what is hidden.
	ScopeOrder Scope = "order"
	// ScopeHidden puts every column back on the table, keeping the widths and the
	// order.
	ScopeHidden Scope = "hidden"
)

// Valid says whether this is one of the four.
//
// The empty string is not valid: the API maps a missing scope to ScopeAll at
// its boundary, and defaulting here too would turn a misspelt scope into the
// most destructive reset. A typo resets nothing and says so.
func (s Scope) Valid() bool {
	switch s {
	case ScopeAll, ScopeWidths, ScopeOrder, ScopeHidden:
		return true
	}
	return false
}

// Reset throws part or all of one kind's arrangement away (see Scope).
//
// It returns the layout as it now stands and, when the file could not be
// written, an error wrapping statefile.ErrNotSaved: the reset holds until the
// next start. A scope that changes nothing writes nothing.
func (s *Store) Reset(kind string, scope Scope, titles []string) (Layout, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !scope.Valid() {
		return s.reconcile(kind, titles), fmt.Errorf(
			"%q is not something to reset; it is one of %s, %s, %s or %s",
			string(scope), ScopeAll, ScopeWidths, ScopeOrder, ScopeHidden)
	}

	found, ok := s.kinds[kind]
	if !ok {
		return s.reconcile(kind, titles), nil
	}

	changed := false
	switch scope {
	case ScopeAll:
		changed = !found.empty()
		if changed {
			delete(s.kinds, kind)
		}
	case ScopeWidths:
		changed = len(found.widths) > 0
		found.widths = nil
	case ScopeOrder:
		changed = len(found.order) > 0
		found.order = nil
	case ScopeHidden:
		changed = len(found.hidden) > 0
		found.hidden = nil
	}
	if !changed {
		return s.reconcile(kind, titles), nil
	}
	s.forget(kind)
	err := s.save()
	return s.reconcile(kind, titles), err
}

// entry is one kind's arrangement, created empty if there is none. The caller
// holds the lock.
func (s *Store) entry(kind string) *arrangement {
	if s.kinds == nil {
		s.kinds = map[string]*arrangement{}
	}
	found, ok := s.kinds[kind]
	if !ok {
		found = &arrangement{widths: map[string]int{}}
		s.kinds[kind] = found
	}
	if found.widths == nil {
		found.widths = map[string]int{}
	}
	return found
}

// forget drops a kind that has come back to holding nothing, so an undone
// change leaves the file as it was and Layout.Custom false. The caller holds
// the lock.
func (s *Store) forget(kind string) {
	found, ok := s.kinds[kind]
	if !ok {
		return
	}
	if found.empty() {
		delete(s.kinds, kind)
	}
}

// reconcile is the one place the stored names and the live columns are put
// together. The caller holds the lock; this only reads.
//
// For each stored title it takes the next live column not yet taken, which
// keeps duplicate titles apart: matching by title alone would draw the first
// twice and drop the second.
func (s *Store) reconcile(kind string, titles []string) Layout {
	layout := Layout{Kind: kind}
	stored := s.kinds[kind]
	layout.Custom = !stored.empty()

	taken := make([]bool, len(titles))
	byTitle := map[string][]int{}
	for i, title := range titles {
		byTitle[title] = append(byTitle[title], i)
	}
	// next hands out the columns with a given title in the order they appear.
	next := func(title string) int {
		for _, i := range byTitle[title] {
			if !taken[i] {
				return i
			}
		}
		return -1
	}

	appendColumn := func(at int) {
		taken[at] = true
		column := Column{Title: titles[at], At: at}
		if stored != nil {
			// Clamped again, so a caller can use it in a stylesheet as is.
			column.Width = clampWidth(stored.widths[titles[at]])
			column.Hidden, column.Decided = stored.hidden[titles[at]]
		}
		layout.Columns = append(layout.Columns, column)
	}

	if stored != nil {
		for _, title := range stored.order {
			if at := next(title); at >= 0 {
				appendColumn(at)
			}
			// Else a column this table does not have: skipped (see Arrangement).
		}
	}
	// Everything the file has never heard of goes at the end, in declared
	// order: its index means nothing in a rearranged list.
	for i := range titles {
		if !taken[i] {
			appendColumn(i)
		}
	}
	return layout
}

// save encodes and writes the file through statefile.Write. It logs a failure
// once and returns it, so the operator is told the change will not survive a
// restart. The caller holds the lock.
func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	// Both flags are always written true: what this build saves has been
	// through the load above, which makes that drop happen once.
	file := persisted{
		Kinds:               map[string]persistedKind{},
		Density:             s.density,
		WidthsAreDeliberate: true,
		// An older build ignores a field it does not know.
		WidthsAreShares: true,
	}
	for kind, entry := range s.kinds {
		stored := persistedKind{Order: entry.order}
		if len(entry.widths) > 0 {
			stored.Widths = entry.widths
		}
		for title, hidden := range entry.hidden {
			if hidden {
				stored.Hidden = append(stored.Hidden, title)
			} else {
				stored.Shown = append(stored.Shown, title)
			}
		}
		// Sorted, so the file does not churn with map order.
		sort.Strings(stored.Hidden)
		sort.Strings(stored.Shown)
		file.Kinds[kind] = stored
	}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		// Unreachable for today's persisted, but reported rather than ignored.
		err = fmt.Errorf("%w: the arrangement could not be encoded (%v)", statefile.ErrNotSaved, err)
		s.say.Once("columns: %v", err)
		return err
	}
	if err := statefile.Write(s.path, raw); err != nil {
		s.say.Once("columns: %v", err)
		return err
	}
	s.say.Clear()
	return nil
}

// Kinds is the kinds that have an arrangement, sorted.
// Tests use it to check that a reset emptied the file.
func (s *Store) Kinds() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.kinds))
	for kind := range s.kinds {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

// clampWidth holds a width to the bounds, and passes zero through as zero:
// "nobody has said" is not a narrow column.
func clampWidth(width int) int {
	switch {
	case width <= 0:
		return 0
	case width < MinWidth:
		return MinWidth
	case width > MaxWidth:
		return MaxWidth
	}
	return width
}

func set(titles []string) map[string]bool {
	out := make(map[string]bool, len(titles))
	for _, title := range titles {
		out[title] = true
	}
	return out
}

// held is the stored titles that are not columns of this table: another
// cluster's copy of the CRD has them, or an older release did.
func held(order []string, live map[string]bool) []string {
	var out []string
	for _, title := range order {
		if !live[title] {
			out = append(out, title)
		}
	}
	return out
}

// Compact is the tighter of the two row heights.
const Compact = "compact"

// Density reports how tightly tables are drawn: "" or Compact.
func (s *Store) Density() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.density
}

// SetDensity records a choice and returns what was stored, and any failure to
// write it, which wraps statefile.ErrNotSaved. Anything but Compact is stored
// as the default rather than refused, so the error is never a refusal.
func (s *Store) SetDensity(density string) (string, error) {
	if s == nil {
		return "", nil
	}
	if density != Compact {
		density = ""
	}
	// Held across the write: save() requires it.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.density = density
	return density, s.save()
}

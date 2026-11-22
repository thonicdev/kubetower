// Package hotbars is the arrangement of the cluster rail: which hotbar each
// context sits on, in what order, and which hotbar is on screen — plus the small
// JSON file that remembers all three across a restart.
//
// It is the operator's preference, kept apart from `internal/web` and shaped
// like `internal/columns`, the tables' equivalent. Nothing here touches a
// cluster or the kubeconfig, which is read-only: this package only says where
// each context is drawn.
package hotbars

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/thonicdev/kubetower/internal/statefile"
)

// Count is how many hotbars there are, numbered 1 to 3 on screen. Fixed, so the
// switcher stays a row of digits small enough for the rail.
const Count = 3

// FileName is the arrangement, inside the state directory. The directory, the
// write and the once-only logging are `internal/statefile`'s.
const FileName = "hotbars.json"

// Layout is the arrangement reconciled against the contexts that exist now:
// every name in it is a known context, and every known context appears once,
// on a bar or in Hidden (both, in the one case described under Hidden).
type Layout struct {
	// Active is the hotbar on screen, 1-based.
	Active int
	// Bars[0] is hotbar 1. Always Count long; a bar may be empty. Hidden
	// contexts are not in it.
	Bars [][]string
	// Hidden are the contexts the operator has taken off the rail. They stay in
	// the kubeconfig and reachable by URL; they are not drawn.
	//
	// One context can be in Hidden and on a bar at once: the cluster whose page
	// is open, put back by Showing.
	Hidden []string
}

// Current is the contexts on the hotbar that is on screen.
func (l Layout) Current() []string {
	if l.Active < 1 || l.Active > len(l.Bars) {
		return nil
	}
	return l.Bars[l.Active-1]
}

// BarOf is the hotbar a context sits on, 1-based, or 0 if it sits on none —
// which means the context is hidden, or is not in the kubeconfig.
func (l Layout) BarOf(context string) int {
	for i, bar := range l.Bars {
		for _, name := range bar {
			if name == context {
				return i + 1
			}
		}
	}
	return 0
}

// IsHidden says whether a context is one the operator has taken off the rail.
// The tile put back by Showing is hidden and drawn at once, and the rail marks
// it as hidden.
func (l Layout) IsHidden(context string) bool {
	return statefile.Contains(l.Hidden, context)
}

// Store is the arrangement and the file under it.
// Everything goes through the mutex, the file write included, so two
// concurrent writes cannot interleave into a file that describes neither.
type Store struct {
	// path is empty when there is nowhere to persist to; the arrangement then
	// lives as long as the process.
	path string
	// say logs a standing write failure once rather than per click.
	say *statefile.Complainer

	mu   sync.RWMutex
	bars [Count][]string
	// hidden is the contexts taken off the rail, a slice so the file keeps a
	// stable order.
	//
	// A hidden context keeps its entry in bars, so revealing it puts it back on
	// its hotbar. Its place within that bar lasts until the next arranging
	// action, which (commit) puts everything held back at the end.
	hidden []string
	active int
}

// Dir is where the arrangement is kept: KT_STATE_DIR, or a per-user path when
// it is not set. A container needs a volume there, or the arrangement dies
// with the container.
func Dir() string { return statefile.Dir() }

// New opens the store, reading the arrangement if there is one.
// A file that cannot be read is logged, and the default arrangement used.
func New(dir string, logf func(string, ...any)) *Store {
	s := &Store{say: statefile.NewComplainer(logf), active: 1}
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
	Active int             `json:"active"`
	Bars   [Count][]string `json:"bars"`
	// Hidden absent means nothing is hidden.
	Hidden []string `json:"hidden,omitempty"`
}

func (s *Store) load() {
	if s.path == "" {
		return
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			s.say.Log("hotbars: %s could not be read (%v); starting from the default arrangement", s.path, err)
		}
		return
	}
	var file persisted
	if err := json.Unmarshal(raw, &file); err != nil {
		s.say.Log("hotbars: %s is not readable JSON (%v); starting from the default arrangement", s.path, err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.bars = file.Bars
	s.hidden = file.Hidden
	s.active = clampBar(file.Active)
}

// Arrangement is the whole layout, reconciled against the contexts that exist.
// Reconciled on every read: a context the file has never heard of lands on
// hotbar 1 in kubeconfig order, and one the kubeconfig no longer has is left
// out.
func (s *Store) Arrangement(contexts []string) Layout {
	s.mu.RLock()
	defer s.mu.RUnlock()
	layout, _ := s.reconcile(contexts, "")
	return layout
}

// Showing is the arrangement as it must be drawn beside a page: Arrangement,
// except that the cluster the page is about is put back on its hotbar even when
// it is hidden.
// A hidden cluster is still reachable by URL, and a rail without it would
// misstate where the operator is, so it comes back in its place, marked hidden.
// Nothing is written: navigating is not a change of preference.
func (s *Store) Showing(current string, contexts []string) Layout {
	s.mu.RLock()
	defer s.mu.RUnlock()
	layout, _ := s.reconcile(contexts, current)
	return layout
}

// The arranging verbs (Show, Assign, Move, Place, Hide, Reveal) return two
// kinds of error, told apart with `errors.Is(err, statefile.ErrNotSaved)`:
//
//   - a refusal: a hotbar that does not exist, a context not in the
//     kubeconfig, a hidden tile dropped onto. Nothing changed, and the Layout
//     returned is the zero one.
//   - a failed write: the arrangement changed and is on screen, but will be
//     gone at the next start. The Layout returned is the new one.

// Show puts a hotbar on screen, and remembers it across a reload.
func (s *Store) Show(bar int, contexts []string) (Layout, error) {
	if bar < 1 || bar > Count {
		return Layout{}, fmt.Errorf("hotbar %d does not exist; there are %d", bar, Count)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	layout, held := s.reconcile(contexts, "")
	layout.Active = bar
	return s.commit(layout, held)
}

// Assign moves a context onto a hotbar, at the end of it.
// The hotbar on screen stays, so the operator keeps their place while
// arranging. A hidden context is revealed by being assigned.
func (s *Store) Assign(context string, bar int, contexts []string) (Layout, error) {
	if bar < 1 || bar > Count {
		return Layout{}, fmt.Errorf("hotbar %d does not exist; there are %d", bar, Count)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if !statefile.Contains(contexts, context) {
		return Layout{}, fmt.Errorf("%q is not a context in the kubeconfig", context)
	}
	s.hidden = statefile.Remove(s.hidden, context)

	layout, held := s.reconcile(contexts, "")
	for i := range layout.Bars {
		layout.Bars[i] = statefile.Remove(layout.Bars[i], context)
	}
	layout.Bars[bar-1] = append(layout.Bars[bar-1], context)
	return s.commit(layout, held)
}

// Move shifts a context one place within its own hotbar: delta is -1 for up and
// +1 for down. At either end it is a no-op rather than an error, so a stale
// request (a double click) just redraws the rail.
func (s *Store) Move(context string, delta int, contexts []string) (Layout, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !statefile.Contains(contexts, context) {
		return Layout{}, fmt.Errorf("%q is not a context in the kubeconfig", context)
	}
	layout, held := s.reconcile(contexts, "")
	bar := layout.BarOf(context)
	if bar == 0 {
		return Layout{}, fmt.Errorf("%q is hidden; reveal it before moving it", context)
	}
	tiles := layout.Bars[bar-1]
	at := statefile.Index(tiles, context)
	to := at + delta
	if to >= 0 && to < len(tiles) {
		tiles[at], tiles[to] = tiles[to], tiles[at]
	}
	return s.commit(layout, held)
}

// Place puts a context immediately before or after another one, on that other
// one's hotbar. It is what a drag ends in: the tile that was dropped on, and
// which side of it the pointer was.
// A neighbor rather than an index, because an index is a fact about the list
// the browser last saw, which may be stale; the server resolves a neighbor
// against what it holds. Dropping onto another hotbar moves the context there,
// and a hidden context is revealed by being placed.
func (s *Store) Place(context, target string, after bool, contexts []string) (Layout, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !statefile.Contains(contexts, context) {
		return Layout{}, fmt.Errorf("%q is not a context in the kubeconfig", context)
	}
	if !statefile.Contains(contexts, target) {
		return Layout{}, fmt.Errorf("%q is not a context in the kubeconfig", target)
	}
	// Every refusal comes before any change, so a refused drop leaves memory
	// and file agreeing.
	if context != target && statefile.Contains(s.hidden, target) {
		return Layout{}, fmt.Errorf("%q is hidden; it cannot be dropped onto", target)
	}
	s.hidden = statefile.Remove(s.hidden, context)

	layout, held := s.reconcile(contexts, "")
	bar := layout.BarOf(target)
	if bar == 0 {
		// Unreachable: a live, unhidden target is on a bar.
		return Layout{}, fmt.Errorf("%q is on no hotbar", target)
	}
	// Dropping a tile on itself is a no-op: a drag that changed its mind.
	if context == target {
		return s.commit(layout, held)
	}

	for i := range layout.Bars {
		layout.Bars[i] = statefile.Remove(layout.Bars[i], context)
	}
	tiles := layout.Bars[bar-1]
	at := statefile.Index(tiles, target)
	if at < 0 {
		// Unreachable: only context was taken out of this bar.
		at = len(tiles)
	} else if after {
		at++
	}
	layout.Bars[bar-1] = statefile.Insert(tiles, at, context)
	return s.commit(layout, held)
}

// Hide takes a context off the rail. It stays in the kubeconfig, reachable by
// URL, and keeps its place on its hotbar for when it comes back.
func (s *Store) Hide(context string, contexts []string) (Layout, error) {
	return s.setHidden(context, true, contexts)
}

// Reveal puts a hidden context back on the rail, on the hotbar it was on.
func (s *Store) Reveal(context string, contexts []string) (Layout, error) {
	return s.setHidden(context, false, contexts)
}

func (s *Store) setHidden(context string, hide bool, contexts []string) (Layout, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !statefile.Contains(contexts, context) {
		return Layout{}, fmt.Errorf("%q is not a context in the kubeconfig", context)
	}
	// bars is left alone, so revealing puts a tile back where it was.
	var err error
	if was := statefile.Contains(s.hidden, context); was != hide {
		if hide {
			s.hidden = append(s.hidden, context)
		} else {
			s.hidden = statefile.Remove(s.hidden, context)
		}
		err = s.save()
	}
	layout, _ := s.reconcile(contexts, "")
	// The layout goes back even on a failed write: the change holds in memory.
	return layout, err
}

// reconcile is the one place the stored names and the live contexts are put
// together. It returns the layout to draw, and per bar the names that are stored
// on that bar but are not drawn on it: the ghosts, and the hidden ones.
//
// Ghosts, stored names that are not contexts any more, are kept: a context
// missing from one reading of the kubeconfig (a file being rewritten) keeps
// its place when it comes back. A hidden context is held back likewise.
//
// `showing` is one context drawn even if hidden, still listed in
// Layout.Hidden. The caller holds the lock; this only reads.
func (s *Store) reconcile(contexts []string, showing string) (Layout, [Count][]string) {
	known := make(map[string]bool, len(contexts))
	for _, name := range contexts {
		known[name] = true
	}
	hidden := make(map[string]bool, len(s.hidden))
	for _, name := range s.hidden {
		hidden[name] = true
	}

	layout := Layout{Active: clampBar(s.active), Bars: make([][]string, Count)}
	var held [Count][]string
	seen := make(map[string]bool, len(contexts))

	// draw puts a live context on its bar, or holds it back as hidden; the one
	// the page is about is both.
	draw := func(i int, name string) {
		if !hidden[name] {
			layout.Bars[i] = append(layout.Bars[i], name)
			return
		}
		layout.Hidden = append(layout.Hidden, name)
		if name == showing {
			layout.Bars[i] = append(layout.Bars[i], name)
			return
		}
		held[i] = append(held[i], name)
	}

	for i, bar := range s.bars {
		for _, name := range bar {
			switch {
			case seen[name]:
				// A duplicate, from a hand-edited file: the first placement wins.
			case known[name]:
				draw(i, name)
				seen[name] = true
			default:
				held[i] = append(held[i], name)
				seen[name] = true
			}
		}
	}
	// Everything the file has never heard of goes on hotbar 1, in kubeconfig
	// order, unless it is hidden.
	for _, name := range contexts {
		if !seen[name] {
			draw(0, name)
			seen[name] = true
		}
	}
	return layout, held
}

// commit takes the layout as the new truth, puts the names held back at the end
// of their bars so they are not forgotten, and writes the file. The caller holds
// the lock.
//
// It takes only a layout from reconcile(contexts, ""): one drawn for a page
// carries the hidden tile it put back, which would be filed twice.
//
// The new layout is returned even when the write failed, with the error: what
// is on screen and what survives a restart are separate facts.
func (s *Store) commit(layout Layout, held [Count][]string) (Layout, error) {
	for i := range s.bars {
		bar := append([]string(nil), layout.Bars[i]...)
		s.bars[i] = append(bar, held[i]...)
	}
	s.active = clampBar(layout.Active)
	layout.Active = s.active
	return layout, s.save()
}

// save encodes and writes the file through statefile.Write. It logs a
// failure once and returns it, so the handler can say so on screen.
func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(persisted{Active: s.active, Bars: s.bars, Hidden: s.hidden}, "", "  ")
	if err != nil {
		// Unreachable for today's persisted, but reported rather than ignored.
		err = fmt.Errorf("%w: the arrangement could not be encoded (%v)", statefile.ErrNotSaved, err)
		s.say.Once("hotbars: %v", err)
		return err
	}
	if err := statefile.Write(s.path, raw); err != nil {
		s.say.Once("hotbars: %v", err)
		return err
	}
	s.say.Clear()
	return nil
}

func clampBar(bar int) int {
	if bar < 1 || bar > Count {
		return 1
	}
	return bar
}

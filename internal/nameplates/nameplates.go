// Package nameplates is what a cluster is called and what color it is on the
// rail, in KubeTower and nowhere else.
//
// A context is named by whoever wrote the kubeconfig, on EKS an ARN nobody
// chose, and several can differ only by a suffix. A nameplate is the
// operator's own word for one, plus a color off a fixed palette, so the tile
// can be told apart at a glance. Like the other layout stores it is one small
// JSON file in the state directory, written atomically, and a broken state
// directory costs a log line rather than a refusal to draw.
//
// **A nameplate is a label and never an identity.** The map is keyed by the
// context name as the kubeconfig spells it (resolved by the caller), and no
// function here takes a display name and returns a context: naming a cluster
// "prod" must not make a command run on prod. The display name reaches no URL,
// no client, no kubeconfig and no tool call.
//
// The kubeconfig is never written: renaming a cluster here leaves every
// `kubectl --context` working unchanged.
package nameplates

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/thonicdev/kubetower/internal/statefile"
)

// FileName is the nameplates, inside the state directory.
//
// The write and the once-only complaining are `internal/statefile`, shared with
// the other stores; what stays here is the palette and clean().
const FileName = "nameplates.json"

// The bounds a nameplate is held to, in runes.
//
// MaxName fits the rail twice over; the tile label is middle-truncated anyway.
// The bounds are applied here and not only in the browser, because a
// `maxlength` constrains nothing that arrives, and a hand-edited file is input
// too.
//
// MaxInitials is three because more characters on the avatar are unreadable.
//
// MaxIcon is in bytes, and generous (a recipe the console writes is well under
// 300): a bound on what an untrusted file or request can make every page carry.
const (
	MaxName     = 32
	MaxInitials = 3
	MaxIcon     = 1024
)

// Color is one swatch on the palette.
//
// The stored value is the palette key, never operator text, so nothing typed
// into a form can reach a style.
type Colour struct {
	// Key is what is stored and what a form posts. Never rendered into CSS.
	Key string
	// Label is what the swatch says to a screen reader and to a tooltip.
	Label string
	// Hue in degrees, 0–359.
	Hue int
	// Pale picks the light half of the pair, which also decides whether the
	// initials on it are drawn dark or light.
	Pale bool
}

// palette is eight hues, 45 degrees apart, in a deep and a pale version each.
//
// The automatic coloring (AutoColour) draws from the same sixteen. A picker
// with more swatches than a person can hold in mind is chosen by where the
// pointer lands.
var palette = []Colour{
	{Key: "red", Label: "Red", Hue: 0},
	{Key: "amber", Label: "Amber", Hue: 45},
	{Key: "lime", Label: "Lime", Hue: 90},
	{Key: "green", Label: "Green", Hue: 135},
	{Key: "teal", Label: "Teal", Hue: 180},
	{Key: "blue", Label: "Blue", Hue: 225},
	{Key: "violet", Label: "Violet", Hue: 270},
	{Key: "pink", Label: "Pink", Hue: 315},
	{Key: "red-pale", Label: "Pale red", Hue: 0, Pale: true},
	{Key: "amber-pale", Label: "Pale amber", Hue: 45, Pale: true},
	{Key: "lime-pale", Label: "Pale lime", Hue: 90, Pale: true},
	{Key: "green-pale", Label: "Pale green", Hue: 135, Pale: true},
	{Key: "teal-pale", Label: "Pale teal", Hue: 180, Pale: true},
	{Key: "blue-pale", Label: "Pale blue", Hue: 225, Pale: true},
	{Key: "violet-pale", Label: "Pale violet", Hue: 270, Pale: true},
	{Key: "pink-pale", Label: "Pale pink", Hue: 315, Pale: true},
}

// Palette is the swatches, in the order they are drawn. A copy, so a caller
// building a view model cannot reorder the one every other caller reads.
func Palette() []Colour {
	return append([]Colour(nil), palette...)
}

// ColourOf is the swatch a stored key names, and whether there is one.
//
// The only way a key becomes a color. Set refuses a key that is not on the
// palette, so a hand-written POST cannot get text where a hue belongs.
func ColourOf(key string) (Colour, bool) {
	for _, c := range palette {
		if c.Key == key {
			return c, true
		}
	}
	return Colour{}, false
}

// AutoColour is the palette key a context is drawn in when nobody chose one.
//
// **Always a key on the palette**, so the client holds one token per key and
// needs no arithmetic, and a cluster later given a color stays in one set.
//
// FNV-1a of the context name modulo the palette: stable across restarts and
// machines. Two contexts can share a swatch; a nameplate separates them.
func AutoColour(context string) string {
	sum := fnv.New32a()
	_, _ = sum.Write([]byte(context)) // a hash.Hash never returns an error
	return palette[sum.Sum32()%uint32(len(palette))].Key
}

// Nameplate is one cluster's appearance, as the operator set it. Every field is
// optional, and empty means "work it out from the context name".
type Nameplate struct {
	// Name is what the tile, the breadcrumb and the page title say. A label:
	// never resolved, never compared against a context, never put in a URL.
	Name string `json:"name,omitempty"`
	// Initials are the characters on the avatar. Separate from Name because
	// the derivation right for `SHOP-EKS-1` ("P1") is wrong for a chosen name.
	Initials string `json:"initials,omitempty"`
	// Color is a key on the palette, or "" for the automatic color, which is
	// AutoColour of the context name - also a key on the palette.
	Colour string `json:"colour,omitempty"`
	// Icon is the tile's icon recipe, or "" for none, which draws the default
	// icon on the color.
	//
	// A JSON object the browser writes and reads, never rendered SVG. The
	// server holds it to "one JSON object, at most MaxIcon bytes" and does not
	// interpret it; a recipe the front end cannot read is drawn as the default.
	Icon string `json:"icon,omitempty"`
}

// IsZero says this cluster has no nameplate: nothing has been chosen, and
// everything about the tile is derived.
func (n Nameplate) IsZero() bool {
	return n.Name == "" && n.Initials == "" && n.Colour == "" && n.Icon == ""
}

// Store is the nameplates and the file under them.
//
// Read on every page and written from one handler. The file write is done
// under the mutex too, so two saves cannot interleave into a file that
// describes neither.
type Store struct {
	// path is empty when there is nowhere to persist to: the nameplates then
	// live as long as the process.
	path string
	// say is the log, and the once-only half of it. See statefile.Complainer.
	say *statefile.Complainer

	mu sync.RWMutex
	// plates is keyed by the context name as the kubeconfig spells it. Nothing
	// here has an order; the rail's order is decided elsewhere.
	plates map[string]Nameplate
}

// Dir is where the nameplates are kept: KT_STATE_DIR, or a per-user path when it
// is not set. In a container it must be a volume, or the file goes with the
// container.
func Dir() string { return statefile.Dir() }

// New opens the store, reading the nameplates if there are any.
//
// A file that cannot be read is not fatal: the console starts with the context
// names and says so in the log.
func New(dir string, logf func(string, ...any)) *Store {
	s := &Store{say: statefile.NewComplainer(logf), plates: map[string]Nameplate{}}
	if dir = strings.TrimSpace(dir); dir != "" {
		s.path = filepath.Join(dir, FileName)
	}
	s.load()
	return s
}

// Path is the file the nameplates are written to, or "" when they are only in
// memory. Reported at startup.
func (s *Store) Path() string { return s.path }

type persisted struct {
	// Clusters keyed by context name. Under a key so a later per-cluster
	// preference has somewhere to go that does not read as a context name.
	Clusters map[string]Nameplate `json:"clusters,omitempty"`
}

func (s *Store) load() {
	if s.path == "" {
		return
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			s.say.Log("nameplates: %s could not be read (%v); every cluster keeps its context name", s.path, err)
		}
		return
	}
	var file persisted
	if err := json.Unmarshal(raw, &file); err != nil {
		s.say.Log("nameplates: %s is not readable JSON (%v); every cluster keeps its context name", s.path, err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for context, plate := range file.Clusters {
		// Cleaned on the way in too: the file can be hand-edited, and a control
		// character or a right-to-left override would be drawn beside a real
		// context name on every page.
		plate, err := clean(plate)
		if err != nil {
			s.say.Log("nameplates: the entry for %s is not usable (%v); that cluster keeps its context name", context, err)
			continue
		}
		if plate.IsZero() {
			continue
		}
		s.plates[context] = plate
	}
}

// Get is one cluster's nameplate, or the zero one when it has none.
//
// The argument is a context name as the kubeconfig spells it. Resolving is the
// caller's: a store that resolved could be asked about a display name.
func (s *Store) Get(context string) Nameplate {
	// A nil store answers "no nameplate": this is read on every render, and a
	// panic in the paint must not take a page down. Set has no such guard,
	// because writing nowhere is a bug.
	if s == nil {
		return Nameplate{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.plates[context]
}

// All is every nameplate, by context name. A copy, and it exists for the
// settings-shaped question "what have I named things", not for lookups.
func (s *Store) All() map[string]Nameplate {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Nameplate, len(s.plates))
	for context, plate := range s.plates {
		out[context] = plate
	}
	return out
}

// Set gives a cluster a nameplate, and clears it when the nameplate is empty —
// which is how "back to the context name" is expressed.
//
// `contexts` is the kubeconfig's own list: a preference about a cluster that
// does not exist is one nobody can see or remove.
//
// The name and the initials are clamped rather than refused, since an
// over-long name is a typing accident. A color off the palette is an error:
// only something other than the form produces one.
func (s *Store) Set(context string, plate Nameplate, contexts []string) (Nameplate, error) {
	if !statefile.Contains(contexts, context) {
		return Nameplate{}, fmt.Errorf("%q is not a context in the kubeconfig", context)
	}
	plate, err := clean(plate)
	if err != nil {
		return Nameplate{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	was, had := s.plates[context]
	switch {
	case plate.IsZero():
		if !had {
			return Nameplate{}, nil
		}
		delete(s.plates, context)
	case was == plate:
		// Nothing changed, so nothing is written.
		return plate, nil
	default:
		s.plates[context] = plate
	}
	// The nameplate is returned whatever happened to the file: it is on the
	// rail now, and the error (statefile.ErrNotSaved) says only that it will
	// not survive a restart. A refusal above changed nothing.
	return plate, s.save()
}

// clean is the one place a nameplate is made safe, and it runs on everything
// from outside: a form, and the file, which is also outside.
//
// Control and format characters are dropped rather than refused, since a name
// pasted from elsewhere intends neither. A right-to-left override reverses the
// text after it, so a nameplate could read as another cluster beside the real
// context name. The cost: an emoji built with a zero-width joiner comes apart.
func clean(plate Nameplate) (Nameplate, error) {
	plate.Name = trimTo(oneLine(plate.Name), MaxName)
	// Initials have no room for whitespace.
	plate.Initials = trimTo(strings.Join(strings.Fields(oneLine(plate.Initials)), ""), MaxInitials)
	if plate.Colour != "" {
		if _, ok := ColourOf(plate.Colour); !ok {
			return Nameplate{}, fmt.Errorf("%q is not a color on the palette", plate.Colour)
		}
	}
	if plate.Icon != "" {
		if err := checkIcon(plate.Icon); err != nil {
			return Nameplate{}, err
		}
	}
	return plate, nil
}

// checkIcon holds an icon recipe to what the server promises about it: one JSON
// object, and small. Refused rather than cut: half a JSON object is not a
// recipe.
func checkIcon(icon string) error {
	if len(icon) > MaxIcon {
		return fmt.Errorf("an icon recipe is at most %d bytes, this one is %d", MaxIcon, len(icon))
	}
	var recipe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(icon), &recipe); err != nil || recipe == nil {
		return fmt.Errorf("an icon recipe is one JSON object")
	}
	return nil
}

func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		// Whitespace becomes a space (a tab included): dropping it would glue
		// two words together.
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// trimTo cuts to a number of runes and not of bytes, so no rune is split.
func trimTo(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return strings.TrimSpace(string(runes[:max]))
}

// save writes the file through statefile.Write. The caller holds the lock.
//
// The error is returned as well as logged: the handler is what can tell the
// operator who just renamed a cluster.
func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(persisted{Clusters: s.plates}, "", "  ")
	if err != nil {
		// Should not happen for this struct; reported rather than ignored.
		err = fmt.Errorf("%w: the nameplates could not be encoded (%v)", statefile.ErrNotSaved, err)
		s.say.Once("nameplates: %v", err)
		return err
	}
	if err := statefile.Write(s.path, raw); err != nil {
		s.say.Once("nameplates: %v", err)
		return err
	}
	s.say.Clear()
	return nil
}

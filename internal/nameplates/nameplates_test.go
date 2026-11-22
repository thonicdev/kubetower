package nameplates

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/statefile"
)

// arn is the shape of context name this whole package exists for: an EKS ARN
// nobody chose, long enough that fourteen of them down a 92-pixel rail are told
// apart by their suffix and nothing else.
//
// The account id is AWS's own documentation placeholder and the cluster name is
// `example-`, deliberately: docs/DEFINITION-OF-DONE.md §6.C says this
// repository is not a personal configuration, and a fixture carrying a real
// account number and a real cluster name is exactly the line that reads as
// universal on somebody else's laptop. Nothing here depends on the value — it
// is an opaque map key and a string the file is checked to contain.
const arn = "arn:aws:eks:eu-central-1:123456789012:cluster/example-prod-eks-1"

func contexts() []string {
	return []string{"mi-k3s", "docker-desktop", "SHOP-EKS-1", arn}
}

// A nameplate survives a restart, and the file it survives in is the one the
// state directory is for. The whole feature is "KubeTower remembers what I call
// this cluster"; a store that forgot on restart would be a tooltip.
func TestANameplateSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	store := New(dir, nil)

	if _, err := store.Set(arn, Nameplate{Name: "Prod Europe", Initials: "PE", Colour: "teal"}, contexts()); err != nil {
		t.Fatalf("Set: %v", err)
	}

	again := New(dir, nil)
	plate := again.Get(arn)
	if plate.Name != "Prod Europe" || plate.Initials != "PE" || plate.Colour != "teal" {
		t.Errorf("the nameplate did not survive: %+v", plate)
	}
	if again.Path() != filepath.Join(dir, FileName) {
		t.Errorf("Path is %q, want the file in the state directory", again.Path())
	}
	// The key on disk is the context name, spelled as the kubeconfig spells it.
	// Anything else and one cluster would collect a nameplate per spelling.
	raw, err := os.ReadFile(again.Path())
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if !strings.Contains(string(raw), arn) {
		t.Errorf("the file is not keyed on the context name:\n%s", raw)
	}
}

// An empty nameplate is how "back to the context name" is said. It removes the
// entry rather than storing three empty strings, so a cluster nobody has named
// leaves no trace in the file.
func TestAnEmptyNameplateClearsTheEntry(t *testing.T) {
	dir := t.TempDir()
	store := New(dir, nil)

	if _, err := store.Set("mi-k3s", Nameplate{Name: "Laptop"}, contexts()); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if store.Get("mi-k3s").IsZero() {
		t.Fatal("the nameplate was not stored")
	}
	if _, err := store.Set("mi-k3s", Nameplate{}, contexts()); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if !store.Get("mi-k3s").IsZero() {
		t.Errorf("the nameplate was not cleared: %+v", store.Get("mi-k3s"))
	}
	if raw, err := os.ReadFile(store.Path()); err != nil {
		t.Fatalf("%v", err)
	} else if strings.Contains(string(raw), "Laptop") {
		t.Errorf("the cleared name is still in the file:\n%s", raw)
	}
	if !New(dir, nil).Get("mi-k3s").IsZero() {
		t.Error("the cleared name came back after a restart")
	}
}

// A color is a key on a fixed palette, and everything else is refused. The
// point is not tidiness: this value is what a hue is looked up by, and the hue
// is formatted into a style attribute. Text must not be able to get that far.
func TestAColourMustBeOnThePalette(t *testing.T) {
	store := New("", nil)

	for _, bad := range []string{
		"chartreuse",
		"red; background:url(https://example.invalid/x)",
		"expression(alert(1))",
		"TEAL",
	} {
		if _, err := store.Set("mi-k3s", Nameplate{Colour: bad}, contexts()); err == nil {
			t.Errorf("%q was accepted as a color", bad)
		}
		if !store.Get("mi-k3s").IsZero() {
			t.Fatalf("%q was stored anyway: %+v", bad, store.Get("mi-k3s"))
		}
	}
	// And every key the palette offers is accepted, or the picker would draw
	// swatches that cannot be chosen.
	for _, colour := range Palette() {
		if _, err := store.Set("mi-k3s", Nameplate{Colour: colour.Key}, contexts()); err != nil {
			t.Errorf("the palette offers %q and the store refuses it: %v", colour.Key, err)
		}
		if got, ok := ColourOf(colour.Key); !ok || got.Hue != colour.Hue {
			t.Errorf("ColourOf(%q) = %+v, %v", colour.Key, got, ok)
		}
	}
	if _, ok := ColourOf(""); ok {
		t.Error(`ColourOf("") found a color; the empty key means "automatic" and is not on the palette`)
	}
}

// What arrives is text somebody typed or pasted. A newline, a tab and a
// right-to-left override are all dropped: the last one reverses everything after
// it, so a nameplate could otherwise be made to read as a cluster it is not
// while sitting next to the real context name.
func TestANameIsOneLineOfPlainText(t *testing.T) {
	store := New("", nil)

	plate, err := store.Set("mi-k3s", Nameplate{
		Name:     "  Prod‮\tEurope\nnorth  ",
		Initials: " pe ",
	}, contexts())
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if plate.Name != "Prod Europe north" {
		t.Errorf("Name = %q, want the control characters gone and the whitespace collapsed", plate.Name)
	}
	if plate.Initials != "pe" {
		t.Errorf("Initials = %q, want %q", plate.Initials, "pe")
	}
	if strings.ContainsRune(plate.Name, '‮') {
		t.Error("a right-to-left override survived into a name that is drawn beside a real context name")
	}
}

// Long is clamped, not refused: an over-long name is a typing accident, and
// throwing away what was typed teaches nobody anything. Clamped by runes,
// because half a rune is not a shorter name.
func TestALongNameIsClampedByRunes(t *testing.T) {
	store := New("", nil)

	plate, err := store.Set("mi-k3s", Nameplate{
		Name:     strings.Repeat("é", MaxName+40),
		Initials: "ABCDEFG",
	}, contexts())
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := len([]rune(plate.Name)); got != MaxName {
		t.Errorf("the name is %d runes, want %d", got, MaxName)
	}
	if plate.Name != strings.Repeat("é", MaxName) {
		t.Errorf("the name was cut mid-rune: %q", plate.Name)
	}
	if got := len([]rune(plate.Initials)); got != MaxInitials {
		t.Errorf("the initials are %d runes, want %d", got, MaxInitials)
	}
}

// A context that is not in the kubeconfig cannot be given a nameplate. The same
// bargain the hotbars are on: a preference about a cluster that does not exist
// is one nobody can see and nobody can remove.
func TestANameplateNeedsAContextThatExists(t *testing.T) {
	store := New("", nil)

	if _, err := store.Set("not-a-context", Nameplate{Name: "x"}, contexts()); err == nil {
		t.Error("a nameplate was accepted for a context that is not in the kubeconfig")
	}
	if !store.Get("not-a-context").IsZero() {
		t.Error("it was stored anyway")
	}
}

// The file is input too. A hand-edited one carrying a control character or a
// color that is not on the palette must not reach a page — the entry is
// cleaned, or dropped and said out loud, and the console still starts.
func TestAHandEditedFileIsCleanedOnTheWayIn(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(`{
  "clusters": {
    "mi-k3s": {"name": "Home‮Lab", "colour": "teal"},
    "SHOP-EKS-1": {"name": "Prod", "colour": "red; background:url(x)"}
  }
}`), 0o600); err != nil {
		t.Fatalf("%v", err)
	}
	var said []string
	store := New(dir, func(format string, args ...any) { said = append(said, format) })

	if got := store.Get("mi-k3s").Name; got != "HomeLab" {
		t.Errorf("mi-k3s is called %q; the override was not stripped on the way in", got)
	}
	if !store.Get("SHOP-EKS-1").IsZero() {
		t.Errorf("an entry with an off-palette color was kept: %+v", store.Get("SHOP-EKS-1"))
	}
	if len(said) != 1 {
		t.Errorf("the dropped entry cost %d log lines, want 1: %v", len(said), said)
	}
}

// A state directory that cannot be written is one log line and a rail that still
// works. The same bargain as the hotbars: a console that will not draw because
// it could not save a preference is worse than one that forgets it.
//
// And one error as well as one log line: Set returning nil over an unwritable
// directory would mean the tile takes the new name, the rail redraws, and the
// name is gone at the next start with nothing on screen having said so.
func TestABrokenStateDirectoryIsOneLineAndAnError(t *testing.T) {
	// A file where the directory should be: MkdirAll fails on every platform.
	dir := filepath.Join(t.TempDir(), "wall")
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("%v", err)
	}
	var lines int
	store := New(filepath.Join(dir, "state"), func(string, ...any) { lines++ })

	for i := 0; i < 5; i++ {
		saved, err := store.Set("mi-k3s", Nameplate{Name: strings.Repeat("x", i+1)}, contexts())
		if err == nil {
			t.Fatal("the nameplate could not be written and Set said nothing. The operator " +
				"renames a cluster, watches the rail redraw, and finds the context name back " +
				"at the next restart")
		}
		if !errors.Is(err, statefile.ErrNotSaved) {
			t.Fatalf("the error does not wrap statefile.ErrNotSaved, so a handler cannot tell "+
				"it from the one refusal this has — a color that is not on the palette, "+
				"where nothing changed at all: %v", err)
		}
		// The nameplate comes back applied, beside the error.
		if saved.Name != strings.Repeat("x", i+1) {
			t.Errorf("the nameplate returned with the error does not carry the change: %+v", saved)
		}
	}
	if store.Get("mi-k3s").Name != "xxxxx" {
		t.Errorf("the nameplate did not take effect in memory: %+v", store.Get("mi-k3s"))
	}

	// The property is "not a line per save", not an exact count — and the
	// difference is a platform difference, which is why this is worth spelling
	// out. Opening a path whose parent is a *file* fails at startup on Linux
	// (ENOTDIR) and is indistinguishable from "no file yet" on Windows, so the
	// same broken directory costs two lines on one and one on the other, so
	// the bound is 2, not 1.
	after := lines
	if after > 2 {
		t.Errorf("a broken state directory cost %d log lines before it was even used", after)
	}
	// A different name every time, so every one of them actually reaches save():
	// setting the same nameplate twice returns early without writing, and a loop
	// of twenty identical Sets would prove nothing about the log at all.
	for i := 0; i < 20; i++ {
		if _, err := store.Set("mi-k3s", Nameplate{Name: fmt.Sprintf("y%d", i)}, contexts()); err == nil {
			t.Fatalf("a later write to the same broken directory reported success")
		}
	}
	if lines != after {
		t.Errorf("twenty more saves added %d log lines. A directory that cannot be written is "+
			"a standing condition, and one line per attempt buries everything else in the log",
			lines-after)
	}
}

// Nothing here maps a display name back to a cluster, and this test is the
// statement of it. A store that could be asked "which cluster is called prod"
// would make a label into an identity, which is the failure that turns a
// cosmetic feature into a command running somewhere nobody meant.
func TestADisplayNameIsNeverAKey(t *testing.T) {
	store := New("", nil)
	if _, err := store.Set(arn, Nameplate{Name: "docker-desktop"}, contexts()); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// The ARN wears another cluster's context name as its label. That must
	// change nothing about either cluster.
	if !store.Get("docker-desktop").IsZero() {
		t.Errorf("naming one cluster after another gave the second a nameplate: %+v", store.Get("docker-desktop"))
	}
	if store.Get("Prod Europe").Name != "" || store.Get("docker-desktop").Name != "" {
		t.Error("a display name resolved to a nameplate")
	}
	all := store.All()
	if _, ok := all["docker-desktop"]; ok {
		t.Errorf("the display name became a key: %v", all)
	}
	if all[arn].Name != "docker-desktop" {
		t.Errorf("the only key is not the context name: %v", all)
	}
	// All is a copy: a caller that mutates what it was handed must not be
	// mutating the store.
	all[arn] = Nameplate{Name: "tampered"}
	if store.Get(arn).Name != "docker-desktop" {
		t.Error("All handed out the store's own map")
	}
}

// An icon recipe is stored as the browser wrote it, survives
// a restart beside the color it falls back to, and is the only thing that can
// keep an entry alive on its own.
func TestAnIconRecipeIsStoredAndSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	store := New(dir, nil)
	recipe := `{"v":1,"style":"shapes","seed":"mi-k3s"}`

	if _, err := store.Set("mi-k3s", Nameplate{Colour: "teal", Icon: recipe}, contexts()); err != nil {
		t.Fatalf("Set: %v", err)
	}
	plate := New(dir, nil).Get("mi-k3s")
	if plate.Icon != recipe || plate.Colour != "teal" {
		t.Errorf("the icon did not survive a restart as written: %+v", plate)
	}

	if _, err := store.Set("docker-desktop", Nameplate{Icon: recipe}, contexts()); err != nil {
		t.Fatalf("an icon alone: %v", err)
	}
	if store.Get("docker-desktop").IsZero() {
		t.Error("a nameplate holding only an icon reads as empty, so it would be dropped")
	}
}

// Anything that is not one JSON object of at most MaxIcon bytes is refused, and
// nothing is stored: the recipe is opaque to the server, but its shape is not.
func TestAnIconRecipeMustBeOneSmallJSONObject(t *testing.T) {
	store := New("", nil)

	for _, bad := range []string{
		"shapes",
		`"shapes"`,
		`["shapes"]`,
		`null`,
		`{"style":"shapes"`,
		`{"style":"shapes"} {}`,
		`<svg xmlns="http://www.w3.org/2000/svg"/>`,
		`{"seed":"` + strings.Repeat("x", MaxIcon) + `"}`,
	} {
		if _, err := store.Set("mi-k3s", Nameplate{Icon: bad}, contexts()); err == nil {
			t.Errorf("%.40q was accepted as an icon recipe", bad)
		}
		if !store.Get("mi-k3s").IsZero() {
			t.Fatalf("%.40q was stored anyway: %+v", bad, store.Get("mi-k3s"))
		}
	}
}

// A reset icon is stored as a recipe object with no style: it survives a
// restart on its own, and an empty icon clears it.
func TestAResetIconMarkerSurvivesARestartAndClears(t *testing.T) {
	dir := t.TempDir()
	store := New(dir, nil)
	marker := `{"v":1,"reset":true}`

	if _, err := store.Set("mi-k3s", Nameplate{Icon: marker}, contexts()); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := New(dir, nil).Get("mi-k3s").Icon; got != marker {
		t.Errorf("the marker did not survive a restart: %q", got)
	}
	if _, err := store.Set("mi-k3s", Nameplate{Icon: ""}, contexts()); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if got := New(dir, nil).Get("mi-k3s").Icon; got != "" {
		t.Errorf("the marker was not cleared: %q", got)
	}
}

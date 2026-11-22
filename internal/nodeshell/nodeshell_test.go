package nodeshell

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/statefile"
)

// The setting survives a restart. That is the only reason this package writes a
// file at all, so it is the first thing asserted: a store that forgot on every
// restart would look identical from the settings page and be discovered the next
// morning, when the node shell that ran netshoot yesterday runs alpine today.
func TestTheImageIsRememberedAcrossAStore(t *testing.T) {
	dir := t.TempDir()

	first := New(dir, nil)
	if got := first.Image(); got != DefaultImage {
		t.Fatalf("a fresh store starts on %q, want the shipped default %q", got, DefaultImage)
	}
	if _, err := first.Set("ghcr.io/example/debug:1.4"); err != nil {
		t.Fatalf("setting the image: %v", err)
	}

	second := New(dir, nil)
	if got := second.Image(); got != "ghcr.io/example/debug:1.4" {
		t.Errorf("the next process reads %q; the setting did not survive", got)
	}
	if second.Path() != filepath.Join(dir, FileName) {
		t.Errorf("Path() = %q, and the startup log is what tells an operator where the setting "+
			"went when it resets", second.Path())
	}
}

// Emptying the field is how an operator goes back to the default, and it is what
// the settings page tells them to do. Storing the empty string instead would
// move the failure from a form to a pod spec on a production node.
func TestAnEmptyImageGoesBackToTheDefault(t *testing.T) {
	store := New(t.TempDir(), nil)
	if _, err := store.Set("busybox:1.37"); err != nil {
		t.Fatalf("setting: %v", err)
	}
	applied, err := store.Set("   ")
	if err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if applied.Image != DefaultImage {
		t.Errorf("clearing the field left %q rather than the default", applied.Image)
	}
}

// What Validate is for is the shapes that are certainly not an image and would
// otherwise reach the API server as an invalid pod spec — or worse, reach it as
// something that parses. Whether an image *exists* is the kubelet's answer, and
// it says so in the pod's own status where the operator can read it.
func TestValidateRefusesWhatIsNotAnImageReference(t *testing.T) {
	good := []string{
		"alpine:3.24",
		"busybox",
		"ghcr.io/example/debug:v1.4.0",
		"registry.example.com:5000/team/tools@sha256:" + strings.Repeat("a", 64),
	}
	for _, image := range good {
		if err := Validate(image); err != nil {
			t.Errorf("Validate(%q) refused a real reference: %v", image, err)
		}
	}

	bad := map[string]string{
		"":                        "empty",
		"   ":                     "blank",
		"alpine:3.24 && rm -rf /": "a shell fragment",
		"alpine 3.24":             "a space",
		"-alpine":                 "a leading dash, which a container runtime reads as a flag",
		"alpine\n:3.24":           "a newline",
		strings.Repeat("a", 400):  "far past any real reference",
	}
	for image, why := range bad {
		if err := Validate(image); err == nil {
			t.Errorf("Validate(%q) accepted %s", image, why)
		}
	}
}

// A refusal has to say what is wrong with the value, not that it is wrong. This
// sentence is rendered next to the field, and "invalid" over a value with a
// trailing space is a refusal the operator cannot act on.
func TestARefusalSaysWhatIsWrong(t *testing.T) {
	store := New(t.TempDir(), nil)
	_, err := store.Set("alpine 3.24")
	if err == nil {
		t.Fatal("a value with a space in it was accepted")
	}
	if !strings.Contains(err.Error(), "image reference") {
		t.Errorf("the refusal does not name what the field is for: %v", err)
	}
	// And the store is unchanged: a refused value must not half-apply.
	if store.Image() != DefaultImage {
		t.Errorf("a refused value was applied anyway: %q", store.Image())
	}
}

// A file edited by hand is input like anything else. A `nodeshell.json` naming
// something that is not an image reference would otherwise only be discovered as
// a rejected pod spec on a production node.
func TestAHandEditedFileIsValidatedOnTheWayIn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(`{"image":"alpine 3.24 ; curl evil"}`), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	var said []string
	store := New(dir, func(format string, v ...any) { said = append(said, format) })
	if store.Image() != DefaultImage {
		t.Errorf("a hand-edited file put %q into a pod spec", store.Image())
	}
	if len(said) == 0 {
		t.Error("it fell back to the default without saying so anywhere")
	}

	// Unreadable JSON is the same bargain, and must not stop the console.
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}
	if got := New(dir, nil).Image(); got != DefaultImage {
		t.Errorf("an unreadable file gave %q", got)
	}
}

// Nowhere to persist to is a supported state, not an error: the setting then
// lives as long as the process, which is better than a console that will not
// open a shell because it could not find a state directory.
func TestNoStateDirectoryStillWorks(t *testing.T) {
	store := New("", nil)
	if store.Path() != "" {
		t.Errorf("a store with no directory claims a path: %q", store.Path())
	}
	applied, err := store.Set("busybox:1.37")
	if err != nil {
		t.Fatalf("setting on an unpersisted store: %v", err)
	}
	if applied.Image != "busybox:1.37" || store.Image() != "busybox:1.37" {
		t.Errorf("the setting did not take in memory: %q", store.Image())
	}
}

// The deployment's own default, for an installation that arrives with the
// decision already made. A value that does not validate is ignored rather than
// stored: an environment variable is not a place an operator sees a refusal.
func TestTheEnvironmentCanSeedTheDefault(t *testing.T) {
	t.Setenv(EnvImage, "ghcr.io/example/netshoot:v0.13")
	if got := New(t.TempDir(), nil).Image(); got != "ghcr.io/example/netshoot:v0.13" {
		t.Errorf("the environment default was ignored: %q", got)
	}

	t.Setenv(EnvImage, "not an image")
	if got := New(t.TempDir(), nil).Image(); got != DefaultImage {
		t.Errorf("an unusable environment default reached a pod spec: %q", got)
	}

	// The stored value wins over it. An operator who changed it on the page
	// meant to, and a deployment variable must not undo that on every restart.
	dir := t.TempDir()
	t.Setenv(EnvImage, "ghcr.io/example/netshoot:v0.13")
	if _, err := New(dir, nil).Set("busybox:1.37"); err != nil {
		t.Fatalf("setting: %v", err)
	}
	if got := New(dir, nil).Image(); got != "busybox:1.37" {
		t.Errorf("the environment overrode what the operator chose: %q", got)
	}
}

// One state directory, the same one the hotbars, the column layouts and the
// archive use. Checked because "my node shell image reset" is otherwise
// diagnosed by guesswork, and the usual cause is a container with no volume
// under KT_STATE_DIR.
func TestTheDirectoryIsTheOneStateDirectory(t *testing.T) {
	t.Setenv(statefile.EnvDir, filepath.Join("tmp", "kubetower-state"))
	if got := Dir(); got != filepath.Join("tmp", "kubetower-state") {
		t.Errorf("Dir() = %q, and it is meant to be KT_STATE_DIR", got)
	}
}

// Set over a broken directory says "in effect now, but it will not survive a
// restart" (statefile.ErrNotSaved, shared by the hotbars, the column layouts and
// the nameplates); this test holds this store to it.
//
// `internal/web/settings.go` reads it: it puts the first line of this error on
// the settings page as `view.Problem`, because the setting *is* in effect and
// reporting only a failure would be worse than useless.
func TestAnUnwritableDirectoryStillSaysSo(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("%v", err)
	}
	var lines int
	store := New(filepath.Join(blocked, "state"), func(string, ...any) { lines++ })
	lines = 0

	applied, err := store.Set("busybox:1.37")
	if !errors.Is(err, statefile.ErrNotSaved) {
		t.Fatalf("Set over an unwritable directory returned %v", err)
	}
	if !strings.Contains(err.Error(), "in effect now") {
		t.Errorf("the sentence the settings page shows is gone: %q", err)
	}
	if applied.Image != "busybox:1.37" || store.Image() != "busybox:1.37" {
		t.Errorf("the setting did not take in memory: %q", store.Image())
	}

	// A refusal is a different answer: nothing is in effect, and a page that
	// said "in effect now" over one would be lying.
	if _, err := store.Set("not an image"); errors.Is(err, statefile.ErrNotSaved) {
		t.Errorf("a refused image reference reads as a change that merely failed to save: %v", err)
	}

	for i := 0; i < 5; i++ {
		if _, err := store.Set("busybox:1.3" + string(rune('0'+i))); err == nil {
			t.Fatal("a later write to the same broken directory reported success")
		}
	}
	if lines != 1 {
		t.Errorf("%d log lines for one broken directory, want exactly 1", lines)
	}
}

// A plain os.WriteFile interrupted by a crash or a full disk would leave a
// truncated nodeshell.json that the next start reads as unparseable and
// replaces with the default image — silently, and on a node where the operator
// was expecting their own toolbox. The setting goes through statefile.Write,
// which replaces the file rather than rewriting it in place, and sweeps what an
// interrupted save leaves behind.
func TestAStrandedTemporaryFileIsSweptUp(t *testing.T) {
	dir := t.TempDir()
	stranded := filepath.Join(dir, FileName+".9999999")
	if err := os.WriteFile(stranded, []byte("half a file"), 0o600); err != nil {
		t.Fatalf("%v", err)
	}

	if _, err := New(dir, nil).Set("busybox:1.37"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := os.Stat(stranded); !os.IsNotExist(err) {
		t.Errorf("%s survived a save", stranded)
	}
	if got := New(dir, nil).Image(); got != "busybox:1.37" {
		t.Errorf("the sweep took the setting with it: %q", got)
	}
}

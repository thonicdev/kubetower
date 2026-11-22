package extras

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Two renamed keys — `golf` to **Delta** and `hotel` to **Charlie** — and the
// one thing neither may cost.
//
// The key is the page's URL segment *and* the string written into extras.json,
// so changing it changes a file already sitting on every installation that ever
// ticked the page. Without the alias the page would come back off after the
// upgrade, on every cluster, with no error and nothing on the page to say why.
// That does not read as a rename. It reads as a feature somebody deleted.
//
// The second rename is the same shape and is tested the same way, deliberately
// side by side rather than in a file of its own: one alias working says nothing
// about another, and a table would hide which of the two is broken.

// A file written before the rename must still turn the page on.
func TestAStoredScheduledTickStillEnablesKubeCron(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "extras.json")

	// The shape such a file has on a running installation.
	const before = `{
  "on": {
    "kubernetes-admin@kubernetes": {
      "golf": true,
      "alfa": true
    }
  }
}`
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatalf("writing the pre-rename file: %v", err)
	}

	s := New(dir, []string{"kubernetes-admin@kubernetes"}, nil)

	if !s.EnabledOn("kubernetes-admin@kubernetes", Delta) {
		t.Error("a file holding the old \"golf\" key left Delta off. " +
			"Every installation that had the page on would lose it at this upgrade, " +
			"silently — see the legacy map in extras.go")
	}
	// The neighboring tick must be unaffected: an alias that folds one key must
	// not disturb the others in the same file.
	if !s.EnabledOn("kubernetes-admin@kubernetes", Alfa) {
		t.Error("reading the old file lost the Alfa page tick beside it")
	}
}

// The alias is one-way. Nothing writes the old spelling back, so the file
// converts itself on the next save and the alias becomes dead weight rather than
// a second permanent name for one page.
func TestTheOldKeyIsNotWrittenBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "extras.json")
	if err := os.WriteFile(path, []byte(`{"on":{"alpha":{"golf":true}}}`), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	s := New(dir, []string{alpha}, nil)
	// Any save at all. Ticking something else is the cheapest one that does not
	// change the fact under test.
	if err := s.SetOn(alpha, Alfa, true); err != nil {
		t.Fatalf("SetOn: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if strings.Contains(string(raw), legacyDelta) {
		t.Errorf("the old key was written back:\n%s\nThe alias is a migration, not a "+
			"synonym; leaving it in the file means carrying two names for one page for ever", raw)
	}
	if !strings.Contains(string(raw), Delta) {
		t.Errorf("the tick did not survive the conversion:\n%s", raw)
	}

	// And it still reads as on, which is the point of the whole exercise.
	var doc struct {
		On map[string]map[string]bool `json:"on"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the file it wrote is not readable: %v", err)
	}
	if !doc.On[alpha][Delta] {
		t.Errorf("Delta is not ticked in the written file: %v", doc.On[alpha])
	}
}

// KT_EXTRAS in a compose file carries the same string, and a deployment pinning
// the page on must not start reporting it as "not an extra".
func TestTheEnvironmentStillAcceptsTheOldName(t *testing.T) {
	pinned, complaints := ParseEnv("golf=on")

	if len(complaints) != 0 {
		t.Errorf("KT_EXTRAS=golf complained: %v", complaints)
	}
	on, ok := pinned[Delta]
	if !ok {
		t.Fatalf("KT_EXTRAS=golf pinned %v, which does not name Delta", pinned)
	}
	if !on {
		t.Error("KT_EXTRAS=golf pinned Delta off")
	}
	if _, stale := pinned[legacyDelta]; stale {
		t.Error("the old key survived into the pinned map, so two keys now describe one page")
	}
}

// Canonical must not invent keys. An unknown string is the caller's problem to
// reject — two places deciding what is real is how they come to disagree.
func TestCanonicalLeavesAnythingElseAlone(t *testing.T) {
	for _, key := range []string{Alfa, Echo, "not-an-extra", ""} {
		if got := Canonical(key); got != key {
			t.Errorf("Canonical(%q) = %q, want it returned unchanged", key, got)
		}
	}
	if got := Canonical(legacyDelta); got != Delta {
		t.Errorf("Canonical(\"golf\") = %q, want %q", got, Delta)
	}
	if got := Canonical(legacyCharlie); got != Charlie {
		t.Errorf("Canonical(\"hotel\") = %q, want %q", got, Charlie)
	}
}

// --- and the same four facts for Charlie --------------------------------
//
// Written out rather than folded into a table with the Delta cases above.
// A table over two aliases fails as one line naming neither, and these are the
// tests somebody reads at the moment an operator says the page came back off.

// A file written before the rename must still turn the page on.
func TestAStoredRightsizingTickStillEnablesKubeAdjust(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "extras.json")

	// Both old spellings in one file, because that is what an installation that
	// had both pages on actually holds — and an alias that folds one key must
	// not disturb the other one beside it.
	const before = `{
  "on": {
    "kubernetes-admin@kubernetes": {
      "hotel": true,
      "golf": true,
      "alfa": true
    }
  }
}`
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatalf("writing the pre-rename file: %v", err)
	}

	s := New(dir, []string{"kubernetes-admin@kubernetes"}, nil)

	if !s.EnabledOn("kubernetes-admin@kubernetes", Charlie) {
		t.Error("a file holding the old \"hotel\" key left Charlie off. " +
			"Every installation that had the page on would lose it at this upgrade, " +
			"silently — see the legacy map in extras.go")
	}
	if !s.EnabledOn("kubernetes-admin@kubernetes", Delta) {
		t.Error("folding \"hotel\" lost the Delta tick beside it, so one alias " +
			"is eating another")
	}
	if !s.EnabledOn("kubernetes-admin@kubernetes", Alfa) {
		t.Error("reading the old file lost the Alfa page tick beside it")
	}
}

// The alias is one-way. Nothing writes the old spelling back.
func TestTheOldRightsizingKeyIsNotWrittenBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "extras.json")
	if err := os.WriteFile(path, []byte(`{"on":{"alpha":{"hotel":true}}}`), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	s := New(dir, []string{alpha}, nil)
	if err := s.SetOn(alpha, Alfa, true); err != nil {
		t.Fatalf("SetOn: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if strings.Contains(string(raw), legacyCharlie) {
		t.Errorf("the old key was written back:\n%s\nThe alias is a migration, not a "+
			"synonym; leaving it in the file means carrying two names for one page for ever", raw)
	}

	var doc struct {
		On map[string]map[string]bool `json:"on"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the file it wrote is not readable: %v", err)
	}
	if !doc.On[alpha][Charlie] {
		t.Errorf("Charlie is not ticked in the written file: %v", doc.On[alpha])
	}
}

// KT_EXTRAS in a compose file carries the same string, and a deployment pinning
// the page on must not start reporting it as "not an extra".
func TestTheEnvironmentStillAcceptsRightsizing(t *testing.T) {
	pinned, complaints := ParseEnv("hotel=on")

	if len(complaints) != 0 {
		t.Errorf("KT_EXTRAS=hotel complained: %v", complaints)
	}
	on, ok := pinned[Charlie]
	if !ok {
		t.Fatalf("KT_EXTRAS=hotel pinned %v, which does not name Charlie", pinned)
	}
	if !on {
		t.Error("KT_EXTRAS=hotel pinned Charlie off")
	}
	if _, stale := pinned[legacyCharlie]; stale {
		t.Error("the old key survived into the pinned map, so two keys now describe one page")
	}
}

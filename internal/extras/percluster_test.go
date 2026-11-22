package extras

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Extras are ticked per cluster, and this file is about what that means.
//
// A tick reaches exactly one cluster, it survives a restart, and the file does
// not grow a row per cluster anybody ever looked at. And one that matters more
// than any of them: a file in an older format must be converted rather than
// read as empty.

const alpha, beta, gamma = "alpha", "beta", "gamma"

func threeClusters() []string { return []string{alpha, beta, gamma} }

// **The property the per-cluster model exists for.**
//
// A tick reaches exactly the cluster it was made on, never every cluster at
// once — which is what makes the blast radius of a tick visible from
// where the tick is made.
func TestATickReachesExactlyOneCluster(t *testing.T) {
	s := New("", threeClusters(), nil)

	if err := s.SetOn(alpha, Alfa, true); err != nil {
		t.Fatalf("SetOn: %v", err)
	}
	if !s.EnabledOn(alpha, Alfa) {
		t.Error("the cluster it was ticked on does not have it")
	}
	for _, other := range []string{beta, gamma} {
		if s.EnabledOn(other, Alfa) {
			t.Errorf("ticking %s on %s turned it on for %s as well", Alfa, alpha, other)
		}
	}
	// And no other page came with it.
	if s.EnabledOn(alpha, Bravo) {
		t.Error("ticking one page ticked another")
	}
}

// A page nobody has ticked is off everywhere, and a *fresh* store is that state.
//
// By default nothing is ticked. A context added tomorrow arrives the same way,
// because there is nothing global for it to inherit.
func TestAFreshStoreAndANewClusterBothStartWithNothing(t *testing.T) {
	s := New("", threeClusters(), nil)
	for _, e := range Catalogue() {
		for _, c := range threeClusters() {
			if s.EnabledOn(c, e.Key) {
				t.Errorf("%s is ticked on %s in a fresh store", e.Key, c)
			}
		}
	}
	// A context this store has never heard of is the same answer, which is what
	// makes "a new cluster arrives with nothing" true rather than merely likely.
	if s.EnabledOn("a-cluster-added-tomorrow", Alfa) {
		t.Error("a cluster nobody has ticked anything on has something ticked")
	}
	if s.Any() {
		t.Error("Any() is true with nothing ticked, so the menu would draw an empty Extra group")
	}
}

// A tick survives a restart, and lands back on the same cluster.
func TestATickSurvivesARestartOnItsOwnCluster(t *testing.T) {
	dir := t.TempDir()

	first := New(dir, threeClusters(), nil)
	if err := first.SetOn(beta, Bravo, true); err != nil {
		t.Fatalf("SetOn: %v", err)
	}

	second := New(dir, threeClusters(), nil)
	if !second.EnabledOn(beta, Bravo) {
		t.Error("the tick did not survive the restart")
	}
	if second.EnabledOn(alpha, Bravo) || second.EnabledOn(gamma, Bravo) {
		t.Error("the tick spread to another cluster across the restart")
	}
}

// Unticking is written down as a choice, not as an absence.
//
// Off is written as false, on every profile, and the file says which format it
// is in, so a future default cannot answer for a choice written down.
func TestUntickingIsWrittenAsAChoiceRatherThanAnAbsence(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, threeClusters(), nil)

	if err := s.SetOn(alpha, Delta, true); err != nil {
		t.Fatalf("SetOn on: %v", err)
	}
	if err := s.SetOn(alpha, Delta, false); err != nil {
		t.Fatalf("SetOn off: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("reading the file: %v", err)
	}
	var doc stored
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing the file: %v", err)
	}
	if doc.Format != formatChoices {
		t.Errorf("the file does not say its format, so absent cannot be told from off: %s", raw)
	}
	if on, chosen := doc.On[alpha][Delta]; !chosen || on {
		t.Errorf("turning %s off on %s was not written as false: %s", Delta, alpha, raw)
	}
}

// **A cluster nobody named is not a question this store can answer.**
//
// There is no global question, and returning true for "" would let a route that
// forgot to resolve its cluster through the guard.
func TestNoClusterIsNotEveryCluster(t *testing.T) {
	s := New("", threeClusters(), nil)
	if err := s.SetOn(alpha, Alfa, true); err != nil {
		t.Fatalf("SetOn: %v", err)
	}
	if s.EnabledOn("", Alfa) {
		t.Error("an unnamed cluster answered yes, so a route that failed to resolve its " +
			"cluster would walk straight through the guard")
	}
	// But `Enabled` — the "anywhere" question — must still see it, because that
	// is what tells a 404 "you have not ticked this anywhere" from "not here".
	if !s.Enabled(Alfa) {
		t.Error("Enabled cannot see a tick, so the 404 cannot tell the two refusals apart")
	}
}

// SetOn refuses what it cannot do, and says which.
func TestSetOnRefusesWhatItCannotDo(t *testing.T) {
	s := New("", threeClusters(), nil)

	if err := s.SetOn(alpha, "kafka", true); err == nil {
		t.Error("a key that is not an extra was accepted")
	}
	if err := s.SetOn("", Alfa, true); err == nil {
		t.Error("a tick with no cluster was accepted, and it would land nowhere")
	}

	// Pinned by the deployment. The Settings page must not appear to override
	// the deployment's KT_EXTRAS, and the refusal has to say which one decided.
	t.Setenv(EnvExtras, Alfa+"=off")
	pinned := New("", threeClusters(), nil)
	err := pinned.SetOn(alpha, Alfa, true)
	if err == nil {
		t.Fatal("a pinned extra was changed from the page")
	}
	if !strings.Contains(err.Error(), EnvExtras) {
		t.Errorf("the refusal does not name %s, so the operator does not know where to look: %v",
			EnvExtras, err)
	}
}

// **The one the change created, and the one that decides whether it is safe to
// ship.**
//
// An operator upgrading has a file in the previous format: `enabled` globally,
// `off` per cluster. Read as the current format it holds nothing, so every page
// they had would vanish at a restart they did not connect to it. It is converted
// instead — and the conversion needs the cluster list, which is why New takes
// one.
func TestAFileInThePreviousFormatIsConvertedRatherThanReadAsEmpty(t *testing.T) {
	dir := t.TempDir()
	older := map[string]any{
		"enabled": map[string]bool{
			Alfa:  true,
			Bravo: true,
			Echo:  false, // never turned on
		},
		// The operator had taken the Alfa page away on gamma.
		"off": map[string]map[string]bool{
			gamma: {Alfa: true},
		},
	}
	raw, err := json.MarshalIndent(older, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	var said []string
	s := New(dir, threeClusters(), func(format string, args ...any) { said = append(said, format) })

	// On everywhere it was not excluded.
	for _, c := range []string{alpha, beta} {
		if !s.EnabledOn(c, Alfa) {
			t.Errorf("%s lost the Alfa page on %s", FileName, c)
		}
		if !s.EnabledOn(c, Bravo) {
			t.Errorf("%s lost the Bravo page on %s", FileName, c)
		}
	}
	// And the exclusion carried across: a decision already made is not made again.
	if s.EnabledOn(gamma, Alfa) {
		t.Errorf("the exclusion on %s was dropped, turning a page back on where the "+
			"operator had taken it away", gamma)
	}
	if s.EnabledOn(gamma, Bravo) != true {
		t.Errorf("%s lost the Bravo page on %s, which was never excluded", FileName, gamma)
	}
	// A page that was never on stays off.
	for _, c := range threeClusters() {
		if s.EnabledOn(c, Echo) {
			t.Errorf("a page that was off in the old file came on for %s", c)
		}
	}
	// And it is said out loud. A conversion nobody is told about is one that
	// looks like the console rearranging itself.
	if len(said) == 0 {
		t.Error("the conversion was silent")
	}
}

// And when there is no cluster list, the conversion cannot happen — so the loss
// is announced rather than performed quietly.
//
// This is the branch a test fixture takes, and the reason it must be loud: a
// process that reads an old file with no kubeconfig behind it would otherwise
// write an empty file back on the first tick and take the operator's pages with
// it.
func TestAnOldFileWithNoClusterListSaysWhatItCannotDo(t *testing.T) {
	dir := t.TempDir()
	raw, _ := json.Marshal(map[string]any{"enabled": map[string]bool{Alfa: true}})
	if err := os.WriteFile(filepath.Join(dir, FileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	var said []string
	s := New(dir, nil, func(format string, args ...any) { said = append(said, format) })

	if s.Enabled(Alfa) {
		t.Error("something was ticked with nothing to tick it against")
	}
	if len(said) == 0 {
		t.Fatal("an old file was dropped silently")
	}
	if !strings.Contains(strings.Join(said, " "), "previous format") {
		t.Errorf("the message does not say what happened: %v", said)
	}
}

// The previous format is read and never written back, so the first tick after an
// upgrade retires it.
func TestTheFirstTickAfterAnUpgradeRetiresTheOldKeys(t *testing.T) {
	dir := t.TempDir()
	raw, _ := json.Marshal(map[string]any{"enabled": map[string]bool{Alfa: true}})
	if err := os.WriteFile(filepath.Join(dir, FileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	s := New(dir, threeClusters(), nil)
	if err := s.SetOn(beta, Bravo, true); err != nil {
		t.Fatalf("SetOn: %v", err)
	}

	after, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), `"enabled"`) || strings.Contains(string(after), `"off"`) {
		t.Errorf("the previous format's keys were written back:\n%s", after)
	}
	// And the converted ticks are still there, which is what makes retiring them
	// safe rather than lossy.
	reopened := New(dir, threeClusters(), nil)
	if !reopened.EnabledOn(alpha, Alfa) {
		t.Error("the converted ticks were lost when the old keys were retired")
	}
}

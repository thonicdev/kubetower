package extras

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The sentences of the package comment, checked. The three that matter: the
// default is off, KT_EXTRAS pins, and a pinned extra cannot be moved from the
// page.

// off is the whole default, and it is the one a fresh install gets.
//
// Worth a test of its own rather than an assumption inside a bigger one: "the
// default is off" is the sentence the feature rests on, and a store that
// quietly defaulted to on would still pass every test about pinning below.
// testClusters is the kubeconfig every store in this file is opened against.
//
// Two, and deliberately not one: an extra is ticked per cluster, so a test with
// a single context cannot tell "ticked here" from "ticked everywhere".
var testClusters = []string{"alpha", "beta"}

func TestAFreshStoreHasEveryExtraOff(t *testing.T) {
	t.Setenv(EnvExtras, "")
	store := New(t.TempDir(), testClusters, nil)

	for _, e := range Catalogue() {
		if store.Enabled(e.Key) {
			t.Errorf("%s is on in a fresh store; nothing had asked for it", e.Key)
		}
	}
	if store.Any() {
		t.Error("Any() is true with nothing enabled — the menu would draw an Extra " +
			"heading with no links under it")
	}
}

// A store with nowhere to write is a supported state, not an error. It is what
// every test fixture and every no-state-directory deployment gets, and it must
// still answer.
func TestAStoreWithNoDirectoryStillAnswersAndForgets(t *testing.T) {
	t.Setenv(EnvExtras, "")
	store := New("", testClusters, nil)

	if store.Path() != "" {
		t.Errorf("Path() = %q, want empty: there is nowhere to persist to", store.Path())
	}
	if err := store.SetOn("alpha", Alfa, true); err != nil {
		t.Fatalf("Set on a store with no directory: %v", err)
	}
	if !store.Enabled(Alfa) {
		t.Error("the choice did not take effect in memory; a store with no file still has to answer")
	}
}

func TestAChoiceSurvivesARestart(t *testing.T) {
	t.Setenv(EnvExtras, "")
	dir := t.TempDir()

	first := New(dir, testClusters, nil)
	if err := first.SetOn("alpha", Delta, true); err != nil {
		t.Fatalf("Set: %v", err)
	}

	second := New(dir, testClusters, nil)
	if !second.Enabled(Delta) {
		t.Error("the extra came back off after a restart; the file is what makes this a setting " +
			"rather than a session")
	}
	// The other three must not have been turned on by the round trip.
	if second.Enabled(Alfa) || second.Enabled(Bravo) || second.Enabled(Charlie) {
		t.Errorf("a restart enabled something nobody asked for: %+v", second.State("alpha"))
	}
}

// Turning one off is remembered: a page turned off stays off across a restart,
// and does not come back.
func TestTurningOneOffIsRememberedAsADecision(t *testing.T) {
	dir := t.TempDir()

	first := New(dir, testClusters, nil)
	if err := first.SetOn("alpha", Alfa, true); err != nil {
		t.Fatalf("SetOn on: %v", err)
	}
	if err := first.SetOn("alpha", Alfa, false); err != nil {
		t.Fatalf("SetOn off: %v", err)
	}

	second := New(dir, testClusters, nil)
	if second.EnabledOn("alpha", Alfa) {
		t.Error("a page turned off came back after a restart")
	}
	// And it did not leak to the other cluster on the way through, which is the
	// property the per-cluster model exists for.
	if second.EnabledOn("beta", Alfa) {
		t.Error("turning a page on for one cluster turned it on for another")
	}
}

// --- KT_EXTRAS ------------------------------------------------------------------

func TestParseEnvReadsTheGrammarAPersonWrites(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want map[string]bool
	}{
		{"empty", "", map[string]bool{}},
		{"a bare key means on", Alfa, map[string]bool{Alfa: true}},
		{"several", "alfa,bravo", map[string]bool{Alfa: true, Bravo: true}},
		{"explicit off", "alfa=off", map[string]bool{Alfa: false}},
		{"the other spellings of off", "alfa=false,bravo=no,charlie=0",
			map[string]bool{Alfa: false, Bravo: false, Charlie: false}},
		{"the other spellings of on", "alfa=true,bravo=yes,charlie=1",
			map[string]bool{Alfa: true, Bravo: true, Charlie: true}},
		// Written by a person in a YAML file, so the space after the comma and
		// the capital are what they will actually type.
		{"space and case", " Alfa , BRAVO=Off ", map[string]bool{Alfa: true, Bravo: false}},
		{"a trailing comma is not an item", "alfa,", map[string]bool{Alfa: true}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, complaints := ParseEnv(c.in)
			if len(complaints) != 0 {
				t.Errorf("complaints on a valid value: %v", complaints)
			}
			if len(got) != len(c.want) {
				t.Fatalf("parsed %v, want %v", got, c.want)
			}
			for key, want := range c.want {
				if got[key] != want {
					t.Errorf("%s = %v, want %v (whole: %v)", key, got[key], want, got)
				}
			}
		})
	}
}

// A typo must be said out loud and must not be silently taken as an extra.
//
// It must also not stop the process: an environment variable is not a place an
// operator sees a refusal, and refusing to start over a misspelled optional page
// would take the whole console away over one word.
func TestParseEnvComplainsAboutWhatItCannotUse(t *testing.T) {
	cases := []struct {
		name string
		in   string
		// mention is the word the operator got wrong; the complaint is useless
		// without it.
		mention string
		// alsoLists is what else has to be there for the complaint to be
		// actionable, and it differs by case: a key that is not an extra needs
		// the four spelled out, while a bad *value* needs the two words that
		// would have worked. Listing the keys in the second case would point at
		// the half that was already right.
		alsoLists []string
	}{
		{"an unknown key", "secutiry", "secutiry", Keys()},
		{"a value that is neither", "alfa=maybe", "maybe", []string{"on", "off"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pinned, complaints := ParseEnv(c.in)
			if len(pinned) != 0 {
				t.Errorf("pinned %v from an unusable value; nothing should have been taken", pinned)
			}
			if len(complaints) != 1 {
				t.Fatalf("complaints = %v, want exactly one", complaints)
			}
			if !strings.Contains(complaints[0], c.mention) {
				t.Errorf("the complaint does not quote %q, so it cannot be acted on: %s",
					c.mention, complaints[0])
			}
			// A complaint that does not offer the alternatives leaves the reader
			// guessing at what would have worked.
			for _, want := range c.alsoLists {
				if !strings.Contains(complaints[0], want) {
					t.Errorf("the complaint does not offer %q: %s", want, complaints[0])
				}
			}
		})
	}
}

// The pin is the whole point of the compose half: it wins over the stored
// choice, in both directions.
func TestComposeWinsOverTheStoredChoice(t *testing.T) {
	dir := t.TempDir()

	// Store the opposite of what the environment will say, both ways round.
	t.Setenv(EnvExtras, "")
	seed := New(dir, testClusters, nil)
	if err := seed.SetOn("alpha", Alfa, false); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := seed.SetOn("alpha", Bravo, true); err != nil {
		t.Fatalf("Set: %v", err)
	}

	t.Setenv(EnvExtras, "alfa=on,bravo=off")
	store := New(dir, testClusters, nil)

	if !store.Enabled(Alfa) {
		t.Error("KT_EXTRAS pinned alfa on and the stored off won; the deployment has to win")
	}
	if store.Enabled(Bravo) {
		t.Error("KT_EXTRAS pinned bravo off and the stored on won; the deployment has to win")
	}
	// And the half that makes it a *per extra* rule rather than a mode: what
	// compose did not mention is still the operator's.
	if store.Pinned(Charlie) {
		t.Error("charlie is pinned although KT_EXTRAS never named it — that is a mode " +
			"switch, not a per-extra override")
	}
}

func TestAPinnedExtraCannotBeChangedFromThePage(t *testing.T) {
	t.Setenv(EnvExtras, "alfa=on")
	store := New(t.TempDir(), testClusters, nil)

	err := store.SetOn("alpha", Alfa, false)
	if err == nil {
		t.Fatal("Set on a pinned extra was accepted; the page could then contradict the deployment")
	}
	if !errors.Is(err, ErrPinned) {
		t.Errorf("error is %v, which does not wrap ErrPinned — the handler tells the pinned case "+
			"from a typo by that wrap", err)
	}
	if !store.Enabled(Alfa) {
		t.Error("the refused Set changed the answer anyway")
	}
	// The refusal has to name the variable, or the operator goes looking on the
	// page that just refused them.
	if !strings.Contains(err.Error(), EnvExtras) {
		t.Errorf("the refusal does not name %s, so it does not say where the decision lives: %v",
			EnvExtras, err)
	}
}

func TestSetRefusesAKeyThatIsNotAnExtra(t *testing.T) {
	t.Setenv(EnvExtras, "")
	store := New(t.TempDir(), testClusters, nil)

	// Not a plausible page name: a placeholder chosen from the plausible
	// neighborhood of the real thing is one the real thing eventually reaches.
	err := store.SetOn("alpha", "kafka", true)
	if err == nil {
		t.Fatal("Set stored a key that is not an extra")
	}
	if !errors.Is(err, ErrUnknown) {
		t.Errorf("error is %v, which does not wrap ErrUnknown", err)
	}
}

// The note is what turns a switch that will not move into a switch that explains
// itself. Both directions, because "pinned off" is the case an operator meets
// when a page they expect is missing.
func TestTheNoteNamesTheVariableAndTheDirection(t *testing.T) {
	t.Setenv(EnvExtras, "alfa=on,bravo=off")
	store := New(t.TempDir(), testClusters, nil)

	for _, e := range store.State("alpha") {
		switch e.Key {
		case Alfa, Bravo:
			if !e.Pinned {
				t.Errorf("%s is not marked pinned", e.Key)
			}
			if !strings.Contains(e.Note, EnvExtras) {
				t.Errorf("%s's note does not name %s: %q", e.Key, EnvExtras, e.Note)
			}
		default:
			if e.Pinned {
				t.Errorf("%s is marked pinned and KT_EXTRAS never named it", e.Key)
			}
			if e.Note != "" {
				t.Errorf("%s has a note (%q) and nothing decided it for the operator", e.Key, e.Note)
			}
		}
	}
}

// --- the file ---------------------------------------------------------------------

// extras.json is hand-editable, so a key that is not an extra has to be dropped
// on the way in. Kept, it would be written back on every save and would look
// like a feature somebody removed.
func TestAnUnknownKeyInTheFileIsIgnoredAndSaidOutLoud(t *testing.T) {
	t.Setenv(EnvExtras, "")
	dir := t.TempDir()
	doc := `{"enabled":{"alfa":true,"kafka":true}}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(doc), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	var said []string
	store := New(dir, testClusters, func(format string, args ...any) {
		said = append(said, format)
	})

	if !store.Enabled(Alfa) {
		t.Error("the extra that *is* an extra was dropped along with the one that is not")
	}
	if store.Enabled("kafka") {
		t.Error("a key that is not an extra was honored")
	}
	if len(said) == 0 {
		t.Error("nothing was logged; a file quietly ignored in part is how a hand edit " +
			"becomes a mystery")
	}
}

// A corrupt file is a forgotten preference, never a console that will not start.
// The same bargain hotbars, columns and nameplates are on.
func TestAnUnreadableFileLeavesEveryExtraOffAndStarts(t *testing.T) {
	t.Setenv(EnvExtras, "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	var said []string
	store := New(dir, testClusters, func(format string, args ...any) { said = append(said, format) })

	if store.Any() {
		t.Error("an unreadable file enabled something")
	}
	if len(said) == 0 {
		t.Error("an unreadable state file was not reported")
	}
}

// Catalog hands out a copy. A caller that could mutate it — and the caller is a
// template — would change what every other page draws.
func TestCatalogueCannotBeMutatedByItsCaller(t *testing.T) {
	got := Catalogue()
	if len(got) == 0 {
		t.Fatal("the catalog is empty")
	}
	got[0].Label = "clobbered"

	if Catalogue()[0].Label == "clobbered" {
		t.Error("Catalog returns the package's own slice; one page could rename an extra " +
			"for every other page")
	}
}

// Every entry in the catalog has to be drawable: a switch with no label is one
// nobody can match to a menu entry, and one with no summary tells an operator who
// has not used it nothing.
func TestEveryExtraIsDescribedWellEnoughToDraw(t *testing.T) {
	for _, e := range Catalogue() {
		if e.Key == "" || e.Label == "" || e.Summary == "" || e.Needs == "" {
			t.Errorf("%+v is missing one of Key/Label/Summary/Needs", e)
		}
		if !Known(e.Key) {
			t.Errorf("%q is in the catalog and Known says it is not an extra", e.Key)
		}
	}
}

// Every sentence that tells an operator where KT_EXTRAS is set says it in the
// same words: the note beside a pinned switch, the error a pinned write gets,
// and HowToTurnOn's desktop form, which the refusal in internal/web and the
// start-up line both end with. Spelled separately, one of them would go on
// naming a place after the others were corrected.
func TestEverySentenceNamesTheDeploymentsEnvironmentAndNothingThatIsGone(t *testing.T) {
	t.Setenv(EnvExtras, "alfa=on,bravo=off")
	store := New(t.TempDir(), testClusters, nil)

	sentences := map[string]string{
		"the note, pinned on":      store.PinnedNote(Alfa),
		"the note, pinned off":     store.PinnedNote(Bravo),
		"ErrPinned":                ErrPinned.Error(),
		"HowToTurnOn":              HowToTurnOn("alpha", Alfa, Desktop),
		"HowToTurnOn, every extra": HowToTurnOn("<cluster>", "", Desktop),
	}
	for name, s := range sentences {
		if !strings.Contains(s, EnvExtras) || !strings.Contains(s, WhereSet) {
			t.Errorf("%s does not say %s %s: %q", name, EnvExtras, WhereSet, s)
		}
		if strings.Contains(s, "docker-compose") {
			t.Errorf("%s names docker-compose, which does not exist: %q", name, s)
		}
	}
}

// The per-cluster write is named exactly where it is registered: on one
// console, desktop or server, with the cluster and the key spelled to be typed;
// never on one of several replicas, where internal/api does not register it.
// The server form names who may press it (the verb `use`) and that the chart's
// values must name the extra. With no key the sentence is about every
// extra, and says so with a placeholder rather than an empty path segment.
func TestHowToTurnOnNamesThePerClusterWriteOnlyWhereItExists(t *testing.T) {
	for _, profile := range []Profile{Desktop, Server} {
		got := HowToTurnOn("prod%20one", Alfa, profile)
		for _, want := range []string{"PUT /api/c/prod%20one/extensions/" + Alfa, "Settings > Extra"} {
			if !strings.Contains(got, want) {
				t.Errorf("profile %d: %q does not name %q", profile, got, want)
			}
		}
	}
	if got, want := HowToTurnOn("prod", Alfa, Desktop), EnvExtras+"="+Alfa+" "+WhereSet; !strings.Contains(got, want) {
		t.Errorf("desktop: %q does not name %q", got, want)
	}
	if got := HowToTurnOn("prod", Alfa, Desktop); !strings.Contains(got, "restart") {
		t.Errorf("desktop: %q names KT_EXTRAS and not the restart it needs", got)
	}
	server := HowToTurnOn("prod", Alfa, Server)
	for _, want := range []string{"use on extras.kubetower.dev/" + Alfa, "chart's values", EnvExtras} {
		if !strings.Contains(server, want) {
			t.Errorf("server: %q does not say %q", server, want)
		}
	}
	if strings.Contains(server, EnvExtras+"="+Alfa) {
		t.Errorf("server: %q offers KT_EXTRAS as a pin, which it is not there", server)
	}
	replicated := HowToTurnOn("prod", Alfa, Replicated)
	if strings.Contains(replicated, "PUT") {
		t.Errorf("several replicas: %q names a write that is not registered there", replicated)
	}
	if !strings.Contains(replicated, "one replica") {
		t.Errorf("several replicas: %q does not say one replica is what it takes", replicated)
	}
	unkept := HowToTurnOn("prod", Alfa, Unkept)
	if strings.Contains(unkept, "PUT") || strings.Contains(unkept, "replicas") || !strings.Contains(unkept, "state volume") {
		t.Errorf("no state volume: %q names a write or replicas, or not the state volume", unkept)
	}
	every := HowToTurnOn("<cluster>", "", Desktop)
	for _, want := range []string{"an extra", "/extensions/<extra>", EnvExtras + "=<extra>"} {
		if !strings.Contains(every, want) {
			t.Errorf("every extra: %q does not say %q", every, want)
		}
	}
}

// The two start-up sentences that are not refusals: a KT_EXTRAS value that is
// neither on nor off, and a file in the previous format with no cluster list to
// convert it with. The second
// is pinned to HowToTurnOn itself, the first to the deployment's switch, which
// is the one that exists before anything knows how many replicas there are.
func TestTheStartupComplaintsNameOnlySwitchesThatExist(t *testing.T) {
	_, complaints := ParseEnv(Alfa + "=maybe")
	if len(complaints) != 1 {
		t.Fatalf("ParseEnv(%q) complained %d times, want once: %v", Alfa+"=maybe", len(complaints), complaints)
	}
	if want := "write " + Alfa + "=on or " + Alfa + "=off in " + EnvExtras + " " + WhereSet + " and restart"; !strings.Contains(complaints[0], want) {
		t.Errorf("the complaint does not say %q: %q", want, complaints[0])
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(`{"enabled":{"`+Alfa+`":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var said []string
	New(dir, nil, func(format string, args ...any) { said = append(said, fmt.Sprintf(format, args...)) })
	migrated := strings.Join(said, "\n")
	if want := HowToTurnOn("<cluster>", "", Desktop); !strings.Contains(migrated, want) {
		t.Errorf("the previous-format message does not end in HowToTurnOn's words %q: %q", want, migrated)
	}

	for name, s := range map[string]string{"the KT_EXTRAS complaint": complaints[0], "the migration message": migrated} {
		for _, stale := range []string{"settings page", "docker-compose"} {
			if strings.Contains(s, stale) {
				t.Errorf("%s names %q, which does not exist: %q", name, stale, s)
			}
		}
	}
}

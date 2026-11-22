package web

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/columns"
	"github.com/thonicdev/kubetower/internal/extras"
	"github.com/thonicdev/kubetower/internal/hotbars"
	"github.com/thonicdev/kubetower/internal/nameplates"
	"github.com/thonicdev/kubetower/internal/resources"
)

// A preference write that took effect and could not be written down is not a
// refusal.
//
// `statefile.ErrNotSaved` says the change **is in effect now** and will be gone
// at the next start. A write wrapping it as `api.ErrInvalid` would make the
// client read a 400 - "nothing happened, your request was wrong" - for a
// change the console had made. `RailArrange.tsx`'s swap is two writes; if the first
// answered 400 the second would never be sent, and the rail would be left with the first
// half of a swap applied in memory.
//
// Over the real stores, on a state directory under a file, so that every write
// fails the way a full or read-only disk makes it fail, and the stores' own
// wording reaches the translation untouched.
func TestAWriteThatWasAppliedButNotSavedIsNotARefusal(t *testing.T) {
	t.Setenv(extras.EnvExtras, "")
	prefs, server := prefsUnder(t)
	contexts := server.clusters.Contexts()

	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("%v", err)
	}
	state := filepath.Join(blocked, "state")
	server.bars = hotbars.New(state, nil)
	server.columns = columns.New(state, nil)
	server.plates = nameplates.New(state, nil)
	server.extras = extras.New(state, contexts, nil)

	bar := server.bars.Arrangement(contexts).Bars[0]
	if len(bar) < 3 {
		t.Fatalf("the fixture's hotbar 1 holds %d tiles, too few for these writes", len(bar))
	}
	first, second, third := bar[0], bar[1], bar[2]

	declared := resources.ColumnsOf("pods")
	key := func(i int) string { return columnKey(declared[i].Title, i) }
	last := len(declared) - 1
	width := 200
	label := "Somebody's name"

	// In order, not a map: each write changes what the next one starts from, and
	// a reset needs something to forget or it writes nothing at all.
	for _, c := range []struct {
		what  string
		write func() error
	}{
		{"rail bar", func() error { return prefs.SetRailBar(2) }},
		{"rail assign", func() error { return prefs.SetRailAssign(first, 2) }},
		{"rail move", func() error {
			return prefs.SetRailMove(second, positionOf(server.bars.Arrangement(contexts), second)+1)
		}},
		{"rail step", func() error { return prefs.SetRailStep(second, -1) }},
		{"rail place", func() error { return prefs.SetRailPlace(third, second, false) }},
		{"rail nameplate", func() error { return prefs.SetRailNameplate(api.RailNameplateWrite{Cluster: second, Label: &label}) }},
		{"rail hide", func() error { return prefs.SetRailHidden(third, true) }},
		{"column width", func() error { return prefs.SetColumnWidth("pods", key(1), &width) }},
		{"column order", func() error { return prefs.SetColumnOrder("pods", key(last), 1) }},
		{"column place", func() error { return prefs.SetColumnPlace("pods", key(1), key(last), true) }},
		{"column visible", func() error { return prefs.SetColumnVisible("pods", key(1), false) }},
		{"column density", func() error { return prefs.SetColumnDensity(string(columns.Compact)) }},
		{"columns reset", func() error { return prefs.ResetColumns("pods", string(columns.ScopeAll)) }},
		{"extension", func() error { return apiClusters{server}.SetExtension(context.Background(), first, fixtureKey, true) }},
		{"extension setting", func() error {
			return apiClusters{server}.SetExtensionSetting(context.Background(), first, fixtureOtherKey, fixtureSetting, false)
		}},
	} {
		t.Run(c.what, func(t *testing.T) {
			err := c.write()
			if !errors.Is(err, api.ErrNotSaved) {
				t.Fatalf("returned %v, want an api.ErrNotSaved: the change is in effect and the "+
					"client must be told it will not survive a restart, not that it was refused", err)
			}
			if errors.Is(err, api.ErrInvalid) {
				t.Errorf("%v is also an ErrInvalid, so it would still reach the client as a 400", err)
			}
		})
	}

	// The store really did apply them: that is what makes "not saved" the true
	// answer and "refused" the false one.
	if got := server.bars.Arrangement(contexts).Active; got != 2 {
		t.Errorf("hotbar %d is on screen after an unsaved SetRailBar(2); the change was not applied", got)
	}
	if !server.extras.EnabledOn(first, fixtureKey) {
		t.Error("the extension is off after an unsaved SetExtension; the change was not applied")
	}

	// A real refusal is still a refusal, on the same unwritable directory: the
	// store said no before it ever tried to write.
	if err := prefs.SetRailBar(99); !errors.Is(err, api.ErrInvalid) || errors.Is(err, api.ErrNotSaved) {
		t.Errorf("hotbar 99 returned %v, want an ErrInvalid and nothing else", err)
	}
}

// An extension key this build does not carry is not found, not a malformed
// request: the key is well formed - the route's pattern already checked that -
// and names nothing. The contract declares 404 for it.
func TestAnUnknownExtensionKeyIsNotFound(t *testing.T) {
	t.Setenv(extras.EnvExtras, "")
	_, server := prefsUnder(t)
	server.extras = extras.New(t.TempDir(), server.clusters.Contexts(), nil)

	err := apiClusters{server}.SetExtension(context.Background(), "mi-k3s", "no-such-extra", true)
	if !errors.Is(err, api.ErrNotFound) {
		t.Errorf("an unknown extension key returned %v, want an api.ErrNotFound (404)", err)
	}
	if errors.Is(err, api.ErrInvalid) {
		t.Errorf("%v is also an ErrInvalid, so it would still reach the client as a 400", err)
	}

	// The same for a setting: an extension this build does not carry, and a
	// setting a carried one does not offer, are both not found.
	for what, err := range map[string]error{
		"unknown extension": apiClusters{server}.SetExtensionSetting(context.Background(), "mi-k3s", "no-such-extra", fixtureSetting, true),
		"unknown setting":   apiClusters{server}.SetExtensionSetting(context.Background(), "mi-k3s", fixtureOtherKey, "no-such-setting", true),
		"no setting at all": apiClusters{server}.SetExtensionSetting(context.Background(), "mi-k3s", fixtureKey, fixtureSetting, true),
	} {
		if !errors.Is(err, api.ErrNotFound) || errors.Is(err, api.ErrInvalid) {
			t.Errorf("%s returned %v, want an api.ErrNotFound (404) and nothing else", what, err)
		}
	}
}

// The listing carries what the Settings page's Extra tab draws: the
// extension's own words, and each setting at its value on that cluster.
func TestTheListingCarriesEachExtensionsWordsAndSettings(t *testing.T) {
	t.Setenv(extras.EnvExtras, "")
	_, server := prefsUnder(t)
	contexts := server.clusters.Contexts()
	server.extras = extras.New(t.TempDir(), contexts, nil)
	clusters := apiClusters{server}

	if err := clusters.SetExtensionSetting(context.Background(), contexts[0], fixtureOtherKey, fixtureSetting, false); err != nil {
		t.Fatalf("SetExtensionSetting: %v", err)
	}
	for i, cluster := range contexts[:2] {
		rows, failure := clusters.Extensions(context.Background(), cluster)
		if failure != nil {
			t.Fatalf("Extensions(%s): %+v", cluster, failure)
		}
		byKey := map[string]api.Extension{}
		for _, row := range rows {
			byKey[row.Key] = row
		}
		alfa, bravo := byKey[fixtureKey], byKey[fixtureOtherKey]
		if alfa.Enabled || bravo.Enabled {
			t.Errorf("on %s, with nothing chosen, an extension is on: %+v", cluster, rows)
		}
		if !alfa.Writable || alfa.Locked != "" || alfa.Reason != "" {
			t.Errorf("on the desktop an extension nothing pins is not writable: %+v", alfa)
		}
		if alfa.Summary == "" || alfa.Needs == "" {
			t.Errorf("the listing dropped the extension's words: %+v", alfa)
		}
		if alfa.Settings == nil || len(alfa.Settings) != 0 {
			t.Errorf("an extension with no setting carries %#v, want an empty list", alfa.Settings)
		}
		if len(bravo.Settings) != 1 || bravo.Settings[0].Key != fixtureSetting || !bravo.Settings[0].Default {
			t.Fatalf("the configurable fixture's setting is not listed as declared: %+v", bravo.Settings)
		}
		if want := i != 0; bravo.Settings[0].Value != want {
			t.Errorf("on %s the setting reads %v, want %v (chosen off on %s only)",
				cluster, bravo.Settings[0].Value, want, contexts[0])
		}
	}
}

// KT_EXTRA_SETTINGS pins a setting for every cluster:
// the listing says `pinned` with the pinned value, and the write is refused as
// a 400 naming the variable - the same answer a pinned extra's switch gets.
func TestAPinnedSettingIsListedAsPinnedAndItsWriteRefused(t *testing.T) {
	t.Setenv(extras.EnvExtras, "")
	t.Setenv(extras.EnvExtraSettings, fixtureOtherKey+"."+fixtureSetting+"=off")
	_, server := prefsUnder(t)
	contexts := server.clusters.Contexts()
	server.extras = extras.New(t.TempDir(), contexts, nil)
	clusters := apiClusters{server}

	rows, failure := clusters.Extensions(context.Background(), contexts[0])
	if failure != nil {
		t.Fatalf("Extensions: %+v", failure)
	}
	for _, row := range rows {
		if row.Key != fixtureOtherKey {
			continue
		}
		if len(row.Settings) != 1 || !row.Settings[0].Pinned || row.Settings[0].Value {
			t.Fatalf("the pinned setting is listed as %+v, want pinned and off", row.Settings)
		}
	}

	err := clusters.SetExtensionSetting(context.Background(), contexts[0], fixtureOtherKey, fixtureSetting, true)
	if !errors.Is(err, api.ErrInvalid) || !strings.Contains(err.Error(), extras.EnvExtraSettings) {
		t.Errorf("writing a pinned setting returned %v, want a 400 naming %s", err, extras.EnvExtraSettings)
	}
}

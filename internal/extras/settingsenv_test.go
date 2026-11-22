package extras

import (
	"errors"
	"strings"
	"testing"
)

// KT_EXTRA_SETTINGS pins an extra's setting for every cluster, as KT_EXTRAS
// pins an extra: the deployment's way to turn a setting such as Argo CD's
// column on under the server profile, where nothing else can.

func TestParseSettingsEnvReadsWhatAPersonWrites(t *testing.T) {
	pinned, complaints := ParseSettingsEnv(" Echo.List-Column = ON , echo.compact=false,, ")
	if len(complaints) != 0 {
		t.Fatalf("complaints = %q, want none", complaints)
	}
	if on, ok := pinned[Echo][echoColumn]; !ok || !on {
		t.Errorf("echo.list-column = %v (pinned %v), want on", on, ok)
	}
	if on, ok := pinned[Echo][echoCompact]; !ok || on {
		t.Errorf("echo.compact = %v (pinned %v), want off", on, ok)
	}
}

func TestParseSettingsEnvFollowsAnExtrasOldKey(t *testing.T) {
	// Charlie offers no settings, so the alias is checked through the
	// complaint: it names the canonical key, not the old one.
	_, complaints := ParseSettingsEnv(legacyCharlie + ".anything=on")
	if len(complaints) != 1 || !strings.Contains(complaints[0], Charlie+".anything") ||
		!strings.Contains(complaints[0], "offers no settings") {
		t.Errorf("complaints = %q, want one naming %s.anything and that it offers no settings", complaints, Charlie)
	}
}

func TestParseSettingsEnvPinsNothingItCannotRead(t *testing.T) {
	for _, tc := range []struct{ value, says string }{
		{"echo.list-column=maybe", "neither on nor off"},
		{"echo.list-column", "with no value"},
		{"echo=on", "not <extra>.<setting>"},
		{".list-column=on", "not <extra>.<setting>"},
		{"zulu.list-column=on", "not an extra"},
		{"echo.nope=on", "does not offer"},
	} {
		pinned, complaints := ParseSettingsEnv(tc.value)
		if len(pinned) != 0 {
			t.Errorf("%q pinned %v, want nothing", tc.value, pinned)
		}
		if len(complaints) != 1 || !strings.Contains(complaints[0], tc.says) ||
			!strings.Contains(complaints[0], EnvExtraSettings) {
			t.Errorf("%q: complaints = %q, want one naming %s and saying %q", tc.value, complaints, EnvExtraSettings, tc.says)
		}
	}
}

func TestAPinnedSettingWinsOnEveryClusterAndRefusesTheWrite(t *testing.T) {
	t.Setenv(EnvExtras, "")
	t.Setenv(EnvExtraSettings, "echo.compact=on")
	var logged []string
	s := New(t.TempDir(), []string{"prod", "staging"}, func(f string, a ...any) {
		logged = append(logged, f)
	})

	for _, cluster := range []string{"prod", "staging", "never-seen"} {
		if !s.Setting(cluster, Echo, echoCompact) {
			t.Errorf("echo.compact on %s is off, want on: the variable pins it everywhere", cluster)
		}
	}
	if !s.SettingPinned(Echo, echoCompact) || s.SettingPinned(Echo, echoColumn) {
		t.Errorf("SettingPinned: compact %v, column %v; want true, false",
			s.SettingPinned(Echo, echoCompact), s.SettingPinned(Echo, echoColumn))
	}

	err := s.SetSetting("prod", Echo, echoCompact, false)
	if !errors.Is(err, ErrSettingPinned) || !strings.Contains(err.Error(), EnvExtraSettings) {
		t.Fatalf("SetSetting on a pinned setting = %v, want ErrSettingPinned naming %s", err, EnvExtraSettings)
	}
	if !s.Setting("prod", Echo, echoCompact) {
		t.Error("a refused write changed the value")
	}
	// The setting beside it is still the person's.
	if err := s.SetSetting("prod", Echo, echoColumn, false); err != nil {
		t.Fatalf("SetSetting on an unpinned setting: %v", err)
	}
	if s.Setting("prod", Echo, echoColumn) {
		t.Error("echo.list-column on prod is on after it was turned off")
	}

	var state State
	for _, one := range s.State("prod") {
		if one.Key == Echo {
			state = one
		}
	}
	if len(state.SettingsPinned) != 2 || state.SettingsPinned[0] || !state.SettingsPinned[1] {
		t.Errorf("State.SettingsPinned = %v, want [false true] (list-column, compact)", state.SettingsPinned)
	}
	if got := s.PinnedSettings(); len(got) != 1 || got[0] != "echo.compact=on" {
		t.Errorf("PinnedSettings = %q, want [echo.compact=on]", got)
	}
	if len(logged) != 0 {
		t.Errorf("a valid variable was complained about: %q", logged)
	}
}

func TestUnsetItLeavesEverySettingToThePersonAndTheDefault(t *testing.T) {
	t.Setenv(EnvExtras, "")
	t.Setenv(EnvExtraSettings, "")
	s := New(t.TempDir(), []string{"prod"}, func(string, ...any) {})

	if !s.Setting("prod", Echo, echoColumn) || s.Setting("prod", Echo, echoCompact) {
		t.Error("with nothing pinned, each setting should be at its declared default")
	}
	if s.SettingPinned(Echo, echoColumn) || len(s.PinnedSettings()) != 0 {
		t.Error("an empty variable pinned something")
	}
	if err := s.SetSetting("prod", Echo, echoCompact, true); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if !s.Setting("prod", Echo, echoCompact) {
		t.Error("the person's choice did not take")
	}
}

func TestAnInvalidValueIsLoggedAndPinsNothing(t *testing.T) {
	t.Setenv(EnvExtras, "")
	t.Setenv(EnvExtraSettings, "echo.compact=sometimes")
	var logged []string
	s := New(t.TempDir(), []string{"prod"}, func(f string, a ...any) {
		logged = append(logged, f)
	})

	if s.SettingPinned(Echo, echoCompact) || s.Setting("prod", Echo, echoCompact) {
		t.Error("an unreadable value pinned the setting")
	}
	if len(logged) != 1 {
		t.Errorf("logged %d lines, want the one complaint", len(logged))
	}
}

func TestAChoiceMadeBeforeThePinIsHonouredOnceItGoes(t *testing.T) {
	t.Setenv(EnvExtras, "")
	dir := t.TempDir()
	t.Setenv(EnvExtraSettings, "")
	before := New(dir, []string{"prod"}, func(string, ...any) {})
	if err := before.SetSetting("prod", Echo, echoColumn, false); err != nil {
		t.Fatal(err)
	}

	t.Setenv(EnvExtraSettings, "echo.list-column=on")
	pinned := New(dir, []string{"prod"}, func(string, ...any) {})
	if !pinned.Setting("prod", Echo, echoColumn) {
		t.Error("the pin did not win over the stored choice")
	}

	t.Setenv(EnvExtraSettings, "")
	after := New(dir, []string{"prod"}, func(string, ...any) {})
	if after.Setting("prod", Echo, echoColumn) {
		t.Error("the stored choice (off) was lost while the pin was in place")
	}
}

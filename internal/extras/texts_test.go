package extras

import (
	"errors"
	"strings"
	"testing"
)

// A line of text is empty until set, kept per cluster,
// survives a restart, and an empty value forgets it.
func TestATextSettingIsKeptPerClusterAndForgottenWhenEmptied(t *testing.T) {
	t.Setenv(EnvExtras, "")
	t.Setenv(foxtrotEnv, "")
	dir := t.TempDir()
	s := New(dir, threeClusters(), nil)
	if got := s.Text(alpha, Foxtrot, foxtrotServer); got != "" {
		t.Fatalf("unset reads %q", got)
	}
	if err := s.SetText(alpha, Foxtrot, foxtrotServer, "  ops/cron  "); err != nil {
		t.Fatal(err)
	}
	again := New(dir, threeClusters(), nil)
	if got := again.Text(alpha, Foxtrot, foxtrotServer); got != "ops/cron" {
		t.Errorf("after a restart %q, want ops/cron", got)
	}
	if got := again.Text(beta, Foxtrot, foxtrotServer); got != "" {
		t.Errorf("another cluster reads %q", got)
	}
	for _, row := range again.State(alpha) {
		if row.Key == Foxtrot && (len(row.Texts) != 1 || row.Texts[0] != "ops/cron" || row.SettingsPinned[0]) {
			t.Errorf("state %+v", row)
		}
	}
	if doc := readFile(t, dir); doc.Texts[alpha][Foxtrot][foxtrotServer] != "ops/cron" {
		t.Errorf("the file does not hold it: %+v", doc.Texts)
	}
	if err := again.SetText(alpha, Foxtrot, foxtrotServer, " "); err != nil {
		t.Fatal(err)
	}
	if got := again.Text(alpha, Foxtrot, foxtrotServer); got != "" {
		t.Errorf("emptied reads %q", got)
	}
	if doc := readFile(t, dir); len(doc.Texts[alpha][Foxtrot]) != 0 {
		t.Errorf("an emptied value is still in the file: %+v", doc.Texts)
	}
}

func TestSetTextRefusesWhatItCannotDo(t *testing.T) {
	t.Setenv(EnvExtras, "")
	t.Setenv(foxtrotEnv, "")
	s := New("", threeClusters(), nil)
	for name, tc := range map[string]struct {
		key, setting, value string
		want                error
	}{
		"a checkbox":           {Echo, echoColumn, "on", ErrSettingKind},
		"an undeclared one":    {Foxtrot, "nope", "x", ErrUnknownSetting},
		"not an extra":         {"kafka", foxtrotServer, "x", ErrUnknown},
		"its check refuses it": {Foxtrot, foxtrotServer, "refused", ErrTextInvalid},
		"two lines":            {Foxtrot, foxtrotServer, "a\nb", ErrTextInvalid},
		"too long":             {Foxtrot, foxtrotServer, strings.Repeat("a", 254), ErrTextInvalid},
	} {
		if err := s.SetText(alpha, tc.key, tc.setting, tc.value); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	if err := s.SetSetting(alpha, Foxtrot, foxtrotServer, true); !errors.Is(err, ErrSettingKind) {
		t.Errorf("a line of text written as on: %v", err)
	}
	if s.Setting(alpha, Foxtrot, foxtrotServer) {
		t.Error("a line of text reads as on")
	}
}

// Its variable set, the setting is the deployment's: the write is refused and
// the state says it is pinned.
func TestATextSettingsVariablePinsIt(t *testing.T) {
	t.Setenv(EnvExtras, "")
	t.Setenv(foxtrotEnv, "elsewhere/cron")
	s := New("", threeClusters(), nil)
	if err := s.SetText(alpha, Foxtrot, foxtrotServer, "ops/cron"); !errors.Is(err, ErrTextPinned) || !strings.Contains(err.Error(), foxtrotEnv) {
		t.Errorf("%v", err)
	}
	for _, row := range s.State(alpha) {
		if row.Key == Foxtrot && !row.SettingsPinned[0] {
			t.Errorf("not pinned: %+v", row)
		}
	}
}

// Under the server profile a line of text is the chart's values: the write is
// refused, even for an extra the values name.
func TestATextSettingIsNotWrittenUnderTheServerProfile(t *testing.T) {
	t.Setenv(EnvExtras, Foxtrot)
	t.Setenv(foxtrotEnv, "")
	s := New("", threeClusters(), nil, InCluster())
	if err := s.SetText(alpha, Foxtrot, foxtrotServer, "ops/cron"); !errors.Is(err, ErrTextInCluster) {
		t.Errorf("%v, want %v", err, ErrTextInCluster)
	}
	if got := s.Text(alpha, Foxtrot, foxtrotServer); got != "" {
		t.Errorf("stored %q", got)
	}
}

// A hand-edited file: an unusable value is dropped out loud, one for an
// extension this build does not carry is kept and written back.
func TestTextsFromTheFileAreCheckedAndKept(t *testing.T) {
	t.Setenv(EnvExtras, "")
	t.Setenv(foxtrotEnv, "")
	dir := t.TempDir()
	writeFile(t, dir, `{"format":2,"texts":{"alpha":{"foxtrot":{"server":"refused"},"kafka":{"broker":"b:9092"}}}}`)
	var said []string
	s := New(dir, threeClusters(), func(format string, args ...any) { said = append(said, format) })
	if got := s.Text(alpha, Foxtrot, foxtrotServer); got != "" {
		t.Errorf("a refused value was read: %q", got)
	}
	// The refused value is said; the kept one is not, as keepSetting does not.
	if len(said) != 1 {
		t.Errorf("complaints %v", said)
	}
	if err := s.SetText(beta, Foxtrot, foxtrotServer, "ops/cron"); err != nil {
		t.Fatal(err)
	}
	if doc := readFile(t, dir); doc.Texts[alpha]["kafka"]["broker"] != "b:9092" {
		t.Errorf("a value for an extension this build does not carry was lost: %+v", doc.Texts)
	}
}

// KT_EXTRA_SETTINGS pins checkboxes; it says how a line of text is set instead.
func TestTheSettingsVariableDoesNotPinALineOfText(t *testing.T) {
	pinned, complaints := ParseSettingsEnv(Foxtrot + "." + foxtrotServer + "=on")
	if len(pinned) != 0 || len(complaints) != 1 || !strings.Contains(complaints[0], "set "+foxtrotEnv+" instead") {
		t.Errorf("pinned %v complaints %v", pinned, complaints)
	}
}

// A choice setting accepts only its declared values, and empty (not set).
func TestAChoiceSettingRefusesAnyOtherValue(t *testing.T) {
	declared := Setting{Key: "tool", Text: true, Choices: []Choice{{Value: "auto", Label: "Auto-detect"}, {Value: "flux", Label: "Flux"}}}
	for _, ok := range []string{"", " ", "auto", " flux "} {
		if err := checkText(declared, ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"helm", "Flux", "auto,flux"} {
		err := checkText(declared, bad)
		if err == nil || !strings.Contains(err.Error(), "auto, flux") {
			t.Errorf("%q: %v, want a refusal naming the choices", bad, err)
		}
	}
}

package extras

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every extra is off by default, on every profile. This file is about what that
// means for the store - a choice and its absence, a file written in an earlier
// format, and what KT_EXTRAS means under the server profile.

func writeFile(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir string) stored {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("reading the file: %v", err)
	}
	var doc stored
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing the file: %v\n%s", err, raw)
	}
	return doc
}

// **A fresh context has every extra off, on every profile** - one assertion per
// extra the build carries, on the desktop's store and the server profile's,
// including a cluster the kubeconfig did not hold when the store opened.
func TestAFreshContextHasEveryExtraOffOnEveryProfile(t *testing.T) {
	for name, opts := range map[string][]Option{"desktop and container": nil, "server": {InCluster()}} {
		t.Run(name, func(t *testing.T) {
			// Under the server profile the values name every extra: allowed is
			// not on.
			t.Setenv(EnvExtras, strings.Join(Keys(), ","))
			if opts == nil {
				t.Setenv(EnvExtras, "")
			}
			s := New(t.TempDir(), threeClusters(), nil, opts...)
			for _, cluster := range append(threeClusters(), "added-later") {
				for _, e := range Catalogue() {
					if s.EnabledOn(cluster, e.Key) {
						t.Errorf("%s is on on %s, where nobody chose anything", e.Key, cluster)
					}
				}
			}
			for _, e := range Catalogue() {
				if s.Enabled(e.Key) {
					t.Errorf("Enabled(%s) is true with nothing chosen anywhere", e.Key)
				}
			}
			if s.Any() {
				t.Error("Any is true with nothing chosen anywhere")
			}
			// And a fresh store writes nothing: the default is not a choice.
			if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
				t.Errorf("a fresh store wrote %s before anybody chose anything (%v)", s.Path(), err)
			}
		})
	}
}

// Turned on survives a restart, reaches one cluster, and turned off again is a
// choice written as false.
func TestTurnedOnSurvivesARestartAndReachesOneCluster(t *testing.T) {
	t.Setenv(EnvExtras, "")
	dir := t.TempDir()

	first := New(dir, threeClusters(), nil)
	if err := first.SetOn(alpha, Bravo, true); err != nil {
		t.Fatalf("SetOn: %v", err)
	}
	second := New(dir, threeClusters(), nil)
	if !second.EnabledOn(alpha, Bravo) || !second.Enabled(Bravo) {
		t.Error("an extra turned on did not survive a restart")
	}
	if second.EnabledOn(beta, Bravo) {
		t.Error("turning an extra on on one cluster turned it on on another")
	}
	if second.EnabledOn(alpha, Alfa) {
		t.Error("turning one extra on turned another on on the same cluster")
	}
	if err := second.SetOn(alpha, Bravo, false); err != nil {
		t.Fatal(err)
	}
	doc := readFile(t, dir)
	if on, chosen := doc.On[alpha][Bravo]; !chosen || on {
		t.Errorf("the file does not hold %s=false on %s: %+v", Bravo, alpha, doc.On)
	}
	if _, chosen := doc.On[beta]; chosen {
		t.Errorf("the file holds a choice on %s, where none was made: %+v", beta, doc.On)
	}
}

// **A written false** stays a choice, and so does a true: a file of the current
// format reads exactly as it says.
func TestAFileWrittenUnderD51ReadsAsItSays(t *testing.T) {
	t.Setenv(EnvExtras, "")
	dir := t.TempDir()
	writeFile(t, dir, `{"format":2,"on":{"alpha":{"alfa":true,"bravo":false}}}`)
	s := New(dir, threeClusters(), nil)
	if !s.EnabledOn(alpha, Alfa) || s.EnabledOn(alpha, Bravo) || s.EnabledOn(alpha, Charlie) || s.EnabledOn(beta, Alfa) {
		t.Error("a file of the current format does not read as it says")
	}
}

// On the desktop KT_EXTRAS pins, in both directions.
func TestOnTheDesktopThePinStillWins(t *testing.T) {
	t.Setenv(EnvExtras, Alfa+",bravo=off")
	dir := t.TempDir()
	writeFile(t, dir, `{"format":2,"on":{"alpha":{"bravo":true}}}`)
	s := New(dir, threeClusters(), nil)
	if !s.EnabledOn(alpha, Alfa) || !s.EnabledOn("added-later", Alfa) || !s.Pinned(Alfa) {
		t.Error("an extra the deployment pinned on is off")
	}
	if s.EnabledOn(alpha, Bravo) || s.Enabled(Bravo) {
		t.Error("an extra the deployment pinned off is on because of a choice")
	}
	if !s.Allowed(Charlie) {
		t.Error("on the desktop an extra KT_EXTRAS does not name is not allowed")
	}
	if err := s.SetOn(alpha, Alfa, false); !errors.Is(err, ErrPinned) {
		t.Errorf("turning a pinned extra off: %v, want ErrPinned", err)
	}
}

// **Under the server profile KT_EXTRAS is the chart's values**: it names what
// may be turned on, pins nothing, and an extra it does not
// name - or names off - is off everywhere and refuses every write.
func TestUnderInClusterKTExtrasAllowsAndPinsNothing(t *testing.T) {
	t.Setenv(EnvExtras, Alfa+","+Echo+",bravo=off")
	dir := t.TempDir()
	s := New(dir, []string{alpha}, nil, InCluster())

	for _, key := range []string{Alfa, Echo} {
		if !s.Allowed(key) || s.Pinned(key) || s.EnabledOn(alpha, key) {
			t.Errorf("%s, named by the values: allowed=%v pinned=%v on=%v, want true false false",
				key, s.Allowed(key), s.Pinned(key), s.EnabledOn(alpha, key))
		}
	}
	for _, key := range []string{Bravo, Charlie} {
		if s.Allowed(key) || s.Pinned(key) {
			t.Errorf("%s, not named on: allowed=%v pinned=%v, want false false", key, s.Allowed(key), s.Pinned(key))
		}
		if err := s.SetOn(alpha, key, true); !errors.Is(err, ErrNotInValues) {
			t.Errorf("turning on %s: %v, want ErrNotInValues", key, err)
		}
		if err := s.SetOn(alpha, key, false); !errors.Is(err, ErrNotInValues) {
			t.Errorf("turning off %s: %v, want ErrNotInValues", key, err)
		}
	}
	if err := s.SetSetting(alpha, Echo, echoColumn, false); err != nil {
		t.Errorf("a setting of an allowed extra was refused: %v", err)
	}
	t.Setenv(EnvExtras, Alfa)
	narrow := New("", []string{alpha}, nil, InCluster())
	if err := narrow.SetSetting(alpha, Echo, echoColumn, false); !errors.Is(err, ErrNotInValues) {
		t.Errorf("a setting of an extra the values do not name: %v, want ErrNotInValues", err)
	}
	for _, row := range narrow.State(alpha) {
		want := row.Key == Alfa
		if row.Allowed != want {
			t.Errorf("State: %s allowed=%v, want %v", row.Key, row.Allowed, want)
		}
		if !want && row.Note != NotInValues {
			t.Errorf("State: %s carries the note %q, want %q", row.Key, row.Note, NotInValues)
		}
	}

	// Allowed and turned on: kept, and read back after a restart.
	t.Setenv(EnvExtras, Alfa+","+Echo+",bravo=off")
	if err := s.SetOn(alpha, Alfa, true); err != nil {
		t.Fatalf("SetOn: %v", err)
	}
	again := New(dir, []string{alpha}, nil, InCluster())
	if !again.EnabledOn(alpha, Alfa) || again.EnabledOn(alpha, Echo) {
		t.Error("the choice made under the server profile did not survive a restart as it was")
	}
	// The values stop naming it: off, and the choice is kept for when they do.
	t.Setenv(EnvExtras, Echo)
	if New(dir, []string{alpha}, nil, InCluster()).EnabledOn(alpha, Alfa) {
		t.Error("an extra the values no longer name is still on")
	}
}

// **Existing saved choices are honored**: a file in the ticks-only format holds
// only ticks, which is all there is to read since absent is off. It is not
// rewritten until a write is asked for.
func TestATicksOnlyFileIsReadByItsTicks(t *testing.T) {
	t.Setenv(EnvExtras, "")
	dir := t.TempDir()
	// The old spelling of Delta, too: an alias is folded on read.
	body := `{"on":{"alpha":{"alfa":true,"golf":true,"bravo":false}}}`
	writeFile(t, dir, body)

	s := New(dir, threeClusters(), nil)
	for key, want := range map[string]bool{Alfa: true, Delta: true, Bravo: false, Charlie: false, Echo: false, Foxtrot: false} {
		if got := s.EnabledOn(alpha, key); got != want {
			t.Errorf("on %s, %s = %v; the file had it %v", alpha, key, got, want)
		}
	}
	if s.EnabledOn(beta, Alfa) {
		t.Error("a cluster the old file did not name has an extra on")
	}
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != body {
		t.Errorf("the file was rewritten with no write asked for:\n%s", raw)
	}
}

// The oldest format, global switches with per-cluster exclusions: every listed
// cluster keeps its ticks, and its exclusions stay off.
func TestAGlobalFormatFileKeepsItsTicks(t *testing.T) {
	t.Setenv(EnvExtras, "")
	dir := t.TempDir()
	writeFile(t, dir, `{"enabled":{"alfa":true,"bravo":false},"off":{"gamma":{"alfa":true}}}`)

	s := New(dir, threeClusters(), nil)
	if !s.EnabledOn(alpha, Alfa) || s.EnabledOn(gamma, Alfa) {
		t.Error("the global tick or its exclusion was not carried across")
	}
	if s.EnabledOn(alpha, Bravo) || s.EnabledOn("added-later", Alfa) {
		t.Error("an extra the old file did not tick there is on")
	}
}

// --- Settings (extension.Configurable) -------------------------------------------

func TestASettingIsItsDefaultUntilChosenAndThenPerCluster(t *testing.T) {
	t.Setenv(EnvExtras, "")
	dir := t.TempDir()
	s := New(dir, threeClusters(), nil)

	if !s.Setting(alpha, Echo, echoColumn) || s.Setting(alpha, Echo, echoCompact) {
		t.Fatal("a setting nobody chose does not read as its declared default")
	}
	if err := s.SetSetting(alpha, Echo, echoColumn, false); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	again := New(dir, threeClusters(), nil)
	if again.Setting(alpha, Echo, echoColumn) {
		t.Error("a setting turned off did not survive a restart")
	}
	if !again.Setting(beta, Echo, echoColumn) {
		t.Error("a setting chosen on one cluster changed another")
	}
	state := again.State(alpha)
	for _, row := range state {
		if row.Key == Foxtrot {
			continue // a line of text: texts_test.go
		}
		if row.Key != Echo {
			if len(row.Values) != 0 {
				t.Errorf("%s declares no setting and carries values %v", row.Key, row.Values)
			}
			continue
		}
		if len(row.Values) != 2 || row.Values[0] || row.Values[1] {
			t.Errorf("State carries %v for Echo on %s, want [false false]", row.Values, alpha)
		}
	}
	doc := readFile(t, dir)
	if v, chosen := doc.Settings[alpha][Echo][echoColumn]; !chosen || v {
		t.Errorf("the file does not hold the setting: %+v", doc.Settings)
	}
}

func TestSetSettingRefusesWhatItCannotDo(t *testing.T) {
	t.Setenv(EnvExtras, Echo+"=off")
	s := New("", threeClusters(), nil)

	if err := s.SetSetting(alpha, "kafka", echoColumn, true); !errors.Is(err, ErrUnknown) {
		t.Errorf("a key that is not an extra: %v, want ErrUnknown", err)
	}
	if err := s.SetSetting(alpha, Echo, "nope", true); !errors.Is(err, ErrUnknownSetting) {
		t.Errorf("a setting Echo does not declare: %v, want ErrUnknownSetting", err)
	} else if !strings.Contains(err.Error(), echoColumn) {
		t.Errorf("the refusal does not name what Echo offers: %v", err)
	}
	if err := s.SetSetting(alpha, Alfa, echoColumn, true); !errors.Is(err, ErrUnknownSetting) {
		t.Errorf("a setting on an extra that offers none: %v, want ErrUnknownSetting", err)
	}
	if err := s.SetSetting("", Echo, echoColumn, true); err == nil {
		t.Error("a setting with no cluster was accepted")
	}
	// Pinned off is not a refusal: the pin decides whether the page is there,
	// the setting how it is drawn.
	if err := s.SetSetting(alpha, Echo, echoColumn, false); err != nil {
		t.Errorf("a setting of a pinned extra was refused: %v", err)
	}
}

func TestASettingTheBuildDoesNotDeclareIsDroppedOutLoud(t *testing.T) {
	t.Setenv(EnvExtras, "")
	dir := t.TempDir()
	writeFile(t, dir, `{"format":2,"settings":{"alpha":{"echo":{"list-column":false,"gone":true},"alfa":{"x":true}}}}`)

	var said []string
	s := New(dir, threeClusters(), func(format string, args ...any) { said = append(said, format) })
	if s.Setting(alpha, Echo, echoColumn) {
		t.Error("a declared setting in the file was not read")
	}
	if got := len(said); got != 2 {
		t.Errorf("%d complaints for two undeclared settings: %v", got, said)
	}
}

// **What this build does not carry is kept, never honored**: a binary that no
// longer carries an extra keeps the operator's choices for the build that does
// again - through a ticks-only later write.
func TestChoicesAboutAnExtensionThisBuildDoesNotCarryAreKept(t *testing.T) {
	t.Setenv(EnvExtras, "")
	dir := t.TempDir()
	writeFile(t, dir, `{"on":{"alpha":{"alfa":true,"kafka":true}},"settings":{"alpha":{"kafka":{"wide":false}}}}`)

	s := New(dir, threeClusters(), nil)
	if s.EnabledOn(alpha, "kafka") || s.Enabled("kafka") {
		t.Error("a key this build does not carry was honored")
	}
	if err := s.SetOn(beta, Bravo, false); err != nil {
		t.Fatal(err)
	}
	doc := readFile(t, dir)
	if on, kept := doc.On[alpha]["kafka"]; !kept || !on {
		t.Errorf("the tick for an extension this build does not carry was dropped: %+v", doc.On)
	}
	if v, kept := doc.Settings[alpha]["kafka"]["wide"]; !kept || v {
		t.Errorf("the setting of an extension this build does not carry was dropped: %+v", doc.Settings)
	}
	if on, chosen := doc.On[alpha][Alfa]; !chosen || !on {
		t.Errorf("keeping the unknown key cost the known one on the same cluster: %+v", doc.On[alpha])
	}
	if on, chosen := doc.On[beta][Bravo]; !chosen || on {
		t.Errorf("the write itself was lost: %+v", doc.On)
	}
}

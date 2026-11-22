package registry

import (
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/extras"
)

// page is an extension with nothing to mount, enough to be registered.
type page struct{ key string }

func (p page) Key() string                           { return p.key }
func (p page) Label() string                         { return "Page " + p.key }
func (page) Summary() string                         { return "A fixture page." }
func (page) Needs() string                           { return "nothing" }
func (page) Legacy() []string                        { return nil }
func (page) Routes(extension.Host) []extension.Route { return nil }

// configurable offers whatever settings it is given.
type configurable struct {
	page
	settings []extension.Setting
}

func (c configurable) Settings() []extension.Setting { return c.settings }

// What an extension offers reaches the catalog the Settings page draws from,
// in its declared order, and one that offers nothing carries nothing.
func TestTheSettingsAnExtensionOffersReachTheCatalogue(t *testing.T) {
	Reset()
	t.Cleanup(Reset)

	offered := []extension.Setting{
		{Key: "list-column", Label: "Show the column", Summary: "Off, no column.", Default: true},
		{Key: "compact", Label: "Compact", Default: false},
	}
	if err := Register(page{key: "plain"}, configurable{page{key: "rich"}, offered}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got := map[string]extras.Extra{}
	for _, e := range extras.Catalogue() {
		got[e.Key] = e
	}
	if n := len(got["plain"].Settings); n != 0 {
		t.Errorf("an extension that offers nothing carries %d settings", n)
	}
	rich := got["rich"].Settings
	if len(rich) != 2 || rich[0].Key != "list-column" || !rich[0].Default || rich[1].Key != "compact" || rich[1].Default {
		t.Errorf("the catalog holds %+v, want the two settings as declared, in order", rich)
	}
}

// A setting that could be drawn and never saved, or two that write one value,
// is refused at start-up - and nothing of the batch is registered.
func TestASettingThatCannotBeStoredIsRefused(t *testing.T) {
	for name, settings := range map[string][]extension.Setting{
		"upper case": {{Key: "List", Label: "x"}},
		"empty key":  {{Key: "", Label: "x"}},
		"a slash":    {{Key: "a/b", Label: "x"}},
		"twice":      {{Key: "same", Label: "x"}, {Key: "same", Label: "y"}},
		"no label":   {{Key: "fine", Label: "  "}},
		"one letter": {{Key: "a", Label: "x"}},
		"trailing -": {{Key: "column-", Label: "x"}},
	} {
		t.Run(name, func(t *testing.T) {
			Reset()
			t.Cleanup(Reset)
			err := Register(page{key: "first"}, configurable{page{key: "broken"}, settings})
			if err == nil {
				t.Fatal("registered")
			}
			if !strings.Contains(err.Error(), "broken") {
				t.Errorf("the refusal does not name the extension: %v", err)
			}
			if len(extras.Catalogue()) != 0 || len(Extensions()) != 0 {
				t.Error("part of a refused batch was registered")
			}
		})
	}
}

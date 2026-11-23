package web

import (
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/registry"
)

// The extensions these tests run against.
//
// **Deliberately not the keys of any real page.** This repository carries none:
// the catalog is registered at startup by whatever binary holds the pages, and
// a fixture spelling the real keys would put the list of what is sold back into
// the public repository through the test suite. Every assertion here is about
// the *mechanism* — the gate, the mounting, the settings grid — and none of them
// is about which pages exist.
const (
	fixtureKey      = "alfa"
	fixtureLabel    = "Alfa Center"
	fixtureOtherKey = "bravo"
	// fixtureLegacyKey is the key the first fixture was published under before:
	// a grant written for it must keep granting the page.
	fixtureLegacyKey = "alpha"
)

// fixtureExtension is an extension with three routes of the three shapes the
// router has to handle: a page, a sub-path, and a route registered with no
// method at all — which is what a WebSocket upgrade needs, an upgrade not being
// a verb.
type fixtureExtension struct {
	key, label string
	patterns   []extension.Route
	legacy     []string
}

func (e fixtureExtension) Key() string      { return e.key }
func (e fixtureExtension) Label() string    { return e.label }
func (e fixtureExtension) Summary() string  { return "A fixture page, for the tests of the mechanism." }
func (e fixtureExtension) Needs() string    { return "nothing" }
func (e fixtureExtension) Legacy() []string { return e.legacy }

// Configured names, for every fixture, the Secret shop/db and the Service
// shop/api: what ConfiguredSecret and ConfiguredService may read for it.
func (e fixtureExtension) Configured(string, func(string) string) extension.Configuration {
	return extension.Configuration{
		Secrets:  []extension.ObjectName{{Namespace: "shop", Name: "db"}},
		Services: []extension.ObjectName{{Namespace: "shop", Name: "api"}},
	}
}

func (e fixtureExtension) Routes(h extension.Host) []extension.Route {
	if e.patterns != nil {
		return e.patterns
	}
	answer := func(what string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The host is used rather than ignored, so that a Host method
			// removed from the contract fails this file at compile time rather
			// than quietly at whatever release Extra next builds against.
			_ = h.ConfirmToken(r)
			fmt.Fprintf(w, "%s reached %s", e.key, what)
		})
	}
	return []extension.Route{
		{Method: http.MethodGet, Pattern: "/{$}", Handler: answer("the page")},
		{Method: http.MethodGet, Pattern: "/panel", Handler: answer("the panel")},
		{Pattern: "/socket", Handler: answer("the socket")},
	}
}

// configurableFixture is a fixture extension that offers one setting
// (extension.Configurable), so the listing and the settings write are tested
// over what registration really produces.
type configurableFixture struct{ fixtureExtension }

// fixtureSetting is the one setting the configurable fixture offers.
const fixtureSetting = "list-column"

func (configurableFixture) Settings() []extension.Setting {
	return []extension.Setting{{
		Key: fixtureSetting, Label: "Show the Bravo column in the lists",
		Summary: "Off, the lists draw no Bravo column.", Default: true,
	}}
}

// fixtureSuggestingKey is the fixture that can say whether a cluster runs what
// it reads (extension.Suggester), as the Redis viewer does.
const fixtureSuggestingKey = "charlie"

type suggestingFixture struct{ fixtureExtension }

// suggested is what the suggesting fixture answers, and what it was asked:
// set by a test, read back by it.
var suggested struct {
	sync.Mutex
	evidence string
	err      error
	asked    []*http.Request
}

func (suggestingFixture) Suggest(r *http.Request, _ extension.Host, _ string) (string, error) {
	suggested.Lock()
	defer suggested.Unlock()
	suggested.asked = append(suggested.asked, r)
	return suggested.evidence, suggested.err
}

// Component names what the fixture looks for, so a cluster that runs none of it
// is reported absent, not silent.
func (suggestingFixture) Component() string { return "charlie" }

// TestMain registers the fixture extensions once, before any test runs.
//
// Process-wide and additive by design — nothing in the product unregisters a
// page — so it happens here rather than per test. A test that needs an *empty*
// catalog calls registry.Reset and says in its own comment why.
func TestMain(m *testing.M) {
	if err := registry.Register(
		fixtureExtension{key: fixtureKey, label: fixtureLabel, legacy: []string{fixtureLegacyKey}},
		configurableFixture{fixtureExtension{key: fixtureOtherKey, label: "Bravo Center"}},
		suggestingFixture{fixtureExtension{key: fixtureSuggestingKey, label: "Charlie viewer"}},
	); err != nil {
		fmt.Fprintln(os.Stderr, "registering the fixture extensions:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

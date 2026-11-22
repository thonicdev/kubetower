package extras

import (
	"errors"
	"os"
	"testing"
)

// The catalog these tests run against.
//
// **Deliberately not the keys of any real page.** The catalog is registered at
// startup by whatever binary carries the pages, so this package has no keys of
// its own — and a fixture that spelled the real ones would put the list
// of what is sold into the public repository through the back door, which is the
// one thing the repository split exists to prevent. The tests are about the
// store, the file, the environment grammar and the per-cluster axis; none of
// them is about which pages exist.
const (
	// The five the tests exercise. The names are phonetic so that a failure
	// message naming one cannot be read as a claim about a product.
	Alfa    = "alfa"
	Bravo   = "bravo"
	Charlie = "charlie"
	Delta   = "delta"
	Echo    = "echo"
	Foxtrot = "foxtrot"
)

// Two of the fixture pages were "published" under an older key, which is what
// exercises the alias path. Same shape as a real rename: one-way, folded on
// read, never written back.
const (
	legacyDelta   = "golf"
	legacyCharlie = "hotel"
)

// The fixture settings of Echo.
const (
	echoColumn  = "list-column"
	echoCompact = "compact"
)

// The fixture text setting of Foxtrot, and its variable.
const (
	foxtrotServer = "server"
	foxtrotEnv    = "KT_FOXTROT_SERVER"
)

// TestMain registers the fixture catalog once, before any test runs.
//
// Registration is process-wide and additive by design — nothing in the product
// un-registers a page — so it happens here rather than per test. A test that
// needs an *empty* catalog calls Reset and registers again itself, and says
// why in its own comment.
func TestMain(m *testing.M) {
	Register(
		Extra{Key: Alfa, Label: "Alfa page", Summary: "The first fixture page.", Needs: "nothing"},
		Extra{Key: Bravo, Label: "Bravo page", Summary: "The second fixture page.", Needs: "nothing"},
		Extra{Key: Charlie, Label: "Charlie", Summary: "The third fixture page.", Needs: "nothing",
			Legacy: []string{legacyCharlie}},
		Extra{Key: Delta, Label: "Delta", Summary: "The fourth fixture page.", Needs: "nothing",
			Legacy: []string{legacyDelta}},
		Extra{Key: Echo, Label: "Echo viewer", Summary: "The fifth fixture page.", Needs: "nothing",
			// The one fixture with settings (extension.Configurable): one on by
			// default and one off, so a test can tell a default from a choice
			// in both directions.
			Settings: []Setting{
				{Key: echoColumn, Label: "Show the Echo column", Default: true},
				{Key: echoCompact, Label: "Compact", Default: false},
			}},
		Extra{Key: Foxtrot, Label: "Foxtrot viewer", Summary: "The sixth fixture page.", Needs: "nothing",
			// The fixture with a line of text: a variable that decides it, and
			// a check that refuses one word.
			Settings: []Setting{{Key: foxtrotServer, Label: "Server", Text: true, Placeholder: "found by itself",
				Env: foxtrotEnv, Check: func(v string) error {
					if v == "refused" {
						return errors.New("that word is refused")
					}
					return nil
				}}}},
	)
	os.Exit(m.Run())
}

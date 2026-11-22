package web

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/access"
)

// The menu carries the extras a person may use (extrasInMenu), reviewed by the
// gate and held there; the browser holds the menu itself for MENU_FRESH_MS
// (`navQuery`). Kept longer in the browser than the server keeps a review it
// could not make, a review that came back, a grant or a revocation reaches the
// menu late by the difference: it was sixty seconds against ten.
//
// Neither language can import the other's constant, so this reads the
// TypeScript file as text, as extensionkey_test.go in internal/api does.
func TestTheMenuIsKeptNoLongerThanTheServerKeepsItsReview(t *testing.T) {
	const source = "../../web/ui/src/api/queries.ts"

	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("reading %s: %v", source, err)
	}

	// The declaration, not any mention of it: a comment quoting an old value
	// would otherwise satisfy this.
	found := regexp.MustCompile(`(?m)^export const MENU_FRESH_MS = ([0-9_]+);$`).FindSubmatch(raw)
	if found == nil {
		t.Fatalf("%s does not declare MENU_FRESH_MS as one number", source)
	}
	ms, err := strconv.Atoi(strings.ReplaceAll(string(found[1]), "_", ""))
	if err != nil {
		t.Fatalf("MENU_FRESH_MS = %s: %v", found[1], err)
	}
	if limit := access.FailedTTL.Milliseconds(); ms <= 0 || int64(ms) > limit {
		t.Errorf("the browser keeps the menu %d ms, the server keeps a failed review %d ms; it must be kept no longer", ms, limit)
	}

	// And navQuery reads it, rather than a figure of its own.
	nav := regexp.MustCompile(`(?s)export function navQuery\(.*?\n}\n`).Find(raw)
	if nav == nil || !strings.Contains(string(nav), "staleTime: MENU_FRESH_MS,") {
		t.Errorf("navQuery does not keep the menu for MENU_FRESH_MS: %s", nav)
	}
}

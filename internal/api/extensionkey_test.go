package api

import (
	"os"
	"regexp"
	"testing"
)

// The console refuses to mount an extension whose key the API would refuse:
// `buildRoutes` holds every key to `KEY_PATTERN`, a copy
// of `extensionPattern` here. Neither language can import the other's constant,
// so this reads the TypeScript file as text, as basename_test.go in
// internal/web does for the console's base address. Widen one copy and not the
// other, and a key the API accepts becomes a console that does not start - or a
// key the console mounts becomes a page whose every read is refused.
func TestTheConsoleHoldsExtensionKeysToTheAPIPattern(t *testing.T) {
	const source = "../../web/ui/src/router/routes.tsx"

	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("reading %s: %v", source, err)
	}

	// The declaration, not any mention of it: a comment quoting an old value
	// would otherwise satisfy this.
	found := regexp.MustCompile(`(?m)^const KEY_PATTERN = /(.+)/;$`).FindSubmatch(raw)
	if found == nil {
		t.Fatalf("%s does not declare KEY_PATTERN as one regular-expression literal", source)
	}
	if got, want := string(found[1]), extensionPattern.String(); got != want {
		t.Errorf("the console's KEY_PATTERN is /%s/, the API's extensionPattern is %q; the two must be the same pattern", got, want)
	}
}

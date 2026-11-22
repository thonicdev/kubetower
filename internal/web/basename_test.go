package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The client's basename and the address this package mounts the shell at are one
// fact in two languages, and this is the check that they agree.
//
// # Why it matters
//
// If this package serves the console at `/ui/` while `createApp.tsx` builds its
// router with no `basename`, every route in the built bundle is rooted at `/`
// and matches nothing: signing in produces React Router's own `ErrorResponse`
// on the boundary, rendered as *This page failed to load.* and
// `[object Object]`. Unit tests and a green pipeline do not notice; only
// driving the built bundle does.
//
// # Why this reads a TypeScript file as text
//
// Because neither language can import the other's constant, and the alternative
// is a comment asking two files to be kept in step. If the console moves to
// another address, `spaPrefix` here and `CONSOLE_BASE` there change, and this
// test is what makes a change to one of them and not the other fail loudly
// instead of shipping a console that does not open.
func TestTheClientRoutesAtTheAddressThisPackageServes(t *testing.T) {
	const source = "../../web/ui/src/router/base.ts"

	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("reading %s: %v", source, err)
	}

	// The declaration, not any mention of it: a comment naming a value would
	// otherwise satisfy this.
	// `SERVED_AT` and not `CONSOLE_BASE`: the exported constant is
	// `import.meta.env.DEV ? '/' : SERVED_AT`, because the dev server serves
	// the application at `/`. This test is about the **built** console, which
	// is the half this package serves.
	found := regexp.MustCompile(`const SERVED_AT = '([^']*)';`).FindSubmatch(raw)
	if found == nil {
		t.Fatalf("%s does not declare SERVED_AT as a single-quoted string literal", source)
	}
	client := string(found[1])

	// `spaPrefix` carries a trailing slash because it is a mux pattern;
	// `basename` must not, because React Router joins it to paths that begin
	// with one. So the comparison is on the meaningful part rather than on the
	// spelling, and **both sides are trimmed** - which matters when the
	// server serves `/` and the client routes at `/`: trimming only one side
	// would make them read as `""` and `/`.
	server := strings.TrimSuffix(spaPrefix, "/")
	if strings.TrimSuffix(client, "/") != server {
		t.Fatalf(
			"the client routes at %q and this package serves the shell at %q; "+
				"a console mounted under a prefix its router does not know about matches no route at all",
			client, spaPrefix,
		)
	}
}

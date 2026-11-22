/// <reference types="vite/client" />
//
// This file reads `import.meta.env.DEV`, so it names Vite's ambient types
// itself rather than relying on whichever project compiles it.

/**
 * Where this console is mounted, as one string.
 *
 * A router whose basename does not match where the server mounts the shell
 * matches no address at all: signing in lands on a path with no route, and the
 * boundary renders React Router's own `ErrorResponse`. Unit tests build their
 * own router with no basename, so only a browser driving the built bundle
 * would see it.
 *
 * ## And it is two addresses, because the console is served at two
 *
 * The built bundle is served by `internal/web/spa.go`. **The Vite dev server
 * serves it at `/`.** Exporting the production string unconditionally would
 * give `task dev` a router that matches nothing and renders an empty document
 * - no error, no boundary, nothing on screen.
 *
 * `import.meta.env.DEV` is Vite's own answer to *which of the two am I*, decided
 * at build time and constant-folded away, so the production bundle carries no
 * branch.
 *
 * ## Why a constant here rather than an argument at the call site
 *
 * So that moving the shell is one edit, and `internal/web/basename_test.go`
 * fails if the two sides disagree. That test reads this file as text, which is
 * unusual and is the point: neither language can import the other's constant,
 * and a comment saying "keep these in step" is not a check.
 *
 * ## Not Vite's `base`
 *
 * Setting `base` would move the bundle's own asset URLs with it, and `spa.go`
 * deliberately serves those at `/assets/` - the address the bundle already
 * names - so that no HTML is rewritten on the way out. Where the assets live
 * and where the shell is mounted are two different questions, and this is only
 * the second one.
 */
/**
 * Where the **built** console is mounted, and the one string
 * `internal/web/spa.go` has to agree with. `basename_test.go` reads this
 * declaration out of this file as text and fails when the two drift.
 */
const SERVED_AT = '/';

/** The basename this build's router runs under. `/` under the dev server, which
 *  serves the application at the root; `SERVED_AT` in the bundle the Go binary
 *  embeds. */
export const CONSOLE_BASE = import.meta.env.DEV ? '/' : SERVED_AT;

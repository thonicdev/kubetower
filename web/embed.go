// Package web carries the compiled front end into the binary.
//
// One variable and no behavior, and that is deliberate: a `go:embed` directive
// can only name files in its own directory tree, so the embed has to live here,
// beside `app/`, `ui/` and `dist/` — while every decision about *serving* what
// is embedded belongs next to the router it is mounted on, in
// internal/web/spa.go. Splitting them this way means the front-end directory
// holds no serving policy and the serving policy holds no build layout.
//
// Public rather than internal because a binary built in another module may
// hand its own bundle to the console (`app.Options.UI`), and Go forbids
// importing `internal/` across a module. The extras' pages are in
// this bundle (`web/app/extras/`), so `cmd/kubetower` serves it as it is.
package web

import "embed"

// Assets is `web/dist`. Vite writes the bundle to `web/dist/ui`, so everything
// served out of here is under `dist/ui/`.
//
// **`all:` is load-bearing, and so is the placeholder it exists for.** Without
// the prefix the directive skips any file whose name begins with `.` or `_`,
// and `web/dist/.gitkeep` is exactly such a file — the committed placeholder
// that lets a backend contributor build and test with no Node installed. Remove
// either and `go build ./...` fails on a tree that has never run `pnpm build`.
//
// **And that is the trap this variable cannot solve on its own:** because the
// placeholder satisfies the directive, the embed **cannot** fail when the bundle
// is missing. A binary carrying no front end compiles, links and starts happily.
// What tells the two apart is the bundle check in internal/web/spa.go, run at
// startup and asserted by a test (docs/DEFINITION-OF-DONE.md §14).
//
//go:embed all:dist
var Assets embed.FS

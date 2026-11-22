package main

// The console a shipped binary has to carry.
//
// `//go:embed all:dist` is satisfied by the committed web/dist/.gitkeep, so a
// binary built without the front end compiles and opens on "The front end is
// not in this binary". The placeholder is deliberate (go build and go test need
// no Node), but it must not reach a person:
//
//   - Before linking, a build that ships (`-setup` or any `-version`) refuses
//     when web/dist/ui/index.html is missing or empty. Unstamped builds are
//     untouched.
//   - After linking, checkEmbedsConsole finds the page Vite writes and the
//     entry chunk it names in the embedded file table. `-check-console FILE`
//     runs the same read-back on a binary built elsewhere.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// consoleIndex is the page internal/web/spa.go looks for, from the module root.
const consoleIndex = "web/dist/ui/index.html"

// consoleEntry is the line Vite writes into the built index.html for the entry
// chunk; neither the source index.html nor the "not built" page has it. The hash
// is captured so the chunk itself can be looked for.
var consoleEntry = regexp.MustCompile(`<script type="module"[^>]*\ssrc="/assets/(index-[0-9A-Za-z_-]{6,}\.js)"`)

// shipsConsole reports whether this build is one a person runs: the setup, or
// a stamped version. Those refuse a tree whose console was never built.
func (t target) shipsConsole() bool {
	return t.setup || t.version != ""
}

// consoleBuilt refuses a tree whose console was never built.
func consoleBuilt(root string) error {
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(consoleIndex)))
	if err == nil && !info.IsDir() && info.Size() > 0 {
		return nil
	}
	return fmt.Errorf("%s is missing or empty, so the binary would embed only the web/dist/.gitkeep placeholder "+
		"and open on \"The front end is not in this binary\". A -setup or -version build is one that ships, "+
		"and it refuses that. Build the console first: `task build:web` and `task build:notices` "+
		"(or `corepack pnpm -C web run build`), then run this again", consoleIndex)
}

// checkEmbedsConsole reads a built kubetower binary back and refuses it unless
// it embeds the built index.html and the entry chunk that page loads (an index
// naming absent code renders nothing). Go's embed stores both names and bytes
// uncompressed, so a search of the file finds them.
func checkEmbedsConsole(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	m := consoleEntry.FindSubmatch(raw)
	if m == nil {
		return errors.New("it does not embed the console: no built web/dist/ui/index.html in it, so it " +
			"opens on \"The front end is not in this binary\"")
	}
	chunk := append([]byte("dist/ui/assets/"), m[1]...)
	if !bytes.Contains(raw, chunk) {
		return fmt.Errorf("its index.html loads /assets/%s, and the binary embeds no %s", m[1], chunk)
	}
	return nil
}

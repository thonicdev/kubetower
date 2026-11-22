package launch

import (
	"fmt"
	"net/url"
	"strings"
)

// What the macOS window does with a `kubetower://` link once macOS has handed
// it over, between the validation (urlscheme.go) and the sending
// (nativewindow_darwin.go). No build tag, so it is tested on Linux; the darwin
// file only receives the Apple Event, calls arrive and windowReady, and runs
// openPageScript on what they return.
//
// A link that starts the application arrives before the window can show it.
// The first page is the one-time sign-in address, answered with a cookie and a
// redirect; a route change run before it loads would run in the blank document,
// and navigateScript's fallback would cancel the sign-in in flight, spending the
// code. So a link arriving before the first page has loaded is held, and opened
// once it has. With several, the last wins (the log says which it replaced):
// the window shows one page.
//
// It only ever sends a path consolePathFromURL returned, only through
// openPageScript, the same native→web door as the Cluster menu and ⌘,. Nothing
// reads from the page.

// schemeLinks is the window's state for links. Main thread only (every caller
// is an AppKit callback), so it carries no lock.
type schemeLinks struct {
	// ready is true once the window's first page has finished loading (signed
	// in, or failed onto the sign-in page), for the life of the window.
	ready bool
	// held is the console path of the last link accepted before ready, or "".
	held string
}

// arrive takes one address macOS handed over and returns the console path to
// open now ("" for nothing) and the line to log, which is never empty: a link
// opened, held or refused is always said.
//
// A refusal changes nothing, not even a held link: a refused address is not a
// choice of page.
func (l *schemeLinks) arrive(raw string) (open, line string) {
	path, err := consolePathFromURL(raw)
	if err != nil {
		return "", fmt.Sprintf("a %s:// link was refused and nothing changed: %v (the link: %s)",
			urlScheme, err, loggableLink(raw))
	}
	if l.ready {
		return path, fmt.Sprintf("a %s:// link opened %s", urlScheme, path)
	}
	line = fmt.Sprintf("a %s:// link for %s arrived before the window had signed in; it opens once it has",
		urlScheme, path)
	if l.held != "" && l.held != path {
		line += fmt.Sprintf(", instead of %s, which an earlier link asked for", l.held)
	}
	l.held = path
	return "", line
}

// windowReady records that the window's first page has finished loading and
// returns the held path to open now, or "" with nothing held. Called on every
// finished navigation; only the first can return anything.
func (l *schemeLinks) windowReady() (open, line string) {
	if l.ready {
		return "", ""
	}
	l.ready = true
	open, l.held = l.held, ""
	if open == "" {
		return "", ""
	}
	return open, fmt.Sprintf("the window has signed in, so the %s:// link held for %s opens now", urlScheme, open)
}

// maxLoggedLink bounds how much of a refused address reaches the log, so a
// megabyte-long one is one readable line.
const maxLoggedLink = 256

// loggableLink is a refused address as the log may carry it: through
// loggableURL (no query, fragment or user information, which are not this
// console's to copy into a bug report), cut to maxLoggedLink bytes and quoted
// with %q so a control character cannot forge a line. An address that does not
// parse skips loggableURL, whose answer for that case names the console.
func loggableLink(raw string) string {
	if raw == "" {
		return "empty"
	}
	shown := ""
	if _, err := url.Parse(raw); err != nil {
		shown = raw
		if i := strings.IndexAny(shown, "?#"); i >= 0 {
			shown = shown[:i]
		}
	} else {
		shown = loggableURL(raw)
	}
	if len(shown) > maxLoggedLink {
		shown = shown[:maxLoggedLink] + "…"
	}
	return fmt.Sprintf("%q", shown)
}

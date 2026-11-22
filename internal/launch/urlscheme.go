package launch

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// The `kubetower://` scheme: a link that brings the application forward on a
// page of the console, and never acts.
//
// This file is the validation only, untagged so it is tested on every host.
// The macOS half (CFBundleURLTypes in infoPlist, the kAEGetURL Apple Event in
// nativewindow_darwin.go, nativewindowlink.go) passes whatever the system handed
// it to consolePathFromURL and navigates the window to the returned path through
// the native→web door; nothing else reaches the window.
//
// Any website can put such a link on a page, so the worst a link may do is
// choose which page is shown. Hence a positive list of read-only pages:
//
//   - `/`, `/settings`, `/settings/<tab>`, and `/health` (which the SPA
//     redirects to `/`, kept so an old link still opens);
//   - `/c/<cluster>`, `/c/<cluster>/<kind>`, `/c/<cluster>/<kind>/<name>` and
//     `/c/<cluster>/<kind>/<namespace>/<name>`, Helm's included.
//
// Everything else is refused:
//
//   - any query or fragment, which a page could read as an instruction;
//   - other shapes: `/api/`, `/auth/` and `/login` are not pages, and
//     `/c/<cluster>/x/…` is an extension's, not this core's to vouch for;
//   - a segment that is empty, `.` or `..`, or holds a slash, a backslash, `?`,
//     `#` or a control character once decoded (an encoded slash in a cluster
//     name is refused rather than guessed at);
//   - user information, a port, an over-long address, any other scheme.
//
// A refusal is an error saying why, for the caller to log with %q, so a control
// character in the address cannot forge a line.

// urlScheme is the scheme this application registers.
const urlScheme = "kubetower"

// maxSchemeURL bounds the address and maxSegment each segment. A Kubernetes name
// is at most 253 characters; a kubeconfig context name has no limit, but one
// longer than this is not one somebody will link to.
const (
	maxSchemeURL = 2048
	maxSegment   = 512
)

// consolePathFromURL turns a `kubetower://` address into the console path to
// show, or refuses it. The returned path is re-encoded segment by segment, so
// what the window navigates to is spelled by this function and not by the link.
func consolePathFromURL(raw string) (string, error) {
	if len(raw) > maxSchemeURL {
		return "", fmt.Errorf("the address is %d characters long; at most %d are accepted", len(raw), maxSchemeURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("not an address: %w", err)
	}
	if !strings.EqualFold(u.Scheme, urlScheme) {
		return "", fmt.Errorf("the scheme is %q, not %s", u.Scheme, urlScheme)
	}
	if u.User != nil {
		return "", errors.New("the address carries user information")
	}
	if u.Port() != "" {
		return "", errors.New("the address carries a port")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", errors.New("the address carries a query; a link may choose a page and nothing else")
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return "", errors.New("the address carries a fragment; a link may choose a page and nothing else")
	}

	// `kubetower://c/prod/pods` parses with the first segment as the host;
	// `kubetower:///c/prod/pods` and `kubetower:c/prod/pods` do not. All three
	// name the same page.
	var escaped string
	switch {
	case u.Opaque != "":
		escaped = u.Opaque
	case u.Host != "":
		escaped = u.Host + u.EscapedPath()
	default:
		escaped = u.EscapedPath()
	}
	escaped = strings.TrimPrefix(escaped, "/")
	escaped = strings.TrimSuffix(escaped, "/")

	var segments []string
	if escaped != "" {
		for _, part := range strings.Split(escaped, "/") {
			segment, err := schemeSegment(part)
			if err != nil {
				return "", err
			}
			segments = append(segments, segment)
		}
	}
	if !readOnlyPage(segments) {
		return "", fmt.Errorf("/%s is not a page a link may open", strings.Join(segments, "/"))
	}

	path := "/"
	for i, segment := range segments {
		if i > 0 {
			path += "/"
		}
		path += url.PathEscape(segment)
	}
	return path, nil
}

// schemeSegment decodes one segment of the address and refuses what no page
// segment can be.
func schemeSegment(part string) (string, error) {
	if part == "" {
		return "", errors.New("the address has an empty segment")
	}
	segment, err := url.PathUnescape(part)
	if err != nil {
		return "", fmt.Errorf("a segment does not decode: %w", err)
	}
	if segment == "." || segment == ".." {
		return "", errors.New("the address has a . or .. segment")
	}
	if len(segment) > maxSegment {
		return "", fmt.Errorf("a segment is %d characters long; at most %d are accepted", len(segment), maxSegment)
	}
	for _, r := range segment {
		switch {
		case r < 0x20, r == 0x7f, r == '\u0085', r == ' ', r == ' ':
			// A plain space is legal in a context name and re-encoded on the
			// way out; control and line-breaking characters are not.
			return "", errors.New("a segment holds a control or line-breaking character")
		case r == '/', r == '\\':
			return "", errors.New("a segment holds a slash once decoded")
		case r == '?' || r == '#':
			return "", errors.New("a segment holds ? or # once decoded")
		}
	}
	return segment, nil
}

// readOnlyPage is the positive list: whether these segments name one of the
// readOnlyPage is the positive list: whether these segments name a page a link
// may open, in the SPA router's shapes (web/ui/src/router/routes.tsx).
func readOnlyPage(segments []string) bool {
	switch len(segments) {
	case 0:
		return true // the cluster list
	}
	switch segments[0] {
	case "health":
		// A redirect to the cluster list, kept so an old link works.
		return len(segments) == 1
	case "settings":
		return len(segments) <= 2
	case "c":
		// /c/<cluster>[/<kind>[/<name> | /<namespace>/<name>]], never an
		// extension's `x` segment where a kind would be.
		if len(segments) < 2 || len(segments) > 5 {
			return false
		}
		return len(segments) < 3 || segments[2] != "x"
	}
	return false
}

package launch

import "net/url"

// What a native window lets its page navigate to, decided once for the macOS
// window (decideNavigation and the UI delegate) and the Windows one
// (NavigationStarting, NewWindowRequested). No build tag, so it is tested on
// Linux, which compiles neither window file.
//
//   - A whole-page navigation to another origin is canceled and handed to the
//     default browser: following it would replace the console in a window with
//     no back button.
//   - A request for a new window (`target="_blank"`, `window.open`) is handed
//     to the browser whatever its address: the window has no tabs.
//   - Everything else is allowed: the console's origin, and the `about:`,
//     `blob:` and `data:` documents the page makes.
//
// Frames inside the page are not judged: both engines ask about the main frame
// only.

// originOf is the scheme and host of an address, which is what tells the
// console's own links from everybody else's.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// leavesConsole reports whether an address is a web page somewhere other than
// origin, the console's. Only http and https: `about:`, `blob:` and `data:` are
// the page's own, and are never handed to a browser that could not open them.
func leavesConsole(origin, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return u.Scheme+"://"+u.Host != origin
}

// outsideAddress reports whether an address may be handed to another
// application, and when it may not, the line the log says about it ("" for an
// empty address, which is not worth a line).
//
// http, https and mailto only: a `blob:` or `about:` address would open nothing
// elsewhere, and an arbitrary scheme would launch whatever application claims
// it on a page's say-so.
func outsideAddress(raw string) (ok bool, refusal string) {
	u, err := url.Parse(raw)
	if err == nil && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "mailto") {
		return true, ""
	}
	if raw == "" {
		return false, ""
	}
	return false, "a link to " + loggableURL(raw) + " was not opened: only http, https and " +
		"mailto addresses are handed to another application"
}

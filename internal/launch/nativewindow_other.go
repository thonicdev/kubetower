//go:build !windows && !darwin

package launch

// nativeWindow has no implementation on Linux and the other Unixes, and returns
// false rather than not existing, so the caller reads the same on every
// platform.
//
// Windows uses WebView2 and macOS WKWebView, the latter through purego with no
// cgo. Whether WebKitGTK, a C library, can be reached the same way (purego's
// Dlopen rather than its objc package) is not established. So `--app` here is
// the browser window (appwindow.go).
func nativeWindow(string, windowSources) bool { return false }

// interruptNativeWindow has no window to close here.
func interruptNativeWindow() {}

// refreshNativeWindow has no window to refresh here either.
func refreshNativeWindow() {}

// deliverNativeNotices has nothing to deliver here: windowNotices.enabled is
// never set on this platform.
func deliverNativeNotices() {}

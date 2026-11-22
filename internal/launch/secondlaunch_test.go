package launch

// A second launch, or `kubetower open`, on the loopback
// desktop opens a signed-in window on the instance already running and then
// returns, without building a server of its own. The window is a fake opener:
// no window is drawn here, on any platform.

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// openedWindow is what a fake opener was handed, and what the codes it minted
// did on the instance.
type openedWindow struct {
	calls          int
	listen         string
	feed           windowSources
	nativeSignedIn bool
	browserSigned  bool
	replaySignedIn bool
}

// arrives reports whether the window address for code opens a session on the
// instance at listen: a 302 to / that sets a cookie.
func arrives(t *testing.T, listen, code string) bool {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(windowURL(listen, code))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusFound && resp.Header.Get("Location") == "/" && len(resp.Cookies()) > 0
}

// fakeOpener stands in for openWindow: it spends a native code, asks for a
// browser one as the fallback tiers would, tries the native code a second time,
// and then reports the window closed.
func fakeOpener(t *testing.T, seen *openedWindow) windowOpener {
	return func(_ context.Context, listen string, mint func(native bool) (string, error), feed windowSources) bool {
		seen.calls++
		seen.listen, seen.feed = listen, feed
		native, err := mint(true)
		if err != nil {
			t.Fatalf("the second launch's window could not mint a native code: %v", err)
		}
		seen.nativeSignedIn = arrives(t, listen, native)
		browser, err := mint(false)
		if err != nil {
			t.Fatalf("the second launch's window could not mint a browser code: %v", err)
		}
		seen.browserSigned = arrives(t, listen, browser)
		seen.replaySignedIn = arrives(t, listen, native)
		return true
	}
}

// aRunningInstance is a first instance on a real loopback listener, with its
// launcher secret written into a fresh state directory as Run writes it.
func aRunningInstance(t *testing.T) (listen string) {
	t.Helper()
	t.Setenv("KT_STATE_DIR", t.TempDir())
	secret, err := launcherSecret(true)
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := instance(t, secret)
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestASecondLaunchOpensASignedInWindowOnTheFirstAndReturns(t *testing.T) {
	listen := aRunningInstance(t)

	var seen openedWindow
	if !secondLaunch(true, listen, false, false, fakeOpener(t, &seen)) {
		t.Fatal("a second launch on the loopback desktop did not hand off, so Run would go on to " +
			"build a server and fail on the port")
	}
	if seen.calls != 1 {
		t.Fatalf("the window was opened %d times; want once", seen.calls)
	}
	if seen.listen != listen {
		t.Errorf("the window was opened on %q; want the first instance's %q", seen.listen, listen)
	}
	if !seen.nativeSignedIn {
		t.Error("the second launch's native window did not arrive signed in")
	}
	if !seen.browserSigned {
		t.Error("the second launch's browser fallback did not arrive signed in")
	}
	if seen.replaySignedIn {
		t.Error("the native window's code opened a session a second time")
	}
	if seen.feed.Clusters != nil || seen.feed.Terminals != nil || seen.feed.Update != nil {
		t.Error("the second launch's window was given a feed; it has nothing of this process to read")
	}
}

func TestASecondLaunchWithTheWrongSecretOpensNothing(t *testing.T) {
	listen := aRunningInstance(t)
	// Another state directory's secret: the instance answers, and refuses.
	t.Setenv("KT_STATE_DIR", t.TempDir())
	if _, err := launcherSecret(true); err != nil {
		t.Fatal(err)
	}

	var seen openedWindow
	if secondLaunch(true, listen, false, false, fakeOpener(t, &seen)) {
		t.Error("a second launch holding another secret handed off")
	}
	if seen.calls != 0 {
		t.Error("a window was opened although the instance refused the handoff")
	}
}

func TestASecondLaunchWithNoSecretOpensNothing(t *testing.T) {
	listen := aRunningInstance(t)
	t.Setenv("KT_STATE_DIR", t.TempDir())

	var seen openedWindow
	if secondLaunch(true, listen, false, false, fakeOpener(t, &seen)) || seen.calls != 0 {
		t.Error("a second launch with no launcher secret opened a window")
	}
}

func TestOnlyAWindowedLoopbackLaunchOntoAnAnsweringPortHandsOff(t *testing.T) {
	listen := aRunningInstance(t)

	for name, c := range map[string]struct {
		window, inCluster, inContainer bool
	}{
		"no window asked for": {false, false, false},
		"--incluster":         {true, true, false},
		"in a container":      {true, false, true},
	} {
		var seen openedWindow
		if secondLaunch(c.window, listen, c.inCluster, c.inContainer, fakeOpener(t, &seen)) || seen.calls != 0 {
			t.Errorf("%s: handed off", name)
		}
	}

	// Nothing answering: the ordinary start, which takes the port.
	var seen openedWindow
	if secondLaunch(true, "127.0.0.1:1", false, false, fakeOpener(t, &seen)) || seen.calls != 0 {
		t.Error("a launch handed off with nothing answering on its port")
	}
}

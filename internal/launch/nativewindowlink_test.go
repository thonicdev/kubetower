package launch

import (
	"strings"
	"testing"
)

// What the macOS window does with a `kubetower://` link,
// tested on every platform. The validation itself is `urlscheme_test.go`'s;
// these are what the glue adds - the link opened at once on a window that has
// signed in, held until it has on a cold start, the last one kept, a refusal
// that changes nothing, and what the log carries.

func TestALinkOnASignedInWindowOpensAtOnce(t *testing.T) {
	links := schemeLinks{ready: true}
	open, line := links.arrive("kubetower://c/prod-eks/pods/default/web-1")
	if open != "/c/prod-eks/pods/default/web-1" {
		t.Errorf("open = %q; want the pod's page", open)
	}
	if !strings.Contains(line, "opened /c/prod-eks/pods/default/web-1") {
		t.Errorf("the log line does not say what was opened: %q", line)
	}
	if links.held != "" {
		t.Errorf("a link opened at once is also held as %q", links.held)
	}
}

// A cold start: the application was started by the link, and the window's
// first page - the one-time sign-in - has not loaded yet.
func TestALinkBeforeTheSignInIsHeldThenOpenedOnce(t *testing.T) {
	var links schemeLinks
	open, line := links.arrive("kubetower://c/prod/pods/default/web-1")
	if open != "" {
		t.Fatalf("a link was opened before the window had signed in (%q); it would cancel the sign-in", open)
	}
	if !strings.Contains(line, "before the window had signed in") {
		t.Errorf("the log line does not say the link is held: %q", line)
	}

	open, line = links.windowReady()
	if open != "/c/prod/pods/default/web-1" {
		t.Errorf("once signed in, open = %q; want the held page", open)
	}
	if !strings.Contains(line, "opens now") {
		t.Errorf("the log line does not say the held link opens: %q", line)
	}

	// Every later navigation - a reload, a route change that reloads - is a
	// finished navigation too, and must not open the link again.
	if open, line := links.windowReady(); open != "" || line != "" {
		t.Errorf("a second finished navigation returned %q, %q; the link opens once", open, line)
	}
	// And a link after that opens at once.
	if open, _ := links.arrive("kubetower://c/prod"); open != "/c/prod" {
		t.Errorf("a link after the sign-in returned %q; want it opened at once", open)
	}
}

func TestSeveralLinksWhileStartingKeepTheLast(t *testing.T) {
	var links schemeLinks
	links.arrive("kubetower://c/prod/pods")
	_, line := links.arrive("kubetower://c/staging/nodes")
	if !strings.Contains(line, "instead of /c/prod/pods") {
		t.Errorf("the log does not say which link was replaced: %q", line)
	}
	// The same page twice is not a replacement worth a sentence.
	if _, line := links.arrive("kubetower://c/staging/nodes"); strings.Contains(line, "instead of") {
		t.Errorf("the same link twice was logged as a replacement: %q", line)
	}
	if open, _ := links.windowReady(); open != "/c/staging/nodes" {
		t.Errorf("open = %q; want the last link, /c/staging/nodes", open)
	}
}

// Nothing to open: a launch with no link, or a window that is told twice.
func TestAWindowWithNoLinkHeldOpensNothing(t *testing.T) {
	var links schemeLinks
	if open, line := links.windowReady(); open != "" || line != "" {
		t.Errorf("windowReady with nothing held returned %q, %q", open, line)
	}
}

// A refused link changes nothing, in either state: it opens nothing, it does
// not replace a link already held, and the log says why with the address made
// safe to print.
func TestARefusedLinkChangesNothing(t *testing.T) {
	refused := []string{
		"kubetower://api/c/prod/pods",
		"kubetower://auth/launch?code=secret-code",
		"kubetower://auth/logout",
		"kubetower://c/prod/pods?delete=1",
		"kubetower://c/prod/pods#exec",
		"kubetower://c/prod/x/argocd",
		"kubetower://c/prod/../../api/clusters",
		"javascript:alert(document.cookie)",
		"http://127.0.0.1:5823/c/prod/pods",
		"kubetower://c/prod/pods/default/web%0A[kubetower] forged",
		"",
	}
	for _, ready := range []bool{false, true} {
		for _, raw := range refused {
			links := schemeLinks{ready: ready}
			if !ready {
				links.arrive("kubetower://c/prod")
			}
			open, line := links.arrive(raw)
			if open != "" {
				t.Errorf("%q was opened as %q", raw, open)
			}
			if !strings.Contains(line, "was refused and nothing changed") {
				t.Errorf("%q: the log does not say it was refused: %q", raw, line)
			}
			if strings.ContainsAny(line, "\n\r") {
				t.Errorf("%q: the log line holds a line break: %q", raw, line)
			}
			if strings.Contains(line, "secret-code") {
				t.Errorf("%q: the log carries the link's query: %q", raw, line)
			}
			if !ready && links.held != "/c/prod" {
				t.Errorf("%q replaced the held link; held is now %q", raw, links.held)
			}
			if ready && links.held != "" {
				t.Errorf("%q left something held on a ready window: %q", raw, links.held)
			}
		}
	}
}

func TestARefusedLinkIsLoggedSafely(t *testing.T) {
	for raw, want := range map[string]string{
		"kubetower://auth/launch?code=x":       `"kubetower://auth/launch"`,
		"kubetower://c/prod/pods#exec":         `"kubetower://c/prod/pods"`,
		"kubetower://user:pass@c/prod/pods":    `"kubetower://c/prod/pods"`,
		"javascript:alert(1)":                  `"javascript:alert(1)"`,
		"kubetower://c/%zz?code=x":             `"kubetower://c/%zz"`,
		"kubetower://c/prod/web%0A[kubetower]": `"kubetower://c/prod/web%0A[kubetower]"`,
		// A raw line break does not parse, and is quoted rather than printed.
		"kubetower://c/prod/web\n[kubetower] forged?code=x": `"kubetower://c/prod/web\n[kubetower] forged"`,
		"": "empty",
	} {
		if got := loggableLink(raw); got != want {
			t.Errorf("loggableLink(%q) = %s; want %s", raw, got, want)
		}
	}
	long := loggableLink("kubetower://c/" + strings.Repeat("a", 10*maxSchemeURL))
	if len(long) > maxLoggedLink+16 || !strings.HasSuffix(long, `…"`) {
		t.Errorf("a long address is logged as %d bytes (%q…); want it cut at %d", len(long), long[:32], maxLoggedLink)
	}
}

// A link goes to the page it names even from under it - unlike a Cluster menu
// item, which stays on any page of its cluster - through the same one script.
func TestALinkUsesTheOneNavigationScript(t *testing.T) {
	script := openPageScript("/c/prod")
	for _, want := range []string{
		`var path = "/c/prod";`,
		"if (here === path) return;",
		"history.pushState(null, '', path);",
		"new PopStateEvent('popstate'",
		"location.assign(path)",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the link's script lacks %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "indexOf(path + '/')") {
		t.Errorf("a link stays put on a page under the one it names:\n%s", script)
	}
	if strings.Replace(script, "here === path)", "here === path || here.indexOf(path + '/') === 0)", 1) != navigateScript("/c/prod") {
		t.Error("the link's script and the menu's differ by more than when the page counts as already there")
	}
}

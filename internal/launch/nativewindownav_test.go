package launch

import (
	"strings"
	"testing"
)

// Which addresses a native window keeps and which it hands to the browser
// One table for both windows, because both ask the same
// functions: the macOS window from `decideNavigation`, the Windows one from
// `NavigationStarting`.
//
// The console's address in the window carries the one-time sign-in code, so
// the origin is taken from an address shaped like that one.
func TestTheWindowKeepsTheConsoleAndHandsOutEverythingElse(t *testing.T) {
	origin := originOf("http://127.0.0.1:5823/signin/window?code=one-time")
	if origin != "http://127.0.0.1:5823" {
		t.Fatalf("originOf gave %q, want http://127.0.0.1:5823", origin)
	}
	for _, tc := range []struct {
		target string
		leaves bool
	}{
		// The console's own pages stay, whatever their path, query or fragment.
		{"http://127.0.0.1:5823/", false},
		{"http://127.0.0.1:5823/signin/window?code=one-time", false},
		{"http://127.0.0.1:5823/clusters/kind-dev/pods?ns=default#logs", false},
		// So do the documents the page makes for itself, which a browser
		// handed one could not open.
		{"about:blank", false},
		{"blob:http://127.0.0.1:5823/0b8c0f6e-3c1d-4f43-9a52-5a3a1f7c2d10", false},
		{"data:text/plain,hello", false},
		// An address that is not one is not a page somewhere else.
		{"", false},
		{"http://[::1", false},
		// Another origin leaves: another host, another port of the same host,
		// another scheme on the same host and port.
		{"https://github.com/thonicdev/kubetower", true},
		{"http://localhost:5823/", true},
		{"http://127.0.0.1:8080/", true},
		{"https://127.0.0.1:5823/", true},
		{"http://127.0.0.1/", true},
		// A port-forward or an Ingress host the console links to is somebody
		// else's page.
		{"http://127.0.0.1:30080/api", true},
		{"https://grafana.example.internal/d/abc", true},
	} {
		if got := leavesConsole(origin, tc.target); got != tc.leaves {
			t.Errorf("leavesConsole(%q, %q) = %t, want %t", origin, tc.target, got, tc.leaves)
		}
	}
}

// What may be handed to another application at all, which is asked of every
// address before it leaves: a navigation that left the console, and every
// request for a new window, whose address may be anything a page wrote.
func TestOnlyWebAndMailAddressesAreHandedToAnotherApplication(t *testing.T) {
	for _, tc := range []struct {
		target  string
		ok      bool
		refused bool // a log line says so
	}{
		{"https://github.com/thonicdev/kubetower", true, false},
		{"http://127.0.0.1:30080/", true, false},
		{"mailto:someone@example.com", true, false},
		// The console's own address, asked for in a new window, goes to the
		// browser like any other: the window does not grow a second one.
		{"http://127.0.0.1:5823/clusters", true, false},
		{"", false, false},
		{"about:blank", false, true},
		{"blob:http://127.0.0.1:5823/0b8c0f6e", false, true},
		{"data:text/html,<script>1</script>", false, true},
		{"javascript:alert(1)", false, true},
		{"file:///C:/Windows/System32/calc.exe", false, true},
		{"ms-settings:privacy", false, true},
		{"kubetower://open?path=/", false, true},
	} {
		ok, refusal := outsideAddress(tc.target)
		if ok != tc.ok || (refusal != "") != tc.refused {
			t.Errorf("outsideAddress(%q) = %t, %q; want %t and a refusal %t", tc.target, ok, refusal, tc.ok, tc.refused)
		}
	}
}

// A refusal goes to `kubetower.log`, which a bug report receives whole, so it
// carries the address the way every other line does: without its query, its
// fragment or any user information.
func TestARefusedAddressIsLoggedWithoutItsQuery(t *testing.T) {
	_, refusal := outsideAddress("ftp://user:secret@files.example/x?token=abc#f")
	for _, leak := range []string{"secret", "token", "abc", "#f"} {
		if strings.Contains(refusal, leak) {
			t.Errorf("the refusal %q carries %q", refusal, leak)
		}
	}
	if !strings.Contains(refusal, "ftp://files.example/x") {
		t.Errorf("the refusal %q does not name the address it refused", refusal)
	}
}

package launch

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The three shell upgrades, and whether the server profile registers them.
//
// **Asserted with `mux.Handler` rather than by serving**, which is the
// mechanism `desktopterm_test.go` uses and which
// `docs/SERVER-MODE-THREAT-MODEL.md` §9.1 asks for: *"not registering the route
// is a property of the router … visible by walking the router"*, where a
// handler that refuses is a property of one code path that no walk can see.
//
// These live here because the registration does: inside `main`, which is not
// callable, the only way to check it would be to read it, which is not enough.

// theUpgrades is written once so the two tests cannot disagree about the list.
var theUpgrades = []struct {
	path string
	why  string
}{
	{"/kt-api/terminal", "a pseudo-terminal on the machine this process runs on; in a pod that machine is the pod"},
	{"/kt-api/exec", "the template-era exec endpoint, which the SPA does not use"},
	{"/kt-api/claude", "the same shell with an assistant in front of it — absent, not refused"},
}

func TestTheServerProfileMountsNoShellUpgrade(t *testing.T) {
	mux := http.NewServeMux()
	mountShellUpgrades(mux, refusingShells{}, true)

	for _, upgrade := range theUpgrades {
		req := httptest.NewRequest(http.MethodGet, upgrade.path, nil)
		if _, pattern := mux.Handler(req); pattern != "" {
			t.Errorf("%s is registered as %q under --incluster.\n"+
				"It must not be: %s.\n"+
				"The point of not registering it is that this walk can see the answer; "+
				"a handler that refuses cannot be seen from here.",
				upgrade.path, pattern, upgrade.why)
		}
	}
}

// TestTheDesktopMountsEveryUpgrade is the half that would be skipped.
//
// The rule is *the profile takes routes away and adds nothing*. The cheapest
// way to make the test above pass is to stop registering them for everybody,
// and nobody reading only that test would notice.
func TestTheDesktopMountsEveryUpgrade(t *testing.T) {
	mux := http.NewServeMux()
	mountShellUpgrades(mux, refusingShells{}, false)

	for _, upgrade := range theUpgrades {
		req := httptest.NewRequest(http.MethodGet, upgrade.path, nil)
		if _, pattern := mux.Handler(req); pattern == "" {
			t.Errorf("%s is not registered without --incluster, and the desktop loses nothing to this change",
				upgrade.path)
		}
	}
}

// TestEveryKnownArgumentIsKnownToTheDiagnostics guards a list that drifts in
// silence.
//
// `main`'s switch and `argvNote`'s map are the same vocabulary written twice. A
// flag added to one and missed in the other is reported as an argument nobody
// recognizes — in the one document somebody reads when the console is behaving
// oddly, which is the worst place for a wrong answer.
func TestEveryKnownArgumentIsKnownToTheDiagnostics(t *testing.T) {
	for _, spelling := range []string{"--incluster", "-incluster", "incluster"} {
		if got := argvNote([]string{spelling}); got != spelling {
			t.Errorf("argvNote(%q) = %q, want the argument itself.\n"+
				"An argument main understands and diagnostics does not is counted as unrecognized.",
				spelling, got)
		}
	}
}

// refusingShells satisfies shellUpgrades with handlers nothing calls: these tests
// ask the router what it holds, never what a handler does.
type refusingShells struct{}

func (refusingShells) ShellHandler() http.Handler  { return http.NotFoundHandler() }
func (refusingShells) ExecHandler() http.Handler   { return http.NotFoundHandler() }
func (refusingShells) ClaudeHandler() http.Handler { return http.NotFoundHandler() }

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Why these walk the router instead of serving a request.
//
// There are two ways to turn a route off, and the server profile uses
// the first:
//
//	Not registering the route is a property of the router. It is true for every
//	method, every header, every path-matching subtlety, and it is visible by
//	walking the router.
//
//	Refusing inside the handler is a property of one code path. It is true only
//	where the condition is evaluated, it has to be re-established for every early
//	return added later, and no walk of the router can see it.
//
// **And the walk has to be of the right mux.**
// `Mount` registers this whole package as one handler on `/api/`, so asking the
// process's mux about `/api/dock/terminals` answers `/api/` whatever is
// registered underneath. The router that holds the decision is `a.mux`, which is
// why these tests are in this package rather than in `internal/web`.

// addressesTheServerProfileRemoves is what **this package** decides: the desktop
// terminal and the three forward operations.
//
// The four socket channels are removed too, and they are not here: `channels`
// asks `Channels.Offers`, and the implementation that knows about the profile is
// `internal/web`'s `apiChannels`. Asserting them here would only prove that a
// fake said yes. They are asserted where the decision is taken, in
// `internal/web/profileroutes_test.go` — one decision, one place, one test.
var addressesTheServerProfileRemoves = []struct {
	method string
	path   string
	why    string
}{
	{http.MethodPost, "/api/dock/terminals", "a pseudo-terminal on the machine running the console, which in a pod is the pod"},
	// The three forward operations. A forward binds a port in this process and
	// the browser is handed 127.0.0.1, which from a pod is the person's own
	// machine: broken whatever the RBAC, and shared by everybody if it worked.
	{http.MethodGet, "/api/forwards", "a forward binds a port inside the pod, and the browser's 127.0.0.1 is not the pod"},
	{http.MethodPost, "/api/forwards", "a forward binds a port inside the pod, and the browser's 127.0.0.1 is not the pod"},
	{http.MethodDelete, "/api/forwards/f-1", "a forward binds a port inside the pod, and the browser's 127.0.0.1 is not the pod"},
}

const addressTheServerProfileKeeps = "/api/c/prod/ws/exec"

func TestTheServerProfileRegistersNoLocalShell(t *testing.T) {
	a := apiForProfile(true)

	for _, address := range addressesTheServerProfileRemoves {
		req := httptest.NewRequest(address.method, address.path, nil)
		if _, pattern := a.mux.Handler(req); pattern != "" {
			t.Errorf("%s %s is registered as %q under the server profile.\n"+
				"It must not be: %s.\n"+
				"A handler that refuses is not the same guarantee as an address that does not exist.",
				address.method, address.path, pattern, address.why)
		}
	}

	req := httptest.NewRequest(http.MethodGet, addressTheServerProfileKeeps, nil)
	if _, pattern := a.mux.Handler(req); pattern == "" {
		t.Errorf("%s is not registered under the server profile, and it should be.\n"+
			"Exec reaches the kubelet rather than this machine; what bounds it is the "+
			"ServiceAccount's pods/exec, which a deployment grants or withholds on purpose.",
			addressTheServerProfileKeeps)
	}
}

// TestTheDesktopKeepsEveryAddress protects the operator who did not ask for any
// of this.
//
// The profile takes routes away and adds nothing, so a build without the flag
// must register every address.
// Without this test the cheapest way to make the one above pass is to remove the
// routes for everybody, and nobody reading only that test would see it.
func TestTheDesktopKeepsEveryAddress(t *testing.T) {
	a := apiForProfile(false)

	for _, address := range addressesTheServerProfileRemoves {
		req := httptest.NewRequest(address.method, address.path, nil)
		if _, pattern := a.mux.Handler(req); pattern == "" {
			t.Errorf("%s %s is not registered without the server profile.\n"+
				"The desktop loses nothing here: the profile removes, it never adds.",
				address.method, address.path)
		}
	}

	req := httptest.NewRequest(http.MethodGet, addressTheServerProfileKeeps, nil)
	if _, pattern := a.mux.Handler(req); pattern == "" {
		t.Errorf("%s is not registered without the server profile", addressTheServerProfileKeeps)
	}
}

// TestTheDeskTopTerminalIsNotDecidedByLocalShell guards a mistake that is easy
// to make.
//
// Registering `/api/dock/terminals` where
// `capabilities.localShell` is true would be wrong. That field is **also** false on a desktop
// that cannot open a pseudo-terminal — an old Windows — where the address exists
// and answers `ErrNotInProfile`. Keying registration off it would remove the
// route on those machines too: a change to the desktop, made by a change scoped
// to the server. `TestRealResponsesMatchTheSchemasInTheContract` also catches it,
// because `fakeProfile` reports exactly that shape.
func TestTheDesktopTerminalIsNotDecidedByLocalShell(t *testing.T) {
	a := apiForProfile(false)
	if caps := a.deps.Profile.Capabilities(nil); caps.LocalShell {
		t.Fatal("this test needs a profile whose localShell is false to mean anything")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/dock/terminals", nil)
	if _, pattern := a.mux.Handler(req); pattern == "" {
		t.Error("the desktop terminal is unregistered on a build with no pseudo-terminal.\n" +
			"It should exist and refuse: localShell answers what the front end draws, " +
			"the profile answers what the router holds.")
	}
}

// thePreferenceWrites are the seven rail writes, the five column writes and
// the version seen (#80).
var thePreferenceWrites = []string{
	"/api/preferences/rail/bar",
	"/api/preferences/rail/assign",
	"/api/preferences/rail/move",
	"/api/preferences/rail/place",
	"/api/preferences/rail/hide",
	"/api/preferences/rail/reveal",
	"/api/preferences/rail/nameplate",
	"/api/preferences/columns/size",
	"/api/preferences/columns/place",
	"/api/preferences/columns/show",
	"/api/preferences/columns/density",
	"/api/preferences/columns/reset",
	"/api/preferences/seen-version",
}

// TestTheServerProfileRegistersNoPreferenceWrite: the preference writes are the
// console's, not the person's.
//
// Under the server profile the first person to rename or hide a rail tile would
// do it for everybody who signed in after, and one person's column widths would
// become everybody's. The chart's default is one replica, so the profile takes
// all twelve away on one replica too - absent, not refusing, for the reason this
// file opens with - and the read stays, so the page still draws the defaults.
//
// Both arms, and the control is the desktop keeping all twelve: the cheapest way
// to make the first arm pass is to remove them for everybody.
func TestTheServerProfileRegistersNoPreferenceWrite(t *testing.T) {
	registered := func(a *API, method, path string) bool {
		_, pattern := a.mux.Handler(httptest.NewRequest(method, path, nil))
		return pattern != ""
	}
	server, desktop := apiForProfile(true), apiForProfile(false)
	for _, path := range thePreferenceWrites {
		if registered(server, http.MethodPost, path) {
			t.Errorf("POST %s is registered under the server profile. It writes the console's "+
				"arrangement, which there is everybody's: it must not exist", path)
		}
		if !registered(desktop, http.MethodPost, path) {
			t.Errorf("POST %s is not registered on the desktop, which loses nothing to the server rule", path)
		}
	}
	for name, a := range map[string]*API{"server profile": server, "desktop": desktop} {
		if !registered(a, http.MethodGet, "/api/preferences") {
			t.Errorf("GET /api/preferences is not registered on the %s; the read is what draws the defaults", name)
		}
	}
}

// apiForProfile builds the router with every channel on offer, so what the tests
// measure is the profile rather than a build assembled without a terminal layer.
func apiForProfile(inCluster bool) *API {
	return New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Channels: allChannels(), Probe: fakeProbe{},
		InCluster:   inCluster,
		AllowOrigin: func(*http.Request, string) bool { return true },
	})
}

// TestTheNodeShellPreviewAndSettingFollowTheChannel: the node-shell preview,
// its setting and its channel are all offered, or none is.
//
// Under the server profile the node-shell channel is not registered, so
// `POST .../nodeshell/preview` must not answer `400 node is required` and
// `PUT /api/settings/nodeshell` must not answer 200: that would be a console offering to plan and
// configure a shell whose address does not exist. All three ask the same
// question - offersNodeShell - so they cannot disagree.
func TestTheNodeShellPreviewAndSettingFollowTheChannel(t *testing.T) {
	addresses := []struct{ method, path string }{
		{http.MethodPost, "/api/c/prod/nodeshell/preview"},
		{http.MethodPut, "/api/settings/nodeshell"},
		{http.MethodGet, "/api/c/prod/ws/nodeshell"},
	}

	without := allChannels()
	without.offers[ChannelNodeShell] = false
	for name, channels := range map[string]*fakeChannels{"offered": allChannels(), "not offered": without} {
		a := New(Deps{
			Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
			Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
			Acts: fakeActs{}, Dock: fakeDock{}, Channels: channels, Probe: fakeProbe{},
			AllowOrigin: func(*http.Request, string) bool { return true },
		})
		want := name == "offered"
		for _, address := range addresses {
			req := httptest.NewRequest(address.method, address.path, nil)
			_, pattern := a.mux.Handler(req)
			if got := pattern != ""; got != want {
				t.Errorf("node shell %s: %s %s registered = %v, want %v - the channel, the preview and the setting must agree",
					name, address.method, address.path, got, want)
			}
		}
	}
}

// TestNoSettingsWriteWhereTheStateDoesNotPersist is the router half of offering
// disk-backed settings only where the state directory survives a restart.
//
// Both settings writes keep their value in the state directory - the archive's
// three in its settings file, the node-shell image in its own - so on a
// directory the deployment says is wiped at every restart, a save is a
// promise nothing keeps. The addresses are absent rather than refusing, for
// the reason the rest of this file gives.
//
// **Clear goes with them.** It discards what the state directory keeps, and
// there it keeps nothing: no archive is opened, and the preference scopes are
// not wired to this contract on any profile. So the address is absent too: one
// whose archive scope could only answer with a refusal that explained the missing volume - a
// destructive address whose every answer was "no" is a control for nothing,
// and the settings read already says why.
//
// **And the rest of the settings surface stays**, asserted in the same loop:
// the read is what says *why* the writes are gone, and the node shell is not a
// setting.
func TestNoSettingsWriteWhereTheStateDoesNotPersist(t *testing.T) {
	writes := []struct{ method, path string }{
		{http.MethodPut, "/api/settings"},
		{http.MethodPut, "/api/settings/nodeshell"},
		{http.MethodPost, "/api/settings/clear"},
	}
	kept := []struct{ method, path string }{
		{http.MethodGet, "/api/settings"},
		// The node shell itself is not a setting: it opens a pod with the
		// default image whatever the disk does.
		{http.MethodPost, "/api/c/prod/nodeshell/preview"},
	}

	for _, ephemeral := range []bool{false, true} {
		a := New(Deps{
			Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
			Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
			Acts: fakeActs{}, Dock: fakeDock{}, Channels: allChannels(), Probe: fakeProbe{},
			StateEphemeral: ephemeral,
			AllowOrigin:    func(*http.Request, string) bool { return true },
		})
		for _, address := range writes {
			_, pattern := a.mux.Handler(httptest.NewRequest(address.method, address.path, nil))
			if got, want := pattern != "", !ephemeral; got != want {
				t.Errorf("state ephemeral=%v: %s %s registered = %v, want %v - a setting kept, "+
					"or a store cleared, on a disk wiped at every restart is a control that lies",
					ephemeral, address.method, address.path, got, want)
			}
		}
		for _, address := range kept {
			_, pattern := a.mux.Handler(httptest.NewRequest(address.method, address.path, nil))
			if pattern == "" {
				t.Errorf("state ephemeral=%v: %s %s is not registered, and persistence has "+
					"nothing to do with it", ephemeral, address.method, address.path)
			}
		}
	}
}

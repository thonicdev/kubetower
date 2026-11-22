// What this file asserts, and why each assertion is here rather than in
// `internal/web`.
//
// `internal/web/fixtureextension_test.go` already covers the mechanism: the
// gate, the mounting, the settings grid, the router's refusal of a pattern that
// collides with the console's wildcards. None of that is repeated here and none
// of it could be - the mount lives behind `internal/`, and this module cannot
// see it, which is the whole point.
//
// What is only assertable from here is the other half: that an extension
// declared outside the core module can be **built, registered and served**, and
// that every door on `extension.Host` is reachable with every type in its
// signature nameable from this side.
package boundary_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"boundary.kubetower.invalid"
	"github.com/thonicdev/kubetower/app"
	"github.com/thonicdev/kubetower/extension"
)

// TestTheConsumerSatisfiesTheContract is the compile-time half, written as a
// declaration so that it fails at build rather than at run.
//
// A method leaving extension.Extension turns this line red in the repository
// that removed it, on the pull request that removed it.
var _ extension.Extension = boundary.Consumer{}

// And the same for the host, from the other direction.
var _ extension.Host = (*recordingHost)(nil)

// TestRegisterAcceptsAnExtensionDeclaredOutsideTheCore is the registration door.
//
// `app` is public for exactly this reason - its own header says so: "a module
// cannot import internal/, so the two things an extra-carrying binary has to do
// both have to be expressible from outside". Expressible was established by
// reading. This is the call.
func TestRegisterAcceptsAnExtensionDeclaredOutsideTheCore(t *testing.T) {
	if err := app.Register(boundary.Consumer{}); err != nil {
		t.Fatalf("registering an extension declared in another module: %v", err)
	}

	keys := app.Keys()
	found := false
	for _, k := range keys {
		if k == boundary.Key {
			found = true
		}
	}
	if !found {
		t.Fatalf("app.Keys() = %v, want it to contain %q", keys, boundary.Key)
	}

	// Registration is process-wide and additive by design, so the second call is
	// the refusal rather than a second page. Asserted here because a consumer
	// outside the core is exactly who would hit it - two binaries, one linked
	// twice, a test that registers in TestMain and again in a case.
	if err := app.Register(boundary.Consumer{}); err == nil {
		t.Fatal("registering the same key twice was accepted; one page would shadow the other")
	}
}

// TestEveryDoorOnTheHostIsReachedFromOutside serves each route and asserts the
// method behind it was called.
//
// **Reached, not referenced.** A handler that names a method without calling it
// compiles against a nil interface and proves nothing about the argument and
// result types, which is the property an extra actually depends on.
func TestEveryDoorOnTheHostIsReachedFromOutside(t *testing.T) {
	host := newRecordingHost()
	routes := boundary.Consumer{}.Routes(host)

	// One route per door, and the count is asked of the interface rather than
	// written down.
	//
	// **A literal 6 here would have compared this consumer against itself.**
	// Asking the type is what makes the assertion about the contract, and a door
	// added to `extension.Host` then lands in one of two places, shown here with
	// a seventh method added:
	//
	//	stub untouched       consumer_test.go:37: *recordingHost does not implement
	//	                     extension.Host (missing method Probe)
	//	stub updated, no     consumer_test.go:92: routes = 6, extension.Host has
	//	route added          7 methods
	//
	// The first is the compiler, through `recordingHost`, and it is the one that
	// fires in practice. This line is the second: the case where somebody teaches
	// the stub about a new door and leaves it unreached, which compiles cleanly
	// and would otherwise leave a method on the contract that nothing outside the
	// core has ever called.
	doors := reflect.TypeOf((*extension.Host)(nil)).Elem().NumMethod()
	if len(routes) != doors {
		t.Fatalf("routes = %d, extension.Host has %d methods.\n"+
			"Every door is meant to be reached from outside the core module; one that is not "+
			"has never been called from a package the internal rule shuts out.", len(routes), doors)
	}

	for _, want := range []struct {
		pattern string
		door    string
		body    string
	}{
		{"/{$}", "Client", "typed=true dynamic=true"},
		{"/definitions", "Definitions", "all=0 lookup=no kind of that name"},
		{"/list", "List", "items=0 complete"},
		{"/reveal", "Reveal", "reveal=not permitted"},
		{"/exec", "Exec", "exit=3 stdout=3 bytes truncated=false"},
		{"/logs", "MayReadLogs", "logs=not permitted"},
		{"/usage", "Usage", "nothing could measure it; pods complete"},
		// A measured zero stays a zero, under the namespace/pod/container key.
		{"/containers", "ContainerUsage", "containers=1 default/web-0/app cpu=0m memory=67108864 complete=true"},
		{"/window", "UsageWindow", "state=no-prometheus: this installation has no Prometheus configured"},
		{"/dns", "DNSWindow", "state=no-prometheus: no Prometheus"},
		{"/forward", "Forward", "addr=127.0.0.1:0 pod=a-service-0"},
		{"/setting", "Setting", `setting="a-value"`},
		{"/console-forward", "ForwardAsConsole", "addr=127.0.0.1:1 known=true"},
		{"/configured-secret", "ConfiguredSecret", "configured=13 bytes"},
		{"/configured-service", "ConfiguredService", "service=a-backend ports=1"},
		{"/socket", "ConfirmToken", "token=a-word"},
		{"/refresh-application", "RefreshApplication", "refresh=done"},
	} {
		route := routeFor(t, routes, want.pattern)

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.SetPathValue("cluster", "a-cluster")
		route.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, body %q", want.pattern, rec.Code, rec.Body.String())
			continue
		}
		if got := rec.Body.String(); !strings.Contains(got, want.body) {
			t.Errorf("%s: body %q, want it to contain %q", want.pattern, got, want.body)
		}
		if !host.opened(want.door) {
			t.Errorf("%s answered without calling Host.%s", want.pattern, want.door)
		}
	}

	// The teardown Forward hands back is part of its signature and is the half a
	// caller forgets. A tunnel that is opened and never stopped is the process
	// cap spent on nothing.
	if !host.opened("Forward.stop") {
		t.Error("the forward handler never called the stop it was handed")
	}
	if !host.opened("ForwardAsConsole.stop") {
		t.Error("the console-forward handler never called the stop it was handed")
	}
}

// TestUnmeasuredAndReadAreTellableApartFromOutside: the two usage doors each
// have an answer that is not a number - no metrics-server, a window not read -
// and an extra in another module must be able to name it with the contract's
// own types, not by matching a sentence.
func TestUnmeasuredAndReadAreTellableApartFromOutside(t *testing.T) {
	host := newRecordingHost()
	host.liveErr = fmt.Errorf("reading: %w", &extension.MetricsUnavailable{
		Reason: extension.MetricsAbsent, Detail: "metrics.k8s.io is not there"})
	host.window = extension.Window{State: extension.WindowRead, Complete: true,
		Containers: map[string]extension.ContainerWindow{
			extension.ContainerKey("default", "web-0", "app"): {
				CPU:    []extension.Sample{{T: 1, V: 100}, {T: 121, V: 120}},
				Memory: []extension.Sample{{T: 1, V: 64 << 20}},
			},
		}}
	routes := boundary.Consumer{}.Routes(host)
	for pattern, want := range map[string]string{
		"/containers": "unmeasured=absent",
		"/window":     "state=read containers=1 cpu=2 memory=1 span=168h0m0s step=15m0s",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.SetPathValue("cluster", "a-cluster")
		routeFor(t, routes, pattern).Handler.ServeHTTP(rec, req)
		if got := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(got, want) {
			t.Errorf("%s: status %d, body %q, want it to contain %q", pattern, rec.Code, got, want)
		}
	}
}

// TestANarrowedUsageIsReadableFromOutside reads Usage's Complete and Note from
// another module, as a page would: pods narrowed to the person's namespaces must
// be printable as narrowed, not drawn as every pod on the node.
func TestANarrowedUsageIsReadableFromOutside(t *testing.T) {
	host := newRecordingHost()
	host.usage = extension.Usage{Nodes: map[string]extension.NodeUsage{}, Complete: false,
		Note: "only the namespaces where you may"}
	route := routeFor(t, boundary.Consumer{}.Routes(host), "/usage")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetPathValue("cluster", "a-cluster")
	route.Handler.ServeHTTP(rec, req)

	if got, want := rec.Body.String(), "pods narrowed: only the namespaces where you may"; !strings.Contains(got, want) {
		t.Errorf("body %q, want it to contain %q", got, want)
	}
}

// TestARouteWithNoMethodIsExpressibleFromOutside guards the route with no
// method: a WebSocket upgrade is not a verb, so its route carries no method,
// and a contract that could not express that from another module would push
// every extra to invent something.
func TestARouteWithNoMethodIsExpressibleFromOutside(t *testing.T) {
	routes := boundary.Consumer{}.Routes(newRecordingHost())
	socket := routeFor(t, routes, "/socket")
	if socket.Method != "" {
		t.Errorf("the socket route carries method %q, want none", socket.Method)
	}
}

func routeFor(t *testing.T, routes []extension.Route, pattern string) extension.Route {
	t.Helper()
	for _, r := range routes {
		if r.Pattern == pattern {
			return r
		}
	}
	t.Fatalf("no route with pattern %q in %d routes", pattern, len(routes))
	return extension.Route{}
}

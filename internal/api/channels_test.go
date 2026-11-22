package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeChannels records what was served, and refuses the channels it is told to.
type fakeChannels struct {
	mu       sync.Mutex
	offers   map[ChannelID]bool
	served   []string
	nodes    []string
	clusters []string
}

func allChannels() *fakeChannels {
	return &fakeChannels{offers: map[ChannelID]bool{
		ChannelTerminal: true, ChannelExec: true,
		ChannelNodeShell: true, ChannelAssistant: true,
	}}
}

func (f *fakeChannels) Offers(id ChannelID) bool { return f.offers[id] }

func (f *fakeChannels) record(what, cluster string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.served = append(f.served, what)
	f.clusters = append(f.clusters, cluster)
}

func (f *fakeChannels) ServeTerminal(w http.ResponseWriter, _ *http.Request, cluster string) {
	f.record("terminal", cluster)
	w.WriteHeader(http.StatusSwitchingProtocols)
}

func (f *fakeChannels) ServeExec(w http.ResponseWriter, _ *http.Request, cluster string) {
	f.record("exec", cluster)
	w.WriteHeader(http.StatusSwitchingProtocols)
}

func (f *fakeChannels) ServeNodeShell(w http.ResponseWriter, _ *http.Request, cluster, node string) {
	f.record("nodeshell", cluster)
	f.mu.Lock()
	f.nodes = append(f.nodes, node)
	f.mu.Unlock()
	w.WriteHeader(http.StatusSwitchingProtocols)
}

func (f *fakeChannels) ServeAssistant(w http.ResponseWriter, _ *http.Request) {
	f.record("assistant", "")
	w.WriteHeader(http.StatusSwitchingProtocols)
}

func routerWith(channels Channels) *http.ServeMux {
	return routerWithClusters(channels, fakeClusters{})
}

// routerWithClusters is routerWith over another estate, for a test whose point
// is which cluster names exist.
func routerWithClusters(channels Channels, clusters Clusters) *http.ServeMux {
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: clusters,
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Channels: channels, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)
	return mux
}

// The four addresses `x-channels` advertises are served, and they are served
// **without a method**.
//
// Every address answers; none answers a JSON 404. That includes the one the
// node-shell preview mints a real, single-use, short-lived plan token for. The
// four are listed in `api/openapi.yaml` under `x-channels`, with their protocol
// in `api/websocket.md`.
//
// The expectation is read out of the contract at run time rather than listed
// here, so a channel added to `x-channels` and to nothing else fails this test
// instead of being agreed with by construction.
func TestEveryAdvertisedChannelIsServed(t *testing.T) {
	channels := allChannels()
	mux := routerWith(channels)

	for _, path := range channelPathsInContract(t) {
		address := strings.ReplaceAll(path, "{cluster}", "prod")
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, address+nodeShellToken(t, mux, path), nil))
		if recorder.Code == http.StatusNotFound {
			t.Errorf("%s is advertised in x-channels and answers 404: %s",
				path, strings.TrimSpace(recorder.Body.String()))
		}
	}
}

// An upgrade is not a verb, so the pattern carries no method - and a client that
// sends anything other than GET still reaches the handler rather than net/http's
// plain-text 405.
//
// `api/websocket.md` carries this as a warning to whoever regenerates a router
// from the spec: *"a generator that emits GET for these has produced a router
// that rejects the handshake."*
func TestAChannelIsRegisteredWithoutAMethod(t *testing.T) {
	channels := allChannels()
	mux := routerWith(channels)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder,
		httptest.NewRequest(http.MethodHead, "/api/c/prod/ws/terminal", nil))
	if recorder.Code == http.StatusNotFound || recorder.Code == http.StatusMethodNotAllowed {
		t.Errorf("a HEAD on the terminal channel answered %d; the pattern carries a method, "+
			"and an upgrade is not one", recorder.Code)
	}
}

// **Absent, not disabled.**
//
// A channel the profile does not offer is not registered, so its address answers
// this contract's ordinary JSON 404 through the miss path. There is no condition
// at a handler for a later change to get wrong, and walking the router is enough
// to establish that the control is gone - which a flag can never be.
//
// The assistant is the named case: on a shared console there should be no
// condition to test between the network and a shell holding the ServiceAccount.
func TestAChannelThisProfileDoesNotOfferIsNotThere(t *testing.T) {
	channels := allChannels()
	channels.offers[ChannelAssistant] = false
	channels.offers[ChannelTerminal] = false
	mux := routerWith(channels)

	for _, address := range []string{"/api/ws/assistant", "/api/c/prod/ws/terminal"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, address, nil))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s answered %d on a profile that does not offer it; the contract asks for absent "+
				"rather than disabled", address, recorder.Code)
		}
		// And it is this contract's 404, not the front end's index.html and not
		// net/http's plain text - a client that gets HTML here cannot tell a
		// missing endpoint from a logged-out session.
		if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "problem+json") {
			t.Errorf("%s answered 404 as %q, want application/problem+json", address, got)
		}
	}
	// Nothing reached the backend, which is the other half: a route that is not
	// registered cannot call a handler that then decides.
	if len(channels.served) != 0 {
		t.Errorf("a channel that is not offered reached the backend anyway: %v", channels.served)
	}
}

// A build with no terminal layer at all offers nothing, and nil Channels is a
// configuration rather than a defensive check.
func TestABuildWithNoChannelsRegistersNone(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/c/prod/ws/exec", nil))
	if recorder.Code != http.StatusNotFound {
		t.Errorf("a build with no terminal layer answered %d on a channel; a nil handler on a "+
			"socket route is a panic rather than a 404", recorder.Code)
	}
}

// The Origin check, which is the half people forget because the JSON side is the
// one the framework warns about.
//
// The same-origin policy **does not apply to a WebSocket handshake at all**: a
// page on any origin may open one at this console and the browser will attach
// the session cookie. The custom-header half of `guard` is not available here -
// a browser cannot set a header on a handshake - so the Origin check is not a
// second line of defense on this surface, it is the only one this contract adds.
//
// And the refusal is an HTTP 403 before the upgrade, never a socket that opens
// and closes: that is indistinguishable from a container that exited, and a front
// end retries it.
func TestAnUpgradeFromAnotherOriginIsRefusedBeforeAnythingIsAllocated(t *testing.T) {
	channels := allChannels()
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Channels: channels, Probe: fakeProbe{},
		// Same-origin only, worked out from the request's own Host, which is what
		// one process serving both halves wants.
	}).Mount(mux)

	request := httptest.NewRequest(http.MethodGet, "/api/c/prod/ws/terminal", nil)
	request.Header.Set("Origin", "https://evil.example")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("an upgrade from another origin answered %d, want 403: %s",
			recorder.Code, recorder.Body.String())
	}
	if len(channels.served) != 0 {
		t.Errorf("the upgrade reached the backend before being refused: %v", channels.served)
	}
	var problem Problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("the refusal is not a problem object: %v", err)
	}
	if problem.Code != ProblemOriginRejected {
		t.Errorf("the refusal is %q, want %q", problem.Code, ProblemOriginRejected)
	}
}

// --- The node shell, which is the one place a plan token crosses protocols ----

// The whole confirm-then-act pair, end to end, and the three properties that
// make it one rather than two unrelated requests.
//
// Without this handler, `POST /api/c/{cluster}/nodeshell/preview` would issue a
// real token and set `channel` to an address nothing served, so the token would
// never be spent, never expire by use, and bind nothing to anything.
func TestTheNodeShellSpendsItsPlanExactlyOnce(t *testing.T) {
	channels := allChannels()
	mux := routerWith(channels)

	token, node := previewNodeShell(t, mux, "prod", "node-3")

	// First use: the socket opens, and the node is the one the plan named.
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/api/c/prod/ws/nodeshell?token="+token, nil))
	if recorder.Code != http.StatusSwitchingProtocols {
		t.Fatalf("the first upgrade answered %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(channels.nodes) != 1 || channels.nodes[0] != node {
		t.Fatalf("the node shell opened on %v, want [%s] - the node is re-derived from the "+
			"plan, which is the only thing binding the confirmation to what happens",
			channels.nodes, node)
	}

	// Second use: the same token, and it is gone. A plan that has been acted on
	// no longer describes anything, and a token that survived a use would be a
	// confirmation that could be replayed.
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/api/c/prod/ws/nodeshell?token="+token, nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("the same token opened a second shell, answering %d", recorder.Code)
	}
	if len(channels.nodes) != 1 {
		t.Errorf("a spent token still reached the backend: %v", channels.nodes)
	}
}

// No token, and an unknown one. Both are 403 and **no socket is opened** -
// `api/websocket.md` says so in as many words, and the reason is the one every
// refusal on this surface has: a socket that opens and closes is
// indistinguishable from a container that exited.
func TestAnUpgradeWithNoUsablePlanOpensNothing(t *testing.T) {
	channels := allChannels()
	mux := routerWith(channels)

	for _, query := range []string{"", "?token=", "?token=not-a-token"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder,
			httptest.NewRequest(http.MethodGet, "/api/c/prod/ws/nodeshell"+query, nil))
		if recorder.Code != http.StatusForbidden {
			t.Errorf("%q answered %d, want 403: %s", query, recorder.Code, recorder.Body.String())
		}
	}
	if len(channels.served) != 0 {
		t.Errorf("an upgrade with no usable plan reached the backend: %v", channels.served)
	}
}

// A plan agreed for one cluster cannot be spent on another.
//
// The token is opaque and single-use, so this is not about guessing one: it is
// about a client that holds a legitimate plan for a cluster it may reach and
// points the upgrade somewhere else. The plan carries the cluster it was minted
// for and the two are compared; the token is spent either way, because `take` is
// what makes single-use a mechanism rather than a comment.
func TestAPlanIsNotSpendableOnAnotherCluster(t *testing.T) {
	channels := allChannels()
	mux := routerWith(channels)

	token, _ := previewNodeShell(t, mux, "prod", "node-3")

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/api/c/staging/ws/nodeshell?token="+token, nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("a plan for prod opened a shell on staging, answering %d", recorder.Code)
	}
	if len(channels.served) != 0 {
		t.Errorf("it reached the backend: %v", channels.served)
	}
}

// The node is taken from the plan and never from the URL.
//
// This is the re-derivation every other act performs, expressed for a protocol
// that has no request body. A query string saying `node=node-7` is simply not
// read: there is nowhere for it to be read, which is stronger than reading it
// and comparing.
func TestTheNodeComesFromThePlanAndNotFromTheQuery(t *testing.T) {
	channels := allChannels()
	mux := routerWith(channels)

	token, _ := previewNodeShell(t, mux, "prod", "node-3")

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/api/c/prod/ws/nodeshell?token="+token+"&node=node-7", nil))
	if recorder.Code != http.StatusSwitchingProtocols {
		t.Fatalf("the upgrade answered %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(channels.nodes) != 1 || channels.nodes[0] != "node-3" {
		t.Errorf("the shell opened on %v; the query named node-7 and the plan named node-3, "+
			"and the plan is the agreement", channels.nodes)
	}
}

// The preview's `channel` is the address this router serves.
//
// Asserted by *using* the string the server handed back rather than by
// comparing it to a literal, so a rename on either side fails here.
func TestThePreviewHandsBackAnAddressThisRouterServes(t *testing.T) {
	channels := allChannels()
	mux := routerWith(channels)

	plan := previewNodeShellPlan(t, mux, "prod", "node-3")
	if plan.Channel == "" {
		t.Fatal("the preview names no channel; the client would have to compose the address")
	}

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, plan.Channel+"?token="+plan.Token, nil))
	if recorder.Code != http.StatusSwitchingProtocols {
		t.Fatalf("the address the preview handed back, %q, answered %d: %s",
			plan.Channel, recorder.Code, recorder.Body.String())
	}
}

// --- helpers -----------------------------------------------------------------

func previewNodeShellPlan(t *testing.T, mux *http.ServeMux, cluster, node string) ConfirmationPlan {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/c/"+cluster+"/nodeshell/preview",
		strings.NewReader(`{"node":"`+node+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-KubeTower-Request", "1")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("previewing a node shell answered %d: %s", recorder.Code, recorder.Body.String())
	}
	var plan ConfirmationPlan
	if err := json.Unmarshal(recorder.Body.Bytes(), &plan); err != nil {
		t.Fatalf("the preview is not a plan: %v", err)
	}
	if plan.Token == "" {
		t.Fatal("the preview minted no token")
	}
	return plan
}

func previewNodeShell(t *testing.T, mux *http.ServeMux, cluster, node string) (string, string) {
	t.Helper()
	return previewNodeShellPlan(t, mux, cluster, node).Token, node
}

// nodeShellToken supplies the one channel that needs a plan, and nothing for the
// other three. Written this way so that `TestEveryAdvertisedChannelIsServed`
// reads its list out of the contract rather than carrying a copy of it.
func nodeShellToken(t *testing.T, mux *http.ServeMux, path string) string {
	t.Helper()
	if !strings.HasSuffix(path, "/ws/nodeshell") {
		return ""
	}
	token, _ := previewNodeShell(t, mux, "prod", "node-3")
	return "?token=" + token
}

// channelPathsInContract reads `x-channels` out of `api/openapi.yaml`.
func channelPathsInContract(t *testing.T) []string {
	t.Helper()
	spec := loadSpec(t)
	raw, ok := spec["x-channels"].([]any)
	if !ok || len(raw) == 0 {
		t.Fatal("the contract advertises no x-channels; there is nothing to measure")
	}
	paths := make([]string, 0, len(raw))
	for _, entry := range raw {
		channel, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("an x-channels entry is not an object: %T", entry)
		}
		path, ok := channel["path"].(string)
		if !ok || path == "" {
			t.Fatalf("an x-channels entry has no path: %v", channel)
		}
		paths = append(paths, path)
	}
	return paths
}

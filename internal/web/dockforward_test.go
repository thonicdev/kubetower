package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/forward"
)

// The compile-time half, and it is the one that matters most cheaply.
//
// `ForwardSet` is discovered by a type assertion, so a rename or a signature
// change on `forward.Manager` would not break a build — it would make the
// assertion fail at run time and every forward operation answer "this build's
// port-forwarder cannot be asked for its live set", on a build that has one.
// That would be a silent regression into a sentence which reads like a
// deliberate profile decision.
var (
	_ ForwardSet = (*forward.Manager)(nil)
	_ Forwards   = (*forward.Manager)(nil)
)

// The three forward operations.
//
// The subject is the mapping, including the `scheme` member:
// `scheme` is decided by the server in three tiers of authority, and a copy of
// that table in the browser would be a second answer to the same question,
// drifting the first time one of them learned a new port. What it costs to get
// wrong is "The plain HTTP request was sent to an HTTPS port", which reads as a
// broken tunnel rather than as a wrong link.
func TestTheForwardListIsFilledFromTheManagerThatHoldsIt(t *testing.T) {
	opened := time.Date(2026, 9, 12, 8, 15, 0, 0, time.UTC)
	set := &fakeForwards{live: []forward.Tunnel{{
		ID: "fwd-1", Cluster: "prod", Namespace: "argocd",
		Kind: "service", Name: "argocd-server", Target: "service/argocd-server",
		// Asked for 443; the tunnel lands on 8080 inside the pod. RemotePort is
		// what was asked for, which is the number the operator recognizes.
		Port: 443, PodPort: 8080, Pod: "argocd-server-6d4c9b7f-2xk4n",
		LocalPort: 34512, LocalAddr: "127.0.0.1:34512",
		Scheme: "https", Opened: opened,
	}}}
	dock := apiDock{&Server{forwards: set}}

	live, err := dock.Forwards()
	if err != nil {
		t.Fatalf("listing forwards: %v", err)
	}
	if len(live) != 1 {
		t.Fatalf("%d forwards, want 1: %+v", len(live), live)
	}
	got := live[0]

	// The scheme, which is the whole reason `scheme.go` exists.
	if got.Scheme != "https" {
		t.Errorf("scheme is %q, want %q: without it the link opens http against a TLS "+
			"port and reads as a broken tunnel", got.Scheme, "https")
	}
	// The replica actually reached, not the service that was asked for. A service
	// forward is not load balanced — it picks one pod and stays on it — so naming
	// it is the difference between "the service answered" and a fact.
	if got.Pod != "argocd-server-6d4c9b7f-2xk4n" {
		t.Errorf("pod is %q, want the replica the tunnel landed on", got.Pod)
	}
	// And the Service that was asked for, beside it. The Ports panel lists this
	// process's forwards on mount to show one opened elsewhere beside its port,
	// and a Service row has nothing else to recognize its own forward by.
	if got.Service != "argocd-server" {
		t.Errorf("service is %q, want the Service the forward was opened on", got.Service)
	}
	if got.Address != "127.0.0.1:34512" {
		t.Errorf("address is %q; it is what the operator dials", got.Address)
	}
	if got.RemotePort != 443 {
		t.Errorf("remotePort is %d, want the port that was asked for", got.RemotePort)
	}
	if got.OpenedAt == nil || !got.OpenedAt.Equal(opened) {
		t.Errorf("openedAt is %v, want %v", got.OpenedAt, opened)
	}

	// Both directions against the contract, read at run time rather than listed
	// here — a list would agree with the struct by construction.
	declared := schemaProperties(t, "Forward")
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling a forward: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal(encoded, &sent); err != nil {
		t.Fatalf("re-reading a forward: %v", err)
	}
	for name := range declared {
		if _, ok := sent[name]; !ok {
			t.Errorf("api/openapi.yaml declares Forward.%s and the mapper never fills it", name)
		}
	}
	for name := range sent {
		if _, ok := declared[name]; !ok {
			t.Errorf("the mapper sends Forward.%s and api/openapi.yaml does not declare it", name)
		}
	}
}

// A pod forward names no Service: `Tunnel.Name` is the pod's own name there,
// and the contract already carries the pod reached. An absent member, not an
// empty one, so a client can tell the two halves apart by presence.
func TestAPodForwardNamesNoService(t *testing.T) {
	set := &fakeForwards{live: []forward.Tunnel{{
		ID: "fwd-2", Cluster: "prod", Namespace: "argo",
		Kind: "pod", Name: "api-0", Target: "pod/api-0",
		Port: 8080, PodPort: 8080, Pod: "api-0",
		LocalPort: 51234, LocalAddr: "127.0.0.1:51234", Scheme: "http",
	}}}
	dock := apiDock{&Server{forwards: set}}

	live, err := dock.Forwards()
	if err != nil || len(live) != 1 {
		t.Fatalf("listing forwards: %v, %d forwards", err, len(live))
	}
	if live[0].Service != "" {
		t.Errorf("a pod forward names Service %q", live[0].Service)
	}
	encoded, err := json.Marshal(live[0])
	if err != nil {
		t.Fatalf("marshalling a forward: %v", err)
	}
	if strings.Contains(string(encoded), `"service"`) {
		t.Errorf("a pod forward sends an empty service member: %s", encoded)
	}
}

// What the object says about a port is sent so the *server* can decide the
// scheme. Dropping either member on the way through leaves `scheme.go` with its
// two weaker tiers and no way to know a port is TLS by its name.
func TestOpeningAForwardCarriesWhatTheObjectSaidAboutThePort(t *testing.T) {
	set := &fakeForwards{}
	dock := apiDock{&Server{forwards: set}}

	if _, err := dock.OpenForward(context.Background(), api.ForwardRequest{
		Cluster: "prod", Namespace: "argocd", Pod: "argocd-server-6d4c9b7f-2xk4n",
		RemotePort: 8443, PortName: "https-metrics", AppProtocol: "https",
	}); err != nil {
		t.Fatalf("opening a forward: %v", err)
	}
	if set.asked.PortName != "https-metrics" {
		t.Errorf("portName reached the manager as %q; the scheme table's second tier "+
			"reads it and cannot without it", set.asked.PortName)
	}
	if set.asked.AppProtocol != "https" {
		t.Errorf("appProtocol reached the manager as %q; it is the most authoritative "+
			"of the three tiers", set.asked.AppProtocol)
	}
	if set.asked.Port != 8443 {
		t.Errorf("port reached the manager as %d, want 8443", set.asked.Port)
	}
}

// The ceiling is a 409 with the number in it, and the contract has a response of
// its own for it: "try again later" is not actionable when the answer is that
// another tab holds them all.
//
// Classified through `forward.StatusFor` rather than by reading the sentence a
// second time: two functions sniffing error text with different needles for one
// job would turn the same refusal into a 409 in one interface and a 500 in the
// other.
func TestAForwardRefusedByTheCeilingReachesTheContractAsALimit(t *testing.T) {
	set := &fakeForwards{err: errors.New(
		"16 port-forwards are already open, which is the limit; close one first")}
	dock := apiDock{&Server{forwards: set}}

	_, err := dock.OpenForward(context.Background(), api.ForwardRequest{
		Cluster: "prod", Namespace: "default", Pod: "api", RemotePort: 8080})
	if !errors.Is(err, api.ErrLimitReached) {
		t.Errorf("the ceiling arrived as %v, want api.ErrLimitReached so the contract "+
			"answers its own 409 rather than a generic failure", err)
	}
	// The server's own sentence travels whole. Showing it beats inventing one,
	// and it is what stops a refused forward bottoming out at "failed: 429".
	if err == nil || !strings.Contains(err.Error(), "which is the limit") {
		t.Errorf("the manager's sentence did not travel: %v", err)
	}
}

// `POST /api/forwards` on a cluster marked read-only is the
// contract's 403 under `read_only` (`api.ErrReadOnly`), never `rbac_denied`:
// the guardrail, not the person's credentials. It carries the forwarder's
// sentence that names the marking - through the real manager rather than a fake, so the
// refusal and its classification are the ones that ship. The kubeconfig points
// at 127.0.0.1:1 and nothing is dialled: the refusal comes before the dig.
func TestAForwardOnAReadOnlyClusterIsRefusedThroughTheContract(t *testing.T) {
	clusters := serverFor(t, false).clusters
	manager := forward.New(forward.Options{
		Clusters: clusters,
		Ports:    "30000-30001",
		ReadOnly: func(name string) bool { return name == "prod" },
	})
	t.Cleanup(manager.Shutdown)
	dock := apiDock{&Server{forwards: manager}}

	_, err := dock.OpenForward(context.Background(), api.ForwardRequest{
		Cluster: "prod", Namespace: "default", Pod: "api", RemotePort: 8080})
	if !errors.Is(err, api.ErrReadOnly) {
		t.Fatalf("a forward on a read-only cluster answered %v, want api.ErrReadOnly (403 read_only)", err)
	}
	if !strings.Contains(err.Error(), "marked read-only in KT_READONLY_CLUSTERS") {
		t.Errorf("the refusal does not name the marking: %v", err)
	}
	if live, _ := dock.Forwards(); len(live) != 0 {
		t.Errorf("%d forwards listed after a refusal", len(live))
	}
}

// A build that cannot see its forwards says so. It does not answer `[]`.
//
// Answering an empty array for a process that is in fact holding four forwards
// is the collapse of an empty answer into an absent one, one layer up from an
// envelope — and it is the more tempting answer, because it renders.
func TestABuildThatCannotSeeItsForwardsSaysSoRatherThanAnsweringNone(t *testing.T) {
	cases := map[string]*Server{
		"no forwarder at all":                   {},
		"a forwarder that only mounts handlers": {forwards: handlersOnlyForwards{}},
	}
	for name, server := range cases {
		t.Run(name, func(t *testing.T) {
			live, err := apiDock{server}.Forwards()
			if err == nil {
				t.Fatalf("answered %v with no error; there is then no way to tell that "+
					"from a process holding none", live)
			}
			if !errors.Is(err, api.ErrNotInProfile) {
				t.Errorf("answered %v, want api.ErrNotInProfile", err)
			}
			if live != nil {
				t.Errorf("answered %v beside the error", live)
			}
		})
	}
}

// Closing one that is not there is not the same answer as a build that cannot
// close anything.
func TestClosingAForwardThatIsNotThereIsNotTheSameAsHavingNoForwarder(t *testing.T) {
	gone := apiDock{&Server{forwards: &fakeForwards{missing: true}}}.CloseForward("fwd-9")
	if gone == nil {
		t.Fatal("closing a forward that is not open reported success")
	}
	if errors.Is(gone, api.ErrNotInProfile) {
		t.Errorf("a missing forward arrived as %v, which says this build cannot close "+
			"forwards at all", gone)
	}
	if !strings.Contains(gone.Error(), "fwd-9") {
		t.Errorf("the refusal does not name what was not found: %v", gone)
	}
	if !errors.Is(gone, api.ErrNotFound) {
		t.Errorf("a missing forward arrived as %v, which is no sentinel, so `fail` answers 500 "+
			"internal; the contract declares 404", gone)
	}

	none := apiDock{&Server{}}.CloseForward("fwd-9")
	if !errors.Is(none, api.ErrNotInProfile) {
		t.Errorf("a build with no forwarder answered %v", none)
	}
}

// Through the route, because the status is what matters: `DELETE
// /api/forwards/{id}` on a forward that has already gone - closed in another
// tab, or ended with its pod - answers 404, as `api/openapi.yaml` declares, not
// `500 internal`. A client that closes a forward twice is told
// it is not there, not that the console broke.
func TestClosingAForwardThatIsGoneAnswers404(t *testing.T) {
	mux := http.NewServeMux()
	api.New(api.Deps{
		Dock:        apiDock{&Server{forwards: &fakeForwards{missing: true}}},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	request := httptest.NewRequest(http.MethodDelete, "/api/forwards/fwd-9", nil)
	request.Header.Set("X-KubeTower-Request", "1")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	var problem api.Problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("the body is not a problem: %s", recorder.Body.String())
	}
	if recorder.Code != http.StatusNotFound || problem.Code != api.ProblemNotFound {
		t.Errorf("closing a forward that is gone answered %d %s, want 404 %s: %s",
			recorder.Code, problem.Code, api.ProblemNotFound, problem.Detail)
	}
}

type fakeForwards struct {
	live    []forward.Tunnel
	asked   forward.Request
	err     error
	missing bool
}

func (f *fakeForwards) List() []forward.Tunnel { return f.live }

func (f *fakeForwards) Open(_ context.Context, req forward.Request) (forward.Tunnel, error) {
	f.asked = req
	if f.err != nil {
		return forward.Tunnel{}, f.err
	}
	return forward.Tunnel{ID: "fwd-1", Cluster: req.Cluster, Namespace: req.Namespace,
		Pod: req.Pod, Port: req.Port, LocalAddr: "127.0.0.1:34512", Scheme: "https"}, nil
}

func (f *fakeForwards) Close(string) bool { return !f.missing }

func (f *fakeForwards) OpenHandler() http.Handler  { return nil }
func (f *fakeForwards) ListHandler() http.Handler  { return nil }
func (f *fakeForwards) CloseHandler() http.Handler { return nil }

// handlersOnlyForwards is a legitimate build: something that mounts the template
// dock's three endpoints and cannot be asked anything else.
type handlersOnlyForwards struct{}

func (handlersOnlyForwards) OpenHandler() http.Handler  { return nil }
func (handlersOnlyForwards) ListHandler() http.Handler  { return nil }
func (handlersOnlyForwards) CloseHandler() http.Handler { return nil }

package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/logs"
)

// The dock's half of the mapping tests, and it exists for the same reason
// `listfilled_test.go` does: an absent optional member conforms.
//
// `api/openapi.yaml` declares `Container.ready` and `Container.restarts` with no
// `required` on either, so a response carrying only `name` and `init` validates
// against the schema and validates again tomorrow. The response-conformance test
// therefore cannot see this class of defect at all, and neither can
// `internal/api`'s member test, which drives a **fake** backend: it proves the
// contract type can carry the member, not that this adapter fills it.
//
// `apiDock.LogContainers` must set all four members. `Restarts` is the only
// thing in the browser that can tell whether a previous run exists — an
// EventSource never sees the status or the body of a failed handshake, so the
// 404 for "previous terminated container not found" arrives as nothing at all
// and the pane sits empty looking like a stream that has not started. With the
// member absent, the Prev-run control cannot be drawn at all, and the single
// most common reason to open a log panel — why is this pod crash-looping — has
// no answer in the console.
//
// The expectation is read out of `api/openapi.yaml` at run time rather than
// listed here, the way `listmembers_test.go` does it, so that a member added to
// the contract and not to the mapper fails here instead of being agreed with by
// construction.
func TestTheContainerListIsFilledFromTheStreamerThatHoldsIt(t *testing.T) {
	server := &Server{logs: containerFixture(t)}
	dock := apiDock{server}

	found, failure := dock.LogContainers(context.Background(),
		api.ObjectRef{Cluster: "prod", Namespace: "default", Name: "api-7f9c4b8d6-x2kqp"})
	if failure != nil {
		t.Fatalf("listing containers: %+v", failure)
	}

	// An empty answer and a failed answer are different answers, and both differ
	// from a list this test walked over without looking at anything.
	if len(found) != 2 {
		t.Fatalf("%d containers, want 2 (one init, one main): %+v", len(found), found)
	}

	// Init containers first, in the order they ran. `logs.Containers` promises it
	// and the mapper must not re-sort.
	if found[0].Name != "wait-for-db" || found[0].Init == nil || !*found[0].Init {
		t.Errorf("the init container is not first and marked init: %+v", found[0])
	}

	// --- The two members the contract declares and the adapter must send -------------------

	// Restarts. The chip reads `↺3` from it and the Prev-run control is drawn
	// from it; nothing else in the browser can tell a previous run exists.
	if found[1].Restarts == nil {
		t.Fatal("restarts is nil on the main container; the Prev-run control cannot be drawn at all, " +
			"and an EventSource cannot read the 404 the server would otherwise send")
	}
	if *found[1].Restarts != 3 {
		t.Errorf("restarts is %d, want 3 — the value comes from the container status, not from a default",
			*found[1].Restarts)
	}
	// Zero is a value and not an absence. A container that has never restarted
	// must arrive with `restarts: 0`, or the client cannot tell "never restarted"
	// from "this build does not send it".
	if found[0].Restarts == nil || *found[0].Restarts != 0 {
		t.Errorf("the init container's restarts is %v, want a present 0: a nil here reads as "+
			"'unknown' and the Prev-run refusal cannot be written", found[0].Restarts)
	}

	// Ready. Travels with Restarts because one chip shows both.
	if found[1].Ready == nil || !*found[1].Ready {
		t.Errorf("ready is %v on a container the status reports ready", found[1].Ready)
	}
	if found[0].Ready == nil || *found[0].Ready {
		t.Errorf("ready is %v on a container the status reports not ready; "+
			"the flag is per container and not per pod", found[0].Ready)
	}

	// --- Both directions, against the contract rather than against this file --
	//
	// The member set on the wire is the member set `api/openapi.yaml` declares.
	// A member the contract grows and this mapper never fills fails here; so does
	// one this mapper sends that the contract has deleted.
	declared := schemaProperties(t, "Container")
	encoded, err := json.Marshal(found[1])
	if err != nil {
		t.Fatalf("marshalling a container: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal(encoded, &sent); err != nil {
		t.Fatalf("re-reading a container: %v", err)
	}
	for name := range declared {
		if _, ok := sent[name]; !ok {
			t.Errorf("api/openapi.yaml declares Container.%s and the mapper never fills it; "+
				"an absent optional member conforms, so nothing else in this tree would say so", name)
		}
	}
	for name := range sent {
		if _, ok := declared[name]; !ok {
			t.Errorf("the mapper sends Container.%s and api/openapi.yaml does not declare it", name)
		}
	}
}

// schemaProperties reads one schema's property names out of the contract at run
// time. Listing them here instead would agree with the struct by construction
// and fail on nothing.
func schemaProperties(t *testing.T, schema string) map[string]struct{} {
	t.Helper()
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("reading the contract: %v", err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing the contract: %v", err)
	}
	found, ok := doc.Components.Schemas[schema]
	if !ok {
		t.Fatalf("api/openapi.yaml declares no schema %q", schema)
	}
	if len(found.Properties) == 0 {
		t.Fatalf("schema %q has no properties; the read is wrong, not the contract", schema)
	}
	out := make(map[string]struct{}, len(found.Properties))
	for name := range found.Properties {
		out[name] = struct{}{}
	}
	return out
}

// The framed log stream.
//
// The subject is the framing rather than the reading. `logs.Streamer` has its
// own tests for reading a container's log; what is pinned here is the
// translation into the contract's three event types, and it is the
// translation that decides whether a panel can tell a quiet container from a
// broken one. Every assertion below is about a frame the *server emits*, because
// an event that is never sent conforms to every schema.
func TestTheLogStreamIsFramedFromTheStreamerThatReadsIt(t *testing.T) {
	// Four lines, each stamped by the "apiserver" as the real one does with
	// `timestamps=true`: RFC3339Nano, a space, then what the container printed.
	stamped := []string{
		"2026-09-12T08:15:01.100000000Z starting up",
		"2026-09-12T08:15:02.200000000Z listening on :8080",
		`2026-09-12T08:15:03.300000000Z {"level":"error","msg":"db refused"}`,
		"2026-09-12T08:15:04.400000000Z bye",
	}
	server := &Server{logs: logFixture(t, stamped)}
	dock := apiDock{server}

	var got []api.LogEvent
	failure := dock.StreamLogs(context.Background(),
		api.LogQuery{
			ObjectRef:  api.ObjectRef{Cluster: "prod", Namespace: "default", Name: "api-7f9c4b8d6-x2kqp"},
			Follow:     false,
			TailLines:  200,
			Timestamps: true,
		},
		func(event api.LogEvent) error { got = append(got, event); return nil })
	if failure != nil {
		t.Fatalf("streaming: %+v", failure)
	}

	// Four lines and an end. An empty answer and a failed answer are different
	// answers, and both differ from a stream this test walked over.
	if len(got) != 5 {
		t.Fatalf("%d events, want 4 lines and an end: %+v", len(got), got)
	}

	// --- The line frame ------------------------------------------------------

	first := got[0]
	if first.Type != "line" {
		t.Fatalf("the first event is %q, want a line: %+v", first.Type, first)
	}

	// The stamp comes off the text and arrives as a field. Leaving it on means
	// every client re-parses it, and shifts every color offset on the line past
	// a prefix it did not print.
	if first.Text != "starting up" {
		t.Errorf("text is %q, want %q: the timestamp is still on the line",
			first.Text, "starting up")
	}
	if first.At == nil {
		t.Fatal("at is nil on a stamped line; the Time toggle then has nothing to read " +
			"and knowing when a line was written is lost")
	}
	if want := "2026-09-12T08:15:01.1Z"; first.At.UTC().Format(time.RFC3339Nano) != want {
		t.Errorf("at is %s, want %s", first.At.UTC().Format(time.RFC3339Nano), want)
	}

	// Every line says which container it came from. The caller named none, so the
	// answer is the first *main* container and not the init container that sorts
	// ahead of it — Kubernetes' own default is the first in the spec, which on a
	// pod with an init container in front reads as an empty log for a pod that is
	// talking perfectly well.
	for i, event := range got[:4] {
		if event.Container != "api" {
			t.Errorf("event %d carries container %q, want %q: the switcher would highlight "+
				"the wrong chip", i, event.Container, "api")
		}
	}

	// A line the container printed is passed through whole. It is the one thing
	// this stream must never edit: the operator came for the line.
	if got[2].Text != `{"level":"error","msg":"db refused"}` {
		t.Errorf("a JSON line arrived as %q", got[2].Text)
	}

	// --- The end frame -------------------------------------------------------

	end := got[4]
	if end.Type != "end" {
		t.Fatalf("the last event is %q, want an end: %+v", end.Type, end)
	}
	// Four lines came back against a tail of 200, so the read reached the
	// beginning of the log the cluster still holds. This is the only member that
	// can tell "there is nothing older" from "there is more and you did not ask",
	// and the panel's Read-earlier control is drawn from it.
	if end.AtStart == nil || !*end.AtStart {
		t.Errorf("atStart is %v on a bounded read that came back short of its tail; "+
			"a panel cannot then tell the beginning of the log from the beginning of the page",
			end.AtStart)
	}
}

// atStart is a fact or it is absent, and the second half is the one a port
// drops: a `false` asserts that there *is* more, which a follow has not
// established and cannot.
func TestAFollowEstablishesNoBeginningAndDoesNotClaimOne(t *testing.T) {
	server := &Server{logs: logFixture(t, []string{"one", "two"})}
	dock := apiDock{server}

	var got []api.LogEvent
	if f := dock.StreamLogs(context.Background(),
		api.LogQuery{
			ObjectRef: api.ObjectRef{Cluster: "prod", Namespace: "default", Name: "api-7f9c4b8d6-x2kqp"},
			Follow:    true,
			TailLines: 200,
		},
		func(event api.LogEvent) error { got = append(got, event); return nil }); f != nil {
		t.Fatalf("streaming: %+v", f)
	}
	if len(got) == 0 {
		t.Fatal("no events at all")
	}
	end := got[len(got)-1]
	if end.Type != "end" {
		t.Fatalf("the last event is %q, want an end", end.Type)
	}
	if end.AtStart != nil {
		t.Errorf("atStart is %v on a follow; a follow keeps receiving and establishes "+
			"no beginning, so the honest answer is to say nothing rather than false", *end.AtStart)
	}
	// Without timestamps the first word of a line belongs to the container and
	// must not be parsed as a date.
	if got[0].At != nil {
		t.Errorf("at is set on a read that did not ask for timestamps; the first word of " +
			"a line belongs to the container")
	}
	if got[0].Text != "one" {
		t.Errorf("text is %q, want %q", got[0].Text, "one")
	}
}

// A condition established before the first line is still a frame, not a status.
// The response is already a 200 by then; a silent close is the stream-shaped
// version of `{"items": []}`.
func TestAStreamThatCannotStartSaysSoInsideTheStream(t *testing.T) {
	server := &Server{logs: logFixture(t, nil)}
	dock := apiDock{server}

	var got []api.LogEvent
	failure := dock.StreamLogs(context.Background(),
		// A cluster the resolver does not know. `logs.Streamer` answers
		// `unknown cluster "nosuch"` before anything is opened.
		api.LogQuery{
			ObjectRef: api.ObjectRef{Cluster: "", Namespace: "default", Name: "api-7f9c4b8d6-x2kqp"},
			Follow:    true, TailLines: 200,
		},
		func(event api.LogEvent) error { got = append(got, event); return nil })

	// Before the stream opened, so this arm *may* answer a Failure — the handler
	// turns it into a state frame. What must not happen is silence.
	if failure == nil && len(got) == 0 {
		t.Fatal("neither a failure nor a frame: a stream that will not start rendered as nothing at all")
	}
	if failure != nil {
		if failure.State == api.StateOK || failure.State == "" {
			t.Errorf("the failure carries state %q", failure.State)
		}
		return
	}
	if got[0].Type != "state" {
		t.Errorf("the first event is %q, want a state frame naming why", got[0].Type)
	}
	if got[len(got)-1].Type != "end" {
		t.Error("the stream did not end; a panel would sit on a spinner")
	}
}

// logFixture is a real logs.Streamer over a fake apiserver that serves both the
// pod GET behind the container default and the log body itself.
func logFixture(t *testing.T, lines []string) *logs.Streamer {
	t.Helper()
	return newLogStreamer(t, func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/log") {
			return false
		}
		w.Header().Set("Content-Type", "text/plain")
		for _, line := range lines {
			_, _ = io.WriteString(w, line+"\n")
		}
		return true
	})
}

// containerFixture is a real logs.Streamer over a fake apiserver, because the
// subject is the mapping from what the streamer holds to what the contract
// carries. A fake Streamer would let the mapper read a member the real one never
// sets, which is the failure next door.
func containerFixture(t *testing.T) *logs.Streamer {
	t.Helper()
	return newLogStreamer(t, func(http.ResponseWriter, *http.Request) bool { return false })
}

// newLogStreamer serves the fixture pod, and hands anything else to `extra`
// first so a test can add the log body.
func newLogStreamer(t *testing.T, extra func(http.ResponseWriter, *http.Request) bool) *logs.Streamer {
	t.Helper()
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-7f9c4b8d6-x2kqp", Namespace: "default"},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "wait-for-db"}},
			Containers:     []corev1.Container{{Name: "api"}},
		},
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{
				{Name: "wait-for-db", Ready: false, RestartCount: 0},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "api", Ready: true, RestartCount: 3},
			},
		},
	}
	body, err := json.Marshal(pod)
	if err != nil {
		t.Fatalf("encoding the fixture pod: %v", err)
	}
	apiserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if extra(w, r) {
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/pods/api-7f9c4b8d6-x2kqp") {
			http.Error(w, "not this test's endpoint: "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(apiserver.Close)
	return logs.New(fakeLogClients{t: t, host: apiserver.URL}, accesstest.Allow(), nil, nil)
}

type fakeLogClients struct {
	t    *testing.T
	host string
}

func (f fakeLogClients) Resolve(name string) (string, bool) { return name, name != "" }

func (f fakeLogClients) Client(string) (*cluster.Client, error) { return f.build() }

func (f fakeLogClients) StreamingClient(string) (*cluster.Client, error) { return f.build() }

func (f fakeLogClients) build() (*cluster.Client, error) {
	config := &rest.Config{Host: f.host}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &cluster.Client{Context: "test", Config: config, Clients: clientset}, nil
}

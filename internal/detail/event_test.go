package detail

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// What an event says, read out of the object.
//
// The message is the whole point. The list clips it to 140 characters because a
// table cell has to, and everything an operator opens an event for is in the
// part that was cut off — the scheduler's reason, the kubelet's error, the
// webhook's refusal. A panel that clipped it again would be the list over.

func eventObject(t *testing.T, doc string) *unstructured.Unstructured {
	t.Helper()
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(doc), &raw); err != nil {
		t.Fatalf("parsing the fixture: %v", err)
	}
	return &unstructured.Unstructured{Object: raw}
}

// A real one, near enough: 0/6 nodes available is the message every operator
// has read, and it is 236 characters long.
const failedScheduling = `
apiVersion: v1
kind: Event
metadata:
  name: api-7d9f.17f0a1b2c3d4e5f6
  namespace: shop
  creationTimestamp: "2026-08-17T09:12:00Z"
type: Warning
reason: FailedScheduling
message: "0/6 nodes are available: 3 node(s) had untolerated taint {node-role.kubernetes.io/control-plane: }, 2 Insufficient cpu, 1 node(s) had volume node affinity conflict. preemption: 0/6 nodes are available: 6 Preemption is not helpful for scheduling."
count: 47
firstTimestamp: "2026-08-17T09:12:00Z"
lastTimestamp: "2026-08-17T10:34:12Z"
source:
  component: default-scheduler
involvedObject:
  kind: Pod
  name: api-7d9f
  namespace: shop
  fieldPath: spec.containers{api}
`

func TestAnEventCarriesItsWholeMessage(t *testing.T) {
	got := eventDetailOf(eventObject(t, failedScheduling), "Event")
	if got == nil {
		t.Fatal("an Event produced no panel")
	}

	// The assertion this file exists for. 140 is where the list cuts it and 400
	// is where the drawer's event *list* cuts it; neither may apply here.
	if len(got.Message) < 200 {
		t.Fatalf("the message was clipped to %d characters: %q", len(got.Message), got.Message)
	}
	if !strings.HasSuffix(got.Message, "not helpful for scheduling.") {
		t.Fatalf("the end of the message is missing, which is where the reason is: %q", got.Message)
	}

	if got.Reason != "FailedScheduling" || got.Type != "Warning" || !got.Warning() {
		t.Fatalf("reason and type did not survive: %+v", got)
	}
	// 47 is a different fact from 1, and the count column of the list is the
	// only other place it is said.
	if got.Count != 47 || !got.Repeated() {
		t.Fatalf("the count did not survive: %d", got.Count)
	}
	if got.Source != "default-scheduler" {
		t.Fatalf("the reporter did not survive: %q", got.Source)
	}
	if got.ObjectKind != "Pod" || got.ObjectName != "api-7d9f" || got.ObjectNamespace != "shop" {
		t.Fatalf("the involved object did not survive: %+v", got)
	}
	if got.ObjectField != "spec.containers{api}" {
		t.Fatalf("the field path did not survive: %q", got.ObjectField)
	}
	if got.Object() != "Pod/api-7d9f" {
		t.Fatalf("the heading is wrong: %q", got.Object())
	}

	// Both timestamps, and they must differ: an event repeating for an hour and
	// one that fired forty-seven times in a minute are different problems, and
	// only the pair tells them apart.
	if got.First.IsZero() || got.Last.IsZero() {
		t.Fatalf("a timestamp is missing: %+v", got)
	}
	if !got.Last.After(got.First) {
		t.Fatalf("first and last seen came out the same: %v %v", got.First, got.Last)
	}
	if got.Last.Sub(got.First) != 82*time.Minute+12*time.Second {
		t.Fatalf("the window between them is wrong: %v", got.Last.Sub(got.First))
	}
}

// The newer API spells every one of those fields differently. A cluster whose
// preferred version for `events` is events.k8s.io/v1 must not get a blank panel
// — which reads as broken rather than as looking in the wrong field.
const newAPIEvent = `
apiVersion: events.k8s.io/v1
kind: Event
metadata:
  name: web-0.17f0aa
  namespace: shop
  creationTimestamp: "2026-08-17T09:00:00Z"
type: Warning
reason: Failed
note: "Failed to pull image \"registry.internal/app:1.4.2\": rpc error: code = Unknown desc = failed to pull and unpack image: unexpected status from HEAD request: 401 Unauthorized"
action: Pulling
eventTime: "2026-08-17T09:00:04.000000Z"
reportingController: kubelet
series:
  count: 9
  lastObservedTime: "2026-08-17T09:20:04.000000Z"
regarding:
  kind: Pod
  name: web-0
  namespace: shop
`

func TestAnEventFromTheNewerAPIIsReadToo(t *testing.T) {
	got := eventDetailOf(eventObject(t, newAPIEvent), "Event")
	if got == nil {
		t.Fatal("an Event produced no panel")
	}
	if !strings.Contains(got.Message, "401 Unauthorized") {
		t.Fatalf("`note` was not read as the message: %q", got.Message)
	}
	if got.Source != "kubelet" {
		t.Fatalf("`reportingController` was not read as the source: %q", got.Source)
	}
	if got.Action != "Pulling" {
		t.Fatalf("the action did not survive: %q", got.Action)
	}
	if got.Count != 9 {
		t.Fatalf("`series.count` was not read as the count: %d", got.Count)
	}
	if got.ObjectKind != "Pod" || got.ObjectName != "web-0" {
		t.Fatalf("`regarding` was not read as the involved object: %+v", got)
	}
	// Without the eventTime fallbacks a fresh event reads as 1970, which looks
	// like a bug in this console rather than like a field that is not there.
	if got.First.Year() != 2026 || got.Last.Year() != 2026 {
		t.Fatalf("the timestamps fell back to the epoch: %v %v", got.First, got.Last)
	}
}

// An event about a Node is filed in `default` and its reference carries no
// namespace. Copying the event's own across would state that a cluster-scoped
// object lives in a namespace — a name taken for a namespace, which passes
// every validator on its way to being wrong.
const nodeEvent = `
apiVersion: v1
kind: Event
metadata:
  name: node-3.17f0bb
  namespace: default
type: Normal
reason: NodeNotReady
message: "Node node-3 status is now: NodeNotReady"
count: 1
source:
  component: node-controller
  host: node-3
involvedObject:
  kind: Node
  name: node-3
`

func TestAnEventDoesNotGiveItsOwnNamespaceToAClusterScopedObject(t *testing.T) {
	got := eventDetailOf(eventObject(t, nodeEvent), "Event")
	if got == nil {
		t.Fatal("an Event produced no panel")
	}
	if got.ObjectNamespace != "" {
		t.Fatalf("a cluster-scoped object was given the event's namespace %q", got.ObjectNamespace)
	}
	if got.Node != "node-3" {
		t.Fatalf("source.host did not survive: %q", got.Node)
	}
	// A count the API left at zero is an event that happened once, and "0 times"
	// is a thing that did not happen.
	if got.Count != 1 || got.Repeated() {
		t.Fatalf("a single occurrence came out as %d", got.Count)
	}
	// With no timestamps at all the panel must still not claim 1970.
	if got.Last.IsZero() != got.First.IsZero() {
		t.Fatalf("one timestamp was filled and the other was not: %v %v", got.First, got.Last)
	}
}

// Nil for everything else, and it has to be nil rather than a zero value: the
// drawer branches on the pointer, and a struct of empty strings would draw a
// panel of blank rows on every pod in the cluster.
func TestOnlyAnEventGetsTheEventPanel(t *testing.T) {
	pod := eventObject(t, "apiVersion: v1\nkind: Pod\nmetadata:\n  name: api\n  namespace: shop\n")
	if got := eventDetailOf(pod, "Pod"); got != nil {
		t.Fatalf("a Pod was given an event panel: %+v", got)
	}
	if got := eventDetailOf(nil, "Event"); got != nil {
		t.Fatal("a nil object was given an event panel")
	}
	if !IsEventKind("Event") || IsEventKind("Pod") {
		t.Fatal("IsEventKind does not recognize the kind it is named for")
	}
}

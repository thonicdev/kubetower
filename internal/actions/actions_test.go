package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/identity"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/edit"
)

// Every test in this package runs against a fake clientset, and none of them may
// be made to run against anything else.
//
// This package deletes pods, evicts them, cordons nodes and drains them. Never a
// mutating command against a real cluster: a guard test that finds a hole proves
// it by exercising it, and against a live cluster that does the damage it was
// looking for. There is no `KT_INTEGRATION_*` escape hatch in this file and
// there must not be one.

var (
	podGVR         = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	nodeGVR        = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	deploymentGVR  = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	daemonSetGVR   = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}
	cronJobGVR     = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}
	jobGVR         = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	applicationGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
)

// stamp is the clock every test runs on, so the rollout annotation and a
// triggered Job's name have exactly one right answer.
var stamp = time.Date(2026, 8, 18, 9, 30, 0, 0, time.UTC)

// --- the harness --------------------------------------------------------------

// recorded is one request that reached the cluster. The assertions are as much
// about these as about return values: "it refused" and "it refused and sent
// nothing" are different claims and only the second is a guardrail.
type recorded struct {
	Verb        string
	Resource    string
	Subresource string
	Namespace   string
	Name        string
	Body        string
	DryRun      bool
	UID         string
	// GracePeriod is a delete's gracePeriodSeconds; nil where none was sent.
	GracePeriod *int64
}

// auditSink collects audit records; the bulk fan-out writes from four goroutines.
type auditSink struct {
	mu  sync.Mutex
	buf []string
}

func (s *auditSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, line := range strings.Split(strings.TrimSpace(string(p)), "\n") {
		if line != "" {
			s.buf = append(s.buf, line)
		}
	}
	return len(p), nil
}

func (s *auditSink) all(t *testing.T) []audit.Record {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []audit.Record
	for _, line := range s.buf {
		var r audit.Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("not a record: %v: %s", err, line)
		}
		out = append(out, r)
	}
	return out
}

type harness struct {
	t      *testing.T
	dyn    *dynamicfake.FakeDynamicClient
	runner *Runner
	// reviews answers the access review every write asks; nil Answer allows.
	reviews *accesstest.Reviews

	// logMu guards logged. RunBulk acts on BulkConcurrency objects at once and
	// every one of them writes a `write:` line through edit.Writer.Logf, so this
	// closure is called from four goroutines. Without the lock the append races
	// and `go test -race ./...` fails in this package. The production Logf is
	// log.Printf and is safe; a race in this double would bury a real race
	// anywhere in the tree.
	logMu  sync.Mutex
	logged []string

	// records is what the gate's audit writer received, one JSON line each.
	records auditSink
	// evictErr, when set, is what the eviction subresource answers.
	evictErr error
}

type fakeResolver struct {
	dyn     dynamic.Interface
	cluster string
}

func (f fakeResolver) Locate(_ context.Context, _, kind string) (edit.Location, error) {
	switch strings.ToLower(kind) {
	case "pods", "pod":
		return edit.Location{Cluster: f.cluster, Resource: podGVR, Kind: "Pod", Namespaced: true, Dynamic: f.dyn}, nil
	case "nodes", "node":
		return edit.Location{Cluster: f.cluster, Resource: nodeGVR, Kind: "Node", Namespaced: false, Dynamic: f.dyn}, nil
	case "deployments", "deployment":
		return edit.Location{Cluster: f.cluster, Resource: deploymentGVR, Kind: "Deployment", Namespaced: true, Dynamic: f.dyn}, nil
	case "daemonsets", "daemonset":
		return edit.Location{Cluster: f.cluster, Resource: daemonSetGVR, Kind: "DaemonSet", Namespaced: true, Dynamic: f.dyn}, nil
	case "cronjobs", "cronjob":
		return edit.Location{Cluster: f.cluster, Resource: cronJobGVR, Kind: "CronJob", Namespaced: true, Dynamic: f.dyn}, nil
	case "applications.argoproj.io":
		return edit.Location{Cluster: f.cluster, Resource: applicationGVR, Kind: "Application", Namespaced: true, Dynamic: f.dyn}, nil
	case "jobs", "job":
		return edit.Location{Cluster: f.cluster, Resource: jobGVR, Kind: "Job", Namespaced: true, Dynamic: f.dyn}, nil
	}
	return edit.Location{}, fmt.Errorf("%q is not a kind this cluster serves", kind)
}

func newHarness(t *testing.T, readOnly func(string) bool, objects ...*unstructured.Unstructured) *harness {
	t.Helper()

	listKinds := map[schema.GroupVersionResource]string{
		podGVR:         "PodList",
		nodeGVR:        "NodeList",
		deploymentGVR:  "DeploymentList",
		daemonSetGVR:   "DaemonSetList",
		cronJobGVR:     "CronJobList",
		jobGVR:         "JobList",
		applicationGVR: "ApplicationList",
	}
	seeds := make([]runtime.Object, 0, len(objects))
	for _, object := range objects {
		seeds = append(seeds, object)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, seeds...)

	h := &harness{t: t, dyn: dyn}

	// A dry run changes nothing. The fake tracker does not know that — it stores
	// what a dry-run create hands it, so the commit that follows is refused as
	// "already exists" — and a harness that does not model the one property the
	// dry run is *for* would make the validate-then-commit pattern untestable.
	dyn.PrependReactor("create", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create, ok := action.(k8stesting.CreateActionImpl)
		if !ok || !hasDryRun(create.CreateOptions.DryRun) {
			return false, nil, nil
		}
		return true, create.GetObject(), nil
	})

	// The eviction subresource. Intercepted rather than left to the default
	// tracker, which treats a subresource create as an update of the parent and
	// would quietly replace the pod object with the Eviction.
	dyn.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create, ok := action.(k8stesting.CreateAction)
		if !ok || create.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		return true, create.GetObject(), h.evictErr
	})

	h.reviews = &accesstest.Reviews{}
	writer := edit.New(edit.Options{
		Resolve:  fakeResolver{dyn: dyn, cluster: "prod"},
		Access:   access.New(access.Options{Reviews: h.reviews.For, Audit: audit.New(&h.records)}),
		ReadOnly: readOnly,
		Logf: func(format string, args ...any) {
			h.logMu.Lock()
			defer h.logMu.Unlock()
			h.logged = append(h.logged, fmt.Sprintf(format, args...))
		},
	})
	h.runner = New(Options{Writer: writer, Now: func() time.Time { return stamp }})
	return h
}

// writes are every request that changed something, in order. A `get` and a
// `list` are reads and are deliberately not here: the question these tests keep
// asking is "what did this send", and a refusal that still sent a GET is not a
// hole.
func (h *harness) writes() []recorded {
	var out []recorded
	for _, action := range h.dyn.Actions() {
		entry := recorded{
			Verb:        action.GetVerb(),
			Resource:    action.GetResource().Resource,
			Subresource: action.GetSubresource(),
			Namespace:   action.GetNamespace(),
		}
		switch typed := action.(type) {
		case k8stesting.PatchActionImpl:
			entry.Name = typed.Name
			entry.Body = string(typed.Patch)
			entry.DryRun = hasDryRun(typed.PatchOptions.DryRun)
		case k8stesting.DeleteActionImpl:
			entry.Name = typed.Name
			entry.DryRun = hasDryRun(typed.DeleteOptions.DryRun)
			if typed.DeleteOptions.Preconditions != nil && typed.DeleteOptions.Preconditions.UID != nil {
				entry.UID = string(*typed.DeleteOptions.Preconditions.UID)
			}
			entry.GracePeriod = typed.DeleteOptions.GracePeriodSeconds
		case k8stesting.CreateActionImpl:
			entry.Name = typed.Name
			entry.DryRun = hasDryRun(typed.CreateOptions.DryRun)
			if object, ok := typed.Object.(*unstructured.Unstructured); ok {
				raw, _ := json.Marshal(object.Object)
				entry.Body = string(raw)
				if entry.Name == "" {
					entry.Name = object.GetName()
				}
			}
		default:
			continue
		}
		out = append(out, entry)
	}
	return out
}

func hasDryRun(values []string) bool {
	for _, value := range values {
		if value == metav1.DryRunAll {
			return true
		}
	}
	return false
}

// noWrites is the assertion that matters on every refusal: nothing left the
// process.
func (h *harness) noWrites() {
	h.t.Helper()
	if writes := h.writes(); len(writes) != 0 {
		h.t.Fatalf("a refused action sent %d request(s) anyway: %+v", len(writes), writes)
	}
}

// --- fixtures -----------------------------------------------------------------

// ownedBy sets the controller ownerReference, which is what decides whether
// deleting a pod restarts it or ends it. Used as pod("prod", "api-1", ownedBy(
// "ReplicaSet", "api")) so a fixture states its owner instead of implying one.
func ownedBy(kind, name string) func(map[string]any) {
	return func(object map[string]any) {
		meta := object["metadata"].(map[string]any)
		meta["ownerReferences"] = []any{map[string]any{
			"apiVersion": "apps/v1", "kind": kind, "name": name,
			"uid": "uid-" + name, "controller": true,
		}}
	}
}

func pod(namespace, name string, mutate ...func(map[string]any)) *unstructured.Unstructured {
	object := map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":            name,
			"namespace":       namespace,
			"uid":             "uid-" + name,
			"resourceVersion": "100",
		},
		"spec":   map[string]any{"nodeName": "node-a"},
		"status": map[string]any{"phase": "Running"},
	}
	for _, m := range mutate {
		m(object)
	}
	return &unstructured.Unstructured{Object: object}
}

// replicaCount states a scale target. A helper rather than an inline `&n`
// because `Request.Replicas` is a pointer precisely so that *not* calling this
// is a different request from calling it with zero, and a test that says
// `replicaCount(0)` is visibly making that distinction.
func replicaCount(n int64) *int64 { return &n }

func node(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Node",
		"metadata":   map[string]any{"name": name, "uid": "uid-" + name, "resourceVersion": "7"},
		"spec":       map[string]any{},
	}}
}

func deployment(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": name, "namespace": namespace, "uid": "uid-" + name, "resourceVersion": "12"},
		"spec": map[string]any{
			"replicas": int64(3),
			"template": map[string]any{"metadata": map[string]any{}, "spec": map[string]any{}},
		},
	}}
}

func cronJob(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "CronJob",
		"metadata":   map[string]any{"name": name, "namespace": namespace, "uid": "uid-" + name, "resourceVersion": "4"},
		"spec": map[string]any{
			"schedule": "*/5 * * * *",
			"suspend":  false,
			"jobTemplate": map[string]any{
				"metadata": map[string]any{
					"labels":      map[string]any{"app": "billing"},
					"annotations": map[string]any{"team": "payments"},
				},
				"spec": map[string]any{
					"template": map[string]any{"spec": map[string]any{"restartPolicy": "Never"}},
				},
			},
		},
	}}
}

func target(kind, namespace, name string) Target {
	return Target{Cluster: "prod", Kind: kind, Namespace: namespace, Name: name}
}

func refusal(t *testing.T, err error) *edit.Refusal {
	t.Helper()
	if err == nil {
		t.Fatal("expected a refusal and got none")
	}
	out, ok := edit.AsRefusal(err)
	if !ok {
		t.Fatalf("expected one of internal/edit's refusals, got %T: %v", err, err)
	}
	return out
}

// --- the guardrail ------------------------------------------------------------

// TestReadOnlyRefusesEveryAction is the one that must never be deleted.
//
// It walks every verb this package offers, against a cluster marked in
// KT_READONLY_CLUSTERS, and asserts two things about each: the refusal names the
// setting, and **nothing was sent**. The second is the guardrail; the first is
// only how an operator finds out why.
//
// Table-driven over `Known`, not over a list written here, so a verb added later
// without an entry fails this test rather than shipping unguarded.
func TestReadOnlyRefusesEveryAction(t *testing.T) {
	every := []Verb{Delete, Restart, Evict, Cordon, Uncordon, Drain, Suspend, Resume, Trigger, Scale}

	for _, verb := range every {
		t.Run(string(verb), func(t *testing.T) {
			h := newHarness(t, func(string) bool { return true },
				pod("prod", "api-1"), node("node-a"), deployment("prod", "api"), cronJob("prod", "billing"))

			request := Request{
				Target: target(kindFor(verb), namespaceFor(verb), nameFor(verb)),
				Verb:   verb, Replicas: replicaCount(2), Confirm: confirmFor(verb),
			}
			_, err := h.runner.Run(context.Background(), request)
			out := refusal(t, err)
			if out.Kind != edit.RefusedReadOnly {
				t.Fatalf("%s on a read-only cluster refused as %v: %s", verb, out.Kind, out.Reason)
			}
			if !strings.Contains(out.Reason, "KT_READONLY_CLUSTERS") {
				t.Errorf("the refusal does not name the setting that caused it: %s", out.Reason)
			}
			h.noWrites()
		})
	}

	// The table above is the whole set only for as long as somebody keeps it that
	// way, so the count is asserted too: a verb added to Known without a row here
	// fails this test instead of shipping untested against the guardrail.
	if got := countKnownVerbs(); got != len(every) {
		t.Fatalf("this package knows %d verbs and only %d are exercised against a "+
			"read-only cluster. Add the new one to `every` above — an action that has "+
			"never been refused is an action nobody has checked can be.", got, len(every))
	}
}

// countKnownVerbs counts what Known accepts, by asking it about every verb this
// package declares. The candidate list is written out rather than derived,
// because Go has no way to enumerate the constants of a type — which is exactly
// why the count above is worth asserting.
func countKnownVerbs() int {
	candidates := []Verb{Delete, Restart, Evict, Cordon, Uncordon, Drain, Suspend, Resume, Trigger, Scale}
	count := 0
	for _, verb := range candidates {
		if Known(verb) {
			count++
		}
	}
	return count
}

func kindFor(verb Verb) string {
	switch verb {
	case Cordon, Uncordon, Drain:
		return "nodes"
	case Suspend, Resume, Trigger:
		return "cronjobs"
	case Scale:
		return "deployments"
	case Evict:
		return "pods"
	case Restart:
		return "deployments"
	}
	return "pods"
}

func namespaceFor(verb Verb) string {
	if kindFor(verb) == "nodes" {
		return ""
	}
	return "prod"
}

func nameFor(verb Verb) string {
	switch kindFor(verb) {
	case "nodes":
		return "node-a"
	case "cronjobs":
		return "billing"
	case "deployments":
		return "api"
	}
	return "api-1"
}

// confirmFor returns a *valid* confirmation, so that the read-only refusal is
// being tested rather than the confirmation. If the guardrail ever moved below
// the confirmation check, these tests would go on passing and mean nothing —
// which is why the confirmation is correct here on purpose.
func confirmFor(verb Verb) string {
	// SingleConfirm and not this file's own copy of the rule, and against the
	// kind this verb is actually pointed at rather than "pods": a pod is
	// confirmed by its count, and a helper answering "the name" would confirm
	// nothing for half this table.
	return SingleConfirm(verb, kindFor(verb), nameFor(verb))
}

// TestReadOnlyIsCheckedBeforeTheConfirmation pins the order of the three
// statements at the top of Run.
//
// A read-only cluster asked for a delete with no confirmation must answer
// "read-only", not "type the name to confirm" — the second reads as an
// instruction, and an operator who follows it has typed the name of a production
// object into a box before being told it was never going to work.
func TestReadOnlyIsCheckedBeforeTheConfirmation(t *testing.T) {
	h := newHarness(t, func(string) bool { return true }, pod("prod", "api-1"))
	_, err := h.runner.Run(context.Background(), Request{
		Target: target("pods", "prod", "api-1"), Verb: Delete, Confirm: "",
	})
	if out := refusal(t, err); out.Kind != edit.RefusedReadOnly {
		t.Fatalf("expected the read-only refusal first, got %v: %s", out.Kind, out.Reason)
	}
	h.noWrites()
}

// --- confirmation ---------------------------------------------------------------

func TestConfirmationIsRequired(t *testing.T) {
	cases := []struct {
		name    string
		request Request
	}{
		{"delete with nothing typed", Request{Target: target("pods", "prod", "api-1"), Verb: Delete}},
		{"delete with a name typed in place of the token", Request{Target: target("pods", "prod", "api-1"), Verb: Delete, Confirm: "api-1"}},
		{"restart on a pod, unconfirmed", Request{Target: target("pods", "prod", "api-1"), Verb: Restart}},
		{"evict, unconfirmed", Request{Target: target("pods", "prod", "api-1"), Verb: Evict}},
		{"evict with a made-up token", Request{Target: target("pods", "prod", "api-1"), Verb: Evict, Confirm: "yes"}},
		{"cordon, unconfirmed", Request{Target: target("nodes", "", "node-a"), Verb: Cordon}},
		{"drain with nothing typed", Request{Target: target("nodes", "", "node-a"), Verb: Drain}},
		{"suspend, unconfirmed", Request{Target: target("cronjobs", "prod", "billing"), Verb: Suspend}},
		{"trigger, unconfirmed", Request{Target: target("cronjobs", "prod", "billing"), Verb: Trigger}},
		{"scale, unconfirmed", Request{Target: target("deployments", "prod", "api"), Verb: Scale, Replicas: replicaCount(5)}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, nil,
				pod("prod", "api-1"), node("node-a"), deployment("prod", "api"), cronJob("prod", "billing"))
			_, err := h.runner.Run(context.Background(), c.request)
			if out := refusal(t, err); out.Kind != edit.RefusedInput {
				t.Fatalf("expected an input refusal, got %v: %s", out.Kind, out.Reason)
			}
			h.noWrites()
		})
	}
}

// The typed confirmation has to be the object's own name and no substitute, or
// it is a yes/no with extra steps.
func TestTypedConfirmationIsTheObjectName(t *testing.T) {
	if !Typed(Delete, "pods") || !Typed(Drain, "pods") {
		t.Fatal("delete and drain must be confirmed by name")
	}
	// Restart is absent from this list on purpose: on a Pod it *is* a delete,
	// so it is confirmed like one. See
	// TestRestartingPodsIsConfirmedLikeDeletingThem. On a controller it is an
	// annotation write and stays here, which is why the check takes a kind.
	for _, verb := range []Verb{Evict, Cordon, Uncordon, Suspend, Resume, Trigger, Scale} {
		if Typed(verb, "pods") {
			t.Errorf("%s asks for a typed name; only the irreversible ones should", verb)
		}
	}
	if Typed(Restart, "deployments") {
		t.Error("a rollout restart asks for a typed name; its pods come back by construction")
	}

	// The name rule's own case - surrounding whitespace forgiven - is a
	// drain's: a single delete is confirmed by a button, and
	// TestOneDeleteIsConfirmedByAButton pins that.
	if got := SingleConfirm(Drain, "nodes", "node-a"); got != "node-a" {
		t.Errorf("SingleConfirm(drain, nodes, node-a) = %q, want the name", got)
	}
	if err := checkConfirm(Request{Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "  node-a "}); err != nil {
		t.Fatalf("a name pasted with whitespace around it should be accepted: %v", err)
	}
}

// One resource is deleted on a button: "Are you sure?", Delete, Cancel - no
// name, no count. The box and the check are still
// one function, so this asserts both halves, and the boundary: a delete over a
// selection keeps its typed count (`BulkConfirm`), a restart on a pod keeps
// "1", a drain keeps the node's name.
func TestOneDeleteIsConfirmedByAButton(t *testing.T) {
	for _, c := range []struct{ kind, name string }{
		{"pods", "api-7d9f4c8b6d-x4k2p"}, {"deployments", "api"}, {"namespaces", "prod"}, {"configmaps", "settings"},
	} {
		if got := SingleConfirm(Delete, c.kind, c.name); got != ConfirmToken {
			t.Errorf("SingleConfirm(delete, %s, %s) = %q, want the token: one delete is a button", c.kind, c.name, got)
		}
		if TypedOne(Delete, c.kind) {
			t.Errorf("TypedOne(delete, %s) is true; one delete is a button", c.kind)
		}
		if !Typed(Delete, c.kind) {
			t.Errorf("Typed(delete, %s) is false; a delete over a selection keeps its typed count", c.kind)
		}
	}
	if got := BulkConfirm(Delete, "pods", 3); got != "3" {
		t.Errorf("BulkConfirm(delete, pods, 3) = %q, want the count: bulk keeps its confirmation", got)
	}
	if got := SingleConfirm(Restart, "pods", "api-7d9f4c8b6d-x4k2p"); got != "1" {
		t.Errorf("SingleConfirm(restart, pods, …) = %q, want %q — untouched by the delete's button", got, "1")
	}
	if got := SingleConfirm(Drain, "pods", "api-1"); got != "api-1" {
		t.Errorf("SingleConfirm(drain, pods, api-1) = %q, want the name", got)
	}

	// The server's half: the token is accepted, and the count and the name
	// are not, because the box asks for neither.
	h := newHarness(t, nil, pod("prod", "api-1"))
	if _, err := h.runner.Run(context.Background(), Request{
		Target: target("pods", "prod", "api-1"), Verb: Delete, Confirm: ConfirmToken,
	}); err != nil {
		t.Fatalf("a delete confirmed by its button was refused: %v", err)
	}
	for _, typed := range []string{"1", "api-1"} {
		h2 := newHarness(t, nil, pod("prod", "api-1"))
		_, err := h2.runner.Run(context.Background(), Request{
			Target: target("pods", "prod", "api-1"), Verb: Delete, Confirm: typed,
		})
		if out := refusal(t, err); out.Kind != edit.RefusedInput {
			t.Fatalf("a delete confirmed by %q was accepted or refused wrongly (%v)", typed, out.Kind)
		}
		h2.noWrites()
	}
}

// A forced delete is sent with a grace period of zero, and an ordinary one
// with none, so the object's own terminationGracePeriodSeconds applies.
// Force on any other verb is refused before
// anything is sent - a restart on a pod is a delete, and must not inherit it.
func TestAForcedDeleteCarriesAZeroGracePeriod(t *testing.T) {
	h := newHarness(t, nil, pod("prod", "api-1"))
	if _, err := h.runner.Run(context.Background(), Request{
		Target: target("pods", "prod", "api-1"), Verb: Delete, Confirm: ConfirmToken, Force: true,
	}); err != nil {
		t.Fatalf("forced delete: %v", err)
	}
	writes := h.writes()
	if len(writes) != 1 || writes[0].Verb != "delete" {
		t.Fatalf("expected one delete, got %+v", writes)
	}
	if writes[0].GracePeriod == nil || *writes[0].GracePeriod != 0 {
		t.Errorf("a forced delete did not carry gracePeriodSeconds=0: %+v", writes[0])
	}

	plain := newHarness(t, nil, pod("prod", "api-1"))
	if _, err := plain.runner.Run(context.Background(), Request{
		Target: target("pods", "prod", "api-1"), Verb: Delete, Confirm: ConfirmToken,
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := plain.writes(); len(got) != 1 || got[0].GracePeriod != nil {
		t.Errorf("an ordinary delete set a grace period; the pod's own should apply: %+v", got)
	}

	other := newHarness(t, nil, pod("prod", "api-1"))
	_, err := other.runner.Run(context.Background(), Request{
		Target: target("pods", "prod", "api-1"), Verb: Restart, Confirm: "1", Force: true,
	})
	// Refused by Run's own guard, by its own words: a restart refused for any
	// other reason would pass a check on the kind alone.
	if out := refusal(t, err); out.Kind != edit.RefusedInput || !strings.Contains(out.Reason, "force is a delete's") {
		t.Fatalf("force on a restart was accepted or refused for another reason (%v: %s)", out.Kind, out.Reason)
	}
	other.noWrites()
}

// --- delete and evict are not the same request ---------------------------------

// TestDeleteIssuesADelete and TestEvictIssuesAnEviction are two tests rather
// than one because the property is symmetric and both halves matter: delete must
// not evict, and evict must not delete. A single test asserting "evict posts an
// Eviction" would still pass if evict *also* deleted the pod.
func TestDeleteIssuesADelete(t *testing.T) {
	h := newHarness(t, nil, pod("prod", "api-1"))

	result, err := h.runner.Run(context.Background(), Request{
		Target: target("pods", "prod", "api-1"), Verb: Delete, Confirm: ConfirmToken,
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	writes := h.writes()
	if len(writes) != 1 {
		t.Fatalf("expected exactly one write, got %d: %+v", len(writes), writes)
	}
	got := writes[0]
	if got.Verb != "delete" || got.Resource != "pods" || got.Name != "api-1" || got.Namespace != "prod" {
		t.Fatalf("delete sent the wrong request: %+v", got)
	}
	if got.Subresource != "" {
		t.Fatalf("delete used the %q subresource; a delete is a delete", got.Subresource)
	}
	// The precondition that stops a delete-by-name removing a pod's replacement.
	if got.UID != "uid-api-1" {
		t.Errorf("delete did not carry the object's UID as a precondition: %+v", got)
	}
	if !strings.Contains(result.Message, "Deleted") {
		t.Errorf("the sentence does not say what happened: %q", result.Message)
	}
}

func TestEvictIssuesAnEvictionAndNeverADelete(t *testing.T) {
	h := newHarness(t, nil, pod("prod", "api-1"))

	if _, err := h.runner.Run(context.Background(), Request{
		Target: target("pods", "prod", "api-1"), Verb: Evict, Confirm: ConfirmToken,
	}); err != nil {
		t.Fatalf("evict: %v", err)
	}

	writes := h.writes()
	if len(writes) != 1 {
		t.Fatalf("expected exactly one write, got %d: %+v", len(writes), writes)
	}
	got := writes[0]
	if got.Verb == "delete" {
		t.Fatal("evict issued a DELETE. An eviction that deletes regardless of a " +
			"PodDisruptionBudget is the exact lie this separation exists to prevent")
	}
	if got.Verb != "create" || got.Subresource != "eviction" || got.Resource != "pods" {
		t.Fatalf("evict did not post to the eviction subresource: %+v", got)
	}
	if got.Name != "api-1" || got.Namespace != "prod" {
		t.Fatalf("the eviction was addressed to the wrong pod: %+v", got)
	}
	// The body is a policy/v1 Eviction naming the same pod. The API server checks
	// this and a mismatch is a 400 that reads as an internal error.
	for _, want := range []string{`"apiVersion":"policy/v1"`, `"kind":"Eviction"`, `"name":"api-1"`, `"namespace":"prod"`} {
		if !strings.Contains(got.Body, want) {
			t.Errorf("the eviction body is missing %s: %s", want, got.Body)
		}
	}
	// No dry run: see the package comment. A dry run that is quietly not honored
	// by the eviction endpoint would *be* the eviction.
	if got.DryRun {
		t.Error("the eviction was sent as a dry run; there is no way from here to tell " +
			"a simulated eviction from a real one, so it must never be attempted")
	}
}

// A PodDisruptionBudget refusing an eviction is the feature working, and has to
// read that way.
func TestEvictReportsADisruptionBudgetAsSuch(t *testing.T) {
	h := newHarness(t, nil, pod("prod", "api-1"))
	h.evictErr = apierrors.NewTooManyRequests("Cannot evict pod as it would violate the pod's disruption budget.", 5)

	_, err := h.runner.Run(context.Background(), Request{
		Target: target("pods", "prod", "api-1"), Verb: Evict, Confirm: ConfirmToken,
	})
	out := refusal(t, err)
	if !strings.Contains(out.Reason, "PodDisruptionBudget") {
		t.Errorf("a 429 from the eviction endpoint has to name the budget: %s", out.Reason)
	}
	if !strings.Contains(out.Reason, "Nothing was removed") {
		t.Errorf("the operator has to be told the pod is still there: %s", out.Reason)
	}
}

// Restart on a Pod is a delete, and the sentence has to say the word. The
// fixture states an owner that recreates the pod: restarting a pod **nothing
// owns** is refused, since nothing would make another.
func TestRestartingAPodIsADeleteAndSaysSo(t *testing.T) {
	h := newHarness(t, nil, pod("prod", "api-1", ownedBy("ReplicaSet", "api")))

	// The object's own name, not the token: restarting a pod deletes it, so it
	// is confirmed the way deleting it is.
	result, err := h.runner.Run(context.Background(), Request{
		Target: target("pods", "prod", "api-1"), Verb: Restart, Confirm: "1",
	})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	writes := h.writes()
	if len(writes) != 1 || writes[0].Verb != "delete" {
		t.Fatalf("restarting a pod should issue one delete: %+v", writes)
	}
	if !strings.Contains(strings.ToLower(result.Message), "deleted") {
		t.Errorf("restarting a pod must be described as a delete, not as a restart: %q", result.Message)
	}
}

// --- the rollout restart --------------------------------------------------------

// The annotation is the whole behavior, so its name, its place in the object
// and the format of its value are all pinned. A patch that puts the same
// timestamp anywhere else is a button that reports success and does nothing.
func TestRolloutRestartWritesWhatKubectlWrites(t *testing.T) {
	for _, kind := range []string{"deployments", "daemonsets"} {
		t.Run(kind, func(t *testing.T) {
			seed := deployment("prod", "api")
			if kind == "daemonsets" {
				seed = &unstructured.Unstructured{Object: map[string]any{
					"apiVersion": "apps/v1", "kind": "DaemonSet",
					"metadata": map[string]any{"name": "api", "namespace": "prod", "resourceVersion": "3"},
					"spec":     map[string]any{"template": map[string]any{"metadata": map[string]any{}}},
				}}
			}
			h := newHarness(t, nil, seed)

			if _, err := h.runner.Run(context.Background(), Request{
				Target: target(kind, "prod", "api"), Verb: Restart, Confirm: ConfirmToken,
			}); err != nil {
				t.Fatalf("restart: %v", err)
			}

			writes := h.writes()
			// Validate, then commit: two patches with the same body, the first a
			// dry run. That is internal/edit's contract and this reuses it.
			if len(writes) != 2 {
				t.Fatalf("expected a dry run and a commit, got %d: %+v", len(writes), writes)
			}
			if !writes[0].DryRun {
				t.Error("the first patch was not a dry run, so an admission webhook would " +
					"refuse this half-way through rather than before it started")
			}
			if writes[1].DryRun {
				t.Error("the second patch was a dry run, so nothing was committed")
			}

			want := `{"spec":{"template":{"metadata":{"annotations":` +
				`{"kubectl.kubernetes.io/restartedAt":"2026-08-18T09:30:00Z"}}}}}`
			if writes[1].Body != want {
				t.Errorf("the rollout patch is not what kubectl writes.\n got: %s\nwant: %s", writes[1].Body, want)
			}
			if writes[0].Body != writes[1].Body {
				t.Errorf("the dry run validated a different body from the one committed:\n%s\n%s",
					writes[0].Body, writes[1].Body)
			}
		})
	}
}

// The annotation's constant is the contract with kubectl; a typo in it is a
// silent no-op, so it is asserted against the literal string rather than
// against itself.
func TestRestartAnnotationIsTheKubectlOne(t *testing.T) {
	if RestartedAtAnnotation != "kubectl.kubernetes.io/restartedAt" {
		t.Fatalf("the rollout annotation is %q; kubectl reads kubectl.kubernetes.io/restartedAt "+
			"and nothing else, so anything different is a patch no controller acts on", RestartedAtAnnotation)
	}
}

func TestRolloutRestartRefusesAKindWithNoPodTemplate(t *testing.T) {
	h := newHarness(t, nil, cronJob("prod", "billing"))
	_, err := h.runner.Run(context.Background(), Request{
		Target: target("cronjobs", "prod", "billing"), Verb: Restart, Confirm: ConfirmToken,
	})
	if out := refusal(t, err); out.Kind != edit.RefusedInput {
		t.Fatalf("expected an input refusal, got %v: %s", out.Kind, out.Reason)
	}
	h.noWrites()
}

// --- cordon, suspend, scale -----------------------------------------------------

func TestCordonAndUncordonPatchTheOneField(t *testing.T) {
	for _, c := range []struct {
		verb Verb
		want string
	}{
		{Cordon, `{"spec":{"unschedulable":true}}`},
		{Uncordon, `{"spec":{"unschedulable":false}}`},
	} {
		t.Run(string(c.verb), func(t *testing.T) {
			h := newHarness(t, nil, node("node-a"))
			if _, err := h.runner.Run(context.Background(), Request{
				Target: target("nodes", "", "node-a"), Verb: c.verb, Confirm: ConfirmToken,
			}); err != nil {
				t.Fatalf("%s: %v", c.verb, err)
			}
			writes := h.writes()
			if len(writes) != 2 {
				t.Fatalf("expected a dry run and a commit, got %+v", writes)
			}
			if writes[1].Body != c.want {
				t.Errorf("the %s patch is %s, want %s", c.verb, writes[1].Body, c.want)
			}
			if writes[1].Namespace != "" {
				t.Errorf("a node is cluster-scoped; this was sent into namespace %q", writes[1].Namespace)
			}
		})
	}
}

// The suspend patch shape, both ways. `false` matters as much as `true`: a merge
// patch that omitted the field on Resume would leave the CronJob suspended and
// report that it had been resumed.
func TestSuspendAndResumePatchSpecSuspend(t *testing.T) {
	for _, c := range []struct {
		verb Verb
		want string
	}{
		{Suspend, `{"spec":{"suspend":true}}`},
		{Resume, `{"spec":{"suspend":false}}`},
	} {
		t.Run(string(c.verb), func(t *testing.T) {
			h := newHarness(t, nil, cronJob("prod", "billing"))
			if _, err := h.runner.Run(context.Background(), Request{
				Target: target("cronjobs", "prod", "billing"), Verb: c.verb, Confirm: ConfirmToken,
			}); err != nil {
				t.Fatalf("%s: %v", c.verb, err)
			}
			writes := h.writes()
			if len(writes) != 2 {
				t.Fatalf("expected a dry run and a commit, got %+v", writes)
			}
			if writes[1].Body != c.want {
				t.Errorf("the %s patch is %s, want %s", c.verb, writes[1].Body, c.want)
			}
			if writes[1].Resource != "cronjobs" || writes[1].Name != "billing" {
				t.Errorf("the patch went to the wrong object: %+v", writes[1])
			}
		})
	}
}

func TestSuspendRefusesAnythingThatIsNotACronJob(t *testing.T) {
	h := newHarness(t, nil, deployment("prod", "api"))
	_, err := h.runner.Run(context.Background(), Request{
		Target: target("deployments", "prod", "api"), Verb: Suspend, Confirm: ConfirmToken,
	})
	if out := refusal(t, err); out.Kind != edit.RefusedInput {
		t.Fatalf("expected an input refusal, got %v: %s", out.Kind, out.Reason)
	}
	h.noWrites()
}

func TestScale(t *testing.T) {
	h := newHarness(t, nil, deployment("prod", "api"))
	if _, err := h.runner.Run(context.Background(), Request{
		Target: target("deployments", "prod", "api"), Verb: Scale, Replicas: replicaCount(5), Confirm: ConfirmToken,
	}); err != nil {
		t.Fatalf("scale: %v", err)
	}
	writes := h.writes()
	if len(writes) != 2 || writes[1].Body != `{"spec":{"replicas":5}}` {
		t.Fatalf("the scale patch is wrong: %+v", writes)
	}
}

// TestScaleRefusesARequestThatStatedNoReplicaCount is why `Replicas` is a
// pointer.
//
// Were `Replicas` an int64, a caller that never mentioned replicas would ask
// for zero of them — and both guards below it let zero through, because zero is
// neither negative nor above the ceiling. `internal/api`'s `ActionRequest`
// carries no replica count at all, so a scale through the API would patch
// `spec.replicas: 0` after a preview and a single acknowledgement.
//
// The assertion that matters is `noWrites`. "It returned an error" and "it
// returned an error and sent nothing" are different claims, and an action that
// errors *after* patching is the failure this whole test is about.
func TestScaleRefusesARequestThatStatedNoReplicaCount(t *testing.T) {
	h := newHarness(t, nil, deployment("prod", "api"))
	_, err := h.runner.Run(context.Background(), Request{
		Target: target("deployments", "prod", "api"), Verb: Scale, Confirm: ConfirmToken,
		// Replicas deliberately absent. This is the whole test.
	})
	out := refusal(t, err)
	if out.Kind != edit.RefusedInput {
		t.Fatalf("expected an input refusal, got %v: %s", out.Kind, out.Reason)
	}
	// The sentence has to name the distinction, or an operator reads it as the
	// console refusing a scale to zero — which it does not.
	if !strings.Contains(out.Reason, "no replica count was stated") {
		t.Errorf("the refusal does not say what was missing: %s", out.Reason)
	}
	h.noWrites()
}

// TestScaleToZeroIsAcceptedWhenItIsStated is the other half, and it is not a
// formality: the cheap fix for the test above is to refuse zero, and that would
// break a real operation an operator performs on purpose.
//
// Stating zero and saying nothing must be two different requests. This asserts
// on the patch body rather than on the absence of an error, because "it was
// accepted" and "it sent exactly `replicas: 0`" are different claims.
func TestScaleToZeroIsAcceptedWhenItIsStated(t *testing.T) {
	h := newHarness(t, nil, deployment("prod", "api"))
	result, err := h.runner.Run(context.Background(), Request{
		Target: target("deployments", "prod", "api"), Verb: Scale,
		Replicas: replicaCount(0), Confirm: ConfirmToken,
	})
	if err != nil {
		t.Fatalf("a deliberate scale to zero was refused: %v", err)
	}
	writes := h.writes()
	if len(writes) != 2 {
		t.Fatalf("expected a dry run and a commit, got %+v", writes)
	}
	for _, write := range writes {
		if write.Body != `{"spec":{"replicas":0}}` {
			t.Fatalf("the patch is not a scale to zero: %+v", writes)
		}
	}
	if !writes[0].DryRun || writes[1].DryRun {
		t.Errorf("expected validate-then-commit, got %+v", writes)
	}
	if !strings.Contains(result.Message, "to 0 replica(s)") {
		t.Errorf("the message does not say what it scaled to: %q", result.Message)
	}
}

// TestPreviewDoesNotInventAReplicaCount pins the recap against the same defect.
//
// A preview that printed "spec.replicas: 3 -> 0" for a request naming no count
// would be the confirmation agreeing with the bug: the operator would read an
// accurate-looking sentence describing something nobody asked for, and agree to
// it. With no count stated the placeholder FieldFor returns has to survive.
func TestPreviewDoesNotInventAReplicaCount(t *testing.T) {
	h := newHarness(t, nil, deployment("prod", "api"))

	unstated, err := h.runner.Preview(context.Background(), Request{
		Target: target("deployments", "prod", "api"), Verb: Scale,
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if unstated.After == "0" {
		t.Fatal("a preview of a scale that stated no count reads as a scale to zero")
	}
	_, placeholder, _ := FieldFor(Scale)
	if unstated.After != placeholder {
		t.Errorf("After is %q, expected the placeholder %q", unstated.After, placeholder)
	}

	// And a stated zero still previews as zero, so the guard above is about what
	// was said rather than about the digit.
	stated, err := h.runner.Preview(context.Background(), Request{
		Target: target("deployments", "prod", "api"), Verb: Scale, Replicas: replicaCount(0),
	})
	if err != nil {
		t.Fatalf("preview of a stated zero: %v", err)
	}
	if stated.After != "0" {
		t.Errorf("a stated zero previewed as %q, expected \"0\"", stated.After)
	}
}

func TestScaleRefusesNonsense(t *testing.T) {
	for _, replicas := range []int64{-1, maxReplicas + 1} {
		h := newHarness(t, nil, deployment("prod", "api"))
		_, err := h.runner.Run(context.Background(), Request{
			Target: target("deployments", "prod", "api"), Verb: Scale, Replicas: replicaCount(replicas), Confirm: ConfirmToken,
		})
		if out := refusal(t, err); out.Kind != edit.RefusedInput {
			t.Fatalf("%d replicas: expected an input refusal, got %v", replicas, out.Kind)
		}
		h.noWrites()
	}
}

// --- running a CronJob now --------------------------------------------------------

func TestTriggerCreatesAJobFromTheTemplate(t *testing.T) {
	h := newHarness(t, nil, cronJob("prod", "billing"))

	result, err := h.runner.Run(context.Background(), Request{
		Target: target("cronjobs", "prod", "billing"), Verb: Trigger, Confirm: ConfirmToken,
	})
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}

	writes := h.writes()
	if len(writes) != 2 {
		t.Fatalf("expected a dry-run create and a real one, got %+v", writes)
	}
	if !writes[0].DryRun || writes[1].DryRun {
		t.Errorf("a create can be dry-run without ambiguity and should be: %+v", writes)
	}
	got := writes[1]
	if got.Verb != "create" || got.Resource != "jobs" || got.Namespace != "prod" {
		t.Fatalf("the Job went somewhere unexpected: %+v", got)
	}
	wantName := fmt.Sprintf("billing-manual-%d", stamp.Unix())
	if got.Name != wantName {
		t.Errorf("the Job is named %q, want %q", got.Name, wantName)
	}
	for _, want := range []string{
		`"kind":"Job"`,
		`"apiVersion":"batch/v1"`,
		`"cronjob.kubernetes.io/instantiate":"manual"`,
		`"team":"payments"`, // the template's own annotations survive
		`"app":"billing"`,   // and its labels
		`"restartPolicy":"Never"`,
	} {
		if !strings.Contains(got.Body, want) {
			t.Errorf("the Job is missing %s:\n%s", want, got.Body)
		}
	}
	if strings.Contains(got.Body, "ownerReferences") {
		t.Error("the Job carries an ownerReference. kubectl's --from does not set one, " +
			"and inventing one here would make the CronJob controller clean up a run it did not make")
	}
	if !strings.Contains(result.Message, wantName) {
		t.Errorf("the sentence does not name the Job it made: %q", result.Message)
	}
}

func TestTriggerRefusesSomethingWithNoJobTemplate(t *testing.T) {
	naked := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1", "kind": "CronJob",
		"metadata": map[string]any{"name": "empty", "namespace": "prod"},
		"spec":     map[string]any{"schedule": "* * * * *"},
	}}
	h := newHarness(t, nil, naked)
	_, err := h.runner.Run(context.Background(), Request{
		Target: target("cronjobs", "prod", "empty"), Verb: Trigger, Confirm: ConfirmToken,
	})
	if out := refusal(t, err); out.Kind != edit.RefusedInput {
		t.Fatalf("expected an input refusal, got %v: %s", out.Kind, out.Reason)
	}
	h.noWrites()
}

func TestJobNameStaysWithinTheLimit(t *testing.T) {
	long := strings.Repeat("a", 60)
	name := jobName(long, stamp)
	if len(name) > 63 {
		t.Fatalf("a Job name of %d characters is refused by the API server: %q", len(name), name)
	}
	if strings.HasPrefix(name, "-") || strings.Contains(name, "--manual") {
		t.Errorf("the truncation produced a name that is not a DNS label: %q", name)
	}
}

// --- drain --------------------------------------------------------------------

// The first stage cordons and plans, and evicts nothing. This is the one that
// would be quietly wrong if somebody "simplified" drain into a single call.
func TestDrainFirstStageCordonsAndEvictsNothing(t *testing.T) {
	h := newHarness(t, nil,
		node("node-a"),
		pod("prod", "api-1", ownedBy("ReplicaSet", "api")),
		pod("prod", "api-2", ownedBy("ReplicaSet", "api")),
	)

	result, err := h.runner.Run(context.Background(), Request{
		Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "node-a",
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}

	for _, write := range h.writes() {
		if write.Subresource == "eviction" {
			t.Fatal("the planning stage of a drain evicted a pod. The stage that shows " +
				"you what would happen must not be the stage that makes it happen")
		}
		if write.Verb == "delete" {
			t.Fatal("the planning stage of a drain deleted something")
		}
	}
	if result.Plan == nil || !result.Plan.Cordoned {
		t.Fatal("the plan does not report the node as cordoned, which it must be before the list means anything")
	}
	if len(result.Plan.Evict) != 2 {
		t.Fatalf("expected both pods in the plan, got %d", len(result.Plan.Evict))
	}
	if !strings.Contains(result.Message, "Nothing has been evicted yet") {
		t.Errorf("the sentence has to say nothing has happened yet: %q", result.Message)
	}
}

func TestDrainSecondStageEvicts(t *testing.T) {
	h := newHarness(t, nil,
		node("node-a"),
		pod("prod", "api-1", ownedBy("ReplicaSet", "api")),
		pod("prod", "api-2", ownedBy("ReplicaSet", "api")),
	)

	result, err := h.runner.Run(context.Background(), Request{
		Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "node-a", Stage: StageEvict,
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}

	var evictions, deletes int
	for _, write := range h.writes() {
		if write.Subresource == "eviction" {
			evictions++
		}
		if write.Verb == "delete" {
			deletes++
		}
	}
	if evictions != 2 {
		t.Fatalf("expected two evictions, got %d: %+v", evictions, h.writes())
	}
	if deletes != 0 {
		t.Fatal("a drain deleted a pod. Drain evicts, so that a PodDisruptionBudget can stop it")
	}
	if result.Plan.Requested != 2 || result.Plan.Failed != 0 {
		t.Errorf("the plan does not report what happened: %+v", result.Plan)
	}
}

// What a drain leaves alone, and why. kubectl's rules, because an operator's
// expectations were formed by kubectl.
func TestDrainSkipsWhatKubectlSkips(t *testing.T) {
	daemon := pod("kube-system", "fluent-bit-x", func(o map[string]any) {
		yes := true
		object := &unstructured.Unstructured{Object: o}
		object.SetOwnerReferences([]metav1.OwnerReference{
			{APIVersion: "apps/v1", Kind: "DaemonSet", Name: "fluent-bit", Controller: &yes},
		})
	})
	mirror := pod("kube-system", "kube-apiserver-node-a", func(o map[string]any) {
		object := &unstructured.Unstructured{Object: o}
		object.SetAnnotations(map[string]string{"kubernetes.io/config.mirror": "abc"})
	})
	finished := pod("prod", "migrate-1", func(o map[string]any) {
		o["status"] = map[string]any{"phase": "Succeeded"}
	})
	elsewhere := pod("prod", "other-node-pod", func(o map[string]any) {
		o["spec"] = map[string]any{"nodeName": "node-b"}
	})
	orphan := pod("prod", "hand-made")
	emptyDir := pod("prod", "cache-1", func(o map[string]any) {
		yes := true
		object := &unstructured.Unstructured{Object: o}
		object.SetOwnerReferences([]metav1.OwnerReference{
			{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "cache", Controller: &yes},
		})
		spec := o["spec"].(map[string]any)
		spec["volumes"] = []any{map[string]any{"name": "scratch", "emptyDir": map[string]any{}}}
	})

	h := newHarness(t, nil, node("node-a"), daemon, mirror, finished, elsewhere, orphan, emptyDir)

	result, err := h.runner.Run(context.Background(), Request{
		Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "node-a",
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	plan := result.Plan

	skipped := map[string]string{}
	for _, ref := range plan.Skip {
		skipped[ref.Name] = ref.Skip
	}
	for _, name := range []string{"fluent-bit-x", "kube-apiserver-node-a", "migrate-1", "hand-made"} {
		if skipped[name] == "" {
			t.Errorf("%s should have been left alone and was not", name)
		}
	}
	// kubectl drain refuses a pod nothing owns without --force; this drain
	// leaves it on the node and says why.
	if skipped["hand-made"] != UnownedSkip {
		t.Errorf("a pod nothing owns is left alone for the wrong reason: %q", skipped["hand-made"])
	}

	evicting := map[string]PodRef{}
	for _, ref := range plan.Evict {
		evicting[ref.Name] = ref
	}
	// The one that matters most: a pod on another node must never appear. The
	// fake ignores the field selector, exactly as a server that did not support
	// it would, and the client-side check is what stops the whole cluster being
	// drained.
	if _, found := evicting["other-node-pod"]; found {
		t.Fatal("a pod on another node is in the plan. The field selector is not " +
			"something to rely on: spec.nodeName is checked again for this reason")
	}
	if _, found := evicting["hand-made"]; found {
		t.Error("a pod nothing owns is evicted by a drain that was not asked to include it; " +
			"nothing would create it again")
	}
	if ref, found := evicting["cache-1"]; !found {
		t.Error("a pod with emptyDir should be evictable, with a warning")
	} else if !strings.Contains(ref.Warning, "emptyDir") {
		t.Errorf("an emptyDir pod must be flagged as losing its data, got %q", ref.Warning)
	}
	if !plan.Warnings() {
		t.Error("the plan carries warnings but does not report that it does")
	}
}

// A drain asked to include the pods nothing owns - kubectl's --force - evicts
// them, each with its warning, and still leaves alone what no drain evicts.
func TestADrainIncludingUnownedPodsEvictsThemWithTheirWarning(t *testing.T) {
	daemon := pod("kube-system", "fluent-bit-x", ownedBy("DaemonSet", "fluent-bit"))
	orphan := pod("kube-system", "storage-provisioner")
	h := newHarness(t, nil, node("node-a"), daemon, orphan)

	result, err := h.runner.Run(context.Background(), Request{
		Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "node-a",
		Stage: StageEvict, IncludeUnowned: true,
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(result.Plan.Evict) != 1 || result.Plan.Evict[0].Name != "storage-provisioner" {
		t.Fatalf("the drain including unowned pods did not plan to evict the one there is: %+v", result.Plan)
	}
	if result.Plan.Evict[0].Warning == "" {
		t.Error("a pod nothing owns is evicted with no warning on it")
	}
	if len(result.Plan.Skip) != 1 || result.Plan.Skip[0].Name != "fluent-bit-x" {
		t.Errorf("including unowned pods changed what else is left alone: %+v", result.Plan.Skip)
	}
	var evicted []string
	for _, write := range h.writes() {
		if write.Subresource == "eviction" {
			evicted = append(evicted, write.Name)
		}
	}
	if len(evicted) != 1 || evicted[0] != "storage-provisioner" {
		t.Errorf("evictions sent: %v", evicted)
	}
}

// And without it the same drain evicts nothing, and its sentence says the node
// is not empty: a drain that left a pod behind and reported success on a node
// "drained" would collapse "empty" and "pods left alone" into one answer.
func TestADrainThatSparedUnownedPodsSaysTheNodeIsNotEmpty(t *testing.T) {
	orphan := pod("kube-system", "storage-provisioner")
	owned := pod("prod", "api-1", ownedBy("ReplicaSet", "api"))
	h := newHarness(t, nil, node("node-a"), orphan, owned)

	result, err := h.runner.Run(context.Background(), Request{
		Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "node-a", Stage: StageEvict,
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	for _, write := range h.writes() {
		if write.Subresource == "eviction" && write.Name == "storage-provisioner" {
			t.Fatal("a pod nothing owns was evicted by default")
		}
	}
	if !strings.Contains(result.Message, "storage-provisioner") || !strings.Contains(result.Message, "not empty") {
		t.Errorf("the sentence does not say which pod nothing owns stayed, or that the node is not empty: %q",
			result.Message)
	}
}

// An empty node must not be reported as one that could not be asked, and the
// other way round: an empty answer and a failed one are different answers.
func TestDrainOnAnEmptyNodeSaysSo(t *testing.T) {
	h := newHarness(t, nil, node("node-a"))
	result, err := h.runner.Run(context.Background(), Request{
		Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "node-a",
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if result.Plan.Note == "" {
		t.Error("a node with no pods on it should say which of the two things that means")
	}
}

func TestDrainRefusesAnythingThatIsNotANode(t *testing.T) {
	h := newHarness(t, nil, pod("prod", "api-1"))
	_, err := h.runner.Run(context.Background(), Request{
		Target: target("pods", "prod", "api-1"), Verb: Drain, Confirm: "api-1",
	})
	if out := refusal(t, err); out.Kind != edit.RefusedInput {
		t.Fatalf("expected an input refusal, got %v: %s", out.Kind, out.Reason)
	}
	h.noWrites()
}

// --- odds and ends ---------------------------------------------------------------

func TestUnknownVerbIsRefusedBeforeAnythingIsLookedUp(t *testing.T) {
	h := newHarness(t, nil, pod("prod", "api-1"))
	_, err := h.runner.Run(context.Background(), Request{
		Target: target("pods", "prod", "api-1"), Verb: Verb("nuke"), Confirm: ConfirmToken,
	})
	if out := refusal(t, err); out.Kind != edit.RefusedInput {
		t.Fatalf("expected an input refusal, got %v", out.Kind)
	}
	h.noWrites()
}

// A forced delete reads as forced on every `write:` line that records it - the
// applied line this package writes, and the refused and failed lines the write
// door writes - and an ordinary delete's lines do not say force at all
// The audit record already says it; the process log is what an operator reads
// along.
func TestAForcedDeleteSaysForceOnEveryWriteLine(t *testing.T) {
	deleteLines := func(h *harness) []string {
		h.logMu.Lock()
		defer h.logMu.Unlock()
		var lines []string
		for _, line := range h.logged {
			if strings.HasPrefix(line, "write: ") && strings.Contains(line, "delete") {
				lines = append(lines, line)
			}
		}
		return lines
	}
	failDeletes := func(h *harness) {
		h.dyn.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewConflict(podGVR.GroupResource(), "api-1", fmt.Errorf("changed"))
		})
	}
	denyAll := func(h *harness) {
		h.reviews.Answer = func(accesstest.Question) (bool, error) { return false, nil }
	}

	for _, tc := range []struct {
		name    string
		arrange func(*harness)
		outcome string
	}{
		{"applied", func(*harness) {}, "outcome=applied"},
		{"failed at the API server", failDeletes, "outcome=failed"},
		{"refused by the review", denyAll, "outcome=refused"},
	} {
		for _, force := range []bool{true, false} {
			h := newHarness(t, nil, pod("prod", "api-1"))
			tc.arrange(h)
			_, err := h.runner.Run(context.Background(), Request{
				Target: target("pods", "prod", "api-1"), Verb: Delete, Confirm: ConfirmToken, Force: force,
			})
			if (err == nil) != (tc.outcome == "outcome=applied") {
				t.Fatalf("%s, force=%v: unexpected error %v", tc.name, force, err)
			}
			lines := deleteLines(h)
			if len(lines) != 1 || !strings.Contains(lines[0], tc.outcome) {
				t.Fatalf("%s, force=%v: want one %s line, got %v", tc.name, force, tc.outcome, lines)
			}
			if said := strings.Contains(lines[0], " force=true"); said != force {
				t.Errorf("%s, force=%v: the line says force=%v: %s", tc.name, force, said, lines[0])
			}
		}
	}
}

// A stale panel must not delete the object that replaced the one it was drawn
// from.
func TestDeleteRefusesAStaleResourceVersion(t *testing.T) {
	h := newHarness(t, nil, pod("prod", "api-1"))
	_, err := h.runner.Run(context.Background(), Request{
		Target: target("pods", "prod", "api-1"), Verb: Delete, Confirm: ConfirmToken,
		Precondition: "99",
	})
	if out := refusal(t, err); out.Kind != edit.RefusedConflict {
		t.Fatalf("expected a conflict refusal, got %v: %s", out.Kind, out.Reason)
	}
	h.noWrites()
}

func TestActionsAreLogged(t *testing.T) {
	h := newHarness(t, nil, pod("prod", "api-1"))
	if _, err := h.runner.Run(context.Background(), Request{
		Target: target("pods", "prod", "api-1"), Verb: Delete, Confirm: ConfirmToken,
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	found := false
	for _, line := range h.logged {
		if strings.HasPrefix(line, "write: ") && strings.Contains(line, "action=delete") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a change to a cluster left no `write:` line behind: %v", h.logged)
	}
}

func TestANilWriterRefusesRatherThanPanics(t *testing.T) {
	runner := New(Options{})
	_, err := runner.Run(context.Background(), Request{
		Target: target("pods", "prod", "api-1"), Verb: Delete, Confirm: ConfirmToken,
	})
	if err == nil {
		t.Fatal("a Runner with no write path must refuse, not proceed")
	}
}

// A delete carrying no Precondition is still sent, and still carries the UID.
//
// The version precondition is optional — a bulk run passes none — and
// TestDeleteRefusesAStaleResourceVersion above proves it works when given. This
// test states the other half: with none, the UID is the precondition there is.
func TestADeleteWithNoResourceVersionStillCarriesTheUID(t *testing.T) {
	h := newHarness(t, nil, pod("prod", "api-1"))

	// No Precondition. If this ever starts being refused, every request that
	// carries none has stopped working.
	if _, err := h.runner.Run(context.Background(), Request{
		Target: target("pods", "prod", "api-1"), Verb: Delete, Confirm: ConfirmToken,
	}); err != nil {
		t.Fatalf("a delete carrying no resourceVersion was refused: %v", err)
	}

	writes := h.writes()
	if len(writes) != 1 {
		t.Fatalf("expected exactly one write, got %d: %+v", len(writes), writes)
	}
	// The precondition that does run, and with no Precondition it is the only
	// one there is. A recreated pod has a new UID even
	// when its name, namespace and kind are identical, which is more than a
	// resourceVersion would give.
	if writes[0].UID != "uid-api-1" {
		t.Errorf("the delete carried no UID precondition: %+v. That is what stops a "+
			"delete-by-name removing the replacement of the object that was on screen",
			writes[0])
	}
}

// Restart is refused on a pod nothing will recreate, and refused **server-side**.
//
// Restarting a pod is a delete, which this package says plainly, and it must
// also ask whether anything would make another:
//
//   - a **Job's** pod: the delete spends one of the Job's backoffLimit retries and
//     at the limit the Job is marked failed. So Restart does not restart, it can
//     end the run — and the pod of a CronJob is exactly this case.
//   - a **bare** pod: nothing makes another, so Restart is a permanent delete
//     wearing a gentler word.
//
// Asserted here rather than only in the drawer, because the drawer withholding the
// button is the courtesy and this is the control: a stale tab, a bookmarked POST
// and a hand-written curl all arrive with no button having existed. And **nothing
// sent** is half the assertion — a refusal that still issued the delete would be a
// message rather than a guardrail.
func TestRestartIsRefusedOnAPodNothingWillRecreate(t *testing.T) {
	for _, c := range []struct {
		what  string
		owner func(map[string]any)
		says  string
	}{
		{"the pod of a CronJob's Job", ownedBy("Job", "nightly-28123456"), "backoffLimit"},
		{"a pod nothing owns", nil, "nothing owns"},
	} {
		t.Run(c.what, func(t *testing.T) {
			var fixture *unstructured.Unstructured
			if c.owner != nil {
				fixture = pod("prod", "api-1", c.owner)
			} else {
				fixture = pod("prod", "api-1")
			}
			h := newHarness(t, nil, fixture)

			_, err := h.runner.Run(context.Background(), Request{
				Target: target("pods", "prod", "api-1"), Verb: Restart, Confirm: "1",
			})
			if err == nil {
				t.Fatal("the restart was allowed")
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the refusal does not say why: %v", err)
			}
			// The half that makes it a guardrail.
			if writes := h.writes(); len(writes) != 0 {
				t.Errorf("the pod was written to anyway: %+v", writes)
			}
		})
	}
}

// And the four owners that do recreate are still allowed, or the refusal has
// removed the feature rather than the trap. A test that only proved the refusal
// would pass against a Restart button that never worked at all.
func TestRestartIsStillOfferedWhereAControllerRefillsTheCount(t *testing.T) {
	for _, owner := range []string{"ReplicaSet", "StatefulSet", "DaemonSet", "ReplicationController"} {
		t.Run(owner, func(t *testing.T) {
			if !RestartsAPod(owner) {
				t.Fatalf("%s is treated as not recreating its pods, so Restart is withheld from "+
					"every pod of a Deployment", owner)
			}
			h := newHarness(t, nil, pod("prod", "api-1", ownedBy(owner, "api")))
			if _, err := h.runner.Run(context.Background(), Request{
				Target: target("pods", "prod", "api-1"), Verb: Restart, Confirm: "1",
			}); err != nil {
				t.Fatalf("restart refused on a %s's pod: %v", owner, err)
			}
			if writes := h.writes(); len(writes) != 1 || writes[0].Verb != "delete" {
				t.Errorf("want one delete, got %+v", writes)
			}
		})
	}
	// And a Job is not on the list, which is the whole point of having one.
	if RestartsAPod("Job") {
		t.Error("a Job is treated as recreating its pods; deleting one spends a retry instead")
	}
}

// The action's line carries who asked, through the same Writer.Logf the edits
// use - asserted here because an action is a second caller of it.
func TestAnActionLineNamesWhoAsked(t *testing.T) {
	h := newHarness(t, nil, pod("prod", "api-1"))
	ctx := identity.With(context.Background(), identity.Who{Name: "bob@example.com", Method: identity.SSO})
	if _, err := h.runner.Run(ctx, Request{
		Target: target("pods", "prod", "api-1"), Verb: Delete, Confirm: ConfirmToken,
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, line := range h.logged {
		if strings.HasPrefix(line, "write: ") && strings.Contains(line, "action=delete") {
			if !strings.HasSuffix(line, `who="bob@example.com" via=sso`) {
				t.Errorf("the delete's line does not name who asked: %s", line)
			}
			return
		}
	}
	t.Fatalf("no write line: %v", h.logged)
}

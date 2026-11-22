package actions

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/edit"
)

// A plan is pinned to what its act depends on, and a status write changes none
// of it.
//
// A node's resourceVersion moves about once a minute with its kubelet's
// heartbeat, so a Cordon pinned to it would be refused 409 whenever the dialog
// is read for longer than that; a Deployment's moves while its controller
// writes the rollout's status, so a scale back made straight after a scale
// would be refused too. Each test below moves an object
// the way its kubelet or controller does - status only, resourceVersion up -
// and then the way an operator does - a taint, a spec - and asserts the first
// goes through and the second is refused before anything is written.

// moveStatus writes the object's status and bumps its resourceVersion, which is
// what a kubelet's heartbeat and a controller's progress report do. The spec and
// the generation are left exactly as they were.
func (h *harness) moveStatus(gvr schema.GroupVersionResource, namespace, name, version string, status map[string]any) {
	h.t.Helper()
	ctx := context.Background()
	live, err := h.dyn.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		h.t.Fatalf("reading %s to move its status: %v", name, err)
	}
	if err := unstructured.SetNestedMap(live.Object, status, "status"); err != nil {
		h.t.Fatal(err)
	}
	live.SetResourceVersion(version)
	if _, err := h.dyn.Resource(gvr).Namespace(namespace).Update(ctx, live, metav1.UpdateOptions{}); err != nil {
		h.t.Fatalf("moving %s's status: %v", name, err)
	}
}

// moveSpec changes the object the way somebody editing it does: a field of the
// spec, a new resourceVersion, and the generation the API server would then
// assign (zero leaves it unset, as on a node).
func (h *harness) moveSpec(gvr schema.GroupVersionResource, namespace, name, version string, generation int64, value any, path ...string) {
	h.t.Helper()
	ctx := context.Background()
	live, err := h.dyn.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		h.t.Fatalf("reading %s to change it: %v", name, err)
	}
	if err := unstructured.SetNestedField(live.Object, value, path...); err != nil {
		h.t.Fatal(err)
	}
	live.SetResourceVersion(version)
	if generation > 0 {
		live.SetGeneration(generation)
	}
	if _, err := h.dyn.Resource(gvr).Namespace(namespace).Update(ctx, live, metav1.UpdateOptions{}); err != nil {
		h.t.Fatalf("changing %s: %v", name, err)
	}
}

// heartbeat is a node's status as its kubelet rewrites it: a condition with a
// fresh timestamp. Nothing in it is anything a cordon depends on.
func heartbeat(at string) map[string]any {
	return map[string]any{"conditions": []any{map[string]any{
		"type": "Ready", "status": "True", "lastHeartbeatTime": at,
	}}}
}

// workload is a Deployment as the API server serves one: with the generation
// it increments on every change to the spec.
func workload(namespace, name string, generation int64) *unstructured.Unstructured {
	object := deployment(namespace, name)
	object.SetGeneration(generation)
	return object
}

// --- a node --------------------------------------------------------------------

func TestANodeWhoseStatusMovedKeepsItsPlan(t *testing.T) {
	for _, verb := range []Verb{Cordon, Uncordon, Drain} {
		t.Run(string(verb), func(t *testing.T) {
			h := newHarness(t, nil, node("node-a"))
			req := Request{Target: target("nodes", "", "node-a"), Verb: verb, Confirm: SingleConfirm(verb, "nodes", "node-a")}

			recap, err := h.runner.Preview(context.Background(), req)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			if recap.Pin.On != PinNodeScheduling {
				t.Fatalf("a %s is pinned to %q; a node verb depends on %q", verb, recap.Pin.On, PinNodeScheduling)
			}

			// The kubelet, twice, while the dialog is read: the version moves
			// from 7 to 9 and nothing the cordon depends on does.
			h.moveStatus(nodeGVR, "", "node-a", "8", heartbeat("2026-10-10T12:00:00Z"))
			h.moveStatus(nodeGVR, "", "node-a", "9", heartbeat("2026-10-10T12:01:00Z"))

			req.Precondition = recap.Pin.Value
			if _, err := h.runner.Run(context.Background(), req); err != nil {
				t.Fatalf("a %s planned at resourceVersion 7 was refused at 9, and only the kubelet "+
					"wrote in between: %v", verb, err)
			}
			patched := false
			for _, write := range h.writes() {
				if write.Verb == "patch" && !write.DryRun {
					patched = true
				}
			}
			if !patched {
				t.Fatalf("the %s went through and wrote nothing: %+v", verb, h.writes())
			}
		})
	}
}

// The other half, or the pin is no pin: a taint added while the dialog is open
// is something a cordon's plan rests on, and the refusal names it.
func TestANodeWhoseTaintsChangedIsRefusedAndTheRefusalNamesTheTaint(t *testing.T) {
	h := newHarness(t, nil, node("node-a"))
	req := Request{Target: target("nodes", "", "node-a"), Verb: Cordon, Confirm: ConfirmToken}
	recap, err := h.runner.Preview(context.Background(), req)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}

	h.moveSpec(nodeGVR, "", "node-a", "8", 0, []any{map[string]any{
		"key": "dedicated", "value": "gpu", "effect": "NoSchedule",
	}}, "spec", "taints")

	req.Precondition = recap.Pin.Value
	_, err = h.runner.Run(context.Background(), req)
	out := refusal(t, err)
	if out.Kind != edit.RefusedConflict {
		t.Fatalf("a cordon planned before a taint was added: expected a conflict, got %v: %s", out.Kind, out.Reason)
	}
	if !strings.Contains(out.Reason, "dedicated=gpu:NoSchedule") {
		t.Errorf("the refusal does not name the taint that appeared: %q", out.Reason)
	}
	h.noWrites()
}

// Somebody else cordoned the node: an Uncordon planned against a schedulable
// node is about a different node now.
func TestANodeSomebodyCordonedIsRefused(t *testing.T) {
	h := newHarness(t, nil, node("node-a"))
	req := Request{Target: target("nodes", "", "node-a"), Verb: Uncordon, Confirm: ConfirmToken}
	recap, err := h.runner.Preview(context.Background(), req)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	h.moveSpec(nodeGVR, "", "node-a", "8", 0, true, "spec", "unschedulable")

	req.Precondition = recap.Pin.Value
	if out := refusal(t, func() error { _, err := h.runner.Run(context.Background(), req); return err }()); out.Kind != edit.RefusedConflict {
		t.Fatalf("expected a conflict, got %v: %s", out.Kind, out.Reason)
	}
	h.noWrites()
}

// --- a Deployment --------------------------------------------------------------

// Scale, then a plan to scale back made at once, while the controller writes the rollout's status.
func TestADeploymentWhoseStatusMovedKeepsItsPlan(t *testing.T) {
	for _, c := range []struct {
		what    string
		request Request
	}{
		{"scale", Request{Target: target("deployments", "prod", "api"), Verb: Scale, Replicas: replicaCount(1), Confirm: ConfirmToken}},
		{"restart", Request{Target: target("deployments", "prod", "api"), Verb: Restart, Confirm: ConfirmToken}},
	} {
		t.Run(c.what, func(t *testing.T) {
			h := newHarness(t, nil, workload("prod", "api", 4))
			recap, err := h.runner.Preview(context.Background(), c.request)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			if recap.Pin.On != PinGeneration || recap.Pin.Value != "4" {
				t.Fatalf("a %s on a Deployment is pinned to %s %q; want %s 4",
					c.what, recap.Pin.On, recap.Pin.Value, PinGeneration)
			}

			// The rollout reporting progress: resourceVersion 12 to 14,
			// generation still 4.
			h.moveStatus(deploymentGVR, "prod", "api", "13",
				map[string]any{"observedGeneration": int64(4), "replicas": int64(2), "readyReplicas": int64(1)})
			h.moveStatus(deploymentGVR, "prod", "api", "14",
				map[string]any{"observedGeneration": int64(4), "replicas": int64(2), "readyReplicas": int64(2)})

			req := c.request
			req.Precondition = recap.Pin.Value
			if _, err := h.runner.Run(context.Background(), req); err != nil {
				t.Fatalf("a %s planned at resourceVersion 12 was refused at 14, and only the "+
					"controller wrote in between: %v", c.what, err)
			}
		})
	}
}

// And a change to the spec - somebody else scaled it - is refused, before
// anything is written.
func TestADeploymentWhoseSpecChangedIsRefused(t *testing.T) {
	h := newHarness(t, nil, workload("prod", "api", 4))
	req := Request{Target: target("deployments", "prod", "api"), Verb: Scale, Replicas: replicaCount(1), Confirm: ConfirmToken}
	recap, err := h.runner.Preview(context.Background(), req)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	h.moveSpec(deploymentGVR, "prod", "api", "13", 5, int64(5), "spec", "replicas")

	req.Precondition = recap.Pin.Value
	_, err = h.runner.Run(context.Background(), req)
	out := refusal(t, err)
	if out.Kind != edit.RefusedConflict {
		t.Fatalf("a scale planned at generation 4 ran at 5: %v: %s", out.Kind, out.Reason)
	}
	if !strings.Contains(out.Reason, "metadata.generation") {
		t.Errorf("the refusal does not say what moved: %q", out.Reason)
	}
	h.noWrites()
}

// --- the rule, kind by kind ----------------------------------------------------

func TestPinOfEachKind(t *testing.T) {
	tainted := node("node-b")
	tainted.Object["spec"] = map[string]any{
		"unschedulable": true,
		// Out of order and with a timeAdded, on purpose: the pin sorts, and
		// says what keeps pods off rather than when.
		"taints": []any{
			map[string]any{"key": "zone", "effect": "NoExecute", "timeAdded": "2026-10-10T12:00:00Z"},
			map[string]any{"key": "dedicated", "value": "gpu", "effect": "NoSchedule"},
		},
	}
	// A pod carries a generation since Kubernetes 1.33, and is pinned to its
	// version all the same: a pod's labels are what select it.
	generatedPod := pod("prod", "api-1")
	generatedPod.SetGeneration(1)
	configMap := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "app", "namespace": "prod", "resourceVersion": "55"},
	}}

	for _, c := range []struct {
		what   string
		kind   string
		object *unstructured.Unstructured
		want   Pin
	}{
		{"a schedulable node", "Node", node("node-a"), Pin{On: PinNodeScheduling, Value: "unschedulable=false; taints=none", UID: "uid-node-a"}},
		{"a cordoned, tainted node", "Node", tainted, Pin{On: PinNodeScheduling,
			Value: "unschedulable=true; taints=dedicated=gpu:NoSchedule, zone:NoExecute", UID: "uid-node-b"}},
		{"a Deployment", "Deployment", workload("prod", "api", 4), Pin{On: PinGeneration, Value: "4", UID: "uid-api"}},
		{"a pod with a generation", "Pod", generatedPod, Pin{On: PinResourceVersion, Value: "100", UID: "uid-api-1"}},
		{"a kind with no generation", "ConfigMap", configMap, Pin{On: PinResourceVersion, Value: "55"}},
	} {
		t.Run(c.what, func(t *testing.T) {
			if got := PinOf(c.kind, c.object); got != c.want {
				t.Errorf("PinOf = %+v, want %+v", got, c.want)
			}
		})
	}
}

// --- the same name, another object --------------------------------------------

// recreate deletes the object and creates successor under the same name, which
// is what an Argo CD replace sync, `helm --force` and a node re-registering do.
// The recorded actions are cleared afterwards: the delete and the create are the
// test's, and the assertions that follow are about what the runner sends.
func (h *harness) recreate(gvr schema.GroupVersionResource, successor *unstructured.Unstructured) {
	h.t.Helper()
	ctx := context.Background()
	resource := h.dyn.Resource(gvr).Namespace(successor.GetNamespace())
	if err := resource.Delete(ctx, successor.GetName(), metav1.DeleteOptions{}); err != nil {
		h.t.Fatalf("deleting %s to recreate it: %v", successor.GetName(), err)
	}
	if _, err := resource.Create(ctx, successor, metav1.CreateOptions{}); err != nil {
		h.t.Fatalf("recreating %s: %v", successor.GetName(), err)
	}
	h.dyn.ClearActions()
}

// A generation restarts at 1 on a recreate, so a plan made at generation 1
// matches the successor's generation 1, and a delete sending the UID it had
// just re-read would remove the successor. The UID is part of every pin, so the
// plan is refused before anything is sent.
func TestADeploymentRecreatedAtTheSameGenerationIsRefused(t *testing.T) {
	for _, c := range []struct {
		what    string
		request Request
	}{
		{"delete", Request{Target: target("deployments", "prod", "api"), Verb: Delete,
			Confirm: SingleConfirm(Delete, "deployments", "api")}},
		{"scale", Request{Target: target("deployments", "prod", "api"), Verb: Scale,
			Replicas: replicaCount(0), Confirm: ConfirmToken}},
	} {
		t.Run(c.what, func(t *testing.T) {
			h := newHarness(t, nil, workload("prod", "api", 1))
			recap, err := h.runner.Preview(context.Background(), c.request)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			if recap.Pin.UID != "uid-api" {
				t.Fatalf("the plan is pinned to UID %q; the object it was made on is uid-api", recap.Pin.UID)
			}

			successor := workload("prod", "api", 1)
			successor.SetUID("uid-successor")
			successor.SetResourceVersion("30")
			h.recreate(deploymentGVR, successor)

			req := c.request
			req.Precondition, req.PreconditionUID = recap.Pin.Value, recap.Pin.UID
			_, err = h.runner.Run(context.Background(), req)
			out := refusal(t, err)
			if out.Kind != edit.RefusedConflict {
				t.Fatalf("a %s planned on uid-api was confirmed against its successor, uid-successor, "+
					"at the same generation: %v: %s", c.what, out.Kind, out.Reason)
			}
			for _, want := range []string{"changed while you had it open", "uid-api", "uid-successor"} {
				if !strings.Contains(out.Reason, want) {
					t.Errorf("the refusal does not say %q: %q", want, out.Reason)
				}
			}
			h.noWrites()
		})
	}
}

// The node's half: a node that re-registers comes back schedulable and with the
// taints it had, so its scheduling string is equal and only the UID differs.
func TestANodeReRegisteredWithTheSameSchedulingIsRefused(t *testing.T) {
	h := newHarness(t, nil, node("node-a"))
	req := Request{Target: target("nodes", "", "node-a"), Verb: Cordon, Confirm: ConfirmToken}
	recap, err := h.runner.Preview(context.Background(), req)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}

	successor := node("node-a")
	successor.SetUID("uid-node-a-again")
	successor.SetResourceVersion("40")
	h.recreate(nodeGVR, successor)

	req.Precondition, req.PreconditionUID = recap.Pin.Value, recap.Pin.UID
	_, err = h.runner.Run(context.Background(), req)
	out := refusal(t, err)
	if out.Kind != edit.RefusedConflict {
		t.Fatalf("a cordon planned on uid-node-a was confirmed against the node that re-registered "+
			"as uid-node-a-again: %v: %s", out.Kind, out.Reason)
	}
	h.noWrites()
}

// And a delete that goes through sends the UID the plan was made on as its
// precondition, so the API server refuses it if the object is recreated after
// the re-read.
func TestADeleteCarriesThePlannedUID(t *testing.T) {
	h := newHarness(t, nil, workload("prod", "api", 1))
	req := Request{Target: target("deployments", "prod", "api"), Verb: Delete,
		Confirm: SingleConfirm(Delete, "deployments", "api")}
	recap, err := h.runner.Preview(context.Background(), req)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	req.Precondition, req.PreconditionUID = recap.Pin.Value, recap.Pin.UID
	if _, err := h.runner.Run(context.Background(), req); err != nil {
		t.Fatalf("delete: %v", err)
	}
	writes := h.writes()
	if len(writes) != 1 || writes[0].Verb != "delete" || writes[0].UID != "uid-api" {
		t.Fatalf("the delete did not carry the planned UID uid-api: %+v", writes)
	}
}

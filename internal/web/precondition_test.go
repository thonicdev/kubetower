package web

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
)

// The precondition, walked end to end over a cluster that moves.
//
// `internal/actions` proves the runner refuses a stale version and
// `internal/api` proves the handler compares and hands one on; this is the
// mapping between them, where the version could be lost on both
// legs - `PreviewAction` must state it and `RunAction` must carry it. The
// two halves are useless without this one, and this one is the only
// place a test can say "previews a cordon, the node moves, the act is refused".
func TestACordonPreviewedAgainstANodeThatThenMovedIsRefused(t *testing.T) {
	h := newActsHarness(t, fakeNode("node-a"))
	request := api.ActionRequest{Verb: "cordon", Target: api.PlanTarget{Kind: "nodes", Name: "node-a"}}

	facts, err := h.acts.PreviewAction(context.Background(), "prod", request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	// A node's plan is pinned to what a cordon depends on, not to its version:
	// the kubelet moves the version every minute.
	if facts.Precondition != "unschedulable=false; taints=none" || facts.PreconditionOn != actions.PinNodeScheduling {
		t.Fatalf("the plan states precondition %q on %q; a cordon depends on the node's "+
			"schedulability and taints, and an act with no pin stated is one with none carried",
			facts.Precondition, facts.PreconditionOn)
	}

	// Somebody taints the node while the dialog is open.
	ctx := context.Background()
	live, err := h.dyn.Resource(fakeNodeGVR).Get(ctx, "node-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedSlice(live.Object, []any{map[string]any{
		"key": "dedicated", "value": "gpu", "effect": "NoSchedule",
	}}, "spec", "taints"); err != nil {
		t.Fatal(err)
	}
	live.SetResourceVersion("8")
	if _, err := h.dyn.Resource(fakeNodeGVR).Update(ctx, live, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	// What `internal/api` hands on after the person agrees: the request the
	// plan was minted from, carrying the pin the plan stated.
	request.Precondition = facts.Precondition
	_, err = h.acts.RunAction(context.Background(), "prod", request, "confirm")
	if !errors.Is(err, api.ErrConflict) {
		t.Fatalf("a cordon confirmed against an untainted node ran against a tainted one: err=%v", err)
	}
	for _, action := range h.dyn.Actions() {
		if action.GetVerb() == "patch" {
			t.Fatal("the node was patched anyway; a refusal that still wrote is a message rather than a guard")
		}
	}
}

// The kubelet writing the node's status between the preview and the click is
// not a change to anything the cordon depends on, and the act goes through.
func TestACordonPreviewedAgainstANodeWhoseStatusMovedGoesThrough(t *testing.T) {
	h := newActsHarness(t, fakeNode("node-a"))
	request := api.ActionRequest{Verb: "cordon", Target: api.PlanTarget{Kind: "nodes", Name: "node-a"}}

	facts, err := h.acts.PreviewAction(context.Background(), "prod", request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	h.bump(t, fakeNodeGVR, "", "node-a", "8")
	h.bump(t, fakeNodeGVR, "", "node-a", "9")

	request.Precondition = facts.Precondition
	outcome, err := h.acts.RunAction(context.Background(), "prod", request, "confirm")
	if err != nil {
		t.Fatalf("a cordon planned at resourceVersion 7 was refused at 9 with only a status write between: %v", err)
	}
	if outcome.Succeeded != 1 {
		t.Fatalf("outcome = %+v", outcome)
	}
}

// And the same walk with nothing moving goes through, or the precondition is a
// cordon button that never works.
func TestACordonPreviewedAgainstAnUnchangedNodeGoesThrough(t *testing.T) {
	h := newActsHarness(t, fakeNode("node-a"))
	request := api.ActionRequest{Verb: "cordon", Target: api.PlanTarget{Kind: "nodes", Name: "node-a"}}

	facts, err := h.acts.PreviewAction(context.Background(), "prod", request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	request.Precondition = facts.Precondition
	outcome, err := h.acts.RunAction(context.Background(), "prod", request, "confirm")
	if err != nil {
		t.Fatalf("a cordon against the version the plan stated was refused: %v", err)
	}
	if outcome.Succeeded != 1 {
		t.Fatalf("outcome = %+v", outcome)
	}
}

// A node that re-registers comes back schedulable and with the taints it had,
// so the pin's value is equal and only its UID says it is another object.
// The plan states the UID, and the act refuses on it.
func TestACordonPreviewedAgainstANodeThatReRegisteredIsRefused(t *testing.T) {
	h := newActsHarness(t, fakeNode("node-a"))
	request := api.ActionRequest{Verb: "cordon", Target: api.PlanTarget{Kind: "nodes", Name: "node-a"}}

	facts, err := h.acts.PreviewAction(context.Background(), "prod", request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if facts.PreconditionUID != "uid-node-a" {
		t.Fatalf("the plan states UID %q; it was made on uid-node-a", facts.PreconditionUID)
	}

	ctx := context.Background()
	if err := h.dyn.Resource(fakeNodeGVR).Delete(ctx, "node-a", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	successor := fakeNode("node-a")
	successor.SetUID("uid-node-a-again")
	successor.SetResourceVersion("40")
	if _, err := h.dyn.Resource(fakeNodeGVR).Create(ctx, successor, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if again, err := h.acts.PreviewAction(ctx, "prod", request); err != nil || again.Precondition != facts.Precondition {
		t.Fatalf("the successor's pin value should equal the plan's, or this test proves nothing: %q vs %q (%v)",
			again.Precondition, facts.Precondition, err)
	}

	request.Precondition, request.PreconditionUID = facts.Precondition, facts.PreconditionUID
	_, err = h.acts.RunAction(ctx, "prod", request, "confirm")
	if !errors.Is(err, api.ErrConflict) {
		t.Fatalf("a cordon planned on uid-node-a ran against uid-node-a-again: err=%v", err)
	}
	for _, action := range h.dyn.Actions() {
		if action.GetVerb() == "patch" {
			t.Fatalf("a refused cordon sent a patch anyway: %+v", action)
		}
	}
}

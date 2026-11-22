package actions

import (
	"context"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/edit"
)

// The precondition every single-object verb shares.
//
// `Request.Precondition` is checked by `patch` — cordon, uncordon, suspend,
// resume, scale and a rollout restart — as well as by `deleteObject`: a patch
// verb that fetched the live object with `edit.Live` and threw it away would let
// a cordon confirmed against a node that has since been rewritten go through
// against whatever is there now, while the JSON API's plan (`PlanFacts`)
// declares a `precondition` "the act will carry". A check that is documented
// and does not run reads, from a search, exactly like one that does.
//
// Every verb in this table reads the object before it writes, so the check
// costs no request: it is one comparison on a value already in hand.
func TestEveryPatchVerbRefusesAStaleResourceVersion(t *testing.T) {
	for _, c := range []struct {
		what    string
		fixture func() *harness
		request Request
	}{
		{"cordon", func() *harness { return newHarness(t, nil, node("node-a")) },
			Request{Target: target("nodes", "", "node-a"), Verb: Cordon, Confirm: ConfirmToken}},
		{"uncordon", func() *harness { return newHarness(t, nil, node("node-a")) },
			Request{Target: target("nodes", "", "node-a"), Verb: Uncordon, Confirm: ConfirmToken}},
		{"suspend", func() *harness { return newHarness(t, nil, cronJob("prod", "billing")) },
			Request{Target: target("cronjobs", "prod", "billing"), Verb: Suspend, Confirm: ConfirmToken}},
		{"resume", func() *harness { return newHarness(t, nil, cronJob("prod", "billing")) },
			Request{Target: target("cronjobs", "prod", "billing"), Verb: Resume, Confirm: ConfirmToken}},
		{"scale", func() *harness { return newHarness(t, nil, deployment("prod", "api")) },
			Request{Target: target("deployments", "prod", "api"), Verb: Scale, Replicas: replicaCount(2), Confirm: ConfirmToken}},
		{"restart", func() *harness { return newHarness(t, nil, deployment("prod", "api")) },
			Request{Target: target("deployments", "prod", "api"), Verb: Restart, Confirm: ConfirmToken}},
		{"evict", func() *harness { return newHarness(t, nil, pod("prod", "api-1")) },
			Request{Target: target("pods", "prod", "api-1"), Verb: Evict, Confirm: ConfirmToken}},
		{"trigger", func() *harness { return newHarness(t, nil, cronJob("prod", "billing")) },
			Request{Target: target("cronjobs", "prod", "billing"), Verb: Trigger, Confirm: ConfirmToken}},
		// Stage one of a drain is a cordon, and it is the one stage a panel's
		// version reaches: the second arrives from its own plan and carries none.
		{"drain", func() *harness { return newHarness(t, nil, node("node-a")) },
			Request{Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "node-a"}},
	} {
		t.Run(c.what, func(t *testing.T) {
			h := c.fixture()
			req := c.request
			// The fixtures carry "7", "4", "12" and "100"; none of them is this.
			req.Precondition = "99"

			_, err := h.runner.Run(context.Background(), req)
			if out := refusal(t, err); out.Kind != edit.RefusedConflict {
				t.Fatalf("%s on a stale version: expected a conflict refusal, got %v: %s",
					c.what, out.Kind, out.Reason)
			}
			// The half that makes it a guardrail rather than a message.
			h.noWrites()
		})
	}
}

// And the pin that matches still goes through, or the check above is a blanket
// refusal wearing a precondition's clothes.
func TestAMatchingPreconditionIsAccepted(t *testing.T) {
	h := newHarness(t, nil, node("node-a"))
	if _, err := h.runner.Run(context.Background(), Request{
		Target: target("nodes", "", "node-a"), Verb: Cordon, Confirm: ConfirmToken,
		Precondition: "unschedulable=false; taints=none",
	}); err != nil {
		t.Fatalf("a cordon carrying the node's own pin was refused: %v", err)
	}
	if writes := h.writes(); len(writes) != 2 {
		t.Fatalf("expected a dry run and a commit, got %+v", writes)
	}
}

// The preview is where the version comes from.
//
// The JSON API has no drawer to carry a version through a hidden field, so the
// plan is drawn from the preview and the act is bound to the plan: the version
// the recap was read at is the version the act carries, and `Recap` is the one
// place it can travel. A preview that read the object and dropped its version
// would leave `PlanFacts.precondition` with nothing to be filled from.
//
// Every verb, not only the ones with a field to show: a delete's precondition is
// worth exactly as much as a cordon's, so the note-only verbs read the object
// too.
func TestPreviewCarriesTheVersionItRead(t *testing.T) {
	for _, c := range []struct {
		what    string
		fixture func() *harness
		request Request
		want    string
	}{
		{"cordon", func() *harness { return newHarness(t, nil, node("node-a")) },
			Request{Target: target("nodes", "", "node-a"), Verb: Cordon}, "7"},
		{"scale", func() *harness { return newHarness(t, nil, deployment("prod", "api")) },
			Request{Target: target("deployments", "prod", "api"), Verb: Scale, Replicas: replicaCount(2)}, "12"},
		{"delete", func() *harness { return newHarness(t, nil, pod("prod", "api-1")) },
			Request{Target: target("pods", "prod", "api-1"), Verb: Delete}, "100"},
		{"trigger", func() *harness { return newHarness(t, nil, cronJob("prod", "billing")) },
			Request{Target: target("cronjobs", "prod", "billing"), Verb: Trigger}, "4"},
	} {
		t.Run(c.what, func(t *testing.T) {
			h := c.fixture()
			recap, err := h.runner.Preview(context.Background(), c.request)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			if recap.ResourceVersion != c.want {
				t.Fatalf("the preview of a %s read the object and carries resourceVersion %q, want %q",
					c.what, recap.ResourceVersion, c.want)
			}
			h.noWrites()
		})
	}
}

// A preview of an object that is not there is an error, not a plan. Otherwise a
// delete would preview cleanly against nothing at all, and the confirmation
// would describe removing an object the cluster does not have.
func TestPreviewOfAMissingObjectIsRefused(t *testing.T) {
	h := newHarness(t, nil)
	if _, err := h.runner.Preview(context.Background(), Request{
		Target: target("pods", "prod", "gone"), Verb: Delete,
	}); err == nil {
		t.Fatal("a delete of an object that does not exist previewed as a plan")
	}
}

// --- the drain's second stage, previewed ---------------------------------------

// DrainPlan is a preview: it lists and sorts, and it cordons and evicts nothing.
// The JSON API mints the second stage's plan from it and re-derives the facts
// from it again before evicting, so a DrainPlan that wrote anything would be a
// preview that acts — twice.
func TestDrainPlanReadsTheListAndWritesNothing(t *testing.T) {
	h := newHarness(t, nil,
		node("node-a"),
		pod("prod", "api-1"),
		pod("prod", "api-2", ownedBy("ReplicaSet", "api")),
	)
	plan, err := h.runner.DrainPlan(context.Background(), Request{
		Target: target("nodes", "", "node-a"), Verb: Drain, Stage: StageEvict,
	})
	if err != nil {
		t.Fatalf("DrainPlan: %v", err)
	}
	if len(plan.Evict) != 1 || len(plan.Skip) != 1 || plan.Node != "node-a" {
		t.Fatalf("the plan does not list both pods on node-a, the owned one to evict and the "+
			"unowned one left alone: %+v", plan)
	}
	if plan.Skip[0].Name != "api-1" || plan.Skip[0].Skip != UnownedSkip {
		t.Errorf("api-1 has no owner and the plan does not leave it alone saying so: %+v", plan.Skip[0])
	}
	if plan.Cordoned {
		t.Error("the plan reports the node as cordoned; nothing has cordoned it")
	}
	h.noWrites()
}

// A node that is not there is a refusal, not an empty plan.
func TestDrainPlanOfAMissingNodeIsRefused(t *testing.T) {
	h := newHarness(t, nil)
	if _, err := h.runner.DrainPlan(context.Background(), Request{
		Target: target("nodes", "", "ghost"), Verb: Drain, Stage: StageEvict,
	}); err == nil {
		t.Fatal("a drain of a node that does not exist planned an empty eviction list")
	}
}

// The first stage promises a second only where there is one to offer.
func TestTheFirstStageDoesNotInviteAConfirmationItCannotOffer(t *testing.T) {
	h := newHarness(t, nil, node("node-a"))
	result, err := h.runner.Run(context.Background(), Request{
		Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "node-a",
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if strings.Contains(result.Message, "confirm again") {
		t.Errorf("nothing is on the node and the sentence asks for a second confirmation: %q", result.Message)
	}
	if !strings.Contains(result.Message, "No pods are on this node") {
		t.Errorf("an empty list is reported as nothing rather than as an empty node: %q", result.Message)
	}
}

package web

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/edit"
)

// bare is a pod nothing owns.
func bare(o map[string]any) { delete(o["metadata"].(map[string]any), "ownerReferences") }

// ownedByADaemonSet is a pod no drain evicts.
func ownedByADaemonSet(o map[string]any) {
	o["metadata"].(map[string]any)["ownerReferences"] = []any{map[string]any{
		"apiVersion": "apps/v1", "kind": "DaemonSet", "name": "fluent-bit", "uid": "uid-ds", "controller": true,
	}}
}

// The offer to include the pods nothing owns is reachable when they are the
// only pods a drain could evict. The first stage has no eviction stage to hand on, so its
// outcome carries the offer; taking it walks the drain again, and the bare pod
// is evicted only by the eviction stage whose plan names it.
func TestADrainOverOnlyUnownedPodsOffersToIncludeThem(t *testing.T) {
	for _, tc := range []struct {
		name   string
		daemon bool
	}{
		{"a bare pod alone", false},
		{"a bare pod beside a DaemonSet's", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := []*unstructured.Unstructured{fakeNode("node-a"), fakePod("prod", "bare", bare)}
			if tc.daemon {
				objects = append(objects, fakePod("kube-system", "fluent-bit-x", ownedByADaemonSet))
			}
			h := newActsHarness(t, objects...)
			ctx := context.Background()
			request := api.ActionRequest{Verb: "drain", Target: api.PlanTarget{Kind: "nodes", Name: "node-a"}}

			first, err := h.acts.PreviewAction(ctx, "prod", request)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			request.Precondition = first.Precondition
			request.PreconditionUID = first.PreconditionUID
			cordoned, err := h.acts.RunAction(ctx, "prod", request, "node-a")
			if err != nil {
				t.Fatalf("first stage: %v", err)
			}
			if cordoned.NextRequest != nil {
				t.Fatalf("an eviction stage was minted over nothing to evict: %+v", cordoned.NextRequest)
			}
			offer := cordoned.Alternative
			if offer == nil || !offer.Request.IncludeUnowned || offer.Request.Verb != "drain" ||
				offer.Request.Target.Name != "node-a" || !strings.Contains(offer.Label, "1 pod nothing owns") {
				t.Fatalf("the cordon's outcome does not offer the drain including the pod nothing owns: %+v", offer)
			}
			if strings.Contains(cordoned.Summary, "as far as a drain") || !strings.Contains(cordoned.Summary, "prod/bare") {
				t.Errorf("the first stage's summary: %q", cordoned.Summary)
			}
			if h.evictions() != 0 {
				t.Fatal("the first stage evicted")
			}

			// The offer, taken: its first stage warns, and its second names the
			// bare pod - and only it - behind the node's name.
			including := api.ActionRequest{Verb: offer.Request.Verb, Target: offer.Request.Target,
				IncludeUnowned: offer.Request.IncludeUnowned}
			firstAgain, err := h.acts.PreviewAction(ctx, "prod", including)
			if err != nil {
				t.Fatalf("preview including unowned pods: %v", err)
			}
			if !strings.Contains(strings.Join(firstAgain.Warnings, "|"), "also evicts the pods nothing owns") {
				t.Errorf("the first stage including unowned pods does not say so: %v", firstAgain.Warnings)
			}
			including.Precondition = firstAgain.Precondition
			including.PreconditionUID = firstAgain.PreconditionUID
			cordonedAgain, err := h.acts.RunAction(ctx, "prod", including, "node-a")
			if err != nil {
				t.Fatalf("first stage including unowned pods: %v", err)
			}
			if cordonedAgain.NextRequest == nil || !cordonedAgain.NextRequest.IncludeUnowned ||
				cordonedAgain.Alternative != nil {
				t.Fatalf("the drain including unowned pods has no eviction stage: %+v", cordonedAgain)
			}
			second, err := h.acts.PreviewAction(ctx, "prod", *cordonedAgain.NextRequest)
			if err != nil {
				t.Fatalf("second stage preview: %v", err)
			}
			if second.Count != 1 || len(second.Targets) != 1 || second.Targets[0].Name != "bare" ||
				!strings.Contains(second.Targets[0].Note, "nothing owns") || second.Alternative != nil {
				t.Fatalf("the eviction plan does not name the bare pod, and only it: %+v", second)
			}
			if second.Confirmation == nil || second.Confirmation.Kind != "name" || second.Confirmation.Expected != "node-a" {
				t.Errorf("the eviction plan's gesture is %+v", second.Confirmation)
			}
			if h.evictions() != 0 {
				t.Fatal("something was evicted before the plan naming the pod was confirmed")
			}

			evicted, err := h.acts.RunAction(ctx, "prod", *cordonedAgain.NextRequest, "node-a")
			if err != nil {
				t.Fatalf("second stage: %v", err)
			}
			if h.evictions() != 1 || evicted.Succeeded != 1 || len(evicted.Results) != 1 ||
				evicted.Results[0].Target.Name != "bare" {
				t.Fatalf("expected the bare pod evicted and nothing else: %d evictions, %+v", h.evictions(), evicted)
			}
		})
	}
}

// No offer where there is nothing it would add: an empty node, a node with
// only pods no drain evicts, and a drain that already includes them.
func TestADrainOffersToIncludeUnownedPodsOnlyWhereSomeWereLeft(t *testing.T) {
	for _, tc := range []struct {
		name      string
		including bool
		pods      bool
	}{
		{"an empty node", false, false},
		{"only a DaemonSet's pod", false, true},
		{"a drain already including them", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := []*unstructured.Unstructured{fakeNode("node-a")}
			if tc.pods {
				objects = append(objects, fakePod("kube-system", "fluent-bit-x", ownedByADaemonSet))
			}
			h := newActsHarness(t, objects...)
			outcome, err := h.acts.RunAction(context.Background(), "prod", api.ActionRequest{
				Verb: "drain", Target: api.PlanTarget{Kind: "nodes", Name: "node-a"}, IncludeUnowned: tc.including,
			}, "node-a")
			if err != nil {
				t.Fatalf("drain: %v", err)
			}
			if outcome.Alternative != nil || outcome.NextRequest != nil {
				t.Errorf("an offer or a stage with nothing in it: %+v", outcome)
			}
		})
	}
}

// An eviction that was not answered, or never sent, is not a refusal, and the
// outcome does not code it as one. Nor is one the
// API server timed out on: not known, like an unanswered
// one.
func TestADrainsOutcomeCodesEachFailureForWhatItWas(t *testing.T) {
	result := actions.Result{Plan: &actions.DrainPlan{
		Node: "node-a", Failed: 4, NotAttempted: 1, Unanswered: 1, TimedOut: 1,
		Evict: []actions.PodRef{
			{Namespace: "prod", Name: "refused", Failed: true, Outcome: "a PodDisruptionBudget refused this eviction"},
			{Namespace: "prod", Name: "unanswered", Failed: true, Unanswered: true, Outcome: "not answered"},
			{Namespace: "prod", Name: "timed-out", Failed: true, TimedOut: true, Outcome: "may still happen"},
			{Namespace: "prod", Name: "skipped", Failed: true, NotAttempted: true, Outcome: "not attempted"},
		},
	}}
	outcome := drainOutcome(api.ActionRequest{Verb: "drain", Target: api.PlanTarget{Kind: "nodes", Name: "node-a"},
		Stage: actions.StageEvict}, result)
	want := map[string]api.ReasonCode{
		"refused": api.CodeVerbDenied, "unanswered": api.CodeTimeout, "timed-out": api.CodeTimeout,
		"skipped": api.CodeNotProbedYet,
	}
	for _, entry := range outcome.Results {
		if entry.Reason == nil || entry.Reason.Code != want[entry.Target.Name] {
			t.Errorf("%s carries %+v, want %s", entry.Target.Name, entry.Reason, want[entry.Target.Name])
		}
	}
}

// A bulk row whose request never left the console carries the same code as a
// drain's unsent eviction, not `verb_denied`, which the dialog reads as a
// refusal.
func TestABulkRowNeverSentIsNotCodedAsARefusal(t *testing.T) {
	for _, tc := range []struct {
		kind edit.RefusalKind
		want api.ReasonCode
	}{
		{edit.RefusedNotSent, api.CodeNotProbedYet},
		{edit.RefusedUnanswered, api.CodeTimeout},
		{edit.RefusedByAPI, api.CodeVerbDenied},
		{edit.RefusedNotInProfile, api.CodeRBACNotInProfile},
	} {
		got := bulkReason(actions.BulkOutcome{Name: "api-1", Failed: true, Refusal: tc.kind, Message: "m"})
		if got.Code != tc.want {
			t.Errorf("kind %v carries %s, want %s", tc.kind, got.Code, tc.want)
		}
	}
}

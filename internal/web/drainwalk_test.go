package web

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
)

// A drain, walked over a fake cluster the way `internal/api` walks it: preview,
// first stage, the request the outcome hands back, the second stage's facts from
// that request, the second stage.
//
// `internal/api` proves the plan for the second stage is minted and spent; this
// proves the mapping underneath it reads the right pods, warns about the right
// ones, and sends the evictions only at the second stage. It walks the whole drain:
// cordon → list → confirm → evictions, with a plan for each stage.
func TestADrainWalksBothStagesOverTheCluster(t *testing.T) {
	h := newActsHarness(t,
		fakeNode("node-a"),
		fakePod("prod", "api-1"),
		// Nothing owns this one, so a drain leaves it alone unless it is asked
		// to include it.
		fakePod("prod", "bare", func(o map[string]any) {
			delete(o["metadata"].(map[string]any), "ownerReferences")
		}),
		// A DaemonSet's pod is left alone, and the plan says so.
		fakePod("kube-system", "fluent-bit-x", func(o map[string]any) {
			o["metadata"].(map[string]any)["ownerReferences"] = []any{map[string]any{
				"apiVersion": "apps/v1", "kind": "DaemonSet", "name": "fluent-bit", "uid": "uid-ds", "controller": true,
			}}
		}),
	)
	ctx := context.Background()
	request := api.ActionRequest{Verb: "drain", Target: api.PlanTarget{Kind: "nodes", Name: "node-a"}}

	// The first stage's plan is about the node: cordon, confirmed by name, and
	// a recap that does not pretend to list pods it has not read.
	first, err := h.acts.PreviewAction(ctx, "prod", request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if first.Confirmation == nil || first.Confirmation.Kind != "name" || first.Confirmation.Expected != "node-a" {
		t.Fatalf("the first stage's gesture is %+v; a drain is confirmed by the node's name", first.Confirmation)
	}
	if first.Recap == nil || !strings.Contains(first.Recap.Note, "cordons the node") {
		t.Errorf("the first stage's recap does not say it is the cordon: %+v", first.Recap)
	}
	if first.Precondition == "" {
		t.Error("the first stage carries no precondition; it is a cordon and a cordon carries one")
	}

	request.Precondition = first.Precondition
	cordoned, err := h.acts.RunAction(ctx, "prod", request, "node-a")
	if err != nil {
		t.Fatalf("first stage: %v", err)
	}
	if h.evictions() != 0 {
		t.Fatal("the first stage evicted; the stage that shows what would happen must not be the stage that makes it happen")
	}
	if cordoned.NextRequest == nil || cordoned.NextRequest.Stage != actions.StageEvict {
		t.Fatalf("the first stage handed back no request for the second: %+v", cordoned)
	}
	if !strings.Contains(cordoned.Summary, "Nothing has been evicted yet") {
		t.Errorf("the first stage's summary does not say nothing has happened yet: %q", cordoned.Summary)
	}

	// The second stage's plan, from the request the first handed back: the pod
	// to evict by name; the DaemonSet's and the bare one as left alone; and the
	// same drain including the bare one, offered as a plan of its own.
	second, err := h.acts.PreviewAction(ctx, "prod", *cordoned.NextRequest)
	if err != nil {
		t.Fatalf("second stage preview: %v", err)
	}
	if second.Verb != "evict" || second.Count != 1 || len(second.Targets) != 1 || second.Targets[0].Name != "api-1" {
		t.Fatalf("the second stage's facts: %+v", second)
	}
	if second.Targets[0].Note != "" {
		t.Errorf("a ReplicaSet's pod carries a warning it does not deserve: %+v", second.Targets[0])
	}
	if len(second.Warnings) != 2 || !strings.Contains(strings.Join(second.Warnings, "|"), "fluent-bit-x") ||
		!strings.Contains(strings.Join(second.Warnings, "|"), "prod/bare is left alone: nothing owns") {
		t.Errorf("the DaemonSet's pod and the bare one are not both reported as left alone: %v", second.Warnings)
	}
	if second.Alternative == nil || !second.Alternative.Request.IncludeUnowned ||
		second.Alternative.Request.Verb != "drain" || second.Alternative.Request.Target.Name != "node-a" ||
		!strings.Contains(second.Alternative.Label, "1 pod nothing owns") {
		t.Errorf("the plan does not offer the drain including the pod nothing owns: %+v", second.Alternative)
	}
	if second.Confirmation == nil || second.Confirmation.Kind != "name" || second.Confirmation.Expected != "node-a" {
		t.Errorf("the second stage's gesture is %+v; it is the node's name again", second.Confirmation)
	}
	if second.Precondition != "" {
		t.Errorf("the second stage carries precondition %q; it arrives from its own plan and carries none", second.Precondition)
	}
	if second.Recap == nil || !strings.Contains(second.Recap.Note, "evicted") {
		t.Errorf("the second stage's recap: %+v", second.Recap)
	}

	// The second stage: one eviction, per pod, no third stage, and a summary
	// that says the node is not empty.
	evicted, err := h.acts.RunAction(ctx, "prod", *cordoned.NextRequest, "node-a")
	if err != nil {
		t.Fatalf("second stage: %v", err)
	}
	if h.evictions() != 1 {
		t.Fatalf("expected one eviction, got %d", h.evictions())
	}
	if evicted.Succeeded != 1 || evicted.Failed != 0 || len(evicted.Results) != 1 {
		t.Errorf("the second stage's outcome is not per pod: %+v", evicted)
	}
	if evicted.NextRequest != nil {
		t.Error("the second stage handed back a request for a third")
	}
	if !strings.Contains(evicted.Summary, "Requested 1 eviction(s)") ||
		!strings.Contains(evicted.Summary, "prod/bare") || !strings.Contains(evicted.Summary, "not empty") {
		t.Errorf("summary = %q", evicted.Summary)
	}

	// The offer, taken: the same drain including the pods nothing owns, from
	// its first stage. It carries the choice to the second stage, which then
	// lists the bare pod with its warning and evicts it.
	offer := second.Alternative.Request
	including := api.ActionRequest{Verb: offer.Verb, Target: offer.Target, IncludeUnowned: offer.IncludeUnowned}
	firstAgain, err := h.acts.PreviewAction(ctx, "prod", including)
	if err != nil {
		t.Fatalf("preview including unowned pods: %v", err)
	}
	if !strings.Contains(strings.Join(firstAgain.Warnings, "|"), "also evicts the pods nothing owns") {
		t.Errorf("the first stage of a drain including unowned pods does not say so: %v", firstAgain.Warnings)
	}
	including.Precondition = firstAgain.Precondition
	cordonedAgain, err := h.acts.RunAction(ctx, "prod", including, "node-a")
	if err != nil {
		t.Fatalf("first stage including unowned pods: %v", err)
	}
	if cordonedAgain.NextRequest == nil || !cordonedAgain.NextRequest.IncludeUnowned {
		t.Fatalf("the choice did not travel to the second stage: %+v", cordonedAgain.NextRequest)
	}
	secondAgain, err := h.acts.PreviewAction(ctx, "prod", *cordonedAgain.NextRequest)
	if err != nil {
		t.Fatalf("second stage preview including unowned pods: %v", err)
	}
	// The fake keeps an evicted pod, so api-1 is listed again beside bare.
	againByName := map[string]api.PlanTarget{}
	for _, target := range secondAgain.Targets {
		againByName[target.Name] = target
	}
	if secondAgain.Count != 2 || !strings.Contains(againByName["bare"].Note, "nothing owns") ||
		secondAgain.Alternative != nil {
		t.Fatalf("the second stage including unowned pods: %+v", secondAgain)
	}
	if _, err := h.acts.RunAction(ctx, "prod", *cordonedAgain.NextRequest, "node-a"); err != nil {
		t.Fatalf("second stage including unowned pods: %v", err)
	}
	if h.evictions() != 3 {
		t.Fatalf("expected the bare pod evicted too, got %d evictions in all", h.evictions())
	}
}

// includeUnowned is a drain's, and any other verb that names it is refused
// rather than read as nothing.
func TestIncludeUnownedIsRefusedOnAnythingButADrain(t *testing.T) {
	h := newActsHarness(t, fakeNode("node-a"))
	_, err := h.acts.PreviewAction(context.Background(), "prod", api.ActionRequest{
		Verb: "cordon", Target: api.PlanTarget{Kind: "nodes", Name: "node-a"}, IncludeUnowned: true,
	})
	if !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("a cordon naming includeUnowned answered %v", err)
	}
}

// A node with nothing to evict finishes at the first stage, and says so rather
// than inviting a confirmation that will never be offered.
func TestADrainWithNothingToEvictHasNoSecondStage(t *testing.T) {
	h := newActsHarness(t, fakeNode("node-a"))
	request := api.ActionRequest{Verb: "drain", Target: api.PlanTarget{Kind: "nodes", Name: "node-a"}}
	outcome, err := h.acts.RunAction(context.Background(), "prod", request, "node-a")
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if outcome.NextRequest != nil {
		t.Fatal("an empty node was handed a second stage with nothing in it")
	}
	if strings.Contains(outcome.Summary, "confirm again") {
		t.Errorf("the summary invites a confirmation that will not be offered: %q", outcome.Summary)
	}
}

package web

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
)

// The verb's own words reach the plan, walked through the mapping that carries
// them rather than read off the spec twice.
//
// # What this guards
//
// `actionSpec` carries a `Prompt`, a `Warning` and a `Button` per (verb, kind),
// and they must be what the plan carries. A dialog drawing only
// `sentenceFor(facts)` — "Restart Pod ns/name." — and a button that said
// the bare verb would make a restart on a Deployment, a restart on a Pod and a
// delete on a Pod the same window with a different word in it.
//
// The harness serves pods and nodes, which is where the six verbs that matter
// live; the pairs it cannot reach are covered by the spec-level test below.
func TestTheVerbsOwnWordsReachThePlan(t *testing.T) {
	for _, one := range []struct {
		verb      actions.Verb
		kind      string
		namespace string
		name      string
	}{
		{actions.Restart, "pods", "argo", "api-0"},
		{actions.Evict, "pods", "argo", "api-0"},
		{actions.Delete, "pods", "argo", "api-0"},
		{actions.Cordon, "nodes", "", "node-a"},
		{actions.Uncordon, "nodes", "", "node-a"},
		{actions.Drain, "nodes", "", "node-a"},
	} {
		h := newActsHarness(t, fakeNode("node-a"), fakePod("argo", "api-0"))
		facts, err := h.acts.PreviewAction(context.Background(), "prod", api.ActionRequest{
			Verb:   string(one.verb),
			Target: api.PlanTarget{Kind: one.kind, Namespace: one.namespace, Name: one.name},
		})
		if err != nil {
			t.Fatalf("previewing %s on %s: %v", one.verb, one.kind, err)
		}
		spec, ok := specFor(one.verb, one.kind)
		if !ok {
			t.Fatalf("%s on %s has no spec, so this row measures nothing", one.verb, one.kind)
		}
		if one.verb == actions.Delete {
			// One resource is a simple confirmation:
			// no details, so neither of the verb's words is carried.
			// TestOneDeleteIsASimpleForceablePlan measures the rest.
			if facts.Explanation != "" || facts.Warning != "" {
				t.Errorf("delete on %s carries details (%q, %q); one delete asks only whether the operator is sure",
					one.kind, facts.Explanation, facts.Warning)
			}
			if facts.Button != "Delete" {
				t.Errorf("delete on %s: the plan's button says %q, it should say %q", one.kind, facts.Button, "Delete")
			}
			continue
		}
		if facts.Explanation != spec.Prompt {
			t.Errorf("%s on %s: the plan explains %q, the spec says %q",
				one.verb, one.kind, facts.Explanation, spec.Prompt)
		}
		if facts.Warning != spec.Warning {
			t.Errorf("%s on %s: the plan warns %q, the spec says %q",
				one.verb, one.kind, facts.Warning, spec.Warning)
		}
		// The spec's wording with this object's name in it: the act and the
		// object, and nothing else.
		if want := buttonFor(spec, one.name); facts.Button != want {
			t.Errorf("%s on %s: the plan's button says %q, it should say %q",
				one.verb, one.kind, facts.Button, want)
		}
	}
}

// Every (verb, kind) the spec covers has words to carry.
//
// The warning is deliberately not required: it is the sentence for a verb whose
// consequence is not what its name suggests, and most verbs do not need one. The
// explanation and the button are required of all of them, because a plan that
// falls back to the bare verb is the window this change exists to remove.
func TestEveryVerbTheSpecCoversHasAnExplanationAndAButton(t *testing.T) {
	covered := 0
	for _, kind := range kindsWithVerbs {
		for _, verb := range allVerbs {
			spec, ok := specFor(verb, kind)
			if !ok {
				continue
			}
			covered++
			if spec.Prompt == "" {
				t.Errorf("%s on %s has no explanation, so its confirmation only asks are-you-sure",
					verb, kind)
			}
			if spec.Button == "" {
				t.Errorf("%s on %s has no button wording, so the control says the bare verb",
					verb, kind)
			}
			// A delete's button is "Delete": the dialog's title names the
			// object.
			if verb == actions.Delete {
				if spec.Button != "Delete" {
					t.Errorf("delete on %s commits with %q, want %q", kind, spec.Button, "Delete")
				}
				continue
			}
			// And it names the object, once: the last
			// thing read before the click is then a check on which row the
			// dialog was opened from.
			if strings.Count(spec.Button, "{name}") != 1 {
				t.Errorf("%s on %s commits with %q, which does not name the object exactly once",
					verb, kind, spec.Button)
			}
		}
	}
	if covered == 0 {
		t.Fatal("no (verb, kind) pair was covered, so this test asserted nothing")
	}
}

// The four acts the spec names, and the property: four
// different windows.
//
// Restart on a Deployment and restart on a Pod are the pair that makes the point
// — the same verb, and one of them is a delete.
//
// # What differs, and what does not
//
// The four explanations differ, and are asserted to. A button is the act and the
// object, "Delete api-0", so the restart and the delete of one pod commit with
// the same words, as they do the same thing (a restart on a pod *is* its delete),
// and the line above the button (`lineFor`) is the same for both for the same
// reason. What must still differ
// is everything that is a different act: the rollout restart and the eviction.
func TestTheFourActsReadAsFourDifferentWindows(t *testing.T) {
	seen := map[string]string{}
	for _, one := range []struct {
		verb actions.Verb
		kind string
	}{
		{actions.Restart, "deployments"},
		{actions.Restart, "pods"},
		{actions.Delete, "pods"},
		{actions.Evict, "pods"},
	} {
		spec, ok := specFor(one.verb, one.kind)
		if !ok {
			t.Fatalf("%s on %s has no spec", one.verb, one.kind)
		}
		where := string(one.verb) + " on " + one.kind
		if other, clash := seen[spec.Prompt]; clash {
			t.Errorf("%s explains itself in the same words as %s", where, other)
		}
		seen[spec.Prompt] = where
	}
	buttons := map[string]string{}
	for _, one := range []struct {
		verb actions.Verb
		kind string
	}{
		{actions.Restart, "deployments"},
		{actions.Delete, "pods"},
		{actions.Evict, "pods"},
	} {
		spec, _ := specFor(one.verb, one.kind)
		where := string(one.verb) + " on " + one.kind
		if other, clash := buttons[spec.Button]; clash {
			t.Errorf("%s commits with the same button wording as %s, and they are different acts", where, other)
		}
		buttons[spec.Button] = where
	}
	// A delete commits with "Delete" and no name, so
	// the pod restart - the same act - does not share its wording: it keeps
	// "Delete <name>", which still says what a restart of a pod does.
	podRestart, _ := specFor(actions.Restart, "pods")
	if podRestart.Button != "Delete {name}" {
		t.Errorf("restarting a pod commits with %q; it is a delete, and the control is where that is said",
			podRestart.Button)
	}
	// The pod restart carries its warning; the rollout restart has nothing to
	// warn about, which is the asymmetry the per-kind key exists for.
	pod, _ := specFor(actions.Restart, "pods")
	rollout, _ := specFor(actions.Restart, "deployments")
	if pod.Warning == "" {
		t.Error("restarting a pod carries no warning, and deleting a pod nothing owns is all that happens")
	}
	if rollout.Warning != "" {
		t.Errorf("a rollout restart warns %q; it replaces pods gradually and undoes like any other",
			rollout.Warning)
	}
}

// A bulk plan's commit control reads a phrase written for a set.
//
// # What this guards
//
// `PreviewBulk` must fill a button, or `PlanDialog`'s `facts.button ?? facts.verb`
// takes its second term and the control that commits a 203-pod delete reads
// `delete` - the wire slug, lower case.
//
// # And it is not `spec.Button`
//
// `specFor`'s sentences are written in the singular. `spec.Button` is one of
// them - "Delete it" - so carrying it would put "it" on a set of two hundred.
// The count and the kind come from the
// facts the server resolved; the verb's word comes from `spec.Label`, which is
// the same producer `bulkAffordance` already draws the bar's own control from.
func TestABulkPlansButtonIsAPhraseWrittenForASet(t *testing.T) {
	for _, one := range []struct {
		verb    actions.Verb
		kind    string
		targets []api.PlanTarget
		want    string
	}{
		{actions.Delete, "pods", bulkTargets("argo", 4), "Delete 4 pods"},
		{actions.Evict, "pods", bulkTargets("argo", 203), "Evict 203 pods"},
		{actions.Delete, "configmaps", bulkTargets("argo", 2), "Delete 2 configmaps"},
		// One ticked row is still a bulk plan, and it is the case a phrase
		// borrowed from the plural would get wrong.
		{actions.Delete, "pods", bulkTargets("argo", 1), "Delete 1 pod"},
	} {
		facts, err := apiActs{}.PreviewBulk(context.Background(), "prod", api.BulkRequest{
			Verb: string(one.verb), Kind: one.kind, Targets: one.targets,
		})
		if err != nil {
			t.Fatalf("previewing a bulk %s on %s: %v", one.verb, one.kind, err)
		}
		if facts.Button != one.want {
			t.Errorf("a bulk %s of %d %s commits with %q, and it should read %q",
				one.verb, len(one.targets), one.kind, facts.Button, one.want)
		}
	}
}

// No bulk plan hands the client the wire slug to draw.
//
// The button is what the dialog draws, and `facts.verb` is what it falls back to.
// Over every (verb, kind) the bulk bar offers, the button exists and is not the verb.
func TestNoBulkPlanLeavesTheClientDrawingTheVerbSlug(t *testing.T) {
	covered := 0
	for _, kind := range kindsWithVerbs {
		for _, verb := range verbsFor(kind) {
			if !actions.IsBulk(verb) {
				continue
			}
			facts, err := apiActs{}.PreviewBulk(context.Background(), "prod", api.BulkRequest{
				Verb: string(verb), Kind: kind, Targets: bulkTargets("argo", 3),
			})
			if err != nil {
				t.Fatalf("previewing a bulk %s on %s: %v", verb, kind, err)
			}
			covered++
			if facts.Button == "" {
				t.Errorf("a bulk %s on %s carries no button, so the control says the bare verb %q",
					verb, kind, facts.Verb)
			}
			if facts.Button == facts.Verb {
				t.Errorf("a bulk %s on %s commits with %q, which is the wire slug", verb, kind, facts.Button)
			}
			// The destructive treatment is `destructiveFor`'s and nothing here
			// may read the word on the button to decide it.
			if facts.Destructive != destructiveFor(verb, kind) {
				t.Errorf("a bulk %s on %s is drawn %v and the one producer says %v",
					verb, kind, facts.Destructive, destructiveFor(verb, kind))
			}
		}
	}
	if covered == 0 {
		t.Fatal("no bulk (verb, kind) pair was covered, so this test asserted nothing")
	}
}

func bulkTargets(namespace string, n int) []api.PlanTarget {
	out := make([]api.PlanTarget, 0, n)
	for i := range n {
		out = append(out, api.PlanTarget{Namespace: namespace, Name: fmt.Sprintf("api-%d", i)})
	}
	return out
}

// One resource, a simple confirmation, with a force option.
// The plan of a single delete names what goes, with a
// button - no typed name, no count - says nothing else, names no object on its
// control, and is `forceable`. No other verb's plan is.
func TestOneDeleteIsASimpleForceablePlan(t *testing.T) {
	for _, one := range []struct {
		kind, namespace, name, line string
	}{
		{"pods", "argo", "api-0", "Deletes Pod api-0."},
		{"nodes", "", "node-a", "Deletes Node node-a."},
	} {
		h := newActsHarness(t, fakeNode("node-a"), fakePod("argo", "api-0"))
		facts, err := h.acts.PreviewAction(context.Background(), "prod", api.ActionRequest{
			Verb:   string(actions.Delete),
			Target: api.PlanTarget{Kind: one.kind, Namespace: one.namespace, Name: one.name},
		})
		if err != nil {
			t.Fatalf("previewing delete on %s: %v", one.kind, err)
		}
		if facts.Confirmation == nil || facts.Confirmation.Kind != "acknowledge" {
			t.Errorf("delete on %s asks for %+v, want a button (acknowledge)", one.kind, facts.Confirmation)
		}
		if facts.Headline != one.line {
			t.Errorf("delete on %s reads %q, want %q", one.kind, facts.Headline, one.line)
		}
		if facts.Button != "Delete" {
			t.Errorf("delete on %s commits with %q, want %q", one.kind, facts.Button, "Delete")
		}
		if facts.Explanation != "" || facts.Warning != "" || len(facts.Warnings) != 0 ||
			facts.Recap != nil || facts.Irreversible != nil {
			t.Errorf("delete on %s carries details: %+v", one.kind, facts)
		}
		if !facts.Forceable {
			t.Errorf("delete on %s is not forceable", one.kind)
		}
		if !facts.Destructive {
			t.Errorf("delete on %s is not destructive, so its dialog is not red", one.kind)
		}
	}

	h := newActsHarness(t, fakeNode("node-a"), fakePod("argo", "api-0"))
	for _, verb := range []actions.Verb{actions.Restart, actions.Evict} {
		facts, err := h.acts.PreviewAction(context.Background(), "prod", api.ActionRequest{
			Verb: string(verb), Target: api.PlanTarget{Kind: "pods", Namespace: "argo", Name: "api-0"},
		})
		if err != nil {
			t.Fatalf("previewing %s: %v", verb, err)
		}
		if facts.Forceable {
			t.Errorf("%s on a pod is forceable; only a delete is", verb)
		}
	}
}

package web

import (
	"testing"

	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/detail"
)

// Every kind `verbsFor` knows about, plus one it does not, so the default arm is
// covered too. Written out rather than derived: a list derived from the same
// switch the subject uses is a test that agrees with the code by construction.
var kindsWithVerbs = []string{
	"pods", "nodes", "cronjobs", "deployments", "statefulsets",
	"daemonsets", "replicasets", "replicationcontrollers", "configmaps",
}

var allVerbs = []actions.Verb{
	actions.Delete, actions.Restart, actions.Evict, actions.Cordon, actions.Uncordon,
	actions.Drain, actions.Suspend, actions.Resume, actions.Trigger, actions.Scale,
}

// The helper is the spec and nothing else, over every (verb, kind) pair the spec
// covers.
//
// **It is true by construction today and that is the point of writing it down.**
// `destructiveFor` is one line; what this pins is that it stays one line. A
// second condition added here — a verb excluded "just for the bulk bar", a kind
// special-cased — is the hand-kept list growing back inside the function that
// replaced it, and it would pass every other test in this file.
//
// # What it would catch
//
// If the drawer read `spec.Danger` while the plan carried a hand-kept verb
// list, they could disagree in both directions at once:
//
//	                  drawer            plan
//	evict             gray              red
//	restart on a pod  red               ordinary
//
// A gray Evict would open a red window, and a red Restart on a pod would open
// a window that did not treat it as destructive - on the verb `specFor` spends
// a paragraph saying *is* a delete. A per-verb list cannot express the second
// row at all: restart is destructive on a Pod and not on a Deployment, which is
// the distinction `specFor`'s key exists for.
func TestOneAnswerToWhetherAVerbIsDestructive(t *testing.T) {
	seen := 0
	for _, kind := range kindsWithVerbs {
		for _, verb := range allVerbs {
			spec, ok := specFor(verb, kind)
			if !ok {
				continue
			}
			seen++
			if got := destructiveFor(verb, kind); got != spec.Danger {
				t.Errorf("%s on %s: the plan says destructive=%t and the spec says %t",
					verb, kind, got, spec.Danger)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no (verb, kind) pair was covered, so this test asserted nothing")
	}
}

// The same answer again, read off the control the drawer actually draws rather
// than off the function behind it — `objectActions` is where `spec.Danger`
// becomes `DetailAction.destructive`, and a mapping is a place a flag can be
// dropped.
func TestTheDrawersFlagAndThePlansFlagAgree(t *testing.T) {
	for _, kind := range kindsWithVerbs {
		object := detail.Object{Kind: kind}
		if isPodKind(kind) {
			// Without a controller the drawer withholds Restart entirely, which
			// is a different question from whether it is destructive.
			object.Relations = []detail.Relation{controlledBy("ReplicaSet")}
		}
		for _, action := range objectActions(kind, object, true, "") {
			drawer := action.Destructive != nil && *action.Destructive
			plan := destructiveFor(actions.Verb(action.Verb), kind)
			if drawer != plan {
				t.Errorf("%s on %s: the drawer draws destructive=%t and the confirmation says %t",
					action.Verb, kind, drawer, plan)
			}
		}
	}
}

// The two rows of the table above, named, so a change to either surface has to
// say out loud that it meant to change them.
//
// Evict is destructive too, not only Delete, Drain and Restart-on-a-Pod - see
// the comment on Evict's spec.
func TestTheVerbsThatAreDestructiveAndTheOnesThatAreNot(t *testing.T) {
	for _, one := range []struct {
		verb actions.Verb
		kind string
		want bool
	}{
		{actions.Restart, "pods", true},
		{actions.Restart, "deployments", false},
		{actions.Evict, "pods", true},
		{actions.Delete, "pods", true},
		{actions.Drain, "nodes", true},
		{actions.Cordon, "nodes", false},
		{actions.Uncordon, "nodes", false},
		{actions.Suspend, "cronjobs", false},
		{actions.Trigger, "cronjobs", false},
		{actions.Scale, "deployments", false},
	} {
		if got := destructiveFor(one.verb, one.kind); got != one.want {
			t.Errorf("%s on %s is destructive=%t, want %t", one.verb, one.kind, got, one.want)
		}
	}
}

// The bulk bar's flag comes from the same producer as the drawer's.
//
// Without the flag, Delete, Restart and Clear would be drawn identically on the
// control that acts on twelve objects at once; the server has the answer.
func TestTheBulkBarsFlagComesFromTheSameProducer(t *testing.T) {
	for _, kind := range []string{"pods", "nodes", "cronjobs", "secrets"} {
		affordance := bulkAffordance("prod", kind, false)
		if affordance == nil {
			continue
		}
		for _, verb := range affordance.Verbs {
			bar := verb.Destructive != nil && *verb.Destructive
			plan := destructiveFor(actions.Verb(verb.Verb), kind)
			if bar != plan {
				t.Errorf("%s on %s: the bar draws destructive=%t and the confirmation says %t",
					verb.Verb, kind, bar, plan)
			}
		}
	}

	// And the two rows that are the point of it: on a pod list, Delete and Evict
	// are marked and the ordinary verbs are not.
	pods := bulkAffordance("prod", "pods", false)
	if pods == nil {
		t.Fatal("a pod list offers no bulk verb")
	}
	marked := map[string]bool{}
	for _, verb := range pods.Verbs {
		marked[verb.Verb] = verb.Destructive != nil && *verb.Destructive
	}
	if !marked["delete"] || !marked["evict"] {
		t.Errorf("a pod list marks %v; delete and evict are both destructive here", marked)
	}
	nodes := bulkAffordance("prod", "nodes", false)
	if nodes == nil {
		t.Fatal("a node list offers no bulk verb")
	}
	// A node list offers Delete as well, and it alone
	// is marked: cordon and uncordon move nothing.
	for _, verb := range nodes.Verbs {
		marked := verb.Destructive != nil && *verb.Destructive
		if marked != (verb.Verb == "delete") {
			t.Errorf("%s is marked destructive=%v on a node list; only delete is", verb.Verb, marked)
		}
	}
}

// The order a kind's verbs arrive in is part of the answer: nothing ordinary
// follows something destructive, on the row or on the bar.
//
// `verbsFor`'s header claims this — *"the everyday ones first, the
// irreversible one last and on its own"* — and this test checks it. A verb
// appended to one of its arms lands wherever it was typed, and both surfaces that read the
// table draw it in the order it arrives. On the selection bar that is a Delete
// in the middle of a row whose every button acts on the whole ticked set.
//
// Two things about the shape of this test, because both are the difference
// between it holding and it looking like it holds:
//
//   - **`destructiveFor` is the judge, never the verb's name.** The bar's own
//     test pins the same property for the color (a custom resource's
//     `delete-snapshot` is not destructive and a `recycle` may be); an ordering
//     rule keyed on a string would be the third place that mistake could be
//     made.
//   - **It asserts the invariant, not a list.** A fixed expected order per kind
//     is a second copy of `verbsFor` that has to be edited whenever the first is,
//     and the edit that forgets it is exactly the one this exists to catch. The
//     per-kind lists that *are* asserted, in listfilled_test.go, are the bulk
//     sets themselves and are a different claim.
//
// It runs over both readers of the table: `verbsFor` as the drawer takes it, and
// `bulkAffordance` as the bar takes it — the second because it drops entries
// (Restart on a pod, and every non-bulk verb), and a filter is exactly the kind
// of thing that can reorder what survives it.
func TestNoOrdinaryVerbFollowsADestructiveOne(t *testing.T) {
	for _, kind := range append(append([]string{}, kindsWithVerbs...), "namespaces", "secrets") {
		seen := ""
		for _, verb := range verbsFor(kind) {
			if destructiveFor(verb, kind) {
				seen = string(verb)
				continue
			}
			if seen != "" {
				t.Errorf("verbsFor(%s) offers %s after the destructive %s: the everyday ones "+
					"come first and the irreversible ones last, so a Delete never sits in the "+
					"middle of the row", kind, verb, seen)
			}
		}

		affordance := bulkAffordance("prod", kind, false)
		if affordance == nil {
			continue
		}
		seen = ""
		for _, verb := range affordance.Verbs {
			if verb.Destructive != nil && *verb.Destructive {
				seen = verb.Verb
				continue
			}
			if seen != "" {
				t.Errorf("the bulk bar for %s offers %s after the destructive %s; the bar's own "+
					"sort is a safety net and this is the order it should not have to fix",
					kind, verb.Verb, seen)
			}
		}
	}
}

// The pod bar's order, named rather than left implicit in the invariant above.
//
// Clear is the client's and is not a verb; what the server owes is Evict before
// Delete, and **that cannot come from the flag**: both carry `Danger: true`, so
// the invariant above is satisfied by either order. It is `verbsFor`'s declaration order and nothing else, which
// is why it needs its own assertion rather than being covered by the rule.
func TestEvictComesBeforeDeleteOnAPodBar(t *testing.T) {
	pods := bulkAffordance("prod", "pods", false)
	if pods == nil {
		t.Fatal("a pod list offers no bulk verb")
	}
	at := map[string]int{}
	for i, verb := range pods.Verbs {
		at[verb.Verb] = i
	}
	evict, hasEvict := at["evict"]
	del, hasDelete := at["delete"]
	if !hasEvict || !hasDelete {
		t.Fatalf("a pod bar offers %v; evict and delete are both expected on it", at)
	}
	if evict > del {
		t.Errorf("a pod bar offers delete before evict (%v): the less final one comes first, "+
			"because an eviction a PodDisruptionBudget refuses leaves the pod where it is", at)
	}
}

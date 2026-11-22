package web

import (
	"testing"

	"github.com/thonicdev/kubetower/internal/detail"
)

// **Built the way `controllerKindOf` reads it**: a test must fill a field the
// way the code does rather than the way its name suggests. The controller is not
// `Object.Controller` here - it is
// the relation whose role is `controlled by`, and its Kind is the half of the
// label before the slash.
func controlledBy(kind string) detail.Relation {
	return detail.Relation{Role: detail.RoleControlledBy, Label: kind + "/owner-1"}
}

func verbsOf(kind string, object detail.Object, writable bool) []string {
	out := []string{}
	for _, action := range objectActions(kind, object, writable, "this cluster is marked read-only") {
		out = append(out, action.Verb)
	}
	return out
}

// The verb set is a property of the kind, and it is the same table the list's
// bulk bar reads: `DetailPage.actions` is in the contract and in the Go struct,
// and the mapper must fill it so a client knows which verbs to offer on an object.
func TestAnObjectOffersTheVerbsItsKindHas(t *testing.T) {
	for _, one := range []struct {
		kind string
		want []string
	}{
		// A pod with a controller: restart is a delete the owner will undo.
		{kind: "pods", want: []string{"restart", "evict", "delete"}},
		// A node deletes too, last: kubectl can, so the console can.
		{kind: "nodes", want: []string{"cordon", "uncordon", "drain", "delete"}},
		{kind: "deployments", want: []string{"restart", "scale", "delete"}},
		{kind: "cronjobs", want: []string{"trigger", "suspend", "resume", "delete"}},
		// A namespace deletes, and nothing more.
		{kind: "namespaces", want: []string{"delete"}},
		// Anything else deletes and nothing more.
		{kind: "configmaps", want: []string{"delete"}},
	} {
		object := detail.Object{Kind: one.kind, Relations: []detail.Relation{controlledBy("ReplicaSet")}}
		got := verbsOf(one.kind, object, true)
		if len(got) != len(one.want) {
			t.Errorf("%s offered %v, want %v", one.kind, got, one.want)
			continue
		}
		for at := range got {
			if got[at] != one.want[at] {
				t.Errorf("%s offered %v, want %v", one.kind, got, one.want)
				break
			}
		}
	}
}

// Withheld, not disabled - and the difference is the sentence it would have to
// say. A disabled control means *not now*; a pod nothing will recreate does not
// have this verb at all.
func TestRestartIsWithheldOnAPodNothingWillRecreate(t *testing.T) {
	bare := detail.Object{Kind: "pods"}
	for _, verb := range verbsOf("pods", bare, true) {
		if verb == "restart" {
			t.Fatal("a bare pod was offered Restart, which on a pod is Delete under a gentler word")
		}
	}

	owned := detail.Object{Kind: "pods", Relations: []detail.Relation{controlledBy("ReplicaSet")}}
	var found bool
	for _, verb := range verbsOf("pods", owned, true) {
		if verb == "restart" {
			found = true
		}
	}
	if !found {
		t.Error("a pod a ReplicaSet will recreate was not offered Restart")
	}
}

// A read-only cluster disables every verb and says which setting refuses. The
// guardrail's own sentence, because the two causes are different people's.
func TestAReadOnlyClusterRefusesEveryVerbAndSaysWhy(t *testing.T) {
	object := detail.Object{Kind: "deployments"}
	actions := objectActions("deployments", object, false, "this cluster is marked read-only")
	if len(actions) == 0 {
		t.Fatal("a read-only cluster withheld the verbs entirely; they are disabled, not absent")
	}
	for _, action := range actions {
		if action.Permitted {
			t.Errorf("%s is permitted on a read-only cluster", action.Verb)
		}
		if action.Reason == nil || action.Reason.Message != "this cluster is marked read-only" {
			t.Errorf("%s carries no reason, so a disabled control says nothing", action.Verb)
		}
	}
}

// `destructive` is the server's answer and not the client's guess at one: the
// table marks Delete, Drain and Restart-on-a-pod, and nothing else.
func TestTheDestructiveVerbsAreMarkedAsSuch(t *testing.T) {
	marked := map[string]bool{}
	for _, action := range objectActions("nodes", detail.Object{Kind: "nodes"}, true, "") {
		marked[action.Verb] = action.Destructive != nil && *action.Destructive
	}
	if !marked["drain"] {
		t.Error("Drain is not marked destructive")
	}
	if marked["cordon"] {
		t.Error("Cordon is marked destructive; it stops scheduling and moves nothing")
	}
}

// Irreversible is the menu's red, and it is not Destructive. On a pod, Evict,
// Restart and Delete are all destructive, and Delete alone is
// irreversible. The plan's `irreversible` is read off the object: a menu has not read
// the pod, so its red is the verb's, and the confirmation has, so it says
// "cannot be undone" only where nothing would make the pod again
// (actionline_test.go).
func TestOnlyDeleteIsIrreversibleAndTheDestructiveSetIsUnchanged(t *testing.T) {
	object := detail.Object{Kind: "pods", Relations: []detail.Relation{controlledBy("ReplicaSet")}}
	got := map[string][2]bool{}
	for _, action := range objectActions("pods", object, true, "") {
		got[action.Verb] = [2]bool{
			action.Destructive != nil && *action.Destructive,
			action.Irreversible != nil && *action.Irreversible,
		}
	}
	for verb, want := range map[string][2]bool{
		"evict":   {true, false},
		"restart": {true, false},
		"delete":  {true, true},
	} {
		if got[verb] != want {
			t.Errorf("%s on a pod is destructive=%t irreversible=%t, want %t %t",
				verb, got[verb][0], got[verb][1], want[0], want[1])
		}
	}
	for _, kind := range kindsWithVerbs {
		for _, verb := range allVerbs {
			spec, ok := specFor(verb, kind)
			if ok && spec.Irreversible && !spec.Danger {
				t.Errorf("%s on %s is irreversible and not destructive; an act that cannot be undone is red everywhere", verb, kind)
			}
		}
	}
}

package detail

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/resources"
)

// A Terminating namespace, as a kind cluster writes it for a namespace holding
// a ConfigMap with a finalizer nobody removes. Its five conditions all name a
// problem, so `True` is the complaint; the generic reading would draw the three
// healthy `False` ones as warnings and hide the two `True` ones that say why
// the namespace is stuck.
func terminatingNamespace() *unstructured.Unstructured {
	condition := func(kind, status, reason, message string) map[string]any {
		return map[string]any{
			"type": kind, "status": status, "reason": reason, "message": message,
			"lastTransitionTime": "2026-11-02T20:53:45Z",
		}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": map[string]any{
			"name": "kt396-blocked", "uid": "uid-ns",
			"creationTimestamp": "2026-11-02T20:49:39Z",
			"deletionTimestamp": "2026-11-02T20:53:39Z",
		},
		"spec": map[string]any{"finalizers": []any{"kubernetes"}},
		"status": map[string]any{
			"phase": "Terminating",
			"conditions": []any{
				condition("NamespaceDeletionDiscoveryFailure", "False", "ResourcesDiscovered",
					"All resources successfully discovered"),
				condition("NamespaceDeletionGroupVersionParsingFailure", "False", "ParsedGroupVersions",
					"All legacy kube types successfully parsed"),
				condition("NamespaceDeletionContentFailure", "False", "ContentDeleted",
					"All content successfully deleted, may be waiting on finalization"),
				condition("NamespaceContentRemaining", "True", "SomeResourcesRemain",
					"Some resources are remaining: configmaps. has 1 resource instances"),
				condition("NamespaceFinalizersRemaining", "True", "SomeFinalizersRemain",
					"Some content in the namespace has finalizers remaining: "+
						"example.com/block in 1 resource instances"),
			},
		},
	}}
}

var namespaceGVK = schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}

// Every condition is on the panel with its status, reason, message and last
// transition, and the phase beside them.
func TestATerminatingNamespaceDrawsEveryConditionWithItsFields(t *testing.T) {
	facts := factsOf(terminatingNamespace(), namespaceGVK)

	phase, ok := factNamed(facts, "Phase")
	if !ok || phase.Value != "Terminating" || !phase.Pill {
		t.Errorf("the phase is drawn as %+v (found %v), want a Terminating pill", phase, ok)
	}

	since := "since " + time.Date(2026, 11, 2, 20, 53, 45, 0, time.UTC).Local().Format(time.RFC3339)
	for _, want := range []struct {
		kind, status, reason, message, class string
	}{
		{"NamespaceDeletionDiscoveryFailure", "False", "ResourcesDiscovered",
			"All resources successfully discovered", resources.ClassNone},
		{"NamespaceDeletionGroupVersionParsingFailure", "False", "ParsedGroupVersions",
			"All legacy kube types successfully parsed", resources.ClassNone},
		{"NamespaceDeletionContentFailure", "False", "ContentDeleted",
			"may be waiting on finalization", resources.ClassNone},
		{"NamespaceContentRemaining", "True", "SomeResourcesRemain",
			"configmaps. has 1 resource instances", resources.ClassWarn},
		{"NamespaceFinalizersRemaining", "True", "SomeFinalizersRemain",
			"example.com/block in 1 resource instances", resources.ClassWarn},
	} {
		fact, ok := factNamed(facts, want.kind)
		if !ok {
			t.Errorf("%s is not on the panel", want.kind)
			continue
		}
		for _, part := range []string{want.status + " — " + want.reason, want.message, since} {
			if !strings.Contains(fact.Value, part) {
				t.Errorf("%s reads %q; %q is missing", want.kind, fact.Value, part)
			}
		}
		if fact.Class != want.class {
			t.Errorf("%s is colored %q, want %q", want.kind, fact.Class, want.class)
		}
		if !fact.Sentence {
			t.Errorf("%s is not marked as prose, so its message is held on one line", want.kind)
		}
	}

	deleting, ok := factNamed(facts, "Deleting since")
	if !ok || !strings.Contains(deleting.Value, "kubernetes (spec)") {
		t.Errorf("Deleting since reads %q; the namespace's own spec finalizer is what holds it",
			deleting.Value)
	}
}

// A deletion failure is a fault, not a wait.
func TestANamespaceDeletionFailureIsBad(t *testing.T) {
	ns := terminatingNamespace()
	conditions, _, _ := unstructured.NestedSlice(ns.Object, "status", "conditions")
	conditions[0].(map[string]any)["status"] = "True"
	conditions[0].(map[string]any)["reason"] = "DiscoveryFailed"
	_ = unstructured.SetNestedSlice(ns.Object, conditions, "status", "conditions")

	fact, ok := factNamed(factsOf(ns, namespaceGVK), "NamespaceDeletionDiscoveryFailure")
	if !ok || fact.Class != resources.ClassBad {
		t.Errorf("a True NamespaceDeletionDiscoveryFailure is drawn %+v, want bad", fact)
	}
}

// The generic reading, which every other kind goes through: a built-in type
// that names a problem is drawn when True (ReplicaFailure, as a node's
// pressures are), and a custom resource's condition is read True-healthy,
// whatever its name ends with - a suffix rule could invert a CRD's healthy
// condition, so there is none.
func TestOnlyBuiltInProblemConditionsAreReadTheOtherWayRound(t *testing.T) {
	deployment := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "api", "namespace": "shop"},
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "Available", "status": "True"},
			map[string]any{
				"type": "ReplicaFailure", "status": "True", "reason": "FailedCreate", "message": "quota",
			},
		}},
	}}
	facts := conditionsOf(deployment, time.Now())
	if len(facts) != 1 || facts[0].Label != "ReplicaFailure" {
		t.Fatalf("drawn %+v, want ReplicaFailure alone", facts)
	}
	if !strings.Contains(facts[0].Value, "True — FailedCreate: quota") {
		t.Errorf("ReplicaFailure reads %q", facts[0].Value)
	}

	widget := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.com/v1", "kind": "Widget",
		"metadata": map[string]any{"name": "w", "namespace": "shop"},
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "Ready", "status": "True"},
			map[string]any{"type": "SyncFailure", "status": "True"},
			map[string]any{"type": "FetchFailed", "status": "True"},
			map[string]any{"type": "FinalizersRemaining", "status": "False"},
		}},
	}}
	facts = conditionsOf(widget, time.Now())
	if len(facts) != 1 || facts[0].Label != "FinalizersRemaining" {
		t.Fatalf("a custom resource's conditions drawn %+v; want the ordinary reading, "+
			"FinalizersRemaining (False) alone", facts)
	}
}

// A condition with no lastTransitionTime says nothing about when, rather than
// "since 0001-01-01".
func TestAConditionWithNoTransitionTimeCarriesNoDate(t *testing.T) {
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.com/v1", "kind": "Widget",
		"metadata": map[string]any{"name": "w", "namespace": "shop"},
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "Ready", "status": "False", "reason": "Waiting", "message": "no pods"},
		}},
	}}
	facts := conditionsOf(object, time.Now())
	if len(facts) != 1 {
		t.Fatalf("drawn %+v, want Ready alone", facts)
	}
	if got, want := facts[0].Value, "False — Waiting: no pods"; got != want {
		t.Errorf("Ready reads %q, want %q: no time, no `since`", got, want)
	}
}

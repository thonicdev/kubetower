package api

import (
	"strings"
	"testing"
)

// The sentence names one object in the singular, and a set in the plural.
//
// # What this guards
//
// `PlanTarget.Kind` is the plural resource slug the URL carries, and the
// singular arm must not print it as it stands, or a delete
// of one pod reads:
//
//	Delete pods krow-system/krow-retention-29826917-6t5qn.
//
// One object, named in the plural, in the sentence a user reads before
// destroying it. The plural arm is right by construction — a plural slug is already
// plural — and a careless rule would be wrong at `Count == 1` with no target to name, where it would produce
// "1 objects".
func TestTheSentenceNamesOneObjectInTheSingular(t *testing.T) {
	for _, one := range []struct {
		name  string
		facts PlanFacts
		want  string
	}{
		{
			name: "one pod",
			facts: PlanFacts{Verb: "delete", Count: 1, Targets: []PlanTarget{
				{Kind: "pods", Namespace: "krow-system", Name: "krow-retention-29826917-6t5qn"},
			}},
			want: "Delete pod krow-system/krow-retention-29826917-6t5qn.",
		},
		{
			name: "one cluster-scoped object, which has no namespace to print",
			facts: PlanFacts{Verb: "cordon", Count: 1, Targets: []PlanTarget{
				{Kind: "nodes", Name: "node-a"},
			}},
			want: "Cordon node node-a.",
		},
		{
			name: "one config map, the kind whose console title is two words",
			facts: PlanFacts{Verb: "write", Count: 1, Targets: []PlanTarget{
				{Kind: "configmaps", Namespace: "argo", Name: "settings"},
			}},
			want: "Write configmap argo/settings.",
		},
		{
			// The plural arm, unchanged and asserted so it stays unchanged: a
			// change to the singular must not cost the case that is already right.
			name: "a set of pods",
			facts: PlanFacts{Verb: "delete", Count: 203, Targets: []PlanTarget{
				{Kind: "pods", Namespace: "argo", Name: "api-0"},
			}},
			want: "Delete 203 pods. This cannot be undone.",
		},
		{
			// Count is the server's figure and the array may be shorter, so a
			// count of one with no target to name still reaches the plural arm.
			// It must not say "1 objects".
			name:  "one object the plan could not name",
			facts: PlanFacts{Verb: "delete", Count: 1},
			want:  "Delete 1 object. This cannot be undone.",
		},
		{
			name:  "nothing at all",
			facts: PlanFacts{Verb: "delete"},
			want:  "Nothing matches: delete would touch no object.",
		},
	} {
		t.Run(one.name, func(t *testing.T) {
			if got := sentenceFor(one.facts); got != one.want {
				t.Errorf("the sentence reads %q, and it should read %q", got, one.want)
			}
		})
	}
}

// Every slug the console routes on has a singular this rule does not mangle.
//
// The list is the slugs `internal/resources` declares. It is spelled out here
// rather than imported because this package owns the contract and does not know
// how a cluster is reached (`backend.go`) — so the price of the rule is a list
// in a test, where a slug that breaks it is a red line rather than a word on a
// confirmation.
func TestTheSingularRuleAnswersEveryKindTheConsoleRoutesOn(t *testing.T) {
	for slug, want := range map[string]string{
		"pods": "pod", "deployments": "deployment", "statefulsets": "statefulset",
		"daemonsets": "daemonset", "replicasets": "replicaset", "jobs": "job",
		"cronjobs": "cronjob", "services": "service", "ingresses": "ingress",
		"endpoints": "endpoint", "configmaps": "configmap", "secrets": "secret",
		"serviceaccounts": "serviceaccount", "persistentvolumeclaims": "persistentvolumeclaim",
		"persistentvolumes": "persistentvolume", "nodes": "node", "namespaces": "namespace",
		"events": "event", "replicationcontrollers": "replicationcontroller",
		"endpointslices": "endpointslice", "ingressclasses": "ingressclass",
		"networkpolicies": "networkpolicy", "resourcequotas": "resourcequota",
		"limitranges": "limitrange", "horizontalpodautoscalers": "horizontalpodautoscaler",
		"poddisruptionbudgets": "poddisruptionbudget", "priorityclasses": "priorityclass",
		"runtimeclasses": "runtimeclass", "leases": "lease",
		"mutatingwebhookconfigurations":   "mutatingwebhookconfiguration",
		"validatingwebhookconfigurations": "validatingwebhookconfiguration",
		"storageclasses":                  "storageclass", "clusterroles": "clusterrole",
		"roles": "role", "clusterrolebindings": "clusterrolebinding",
		"rolebindings": "rolebinding",

		// A qualified custom resource. Its slug is a name somebody else chose and
		// this rule may not take a letter off it.
		"applicationsets.argoproj.io": "applicationsets.argoproj.io",

		// The fallback the plural arm uses when the plan names no target.
		"object": "object",
		"":       "object",
	} {
		if got := KindWord(slug, 1); got != want {
			t.Errorf("one %q is a %q, and it should be a %q", slug, got, want)
		}
		// And the plural is left alone: a slug is already the plural, so the
		// word a set is named by must come back as the slug was spelled.
		if slug != "" && slug != "object" {
			if got := KindWord(slug, 2); got != slug {
				t.Errorf("two %q are %q, and the slug is already the plural", slug, got)
			}
		}
	}
}

// No sentence this package composes carries a plural slug next to a single
// object's name, whatever arm it came out of.
//
// The check, in the shape a regression would take: a future
// arm that prints `t.Kind` directly passes every table row above that it does
// not have a row in, and fails here.
func TestNoSingularSentenceNamesItsObjectInThePlural(t *testing.T) {
	for _, kind := range []string{"pods", "nodes", "configmaps", "networkpolicies", "ingresses"} {
		facts := PlanFacts{Verb: "delete", Count: 1, Targets: []PlanTarget{
			{Kind: kind, Namespace: "argo", Name: "one"},
		}}
		got := sentenceFor(facts)
		if strings.Contains(got, " "+kind+" ") {
			t.Errorf("the sentence for one object reads %q and carries the plural slug %q", got, kind)
		}
	}
}

// A plan that carries a headline is read by it, and one that does not keeps the
// sentence composed from its verb and its target.
//
// The headline is the line a single verb's preview composed from the object -
// who owns it, what would make it again - and "Delete pod argo/api-0." is true
// of every pod. It is a member of the facts, so the sentence is still derived
// from nothing else and cannot describe another object than the facts name.
func TestASentenceIsTheHeadlineWhereThePlanCarriesOne(t *testing.T) {
	one := []PlanTarget{{Kind: "pods", Namespace: "argo", Name: "web-7f9c-x2"}}
	with := PlanFacts{Verb: "delete", Count: 1, Targets: one,
		Headline: "Deletes pod web-7f9c-x2. ReplicaSet web-7f9c creates a new one."}
	if got := sentenceFor(with); got != with.Headline {
		t.Errorf("the sentence reads %q, and the plan's headline is %q", got, with.Headline)
	}
	without := PlanFacts{Verb: "delete", Count: 1, Targets: one}
	if got := sentenceFor(without); got != "Delete pod argo/web-7f9c-x2." {
		t.Errorf("a plan with no headline reads %q; it keeps the composed sentence", got)
	}
}

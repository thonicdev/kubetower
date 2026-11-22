package web

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
)

// The confirmation leads with one line about this object.
//
// # What these catch
//
// A line that is true of the verb and says nothing about the pod - "Delete pod
// argo/api-0.", the verb's paragraph about garbage collection, and in red "If
// nothing owns this pod, deleting it is all that happens" - reads as decoration
// on a pod its ReplicaSet replaces a second later and on a bare debug pod alike,
// including the one where it is the whole point. These walk the preview end to end, through the
// harness's cluster, so the line is the one an operator reads and not one
// composed in the test.

// previewLine previews one verb on one object through the JSON mapping.
func previewLine(t *testing.T, verb actions.Verb, kind, namespace, name string, objects ...*unstructured.Unstructured) api.PlanFacts {
	t.Helper()
	h := newActsHarness(t, objects...)
	facts, err := h.acts.PreviewAction(context.Background(), "prod", api.ActionRequest{
		Verb:   string(verb),
		Target: api.PlanTarget{Kind: kind, Namespace: namespace, Name: name},
	})
	if err != nil {
		t.Fatalf("previewing %s on %s %s: %v", verb, kind, name, err)
	}
	return facts
}

func irreversible(facts api.PlanFacts) bool {
	return facts.Irreversible != nil && *facts.Irreversible
}

// unowned takes a fixture's ownerReferences away.
func unowned(object map[string]any) {
	delete(object["metadata"].(map[string]any), "ownerReferences")
}

// annotated adds annotations to a fixture.
func annotated(pairs ...string) func(map[string]any) {
	return func(object map[string]any) {
		meta := object["metadata"].(map[string]any)
		annotations := map[string]any{}
		for i := 0; i+1 < len(pairs); i += 2 {
			annotations[pairs[i]] = pairs[i+1]
		}
		meta["annotations"] = annotations
	}
}

// A pod's line says what makes another, by kind and name, or that nothing will
// - and only the second is irreversible.
//
// Restart and Evict on a pod remove it as a delete does, so they say the same
// about what comes after; the first word is each verb's own.
func TestAPodsLineSaysWhatMakesAnotherOrThatNothingWill(t *testing.T) {
	for _, one := range []struct {
		what         string
		verb         actions.Verb
		pod          *unstructured.Unstructured
		want         string
		irreversible bool
	}{
		// A single delete names only what goes;
		// a restart on a pod is the same removal and keeps the line, so the
		// owner cases are measured on it.
		{
			what: "a restart of a pod its ReplicaSet replaces",
			verb: actions.Restart, pod: fakePod("argo", "api-0"),
			want: "Deletes pod api-0. ReplicaSet api creates a new one.",
		},
		{
			what: "a restart of a pod nothing owns",
			verb: actions.Restart, pod: fakePod("argo", "debug", unowned),
			want: "Deletes pod debug. Nothing will recreate it.", irreversible: true,
		},
		{
			what: "an eviction of a pod nothing owns",
			verb: actions.Evict, pod: fakePod("argo", "debug", unowned),
			want: "Evicts pod debug. Nothing will recreate it.", irreversible: true,
		},
		{
			what: "a restart of a Job's pod, which costs the Job a retry",
			verb: actions.Restart, pod: fakePod("argo", "nightly-1-x", ownedByController("Job", "nightly-1")),
			want: "Deletes pod nightly-1-x. Job nightly-1 starts another, spending one of its retries.",
		},
		{
			what: "a restart of a bare pod Argo CD applied",
			verb: actions.Restart,
			pod:  fakePod("argo", "debug", unowned, annotated("argocd.argoproj.io/tracking-id", "tools:/Pod:argo/debug")),
			want: "Deletes pod debug. Argo CD application tools can put it back.",
		},
	} {
		t.Run(one.what, func(t *testing.T) {
			facts := previewLine(t, one.verb, "pods", "argo", one.pod.GetName(), one.pod, fakeNode("node-a"))
			if facts.Headline != one.want {
				t.Errorf("the line reads %q, and it should read %q", facts.Headline, one.want)
			}
			if irreversible(facts) != one.irreversible {
				t.Errorf("the plan is irreversible=%t, and it should be %t: %q",
					irreversible(facts), one.irreversible, facts.Headline)
			}
			// The verb's own general caveat stays on the plan for Details;
			// what is true of this pod is the line, and no warning repeats it.
			for _, warning := range facts.Warnings {
				if strings.Contains(warning, "own") || strings.Contains(warning, "recreate") {
					t.Errorf("a warning repeats what the line says: %q", warning)
				}
			}
		})
	}
}

// ownedByController is a fixture's controller ownerReference.
func ownedByController(kind, name string) func(map[string]any) {
	return func(object map[string]any) {
		object["metadata"].(map[string]any)["ownerReferences"] = []any{map[string]any{
			"apiVersion": "batch/v1", "kind": kind, "name": name, "uid": "uid-" + name, "controller": true,
		}}
	}
}

// Run now's line, and a warning only for what is true of this CronJob.
//
// The verb's warning - the Job is not owned, concurrencyPolicy does not apply -
// is the same on every CronJob and is folded under Details. What is said in
// the open is a run already in flight and a suspension the run ignores, each
// only where the CronJob carries it.
func TestARunNowLineWarnsOnlyOfWhatIsTrueOfThisCronJob(t *testing.T) {
	active := func(names ...string) func(map[string]any) {
		return func(object map[string]any) {
			refs := make([]any, 0, len(names))
			for _, name := range names {
				refs = append(refs, map[string]any{"kind": "Job", "name": name, "namespace": "shop"})
			}
			object["status"] = map[string]any{"active": refs}
		}
	}
	suspended := func(object map[string]any) {
		object["spec"].(map[string]any)["suspend"] = true
	}
	for _, one := range []struct {
		what     string
		cronjob  *unstructured.Unstructured
		warnings []string
	}{
		{"an idle CronJob", fakeCronJob("shop", "nightly"), nil},
		{
			"a CronJob with a run in flight", fakeCronJob("shop", "nightly", active("nightly-29384")),
			[]string{"A run is already in progress (nightly-29384)."},
		},
		{
			"a suspended CronJob", fakeCronJob("shop", "nightly", suspended),
			[]string{"nightly is suspended: this run starts anyway."},
		},
		{
			"a suspended CronJob with two runs in flight, named in order",
			fakeCronJob("shop", "nightly", suspended, active("nightly-2", "nightly-1")),
			[]string{
				"A run is already in progress (nightly-1, nightly-2).",
				"nightly is suspended: this run starts anyway.",
			},
		},
	} {
		t.Run(one.what, func(t *testing.T) {
			facts := previewLine(t, actions.Trigger, "cronjobs", "shop", "nightly", one.cronjob)
			if facts.Headline != "Starts a Job from nightly, now." {
				t.Errorf("the line reads %q", facts.Headline)
			}
			if strings.Join(facts.Warnings, "|") != strings.Join(one.warnings, "|") {
				t.Errorf("the warnings are %q, and they should be %q", facts.Warnings, one.warnings)
			}
			if facts.Button != "Run nightly now" {
				t.Errorf("the button reads %q", facts.Button)
			}
			// Run now creates a Job and removes nothing.
			if irreversible(facts) {
				t.Error("a Run now is drawn as irreversible")
			}
			// The general caveat is still carried, for Details.
			if !strings.Contains(facts.Warning, "concurrencyPolicy") {
				t.Errorf("the verb's own warning is gone from the plan: %q", facts.Warning)
			}
		})
	}
}

// The button is the act and the object, composed by the server - except a
// delete's, which is "Delete": the title names the object.
func TestTheButtonNamesTheActAndTheObject(t *testing.T) {
	for _, one := range []struct {
		verb      actions.Verb
		kind      string
		namespace string
		name      string
		objects   []*unstructured.Unstructured
		want      string
	}{
		{actions.Delete, "pods", "argo", "web-7f9c-x2", []*unstructured.Unstructured{fakePod("argo", "web-7f9c-x2")}, "Delete"},
		// The honest word on the control: a pod's restart is
		// its delete.
		{actions.Restart, "pods", "argo", "api-0", []*unstructured.Unstructured{fakePod("argo", "api-0")}, "Delete api-0"},
		{actions.Evict, "pods", "argo", "api-0", []*unstructured.Unstructured{fakePod("argo", "api-0")}, "Evict api-0"},
		{actions.Restart, "deployments", "shop", "web", []*unstructured.Unstructured{fakeDeployment("shop", "web")}, "Restart web"},
		{actions.Scale, "deployments", "shop", "web", []*unstructured.Unstructured{fakeDeployment("shop", "web")}, "Scale web"},
		{actions.Delete, "deployments", "shop", "web", []*unstructured.Unstructured{fakeDeployment("shop", "web")}, "Delete"},
		{actions.Cordon, "nodes", "", "node-a", []*unstructured.Unstructured{fakeNode("node-a")}, "Cordon node-a"},
		{actions.Uncordon, "nodes", "", "node-a", []*unstructured.Unstructured{fakeNode("node-a")}, "Uncordon node-a"},
		{actions.Drain, "nodes", "", "node-a", []*unstructured.Unstructured{fakeNode("node-a")}, "Cordon node-a and list its pods"},
		{actions.Trigger, "cronjobs", "shop", "nightly", []*unstructured.Unstructured{fakeCronJob("shop", "nightly")}, "Run nightly now"},
		{actions.Suspend, "cronjobs", "shop", "nightly", []*unstructured.Unstructured{fakeCronJob("shop", "nightly")}, "Suspend nightly"},
		{actions.Resume, "cronjobs", "shop", "nightly", []*unstructured.Unstructured{fakeCronJob("shop", "nightly")}, "Resume nightly"},
	} {
		facts := previewLine(t, one.verb, one.kind, one.namespace, one.name, one.objects...)
		if facts.Button != one.want {
			t.Errorf("%s on %s %s commits with %q, and it should read %q",
				one.verb, one.kind, one.name, facts.Button, one.want)
		}
	}
}

// One resource, one line: a delete names what goes - the object by its Kind
// as the read spelled it, and for a namespace everything in it - whatever
// stands behind the object, and is never irreversible: no detail is drawn
// under it.
func TestADeleteNamesWhatGoesAndNothingElse(t *testing.T) {
	for _, one := range []struct {
		kind    string
		subject actions.Subject
		want    string
	}{
		{"deployments", actions.Subject{Kind: "Deployment"}, "Deletes Deployment web."},
		{"deployments", actions.Subject{Kind: "Deployment", Manager: actions.Manager{By: "Argo CD application", Name: "shop"}},
			"Deletes Deployment web."},
		{"replicasets", actions.Subject{Kind: "ReplicaSet", Controller: actions.Owner{Kind: "Deployment", Name: "api"}},
			"Deletes ReplicaSet web."},
		{"pods", actions.Subject{Kind: "Pod"}, "Deletes Pod web."},
		{"configmaps", actions.Subject{Kind: "ConfigMap"}, "Deletes ConfigMap web."},
		{"endpoints", actions.Subject{Kind: "Endpoints"}, "Deletes Endpoints web."},
		{"namespaces", actions.Subject{Kind: "Namespace"}, "Deletes Namespace web and everything in it."},
	} {
		line := lineFor(actions.Delete, one.kind, "web", actions.Recap{Subject: one.subject})
		if line.Text != one.want || line.Irreversible || len(line.Warnings) != 0 {
			t.Errorf("a delete of %+v reads %+v, want only %q", one.subject, line, one.want)
		}
	}
}

// Every verb the spec covers has a line of its own, on every kind it is
// offered on - the property "one line, specific to this object" asks of all of
// them, and the check that a verb added to `specFor` without an arm in
// `lineFor` fails here rather than opening on the generic sentence.
func TestEveryVerbTheSpecCoversHasALine(t *testing.T) {
	covered := 0
	for _, kind := range kindsWithVerbs {
		for _, verb := range verbsFor(kind) {
			if _, ok := specFor(verb, kind); !ok {
				continue
			}
			covered++
			line := lineFor(verb, kind, "one", actions.Recap{})
			if line.Text == "" {
				t.Errorf("%s on %s has no line, so its confirmation opens on the bare sentence", verb, kind)
				continue
			}
			if !strings.Contains(line.Text, "one") {
				t.Errorf("%s on %s reads %q, which does not name the object", verb, kind, line.Text)
			}
		}
	}
	if covered == 0 {
		t.Fatal("no (verb, kind) pair was covered, so this test asserted nothing")
	}
}

// A field already at the value the verb writes is said in the line, where the
// operator reads before the click - the recap that says it is folded with the
// rest of the detail.
func TestALineSaysAVerbThatChangesNothingChangesNothing(t *testing.T) {
	cordoned := fakeNode("node-a")
	cordoned.Object["spec"] = map[string]any{"unschedulable": true}
	facts := previewLine(t, actions.Cordon, "nodes", "", "node-a", cordoned)
	if facts.Headline != "Node node-a is already cordoned: this changes nothing." {
		t.Errorf("the line reads %q", facts.Headline)
	}

	facts = previewLine(t, actions.Uncordon, "nodes", "", "node-a", fakeNode("node-a"))
	if facts.Headline != "Node node-a is not cordoned: this changes nothing." {
		t.Errorf("the line reads %q", facts.Headline)
	}
}

// Suspend warns of a run in flight, which it does not stop - and only then.
func TestASuspendWarnsOfARunItDoesNotStop(t *testing.T) {
	facts := previewLine(t, actions.Suspend, "cronjobs", "shop", "nightly", fakeCronJob("shop", "nightly"))
	if facts.Headline != "Stops CronJob nightly starting new runs." || len(facts.Warnings) != 0 {
		t.Errorf("an idle CronJob's suspend reads %q with warnings %q", facts.Headline, facts.Warnings)
	}
	running := fakeCronJob("shop", "nightly", func(object map[string]any) {
		object["status"] = map[string]any{"active": []any{map[string]any{"name": "nightly-29384"}}}
	})
	facts = previewLine(t, actions.Suspend, "cronjobs", "shop", "nightly", running)
	if strings.Join(facts.Warnings, "|") != "A run is in progress (nightly-29384): suspending does not stop it." {
		t.Errorf("the warnings are %q", facts.Warnings)
	}
}

// The line is the plan's sentence: `internal/api` draws it where it drew the
// generic one, and a set keeps its count.
func TestTheLineIsTheSentenceTheServerSends(t *testing.T) {
	facts := previewLine(t, actions.Delete, "pods", "argo", "api-0", fakePod("argo", "api-0"))
	if facts.Headline == "" {
		t.Fatal("a single delete's plan carries no line")
	}
	bulk, err := apiActs{}.PreviewBulk(context.Background(), "prod", api.BulkRequest{
		Verb: string(actions.Delete), Kind: "pods", Targets: bulkTargets("argo", 4),
	})
	if err != nil {
		t.Fatalf("previewing a bulk delete: %v", err)
	}
	if bulk.Headline != "" {
		t.Errorf("a set carries a line about one object: %q", bulk.Headline)
	}
}

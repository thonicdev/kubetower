package actions

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// What a confirmation's one line is composed from, read off the object the
// preview already fetched.

func withAnnotations(pairs ...string) func(map[string]any) {
	return func(object map[string]any) {
		annotations := map[string]any{}
		for i := 0; i+1 < len(pairs); i += 2 {
			annotations[pairs[i]] = pairs[i+1]
		}
		object["metadata"].(map[string]any)["annotations"] = annotations
	}
}

func withLabels(pairs ...string) func(map[string]any) {
	return func(object map[string]any) {
		labels := map[string]any{}
		for i := 0; i+1 < len(pairs); i += 2 {
			labels[pairs[i]] = pairs[i+1]
		}
		object["metadata"].(map[string]any)["labels"] = labels
	}
}

// The controller, by kind and name, and only the one marked controller.
func TestASubjectNamesTheControllerAndNothingElse(t *testing.T) {
	owned := SubjectOf(pod("prod", "api-1", ownedBy("ReplicaSet", "api-7f9c")))
	if owned.Kind != "Pod" || owned.Controller != (Owner{Kind: "ReplicaSet", Name: "api-7f9c"}) {
		t.Errorf("an owned pod reads %+v", owned)
	}
	if owned.Remaker() != owned.Controller {
		t.Errorf("a ReplicaSet does not make another of its pod: %+v", owned.Remaker())
	}

	bare := SubjectOf(pod("prod", "debug"))
	if bare.Controller.Known() || bare.Manager.Known() || bare.Remaker().Known() {
		t.Errorf("a pod nothing owns reads %+v", bare)
	}

	// An ownerReference that is not the controller is not the thing that makes
	// another.
	notController := SubjectOf(pod("prod", "api-1", func(object map[string]any) {
		object["metadata"].(map[string]any)["ownerReferences"] = []any{map[string]any{
			"apiVersion": "v1", "kind": "ConfigMap", "name": "holder", "uid": "uid-holder",
		}}
	}))
	if notController.Controller.Known() {
		t.Errorf("an ownerReference that is not the controller was taken as one: %+v", notController.Controller)
	}
}

// A CronJob starts the next run; it does not put back the Job that was deleted.
func TestACronJobDoesNotRemakeTheJobItStarted(t *testing.T) {
	job := Subject{Kind: "Job", Controller: Owner{Kind: "CronJob", Name: "nightly"}}
	if job.Remaker().Known() {
		t.Errorf("a CronJob is taken as remaking its Job: %+v", job.Remaker())
	}
}

// Argo CD's tracking id and Helm's release annotation name a manager; the
// instance label alone does not.
func TestASubjectNamesTheToolThatHoldsItsManifest(t *testing.T) {
	for _, one := range []struct {
		what   string
		mutate func(map[string]any)
		want   Manager
	}{
		{"Argo CD's tracking id", withAnnotations("argocd.argoproj.io/tracking-id", "shop:apps/Deployment:prod/api"),
			Manager{By: "Argo CD application", Name: "shop"}},
		{"Helm's release annotation", withAnnotations("meta.helm.sh/release-name", "shop"),
			Manager{By: "Helm release", Name: "shop"}},
		{"both, where Argo CD rendered the chart and is the one that syncs",
			withAnnotations("meta.helm.sh/release-name", "chart", "argocd.argoproj.io/tracking-id", "shop:apps/Deployment:prod/api"),
			Manager{By: "Argo CD application", Name: "shop"}},
		// A convention label proves nothing about what would put the object
		// back, and taking it would take "Cannot be undone." off a line on its
		// word alone.
		{"the instance label alone", withLabels("app.kubernetes.io/instance", "shop"), Manager{}},
	} {
		t.Run(one.what, func(t *testing.T) {
			got := SubjectOf(pod("prod", "api-1", one.mutate)).Manager
			if got != one.want {
				t.Errorf("the manager reads %+v, and it should read %+v", got, one.want)
			}
		})
	}
}

// A CronJob's suspension and its runs in flight, in order.
func TestASubjectReadsACronJobsRunsAndSuspension(t *testing.T) {
	object := cronJob("prod", "billing")
	object.Object["spec"].(map[string]any)["suspend"] = true
	object.Object["status"] = map[string]any{"active": []any{
		map[string]any{"kind": "Job", "name": "billing-2"},
		map[string]any{"kind": "Job", "name": "billing-1"},
		map[string]any{"kind": "Job"},
	}}
	got := SubjectOf(object)
	if !got.Suspended {
		t.Error("a suspended CronJob reads as not suspended")
	}
	if strings.Join(got.Active, ",") != "billing-1,billing-2" {
		t.Errorf("the runs in flight read %q", got.Active)
	}

	idle := SubjectOf(cronJob("prod", "billing"))
	if idle.Suspended || len(idle.Active) != 0 {
		t.Errorf("an idle CronJob reads %+v", idle)
	}

	// A field of that name on something that is not a CronJob is not read.
	other := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "x", "namespace": "prod"},
		"spec":     map[string]any{"suspend": true},
	}}
	if SubjectOf(other).Suspended {
		t.Error("a ConfigMap was read as a suspended CronJob")
	}
}

// The preview carries the subject, from the one GET it already made.
func TestThePreviewCarriesTheSubjectFromItsOwnRead(t *testing.T) {
	h := newHarness(t, nil, pod("prod", "api-1", ownedBy("ReplicaSet", "api-7f9c")))
	recap, err := h.runner.Preview(context.Background(), Request{
		Target: target("pods", "prod", "api-1"), Verb: Delete,
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if recap.Subject.Controller != (Owner{Kind: "ReplicaSet", Name: "api-7f9c"}) {
		t.Errorf("the preview's subject reads %+v", recap.Subject)
	}
	gets := 0
	for _, action := range h.dyn.Actions() {
		if action.GetVerb() == "get" {
			gets++
		}
	}
	if gets != 1 {
		t.Errorf("the preview made %d GETs; the subject is read off the one the pin is", gets)
	}
	h.noWrites()
}

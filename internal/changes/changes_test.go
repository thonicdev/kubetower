package changes

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func fields(t *testing.T, raw string) *metav1.FieldsV1 {
	t.Helper()
	if !json.Valid([]byte(raw)) {
		t.Fatalf("the test's own fixture is not JSON: %s", raw)
	}
	return &metav1.FieldsV1{Raw: []byte(raw)}
}

// The whole point of the package. The raw form is unreadable and is the only
// record of which fields a manager owns.
func TestTheFieldTreeBecomesPathsAPersonCanRead(t *testing.T) {
	got, total := flattenFieldsV1(fields(t, `{
		"f:spec": {
			"f:replicas": {},
			"f:template": {
				"f:spec": {
					"f:containers": {
						"k:{\"name\":\"app\"}": {
							".": {},
							"f:image": {},
							"f:resources": { "f:limits": { "f:memory": {} } }
						}
					}
				}
			}
		}
	}`))

	want := []string{
		"spec.replicas",
		"spec.template.spec.containers.image",
		"spec.template.spec.containers.resources.limits.memory",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
	if total != len(want) {
		t.Errorf("total is %d, want %d", total, len(want))
	}
}

// A list key is a whole JSON object used as a map key. Left in, it produces a
// "path" like `spec.containers.k:{"name":"app"}.image`, which is worse than
// showing nothing.
func TestListKeysNeverAppearInAPath(t *testing.T) {
	got, _ := flattenFieldsV1(fields(t, `{
		"f:spec": { "f:containers": {
			"k:{\"name\":\"app\"}":  { "f:image": {} },
			"k:{\"name\":\"sidecar\"}": { "f:image": {} }
		} },
		"f:metadata": { "f:finalizers": { "v:\"kubernetes\"": {} } }
	}`))

	for _, path := range got {
		if strings.ContainsAny(path, "{}\"") || strings.Contains(path, "k:") || strings.Contains(path, "v:") {
			t.Errorf("a raw list key reached the output: %q", path)
		}
	}
}

// Several entries of one list collapse onto the same parent path. Left
// duplicated they spend the twelve-line budget repeating themselves and push the
// interesting entry off the panel — on a real coredns Deployment, six of the
// first twelve lines are the same two paths.
func TestRepeatedListEntriesCollapseToOnePath(t *testing.T) {
	got, total := flattenFieldsV1(fields(t, `{
		"f:status": { "f:conditions": {
			"k:{\"type\":\"Available\"}":   { "f:lastTransitionTime": {}, "f:status": {} },
			"k:{\"type\":\"Progressing\"}": { "f:lastTransitionTime": {}, "f:status": {} }
		} }
	}`))

	want := []string{"status.conditions.lastTransitionTime", "status.conditions.status"}
	if !slices.Equal(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
	if total != len(want) {
		t.Errorf("total is %d, want %d — the count must be of distinct paths too, "+
			"or \"and 4 more\" refers to repetitions", total, len(want))
	}
}

// "." means "this node itself" and is a marker, not a field.
func TestTheSelfMarkerIsNotAField(t *testing.T) {
	got, _ := flattenFieldsV1(fields(t, `{"f:metadata": {".": {}, "f:labels": {".": {}}}}`))

	want := []string{"metadata.labels"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A manager owning dozens of fields must not push the interesting entry off the
// page — but the count must survive the trim, or a fraction is shown as if it
// were everything.
func TestALongFieldListIsTrimmedAndSaysHowManyThereWere(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"f:spec": {`)
	const n = 40
	for i := range n {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"f:field`)
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(string(rune('a' + i/26)))
		b.WriteString(`": {}`)
	}
	b.WriteString("}}")

	got, total := flattenFieldsV1(fields(t, b.String()))

	if len(got) != maxFields {
		t.Errorf("kept %d paths, want the cap of %d", len(got), maxFields)
	}
	if total != n {
		t.Errorf("total is %d, want %d — without it the page shows %d paths as though they were all of them",
			total, n, maxFields)
	}
}

func TestMalformedOrAbsentFieldsAreNotACrash(t *testing.T) {
	for _, raw := range []*metav1.FieldsV1{
		nil,
		{},
		{Raw: []byte("")},
		{Raw: []byte("not json at all")},
		{Raw: []byte("[]")},
	} {
		got, total := flattenFieldsV1(raw)
		if len(got) != 0 || total != 0 {
			t.Errorf("%v produced %v (total %d)", raw, got, total)
		}
	}
}

func TestEntriesComeBackNewestFirst(t *testing.T) {
	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	object := &unstructured.Unstructured{Object: map[string]any{}}
	object.SetManagedFields([]metav1.ManagedFieldsEntry{
		{Manager: "oldest", Time: &metav1.Time{Time: base.Add(-3 * time.Hour)}},
		{Manager: "newest", Time: &metav1.Time{Time: base}},
		{Manager: "middle", Time: &metav1.Time{Time: base.Add(-time.Hour)}},
	})

	got := changesFrom(object, "")

	want := []string{"newest", "middle", "oldest"}
	for i, manager := range want {
		if got[i].Manager != manager {
			t.Fatalf("entry %d is %q, want %q — the question is always what happened most recently",
				i, got[i].Manager, manager)
		}
	}
}

// A controller rewriting status on every reconcile is noise on every object in
// the cluster; it must be distinguishable from someone changing the spec.
func TestAStatusWriteIsMarkedAsOne(t *testing.T) {
	object := &unstructured.Unstructured{Object: map[string]any{}}
	object.SetManagedFields([]metav1.ManagedFieldsEntry{
		{Manager: "kube-controller-manager", Operation: "Update", Subresource: "status"},
		{Manager: "kubectl-edit", Operation: "Update"},
	})

	got := changesFrom(object, "")

	var status, spec Change
	for _, c := range got {
		switch c.Manager {
		case "kube-controller-manager":
			status = c
		case "kubectl-edit":
			spec = c
		}
	}
	if !status.StatusOnly() {
		t.Errorf("a status write reported operation %q and was not recognized", status.Operation)
	}
	if spec.StatusOnly() {
		t.Errorf("a spec write was reported as a status write: %q", spec.Operation)
	}
}

// The single most useful bit on the panel: a person did this, or a controller
// did. Everything in a cluster is written by controllers constantly.
func TestAPersonIsToldApartFromAController(t *testing.T) {
	people := []string{
		"kubectl-client-side-apply", "kubectl-edit", "helm", "argocd-controller",
		"flux", "terraform-provider-kubernetes", "kustomize-controller", "oc-edit",
	}
	for _, manager := range people {
		if !(Change{Manager: manager}).Human() {
			t.Errorf("%q was not recognized as a person or a deployment tool", manager)
		}
	}

	controllers := []string{
		"kube-controller-manager", "kube-scheduler", "cloud-controller-manager",
		"metrics-server", "cluster-autoscaler",
	}
	for _, manager := range controllers {
		if (Change{Manager: manager}).Human() {
			t.Errorf("%q was reported as a person; it writes on every reconcile and would drown the panel", manager)
		}
	}
}

// "Nothing changed" and "this object records nothing" look identical on a page
// and mean opposite things.
func TestAnEmptyReportExplainsItself(t *testing.T) {
	if note := noteFor(Report{}); note == "" {
		t.Error("an empty report carries no note; the reader cannot tell a quiet object " +
			"from one that records no history at all")
	}
	if note := noteFor(Report{Changes: []Change{{Manager: "kubectl"}}}); note != "" {
		t.Errorf("a report with changes carries the note anyway: %q", note)
	}
}

func TestImagesAreReadFromThePodTemplate(t *testing.T) {
	rs := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{
				map[string]any{"name": "app", "image": "example/app:1.4.2"},
				map[string]any{"name": "sidecar", "image": "example/proxy:2.0"},
			},
		}}},
	}}

	if got := imagesOf(rs); got != "example/app:1.4.2, example/proxy:2.0" {
		t.Errorf("got %q", got)
	}
	if got := imagesOf(&unstructured.Unstructured{Object: map[string]any{}}); got != "" {
		t.Errorf("an object with no template gave %q", got)
	}
}

// A ReplicaSet can match a Deployment's selector without belonging to it —
// another Deployment with overlapping labels, or one left by hand. Showing it as
// a revision invents history.
func TestOnlyTheDeploymentsOwnReplicaSetsCount(t *testing.T) {
	owned := &unstructured.Unstructured{Object: map[string]any{}}
	owned.SetOwnerReferences([]metav1.OwnerReference{{Kind: "Deployment", Name: "api"}})

	other := &unstructured.Unstructured{Object: map[string]any{}}
	other.SetOwnerReferences([]metav1.OwnerReference{{Kind: "Deployment", Name: "worker"}})

	orphan := &unstructured.Unstructured{Object: map[string]any{}}

	if !ownedBy(owned, "api") {
		t.Error("the deployment's own ReplicaSet was rejected")
	}
	if ownedBy(other, "api") {
		t.Error("another deployment's ReplicaSet was counted as this one's history")
	}
	if ownedBy(orphan, "api") {
		t.Error("a ReplicaSet with no owner was counted")
	}
}

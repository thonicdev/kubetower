package detail

// A Namespace's quotas and limit ranges: rows with their
// figures where there are some, nothing where there are none, and a sentence
// where the list was refused - three answers, never two drawn the same.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

func quota(namespace, name string, hard, used corev1.ResourceList) *corev1.ResourceQuota {
	return &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       corev1.ResourceQuotaSpec{Hard: hard},
		Status:     corev1.ResourceQuotaStatus{Hard: hard, Used: used},
	}
}

func list(pairs ...string) corev1.ResourceList {
	out := corev1.ResourceList{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out[corev1.ResourceName(pairs[i])] = resource.MustParse(pairs[i+1])
	}
	return out
}

// The pair a kind cluster holds after `kubectl create quota compute
// --hard=pods=10,requests.cpu=1` and a LimitRange defaulting a container's CPU.
func boundedShop() []runtime.Object {
	return []runtime.Object{
		quota("shop", "compute", list("pods", "10", "requests.cpu", "1"), list("pods", "2", "requests.cpu", "250m")),
		&corev1.LimitRange{
			ObjectMeta: metav1.ObjectMeta{Name: "defaults", Namespace: "shop"},
			Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
				Type:           corev1.LimitTypeContainer,
				Max:            list("cpu", "2"),
				Default:        list("cpu", "500m"),
				DefaultRequest: list("cpu", "100m"),
			}}},
		},
		// A neighbor's, which the shop's page must not draw.
		quota("other", "elsewhere", list("pods", "5"), list("pods", "1")),
	}
}

func theNamespace(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": map[string]any{"name": name, "uid": "uid-" + name},
	}}
}

func noKinds(string, string) (string, bool, bool) { return "", false, false }

// openNamespace runs the relational pass a Namespace's page runs.
func openNamespace(t *testing.T, reader *Reader, clients *fake.Clientset, name string) Object {
	t.Helper()
	object := Object{Kind: resNamespaces, Name: name}
	reader.enrich(context.Background(), &cluster.Client{Context: "kind", Clients: clients}, &object, theNamespace(name), noKinds)
	return object
}

func rowsOf(relations []Relation, role string) []Relation {
	var out []Relation
	for _, rel := range relations {
		if rel.Role == role {
			out = append(out, rel)
		}
	}
	return out
}

func TestANamespaceDrawsItsQuotaUsedAgainstHardAndItsLimitRange(t *testing.T) {
	reviews := &accesstest.Reviews{}
	object := openNamespace(t, &Reader{gate: reviews.Gate()}, fake.NewClientset(boundedShop()...), "shop")

	q := find(t, object.Relations, RoleResourceQuota, "compute")
	if !q.Linkable() || q.Kind != "resourcequotas" || q.Namespace != "shop" || q.Name != "compute" {
		t.Errorf("the quota row is %+v, want a link to resourcequotas/compute in shop", q)
	}
	// Fullest first: requests.cpu is at 25%, pods at 20%.
	if q.Detail != "requests.cpu 250m/1, pods 2/10" {
		t.Errorf("the quota reads %q, want its used against hard, fullest first", q.Detail)
	}

	l := find(t, object.Relations, RoleLimitRange, "defaults")
	if !l.Linkable() || l.Kind != "limitranges" || l.Namespace != "shop" {
		t.Errorf("the limit range row is %+v, want a link to limitranges/defaults in shop", l)
	}
	if l.Detail != "Container/cpu max=2 req=100m def=500m" {
		t.Errorf("the limit range reads %q", l.Detail)
	}

	absent(t, object.Relations, RoleResourceQuota, "elsewhere")
	if object.RelationsNote != "" {
		t.Errorf("relationsNote = %q on a namespace whose lists both answered", object.RelationsNote)
	}

	// Each list is reviewed as a list in this namespace, for the person: a
	// review of the kind across the cluster would answer a different question.
	var asked []string
	for _, q := range reviews.Asked() {
		a := q.Attributes
		asked = append(asked, a.Verb+" "+a.Resource+" in "+a.Namespace)
	}
	if got := strings.Join(asked, "; "); got != "list resourcequotas in shop; list limitranges in shop" {
		t.Errorf("reviews asked: %s", got)
	}
}

// A namespace with none: no row and no sentence. The ordinary case is not a
// finding.
func TestANamespaceWithNoQuotaAndNoLimitRangeSaysNothing(t *testing.T) {
	object := openNamespace(t, &Reader{gate: accesstest.Allow()}, fake.NewClientset(boundedShop()...), "quiet")
	if rows := append(rowsOf(object.Relations, RoleResourceQuota), rowsOf(object.Relations, RoleLimitRange)...); len(rows) != 0 {
		t.Errorf("drew %s on a namespace that holds none", describe(rows))
	}
	if object.RelationsNote != "" {
		t.Errorf("relationsNote = %q, want nothing on an empty answer", object.RelationsNote)
	}
}

// Refused by the access review: no rows, and a sentence saying what could not
// be listed and why - never the silence of an empty answer.
func TestARefusedReviewIsSaidAndNotDrawnAsNone(t *testing.T) {
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		r := q.Attributes.Resource
		return r != "resourcequotas" && r != "limitranges", nil
	}}
	object := openNamespace(t, &Reader{gate: reviews.Gate()}, fake.NewClientset(boundedShop()...), "shop")

	if rows := append(rowsOf(object.Relations, RoleResourceQuota), rowsOf(object.Relations, RoleLimitRange)...); len(rows) != 0 {
		t.Errorf("drew %s from lists that were refused", describe(rows))
	}
	// No person on this context, so the review is the console's own; the
	// sentence is the refusal's, verbatim.
	note := object.RelationsNote
	if !strings.HasPrefix(note, "ResourceQuotas and LimitRanges in this namespace could not be listed: the cluster says ") ||
		!strings.HasSuffix(note, "may not list resourcequotas in namespace shop.") {
		t.Errorf("relationsNote = %q, want both lists named and the refusal said", note)
	}
}

// Refused by the API server on one list only: the other's rows are drawn and
// the note names the one that is missing.
func TestOneListRefusedByTheClusterKeepsTheOther(t *testing.T) {
	clients := fake.NewClientset(boundedShop()...)
	clients.PrependReactor("list", "limitranges", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "limitranges"}, "",
			errors.New(`User "reader" cannot list resource "limitranges" in API group "" in the namespace "shop"`))
	})
	object := openNamespace(t, &Reader{gate: accesstest.Allow()}, clients, "shop")

	find(t, object.Relations, RoleResourceQuota, "compute")
	if rows := rowsOf(object.Relations, RoleLimitRange); len(rows) != 0 {
		t.Errorf("drew %s from a refused list", describe(rows))
	}
	if !strings.HasPrefix(object.RelationsNote, "LimitRanges in this namespace could not be listed: limitranges is forbidden") {
		t.Errorf("relationsNote = %q", object.RelationsNote)
	}
}

// A quota the controller has not accounted for yet reads `?` for used rather
// than nothing, and one with no hard limit says so.
func TestAQuotaNotYetAccountedReadsUnknownUsed(t *testing.T) {
	fresh := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "fresh", Namespace: "shop"},
		Spec:       corev1.ResourceQuotaSpec{Hard: list("pods", "10")},
	}
	empty := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "empty", Namespace: "shop"}}
	object := openNamespace(t, &Reader{gate: accesstest.Allow()}, fake.NewClientset(fresh, empty), "shop")

	if got := find(t, object.Relations, RoleResourceQuota, "fresh").Detail; got != "pods ?/10" {
		t.Errorf("an unaccounted quota reads %q, want pods ?/10", got)
	}
	if got := find(t, object.Relations, RoleResourceQuota, "empty").Detail; got != "no hard limits" {
		t.Errorf("a quota with no hard limit reads %q", got)
	}
}

// Past the cap the panel says it was cut.
func TestMoreQuotasThanTheCapAreSaid(t *testing.T) {
	var seed []runtime.Object
	for i := range maxNamespaceBounds + 1 {
		seed = append(seed, quota("shop", fmt.Sprintf("q%02d", i), list("pods", "1"), list("pods", "0")))
	}
	object := openNamespace(t, &Reader{gate: accesstest.Allow()}, fake.NewClientset(seed...), "shop")

	if rows := rowsOf(object.Relations, RoleResourceQuota); len(rows) != maxNamespaceBounds {
		t.Errorf("drew %d quota rows, want the cap, %d", len(rows), maxNamespaceBounds)
	}
	if !strings.Contains(object.RelationsNote, fmt.Sprintf("more than %d ResourceQuotas", maxNamespaceBounds)) {
		t.Errorf("relationsNote = %q, want the cut said", object.RelationsNote)
	}
}

// Only a Namespace lists them: a ConfigMap's page asks nothing of the kind.
func TestOnlyANamespaceListsQuotas(t *testing.T) {
	reviews := &accesstest.Reviews{}
	clients := fake.NewClientset(boundedShop()...)
	object := Object{Kind: resServiceAccounts, Name: "shop", Namespace: "shop"}
	raw := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ServiceAccount",
		"metadata": map[string]any{"name": "shop", "namespace": "shop"},
	}}
	(&Reader{gate: reviews.Gate()}).enrich(context.Background(),
		&cluster.Client{Context: "kind", Clients: clients}, &object, raw, noKinds)
	for _, q := range reviews.Asked() {
		if r := q.Attributes.Resource; r == "resourcequotas" || r == "limitranges" {
			t.Errorf("a ServiceAccount's page asked about %s", r)
		}
	}
}

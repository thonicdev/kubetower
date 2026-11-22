package web

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/detail"
)

// A namespace can be deleted from the console: the
// write path can delete a cluster-scoped object, so `verbsFor`
// must offer the verb on the page, the drawer, the row menu and the
// selection bar. These walk the whole JSON mapping - the verb
// offered, the plan, the act - against a fake cluster.

func fakeNamespace(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": name, "uid": "uid-" + name, "resourceVersion": "12"},
		"spec":       map[string]any{"finalizers": []any{"kubernetes"}},
		"status":     map[string]any{"phase": "Active"},
	}}
}

// deleteNamespace previews and acts as internal/api does: the plan's
// precondition handed back to the act, the acknowledgement as the confirmation.
func deleteNamespace(t *testing.T, h *actsHarness, name string) (api.ActOutcome, error) {
	t.Helper()
	ctx := context.Background()
	request := api.ActionRequest{
		Verb:   string(actions.Delete),
		Target: api.PlanTarget{Kind: "namespaces", Name: name},
	}
	facts, err := h.acts.PreviewAction(ctx, "prod", request)
	if err != nil {
		t.Fatalf("previewing the delete of namespace %s: %v", name, err)
	}
	confirmation := facts.Confirmation
	if confirmation.Kind != "acknowledge" || confirmation.Expected != actions.ConfirmToken {
		t.Errorf("a namespace's delete is confirmed by %+v; want the button every single delete has",
			facts.Confirmation)
	}
	if !facts.Destructive {
		t.Error("the plan to delete a namespace is not marked destructive")
	}
	request.Precondition = facts.Precondition
	request.PreconditionUID = facts.PreconditionUID
	return h.acts.RunAction(ctx, "prod", request, actions.ConfirmToken)
}

// The page offers Delete on a namespace, and the selection bar does too, both
// read from the one table.
func TestANamespaceOffersDeleteOnItsPageAndInItsList(t *testing.T) {
	offered := objectActions("namespaces", detail.Object{Kind: "namespaces"}, true, "")
	if len(offered) != 1 || offered[0].Verb != string(actions.Delete) || !offered[0].Permitted {
		t.Fatalf("a namespace's page offers %+v, want one permitted Delete", offered)
	}
	if offered[0].Irreversible == nil || !*offered[0].Irreversible {
		t.Error("Delete on a namespace is not marked irreversible, " +
			"so the menu draws it like an ordinary verb")
	}
	bulk := bulkAffordance("prod", "namespaces", false)
	if bulk == nil || len(bulk.Verbs) != 1 || bulk.Verbs[0].Verb != string(actions.Delete) {
		t.Fatalf("the namespaces list offers %+v in bulk, want Delete", bulk)
	}
}

// The delete reaches the API server: one DELETE on the cluster-scoped
// namespace, with no namespace of its own, carrying the planned UID.
func TestANamespaceDeleteReachesTheAPIServer(t *testing.T) {
	h := newActsHarness(t, fakeNamespace("kubeadjust"))
	outcome, err := deleteNamespace(t, h, "kubeadjust")
	if err != nil {
		t.Fatalf("deleting namespace kubeadjust: %v", err)
	}
	if outcome.Succeeded != 1 || outcome.Failed != 0 {
		t.Fatalf("outcome %+v, want one success", outcome)
	}

	var deletes []k8stesting.DeleteActionImpl
	for _, action := range h.dyn.Actions() {
		if one, ok := action.(k8stesting.DeleteActionImpl); ok {
			deletes = append(deletes, one)
		}
	}
	if len(deletes) != 1 {
		t.Fatalf("%d delete request(s) reached the cluster, want 1", len(deletes))
	}
	sent := deletes[0]
	if sent.GetResource() != fakeNamespaceGVR || sent.GetNamespace() != "" ||
		sent.GetName() != "kubeadjust" {
		t.Errorf("the delete went to %s %q/%q, want namespaces \"\"/kubeadjust",
			sent.GetResource(), sent.GetNamespace(), sent.GetName())
	}
	if sent.DeleteOptions.Preconditions == nil || sent.DeleteOptions.Preconditions.UID == nil ||
		*sent.DeleteOptions.Preconditions.UID != "uid-kubeadjust" {
		t.Errorf("the delete carries preconditions %+v, want the planned UID",
			sent.DeleteOptions.Preconditions)
	}
	namespaces := h.dyn.Resource(fakeNamespaceGVR)
	if _, err := namespaces.Get(context.Background(), "kubeadjust", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("the namespace is still there after its delete: %v", err)
	}
}

// A refusal is the API server's, said back in its own words.
func TestANamespaceDeleteRefusalSaysTheAPIServersReason(t *testing.T) {
	h := newActsHarness(t, fakeNamespace("kube-system"))
	const why = "namespace kube-system is protected by an admission policy"
	h.dyn.PrependReactor("delete", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "kube-system",
			errorString(why))
		return true, nil, forbidden
	})
	_, err := deleteNamespace(t, h, "kube-system")
	if err == nil {
		t.Fatal("the API server refused the delete and the act reported no error")
	}
	if !strings.Contains(err.Error(), why) {
		t.Errorf("the refusal reads %q; the API server's reason %q is not in it", err, why)
	}
	namespaces := h.dyn.Resource(fakeNamespaceGVR)
	if _, err := namespaces.Get(context.Background(), "kube-system", metav1.GetOptions{}); err != nil {
		t.Errorf("the refused namespace is gone: %v", err)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

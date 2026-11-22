package web

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/detail"
)

// Under `--incluster` the namespace the console runs in offers no Delete:
// deleting it
// takes the console's own Deployment, Service and state volume with it. Every
// other namespace is unchanged, and outside the server profile nothing is.

const ownNamespaceForTest = "kubetower"

// inClusterServer is a Server as New builds it under `--incluster`, with the
// pod's namespace file read as ownNamespaceForTest.
func inClusterServer() *Server {
	return &Server{inCluster: true, ownNamespace: func() string { return ownNamespaceForTest }}
}

func namespaceRef(name string) api.ObjectRef {
	return api.ObjectRef{Cluster: "in-cluster", Kind: "namespaces", Name: name}
}

func activeNamespace(name string) detail.Object {
	return detail.Object{
		Kind:  "namespaces",
		Facts: []detail.Fact{{Label: "Name", Value: name}, {Label: "Phase", Value: "Active"}},
	}
}

// The page, the drawer and the row menu all draw DetailPage.actions, so this
// is the one place the three are decided.
func TestTheConsolesOwnNamespaceOffersNoDeleteInCluster(t *testing.T) {
	s := inClusterServer()

	for _, kind := range []string{"namespaces", "namespace", "Namespace"} {
		ref := namespaceRef(ownNamespaceForTest)
		ref.Kind = kind
		if got := s.offeredActions(ref, activeNamespace(ownNamespaceForTest), true, ""); len(got) != 0 {
			t.Errorf("kind %q: the console's own namespace was offered %v, want nothing", kind, verbNames(got))
		}
	}

	other := s.offeredActions(namespaceRef("shop"), activeNamespace("shop"), true, "")
	if strings.Join(verbNames(other), ",") != "delete" {
		t.Errorf("another namespace was offered %v, want delete", verbNames(other))
	}

	// Only a namespace: a pod that happens to share the name keeps its verbs.
	pod := detail.Object{Kind: "pods"}
	podRef := api.ObjectRef{Cluster: "in-cluster", Kind: "pods", Namespace: ownNamespaceForTest, Name: ownNamespaceForTest}
	if got := verbNames(s.offeredActions(podRef, pod, true, "")); !strings.Contains(strings.Join(got, ","), "delete") {
		t.Errorf("a pod in the console's namespace lost Delete: %v", got)
	}
}

// Outside the server profile there is no namespace of the console's own, so
// nothing is withheld - even where a namespace file happens to be readable,
// as for the desktop profile run in a pod.
func TestOutsideInClusterEveryNamespaceOffersDelete(t *testing.T) {
	if ownNamespaceOf(false) != nil {
		t.Fatal("outside --incluster the console reads a namespace of its own")
	}
	if ownNamespaceOf(true) == nil {
		t.Fatal("under --incluster the console reads no namespace of its own")
	}
	s := &Server{ownNamespace: ownNamespaceOf(false)}
	got := s.offeredActions(namespaceRef(ownNamespaceForTest), activeNamespace(ownNamespaceForTest), true, "")
	if strings.Join(verbNames(got), ",") != "delete" {
		t.Errorf("outside --incluster the namespace was offered %v, want delete", verbNames(got))
	}
}

// A namespace file that cannot be read names no namespace, so it withholds
// nothing rather than every namespace.
func TestAnUnreadableOwnNamespaceWithholdsNothing(t *testing.T) {
	s := &Server{inCluster: true, ownNamespace: func() string { return "" }}
	got := s.offeredActions(namespaceRef("shop"), activeNamespace("shop"), true, "")
	if strings.Join(verbNames(got), ",") != "delete" {
		t.Errorf("with no namespace file, a namespace was offered %v, want delete", verbNames(got))
	}
}

// deletes is how many DELETE requests reached the fake cluster.
func (h *actsHarness) deletes() int {
	count := 0
	for _, action := range h.dyn.Actions() {
		if _, ok := action.(k8stesting.DeleteActionImpl); ok {
			count++
		}
	}
	return count
}

func (h *actsHarness) namespaceExists(t *testing.T, name string) bool {
	t.Helper()
	_, err := h.dyn.Resource(fakeNamespaceGVR).Get(context.Background(), name, metav1.GetOptions{})
	return err == nil
}

func inClusterActs(t *testing.T) *actsHarness {
	t.Helper()
	h := newActsHarness(t, fakeNamespace(ownNamespaceForTest), fakeNamespace("shop"))
	h.acts.s.inCluster = true
	h.acts.s.ownNamespace = func() string { return ownNamespaceForTest }
	return h
}

// The courtesy above is not the control: a request made by hand is refused
// by the plan and by the act, and nothing reaches the cluster.
func TestTheConsolesOwnNamespaceDeleteIsRefusedInCluster(t *testing.T) {
	h := inClusterActs(t)
	ctx := context.Background()
	request := api.ActionRequest{
		Verb:   string(actions.Delete),
		Target: api.PlanTarget{Kind: "namespaces", Name: ownNamespaceForTest},
	}

	_, err := h.acts.PreviewAction(ctx, "in-cluster", request)
	if !errors.Is(err, api.ErrNotInProfile) {
		t.Errorf("the plan to delete the console's own namespace answered %v, want a refusal", err)
	}
	if err != nil && !strings.Contains(err.Error(), ownNamespaceForTest) {
		t.Errorf("the refusal %q does not name the namespace", err)
	}

	request.Force = true
	if _, err := h.acts.RunAction(ctx, "in-cluster", request, actions.ConfirmToken); !errors.Is(err, api.ErrNotInProfile) {
		t.Errorf("the act deleting the console's own namespace answered %v, want a refusal", err)
	}

	bulk := api.BulkRequest{
		Verb: string(actions.Delete), Kind: "namespaces",
		Targets: []api.PlanTarget{{Name: "shop"}, {Name: ownNamespaceForTest}},
	}
	if _, err := h.acts.PreviewBulk(ctx, "in-cluster", bulk); !errors.Is(err, api.ErrNotInProfile) {
		t.Errorf("a bulk plan holding the console's own namespace answered %v, want a refusal", err)
	}
	if _, err := h.acts.RunBulk(ctx, "in-cluster", bulk); !errors.Is(err, api.ErrNotInProfile) {
		t.Errorf("a bulk act holding the console's own namespace answered %v, want a refusal", err)
	}

	if n := h.deletes(); n != 0 {
		t.Errorf("%d delete request(s) reached the cluster, want none", n)
	}
	if !h.namespaceExists(t, ownNamespaceForTest) || !h.namespaceExists(t, "shop") {
		t.Error("a namespace is gone after every request was refused")
	}
}

// Every other namespace is deleted, singly and in a set.
func TestAnotherNamespaceIsStillDeletedInCluster(t *testing.T) {
	h := inClusterActs(t)
	if _, err := deleteNamespace(t, h, "shop"); err != nil {
		t.Fatalf("deleting namespace shop: %v", err)
	}
	if h.namespaceExists(t, "shop") {
		t.Error("namespace shop is still there after its delete")
	}

	h = newActsHarness(t, fakeNamespace("shop"), fakeNamespace("lab"), fakeNamespace(ownNamespaceForTest))
	h.acts.s.inCluster = true
	h.acts.s.ownNamespace = func() string { return ownNamespaceForTest }
	bulk := api.BulkRequest{
		Verb: string(actions.Delete), Kind: "namespaces",
		Targets: []api.PlanTarget{{Name: "shop"}, {Name: "lab"}},
	}
	ctx := context.Background()
	if _, err := h.acts.PreviewBulk(ctx, "in-cluster", bulk); err != nil {
		t.Fatalf("a bulk plan of two other namespaces: %v", err)
	}
	outcome, err := h.acts.RunBulk(ctx, "in-cluster", bulk)
	if err != nil || outcome.Succeeded != 2 {
		t.Fatalf("a bulk delete of two other namespaces: %+v, %v", outcome, err)
	}
	if !h.namespaceExists(t, ownNamespaceForTest) {
		t.Error("the console's own namespace is gone after a delete of two others")
	}
}

// Outside the server profile the same request deletes the namespace: nothing
// changes for the desktop.
func TestOutsideInClusterTheNamespaceIsDeleted(t *testing.T) {
	h := newActsHarness(t, fakeNamespace(ownNamespaceForTest))
	h.acts.s.ownNamespace = ownNamespaceOf(false)
	if _, err := deleteNamespace(t, h, ownNamespaceForTest); err != nil {
		t.Fatalf("deleting namespace %s outside --incluster: %v", ownNamespaceForTest, err)
	}
	if h.namespaceExists(t, ownNamespaceForTest) {
		t.Errorf("namespace %s is still there after its delete", ownNamespaceForTest)
	}
}

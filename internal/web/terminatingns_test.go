package web

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/detail"
	"github.com/thonicdev/kubetower/internal/resources"
)

// A namespace already Terminating offers no Delete. The
// page, the drawer and the row menu all draw DetailPage.actions, which is what
// objectActions builds, so this is the one place the three are decided.
func TestATerminatingNamespaceOffersNoDelete(t *testing.T) {
	phase := func(value string) detail.Object {
		return detail.Object{
			Kind:  "namespaces",
			Facts: []detail.Fact{{Label: "Name", Value: "shop"}, {Label: "Phase", Value: value}},
		}
	}

	active := verbNames(objectActions("namespaces", phase("Active"), true, ""))
	if strings.Join(active, ",") != "delete" {
		t.Errorf("an Active namespace was offered %v, want delete", active)
	}
	if got := objectActions("namespaces", phase("Terminating"), true, ""); len(got) != 0 {
		t.Errorf("a Terminating namespace was offered %v, want nothing", verbNames(got))
	}

	// Only a namespace: a Terminating pod keeps its verbs.
	pod := detail.Object{Kind: "pods", Facts: []detail.Fact{{Label: "Phase", Value: "Terminating"}}}
	if got := verbNames(objectActions("pods", pod, true, "")); !strings.Contains(strings.Join(got, ","), "delete") {
		t.Errorf("a pod lost Delete: %v", got)
	}
}

// The selection bar reads a namespace's phase from the row's Status cell, under
// the key the server gives that column. This builds the list from a real
// Namespace object and pins the key and the text, so the UI's fixture and the
// server agree: columnKey("Status", 1) is "status-1".
func TestTheNamespacesListCarriesThePhaseUnderTheStatusColumnKey(t *testing.T) {
	client := &cluster.Client{Context: "prod", Clients: fake.NewSimpleClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "shop"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating},
		},
	)}
	table, err := resources.List(context.Background(), accesstest.Allow(), client,
		resources.Query{Kind: "namespaces"}, nil)
	if err != nil {
		t.Fatalf("the Namespaces list: %v", err)
	}
	page := apiReads{listPageFixture(t)}.pageOf(context.Background(),
		api.ListQuery{Cluster: "prod", Kind: "namespaces"},
		listKind{slug: "namespaces", title: "Namespaces"}, table)

	var statusKey string
	for _, column := range page.Columns {
		if column.Label == "Status" {
			statusKey = column.Key
		}
	}
	if statusKey != "status-1" {
		t.Fatalf("the Status column's key is %q, want status-1", statusKey)
	}
	if len(page.Rows) != 1 {
		t.Fatalf("%d rows, want the one namespace", len(page.Rows))
	}
	if got := page.Rows[0].Cells[statusKey].Text; got != "Terminating" {
		t.Errorf("the Status cell of a Terminating namespace reads %q", got)
	}
}

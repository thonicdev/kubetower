package resources

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

// Every Filterable column is one the filter can honor, on every kind.
//
// The flag is the server's statement that a column holds a category worth
// choosing among, and Matcher refuses two kinds of column whatever a descriptor
// says: a figure, whose text changes every refresh, and the namespace, which
// has a control of its own. A descriptor flagging either would draw a menu that
// filters nothing. And the parameter names a column by its title, so two
// Filterable columns with one title on one kind could not be told apart.
func TestEveryFilterableColumnIsACategoryWithATitleOfItsOwn(t *testing.T) {
	flagged := 0
	for _, d := range descriptors {
		titles := map[string]bool{}
		for _, c := range d.Columns {
			if !c.Filterable {
				continue
			}
			flagged++
			if c.Numeric {
				t.Errorf("%s: %q is Filterable and Numeric; a figure's text is never filtered", d.Kind, c.Title)
			}
			if strings.EqualFold(c.Title, "Namespace") {
				t.Errorf("%s: the namespace is Filterable; it has a control of its own", d.Kind)
			}
			if titles[strings.ToLower(c.Title)] {
				t.Errorf("%s: two Filterable columns are titled %q, and `filter=%s:x` cannot say which", d.Kind, c.Title, c.Title)
			}
			titles[strings.ToLower(c.Title)] = true
		}
	}
	if flagged == 0 {
		t.Fatal("no descriptor flags a Filterable column; the filter control would offer nothing anywhere")
	}

	// The three Filterable columns of the pods list.
	want := []string{"Status", "Node", "QoS"}
	for _, title := range want {
		found := false
		for _, c := range ColumnsOf("pods") {
			if c.Title == title {
				found = c.Filterable
			}
		}
		if !found {
			t.Errorf("pods: %q is not Filterable", title)
		}
	}
}

// Controlled by is a column and a relation, and not a filter: one value per
// owner makes the control's menu too long to read. The column stays on the
// three kinds that carry it; it is simply not Filterable.
func TestControlledByIsAColumnAndNotAFilter(t *testing.T) {
	for _, kind := range []string{"pods", "replicasets", "jobs"} {
		present := false
		for _, c := range ColumnsOf(kind) {
			if c.Title != "Controlled by" {
				continue
			}
			present = true
			if c.Filterable {
				t.Errorf("%s: Controlled by is Filterable; the filter control offers Status, Node and QoS", kind)
			}
		}
		if !present {
			t.Errorf("%s: the Controlled by column is gone; only its filter was to go", kind)
		}
	}
}

// Through List, against a cluster: the filter narrows the rows, the table
// echoes what it applied, and it offers each Filterable column's values.
func TestListNarrowsByTheFilterAndOffersTheValues(t *testing.T) {
	onNode := func(name, node string) *corev1.Pod {
		p := pod(name, corev1.PodRunning, container("app", waiting(""), true))
		p.Spec.NodeName = node
		return p
	}
	c := client(
		onNode("web-a", "ip-10-0-1-7"),
		onNode("web-b", "ip-10-0-1-70"),
		onNode("web-c", "ip-10-0-1-8"),
	)

	table, err := List(context.Background(), accesstest.Allow(), c, Query{
		Kind:    "pods",
		Filters: []Filter{{Column: "node", Value: "IP-10-0-1-7"}, {Column: "Color", Value: "blue"}},
	}, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(table.Rows) != 1 || table.Rows[0].Name != "web-a" {
		names := make([]string, 0, len(table.Rows))
		for _, row := range table.Rows {
			names = append(names, row.Name)
		}
		t.Fatalf("Node:ip-10-0-1-7 listed %v, want [web-a] - not the pod on ip-10-0-1-70", names)
	}

	node := columnOf(t, table, "Node")
	if len(table.Filters) != 1 || table.Filters[0] != (Applied{Column: node, Value: "IP-10-0-1-7"}) {
		t.Errorf("the table echoed %+v, want the Node filter alone - `Colour` is no column", table.Filters)
	}
	// The Node menu still offers all three nodes: its own filter does not
	// narrow it.
	if got := strings.Join(table.Offered[node], ","); got != "ip-10-0-1-7,ip-10-0-1-70,ip-10-0-1-8" {
		t.Errorf("Node offered %q, want all three nodes", got)
	}
	if _, ok := table.Offered[columnOf(t, table, "CPU")]; ok {
		t.Error("CPU, a figure, was offered values")
	}

	// And the old address's text is a plain search: `node:ip-10-0-1-7` is
	// in no cell, so nothing is listed - which is why the client rewrites it.
	searched, err := listing(c, "pods", "", "node:ip-10-0-1-7")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(searched.Rows) != 0 || len(searched.Filters) != 0 {
		t.Errorf("search=node:ip-10-0-1-7 listed %d rows and applied %+v; the box no longer addresses a column",
			len(searched.Rows), searched.Filters)
	}
}

// Within narrows before matching, so a value held only outside the selection
// is neither listed nor offered.
func TestASelectionOfNamespacesIsNarrowedBeforeTheValuesAreOffered(t *testing.T) {
	in := func(namespace, name string, phase corev1.PodPhase) *corev1.Pod {
		p := pod(name, phase, container("app", waiting(""), true))
		p.Namespace = namespace
		return p
	}
	c := client(
		in("argocd", "repo-server", corev1.PodRunning),
		in("shop-prod", "web", corev1.PodRunning),
		in("shop-prod", "batch", corev1.PodPending),
		in("kube-system", "broken", corev1.PodFailed),
	)

	table, err := List(context.Background(), accesstest.Allow(), c, Query{
		Kind: "pods", Within: []string{"argocd", "shop-prod"},
	}, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	names := make([]string, 0, len(table.Rows))
	for _, row := range table.Rows {
		names = append(names, row.Namespace+"/"+row.Name)
	}
	if strings.Contains(strings.Join(names, ","), "kube-system") || len(names) != 3 {
		t.Errorf("the selection listed %v, want the three pods of argocd and shop-prod", names)
	}
	status := table.Offered[columnOf(t, table, "Status")]
	for _, value := range status {
		if value == "Failed" {
			t.Errorf("Status offered %v: Failed is kube-system's alone, and kube-system is not selected", status)
		}
	}
	if len(status) != 2 {
		t.Errorf("Status offered %v, want the two values the selected namespaces hold", status)
	}

	// No selection is every namespace, Failed included.
	whole, err := List(context.Background(), accesstest.Allow(), c, Query{Kind: "pods"}, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := len(whole.Offered[columnOf(t, whole, "Status")]); got != 3 {
		t.Errorf("every namespace offered %d statuses, want 3", got)
	}
}

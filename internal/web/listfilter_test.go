package web

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/resources"
)

// The filter control's half of the list page: the flag and the values travel on
// the column, and the filters that took effect are echoed by column key.
//
// internal/api's member test proves the contract types can carry `filterable`,
// `filterValues` and `filters`; this is the other half, that pageOf fills them
// from the table that holds them rather than leaving them at their zero values.
func TestTheListPageCarriesTheFilterFlagValuesAndEcho(t *testing.T) {
	table := resources.Table{
		Kind: "pods", Title: "Pods", Namespaced: true,
		Columns: []resources.Column{
			{Title: "Name"},
			{Title: "Namespace"},
			{Title: "Node", Filterable: true},
			{Title: "Status", Filterable: true},
			{Title: "CPU", Numeric: true},
		},
		Rows: []resources.Row{{
			Name: "web-a", Namespace: "default",
			Cells: []resources.Cell{{Text: "web-a"}, {Text: "default"}, {Text: "worker-2"}, {Text: "Running"}, {Text: "3m"}},
		}},
		Filters: []resources.Applied{{Column: 2, Value: "worker-2"}},
		// Status's rows were all filtered away: an empty menu, not no menu.
		Offered: map[int][]string{2: {"worker-2", "worker-3"}, 3: {}},
	}
	page := apiReads{listPageFixture(t)}.pageOf(context.Background(),
		api.ListQuery{Cluster: "prod", Kind: "pods", Filters: []string{"Node:worker-2"}},
		listKind{slug: "pods", title: "Pods", refresh: time.Minute}, table)

	byLabel := map[string]api.Column{}
	for _, column := range page.Columns {
		byLabel[column.Label] = column
	}
	if !byLabel["Node"].Filterable || !byLabel["Status"].Filterable {
		t.Error("a Filterable descriptor column arrived without the flag; the control would offer nothing")
	}
	if byLabel["Name"].Filterable || byLabel["CPU"].Filterable {
		t.Error("a column the descriptor did not flag arrived filterable")
	}
	if values := byLabel["Node"].FilterValues; values == nil || strings.Join(*values, ",") != "worker-2,worker-3" {
		t.Errorf("Node offered %v, want the table's own values", values)
	}

	// On the wire, which is what the client reads: a Filterable
	// column whose rows were all filtered away carries `"filterValues":[]`, and
	// a column that cannot be filtered carries no key at all - not `null`.
	wire, err := json.Marshal(page.Columns)
	if err != nil {
		t.Fatalf("marshalling the columns: %v", err)
	}
	var decoded []map[string]json.RawMessage
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("reading the columns back: %v", err)
	}
	for _, column := range decoded {
		label := strings.Trim(string(column["label"]), `"`)
		values, present := column["filterValues"]
		switch label {
		case "Status":
			if !present || string(values) != "[]" {
				t.Errorf("Status sent filterValues %s (present %v), want [] - present and empty", values, present)
			}
		case "Node":
			if string(values) != `["worker-2","worker-3"]` {
				t.Errorf("Node sent filterValues %s", values)
			}
		default:
			if present {
				t.Errorf("%s, which cannot be filtered, sent filterValues %s; it should be absent", label, values)
			}
		}
	}

	node := byLabel["Node"].Key
	if len(page.Filters) != 1 || page.Filters[0] != (api.ListFilter{Column: node, Value: "worker-2"}) {
		t.Errorf("the page echoed %+v, want the Node filter by its key %q", page.Filters, node)
	}
}

func TestParseFiltersDropsWhatIsNotAFilter(t *testing.T) {
	got := parseFilters([]string{"Node:worker-2", "worker-3", "Status:", "Storage class:gp3"})
	want := []resources.Filter{{Column: "Node", Value: "worker-2"}, {Column: "Storage class", Value: "gp3"}}
	if len(got) != len(want) {
		t.Fatalf("parseFilters kept %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("filter %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A selection of several namespaces offers the values of its own rows, and not
// those of the whole cluster it was read from.
//
// Two namespaces are chosen and a third holds a Status nobody else has. The
// menu must not offer it - the rows are narrowed to the selection before
// the values are collected - since choosing it would list nothing.
func TestASelectionOfNamespacesOffersOnlyTheValuesOfItsOwnRows(t *testing.T) {
	s, _ := readsServer(t)
	carol := person("carol@example.com", "oidc:platform")
	statusOf := func(page api.ListPage) []string {
		for _, column := range page.Columns {
			if column.Label == "Status" {
				if column.FilterValues == nil {
					t.Fatal("Status carried no filterValues at all")
				}
				return *column.FilterValues
			}
		}
		t.Fatal("no Status column on the pods page")
		return nil
	}

	// The whole cluster offers kube-system's Failed: the fixture does hold it.
	whole, fail := apiReads{s}.ListResources(carol, api.ListQuery{Cluster: "prod", Kind: "pods"})
	if fail != nil {
		t.Fatalf("the list failed: %+v", fail)
	}
	if got := strings.Join(statusOf(whole), ","); got != "Failed,Running" {
		t.Fatalf("every namespace offered Status %q, want Failed,Running - the fixture has lost its third namespace", got)
	}

	page, fail := apiReads{s}.ListResources(carol, api.ListQuery{Cluster: "prod", Kind: "pods", Namespace: "shop,billing"})
	if fail != nil {
		t.Fatalf("the list failed: %+v", fail)
	}
	if got := podRows(page); strings.Join(got, ",") != "billing/ledger-0,shop/api-1,shop/api-2" {
		t.Errorf("rows = %v, want the pods of shop and billing", got)
	}
	if got := strings.Join(statusOf(page), ","); got != "Running" {
		t.Errorf("shop and billing offered Status %q, want Running alone - Failed is kube-system's, "+
			"which is not selected, and choosing it would list nothing", got)
	}
}

// The fallback in pageOf, pinned: a Filterable
// column the table has no values for at all - not even an empty list, which is
// what `Offer` hands every Filterable column today - is still sent as `[]`,
// never `null` and never absent. The day a producer forgets a column, the
// control draws an empty menu rather than no menu.
func TestAFilterableColumnWithNoOfferedEntryIsStillSentAsAnEmptyList(t *testing.T) {
	table := resources.Table{
		Kind: "pods", Title: "Pods", Namespaced: true,
		Columns: []resources.Column{{Title: "Name"}, {Title: "QoS", Filterable: true}},
		Rows:    []resources.Row{{Name: "a", Namespace: "default", Cells: []resources.Cell{{Text: "a"}, {Text: "Burstable"}}}},
		Offered: map[int][]string{},
	}
	page := apiReads{listPageFixture(t)}.pageOf(context.Background(),
		api.ListQuery{Cluster: "prod", Kind: "pods"},
		listKind{slug: "pods", title: "Pods", refresh: time.Minute}, table)
	wire, err := json.Marshal(page.Columns)
	if err != nil {
		t.Fatalf("marshalling the columns: %v", err)
	}
	if !strings.Contains(string(wire), `"label":"QoS"`) || !strings.Contains(string(wire), `"filterValues":[]`) {
		t.Errorf("a Filterable column with no offered entry was sent as %s; want \"filterValues\":[]", wire)
	}
}

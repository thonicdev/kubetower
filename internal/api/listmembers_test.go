package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An absent optional member conforms, so this test is about the member set.
//
// `TestRealResponsesMatchTheSchemasInTheContract` is the strongest test in this
// package and it cannot see this defect at all. Every one of `Column`'s
// behavioral flags carries a `default` in `api/openapi.yaml`, which makes it
// **optional on the wire**; `Cell.gauge`, `Cell.copy`, `Cell.hint` and
// `ListPage.refreshSeconds` are simply not required. So a response carrying
// seven members out of thirteen validates, and validates again when the contract
// grows an eighth that nothing fills. The members `internal/api` must carry:
//
//	Column      secondary gauged copyable prose role lockedNote
//	Cell        gauge copy hint rank parts
//	ListPage    refreshSeconds unknownNamespace readOnlyNote namespaces search
//	            selector count columnsCustomised
//	BulkAffordance  scope
//
// It matters for the client: `openapi-typescript` emits a member with a
// `default` as **required**, so the client's own types promise a value the
// server must send. A missing `column.gauged` is `undefined`, `ROLE[column.role]`
// is `undefined`, and every branch reading one is dead at run time: no usage bar
// drawn, no cell that can be copied, the namespace cell not a control, a name
// dragged elsewhere not styled as a name, a column menu that cannot tell "off by
// default" from "you hid this", and a selection's scope composed by the client
// from the route, which the contract names as the wrong answer in as many words.
//
// # So this test is about the member set, not about the values
//
// Both directions, because the two failures are different and only one of them
// is caught by anything else:
//
//   - a member the contract declares and the response never carries — those
//     above, and the next one somebody adds to the YAML;
//   - a member the response carries and the contract does not declare — how
//     a removed `Cluster.group` could survive.
//
// The expectation is **read out of `api/openapi.yaml` at run time**, the way
// `conformance_test.go` and `clustermembers_test.go` do. A test that listed the
// names it had just seen in `types.go` would agree with the struct by
// construction and fail on nothing.
//
// # What it does not check
//
// This drives the real `listResources` handler over `fakeReads`, so it proves the
// *contract types* can carry every declared member and that the handler emits
// what it is given. It cannot prove that `internal/web` fills them from the
// descriptors and stores that hold them — that package imports this one, so a
// test here cannot reach it. `TestTheListPageIsFilledFromTheProducersThatHoldIt`
// in `internal/web` is the other half, and neither is sufficient alone.
func TestTheListPageCarriesEveryMemberTheContractDeclares(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/c/prod/r/pods", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/c/prod/r/pods answered %d: %s", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		State string         `json:"state"`
		Data  map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("the answer is not an envelope: %v", err)
	}
	// An empty answer and a failed answer are different answers, and so is one
	// that never reached the handler. Without this every loop below would pass
	// over nothing and report success.
	if envelope.State != string(StateOK) || len(envelope.Data) == 0 {
		t.Fatalf("state=%q with a %d-member page; this test measures a page that has something in it",
			envelope.State, len(envelope.Data))
	}

	columns, ok := envelope.Data["columns"].([]any)
	if !ok || len(columns) == 0 {
		t.Fatalf("the page carries no columns; there is nothing to measure")
	}
	rows, ok := envelope.Data["rows"].([]any)
	if !ok || len(rows) == 0 {
		t.Fatalf("the page carries no rows; there is nothing to measure")
	}
	bulk, ok := envelope.Data["bulk"].(map[string]any)
	if !ok || len(bulk) == 0 {
		t.Fatalf("the page carries no bulk affordance; there is nothing to measure")
	}

	// The union across the columns and across every cell of every row, never one
	// of them: `lockedNote` belongs to the column that cannot be hidden, `gauge`
	// to a cell that was measured, `reason` to one that could not be, and a
	// single column or cell carrying all of them at once would be one that cannot
	// exist.
	cells := map[string]bool{}
	for _, row := range rows {
		object, ok := row.(map[string]any)
		if !ok {
			t.Fatalf("a row is not an object: %T", row)
		}
		byKey, ok := object["cells"].(map[string]any)
		if !ok || len(byKey) == 0 {
			t.Fatalf("a row carries no cells; there is nothing to measure")
		}
		for _, cell := range byKey {
			carried, ok := cell.(map[string]any)
			if !ok {
				t.Fatalf("a cell is not an object: %T", cell)
			}
			for member := range carried {
				cells[member] = true
			}
		}
	}

	for _, subject := range []struct {
		schema  string
		carried map[string]bool
	}{
		{"ListPage", membersOf(envelope.Data)},
		{"Column", unionOf(t, columns)},
		{"Cell", cells},
		{"BulkAffordance", membersOf(bulk)},
	} {
		declared := schemaMembersInContract(t, subject.schema)
		for _, member := range sorted(declared) {
			if why, allowed := listMembersWithNoSource[subject.schema+"."+member]; allowed {
				t.Logf("%s.%s is declared and deliberately not carried: %s", subject.schema, member, why)
				continue
			}
			if !subject.carried[member] {
				t.Errorf("the contract declares %s.%s and no %s in the response carries it; "+
					"either fill it from the producer that holds it, or record here why it has no source",
					subject.schema, member, subject.schema)
			}
		}
		for _, member := range sorted(subject.carried) {
			if !declared[member] {
				t.Errorf("the response carries %s.%s and the contract does not declare it; "+
					"this is how `Cluster.group` survived a contract that had removed it",
					subject.schema, member)
			}
		}
	}
}

// listMembersWithNoSource is the deliberate exceptions, each with the evidence
// that there is nothing to fill it from. **It is empty, and that is the finding
// rather than an omission**: every member of the four schemas is traced to a
// producer:
//
//	Column.secondary   internal/resources.Column.Secondary, sideCol() in format.go:32
//	Column.gauged      internal/resources.Column.Gauged, gaugeCol() in format.go:62
//	Column.copyable    internal/resources.Column.Copyable, addrCol() in format.go:38
//	Column.prose       no descriptor declares one and list.html puts `.prose` on no
//	                   cell of this grid, so `false` is the faithful value rather
//	                   than a missing one. It is carried, not excepted
//	                   other. "age" is in the enum and never produced: no age
//	                   column is styled, and giving one a role would be adding
//	                   a behavior rather than carrying one
//	Column.lockedNote  internal/columns.Store.SetVisible refuses the first column;
//	                   the sentence is list.go's columnChoices
//	Cell.rank          internal/resources.Cell.Rank
//	Cell.hint          internal/resources.Cell.Hint
//	Cell.copy          internal/resources.Cell.Copy
//	Cell.parts         internal/resources.Cell.Addresses
//	Cell.gauge         internal/resources.Cell.Gauge, {Percent, Class}
//	ListPage.*         the query it was asked, the arrangement store, the kind's
//	                   own refresh band, and internal/web's writable() note
//	BulkAffordance.scope   cluster|kind, which selection.go already composed
//
// An entry belongs here only with the search that established the absence. A name
// added to get past a failure is this test switched off one member at a time.
var listMembersWithNoSource = map[string]string{}

// unionOf is every member name appearing on any element of a JSON array of
// objects.
func unionOf(t *testing.T, items []any) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("an item is not an object: %T", item)
		}
		for member := range object {
			out[member] = true
		}
	}
	return out
}

func membersOf(object map[string]any) map[string]bool {
	out := make(map[string]bool, len(object))
	for member := range object {
		out[member] = true
	}
	return out
}

// schemaMembersInContract is `components.schemas.<name>.properties`, read out of
// the file rather than restated here.
func schemaMembersInContract(t *testing.T, name string) map[string]bool {
	t.Helper()
	spec := loadSpec(t)
	components, ok := spec["components"].(map[string]any)
	if !ok {
		t.Fatal("the contract declares no components")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatal("the contract declares no schemas")
	}
	schema, ok := schemas[name].(map[string]any)
	if !ok {
		t.Fatalf("the contract declares no %s schema", name)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		t.Fatalf("the %s schema declares no properties", name)
	}
	out := make(map[string]bool, len(properties))
	for member := range properties {
		out[member] = true
	}
	return out
}

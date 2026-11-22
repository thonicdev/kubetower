package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The dashboard's half of `TestTheListPageCarriesEveryMemberTheContractDeclares`,
// and it exists for the same reason that one does.
//
// # An absent optional member conforms
//
// `TestRealResponsesMatchTheSchemasInTheContract` cannot see this defect.
// Every member of every panel schema below is optional on the wire, and
// `Overview` requires only `cluster` — so a response carrying four panels out of
// ten validates, while the page is not the dashboard that was designed.
//
// The members `internal/api.Overview` must carry:
//
//	Overview     version collectedAt allWell nodesSummary nodesTone
//	             workloadsSummary alerts trouble restarting nodes phases history
//	CountEntry   healthy note tone
//	CapacityCell ref tone
//	EventEntry   at age scope object reason message count tone
//	             (a bare `Ref` validates as an EventEntry with every optional
//	              member absent)
//
// `AlertEntry`, `TroubleEntry`, `RestartEntry`, `NodeEntry` and `PhaseEntry`
// need a Go type each, or there is nothing for a conformance test to be wrong
// about.
//
// # Both directions, and the expectation is read out of the file
//
//   - a member the contract declares and the response never carries, and the
//     next one somebody adds to the YAML;
//   - a member the response carries and the contract does not declare — how
//     a removed `Cluster.group` could survive.
//
// The expectation comes from `api/openapi.yaml` at run time. A test that listed
// the names it had just read out of `types.go` would agree with the struct by
// construction and fail on nothing.
//
// # Proving it fails
//
// Removing `Alerts` from the response — commenting out the field in
// `fakeReads.Overview` — and re-running:
//
//	overviewmembers_test.go: the contract declares Overview.alerts and the
//	response does not carry it; either fill it from the producer that holds it,
//	or record here why it has no source
//
// And adding `"restarts"` to a `TroubleEntry`, which the contract does not
// declare:
//
//	overviewmembers_test.go: the response carries TroubleEntry.restarts and the
//	contract does not declare it
//
// # What it does not check
//
// This drives the real handler over `fakeReads`, so it proves the contract types
// can carry every declared member and that the handler emits what it is given.
// It cannot prove that `internal/web` fills them from `internal/overview` —
// that package imports this one. `TestTheDashboardDrawsTheHalfAnOperatorOpensItFor`
// in `internal/web` is the other half, and neither is sufficient alone.
func TestTheDashboardCarriesEveryMemberTheContractDeclares(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/c/prod/overview", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/c/prod/overview answered %d: %s", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		State string         `json:"state"`
		Data  map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("the answer is not an envelope: %v", err)
	}
	// An empty answer and a failed answer are different answers, and so is one
	// that never reached the handler.
	if envelope.State != string(StateOK) || len(envelope.Data) == 0 {
		t.Fatalf("state=%q with a %d-member page; this test measures a page that has something in it",
			envelope.State, len(envelope.Data))
	}

	// Each panel's items, unioned across the elements: a single entry carrying
	// every member at once would in several cases be one that cannot exist — a
	// node cell that was both measured and unmeasured, a tile for a kind that
	// both has and has not a notion of health.
	panels := map[string]string{
		"counts":     "CountEntry",
		"capacity":   "CapacityCell",
		"top":        "TopBoard",
		"events":     "EventEntry",
		"alerts":     "AlertEntry",
		"trouble":    "TroubleEntry",
		"restarting": "RestartEntry",
		"phases":     "PhaseEntry",
	}

	subjects := []struct {
		schema  string
		carried map[string]bool
	}{{"Overview", membersOf(envelope.Data)}}

	// **A panel that is not measured has to be named here, not discovered.**
	//
	// A panel with no `items` that was skipped and logged would be the test
	// switched off by omission: deleting `Alerts` from the
	// response leaves a **zero-value** `List` behind, and a zero-value List
	// marshals as `{"state":""}` — a panel that is present, carries no items, and
	// would be silently passed over.
	//
	// `top` is the one deliberate exception: the fixture is a cluster with no
	// metrics backend, which is the state the panel exists to distinguish, so
	// `TopBoard`'s own members are measured nowhere and that is said out loud
	// rather than hidden behind a skip.
	unmeasured := map[string]string{
		"top": "the fixture is a cluster with no metrics backend, which is the answer this panel exists to give",
	}

	for member, schema := range panels {
		panel, ok := envelope.Data[member].(map[string]any)
		if !ok {
			t.Errorf("the page carries no %s panel at all", member)
			continue
		}
		state, _ := panel["state"].(string)
		if why, expected := unmeasured[member]; expected {
			if state == string(StateOK) {
				t.Errorf("%s is on the ok arm and is listed as unmeasured; remove the exception", member)
			}
			t.Logf("%s is %q and %s is not measured here: %s", member, state, schema, why)
			continue
		}
		if state != string(StateOK) {
			t.Errorf("%s is on the %q arm; a panel this fixture fills must be ok, and a zero-value "+
				"envelope marshals as state \"\" — which is exactly how a deleted panel passes unnoticed",
				member, state)
			continue
		}
		items, ok := panel["items"].([]any)
		if !ok || len(items) == 0 {
			t.Errorf("%s is ok and carries no items; there is nothing to measure", member)
			continue
		}
		subjects = append(subjects, struct {
			schema  string
			carried map[string]bool
		}{schema, unionOf(t, items)})
	}

	// `history` is a value envelope rather than a list, so it is checked apart.
	//
	// And it is **unioned with the object metrics panel**, because the two
	// surfaces produce the same schema and neither carries all of it. The
	// dashboard's cluster series offers no icon row and no span buttons — the
	// dashboard draws both sparklines at once, so there is nothing to select
	// between — and an object's panel is where those two members have a producer.
	// Measuring only one of them would leave four members declared, unfilled and
	// green.
	metricsPanel := map[string]bool{}
	history, ok := envelope.Data["history"].(map[string]any)
	if !ok {
		t.Error("the page carries no history panel at all")
	} else if data, ok := history["data"].(map[string]any); ok {
		for member := range data {
			metricsPanel[member] = true
		}
	}

	object := httptest.NewRecorder()
	mux.ServeHTTP(object, httptest.NewRequest(http.MethodGet,
		"/api/c/prod/metrics?kind=pods&namespace=default&name=api-0", nil))
	if object.Code != http.StatusOK {
		t.Fatalf("GET /api/c/prod/metrics answered %d: %s", object.Code, object.Body.String())
	}
	var panel struct {
		State string         `json:"state"`
		Data  map[string]any `json:"data"`
	}
	if err := json.Unmarshal(object.Body.Bytes(), &panel); err != nil {
		t.Fatalf("the metrics panel is not an envelope: %v", err)
	}
	if panel.State != string(StateOK) || len(panel.Data) == 0 {
		t.Fatalf("the metrics panel is %q with %d members; there is nothing to measure",
			panel.State, len(panel.Data))
	}
	for member := range panel.Data {
		metricsPanel[member] = true
	}
	subjects = append(subjects, struct {
		schema  string
		carried map[string]bool
	}{"MetricsPanel", metricsPanel})

	// `notShown` is a bare array on the page rather than an envelope: it is not a
	// panel, it is the list of panels that were never asked for.
	if skipped, ok := envelope.Data["notShown"].([]any); !ok || len(skipped) == 0 {
		t.Error("the page carries no notShown entries; there is nothing to measure")
	} else {
		subjects = append(subjects, struct {
			schema  string
			carried map[string]bool
		}{"SkippedPanel", unionOf(t, skipped)})
	}

	for _, subject := range subjects {
		declared := schemaMembersInContract(t, subject.schema)
		for _, member := range sorted(declared) {
			if why, allowed := overviewMembersWithNoSource[subject.schema+"."+member]; allowed {
				t.Logf("%s.%s is declared and deliberately not carried: %s", subject.schema, member, why)
				continue
			}
			if !subject.carried[member] {
				t.Errorf("the contract declares %s.%s and the response does not carry it; "+
					"either fill it from the producer that holds it, or record here why it has no source",
					subject.schema, member)
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

// overviewMembersWithNoSource is the deliberate exceptions, each with the
// evidence that there is nothing to fill it from.
//
// One entry, and it is a property of the fixture rather than of the product:
//
//	PhaseEntry.ref   the phase bar has **one** destination, the
//	                 pod list, and no segment leads anywhere narrower - nothing
//	                 narrower is addressable, since the filter above a table
//	                 matches a name and a namespace and not a phase.
//	                 `internal/web/apioverview.go` therefore leaves it unset on
//	                 every segment, and the panel's destination is
//	                 `Overview.workloadsSummary`. The member stays in the
//	                 contract because a phase that *is* addressable would
//	                 use it; it is carried by the fake so that the shape is
//	                 exercised, and this entry is what says the real producer
//	                 does not fill it.
//
// An entry belongs here only with the search that established the absence. A name
// added to get past a failure is this test switched off one member at a time.
var overviewMembersWithNoSource = map[string]string{}

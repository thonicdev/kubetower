package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The members of one line of a metrics panel, in both directions.
//
// `TestTheDashboardCarriesEveryMemberTheContractDeclares` measures
// `MetricsPanel`'s own members and stops there: a line is declared inline under
// `series.items`, not as a named schema, so no member test would reach it. That
// is the gap a member such as `current` could fall into - declared
// in the YAML, emitted as required by `openapi-typescript`, and conforming
// while absent, because every optional member is satisfied by not being sent.
//
// The expectation is read out of `api/openapi.yaml` at run time, like every
// other member test here, and the union is across the lines: `reason` and
// `current` belong to a line with no samples, and a line that both was drawn and
// had nothing to draw cannot exist.
func TestAMetricsLineCarriesEveryMemberTheContractDeclares(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/api/c/prod/metrics?kind=pods&namespace=default&name=api-0", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/c/prod/metrics answered %d: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		State string         `json:"state"`
		Data  map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("the answer is not an envelope: %v", err)
	}
	if envelope.State != string(StateOK) || len(envelope.Data) == 0 {
		t.Fatalf("state=%q with a %d-member panel; this test measures a panel that has something in it",
			envelope.State, len(envelope.Data))
	}
	lines, ok := envelope.Data["series"].([]any)
	if !ok || len(lines) == 0 {
		t.Fatal("the panel carries no lines; there is nothing to measure")
	}
	carried := unionOf(t, lines)

	declared := lineMembersInContract(t)
	for _, member := range sorted(declared) {
		if !carried[member] {
			t.Errorf("the contract declares MetricsPanel.series[].%s and no line in the response carries it", member)
		}
	}
	for _, member := range sorted(carried) {
		if !declared[member] {
			t.Errorf("a line in the response carries %s and the contract does not declare it", member)
		}
	}
}

// lineMembersInContract is the property names under
// `components.schemas.MetricsPanel.properties.series.items.properties`.
func lineMembersInContract(t *testing.T) map[string]bool {
	t.Helper()
	walk := func(from any, key string) map[string]any {
		t.Helper()
		object, ok := from.(map[string]any)
		if !ok {
			t.Fatalf("the contract has no object where %q should be", key)
		}
		next, ok := object[key].(map[string]any)
		if !ok {
			t.Fatalf("the contract has no %q", key)
		}
		return next
	}
	node := any(loadSpec(t))
	for _, key := range []string{"components", "schemas", "MetricsPanel", "properties", "series", "items", "properties"} {
		node = walk(node, key)
	}
	properties := node.(map[string]any)
	out := make(map[string]bool, len(properties))
	for member := range properties {
		out[member] = true
	}
	if len(out) == 0 {
		t.Fatal("a metrics line declares no members")
	}
	return out
}

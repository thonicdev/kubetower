package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
)

// An absent optional member conforms, so this test is about the member set.
//
// `Cluster` declares no `additionalProperties: false`, requires only `name`,
// `label` and `state`, and every other member is optional. So
// `TestRealResponsesMatchTheSchemasInTheContract` — which is the strongest test
// in this package — passes a response that carries three members out of eleven,
// and passes it again when the contract grows a fourth that nothing fills.
//
// The members at risk are the ones the UI depends on. The rail sorts on `bar` as
// its primary key and draws a seam between bars when the visible tiles span more
// than one; with no `bar` on the wire the arrangement is inert and the seam is
// never drawn. The nameplate's initials and color are in the same position:
// the store holds them and the JSON must carry them.
//
// # So this test is about the member set, not about the values
//
// Both directions, because the two failures are different and only one of them
// is caught by anything else:
//
//   - a member the contract declares and the response never carries — `initials`,
//     `colour`, `bar`, `readOnly`, `remedy`, and the next one somebody adds to
//     the YAML;
//   - a member the response carries and the contract does not declare, such as
//     a removed `group`, and the next one somebody forgets to delete from the
//     struct.
//
// The expectation is **read out of `api/openapi.yaml` at run time**, the way
// `conformance_test.go` reads it. A test that listed the eleven names it had just
// seen in `types.go` would agree with the struct by construction and fail on
// nothing.
//
// # What it does not check
//
// This drives the real `listClusters` handler over `fakeClusters`, so it proves
// the *contract type* can carry every declared member and that the handler emits
// what it is given. It cannot prove that `internal/web` fills them from the
// stores — that package imports this one, so a test here cannot reach it.
// `TestTheClusterListIsFilledFromTheStoresThatHoldIt` in `internal/web` is the
// other half, and neither is sufficient alone.
func TestTheClusterListCarriesEveryMemberTheContractDeclares(t *testing.T) {
	declared := clusterMembersInContract(t)

	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/clusters", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/clusters answered %d: %s", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		State string           `json:"state"`
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("the answer is not an envelope: %v", err)
	}
	// An empty answer and a failed answer are different answers, and so is one
	// that never reached the handler. Without this the loops below would pass
	// over nothing and report success.
	if envelope.State != string(StateOK) || len(envelope.Items) == 0 {
		t.Fatalf("state=%q with %d items; this test measures a list that has something in it",
			envelope.State, len(envelope.Items))
	}

	// The union across the items, not one item: `reason` and `remedy` belong to
	// a cluster that failed, `readOnly` to one that is fenced, and an item
	// carrying all of them at once would be a cluster that cannot exist.
	carried := map[string]bool{}
	for _, item := range envelope.Items {
		for member := range item {
			carried[member] = true
		}
	}

	for _, member := range sorted(declared) {
		if why, allowed := clusterMembersWithNoSource[member]; allowed {
			t.Logf("%q is declared and deliberately not carried: %s", member, why)
			continue
		}
		if !carried[member] {
			t.Errorf("the contract declares Cluster.%s and no cluster in the response carries it; "+
				"either fill it from the store that holds it, or record here why it has no source", member)
		}
	}
	for _, member := range sorted(carried) {
		if !declared[member] {
			t.Errorf("the response carries Cluster.%s and the contract does not declare it; "+
				"this is how `group` survived a contract that had removed it", member)
		}
	}
}

// clusterMembersWithNoSource is the deliberate exceptions, each with the evidence
// that there is nothing to fill it from. **It is empty, and that is the finding
// rather than an omission**: every one of the eleven members `Cluster` declares
// is traced to a store:
//
//	name      the kubeconfig, through cluster.Manager.Contexts
//	label     internal/nameplates, falling back to the context name
//	initials  internal/nameplates, or derived from the context name as the tile derives it
//	color    internal/nameplates, a palette key and absent for the automatic color
//	hidden    internal/hotbars, Layout.IsHidden
//	order     internal/hotbars, the tile's position; absent when it is on no bar
//	bar       internal/hotbars, Layout.BarOf
//	readOnly  Options.ReadOnly — KT_READONLY_CLUSTERS, per cluster
//	state     internal/health, the probe's own snapshot
//	reason    internal/health, through the same classification
//	remedy    internal/health's state, through whoFixes in internal/web
//	loopback  the kubeconfig's server address, through cluster.Manager.Loopback
//
// An entry belongs here only with the search that established the absence. A name
// added to skip a failure is this test switched off one member at a time.
var clusterMembersWithNoSource = map[string]string{}

// clusterMembersInContract is `components.schemas.Cluster.properties`, read out
// of the file rather than restated here.
func clusterMembersInContract(t *testing.T) map[string]bool {
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
	schema, ok := schemas["Cluster"].(map[string]any)
	if !ok {
		t.Fatal("the contract declares no Cluster schema")
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		t.Fatal("the Cluster schema declares no properties")
	}
	out := make(map[string]bool, len(properties))
	for name := range properties {
		out[name] = true
	}
	return out
}

// sorted keeps the failures in a stable order, so a run that reports four of
// them reports them the same way twice.
func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

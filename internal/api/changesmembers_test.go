package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// `ChangesPanel` declares members that an optional-member check cannot see
// missing: `entriesState`, `entriesReason`, `entriesTruncated`,
// `retentionDays`, `kindResolved` and `logs`. An **optional member is
// satisfied by being absent**, so
// `TestRealResponsesMatchTheSchemasInTheContract` cannot notice that nothing
// sets them. The same hole `editmembers_test.go` closes on
// `ManifestPanel.editableNote`.
//
// It matters here because `Changes.tsx` branches on `entriesState` so that
// *the store is off*, *the window holds nothing* and *it could not be read*
// stay three different sentences. With nothing setting it, the panel always
// takes the same branch and draws the emptiness that means "there is nothing".
//
// Both directions, as in `listmembers_test.go`, because the two failures are
// different: a member the contract declares and nothing carries, and a member a
// response carries that the contract does not declare.
//
// The expectation is read out of the contract **at run time**. A test that
// listed the names it had just read off `types.go` would agree with the struct
// by construction and could fail on nothing.
//
// What it cannot check: this drives the real handlers over `fakeReads`, so it
// proves the contract types can carry every declared member. That
// `internal/web` actually fills them is the other half, and it is
// `TestTheChangesPanelSaysWhichEmptinessItMeans` over there.
func TestTheChangesPanelCarriesEveryMemberTheContractDeclares(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	// Two clusters, because the panel's two halves cannot show every member on
	// one answer: a `reason` rides only on a state that is not ok, so a panel
	// that read cleanly carries neither reason and a panel that did not carries
	// no rollouts. Measuring a single response would mean excepting the members
	// this test was written for.
	read := func(cluster string) map[string]any {
		t.Helper()
		recorder := httptest.NewRecorder()
		url := "/api/c/" + cluster + "/changes?kind=deployments&namespace=default&name=api"
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, url, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s answered %d: %s", url, recorder.Code, recorder.Body.String())
		}
		var envelope struct {
			State string         `json:"state"`
			Data  map[string]any `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("%s did not answer an envelope: %v", url, err)
		}
		// An empty answer and a failed answer are different answers, and so is
		// one that never reached the handler: without this the loops below
		// would pass over nothing and report success.
		if envelope.State != string(StateOK) || len(envelope.Data) == 0 {
			t.Fatalf("%s answered state=%q with %d members; this test measures a panel that has something in it",
				url, envelope.State, len(envelope.Data))
		}
		return envelope.Data
	}

	clean, refused := read("prod"), read(guardedCluster)

	entries, ok := clean["entries"].([]any)
	if !ok || len(entries) == 0 {
		t.Fatal("the changes panel carries no entries; there is nothing to measure")
	}
	rollouts, ok := clean["rollouts"].([]any)
	if !ok || len(rollouts) == 0 {
		t.Fatal("the changes panel carries no rollouts; there is nothing to measure")
	}

	for _, subject := range []struct {
		schema  string
		carried map[string]bool
	}{
		{"ChangesPanel", both(membersOf(clean), membersOf(refused))},
		{"Rollout", unionOf(t, rollouts)},
	} {
		declared := schemaMembersInContract(t, subject.schema)
		for _, member := range sorted(declared) {
			if !subject.carried[member] {
				t.Errorf("the contract declares %s.%s and no response carries it; "+
					"either fill it from the producer that holds it, or remove it from the contract - "+
					"a member with no producer is a promise the panel cannot keep",
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

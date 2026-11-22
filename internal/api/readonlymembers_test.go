package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// `Capabilities.readOnly` carries every member the contract declares.
//
// `measured` and `detail` are optional, so the
// conformance test cannot see them missing: an optional member is satisfied by being
// absent. So the members are read out of the YAML at run time and asserted in
// both directions, `listmembers_test.go`'s shape.
func TestTheReadOnlyStateCarriesEveryMemberTheContractDeclares(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)
	body := settingsBody(t, mux, httptest.NewRequest("GET", "/api/capabilities", nil))
	sent := mapOf(t, body, "readOnly")

	declared := inlinePropertiesInContract(t, "Capabilities", "readOnly")
	for member := range declared {
		if _, ok := sent[member]; !ok {
			t.Errorf("the contract declares readOnly.%s and the server does not send it", member)
		}
	}
	for member := range sent {
		if !declared[member] {
			t.Errorf("the server sends readOnly.%s and the contract does not declare it", member)
		}
	}
	// `measured: false` must travel, not be omitted: its default is true.
	if measured, ok := sent["measured"].(bool); !ok || measured {
		t.Errorf("readOnly.measured is %v, and an unknown fence must say false", sent["measured"])
	}
}

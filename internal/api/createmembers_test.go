package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// The list's "+": `ListPage.create` and the template, by
// member set in both directions, read out of the contract at run time - as
// listmembers_test.go and editmembers_test.go do, and for their reason: an
// optional member is satisfied by being absent, so nothing else notices one
// nothing fills.
//
// Over `fakeReads`, so it proves the types and the handlers carry every
// member. That internal/web fills them is its own tests' half.

func createMux() *http.ServeMux {
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)
	return mux
}

func readData(t *testing.T, mux *http.ServeMux, url string) map[string]any {
	t.Helper()
	recorder := httptest.NewRecorder()
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
	if envelope.State != string(StateOK) || len(envelope.Data) == 0 {
		t.Fatalf("%s answered state=%q with %d members; this test measures an answer that has something in it",
			url, envelope.State, len(envelope.Data))
	}
	return envelope.Data
}

func TestTheCreateAffordanceAndTemplateCarryEveryMemberTheContractDeclares(t *testing.T) {
	mux := createMux()

	// Two answers, because `namespace` (the one namespace a create goes to) and
	// `reason` (why it may not) are never on one.
	affordance := func(cluster string) map[string]bool {
		t.Helper()
		page := readData(t, mux, "/api/c/"+cluster+"/r/pods")
		create, ok := page["create"].(map[string]any)
		if !ok || len(create) == 0 {
			t.Fatalf("the %s list carries no create affordance; there is nothing to measure", cluster)
		}
		return membersOf(create)
	}
	template := readData(t, mux, "/api/c/prod/create/template?kind=widgets.example.com&namespace=default")

	for _, subject := range []struct {
		schema  string
		carried map[string]bool
	}{
		{"CreateAffordance", both(affordance("prod"), affordance(guardedCluster))},
		{"CreateTemplate", membersOf(template)},
	} {
		declared := schemaMembersInContract(t, subject.schema)
		for _, member := range sorted(declared) {
			if !subject.carried[member] {
				t.Errorf("the contract declares %s.%s and no response carries it", subject.schema, member)
			}
		}
		for _, member := range sorted(subject.carried) {
			if !declared[member] {
				t.Errorf("the response carries %s.%s and the contract does not declare it", subject.schema, member)
			}
		}
	}
}

func postJSON(mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("X-KubeTower-Request", "1")
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recorder, request)
	return recorder
}

// A new object has no resourceVersion: one sent with the body is refused
// before anything is previewed, as a request rather than as a cluster's no.
func TestACreatePreviewRefusesAResourceVersion(t *testing.T) {
	got := postJSON(createMux(), "/api/c/prod/create/preview",
		`{"document":"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n  namespace: default\n","resourceVersion":"7"}`)
	if got.Code != http.StatusBadRequest {
		t.Errorf("a create carrying a resourceVersion answered %d, want 400: %s", got.Code, got.Body.String())
	}
}

// The namespace is written into the document, so the template refuses one
// that is not a DNS label rather than handing it to the backend.
func TestTheTemplateRefusesANamespaceThatIsNotALabel(t *testing.T) {
	for _, namespace := range []string{"a%0Akind:%20Secret", "UPPER", "-lead", strings.Repeat("a", 64)} {
		recorder := httptest.NewRecorder()
		createMux().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
			"/api/c/prod/create/template?kind=deployments&namespace="+namespace, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("namespace %q answered %d, want 400", namespace, recorder.Code)
		}
	}
}

// A token minted for a manifest apply cannot be spent on a create, nor the
// other way: the plan's kind is part of what was agreed.
func TestACreateTokenIsACreatesOnly(t *testing.T) {
	mux := createMux()
	token := func(path, body string) string {
		t.Helper()
		got := postJSON(mux, path, body)
		if got.Code != http.StatusOK {
			t.Fatalf("%s answered %d: %s", path, got.Code, got.Body.String())
		}
		var plan struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal(got.Body.Bytes(), &plan)
		return plan.Token
	}
	manifest := token("/api/c/prod/manifest/preview", `{"document":"apiVersion: v1\nkind: ConfigMap\n","resourceVersion":"42"}`)
	if got := postJSON(mux, "/api/c/prod/create/apply", `{"token":`+strconv.Quote(manifest)+`}`); got.Code == http.StatusOK {
		t.Errorf("a manifest's token created an object: %s", got.Body.String())
	}
	create := token("/api/c/prod/create/preview", `{"document":"apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n  namespace: default\n"}`)
	if got := postJSON(mux, "/api/c/prod/manifest/apply", `{"token":`+strconv.Quote(create)+`}`); got.Code == http.StatusOK {
		t.Errorf("a create's token applied a manifest: %s", got.Body.String())
	}
	fresh := token("/api/c/prod/create/preview", `{"document":"apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n  namespace: default\n"}`)
	got := postJSON(mux, "/api/c/prod/create/apply", `{"token":`+strconv.Quote(fresh)+`}`)
	if got.Code != http.StatusOK {
		t.Fatalf("the create answered %d: %s", got.Code, got.Body.String())
	}
	var outcome ActOutcome
	if err := json.Unmarshal(got.Body.Bytes(), &outcome); err != nil || len(outcome.Next) != 1 {
		t.Errorf("the create's outcome names no next object (%v): %s", err, got.Body.String())
	}
}

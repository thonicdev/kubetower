package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// refusingReads answers every list as a request the backend refuses, as it
// answers a label selector that does not parse.
type refusingReads struct{ fakeReads }

func (refusingReads) ListResources(_ context.Context, q ListQuery) (ListPage, *Failure) {
	return ListPage{}, InvalidSelector(`the label selector "` + q.Selector + `" is not valid`)
}

// A selector the backend cannot parse is answered 400 with its sentence, never
// as a list: a page drawn without it would show every row as if each matched.
// Its code is `invalid_selector`, apart from every other bad request, so a
// client says the selector is wrong only when it is.
func TestAnInvalidSelectorIsABadRequest(t *testing.T) {
	mux := listMux()
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/c/prod/r/pods?selector=app%3D%3D%3D", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("answered %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	var problem Problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("the body is not a problem: %v: %s", err, recorder.Body.String())
	}
	if problem.Code != ProblemInvalidSelector || !strings.Contains(problem.Detail, `"app==="`) {
		t.Errorf("the problem is %+v; want invalid_selector naming the selector", problem)
	}
}

// A bad request that is not the selector's keeps `invalid_request`, even with a
// selector set: a search over its bound is not a selector the client should
// call invalid.
func TestABadRequestBesideASelectorIsNotAnInvalidSelector(t *testing.T) {
	mux := listMux()
	recorder := httptest.NewRecorder()
	long := strings.Repeat("x", 300)
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/c/prod/r/pods?selector=app%3Dx&search="+long, nil))
	var problem Problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("the body is not a problem: %v: %s", err, recorder.Body.String())
	}
	if recorder.Code != http.StatusBadRequest || problem.Code != ProblemInvalidRequest {
		t.Errorf("answered %d %+v; want 400 invalid_request", recorder.Code, problem)
	}
}

// listMux is the API with a backend that refuses every list's selector.
func listMux() *http.ServeMux {
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: refusingReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)
	return mux
}

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thonicdev/kubetower/extension"
)

// A cluster name that is not in the kubeconfig is answered with the console's
// one sentence for it, the one an extra's page answers with too,
// whether the name arrives in the path or in the query.
func TestAnUnknownClusterIsAnsweredWithTheConsolesSentence(t *testing.T) {
	deps := Deps{Clusters: fakeClusters{}, Sessions: fakeSessions{}, Profile: fakeProfile{}}
	deps.AllowOrigin = func(*http.Request, string) bool { return true }
	mux := http.NewServeMux()
	New(deps).Mount(mux)

	for _, path := range []string{
		"/api/c/nope/nav",
		"/api/logs/containers?cluster=nope&namespace=shop&kind=pods&name=db-0",
	} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			var problem Problem
			if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
				t.Fatalf("the body is not a problem: %s", recorder.Body.String())
			}
			if recorder.Code != http.StatusNotFound || problem.Code != ProblemNotFound {
				t.Fatalf("answered %d %s, want 404 %s", recorder.Code, problem.Code, ProblemNotFound)
			}
			if want := extension.UnknownClusterText("nope"); problem.Detail != want {
				t.Errorf("the detail is %q, want %q", problem.Detail, want)
			}
		})
	}
}

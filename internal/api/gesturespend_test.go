package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// A mistyped gesture does not spend the plan.
//
// If the token were taken before the gesture was checked, a Scale typed as -1
// would answer 400 - and the corrected value, sent with the same token from the
// same dialog, would answer 409 "unknown, already spent or expired" with no fresh
// plan to adopt: a dead end that only closing and starting again gets out of. A
// refused gesture leaves the token where it was: nothing was agreed to, so
// nothing is used up.

// actPoster is one console's mux, and a POST into it as the front end sends it.
func actPoster(t *testing.T, deps Deps) func(path, body string) *httptest.ResponseRecorder {
	t.Helper()
	deps.AllowOrigin = func(*http.Request, string) bool { return true }
	mux := http.NewServeMux()
	New(deps).Mount(mux)
	return func(path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-KubeTower-Request", "1")
		mux.ServeHTTP(recorder, request)
		return recorder
	}
}

func tokenOf(t *testing.T, preview *httptest.ResponseRecorder) string {
	t.Helper()
	if preview.Code != http.StatusOK {
		t.Fatalf("the preview answered %d: %s", preview.Code, preview.Body.String())
	}
	var plan struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &plan); err != nil || plan.Token == "" {
		t.Fatalf("the preview is not a plan: %v (%s)", err, preview.Body.String())
	}
	return plan.Token
}

func actBody(token, confirmed string) string {
	return `{"token":` + strconv.Quote(token) + `,"confirmed":` + strconv.Quote(confirmed) + `}`
}

func TestAMistypedScaleKeepsItsPlan(t *testing.T) {
	acts := &gestureActs{confirmation: &Confirmation{Kind: "number", Minimum: intPointer64(0)}}
	post := actPoster(t, Deps{Clusters: fakeClusters{}, Acts: acts})

	token := tokenOf(t, post("/api/c/prod/actions/preview",
		`{"verb":"scale","target":{"kind":"deployments","namespace":"prod","name":"api"}}`))

	wrong := post("/api/c/prod/actions", actBody(token, "-1"))
	if wrong.Code != http.StatusBadRequest {
		t.Fatalf("-1 against a floor of 0 answered %d: %s", wrong.Code, wrong.Body.String())
	}
	if acts.ran != 0 {
		t.Fatalf("a refused gesture reached the act half %d time(s)", acts.ran)
	}

	right := post("/api/c/prod/actions", actBody(token, "3"))
	if right.Code != http.StatusOK {
		t.Fatalf("the corrected value with the same token answered %d: %s - the mistyped one spent the plan",
			right.Code, right.Body.String())
	}
	if acts.ran != 1 || acts.confirmed != "3" {
		t.Fatalf("the act half ran %d time(s) with %q, want once with 3", acts.ran, acts.confirmed)
	}

	// Still single-use: the act spent it.
	again := post("/api/c/prod/actions", actBody(token, "3"))
	if again.Code != http.StatusConflict {
		t.Fatalf("a token already acted on answered %d: %s", again.Code, again.Body.String())
	}
	if acts.ran != 1 {
		t.Fatalf("the act ran %d times on one plan", acts.ran)
	}
}

func TestAMistypedBulkCountKeepsItsPlan(t *testing.T) {
	post := actPoster(t, Deps{Clusters: fakeClusters{}, Acts: fakeActs{}})

	token := tokenOf(t, post("/api/c/prod/bulk/preview",
		`{"verb":"delete","kind":"pods","targets":[{"kind":"pods","namespace":"prod","name":"a"},`+
			`{"kind":"pods","namespace":"prod","name":"b"}]}`))

	if wrong := post("/api/c/prod/bulk", actBody(token, "3")); wrong.Code != http.StatusBadRequest {
		t.Fatalf("a wrong count answered %d: %s", wrong.Code, wrong.Body.String())
	}
	if right := post("/api/c/prod/bulk", actBody(token, "2")); right.Code != http.StatusOK {
		t.Fatalf("the right count with the same token answered %d: %s", right.Code, right.Body.String())
	}
}

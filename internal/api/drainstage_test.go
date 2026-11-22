package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// The second stage of a drain, reachable.
//
// # The shape
//
// `internal/actions` drains in two stages — cordon and list, then evict — and
// the second needs `Request.Stage == StageEvict`. No request through the JSON
// API can say so, and `RunAction` must not drop `Result.Plan`: otherwise the
// user would get a real, unreversed cordon, a sentence saying "confirm again to
// evict", and no way to.
//
// The first stage's outcome carries the plan for the second, minted
// by this package from a request the backend hands back with its stage set.
// The stage is off the wire, so the only way a stage-two request exists is
// through that outcome — and the plan is then a plan like any other: bound by
// a token, re-derived before the act, single-use.

// drainActs is the backend as the drain sees it: a first stage that hands back
// the request for the second, a preview that answers each stage with its own
// facts, and a record of which stage the act half was reached with.
type drainActs struct {
	fakeActs
	stagesRun []string
}

func (d *drainActs) PreviewAction(_ context.Context, _ string, req ActionRequest) (PlanFacts, error) {
	if req.Verb != "drain" || req.Stage == "" {
		return PlanFacts{
			Verb: req.Verb, Targets: []PlanTarget{req.Target}, Count: 1, Destructive: true,
			Confirmation: &Confirmation{Kind: "name", Expected: req.Target.Name},
		}, nil
	}
	return PlanFacts{
		Verb: "evict", Count: 2, Destructive: true,
		Targets: []PlanTarget{
			{Kind: "pods", Namespace: "prod", Name: "api-1", Note: "nothing owns this pod"},
			{Kind: "pods", Namespace: "prod", Name: "api-2"},
		},
		Warnings:     []string{"kube-system/fluent-bit-x is left alone: managed by a DaemonSet"},
		Confirmation: &Confirmation{Kind: "name", Expected: req.Target.Name},
	}, nil
}

func (d *drainActs) RunAction(_ context.Context, _ string, req ActionRequest, _ string) (ActOutcome, error) {
	d.stagesRun = append(d.stagesRun, req.Stage)
	if req.Stage == "" {
		return ActOutcome{
			Succeeded: 1,
			Results:   []ActResult{{Target: req.Target, OK: true}},
			Summary:   "Cordoned node-a. Nothing has been evicted yet.",
			NextRequest: &ActionRequest{
				Verb: req.Verb, Target: req.Target, Stage: "evict",
			},
		}, nil
	}
	return ActOutcome{Succeeded: 2, Results: []ActResult{
		{Target: PlanTarget{Kind: "pods", Namespace: "prod", Name: "api-1"}, OK: true},
		{Target: PlanTarget{Kind: "pods", Namespace: "prod", Name: "api-2"}, OK: true},
	}, Summary: "Requested 2 eviction(s) on node-a."}, nil
}

func drainMux(acts *drainActs) (*http.ServeMux, func(path, body string) *httptest.ResponseRecorder) {
	mux := http.NewServeMux()
	New(Deps{Clusters: fakeClusters{}, Acts: acts,
		AllowOrigin: func(*http.Request, string) bool { return true }}).Mount(mux)
	post := func(path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("X-KubeTower-Request", "1")
		mux.ServeHTTP(recorder, request)
		return recorder
	}
	return mux, post
}

type outcomeOnTheWire struct {
	Succeeded int    `json:"succeeded"`
	Summary   string `json:"summary"`
	NextPlan  *struct {
		Token string `json:"token"`
		Facts struct {
			Verb     string       `json:"verb"`
			Count    int          `json:"count"`
			Targets  []PlanTarget `json:"targets"`
			Warnings []string     `json:"warnings"`
		} `json:"facts"`
	} `json:"nextPlan"`
	// Never on the wire. Decoded here so the test can say so.
	NextRequest *ActionRequest `json:"nextRequest"`
}

func TestADrainWalksCordonThenListThenEvict(t *testing.T) {
	acts := &drainActs{}
	_, post := drainMux(acts)

	preview := post("/api/c/prod/actions/preview", `{"verb":"drain","target":{"kind":"nodes","name":"node-a"}}`)
	if preview.Code != http.StatusOK {
		t.Fatalf("the preview answered %d: %s", preview.Code, preview.Body.String())
	}
	var first struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}

	// Stage one: the cordon, confirmed by the node's name.
	cordoned := post("/api/c/prod/actions", `{"token":`+strconv.Quote(first.Token)+`,"confirmed":"node-a"}`)
	if cordoned.Code != http.StatusOK {
		t.Fatalf("the first stage answered %d: %s", cordoned.Code, cordoned.Body.String())
	}
	var outcome outcomeOnTheWire
	if err := json.Unmarshal(cordoned.Body.Bytes(), &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.NextPlan == nil || outcome.NextPlan.Token == "" {
		t.Fatalf("the first stage's outcome carries no plan for the second; the operator is told to "+
			"confirm again and has nothing to confirm: %s", cordoned.Body.String())
	}
	if outcome.NextRequest != nil {
		t.Fatal("the request behind the next stage reached the wire; a client is given the plan, never the request")
	}
	if outcome.Summary == "" {
		t.Error("the first stage's sentence was dropped; the operator reads a count and not that nothing has been evicted yet")
	}
	next := outcome.NextPlan
	if next.Facts.Verb != "evict" || next.Facts.Count != 2 || len(next.Facts.Targets) != 2 {
		t.Errorf("the second stage's facts do not list what would be evicted: %+v", next.Facts)
	}
	if len(next.Facts.Warnings) != 1 {
		t.Errorf("the pods left alone are not carried as warnings: %+v", next.Facts.Warnings)
	}
	if len(acts.stagesRun) != 1 || acts.stagesRun[0] != "" {
		t.Fatalf("stages run after the first act: %q; the evictions ran before the list was confirmed", acts.stagesRun)
	}

	// Stage two: the evictions, confirmed by the node's name again, on the plan
	// the outcome carried.
	evicted := post("/api/c/prod/actions", `{"token":`+strconv.Quote(next.Token)+`,"confirmed":"node-a"}`)
	if evicted.Code != http.StatusOK {
		t.Fatalf("the second stage answered %d: %s", evicted.Code, evicted.Body.String())
	}
	if len(acts.stagesRun) != 2 || acts.stagesRun[1] != "evict" {
		t.Fatalf("stages run: %q, want the second to be \"evict\"", acts.stagesRun)
	}
	var final outcomeOnTheWire
	if err := json.Unmarshal(evicted.Body.Bytes(), &final); err != nil {
		t.Fatal(err)
	}
	if final.Succeeded != 2 || final.NextPlan != nil {
		t.Errorf("the second stage's outcome is %s; two evictions and no further stage were expected",
			evicted.Body.String())
	}

	// And the second stage's token was spent by that act.
	again := post("/api/c/prod/actions", `{"token":`+strconv.Quote(next.Token)+`,"confirmed":"node-a"}`)
	if again.Code != http.StatusConflict {
		t.Errorf("a spent second-stage token was accepted again: %d", again.Code)
	}
}

// The stage is not a client's to name. Off the wire by tag, and the decoder
// refuses unknown members, so a preview asking for the second stage is a 400 -
// the pod list cannot be skipped by writing the request by hand.
func TestAClientCannotStartADrainAtItsSecondStage(t *testing.T) {
	acts := &drainActs{}
	_, post := drainMux(acts)
	preview := post("/api/c/prod/actions/preview",
		`{"verb":"drain","target":{"kind":"nodes","name":"node-a"},"stage":"evict"}`)
	if preview.Code != http.StatusBadRequest {
		t.Fatalf("a preview naming its own stage answered %d, expected 400: %s", preview.Code, preview.Body.String())
	}
	if len(acts.stagesRun) != 0 {
		t.Fatalf("something ran: %q", acts.stagesRun)
	}
}

// The mismatch between what the second stage's gesture asks for and what is
// typed is refused before anything is evicted, like any other plan's.
func TestTheSecondStageIsConfirmedOnItsOwn(t *testing.T) {
	acts := &drainActs{}
	_, post := drainMux(acts)
	preview := post("/api/c/prod/actions/preview", `{"verb":"drain","target":{"kind":"nodes","name":"node-a"}}`)
	var first struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	cordoned := post("/api/c/prod/actions", `{"token":`+strconv.Quote(first.Token)+`,"confirmed":"node-a"}`)
	var outcome outcomeOnTheWire
	if err := json.Unmarshal(cordoned.Body.Bytes(), &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.NextPlan == nil {
		t.Fatalf("no next plan: %s", cordoned.Body.String())
	}
	refused := post("/api/c/prod/actions", `{"token":`+strconv.Quote(outcome.NextPlan.Token)+`,"confirmed":"node-b"}`)
	if refused.Code != http.StatusBadRequest {
		t.Errorf("the wrong name confirmed the evictions: %d", refused.Code)
	}
	if len(acts.stagesRun) != 1 {
		t.Fatalf("stages run: %q; the evictions ran on a wrong name", acts.stagesRun)
	}
}

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

// The precondition, on the one pair a client exists for.
//
// # What it guards
//
// `PlanFacts.precondition` is declared in the contract as "the resourceVersion
// the act will carry"; `sameFacts` must compare it as well as verb, count
// and targets, or the re-derivation between a preview and a click sees a change
// of *set* and not of *state*. A cordon confirmed against a node that had since
// been rewritten would be the same plan as far as this package could tell, and
// the backend's `patch` would compare nothing either. Every layer would assume
// the next one held the check.
//
// Three properties, each its own test, because they fail separately:
//
//   - `sameFacts` sees a moved precondition as a different plan;
//   - the act half answers that with the existing 409 and a fresh plan, and the
//     backend is never reached;
//   - the version the operator agreed to is what the backend is handed, since a
//     handler that compared it and then dropped it would satisfy the first two.

// preconditionActs answers every preview with whatever version the test has
// set, and records what the act half was handed.
type preconditionActs struct {
	fakeActs
	version string
	// uid is the UID the preview states; empty states none.
	uid    string
	ran    int
	handed ActionRequest
}

func (p *preconditionActs) PreviewAction(_ context.Context, _ string, req ActionRequest) (PlanFacts, error) {
	return PlanFacts{
		Verb: req.Verb, Targets: []PlanTarget{req.Target}, Count: 1,
		Precondition: p.version, PreconditionUID: p.uid,
		Confirmation: &Confirmation{Kind: "acknowledge"},
	}, nil
}

func (p *preconditionActs) RunAction(_ context.Context, _ string, req ActionRequest, _ string) (ActOutcome, error) {
	p.ran++
	p.handed = req
	return ActOutcome{Succeeded: 1, Results: []ActResult{{Target: req.Target, OK: true}}}, nil
}

func TestSameFactsSeesAMovedPrecondition(t *testing.T) {
	agreed := PlanFacts{
		Verb: "cordon", Count: 1, Targets: []PlanTarget{{Kind: "nodes", Name: "node-a"}},
		Precondition: "7",
	}
	moved := agreed
	moved.Precondition = "8"
	if sameFacts(agreed, moved) {
		t.Fatal("a plan against resourceVersion 7 and one against 8 were the same plan; " +
			"the object changed and the confirmation did not notice")
	}
	if !sameFacts(agreed, agreed) {
		t.Fatal("a plan is not the same as itself")
	}
	// And two plans with no precondition at all still compare equal, or every
	// bulk run and every drain's second stage is refused on the way through.
	none := agreed
	none.Precondition = ""
	if !sameFacts(none, none) {
		t.Fatal("two plans carrying no precondition were not the same plan")
	}
}

// previewThenAct drives the handler pair with the recording double: preview,
// let the test move the version, then spend the token.
func previewThenAct(t *testing.T, acts *preconditionActs, between func()) (*httptest.ResponseRecorder, string) {
	t.Helper()
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

	preview := post("/api/c/prod/actions/preview",
		`{"verb":"cordon","target":{"kind":"nodes","name":"node-a"}}`)
	if preview.Code != http.StatusOK {
		t.Fatalf("the preview answered %d: %s", preview.Code, preview.Body.String())
	}
	var plan struct {
		Token string `json:"token"`
		Facts struct {
			Precondition string `json:"precondition"`
		} `json:"facts"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	between()
	return post("/api/c/prod/actions", `{"token":`+strconv.Quote(plan.Token)+`}`), plan.Facts.Precondition
}

func TestAnActWhosePreconditionMovedIsRefusedWithAFreshPlan(t *testing.T) {
	acts := &preconditionActs{version: "7"}
	act, agreed := previewThenAct(t, acts, func() { acts.version = "8" })

	if agreed != "7" {
		t.Fatalf("the plan states precondition %q; the operator agreed against nothing", agreed)
	}
	if act.Code != http.StatusConflict {
		t.Fatalf("the act answered %d after the object moved, expected 409: %s", act.Code, act.Body.String())
	}
	if acts.ran != 0 {
		t.Fatalf("the act half ran %d time(s) against an object that is not the one the plan described", acts.ran)
	}

	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
		Plan   *struct {
			Token string `json:"token"`
			Facts struct {
				Precondition string `json:"precondition"`
			} `json:"facts"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(act.Body.Bytes(), &problem); err != nil {
		t.Fatalf("the refusal is not a problem object: %v", err)
	}
	if problem.Code != string(ProblemPlanChanged) {
		t.Errorf("code = %q, want %q", problem.Code, ProblemPlanChanged)
	}
	// The sentence names the change, because a 409 arriving with no earlier
	// mention of a version reads as a bug rather than as the guard working.
	if !strings.Contains(problem.Detail, "7") || !strings.Contains(problem.Detail, "8") {
		t.Errorf("the refusal does not say which version moved to which: %q", problem.Detail)
	}
	if problem.Plan == nil || problem.Plan.Token == "" {
		t.Fatal("the 409 carries no fresh plan; the front end is left with a spent token and nothing to show")
	}
	if problem.Plan.Facts.Precondition != "8" {
		t.Errorf("the fresh plan states precondition %q, want the version in effect now, 8",
			problem.Plan.Facts.Precondition)
	}
}

func TestTheAgreedPreconditionReachesTheActHalf(t *testing.T) {
	acts := &preconditionActs{version: "7"}
	act, _ := previewThenAct(t, acts, func() {})

	if act.Code != http.StatusOK {
		t.Fatalf("the act answered %d: %s", act.Code, act.Body.String())
	}
	if acts.ran != 1 {
		t.Fatalf("the act half ran %d time(s)", acts.ran)
	}
	if acts.handed.Precondition != "7" {
		t.Errorf("the backend was handed precondition %q; the plan promised the act would carry 7, "+
			"and a check that is compared here and dropped on the way through is the courtesy without the control",
			acts.handed.Precondition)
	}
}

// A client cannot name the precondition itself. Off the wire by tag, and the
// decoder refuses unknown members, so a body that tries is a 400 rather than a
// version this server would then trust.
func TestAClientCannotSupplyThePrecondition(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{Clusters: fakeClusters{}, Acts: &preconditionActs{version: "7"},
		AllowOrigin: func(*http.Request, string) bool { return true }}).Mount(mux)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/c/prod/actions/preview",
		strings.NewReader(`{"verb":"cordon","target":{"kind":"nodes","name":"node-a"},"precondition":"1"}`))
	request.Header.Set("X-KubeTower-Request", "1")
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("a body naming its own precondition answered %d, expected 400: %s", recorder.Code, recorder.Body.String())
	}
}

// A verb's plan is pinned to what its act depends on, and
// the 409 says what that was and what it is now - for a node, the taint that
// appeared rather than two version numbers.
func TestTheRefusalNamesWhatThePinIsOn(t *testing.T) {
	agreed := PlanFacts{
		Verb: "cordon", Count: 1, Targets: []PlanTarget{{Kind: "nodes", Name: "node-a"}},
		Precondition:   "unschedulable=false; taints=none",
		PreconditionOn: "spec.unschedulable and spec.taints",
	}
	now := agreed
	now.Precondition = "unschedulable=false; taints=dedicated=gpu:NoSchedule"

	if sameFacts(agreed, now) {
		t.Fatal("a taint added since the plan was made left it the same plan")
	}
	why := whyChanged(agreed, now, "otherwise")
	for _, want := range []string{"spec.unschedulable and spec.taints", "taints=none", "dedicated=gpu:NoSchedule", "were", "are"} {
		if !strings.Contains(why, want) {
			t.Errorf("the refusal does not say %q: %q", want, why)
		}
	}

	// A plan with no `preconditionOn` is pinned to the resourceVersion, and
	// says so: every plan that is not a verb's is still that.
	legacy := whyChanged(PlanFacts{Precondition: "7"}, PlanFacts{Precondition: "8"}, "otherwise")
	if !strings.Contains(legacy, "metadata.resourceVersion was 7 and is 8") {
		t.Errorf("a resourceVersion pin is not named as one: %q", legacy)
	}
}

// The same value on two different things is two different plans: a plan store
// shared across an upgrade must not compare a generation with a version.
func TestSameFactsComparesWhatThePinIsOn(t *testing.T) {
	agreed := PlanFacts{Verb: "scale", Count: 1, Precondition: "4", PreconditionOn: "metadata.generation"}
	other := agreed
	other.PreconditionOn = ""
	if sameFacts(agreed, other) {
		t.Fatal("generation 4 and resourceVersion 4 were the same plan")
	}
}

// A generation restarts at 1 when an object is
// deleted and created again, so facts drawn from the successor would match the
// agreed ones on every member sameFacts compared. The UID is a fact of its own.
func TestSameFactsSeesARecreatedObject(t *testing.T) {
	agreed := PlanFacts{
		Verb: "delete", Count: 1, Targets: []PlanTarget{{Kind: "deployments", Namespace: "prod", Name: "api"}},
		Precondition: "1", PreconditionOn: "metadata.generation", PreconditionUID: "uid-api",
	}
	successor := agreed
	successor.PreconditionUID = "uid-successor"
	if sameFacts(agreed, successor) {
		t.Fatal("a plan made on uid-api and facts drawn from uid-successor, both at generation 1, " +
			"were the same plan")
	}
	why := whyChanged(agreed, successor, "otherwise")
	for _, want := range []string{"changed while you had it open", "deleted and created again", "uid-api", "uid-successor"} {
		if !strings.Contains(why, want) {
			t.Errorf("the refusal does not say %q: %q", want, why)
		}
	}
}

// End to end through the handler pair: the object is recreated between the
// preview and the click at the same pin value, and the act half is never
// reached.
func TestAnActOnARecreatedObjectIsRefusedWithAFreshPlan(t *testing.T) {
	acts := &preconditionActs{version: "1", uid: "uid-a"}
	act, _ := previewThenAct(t, acts, func() { acts.uid = "uid-b" })
	if act.Code != http.StatusConflict {
		t.Fatalf("the act answered %d after the object was recreated, expected 409: %s", act.Code, act.Body.String())
	}
	if acts.ran != 0 {
		t.Fatalf("the act half ran %d time(s) against the successor of the object the plan described", acts.ran)
	}
}

func TestTheAgreedUIDReachesTheActHalf(t *testing.T) {
	acts := &preconditionActs{version: "1", uid: "uid-a"}
	act, _ := previewThenAct(t, acts, func() {})
	if act.Code != http.StatusOK {
		t.Fatalf("the act answered %d: %s", act.Code, act.Body.String())
	}
	if acts.handed.PreconditionUID != "uid-a" {
		t.Errorf("the backend was handed UID %q; the plan was made on uid-a, and a delete sent "+
			"without it takes its precondition from whatever it re-reads", acts.handed.PreconditionUID)
	}
}

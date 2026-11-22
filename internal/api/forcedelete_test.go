package api

import (
	"context"
	"net/http"
	"strconv"
	"testing"
)

// A single delete may be forced - deleted with a
// grace period of zero - from a box in its confirmation. The box is drawn from
// the plan's `forceable`, and the act's `force` is accepted only against a plan
// that says it. A refused force agreed to nothing, so like a mistyped gesture
// it leaves the plan where it was.

// forceActs is a backend whose plan says `forceable` when asked to, and which
// records the request the act half received.
type forceActs struct {
	fakeActs
	forceable bool
	ran       int
	got       ActionRequest
}

func (f *forceActs) PreviewAction(_ context.Context, _ string, req ActionRequest) (PlanFacts, error) {
	return PlanFacts{
		Verb: req.Verb, Targets: []PlanTarget{req.Target}, Count: 1, Destructive: true,
		Confirmation: &Confirmation{Kind: "acknowledge"},
		Forceable:    f.forceable,
	}, nil
}

func (f *forceActs) RunAction(_ context.Context, _ string, req ActionRequest, _ string) (ActOutcome, error) {
	f.ran++
	f.got = req
	return ActOutcome{Succeeded: 1, Results: []ActResult{{Target: req.Target, OK: true}}}, nil
}

func forcedBody(token string, force bool) string {
	return `{"token":` + strconv.Quote(token) + `,"force":` + strconv.FormatBool(force) + `}`
}

func TestAForcedDeleteReachesTheBackend(t *testing.T) {
	acts := &forceActs{forceable: true}
	post := actPoster(t, Deps{Clusters: fakeClusters{}, Acts: acts})

	token := tokenOf(t, post("/api/c/prod/actions/preview",
		`{"verb":"delete","target":{"kind":"pods","namespace":"prod","name":"api-1"}}`))
	if got := post("/api/c/prod/actions", forcedBody(token, true)); got.Code != http.StatusOK {
		t.Fatalf("a forced delete on a forceable plan answered %d: %s", got.Code, got.Body.String())
	}
	if acts.ran != 1 || !acts.got.Force {
		t.Fatalf("the act half ran %d time(s) with force=%v, want once with force", acts.ran, acts.got.Force)
	}

	plain := tokenOf(t, post("/api/c/prod/actions/preview",
		`{"verb":"delete","target":{"kind":"pods","namespace":"prod","name":"api-1"}}`))
	if got := post("/api/c/prod/actions", forcedBody(plain, false)); got.Code != http.StatusOK {
		t.Fatalf("an ordinary delete answered %d: %s", got.Code, got.Body.String())
	}
	if acts.ran != 2 || acts.got.Force {
		t.Fatalf("an ordinary delete reached the backend with force=%v", acts.got.Force)
	}
}

func TestForceOnAPlanThatIsNotForceableIsRefusedAndKeepsThePlan(t *testing.T) {
	acts := &forceActs{forceable: false}
	post := actPoster(t, Deps{Clusters: fakeClusters{}, Acts: acts})

	token := tokenOf(t, post("/api/c/prod/actions/preview",
		`{"verb":"cordon","target":{"kind":"nodes","name":"node-a"}}`))
	refused := post("/api/c/prod/actions", forcedBody(token, true))
	if refused.Code != http.StatusBadRequest {
		t.Fatalf("force on a plan without forceable answered %d: %s", refused.Code, refused.Body.String())
	}
	if acts.ran != 0 {
		t.Fatalf("a refused force reached the act half %d time(s)", acts.ran)
	}
	// Nothing was agreed to, so the plan is still there.
	if again := post("/api/c/prod/actions", forcedBody(token, false)); again.Code != http.StatusOK {
		t.Fatalf("the same plan without force answered %d: %s - the refused force spent it",
			again.Code, again.Body.String())
	}
}

func TestForceOnABulkPlanIsRefused(t *testing.T) {
	post := actPoster(t, Deps{Clusters: fakeClusters{}, Acts: fakeActs{}})
	token := tokenOf(t, post("/api/c/prod/bulk/preview",
		`{"verb":"delete","kind":"pods","targets":[{"kind":"pods","namespace":"prod","name":"a"},`+
			`{"kind":"pods","namespace":"prod","name":"b"}]}`))
	body := `{"token":` + strconv.Quote(token) + `,"confirmed":"2","force":true}`
	if got := post("/api/c/prod/bulk", body); got.Code != http.StatusBadRequest {
		t.Fatalf("force on a bulk delete answered %d: %s - a selection keeps its confirmation", got.Code, got.Body.String())
	}
}

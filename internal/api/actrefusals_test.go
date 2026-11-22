package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/sharedstate"
)

// The refusals of the confirm-then-act pairs other than runAction's, which
// precondition_test.go drives. Each pair re-derives its plan at the click and
// refuses when the cluster moved; each spends its token through spend or
// spendConfirmed, which refuse an empty token and say when the plan store did
// not answer. Every refusal below is checked to stop before the act half.

// movingActs answers every preview with the double's facts, plus a
// precondition once the test has moved the object, and counts the acts.
type movingActs struct {
	fakeActs
	moved bool
	acts  int
}

func (m *movingActs) move(f PlanFacts, err error) (PlanFacts, error) {
	if m.moved {
		f.Precondition = "moved since the preview"
	}
	return f, err
}

func (m *movingActs) PreviewData(ctx context.Context, cluster string, req DataWrite) (PlanFacts, error) {
	return m.move(m.fakeActs.PreviewData(ctx, cluster, req))
}

func (m *movingActs) PreviewManifest(ctx context.Context, cluster string, req ManifestWrite) (PlanFacts, error) {
	return m.move(m.fakeActs.PreviewManifest(ctx, cluster, req))
}

func (m *movingActs) PreviewCreate(ctx context.Context, cluster string, req ManifestWrite) (PlanFacts, error) {
	return m.move(m.fakeActs.PreviewCreate(ctx, cluster, req))
}

func (m *movingActs) PreviewBulk(ctx context.Context, cluster string, req BulkRequest) (PlanFacts, error) {
	return m.move(m.fakeActs.PreviewBulk(ctx, cluster, req))
}

func (m *movingActs) PreviewAction(ctx context.Context, cluster string, req ActionRequest) (PlanFacts, error) {
	return m.move(m.fakeActs.PreviewAction(ctx, cluster, req))
}

func (m *movingActs) ApplyData(ctx context.Context, cluster string, req DataWrite) (ActOutcome, error) {
	m.acts++
	return m.fakeActs.ApplyData(ctx, cluster, req)
}

func (m *movingActs) ApplyManifest(ctx context.Context, cluster string, req ManifestWrite) (ActOutcome, error) {
	m.acts++
	return m.fakeActs.ApplyManifest(ctx, cluster, req)
}

func (m *movingActs) ApplyCreate(ctx context.Context, cluster string, req ManifestWrite) (ActOutcome, error) {
	m.acts++
	return m.fakeActs.ApplyCreate(ctx, cluster, req)
}

func (m *movingActs) RunBulk(ctx context.Context, cluster string, req BulkRequest) (ActOutcome, error) {
	m.acts++
	return m.fakeActs.RunBulk(ctx, cluster, req)
}

func (m *movingActs) RunAction(ctx context.Context, cluster string, req ActionRequest, confirmed string) (ActOutcome, error) {
	m.acts++
	return m.fakeActs.RunAction(ctx, cluster, req, confirmed)
}

// pair is one confirm-then-act pair: where its preview and act are, a body
// the preview accepts, and the gesture its plan asks for.
type pair struct {
	name, preview, body, act, confirmed string
}

var pairs = []pair{
	{name: "data", preview: "/api/c/prod/data/preview", act: "/api/c/prod/data",
		body: `{"kind":"configmaps","namespace":"default","name":"app","resourceVersion":"310","entries":[{"key":"level","value":"debug"}]}`},
	{name: "manifest", preview: "/api/c/prod/manifest/preview", act: "/api/c/prod/manifest/apply",
		body: `{"document":"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n"}`},
	{name: "create", preview: "/api/c/prod/create/preview", act: "/api/c/prod/create/apply",
		body: `{"document":"apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n"}`},
	{name: "bulk", preview: "/api/c/prod/bulk/preview", act: "/api/c/prod/bulk", confirmed: "2",
		body: `{"verb":"delete","kind":"pods","targets":[` +
			`{"kind":"pods","namespace":"default","name":"a"},{"kind":"pods","namespace":"default","name":"b"}]}`},
	{name: "action", preview: "/api/c/prod/actions/preview", act: "/api/c/prod/actions", confirmed: "1",
		body: `{"verb":"delete","target":{"kind":"pods","namespace":"default","name":"api-1"}}`},
}

func actsOver(acts *movingActs, kv sharedstate.Store) *http.ServeMux {
	mux := http.NewServeMux()
	New(Deps{Clusters: fakeClusters{}, Acts: acts, Shared: kv,
		AllowOrigin: func(*http.Request, string) bool { return true }}).Mount(mux)
	return mux
}

func (p pair) previewToken(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	preview := postTo(mux, p.preview, p.body)
	if preview.Code != http.StatusOK {
		t.Fatalf("%s: the preview answered %d: %s", p.name, preview.Code, preview.Body.String())
	}
	var plan struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &plan); err != nil || plan.Token == "" {
		t.Fatalf("%s: the preview carried no token: %v %s", p.name, err, preview.Body.String())
	}
	return plan.Token
}

func (p pair) spend(mux *http.ServeMux, token string) *httptest.ResponseRecorder {
	body := `{"token":` + strconv.Quote(token)
	if p.confirmed != "" {
		body += `,"confirmed":` + strconv.Quote(p.confirmed)
	}
	return postTo(mux, p.act, body+`}`)
}

func problemOf(t *testing.T, name string, got *httptest.ResponseRecorder) Problem {
	t.Helper()
	var problem Problem
	if err := json.Unmarshal(got.Body.Bytes(), &problem); err != nil {
		t.Fatalf("%s: the refusal is not a problem object: %v %s", name, err, got.Body.String())
	}
	return problem
}

// Every pair whose object moved between the preview and the click answers 409
// `plan_changed` with a fresh plan, and its act half is never reached.
func TestEveryPairRefusesAnObjectChangedSinceThePreview(t *testing.T) {
	for _, p := range pairs {
		acts := &movingActs{}
		mux := actsOver(acts, nil)
		token := p.previewToken(t, mux)
		acts.moved = true
		got := p.spend(mux, token)
		if got.Code != http.StatusConflict {
			t.Errorf("%s: the act answered %d after the object moved, expected 409: %s",
				p.name, got.Code, got.Body.String())
			continue
		}
		problem := problemOf(t, p.name, got)
		if problem.Code != ProblemPlanChanged {
			t.Errorf("%s: code = %q, want %q", p.name, problem.Code, ProblemPlanChanged)
		}
		if problem.Plan == nil || problem.Plan.Token == "" {
			t.Errorf("%s: the 409 carries no fresh plan; the front end is left with a spent token", p.name)
		} else if problem.Plan.Facts.Precondition != "moved since the preview" {
			t.Errorf("%s: the fresh plan states %q, not the object as it is now",
				p.name, problem.Plan.Facts.Precondition)
		}
		if acts.acts != 0 {
			t.Errorf("%s: the act half ran %d time(s) against an object the plan no longer describes",
				p.name, acts.acts)
		}
	}
}

// The control: the same pairs, the object unmoved, act. Without it a pair that
// refused everything would pass the test above.
func TestEveryPairActsOnAnUnmovedObject(t *testing.T) {
	for _, p := range pairs {
		acts := &movingActs{}
		mux := actsOver(acts, nil)
		if got := p.spend(mux, p.previewToken(t, mux)); got.Code != http.StatusOK || acts.acts != 1 {
			t.Errorf("%s: the act answered %d and ran %d time(s), expected 200 and once: %s",
				p.name, got.Code, acts.acts, got.Body.String())
		}
	}
}

// An empty token is a 400 saying to preview first, not a 409 about a plan
// that never existed: spend for data, manifest and create, spendConfirmed for
// actions and bulk.
func TestAnEmptyTokenIsRefusedBeforeThePlanStore(t *testing.T) {
	for _, p := range pairs {
		acts := &movingActs{}
		got := p.spend(actsOver(acts, nil), "")
		if got.Code != http.StatusBadRequest {
			t.Errorf("%s: an empty token answered %d, expected 400: %s", p.name, got.Code, got.Body.String())
			continue
		}
		if problem := problemOf(t, p.name, got); problem.Detail != "token is required; preview first" {
			t.Errorf("%s: the refusal says %q", p.name, problem.Detail)
		}
		if acts.acts != 0 {
			t.Errorf("%s: the act half ran on an empty token", p.name)
		}
	}
}

// takeless is a store that keeps plans and reads them, but does not answer
// when one is spent: the preview, and spendConfirmed's peek, succeed, and only
// spend's take fails.
type takeless struct{ sharedstate.Store }

func (takeless) Take(context.Context, string) ([]byte, bool, error) {
	return nil, false, sharedstate.ErrUnreachable
}

// A store that does not answer when the token is spent is a 503 naming it, on
// every pair; never the 409 that would send the person back to a preview that
// fails the same way.
func TestAPlanStoreThatDoesNotAnswerAtTheClickIsA503(t *testing.T) {
	for _, p := range pairs {
		acts := &movingActs{}
		mux := actsOver(acts, takeless{sharedstate.NewMemory(time.Now)})
		got := p.spend(mux, p.previewToken(t, mux))
		if problem := problemOf(t, p.name, got); got.Code != http.StatusServiceUnavailable ||
			problem.Code != ProblemStoreUnreachable {
			t.Errorf("%s: the act answered %d %q when the store did not answer, expected 503 %q",
				p.name, got.Code, problem.Code, ProblemStoreUnreachable)
		}
		if acts.acts != 0 {
			t.Errorf("%s: the act half ran on a plan the store did not give back", p.name)
		}
	}
}

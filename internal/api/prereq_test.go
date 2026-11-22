package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The prerequisite install's three operations through the router: the plan,
// the gesture checked before the token is spent, the stale and foreign
// tokens, and the re-derivation that refuses an install whose document moved.
// Every refusal is checked to stop before the act.

// countingPrereqs is fakePrereqs that can move its document between the
// preview and the act, and counts the installs.
type countingPrereqs struct {
	fakePrereqs
	request  string
	installs int
	into     string
}

func (c *countingPrereqs) PreviewPrereq(ctx context.Context, cluster, key string) (PlanFacts, error) {
	facts, err := c.fakePrereqs.PreviewPrereq(ctx, cluster, key)
	if c.request != "" {
		facts.Request = c.request
	}
	return facts, err
}

func (c *countingPrereqs) InstallPrereq(ctx context.Context, cluster, key, namespace string) (ActOutcome, error) {
	c.installs++
	c.into = namespace
	return c.fakePrereqs.InstallPrereq(ctx, cluster, key, namespace)
}

type prereqRig struct {
	t       *testing.T
	mux     *http.ServeMux
	backend *countingPrereqs
	now     time.Time
}

func newPrereqRig(t *testing.T) *prereqRig {
	t.Helper()
	rig := &prereqRig{t: t, mux: http.NewServeMux(), backend: &countingPrereqs{},
		now: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)}
	New(Deps{Clusters: fakeClusters{}, Acts: fakeActs{}, Prereqs: rig.backend,
		Now:         func() time.Time { return rig.now },
		AllowOrigin: func(*http.Request, string) bool { return true }}).Mount(rig.mux)
	return rig
}

func (r *prereqRig) post(path, body string) *httptest.ResponseRecorder {
	r.t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("X-KubeTower-Request", "1")
	r.mux.ServeHTTP(recorder, request)
	return recorder
}

func (r *prereqRig) preview() string {
	r.t.Helper()
	answer := r.post("/api/c/prod/prereq/trivy-operator/preview", "")
	if answer.Code != http.StatusOK {
		r.t.Fatalf("the preview answered %d: %s", answer.Code, answer.Body.String())
	}
	var plan ConfirmationPlan
	if err := json.Unmarshal(answer.Body.Bytes(), &plan); err != nil {
		r.t.Fatal(err)
	}
	if plan.Facts.Request != fakePrereqYAML || plan.Facts.Confirmation == nil || plan.Facts.Confirmation.Kind != "name" {
		r.t.Fatalf("the plan is %+v", plan.Facts)
	}
	return plan.Token
}

func installBody(token, confirmed string) string {
	return `{"token":` + strconv.Quote(token) + `,"confirmed":` + strconv.Quote(confirmed) + `}`
}

func problemCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var problem struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &problem)
	return problem.Code
}

const installPath = "/api/c/prod/prereq/trivy-operator/install"

func TestAPrereqInstallActsOnTheConfirmedPlanOnce(t *testing.T) {
	rig := newPrereqRig(t)
	token := rig.preview()

	first := rig.post(installPath, installBody(token, "trivy-operator"))
	if first.Code != http.StatusOK {
		t.Fatalf("the install answered %d: %s", first.Code, first.Body.String())
	}
	// The namespace is the plan's, which the console found; no client sent it.
	if rig.backend.installs != 1 || rig.backend.into != "argocd" {
		t.Errorf("installs %d into %q, want 1 into argocd", rig.backend.installs, rig.backend.into)
	}
	second := rig.post(installPath, installBody(token, "trivy-operator"))
	if second.Code != http.StatusConflict || rig.backend.installs != 1 {
		t.Errorf("a spent token answered %d and installed %d times", second.Code, rig.backend.installs)
	}
}

// The gesture is checked before the token is taken: a missing or wrong name
// agreed to nothing, so the same plan can still be confirmed.
func TestAPrereqInstallWithoutItsConfirmationIsRefusedAndKeepsThePlan(t *testing.T) {
	rig := newPrereqRig(t)
	token := rig.preview()

	for _, typed := range []string{"", "trivy", "Trivy-Operator"} {
		answer := rig.post(installPath, installBody(token, typed))
		if answer.Code != http.StatusBadRequest {
			t.Errorf("confirmed %q answered %d, want 400: %s", typed, answer.Code, answer.Body.String())
		}
	}
	if answer := rig.post(installPath, `{"token":`+strconv.Quote(token)+`}`); answer.Code != http.StatusBadRequest {
		t.Errorf("no confirmation at all answered %d, want 400", answer.Code)
	}
	if rig.backend.installs != 0 {
		t.Fatalf("an unconfirmed install reached the backend %d times", rig.backend.installs)
	}
	if answer := rig.post(installPath, installBody(token, "trivy-operator")); answer.Code != http.StatusOK {
		t.Errorf("the plan did not survive the refused gestures: %d %s", answer.Code, answer.Body.String())
	}
}

func TestAnExpiredOrForeignTokenInstallsNothing(t *testing.T) {
	rig := newPrereqRig(t)

	// Expired: past the plan's life.
	token := rig.preview()
	rig.now = rig.now.Add(planLife + time.Second)
	expired := rig.post(installPath, installBody(token, "trivy-operator"))
	if expired.Code != http.StatusConflict || problemCode(t, expired) != string(ProblemPlanExpired) {
		t.Errorf("an expired token answered %d %s", expired.Code, expired.Body.String())
	}

	// A token minted for a create cannot install.
	create := rig.post("/api/c/prod/create/preview",
		`{"document":"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n  namespace: default\n"}`)
	var plan ConfirmationPlan
	_ = json.Unmarshal(create.Body.Bytes(), &plan)
	if plan.Token == "" {
		t.Fatalf("the create preview answered %d", create.Code)
	}
	foreign := rig.post(installPath, installBody(plan.Token, "trivy-operator"))
	if foreign.Code != http.StatusConflict {
		t.Errorf("a create's token answered %d at the install", foreign.Code)
	}

	// No token.
	if none := rig.post(installPath, `{"token":"","confirmed":"trivy-operator"}`); none.Code != http.StatusBadRequest {
		t.Errorf("an empty token answered %d", none.Code)
	}

	// A plan for this key, spent at another key's address.
	token = rig.preview()
	other := rig.post("/api/c/prod/prereq/something-else/install", installBody(token, "trivy-operator"))
	if other.Code != http.StatusConflict || problemCode(t, other) != string(ProblemPlanChanged) {
		t.Errorf("a plan spent at another key answered %d %s", other.Code, other.Body.String())
	}
	if rig.backend.installs != 0 {
		t.Errorf("%d installs went ahead", rig.backend.installs)
	}
}

// The document sent is the one confirmed: a re-derived plan whose YAML differs
// - another served version of the kind, say - is a 409 with the fresh plan.
func TestAnInstallWhoseDocumentMovedIsReplanned(t *testing.T) {
	rig := newPrereqRig(t)
	token := rig.preview()
	rig.backend.request = strings.Replace(fakePrereqYAML, "v1alpha1", "v1beta1", 1)

	answer := rig.post(installPath, installBody(token, "trivy-operator"))
	if answer.Code != http.StatusConflict || problemCode(t, answer) != string(ProblemPlanChanged) {
		t.Fatalf("a moved document answered %d %s", answer.Code, answer.Body.String())
	}
	if !strings.Contains(answer.Body.String(), "v1beta1") {
		t.Errorf("the 409 does not carry the fresh plan: %s", answer.Body.String())
	}
	if rig.backend.installs != 0 {
		t.Errorf("an install went ahead on a plan nobody read")
	}
}

func TestAPrereqReadWithNoInstallerSaysAbsent(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{Clusters: fakeClusters{}}).Mount(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/c/prod/prereq/trivy-operator", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"state":"absent"`) {
		t.Errorf("answered %d %s", recorder.Code, recorder.Body.String())
	}
	bad := httptest.NewRecorder()
	mux.ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/api/c/prod/prereq/Trivy_Operator", nil))
	if bad.Code != http.StatusBadRequest {
		t.Errorf("a malformed key answered %d", bad.Code)
	}
}

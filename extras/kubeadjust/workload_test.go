package kubeadjust

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
)

// The tab in a workload's panel.

// getWorkload serves the workload route.
func getWorkload(t *testing.T, h extension.Host, kind, namespace, name string) *httptest.ResponseRecorder {
	t.Helper()
	var handler http.Handler
	for _, r := range testExtra().Routes(h) {
		if r.Pattern == "/workload/{kind}/{namespace}/{name}" {
			handler = r.Handler
		}
	}
	if handler == nil {
		t.Fatal("no workload route")
	}
	req := httptest.NewRequest(http.MethodGet, "/x/docker-desktop/kubeadjust/workload/"+kind+"/"+namespace+"/"+name, nil)
	req.SetPathValue("cluster", "docker-desktop")
	req.SetPathValue("kind", kind)
	req.SetPathValue("namespace", namespace)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func workloadTab(t *testing.T, h extension.Host, kind, namespace, name string) workloadAnswer {
	t.Helper()
	rec := getWorkload(t, h, kind, namespace, name)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET workload/%s/%s/%s = %d %s", kind, namespace, name, rec.Code, rec.Body.String())
	}
	var out workloadAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func deployment(namespace, name string) *appsv1.Deployment {
	return &appsv1.Deployment{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
}

// shopWithDeployment is shop with its Deployment `api` itself in the cluster.
func shopWithDeployment(t *testing.T) *fakeHost {
	h := shop(t)
	h.add("deployments", deployment("shop", "api"))
	return h
}

// The tab is the page's card for this workload, not a second computation: the
// same bars, findings, confidence and commands as `GET /?ns=shop` draws.
func TestTheWorkloadTabIsThePagesCardForThatWorkload(t *testing.T) {
	h := shopWithDeployment(t)
	a := workloadTab(t, h, "deployments", "shop", "api")
	if !a.Ready || a.Card == nil {
		t.Fatalf("a measured workload with pods drew no card: %+v", a)
	}
	if a.Kind != "Deployment" || a.Name != "api" || a.Namespace != "shop" || len(a.Notes) != 4 {
		t.Errorf("identity or notes: %+v", a)
	}

	p := page(t, h, map[string][]string{"ns": {"shop"}})
	want, _ := json.Marshal(p.Cards[0])
	got, _ := json.Marshal(*a.Card)
	if string(want) != string(got) {
		t.Errorf("the tab's card differs from the page's:\n tab  %s\n page %s", got, want)
	}

	c := a.Card.Containers[0]
	if !c.OOMKilled {
		t.Errorf("the OOM kill is not remembered on the tab: %+v", c)
	}
	if len(a.Card.Findings) == 0 || a.Card.Findings[0].Confidence == "" {
		t.Errorf("findings without their confidence: %+v", a.Card.Findings)
	}
	if len(a.Commands) == 0 || !strings.HasPrefix(a.Commands[0], "kubectl set resources deployment/api") {
		t.Errorf("commands = %v", a.Commands)
	}
	for _, cmd := range a.Commands {
		if !slices.Contains(p.Commands, cmd) {
			t.Errorf("the tab carries %q, a command the page does not", cmd)
		}
	}
}

// The tab reads the workload's namespace and nothing wider.
func TestTheWorkloadTabReadsOnlyItsNamespace(t *testing.T) {
	h := shopWithDeployment(t)
	workloadTab(t, h, "deployments", "shop", "api")
	for _, q := range h.queries {
		if q.GVR.Resource == "nodes" || q.GVR.Resource == "namespaces" {
			continue // cluster-scoped: the page's notes need them
		}
		if q.Namespace != "shop" {
			t.Errorf("%s listed in %q, want only shop", q.GVR.Resource, q.Namespace)
		}
	}
	if !slices.Equal(h.windowFor, []string{"shop"}) {
		t.Errorf("windows asked for %v, want shop only", h.windowFor)
	}
}

// A workload that is there with no pod is a 200 with no card; one that is not
// there is a 404; a kind the tab is not drawn on is a 404 too.
func TestAWorkloadWithNoPodIsEmptyAndAnUnknownOneIs404(t *testing.T) {
	h := shopWithDeployment(t)
	h.add("deployments", deployment("shop", "idle"))
	a := workloadTab(t, h, "deployments", "shop", "idle")
	if !a.Ready || a.Card != nil || len(a.Commands) != 0 {
		t.Errorf("a workload with no pod: %+v", a)
	}

	for _, c := range []struct{ kind, ns, name string }{
		{"deployments", "shop", "gone"},
		{"deployments", "data", "api"},  // the right name, the wrong namespace
		{"statefulsets", "shop", "api"}, // the right name, the wrong kind
		{"replicasets", "shop", "api-7d9f"},
		{"pods", "shop", "api-7d9f-a"},
	} {
		rec := getWorkload(t, h, c.kind, c.ns, c.name)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), CodeUnknownWorkload) {
			t.Errorf("%s %s/%s = %d %s, want 404", c.kind, c.ns, c.name, rec.Code, rec.Body.String())
		}
	}
	if rec := getWorkload(t, h, "deployments", "Not_A_Namespace", "api"); rec.Code != http.StatusBadRequest {
		t.Errorf("a namespace that cannot be one = %d", rec.Code)
	}
	if rec := getWorkload(t, h, "deployments", "shop", "Not_A_Name"); rec.Code != http.StatusBadRequest {
		t.Errorf("a name that cannot be one = %d", rec.Code)
	}
}

// Nothing measured is not a well-sized workload: no card, and Ready false.
func TestAnUnmeasuredWorkloadIsNotDrawnAsSizedRight(t *testing.T) {
	h := shopWithDeployment(t)
	h.usageErr = &extension.MetricsUnavailable{Reason: extension.MetricsAbsent, Detail: "the cluster has no metrics-server"}
	a := workloadTab(t, h, "deployments", "shop", "api")
	if a.Ready || a.Card != nil {
		t.Errorf("an unmeasured workload: %+v", a)
	}
	if u := a.Notes[0]; u.OK || u.Detail == "" {
		t.Errorf("the Usage note does not say why: %+v", u)
	}
}

// A person narrowed to their namespaces sees a workload in one of them whole,
// and is refused - never handed an empty tab - in another.
func TestANarrowedPersonSeesTheirWorkloadAndIsRefusedOthers(t *testing.T) {
	h := shopWithDeployment(t)
	h.allowed = []string{"shop"}
	h.nodesListable = true
	if a := workloadTab(t, h, "deployments", "shop", "api"); a.Card == nil {
		t.Errorf("the person's own workload drew no card: %+v", a)
	}

	h.listErr = map[string]error{"deployments": apierrors.NewForbidden(
		schema.GroupResource{Group: "apps", Resource: "deployments"}, "", errors.New("RBAC"))}
	rec := getWorkload(t, h, "deployments", "data", "db")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), CodeForbidden) {
		t.Errorf("a namespace the person may not read = %d %s, want 403", rec.Code, rec.Body.String())
	}
}

// A cluster that is gone is a 404 with the console's sentence; a workload that
// could not be read is a 502, never a 404 saying it is not there.
func TestAGoneClusterIs404AndAnUnreadableWorkloadIs502(t *testing.T) {
	h := shopWithDeployment(t)
	h.unknown = true
	if rec := getWorkload(t, h, "deployments", "shop", "api"); rec.Code != http.StatusNotFound ||
		!strings.Contains(rec.Body.String(), CodeUnknownCluster) {
		t.Errorf("gone cluster = %d %s", rec.Code, rec.Body.String())
	}
	h = shopWithDeployment(t)
	h.listErr = map[string]error{"deployments": errors.New("connection refused")}
	if rec := getWorkload(t, h, "deployments", "shop", "api"); rec.Code != http.StatusBadGateway {
		t.Errorf("unreadable workload = %d %s", rec.Code, rec.Body.String())
	}
}

// A CronJob's tab is its Jobs' pods, filed under it through the Job they belong
// to, with a command that reaches its job template.
func TestACronJobsTabIsItsJobsPods(t *testing.T) {
	h := shopWithDeployment(t)
	h.add("cronjobs", &batchv1.CronJob{TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "CronJob"},
		ObjectMeta: metav1.ObjectMeta{Name: "report", Namespace: "shop"}})
	yes := true
	h.add("jobs", &metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{Name: "report-2900", Namespace: "shop", OwnerReferences: []metav1.OwnerReference{
			{APIVersion: "batch/v1", Kind: "CronJob", Name: "report", UID: "c", Controller: &yes}}},
	})
	pod := runningPod("shop", "report-2900-x", "", "worker-1", app("1", "2Gi", "", ""))
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "report-2900", UID: "j", Controller: &yes}}
	h.add("pods", pod)
	h.usage.Containers["shop/report-2900-x/app"] = extension.Resources{CPUMillicores: 20, MemoryBytes: 64 << 20}

	a := workloadTab(t, h, "cronjobs", "shop", "report")
	if a.Kind != "CronJob" || a.Card == nil || a.Card.Workload != "report" || a.Card.Kind != "CronJob" {
		t.Fatalf("the CronJob's card: %+v", a)
	}
	for _, cmd := range a.Commands {
		if !strings.HasPrefix(cmd, "kubectl set resources cronjob/report ") {
			t.Errorf("command %q does not address the CronJob", cmd)
		}
	}
	if rec := getWorkload(t, h, "cronjobs", "shop", "nightly"); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown CronJob = %d", rec.Code)
	}
}

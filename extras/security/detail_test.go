package security

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/thonicdev/kubetower/extension"
)

// The detail reads the chosen reports' bodies by label selector in the
// workload's namespace, and sends a summary: titles clipped, no description,
// failed checks only, secrets without their match.
func TestTheDetailSummarisesTheReportsItChose(t *testing.T) {
	h := shop(t)
	// An older ReplicaSet's report that must not be read into the detail.
	h.add("replicasets", replicaSet("shop", "web-0", "web", 0))
	h.add(KindVulnerability, vulnReport("replicaset-web-0-app", scan{"shop", "ReplicaSet", "web-0", "app"}, "shop/app", "1.0", Counts{Critical: 50}))
	h.add(KindExposedSecret, secretReport("replicaset-web-1-app", scan{"shop", "ReplicaSet", "web-1", "app"}, "shop/app", "2.0", Counts{Critical: 2}))
	rec := serve(t, h, "/workload/shop/Deployment/web")
	d := decode[Detail](t, rec)
	body := rec.Body.String()
	if strings.Contains(body, "DESCRIPTION-NEVER-SENT") || strings.Contains(body, "MATCH-NEVER-SENT") || strings.Contains(body, `"match"`) {
		t.Fatalf("a description or a secret's match reached the answer")
	}
	if d.Workload != (WorkloadRef{Namespace: "shop", Kind: "Deployment", Name: "web"}) || d.CVEsTotal != 6 || len(d.CVEs) != 6 {
		t.Fatalf("workload %+v, cves %d of %d", d.Workload, len(d.CVEs), d.CVEsTotal)
	}
	if len(d.Containers) != 1 || d.Containers[0].Image != "shop/app:2.0" || d.Containers[0].Container != "app" ||
		d.Containers[0].OS != "debian 12.5" || d.Containers[0].EOSL == nil || *d.Containers[0].EOSL ||
		d.Containers[0].Counts != (Counts{Critical: 2, High: 3, Medium: 1}) || d.Containers[0].Scanned == "" {
		t.Errorf("containers %+v", d.Containers)
	}
	first := d.CVEs[0]
	if first.Severity != "CRITICAL" || first.Score == nil || *first.Score != 9.8 || first.Link == "" || first.Container != "app" ||
		utf8.RuneCountInString(first.Title) > MaxTitle || !strings.HasSuffix(first.Title, "…") {
		t.Errorf("first cve %+v", first)
	}
	if d.CVEs[5].Severity != "MEDIUM" {
		t.Errorf("not the worst first: %+v", d.CVEs)
	}
	if len(d.Checks) != 3 || d.Checks[0].Severity != "HIGH" || d.Checks[0].Category == "" {
		t.Errorf("checks %+v: only the failed ones, the worst first", d.Checks)
	}
	for _, c := range d.Checks {
		if strings.HasPrefix(c.ID, "KSV90") {
			t.Errorf("a passing check was sent: %+v", c)
		}
	}
	if len(d.Secrets) != 2 || d.Secrets[0].Rule != "aws-secret-access-key" || d.Secrets[0].Target == "" {
		t.Errorf("secrets %+v", d.Secrets)
	}
	whole := 0
	for _, q := range h.reportQueries(KindVulnerability) {
		if q.Columns {
			continue
		}
		whole++
		if q.Namespace != "shop" || q.LabelSelector != labelKind+"=ReplicaSet,"+labelName+"=web-1" {
			t.Errorf("body read %+v", q)
		}
	}
	if whole != 1 {
		t.Errorf("%d body reads of vulnerability reports, want 1", whole)
	}
}

// A report whose name is only a hash in its label is read by its own name.
func TestAHashedReportIsReadByName(t *testing.T) {
	h := newHost(t)
	long := "a-very-long-statefulset-name-that-goes-on-and-on-past-sixty-three-x"
	rep := vulnReport("statefulset-hashed", scan{"ns", "StatefulSet", long, "app"}, "img", "1", Counts{High: 1})
	delete(rep.labels, labelName)
	rep.labels[labelNameHash] = "abc"
	h.add(KindVulnerability, rep)
	d := decode[Detail](t, serve(t, h, "/workload/ns/StatefulSet/"+long))
	if d.CVEsTotal != 1 {
		t.Errorf("cves %d", d.CVEsTotal)
	}
	for _, q := range h.reportQueries(KindVulnerability) {
		if !q.Columns && q.FieldSelector != "metadata.name=statefulset-hashed" {
			t.Errorf("read %+v", q)
		}
	}
}

// More CVEs than a detail sends: the worst MaxCVEs, and the total said.
func TestTheDetailIsCutAtMaxCVEs(t *testing.T) {
	h := newHost(t)
	h.add(KindVulnerability, vulnReport("pod-p-app", scan{"ns", "Pod", "p", "app"}, "img", "1", Counts{Critical: 10, Low: MaxCVEs + 5}))
	d := decode[Detail](t, serve(t, h, "/workload/ns/Pod/p"))
	if d.CVEsTotal != MaxCVEs+15 || len(d.CVEs) != MaxCVEs || d.CVEs[0].Severity != "CRITICAL" {
		t.Errorf("total %d listed %d first %s", d.CVEsTotal, len(d.CVEs), d.CVEs[0].Severity)
	}
}

// A workload no report covers answers its sources and empty lists, and a body
// read refused for the person is its source's state.
func TestTheDetailOfAnUnscannedOrRefusedWorkload(t *testing.T) {
	h := shop(t)
	d := decode[Detail](t, serve(t, h, "/workload/shop/Deployment/nothing"))
	if len(d.Containers) != 0 || d.CVEsTotal != 0 || len(d.Sources) != 3 {
		t.Errorf("%+v", d)
	}
	problemOf(t, serve(t, h, "/workload/shop/Ingress/web"), http.StatusBadRequest, CodeBadRequest)
	problemOf(t, serve(t, h, "/workload/shop/Deployment/Bad_Name"), http.StatusBadRequest, CodeBadRequest)

	// The table read is allowed and the body read refused: the source says so.
	r := refusingBodies{shop(t)}
	d = decode[Detail](t, serve(t, r, "/workload/shop/Deployment/web"))
	if d.Sources[0].State != StateDenied || len(d.CVEs) != 0 {
		t.Errorf("source %+v cves %d", d.Sources[0], len(d.CVEs))
	}
}

// refusingBodies refuses every whole-object read of a report.
type refusingBodies struct{ *fakeHost }

func (r refusingBodies) List(req *http.Request, c string, q extension.ListQuery) (extension.Page, error) {
	if !q.Columns && strings.HasSuffix(q.GVR.Resource, "reports") {
		return extension.Page{}, fmt.Errorf("x: %w", extension.ErrDenied)
	}
	return r.fakeHost.List(req, c, q)
}

// Cells: the worst severity's count, toned, with every count and the image in
// the hint, linked to the workload's detail; pods stand for their workload.
func TestCellsAndBriefs(t *testing.T) {
	h := shop(t)
	h.add("pods", pod("tools", "debug", nil, "", ""))
	h.add(KindVulnerability, vulnReport("pod-debug-sh", scan{"tools", "Pod", "debug", "sh"}, "busybox", "1", Counts{}))
	cells := decode[map[string]Cell](t, serve(t, h, "/cells?kind=pods"))
	web := cells["shop/web-1-a"]
	if web.Text != "2 critical" || web.Tone != "bad" || web.Href != "/c/kind/x/security/shop/Deployment/web" ||
		web.Hint != "Critical 2 · High 3 · Medium 1 · Low 0 · Unknown 0 · shop/app:2.0" {
		t.Errorf("web pod %+v", web)
	}
	if c := cells["tools/debug"]; c.Text != "clean" || c.Tone != "ok" || c.Href != "/c/kind/x/security/tools/Pod/debug" {
		t.Errorf("clean pod %+v", c)
	}
	if c := cells["shop/worker-1-a"]; c.Text != "4 medium" && c.Text != "clean" {
		t.Logf("worker %+v", c)
	}
	if len(cells) != 4 {
		t.Errorf("cells %v", cells)
	}
	deps := decode[map[string]Cell](t, serve(t, h, "/cells?kind=deployments"))
	if len(deps) != 3 || deps["shop/api"].Text != "2 critical" || deps["shop/worker"].Tone != "ok" {
		t.Errorf("deployments %+v", deps)
	}
	sets := decode[map[string]Cell](t, serve(t, h, "/cells?kind=replicasets"))
	if c := sets["shop/web-1"]; c.Text != "2 critical" || c.Href != "/c/kind/x/security/shop/Deployment/web" {
		t.Errorf("replicaset cell %+v", c)
	}
	problemOf(t, serve(t, h, "/cells?kind=services"), http.StatusBadRequest, CodeBadRequest)

	b := decode[Brief](t, serve(t, h, "/brief/shop/web?kind=deployments"))
	if !b.Scanned || b.Counts == nil || b.Counts.Critical != 2 || b.Image != "shop/app:2.0" || b.Href != "/c/kind/x/security/shop/Deployment/web" {
		t.Errorf("brief %+v", b)
	}
	b = decode[Brief](t, serve(t, h, "/brief/shop/api-1-a?kind=pods"))
	if !b.Scanned || b.Workload.Kind != "Deployment" || b.Workload.Name != "api" {
		t.Errorf("pod brief %+v", b)
	}

	// The vulnerability reports refused: a refusal, never an empty column.
	h.listErr[KindVulnerability] = fmt.Errorf("x: %w", extension.ErrDenied)
	problemOf(t, serve(t, h, "/cells?kind=pods"), http.StatusForbidden, CodeListForbidden)
	problemOf(t, serve(t, h, "/brief/shop/web?kind=deployments"), http.StatusForbidden, CodeListForbidden)
	h.listErr[KindVulnerability] = errors.New("boom")
	problemOf(t, serve(t, h, "/cells?kind=pods"), http.StatusBadGateway, CodeListFailed)
}

func TestSuggest(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	h := newHost(t)
	if s, err := New().Suggest(r, h, "kind"); s != "" || err != nil {
		t.Errorf("no reports: %q %v", s, err)
	}
	h.add(KindVulnerability, vulnReport("pod-p-app", scan{"ns", "Pod", "p", "app"}, "img", "1", Counts{High: 1}))
	s, err := New().Suggest(r, h, "kind")
	if s == "" || err != nil {
		t.Errorf("reports: %q %v", s, err)
	}
	for _, q := range h.queries {
		if !q.Columns || q.Limit != 1 {
			t.Errorf("the suggestion read %+v, want one table row", q)
		}
	}
	h.reg.served = map[string]bool{}
	if s, err := New().Suggest(r, h, "kind"); s != "" || err != nil {
		t.Errorf("not served: %q %v", s, err)
	}
	h.reg.served = map[string]bool{KindVulnerability: true}
	h.listErr[KindVulnerability] = fmt.Errorf("x: %w", extension.ErrDenied)
	if _, err := New().Suggest(r, h, "kind"); !errors.Is(err, extension.ErrDenied) {
		t.Errorf("a refusal is an error: %v", err)
	}
	h.reg.errs = map[string]error{KindVulnerability + "." + group: context.DeadlineExceeded}
	if _, err := New().Suggest(r, h, "kind"); err == nil {
		t.Errorf("a failed lookup is an error")
	}
}

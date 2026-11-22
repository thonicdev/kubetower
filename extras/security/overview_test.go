package security

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/extension"
)

// shop is a small cluster: a web Deployment behind a LoadBalancer, an api
// behind an Ingress, a worker no one reaches, all three on one image for two
// of them.
func shop(t *testing.T) *fakeHost {
	h := newHost(t)
	h.add("replicasets", replicaSet("shop", "web-1", "web", 1), replicaSet("shop", "api-1", "api", 1), replicaSet("shop", "worker-1", "worker", 1))
	h.add("pods",
		pod("shop", "web-1-a", map[string]string{"app": "web"}, "ReplicaSet", "web-1"),
		pod("shop", "api-1-a", map[string]string{"app": "api"}, "ReplicaSet", "api-1"),
		pod("shop", "worker-1-a", map[string]string{"app": "worker"}, "ReplicaSet", "worker-1"))
	h.add("services",
		svc("shop", "web", "LoadBalancer", map[string]string{"app": "web"}),
		svc("shop", "api", "ClusterIP", map[string]string{"app": "api"}),
		svc("shop", "worker", "ClusterIP", map[string]string{"app": "worker"}))
	h.add("ingresses", ing("shop", "public", "api"))
	h.add(KindVulnerability,
		vulnReport("replicaset-web-1-app", scan{"shop", "ReplicaSet", "web-1", "app"}, "shop/app", "2.0", Counts{Critical: 2, High: 3, Medium: 1}),
		vulnReport("replicaset-api-1-app", scan{"shop", "ReplicaSet", "api-1", "app"}, "shop/app", "2.0", Counts{Critical: 2, High: 3, Medium: 1}),
		vulnReport("replicaset-worker-1-app", scan{"shop", "ReplicaSet", "worker-1", "app"}, "shop/worker", "1.0", Counts{Medium: 4, Low: 2}))
	h.add(KindConfigAudit, configReport("replicaset-web-1", scan{"shop", "ReplicaSet", "web-1", ""}, Counts{High: 1, Low: 2}, 3))
	h.add(KindExposedSecret, secretReport("replicaset-worker-1-app", scan{"shop", "ReplicaSet", "worker-1", "app"}, "shop/worker", "1.0", Counts{Critical: 1}))
	return h
}

// Exposure is a pod a LoadBalancer or NodePort Service selects, or one a
// Service an Ingress names selects; the answer names which.
func TestExposureNamesTheServiceOrTheIngress(t *testing.T) {
	o := decode[Overview](t, serve(t, shop(t), "/"))
	web, api, worker := rowOf(t, o, "Deployment", "web"), rowOf(t, o, "Deployment", "api"), rowOf(t, o, "Deployment", "worker")
	if !web.Exposed || web.Exposure != "Service shop/web (LoadBalancer)" {
		t.Errorf("web: %+v", web)
	}
	if !api.Exposed || api.Exposure != "Ingress shop/public" {
		t.Errorf("api: %+v", api)
	}
	if worker.Exposed || worker.Exposure != "" {
		t.Errorf("a ClusterIP no Ingress names exposes nothing: %+v", worker)
	}
	d := decode[Detail](t, serve(t, shop(t), "/workload/shop/Deployment/api"))
	if strings.Join(d.Exposure, ";") != "Ingress shop/public" {
		t.Errorf("detail exposure %v", d.Exposure)
	}
}

// One image run by two workloads is counted once in the totals and in the
// namespace's bar, and the image says it runs in two.
func TestTotalsCountAnImageOnce(t *testing.T) {
	o := decode[Overview](t, serve(t, shop(t), "/"))
	if o.Totals != (Counts{Critical: 2, High: 3, Medium: 5, Low: 2}) {
		t.Errorf("totals %+v", o.Totals)
	}
	if o.Affected != (Affected{Critical: 2, High: 2, Medium: 3}) {
		t.Errorf("affected %+v", o.Affected)
	}
	if o.ImageCount != 1 || o.ExposedCount != 2 || o.SecretCount != 1 {
		t.Errorf("images %d exposed %d secrets %d", o.ImageCount, o.ExposedCount, o.SecretCount)
	}
	if len(o.Images) != 2 || o.Images[0].Image != "shop/app:2.0" || o.Images[0].Workloads != 2 {
		t.Errorf("images %+v", o.Images)
	}
	if len(o.Namespaces) != 1 || o.Namespaces[0].Counts != o.Totals || o.Namespaces[0].Workloads != 3 {
		t.Errorf("namespaces %+v", o.Namespaces)
	}
	web := rowOf(t, o, "Deployment", "web")
	if web.Config == nil || *web.Config != (Counts{High: 1, Low: 2}) || web.Secrets != nil {
		t.Errorf("web's config and secrets %+v %v", web.Config, web.Secrets)
	}
	if w := rowOf(t, o, "Deployment", "worker"); w.Secrets == nil || *w.Secrets != 1 || w.Config != nil {
		t.Errorf("worker's secrets %v config %v", w.Secrets, w.Config)
	}
	// The worst first: the two with criticals, then the worker.
	if o.Workloads[2].Name != "worker" || o.Workloads[0].Name != "api" {
		t.Errorf("order %v", names(o.Workloads))
	}
}

func names(rows []WorkloadRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Kind+"/"+r.Name)
	}
	return out
}

// The overview reads every report as a table, never a body, and at most four
// reads at once; every list it asks is a GET through Host.List.
func TestTheOverviewReadsTablesOnlyFourAtATime(t *testing.T) {
	h := shop(t)
	h.delay = 5e6 // 5 ms, so the reads overlap
	_ = decode[Overview](t, serve(t, h, "/"))
	for _, kind := range reportKinds {
		qs := h.reportQueries(kind)
		if len(qs) != 1 || !qs[0].Columns {
			t.Errorf("%s read %+v, want one table read", kind, qs)
		}
	}
	for _, res := range []string{"pods", "replicasets", "jobs"} {
		qs := h.reportQueries(res)
		if res == "pods" {
			// Beside the pods, the host-network ones, selected by field.
			if len(qs) != 2 || !qs[0].Columns || !qs[1].Columns || qs[0].FieldSelector+qs[1].FieldSelector != "spec.hostNetwork=true" {
				t.Errorf("pods read %+v, want two table reads, one by host network", qs)
			}
			continue
		}
		if len(qs) != 1 || !qs[0].Columns {
			t.Errorf("%s read %+v, want one table read", res, qs)
		}
	}
	if h.peak > parallel || h.peak < 2 {
		t.Errorf("%d reads at once, want between 2 and %d", h.peak, parallel)
	}
}

// Every list in the answer is [] when empty, never null, and the nullable
// fields are null rather than absent.
func TestEmptyListsAreArraysNotNull(t *testing.T) {
	h := newHost(t)
	body := serve(t, h, "/").Body.String()
	for _, want := range []string{`"namespaces":[]`, `"images":[]`, `"workloads":[]`, `"notes":[]`, `"operator":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("%s missing in %s", want, body)
		}
	}
	h.add("pods", pod("a", "p", nil, "", ""))
	body = serve(t, h, "/").Body.String()
	if !strings.Contains(body, `"config":null`) || !strings.Contains(body, `"secrets":null`) || !strings.Contains(body, `"images":[]`) {
		t.Errorf("nullable fields: %s", body)
	}
	d := serve(t, h, "/workload/a/Pod/p").Body.String()
	for _, want := range []string{`"containers":[]`, `"exposure":[]`, `"cves":[]`, `"checks":[]`, `"secrets":[]`, `"cvesTotal":0`} {
		if !strings.Contains(d, want) {
			t.Errorf("%s missing in %s", want, d)
		}
	}
}

// The four states of a source, each said and never drawn as zero: absent
// (not served), denied (refused for the person), unreadable (failed), read
// and incomplete (narrowed).
func TestEachSourceSaysItsState(t *testing.T) {
	h := shop(t)
	h.reg.served[KindExposedSecret] = false
	h.listErr[KindConfigAudit] = fmt.Errorf("list: %w", extension.ErrDenied)
	o := decode[Overview](t, serve(t, h, "/"))
	states := map[string]Source{}
	for _, s := range o.Sources {
		states[s.Kind] = s
	}
	if s := states[KindVulnerability]; s.State != StateRead || !s.Complete || s.Reports != 3 {
		t.Errorf("vulnerabilities %+v", s)
	}
	if s := states[KindConfigAudit]; s.State != StateDenied || s.Complete || s.Note == "" {
		t.Errorf("config %+v", s)
	}
	if s := states[KindExposedSecret]; s.State != StateAbsent {
		t.Errorf("secrets %+v", s)
	}
	if !o.Operator || rowOf(t, o, "Deployment", "web").Config != nil {
		t.Errorf("a refused config read is null config, not zero")
	}

	h.listErr[KindConfigAudit] = errors.New("the API server did not answer")
	h.listErr[KindVulnerability] = fmt.Errorf("x: %w", extension.ErrUnreviewable)
	o = decode[Overview](t, serve(t, h, "/"))
	if s := o.Sources[1]; s.State != StateUnreadable || !strings.Contains(s.Note, "did not answer") {
		t.Errorf("config %+v", s)
	}
	if s := o.Sources[0]; s.State != StateDenied || s.Note != extension.UnreviewedText {
		t.Errorf("vulnerabilities %+v", s)
	}

	delete(h.listErr, KindVulnerability)
	delete(h.listErr, KindConfigAudit)
	h.allowed = []string{"shop"}
	o = decode[Overview](t, serve(t, h, "/"))
	if s := o.Sources[0]; s.State != StateRead || s.Complete || !strings.Contains(s.Note, "namespaces where you may") {
		t.Errorf("narrowed %+v", s)
	}
}

// No CRD of trivy-operator served: operator false, one state, and nothing
// else read at all.
func TestNoOperatorIsOneStateAndNoRead(t *testing.T) {
	h := shop(t)
	h.reg.served = map[string]bool{}
	o := decode[Overview](t, serve(t, h, "/"))
	if o.Operator || len(o.Workloads) != 0 || len(o.Sources) != 3 {
		t.Errorf("%+v", o)
	}
	for _, s := range o.Sources {
		if s.State != StateAbsent || s.Reports != 0 {
			t.Errorf("source %+v", s)
		}
	}
	if len(h.queries) != 0 {
		t.Errorf("read %d lists with no operator", len(h.queries))
	}
	if b := decode[Brief](t, serve(t, h, "/brief/shop/web-1-a?kind=pods")); b.Scanned {
		t.Errorf("brief %+v", b)
	}
	if c := decode[map[string]Cell](t, serve(t, h, "/cells?kind=pods")); len(c) != 0 {
		t.Errorf("cells %+v", c)
	}
}

// A read beside the reports that fails is a note, not an empty cluster; a
// person narrowed to some namespaces sees nothing of the others.
func TestAFailedPodReadIsANoteAndANarrowedPersonSeesTheirNamespacesOnly(t *testing.T) {
	h := shop(t)
	h.add("pods", pod("secret-team", "p", nil, "", ""))
	h.add(KindVulnerability, vulnReport("pod-p-app", scan{"secret-team", "Pod", "p", "app"}, "hidden", "1", Counts{Critical: 1}))
	h.allowed = []string{"shop"}
	o := decode[Overview](t, serve(t, h, "/"))
	body := serve(t, h, "/").Body.String()
	if strings.Contains(body, "secret-team") || strings.Contains(body, "hidden") {
		t.Errorf("a narrowed person saw another namespace: %s", body)
	}
	if len(o.Notes) == 0 {
		t.Errorf("the narrowed pod read is not noted")
	}

	h.allowed = nil
	h.listErr["services"] = fmt.Errorf("x: %w", extension.ErrDenied)
	h.listErr["pods"] = errors.New("timeout")
	o = decode[Overview](t, serve(t, h, "/"))
	joined := strings.Join(o.Notes, " | ")
	if !strings.Contains(joined, "Pods could not be read") || !strings.Contains(joined, "Services were refused") {
		t.Errorf("notes %q", joined)
	}
	if !hasRow(o, "Deployment", "web") {
		t.Errorf("the reports still link through the ReplicaSets: %v", names(o.Workloads))
	}
}

// The bounds: workloads, namespaces and images are cut the worst first, and
// the cut is said.
func TestTheAnswerIsBounded(t *testing.T) {
	h := newHost(t)
	for i := 0; i < MaxWorkloads+10; i++ {
		ns := fmt.Sprintf("ns-%03d", i%(MaxNamespaces+20))
		h.add("pods", pod(ns, fmt.Sprintf("p-%05d", i), nil, "", ""))
		if i < MaxImages+15 {
			h.add(KindVulnerability, vulnReport(fmt.Sprintf("pod-p-%05d-app", i), scan{ns, "Pod", fmt.Sprintf("p-%05d", i), "app"},
				fmt.Sprintf("img-%03d", i), "1", Counts{Critical: i % 7, High: 1}))
		}
	}
	o := decode[Overview](t, serve(t, h, "/"))
	if !o.Capped || o.Total != MaxWorkloads+10 || len(o.Workloads) != MaxWorkloads {
		t.Errorf("capped %v total %d rows %d", o.Capped, o.Total, len(o.Workloads))
	}
	if len(o.Images) != MaxImages || o.Images[0].Counts.Critical != 6 {
		t.Errorf("images %d, first %+v", len(o.Images), o.Images[0])
	}
	if len(o.Namespaces) > MaxNamespaces {
		t.Errorf("namespaces %d", len(o.Namespaces))
	}
	if o.Workloads[0].Counts.Critical != 6 || o.Workloads[len(o.Workloads)-1].Scanned != "" {
		t.Errorf("the worst first, the unscanned last")
	}
}

// Failures are problems: an unknown cluster 404 with the console's sentence,
// a catalog that could not be read 502, a bad namespace 400.
func TestFailuresAreProblems(t *testing.T) {
	h := newHost(t)
	h.unknown = true
	p := problemOf(t, serve(t, h, "/"), http.StatusNotFound, CodeUnknownCluster)
	if p.Message != extension.UnknownClusterText("kind") {
		t.Errorf("message %q", p.Message)
	}
	h = newHost(t)
	h.defErr = errors.New("discovery failed")
	problemOf(t, serve(t, h, "/"), http.StatusBadGateway, CodeCatalogueUnreadable)
	problemOf(t, serve(t, newHost(t), "/?ns=Not_A_Namespace"), http.StatusBadRequest, CodeBadRequest)

	// A report read that meets an unknown cluster is the request's 404, not a
	// source's state.
	h = newHost(t)
	h.listErr[KindVulnerability] = fmt.Errorf("x: %w", extension.ErrUnknownCluster)
	problemOf(t, serve(t, h, "/"), http.StatusNotFound, CodeUnknownCluster)
}

// Every route is a GET, and the mux refuses any other method.
func TestEveryRouteIsAGet(t *testing.T) {
	routes := New().Routes(newHost(t))
	if len(routes) != 11 {
		t.Errorf("%d routes", len(routes))
	}
	for _, r := range routes {
		if r.Method != http.MethodGet {
			t.Errorf("%s %s", r.Method, r.Pattern)
		}
	}
	h := shop(t)
	for _, path := range []string{"/", "/workload/shop/Deployment/web", "/workload/shop/Deployment/web/sbom", "/brief/shop/web?kind=deployments", "/cells?kind=pods", "/cluster", "/rbac", "/compliance", "/config?w=shop/Deployment/web", "/policies", "/policies/kyverno/require-labels"} {
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			if rec := serveMethod(t, h, m, path); rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s answered %d", m, path, rec.Code)
			}
		}
	}
}

func TestTheExtensionIsDescribed(t *testing.T) {
	x := New()
	if x.Key() != "security" || x.Label() != "Security Center" || x.Summary() == "" || x.Needs() == "" || x.Legacy() != nil {
		t.Errorf("%q %q", x.Key(), x.Label())
	}
}

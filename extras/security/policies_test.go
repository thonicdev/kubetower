package security

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
)

// polResult is one result of a PolicyReport.
func polResult(policy, rule, outcome, severity, message string) map[string]any {
	return map[string]any{"policy": policy, "rule": rule, "result": outcome, "severity": severity, "category": "Pod Security",
		"source": "kyverno", "message": message, "properties": map[string]any{"PROPERTIES-NEVER-SENT": "x"}}
}

// policyReport is a Kyverno PolicyReport about one resource.
func policyReport(namespace, name, kind, object string, results ...map[string]any) *fakeObj {
	return &fakeObj{namespace: namespace, name: name, body: func() map[string]any {
		rs := make([]any, 0, len(results))
		for _, r := range results {
			rs = append(rs, r)
		}
		return map[string]any{"apiVersion": "wgpolicyk8s.io/v1alpha2", "kind": "PolicyReport",
			"metadata": meta(namespace, name, nil, nil),
			"scope":    map[string]any{"apiVersion": "v1", "kind": kind, "namespace": namespace, "name": object, "uid": "uid-" + object},
			"results":  rs}
	}}
}

func kyvernoPolicy(name, action string, rules ...string) *fakeObj {
	return &fakeObj{name: name, body: func() map[string]any {
		var rs []any
		for _, r := range rules {
			rs = append(rs, map[string]any{"name": r, "validate": map[string]any{"message": "m"}})
		}
		return map[string]any{"apiVersion": "kyverno.io/v1", "kind": "ClusterPolicy", "metadata": meta("", name, nil, nil),
			"spec": map[string]any{"validationFailureAction": action, "rules": rs}}
	}}
}

type violation struct{ namespace, kind, name string }

func constraint(name, action string, total int, vs ...violation) *fakeObj {
	return &fakeObj{name: name, body: func() map[string]any {
		var list []any
		for _, v := range vs {
			list = append(list, map[string]any{"enforcementAction": action, "kind": v.kind, "namespace": v.namespace,
				"name": v.name, "message": "you must provide labels: {\"owner\"}"})
		}
		return map[string]any{"apiVersion": "constraints.gatekeeper.sh/v1beta1", "kind": "K8sRequiredLabels",
			"metadata": meta("", name, nil, nil), "spec": map[string]any{"enforcementAction": action},
			"status": map[string]any{"totalViolations": int64(total), "violations": list}}
	}}
}

func servePolicies(h *fakeHost) {
	if h.reg.other == nil {
		h.reg.other = map[string]extension.CRD{}
	}
	h.reg.other[KindPolicyReports] = extension.CRD{Group: "wgpolicyk8s.io", Version: "v1alpha2", Plural: "policyreports", Namespaced: true}
	h.reg.other[KindOpenReports] = extension.CRD{Group: "openreports.io", Version: "v1alpha1", Plural: "reports", Namespaced: true}
	h.reg.other[KindKyvernoCluster] = extension.CRD{Group: "kyverno.io", Version: "v1", Plural: "clusterpolicies"}
	h.reg.constraints = []string{"K8sRequiredLabels"}
}

// policyShop is shop with Kyverno and Gatekeeper: require-labels audits,
// disallow-latest enforces; web's pod fails both, api fails one and warns;
// the same result in both report groups is counted once; one Gatekeeper
// constraint wrote 2 of its 25 violations, the other only audits (dryrun).
func policyShop(t *testing.T) *fakeHost {
	h := shop(t)
	servePolicies(h)
	h.add("clusterpolicies", kyvernoPolicy("require-labels", "Audit", "check-labels"), kyvernoPolicy("disallow-latest", "Enforce", "no-latest"))
	webPod := []map[string]any{
		polResult("require-labels", "check-labels", "fail", "medium", "label 'owner' is required"),
		polResult("disallow-latest", "no-latest", "fail", "high", strings.Repeat("an image tag ", 40)),
		polResult("disallow-latest", "other", "pass", "high", ""),
	}
	h.add("policyreports",
		policyReport("shop", "r-web", "Pod", "web-1-a", webPod...),
		policyReport("shop", "r-api", "Deployment", "api",
			polResult("require-labels", "autogen-check-labels", "fail", "medium", "label 'owner' is required"),
			polResult("disallow-latest", "autogen-no-latest", "warn", "high", "a tag")),
		policyReport("billing", "r-ok", "Pod", "ok", polResult("require-labels", "check-labels", "pass", "medium", "")))
	// The same report in the newer group, during a migration.
	h.add("reports", policyReport("shop", "r-web", "Pod", "web-1-a", webPod...))
	h.add("k8srequiredlabels",
		constraint("must-have-owner", "deny", 25, violation{"shop", "Deployment", "worker"}, violation{"billing", "Pod", "ok"}),
		constraint("audit-team", "dryrun", 1, violation{"shop", "Deployment", "web"}))
	return h
}

func TestPoliciesOverview(t *testing.T) {
	h := policyShop(t)
	rec := serve(t, h, "/policies")
	if strings.Contains(rec.Body.String(), "PROPERTIES-NEVER-SENT") {
		t.Fatal("a result's properties reached the answer")
	}
	o := decode[PolicyOverview](t, rec)
	if !o.Engines {
		t.Fatalf("engines false: %+v", o.Sources)
	}
	// Kyverno: web 2 fails (1 audit), api 1 fail (audit) + 1 warn; the copy in
	// reports.openreports.io counted once. Gatekeeper: 25 deny (2 listed),
	// 1 dryrun (audit).
	want := PolicyCounts{Fail: 3 + 25 + 1, Warn: 1, Audit: 2 + 1}
	if o.Totals != want {
		t.Errorf("totals %+v, want %+v", o.Totals, want)
	}
	if o.PoliciesTotal != 4 || o.Policies[0].Name != "k8srequiredlabels.must-have-owner" || o.Policies[0].Listed != 2 ||
		o.Policies[0].Violations != 25 || o.Policies[0].Mode != "deny" || o.Policies[0].Kind != "K8sRequiredLabels" {
		t.Errorf("policies %+v", o.Policies)
	}
	byName := map[string]PolicyRow{}
	for _, p := range o.Policies {
		byName[p.Name] = p
	}
	if rl := byName["require-labels"]; rl.Mode != "Audit" || rl.Counts.Audit != 2 || rl.Resources != 2 || rl.Severity != "medium" {
		t.Errorf("require-labels %+v", rl)
	}
	if dl := byName["disallow-latest"]; dl.Mode != "Enforce" || dl.Counts.Audit != 0 || dl.Counts.Warn != 1 || dl.Severity != "high" {
		t.Errorf("disallow-latest %+v", dl)
	}
	// web's pod's results count against web; Gatekeeper's dryrun there too.
	var web *PolicyResource
	for i, r := range o.Resources {
		if r.Kind == "Pod" && r.Name == "web-1-a" {
			t.Errorf("web's pod listed as itself: %+v", r)
		}
		if r.Kind == "Deployment" && r.Name == "web" {
			web = &o.Resources[i]
		}
	}
	if web == nil || web.Workload == nil || *web.Workload != (WorkloadRef{Namespace: "shop", Kind: "Deployment", Name: "web"}) || web.Policies != 3 {
		t.Errorf("web %+v: its own workload, three policies", web)
	}
	if len(o.Namespaces) != 2 || o.Namespaces[0].Namespace != "shop" {
		t.Errorf("namespaces %+v", o.Namespaces)
	}
	for _, q := range h.reportQueries("policyreports") {
		if q.Each == nil || q.Columns {
			t.Errorf("reports read %+v, want a walk", q)
		}
	}
}

func TestPoliciesWithoutAnyEngine(t *testing.T) {
	o := decode[PolicyOverview](t, serve(t, shop(t), "/policies"))
	if o.Engines || len(o.Policies) != 0 || o.Policies == nil || o.Resources == nil {
		t.Fatalf("%+v", o)
	}
	for _, s := range o.Sources {
		if s.State != StateAbsent {
			t.Errorf("%s: %s", s.Kind, s.State)
		}
	}
}

func TestPolicySourcesSayWhatWentWrong(t *testing.T) {
	h := policyShop(t)
	h.listErr["policyreports"] = fmt.Errorf("x: %w", extension.ErrDenied)
	h.listErr["clusterpolicies"] = errors.New("boom")
	o := decode[PolicyOverview](t, serve(t, h, "/policies"))
	states := map[string]string{}
	for _, s := range o.Sources {
		states[s.Kind] = s.State
	}
	if states[KindPolicyReports] != StateDenied || states[KindKyvernoCluster] != StateUnreadable ||
		states[KindOpenReports] != StateRead || states[KindGatekeeper] != StateRead || states[KindClusterPolicyReports] != StateAbsent {
		t.Errorf("states %v", states)
	}
	// The mode unread is unknown, never Enforce: no failure counts as audit.
	for _, p := range o.Policies {
		if p.Engine == engineKyverno && (p.Mode != "" || p.Counts.Audit != 0) {
			t.Errorf("%s: mode %q audit %d with the policies unread", p.Name, p.Mode, p.Counts.Audit)
		}
	}
	h = policyShop(t)
	h.unknown = true
	problemOf(t, serve(t, h, "/policies"), http.StatusNotFound, CodeUnknownCluster)
}

func TestOnePolicysResults(t *testing.T) {
	d := decode[PolicyDetail](t, serve(t, policyShop(t), "/policies/kyverno/require-labels"))
	if d.Policy == nil || d.Policy.Name != "require-labels" || d.Total != 2 || len(d.Results) != 2 {
		t.Fatalf("%+v", d)
	}
	for _, r := range d.Results {
		if r.Policy != "require-labels" || !r.Audit || r.Message == "" {
			t.Errorf("result %+v", r)
		}
	}
	g := decode[PolicyDetail](t, serve(t, policyShop(t), "/policies/gatekeeper/k8srequiredlabels.must-have-owner"))
	if g.Policy == nil || g.Policy.Violations != 25 || len(g.Results) != 2 {
		t.Errorf("gatekeeper %+v", g)
	}
	if n := decode[PolicyDetail](t, serve(t, policyShop(t), "/policies/kyverno/none")); n.Policy != nil || len(n.Results) != 0 {
		t.Errorf("an unknown policy %+v", n)
	}
}

func TestAWorkloadsPolicies(t *testing.T) {
	d := decode[Detail](t, serve(t, policyShop(t), "/workload/shop/Deployment/web"))
	if d.Policies == nil || d.Policies.Total != 3 || d.Policies.Counts != (PolicyCounts{Fail: 3, Audit: 2}) {
		t.Fatalf("web's policies %+v", d.Policies)
	}
	for _, r := range d.Policies.Results {
		if r.Message != "" && len([]rune(r.Message)) > MaxPolicyMessage {
			t.Errorf("message of %d runes", len([]rune(r.Message)))
		}
	}
	b := decode[Brief](t, serve(t, policyShop(t), "/brief/shop/web-1-a?kind=pods"))
	if b.Policies == nil || *b.Policies != (PolicyCounts{Fail: 3, Audit: 2}) {
		t.Errorf("web's pod's brief %+v", b.Policies)
	}
	if d := decode[Detail](t, serve(t, shop(t), "/workload/shop/Deployment/web")); d.Policies != nil {
		t.Errorf("no engine, no policies: %+v", d.Policies)
	}
}

// The results kept are bounded, the worst first, and every one is counted.
func TestPolicyResultsAreBounded(t *testing.T) {
	p := &policyRead{byPolicy: map[string]*policyAgg{}, byResource: map[string]*resourceAgg{}}
	for i := 0; i < MaxPolicyResults+10; i++ {
		outcome := "warn"
		if i%1000 == 0 {
			outcome = "error"
		}
		p.keep(policyResult{engine: "kyverno", policy: "p", outcome: outcome, res: resource{"shop", "Pod", fmt.Sprint(i)}}, "")
	}
	p.finish()
	if len(p.results) != MaxPolicyResults || p.total != MaxPolicyResults+10 || p.results[0].outcome != "error" {
		t.Errorf("kept %d of %d, first %q", len(p.results), p.total, p.results[0].outcome)
	}
	if c := p.byPolicy["kyverno/p"].counts; c.Warn+c.Error != MaxPolicyResults+10 {
		t.Errorf("counted %+v", c)
	}
}

// A cluster with policy reports and no trivy-operator is suggested the extra.
func TestPolicyReportsSuggestTheExtra(t *testing.T) {
	h := newHost(t)
	h.reg.served = map[string]bool{}
	servePolicies(h)
	h.add("policyreports", policyReport("shop", "r", "Pod", "p"))
	got, err := New().Suggest(httptest.NewRequest(http.MethodGet, "/", nil), h, "kind")
	if err != nil || got != "policy reports" {
		t.Errorf("%q, %v", got, err)
	}
	h = newHost(t)
	h.reg.served = map[string]bool{}
	if got, err := New().Suggest(httptest.NewRequest(http.MethodGet, "/", nil), h, "kind"); err != nil || got != "" {
		t.Errorf("nothing served: %q, %v", got, err)
	}
}

// celPolicy is a Kyverno CEL policy: ValidatingPolicy and the like.
func celPolicy(kind, namespace, name string, actions ...string) *fakeObj {
	return &fakeObj{namespace: namespace, name: name, body: func() map[string]any {
		as := make([]any, 0, len(actions))
		for _, a := range actions {
			as = append(as, a)
		}
		return map[string]any{"apiVersion": "policies.kyverno.io/v1", "kind": kind, "metadata": meta(namespace, name, nil, nil),
			"spec": map[string]any{"validationActions": as}}
	}}
}

func celResult(policy, rule, source, message string) map[string]any {
	return map[string]any{"policy": policy, "rule": rule, "result": "fail", "source": source, "message": message,
		"properties": map[string]any{"process": "background scan"}}
}

// kyvernoCEL is Kyverno 1.20 on sec-test: three CEL policies that audit, and
// the Deployment unsigned, its ReplicaSet, an old one scaled to 0 and its pod,
// each with a report carrying the same three failures, in both groups.
func kyvernoCEL(t *testing.T) *fakeHost {
	h := newHost(t)
	servePolicies(h)
	h.reg.constraints = nil
	h.reg.other[KindKyvernoValidating] = extension.CRD{Group: "policies.kyverno.io", Version: "v1", Plural: "validatingpolicies"}
	h.reg.other[KindKyvernoImage] = extension.CRD{Group: "policies.kyverno.io", Version: "v1", Plural: "imagevalidatingpolicies"}
	h.reg.other[KindKyvernoNsValidating] = extension.CRD{Group: "policies.kyverno.io", Version: "v1", Plural: "namespacedvalidatingpolicies", Namespaced: true}
	h.add("validatingpolicies", celPolicy("ValidatingPolicy", "", "require-limits", "Audit"),
		celPolicy("ValidatingPolicy", "", "require-team-label", "Audit"))
	h.add("imagevalidatingpolicies", celPolicy("ImageValidatingPolicy", "", "verify-test-image", "Audit"))
	h.add("replicasets", replicaSet("sec-test", "unsigned-cbb869fd8", "unsigned", 2), replicaSet("sec-test", "unsigned-6d4f5", "unsigned", 1))
	h.add("pods", pod("sec-test", "unsigned-cbb869fd8-7gq78", nil, "ReplicaSet", "unsigned-cbb869fd8"))
	three := func(prefix string) []map[string]any {
		return []map[string]any{
			celResult("require-limits", prefix+"validation", "KyvernoValidatingPolicy", "CPU and memory limits are required"),
			celResult("require-team-label", prefix+"validation", "KyvernoValidatingPolicy", "label team is required"),
			celResult("verify-test-image", prefix+"verify-test-image", "KyvernoImageValidatingPolicy", "the image is not signed with the test key"),
		}
	}
	for _, kind := range []string{"policyreports", "reports"} {
		h.add(kind,
			policyReport("sec-test", "uid-pod", "Pod", "unsigned-cbb869fd8-7gq78", three("")...),
			policyReport("sec-test", "uid-rs", "ReplicaSet", "unsigned-cbb869fd8", three("autogen-")...),
			policyReport("sec-test", "uid-old", "ReplicaSet", "unsigned-6d4f5", three("autogen-")...),
			policyReport("sec-test", "uid-deploy", "Deployment", "unsigned", three("autogen-")...))
	}
	return h
}

// Kyverno's CEL policies are kyverno: their mode is read, their audit
// failures counted, and a result on a workload's objects counted once.
func TestKyvernoCELPolicies(t *testing.T) {
	o := decode[PolicyOverview](t, serve(t, kyvernoCEL(t), "/policies"))
	if o.Totals != (PolicyCounts{Fail: 3, Audit: 3}) {
		t.Errorf("totals %+v, want 3 failures, all audit", o.Totals)
	}
	if len(o.Policies) != 3 {
		t.Fatalf("policies %+v", o.Policies)
	}
	for _, p := range o.Policies {
		if p.Engine != engineKyverno || p.Mode != "Audit" || p.Counts != (PolicyCounts{Fail: 1, Audit: 1}) || p.Resources != 1 {
			t.Errorf("%+v", p)
		}
	}
	if len(o.Resources) != 1 || o.Resources[0].Kind != "Deployment" || o.Resources[0].Name != "unsigned" || o.Resources[0].Policies != 3 {
		t.Errorf("resources %+v, want unsigned alone", o.Resources)
	}
	if len(o.Namespaces) != 1 || o.Namespaces[0].Resources != 1 || o.Namespaces[0].Counts.Fail != 3 {
		t.Errorf("namespaces %+v", o.Namespaces)
	}
	states := map[string]string{}
	for _, s := range o.Sources {
		states[s.Kind] = s.State
	}
	if states[KindKyvernoImage] != StateRead || states[KindKyvernoNsValidating] != StateRead || states[KindKyvernoNsImage] != StateAbsent {
		t.Errorf("sources %v", states)
	}

	d := decode[Detail](t, serve(t, kyvernoCEL(t), "/workload/sec-test/Deployment/unsigned"))
	if d.Policies == nil || d.Policies.Total != 3 || len(d.Policies.Results) != 3 || d.Policies.Counts != (PolicyCounts{Fail: 3, Audit: 3}) {
		t.Fatalf("unsigned's policies %+v", d.Policies)
	}
	for _, r := range d.Policies.Results {
		if r.Engine != engineKyverno || !r.Audit || r.Kind != "Deployment" || r.Name != "unsigned" {
			t.Errorf("result %+v", r)
		}
	}

	p := decode[PolicyDetail](t, serve(t, kyvernoCEL(t), "/policies/kyverno/verify-test-image"))
	if p.Policy == nil || p.Policy.Mode != "Audit" || p.Total != 1 || len(p.Results) != 1 {
		t.Errorf("verify-test-image %+v", p)
	}
}

// Without the owners, a result is counted per object, as reported.
func TestKyvernoResultsPerObjectWithoutOwners(t *testing.T) {
	h := kyvernoCEL(t)
	h.listErr["replicasets"] = errors.New("boom")
	o := decode[PolicyOverview](t, serve(t, h, "/policies"))
	if o.Totals != (PolicyCounts{Fail: 12, Audit: 12}) || len(o.Resources) != 4 {
		t.Errorf("totals %+v, resources %d: want each object's", o.Totals, len(o.Resources))
	}
	if len(o.Notes) == 0 {
		t.Error("the owners unread are not said")
	}
}

// A namespaced CEL policy's mode is found from a result that names it bare,
// and never overwrites a cluster policy of the same name.
func TestNamespacedKyvernoModes(t *testing.T) {
	m := modes{policy: map[string]string{}, rule: map[string]string{}}
	m.read(&unstructured.Unstructured{Object: celPolicy("NamespacedValidatingPolicy", "team-a", "p", "Deny").body()}, KindKyvernoNsValidating)
	m.read(&unstructured.Unstructured{Object: celPolicy("NamespacedImageValidatingPolicy", "team-a", "img", "Warn").body()}, KindKyvernoNsImage)
	m.read(&unstructured.Unstructured{Object: celPolicy("ValidatingPolicy", "", "p", "Audit").body()}, KindKyvernoValidating)
	m.read(&unstructured.Unstructured{Object: celPolicy("ValidatingPolicy", "", "both", "Audit", "Deny").body()}, KindKyvernoValidating)
	for _, c := range []struct{ ns, policy, want string }{
		{"team-a", "p", "Audit"}, {"team-a", "team-a/p", "Enforce"}, {"team-a", "img", "Audit"},
		{"team-b", "img", ""}, {"", "both", "Enforce"},
	} {
		if got := m.of(c.ns, c.policy, "validation"); got != c.want {
			t.Errorf("%s in %q: %q, want %q", c.policy, c.ns, got, c.want)
		}
	}
}

func TestEngineOf(t *testing.T) {
	for in, want := range map[string]string{"kyverno": "kyverno", "KyvernoValidatingPolicy": "kyverno",
		"KyvernoImageValidatingPolicy": "kyverno", "": "unknown", "Falco": "falco"} {
		if got := engineOf(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

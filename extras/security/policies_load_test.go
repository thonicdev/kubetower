package security

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	maxPoliciesAlloc = 160 << 20
	maxPolicyTook    = 10 * time.Second
)

// policyCluster is the Policy Center's load: 8 000 PolicyReports of 20
// results each, 2 000 of them with failures, about 5 000 pods to tie them to,
// 40 Gatekeeper constraints of 500 written violations, 300 Kyverno policies.
func policyCluster(t *testing.T) *fakeHost {
	h := largeCluster(t)
	servePolicies(h)
	kinds := make([]string, 40)
	for i := range kinds {
		kinds[i] = fmt.Sprintf("K8sRule%02d", i)
	}
	h.reg.constraints = kinds
	for i := 0; i < 300; i++ {
		h.add("clusterpolicies", kyvernoPolicy(fmt.Sprintf("policy-%03d", i), []string{"Audit", "Enforce"}[i%2], "rule"))
	}
	msg := strings.Repeat("validation error: ", 10)
	for i := 0; i < 8000; i++ {
		ns := fmt.Sprintf("team-%02d", i%40)
		results := make([]map[string]any, 20)
		for j := range results {
			outcome := "pass"
			if i < 2000 && j < 4 {
				outcome = []string{"fail", "fail", "warn", "error"}[j]
			}
			results[j] = polResult(fmt.Sprintf("policy-%03d", (i+j)%300), "rule", outcome, "medium", msg)
		}
		h.add("policyreports", policyReport(ns, fmt.Sprintf("r-%05d", i), "Pod",
			fmt.Sprintf("svc-%04d-7f9c8d6b5-%05d", i%2000, i), results...))
	}
	for k, kind := range kinds {
		vs := make([]violation, 500)
		for j := range vs {
			vs[j] = violation{fmt.Sprintf("team-%02d", j%40), "Deployment", fmt.Sprintf("svc-%04d", (j*7+k)%2000)}
		}
		h.add(strings.ToLower(kind), constraint("c-"+strings.ToLower(kind), []string{"deny", "dryrun", "warn"}[k%3], 500, vs...))
	}
	return h
}

func TestPolicyCentreCostsABoundedAmount(t *testing.T) {
	if testing.Short() {
		t.Skip("a load test")
	}
	h := policyCluster(t)
	o := decode[PolicyOverview](t, serve(t, h, "/policies"))
	// Kyverno: 2 000 reports of 2 failures. Gatekeeper: 14 deny and 13
	// dryrun constraints of 500 violations fail, 13 warn ones warn.
	if o.Totals.Fail != 2000*2+27*500 || o.Totals.Warn != 2000+13*500 || o.Totals.Error != 2000 {
		t.Fatalf("totals %+v", o.Totals)
	}
	if o.ResourcesTotal < 2000 || len(o.Resources) > MaxPolicyResources || o.PoliciesTotal < 300 {
		t.Fatalf("resources %d of %d, policies %d", len(o.Resources), o.ResourcesTotal, o.PoliciesTotal)
	}
	for _, c := range []struct{ name, path string }{
		{"policies overview", "/policies"},
		{"one policy", "/policies/kyverno/policy-001"},
		{"a workload's policies", "/workload/team-01/Deployment/svc-0001"},
	} {
		alloc, retained, took := measure(t, h, c.path)
		t.Logf("%s: allocated %d MiB, retained %d KiB, %s", c.name, alloc>>20, retained>>10, took.Round(time.Millisecond))
		if alloc > maxPoliciesAlloc || retained > maxRetained || took > maxPolicyTook {
			t.Errorf("%s: allocated %d MiB (max %d), retained %d KiB (max %d), %s (max %s)",
				c.name, alloc>>20, maxPoliciesAlloc>>20, retained>>10, maxRetained>>10, took, maxPolicyTook)
		}
	}
}

package security

import (
	"net/http"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/extension"
)

func TestRBACIsReadAsTables(t *testing.T) {
	h := newHost(t)
	h.reg.served[KindRBAC] = true
	h.reg.served[KindClusterRBAC] = true
	h.add(KindRBAC, clusterReport("shop", "role-deployer", "Role", "deployer", Counts{High: 1}))
	h.add(KindClusterRBAC, clusterReport("", "clusterrole-admin", "ClusterRole", "admin", Counts{Critical: 2, High: 1}))
	f := decode[Findings](t, serve(t, h, "/rbac"))
	if len(f.Rows) != 2 || f.Rows[0].Kind != "ClusterRole" || f.Rows[1].Namespace != "shop" || f.Rows[1].Name != "deployer" {
		t.Errorf("rows %+v", f.Rows)
	}
	for _, q := range h.queries {
		if !q.Columns {
			t.Errorf("a body read: %+v", q)
		}
	}
}

// compliance is a report as trivy-operator writes it: the controls in the
// spec, their failures in the summary, and a detail that is never sent.
func compliance(name, title string, controls [][3]string, fails map[string]int, pass, fail int) *fakeObj {
	return &fakeObj{name: name, body: func() map[string]any {
		var cs, checks []any
		for _, c := range controls {
			cs = append(cs, map[string]any{"id": c[0], "name": c[1], "severity": c[2]})
			if n, ok := fails[c[0]]; ok {
				checks = append(checks, map[string]any{"id": c[0], "name": c[1], "severity": c[2], "totalFail": int64(n)})
			}
		}
		return map[string]any{"apiVersion": group + "/v1alpha1", "kind": "ClusterComplianceReport", "metadata": meta("", name, nil, nil),
			"spec": map[string]any{"reportType": "summary", "compliance": map[string]any{"title": title, "version": "1.0", "controls": cs}},
			"status": map[string]any{
				"summary":         map[string]any{"passCount": int64(pass), "failCount": int64(fail)},
				"summaryReport":   map[string]any{"controlCheck": checks},
				"detailReport":    map[string]any{"results": []any{map[string]any{"checks": []any{map[string]any{"messages": []any{"DETAIL-NEVER-SENT"}}}}}},
				"updateTimestamp": "2026-11-11T10:00:00Z",
			}}
	}}
}

func TestComplianceListsEachControlTheFailedFirst(t *testing.T) {
	h := newHost(t)
	h.reg.served[KindCompliance] = true
	h.add(KindCompliance,
		compliance("nsa", "National Security Agency - Kubernetes Hardening Guidance",
			[][3]string{{"1.0", "Non-root containers", "MEDIUM"}, {"1.1", "Immutable container file systems", "LOW"}, {"2.0", "Privileged", "HIGH"}, {"9.9", "Manual review", "MEDIUM"}},
			map[string]int{"1.0": 0, "1.1": 4, "2.0": 2}, 1, 2),
		compliance("cis", "CIS Kubernetes Benchmarks v1.23", [][3]string{{"1.1.1", "API server pod spec", "HIGH"}}, map[string]int{"1.1.1": 0}, 1, 0))
	rec := serve(t, h, "/compliance")
	if strings.Contains(rec.Body.String(), "DETAIL-NEVER-SENT") {
		t.Fatal("the detail report reached the answer")
	}
	c := decode[Compliance](t, rec)
	if c.Source.State != StateRead || len(c.Reports) != 2 || c.Reports[0].Name != "cis" {
		t.Fatalf("source %+v reports %d", c.Source, len(c.Reports))
	}
	nsa := c.Reports[1]
	if nsa.Pass != 1 || nsa.Fail != 2 || nsa.ControlsTotal != 4 || nsa.Updated == "" {
		t.Errorf("nsa %+v", nsa)
	}
	ids := []string{}
	for _, ctl := range nsa.Controls {
		ids = append(ids, ctl.ID)
	}
	if strings.Join(ids, ",") != "2.0,1.1,1.0,9.9" {
		t.Errorf("order %v: the failed first, the worst first", ids)
	}
	if nsa.Controls[3].Fail != nil || nsa.Controls[2].Fail == nil || *nsa.Controls[2].Fail != 0 {
		t.Errorf("a manual control has no count, a passed one 0: %+v", nsa.Controls)
	}
}

func TestComplianceStates(t *testing.T) {
	c := decode[Compliance](t, serve(t, newHost(t), "/compliance"))
	if c.Source.State != StateAbsent || c.Reports == nil {
		t.Errorf("%+v", c)
	}
	h := newHost(t)
	h.reg.served[KindCompliance] = true
	if c := decode[Compliance](t, serve(t, h, "/compliance")); c.Source.State != StateOff {
		t.Errorf("served with no report: %+v", c.Source)
	}
	h.listErr[KindCompliance] = extension.ErrDenied
	if c := decode[Compliance](t, serve(t, h, "/compliance")); c.Source.State != StateDenied {
		t.Errorf("refused: %+v", c.Source)
	}
	h.unknown = true
	problemOf(t, serve(t, h, "/compliance"), http.StatusNotFound, CodeUnknownCluster)
}

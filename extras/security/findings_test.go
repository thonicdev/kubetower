package security

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/extension"
)

// clusterReport is a report about no workload: its table cells and the
// labels naming what it scanned, with no body at all, so a body read fails.
func clusterReport(namespace, name, kind, object string, c Counts, image ...string) *fakeObj {
	cells := countCells(c, false)
	cells["Scanner"] = "Trivy"
	if len(image) == 2 {
		cells["Repository"], cells["Tag"] = image[0], image[1]
	}
	return &fakeObj{namespace: namespace, name: name, created: testNow.Add(-30 * time.Minute), cells: cells,
		labels: map[string]string{labelKind: kind, labelName: object}}
}

func clusterHost(t *testing.T) *fakeHost {
	h := newHost(t)
	h.reg.served[KindClusterVulnerability] = true
	h.reg.served[KindClusterConfigAudit] = true
	h.reg.served[KindInfraAssessment] = true
	h.reg.served[KindClusterInfra] = true
	h.add(KindClusterVulnerability,
		clusterReport("", "k8s-cluster-kubelet", "Node", "kubelet", Counts{Critical: 1, High: 4}, "k8s.io/kubelet", "1.30.2"),
		clusterReport("", "k8s-cluster-api", "Pod", "kube-apiserver", Counts{High: 2}, "k8s.io/kube-apiserver", "1.30.2"))
	h.add(KindClusterConfigAudit, clusterReport("", "clusterrole-admin", "ClusterRole", "admin", Counts{Medium: 3}))
	long := clusterReport("", "node-worker-1", "Node", "", Counts{High: 1})
	long.labels[labelNameHash] = "5d9f"
	h.add(KindClusterInfra, long)
	return h
}

func TestTheClusterReportsAreReadAsTablesTheWorstFirst(t *testing.T) {
	h := clusterHost(t)
	f := decode[Findings](t, serve(t, h, "/cluster"))
	if f.Total != 4 || len(f.Rows) != 4 || f.Capped {
		t.Fatalf("total %d rows %d", f.Total, len(f.Rows))
	}
	first := f.Rows[0]
	if first.Source != KindClusterVulnerability || first.Kind != "Node" || first.Name != "kubelet" || first.Image != "k8s.io/kubelet:1.30.2" ||
		first.Counts != (Counts{Critical: 1, High: 4}) || first.Scanned == "" {
		t.Errorf("first %+v", first)
	}
	if f.Rows[3].Source != KindClusterConfigAudit || f.Rows[3].Kind != "ClusterRole" {
		t.Errorf("last %+v, want the medium one", f.Rows[3])
	}
	for _, r := range f.Rows {
		if r.Source == KindClusterInfra && r.Name != "node-worker-1" {
			t.Errorf("a hashed name reads as the report's own: %+v", r)
		}
	}
	if f.Totals != (Counts{Critical: 1, High: 7, Medium: 3}) || f.ByKind[KindClusterVulnerability] != 2 {
		t.Errorf("totals %+v by kind %v", f.Totals, f.ByKind)
	}
	states := map[string]string{}
	for _, s := range f.Sources {
		states[s.Kind] = s.State
	}
	if states[KindClusterVulnerability] != StateRead || states[KindInfraAssessment] != StateOff {
		t.Errorf("states %v: a kind served with no report is off", states)
	}
	for _, q := range h.queries {
		if !q.Columns || q.Namespace != "" {
			t.Errorf("read %+v, want a table read in every namespace", q)
		}
	}
}

func TestClusterReportsWhenNothingIsServedOrRefused(t *testing.T) {
	f := decode[Findings](t, serve(t, newHost(t), "/cluster"))
	for _, s := range f.Sources {
		if s.State != StateAbsent {
			t.Errorf("%s: %s, want absent", s.Kind, s.State)
		}
	}
	if len(f.Rows) != 0 || f.Rows == nil {
		t.Errorf("rows %v, want an empty list", f.Rows)
	}
	h := clusterHost(t)
	h.listErr[KindClusterConfigAudit] = extension.ErrDenied
	h.listErr[KindClusterInfra] = errors.New("boom")
	f = decode[Findings](t, serve(t, h, "/cluster"))
	got := map[string]string{}
	for _, s := range f.Sources {
		got[s.Kind] = s.State
	}
	if got[KindClusterConfigAudit] != StateDenied || got[KindClusterInfra] != StateUnreadable || got[KindClusterVulnerability] != StateRead {
		t.Errorf("states %v", got)
	}
	h = clusterHost(t)
	h.unknown = true
	problemOf(t, serve(t, h, "/cluster"), http.StatusNotFound, CodeUnknownCluster)
}

func TestClusterReportsAreCapped(t *testing.T) {
	h := clusterHost(t)
	for i := 0; i < MaxFindingRows+10; i++ {
		h.add(KindClusterConfigAudit, clusterReport("", "r", "ClusterRole", "role", Counts{Low: 1}))
	}
	f := decode[Findings](t, serve(t, h, "/cluster"))
	if len(f.Rows) != MaxFindingRows || !f.Capped || f.Total != MaxFindingRows+14 {
		t.Errorf("rows %d capped %v total %d", len(f.Rows), f.Capped, f.Total)
	}
}

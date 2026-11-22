package security

import (
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"testing"
	"time"
)

// largeCluster is the load target: 5 000 pods of 2 000 Deployments in 40
// namespaces, one VulnerabilityReport per Deployment's ReplicaSet - 50 of
// them with 3 000 CVEs, the others with 20 to 120 - a ConfigAuditReport and
// an ExposedSecretReport each, 300 Services, 100 Ingresses and 200 HTTPRoutes.
//
// The fake answers a table read from each report's cells and builds a body
// only when a whole report is read, as the API server would serialize it:
// the overview's cost is the tables', and the detail's includes decoding the
// worst report whole.
func largeCluster(t *testing.T) *fakeHost {
	h := newHost(t)
	const deployments, pods, namespaces = 2000, 5000, 40
	for i := 0; i < deployments; i++ {
		ns := fmt.Sprintf("team-%02d", i%namespaces)
		dep := fmt.Sprintf("svc-%04d", i)
		rs := dep + "-7f9c8d6b5"
		h.add("replicasets", replicaSet(ns, rs, dep, 3), replicaSet(ns, dep+"-5d4c3b2a1", dep, 2))
		counts := Counts{Critical: i % 4, High: 5 + i%13, Medium: 10 + i%40, Low: 5 + i%60, Unknown: i % 3}
		if i < 50 {
			counts = Counts{Critical: 120 - i, High: 600, Medium: 1200, Low: 1000, Unknown: 80 + i}
		}
		image := fmt.Sprintf("registry.example/team-%02d/svc-%03d", i%namespaces, i%600)
		h.add(KindVulnerability, vulnReport("replicaset-"+rs+"-app", scan{ns, "ReplicaSet", rs, "app"}, image, "1.4.2", counts))
		h.add(KindConfigAudit, configReport("replicaset-"+rs, scan{ns, "ReplicaSet", rs, ""}, Counts{High: i % 3, Medium: 2, Low: 4}, 8))
		h.add(KindExposedSecret, secretReport("replicaset-"+rs+"-app", scan{ns, "ReplicaSet", rs, "app"}, image, "1.4.2", Counts{Critical: i % 5 / 4}))
	}
	for i := 0; i < pods; i++ {
		d := i % deployments
		ns := fmt.Sprintf("team-%02d", d%namespaces)
		dep := fmt.Sprintf("svc-%04d", d)
		h.add("pods", pod(ns, fmt.Sprintf("%s-7f9c8d6b5-%05d", dep, i),
			map[string]string{"app": dep, "pod-template-hash": "7f9c8d6b5", "team": ns, "tier": "web", "version": "1.4.2"},
			"ReplicaSet", dep+"-7f9c8d6b5"))
	}
	for i := 0; i < 300; i++ {
		d := i * 6
		ns := fmt.Sprintf("team-%02d", d%namespaces)
		kind := "ClusterIP"
		if i%3 == 0 {
			kind = "LoadBalancer"
		}
		h.add("services", svc(ns, fmt.Sprintf("svc-%04d", d), kind, map[string]string{"app": fmt.Sprintf("svc-%04d", d)}))
	}
	h.reg.gateway = map[string]bool{"httproutes": true}
	for i := 0; i < 200; i++ {
		d := i*10 + 3
		ns := fmt.Sprintf("team-%02d", d%namespaces)
		h.add("httproutes", httpRoute(ns, fmt.Sprintf("route-%03d", i), []map[string]any{{"name": "public", "namespace": "infra"}},
			map[string]any{"name": fmt.Sprintf("svc-%04d", d)}))
	}
	for i := 0; i < 100; i++ {
		d := (i*3 + 1) * 6
		ns := fmt.Sprintf("team-%02d", d%namespaces)
		h.add("ingresses", ing(ns, fmt.Sprintf("public-%03d", i), fmt.Sprintf("svc-%04d", d)))
	}
	return h
}

// measure runs one request and answers what it allocated in all, what it left
// on the heap once collected, and how long it took.
func measure(t *testing.T, h *fakeHost, path string) (alloc, retained uint64, took time.Duration) {
	t.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	rec := serve(t, h, path)
	took = time.Since(start)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
	}
	runtime.ReadMemStats(&after)
	alloc = after.TotalAlloc - before.TotalAlloc
	rec = nil
	runtime.GC()
	runtime.ReadMemStats(&after)
	if after.HeapAlloc > before.HeapAlloc {
		retained = after.HeapAlloc - before.HeapAlloc
	}
	return alloc, retained, took
}

// The ceilings a large cluster is held to, per request. What a request
// allocates includes the fake's own answers (the rows it builds, a body
// decoded whole for the detail), as decoding them in the console would.
// Measured locally on 2026-11-13 (4 cores, 8 threads, load average near 7
// from other builds): the page 23 MiB in 34 ms, the worst workload's detail
// (3 000 CVEs, its body decoded whole) 15 MiB in 27 ms, the pods' cells
// 28 MiB in 44 ms, the Deployments' cells 16 MiB in 25 ms, a brief under
// 1 MiB in 1 ms; at most 1.4 MiB left on the heap after any of them. The
// ceilings are three to four times that, and the time is generous for a
// loaded CI runner.
const (
	maxPageAlloc   = 96 << 20
	maxDetailAlloc = 64 << 20
	maxCellsAlloc  = 96 << 20
	maxBriefAlloc  = 16 << 20
	maxConfigAlloc = 32 << 20
	maxRetained    = 8 << 20
	maxTook        = 10 * time.Second
)

func TestALargeClusterCostsABoundedAmountPerRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("a load test")
	}
	h := largeCluster(t)
	o := decode[Overview](t, serve(t, h, "/"))
	if o.Total != 2000 || len(o.Workloads) != 2000 || o.Capped || len(o.Images) != MaxImages || len(o.Namespaces) != 40 {
		t.Fatalf("total %d rows %d images %d namespaces %d", o.Total, len(o.Workloads), len(o.Images), len(o.Namespaces))
	}
	worst := o.Workloads[0]
	if worst.Counts.Critical != 120 || !worst.Exposed {
		t.Fatalf("worst %+v", worst)
	}
	detail := "/workload/" + worst.Namespace + "/" + worst.Kind + "/" + worst.Name
	d := decode[Detail](t, serve(t, h, detail))
	if d.CVEsTotal != 3000 || len(d.CVEs) != MaxCVEs || len(d.Checks) != 8 {
		t.Fatalf("detail: %d of %d cves, %d checks", len(d.CVEs), d.CVEsTotal, len(d.Checks))
	}
	if c := decode[map[string]Cell](t, serve(t, h, "/cells?kind=pods")); len(c) != 5000 {
		t.Fatalf("%d pod cells", len(c))
	}

	// A page of the table: the 50 worst rows, in 40 namespaces.
	q := url.Values{}
	for _, w := range o.Workloads[:MaxConfigRows] {
		q.Add("w", w.Namespace+"/"+w.Kind+"/"+w.Name)
	}
	config := "/config?" + q.Encode()
	if c := decode[map[string]*Counts](t, serve(t, h, config)); len(c) != MaxConfigRows || c[worst.Namespace+"/"+worst.Kind+"/"+worst.Name] == nil {
		t.Fatalf("config: %d rows", len(c))
	}

	for _, c := range []struct {
		name, path string
		max        uint64
	}{
		{"page", "/", maxPageAlloc},
		{"detail of the worst workload (3 000 CVEs)", detail, maxDetailAlloc},
		{"cells of pods", "/cells?kind=pods", maxCellsAlloc},
		{"cells of deployments", "/cells?kind=deployments", maxCellsAlloc},
		{"config of a page of 50 workloads", config, maxConfigAlloc},
		{"brief", "/brief/" + worst.Namespace + "/" + worst.Name + "?kind=deployments", maxBriefAlloc},
	} {
		alloc, retained, took := measure(t, h, c.path)
		t.Logf("%s: allocated %d MiB, retained %d KiB, %s", c.name, alloc>>20, retained>>10, took.Round(time.Millisecond))
		if alloc > c.max || retained > maxRetained || took > maxTook {
			t.Errorf("%s: allocated %d MiB (max %d), retained %d KiB (max %d), %s (max %s)",
				c.name, alloc>>20, c.max>>20, retained>>10, maxRetained>>10, took, maxTook)
		}
	}
}

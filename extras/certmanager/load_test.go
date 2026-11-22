package certmanager

import (
	"fmt"
	"net/http"
	"runtime"
	"testing"
	"time"
)

// largeCluster is 1 000 Certificates over 50 namespaces, their 1 000 Secrets
// plus 4 000 more, 1 000 Ingresses, 100 Issuers and 300 Challenges: the size the page must
// stay cheap at.
func largeCluster(t testing.TB) *fakeHost {
	h := newHost()
	for i := 0; i < 1000; i++ {
		ns := fmt.Sprintf("team-%02d", i%50)
		h.add("certificates", certificate(t, ns, fmt.Sprintf("cert-%04d", i), testNow.Add(time.Duration(i%120-5)*day), []string{"True", "True", "True", "False"}[i%4]))
	}
	for i := 0; i < 5000; i++ {
		h.add("secrets", obj(t, map[string]any{
			"apiVersion": "v1", "kind": "Secret", "type": "kubernetes.io/tls",
			"metadata": map[string]any{"namespace": fmt.Sprintf("team-%02d", i%50), "name": fmt.Sprintf("cert-%04d-tls", i)},
			"data":     map[string]any{"tls.crt": "LS0tLS1CRUdJTi", "tls.key": "LS0tLS1CRUdJTi"},
		}))
	}
	for i := 0; i < 1000; i++ {
		ns := fmt.Sprintf("team-%02d", i%50)
		h.add("ingresses", ingress(t, ns, fmt.Sprintf("ing-%04d", i), fmt.Sprintf("cert-%04d-tls", i)))
	}
	for i := 0; i < 100; i++ {
		h.add("issuers", issuer(t, "Issuer", fmt.Sprintf("team-%02d", i%50), fmt.Sprintf("ca-%03d", i), []string{"True", "False"}[i%2], "keypair"))
	}
	for i := 0; i < 300; i++ {
		h.add("challenges", challenge(t, fmt.Sprintf("team-%02d", i%50), fmt.Sprintf("ch-%03d", i), "pending", []string{"", "self check failed"}[i%2]))
	}
	return h
}

// The whole page over the large cluster, ten times: bounded in memory and in
// time, and not one Secret read.
func TestLoadLargeCluster(t *testing.T) {
	if testing.Short() {
		t.Skip("load test")
	}
	at(t)
	h := largeCluster(t)

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	var o Overview
	for i := 0; i < 10; i++ {
		rec := serve(t, h, "/x/prod/certmanager/")
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		if i == 0 {
			o = decode[Overview](t, rec)
		}
	}
	elapsed := time.Since(start)
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	if o.Counts.Total != 1000 || o.Shown != 1000 || o.ACME.InFlight != 300 || o.Issuers.Total != 100 {
		t.Fatalf("counts %+v shown %d acme %d issuers %d", o.Counts, o.Shown, o.ACME.InFlight, o.Issuers.Total)
	}
	// What stays after ten answers: nothing is kept between requests.
	if grown := int64(after.HeapAlloc) - int64(before.HeapAlloc); grown > 8<<20 {
		t.Errorf("the heap grew by %d MiB over ten answers", grown>>20)
	}
	// What one answer allocates, all told.
	if per := (after.TotalAlloc - before.TotalAlloc) / 10; per > 16<<20 {
		t.Errorf("one answer allocates %d MiB", per>>20)
	}
	if per := elapsed / 10; per > time.Second {
		t.Errorf("one answer takes %v", per)
	}
	t.Logf("one answer: %v, %d KiB allocated, heap grew %d KiB over ten", elapsed/10,
		(after.TotalAlloc-before.TotalAlloc)/10>>10, (int64(after.HeapAlloc)-int64(before.HeapAlloc))>>10)
	for _, resource := range h.listedResources() {
		if resource == "secrets" {
			t.Fatal("a Secret list was asked for")
		}
	}
}

// The three columns over the large cluster: bounded, and not one Secret read
// even for the Secrets' own column.
func TestLoadColumns(t *testing.T) {
	if testing.Short() {
		t.Skip("load test")
	}
	at(t)
	h := largeCluster(t)
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	for _, kind := range []string{"certificates.cert-manager.io", "secrets", "ingresses"} {
		rec := serve(t, h, "/x/prod/certmanager/cells?kind="+kind)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", kind, rec.Code)
		}
		if n := len(decode[map[string]Cell](t, rec)); n != 1000 {
			t.Errorf("%s: %d cells, want 1000", kind, n)
		}
	}
	elapsed := time.Since(start)
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if per := (after.TotalAlloc - before.TotalAlloc) / 3; per > 16<<20 {
		t.Errorf("one column allocates %d MiB", per>>20)
	}
	if elapsed > 3*time.Second {
		t.Errorf("three columns took %v", elapsed)
	}
	t.Logf("three columns: %v, %d KiB allocated", elapsed, (after.TotalAlloc-before.TotalAlloc)>>10)
	for _, resource := range h.listedResources() {
		if resource == "secrets" {
			t.Fatal("a Secret list was asked for")
		}
	}
}

// Benchmark of one answer, for a figure in a review.
func BenchmarkOverview(b *testing.B) {
	at(b)
	h := largeCluster(b)
	b.ReportAllocs()
	for b.Loop() {
		serve(b, h, "/x/prod/certmanager/")
	}
}

package cnpg

import (
	"fmt"
	"net/http"
	"runtime"
	"testing"
	"time"

	"go.uber.org/goleak"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
)

// No goroutine this extra's routes start outlives the request: it starts
// none, and this holds it to that.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// copyingHost hands out a fresh copy of every object it lists, as the
// console's List does when it decodes the API server's answer: a fake that
// shared its objects would measure none of what a real read allocates.
type copyingHost struct{ *fakeHost }

func (h copyingHost) List(r *http.Request, cluster string, q extension.ListQuery) (extension.Page, error) {
	page, err := h.fakeHost.List(r, cluster, q)
	items := make([]unstructured.Unstructured, len(page.Items))
	for i := range page.Items {
		items[i] = *page.Items[i].DeepCopy()
	}
	page.Items = items
	return page, err
}

// The load the extra is sized for: 300 Clusters of 3 instances, 900 instance
// pods and 5 000 Backups, every Cluster with a ScheduledBackup and a Pooler.
func bigHost() copyingHost {
	h := newHost()
	for i := 0; i < 300; i++ {
		ns := fmt.Sprintf("team-%02d", i%40)
		name := fmt.Sprintf("pg-%03d", i)
		var spec map[string]any
		if i%3 != 0 {
			spec = barman()
		}
		h.add("clusters", pgCluster(ns, name, 3, spec, nil))
		for j := 1; j <= 3; j++ {
			role := "replica"
			if j == 1 {
				role = "primary"
			}
			h.add("pods", podDoc(ns, name, j, role, fmt.Sprintf("node-%d", j)))
		}
		h.add("scheduledbackups", map[string]any{"metadata": map[string]any{"namespace": ns, "name": name + "-nightly"},
			"spec": map[string]any{"cluster": map[string]any{"name": name}, "schedule": "0 0 2 * * *"}})
		h.add("poolers", map[string]any{"metadata": map[string]any{"namespace": ns, "name": name + "-rw"},
			"spec": map[string]any{"cluster": map[string]any{"name": name}, "type": "rw"}})
	}
	for b := 0; b < 5000; b++ {
		i := b % 300
		ns := fmt.Sprintf("team-%02d", i%40)
		phase := "completed"
		if b%17 == 0 {
			phase = "failed"
		}
		h.add("backups", pgBackup(ns, fmt.Sprintf("pg-%03d-%05d", i, b), fmt.Sprintf("pg-%03d", i), phase,
			testNow.Add(-time.Duration(b)*time.Hour/10)))
	}
	return copyingHost{h}
}

// measure runs one request and answers how long it took, how many bytes it
// allocated, and how much of the heap it left behind once collected.
func measure(t *testing.T, h extension.Host, path string) (time.Duration, uint64, int64) {
	t.Helper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	rec := serve(t, h, path)
	took := time.Since(start)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String()[:200])
	}
	size := rec.Body.Len()
	rec = nil
	runtime.GC()
	runtime.ReadMemStats(&after)
	t.Logf("%s: %v, %d KiB allocated, answer %d KiB", path, took, (after.TotalAlloc-before.TotalAlloc)>>10, size>>10)
	return took, after.TotalAlloc - before.TotalAlloc, int64(after.HeapAlloc) - int64(before.HeapAlloc)
}

// The overview of 300 Clusters and 5 000 Backups, and one Cluster's page on
// the same cluster, within a time and a memory ceiling, keeping nothing.
//
// Measured on 2026-11-11 under -race on a 4-core i7: the overview in 0.23 s,
// allocating 19 MiB (most of it the copies the fake hands out, which the real
// List also makes), one cluster's page in 8 ms, the pods column in 23 ms. The
// ceilings leave a slow CI runner room and still catch a quadratic loop or a
// list kept between requests.
func TestLoadThreeHundredClustersAndFiveThousandBackups(t *testing.T) {
	if testing.Short() {
		t.Skip("a load test")
	}
	h := bigHost()

	took, allocated, kept := measure(t, h, "/x/prod/cnpg/")
	if took > 3*time.Second {
		t.Errorf("the overview took %v, the ceiling is 3s", took)
	}
	if allocated > 120<<20 {
		t.Errorf("the overview allocated %d MiB, the ceiling is 120 MiB", allocated>>20)
	}
	if kept > 8<<20 {
		t.Errorf("the overview left %d MiB on the heap: something is kept between requests", kept>>20)
	}

	o := decode[Overview](t, serve(t, h, "/x/prod/cnpg/"))
	if len(o.Clusters) != 300 || !o.Complete {
		t.Fatalf("clusters %d complete %v", len(o.Clusters), o.Complete)
	}
	for _, s := range o.Clusters {
		if len(s.Backup.Recent) > RecentBackups {
			t.Fatalf("%s keeps %d backups, the card draws %d", s.Name, len(s.Backup.Recent), RecentBackups)
		}
	}

	took, allocated, _ = measure(t, h, "/x/prod/cnpg/clusters/team-05/pg-005")
	if took > 2*time.Second || allocated > 40<<20 {
		t.Errorf("one cluster's page: %v and %d MiB, ceilings 2s and 40 MiB", took, allocated>>20)
	}
	took, allocated, _ = measure(t, h, "/x/prod/cnpg/pods")
	if took > time.Second || allocated > 40<<20 {
		t.Errorf("the pods column: %v and %d MiB, ceilings 1s and 40 MiB", took, allocated>>20)
	}
}

// A list past its cap is said, and the counts are then floors.
func TestAListPastItsCapIsSaid(t *testing.T) {
	h := newHost()
	for i := 0; i < MaxClusters+5; i++ {
		h.add("clusters", pgCluster("ns", fmt.Sprintf("pg-%04d", i), 1, nil, nil))
	}
	o := decode[Overview](t, serve(t, h, "/x/prod/cnpg/"))
	if o.Complete || o.Note == "" || len(o.Clusters) != MaxClusters {
		t.Fatalf("complete %v note %q clusters %d", o.Complete, o.Note, len(o.Clusters))
	}
}

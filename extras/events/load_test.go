package events

import (
	"fmt"
	"net/http"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
)

// The ceilings a large cluster is held to (study: 50 000 Events and 2 000
// ReplicaSets in under two seconds and under 64 MiB of extra heap at peak).
const (
	loadEvents      = 50000
	loadReplicaSets = 2000
	maxLoadTime     = 2 * time.Second
	maxLoadHeap     = 64 << 20
)

// streamHost is a host whose Events are generated as they are walked, as the
// API server's pages are: the fake itself holds none of the 50 000, so the
// heap measured is the aggregate's and the walk's, not the fixture's.
type streamHost struct {
	*fakeHost
}

func (s streamHost) List(r *http.Request, c string, q extension.ListQuery) (extension.Page, error) {
	if q.GVR.Resource != "events" || q.Each == nil {
		return s.fakeHost.List(r, c, q)
	}
	for i := 0; i < loadEvents; i++ {
		if err := q.Each(unstructured.Unstructured{Object: loadEvent(i)}); err != nil {
			return extension.Page{}, err
		}
	}
	return extension.Page{Complete: true}, nil
}

// loadEvent is the i-th Event: 300 namespaces' worth of pods across 2 000
// Deployments, a third of them new-shape, over twelve reasons.
func loadEvent(i int) map[string]any {
	ns := fmt.Sprintf("ns-%03d", i%300)
	pod := fmt.Sprintf("web-%04d-7f9c8d6b5-%05d", i%loadReplicaSets, i)
	at := testNow.Add(-time.Duration(i%50) * time.Minute)
	reasons := []string{"BackOff", "Failed", "Unhealthy", "Pulled", "Scheduled", "Started", "Created", "FailedMount", "Killing", "Pulling", "FailedScheduling", "FailedCreate"}
	reason := reasons[i%len(reasons)]
	typ := "Warning"
	switch reason {
	case "Pulled", "Scheduled", "Started", "Created", "Killing", "Pulling":
		typ = "Normal"
	}
	msg := fmt.Sprintf("%s happened for pod %s on node ip-10-0-%d-%d with uid %08x-7c6e-4e5d-835c-11592cc352ff", reason, pod, i%250, i%200, i)
	if i%3 == 0 {
		return newEvent(ns, "Pod", pod, reason, msg, typ, at, i%5)
	}
	return oldEvent(ns, "Pod", pod, reason, msg, typ, 1+i%20, at.Add(-5*time.Minute), at)
}

func loadCluster(t *testing.T) streamHost {
	h := newHost(t)
	for i := 0; i < loadReplicaSets; i++ {
		ns := fmt.Sprintf("ns-%03d", i%300)
		h.add("replicasets",
			replicaSetDoc(ns, fmt.Sprintf("web-%04d-7f9c8d6b5", i), fmt.Sprintf("web-%04d", i), 2, testNow.Add(-time.Duration(i%60)*time.Minute), "app:2", "cfg"),
			replicaSetDoc(ns, fmt.Sprintf("web-%04d-6c8d9", i), fmt.Sprintf("web-%04d", i), 1, testNow.Add(-72*time.Hour), "app:1", "cfg"))
	}
	for i := 0; i < 3000; i++ {
		h.add("pods", podDoc(fmt.Sprintf("ns-%03d", i%300), fmt.Sprintf("web-%04d-7f9c8d6b5-%05d", i%loadReplicaSets, i), fmt.Sprintf("web-%04d-7f9c8d6b5", i%loadReplicaSets), "node-a", nil))
	}
	for i := 0; i < 300; i++ {
		h.add("configmaps", configMapDoc(fmt.Sprintf("ns-%03d", i), "cfg", "kubectl", testNow.Add(-time.Duration(i%40)*time.Minute)))
	}
	return streamHost{h}
}

// peakHeap runs f while sampling the heap, and answers the most the heap grew
// past what it held before.
func peakHeap(f func()) (peak uint64, took time.Duration) {
	debug.SetGCPercent(20)
	defer debug.SetGCPercent(100)
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var stop atomic.Bool
	var wg sync.WaitGroup
	var max uint64
	wg.Add(1)
	go func() {
		defer wg.Done()
		var m runtime.MemStats
		for !stop.Load() {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > max {
				max = m.HeapAlloc
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	start := time.Now()
	f()
	took = time.Since(start)
	stop.Store(true)
	wg.Wait()
	if max > base.HeapAlloc {
		peak = max - base.HeapAlloc
	}
	return peak, took
}

func TestFiftyThousandEventsAndTwoThousandReplicaSets(t *testing.T) {
	h := loadCluster(t)
	e := newExt()
	var r Response
	run := func() {
		rec := serve(t, e, h, "/", "")
		if rec.Code != http.StatusOK {
			t.Errorf("%d %s", rec.Code, rec.Body.String()[:200])
			return
		}
		r = decode[Response](t, rec)
	}
	// The time is measured on a run of its own: sampling the heap stops the
	// world, and would be charged to the aggregate.
	start := time.Now()
	run()
	took := time.Since(start)
	peak, _ := peakHeap(run)
	t.Logf("%d events, %d replicasets: %s, peak extra heap %d MiB (%d causes, %d changes, truncated %v)",
		loadEvents, loadReplicaSets, took, peak>>20, len(r.Causes), len(r.Changes), r.Truncated)
	if raceEnabled {
		t.Log("-race: the ceilings are not asserted")
	} else if took > maxLoadTime {
		t.Errorf("took %s, want under %s", took, maxLoadTime)
	}
	if !raceEnabled && peak > maxLoadHeap {
		t.Errorf("peak extra heap %d MiB, want under %d MiB", peak>>20, maxLoadHeap>>20)
	}
	if len(r.Causes) == 0 || len(r.Changes) == 0 || r.Tiles.Warnings.Count == 0 {
		t.Errorf("nothing aggregated: %d causes, %d changes, tiles %+v", len(r.Causes), len(r.Changes), r.Tiles)
	}
	if !r.Truncated {
		t.Error("50 000 events over thousands of objects must hit a cap and say so")
	}
	groups := 0
	for _, c := range r.Causes {
		groups += len(c.Groups)
	}
	if groups > 2*MaxGroups || len(r.Causes) > 2*MaxCauses || len(r.Changes) > MaxChanges {
		t.Errorf("caps broken: %d groups, %d causes, %d changes", groups, len(r.Causes), len(r.Changes))
	}
}

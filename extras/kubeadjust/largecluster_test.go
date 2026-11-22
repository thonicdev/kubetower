//go:build largecluster

package kubeadjust

// A large cluster's first display, timed against a fake of it.
//
// Off the normal test run (the build tag), because it holds a 50-node,
// 3000-pod, 8000-container cluster and waits out a simulated seven-day read.
// Run it where a measurement is wanted:
//
//	go test -tags largecluster -run TestFirstDisplayOnALargeCluster -v -count=1 ./extras/kubeadjust/
//
// What it fakes is the host's doors, each with a latency of its own, set to
// what a managed cluster and a shared Prometheus answer in:
//
//   - the pods' list (3000 pods): 300 ms; every other list: 60 ms;
//   - metrics-server's usage of every container: 200 ms;
//   - the kubelets' disk figures, 50 nodes: 500 ms;
//   - the seven-day window: a listing, then the batches the core reads (500
//     containers each, two at a time), each batch taking KT_LARGE_BATCH_MS
//     (default 3000, a week of 500 containers' CPU and memory on a Prometheus
//     that is not idle). The first ask starts the read and later ones wait on
//     it, as the core's window cache does (internal/metrics.WindowCache).
//
// Its series are of the real shape - 673 samples per container and half, the
// 15-minute step over seven days, a daily cycle with bursts - but share a few
// backing slices, so holding 8000 containers costs megabytes, not 170 MB. What
// the window costs the core to read and parse is measured on its own, with a
// real HTTP Prometheus, in internal/metrics (TestLargeWindowOverHTTP).
//
// It then plays the front's part: draw the page, ask where the window stands
// every PENDING_POLL_MS (3 s) while it is pending, read the page again once
// it is kept.

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

const (
	largeNodes      = 50
	largePods       = 3000
	largeNamespaces = 60
	// frontPoll is web/app/extras/kubeadjust/data.ts's PENDING_POLL_MS.
	frontPoll = 3 * time.Second
)

func envMillis(name string, def int) time.Duration {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v >= 0 {
		return time.Duration(v) * time.Millisecond
	}
	return time.Duration(def) * time.Millisecond
}

// largeHost is the fake cluster behind latencies.
type largeHost struct {
	*fakeHost

	podsList, otherList, usage, kubelets time.Duration
	batch                                time.Duration
	containers                           int
	series                               map[string]extension.ContainerWindow

	once sync.Once
	done chan struct{}
}

func (h *largeHost) List(r *http.Request, c string, q extension.ListQuery) (extension.Page, error) {
	if q.GVR.Resource == "pods" {
		time.Sleep(h.podsList)
	} else {
		time.Sleep(h.otherList)
	}
	return h.fakeHost.List(r, c, q)
}

func (h *largeHost) ContainerUsage(r *http.Request, c, ns string) (extension.LiveUsage, error) {
	time.Sleep(h.usage)
	return h.fakeHost.ContainerUsage(r, c, ns)
}

func (h *largeHost) Usage(r *http.Request, c string, nodes []string) (extension.Usage, error) {
	time.Sleep(h.kubelets)
	return h.fakeHost.Usage(r, c, nodes)
}

// UsageWindow starts the read on the first ask, on a clock of its own, and
// answers it once it is done; a caller that gives up first gets its own
// context's error, and the read goes on.
func (h *largeHost) UsageWindow(r *http.Request, _, namespace string) (extension.Window, error) {
	h.once.Do(func() {
		go func() {
			batches := (h.containers + WindowBatchForTest - 1) / WindowBatchForTest
			time.Sleep(300*time.Millisecond + time.Duration((batches+1)/2)*h.batch)
			close(h.done)
		}()
	})
	select {
	case <-h.done:
	case <-r.Context().Done():
		return extension.Window{State: extension.WindowUnreadable, Detail: "context deadline exceeded"}, r.Context().Err()
	}
	return extension.Window{State: extension.WindowRead, Detail: "Prometheus, 7d at 15m", Complete: true,
		ReadAt: testNow, Containers: h.series}, nil
}

// WindowBatchForTest is internal/metrics.WindowBatch, which an extra may not
// import.
const WindowBatchForTest = 500

// cycle is one of the shared series: a week at 15 minutes, a daily cycle and
// a burst now and then.
func cycle(base, amp float64, phase int) []extension.Sample {
	out := make([]extension.Sample, 673)
	for i := range out {
		v := base + amp*math.Sin(2*math.Pi*float64(i+phase)/96)
		if (i+phase)%131 == 0 {
			v *= 1.8
		}
		out[i] = extension.Sample{T: float64(1_700_000_000 + 900*i), V: v}
	}
	return out
}

func newLargeHost(t *testing.T) *largeHost {
	f := &fakeHost{t: t}
	var nodes []any
	for i := range largeNodes {
		nodes = append(nodes, node(fmt.Sprintf("node-%02d", i), "16", "64Gi"))
	}
	f.add("nodes", nodes...)
	var namespaces []any
	for i := range largeNamespaces {
		namespaces = append(namespaces, namespaceObj(fmt.Sprintf("ns-%02d", i)))
	}
	f.add("namespaces", namespaces...)

	cpuShapes := []([]extension.Sample){cycle(120, 60, 0), cycle(40, 20, 13), cycle(300, 150, 47), cycle(15, 5, 71)}
	memShapes := []([]extension.Sample){cycle(600e6, 80e6, 5), cycle(200e6, 10e6, 29), cycle(1.5e9, 3e8, 61)}

	h := &largeHost{fakeHost: f, series: map[string]extension.ContainerWindow{}, done: make(chan struct{})}
	usage := map[string][2]int64{}
	var pods, sets []any
	for i := range largePods {
		ns := fmt.Sprintf("ns-%02d", i%largeNamespaces)
		rs := fmt.Sprintf("app-%d-5d9c", i/3)
		if i%3 == 0 {
			sets = append(sets, replicaSet(ns, rs, fmt.Sprintf("app-%d", i/3)))
		}
		// 3000 pods, 8000 containers: two or three each.
		var cs []corev1.Container
		for j := range 2 + i%2 + (i%7)/6 {
			cs = append(cs, corev1.Container{Name: fmt.Sprintf("c%d", j), Resources: corev1.ResourceRequirements{
				Requests: quantities("250m", "512Mi"), Limits: quantities("1", "1Gi")}})
		}
		name := fmt.Sprintf("app-%d-5d9c-%d", i/3, i%3)
		pods = append(pods, runningPod(ns, name, rs, fmt.Sprintf("node-%02d", i%largeNodes), cs...))
		for j, c := range cs {
			key := ns + "/" + name + "/" + c.Name
			usage[key] = [2]int64{int64(40 + 30*(i%5)), int64(300+80*(i%4)) << 20}
			h.series[key] = extension.ContainerWindow{CPU: cpuShapes[(i+j)%len(cpuShapes)], Memory: memShapes[(i+j)%len(memShapes)]}
		}
	}
	f.add("pods", pods...)
	f.add("replicasets", sets...)
	f.usage = live(usage)
	h.containers = len(usage)

	h.podsList = envMillis("KT_LARGE_PODS_MS", 300)
	h.otherList = envMillis("KT_LARGE_LIST_MS", 60)
	h.usage = envMillis("KT_LARGE_USAGE_MS", 200)
	h.kubelets = envMillis("KT_LARGE_KUBELETS_MS", 500)
	h.batch = envMillis("KT_LARGE_BATCH_MS", 3000)
	return h
}

func serve(t *testing.T, h extension.Host, pattern, query string) (time.Duration, *httptest.ResponseRecorder) {
	t.Helper()
	var handler http.Handler
	for _, r := range testExtra().Routes(h) {
		if r.Pattern == pattern {
			handler = r.Handler
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/x/c/kubeadjust"+strings.TrimPrefix(pattern, "/{$}")+"?"+query, nil)
	req.SetPathValue("cluster", "large")
	rec := httptest.NewRecorder()
	start := time.Now()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", pattern, rec.Code, rec.Body.String())
	}
	return time.Since(start), rec
}

func TestFirstDisplayOnALargeCluster(t *testing.T) {
	h := newLargeHost(t)
	t.Logf("cluster: %d nodes, %d pods, %d containers, %d series per half; window batch %s, windowWait %s",
		largeNodes, largePods, h.containers, h.containers, h.batch, windowWait)

	begin := time.Now()
	first, rec := serve(t, h, "/{$}", url.Values{}.Encode())
	state := windowStateOf(t, rec)
	t.Logf("first display: %s (window %s) Server-Timing: %s", first.Round(time.Millisecond), state, rec.Header().Get("Server-Timing"))

	polls := 0
	for state == string(WindowPending) {
		time.Sleep(frontPoll)
		polls++
		_, w := serve(t, h, "/window", "")
		state = windowStateOf(t, w)
	}
	historyAt := time.Since(begin)
	full, rec := serve(t, h, "/{$}", "")
	t.Logf("history arrived at %s after %d polls; the full page then took %s (window %s) Server-Timing: %s",
		historyAt.Round(time.Millisecond), polls, full.Round(time.Millisecond), windowStateOf(t, rec), rec.Header().Get("Server-Timing"))
	t.Logf("full display: %s", (historyAt + full).Round(time.Millisecond))
}

func windowStateOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var a struct {
		Window string `json:"window"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	return a.Window
}

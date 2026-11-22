//go:build largecluster

package metrics

// What the core pays to read a large cluster's seven-day window, over HTTP.
//
// Off the normal test run (the build tag): it serves and decodes about 330 MB
// of JSON. Run it where a measurement is wanted:
//
//	go test -tags largecluster -run TestLargeWindowOverHTTP -v -count=1 ./internal/metrics/
//
// An httptest server plays Prometheus: it lists 8000 containers (50 nodes'
// worth, about 3000 pods) and answers each batch's CPU and memory range
// queries with series of the real shape and the real wire format - 673
// samples, `[unix,"value"]`, a rate printed to seventeen digits - after
// KT_LARGE_PROM_MS of its own (default 0, so what is measured is this side's
// share: the HTTP read, the decoding, the assembly). A real Prometheus adds its
// own time per query to that, which the table in the pull request models as
// `queries / windowInFlight / 2 x latency`.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// httpProm is a RangeQuerier over a real HTTP Prometheus API.
type httpProm struct{ base string }

func (p httpProm) get(ctx context.Context, path string, v url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+path+"?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	return io.ReadAll(io.LimitReader(res.Body, 16<<20))
}

func (p httpProm) Query(ctx context.Context, _, promQL string) ([]byte, error) {
	return p.get(ctx, "/api/v1/query", url.Values{"query": {promQL}})
}

func (p httpProm) QueryRange(ctx context.Context, _, promQL string, start, end time.Time, step time.Duration) ([]byte, error) {
	return p.get(ctx, "/api/v1/query_range", url.Values{"query": {promQL},
		"start": {strconv.FormatInt(start.Unix(), 10)}, "end": {strconv.FormatInt(end.Unix(), 10)},
		"step": {strconv.Itoa(int(step.Seconds()))}})
}

type largeContainer struct{ ns, pod, name string }

// largeCluster is 3000 pods of two to three containers, 8000 in all.
func largeCluster() (all []largeContainer, byPod map[string][]largeContainer) {
	byPod = map[string][]largeContainer{}
	for i := range 3000 {
		ns := fmt.Sprintf("ns-%02d", i%60)
		pod := fmt.Sprintf("app-%d-5d9c-%d", i/3, i%3)
		for j := range 2 + i%2 + (i%7)/6 {
			c := largeContainer{ns, pod, fmt.Sprintf("c%d", j)}
			all = append(all, c)
			byPod[pod] = append(byPod[pod], c)
		}
	}
	return all, byPod
}

// seriesJSON is one series' values in Prometheus's wire format.
func seriesJSON(base, amp float64, phase int, end int64) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < 673; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		v := base + amp*float64((i+phase)%96)/96
		fmt.Fprintf(&b, `[%d,"%s"]`, end-int64(672-i)*900, strconv.FormatFloat(v, 'g', 17, 64))
	}
	b.WriteByte(']')
	return b.String()
}

var podSelector = regexp.MustCompile(`pod=~"([^"]*)"`)

func TestLargeWindowOverHTTP(t *testing.T) {
	all, byPod := largeCluster()
	latency := time.Duration(0)
	if v, err := strconv.Atoi(os.Getenv("KT_LARGE_PROM_MS")); err == nil {
		latency = time.Duration(v) * time.Millisecond
	}
	end := time.Now().Truncate(WindowStep).Unix()
	cpu := []string{seriesJSON(0.12, 0.06, 0, end), seriesJSON(0.04, 0.02, 13, end), seriesJSON(0.3, 0.15, 47, end)}
	mem := []string{seriesJSON(6e8, 8e7, 5, end), seriesJSON(2e8, 1e7, 29, end)}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(latency)
		q := r.URL.Query().Get("query")
		if r.URL.Path == "/api/v1/query" {
			rows := make([]string, 0, len(all))
			for _, c := range all {
				rows = append(rows, fmt.Sprintf(`{"metric":{"namespace":%q,"pod":%q,"container":%q},"value":[%d,"1"]}`, c.ns, c.pod, c.name, end))
			}
			fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[%s]}}`, strings.Join(rows, ","))
			return
		}
		m := podSelector.FindStringSubmatch(q)
		if m == nil {
			http.Error(w, "no pod selector", http.StatusBadRequest)
			return
		}
		pods := strings.Split(m[1], "|")
		sort.Strings(pods)
		shapes := mem
		if strings.Contains(q, "container_cpu_usage_seconds_total") {
			shapes = cpu
		}
		io.WriteString(w, `{"status":"success","data":{"resultType":"matrix","result":[`)
		n := 0
		for _, p := range pods {
			for _, c := range byPod[strings.ReplaceAll(p, `\\.`, ".")] {
				if n > 0 {
					io.WriteString(w, ",")
				}
				fmt.Fprintf(w, `{"metric":{"namespace":%q,"pod":%q,"container":%q},"values":%s}`, c.ns, c.pod, c.name, shapes[n%len(shapes)])
				n++
			}
		}
		io.WriteString(w, `]}}`)
	}))
	defer srv.Close()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	w, err := ReadWindow(context.Background(), httpProm{srv.URL}, "large", "")
	took := time.Since(start)
	runtime.ReadMemStats(&after)
	if err != nil || w.State != WindowRead {
		t.Fatalf("window: %s %q, %v", w.State, w.Detail, err)
	}
	t.Logf("%d containers in %d batches (%d range queries): read, sent, decoded and assembled in %s "+
		"with %s of Prometheus time per query; %d MiB allocated, %d MiB live afterwards",
		len(w.CPU), w.Batches, 2*w.Batches, took.Round(time.Millisecond), latency,
		(after.TotalAlloc-before.TotalAlloc)>>20, (after.HeapAlloc-before.HeapAlloc)>>20)
}

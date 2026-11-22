package metrics

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thonicdev/kubetower/safego"
)

// Reading a window in batches.
//
// A week of series for every container of a large cluster does not fit in one
// answer (WindowBatch), and declining the window past a size left the page with
// one sample per pod and an instruction to narrow it. The containers are now
// listed first, cut into batches of WindowBatch, each batch read with its own
// pair of range queries, and the answers assembled. What bounds the read is no
// longer the scope but these:
//
//   - every answer is still capped at 16 MB (prom.go), and a batch is sized to
//     stay under it;
//   - windowInFlight batches at a time, so a Prometheus shared with everything
//     else is not flooded;
//   - one budget for the whole read, windowBudget, after which a batch not yet
//     read is a batch not read.
//
// **A batch that fails is a part of the window that is missing, never a window
// that is missing.** The rest is drawn, and the Detail says how many batches
// could not be read and why; only every batch failing is an unreadable window.

const (
	// windowInFlight is how many batches are read at once. Each is two range
	// queries, so two batches are four queries in flight.
	windowInFlight = 2

	// windowBudget is how long the whole read may take, the listing and every
	// batch: three rounds of the range query's own 45 s timeout, so a batch
	// that times out once does not cost the page the others.
	windowBudget = 135 * time.Second

	// maxSelectorBytes bounds the pod names one batch writes into its
	// selector. The queries travel as a GET, through the API server's proxy,
	// and a query string of a few tens of kilobytes is past what some proxies
	// on the way accept; a batch closes at whichever bound comes first.
	maxSelectorBytes = 16 << 10
)

// batch is one pair of range queries: the containers it is for, and the
// selector that asks for them.
type batch struct {
	keys     map[string]bool
	selector string
}

// batchResult is one batch's two halves.
type batchResult struct {
	cpu, memory    map[string][]Point
	cpuErr, memErr error
}

// readBatches reads every batch, windowInFlight at a time, and assembles them.
func readBatches(ctx context.Context, q RangeQuerier, cluster string, batches []batch, start, end time.Time, count int) Window {
	results := make([]batchResult, len(batches))
	slots := make(chan struct{}, windowInFlight)
	var wg sync.WaitGroup
	for i := range batches {
		wg.Add(1)
		// Through safego, so a panic in one batch is that batch's error and
		// not the end of the process. done runs after f has returned or
		// panicked, so the batch's result is settled before Wait lets go.
		safego.Go("metrics: a window batch", func() error {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				// Never started: the budget ran out first.
				err := fmt.Errorf("not read within %s: %w", windowBudget, ctx.Err())
				results[i] = batchResult{cpuErr: err, memErr: err}
				return nil
			}
			results[i] = readBatch(ctx, q, cluster, batches[i], start, end)
			return nil
		}, func(err error) {
			if err != nil {
				results[i] = batchResult{cpuErr: err, memErr: err}
			}
			wg.Done()
		})
	}
	wg.Wait()
	return assemble(results, count)
}

// readBatch asks for one batch's CPU and memory together, on a session that is
// already open (the pool in prom.go): in series, the slower one's latency
// would be added to every render for nothing.
//
// Only the batch's own containers are kept. Its selector names namespaces and
// pods separately, so a pod name one of its namespaces shares with another
// matches there too; that series is another batch's, or nobody's this read is
// for, and keeping it here would hand over what keep refused.
func readBatch(ctx context.Context, q RangeQuerier, cluster string, b batch, start, end time.Time) batchResult {
	var out batchResult
	var wg sync.WaitGroup
	// Through safego, so a panic does not end the process: a panic decoding
	// one half is that half's error, and the other half is drawn, as for any
	// other failure.
	wg.Add(2)
	safego.Go("metrics: the CPU window", func() error {
		var err error
		out.cpu, err = rangeSeries(ctx, q, cluster, fmt.Sprintf(queryCPUSeries, b.selector, windowRate), start, end, 1000)
		return err
	}, func(err error) { out.cpuErr = err; wg.Done() })
	safego.Go("metrics: the memory window", func() error {
		var err error
		out.memory, err = rangeSeries(ctx, q, cluster, fmt.Sprintf(queryMemorySeries, b.selector, short(WindowStep)), start, end, 1)
		return err
	}, func(err error) { out.memErr = err; wg.Done() })
	wg.Wait()
	for _, m := range []map[string][]Point{out.cpu, out.memory} {
		for k := range m {
			if !b.keys[k] {
				delete(m, k)
			}
		}
	}
	return out
}

// assemble puts the batches back together, and says what could not be read.
//
// A half that failed in every batch is that half failing (CPUErr, MemoryErr),
// as when the window was one query; both, and there is no window. A batch
// that failed while others were read makes a partial window: drawn, and said.
func assemble(results []batchResult, count int) Window {
	n := len(results)
	out := Window{State: WindowRead, Count: count, Batches: n,
		CPU: map[string][]Point{}, Memory: map[string][]Point{}}
	var cpuFailed, memFailed int
	var firstCPU, firstMem error
	for _, r := range results {
		if r.cpuErr != nil {
			cpuFailed++
			firstCPU = cmp.Or(firstCPU, r.cpuErr)
		}
		if r.memErr != nil {
			memFailed++
			firstMem = cmp.Or(firstMem, r.memErr)
		}
		for k, v := range r.cpu {
			out.CPU[k] = v
		}
		for k, v := range r.memory {
			out.Memory[k] = v
		}
	}
	if cpuFailed == n && memFailed == n {
		// One cause, said once.
		failed := failedWindow(firstCPU)
		failed.Count, failed.Batches = count, n
		return failed
	}
	if len(out.CPU) == 0 && len(out.Memory) == 0 && cpuFailed == 0 && memFailed == 0 {
		return Window{State: WindowEmpty, Count: count, Batches: n,
			Detail: "Prometheus answered the window query with no usable series"}
	}
	if cpuFailed == n {
		out.CPUErr = firstCPU
	}
	if memFailed == n {
		out.MemoryErr = firstMem
	}
	var partialErr error
	for _, r := range results {
		if (r.cpuErr != nil && cpuFailed < n) || (r.memErr != nil && memFailed < n) {
			out.Partial++
			partialErr = cmp.Or(partialErr, r.cpuErr, r.memErr)
		}
	}

	out.Detail = fmt.Sprintf("Prometheus, %s at %s", short(WindowSpan), short(WindowStep))
	if n > 1 {
		out.Detail += fmt.Sprintf(" · %d containers read in %d batches", count, n)
	}
	// One half failing is not the other half's problem, and the reader has to be
	// able to see which half of the page is which.
	if out.CPUErr != nil {
		out.Detail += " · memory only, the CPU window failed (" + shortError(out.CPUErr) + ")"
	}
	if out.MemoryErr != nil {
		out.Detail += " · CPU only, the memory window failed (" + shortError(out.MemoryErr) + ")"
	}
	if out.Partial > 0 {
		out.PartialNote = fmt.Sprintf("%d of %d batches could not be read in full (%s), so the containers "+
			"in them have no window, or half of one", out.Partial, n, shortError(partialErr))
		out.Detail += " · " + out.PartialNote
	}
	return out
}

// batchesOf cuts the listed containers into batches of WindowBatch, in key
// order so a pod's containers stay together, each batch closing early when its
// pod names would pass maxSelectorBytes. A key whose names are not Kubernetes
// names is left out rather than written into a query: it did not come from a
// pod.
func batchesOf(keys []string) []batch {
	sort.Strings(keys)
	var out []batch
	cur := batch{keys: map[string]bool{}}
	pods, namespaces := map[string]bool{}, map[string]bool{}
	size := 0
	flush := func() {
		if len(cur.keys) == 0 {
			return
		}
		cur.selector = `,namespace=~"` + alternation(namespaces) + `",pod=~"` + alternation(pods) + `"`
		out = append(out, cur)
		cur, pods, namespaces, size = batch{keys: map[string]bool{}}, map[string]bool{}, map[string]bool{}, 0
	}
	for _, k := range keys {
		parts := strings.Split(k, "/")
		if len(parts) != 3 || !kubeName(parts[0]) || !kubeName(parts[1]) || parts[2] == "" {
			continue
		}
		grows := 0
		if !pods[parts[1]] {
			grows = len(parts[1]) + 3 // a separator and room for escaped dots
		}
		if len(cur.keys) >= WindowBatch || (len(cur.keys) > 0 && size+grows > maxSelectorBytes) {
			flush()
			grows = len(parts[1]) + 3
		}
		cur.keys[k] = true
		pods[parts[1]], namespaces[parts[0]] = true, true
		size += grows
	}
	flush()
	return out
}

// alternation is a regular expression matching exactly the given names, each
// dot escaped for the regular expression and that backslash for PromQL's
// string. Prometheus anchors a label regex at both ends.
func alternation(names map[string]bool) string {
	list := make([]string, 0, len(names))
	for n := range names {
		list = append(list, strings.ReplaceAll(n, ".", `\\.`))
	}
	sort.Strings(list)
	return strings.Join(list, "|")
}

// kubeName is a DNS-1123 subdomain's alphabet: lower case, digits, dash, dot.
// Nothing else is ever written into a selector.
func kubeName(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

// promLabels is an instant vector with its labels, for the listing.
type promLabels struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []struct {
			Metric struct {
				Namespace string `json:"namespace"`
				Pod       string `json:"pod"`
				Container string `json:"container"`
			} `json:"metric"`
		} `json:"result"`
	} `json:"data"`
}

// parseKeys reads the listing: one ContainerKey per series. An empty result
// is no container and not an error. A row missing its pod or container label
// is dropped, as parseSeries drops its series.
func parseKeys(raw []byte) ([]string, error) {
	var resp promLabels
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("Prometheus answered with something unreadable: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("Prometheus rejected the query: %s", resp.Error)
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range resp.Data.Result {
		m := r.Metric
		if m.Pod == "" || m.Container == "" {
			continue
		}
		key := ContainerKey(m.Namespace, m.Pod, m.Container)
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	return out, nil
}

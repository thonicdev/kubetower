package web

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/overview"
)

// The series the metrics panel sends, and which chart each one belongs to.
//
// `apiReads.Metrics` must walk `NetIn`/`NetOut` as well as `{CPU, Memory,
// Disk}`: a panel that finds nothing under the Network icon falls back to
// `series[0]`, which is CPU. Disk's key must be `disk`, which the icon row
// uses, not `strings.ToLower(Title)` (`container filesystem` on a pod), or the
// Filesystem tab would draw CPU too.
//
// These tests pin the series *set* — every key, the chart each is drawn under,
// and that a two-line chart arrives as two lines — against a fake Prometheus that
// answers every question with one sample, so nothing here depends on a cluster.

// promFake answers every range query with one sample and the probe with every
// counter the panel knows, unless a test narrows either.
type promFake struct {
	// ranges records the range queries, so a test can say which counters were
	// asked for. Behind a mutex: `overview.gather` asks for every series of a
	// panel from its own goroutine, so a fake that appends bare is a data race
	// - which `go test -race` reports. The shared slice is this fake's alone.
	mu     sync.Mutex
	ranges []string
	// exported is the counters the probe reports; nil means all of them.
	exported []string
	// emptyFor is a substring; a range query containing it answers no samples.
	emptyFor string
}

func (f *promFake) Query(context.Context, string, string) ([]byte, error) {
	names := f.exported
	if names == nil {
		names = []string{
			"container_cpu_usage_seconds_total",
			"container_memory_working_set_bytes",
			"container_network_receive_bytes_total",
			"container_network_transmit_bytes_total",
			"container_fs_usage_bytes",
		}
	}
	rows := make([]any, 0, len(names))
	for _, name := range names {
		rows = append(rows, map[string]any{
			"metric": map[string]string{"__name__": name},
			"value":  []any{1.0, "12"},
		})
	}
	return json.Marshal(map[string]any{"status": "success", "data": map[string]any{"result": rows}})
}

func (f *promFake) QueryRange(_ context.Context, _, promQL string, _, _ time.Time, _ time.Duration) ([]byte, error) {
	f.mu.Lock()
	f.ranges = append(f.ranges, promQL)
	f.mu.Unlock()
	if f.emptyFor != "" && strings.Contains(promQL, f.emptyFor) {
		return json.Marshal(map[string]any{"status": "success", "data": map[string]any{"result": []any{}}})
	}
	return json.Marshal(map[string]any{
		"status": "success",
		"data":   map[string]any{"result": []any{map[string]any{"values": [][]any{{1.0, "1"}, {2.0, "2"}}}}},
	})
}

func podMetrics(t *testing.T, prom *promFake) api.MetricsPanel {
	t.Helper()
	reads := apiReads{&Server{series: prom, gate: accesstest.Allow()}}
	panel, fail := reads.Metrics(context.Background(), api.MetricsQuery{
		ObjectRef: api.ObjectRef{Cluster: "prod", Kind: "pods", Namespace: "argo", Name: "api-0"},
		Range:     "1h",
	})
	if fail != nil {
		t.Fatalf("the panel failed: %+v", fail)
	}
	return panel
}

func seriesByKey(panel api.MetricsPanel) map[string]api.MetricsSeries {
	out := make(map[string]api.MetricsSeries, len(panel.Series))
	for _, one := range panel.Series {
		out[one.Key] = one
	}
	return out
}

// Every chart the icon row offers has at least one series drawn under it, keyed
// by the chart, and the network chart has exactly two: receive and transmit.
func TestEveryOfferedChartHasItsOwnSeries(t *testing.T) {
	panel := podMetrics(t, &promFake{})

	want := map[string]string{
		"cpu":        overview.ChartCPU,
		"memory":     overview.ChartMemory,
		"network_rx": overview.ChartNetwork,
		"network_tx": overview.ChartNetwork,
		"disk":       overview.ChartDisk,
	}
	got := seriesByKey(panel)
	for key, chart := range want {
		one, ok := got[key]
		if !ok {
			t.Errorf("no series keyed %q; the keys are %v", key, seriesKeysOf(panel))
			continue
		}
		if one.Chart != chart {
			t.Errorf("series %q is drawn under chart %q, want %q", key, one.Chart, chart)
		}
		if len(one.Data.Values) != 2 {
			t.Errorf("series %q carries %d values, want the 2 the fake answered", key, len(one.Data.Values))
		}
	}
	if len(panel.Series) != len(want) {
		t.Errorf("%d series, want %d: %v", len(panel.Series), len(want), seriesKeysOf(panel))
	}

	// And every chart the icon row offers as available can be found under its
	// own key by the client, which is the whole point: a client that cannot
	// find the chosen chart is a client that falls back to CPU.
	for _, choice := range panel.Metrics {
		if !choice.Available {
			continue
		}
		found := false
		for _, one := range panel.Series {
			if one.Chart == choice.Key {
				found = true
			}
		}
		if !found {
			t.Errorf("the icon row offers %q and no series is drawn under it", choice.Key)
		}
	}
}

// The two network lines are two Prometheus questions and two labels, so a
// picture of half a conversation cannot be mistaken for the whole.
func TestTheNetworkChartIsTwoLinesWithTwoNames(t *testing.T) {
	prom := &promFake{}
	panel := podMetrics(t, prom)
	got := seriesByKey(panel)

	rx, tx := got["network_rx"], got["network_tx"]
	if rx.Label == tx.Label {
		t.Errorf("receive and transmit carry the same label %q", rx.Label)
	}
	if !strings.Contains(strings.ToLower(rx.Label), "in") || !strings.Contains(strings.ToLower(tx.Label), "out") {
		t.Errorf("labels do not say which way the traffic goes: rx=%q tx=%q", rx.Label, tx.Label)
	}
	if rx.Unit == "" || rx.Unit != tx.Unit {
		t.Errorf("the two lines must share a unit to share an axis: rx=%q tx=%q", rx.Unit, tx.Unit)
	}
	asked := 0
	prom.mu.Lock()
	askedRanges := append([]string(nil), prom.ranges...)
	prom.mu.Unlock()
	for _, q := range askedRanges {
		if strings.Contains(q, "container_network_") {
			asked++
		}
	}
	if asked != 2 {
		t.Errorf("%d network range queries, want 2 (receive and transmit): %v", asked, askedRanges)
	}
}

// A series that could not be read is sent as itself — empty, with the sentence
// saying why — and never dropped. Dropping it is what made the client fall back
// to whatever series came first.
func TestAnUnreadableSeriesIsSentWithItsReasonRatherThanDropped(t *testing.T) {
	// A cluster whose Prometheus never heard of the network counters: the probe
	// says so, and the range queries come back empty.
	prom := &promFake{
		exported: []string{
			"container_cpu_usage_seconds_total",
			"container_memory_working_set_bytes",
			"container_fs_usage_bytes",
		},
		emptyFor: "container_network_",
	}
	panel := podMetrics(t, prom)
	got := seriesByKey(panel)

	for _, key := range []string{"network_rx", "network_tx"} {
		one, ok := got[key]
		if !ok {
			t.Fatalf("series %q was dropped; the keys are %v", key, seriesKeysOf(panel))
		}
		if len(one.Data.Values) != 0 {
			t.Errorf("series %q carries %d values for a counter that does not exist", key, len(one.Data.Values))
		}
		if one.Reason == nil || one.Reason.Message == "" {
			t.Errorf("series %q has no samples and no sentence saying why", key)
			continue
		}
		// Named as the counter itself: the probe asks
		// for per-pod series, and the sentence says "has no per-pod <counter>".
		if !strings.Contains(one.Reason.Message, "container_network_") {
			t.Errorf("series %q carries a reason that does not name the missing counter: %q", key, one.Reason.Message)
		}
		if one.Data.Trustworthy == nil || *one.Data.Trustworthy {
			t.Errorf("series %q is vouched for and has nothing in it", key)
		}
	}

	// The lines that were read carry no reason: a sentence on a chart that
	// works would be drawn as a complaint about it.
	if got["cpu"].Reason != nil {
		t.Errorf("cpu was read and carries a reason: %+v", got["cpu"].Reason)
	}
}

func seriesKeysOf(panel api.MetricsPanel) []string {
	out := make([]string, 0, len(panel.Series))
	for _, one := range panel.Series {
		out = append(out, one.Key+"→"+one.Chart)
	}
	return out
}

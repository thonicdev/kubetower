package web

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/metrics"
	"github.com/thonicdev/kubetower/internal/overview"
	"github.com/thonicdev/kubetower/internal/resources"
)

// Usage and history are two questions, and a cluster with metrics-server
// and no Prometheus answers the first.
//
// On such a cluster (docker-desktop), the Capacity tiles and the pod
// drawer's charts must not say "used - not measured: docker-desktop has
// metrics-server and no Prometheus, so it holds no history" on a page whose
// "Most used" lists every pod's live CPU and beside a Nodes list drawing
// 230m / 4.0 GiB. Nor may the pod's panel say that one sentence three times.
//
// These pin the three states the panel and the tile can be in: history, a
// reading of now with no history, and neither.

// noHistory is the backend as a metrics-server-only cluster presents it: every
// question put to Prometheus is refused with the error internal/metrics
// returns for exactly this shape (`unqueryable`) - typed, so a
// cluster with no Prometheus is told apart from a Prometheus that failed.
type noHistory struct{}

const noHistorySentence = "docker-desktop has metrics-server and no Prometheus, so it holds no history to query"

func (noHistory) Query(context.Context, string, string) ([]byte, error) {
	return nil, metrics.NoHistory{Sentence: noHistorySentence}
}

func (noHistory) QueryRange(context.Context, string, string, time.Time, time.Time, time.Duration) ([]byte, error) {
	return nil, metrics.NoHistory{Sentence: noHistorySentence}
}

// usageNow is metrics-server: one figure per pod and per node, and nothing
// about the past.
type usageNow struct {
	pods  map[string]metrics.Usage
	nodes map[string]metrics.Usage
}

func (u usageNow) PodUsage(context.Context, string, string) (map[string]metrics.Usage, error) {
	if u.pods == nil {
		return nil, &metrics.Unavailable{Cluster: "prod", Kind: metrics.KindNone, Detail: "metrics.k8s.io is not there"}
	}
	return u.pods, nil
}

func (u usageNow) NodeUsage(context.Context, string) (map[string]metrics.Usage, error) {
	if u.nodes == nil {
		return nil, &metrics.Unavailable{Cluster: "prod", Kind: metrics.KindNone, Detail: "metrics.k8s.io is not there"}
	}
	return u.nodes, nil
}

func (u usageNow) ContainerUsage(context.Context, string, string) (map[string]metrics.Usage, error) {
	return nil, errors.New("not asked here")
}

func metricsPanelFor(t *testing.T, s *Server, kind, namespace, name string) api.MetricsPanel {
	t.Helper()
	s.gate = accesstest.Allow()
	panel, fail := apiReads{s}.Metrics(context.Background(), api.MetricsQuery{
		ObjectRef: api.ObjectRef{Cluster: "prod", Kind: kind, Namespace: namespace, Name: name},
		Range:     "1h",
	})
	if fail != nil {
		t.Fatalf("the panel failed: %+v", fail)
	}
	return panel
}

// Instant only: the pod's CPU and memory as they stand, from metrics-server,
// and the reason there is no series said once - on the lines, and nowhere else.
func TestAPodOnAClusterWithNoHistoryShowsItsUsageNowAndSaysWhyOnce(t *testing.T) {
	s := &Server{series: noHistory{}, usage: usageNow{pods: map[string]metrics.Usage{
		"api-0":   {CPUMillicores: 12, MemoryBytes: 48 << 20, From: metrics.KindMetricsServer},
		"other-1": {CPUMillicores: 900, MemoryBytes: 1 << 30, From: metrics.KindMetricsServer},
	}}}
	panel := metricsPanelFor(t, s, "pods", "argo", "api-0")
	got := seriesByKey(panel)

	for key, want := range map[string]struct {
		value float64
		text  string
	}{
		"cpu":    {12, "12m"},
		"memory": {48 << 20, "48 MiB"},
	} {
		line, ok := got[key]
		if !ok {
			t.Fatalf("no %s line; the keys are %v", key, seriesKeysOf(panel))
		}
		if line.Current == nil {
			t.Errorf("the %s line carries no reading of now, on a cluster whose metrics-server answered", key)
			continue
		}
		if !line.Current.Trustworthy || line.Current.Value == nil || *line.Current.Value != want.value {
			t.Errorf("the %s line's reading of now is %+v; want %v, the figure metrics-server gave for this pod and not another", key, line.Current, want.value)
		}
		if line.Current.Text != want.text {
			t.Errorf("the %s line's reading is spelt %q; want %q, as the tables spell it", key, line.Current.Text, want.text)
		}
		if line.Reason == nil || line.Reason.Message != noHistorySentence {
			t.Errorf("the %s line no longer says why it has no series: %+v", key, line.Reason)
		}
	}
	for _, key := range []string{"network_rx", "network_tx", "disk"} {
		if got[key].Current != nil {
			t.Errorf("the %s line carries a reading of now; metrics-server measures CPU and memory only", key)
		}
	}

	// Said once. The sentence is on the lines; the panel's own attribution
	// names where the figures came from and repeats nothing.
	if panel.Caption != "" {
		t.Errorf("caption is %q; a panel that drew no series has no span to caption", panel.Caption)
	}
	if strings.Contains(panel.Caption, "no history") {
		t.Errorf("the caption repeats the lines' reason: %q", panel.Caption)
	}
}

// History: the series is drawn and its newest sample is the figure, so no
// reading of now is asked for or sent, and the attribution is what it was.
func TestAPodWithHistoryIsDrawnFromItsSeriesAlone(t *testing.T) {
	asked := &countingUsage{}
	s := &Server{series: &promFake{}, usage: asked}
	panel := metricsPanelFor(t, s, "pods", "argo", "api-0")

	for _, line := range panel.Series {
		if line.Current != nil {
			t.Errorf("the %s line has samples and carries a reading of now as well: %+v", line.Key, line.Current)
		}
	}
	if asked.calls != 0 {
		t.Errorf("metrics-server was asked %d times for a panel whose series answered", asked.calls)
	}
	if panel.Caption != "the last 1h" {
		t.Errorf("caption is %q; want the span the series covers", panel.Caption)
	}
}

// Neither: nothing drawn and nothing read. The lines' reasons are the whole of
// what there is to say, and the attribution says nothing rather than saying
// them again.
func TestAPodWithNeitherHistoryNorUsageSaysWhyOnce(t *testing.T) {
	s := &Server{series: noHistory{}, usage: usageNow{}}
	panel := metricsPanelFor(t, s, "pods", "argo", "api-0")

	for _, line := range panel.Series {
		if line.Current != nil {
			t.Errorf("the %s line carries a reading of now that nobody gave: %+v", line.Key, line.Current)
		}
		if line.Reason == nil || line.Reason.Message == "" {
			t.Errorf("the %s line has nothing and says nothing", line.Key)
		}
	}
	if panel.Caption != "" {
		t.Errorf("the caption repeats what the lines say: %q", panel.Caption)
	}
}

// A node's panel reads the node's own figure, not a pod's and not the sum.
func TestANodeOnAClusterWithNoHistoryShowsItsOwnUsageNow(t *testing.T) {
	s := &Server{series: noHistory{}, usage: usageNow{nodes: map[string]metrics.Usage{
		"desktop-control-plane": {CPUMillicores: 230, MemoryBytes: 4 << 30, From: metrics.KindMetricsServer},
		"desktop-worker":        {CPUMillicores: 70, MemoryBytes: 1 << 30, From: metrics.KindMetricsServer},
	}}}
	panel := metricsPanelFor(t, s, "nodes", "", "desktop-control-plane")
	cpu := seriesByKey(panel)["cpu"]
	if cpu.Current == nil || cpu.Current.Text != "230m" {
		t.Fatalf("the node's CPU now is %+v; want 230m, this node's own figure", cpu.Current)
	}
}

// countingUsage records whether it was asked at all.
type countingUsage struct{ calls int }

func (c *countingUsage) PodUsage(context.Context, string, string) (map[string]metrics.Usage, error) {
	c.calls++
	return nil, errors.New("asked")
}

func (c *countingUsage) NodeUsage(context.Context, string) (map[string]metrics.Usage, error) {
	c.calls++
	return nil, errors.New("asked")
}

func (c *countingUsage) ContainerUsage(context.Context, string, string) (map[string]metrics.Usage, error) {
	c.calls++
	return nil, errors.New("asked")
}

// The Capacity tile's `used`, in the same three states.
func TestCapacityUsedIsTheReadingOfNowWhereThereIsNoHistory(t *testing.T) {
	reason := noHistorySentence
	noSeries := func() overview.ClusterOverview {
		machines := measured()
		machines.History = overview.Availability{Detail: reason}
		machines.CPUHistory = overview.History{Title: "CPU used", Unit: overview.UnitCores, Unavailable: reason}
		machines.MemoryHistory = overview.History{Title: "Memory used", Unit: overview.UnitBytes, Unavailable: reason}
		return machines
	}

	t.Run("instant only", func(t *testing.T) {
		machines := noSeries()
		machines.Now = overview.Current{CPUMilli: 230, MemoryBytes: 4 << 30, Nodes: 1}
		machines.NowFrom = overview.Availability{OK: true, Detail: "metrics-server answered"}

		cpu := cellNamed(t, capacityCells("prod", machines), "CPU").Used
		if cpu == nil || !cpu.Trustworthy || cpu.Value == nil || *cpu.Value != 230 || cpu.Text != "230m" {
			t.Errorf("CPU used is %+v; want 230m, what metrics-server says the machines are using", cpu)
		}
		memory := cellNamed(t, capacityCells("prod", machines), "Memory").Used
		if memory == nil || !memory.Trustworthy || memory.Text != "4.0 GiB" {
			t.Errorf("Memory used is %+v; want 4.0 GiB, spelled as the Nodes list spells it", memory)
		}
	})

	t.Run("neither", func(t *testing.T) {
		machines := noSeries()
		machines.NowFrom = overview.Availability{Detail: "metrics-server did not answer: the server could not find the requested resource"}
		cpu := cellNamed(t, capacityCells("prod", machines), "CPU").Used
		if cpu == nil || cpu.Trustworthy || cpu.Value != nil {
			t.Fatalf("CPU used is %+v with nothing that measured it; want no reading", cpu)
		}
		if cpu.Text != reason {
			t.Errorf("CPU used says %q; want the reason the series gave", cpu.Text)
		}
	})

	t.Run("history wins where it has a sample", func(t *testing.T) {
		machines := measured()
		machines.Now = overview.Current{CPUMilli: 9999, MemoryBytes: 1, Nodes: 1}
		machines.NowFrom = overview.Availability{OK: true}
		cpu := cellNamed(t, capacityCells("prod", machines), "CPU").Used
		points := machines.CPUHistory.Points
		if len(points) == 0 {
			t.Fatal("the fixture carries no CPU history; this case measures nothing")
		}
		if cpu == nil || cpu.Value == nil || *cpu.Value != points[len(points)-1].Value {
			t.Errorf("CPU used is %+v; the line's newest sample is the figure where there is a line", cpu)
		}
	})
}

// podDoor is the `Usage` door answering one namespace's pods, counting how
// often it was asked. Atomic: the usage cache asks from its own goroutine.
type podDoor struct {
	pods  map[string]metrics.Usage
	nodes map[string]metrics.Usage
	calls atomic.Int32
}

func (d *podDoor) PodUsage(context.Context, string, string) (map[string]metrics.Usage, error) {
	d.calls.Add(1)
	return d.pods, nil
}

func (d *podDoor) NodeUsage(context.Context, string) (map[string]metrics.Usage, error) {
	d.calls.Add(1)
	return d.nodes, nil
}

func (d *podDoor) ContainerUsage(context.Context, string, string) (map[string]metrics.Usage, error) {
	return nil, errors.New("not asked here")
}

// The panel's reading of
// now is the Pods list's read, out of the same cache, and not a read of its
// own on every mount. On a Prometheus-only cluster a young pod would pay
// metrics-server's 404 and a ~1.8 s tunnel each time it was opened.
func TestTheMetricsPanelsReadingOfNowComesOutOfTheUsageCache(t *testing.T) {
	t.Run("two mounts, one read - and the first mount still has its figure", func(t *testing.T) {
		door := &podDoor{pods: map[string]metrics.Usage{
			"api-0": {CPUMillicores: 12, MemoryBytes: 48 << 20, From: metrics.KindMetricsServer},
		}}
		s := &Server{series: noHistory{}, usage: door}
		for mount := 1; mount <= 2; mount++ {
			cpu := seriesByKey(metricsPanelFor(t, s, "pods", "argo", "api-0"))["cpu"]
			if cpu.Current == nil || cpu.Current.Text != "12m" {
				t.Fatalf("mount %d: the CPU line's reading of now is %+v; want 12m - a first read is waited for, "+
					"because the panel is not read again", mount, cpu.Current)
			}
		}
		if got := door.calls.Load(); got != 1 {
			t.Errorf("the usage door was asked %d times for two mounts within the cache's TTL; want 1", got)
		}
	})

	t.Run("figures the Pods list already holds are not read again", func(t *testing.T) {
		door := &podDoor{}
		s := &Server{series: noHistory{}, usage: door}
		now := time.Now()
		s.usageHeld = map[string]*usageEntry{
			usageKey("prod", "pods", "argo"): {usage: map[string]resources.Usage{
				"api-0": {CPUMillicores: 30, MemoryBytes: 64 << 20, From: metrics.KindMetricsServer},
			}, at: now, tried: now},
		}
		panel := metricsPanelFor(t, s, "pods", "argo", "api-0")
		if cpu := seriesByKey(panel)["cpu"]; cpu.Current == nil || cpu.Current.Text != "30m" {
			t.Errorf("the CPU line's reading of now is %+v; want the 30m the list's read holds", cpu.Current)
		}
		if got := door.calls.Load(); got != 0 {
			t.Errorf("the usage door was asked %d times beside a fresh cached answer", got)
		}
	})

	t.Run("a node in the cache only for its disk has no reading of now", func(t *testing.T) {
		door := &podDoor{nodes: map[string]metrics.Usage{"desktop-control-plane": {CPUMillicores: 999}}}
		s := &Server{series: noHistory{}, usage: door}
		now := time.Now()
		s.usageHeld = map[string]*usageEntry{
			usageKey("prod", "nodes", ""): {usage: map[string]resources.Usage{
				"desktop-control-plane": {ComputeUnmeasured: true, Disk: &resources.Disk{UsedBytes: 1, CapacityBytes: 2}},
			}, at: now, tried: now},
		}
		cpu := seriesByKey(metricsPanelFor(t, s, "nodes", "", "desktop-control-plane"))["cpu"]
		if cpu.Current != nil {
			t.Errorf("the CPU line reads %+v from an entry that measured only the disk - the 0m ComputeUnmeasured exists to stop", cpu.Current)
		}
	})
}

// A panel can draw one line from Prometheus's series and carry another's figure
// from metrics-server: the caption is the drawn series' span and names no source.
func TestAMixedPanelCaptionsOnlyTheSpan(t *testing.T) {
	one := 0.4
	object := overview.ObjectHistory{Window: overview.Window{Label: "1h"}}
	lines := []api.MetricsSeries{
		{Key: overview.ChartCPU, Data: api.Series{Values: []*float64{&one}}},
		{Key: overview.ChartMemory, Current: &api.Reading{Trustworthy: true, Text: "48 MiB"}},
	}

	if caption := metricsCaptionOf(object, lines); caption != "the last 1h" {
		t.Errorf("caption is %q; the drawn series' span, and no source", caption)
	}
	if caption := metricsCaptionOf(object, lines[1:]); caption != "" {
		t.Errorf("a panel that drew no series has a caption: %q", caption)
	}
}

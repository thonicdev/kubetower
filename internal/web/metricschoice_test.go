package web

import (
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/metrics"
	"github.com/thonicdev/kubetower/internal/overview"
)

// CPU and memory must not be hard-coded available: a
// cluster nothing can measure would draw "Nothing here" under each of them while
// the network and filesystem controls beside them are grayed with a reason.
// These pin the three states of the icon row: neither backend, metrics-server
// alone, and a Prometheus whose window is empty.

func choicesByKey(panel api.MetricsPanel) map[string]api.MetricsChoice {
	out := map[string]api.MetricsChoice{}
	for _, choice := range panel.Metrics {
		out[choice.Key] = choice
	}
	return out
}

// Neither Prometheus nor metrics-server: CPU and memory are refused like the
// network, each carrying the server's sentence, and nothing is selected - so
// the client has nothing to draw beneath the row.
func TestCPUAndMemoryAreRefusedWithAReasonWhenNothingMeasuresThem(t *testing.T) {
	s := &Server{series: noHistory{}, usage: usageNow{}}
	panel := metricsPanelFor(t, s, "pods", "argo", "api-0")
	choices := choicesByKey(panel)

	for _, key := range []string{"cpu", "memory", "network", "disk"} {
		choice, ok := choices[key]
		if !ok {
			t.Fatalf("no %s control; the row is %+v", key, panel.Metrics)
		}
		if choice.Available {
			t.Errorf("%s is offered on a cluster with neither Prometheus nor metrics-server", key)
		}
		if choice.Reason == nil || choice.Reason.Message == "" {
			t.Errorf("%s is refused with nothing saying why", key)
		}
		if choice.Selected {
			t.Errorf("%s is selected although it cannot be read; the panel would open onto its absence", key)
		}
	}
	if got := choices["cpu"].Reason; got == nil || got.Message != noHistorySentence {
		t.Errorf("cpu's reason is %+v; want the server's own sentence, %q", got, noHistorySentence)
	}
	// The code the period buttons are grayed on: no Prometheus to ask.
	if line := seriesByKey(panel)["cpu"]; line.Reason == nil || line.Reason.Code != api.CodeNotInstalled {
		t.Errorf("the cpu line's reason is %+v; want code %q, which says there is no Prometheus", line.Reason, api.CodeNotInstalled)
	}
}

// metrics-server alone: CPU and memory stay offered, because there is a
// figure of now to show, and they carry no refusal. CPU opens first.
func TestCPUAndMemoryStayAvailableWithAReadingOfNow(t *testing.T) {
	s := &Server{series: noHistory{}, usage: usageNow{pods: map[string]metrics.Usage{
		"api-0": {CPUMillicores: 12, MemoryBytes: 48 << 20, From: metrics.KindMetricsServer},
	}}}
	panel := metricsPanelFor(t, s, "pods", "argo", "api-0")
	choices := choicesByKey(panel)

	for _, key := range []string{"cpu", "memory"} {
		choice := choices[key]
		if !choice.Available {
			t.Errorf("%s is refused on a cluster whose metrics-server gave its figure: %+v", key, choice.Reason)
		}
		if choice.Reason != nil {
			t.Errorf("%s is offered and carries a refusal %q; that is drawn as a complaint about a control that works", key, choice.Reason.Message)
		}
	}
	if !choices["cpu"].Selected {
		t.Errorf("cpu is not the opening measurement although it can be read: %+v", panel.Metrics)
	}
	for _, line := range panel.Series {
		if line.Chart != "cpu" && line.Chart != "memory" {
			continue
		}
		if line.Reason == nil || line.Reason.Code != api.CodeNotInstalled {
			t.Errorf("the %s line's reason is %+v; want %q - the history is not there to be asked, and that is what grays the spans", line.Key, line.Reason, api.CodeNotInstalled)
		}
	}
}

// A Prometheus that answered with an empty window: CPU and memory are refused
// for this window, and the code says the window was empty - never
// `not_installed`, or the client would gray the spans and take away the one
// control that might find samples, a wider window.
func TestAnEmptyWindowIsNotReportedAsAMissingPrometheus(t *testing.T) {
	s := &Server{series: &promFake{emptyFor: "container_"}, usage: usageNow{}}
	panel := metricsPanelFor(t, s, "pods", "argo", "api-0")

	line := seriesByKey(panel)["cpu"]
	if len(line.Data.Values) != 0 {
		t.Fatalf("the fake answered samples for cpu: %v", line.Data.Values)
	}
	if line.Reason == nil || line.Reason.Code != api.CodeEmptyScope {
		t.Errorf("the cpu line's reason is %+v; want %q, a window with nothing in it", line.Reason, api.CodeEmptyScope)
	}
	if choice := choicesByKey(panel)["cpu"]; choice.Available || choice.Reason == nil {
		t.Errorf("cpu with no samples and no figure of now is %+v; want refused, with the window's sentence", choice)
	}
}

// A measurement that can be read is selected when CPU cannot be, and nothing
// is selected when none can.
func TestTheOpeningMeasurementFallsToTheFirstThatCanBeRead(t *testing.T) {
	options := []overview.ChartOption{
		{Key: "cpu", Label: "CPU", Detail: "no samples"},
		{Key: "memory", Label: "Memory", Available: true},
		{Key: "network", Label: "Network", Available: true},
	}
	for _, choice := range metricChoices(options, "cpu") {
		if choice.Selected != (choice.Key == "memory") {
			t.Errorf("%s selected=%v; want memory, the first measurement that can be read", choice.Key, choice.Selected)
		}
	}
	options[1].Available, options[2].Available = false, false
	for _, choice := range metricChoices(options, "cpu") {
		if choice.Selected {
			t.Errorf("%s is selected and nothing can be read", choice.Key)
		}
	}
}

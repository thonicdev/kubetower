package overview

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestRateFloorIsRateWindow(t *testing.T) {
	if got, err := time.ParseDuration(rateWindow); err != nil || got != rateFloor {
		t.Fatalf("rateWindow %q and rateFloor %v disagree", rateWindow, rateFloor)
	}
}

func TestRateRangeFollowsTheStep(t *testing.T) {
	for step, want := range map[time.Duration]string{
		15 * time.Second:               "5m",
		time.Minute:                    "5m",
		3 * time.Minute:                "5m",
		10 * time.Minute:               "10m",
		time.Hour:                      "1h",
		7*time.Minute + 30*time.Second: "450s",
	} {
		if got := rateRange(step); got != want {
			t.Errorf("rateRange(%v) = %q, want %q", step, got, want)
		}
	}
}

var rateRangeIn = regexp.MustCompile(`rate\([^\[]*\[([0-9]+[smh])\]\)`)

// A rate is evaluated once per step and looks back over its range, so a range
// shorter than the step leaves a gap no point covers: a 25 s run with two
// samples in Prometheus would draw nothing at 7d. Every rated query of every
// chart, at every period, must look back at least one step.
func TestEveryRatedQueryCoversItsStep(t *testing.T) {
	for _, window := range Windows {
		for name, ask := range map[string]func(Metrics){
			"pod":     func(m Metrics) { PodHistory(context.Background(), m, "any", "ns", "pod", window) },
			"node":    func(m Metrics) { NodeHistory(context.Background(), m, "any", "node-1", window) },
			"cluster": func(m Metrics) { ZoomClusterHistory(context.Background(), m, "any", window) },
		} {
			m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "1"}), probe: everyCounter(t)}
			ask(m)
			rated := 0
			for _, query := range m.queries {
				for _, match := range rateRangeIn.FindAllStringSubmatch(query, -1) {
					rated++
					got, err := time.ParseDuration(match[1])
					if err != nil {
						t.Fatalf("%s at %s: range %q does not parse: %v", name, window.Label, match[1], err)
					}
					if got < window.Step || got < rateFloor {
						t.Errorf("%s at %s: rate over %v with a %v step leaves a gap:\n  %s",
							name, window.Label, got, window.Step, query)
					}
				}
			}
			if rated == 0 {
				t.Errorf("%s at %s: no rated query was asked: %v", name, window.Label, m.queries)
			}
		}
	}
}

// The cluster's pod-cgroup fallback is a rated query too, asked only
// where this Prometheus has no per-container series.
func TestThePodCgroupFallbackCoversItsStep(t *testing.T) {
	for _, window := range Windows {
		m := &recordingMetrics{answer: []byte(`{"status":"success","data":{"result":[]}}`), probe: exported(t)}
		ZoomClusterHistory(context.Background(), m, "any", window)
		rated := 0
		for _, query := range m.queries {
			if !strings.Contains(query, `container=""`) {
				continue
			}
			for _, match := range rateRangeIn.FindAllStringSubmatch(query, -1) {
				rated++
				if got, err := time.ParseDuration(match[1]); err != nil || got < window.Step || got < rateFloor {
					t.Errorf("at %s: rate over %q with a %v step leaves a gap:\n  %s", window.Label, match[1], window.Step, query)
				}
			}
		}
		if rated != 1 {
			t.Errorf("at %s: %d rated pod-cgroup queries, want 1: %v", window.Label, rated, m.queries)
		}
	}
}

// The dashboard's own chart is drawn at historyStep and keeps its five minutes.
func TestTheDashboardKeepsItsFiveMinuteRate(t *testing.T) {
	if got := rateRangeIn.FindStringSubmatch(queryClusterCPU); got == nil || got[1] != rateWindow {
		t.Errorf("queryClusterCPU = %s", queryClusterCPU)
	}
}

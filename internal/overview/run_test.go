package overview

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The clock of the case under test: a nightly backup ran at 00:30 and the
// panel was opened at 22:30.
var runNow = time.Date(2026, 10, 19, 22, 30, 0, 0, time.UTC)

func at(clock string) time.Time {
	t, err := time.Parse(time.RFC3339, clock)
	if err != nil {
		panic(err)
	}
	return t
}

// emptyPanel is what fill returns for a finished pod: every asked line empty
// from a backend that answered.
func emptyPanel(window Window) ObjectHistory {
	out := ObjectHistory{Window: window}
	for _, line := range []*History{&out.CPU, &out.Memory, &out.NetIn, &out.NetOut} {
		line.Unavailable, line.Missing = emptyWindow, MissingEmpty
	}
	return out
}

// The nightly-backup case: an 8 s backup, 22 hours before an hour-long window.
// The sentence says the run is older than the window, and which period holds
// it.
func TestARunBeforeTheWindowSaysSoAndNamesThePeriodThatHoldsIt(t *testing.T) {
	run := &Run{Start: at("2026-10-19T00:30:00Z"), End: at("2026-10-19T00:30:08Z")}
	got := emptyPanel(WindowFor("1h")).ExplainRun(run, runNow)

	for _, line := range []History{got.CPU, got.Memory, got.NetIn, got.NetOut} {
		if strings.Contains(line.Unavailable, "younger") {
			t.Errorf("a pod that ran 22 hours ago is called younger than the window: %q", line.Unavailable)
		}
		for _, want := range []string{"ran 8s", "before this 1h window", "the 24h period holds it"} {
			if !strings.Contains(line.Unavailable, want) {
				t.Errorf("missing %q in %q", want, line.Unavailable)
			}
		}
		if line.Missing != MissingEmpty {
			t.Errorf("the code moved to %v: the period buttons must stay live", line.Missing)
		}
	}
}

func TestARunOlderThanTheLongestPeriodSaysSo(t *testing.T) {
	run := &Run{Start: at("2026-10-01T00:30:00Z"), End: at("2026-10-01T00:30:08Z")}
	got := emptyPanel(WindowFor("7d")).ExplainRun(run, runNow)
	if !strings.Contains(got.CPU.Unavailable, "before the longest period, 7d") {
		t.Errorf("got %q", got.CPU.Unavailable)
	}
}

// A gauge is read as its last sample within five minutes of each point, so at
// 24h (one point every 10 minutes) a short run can fall between two of them on
// the memory line. A counter's rate covers its whole step, so an
// empty counter line at the same period means too few samples, never a skip.
func TestOnlyAGaugeLineCanSkipARunAtALongStep(t *testing.T) {
	run := &Run{Start: at("2026-10-19T17:30:00Z"), End: at("2026-10-19T17:30:25Z")}
	got := emptyPanel(WindowFor("24h")).ExplainRun(run, runNow)
	for _, want := range []string{"one point is drawn every 10m", "falls between two of them", "the 6h period draws one every 3m"} {
		if !strings.Contains(got.Memory.Unavailable, want) {
			t.Errorf("Memory: missing %q in %q", want, got.Memory.Unavailable)
		}
	}
	for _, line := range []History{got.CPU, got.NetIn, got.NetOut} {
		if strings.Contains(line.Unavailable, "falls between two") {
			t.Errorf("a counter's line blames the step, which its rate now covers: %q", line.Unavailable)
		}
		if !strings.Contains(line.Unavailable, "fewer than two samples") {
			t.Errorf("a counter's line does not say it had too few samples: %q", line.Unavailable)
		}
	}

	old := &Run{Start: at("2026-10-16T00:30:00Z"), End: at("2026-10-16T00:30:25Z")}
	if got := emptyPanel(WindowFor("7d")).ExplainRun(old, runNow); !strings.Contains(got.Memory.Unavailable,
		"no period that draws more often reaches back to it") {
		t.Errorf("a three-day-old run offers a period that does not reach it: %q", got.Memory.Unavailable)
	}
}

func TestARunInsideTheWindowThatLeftTooFewSamplesSaysSo(t *testing.T) {
	run := &Run{Start: at("2026-10-19T22:10:00Z"), End: at("2026-10-19T22:10:08Z")}
	got := emptyPanel(WindowFor("1h")).ExplainRun(run, runNow)
	if want := "fewer than two samples of this counter while it ran: a rate needs two"; !strings.Contains(got.CPU.Unavailable, want) {
		t.Errorf("CPU = %q, want it to contain %q", got.CPU.Unavailable, want)
	}
	// Memory is a gauge, drawn without a rate: one sample would have drawn.
	if want := "took no sample of it while it ran"; !strings.Contains(got.Memory.Unavailable, want) {
		t.Errorf("Memory = %q, want it to contain %q", got.Memory.Unavailable, want)
	}
}

// Only an empty answer is explained. A counter the cluster lacks and a query
// that failed say something truer than the pod's run could.
func TestOnlyAnEmptyLineIsExplained(t *testing.T) {
	panel := emptyPanel(WindowFor("1h"))
	panel.NetIn.Unavailable, panel.NetIn.Missing = "this cluster's Prometheus has no per-pod …", MissingCounter
	panel.CPU.Unavailable, panel.CPU.Missing = "connection refused", MissingFailed
	run := &Run{Start: at("2026-10-19T00:30:00Z"), End: at("2026-10-19T00:30:08Z")}

	got := panel.ExplainRun(run, runNow)
	if got.NetIn.Unavailable != panel.NetIn.Unavailable || got.CPU.Unavailable != panel.CPU.Unavailable {
		t.Errorf("a line with its own reason was rewritten: %q, %q", got.NetIn.Unavailable, got.CPU.Unavailable)
	}
	if !strings.Contains(got.Memory.Unavailable, "before this 1h window") {
		t.Errorf("the empty line was not explained: %q", got.Memory.Unavailable)
	}
}

func TestARunningPodKeepsTheWindowSentence(t *testing.T) {
	if got := emptyPanel(WindowFor("1h")).ExplainRun(nil, runNow); got.CPU.Unavailable != emptyWindow {
		t.Errorf("got %q", got.CPU.Unavailable)
	}
}

func TestRunOfReadsAFinishedPodOnly(t *testing.T) {
	exit := func(clock string) corev1.ContainerStatus {
		return corev1.ContainerStatus{State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{FinishedAt: metav1.NewTime(at(clock))},
		}}
	}
	start := metav1.NewTime(at("2026-10-19T00:30:00Z"))
	finished := &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodSucceeded, StartTime: &start,
		// An init container's exit comes first and is never the run's end.
		InitContainerStatuses: []corev1.ContainerStatus{exit("2026-10-19T00:30:02Z")},
		ContainerStatuses:     []corev1.ContainerStatus{exit("2026-10-19T00:30:08Z"), exit("2026-10-19T00:30:05Z")},
	}}
	run := RunOf(finished)
	if run == nil || !run.Start.Equal(start.Time) || !run.End.Equal(at("2026-10-19T00:30:08Z")) {
		t.Fatalf("RunOf = %+v", run)
	}

	running := finished.DeepCopy()
	running.Status.Phase = corev1.PodRunning
	noExit := finished.DeepCopy()
	noExit.Status.ContainerStatuses = nil
	for name, pod := range map[string]*corev1.Pod{"running": running, "no exit recorded": noExit, "nil": nil} {
		if got := RunOf(pod); got != nil {
			t.Errorf("%s: RunOf = %+v, want nil", name, got)
		}
	}
}

// The probe asks for the dimension each query selects on, not the bare counter:
// docker-desktop's cAdvisor exports all four counters and none per pod or per
// container, and a bare-name probe would call every pod there "younger" than the
// window, CoreDNS after 39 days included.
func TestEveryPodProbeAsksForTheDimensionItsQueryNeeds(t *testing.T) {
	m := &recordingMetrics{answer: matrix(t), probe: everyCounter(t)}
	PodHistory(context.Background(), m, "any", "ns", "pod", WindowFor("1h"))
	if len(m.probes) != 1 {
		t.Fatalf("probes = %v", m.probes)
	}
	for _, want := range []string{
		metricCPU + `{container!=""}`, metricMemory + `{container!=""}`,
		metricNetIn + `{pod!=""}`, metricNetOut + `{pod!=""}`,
	} {
		if !strings.Contains(m.probes[0], want) {
			t.Errorf("the probe does not ask for %s:\n  %s", want, m.probes[0])
		}
	}
}

func TestACPULineWithNoPerContainerSeriesSaysWhatTheClusterLacks(t *testing.T) {
	m := &recordingMetrics{answer: matrix(t), probe: exported(t, metricNetIn, metricNetOut, metricDisk)}
	got := PodHistory(context.Background(), m, "any", "kube-system", "coredns-589f44dc88-nrpv2", WindowFor("1h"), ChartCPU)
	if strings.Contains(got.CPU.Unavailable, "younger") {
		t.Errorf("CPU blames the pod's age for what the cluster lacks: %q", got.CPU.Unavailable)
	}
	if !strings.Contains(got.CPU.Unavailable, "no per-container "+metricCPU) || got.CPU.Missing != MissingCounter {
		t.Errorf("CPU = %q (%v)", got.CPU.Unavailable, got.CPU.Missing)
	}
}

// Source is the cluster overview's caption (clusterLines); nothing reads it on
// an object's panel, whose every line carries its own reason. The object path
// leaves it empty in every state, so no second sentence drifts beside the lines.
func TestTheObjectPathWritesNoSource(t *testing.T) {
	ctx := context.Background()
	run := &Run{Start: at("2026-10-19T00:30:00Z"), End: at("2026-10-19T00:30:08Z")}
	empty := PodHistory(ctx, &recordingMetrics{answer: matrix(t), probe: everyCounter(t)},
		"any", "ns", "pod", WindowFor("1h"))
	for name, got := range map[string]ObjectHistory{
		"no backend": PodHistory(ctx, nil, "any", "ns", "pod", WindowFor("1h")),
		"every query failed": PodHistory(ctx, &recordingMetrics{err: errors.New("connection refused"),
			probe: everyCounter(t)}, "any", "ns", "pod", WindowFor("1h")),
		"nothing asked": PodHistory(ctx, &recordingMetrics{answer: matrix(t), probe: everyCounter(t)},
			"any", "ns", "pod", WindowFor("1h"), "netin"),
		"empty window": empty,
		"finished pod": empty.ExplainRun(run, runNow),
	} {
		if got.Source != "" {
			t.Errorf("%s: the object path wrote Source %q, which nothing reads", name, got.Source)
		}
	}
}

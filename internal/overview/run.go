package overview

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// emptyWindow is what an empty window says when nothing more is known about
// the object. It is wrong for a finished pod, which is older than the window,
// so ExplainRun replaces it there.
const emptyWindow = "no samples in this window — the object may be younger than it, " +
	"or Prometheus may not have kept this far back"

// gaugeLookback is Prometheus's default lookback delta: a gauge (memory here)
// is read as the last sample within it, so a step longer than this leaves
// gaps. A counter's rate covers its whole step (rateRange).
const gaugeLookback = 5 * time.Minute

// Run is when a finished pod ran: from its start to the last of its
// containers' exits.
type Run struct {
	Start, End time.Time
}

// RunOf is the run of a pod whose containers have all exited, and nil for any
// other pod: still running, never started, or with no recorded exit time.
// Init containers are left out: they always exit before the run's own.
func RunOf(pod *corev1.Pod) *Run {
	if pod == nil || pod.Status.StartTime == nil {
		return nil
	}
	if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
		return nil
	}
	var end time.Time
	for _, s := range pod.Status.ContainerStatuses {
		if t := s.State.Terminated; t != nil && t.FinishedAt.Time.After(end) {
			end = t.FinishedAt.Time
		}
	}
	if end.IsZero() {
		return nil
	}
	return &Run{Start: pod.Status.StartTime.Time, End: end}
}

// ExplainRun replaces the empty-window sentence on a finished pod's lines with
// one that names its run: a nightly backup's pod must not say it "may be
// younger" than the hour on screen when it ran for 8 seconds 22 hours earlier.
//
// It touches only lines that came back empty from a backend that answered
// (MissingEmpty); a missing counter or a failed query already says something
// truer. The code stays MissingEmpty so the period buttons stay live. The
// cases, in order:
//
//   - The run ended before the window began: the sentence names the smallest
//     period that holds it.
//   - On a gauge's line only, the period's step exceeds gaugeLookback, so a
//     short run falls between two points: the sentence names a period whose
//     step does not skip it. A counter's rate covers its step, so it never
//     lands here.
//   - Otherwise the run left too few samples: fewer than two for a counter's
//     rate, none for a gauge.
func (h ObjectHistory) ExplainRun(run *Run, now time.Time) ObjectHistory {
	if run == nil {
		return h
	}
	for _, line := range []struct {
		series  *History
		counter bool
	}{
		{&h.CPU, true},
		{&h.Memory, false},
		{&h.NetIn, true},
		{&h.NetOut, true},
	} {
		if line.series.Missing != MissingEmpty {
			continue
		}
		line.series.Unavailable, line.series.Period = runSentence(*run, h.Window, now, line.counter)
	}
	return h
}

// runSentence is the empty line's sentence, and the key of the period it
// names, if any: the client draws a button selecting it. The period on screen
// never changes without that press.
func runSentence(run Run, window Window, now time.Time, counter bool) (string, string) {
	ran := fmt.Sprintf("this pod ran %s, from %s to %s", exactDuration(run.End.Sub(run.Start)),
		run.Start.Format(time.RFC3339), run.End.Format(time.RFC3339))

	if run.End.Before(now.Add(-window.Range)) {
		// To the minute: the window is at least fifteen of them.
		ago := now.Sub(run.End).Round(time.Minute)
		sentence := fmt.Sprintf("%s, %s ago — before this %s window", ran,
			exactDuration(ago), window.Label)
		if holding, ok := smallestHolding(now.Sub(run.Start)); ok {
			return sentence + "; the " + holding.Label + " period holds it", holding.Label
		}
		return sentence + ", and before the longest period, " + Windows[len(Windows)-1].Label, ""
	}

	if !counter && window.Step > gaugeLookback {
		sentence := fmt.Sprintf("%s, and at this period one point is drawn every %s: a run this short "+
			"falls between two of them", ran, exactDuration(window.Step))
		if finer, ok := finestHolding(now.Sub(run.Start)); ok {
			return sentence + "; the " + finer.Label + " period draws one every " + exactDuration(finer.Step),
				finer.Label
		}
		return sentence + ", and no period that draws more often reaches back to it", ""
	}

	if counter {
		return ran + ", and Prometheus took fewer than two samples of this counter while it ran: " +
			"a rate needs two", ""
	}
	return ran + ", and Prometheus took no sample of it while it ran", ""
}

// smallestHolding is the shortest period that reaches back to age.
func smallestHolding(age time.Duration) (Window, bool) {
	for _, w := range Windows {
		if w.Range >= age {
			return w, true
		}
	}
	return Window{}, false
}

// finestHolding is the shortest period that reaches back to age and draws a
// point at least every gaugeLookback, so no gauge sample is skipped.
func finestHolding(age time.Duration) (Window, bool) {
	for _, w := range Windows {
		if w.Range >= age && w.Step <= gaugeLookback {
			return w, true
		}
	}
	return Window{}, false
}

// exactDuration prints every unit down to the second: 1h40m12s, 3d2h, 27s.
// time.Duration's own String prints a day as 24h0m0s.
func exactDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d <= 0 {
		return "0s"
	}
	out := ""
	for _, p := range []struct {
		n    time.Duration
		unit string
	}{
		{d / (24 * time.Hour), "d"},
		{d % (24 * time.Hour) / time.Hour, "h"},
		{d % time.Hour / time.Minute, "m"},
		{d % time.Minute / time.Second, "s"},
	} {
		if p.n > 0 {
			out += fmt.Sprintf("%d%s", p.n, p.unit)
		}
	}
	return out
}

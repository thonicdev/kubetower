package rightsizing

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// How much a figure rests on, and when it rests on too little to name one.
//
// A suggestion sized on a week of usage and one sized on a single scrape used
// to differ only by a phrase in the message. They are different claims, and the
// page has to be able to draw the difference, so each CPU and memory suggestion
// carries a level, and a container with too little usable usage gets an
// explicit state and no recommended value at all.
//
// "Usable" is the word that matters. The samples the sizing may use leave out
// the minutes after a container starts (a working set not yet built) and every
// replica that is crash-looping (a process dying, not working) - see
// usableSamples and usageStats. What is left is measured by how much time its
// samples cover, not by how far apart the first and the last are: a series
// with a hole in the middle covers less than its two ends suggest.

// Confidence is how much a suggestion's figure rests on.
type Confidence string

const (
	// ConfidenceHigh is a figure from (close to) a full week of usable series:
	// every daily peak, and one of each weekday.
	ConfidenceHigh Confidence = "high"
	// ConfidenceMedium is a figure from at least a day of usable series and
	// less than a week: the daily peak is in it, a weekly one may not be.
	ConfidenceMedium Confidence = "medium"
	// ConfidenceLow is a figure from a single metrics-server scrape per
	// replica, which is all there is without a Prometheus. It is always low:
	// one instant says nothing about the hours around it.
	ConfidenceLow Confidence = "low"
)

const (
	// minUsable is the least usable series a figure is sized from: a day. Under
	// it the window has not seen one full daily cycle, and a request sized on
	// the quiet half of a day is the next busy half's throttle or eviction.
	minUsable = 24 * time.Hour

	// minCoverage is the least share of the time between a container's first
	// and last usable sample that its samples must cover. Half: below it the
	// series is mostly holes - a scrape target that came and went, a pod that
	// spent most of the week restarting - and a P95 over it is the P95 of
	// whenever Prometheus happened to see the container, not of its week.
	minCoverage = 0.5

	// highConfidence is the usable series a figure needs to be high
	// confidence: seven days, less 5 %. The window holds at most seven days,
	// so the literal rule could only be met by a series missing no sample,
	// and one scrape lost to a Prometheus restart would demote a whole week.
	highConfidence = 160 * time.Hour
)

// The reasons a container is judged to have too little usable usage, said on
// every suggestion about it.
const (
	reasonNoSeries  = "the usage window has no usable series for it"
	reasonCrashLoop = "every measured replica is crash-looping, and a dying process's usage is not sized from"
)

// judgment is what usageStats concludes about how much its figures rest on.
type judgement struct {
	confidence Confidence
	// insufficient is too little usable usage to name a value, and reason why.
	insufficient bool
	reason       string
	// usable is how much time the usable samples cover.
	usable time.Duration
	// note is a caveat that does not withhold the value: crash-looping
	// replicas left out of a pool that still had others.
	note string
}

// pageHistory indexes the window, keeping only the series of containers this
// page measured, and of the former pods tied to its workloads.
//
// Whether the page has a window at all is read from this index (usageStats),
// so it has to mean "a window covering something here": a window whose series
// match nothing on the page - a Prometheus whose labels are not where they are
// looked for - is said as no window by HistoryNote, and sized as none, from the
// snapshots, rather than withholding every value on the page.
func pageHistory(workloads []Workload, history []ContainerHistory) HistoryMap {
	all := BuildHistoryMap(history)
	out := make(HistoryMap, len(all))
	for _, w := range workloads {
		for _, pod := range w.Pods {
			for i := range pod.Containers {
				c := &pod.Containers[i]
				if !c.HasUsage {
					continue
				}
				key := HistoryKey(pod.Namespace, pod.Name, c.Name)
				if h, ok := all[key]; ok {
					out[key] = h
				}
			}
		}
		// And its former pods' series, which are this page's too (pooled.go).
		for key, h := range workloadHistory(nil, all, Workload{Namespace: w.Namespace, Former: w.Former}) {
			out[key] = h
		}
	}
	return out
}

// judgeWindow judges a pool of windowed series, already reduced to their
// usable samples.
//
// The time covered is counted in steps: each distinct timestamp across the
// replicas covers one step, so two replicas seen at the same instants cover
// it once, and a rollout - one replica's series ending where the next one's
// begins - covers both halves.
func judgeWindow(series [][]Point) judgement {
	step := seriesStep(series...)
	seen, first, last := instants(series, step)
	if step <= 0 || seen < 2 {
		return judgement{confidence: ConfidenceLow, insufficient: true, reason: reasonNoSeries}
	}
	out := judgement{usable: time.Duration(float64(seen) * step * float64(time.Second))}
	expected := (last-first)/step + 1
	coverage := float64(seen) / expected
	switch {
	case out.usable < minUsable:
		out.confidence, out.insufficient = ConfidenceLow, true
		out.reason = "only " + coverText(out.usable) + " of usable series, under the day a figure needs"
	case coverage < minCoverage:
		out.confidence, out.insufficient = ConfidenceLow, true
		out.reason = fmt.Sprintf("its series cover %d%% of the time between their first and last sample, "+
			"under the half a figure needs", int(coverage*100))
	case out.usable >= highConfidence:
		out.confidence = ConfidenceHigh
	default:
		out.confidence = ConfidenceMedium
	}
	return out
}

// instants is how many distinct instants the series hold between them, and
// the first and the last. Counted on the window's grid rather than in a set: a
// page judges every container two or three times, each over a week of points,
// and a set of a few thousand floats per judgment was a fifth of the page's
// arithmetic. An instant is on the grid when first plus a whole number of
// steps gives it back exactly, so two instants share a cell only when they are
// the same instant; a series off the grid (a hand-built one) is sorted and
// counted instead.
func instants(series [][]Point, step float64) (seen int, first, last float64) {
	first, last = math.Inf(1), math.Inf(-1)
	total := 0
	for _, points := range series {
		total += len(points)
		for _, p := range points {
			first, last = math.Min(first, p.T), math.Max(last, p.T)
		}
	}
	if total == 0 {
		return 0, first, last
	}
	if step > 0 {
		if cells := (last-first)/step + 1; cells <= float64(4*total+64) {
			grid := make([]bool, int(cells)+1)
			onGrid := true
			for _, points := range series {
				for _, p := range points {
					at := math.Round((p.T - first) / step)
					if first+at*step != p.T {
						onGrid = false
						break
					}
					if !grid[int(at)] {
						grid[int(at)] = true
						seen++
					}
				}
				if !onGrid {
					break
				}
			}
			if onGrid {
				return seen, first, last
			}
		}
	}
	times := make([]float64, 0, total)
	for _, points := range series {
		for _, p := range points {
			times = append(times, p.T)
		}
	}
	slices.Sort(times)
	seen = 0
	for i, t := range times {
		if i == 0 || t != times[i-1] {
			seen++
		}
	}
	return seen, first, last
}

// judgeSnapshot judges a container sized from snapshots alone. Without a
// window anywhere on the page it is the single-scrape case, always low; with
// one, this container has no usable series in it, which is too little.
func judgeSnapshot(windowed bool) judgement {
	if windowed {
		return judgement{confidence: ConfidenceLow, insufficient: true, reason: reasonNoSeries}
	}
	return judgement{confidence: ConfidenceLow}
}

// coverText is a covered duration as the sentence reads it.
func coverText(d time.Duration) string {
	if d >= time.Hour {
		return toFixed(d.Hours(), 0) + "h"
	}
	return fmt.Sprintf("%dm", int64(math.Round(d.Minutes())))
}

// seriesStep is the resolution of a window: the smallest gap between two
// consecutive samples of any series, in seconds. The window's own step when
// Prometheus answered it, and whatever spacing a hand-built series has.
func seriesStep(series ...[]Point) float64 {
	step := 0.0
	for _, points := range series {
		for i := 1; i < len(points); i++ {
			if d := points[i].T - points[i-1].T; d > 0 && (step == 0 || d < step) {
				step = d
			}
		}
	}
	return step
}

// usableSamples leaves out of one replica's series the samples taken in the
// minutes after a start.
//
// A start is one the status reports (Container.Starts), or a gap of more than
// two steps in the series: the container was not there to be scraped, and the
// status keeps only the last two runs, so an older restart shows only as the
// hole it left. A gap that was a Prometheus outage costs a sample or two, which
// is the cheap direction to be wrong in.
//
// A sample is left out when it is less than warmupWindow plus one step after a
// start: the step, because a memory sample is the largest working set of the
// step it closes (and a CPU one a rate over the minutes before it), so a sample
// taken after the warm-up can still hold it.
func usableSamples(points []Point, starts []float64) []Point {
	if len(points) == 0 {
		return points
	}
	step := seriesStep(points)
	marks := append([]float64(nil), starts...)
	if step > 0 {
		for i := 1; i < len(points); i++ {
			if points[i].T-points[i-1].T > 2*step {
				// The start was somewhere in the gap, at the latest at the first
				// sample after it, so the warm-up is counted from that sample.
				marks = append(marks, points[i].T)
			}
		}
	}
	if len(marks) == 0 {
		return points
	}
	reach := warmupWindow.Seconds() + step
	out := make([]Point, 0, len(points))
	for _, p := range points {
		warming := false
		for _, m := range marks {
			if p.T >= m && p.T < m+reach {
				warming = true
				break
			}
		}
		if !warming {
			out = append(out, p)
		}
	}
	return out
}

// insufficientCaveat is the line every suggestion about a container with too
// little usable usage carries: why, and what that withholds.
func insufficientCaveat(reason string) string {
	return "Insufficient data: " + reason + " — no value is recommended until there is a day of it"
}

// qualify gives each CPU and memory suggestion of one workload its confidence,
// and takes the value off those whose container has too little usable usage.
//
// After the engine rather than inside it, on purpose: the rules that decide
// whether something is wrong are unchanged by how much data there is - an
// alarm on a container under a day old is still worth reading - and what
// changes is only whether a number may be put on the fix. The OOM kill's raise
// keeps its value, which comes from the limit the container was killed at; a
// request folded into it was sized from the usage, so that part goes.
func qualify(rows []Suggestion, pods []Pod, hist HistoryMap, memo statsMemo) []Suggestion {
	type resource struct {
		container string
		isCPU     bool
	}
	judged := map[resource]judgement{}
	out := make([]Suggestion, 0, len(rows))
	for _, s := range rows {
		isCPU := strings.HasPrefix(s.Resource, "CPU")
		if !isCPU && !strings.HasPrefix(s.Resource, "Memory") {
			out = append(out, s)
			continue
		}
		key := resource{s.Container, isCPU}
		j, seen := judged[key]
		if !seen {
			// A container nothing measured has no figure at all; what is said
			// about it (the OOM alarm) rests on its limit and one status.
			j = judgement{confidence: ConfidenceLow}
			if st, ok := memo.usage(pods, s.Container, isCPU, hist); ok {
				j = st.judged
			}
			judged[key] = j
		}
		s.Confidence = j.confidence
		s.Warnings = append([]string(nil), s.Warnings...)
		if j.note != "" {
			s.Warnings = append(s.Warnings, j.note)
		}
		if j.insufficient {
			s.InsufficientData = true
			if !s.fromLimit {
				s.Suggested, s.SuggestedRaw, s.PairedLimitRaw = "", 0, 0
			}
			s.PairedRequestRaw = 0
			kept := s.Warnings[:0]
			for _, w := range s.Warnings {
				// The other half of a command that is no longer offered.
				if !strings.HasSuffix(w, "in the same command") {
					kept = append(kept, w)
				}
			}
			s.Warnings = append(kept, insufficientCaveat(j.reason))
		}
		out = append(out, s)
	}
	return out
}

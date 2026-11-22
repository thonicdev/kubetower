package rightsizing

import "math"

// The numbers every rule is sized from, in one place, and the reason each is
// what it is. They were once scattered through analyzeCPUMem, and scattered
// they disagreed: the reduction sized a CPU request on the mean while the rule
// that calls a request too low read the P95, so the engine's own advice, once
// applied, raised the opposite finding on the next read.
//
// **One target per side, used in both directions.** The request this container
// should have is requestTarget; the limit it should have is limitTarget. A
// reduction proposes the target when it saves enough to be worth a rollout, an
// increase proposes the same target when usage has outgrown what is set, and a
// value at its target triggers neither - so applying a suggestion never makes
// the engine suggest the opposite.
const (
	// requestOverP95CPU and requestOverMaxMemory are the headroom a request
	// keeps over the figure it is sized on, which is never the mean: a
	// request at the mean is below the usage half the time, which is what the
	// scheduler packs against.
	//
	//   - CPU is sized on its P95, plus 20 %. One sample in twenty is above
	//     a P95 by definition, and CPU above its request costs a throttle
	//     under contention rather than a kill, so the margin covers the
	//     ordinary spread and the peaks are left to the limit.
	//   - Memory is sized on its maximum, plus 15 %. Memory above its request
	//     is the kubelet's eviction order under node memory pressure, so the
	//     request covers the worst the window saw; the margin is smaller
	//     because nothing in the window was above that figure, and covers the
	//     growth since it was read.
	//
	// Both are at or above what was observed, so a request applied from here
	// is never below the usage it was sized on.
	requestOverP95CPU    = 1.2
	requestOverMaxMemory = 1.15

	// limitOverP95 and limitOverPeak size a limit: the larger of 1.5 × P95 and
	// 1.2 × the observed peak. **Never under the peak**, which the engine used
	// to allow - 1.5 × P95 on a container whose P95 is 100Mi and whose peak is
	// 400Mi is a 192Mi memory limit the next peak is killed at. Memory is not
	// compressible, so a memory limit under a value the container has reached
	// is an OOMKill with a date on it.
	limitOverP95  = 1.5
	limitOverPeak = 1.2

	// singleScrapeMemoryLimit is the headroom over one sample when a memory
	// limit has to be proposed without a window (a container with none at
	// all): one sample says nothing about peaks, so the margin is wider.
	singleScrapeMemoryLimit = 2.0

	// underRequested is how far the P95 may run above the request before the
	// request is called too low. 10 %: below it is noise around a well-set
	// request.
	underRequested = 1.1
	// memoryUnderRequestedDanger is where a memory request too low becomes a
	// danger: using twice what was reserved puts the pod near the head of the
	// eviction queue the first time the node is short.
	memoryUnderRequestedDanger = 2.0

	// worthARollout is the most a reduced value may be, as a share of the
	// current one, for the reduction to be offered: at least 30 % saved.
	// Every change to requests or limits restarts every pod of the workload,
	// and a 10 % saving is not worth that.
	worthARollout = 0.7

	// limitGenerous is how far above the P95 a limit has to sit before a
	// reduction is considered at all: limits are headroom on purpose, and a
	// limit at twice its P95 is a reasonable choice, not waste.
	limitGenerous = 3.0

	// nearLimit and atLimit grade usage against a limit. For memory, at the
	// limit is a kill (danger); for CPU it is a throttle, so CPU never goes
	// past a warning.
	nearLimit = 0.70
	atLimit   = 0.90
)

// requestTarget is the request this resource should have, rounded up to a
// value worth typing. Zero when usage gives nothing to size from - a container
// measured at exactly zero is not told to request nothing.
//
// CPU is sized on its P95 and memory on its maximum (see requestOverP95CPU).
// A bursty CPU - a peak at least burstThreshold × its P95 - is sized on its
// peak: a workload that needs two cores for a few minutes every night must
// not be told to request a tenth of one. That is the rule.
// Both figures are over the samples the sizing may use (usableSamples): the
// minutes after a start and a crash-looping replica are not in them, so a
// start's spike no longer counts as a burst, and the limit, sized on every
// sample, is what still covers it.
func requestTarget(st usage, isCPU bool) int64 {
	if !isCPU {
		return RoundResource(st.sizeMax*requestOverMaxMemory, isCPU)
	}
	basis := st.p95
	if burstRatio(st) >= burstThreshold {
		basis = st.sizeMax
	}
	return RoundResource(basis*requestOverP95CPU, isCPU)
}

// limitTarget is the limit this resource should have, rounded up: the larger
// of 1.5 × P95 and 1.2 × the peak, so never under a value the container has
// reached. Without a window a memory limit takes twice the one sample there
// is.
func limitTarget(st usage, isCPU bool) int64 {
	if !isCPU && !st.hasHistory {
		return RoundResource(st.max*singleScrapeMemoryLimit, isCPU)
	}
	return RoundResource(math.Max(st.p95*limitOverP95, st.max*limitOverPeak), isCPU)
}

// limitPressure grades the P95 against a limit: danger, warning or nothing.
// CPU stops at a warning: a container at its CPU limit is throttled, not
// killed.
func limitPressure(p95 float64, limit int64, isCPU bool) (Kind, bool) {
	if limit <= 0 {
		return "", false
	}
	pct := p95 / float64(limit)
	switch {
	case pct >= atLimit && !isCPU:
		return KindDanger, true
	case pct >= nearLimit:
		return KindWarning, true
	}
	return "", false
}

// requestPressure grades a request the P95 has outgrown. CPU above its request
// is a warning only: it is neither throttled (that is the limit's doing) nor
// evicted (eviction signals are memory, disk and PIDs) - it runs on the node's
// idle cycles, and under contention gets its request's share.
func requestPressure(p95 float64, request int64, isCPU bool) (Kind, bool) {
	if request <= 0 || p95 <= float64(request)*underRequested {
		return "", false
	}
	if !isCPU && p95 >= float64(request)*memoryUnderRequestedDanger {
		return KindDanger, true
	}
	return KindWarning, true
}

// requestOversized says whether a request is worth reducing to its target.
func requestOversized(st usage, request int64, isCPU bool) bool {
	if request <= 0 {
		return false
	}
	target := requestTarget(st, isCPU)
	return target > 0 && float64(target) <= float64(request)*worthARollout
}

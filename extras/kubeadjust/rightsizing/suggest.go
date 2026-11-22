package rightsizing

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	// burstThreshold is the peak-to-P95 ratio above which a workload is treated
	// as bursty and every reduction is sized on its peak instead of its mean.
	//
	// Five, and not a number tuned to a fleet: the case this exists for is a
	// runtime that needs several cores for a minute at startup and a tenth of one
	// afterwards, where the ratio is tens rather than sixes. A threshold that
	// catches those and lets ordinary jitter through is doing its job.
	burstThreshold = 5
	// crashLoopRestarts is where a container stops being a source of data. Its
	// usage after a restart says what it managed to allocate before dying, not
	// what it needs.
	crashLoopRestarts = 3

	// warmupWindow is how old the oldest measured replica must be before a
	// reduction is trusted. Usage minutes after a start understates the steady
	// state — a JVM's heap has not grown, caches are not filled, lazy
	// initialization has not run — so a page opened right after a rollout would
	// otherwise read "uses 80Mi of a 1Gi request" about a process that has not
	// warmed up yet, and the reduction it suggests is the next OOM. Ten
	// minutes, on the same scale as restartRecency and for the same reason:
	// it is long enough for a working set to be a working set.
	warmupWindow = 10 * time.Minute
)

// singleScrapeCaveat rides on every suggestion able to take something away —
// a reduction, or a new limit where none existed — when it was computed from
// one instantaneous sample per replica rather than a usage window. A workload
// idle at the moment of the scrape reads as always idle, and nothing in a
// single sample can say otherwise; the burst guard that would catch it needs
// history to fire. The page-level History note says this once for the page;
// this says it on the row the operator is about to act on.
const singleScrapeCaveat = "Sized from a single scrape per replica — verify against a usage window before applying"

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func maxOf(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	out := values[0]
	for _, v := range values[1:] {
		if v > out {
			out = v
		}
	}
	return out
}

// percentile95 is the 95th percentile by nearest-rank on the sorted samples.
//
// P95 and not the maximum, for limits: the maximum of a week is one scrape that
// caught a garbage collection, and sizing every limit to it gives back all the
// headroom the exercise was meant to reclaim. P95 and not the mean, for the
// same reason in the other direction.
func percentile95(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	idx := int(math.Ceil(float64(len(values))*0.95)) - 1
	if idx < 0 {
		idx = 0
	}
	return nth(append([]float64(nil), values...), idx)
}

// nth is the value a sort would put at index k, found without sorting: the
// pool is a week of points per replica, taken two or three times per
// container, and only one rank of it is ever read. It reorders values.
func nth(values []float64, k int) float64 {
	lo, hi := 0, len(values)-1
	for lo < hi {
		// The median of three as the pivot, so a window already in order -
		// a memory series that only grows - is not the quadratic case.
		mid := lo + (hi-lo)/2
		if values[mid] < values[lo] {
			values[mid], values[lo] = values[lo], values[mid]
		}
		if values[hi] < values[lo] {
			values[hi], values[lo] = values[lo], values[hi]
		}
		if values[hi] < values[mid] {
			values[hi], values[mid] = values[mid], values[hi]
		}
		pivot := values[mid]
		i, j := lo, hi
		for i <= j {
			for values[i] < pivot {
				i++
			}
			for values[j] > pivot {
				j--
			}
			if i <= j {
				values[i], values[j] = values[j], values[i]
				i++
				j--
			}
		}
		switch {
		case k <= j:
			hi = j
		case k >= i:
			lo = i
		default:
			return values[k]
		}
	}
	return values[k]
}

// secondsToThreshold is a linear regression standing in for PromQL's
// predict_linear: how long until this series reaches threshold, or no answer at
// all when the trend is flat, falling, or too short to be one.
//
// Timestamps are re-based on the first point before the sums, and that is not
// tidiness. Raw unix seconds squared are ~2.5e18, which eats the float64
// mantissa and skews the slope — so the fix stays, because a trend line that is
// subtly wrong produces a confident "exceeds its limit in 3 hours" about a
// workload that is flat.
//
// The last 60 points only: a container's behavior this hour is what predicts
// the next one, and a regression over a week of history answers a question
// nobody asked.
func secondsToThreshold(points []Point, threshold float64) (float64, bool) {
	if len(points) < 5 {
		return 0, false
	}
	recent := points
	if len(recent) > 60 {
		recent = recent[len(recent)-60:]
	}
	n := float64(len(recent))
	t0 := recent[0].T
	var sumT, sumV, sumTT, sumTV float64
	for _, p := range recent {
		t := p.T - t0
		sumT += t
		sumV += p.V
		sumTT += t * t
		sumTV += t * p.V
	}
	denom := n*sumTT - sumT*sumT
	if denom == 0 {
		return 0, false
	}
	slope := (n*sumTV - sumT*sumV) / denom
	if slope <= 0 {
		return 0, false
	}
	intercept := (sumV - slope*sumT) / n
	lastT := recent[len(recent)-1].T - t0
	predictedT := (threshold - intercept) / slope
	if predictedT <= lastT {
		return 0, false
	}
	return predictedT - lastT, true
}

// aggregate is one container name across every replica of a workload.
//
// Requests and limits come from the pod template and are therefore identical
// across replicas, so one representative spec is kept. Usage is pooled. The
// OOMKilled flag and the restart count are the *worst* seen on any replica,
// deliberately: one replica being killed for memory is a fact about the
// workload's memory limit, not about that replica.
type aggregate struct {
	name      string
	spec      Container
	podNames  []string
	oomKilled bool
	// oomEarlier is a kill for memory older than a day on any replica. It no
	// longer raises the alarm, and it still blocks every memory reduction.
	oomEarlier bool
	// crashLooping is true when **any** replica is in a loop, on the same
	// reasoning as oomKilled: one replica crash-looping is a fact about the
	// workload's configuration, not about that replica.
	crashLooping bool
	maxRestarts  int
	qos          string
}

// aggregateContainers groups a workload's pods into one aggregate per container
// name, in the order the first pod declares them — map iteration order would
// otherwise reshuffle the findings between two renders of the same page.
func aggregateContainers(pods []Pod) []aggregate {
	index := map[string]int{}
	var out []aggregate
	for _, pod := range pods {
		for _, c := range pod.Containers {
			at, seen := index[c.Name]
			if !seen {
				out = append(out, aggregate{name: c.Name, spec: c, qos: pod.QOS})
				at = len(out) - 1
				index[c.Name] = at
			}
			out[at].podNames = append(out[at].podNames, pod.Name)
			if c.OOMKilled {
				out[at].oomKilled = true
			}
			if c.OOMKilledEarlier {
				out[at].oomEarlier = true
			}
			if c.CrashLooping {
				out[at].crashLooping = true
			}
			if c.RestartCount > out[at].maxRestarts {
				out[at].maxRestarts = c.RestartCount
			}
		}
	}
	return out
}

// usage is pooled usage for one container and one resource.
//
// Two sets of samples, and the difference is the point. mean, p95 and sizeMax
// are over the samples the sizing may use: no minutes after a start, no
// crash-looping replica (usableSamples). max is over everything observed,
// because a limit is never sized under a value the container has reached,
// warming up or not.
type usage struct {
	hasHistory bool
	p95        float64
	mean       float64
	max        float64
	// sizeMax is the largest sample the sizing may use: what a memory request
	// and a bursty CPU request are sized on.
	sizeMax float64
	// judged is the confidence and the sufficiency of the figures, from how
	// much of the window the usable samples cover - see confidence.go.
	judged judgement
	// samplesPerReplica is the window's points per windowed replica. Per
	// replica and not in total, because ten replicas observed for a minute is
	// a minute of coverage, not ten.
	samplesPerReplica int
	// series is kept per replica rather than concatenated: the trend regression
	// runs on each separately, since pooling would interleave timestamps and
	// regress a sawtooth.
	series [][]Point
	// perReplicaMean is what says the replicas of one workload disagree.
	perReplicaMean []float64
	// span is how much time the usable samples cover (judgment.usable). It
	// is what the message says a figure was read from - "5 days of history" -
	// beside the confidence level, which is derived from it.
	span time.Duration
}

// usageStats pools one container's usage across replicas, or reports that
// nothing measured it.
//
// The absence is the point: a container whose usage was never read produces no
// suggestion at all, rather than a suggestion built on a zero. That is why the
// caller's HasUsage flag exists, and why a container without it is skipped
// before its history is even looked up.
//
// A window is read when hist holds any series at all: the page then has a
// Prometheus, and a container with too little of it is judged insufficient
// rather than sized from its snapshot (confidence.go).
func usageStats(pods []Pod, containerName string, isCPU bool, hist HistoryMap) (usage, bool) {
	var pooled, snapshots, perReplicaMean, unwindowed, observed []float64
	var series [][]Point
	measured, looping := 0, 0

	for _, pod := range pods {
		var c *Container
		for i := range pod.Containers {
			if pod.Containers[i].Name == containerName {
				c = &pod.Containers[i]
				break
			}
		}
		if c == nil || !c.HasUsage {
			continue
		}
		measured++
		snap := float64(c.MemUsage)
		if isCPU {
			snap = float64(c.CPUUsage)
		}
		var points []Point
		if h, ok := hist[HistoryKey(pod.Namespace, pod.Name, containerName)]; ok {
			points = h.Memory
			if isCPU {
				points = h.CPU
			}
		}
		// Everything observed, for the peak a limit is held to: the window, or
		// the snapshot of a replica that has none, as the pool below takes them.
		if len(points) >= 2 {
			for _, p := range points {
				observed = append(observed, p.V)
			}
		} else {
			observed = append(observed, snap)
		}
		// A crash-looping replica's usage says what a dying process managed
		// to allocate, not what the workload needs: none of it is sized from.
		// Its peak stays in `observed` above, so no limit is sized under it.
		if c.CrashLooping {
			looping++
			continue
		}
		points = usableSamples(points, c.Starts)

		// A reported zero is a sample. It used to be dropped here, and dropping it
		// emptied `snapshots`, which made usageStats return false, which made
		// resourceReading answer Measured: false — so a genuinely idle container
		// with a 2 000m request drew a bar saying **nothing reported it**, when
		// metrics-server had reported the most actionable number on the page.
		// ResourceReading.Measured's own doc says that distinction is load-bearing.
		//
		// What it does *not* do is produce a suggestion: `requestOverkill` still
		// requires meanUse > 0, because 1.3 × 0 rounds to zero and "reduce your
		// request to nothing" is not advice. The bar says the container is using
		// none of what it asked for; the engine stays silent because it cannot
		// name a number. That split is deliberate.
		snapshots = append(snapshots, snap)

		if len(points) >= 2 {
			series = append(series, points)
			values := make([]float64, len(points))
			for i, p := range points {
				values[i] = p.V
			}
			pooled = append(pooled, values...)
			perReplicaMean = append(perReplicaMean, mean(values))
		} else {
			// Kept aside rather than pooled — see below for what is done with it.
			unwindowed = append(unwindowed, snap)
			// Same reasoning: a replica measured at zero has a mean of zero, and
			// leaving it out of the pool would make the other replicas' average
			// stand in for the whole workload.
			perReplicaMean = append(perReplicaMean, snap)
		}
	}

	if measured == 0 {
		return usage{}, false
	}
	// The former pods' series: pooled with the running replicas', and never
	// a replica - no snapshot, no share in the spread between replicas, no
	// trend, which is about where the running ones are heading.
	former, formerRaw := formerSeries(hist, pods, containerName, isCPU)
	for _, points := range formerRaw {
		for _, p := range points {
			observed = append(observed, p.V)
		}
	}
	for _, points := range former {
		for _, p := range points {
			pooled = append(pooled, p.V)
		}
	}
	windowed := len(hist) > 0
	if len(snapshots) == 0 {
		// Every measured replica crash-loops: there is nothing to size from,
		// and the bar still draws what was seen rather than "not measured".
		peak := maxOf(observed)
		return usage{mean: mean(observed), p95: peak, max: peak, sizeMax: peak, samplesPerReplica: 1,
			judged: judgement{confidence: ConfidenceLow, insufficient: true, reason: reasonCrashLoop}}, true
	}

	// hasHistory counts **windowed** points only. A workload of two replicas
	// with no window at all has two snapshots, and reading those as a
	// distribution would restore the burst guard and the P95 out of thin air —
	// which is the failure this whole file exists to stop, arrived at from the
	// other side.
	hasHistory := len(pooled) >= 2
	values := snapshots
	if hasHistory {
		// A **partial** window is the ordinary state, not an edge case: a pod
		// started four minutes ago has no series yet, and a Prometheus that
		// dropped samples has none for whoever it dropped them for. Once the
		// pooled values are what mean, P95 and max are taken over, a replica
		// without a window would otherwise be invisible to the engine while its
		// own bar — drawn from this same snapshot — shows it plainly. A replica
		// at 950m the engine cannot see is a reduction suggested past a number on
		// the same screen.
		//
		// So its snapshot joins the pool, weighted as the one sample it is. Among
		// a few hundred windowed points it barely moves the mean or the P95,
		// which is right: a single scrape is not a distribution. What it does
		// move is `max` — and max is what the burst guard and every limit
		// suggestion are sized from, which is the half that must never be
		// silently low. samplesPerReplica deliberately stays over `pooled` alone,
		// because the window's coverage is a claim about series, and one scrape
		// is not coverage.
		values = append(append(make([]float64, 0, len(pooled)+len(unwindowed)), pooled...), unwindowed...)
	}

	st := usage{
		hasHistory:        hasHistory,
		mean:              mean(values),
		max:               math.Max(maxOf(values), maxOf(observed)),
		sizeMax:           maxOf(values),
		samplesPerReplica: 1,
		series:            series,
		perReplicaMean:    perReplicaMean,
	}
	if hasHistory {
		st.p95 = percentile95(values)
		pool := append(append([][]Point(nil), series...), former...)
		st.samplesPerReplica = int(math.Round(float64(len(pooled)) / float64(len(pool))))
		st.judged = judgeWindow(pool)
		st.span = st.judged.usable
	} else {
		// With one sample per replica there is no distribution to take a
		// percentile of, so the worst replica stands in for it. Saying "P95" of a
		// single scrape would be a claim the data cannot support.
		st.p95 = st.sizeMax
		st.judged = judgeSnapshot(windowed)
	}
	if looping > 0 && !st.judged.insufficient {
		st.judged.note = fmt.Sprintf("%d crash-looping replica(s) left out of the sizing", looping)
	}
	return st, true
}

// burstRatio is peak over P95, or zero when there is no window to measure it in.
// The peak is the sizing's (sizeMax): a start's spike is not a burst.
func burstRatio(st usage) float64 {
	if !st.hasHistory || st.p95 <= 0 {
		return 0
	}
	return st.sizeMax / st.p95
}

// replicaSpreadWarning surfaces replicas of one workload that are not doing the
// same amount of work — which usually means the load balancer, not the sizing,
// and a suggestion pooled across them deserves the caveat.
func replicaSpreadWarning(st usage, isCPU bool) (string, bool) {
	if len(st.perReplicaMean) < 2 {
		return "", false
	}
	lo, hi := st.perReplicaMean[0], st.perReplicaMean[0]
	for _, v := range st.perReplicaMean[1:] {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	if lo <= 0 || hi/lo < 2 {
		return "", false
	}
	return fmt.Sprintf("Replica usage varies %s× (%s – %s) — the workload may be unevenly loaded",
		toFixed(hi/lo, 1), fmtRaw(lo, isCPU), fmtRaw(hi, isCPU)), true
}

// subject is what every suggestion of one workload shares.
type subject struct {
	workload     string
	workloadKind string
	namespace    string
	pod          string
	podCount     int
	// capacity is the ceiling for this workload: the cluster's, or for a
	// DaemonSet the smallest of its nodes - see NodeCapacity.forWorkload.
	capacity NodeCapacity
	// autoscalers are the HPAs and VPAs aimed at it.
	autoscalers []Autoscaler
	// rollout is what its kept revisions say about its memory limits - see
	// rollout.go. Nil when nothing was read.
	rollout *Rollout
}

func (c subject) base(container string) Suggestion {
	return Suggestion{
		Workload:     c.workload,
		WorkloadKind: c.workloadKind,
		Namespace:    c.namespace,
		Pod:          c.pod,
		PodCount:     c.podCount,
		Container:    container,
	}
}

// cappedNote is the message suffix admitting a suggestion was clamped to what
// the largest node can hold.
func cappedNote(s sized) string {
	if s.capped {
		return " · capped to node capacity"
	}
	return ""
}

// restartCaveat words the restart count, and says when it is doing something:
// a caveat reading "3 restarts observed" beside a page with no suggestion on
// it does not tell the operator that the second is caused by the first, and
// they then look for the missing advice.
func restartCaveat(agg aggregate, crashLooping bool) string {
	plural := ""
	if agg.maxRestarts > 1 {
		plural = "s"
	}
	oom := ""
	if agg.oomKilled {
		oom = " (OOMKilled)"
	}
	if crashLooping {
		oom += " — crash-looping, so no reduction is suggested for it"
	}
	return fmt.Sprintf("%d restart%s observed%s", agg.maxRestarts, plural, oom)
}

// earlierOOMCaveat is said when a kill for memory is older than a day: no
// alarm, and still no memory reduction - the workload has reached its limit
// before, and a sample since says nothing about the next time.
const earlierOOMCaveat = "Killed for memory more than a day ago — no alarm for that now, but no memory reduction is suggested either"

// fromWhat says what a figure was read from: how much usable history, or
// nothing when it is one scrape (the single-scrape caveat says that). The
// level is Suggestion.Confidence; this is the measurement it was derived from.
func fromWhat(st usage) string {
	if !st.hasHistory {
		return ""
	}
	switch {
	case st.span >= 48*time.Hour:
		return fmt.Sprintf(" · %d days of history", int64(st.span/(24*time.Hour)))
	case st.span >= time.Hour:
		return " · " + toFixed(st.span.Hours(), 0) + "h of history"
	}
	return fmt.Sprintf(" · %dm of history", int64(math.Round(st.span.Minutes())))
}

// oomWithLimit is the OOMKilled danger for a memory limit that exists. A
// function rather than a branch because it has two callers with different
// evidence: the measured path passes the worst observed sample, and the
// unmeasured path passes zero — the alarm's number needs no sample, because
// the limit the container was killed at is itself the floor the increase is
// computed from.
func oomWithLimit(subj subject, container string, lim int64, maxUse float64, guaranteedPair bool, warnings []string) Suggestion {
	s := suggestCapped(math.Max(float64(lim), maxUse)*1.5, false, subj.capacity)
	out := subj.base(container)
	out.Resource, out.Kind = "Memory", KindDanger
	if s.capped && s.raw <= lim {
		out.Action = "Migrate to larger node"
		out.Message = "Container was OOMKilled — node capacity too small to increase the memory limit"
		out.Current, out.Suggested, out.SuggestedRaw = fmtRaw(float64(lim), false), fmtRaw(float64(lim), false), lim
	} else {
		out.Action = "Increase limit"
		out.Message = "Container was OOMKilled — memory limit is too low" + cappedNote(s) + s.fitSuffix
		out.Current, out.Suggested, out.SuggestedRaw = fmtRaw(float64(lim), false), s.suggested, s.raw
		out.AppliesToBoth = guaranteedPair
		out.fromLimit = true
	}
	out.Warnings = warnings
	return out
}

// migrateKind is how loud "no node is big enough" is: a kill for memory, a
// throttle for CPU.
func migrateKind(isCPU bool) Kind {
	if isCPU {
		return KindWarning
	}
	return KindDanger
}

// worse is the louder of two kinds.
func worse(a, b Kind) Kind {
	rank := map[Kind]int{KindDanger: 0, KindWarning: 1, KindOverkill: 2, "": 3}
	if rank[b] < rank[a] {
		return b
	}
	return a
}

// analyzeCPUMem is the engine. Read the guard rails in the package comment
// and the numbers in targets.go before changing anything here: most of the
// branches below exist to *not* emit a suggestion, and each one of those is
// the interesting half.
//
// Per resource it decides the request first and the limit second, because the
// limit has to come out at or above the request the container will have -
// Kubernetes refuses the pod otherwise - and a request raised past the limit
// it has must carry the limit with it in the same command.
func analyzeCPUMem(agg aggregate, pods []Pod, subj subject, hist HistoryMap, memo statsMemo) []Suggestion {
	var results []Suggestion
	c := agg.spec
	nc := subj.capacity

	// youngSample is true when every replica whose usage was read is provably
	// younger than warmupWindow — the sample then describes processes still
	// warming up, and no reduction may be built on it. One mature replica is
	// enough to clear it: its samples are in the pool, so the pool has seen a
	// working set. A replica whose age the API server did not report counts as
	// mature, because a refusal must rest on evidence.
	measured, matured := 0, false
	for _, pod := range pods {
		for i := range pod.Containers {
			cc := &pod.Containers[i]
			if cc.Name != agg.name || !cc.HasUsage {
				continue
			}
			measured++
			if !pod.AgeKnown || pod.Age >= warmupWindow {
				matured = true
			}
		}
	}
	youngSample := measured > 0 && !matured

	// A crash loop, and no longer a restart history. `RestartCount` is
	// cumulative for the life of the pod and never decays, so
	// `maxRestarts >= 3` alone made a StatefulSet up since February with three
	// restarts from node maintenance permanently "crashlooping" — and
	// suppressed every reduction for it, for ever, silently. The count is now
	// corroboration rather than the verdict: it still takes three, and they
	// have to be a loop the kubelet is currently in or a termination inside
	// the last ten minutes. See Container.CrashLooping.
	crashLooping := agg.crashLooping && agg.maxRestarts >= crashLoopRestarts

	// A VPA that applies its recommendations owns these numbers: a kubectl
	// change is overwritten by the next pod it admits. Nothing is suggested,
	// and the card says why (AutoscalerNotes).
	if _, managed := vpaOver(subj.autoscalers); managed {
		return nil
	}

	for _, isCPU := range []bool{true, false} {
		label, resource := "Memory", "memory"
		req, lim := c.MemRequest, c.MemLimit
		if isCPU {
			label, resource = "CPU", "cpu"
			req, lim = c.CPURequest, c.CPULimit
		}

		// Guaranteed holds only while request equals limit for this resource;
		// moving one side alone downgrades the pod to Burstable, which raises its
		// eviction priority — a change of behavior nobody asked for, arriving
		// inside a tip about wasted memory. Computed before the usage gate,
		// because the OOM alarm below needs it whether or not anything measured.
		guaranteedPair := agg.qos == "Guaranteed" && req > 0 && req == lim
		oomNow := !isCPU && agg.oomKilled

		st, ok := memo.usage(pods, agg.name, isCPU, hist)
		if !ok {
			// The OOM alarm must not be gated on a scrape it does not need.
			// metrics-server has nothing for a container that is not running,
			// and the container this alarm exists for — killed so often it is
			// rarely alive at scrape time — is precisely the one that goes
			// unmeasured. The number needs no sample either: the limit it was
			// killed at is the floor the increase is computed from. With no
			// limit at all there is nothing to compute from, and inventing a
			// number is what this package refuses to do — the card still
			// carries the OOMKilled flag.
			if oomNow && lim > 0 {
				var warns []string
				if agg.maxRestarts > 0 {
					warns = append(warns, restartCaveat(agg, crashLooping))
				}
				results = append(results, oomWithLimit(subj, agg.name, lim, 0, guaranteedPair, warns))
			}
			continue
		}

		hpa, hpaTarget, hpaScales := hpaOn(subj.autoscalers, resource)
		oomEver := !isCPU && (agg.oomKilled || agg.oomEarlier)
		reductionsBlocked := crashLooping || oomEver || youngSample
		bursty := burstRatio(st) >= burstThreshold
		reqT := requestTarget(st, isCPU)
		limT := limitTarget(st, isCPU)
		from := fromWhat(st)
		usageWord := "P95"
		if !st.hasHistory {
			usageWord = "current"
		}
		burstNote := ""
		if bursty {
			burstNote = " · bursty (peak " + toFixed(burstRatio(st), 1) + "× P95), sized on the peak"
		}

		// The caveat for a number computed from one instantaneous sample —
		// attached below to every suggestion able to take something away.
		var thinSample []string
		if !st.hasHistory {
			thinSample = []string{singleScrapeCaveat}
		}

		var common []string
		if w, ok := replicaSpreadWarning(st, isCPU); ok {
			common = append(common, w)
		}
		if agg.maxRestarts > 0 {
			common = append(common, restartCaveat(agg, crashLooping))
		}
		if !isCPU && agg.oomEarlier && !agg.oomKilled {
			common = append(common, earlierOOMCaveat)
		}
		// A memory limit raised after a kill outlives the pods that were killed
		// (rollout.go): while that raise is its newest change, no reduction of
		// it is suggested, and every memory row says why. When the history
		// could not be read, nothing is refused and each reduction says the
		// check was not made.
		var raise MemoryRaise
		raised, historyUnknown := false, ""
		if !isCPU {
			if raise, raised = subj.rollout.raise(agg.name, lim); raised {
				common = append(common, raiseCaveat(raise))
			} else {
				historyUnknown = subj.rollout.unknown(agg.name, lim)
			}
		}
		var unchecked []string
		if historyUnknown != "" {
			unchecked = []string{unknownHistoryCaveat(historyUnknown)}
		}
		if youngSample {
			// The same disclosure rule as the crash-loop suffix: a suppression
			// nothing names is advice the operator goes looking for.
			common = append(common, fmt.Sprintf(
				"Every measured replica is younger than %d minutes — usage this early understates the steady state, so no reduction is suggested",
				int(warmupWindow.Minutes())))
		}
		withWarnings := func(extra ...string) []string {
			all := make([]string, 0, len(common)+len(extra))
			all = append(all, common...)
			all = append(all, extra...)
			if len(all) == 0 {
				return nil
			}
			return all
		}
		const qosPairing = "Request and limit are moved together to keep the pod in the Guaranteed QoS class"
		var hpaCaveat []string
		if hpaScales {
			hpaCaveat = []string{fmt.Sprintf(
				"HPA %s scales this workload on %s at %d%% of the request: a new request changes when it scales, so review its target with it",
				hpa.Name, strings.ToLower(label), hpaTarget)}
		}

		// --- OOMKilled in the last day: the strongest signal a memory limit is too low
		limitDone := false
		// limAfter is the limit the container will have: the OOM row's when it
		// raises one, so a request raised in the same pass is checked against
		// the limit it will meet rather than the one being replaced.
		limAfter := lim
		// oomAt is the OOM row raising the limit, when there is one: a request
		// that must rise past the limit is folded into it, so the two never
		// name two different limits.
		oomAt := -1
		if oomNow {
			limitDone = true
			if lim > 0 {
				row := oomWithLimit(subj, agg.name, lim, st.max, guaranteedPair, withWarnings())
				if row.Action == "Increase limit" {
					limAfter = row.SuggestedRaw
					if !guaranteedPair {
						oomAt = len(results)
					}
				}
				results = append(results, row)
			} else if st.max > 0 {
				// A new cap sized from a post-restart sample — the sample the
				// whole guard rail distrusts — so ×2, never under the request,
				// and the single-scrape caveat rides along when there is no
				// window behind it.
				s := suggestCapped(math.Max(st.max*2, float64(req)), isCPU, nc)
				out := subj.base(agg.name)
				out.Resource, out.Kind = label+" — no limit", KindDanger
				out.Action = "Set limit"
				out.Message = "Container was OOMKilled with no memory limit — the node ran out of memory" + cappedNote(s) + s.fitSuffix
				out.Current, out.Suggested, out.SuggestedRaw = "unlimited", s.suggested, s.raw
				out.Warnings = withWarnings(thinSample...)
				results = append(results, out)
			}
		}

		// --- The request -------------------------------------------------
		// reducedAt is the index of this pass's "Reduce request" row, so a limit
		// that would land under the current request can carry the reduction in
		// its own command - see foldRequest.
		reducedAt := -1
		// reqAfter is the request the container will have once what follows is
		// applied: the floor every limit below is held to.
		reqAfter := req
		limitPress, limitPressed := limitPressure(st.p95, lim, isCPU)

		switch {
		case req == 0:
			// No request: the scheduler is placing this pod blind. Only when the
			// samples name a number — a container measured at exactly zero has
			// no basis, and `--requests=cpu=0m` is not advice. metrics-server
			// reports a hard zero for any container that used under a millicore
			// in the window, so an idle sidecar with no request would otherwise
			// walk straight into it.
			if reqT > 0 {
				s := suggestCapped(float64(reqT), isCPU, nc)
				out := subj.base(agg.name)
				out.Resource, out.Kind = label+" — no request", KindWarning
				out.Action = "Set request"
				out.Message = "No " + label + " request set — the scheduler places this pod without reserving anything for it" + cappedNote(s) + s.fitSuffix
				out.Current, out.Suggested, out.SuggestedRaw = "none", s.suggested, s.raw
				if lim > 0 && s.raw > lim {
					// A request has to stay at or under the limit.
					out.Suggested, out.SuggestedRaw = fmtRaw(float64(lim), isCPU), lim
				}
				out.Warnings = withWarnings(hpaCaveat...)
				reqAfter = out.SuggestedRaw
				results = append(results, out)
			}

		case requestOversized(st, req, isCPU):
			// One target, the one an increase would also propose, so the
			// reduction applied is not called too low on the next read.
			value := reqT
			action := "Reduce request"
			var extra []string
			switch {
			case reductionsBlocked:
				value = 0
			case hpaScales:
				// The HPA keeps usage at its target share of the request: the
				// gap is the target at work, and a smaller request means more
				// replicas, not less reserved. Said on the card.
				value = 0
			case guaranteedPair:
				// The pair moves the limit too, so it takes the larger of the
				// two targets: a memory limit under the peak is a kill.
				if !isCPU && !st.hasHistory {
					value = 0 // never lower a memory limit from one sample
					break
				}
				if !isCPU && raised {
					value = 0 // the pair lowers the limit a raise set - see rollout.go
					break
				}
				value = max(reqT, limT)
				action = "Reduce request + limit"
				extra = append(extra, qosPairing)
				if !isCPU {
					extra = append(extra, unchecked...)
				}
				if float64(value) > float64(req)*worthARollout {
					value = 0
				}
			}
			if value > 0 {
				extra = append(extra, thinSample...)
				out := subj.base(agg.name)
				out.Resource, out.Kind = label, KindOverkill
				out.Action = action
				// Against the figure the target was sized on: the P95 for CPU,
				// the peak for memory.
				basis, word := st.p95, usageWord
				if !isCPU {
					basis = st.sizeMax
					if st.hasHistory {
						word = "peak"
					}
				}
				out.Message = fmt.Sprintf("%s request is %s× its %s usage%s%s",
					label, toFixed(float64(req)/math.Max(basis, 1), 1), word, from, burstNote)
				out.Current, out.Suggested, out.SuggestedRaw = fmtRaw(float64(req), isCPU), fmtRaw(float64(value), isCPU), value
				out.AppliesToBoth = guaranteedPair
				out.Warnings = withWarnings(extra...)
				reqAfter = value
				if guaranteedPair {
					limitDone = true
				} else {
					reducedAt = len(results)
				}
				results = append(results, out)
			}

		default:
			kind, under := requestPressure(st.p95, req, isCPU)
			if !under {
				break
			}
			ratio := st.p95 / float64(req)
			s := suggestCapped(float64(reqT), isCPU, nc)
			if s.capped && s.raw <= req {
				out := subj.base(agg.name)
				out.Resource, out.Kind = label, migrateKind(isCPU)
				out.Action = "Migrate to larger node"
				out.Message = fmt.Sprintf("%s %s usage is %s× the request — node capacity too small to increase the request%s",
					label, usageWord, toFixed(ratio, 1), from)
				out.Current, out.Suggested, out.SuggestedRaw = fmtRaw(float64(req), isCPU), fmtRaw(float64(req), isCPU), req
				out.Warnings = withWarnings()
				results = append(results, out)
				break
			}
			why := "first in line for eviction when the node runs short of memory"
			if isCPU {
				why = "it runs on idle node CPU, and under contention gets only its request's share"
			}
			out := subj.base(agg.name)
			out.Resource, out.Kind = label, kind
			out.Action = "Increase request"
			out.Message = fmt.Sprintf("%s %s usage is %s× the request — %s%s%s%s",
				label, usageWord, toFixed(ratio, 1), why, from, cappedNote(s), s.fitSuffix)
			out.Current, out.Suggested, out.SuggestedRaw = fmtRaw(float64(req), isCPU), s.suggested, s.raw
			extra := append([]string(nil), hpaCaveat...)
			switch {
			case guaranteedPair:
				// One value for both, the larger of the two targets, and one
				// row: the limit is moved by this one.
				pair := suggestCapped(float64(max(reqT, limT)), isCPU, nc)
				out.Action = "Increase request + limit"
				out.Suggested, out.SuggestedRaw = pair.suggested, pair.raw
				out.AppliesToBoth = true
				out.Kind = worse(kind, limitPress)
				extra = append(extra, qosPairing)
				limitDone = true
			case lim > 0 && s.raw > lim && oomAt >= 0:
				// An OOM row already raises this limit: one command, on that
				// row, raises both, instead of two rows naming two limits,
				// whose end state depended on which ran last.
				//
				// The OOM row's limit is never under this request: it is
				// 1.5 × max(limit, peak), the request 1.3 × P95 (or the peak
				// when bursty, still under 1.5 × it), both rounded on the same
				// ladder and capped at the same ceiling.
				// TestAnOOMRowCarriesTheRequestItMustRaise holds that.
				oom := &results[oomAt]
				oom.PairedRequestRaw = s.raw
				oom.Warnings = append(oom.Warnings, fmt.Sprintf(
					"Usage is also %s× the request, so the request (%s) is raised to %s in the same command",
					toFixed(ratio, 1), fmtRaw(float64(req), isCPU), s.suggested))
				reqAfter = s.raw
				continue
			case lim > 0 && s.raw > lim:
				// The request would pass the limit the container has, and the
				// API server refuses a request above its limit. The limit moves
				// in the same command, to the largest of its own target, the
				// request, and what an OOM row beside this one raises it to -
				// compared with the limit the container has *now*, because this
				// command may be run first, or alone.
				paired := max(limT, s.raw, limAfter)
				if ceiling := capOf(nc, isCPU); ceiling > 0 && paired > ceiling {
					paired = max(ceiling, s.raw)
				}
				out.Action = "Increase request + limit"
				out.PairedLimitRaw = paired
				out.Kind = worse(kind, limitPress)
				extra = append(extra, fmt.Sprintf("The limit (%s) is below the new request, so it is raised to %s in the same command",
					fmtRaw(float64(lim), isCPU), fmtRaw(float64(paired), isCPU)))
				limitDone = true
			}
			out.Warnings = withWarnings(extra...)
			reqAfter = out.SuggestedRaw
			results = append(results, out)
		}

		if limitDone {
			continue
		}

		// --- The limit ---------------------------------------------------
		switch {
		case lim == 0 && isCPU:
			// No CPU limit is not a finding. CPU is compressible: a container
			// above its request takes only idle cycles, and under contention
			// the kernel shares CPU by request. A CPU limit throttles in 100 ms
			// periods even on an idle node, and current practice is to set the
			// request and leave the limit off unless there is a reason. The
			// engine used to raise a warning on every such container.

		case lim == 0:
			// No memory limit: memory is not compressible, and a leak takes the
			// node's. The basis guard is the same refusal as the request's:
			// "set your limit to 0 KiB" is a patch that suffocates the
			// container. Never under the request it will have - the API server
			// refuses that - and never under the peak.
			if limT <= 0 || raised {
				// raised: the limit was removed after a kill, and setting one
				// now is the reduction rollout.go refuses.
				break
			}
			s := suggestCapped(float64(max(limT, RoundResource(float64(reqAfter), isCPU))), isCPU, nc)
			out := subj.base(agg.name)
			out.Resource, out.Kind = label+" — no limit", KindWarning
			out.Action = "Set limit"
			out.Message = "No memory limit set — a leak can take the node's memory, and the kernel then kills whatever it picks" + cappedNote(s) + s.fitSuffix
			out.Current, out.Suggested, out.SuggestedRaw = "unlimited", s.suggested, s.raw
			out.Warnings = withWarnings(thinSample...)
			results = foldRequest(results, out, reducedAt, req, reqAfter, isCPU)

		case limitPressed:
			s := suggestCapped(float64(max(limT, RoundResource(float64(reqAfter), isCPU))), isCPU, nc)
			pct := int64(math.Round(st.p95 / float64(lim) * 100))
			if s.capped && s.raw <= lim {
				out := subj.base(agg.name)
				out.Resource, out.Kind = label, migrateKind(isCPU)
				out.Action = "Migrate to larger node"
				out.Message = fmt.Sprintf("%s P95 usage at %d%% of limit — node capacity too small to increase the limit%s",
					label, pct, from)
				out.Current, out.Suggested, out.SuggestedRaw = fmtRaw(float64(lim), isCPU), fmtRaw(float64(lim), isCPU), lim
				out.Warnings = withWarnings()
				results = append(results, out)
				break
			}
			why := " — the kernel kills it at the limit"
			if isCPU {
				why = " — throttled at the limit; removing the CPU limit is the other fix, the request already guarantees its share"
			}
			out := subj.base(agg.name)
			out.Resource, out.Kind = label, limitPress
			out.Action = "Increase limit"
			out.Message = fmt.Sprintf("%s P95 usage at %d%% of limit%s%s%s%s", label, pct, why, from, cappedNote(s), s.fitSuffix)
			out.Current, out.Suggested, out.SuggestedRaw = fmtRaw(float64(lim), isCPU), s.suggested, s.raw
			out.AppliesToBoth = guaranteedPair
			out.Warnings = withWarnings()
			results = append(results, out)

		case !isCPU && st.hasHistory && trendReaches(st, lim):
			// Memory only. A memory series climbing towards its limit is a
			// leak or a growing cache, and it ends in a kill. CPU follows
			// traffic - a morning ramp regressed over two hours "exceeds its
			// limit in 3h", and a CPU limit is a throttle besides - so the
			// trend is not run on it any more.
			soonest := soonestToLimit(st, lim)
			hours := soonest / 3600
			trendKind := KindWarning
			if hours < 4 {
				trendKind = KindDanger
			}
			timeStr := toFixed(hours, 1) + "h"
			if hours < 1 {
				timeStr = fmt.Sprintf("%dm", int64(math.Round(soonest/60)))
			}
			s := suggestCapped(math.Max(float64(lim)*1.5, float64(limT)), isCPU, nc)
			out := subj.base(agg.name)
			out.Resource = label
			if s.capped && s.raw <= lim {
				out.Kind = KindDanger
				out.Action = "Migrate to larger node"
				out.Message = fmt.Sprintf("%s trending to exceed limit in ~%s — node capacity too small to increase the limit (linear trend%s)",
					label, timeStr, strings.Replace(from, " · ", ", ", 1))
				out.Current, out.Suggested, out.SuggestedRaw = fmtRaw(float64(lim), isCPU), fmtRaw(float64(lim), isCPU), lim
			} else {
				out.Kind = trendKind
				out.Action = "Increase limit"
				out.Message = fmt.Sprintf("%s trending to exceed limit in ~%s (linear trend%s)%s%s",
					label, timeStr, strings.Replace(from, " · ", ", ", 1), cappedNote(s), s.fitSuffix)
				out.Current, out.Suggested, out.SuggestedRaw = fmtRaw(float64(lim), isCPU), s.suggested, s.raw
				out.AppliesToBoth = guaranteedPair
			}
			out.Warnings = withWarnings()
			results = append(results, out)

		case !isCPU && !guaranteedPair && !reductionsBlocked && !raised && st.hasHistory &&
			st.p95 > 0 && float64(lim)/st.p95 >= limitGenerous:
			// A memory limit far above what is used. **Not money**: a limit
			// reserves nothing, the request does. What a tighter limit buys is
			// containment - a leak killed in its own container before it
			// reaches the node - so it is offered, quietly, with a window only:
			// a memory limit lowered from one sample is the next OOMKill.
			//
			// CPU limits are not reduced at all any more: lowering one saves
			// nothing and adds throttling, which is the opposite of the advice
			// above about not setting one.
			value := max(limT, RoundResource(float64(reqAfter), isCPU))
			if float64(value) > float64(lim)*worthARollout {
				break
			}
			if trendReaches(st, value) {
				// A series climbing towards the limit this would set: the
				// reduction is the trend's next "increase limit". Not offered.
				break
			}
			out := subj.base(agg.name)
			out.Resource, out.Kind = label, KindOverkill
			out.Action = "Reduce limit"
			out.Message = fmt.Sprintf("%s limit is %s× P95 usage — a limit reserves nothing, but a tighter one stops a leak before it reaches the node%s%s",
				label, toFixed(float64(lim)/st.p95, 1), from, burstNote)
			out.Current, out.Suggested, out.SuggestedRaw = fmtRaw(float64(lim), isCPU), fmtRaw(float64(value), isCPU), value
			out.Warnings = withWarnings(unchecked...)
			results = foldRequest(results, out, reducedAt, req, reqAfter, isCPU)
		}
	}
	return results
}

// foldRequest appends a limit row, folding this pass's request reduction into
// it when the limit alone would be refused.
//
// A new or reduced limit is sized at or above the request the container will
// have. When that is the *reduced* request and the limit is under the current
// one, the limit's command alone is refused - and the page lists a warning ("no
// memory limit") before an over-provisioning row, so the refused one was first.
// The two become one command, on the limit's row: `--requests=… --limits=…`,
// and the separate reduction row goes.
func foldRequest(results []Suggestion, limitRow Suggestion, reducedAt int, req, reqAfter int64, isCPU bool) []Suggestion {
	if reducedAt < 0 || limitRow.SuggestedRaw >= req {
		return append(results, limitRow)
	}
	limitRow.PairedRequestRaw = reqAfter
	limitRow.Warnings = append(limitRow.Warnings, fmt.Sprintf(
		"The request (%s) is above this limit, so it is reduced to %s in the same command",
		fmtRaw(float64(req), isCPU), fmtRaw(float64(reqAfter), isCPU)))
	out := append(results[:reducedAt:reducedAt], results[reducedAt+1:]...)
	return append(out, limitRow)
}

// capOf is the ceiling for one resource, zero when none is known.
func capOf(nc NodeCapacity, isCPU bool) int64 {
	if isCPU {
		return nc.MaxCPU
	}
	return nc.MaxMemory
}

// soonestToLimit is the shortest time, over the replicas, until a series
// reaches the limit; zero and no answer when none is heading there.
func soonestToLimit(st usage, lim int64) float64 {
	soonest := math.MaxFloat64
	for _, points := range st.series {
		if secs, ok := secondsToThreshold(points, float64(lim)); ok && secs < soonest {
			soonest = secs
		}
	}
	return soonest
}

// trendReaches says whether any replica's series reaches the limit inside a
// day.
func trendReaches(st usage, lim int64) bool {
	return soonestToLimit(st, lim) < 24*3600
}

// ephemeralWorthALimit is how much a container must be writing to its own
// layer and logs before the absence of an ephemeral-storage limit is a finding.
// Most workloads set none, the kubelet evicts on node disk pressure, and a
// warning on every container that writes a log line was the page's loudest
// noise. A gigabyte is a container that can fill a node.
const ephemeralWorthALimit = 1024 * mib

// analyzeEphemeral sizes a container's ephemeral storage. The worst replica
// drives it: ephemeral usage is a local disk filling on one node, not a pooled
// quantity, so the average across replicas describes nothing that exists.
func analyzeEphemeral(agg aggregate, pods []Pod, subj subject) []Suggestion {
	var use, lim int64
	for _, pod := range pods {
		for _, c := range pod.Containers {
			if c.Name != agg.name {
				continue
			}
			if c.EphemeralUsage > use {
				use = c.EphemeralUsage
			}
			if c.EphemeralLimit > lim {
				lim = c.EphemeralLimit
			}
		}
	}
	if use == 0 {
		return nil
	}

	var results []Suggestion
	if lim == 0 {
		if use < ephemeralWorthALimit {
			return nil
		}
		s := suggest(float64(use)*2, false)
		out := subj.base(agg.name)
		out.Resource, out.Kind = "Ephemeral — no limit", KindWarning
		out.Action = "Set limit"
		out.Message = fmt.Sprintf("No ephemeral-storage limit set, and the container has written %s to its layer and logs — it can fill the node's disk", fmtRaw(float64(use), false))
		out.Current, out.Suggested, out.SuggestedRaw = "unlimited", s.suggested, s.raw
		results = append(results, out)
		return results
	}

	pct := float64(use) / float64(lim)
	if pct < 0.70 {
		return nil
	}
	kind := KindWarning
	if pct >= 0.90 {
		kind = KindDanger
	}
	s := suggest(float64(use)*1.5, false)
	out := subj.base(agg.name)
	out.Resource, out.Kind = "Ephemeral", kind
	out.Action = "Increase limit"
	out.Message = fmt.Sprintf("Ephemeral usage at %d%% of limit — the pod is evicted when it passes it", int64(math.Round(pct*100)))
	out.Current, out.Suggested, out.SuggestedRaw = fmtRaw(float64(lim), false), s.suggested, s.raw
	results = append(results, out)
	return results
}

// analyzeVolumes reports claims near capacity and large emptyDirs with no
// ceiling.
//
// PVCs stay per-pod and emptyDirs are deduplicated, and the asymmetry is real:
// a StatefulSet's volumeClaimTemplates make one physical disk per replica, so
// three replicas at 95 % are three disks to expand; an emptyDir is one line of
// the pod template repeated.
func analyzeVolumes(pods []Pod, workload, kind, namespace string) []Suggestion {
	var results []Suggestion
	seenEmptyDir := map[string]bool{}

	for _, pod := range pods {
		for _, vol := range pod.Volumes {
			if vol.Usage == 0 {
				continue
			}

			if vol.Type == "pvc" && vol.Capacity > 0 {
				pct := float64(vol.Usage) / float64(vol.Capacity)
				if pct < 0.75 {
					continue
				}
				kindOf := KindWarning
				if pct >= 0.90 {
					kindOf = KindDanger
				}
				name := vol.PVCName
				if name == "" {
					name = vol.Name
				}
				s := suggest(float64(vol.Capacity)*1.5, false)
				results = append(results, Suggestion{
					Workload: workload, WorkloadKind: kind, Namespace: namespace,
					Pod: pod.Name, PodCount: 1, Container: name,
					Resource: "PVC", Kind: kindOf, Action: "Expand PVC",
					Message:      fmt.Sprintf("PVC %q at %d%% capacity", name, int64(math.Round(pct*100))),
					Current:      fmtRaw(float64(vol.Capacity), false),
					Suggested:    s.suggested,
					SuggestedRaw: s.raw,
				})
				continue
			}

			// An emptyDir with no sizeLimit is the node's disk; it is a finding
			// once it holds enough to matter, the same gigabyte as a
			// container's own layer.
			if vol.Type == "emptyDir" && !vol.HasSizeLimit && vol.Usage >= ephemeralWorthALimit && !seenEmptyDir[vol.Name] {
				seenEmptyDir[vol.Name] = true
				s := suggest(float64(vol.Usage)*2, false)
				results = append(results, Suggestion{
					Workload: workload, WorkloadKind: kind, Namespace: namespace,
					Pod: pod.Name, PodCount: len(pods), Container: vol.Name,
					Resource: "EmptyDir", Kind: KindWarning, Action: "Set sizeLimit",
					Message:      fmt.Sprintf("EmptyDir %q has no sizeLimit and holds %s — it can fill the node's disk", vol.Name, fmtRaw(float64(vol.Usage), false)),
					Current:      "unlimited",
					Suggested:    s.suggested,
					SuggestedRaw: s.raw,
				})
			}
		}
	}
	return results
}

// Compute is the whole engine: every suggestion across every workload, danger
// first.
//
// history and nodes are both optional and both change what can be said rather
// than how much. Without history there is no P95, no burst ratio and no trend,
// so the suggestions that remain are built on a single scrape and are labeled
// as such. Without nodes no increase is capped, and none of them can say how
// many nodes would still take the pod.
func Compute(workloads []Workload, history []ContainerHistory, nodes []Node) []Suggestion {
	return compute(workloads, history, nodes, nil)
}

// compute is Compute, with the figures already taken in this read (statsMemo).
func compute(workloads []Workload, history []ContainerHistory, nodes []Node, memo statsMemo) []Suggestion {
	all := BuildHistoryMap(history)
	page := pageHistory(workloads, history)
	nc := MaxNodeCapacity(nodes)

	var out []Suggestion
	for _, w := range workloads {
		if len(w.Pods) == 0 {
			continue
		}
		// Where this workload's rows start, so qualify can judge them below,
		// and the series it is sized from, its former pods' included.
		first := len(out)
		hist := workloadHistory(page, all, w)
		subj := subject{
			workload:     w.Name,
			workloadKind: w.Kind,
			namespace:    w.Namespace,
			pod:          w.Pods[0].Name,
			podCount:     len(w.Pods),
			capacity:     nc.forWorkload(w.Kind, w.Pods),
			autoscalers:  w.Autoscalers,
			rollout:      w.Rollout,
		}
		for _, agg := range aggregateContainers(w.Pods) {
			out = append(out, analyzeCPUMem(agg, w.Pods, subj, hist, memo)...)
			out = append(out, analyzeEphemeral(agg, w.Pods, subj)...)
		}
		out = append(out[:first], qualify(out[first:], w.Pods, hist, memo)...)
		out = append(out, analyzeVolumes(w.Pods, w.Name, w.Kind, w.Namespace)...)
	}

	rank := map[Kind]int{KindDanger: 0, KindWarning: 1, KindOverkill: 2}
	// Stable, so two findings of the same severity stay in the order they were
	// produced — which is the order of the containers in the pod template, and
	// the only order an operator can predict.
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Kind] < rank[out[j].Kind] })
	return out
}

// statsMemo is one read's usageStats, kept for that read only: the engine
// takes a container's figures to size it (analyzeCPUMem), again to say how
// sure it is (qualify), and the page a third time to draw its bar
// (resourceReading), each over a week of points. A memo is made by Assemble
// for one call and dropped with it, so nothing outlives the request; nil
// takes the figures every time.
//
// The key is the workload's pods - the same slice for all three, since they
// walk the same workloads - with the container and the resource. The series
// they are taken from are that workload's (workloadHistory), the same for
// the three as long as the workloads and the history are, which is what one
// Assemble call holds.
type statsMemo map[statsKey]statsAnswer

type statsKey struct {
	pods      *Pod
	n         int
	container string
	isCPU     bool
}

type statsAnswer struct {
	st usage
	ok bool
}

// usage is usageStats, taken once per key.
func (m statsMemo) usage(pods []Pod, container string, isCPU bool, hist HistoryMap) (usage, bool) {
	if m == nil || len(pods) == 0 {
		return usageStats(pods, container, isCPU, hist)
	}
	key := statsKey{pods: &pods[0], n: len(pods), container: container, isCPU: isCPU}
	if a, ok := m[key]; ok {
		return a.st, a.ok
	}
	st, ok := usageStats(pods, container, isCPU, hist)
	m[key] = statsAnswer{st: st, ok: ok}
	return st, ok
}

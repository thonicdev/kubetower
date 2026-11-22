package rightsizing

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

// The window the core reads is seven days at a fifteen-minute step. These
// tests build series at that step, so "a day" and "a week" mean here what they
// mean on a cluster.

// stepEvery is the core's step, in seconds.
const stepEvery = 15 * 60

// t0 is where every synthetic window starts: a real unix time, so a start
// given as one lines up with the samples.
const t0 = 1_700_000_000.0

// window is n samples at the core's step from t0.
func window(n int, f func(i int) float64) []Point {
	out := make([]Point, n)
	for i := range out {
		out[i] = Point{T: t0 + float64(i*stepEvery), V: f(i)}
	}
	return out
}

// samplesIn is how many samples at the core's step cover d, both ends counted.
func samplesIn(d time.Duration) int { return int(d.Seconds())/stepEvery + 1 }

var week = samplesIn(7 * 24 * time.Hour)

func steady(v float64) func(int) float64 { return func(int) float64 { return v } }

// cpuOnly and memOnly are one replica's window for pod-1/app.
func cpuOnly(points []Point) []ContainerHistory {
	return []ContainerHistory{{Pod: "pod-1", Container: "app", CPU: points}}
}

func memOnly(points []Point) []ContainerHistory {
	return []ContainerHistory{{Pod: "pod-1", Container: "app", Memory: points}}
}

// overCPU is a container asking 2 cores and using a fraction: a CPU request
// reduction, whose value is what the tests read.
func overCPU() Container {
	return ctr("app", opts{cpuReq: 2000, cpuUse: 300, memReq: 64 * mibTest, memUse: 60 * mibTest})
}

// overMemory is the same for memory: 4Gi asked and limited, a few hundred MiB
// used. The limit is reduced with the request, in one command that carries it.
func overMemory() Container {
	return ctr("app", opts{memReq: 4096 * mibTest, memLim: 4096 * mibTest, memUse: 300 * mibTest, cpuReq: 100, cpuUse: 90})
}

// memoryRequest is the memory request a computation recommends: a reduction's
// own row, or the request a limit row carries in its command.
func memoryRequest(t *testing.T, c Container, hist []ContainerHistory) int64 {
	t.Helper()
	for _, s := range Compute([]Workload{deployment("api", c)}, hist, nil) {
		switch {
		case !strings.HasPrefix(s.Resource, "Memory"):
		case s.Action == "Reduce request":
			return s.SuggestedRaw
		case s.PairedRequestRaw > 0:
			return s.PairedRequestRaw
		}
	}
	t.Fatal("no memory request recommended")
	return 0
}

func reduceRequest(t *testing.T, resource string, c Container, hist []ContainerHistory) Suggestion {
	t.Helper()
	s, ok := find(Compute([]Workload{deployment("api", c)}, hist, nil), byResourceAction(resource, "Reduce request"))
	if !ok {
		t.Fatalf("no %s 'Reduce request' produced", resource)
	}
	return s
}

func TestCPUIsSizedOnItsP95PlusTheMargin(t *testing.T) {
	// A week at 300m with one sample in fifty at 450m: the P95 is 300m, and
	// the mean, which the old reduction read, is lower than some of the week.
	hist := cpuOnly(window(week, func(i int) float64 {
		if i%50 == 0 {
			return 450
		}
		return 300
	}))
	s := reduceRequest(t, "CPU", overCPU(), hist)
	// 1.2 × 300m = 360m, on the ladder.
	if want := RoundResource(300*requestOverP95CPU, true); s.SuggestedRaw != want {
		t.Errorf("suggested %dm, want %dm: the P95 of 300m plus 20 %%", s.SuggestedRaw, want)
	}
	if s.Confidence != ConfidenceHigh || s.InsufficientData {
		t.Errorf("confidence %q, insufficient %v - a full week is high", s.Confidence, s.InsufficientData)
	}
}

func TestMemoryIsSizedOnItsSpikeAndNotOnItsP95(t *testing.T) {
	// A week at 300Mi with one fifteen-minute peak at 900Mi in the middle,
	// nowhere near a start. A P95 never sees it; the next one evicts the pod.
	hist := memOnly(window(week, func(i int) float64 {
		if i == week/2 {
			return 900 * mibTest
		}
		return 300 * mibTest
	}))
	got := memoryRequest(t, overMemory(), hist)
	if want := RoundResource(900*mibTest*requestOverMaxMemory, false); got != want {
		t.Errorf("suggested %s, want %s: the 900Mi peak plus 15 %%", FormatMemory(got), FormatMemory(want))
	}
	if got < 900*mibTest {
		t.Errorf("suggested %s, under the 900Mi the container reached", FormatMemory(got))
	}
}

// Whatever the series, the request recommended is at or above the figure it
// was sized on: the P95 for CPU, the maximum for memory.
func TestARecommendationIsNeverUnderWhatWasObserved(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for round := 0; round < 50; round++ {
		base, jitter := 50+rng.Float64()*400, rng.Float64()*300
		cpu := window(week, func(int) float64 { return base + rng.Float64()*jitter })
		mem := window(week, func(int) float64 { return (100 + rng.Float64()*400) * mibTest })
		hist := []ContainerHistory{{Pod: "pod-1", Container: "app", CPU: cpu, Memory: mem}}
		c := ctr("app", opts{cpuReq: 4000, memReq: 8192 * mibTest, memLim: 32768 * mibTest, cpuUse: 100, memUse: 100 * mibTest})

		cpuValues, memValues := make([]float64, len(cpu)), make([]float64, len(mem))
		for i := range cpu {
			cpuValues[i], memValues[i] = cpu[i].V, mem[i].V
		}
		for _, s := range Compute([]Workload{deployment("api", c)}, hist, nil) {
			if !strings.Contains(s.Action, "request") || s.SuggestedRaw == 0 {
				continue
			}
			floor := maxOf(memValues)
			if strings.HasPrefix(s.Resource, "CPU") {
				floor = percentile95(cpuValues)
			}
			if float64(s.SuggestedRaw) < floor {
				t.Fatalf("round %d: %s %s to %s, under the %.0f observed", round, s.Resource, s.Action, s.Suggested, floor)
			}
		}
	}
}

func TestUnderADayOfSeriesNamesNoValue(t *testing.T) {
	// 23 hours at the core's step: a request sized on it has not seen one
	// whole day.
	hist := cpuOnly(window(samplesIn(23*time.Hour), steady(300)))
	s := reduceRequest(t, "CPU", overCPU(), hist)
	if !s.InsufficientData {
		t.Error("23 hours of series is not marked insufficient")
	}
	if s.SuggestedRaw != 0 || s.Suggested != "" {
		t.Errorf("a value is still recommended from 23 hours: %q (%d)", s.Suggested, s.SuggestedRaw)
	}
	if cmd, ok := ToKubectlCmd(s); ok {
		t.Errorf("a command is offered with no value: %q", cmd)
	}
	if !hasWarning(s, "Insufficient data") {
		t.Errorf("the row does not say why it has no value: %q", s.Warnings)
	}

	// A day and a step is enough: the boundary is the day.
	if s := reduceRequest(t, "CPU", overCPU(), cpuOnly(window(samplesIn(24*time.Hour), steady(300)))); s.InsufficientData {
		t.Error("a full day of series is marked insufficient")
	}
}

func TestASparseSeriesNamesNoValue(t *testing.T) {
	// Five samples every two hours, for a week. Each burst follows a gap,
	// read as a restart, so its first two are left out as a warm-up: about
	// 250 usable samples, more than two days' worth, scattered over seven
	// days - about a third of the time.
	var points []Point
	for at := 0.0; at < 7*24*3600; at += 2 * 3600 {
		for k := 0; k < 5; k++ {
			points = append(points, Point{T: t0 + at + float64(k*stepEvery), V: 300})
		}
	}
	s := reduceRequest(t, "CPU", overCPU(), cpuOnly(points))
	if !s.InsufficientData || s.SuggestedRaw != 0 {
		t.Fatalf("a series covering a third of its week gave %q (insufficient %v)", s.Suggested, s.InsufficientData)
	}
	if !hasWarning(s, "cover") {
		t.Errorf("the reason is not the coverage: %q", s.Warnings)
	}
}

func TestAStartSpikeDoesNotMoveTheFigure(t *testing.T) {
	// A week at 300Mi; the container restarted on day four, and its first
	// samples after the start are a 2Gi warm-up.
	restart := week * 4 / 7
	spiked := window(week, func(i int) float64 {
		if i == restart || i == restart+1 {
			return 2048 * mibTest
		}
		return 300 * mibTest
	})
	flat := window(week, steady(300*mibTest))
	started := overMemory()
	started.Starts = []float64{spiked[restart].T - 60}

	want := memoryRequest(t, overMemory(), memOnly(flat))
	if got := memoryRequest(t, started, memOnly(spiked)); got != want {
		t.Errorf("suggested %s, want %s - the start's warm-up moved the request", FormatMemory(got), FormatMemory(want))
	}

	t.Run("and a limit is still never sized under it", func(t *testing.T) {
		c := started
		c.MemLimit = 0
		s, ok := find(Compute([]Workload{deployment("api", c)}, memOnly(spiked), nil),
			byResourceAction("Memory — no limit", "Set limit"))
		if !ok {
			t.Fatal("no 'Set limit' produced")
		}
		if s.SuggestedRaw < 2048*mibTest {
			t.Errorf("limit %s, under the 2Gi the container reached starting", s.Suggested)
		}
	})

	t.Run("a restart the status no longer shows is the gap it left", func(t *testing.T) {
		var gapped []Point
		for i, p := range spiked {
			if i < restart-3 || i >= restart {
				gapped = append(gapped, p)
			}
		}
		if got := memoryRequest(t, overMemory(), memOnly(gapped)); got != want {
			t.Errorf("suggested %s after a gap, want %s", FormatMemory(got), FormatMemory(want))
		}
	})
}

func TestACrashLoopingReplicaDoesNotMoveTheFigure(t *testing.T) {
	// Two replicas asking 100m. pod-1 works at 300m all week; pod-2 is in
	// CrashLoopBackOff and its window is a dying process spinning at 1500m.
	healthy := ctr("app", opts{cpuReq: 100, cpuUse: 300, memUse: 1})
	looping := ctr("app", opts{cpuReq: 100, cpuUse: 1500, memUse: 1, restarts: 1, crashLooping: true})
	hist := []ContainerHistory{
		{Pod: "pod-1", Container: "app", CPU: window(week, steady(300))},
		{Pod: "pod-2", Container: "app", CPU: window(week, steady(1500))},
	}
	increase := func(pods []Pod, hist []ContainerHistory) Suggestion {
		t.Helper()
		s, ok := find(Compute([]Workload{workloadOf("api", pods, "", "")}, hist, nil),
			byResourceAction("CPU", "Increase request"))
		if !ok {
			t.Fatal("no CPU 'Increase request' produced")
		}
		return s
	}
	alone := increase([]Pod{testPod("pod-1", healthy)}, hist[:1])
	got := increase([]Pod{testPod("pod-1", healthy), testPod("pod-2", looping)}, hist)
	if got.SuggestedRaw != alone.SuggestedRaw {
		t.Errorf("suggested %s, want %s - the crash-looping replica moved the request", got.Suggested, alone.Suggested)
	}
	if !hasWarning(got, "crash-looping replica") {
		t.Errorf("the row does not say a replica was left out: %q", got.Warnings)
	}

	// The same replica, not looping, does move it: the test can see a move.
	working := looping
	working.CrashLooping = false
	if s := increase([]Pod{testPod("pod-1", healthy), testPod("pod-2", working)}, hist); s.SuggestedRaw == alone.SuggestedRaw {
		t.Error("a busy replica that is not looping did not move the request either; the test proves nothing")
	}

	t.Run("every replica looping names no value", func(t *testing.T) {
		c := ctr("app", opts{cpuReq: 100, cpuUse: 1500, memUse: 1, restarts: 1, crashLooping: true})
		for _, s := range Compute([]Workload{deployment("api", c)}, hist[1:2], nil) {
			if strings.HasPrefix(s.Resource, "CPU") && s.SuggestedRaw != 0 {
				t.Errorf("%s %s to %s, sized on a crash loop", s.Resource, s.Action, s.Suggested)
			}
		}
	})
}

func TestConfidenceFollowsTheWindowAndTheSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		hist []ContainerHistory
		want Confidence
	}{
		{"a full week of Prometheus", cpuOnly(window(week, steady(300))), ConfidenceHigh},
		{"the week, less a scrape or two", cpuOnly(window(week, steady(300))[2:]), ConfidenceHigh},
		{"three days of Prometheus", cpuOnly(window(samplesIn(72*time.Hour), steady(300))), ConfidenceMedium},
		{"six days of Prometheus", cpuOnly(window(samplesIn(144*time.Hour), steady(300))), ConfidenceMedium},
		{"one metrics-server scrape", nil, ConfidenceLow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := reduceRequest(t, "CPU", overCPU(), tc.hist)
			if s.Confidence != tc.want {
				t.Errorf("confidence %q, want %q", s.Confidence, tc.want)
			}
			if s.InsufficientData || s.SuggestedRaw == 0 {
				t.Errorf("no value from %s", tc.name)
			}
		})
	}
}

// The OOM kill's raise is computed from the limit the container was killed
// at, so too little usage does not withhold it: the alarm keeps its number.
func TestAnOOMRaiseKeepsItsValueUnderADayOfSeries(t *testing.T) {
	c := ctr("app", opts{memReq: 256 * mibTest, memLim: 512 * mibTest, memUse: 500 * mibTest, cpuUse: 1, oomKilled: true})
	s, ok := find(Compute([]Workload{deployment("api", c)}, memOnly(window(samplesIn(6*time.Hour), steady(500*mibTest))), nil),
		byResourceAction("Memory", "Increase limit"))
	if !ok {
		t.Fatal("no OOM 'Increase limit' produced")
	}
	if s.SuggestedRaw <= 512*mibTest {
		t.Errorf("the OOM raise lost its value under a short window: %q", s.Suggested)
	}
	if !s.InsufficientData {
		t.Error("the row does not say the usage behind it is short")
	}
}

package rightsizing

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// These cases are not in rightsizing_test.go. They were found by measuring
// the package's coverage once that file passed: four behaviors had no test
// behind them at all, and one of them — the trend prediction — is the branch
// most likely to be wrong, because it is the only one that does arithmetic
// nobody can check by eye.
//
// They are kept in their own file so rightsizing_test.go stays the base
// specification: what is there is the baseline, and what is here is
// what it did not say.

const gib = 1024 * mibTest

// --- the trend branch ------------------------------------------------------

// rising builds n samples, spaced `every` seconds, climbing by `step` from
// `from`. A linear ramp is exactly what secondsToThreshold claims to model, so
// the expected answer can be worked out on paper and compared.
// rising is a memory series in MiB: n samples `every` seconds apart, from
// `from` climbing `step` each. Memory, because the trend is a memory rule
// as a rule.
func rising(n int, every, from, step float64) []ContainerHistory {
	return []ContainerHistory{{Pod: "pod-1", Container: "app", Memory: risingPoints(n, every, from*mibTest, step*mibTest)}}
}

func risingPoints(n int, every, from, step float64) []Point {
	out := make([]Point, n)
	for i := range out {
		out[i] = Point{T: float64(i) * every, V: from + float64(i)*step}
	}
	return out
}

func TestComputeTrend(t *testing.T) {
	// Sixty samples a minute apart climbing 6Mi each: P95 lands at 436Mi of a
	// 1000Mi limit — 44 %, well under the 70 % that would raise the P95 warning —
	// while the slope crosses the limit 91 minutes after the last sample.
	// Nothing but the trend can see this one.
	t.Run("flags a limit the trend will cross within a day", func(t *testing.T) {
		c := ctr("app", opts{memReq: 500 * mibTest, memLim: 1000 * mibTest, memUse: 100 * mibTest, cpuUse: 1})
		s, ok := find(Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, rising(60, 60, 100, 6), nil),
			func(s Suggestion) bool { return strings.Contains(s.Message, "trending to exceed limit") })
		if !ok {
			t.Fatal("no trend suggestion produced")
		}
		if s.Action != "Increase limit" {
			t.Errorf("action = %q, want %q", s.Action, "Increase limit")
		}
		if s.Kind != KindDanger {
			t.Errorf("kind = %q, want %q — under four hours is a danger", s.Kind, KindDanger)
		}
		if !strings.Contains(s.Message, "1.5h") {
			t.Errorf("message = %q, want the ~1.5h estimate", s.Message)
		}
		if !strings.Contains(s.Message, "linear trend") {
			t.Errorf("message = %q, want it to admit the estimate is a linear fit", s.Message)
		}
	})

	// Under an hour the estimate is written in minutes: "~0.1h" is a number
	// nobody can act on.
	t.Run("writes an imminent crossing in minutes", func(t *testing.T) {
		c := ctr("app", opts{memReq: 1000 * mibTest, memLim: 1000 * mibTest, memUse: 100 * mibTest, cpuUse: 1})
		s, ok := find(Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, rising(20, 10, 400, 10), nil),
			func(s Suggestion) bool { return strings.Contains(s.Message, "trending to exceed limit") })
		if !ok {
			t.Fatal("no trend suggestion produced")
		}
		if !strings.Contains(s.Message, "~7m") {
			t.Errorf("message = %q, want the estimate in minutes", s.Message)
		}
	})

	// A container whose usage is falling is not about to exceed anything, and
	// predicting backwards is how a linear fit produces nonsense.
	// CPU follows traffic, and a morning ramp regressed
	// over two hours is not a leak; a CPU limit is a throttle besides. The
	// same climb that flags memory says nothing on CPU.
	t.Run("says nothing about a rising CPU series", func(t *testing.T) {
		c := ctr("app", opts{cpuReq: 500, cpuLim: 1000, cpuUse: 100, memUse: 1})
		hist := []ContainerHistory{{Pod: "pod-1", Container: "app", CPU: risingPoints(60, 60, 100, 6)}}
		for _, s := range Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, hist, nil) {
			if strings.Contains(s.Message, "trending") {
				t.Fatalf("predicted a CPU crossing: %q", s.Message)
			}
		}
	})

	t.Run("says nothing about a falling series", func(t *testing.T) {
		c := ctr("app", opts{memReq: 500 * mibTest, memLim: 1000 * mibTest, memUse: 100 * mibTest, cpuUse: 1})
		for _, s := range Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, rising(60, 60, 450, -6), nil) {
			if strings.Contains(s.Message, "trending") {
				t.Fatalf("predicted a crossing on a falling series: %q", s.Message)
			}
		}
	})

	// Four points is not a trend. The floor exists so a container two scrapes
	// old cannot produce a confident prediction.
	t.Run("says nothing on too few samples", func(t *testing.T) {
		if _, ok := secondsToThreshold([]Point{{T: 0, V: 1}, {T: 1, V: 2}, {T: 2, V: 3}, {T: 3, V: 4}}, 100); ok {
			t.Fatal("regressed a four-point series")
		}
	})

	t.Run("says nothing when every sample shares a timestamp", func(t *testing.T) {
		var flat []Point
		for range 6 {
			flat = append(flat, Point{T: 42, V: 1})
		}
		if _, ok := secondsToThreshold(flat, 100); ok {
			t.Fatal("regressed a series with no time span")
		}
	})
}

// --- ephemeral storage -----------------------------------------------------

// ephemeralPod is a container with no CPU or memory usage reported, so only the
// ephemeral analysis runs and its findings are the whole result.
func ephemeralPod(use, limit int64) []Pod {
	return []Pod{{Name: "pod-1", Containers: []Container{{
		Name: "app", EphemeralUsage: use, EphemeralLimit: limit,
	}}}}
}

func TestComputeEphemeral(t *testing.T) {
	t.Run("flags a missing ephemeral limit on a container writing a gigabyte", func(t *testing.T) {
		got := Compute([]Workload{workloadOf("app", ephemeralPod(1500*mibTest, 0), "", "")}, nil, nil)
		s, ok := find(got, func(s Suggestion) bool { return s.Resource == "Ephemeral — no limit" })
		if !ok {
			t.Fatalf("no finding produced: %+v", got)
		}
		if s.Kind != KindWarning || s.Action != "Set limit" || s.Current != "unlimited" {
			t.Errorf("got %q / %q / %q", s.Kind, s.Action, s.Current)
		}
	})

	t.Run("danger at 90% of the ephemeral limit", func(t *testing.T) {
		got := Compute([]Workload{workloadOf("app", ephemeralPod(950*mibTest, 1000*mibTest), "", "")}, nil, nil)
		s, ok := find(got, func(s Suggestion) bool { return s.Resource == "Ephemeral" })
		if !ok {
			t.Fatalf("no finding produced: %+v", got)
		}
		if s.Kind != KindDanger {
			t.Errorf("kind = %q, want %q", s.Kind, KindDanger)
		}
		if !strings.Contains(s.Message, "95% of limit") {
			t.Errorf("message = %q", s.Message)
		}
	})

	t.Run("warning between 70 and 89%", func(t *testing.T) {
		got := Compute([]Workload{workloadOf("app", ephemeralPod(800*mibTest, 1000*mibTest), "", "")}, nil, nil)
		s, ok := find(got, func(s Suggestion) bool { return s.Resource == "Ephemeral" })
		if !ok {
			t.Fatalf("no finding produced: %+v", got)
		}
		if s.Kind != KindWarning {
			t.Errorf("kind = %q, want %q", s.Kind, KindWarning)
		}
	})

	t.Run("silent below 70%", func(t *testing.T) {
		got := Compute([]Workload{workloadOf("app", ephemeralPod(500*mibTest, 1000*mibTest), "", "")}, nil, nil)
		if len(got) != 0 {
			t.Fatalf("len = %d, want 0: %+v", len(got), got)
		}
	})

	// This used to be a warning on every container that wrote a
	// log line. Most workloads set no ephemeral limit, and the kubelet evicts
	// on node disk pressure; a few hundred MiB of logs is not a finding.
	t.Run("silent about a missing limit under a gigabyte", func(t *testing.T) {
		got := Compute([]Workload{workloadOf("app", ephemeralPod(500*mibTest, 0), "", "")}, nil, nil)
		if len(got) != 0 {
			t.Fatalf("len = %d, want 0: %+v", len(got), got)
		}
	})

	// The clusters that refuse nodes/proxy report nothing here, and nothing is
	// what must come out — not a container sized against a zero.
	t.Run("silent when the kubelet reported nothing", func(t *testing.T) {
		got := Compute([]Workload{workloadOf("app", ephemeralPod(0, 1000*mibTest), "", "")}, nil, nil)
		if len(got) != 0 {
			t.Fatalf("len = %d, want 0: %+v", len(got), got)
		}
	})

	t.Run("renders an ephemeral-storage command", func(t *testing.T) {
		got := Compute([]Workload{workloadOf("app", ephemeralPod(1500*mibTest, 0), "", "")}, nil, nil)
		s, _ := find(got, func(s Suggestion) bool { return strings.HasPrefix(s.Resource, "Ephemeral") })
		cmd, ok := ToKubectlCmd(s)
		if !ok {
			t.Fatal("no command rendered")
		}
		if !strings.Contains(cmd, "--limits=ephemeral-storage=3Gi") {
			t.Errorf("cmd = %q", cmd)
		}
	})
}

// --- the node-fit suffix ---------------------------------------------------

// Every test in rightsizing_test.go runs against a single node, where the
// suffix is deliberately suppressed. On a heterogeneous fleet it is the line
// that says an increase will restrict where the pod can be scheduled — which is
// a consequence an operator would rather read before applying it than after.
func TestNodeFitSuffix(t *testing.T) {
	t.Run("says how many nodes still fit", func(t *testing.T) {
		c := ctr("app", opts{cpuReq: 3500, cpuLim: 3800, cpuUse: 3610, memUse: 1})
		nodes := []Node{testNode(4000, 8*gib, true), testNode(8000, 16*gib, true)}
		s, ok := find(Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, nil, nodes),
			byResourceAction("CPU", "Increase limit"))
		if !ok {
			t.Fatal("no CPU limit increase produced")
		}
		if !strings.Contains(s.Message, "fits on 1/2 nodes") {
			t.Errorf("message = %q, want the node-fit suffix", s.Message)
		}
	})

	t.Run("stays silent when every node fits", func(t *testing.T) {
		c := ctr("app", opts{cpuReq: 3500, cpuLim: 3800, cpuUse: 3610, memUse: 1})
		nodes := []Node{testNode(16000, 32*gib, true), testNode(16000, 32*gib, true)}
		s, ok := find(Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, nil, nodes),
			byResourceAction("CPU", "Increase limit"))
		if !ok {
			t.Fatal("no CPU limit increase produced")
		}
		if strings.Contains(s.Message, "fits on") {
			t.Errorf("message = %q, want no suffix when nothing is restricted", s.Message)
		}
	})
}

// --- quantity formatting ---------------------------------------------------

// "1.50 GiB" is how a value is read and "1536Mi" is how it is typed. The base
// suite only ever checked CPU commands, where the two happen to look alike.
func TestMemoryQuantityFormatting(t *testing.T) {
	t.Run("renders a whole number of gibibytes as Gi", func(t *testing.T) {
		// 1 400Mi of a 1 500Mi limit, one scrape: 2 × 1 400Mi → the 3Gi rung.
		c := ctr("app", opts{memReq: 1400 * mibTest, memLim: 1500 * mibTest, memUse: 1400 * mibTest, cpuUse: 1})
		s, ok := find(Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, nil, nil),
			byResourceKind("Memory", KindDanger))
		if !ok {
			t.Fatal("no memory danger produced")
		}
		cmd, ok := ToKubectlCmd(s)
		if !ok {
			t.Fatal("no command rendered")
		}
		if !strings.Contains(cmd, "--limits=memory=3Gi") {
			t.Errorf("cmd = %q, want memory=3Gi", cmd)
		}
	})

	t.Run("renders anything else as Mi", func(t *testing.T) {
		if got := fmtKubectl(1536*mibTest, false); got != "1536Mi" {
			t.Errorf("fmtKubectl = %q, want %q", got, "1536Mi")
		}
	})
}

// --- rounding at the edges -------------------------------------------------

func TestRoundResource(t *testing.T) {
	cases := []struct {
		name  string
		raw   float64
		isCPU bool
		want  int64
	}{
		{"zero stays zero", 0, true, 0},
		{"negative stays zero", -5, true, 0},
		{"CPU under a core rounds up to 50m", 1, true, 50},
		{"CPU on a step is left alone", 500, true, 500},
		{"CPU over a core rounds up to 250m", 1001, true, 1250},
		{"memory below the ladder takes its first rung", 1, false, 64 * mibTest},
		{"memory lands on the ladder", 1000 * mibTest, false, 1024 * mibTest},
		// Past 32 GiB there are no more rungs, so it climbs in blocks of the last
		// one rather than inventing values nobody has seen on a node.
		{"memory past the ladder climbs in 32Gi blocks", 33 * gib, false, 64 * gib},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RoundResource(tc.raw, tc.isCPU); got != tc.want {
				t.Errorf("RoundResource(%v, %v) = %d, want %d", tc.raw, tc.isCPU, got, tc.want)
			}
		})
	}
}

// --- the limit must clear the request underneath it -------------------------

// A reduced limit has to come out above the request the container will actually
// have, because Kubernetes refuses the pod otherwise:
//
//	The Deployment "api" is invalid: spec.template.spec.containers[0].resources.
//	requests: Invalid value: "900m": must be less than or equal to cpu limit
//
// The coherence pass at the end of analyzeCPUMem used to compare a reduced limit
// against a reduced *request*, so it only ever saw the case where this pass
// produced both. The reachable case produces only one: the limit is reduced
// alone and the request already in the spec is above the new limit — no second
// suggestion exists to compare with, so nothing was compared.
//
// Its own fixture walked into it: the case named "overkill when the limit is
// over-provisioned" beside this file used a 200m request against a 3 000m limit
// at 100m usage, whose reduced limit was 150m — under its own request, and
// asserted only to exist.
//
// Every limit the engine proposes - reduced, set where there was none, or
// raised beside a request - is held to the request the container will have, and
// a CPU limit is never reduced at all.
func TestAReducedLimitClearsTheRequestUnderneathIt(t *testing.T) {
	t.Run("a new memory limit is never under the request that stays", func(t *testing.T) {
		// Killed for memory last month: no memory reduction, so the 1 000Mi
		// request stays. The limit's own target is 2 × 300Mi → 768Mi, under it;
		// the API server would refuse `--limits=memory=768Mi` beside a 1 000Mi
		// request. It used to be offered.
		c := ctr("c", opts{memReq: 1000 * mibTest, memUse: 300 * mibTest, cpuUse: 1})
		c.OOMKilledEarlier = true
		s, ok := find(Compute([]Workload{deployment("app", c)}, nil, nil), func(s Suggestion) bool {
			return s.Resource == "Memory — no limit"
		})
		if !ok {
			t.Fatal("no 'Memory — no limit' produced")
		}
		if s.SuggestedRaw < 1000*mibTest {
			t.Errorf("suggested limit = %d bytes under a 1000Mi request; the API server refuses that patch", s.SuggestedRaw)
		}
	})

	t.Run("memory: a limit reduced under the current request carries the request", func(t *testing.T) {
		// 330Mi used of a 900Mi request and a 4Gi limit, with a window: the
		// request goes to 1.15 × 330 → 384Mi and the limit to 512Mi - under
		// the 900Mi request the container has now, so the limit's command alone
		// would be refused. One command carries both.
		dep := deployment("c", ctr("c", opts{memReq: 900 * mibTest, memLim: 4096 * mibTest, memUse: 330 * mibTest, cpuUse: 1}))
		out := Compute([]Workload{dep}, windowOf("c", 330*mibTest), nil)
		lim, ok := find(out, byResourceAction("Memory", "Reduce limit"))
		if !ok {
			t.Fatalf("no limit reduction: %+v", out)
		}
		if lim.PairedRequestRaw == 0 || lim.SuggestedRaw < lim.PairedRequestRaw {
			t.Errorf("limit %d, paired request %d", lim.SuggestedRaw, lim.PairedRequestRaw)
		}
		if _, ok := find(out, byResourceAction("Memory", "Reduce request")); ok {
			t.Error("the request reduction is still a row of its own beside the command that carries it")
		}
		cmd, _ := ToKubectlCmd(lim)
		if !strings.Contains(cmd, "--requests=memory=384Mi --limits=memory=512Mi") {
			t.Errorf("cmd = %q", cmd)
		}
	})

	t.Run("the row is dropped when no reduction worth a rollout is left", func(t *testing.T) {
		// A 1 100Mi limit at 3.3 × its 330Mi P95 calls the limit generous, but
		// the request underneath stays at 900Mi (an HPA scales on memory, so it
		// is not reduced), and a limit at or above it is not 30 % below 1 100Mi.
		w := deployment("c", ctr("c", opts{memReq: 900 * mibTest, memLim: 1100 * mibTest, memUse: 330 * mibTest, cpuUse: 1}))
		w.Autoscalers = []Autoscaler{{Kind: "HorizontalPodAutoscaler", Name: "c", Utilization: map[string]int32{"memory": 60}}}
		out := Compute([]Workload{w}, windowOf("c", 330*mibTest), nil)
		if s, ok := find(out, byResourceAction("Memory", "Reduce request")); ok {
			t.Fatalf("a memory request reduction under an HPA on memory: %+v", s)
		}
		if s, ok := find(out, byResourceAction("Memory", "Reduce limit")); ok {
			t.Errorf("a 'Reduce limit' survived at %d bytes, against a 900Mi request and a 1 100Mi limit", s.SuggestedRaw)
		}
	})
}

// --- the bar and the row are about the same container ------------------------

// ResourceReading.Status says it exists "so the bar and the suggestion cannot
// disagree about what color this is". It did disagree, because it took one
// statistic and the engine takes two: the limit is judged against P95 and the
// request against the mean.
//
// The bar and the suggestion are judged by the same rule, on the same figure.
func TestTheBarAndTheSuggestionAgreeAboutTheSameContainer(t *testing.T) {
	t.Run("the limit is judged on the P95, as the finding is", func(t *testing.T) {
		// Memory, so a limit reached is a kill. The mean is 400Mi; the P95, which
		// is what the "Increase limit" row reads, is 950Mi of a 1 000Mi limit.
		r := ResourceReading{Mean: 400 * mibTest, P95: 950 * mibTest, Peak: 960 * mibTest,
			Request: 500 * mibTest, Limit: 1000 * mibTest, Measured: true, Windowed: true}
		if got := ResourceStatus(r, false); got != StatusDanger {
			t.Errorf("status = %q, want %q — the P95 is at 95%% of the limit", got, StatusDanger)
		}
	})

	t.Run("an oversized request is judged on its own target", func(t *testing.T) {
		// A P95 a fifth of the request: the target (1.2 × P95, rounded) saves far
		// more than 30 %, which is the line the reduction is offered at.
		r := ResourceReading{Mean: 100, P95: 200, Peak: 220, Request: 1000, Measured: true, Windowed: true}
		if got := ResourceStatus(r, true); got != StatusOverkill {
			t.Errorf("status = %q, want %q", got, StatusOverkill)
		}
	})

	t.Run("a container the drawer would call danger is danger on the bar too", func(t *testing.T) {
		// End to end rather than on the function: three replicas, one hot, and the
		// engine's row and the reading's color have to be the same verdict.
		pods := replicasOf(3, ctr("app", opts{memReq: 500 * mibTest, memLim: 1000 * mibTest, memUse: 100 * mibTest, cpuUse: 1}))
		pods[2].Containers[0].MemUsage = 950 * mibTest
		w := workloadOf("api", pods, "Deployment", "")

		readings := Readings([]Workload{w}, nil)
		if len(readings) != 1 || len(readings[0].Containers) != 1 {
			t.Fatalf("wanted one container reading, got %+v", readings)
		}
		mem := readings[0].Containers[0].Memory
		if mem.Status != StatusDanger {
			t.Errorf("bar status = %q, want %q", mem.Status, StatusDanger)
		}
		if _, ok := find(Compute([]Workload{w}, nil, nil), byResourceKind("Memory", KindDanger)); !ok {
			t.Error("the engine produced no memory danger, so this no longer tests agreement")
		}
	})

	t.Run("a request too low is colored, where it was green", func(t *testing.T) {
		pods := replicasOf(2, ctr("app", opts{memReq: 100 * mibTest, memUse: 250 * mibTest, cpuUse: 1}))
		w := workloadOf("api", pods, "Deployment", "")
		mem := Readings([]Workload{w}, nil)[0].Containers[0].Memory
		row, ok := find(Compute([]Workload{w}, nil, nil), byResourceAction("Memory", "Increase request"))
		if !ok {
			t.Fatal("no memory request increase")
		}
		if string(row.Kind) != string(mem.Status) {
			t.Errorf("row is %q, bar is %q", row.Kind, mem.Status)
		}
	})
}

// --- a measured zero is a measurement ---------------------------------------

// The engine dropped a snapshot of exactly zero, which emptied the sample, which
// made usageStats report that nothing had measured the container — so an idle
// container with a large request drew a bar saying "not measured" while
// metrics-server had reported it.
func TestAContainerMeasuredAtZeroIsMeasured(t *testing.T) {
	// HasUsage is set by a non-zero figure in the helper, so this is built by
	// hand: CPU at exactly zero, memory reported, HasUsage true. That is what
	// metrics-server returns for a container that used no CPU in the window.
	c := Container{Name: "app", CPURequest: 2000, HasUsage: true, CPUUsage: 0, MemUsage: 1}
	w := workloadOf("idle", []Pod{testPod("pod-1", c)}, "Deployment", "")

	readings := Readings([]Workload{w}, nil)
	if len(readings) != 1 || len(readings[0].Containers) != 1 {
		t.Fatalf("wanted one container reading, got %+v", readings)
	}
	cpu := readings[0].Containers[0].CPU

	if !cpu.Measured {
		t.Error("Measured is false for a container the metrics API reported at zero; " +
			"the bar then reads as 'nothing told us', which is a different answer")
	}
	if cpu.Status != StatusOverkill {
		t.Errorf("status = %q, want %q — nothing of a 2 000m request is the clearest overkill there is",
			cpu.Status, StatusOverkill)
	}
	// And the engine stays quiet, because 1.3 × 0 rounds to nothing and "reduce
	// your request to zero" is not advice. The bar carries the finding; the
	// suggestion list does not invent a target.
	if s, ok := find(Compute([]Workload{w}, nil, nil), byResourceAction("CPU", "Reduce request")); ok {
		t.Errorf("the engine suggested reducing to %q, which is a request nobody can type", s.Suggested)
	}
}

// --- a restart history is not a crash loop -----------------------------------

// `ContainerStatus.RestartCount` is cumulative for the life of the pod and never
// decays, and the engine read `>= 3` as "crash-looping". So a StatefulSet pod up
// since February with three restarts from node maintenance was crash-looping for
// ever: every reduction suppressed, CPU and memory, request and limit, with the
// only disclosure a "3 restart(s)" pill that did not say a suppression was in
// effect.
//
// A restart count is history; the kubelet's backoff state is the crash loop.
func TestARestartHistoryIsNotACrashLoop(t *testing.T) {
	t.Run("an old restart count does not suppress advice", func(t *testing.T) {
		// db-0: 4 cores requested, using 90m, three restarts months ago and not in
		// backoff now. Four cores is what this page exists to find.
		c := ctr("app", opts{cpuReq: 4000, cpuLim: 4000, cpuUse: 90, memUse: 1, restarts: 3})
		got := Compute([]Workload{workloadOf("db", replicasOf(1, c), "StatefulSet", "")}, nil, nil)
		reduced := false
		for _, s := range got {
			if strings.HasPrefix(s.Action, "Reduce") {
				reduced = true
			}
		}
		if !reduced {
			t.Fatalf("no reduction suggested for a container using 90m of 4 cores; got %+v", got)
		}
	})

	t.Run("the kubelet verdict still suppresses", func(t *testing.T) {
		c := ctr("app", opts{cpuReq: 4000, cpuLim: 4000, cpuUse: 90, memUse: 1, restarts: 3, crashLooping: true})
		for _, s := range Compute([]Workload{workloadOf("db", replicasOf(1, c), "StatefulSet", "")}, nil, nil) {
			if strings.HasPrefix(s.Action, "Reduce") {
				t.Fatalf("emitted a reduction on a container the kubelet says is in CrashLoopBackOff: %+v", s)
			}
		}
	})

	t.Run("a loop with fewer than three restarts is not suppressed", func(t *testing.T) {
		// Both halves are required, and this is the half the other case cannot
		// reach: a container that has restarted once and is in backoff is not yet
		// the pathology the guard is for.
		c := ctr("app", opts{cpuReq: 4000, cpuLim: 4000, cpuUse: 90, memUse: 1, restarts: 1, crashLooping: true})
		got := Compute([]Workload{workloadOf("db", replicasOf(1, c), "StatefulSet", "")}, nil, nil)
		reduced := false
		for _, s := range got {
			if strings.HasPrefix(s.Action, "Reduce") {
				reduced = true
			}
		}
		if !reduced {
			t.Errorf("no reduction for a container with one restart; got %+v", got)
		}
	})

	t.Run("the suppression says it is a suppression", func(t *testing.T) {
		// The caveat used to read "3 restarts observed" beside a page with no
		// advice on it, which does not tell the operator the second is caused by
		// the first. They then go looking for advice that was deliberately
		// withheld.
		c := ctr("app", opts{cpuReq: 4000, cpuLim: 8000, cpuUse: 90, memUse: 1, restarts: 3, crashLooping: true})
		got := Compute([]Workload{workloadOf("db", replicasOf(1, c), "StatefulSet", "")}, nil, nil)
		if len(got) == 0 {
			t.Fatal("nothing at all was produced, so there is no caveat to carry the reason")
		}
		said := false
		for _, s := range got {
			for _, w := range s.Warnings {
				if strings.Contains(w, "crash-looping") && strings.Contains(w, "no reduction") {
					said = true
				}
			}
		}
		if !said {
			t.Errorf("no caveat says why nothing was suggested; warnings were %+v", got)
		}
	})
}

// TestTheCrashLoopStateIsReadFromTheKubelet covers the extraction, which is in
// collect.go and had never been executed by a test — the same gap that leaves
// the OOMKilled reading, the package's headline refusal, uncovered.
func TestTheCrashLoopStateIsReadFromTheKubelet(t *testing.T) {
	at := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		status corev1.ContainerStatus
		want   bool
	}{
		{
			name: "waiting in CrashLoopBackOff",
			status: corev1.ContainerStatus{Name: "app", RestartCount: 5,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
			want: true,
		},
		{
			name: "running, but it died ninety seconds ago",
			// The gap CrashLoopBackOff leaves: between restarts the container is
			// Running, and its RSS then is exactly the sample not to believe.
			status: corev1.ContainerStatus{Name: "app", RestartCount: 5,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					Reason: "Error", FinishedAt: metav1.NewTime(at.Add(-90 * time.Second))}}},
			want: true,
		},
		{
			name: "restarted in February and running ever since",
			status: corev1.ContainerStatus{Name: "app", RestartCount: 3,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					Reason: "Error", FinishedAt: metav1.NewTime(at.Add(-180 * 24 * time.Hour))}}},
			want: false,
		},
		{
			name: "waiting for an image, which is not a loop",
			status: corev1.ContainerStatus{Name: "app",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}},
			want: false,
		},
		{
			name:   "no status at all",
			status: corev1.ContainerStatus{Name: "app"},
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "pod-1"},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
				Status:     corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{tc.status}},
			}
			got := toPod(pod, map[string]Usage{}, at)
			if len(got.Containers) != 1 {
				t.Fatalf("got %d containers, want 1", len(got.Containers))
			}
			if got.Containers[0].CrashLooping != tc.want {
				t.Errorf("CrashLooping = %v, want %v", got.Containers[0].CrashLooping, tc.want)
			}
		})
	}
}

// TestTheOOMFlagIsReadFromBothTerminationStates covers the extraction that feeds
// the package's headline refusal and that no test had ever run.
//
// lastState is the one that matters and is the easy one to leave out: a
// container killed for memory and restarted reports the kill there, and its
// current state is Running.
func TestTheOOMFlagIsReadFromBothTerminationStates(t *testing.T) {
	at := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	long := metav1.NewTime(at.Add(-90 * 24 * time.Hour))

	cases := []struct {
		name        string
		status      corev1.ContainerStatus
		want        bool
		wantEarlier bool
	}{
		{
			name: "killed and not yet restarted",
			status: corev1.ContainerStatus{Name: "app",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}}},
			want: true,
		},
		{
			name: "killed, restarted, running now",
			status: corev1.ContainerStatus{Name: "app", RestartCount: 1,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					Reason: "OOMKilled", FinishedAt: metav1.NewTime(at.Add(-2 * time.Hour))}}},
			want: true,
		},
		{
			// The previous termination stays on the status
			// until the next one. Ninety days ago is not "now": no alarm, and
			// the quieter flag that still blocks memory reductions.
			name: "killed ninety days ago, fine since",
			status: corev1.ContainerStatus{Name: "app", RestartCount: 1,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					Reason: "OOMKilled", FinishedAt: long}}},
			want:        false,
			wantEarlier: true,
		},
		{
			name: "terminated for another reason entirely",
			status: corev1.ContainerStatus{Name: "app", RestartCount: 1,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					Reason: "Error", FinishedAt: long}}},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "pod-1"},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
				Status:     corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{tc.status}},
			}
			got := toPod(pod, map[string]Usage{}, at).Containers[0]
			if got.OOMKilled != tc.want {
				t.Errorf("OOMKilled = %v, want %v", got.OOMKilled, tc.want)
			}
			if got.OOMKilledEarlier != tc.wantEarlier {
				t.Errorf("OOMKilledEarlier = %v, want %v", got.OOMKilledEarlier, tc.wantEarlier)
			}
		})
	}
}

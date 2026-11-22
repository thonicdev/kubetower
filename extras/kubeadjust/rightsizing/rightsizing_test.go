package rightsizing

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// This file is the specification, and it was written before the engine beside
// it: the engine is driven by making it pass rather than by reading the
// implementation and describing what it did.
//
// That order matters more here than usual. The arithmetic of right-sizing is
// twenty lines and would survive being rewritten from scratch; the guard rails
// would not, because each of them is a case where the obvious answer is wrong —
// see the package comment. A test that says "no memory reduction on a container
// that was OOMKilled" is the only durable record of why the code refuses to say
// the helpful thing.

const mibTest = 1024 * 1024

// --- helpers ---------------------------------------------------------------

type opts struct {
	cpuReq, memReq int64
	cpuLim, memLim int64
	cpuUse, memUse int64
	oomKilled      bool
	restarts       int
	// crashLooping is the kubelet's own verdict, and it is not the restart count.
	// The engine once read `restarts >= 3` as a crash loop — see the case below
	// for why that changed.
	crashLooping bool
}

// ctr builds a container. Usage counts as reported when either figure is
// non-zero: these tests never pass an explicit zero, and a container
// measured at exactly zero is treated the same as one never measured by
// the engine anyway.
func ctr(name string, o opts) Container {
	return Container{
		Name:         name,
		CPURequest:   o.cpuReq,
		CPULimit:     o.cpuLim,
		MemRequest:   o.memReq,
		MemLimit:     o.memLim,
		HasUsage:     o.cpuUse != 0 || o.memUse != 0,
		CPUUsage:     o.cpuUse,
		MemUsage:     o.memUse,
		RestartCount: o.restarts,
		OOMKilled:    o.oomKilled,
		CrashLooping: o.crashLooping,
	}
}

func testPod(name string, containers ...Container) Pod {
	return Pod{Name: name, Containers: containers}
}

func deployment(name string, containers ...Container) Workload {
	return Workload{
		Kind: "Deployment", Name: name, Namespace: "default",
		Pods: []Pod{testPod("pod-1", containers...)},
	}
}

func workloadOf(name string, pods []Pod, kind, qos string) Workload {
	if kind == "" {
		kind = "Deployment"
	}
	if qos != "" {
		for i := range pods {
			pods[i].QOS = qos
		}
	}
	return Workload{Kind: kind, Name: name, Namespace: "default", Pods: pods}
}

func replicasOf(n int, c Container) []Pod {
	pods := make([]Pod, n)
	for i := range pods {
		pods[i] = testPod(fmt.Sprintf("pod-%d", i+1), c)
	}
	return pods
}

func testNode(cpu, mem int64, ready bool) Node {
	return Node{Name: "node-1", Ready: ready, Schedulable: true, AllocatableCPU: cpu, AllocatableMemory: mem}
}

// windowOf is a steady memory window for pod-1/c at v bytes: enough history
// for the rules that refuse to act on one scrape.
func windowOf(container string, v float64) []ContainerHistory {
	return []ContainerHistory{{Pod: "pod-1", Container: container, Memory: series(30, func(int) float64 { return v })}}
}

// series is n samples spread evenly over a week, the window the core reads:
// a test about a rule is not a test about how much history it rests on, and a
// series shorter than a day names no value (confidence.go).
func series(n int, f func(i int) float64) []Point {
	out := make([]Point, n)
	for i := range out {
		out[i] = Point{T: float64(i) * weekStep(n), V: f(i)}
	}
	return out
}

// weekStep is the spacing that spreads n samples over seven days.
func weekStep(n int) float64 {
	if n < 2 {
		return 0
	}
	return (7 * 24 * time.Hour).Seconds() / float64(n-1)
}

// find returns the first suggestion matching pred, and whether there was one.
func find(list []Suggestion, pred func(Suggestion) bool) (Suggestion, bool) {
	for _, s := range list {
		if pred(s) {
			return s, true
		}
	}
	return Suggestion{}, false
}

func filter(list []Suggestion, pred func(Suggestion) bool) []Suggestion {
	var out []Suggestion
	for _, s := range list {
		if pred(s) {
			out = append(out, s)
		}
	}
	return out
}

func byResourceKind(resource string, kind Kind) func(Suggestion) bool {
	return func(s Suggestion) bool { return s.Resource == resource && s.Kind == kind }
}

func byResourceAction(resource, action string) func(Suggestion) bool {
	return func(s Suggestion) bool { return s.Resource == resource && s.Action == action }
}

func hasWarning(s Suggestion, substr string) bool {
	for _, w := range s.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// --- ResourceStatus --------------------------------------------------------

// rs is one resource's reading as ResourceStatus takes it: one figure for the
// mean, the P95 and the peak, from a single scrape.
func rs(usage, request, limit int64, measured bool) ResourceReading {
	return ResourceReading{Mean: usage, P95: usage, Peak: usage, Request: request, Limit: limit, Measured: measured}
}

func TestResourceStatus(t *testing.T) {
	// One figure for the mean, the P95 and the peak. The cases where they
	// differ are in gaps_test.go, under
	// TestTheBarAndTheSuggestionAgreeAboutTheSameContainer.
	cases := []struct {
		name                  string
		usage, request, limit int64
		measured              bool
		isCPU                 bool
		want                  Status
	}{
		{"no usage reported", 0, 100, 200, false, true, StatusNone},
		// **A zero is a measurement.** It is StatusOverkill, not StatusNone: a
		// container using nothing of a 100m request is the clearest overkill on
		// the page.
		// The engine still says nothing about it, because a request of nothing is
		// not a request worth typing — see usageStats.
		{"usage is zero, and that is a measurement", 0, 100, 200, true, true, StatusOverkill},
		{"usage is zero with nothing to judge it against", 0, 0, 0, true, true, StatusNone},
		{"memory danger at 90% of limit", 900 * mibTest, 500 * mibTest, 1000 * mibTest, true, false, StatusDanger},
		{"memory danger at exactly 100% of limit", 1000 * mibTest, 500 * mibTest, 1000 * mibTest, true, false, StatusDanger},
		{"memory warning between 70 and 89% of limit", 750 * mibTest, 700 * mibTest, 1000 * mibTest, true, false, StatusWarning},
		// A CPU at its limit is throttled, not killed.
		{"CPU at 95% of its limit is a warning, never a danger", 950, 900, 1000, true, true, StatusWarning},
		{"CPU warning between 70 and 89% of limit", 750, 700, 1000, true, true, StatusWarning},
		{"overkill when the request's own target saves 30%", 30, 100, 0, true, true, StatusOverkill},
		{"overkill at 35% of the request", 35, 100, 0, true, true, StatusOverkill},
		{"healthy in the normal range", 400, 500, 1000, true, true, StatusHealthy},
		// The request is judged both ways.
		{"CPU above its request is a warning", 600, 500, 0, true, true, StatusWarning},
		{"CPU at twice its request is still a warning: a throttle, not an eviction", 1000, 500, 0, true, true, StatusWarning},
		{"memory at twice its request is a danger: first evicted", 1000 * mibTest, 500 * mibTest, 0, true, false, StatusDanger},
		{"memory a little above its request is a warning", 600 * mibTest, 500 * mibTest, 0, true, false, StatusWarning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResourceStatus(rs(tc.usage, tc.request, tc.limit, tc.measured), tc.isCPU); got != tc.want {
				t.Errorf("ResourceStatus(%d, req %d, lim %d, measured %v, cpu %v) = %q, want %q",
					tc.usage, tc.request, tc.limit, tc.measured, tc.isCPU, got, tc.want)
			}
		})
	}
}

// --- StorageStatus ---------------------------------------------------------

func TestStorageStatus(t *testing.T) {
	cases := []struct {
		name            string
		usage, capacity int64
		hasUsage        bool
		hasLimit        bool
		want            Status
	}{
		{"no usage reported", 0, 100, false, true, StatusNone},
		{"usage is zero", 0, 100, true, true, StatusNone},
		// A gigabyte before an absent limit colors the bar,
		// the same line the findings draw. It was a warning at any size.
		{"no limit set, holding a gigabyte, is a warning", 1024 * mibTest, 0, true, false, StatusWarning},
		{"no limit set, holding a little, is healthy", 500, 0, true, false, StatusHealthy},
		{"danger at 90% of capacity", 950, 1000, true, true, StatusDanger},
		{"warning between 75 and 89% of capacity", 800, 1000, true, true, StatusWarning},
		{"healthy below 75%", 500, 1000, true, true, StatusHealthy},
		{"capacity unknown", 100, 0, true, true, StatusNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StorageStatus(tc.usage, tc.capacity, tc.hasUsage, tc.hasLimit); got != tc.want {
				t.Errorf("StorageStatus(%d, %d, %v, %v) = %q, want %q",
					tc.usage, tc.capacity, tc.hasUsage, tc.hasLimit, got, tc.want)
			}
		})
	}
}

// --- BuildHistoryMap -------------------------------------------------------

func TestBuildHistoryMap(t *testing.T) {
	t.Run("indexes by namespace/pod/container", func(t *testing.T) {
		h := ContainerHistory{Namespace: "prod", Pod: "app-1", Container: "main"}
		got, ok := BuildHistoryMap([]ContainerHistory{h})[HistoryKey("prod", "app-1", "main")]
		if !ok || got.Pod != "app-1" || got.Container != "main" {
			t.Fatalf("BuildHistoryMap did not index prod/app-1/main, got %+v (ok=%v)", got, ok)
		}
	})

	// A StatefulSet names its pods `web-0`, `web-1`, and nothing makes those
	// unique across namespaces. The page is read cluster-wide by default, so a
	// key without the namespace would hand staging's window to production —
	// silently, and to a suggestion the operator is about to run.
	t.Run("keeps the same pod name in two namespaces apart", func(t *testing.T) {
		m := BuildHistoryMap([]ContainerHistory{
			{Namespace: "staging", Pod: "web-0", Container: "app", CPU: []Point{{T: 1, V: 10}}},
			{Namespace: "prod", Pod: "web-0", Container: "app", CPU: []Point{{T: 1, V: 900}}},
		})
		if len(m) != 2 {
			t.Fatalf("len = %d, want 2 — one namespace's window has overwritten the other's", len(m))
		}
		prod := m[HistoryKey("prod", "web-0", "app")]
		if len(prod.CPU) != 1 || prod.CPU[0].V != 900 {
			t.Errorf("prod/web-0/app CPU = %v, want the 900m series — it has staging's window", prod.CPU)
		}
	})

	t.Run("empty input yields an empty map", func(t *testing.T) {
		if n := len(BuildHistoryMap(nil)); n != 0 {
			t.Fatalf("len = %d, want 0", n)
		}
	})

	t.Run("handles multiple entries", func(t *testing.T) {
		m := BuildHistoryMap([]ContainerHistory{
			{Pod: "p1", Container: "c1"},
			{Pod: "p1", Container: "c2"},
		})
		if len(m) != 2 {
			t.Fatalf("len = %d, want 2", len(m))
		}
		if _, ok := m[HistoryKey("", "p1", "c2")]; !ok {
			t.Error("p1/c2 missing")
		}
	})
}

// --- Compute ---------------------------------------------------------------

func TestCompute(t *testing.T) {
	t.Run("no workloads yields nothing", func(t *testing.T) {
		if got := Compute(nil, nil, nil); len(got) != 0 {
			t.Fatalf("len = %d, want 0", len(got))
		}
	})

	// What nothing measured is not zero: a container nothing measured
	// produces no finding, rather than a finding built on a zero.
	t.Run("a container with no usage yields nothing", func(t *testing.T) {
		dep := deployment("app", ctr("c", opts{cpuReq: 500, cpuLim: 1000}))
		if got := Compute([]Workload{dep}, nil, nil); len(got) != 0 {
			t.Fatalf("len = %d, want 0: %+v", len(got), got)
		}
	})

	t.Run("danger when usage is near the memory limit", func(t *testing.T) {
		dep := deployment("app", ctr("c", opts{memReq: 900 * mibTest, memLim: 1000 * mibTest, memUse: 950 * mibTest, cpuUse: 1}))
		s, ok := find(Compute([]Workload{dep}, nil, nil), byResourceKind("Memory", KindDanger))
		if !ok {
			t.Fatal("no memory danger produced")
		}
		if s.Action != "Increase limit" {
			t.Errorf("action = %q, want %q", s.Action, "Increase limit")
		}
	})

	// A CPU at its limit is throttled,
	// not killed.
	t.Run("a CPU near its limit is a warning, never a danger", func(t *testing.T) {
		dep := deployment("app", ctr("c", opts{cpuReq: 900, cpuLim: 1000, cpuUse: 950, memUse: 1}))
		got := Compute([]Workload{dep}, nil, nil)
		if s, ok := find(got, byResourceKind("CPU", KindDanger)); ok {
			t.Fatalf("a CPU danger: %+v", s)
		}
		s, ok := find(got, byResourceAction("CPU", "Increase limit"))
		if !ok || s.Kind != KindWarning {
			t.Fatalf("want a CPU 'Increase limit' warning, got %+v", got)
		}
		if !strings.Contains(s.Message, "throttled") {
			t.Errorf("message = %q, want it to say what a CPU limit does", s.Message)
		}
	})

	t.Run("warning when usage is moderately near the limit", func(t *testing.T) {
		dep := deployment("app", ctr("c", opts{cpuReq: 500, cpuLim: 1000, cpuUse: 750, memUse: 1}))
		if _, ok := find(Compute([]Workload{dep}, nil, nil), byResourceKind("CPU", KindWarning)); !ok {
			t.Fatal("no CPU warning produced")
		}
	})

	t.Run("overkill when the request dwarfs usage", func(t *testing.T) {
		dep := deployment("app", ctr("c", opts{cpuReq: 1000, cpuLim: 2000, cpuUse: 50, memUse: 1}))
		s, ok := find(Compute([]Workload{dep}, nil, nil), byResourceKind("CPU", KindOverkill))
		if !ok {
			t.Fatal("no CPU overkill produced")
		}
		if s.Action != "Reduce request" {
			t.Errorf("action = %q, want %q", s.Action, "Reduce request")
		}
	})

	t.Run("overkill when the memory limit is far above use, with a window", func(t *testing.T) {
		dep := deployment("c", ctr("c", opts{memReq: 200 * mibTest, memLim: 3000 * mibTest, memUse: 150 * mibTest, cpuUse: 1}))
		if _, ok := find(Compute([]Workload{dep}, windowOf("c", 150*mibTest), nil), byResourceAction("Memory", "Reduce limit")); !ok {
			t.Fatal("no 'Reduce limit' produced")
		}
	})

	// A CPU limit reserves nothing, so lowering one saves
	// nothing, and it adds throttling. The engine used to offer it as overkill.
	t.Run("a CPU limit is never reduced", func(t *testing.T) {
		dep := deployment("app", ctr("c", opts{cpuReq: 200, cpuLim: 3000, cpuUse: 150, memUse: 1}))
		if s, ok := find(Compute([]Workload{dep}, nil, nil), byResourceAction("CPU", "Reduce limit")); ok {
			t.Fatalf("a CPU limit reduction: %+v", s)
		}
	})

	t.Run("flags a missing request", func(t *testing.T) {
		dep := deployment("app", ctr("c", opts{cpuLim: 1000, cpuUse: 100, memUse: 1}))
		s, ok := find(Compute([]Workload{dep}, nil, nil), func(s Suggestion) bool {
			return s.Resource == "CPU — no request"
		})
		if !ok {
			t.Fatal("no 'CPU — no request' produced")
		}
		if s.Kind != KindWarning {
			t.Errorf("kind = %q, want %q", s.Kind, KindWarning)
		}
		if s.Current != "none" {
			t.Errorf("current = %q, want %q", s.Current, "none")
		}
	})

	// A missing CPU limit is not a finding - CPU
	// is compressible, and leaving the limit off is current practice. A
	// missing memory limit still is.
	t.Run("does not flag a missing CPU limit", func(t *testing.T) {
		dep := deployment("app", ctr("c", opts{cpuReq: 200, cpuUse: 180, memUse: 1}))
		if s, ok := find(Compute([]Workload{dep}, nil, nil), func(s Suggestion) bool {
			return s.Resource == "CPU — no limit"
		}); ok {
			t.Fatalf("flagged a missing CPU limit: %+v", s)
		}
	})

	t.Run("flags a missing memory limit", func(t *testing.T) {
		dep := deployment("app", ctr("c", opts{memReq: 200 * mibTest, memUse: 180 * mibTest, cpuUse: 1}))
		s, ok := find(Compute([]Workload{dep}, nil, nil), func(s Suggestion) bool {
			return s.Resource == "Memory — no limit"
		})
		if !ok {
			t.Fatal("no 'Memory — no limit' produced")
		}
		if s.Kind != KindWarning {
			t.Errorf("kind = %q, want %q", s.Kind, KindWarning)
		}
		if s.Current != "unlimited" {
			t.Errorf("current = %q, want %q", s.Current, "unlimited")
		}
	})

	t.Run("sorts danger before warning before overkill", func(t *testing.T) {
		// Memory in MiB rather than raw bytes: a 1000-byte
		// request "reduced" onto the ladder's 64Mi floor is a reduction pointing
		// at a larger number, which the engine now refuses to emit — see the
		// under-10 % drop in analyzeCPUMem, and sample_test.go for the case that
		// pins the refusal itself.
		dep := deployment("app", ctr("c", opts{
			memReq: 900 * mibTest, memLim: 1000 * mibTest, memUse: 950 * mibTest,
			cpuReq: 1000, cpuUse: 50,
		}))
		got := Compute([]Workload{dep}, nil, nil)
		dangerAt, overkillAt := -1, -1
		for i, s := range got {
			if dangerAt < 0 && s.Kind == KindDanger {
				dangerAt = i
			}
			if overkillAt < 0 && s.Kind == KindOverkill {
				overkillAt = i
			}
		}
		if dangerAt < 0 || overkillAt < 0 {
			t.Fatalf("expected both a danger and an overkill, got %+v", got)
		}
		if dangerAt > overkillAt {
			t.Errorf("danger at %d, overkill at %d — wrong order", dangerAt, overkillAt)
		}
	})

	// The whole reason history is worth fetching: the snapshot says 100m, the
	// window says the container sits at 950m of a 1000m limit.
	t.Run("uses the P95 of history over the snapshot", func(t *testing.T) {
		hist := []ContainerHistory{{Pod: "pod-1", Container: "c", Memory: series(20, func(i int) float64 {
			if i == 0 {
				return 100 * mibTest
			}
			return 950 * mibTest
		})}}
		dep := deployment("app", ctr("c", opts{memReq: 900 * mibTest, memLim: 1000 * mibTest, memUse: 100 * mibTest, cpuUse: 1}))
		if _, ok := find(Compute([]Workload{dep}, hist, nil), byResourceKind("Memory", KindDanger)); !ok {
			t.Fatal("history P95 did not raise a danger")
		}
	})

	t.Run("caps an increase at node allocatable", func(t *testing.T) {
		dep := deployment("app", ctr("c", opts{memReq: 3500 * mibTest, memLim: 3800 * mibTest, memUse: 3610 * mibTest, cpuUse: 1}))
		nodes := []Node{testNode(4000, 4000*mibTest, true)}
		s, ok := find(Compute([]Workload{dep}, nil, nodes), byResourceKind("Memory", KindDanger))
		if !ok {
			t.Fatal("no memory danger produced")
		}
		if s.SuggestedRaw > 4000*mibTest {
			t.Errorf("suggested %d, want <= 4000Mi", s.SuggestedRaw)
		}
		if !strings.Contains(s.Message, "capped to node capacity") {
			t.Errorf("message does not admit the cap: %q", s.Message)
		}
	})

	// The alternative would be "increase your limit to exactly what it already
	// is", which is not advice.
	t.Run("says migrate when the cap leaves no room to grow", func(t *testing.T) {
		dep := deployment("app", ctr("c", opts{memReq: 3900 * mibTest, memLim: 4000 * mibTest, memUse: 3800 * mibTest, cpuUse: 1}))
		nodes := []Node{testNode(4000, 4000*mibTest, true)}
		s, ok := find(Compute([]Workload{dep}, nil, nodes), func(s Suggestion) bool {
			return s.Action == "Migrate to larger node"
		})
		if !ok {
			t.Fatal("no migrate suggestion produced")
		}
		if s.Kind != KindDanger {
			t.Errorf("kind = %q, want %q", s.Kind, KindDanger)
		}
	})

	t.Run("a CPU with no room to grow is a warning to migrate", func(t *testing.T) {
		dep := deployment("app", ctr("c", opts{cpuReq: 3900, cpuLim: 4000, cpuUse: 3800, memUse: 1}))
		nodes := []Node{testNode(4000, 8*1024*mibTest, true)}
		s, ok := find(Compute([]Workload{dep}, nil, nodes), func(s Suggestion) bool {
			return s.Action == "Migrate to larger node"
		})
		if !ok {
			t.Fatal("no migrate suggestion produced")
		}
		if s.Kind != KindWarning {
			t.Errorf("kind = %q, want %q", s.Kind, KindWarning)
		}
	})

	t.Run("does not cap a reduction", func(t *testing.T) {
		dep := deployment("app", ctr("c", opts{cpuReq: 3000, cpuUse: 100, memUse: 1}))
		nodes := []Node{testNode(2000, 8*1024*mibTest, true)}
		s, ok := find(Compute([]Workload{dep}, nil, nodes), byResourceAction("CPU", "Reduce request"))
		if !ok {
			t.Fatal("no 'Reduce request' produced")
		}
		if s.SuggestedRaw >= 3000 || strings.Contains(s.Message, "capped") {
			t.Errorf("suggested %d (%q), want < 3000 and uncapped", s.SuggestedRaw, s.Message)
		}
	})

	// A NotReady node's cores are not somewhere a pod can be scheduled, so they
	// must not raise the ceiling an increase is measured against.
	t.Run("ignores NotReady nodes when computing the ceiling", func(t *testing.T) {
		dep := deployment("app", ctr("c", opts{memReq: 3500 * mibTest, memLim: 3800 * mibTest, memUse: 3610 * mibTest, cpuUse: 1}))
		nodes := []Node{testNode(4000, 4000*mibTest, false)}
		s, ok := find(Compute([]Workload{dep}, nil, nodes), byResourceKind("Memory", KindDanger))
		if !ok {
			t.Fatal("no memory danger produced")
		}
		if s.SuggestedRaw <= 4000*mibTest {
			t.Errorf("suggested %d, want > 4000Mi — a NotReady node capped the value", s.SuggestedRaw)
		}
		if strings.Contains(s.Message, "capped") {
			t.Errorf("message claims a cap that should not apply: %q", s.Message)
		}
	})
}

// --- replica aggregation ---------------------------------------------------

func TestComputeReplicaAggregation(t *testing.T) {
	t.Run("one suggestion per workload, not one per replica", func(t *testing.T) {
		c := ctr("app", opts{cpuReq: 1000, cpuLim: 2000, cpuUse: 50, memUse: 1})
		got := filter(Compute([]Workload{workloadOf("app", replicasOf(20, c), "", "")}, nil, nil),
			byResourceAction("CPU", "Reduce request"))
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
		if got[0].PodCount != 20 {
			t.Errorf("podCount = %d, want 20", got[0].PodCount)
		}
	})

	t.Run("each container of a multi-container pod is separate", func(t *testing.T) {
		var pods []Pod
		for i := 1; i <= 3; i++ {
			pods = append(pods, testPod(fmt.Sprintf("pod-%d", i),
				ctr("app", opts{cpuReq: 1000, cpuLim: 2000, cpuUse: 50, memUse: 1}),
				ctr("sidecar", opts{cpuReq: 500, cpuLim: 1000, cpuUse: 10, memUse: 1}),
			))
		}
		got := filter(Compute([]Workload{workloadOf("app", pods, "", "")}, nil, nil),
			byResourceAction("CPU", "Reduce request"))
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
		if got[0].Container != "app" || got[1].Container != "sidecar" {
			t.Errorf("containers = %q, %q — want app, sidecar in template order",
				got[0].Container, got[1].Container)
		}
	})

	// Each replica looks calm on its own; the pool is what shows one of them
	// pinned at the limit.
	t.Run("pools history across replicas for the P95", func(t *testing.T) {
		hist := []ContainerHistory{
			{Pod: "pod-1", Container: "app", Memory: series(20, func(int) float64 { return 100 * mibTest })},
			{Pod: "pod-2", Container: "app", Memory: series(20, func(int) float64 { return 950 * mibTest })},
		}
		c := ctr("app", opts{memReq: 900 * mibTest, memLim: 1000 * mibTest, memUse: 100 * mibTest, cpuUse: 1})
		s, ok := find(Compute([]Workload{workloadOf("app", replicasOf(2, c), "", "")}, hist, nil),
			byResourceKind("Memory", KindDanger))
		if !ok {
			t.Fatal("pooled P95 did not raise a danger")
		}
		if s.PodCount != 2 {
			t.Errorf("podCount = %d, want 2", s.PodCount)
		}
	})

	t.Run("warns when replicas diverge in usage", func(t *testing.T) {
		hist := []ContainerHistory{
			{Pod: "pod-1", Container: "app", CPU: series(20, func(int) float64 { return 20 })},
			{Pod: "pod-2", Container: "app", CPU: series(20, func(int) float64 { return 200 })},
		}
		c := ctr("app", opts{cpuReq: 2000, cpuLim: 4000, cpuUse: 100, memUse: 1})
		s, ok := find(Compute([]Workload{workloadOf("app", replicasOf(2, c), "", "")}, hist, nil),
			byResourceAction("CPU", "Reduce request"))
		if !ok {
			t.Fatal("no 'Reduce request' produced")
		}
		if !hasWarning(s, "Replica usage varies") {
			t.Errorf("warnings = %q, want a replica-spread caveat", s.Warnings)
		}
	})

	// A StatefulSet's volumeClaimTemplates make one physical disk per replica:
	// three replicas at 95 % are three disks to expand.
	t.Run("keeps one row per PVC", func(t *testing.T) {
		var pods []Pod
		for i := 1; i <= 2; i++ {
			p := testPod(fmt.Sprintf("pod-%d", i), ctr("app", opts{cpuReq: 100, cpuUse: 50, memUse: 1}))
			p.Volumes = []Volume{{
				Name: "data", Type: "pvc", PVCName: fmt.Sprintf("data-pod-%d", i),
				Capacity: 1000 * mibTest, Usage: 950 * mibTest,
			}}
			pods = append(pods, p)
		}
		got := filter(Compute([]Workload{workloadOf("sts", pods, "StatefulSet", "")}, nil, nil),
			func(s Suggestion) bool { return s.Resource == "PVC" })
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
		if got[0].Container != "data-pod-1" || got[1].Container != "data-pod-2" {
			t.Errorf("containers = %q, %q", got[0].Container, got[1].Container)
		}
	})

	// An emptyDir is one line of the pod template repeated, not one disk per
	// replica.
	t.Run("deduplicates emptyDir across replicas", func(t *testing.T) {
		var pods []Pod
		for i := 1; i <= 3; i++ {
			p := testPod(fmt.Sprintf("pod-%d", i), ctr("app", opts{cpuReq: 100, cpuUse: 50, memUse: 1}))
			p.Volumes = []Volume{{Name: "cache", Type: "emptyDir", Usage: 2048 * mibTest}}
			pods = append(pods, p)
		}
		got := filter(Compute([]Workload{workloadOf("app", pods, "", "")}, nil, nil),
			func(s Suggestion) bool { return s.Resource == "EmptyDir" })
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
	})
}

// --- OOMKilled and restart guards ------------------------------------------

func TestComputeOOMAndRestartGuards(t *testing.T) {
	// The limit is 20× the observed usage, which on any other container is a
	// textbook "Reduce limit". Here the usage is what the container managed
	// before the kernel killed it, and reducing the limit is the exact wrong
	// move.
	t.Run("blocks memory reduction and raises a danger when OOMKilled", func(t *testing.T) {
		c := ctr("app", opts{memReq: 100 * mibTest, memLim: 1000 * mibTest, memUse: 50 * mibTest, cpuUse: 1, oomKilled: true, restarts: 2})
		got := filter(Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, nil, nil),
			func(s Suggestion) bool { return s.Resource == "Memory" })
		for _, s := range got {
			if strings.HasPrefix(s.Action, "Reduce") {
				t.Fatalf("emitted a reduction on an OOMKilled container: %+v", s)
			}
		}
		s, ok := find(got, func(s Suggestion) bool { return s.Kind == KindDanger })
		if !ok {
			t.Fatal("no danger raised for an OOMKilled container")
		}
		if !strings.Contains(s.Message, "OOMKilled") {
			t.Errorf("message = %q, want it to say OOMKilled", s.Message)
		}
		if s.SuggestedRaw <= 1000*mibTest {
			t.Errorf("suggested %d, want more than the limit it was killed at", s.SuggestedRaw)
		}
	})

	// The OOM guard is about memory. Blocking the CPU reduction too would be a
	// guard rail spreading past its evidence.
	t.Run("does not block CPU reduction when only memory was OOMKilled", func(t *testing.T) {
		c := ctr("app", opts{
			cpuReq: 1000, cpuLim: 2000, cpuUse: 50,
			memReq: 100 * mibTest, memLim: 200 * mibTest, memUse: 150 * mibTest, oomKilled: true,
		})
		if _, ok := find(Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, nil, nil),
			byResourceAction("CPU", "Reduce request")); !ok {
			t.Fatal("CPU reduction was suppressed by a memory OOM")
		}
	})

	t.Run("says migrate when the node cannot hold a bigger limit", func(t *testing.T) {
		c := ctr("app", opts{memReq: 100 * mibTest, memLim: 4000 * mibTest, memUse: 3900 * mibTest, cpuUse: 1, oomKilled: true})
		nodes := []Node{testNode(4000, 4000*mibTest, true)}
		s, ok := find(Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, nil, nodes),
			func(s Suggestion) bool { return s.Resource == "Memory" && s.Action == "Migrate to larger node" })
		if !ok {
			t.Fatal("no migrate suggestion produced")
		}
		if s.SuggestedRaw != 4000*mibTest {
			t.Errorf("suggested %d, want %d", s.SuggestedRaw, int64(4000*mibTest))
		}
		if _, ok := ToKubectlCmd(s); ok {
			t.Error("a migrate suggestion must not render a kubectl command")
		}
	})

	t.Run("OOMKilled with no limit is a node-level danger", func(t *testing.T) {
		c := ctr("app", opts{memReq: 100 * mibTest, memUse: 500 * mibTest, cpuUse: 1, oomKilled: true})
		s, ok := find(Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, nil, nil),
			func(s Suggestion) bool { return s.Resource == "Memory — no limit" })
		if !ok {
			t.Fatal("no 'Memory — no limit' produced")
		}
		if s.Kind != KindDanger {
			t.Errorf("kind = %q, want %q", s.Kind, KindDanger)
		}
		if !strings.Contains(s.Message, "node ran out of memory") {
			t.Errorf("message = %q", s.Message)
		}
	})

	// One replica being killed is a fact about the pod template's memory limit,
	// which every other replica shares.
	t.Run("an OOM on any replica is an OOM for the workload", func(t *testing.T) {
		healthy := testPod("pod-1", ctr("app", opts{memReq: 100 * mibTest, memLim: 1000 * mibTest, memUse: 50 * mibTest, cpuUse: 1}))
		killed := testPod("pod-2", ctr("app", opts{memReq: 100 * mibTest, memLim: 1000 * mibTest, memUse: 50 * mibTest, cpuUse: 1, oomKilled: true}))
		got := filter(Compute([]Workload{workloadOf("app", []Pod{healthy, killed}, "", "")}, nil, nil),
			func(s Suggestion) bool { return s.Resource == "Memory" })
		for _, s := range got {
			if strings.HasPrefix(s.Action, "Reduce") {
				t.Fatalf("emitted a reduction though one replica was OOMKilled: %+v", s)
			}
		}
		if _, ok := find(got, func(s Suggestion) bool { return s.Kind == KindDanger }); !ok {
			t.Error("no danger raised")
		}
	})

	// A container that keeps restarting reports what it allocated before dying.
	//
	// This case once set `restarts: 7` and nothing else, because the engine read
	// the restart count as the crash loop.
	// It is cumulative for the life of the pod and never decays, so a pod up since
	// February with three restarts from node maintenance was crash-looping for
	// ever and got no suggestion again. The count is now corroboration and the
	// state is the kubelet's — hence `crashLooping: true` here, and
	// TestARestartHistoryIsNotACrashLoop in gaps_test.go for the other side.
	t.Run("suppresses every reduction for a crashlooping container", func(t *testing.T) {
		c := ctr("app", opts{cpuReq: 1000, cpuLim: 2000, cpuUse: 50, memUse: 1, restarts: 7, crashLooping: true})
		for _, s := range Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, nil, nil) {
			if strings.HasPrefix(s.Action, "Reduce") {
				t.Fatalf("emitted a reduction on a crashlooping container: %+v", s)
			}
		}
	})

	t.Run("surfaces the restart count as a caveat", func(t *testing.T) {
		c := ctr("app", opts{memReq: 900 * mibTest, memLim: 1000 * mibTest, memUse: 950 * mibTest, cpuUse: 1, restarts: 2})
		s, ok := find(Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, nil, nil),
			byResourceKind("Memory", KindDanger))
		if !ok {
			t.Fatal("no memory danger produced")
		}
		if !hasWarning(s, "2 restarts") {
			t.Errorf("warnings = %q, want the restart count", s.Warnings)
		}
	})
}

// --- burst detection -------------------------------------------------------

func TestComputeBurstDetection(t *testing.T) {
	// A container idling at `idle` with a short spike to `peak` — a JVM starting
	// up, in one series.
	spiky := func(idle, peak float64) []ContainerHistory {
		return []ContainerHistory{{Pod: "pod-1", Container: "app", CPU: series(200, func(i int) float64 {
			if i < 2 {
				return peak
			}
			return idle
		})}}
	}

	// Mean-based advice would say ~170m here, which starves the startup burst
	// and fails the liveness probe on the next rollout.
	t.Run("never recommends below the observed peak", func(t *testing.T) {
		c := ctr("app", opts{cpuReq: 8000, cpuLim: 16000, cpuUse: 100, memUse: 1})
		s, ok := find(Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, spiky(100, 3000), nil),
			byResourceAction("CPU", "Reduce request"))
		if !ok {
			t.Fatal("no 'Reduce request' produced")
		}
		if s.SuggestedRaw < 3000 {
			t.Errorf("suggested %d, want at least the 3000m peak", s.SuggestedRaw)
		}
		if !strings.Contains(s.Message, "bursty") {
			t.Errorf("message = %q, want it to say the workload is bursty", s.Message)
		}
	})

	// Churning every pod of a workload to reclaim under 10 % is a rollout
	// nobody wanted.
	t.Run("drops the reduction when the peak leaves nothing to save", func(t *testing.T) {
		c := ctr("app", opts{cpuReq: 200, cpuLim: 3000, cpuUse: 100, memUse: 1})
		if s, ok := find(Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, spiky(100, 3000), nil),
			byResourceAction("CPU", "Reduce limit")); ok {
			t.Fatalf("expected no suggestion, got %+v", s)
		}
	})

	t.Run("leaves steady workloads on mean-based sizing", func(t *testing.T) {
		steady := []ContainerHistory{{Pod: "pod-1", Container: "app", CPU: series(200, func(int) float64 { return 100 })}}
		c := ctr("app", opts{cpuReq: 1000, cpuLim: 2000, cpuUse: 100, memUse: 1})
		s, ok := find(Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, steady, nil),
			byResourceAction("CPU", "Reduce request"))
		if !ok {
			t.Fatal("no 'Reduce request' produced")
		}
		if strings.Contains(s.Message, "bursty") {
			t.Errorf("message = %q, want no burst note", s.Message)
		}
		if s.SuggestedRaw >= 300 {
			t.Errorf("suggested %d, want a mean-based value under 300m", s.SuggestedRaw)
		}
	})
}

// --- QoS awareness ---------------------------------------------------------

func TestComputeQoS(t *testing.T) {
	// Moving one side alone downgrades the pod to Burstable and raises its
	// eviction priority — a behavior change hidden inside a tuning tip.
	t.Run("moves request and limit together for a Guaranteed pod", func(t *testing.T) {
		c := ctr("app", opts{cpuReq: 1000, cpuLim: 1000, cpuUse: 50, memUse: 1})
		s, ok := find(Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "Guaranteed")}, nil, nil),
			byResourceKind("CPU", KindOverkill))
		if !ok {
			t.Fatal("no CPU overkill produced")
		}
		if s.Action != "Reduce request + limit" {
			t.Errorf("action = %q, want %q", s.Action, "Reduce request + limit")
		}
		if !s.AppliesToBoth {
			t.Error("AppliesToBoth is false on a Guaranteed pair")
		}
		if !hasWarning(s, "Guaranteed") {
			t.Errorf("warnings = %q, want the QoS caveat", s.Warnings)
		}
	})

	t.Run("emits one reduction for a Guaranteed pair, not two contradicting ones", func(t *testing.T) {
		c := ctr("app", opts{cpuReq: 1000, cpuLim: 1000, cpuUse: 50, memUse: 1})
		got := filter(Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "Guaranteed")}, nil, nil),
			func(s Suggestion) bool { return s.Resource == "CPU" && strings.HasPrefix(s.Action, "Reduce") })
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1: %+v", len(got), got)
		}
	})

	t.Run("keeps request and limit separate for a Burstable pod", func(t *testing.T) {
		c := ctr("app", opts{cpuReq: 1000, cpuLim: 2000, cpuUse: 50, memUse: 1})
		s, ok := find(Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "Burstable")}, nil, nil),
			byResourceAction("CPU", "Reduce request"))
		if !ok {
			t.Fatal("no 'Reduce request' produced")
		}
		if s.AppliesToBoth {
			t.Error("AppliesToBoth is true on a Burstable pod")
		}
	})
}

// --- ToKubectlCmd ----------------------------------------------------------

func TestToKubectlCmd(t *testing.T) {
	first := func(t *testing.T, w Workload, action string) Suggestion {
		t.Helper()
		s, ok := find(Compute([]Workload{w}, nil, nil), byResourceAction("CPU", action))
		if !ok {
			t.Fatalf("no %q suggestion produced", action)
		}
		return s
	}
	c := ctr("app", opts{cpuReq: 1000, cpuLim: 2000, cpuUse: 50, memUse: 1})

	t.Run("targets deployment/ for a Deployment", func(t *testing.T) {
		cmd, ok := ToKubectlCmd(first(t, workloadOf("api", replicasOf(1, c), "", ""), "Reduce request"))
		if !ok || !strings.Contains(cmd, "deployment/api") {
			t.Fatalf("cmd = %q (ok=%v)", cmd, ok)
		}
	})

	t.Run("targets statefulset/ for a StatefulSet", func(t *testing.T) {
		cmd, ok := ToKubectlCmd(first(t, workloadOf("db", replicasOf(1, c), "StatefulSet", ""), "Reduce request"))
		if !ok || !strings.Contains(cmd, "statefulset/db") {
			t.Fatalf("cmd = %q (ok=%v)", cmd, ok)
		}
		if strings.Contains(cmd, "deployment/") {
			t.Errorf("cmd names a deployment: %q", cmd)
		}
	})

	// A CronJob is addressed: `set resources` does reach spec.jobTemplate -
	// verified with kubectl v1.36.1 - so the command is offered.
	t.Run("targets cronjob/ for a CronJob", func(t *testing.T) {
		cmd, ok := ToKubectlCmd(first(t, workloadOf("backup", replicasOf(1, c), "CronJob", ""), "Reduce request"))
		if !ok || !strings.Contains(cmd, "cronjob/backup") {
			t.Fatalf("cmd = %q (ok=%v)", cmd, ok)
		}
	})

	// A Job's pod template is immutable: the API server rejects the patch.
	t.Run("refuses a Job", func(t *testing.T) {
		if cmd, ok := ToKubectlCmd(first(t, workloadOf("once", replicasOf(1, c), "Job", ""), "Reduce request")); ok {
			t.Fatalf("expected no command, got %q", cmd)
		}
	})

	t.Run("sets both flags for a Guaranteed pair", func(t *testing.T) {
		pair := ctr("app", opts{cpuReq: 1000, cpuLim: 1000, cpuUse: 50, memUse: 1})
		s := first(t, workloadOf("api", replicasOf(1, pair), "", "Guaranteed"), "Reduce request + limit")
		cmd, ok := ToKubectlCmd(s)
		if !ok {
			t.Fatal("no command rendered")
		}
		if !strings.Contains(cmd, "--requests=cpu=") || !strings.Contains(cmd, "--limits=cpu=") {
			t.Errorf("cmd = %q, want both flags", cmd)
		}
	})

	t.Run("refuses a node-capacity-blocked suggestion", func(t *testing.T) {
		blocked := ctr("app", opts{cpuReq: 500, cpuLim: 4000, cpuUse: 3800, memUse: 1})
		s, ok := find(Compute([]Workload{workloadOf("api", replicasOf(1, blocked), "", "")}, nil,
			[]Node{testNode(4000, 8*1024*mibTest, true)}),
			func(s Suggestion) bool { return s.Action == "Migrate to larger node" })
		if !ok {
			t.Fatal("no migrate suggestion produced")
		}
		if cmd, ok := ToKubectlCmd(s); ok {
			t.Fatalf("expected no command, got %q", cmd)
		}
	})
}

// --- MaxNodeCapacity -------------------------------------------------------

func TestMaxNodeCapacity(t *testing.T) {
	t.Run("no nodes means no ceiling", func(t *testing.T) {
		got := MaxNodeCapacity(nil)
		if got.MaxCPU != 0 || got.MaxMemory != 0 {
			t.Errorf("got %+v, want zeros", got)
		}
	})

	t.Run("takes the largest Ready node", func(t *testing.T) {
		got := MaxNodeCapacity([]Node{
			testNode(2000, 4*1024*mibTest, true),
			testNode(8000, 16*1024*mibTest, true),
		})
		if got.MaxCPU != 8000 {
			t.Errorf("MaxCPU = %d, want 8000", got.MaxCPU)
		}
		if got.MaxMemory != 16*1024*mibTest {
			t.Errorf("MaxMemory = %d, want %d", got.MaxMemory, int64(16*1024*mibTest))
		}
	})

	t.Run("excludes NotReady nodes", func(t *testing.T) {
		got := MaxNodeCapacity([]Node{
			testNode(8000, 16*1024*mibTest, false),
			testNode(2000, 4*1024*mibTest, true),
		})
		if got.MaxCPU != 2000 {
			t.Errorf("MaxCPU = %d, want 2000", got.MaxCPU)
		}
	})
}

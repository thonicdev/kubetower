package rightsizing

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// What a thin or absent sample is allowed to say. Each case pins a way the
// engine was once confidently wrong — or silent — about a workload it had not
// really measured. Kept beside gaps_test.go and out of rightsizing_test.go for
// the same reason that file gives: that one is the base specification, and this
// is what it did not say.

// --- the OOM alarm does not need a scrape ------------------------------------

// metrics-server has nothing for a container that is not running, and a
// container killed for memory often enough to matter is rarely alive at scrape
// time. Gating the OOM danger on a usage sample therefore silenced the
// package's headline alarm for exactly the container it exists for: an OOM
// crash-looper produced no suggestion at all, only a pill on the card.
func TestAnOOMKilledContainerNobodyMeasuredStillRaisesTheAlarm(t *testing.T) {
	unmeasured := func(lim int64) Container {
		return Container{Name: "app", MemRequest: 256 * mibTest, MemLimit: lim,
			OOMKilled: true, RestartCount: 5}
	}

	t.Run("the limit itself is the floor the increase is computed from", func(t *testing.T) {
		w := workloadOf("oom", []Pod{testPod("pod-1", unmeasured(512*mibTest))}, "Deployment", "")
		s, ok := find(Compute([]Workload{w}, nil, nil),
			func(s Suggestion) bool { return s.Resource == "Memory" && s.Kind == KindDanger })
		if !ok {
			t.Fatal("no memory danger for an OOMKilled container metrics-server cannot see — " +
				"the sickest container on the page produces no suggestion")
		}
		if s.Action != "Increase limit" {
			t.Errorf("action = %q, want %q", s.Action, "Increase limit")
		}
		// 1.5 × the 512Mi it was killed at, on the ladder: 768Mi. No usage
		// sample was needed to name it.
		if s.SuggestedRaw != 768*mibTest {
			t.Errorf("suggested = %d bytes, want 768Mi — 1.5× the limit it was killed at", s.SuggestedRaw)
		}
		if !hasWarning(s, "5 restarts observed (OOMKilled)") {
			t.Errorf("warnings = %q, want the restart count", s.Warnings)
		}
	})

	t.Run("the node cap still turns it into a migrate", func(t *testing.T) {
		w := workloadOf("oom", []Pod{testPod("pod-1", unmeasured(512*mibTest))}, "Deployment", "")
		nodes := []Node{testNode(4000, 512*mibTest, true)}
		s, ok := find(Compute([]Workload{w}, nil, nodes),
			func(s Suggestion) bool { return s.Action == "Migrate to larger node" })
		if !ok {
			t.Fatal("no migrate suggestion when the node cannot hold a bigger limit")
		}
		if s.SuggestedRaw != 512*mibTest {
			t.Errorf("suggested = %d, want the existing limit", s.SuggestedRaw)
		}
	})

	// With no limit and no sample there is no number to compute from, and
	// inventing one is what this package refuses to do. The card still carries
	// the OOMKilled flag; the suggestion list stays honest.
	t.Run("no limit and no sample names no number", func(t *testing.T) {
		w := workloadOf("oom", []Pod{testPod("pod-1", unmeasured(0))}, "Deployment", "")
		if got := Compute([]Workload{w}, nil, nil); len(got) != 0 {
			t.Fatalf("len = %d, want 0: %+v", len(got), got)
		}
	})
}

// --- a measured zero names no number -----------------------------------------

// metrics-server reports a hard zero for any container that used under a
// millicore in the window, and idle sidecars with no requests are ordinary. The
// missing-request and missing-limit branches used to compute 1.3 × 0 and emit
// "Set request → 0m" with a kubectl command reading `--requests=cpu=0m` —
// advice nobody can type and a patch that would suffocate the container.
func TestAContainerMeasuredAtZeroIsNotToldToRequestNothing(t *testing.T) {
	t.Run("measured at zero with nothing in the spec yields nothing", func(t *testing.T) {
		c := Container{Name: "idle", HasUsage: true, CPUUsage: 0, MemUsage: 0}
		w := workloadOf("idle", []Pod{testPod("pod-1", c)}, "Deployment", "")
		for _, s := range Compute([]Workload{w}, nil, nil) {
			t.Errorf("suggested %q %q → %q, a target computed from a measurement of nothing",
				s.Action, s.Resource, s.Suggested)
		}
	})

	// The zero must silence only the resource it was measured on: memory was
	// really measured at 50Mi, and its missing limit is still worth a number.
	t.Run("the other resource keeps its finding", func(t *testing.T) {
		c := Container{Name: "app", HasUsage: true, CPUUsage: 0, MemUsage: 50 * mibTest,
			MemRequest: 100 * mibTest}
		w := workloadOf("app", []Pod{testPod("pod-1", c)}, "Deployment", "")
		got := Compute([]Workload{w}, nil, nil)
		if s, ok := find(got, func(s Suggestion) bool { return s.Resource == "CPU — no request" }); ok {
			t.Errorf("a CPU request of %q was suggested off a zero sample", s.Suggested)
		}
		s, ok := find(got, func(s Suggestion) bool { return s.Resource == "Memory — no limit" })
		if !ok {
			t.Fatal("the memory finding vanished with the CPU zero, which is a different container's problem")
		}
		if s.SuggestedRaw != 128*mibTest {
			t.Errorf("suggested = %d bytes, want 128Mi — 1.5× the measured 50Mi, rounded up", s.SuggestedRaw)
		}
	})
}

// --- a single scrape says it is a single scrape --------------------------------

// With history unwired (Collect passes nil today), every number rests on one
// instantaneous sample per replica: a workload idle at the moment of the scrape
// reads as always idle, and the burst guard that would catch it cannot fire
// without a window. The page-level History note says this once; the suggestions
// able to take something away — a reduction, or a new cap — must say it on the
// row the operator is about to act on.
func TestASnapshotSuggestionAdmitsItIsASnapshot(t *testing.T) {
	steady := []ContainerHistory{{Pod: "pod-1", Container: "app",
		CPU: series(200, func(int) float64 { return 50 })}}

	t.Run("a reduction from one scrape carries the caveat", func(t *testing.T) {
		c := ctr("app", opts{cpuReq: 1000, cpuLim: 4000, cpuUse: 50, memUse: 1})
		got := Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, nil, nil)
		s, ok := find(got, byResourceAction("CPU", "Reduce request"))
		if !ok {
			t.Fatal("no 'Reduce request' produced")
		}
		if !hasWarning(s, "single scrape") {
			t.Errorf("warnings = %q — the operator is not told the number is one sample", s.Warnings)
		}
	})

	// A memory limit lowered from one sample is the next
	// OOMKill - one sample says nothing about the peak. It used to be offered,
	// with the caveat; now it waits for a window.
	t.Run("a memory limit is not reduced from one scrape at all", func(t *testing.T) {
		c := ctr("app", opts{memReq: 200 * mibTest, memLim: 4096 * mibTest, memUse: 150 * mibTest, cpuUse: 1})
		if s, ok := find(Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, nil, nil),
			byResourceAction("Memory", "Reduce limit")); ok {
			t.Fatalf("a memory limit reduced from one scrape: %+v", s)
		}
	})

	t.Run("a new limit from one scrape carries it too", func(t *testing.T) {
		// Setting a cap where none existed is the one suggestion that can OOM a
		// workload that had no problem: a quiet moment's memory is below the
		// next busy one, which is why one sample gets twice its value.
		c := ctr("app", opts{memReq: 200 * mibTest, memUse: 180 * mibTest, cpuUse: 1})
		s, ok := find(Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, nil, nil),
			func(s Suggestion) bool { return s.Resource == "Memory — no limit" })
		if !ok {
			t.Fatal("no 'Memory — no limit' produced")
		}
		if s.SuggestedRaw < 360*mibTest {
			t.Errorf("suggested %d, want at least twice the one sample", s.SuggestedRaw)
		}
		if !hasWarning(s, "single scrape") {
			t.Errorf("warnings = %q, want the single-scrape caveat on a new cap", s.Warnings)
		}
	})

	t.Run("a usage window removes it", func(t *testing.T) {
		c := ctr("app", opts{cpuReq: 1000, cpuLim: 4000, cpuUse: 50, memUse: 1})
		s, ok := find(Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, steady, nil),
			byResourceAction("CPU", "Reduce request"))
		if !ok {
			t.Fatal("no 'Reduce request' produced")
		}
		if hasWarning(s, "single scrape") {
			t.Errorf("warnings = %q — the caveat survived a real window", s.Warnings)
		}
	})

	t.Run("an increase does not carry it", func(t *testing.T) {
		// The caveat marks what can take something away. An increase computed
		// from one scrape errs toward headroom, and a caveat on every row is a
		// caveat nobody reads.
		c := ctr("app", opts{memReq: 900 * mibTest, memLim: 1000 * mibTest, memUse: 950 * mibTest, cpuUse: 1})
		s, ok := find(Compute([]Workload{workloadOf("app", replicasOf(1, c), "", "")}, nil, nil),
			byResourceKind("Memory", KindDanger))
		if !ok {
			t.Fatal("no memory danger produced")
		}
		if hasWarning(s, "single scrape") {
			t.Errorf("warnings = %q — the caveat spread to an increase", s.Warnings)
		}
	})
}

// --- a process still warming up is not oversized --------------------------------

// RSS minutes after a start understates the steady state — heaps not yet grown,
// caches not yet filled — so a page opened right after a rollout used to read
// "uses 80Mi of a 1Gi request" about every replica at once and suggest the
// reduction that becomes the next OOM. The same shape as the post-OOM refusal:
// the sample is not wrong, it is worthless.
func TestAFreshlyStartedWorkloadGetsNoReduction(t *testing.T) {
	aged := func(age time.Duration, known bool) []Pod {
		// Oversized on paper, and near its memory limit so at least one row
		// survives to carry the disclosure.
		pods := replicasOf(2, ctr("app", opts{
			cpuReq: 4000, cpuLim: 8000, cpuUse: 90,
			memReq: 1024 * mibTest, memLim: 1024 * mibTest, memUse: 950 * mibTest,
		}))
		for i := range pods {
			pods[i].Age, pods[i].AgeKnown = age, known
		}
		return pods
	}

	t.Run("every replica young means no reduction, and the page says why", func(t *testing.T) {
		got := Compute([]Workload{workloadOf("api", aged(2*time.Minute, true), "", "")}, nil, nil)
		for _, s := range got {
			if strings.HasPrefix(s.Action, "Reduce") {
				t.Fatalf("emitted %q on a workload whose replicas are two minutes old: %+v", s.Action, s)
			}
		}
		s, ok := find(got, byResourceKind("Memory", KindDanger))
		if !ok {
			t.Fatal("the memory danger vanished too — the guard must block reductions only")
		}
		if !hasWarning(s, "younger than 10 minutes") {
			t.Errorf("warnings = %q — the suppression does not say it is one", s.Warnings)
		}
	})

	t.Run("one mature replica clears it", func(t *testing.T) {
		pods := aged(2*time.Minute, true)
		pods[1].Age = 3 * time.Hour
		got := Compute([]Workload{workloadOf("api", pods, "", "")}, nil, nil)
		if _, ok := find(got, byResourceAction("CPU", "Reduce request")); !ok {
			t.Error("no reduction though a mature replica is in the pool")
		}
	})

	t.Run("an unknown age is not evidence of youth", func(t *testing.T) {
		got := Compute([]Workload{workloadOf("api", aged(0, false), "", "")}, nil, nil)
		if _, ok := find(got, byResourceAction("CPU", "Reduce request")); !ok {
			t.Error("a missing StartTime suppressed advice — a refusal must rest on evidence")
		}
	})
}

// TestThePodAgeIsReadFromItsStatus covers the extraction in collect.go, the way
// gaps_test.go covers the crash-loop and OOM flags: an engine field nothing
// fills is a guard that never fires.
func TestThePodAgeIsReadFromItsStatus(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "pod-1"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}

	t.Run("StartTime becomes the age", func(t *testing.T) {
		p := pod.DeepCopy()
		p.Status.StartTime = &metav1.Time{Time: at.Add(-3 * time.Minute)}
		got := toPod(p, map[string]Usage{}, at)
		if !got.AgeKnown || got.Age != 3*time.Minute {
			t.Errorf("Age = %v (known=%v), want 3m known", got.Age, got.AgeKnown)
		}
	})

	t.Run("no StartTime stays unknown", func(t *testing.T) {
		if got := toPod(pod.DeepCopy(), map[string]Usage{}, at); got.AgeKnown {
			t.Error("AgeKnown = true for a pod the API server gave no StartTime")
		}
	})
}

// --- a memory request covers the peaks -----------------------------------------

// Memory usage above its request is the kubelet's eviction sort key under node
// memory pressure, so a memory request reduced to 1.3× the mean volunteers the
// pod for eviction during exactly the peaks it still has. The reduction target
// is therefore the peak for memory, plus 15 %, and the P95 for CPU, plus 20 %:
// CPU over its request is compressible and costs a throttle, not a kill.
func TestAMemoryRequestReductionCoversThePeaks(t *testing.T) {
	// Three replicas at 100 / 100 / 400: the mean is 200, the worst is 400.
	unevenPods := func() []Pod {
		pods := replicasOf(3, ctr("app", opts{
			memReq: 2048 * mibTest, memUse: 100 * mibTest,
			cpuReq: 2000, cpuUse: 100,
		}))
		pods[2].Containers[0].MemUsage = 400 * mibTest
		pods[2].Containers[0].CPUUsage = 400
		return pods
	}

	t.Run("memory is sized on the worst replica, not the average one", func(t *testing.T) {
		// No memory limit here, so the reduction rides on the new limit's
		// command (it would be refused under the 2Gi request alone).
		row, ok := find(Compute([]Workload{workloadOf("api", unevenPods(), "", "")}, nil, nil),
			func(s Suggestion) bool { return strings.HasPrefix(s.Resource, "Memory") && s.PairedRequestRaw > 0 })
		if !ok {
			t.Fatal("no memory request reduction produced")
		}
		s := Suggestion{SuggestedRaw: row.PairedRequestRaw}
		// 1.15 × 400Mi = 460Mi, on the ladder: 512Mi. The mean-based answer
		// was 1.3 × 200Mi → 384Mi, under the replica measured at 400Mi.
		if s.SuggestedRaw < 400*mibTest {
			t.Errorf("suggested %d bytes, below the 400Mi a replica is using right now — "+
				"that pod is first in line for eviction under node pressure", s.SuggestedRaw)
		}
		if s.SuggestedRaw != 512*mibTest {
			t.Errorf("suggested = %d bytes, want 512Mi — 1.15× the peak, rounded up", s.SuggestedRaw)
		}
	})

	// CPU used to stay on the mean, and the mean is below
	// the usage half the time - the engine's own "request too low" rule, which
	// reads the P95, then fired on the request it had just proposed. One
	// target for both directions: 1.2 × the P95.
	t.Run("CPU is sized on the P95 too", func(t *testing.T) {
		s, ok := find(Compute([]Workload{workloadOf("api", unevenPods(), "", "")}, nil, nil),
			byResourceAction("CPU", "Reduce request"))
		if !ok {
			t.Fatal("no CPU 'Reduce request' produced")
		}
		// One scrape, so the P95 is the worst replica's 400m: 1.2 × 400 → 480 → 500m.
		// The mean-based answer was 300m, under a replica using 400m right now.
		if s.SuggestedRaw != 500 {
			t.Errorf("suggested = %dm, want 500m — 1.2 × the P95, rounded up", s.SuggestedRaw)
		}
	})

	t.Run("a reduction that no longer reduces is dropped", func(t *testing.T) {
		// One replica far above its peers: the mean still calls the request
		// oversized (660/2048 ≈ 0.32) while 1.15 × the 1800Mi peak rounds
		// past the 2Gi request. "Reduce request" pointing at a larger number
		// is worse than saying nothing.
		pods := unevenPods()
		pods[2].Containers[0].MemUsage = 1800 * mibTest
		if s, ok := find(Compute([]Workload{workloadOf("api", pods, "", "")}, nil, nil),
			byResourceAction("Memory", "Reduce request")); ok {
			t.Errorf("a 'reduction' to %q survived against a 2Gi request", s.Suggested)
		}
	})
}

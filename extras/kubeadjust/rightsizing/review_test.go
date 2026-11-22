package rightsizing

import (
	"strings"
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// One test per rule, each stating the answer the engine used to give and why
// it was wrong. The numbers are in targets.go; the reasoning is in the
// comments beside each rule.

// memWindow is a memory window for pod-1/app whose values are given in MiB,
// spread over a week like the core's.
func memWindow(container string, values ...float64) []ContainerHistory {
	points := make([]Point, len(values))
	for i, v := range values {
		points[i] = Point{T: float64(i) * weekStep(len(values)), V: v * mibTest}
	}
	return []ContainerHistory{{Pod: "pod-1", Container: container, Memory: points}}
}

// steadyThenPeak is n samples at base with the first few at peak: a P95 at
// base and a peak the window has seen, early, so no trend climbs at its end.
func steadyThenPeak(n int, base, peak float64, peaks int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = base
		if i < peaks {
			out[i] = peak
		}
	}
	return out
}

func TestAMemoryLimitIsNeverSizedUnderThePeak(t *testing.T) {
	// P95 100Mi, peak 400Mi (4× - under the 5× that makes a workload bursty).
	// The old target was 1.5 × P95 = 150Mi → 192Mi: the container is killed the
	// next time it reaches 400Mi, which it already has.
	hist := memWindow("app", steadyThenPeak(100, 100, 400, 2)...)
	c := ctr("app", opts{memReq: 200 * mibTest, memLim: 2048 * mibTest, memUse: 100 * mibTest, cpuUse: 1})
	s, ok := find(Compute([]Workload{workloadOf("api", replicasOf(1, c), "", "")}, hist, nil),
		byResourceAction("Memory", "Reduce limit"))
	if !ok {
		t.Fatal("no memory limit reduction")
	}
	if s.SuggestedRaw < 480*mibTest {
		t.Errorf("limit = %s, under 1.2 × the 400Mi peak", fmtRaw(float64(s.SuggestedRaw), false))
	}
}

func TestCPUAboveItsRequestIsNotAnEviction(t *testing.T) {
	// "pod may be throttled or evicted" was printed for CPU, and neither is
	// what CPU above its request does.
	c := ctr("app", opts{cpuReq: 500, cpuUse: 1100, memUse: 1})
	s, ok := find(Compute([]Workload{workloadOf("api", replicasOf(1, c), "", "")}, nil, nil),
		byResourceAction("CPU", "Increase request"))
	if !ok {
		t.Fatal("no CPU request increase")
	}
	if s.Kind != KindWarning {
		t.Errorf("kind = %q at 2.2× the request; CPU over its request is a warning", s.Kind)
	}
	if strings.Contains(s.Message, "evict") {
		t.Errorf("message = %q; CPU is not an eviction signal", s.Message)
	}
	// Memory at the same ratio is a danger: the first evicted.
	m := ctr("app", opts{memReq: 500 * mibTest, memUse: 1100 * mibTest, cpuUse: 1})
	ms, ok := find(Compute([]Workload{workloadOf("api", replicasOf(1, m), "", "")}, nil, nil),
		byResourceAction("Memory", "Increase request"))
	if !ok || ms.Kind != KindDanger || !strings.Contains(ms.Message, "eviction") {
		t.Errorf("memory at 2.2× its request: %+v", ms)
	}
}

// TestApplyingASuggestionDoesNotTriggerItsOpposite is the property the one-
// target rule exists for. Each case is applied as printed - requests and
// limits set to what the rows say - and read again with the same usage: no row
// may then ask to change the same numbers again.
//
// Two of these used to fail: a CPU request sized on 1.3 × the mean was "too
// low" against its P95 on the next read, and a limit raised to 1.4 × P95 left
// the P95 at 71 % of it, past the 70 % warning.
func TestApplyingASuggestionDoesNotTriggerItsOpposite(t *testing.T) {
	cpuSpiky := func() []ContainerHistory {
		// mean ≈ 200m, P95 600m: the case that oscillated.
		v := make([]float64, 100)
		for i := range v {
			v[i] = 150
			if i%10 >= 8 {
				v[i] = 600
			}
		}
		points := make([]Point, len(v))
		for i := range v {
			points[i] = Point{T: float64(i) * 120, V: v[i]}
		}
		return []ContainerHistory{{Pod: "pod-1", Container: "app", CPU: points}}
	}
	cases := []struct {
		name string
		c    Container
		hist []ContainerHistory
	}{
		{"an oversized CPU request with a spiky P95", ctr("app", opts{cpuReq: 2000, cpuUse: 200, memUse: 1}), cpuSpiky()},
		{"a CPU near its limit", ctr("app", opts{cpuReq: 900, cpuLim: 1000, cpuUse: 950, memUse: 1}), nil},
		{"a memory near its limit", ctr("app", opts{memReq: 900 * mibTest, memLim: 1000 * mibTest, memUse: 950 * mibTest, cpuUse: 1}), nil},
		{"a memory request outgrown past its limit", ctr("app", opts{memReq: 100 * mibTest, memLim: 200 * mibTest, memUse: 180 * mibTest, cpuUse: 1}), nil},
		{"an oversized memory request and limit", ctr("app", opts{memReq: 2048 * mibTest, memLim: 8192 * mibTest, memUse: 300 * mibTest, cpuUse: 1}), memWindow("app", steadyThenPeak(100, 300, 380, 3)...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := workloadOf("api", replicasOf(1, tc.c), "", "")
			first := Compute([]Workload{w}, tc.hist, nil)
			if len(first) == 0 {
				t.Fatal("nothing to apply, so nothing is tested")
			}
			applied := tc.c
			for _, s := range first {
				apply(&applied, s)
			}
			again := Compute([]Workload{workloadOf("api", replicasOf(1, applied), "", "")}, tc.hist, nil)
			for _, s := range again {
				if s.Resource == "CPU" || s.Resource == "Memory" {
					t.Errorf("after applying %v, the engine asks again: %s %s → %s (%s)",
						actions(first), s.Action, s.Current, s.Suggested, s.Message)
				}
			}
		})
	}
}

// apply sets what a suggestion's command would set.
func apply(c *Container, s Suggestion) {
	isCPU := strings.HasPrefix(s.Resource, "CPU")
	setReq := strings.Contains(s.Action, "request") || s.AppliesToBoth
	setLim := strings.Contains(s.Action, "limit") || s.AppliesToBoth
	lim, req := s.SuggestedRaw, s.SuggestedRaw
	if s.PairedLimitRaw > 0 {
		setLim, lim = true, s.PairedLimitRaw
	}
	if s.PairedRequestRaw > 0 {
		setReq, req = true, s.PairedRequestRaw
	}
	if strings.Contains(s.Action, "Migrate") {
		return
	}
	if isCPU {
		if setReq {
			c.CPURequest = req
		}
		if setLim {
			c.CPULimit = lim
		}
		return
	}
	if setReq {
		c.MemRequest = req
	}
	if setLim {
		c.MemLimit = lim
	}
}

func actions(list []Suggestion) []string {
	var out []string
	for _, s := range list {
		out = append(out, s.Resource+": "+s.Action)
	}
	return out
}

func TestALimitIsNotReducedUnderAClimbingTrend(t *testing.T) {
	// Two days at the core's fifteen-minute step, flat at 300Mi, then
	// climbing for the last seven hours: the P95 calls a 4Gi limit generous,
	// and the reduced limit is where the climb reaches tomorrow. The step is
	// the real one because the trend is a rate per hour.
	values := steadyThenPeak(200, 300, 300, 0)
	for i := 170; i < 200; i++ {
		values[i] = 300 + float64(i-170)*8
	}
	points := make([]Point, len(values))
	for i, v := range values {
		points[i] = Point{T: float64(i) * 900, V: v * mibTest}
	}
	hist := []ContainerHistory{{Pod: "pod-1", Container: "app", Memory: points}}
	c := ctr("app", opts{memReq: 512 * mibTest, memLim: 4096 * mibTest, memUse: 530 * mibTest, cpuUse: 1})
	if s, ok := find(Compute([]Workload{workloadOf("api", replicasOf(1, c), "", "")}, hist, nil),
		byResourceAction("Memory", "Reduce limit")); ok {
		t.Errorf("a limit reduced to %s under a series that crosses it within a day", s.Suggested)
	}
}

func TestNoCommandIsOneTheAPIServerRefuses(t *testing.T) {
	// A request raised past the limit it has: request 100Mi, limit 200Mi, P95
	// 180Mi. The request alone at 256Mi was refused ("must be less than or
	// equal to memory limit"); it now carries the limit.
	c := ctr("app", opts{memReq: 100 * mibTest, memLim: 200 * mibTest, memUse: 180 * mibTest, cpuUse: 1})
	got := Compute([]Workload{workloadOf("api", replicasOf(1, c), "", "")}, nil, nil)
	s, ok := find(got, byResourceAction("Memory", "Increase request + limit"))
	if !ok {
		t.Fatalf("no paired increase: %+v", got)
	}
	if s.PairedLimitRaw < s.SuggestedRaw {
		t.Errorf("paired limit %d under the request %d", s.PairedLimitRaw, s.SuggestedRaw)
	}
	cmd, _ := ToKubectlCmd(s)
	if !strings.Contains(cmd, "--requests=memory=") || !strings.Contains(cmd, "--limits=memory=") {
		t.Errorf("cmd = %q, want both flags in one command", cmd)
	}
	// And no second row fighting it over the same limit.
	if rows := filter(got, func(s Suggestion) bool { return s.Resource == "Memory" }); len(rows) != 1 {
		t.Errorf("want one memory row, got %v", actions(rows))
	}

	for _, s := range got {
		cmdOK(t, c, s)
	}
}

// cmdOK checks what the API server checks: after the command, request <= limit.
func cmdOK(t *testing.T, c Container, s Suggestion) {
	t.Helper()
	applied := c
	apply(&applied, s)
	if applied.CPULimit > 0 && applied.CPURequest > applied.CPULimit {
		t.Errorf("%s leaves CPU request %d above limit %d", s.Action, applied.CPURequest, applied.CPULimit)
	}
	if applied.MemLimit > 0 && applied.MemRequest > applied.MemLimit {
		t.Errorf("%s leaves memory request %d above limit %d", s.Action, applied.MemRequest, applied.MemLimit)
	}
}

func TestAGuaranteedPairIsSizedForItsLimitToo(t *testing.T) {
	// Guaranteed at 2Gi/2Gi, P95 200Mi, peak 600Mi. The pair used to take the
	// request's target, 1.3 × P95 → 384Mi, for both sides: a memory limit under
	// the peak. It takes the larger of the two targets now.
	hist := memWindow("app", steadyThenPeak(100, 200, 600, 2)...)
	c := ctr("app", opts{memReq: 2048 * mibTest, memLim: 2048 * mibTest, memUse: 200 * mibTest, cpuUse: 1})
	s, ok := find(Compute([]Workload{workloadOf("db", replicasOf(1, c), "", "Guaranteed")}, hist, nil),
		byResourceAction("Memory", "Reduce request + limit"))
	if !ok {
		t.Fatal("no paired reduction")
	}
	if s.SuggestedRaw < 720*mibTest {
		t.Errorf("pair = %s, under 1.2 × the 600Mi peak", fmtRaw(float64(s.SuggestedRaw), false))
	}
}

func TestAnOldOOMKillIsNotAnAlarmAndStillBlocksReductions(t *testing.T) {
	c := ctr("app", opts{memReq: 2048 * mibTest, memLim: 4096 * mibTest, memUse: 100 * mibTest, cpuUse: 1})
	c.OOMKilledEarlier = true
	got := Compute([]Workload{workloadOf("api", replicasOf(1, c), "", "")}, memWindow("app", steadyThenPeak(50, 100, 100, 0)...), nil)
	for _, s := range got {
		if strings.HasPrefix(s.Resource, "Memory") && s.Kind == KindDanger {
			t.Errorf("an OOM kill older than a day raised a danger: %+v", s)
		}
		if strings.HasPrefix(s.Resource, "Memory") && strings.HasPrefix(s.Action, "Reduce") {
			t.Errorf("a memory reduction after an OOM kill: %+v", s)
		}
	}
	readings := Readings([]Workload{workloadOf("api", replicasOf(1, c), "", "")}, nil)
	if !readings[0].Containers[0].OOMKilledEarlier {
		t.Error("the card is not told about the earlier kill")
	}
}

func TestTheMessageSaysHowMuchHistoryAndNoConfidenceWord(t *testing.T) {
	// Three days at the core's fifteen-minute step. The message says how much
	// history the figure was read from; the level derived from it is the
	// Confidence field, never a word in the sentence.
	v := make([]float64, 3*96+1)
	for i := range v {
		v[i] = 50
	}
	points := make([]Point, len(v))
	for i := range v {
		points[i] = Point{T: float64(i) * 900, V: v[i]}
	}
	hist := []ContainerHistory{{Pod: "pod-1", Container: "app", CPU: points}}
	c := ctr("app", opts{cpuReq: 2000, cpuUse: 50, memUse: 1})
	s, ok := find(Compute([]Workload{workloadOf("api", replicasOf(1, c), "", "")}, hist, nil), byResourceAction("CPU", "Reduce request"))
	if !ok {
		t.Fatal("no reduction")
	}
	if !strings.Contains(s.Message, "3 days of history") || strings.Contains(s.Message, "confidence") {
		t.Errorf("message = %q", s.Message)
	}
	if s.Confidence != ConfidenceMedium {
		t.Errorf("confidence = %q, want medium for three days", s.Confidence)
	}
}

func TestADaemonSetMustFitOnEveryNodeItRunsOn(t *testing.T) {
	small := Node{Name: "small", Ready: true, Schedulable: true, AllocatableCPU: 2000, AllocatableMemory: 4 * gib}
	big := Node{Name: "big", Ready: true, Schedulable: true, AllocatableCPU: 8000, AllocatableMemory: 32 * gib}
	c := ctr("agent", opts{memReq: 3000 * mibTest, memLim: 3200 * mibTest, memUse: 3100 * mibTest, cpuUse: 1})
	pods := replicasOf(2, c)
	pods[0].NodeName, pods[1].NodeName = "small", "big"

	ds, ok := find(Compute([]Workload{workloadOf("agent", pods, "DaemonSet", "")}, nil, []Node{small, big}),
		byResourceAction("Memory", "Increase limit"))
	if !ok {
		t.Fatal("no increase for the DaemonSet")
	}
	if ds.SuggestedRaw > 4*gib {
		t.Errorf("DaemonSet limit %s does not fit the 4Gi node it runs on", fmtRaw(float64(ds.SuggestedRaw), false))
	}
	// The same container in a Deployment is capped by the largest node only.
	dep, _ := find(Compute([]Workload{workloadOf("agent", replicasOf(2, c), "Deployment", "")}, nil, []Node{small, big}),
		byResourceAction("Memory", "Increase limit"))
	if dep.SuggestedRaw <= 4*gib {
		t.Errorf("Deployment limit %s was capped by the small node", fmtRaw(float64(dep.SuggestedRaw), false))
	}
}

func TestATaintedControlPlaneIsNotTheCeiling(t *testing.T) {
	worker := Node{Name: "w", Ready: true, Schedulable: true, AllocatableCPU: 4000, AllocatableMemory: 4 * gib}
	cp := Node{Name: "cp", Ready: true, Schedulable: false, AllocatableCPU: 16000, AllocatableMemory: 64 * gib}
	if nc := MaxNodeCapacity([]Node{worker, cp}); nc.MaxMemory != 4*gib {
		t.Errorf("ceiling = %s, want the worker's 4Gi", fmtRaw(float64(nc.MaxMemory), false))
	}
	// Every node tainted - one control plane running everything: its own size.
	if nc := MaxNodeCapacity([]Node{cp}); nc.MaxMemory != 64*gib {
		t.Errorf("single tainted node: ceiling = %d", nc.MaxMemory)
	}

	tainted := corev1.Node{Spec: corev1.NodeSpec{Taints: []corev1.Taint{{Key: "node-role.kubernetes.io/control-plane", Effect: corev1.TaintEffectNoSchedule}}}}
	cordoned := corev1.Node{Spec: corev1.NodeSpec{Unschedulable: true}}
	preferred := corev1.Node{Spec: corev1.NodeSpec{Taints: []corev1.Taint{{Key: "x", Effect: corev1.TaintEffectPreferNoSchedule}}}}
	if schedulable(&tainted) || schedulable(&cordoned) || !schedulable(&preferred) {
		t.Error("schedulable: NoSchedule and cordoned are not, PreferNoSchedule is")
	}
}

func hpaOnCPU(name, target string, pct int32) autoscalingv2.HorizontalPodAutoscaler {
	return autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: target},
			Metrics: []autoscalingv2.MetricSpec{{
				Type: autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricSource{Name: corev1.ResourceCPU,
					Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: &pct}},
			}},
		},
	}
}

func vpa(name, target, mode string) unstructured.Unstructured {
	spec := map[string]any{"targetRef": map[string]any{"kind": "Deployment", "name": target}}
	if mode != "" {
		spec["updatePolicy"] = map[string]any{"updateMode": mode}
	}
	u := unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	u.SetNamespace("default")
	u.SetName(name)
	return u
}

func TestAnHPAOnCPUIsNotTakenForWaste(t *testing.T) {
	// An HPA at 50 % keeps every pod at half its CPU request on purpose; a
	// smaller request means more replicas, not less reserved.
	idx := Autoscalers([]autoscalingv2.HorizontalPodAutoscaler{hpaOnCPU("api", "api", 50)}, nil)
	w := workloadOf("api", replicasOf(2, ctr("app", opts{cpuReq: 2000, cpuUse: 300, memReq: 2048 * mibTest, memUse: 200 * mibTest})), "Deployment", "")
	ws := []Workload{w}
	attachAutoscalers(ws, idx)
	got := Compute(ws, nil, nil)
	if s, ok := find(got, byResourceAction("CPU", "Reduce request")); ok {
		t.Errorf("a CPU reduction under an HPA on CPU: %+v", s)
	}
	// Memory, which the HPA does not scale on, keeps its reduction - here
	// carried by the new memory limit's command, which would be refused
	// under the 2Gi request without it.
	if _, ok := find(got, func(s Suggestion) bool { return strings.HasPrefix(s.Resource, "Memory") && s.PairedRequestRaw > 0 }); !ok {
		t.Errorf("memory lost its reduction too: %v", actions(got))
	}
	notes := Readings(ws, nil)[0].Managed
	if len(notes) != 1 || !strings.Contains(notes[0], "HPA api") || !strings.Contains(notes[0], "CPU at 50%") {
		t.Errorf("card notes = %q", notes)
	}
}

func TestAVPAThatAppliesOwnsTheNumbers(t *testing.T) {
	base := ctr("app", opts{cpuReq: 2000, cpuUse: 100, memReq: 2048 * mibTest, memUse: 100 * mibTest})
	for _, tc := range []struct {
		mode  string
		quiet bool
	}{{"", true}, {"Auto", true}, {"Recreate", true}, {"Initial", true}, {"Off", false}} {
		ws := []Workload{workloadOf("api", replicasOf(1, base), "Deployment", "")}
		attachAutoscalers(ws, Autoscalers(nil, []unstructured.Unstructured{vpa("api-vpa", "api", tc.mode)}))
		got := filter(Compute(ws, nil, nil), func(s Suggestion) bool { return s.Resource == "CPU" || s.Resource == "Memory" })
		if tc.quiet && len(got) != 0 {
			t.Errorf("VPA %q: the engine argues with it: %v", tc.mode, actions(got))
		}
		if !tc.quiet && len(got) == 0 {
			t.Errorf("VPA %q only recommends, and the page went quiet", tc.mode)
		}
	}
}

func TestAPodHoldsWhatTheSchedulerReserved(t *testing.T) {
	q := func(cpu, mem string) corev1.ResourceRequirements {
		return corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)},
			Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(mem)},
		}
	}
	always := corev1.ContainerRestartPolicyAlways
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{
			{Name: "migrate", Resources: q("2", "2Gi")},
			{Name: "proxy", RestartPolicy: &always, Resources: q("100m", "128Mi")},
			{Name: "warm", Resources: q("1", "1Gi")},
		},
		Containers: []corev1.Container{{Name: "app", Resources: q("250m", "256Mi")}},
		Overhead:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m")},
	}}
	// CPU: running = 250m + 100m sidecar = 350m; init peak = max(2000m,
	// 100m, 1000m + 100m) = 2000m; plus 50m overhead = 2050m. A sum of the
	// running containers said 350m.
	if req, _ := PodResources(pod, corev1.ResourceCPU); req != 2050 {
		t.Errorf("cpu request = %dm, want 2050m", req)
	}
	// Memory limit: same arithmetic, 2Gi init peak; no overhead declared for memory.
	if _, lim := PodResources(pod, corev1.ResourceMemory); lim != 2*gib {
		t.Errorf("memory limit = %d, want 2Gi", lim)
	}
	// Pod-level resources win when set.
	pod.Spec.Resources = &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")}}
	if req, _ := PodResources(pod, corev1.ResourceCPU); req != 3050 {
		t.Errorf("pod-level cpu request = %dm, want 3000m + 50m overhead", req)
	}

	// And the node total is that figure, not the container sum.
	node := summaryNode("n", true, 4000, 8192)
	p := summaryPod("default", "p", "n", corev1.PodRunning, corev1.Container{Name: "app", Resources: q("250m", "256Mi")})
	p.Spec.InitContainers = []corev1.Container{{Name: "migrate", Resources: q("2", "2Gi")}}
	got := summariseNodes([]corev1.Node{node}, []corev1.Pod{p}, nil, p.CreationTimestamp.Time)
	if got[0].RequestedCPU != 2000 || got[0].RequestedMemory != 2*gib {
		t.Errorf("node reserved %dm / %d, want what the init container holds: 2000m / 2Gi", got[0].RequestedCPU, got[0].RequestedMemory)
	}
}

func TestANodeCountsItsPodsDisk(t *testing.T) {
	node := summaryNode("n", true, 4000, 8192)
	node.Status.Allocatable[corev1.ResourceEphemeralStorage] = resource.MustParse("100Gi")
	p := summaryPod("default", "p", "n", corev1.PodRunning, corev1.Container{Name: "app"})
	p.Spec.Volumes = []corev1.Volume{
		{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "shm", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}}},
	}
	unmeasured := summaryPod("default", "q", "n", corev1.PodRunning, corev1.Container{Name: "app"})
	nodes := summariseNodes([]corev1.Node{node}, []corev1.Pod{p, unmeasured}, nil, p.CreationTimestamp.Time)
	if nodes[0].AllocatableEphemeral != gibs(100) {
		t.Errorf("allocatable ephemeral = %d", nodes[0].AllocatableEphemeral)
	}
	if nodes[0].EphemeralMeasured {
		t.Error("measured before anything was attached")
	}
	AttachNodeEphemeral(nodes, []corev1.Pod{p, unmeasured}, map[string]PodStorage{
		"default/p": {Ephemeral: map[string]int64{"app": 300 * mibTest},
			Volumes: map[string]VolumeStat{"scratch": {UsedBytes: 200 * mibTest}, "shm": {UsedBytes: 64 * mibTest}}},
	})
	// 300Mi of layer and logs, 200Mi of emptyDir; the tmpfs one is memory.
	if !nodes[0].EphemeralMeasured || nodes[0].UsedEphemeral != 500*mibTest {
		t.Errorf("used ephemeral = %d (measured %v), want 500Mi", nodes[0].UsedEphemeral, nodes[0].EphemeralMeasured)
	}
}

func TestAStaticPodIsItsOwnPodAndNotItsNode(t *testing.T) {
	yes := true
	static := func(name string) corev1.Pod {
		return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: name + "-cp",
			OwnerReferences: []metav1.OwnerReference{{Kind: "Node", Name: "cp", Controller: &yes}}}}
	}
	for _, p := range []corev1.Pod{static("etcd"), static("kube-apiserver")} {
		kind, name := attribute(&p, nil)
		if kind != "Pod" || name != p.Name {
			t.Errorf("%s attributed to %s/%s, want its own pod", p.Name, kind, name)
		}
	}
}

func TestAQuantityOffTheMiBIsWrittenExactly(t *testing.T) {
	// A node's allocatable is rarely a whole MiB: rounded up it passes the
	// node, rounded down a limit held to a `1G` request lands under it (a
	// `--limits=memory=953Mi` beside a 1G request is refused).
	if got := fmtKubectl(4000*mibTest+800*1024, false); got != "4195123200" {
		t.Errorf("fmtKubectl = %q, want the bytes", got)
	}
	if got := fmtKubectl(1536*mibTest, false); got != "1536Mi" {
		t.Errorf("fmtKubectl = %q", got)
	}
}

// TestEveryPrintedCommandIsAcceptedRunAlone is a probe, kept: each command is
// parsed back from the text the page prints and applied alone, to the spec as
// it is now - the order the page lists them in is not the order they depend on,
// and any one may be run first.
func TestEveryPrintedCommandIsAcceptedRunAlone(t *testing.T) {
	cases := []struct {
		name string
		c    Container
		hist []ContainerHistory
		oom  bool
	}{
		{"a decimal request beside a reduced limit", Container{Name: "app", HasUsage: true, CPUUsage: 1,
			MemRequest: 1_000_000_000, MemLimit: 2048 * mibTest, MemUsage: 450 * mibTest},
			memWindow("app", steadyThenPeak(100, 450, 500, 2)...), false},
		{"a new limit under the current request", ctr("app", opts{memReq: 2048 * mibTest, memUse: 450 * mibTest, cpuUse: 1}), nil, false},
		{"a reduced limit and request together", ctr("app", opts{memReq: 2048 * mibTest, memLim: 8192 * mibTest, memUse: 300 * mibTest, cpuUse: 1}),
			memWindow("app", steadyThenPeak(100, 300, 300, 0)...), false},
		{"an OOM kill beside a request raised past the limit", ctr("app", opts{memReq: 200 * mibTest, memLim: 256 * mibTest, memUse: 240 * mibTest, cpuUse: 1}), nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.c
			c.OOMKilled = tc.oom
			got := Compute([]Workload{workloadOf("api", replicasOf(1, c), "", "")}, tc.hist, nil)
			if len(got) == 0 {
				t.Fatal("nothing suggested, so nothing is tested")
			}
			for _, s := range got {
				cmd, ok := ToKubectlCmd(s)
				if !ok {
					continue
				}
				req, lim := c.MemRequest, c.MemLimit
				for _, f := range strings.Fields(cmd) {
					for prefix, into := range map[string]*int64{"--requests=memory=": &req, "--limits=memory=": &lim} {
						if v, found := strings.CutPrefix(f, prefix); found {
							q := resource.MustParse(v)
							*into = q.Value()
						}
					}
				}
				if lim > 0 && req > lim {
					t.Errorf("%q run alone leaves request %d above limit %d - refused", cmd, req, lim)
				}
			}
		})
	}
}

func TestAutoscalersAreReadAsTheyAreWritten(t *testing.T) {
	avg := autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "rps"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "web"},
			Metrics: []autoscalingv2.MetricSpec{{
				Type: autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricSource{Name: corev1.ResourceMemory,
					Target: autoscalingv2.MetricTarget{Type: autoscalingv2.AverageValueMetricType}},
			}},
		},
	}
	idx := Autoscalers([]autoscalingv2.HorizontalPodAutoscaler{avg, hpaOnCPU("cpu", "api", 70)},
		[]unstructured.Unstructured{vpa("v", "api", "")})
	web := idx[AutoscalerKey("default", "Deployment", "web")]
	if len(web) != 1 || len(web[0].Utilization) != 0 {
		t.Errorf("an AverageValue target is not a share of the request: %+v", web)
	}
	api := idx[AutoscalerKey("default", "Deployment", "api")]
	if len(api) != 2 || api[0].Utilization["cpu"] != 70 || api[1].Mode != "Auto" {
		t.Errorf("api = %+v; a VPA with no update policy applies (Auto)", api)
	}
}

// TestNoPrintedCommandIsRefusedAcrossAGrid sweeps requests, limits, usage,
// a window or none, and an OOM kill or none, and runs every printed command
// alone against the spec as it is: none may leave a request above its limit.
func TestNoPrintedCommandIsRefusedAcrossAGrid(t *testing.T) {
	sizes := []int64{0, 100 * mibTest, 1_000_000_000, 2048 * mibTest, 8192 * mibTest}
	uses := []float64{50, 240, 900, 3000}
	refused := 0
	for _, req := range sizes {
		for _, lim := range sizes {
			if lim > 0 && req > lim {
				continue // not a spec the API server would hold
			}
			for _, use := range uses {
				for _, windowed := range []bool{false, true} {
					for _, oom := range []bool{false, true} {
						c := Container{Name: "app", HasUsage: true, CPUUsage: 1, MemRequest: req, MemLimit: lim,
							MemUsage: int64(use * mibTest), OOMKilled: oom}
						var hist []ContainerHistory
						if windowed {
							hist = memWindow("app", steadyThenPeak(100, use, use*1.3, 3)...)
						}
						for _, s := range Compute([]Workload{workloadOf("api", replicasOf(1, c), "", "")}, hist, nil) {
							cmd, ok := ToKubectlCmd(s)
							if !ok || !strings.Contains(cmd, "memory=") {
								continue
							}
							r, l := req, lim
							for _, f := range strings.Fields(cmd) {
								if v, found := strings.CutPrefix(f, "--requests=memory="); found {
									q := resource.MustParse(v)
									r = q.Value()
								}
								if v, found := strings.CutPrefix(f, "--limits=memory="); found {
									q := resource.MustParse(v)
									l = q.Value()
								}
							}
							if l > 0 && r > l {
								refused++
								if refused <= 3 {
									t.Errorf("req %d lim %d use %vMi window %v oom %v: %q is refused", req, lim, use, windowed, oom, cmd)
								}
							}
						}
					}
				}
			}
		}
	}
	if refused > 3 {
		t.Errorf("%d refused commands in all", refused)
	}
}

// An OOM kill beside a request that must rise past the limit: one command
// raising both, never two rows naming two different limits whose end state
// depends on which ran last.
func TestAnOOMRowCarriesTheRequestItMustRaise(t *testing.T) {
	c := ctr("app", opts{memReq: 200 * mibTest, memLim: 256 * mibTest, memUse: 240 * mibTest, cpuUse: 1, oomKilled: true})
	rows := filter(Compute([]Workload{workloadOf("api", replicasOf(1, c), "", "")}, nil, nil),
		func(s Suggestion) bool { return strings.HasPrefix(s.Resource, "Memory") })
	if len(rows) != 1 {
		t.Fatalf("want one memory row, got %v", actions(rows))
	}
	s := rows[0]
	if s.Kind != KindDanger || s.PairedRequestRaw == 0 || s.SuggestedRaw < s.PairedRequestRaw {
		t.Errorf("row = %+v", s)
	}
	cmd, _ := ToKubectlCmd(s)
	if !strings.Contains(cmd, "--requests=memory=") || !strings.Contains(cmd, "--limits=memory=") {
		t.Errorf("cmd = %q", cmd)
	}
}

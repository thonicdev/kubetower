package rightsizing

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The defect this file leads with is the one that left summary.go untested for
// a while: the ratio the overview grid ranks on was a **complete** request sum
// over a **partial** usage sum.
//
// What nothing measured is not zero, at the granularity the page sorts by. The
// two sums are gathered in the same loop and it is not obvious from reading it
// that they run over different populations: every container adds its request,
// and only a container the metrics API answered for adds its usage.

// --- fixtures ---------------------------------------------------------------

func summaryPod(namespace, name, node string, phase corev1.PodPhase, containers ...corev1.Container) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       corev1.PodSpec{NodeName: node, Containers: containers},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

// summaryContainer takes CPU in millicores and memory in MiB, which is what the
// cases below are stated in.
func summaryContainer(name string, cpuReq, memReqMiB, cpuLim, memLimMiB int64) corev1.Container {
	q := func(milli, mib int64) corev1.ResourceList {
		list := corev1.ResourceList{}
		if milli > 0 {
			list[corev1.ResourceCPU] = *resource.NewMilliQuantity(milli, resource.DecimalSI)
		}
		if mib > 0 {
			list[corev1.ResourceMemory] = *resource.NewQuantity(mib*1024*1024, resource.BinarySI)
		}
		return list
	}
	return corev1.Container{
		Name: name,
		Resources: corev1.ResourceRequirements{
			Requests: q(cpuReq, memReqMiB),
			Limits:   q(cpuLim, memLimMiB),
		},
	}
}

func summaryNode(name string, ready bool, cpuMilli, memMiB int64) corev1.Node {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(cpuMilli, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(memMiB*1024*1024, resource.BinarySI),
			},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}},
		},
	}
}

func summaryFind(t *testing.T, all []NamespaceSummary, name string) NamespaceSummary {
	t.Helper()
	for _, n := range all {
		if n.Name == name {
			return n
		}
	}
	t.Fatalf("namespace %q not in the summary", name)
	return NamespaceSummary{}
}

// --- the ratio is over one population ---------------------------------------

// TestTheRatioIsTakenOverTheContainersThatReported is the regression.
//
// Ten pods asking 1000m and really using 500m, five of them thirty seconds old
// so metrics-server has nothing for them yet. Summed the old way that is
// 10 000 ÷ 2 500 = ×4.0, which bands "high" and sorts the namespace to the top
// of the grid as one of the places asking for far more than it uses. The truth
// is ×2.0, which bands "ok".
//
// One node whose kubelet is unreachable makes exactly this permanent: the API
// server still lists its pods so the requests count, metrics-server has none of
// them so the usage does not, and nothing decays.
func TestTheRatioIsTakenOverTheContainersThatReported(t *testing.T) {
	var pods []corev1.Pod
	usage := map[string]Usage{}
	for i := 0; i < 10; i++ {
		name := "pod-" + string(rune('a'+i))
		pods = append(pods, summaryPod("prod", name, "node-1", corev1.PodRunning,
			summaryContainer("app", 1000, 0, 0, 0)))
		if i < 5 { // the other five are too new to have been scraped
			usage["prod/"+name+"/app"] = Usage{CPUMillicores: 500}
		}
	}

	got := summaryFind(t, SummariseNamespaces(pods, usage, nil), "prod")

	if got.CPURatio != 2 {
		t.Errorf("CPURatio = %.2f, want 2.00 — the five that reported ask 5 000m and use 2 500m. "+
			"×4.0 is the whole namespace's ask over half its usage, which is not a ratio of anything",
			got.CPURatio)
	}
	if got.Band != "ok" {
		t.Errorf("band = %q, want %q; %q sorts this namespace to the top of the grid on a number "+
			"half of it never contributed to", got.Band, "ok", got.Band)
	}
	// What the namespace asks for in total is still reported, because it is a
	// true fact about the namespace — it is only the *ratio* that has to be over
	// one population.
	if got.CPURequest != 10000 {
		t.Errorf("CPURequest = %d, want 10000 — the whole namespace's ask", got.CPURequest)
	}
	if got.CPURequestMeasured != 5000 {
		t.Errorf("CPURequestMeasured = %d, want 5000", got.CPURequestMeasured)
	}
	if got.Containers != 10 || got.ContainersMeasured != 5 {
		t.Errorf("coverage = %d of %d, want 5 of 10 — the card cannot disclose what the summary does not count",
			got.ContainersMeasured, got.Containers)
	}
}

// TestFullCoverageLeavesTheRatioAlone: the correction must not move a number
// that was never wrong. Without this the test above passes for a function that
// always divides by zero.
func TestFullCoverageLeavesTheRatioAlone(t *testing.T) {
	pods := []corev1.Pod{
		summaryPod("prod", "pod-a", "node-1", corev1.PodRunning, summaryContainer("app", 1000, 512, 0, 0)),
		summaryPod("prod", "pod-b", "node-1", corev1.PodRunning, summaryContainer("app", 1000, 512, 0, 0)),
	}
	usage := map[string]Usage{
		"prod/pod-a/app": {CPUMillicores: 250, MemoryBytes: 128 * 1024 * 1024},
		"prod/pod-b/app": {CPUMillicores: 250, MemoryBytes: 128 * 1024 * 1024},
	}

	got := summaryFind(t, SummariseNamespaces(pods, usage, nil), "prod")

	if got.CPURatio != 4 || got.MemRatio != 4 {
		t.Errorf("ratios = CPU %.2f / MEM %.2f, want 4.00 each", got.CPURatio, got.MemRatio)
	}
	if got.Band != "high" {
		t.Errorf("band = %q, want %q", got.Band, "high")
	}
	if got.ContainersMeasured != got.Containers {
		t.Errorf("coverage = %d of %d; a fully measured namespace must report no gap, "+
			"or every card grows a caveat nobody reads", got.ContainersMeasured, got.Containers)
	}
}

// TestNothingMeasuredIsNotANamespaceUsingNothing. What nothing measured is not
// zero: an unmeasured namespace must not be banded, because a band is a
// judgment and there is nothing to judge.
func TestNothingMeasuredIsNotANamespaceUsingNothing(t *testing.T) {
	pods := []corev1.Pod{
		summaryPod("prod", "pod-a", "node-1", corev1.PodRunning, summaryContainer("app", 1000, 0, 0, 0)),
	}

	got := summaryFind(t, SummariseNamespaces(pods, map[string]Usage{}, nil), "prod")

	if got.Measured {
		t.Error("Measured is true with an empty usage map")
	}
	if got.Band != "" {
		t.Errorf("band = %q, want empty — a namespace nothing reported on is not a tidy namespace", got.Band)
	}
	if got.CPURatio != 0 {
		t.Errorf("CPURatio = %.2f, want 0 — zero means cannot say, and the page draws it as an em dash",
			got.CPURatio)
	}
}

// TestAFinishedPodHasGivenItsReservationBack. holdsCapacity, from the other
// side: a Succeeded pod counted as capacity draws a namespace as full of jobs
// that finished last week.
func TestAFinishedPodHasGivenItsReservationBack(t *testing.T) {
	pods := []corev1.Pod{
		summaryPod("batch", "done", "node-1", corev1.PodSucceeded, summaryContainer("app", 4000, 0, 0, 0)),
		summaryPod("batch", "failed", "node-1", corev1.PodFailed, summaryContainer("app", 4000, 0, 0, 0)),
		summaryPod("batch", "live", "node-1", corev1.PodRunning, summaryContainer("app", 1000, 0, 0, 0)),
	}
	usage := map[string]Usage{"batch/live/app": {CPUMillicores: 500}}

	got := summaryFind(t, SummariseNamespaces(pods, usage, nil), "batch")

	if got.Pods != 1 || got.CPURequest != 1000 {
		t.Errorf("pods = %d, request = %dm; want 1 pod and 1000m — the finished ones hold nothing",
			got.Pods, got.CPURequest)
	}
	if got.CPURatio != 2 {
		t.Errorf("CPURatio = %.2f, want 2.00", got.CPURatio)
	}
}

// TestANamespaceThatAsksForNothingIsNotAnUnmeasuredOne pins the distinction the
// Band field comment records: `argocd` must not read "not measured" while its
// usage sits in the same table, because a ratio needs a denominator.
func TestANamespaceThatAsksForNothingIsNotAnUnmeasuredOne(t *testing.T) {
	pods := []corev1.Pod{
		summaryPod("argocd", "pod-a", "node-1", corev1.PodRunning, summaryContainer("app", 0, 0, 0, 0)),
	}
	usage := map[string]Usage{"argocd/pod-a/app": {CPUMillicores: 300, MemoryBytes: 64 * 1024 * 1024}}

	got := summaryFind(t, SummariseNamespaces(pods, usage, nil), "argocd")

	if got.Band != "norequests" {
		t.Errorf("band = %q, want %q — the scheduler is placing these pods blind, which is a "+
			"problem and not a gap in our reading", got.Band, "norequests")
	}
	if !got.Measured {
		t.Error("Measured is false for a namespace whose usage is right there")
	}
}

// TestTheWorstNamespacesComeFirst. The grid's whole purpose: an overview sorted
// alphabetically buries the answer under `argocd`.
func TestTheWorstNamespacesComeFirst(t *testing.T) {
	var pods []corev1.Pod
	usage := map[string]Usage{}
	add := func(namespace string, req, used int64) {
		pods = append(pods, summaryPod(namespace, "pod", "node-1", corev1.PodRunning,
			summaryContainer("app", req, 0, 0, 0)))
		if used > 0 {
			usage[namespace+"/pod/app"] = Usage{CPUMillicores: used}
		}
	}
	add("tidy", 1000, 900)     // ×1.1 → ok
	add("wasteful", 6000, 500) // ×12  → overcommit
	add("dark", 1000, 0)       // nothing measured
	add("padded", 3000, 1000)  // ×3   → high

	got := SummariseNamespaces(pods, usage, nil)

	want := []string{"wasteful", "padded", "dark", "tidy"}
	for i, name := range want {
		if got[i].Name != name {
			names := make([]string, len(got))
			for j, n := range got {
				names[j] = n.Name
			}
			t.Fatalf("order = %v, want %v — worst first, then what could not be said, then the tidy ones",
				names, want)
		}
	}
}

// --- nodes ------------------------------------------------------------------

// TestANodeCountsOnlyItsOwnPods. A request is a reservation against one machine,
// not against the cluster.
func TestANodeCountsOnlyItsOwnPods(t *testing.T) {
	nodes := []corev1.Node{summaryNode("node-1", true, 4000, 8192), summaryNode("node-2", true, 4000, 8192)}
	pods := []corev1.Pod{
		summaryPod("prod", "a", "node-1", corev1.PodRunning, summaryContainer("app", 1000, 512, 2000, 1024)),
		summaryPod("prod", "b", "node-2", corev1.PodRunning, summaryContainer("app", 3000, 1024, 3000, 2048)),
		// Not scheduled anywhere yet: it belongs to no node's total.
		summaryPod("prod", "c", "", corev1.PodPending, summaryContainer("app", 1000, 512, 0, 0)),
	}
	usage := map[string]Usage{"prod/a/app": {CPUMillicores: 400, MemoryBytes: 256 * 1024 * 1024}}

	got := SummariseNodes(nodes, pods, usage)

	if len(got) != 2 {
		t.Fatalf("got %d nodes, want 2", len(got))
	}
	if got[0].RequestedCPU != 1000 || got[1].RequestedCPU != 3000 {
		t.Errorf("requested = %dm / %dm, want 1000m / 3000m", got[0].RequestedCPU, got[1].RequestedCPU)
	}
	if !got[0].Measured || got[1].Measured {
		t.Errorf("measured = %v / %v, want true / false", got[0].Measured, got[1].Measured)
	}
}

// TestANodeSaysHowMuchOfItWasMeasured is BUG-83 on the node card. Requested and
// Limited count every container; Used counts only what reported. A node whose
// kubelet has gone still lists its pods through the API server, so without this
// it draws as a nearly idle machine with a full reservation.
func TestANodeSaysHowMuchOfItWasMeasured(t *testing.T) {
	nodes := []corev1.Node{summaryNode("node-1", true, 8000, 8192)}
	pods := []corev1.Pod{
		summaryPod("prod", "a", "node-1", corev1.PodRunning, summaryContainer("app", 1000, 0, 0, 0)),
		summaryPod("prod", "b", "node-1", corev1.PodRunning, summaryContainer("app", 1000, 0, 0, 0)),
		summaryPod("prod", "c", "node-1", corev1.PodRunning, summaryContainer("app", 1000, 0, 0, 0)),
	}
	usage := map[string]Usage{"prod/a/app": {CPUMillicores: 900}}

	got := SummariseNodes(nodes, pods, usage)[0]

	if got.Containers != 3 || got.ContainersMeasured != 1 {
		t.Errorf("coverage = %d of %d, want 1 of 3", got.ContainersMeasured, got.Containers)
	}
	if !got.Measured {
		t.Error("Measured is false though one container reported")
	}
	if got.UsedCPU != 900 {
		t.Errorf("UsedCPU = %dm, want 900m — the figure is what reported, and the count beside it "+
			"is what makes that readable rather than misleading", got.UsedCPU)
	}
}

// TestLimitsAboveAllocatableAreOrdinary. Requested above allocatable is
// impossible (the scheduler refuses); Limited above it is the overcommitment
// worth seeing, and must not be clamped away in the arithmetic.
func TestLimitsAboveAllocatableAreOrdinary(t *testing.T) {
	nodes := []corev1.Node{summaryNode("node-1", true, 4000, 4096)}
	pods := []corev1.Pod{
		summaryPod("prod", "a", "node-1", corev1.PodRunning, summaryContainer("app", 500, 256, 4000, 4096)),
		summaryPod("prod", "b", "node-1", corev1.PodRunning, summaryContainer("app", 500, 256, 4000, 4096)),
	}

	got := SummariseNodes(nodes, pods, map[string]Usage{})[0]

	if got.LimitedCPU != 8000 || got.AllocatableCPU != 4000 {
		t.Errorf("limited = %dm of %dm allocatable, want 8000m of 4000m", got.LimitedCPU, got.AllocatableCPU)
	}
}

// TestANotReadyNodeIsStillDrawn. It is the one whose card the operator needs.
func TestANotReadyNodeIsStillDrawn(t *testing.T) {
	got := SummariseNodes([]corev1.Node{summaryNode("sick", false, 4000, 4096)}, nil, nil)
	if len(got) != 1 {
		t.Fatalf("got %d nodes, want 1", len(got))
	}
	if got[0].Ready {
		t.Error("Ready is true for a node whose condition says otherwise")
	}
}

// --- native sidecars -------------------------------------------------------

// Since Kubernetes 1.29 an init container with `restartPolicy: Always` is a
// sidecar: it never exits and its request is reserved for the pod's whole life.
// The package read `Spec.Containers` alone, so on an Istio or Linkerd or
// vault-agent fleet the container most often copy-pasted with the wrong request
// was the one container the page could not see.
func TestANativeSidecarIsSizedLikeAnyOtherContainer(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	never := corev1.ContainerRestartPolicy("")

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "pod-1"},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{
				// A real init container: it has exited by now and its request is
				// released, so it must not be counted.
				{Name: "migrate", RestartPolicy: &never},
				{Name: "istio-proxy", RestartPolicy: &always},
			},
			Containers: []corev1.Container{{Name: "app"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			InitContainerStatuses: []corev1.ContainerStatus{
				{Name: "istio-proxy", RestartCount: 2, LastTerminationState: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}}},
			},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app"}},
		},
	}

	got := toPod(pod, map[string]Usage{}, time.Now())

	names := make([]string, len(got.Containers))
	for i, c := range got.Containers {
		names[i] = c.Name
	}
	if len(names) != 2 || names[0] != "istio-proxy" || names[1] != "app" {
		t.Fatalf("containers = %v, want [istio-proxy app] — the sidecar is sized, the init "+
			"container is not, and the sidecar comes first because that is where it starts", names)
	}

	// And the reason it matters most: the OOMKilled refusal reaches it. A sidecar
	// the kernel keeps killing whose flag never gets set is a container the
	// package will happily tell you to shrink.
	if !got.Containers[0].OOMKilled {
		t.Error("the sidecar's OOMKilled flag was not read from InitContainerStatuses, so the " +
			"headline refusal does not protect the one container this fix is about")
	}
}

func TestANodeCountsItsSidecarsCommitment(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	pod := summaryPod("prod", "a", "node-1", corev1.PodRunning, summaryContainer("app", 500, 0, 0, 0))
	sidecar := summaryContainer("istio-proxy", 100, 0, 0, 0)
	sidecar.RestartPolicy = &always
	pod.Spec.InitContainers = []corev1.Container{sidecar}

	got := SummariseNodes([]corev1.Node{summaryNode("node-1", true, 4000, 4096)},
		[]corev1.Pod{pod}, map[string]Usage{})[0]

	if got.RequestedCPU != 600 {
		t.Errorf("requested = %dm, want 600m — 500m for the app and 100m for the sidecar. "+
			"Sixty of these on a node is six cores a card would not draw", got.RequestedCPU)
	}
	if got.Containers != 2 {
		t.Errorf("containers = %d, want 2", got.Containers)
	}
}

// --- a workload that could not be sized does not vanish ----------------------

// group skips any pod that is not Running, which is right — there is no usage to
// read. What was wrong is that it did so in silence, while summary.go counted
// those same pods' requests: two halves of one package disagreeing about which
// pods exist, with nothing on the page saying a workload had been left out.
//
// The state that matters is Pending, and specifically **Pending because the
// request exceeds every node** — the most right-sizing-relevant thing a pod can
// be, and it appeared nowhere.
func TestAPodThatCouldNotBeSizedIsCounted(t *testing.T) {
	pods := []corev1.Pod{
		summaryPod("prod", "running", "node-1", corev1.PodRunning, summaryContainer("app", 100, 0, 0, 0)),
		summaryPod("prod", "unschedulable", "", corev1.PodPending, summaryContainer("app", 64000, 0, 0, 0)),
		// A finished pod holds nothing and is not a gap: it is not counted.
		summaryPod("prod", "done", "node-1", corev1.PodSucceeded, summaryContainer("app", 100, 0, 0, 0)),
	}

	_, skipped := group(pods, nil, map[string]Usage{}, time.Now())

	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 — the Pending pod, and not the Succeeded one, which gave "+
			"its reservation back", skipped)
	}
}

func TestATerminatingPodIsNotReportedAsAGap(t *testing.T) {
	pod := summaryPod("prod", "going", "node-1", corev1.PodRunning, summaryContainer("app", 100, 0, 0, 0))
	now := metav1.Now()
	pod.DeletionTimestamp = &now

	workloads, skipped := group([]corev1.Pod{pod}, nil, map[string]Usage{}, time.Now())

	if len(workloads) != 0 {
		t.Errorf("got %d workloads, want none — a terminating pod is about to stop having usage", len(workloads))
	}
	if skipped != 0 {
		t.Error("a terminating pod was reported as a workload that could not be sized. It is not a " +
			"gap in the reading; it is a pod on its way out, and saying so would put a caveat on " +
			"every page during every rollout")
	}
}

// --- what a node card says about the machine ---------------------------------
//
// None of it is decoration: a node at 92 % of its memory reads completely
// differently once you can see that it is the control plane, that it was
// created four hours ago, or that it carries a taint nothing tolerates.

// Both label spellings, because reading only the modern one draws a whole fleet
// as "worker" — including its control plane, which is the one node an operator
// most needs to recognize before believing a capacity bar.
func TestANodesRoleIsReadFromEitherLabelSpelling(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"the modern label", map[string]string{"node-role.kubernetes.io/control-plane": ""}, "control-plane"},
		{"the older one several managed offerings still write",
			map[string]string{"kubernetes.io/role": "master"}, "master"},
		{"no label at all is a worker, said rather than left blank", nil, "worker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := summaryNode("n1", true, 4000, 8192)
			node.Labels = tc.labels

			got := summariseNodes([]corev1.Node{node}, nil, nil, time.Now())
			if len(got) != 1 || len(got[0].Roles) != 1 || got[0].Roles[0] != tc.want {
				t.Errorf("roles = %v, want [%s]", got[0].Roles, tc.want)
			}
		})
	}
}

// The kubelet's own alarms. A node under disk pressure explains a page full of
// evictions that otherwise reads as an application problem, and it is already in
// the object that was listed to get the capacity.
func TestTheKubeletsPressuresReachTheCard(t *testing.T) {
	node := summaryNode("n1", true, 4000, 8192)
	node.Status.Conditions = append(node.Status.Conditions,
		corev1.NodeCondition{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue},
		corev1.NodeCondition{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse},
		corev1.NodeCondition{Type: corev1.NodePIDPressure, Status: corev1.ConditionTrue},
	)
	node.Spec.Taints = []corev1.Taint{{
		Key: "node.kubernetes.io/unschedulable", Effect: corev1.TaintEffectNoSchedule,
	}}

	got := summariseNodes([]corev1.Node{node}, nil, nil, time.Now())[0]

	if len(got.Pressures) != 2 {
		t.Errorf("pressures = %v, want the two that are True and not the one that is False",
			got.Pressures)
	}
	if len(got.Taints) != 1 || got.Taints[0].Effect != "NoSchedule" {
		t.Errorf("taints = %+v, want one NoSchedule", got.Taints)
	}
}

// A pod ceiling nothing else on this page would show: a node at 108 of 110 pods
// is full whatever its CPU bar says.
func TestANodeReportsItsPodCeiling(t *testing.T) {
	node := summaryNode("n1", true, 4000, 8192)
	node.Status.Allocatable[corev1.ResourcePods] = resource.MustParse("110")
	node.CreationTimestamp = metav1.NewTime(time.Now().Add(-72 * time.Hour))

	got := summariseNodes([]corev1.Node{node}, nil, nil, time.Now())[0]

	if got.MaxPods != 110 {
		t.Errorf("MaxPods = %d, want 110", got.MaxPods)
	}
	if got.Age < 71*time.Hour || got.Age > 73*time.Hour {
		t.Errorf("Age = %s, want about 72h", got.Age)
	}
}

// Measured pods first, and then a separate ranking of the unmeasured ones.
//
// One merged ranking is the bug: a request and a usage are different
// quantities, so a pod that reserved four cores and uses none would sort above
// one measured at three, and the top of a list headed "top pods" would not be a
// list of the busiest pods at all.
func TestTheBusiestPodsAreRankedOnMeasurementsFirst(t *testing.T) {
	node := summaryNode("n1", true, 8000, 16384)
	pods := []corev1.Pod{
		summaryPod("web", "reserver", "n1", corev1.PodRunning, summaryContainer("app", 4000, 512, 0, 0)),
		summaryPod("web", "busy", "n1", corev1.PodRunning, summaryContainer("app", 500, 512, 0, 0)),
		summaryPod("web", "idle", "n1", corev1.PodRunning, summaryContainer("app", 500, 512, 0, 0)),
	}
	usage := map[string]Usage{
		"web/busy/app": {CPUMillicores: 3000, MemoryBytes: 100},
		"web/idle/app": {CPUMillicores: 10, MemoryBytes: 100},
	}

	got := summariseNodes([]corev1.Node{node}, pods, usage, time.Now())[0]

	var order []string
	for _, p := range got.TopPods {
		order = append(order, p.Name)
	}
	want := []string{"busy", "idle", "reserver"}
	for i := range want {
		if i >= len(order) || order[i] != want[i] {
			t.Fatalf("top pods are %v, want %v. `reserver` has the largest request and no "+
				"measurement; putting it first labels a reservation as the busiest pod", order, want)
		}
	}
	if got.TotalPods != 3 {
		t.Errorf("TotalPods = %d, want 3 — the count is what lets the card say \"top 10 of 34\"",
			got.TotalPods)
	}
	if !got.TopPods[0].Measured || got.TopPods[2].Measured {
		t.Error("the Measured flag does not survive into the card, so a bar drawn from a " +
			"request would be colored as a measurement")
	}
}

package overview

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/resources"
)

// A fixed clock, so an age is testable at all.
var clock = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

func client(objects ...runtime.Object) *cluster.Client {
	return &cluster.Client{Context: "test", Clients: fake.NewClientset(objects...)}
}

// refuses builds a client whose named list call fails, the way a service account
// without the verb or an expired SSO session makes it fail.
func refuses(resource string, objects ...runtime.Object) *cluster.Client {
	set := fake.NewClientset(objects...)
	set.PrependReactor("list", resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: User cannot list resource \"" + resource + "\"\nsecond line")
	})
	return &cluster.Client{Context: "test", Clients: set}
}

func i32(v int32) *int32 { return &v }

func node(name string, cpu, memory, pods string, conditions ...corev1.NodeCondition) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			CreationTimestamp: metav1.NewTime(clock.Add(-48 * time.Hour)),
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(cpu),
				corev1.ResourceMemory: resource.MustParse(memory),
				corev1.ResourcePods:   resource.MustParse(pods),
			},
			Conditions: conditions,
			NodeInfo:   corev1.NodeSystemInfo{KubeletVersion: "v1.31.4"},
		},
	}
}

func ready(status corev1.ConditionStatus) corev1.NodeCondition {
	return corev1.NodeCondition{Type: corev1.NodeReady, Status: status}
}

func requesting(cpu, memory string) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{Requests: corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(cpu),
		corev1.ResourceMemory: resource.MustParse(memory),
	}}
}

func pod(name, nodeName string, phase corev1.PodPhase, containers ...corev1.Container) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       corev1.PodSpec{NodeName: nodeName, Containers: containers},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

// --- the arithmetic --------------------------------------------------------------

func TestRequestedIsSummedAgainstAllocatableAndNotCapacity(t *testing.T) {
	// The node has 8 cores of capacity and 4 of allocatable. Sizing against
	// capacity is how a node reads "25% used" while the scheduler refuses it.
	n := node("worker-1", "4", "8Gi", "110", ready(corev1.ConditionTrue))
	n.Status.Capacity = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")}

	overview, err := Cluster(context.Background(), accesstest.Allow(), client(
		n,
		pod("a", "worker-1", corev1.PodRunning, corev1.Container{Name: "c", Resources: requesting("500m", "1Gi")}),
		pod("b", "worker-1", corev1.PodRunning, corev1.Container{Name: "c", Resources: requesting("1500m", "2Gi")}),
	), nil)
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}

	if got, want := overview.CPU.Total, int64(4000); got != want {
		t.Errorf("allocatable CPU = %dm, want %dm — capacity must not be read in its place", got, want)
	}
	if got, want := overview.CPU.Used, int64(2000); got != want {
		t.Errorf("requested CPU = %dm, want %dm", got, want)
	}
	if got, want := overview.Memory.Used, int64(3)*(1<<30); got != want {
		t.Errorf("requested memory = %d, want %d", got, want)
	}
	if got, want := overview.CPU.Percent(), 50; got != want {
		t.Errorf("CPU at %d%%, want %d%%", got, want)
	}
	if got, want := overview.Pods.Used, int64(2); got != want {
		t.Errorf("pod slots used = %d, want %d", got, want)
	}
	if got, want := overview.Pods.Total, int64(110); got != want {
		t.Errorf("pod slots = %d, want %d", got, want)
	}
}

func TestFinishedPodsHoldNoCapacity(t *testing.T) {
	// A Job that ran last week leaves its pod behind in phase Succeeded. Counting
	// it is how a cluster reports 96% requested while every node sits idle.
	overview, err := Cluster(context.Background(), accesstest.Allow(), client(
		node("worker-1", "4", "8Gi", "110", ready(corev1.ConditionTrue)),
		pod("live", "worker-1", corev1.PodRunning, corev1.Container{Name: "c", Resources: requesting("500m", "1Gi")}),
		pod("done", "worker-1", corev1.PodSucceeded, corev1.Container{Name: "c", Resources: requesting("2", "4Gi")}),
		pod("dead", "worker-1", corev1.PodFailed, corev1.Container{Name: "c", Resources: requesting("2", "4Gi")}),
		// Unscheduled: it costs no node anything, because it is on no node.
		pod("waiting", "", corev1.PodPending, corev1.Container{Name: "c", Resources: requesting("2", "4Gi")}),
	), nil)
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}

	if got, want := overview.CPU.Used, int64(500); got != want {
		t.Errorf("requested CPU = %dm, want %dm — only the live pod costs anything", got, want)
	}
	if got, want := overview.Pods.Used, int64(1); got != want {
		t.Errorf("pod slots used = %d, want %d", got, want)
	}
}

func TestInitContainerIsTheLargestAndNotTheSum(t *testing.T) {
	// Init containers run one at a time, before the rest. The pod needs the
	// biggest init request or the sum of the regular ones, whichever is larger —
	// never both. Summing overstates every pod with a migration step.
	p := pod("migrating", "worker-1", corev1.PodRunning,
		corev1.Container{Name: "app", Resources: requesting("250m", "256Mi")},
		corev1.Container{Name: "helper", Resources: requesting("250m", "256Mi")},
	)
	p.Spec.InitContainers = []corev1.Container{
		{Name: "migrate", Resources: requesting("2", "1Gi")},
		{Name: "wait", Resources: requesting("100m", "64Mi")},
	}

	got := podRequests(p)
	if want := int64(2000); got.cpuMilli != want {
		t.Errorf("CPU request = %dm, want %dm (the largest init container, not the sum of everything)", got.cpuMilli, want)
	}
	if want := int64(1) << 30; got.memoryBytes != want {
		t.Errorf("memory request = %d, want %d", got.memoryBytes, want)
	}
}

func TestNativeSidecarAddsToTheRequestRatherThanCompetingWithIt(t *testing.T) {
	// An init container with restartPolicy: Always keeps running alongside the
	// regular containers, so it adds on both sides of the comparison. Treating it
	// as an ordinary init container understates every mesh-injected pod.
	always := corev1.ContainerRestartPolicyAlways
	p := pod("meshed", "worker-1", corev1.PodRunning,
		corev1.Container{Name: "app", Resources: requesting("250m", "256Mi")},
	)
	p.Spec.InitContainers = []corev1.Container{
		{Name: "proxy", RestartPolicy: &always, Resources: requesting("100m", "128Mi")},
	}

	if got, want := podRequests(p).cpuMilli, int64(350); got != want {
		t.Errorf("CPU request = %dm, want %dm (app plus sidecar)", got, want)
	}
}

func TestCapacityGradesAgainstScheduling(t *testing.T) {
	for _, tc := range []struct {
		used, total int64
		want        string
	}{
		{0, 4000, ClassOK},
		{3000, 4000, ClassWarn},
		{3700, 4000, ClassBad},
		// Over-committed clusters exist, and the percentage must not be capped
		// with the shape.
		{5000, 4000, ClassBad},
	} {
		c := Capacity{Name: "CPU", Unit: UnitCores, Used: tc.used, Total: tc.total}
		if got := c.Class(); got != tc.want {
			t.Errorf("%dm of %dm graded %q, want %q", tc.used, tc.total, got, tc.want)
		}
	}

	over := Capacity{Name: "CPU", Unit: UnitCores, Used: 5000, Total: 4000}
	if got, want := over.Percent(), 125; got != want {
		t.Errorf("over-committed reads %d%%, want %d%% — capping it hides the reading that matters", got, want)
	}
}

func TestCapacityWithNoTotalSaysNothingRatherThanZero(t *testing.T) {
	c := Capacity{Name: "CPU", Unit: UnitCores}
	if c.Known() {
		t.Fatal("a capacity with no total claims to be known")
	}
	if got := c.Text(); got != missing {
		t.Errorf("Text() = %q, want %q — a total nobody reported must not print as 0", got, missing)
	}
	if got := c.Class(); got != ClassNone {
		t.Errorf("Class() = %q, want no grade at all", got)
	}
}

// --- empty is not failed -----------------------------------------------------------

func TestUnreachableClusterErrorsRatherThanDrawingZero(t *testing.T) {
	// A cache that reports "no pods" for a cluster running 429 of them leaves
	// the operator unable to tell which of the two they are looking at. A
	// cluster that cannot be read is an error, never a page of zeros.
	_, err := Cluster(context.Background(), accesstest.Allow(), refuses("nodes"), nil)
	if err == nil {
		t.Fatal("a cluster whose node list failed returned an overview instead of an error")
	}
	if !strings.Contains(err.Error(), "test") {
		t.Errorf("error %q does not name the cluster it is about", err)
	}
}

func TestEmptyClusterAnswersWithoutAnError(t *testing.T) {
	overview, err := Cluster(context.Background(), accesstest.Allow(), client(), nil)
	if err != nil {
		t.Fatalf("an empty cluster is not a broken one: %v", err)
	}
	if overview.Total != 0 || overview.Healthy != 0 {
		t.Errorf("expected no nodes, got %d", overview.Total)
	}
	if len(overview.Degraded) != 0 {
		t.Errorf("nothing failed, yet the page reports %v", overview.Degraded)
	}
}

func TestARefusedPodListDegradesTheRequestsAndKeepsTheNodes(t *testing.T) {
	overview, err := Cluster(context.Background(), accesstest.Allow(), refuses("pods",
		node("worker-1", "4", "8Gi", "110", ready(corev1.ConditionTrue)),
	), nil)
	if err != nil {
		t.Fatalf("the nodes answered; the page is still worth having: %v", err)
	}
	if overview.Total != 1 {
		t.Errorf("node count = %d, want 1", overview.Total)
	}
	if overview.CPU.Known() {
		t.Error("requested CPU is reported as a figure although the pod list failed — a zero here is a lie")
	}
	if len(overview.Degraded) == 0 {
		t.Fatal("a panel failed and the page does not say so")
	}
	var named bool
	for _, d := range overview.Degraded {
		if strings.Contains(d.Panel, "Requested") {
			named = true
			if strings.Contains(d.Reason, "\n") {
				t.Errorf("reason %q carries more than the first line", d.Reason)
			}
		}
	}
	if !named {
		t.Errorf("Degraded = %v, want the requests panel named", overview.Degraded)
	}
}

func TestWorkloadsWithoutPodsIsAnErrorAndNotAnEmptyPage(t *testing.T) {
	if _, err := Workloads(context.Background(), accesstest.Allow(), refuses("pods"), "", nil); err == nil {
		t.Fatal("a workloads page that cannot see the pods returned a page anyway")
	}
}

func TestWorkloadsSurvivesOneKindItCannotList(t *testing.T) {
	out, err := Workloads(context.Background(), accesstest.Allow(), refuses("jobs",
		pod("a", "worker-1", corev1.PodRunning, corev1.Container{Name: "c"}),
	), "", nil)
	if err != nil {
		t.Fatalf("Workloads: %v", err)
	}
	for _, k := range out.Kinds {
		if k.Kind == "Jobs" {
			t.Error("a kind whose list failed is counted as zero — which says there are none, and there may be thousands")
		}
	}
	if len(out.Degraded) == 0 {
		t.Fatal("the Jobs panel failed and the page does not say so")
	}
}

// A cluster that does not serve CronJobs has none to be missing: no panel is
// listed as not shown and no CronJob count is drawn.
func TestAClusterThatDoesNotServeCronJobsListsNoMissingPanel(t *testing.T) {
	set := fake.NewClientset(pod("a", "worker-1", corev1.PodRunning, corev1.Container{Name: "c"}))
	set.PrependReactor("list", "cronjobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "batch", Resource: "cronjobs"}, "")
	})
	out, err := Workloads(context.Background(), accesstest.Allow(), &cluster.Client{Context: "test", Clients: set}, "", nil)
	if err != nil {
		t.Fatalf("Workloads: %v", err)
	}
	for _, d := range out.Degraded {
		if d.Panel == PanelCronJobs {
			t.Errorf("the CronJobs panel is listed as not shown: %+v", d)
		}
	}
	for _, k := range out.Kinds {
		if k.Kind == "CronJobs" {
			t.Errorf("a CronJob count is drawn for a cluster that serves none: %+v", k)
		}
	}
}

// A refused CronJob list is still reported: only "not served" is quiet.
func TestARefusedCronJobListIsStillReported(t *testing.T) {
	set := fake.NewClientset(pod("a", "worker-1", corev1.PodRunning, corev1.Container{Name: "c"}))
	set.PrependReactor("list", "cronjobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "batch", Resource: "cronjobs"}, "", errors.New("no"))
	})
	out, err := Workloads(context.Background(), accesstest.Allow(),
		&cluster.Client{Context: "test", Clients: set}, "", nil)
	if err != nil {
		t.Fatalf("Workloads: %v", err)
	}
	found := false
	for _, d := range out.Degraded {
		found = found || d.Panel == PanelCronJobs
	}
	if !found {
		t.Errorf("a refused CronJob list is not reported: %+v", out.Degraded)
	}
	for _, k := range out.Kinds {
		if k.Kind == "CronJobs" {
			t.Errorf("a refused kind is counted: %+v", k)
		}
	}
}

// --- nodes that must be impossible to miss -------------------------------------------

func TestMemoryPressureAndNotReadyBecomeAlerts(t *testing.T) {
	overview, err := Cluster(context.Background(), accesstest.Allow(), client(
		node("healthy", "4", "8Gi", "110", ready(corev1.ConditionTrue)),
		node("gone", "4", "8Gi", "110", ready(corev1.ConditionFalse)),
		node("squeezed", "4", "8Gi", "110",
			ready(corev1.ConditionTrue),
			corev1.NodeCondition{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue, Reason: "KubeletHasInsufficientMemory"},
		),
	), nil)
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}

	if overview.AllWell() {
		t.Fatal("a cluster with a NotReady node reports itself healthy")
	}
	if len(overview.Alerts) != 2 {
		t.Fatalf("alerts = %d, want 2 (one NotReady, one MemoryPressure): %+v", len(overview.Alerts), overview.Alerts)
	}
	// One of the three is Ready with nothing wrong with it. The node under memory
	// pressure reports Ready and is not healthy, and the headline must not say
	// otherwise.
	if got, want := overview.Healthy, 1; got != want {
		t.Errorf("healthy nodes = %d, want %d", got, want)
	}
}

func TestCordonedNodeSaysSoWithoutBeingCalledBroken(t *testing.T) {
	n := node("drained", "4", "8Gi", "110", ready(corev1.ConditionTrue))
	n.Spec.Unschedulable = true

	status, class := nodeStatus(n)
	if !strings.Contains(status, "SchedulingDisabled") {
		t.Errorf("status = %q, want the cordon named", status)
	}
	if class != ClassWarn {
		t.Errorf("class = %q, want %q — a cordoned node is a decision, not a fault", class, ClassWarn)
	}
}

func TestUnknownNodeCountsAsBroken(t *testing.T) {
	// Ready=Unknown is a kubelet that stopped reporting — a machine that went
	// away, which is worse than one that answered "no".
	_, class := nodeStatus(node("silent", "4", "8Gi", "110", ready(corev1.ConditionUnknown)))
	if class != ClassBad {
		t.Errorf("class = %q, want %q", class, ClassBad)
	}
}

// --- workloads ----------------------------------------------------------------------

func TestCrashLoopingPodIsNotCountedAsRunning(t *testing.T) {
	// A pod in CrashLoopBackOff has phase Running. Counting it as healthy is read
	// as evidence that it is fine, which is how an incident gets twenty minutes
	// longer.
	crashing := pod("crasher", "worker-1", corev1.PodRunning, corev1.Container{Name: "c"})
	crashing.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:         "c",
		Ready:        false,
		RestartCount: 7,
		State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"},
		},
	}}
	fine := pod("fine", "worker-1", corev1.PodRunning, corev1.Container{Name: "c"})
	fine.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "c", Ready: true}}

	out, err := Workloads(context.Background(), accesstest.Allow(), client(crashing, fine), "", nil)
	if err != nil {
		t.Fatalf("Workloads: %v", err)
	}

	var pods KindSummary
	for _, k := range out.Kinds {
		if k.Kind == "Pods" {
			pods = k
		}
	}
	if pods.Total != 2 || pods.Healthy != 1 {
		t.Errorf("pods = %d total, %d healthy; want 2 and 1", pods.Total, pods.Healthy)
	}

	if len(out.Trouble) != 1 || out.Trouble[0].Detail != "CrashLoopBackOff" {
		t.Errorf("Trouble = %+v, want the waiting reason and not the phase", out.Trouble)
	}
	if len(out.Restarting) != 1 || out.Restarting[0].Reason != "OOMKilled" {
		t.Errorf("Restarting = %+v, want the last termination reason", out.Restarting)
	}

	// The phase breakdown must not fold a crash-looping pod into Running either.
	var notReady int
	for _, s := range out.Phases {
		if s.Label == "Not ready" {
			notReady = s.Value
		}
	}
	if notReady != 1 {
		t.Errorf("phases = %+v, want one pod counted as not ready", out.Phases)
	}
}

func TestNilReplicasMeansOneAndNotNone(t *testing.T) {
	// A Deployment with no explicit replicas wants one. Reading nil as zero makes
	// every defaulted Deployment look satisfied by nothing at all.
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 0},
	}
	if deploymentReady(d) {
		t.Fatal("a Deployment with no ready replicas is reported ready")
	}

	scaled := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "batch", Namespace: "default"},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(0)},
	}
	if !deploymentReady(scaled) {
		t.Error("a Deployment deliberately scaled to zero is reported broken — coloring that red teaches the reader to ignore red")
	}
}

func TestPhaseBreakdownLeavesOutWhatIsNotThere(t *testing.T) {
	running := pod("a", "n", corev1.PodRunning, corev1.Container{Name: "c"})
	running.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "c", Ready: true}}

	segments := phaseBreakdown([]corev1.Pod{*running})
	if len(segments) != 1 || segments[0].Label != "Running" || segments[0].Value != 1 {
		t.Errorf("segments = %+v, want only Running — a zero segment still shows in the legend and reads as one", segments)
	}
}

// --- metrics: absent is not broken ---------------------------------------------------

func TestNoMetricsBackendIsNotAnError(t *testing.T) {
	av, cpu, memory := clusterHistory(context.Background(), nil, "test")
	if av.OK {
		t.Fatal("history reported available with no backend at all")
	}
	if !errors.Is(av.Err(), ErrUnavailable) {
		t.Errorf("Err() = %v, want it to wrap ErrUnavailable so a caller can tell absent from broken", av.Err())
	}
	if cpu.Unavailable == "" || memory.Unavailable == "" {
		t.Error("the series carry no reason, so a panel has nothing to print but a blank")
	}
	if len(cpu.Points) != 0 {
		t.Error("points were invented for a cluster with no metrics backend")
	}
}

func TestMissingMetricsServerLeavesTheLeaderboardEmptyAndTheRequestFine(t *testing.T) {
	// The fake client cannot make raw API requests, which is exactly the shape of
	// a cluster without metrics-server: no figures, and nothing wrong.
	out, err := Workloads(context.Background(), accesstest.Allow(), client(
		pod("a", "worker-1", corev1.PodRunning, corev1.Container{Name: "c"}),
	), "", nil)
	if err != nil {
		t.Fatalf("no metrics backend must not fail the page: %v", err)
	}
	if out.Usage.OK {
		t.Fatal("usage reported available")
	}
	if out.TopCPU.Unavailable == "" || out.TopMemory.Unavailable == "" {
		t.Error("the leaderboards carry no reason, so the panel can only show a blank")
	}
	if len(out.TopCPU.Pods) != 0 {
		t.Error("a leaderboard was invented without a backend")
	}
}

// stubMetrics answers with whatever the test wrote, so the parsing and the unit
// conversion can be pinned without a Prometheus.
type stubMetrics struct {
	rangeBody   []byte
	rangeErr    error
	instantBody []byte
	instantErr  error
}

func (s stubMetrics) Query(context.Context, string, string) ([]byte, error) {
	return s.instantBody, s.instantErr
}

func (s stubMetrics) QueryRange(context.Context, string, string, time.Time, time.Time, time.Duration) ([]byte, error) {
	return s.rangeBody, s.rangeErr
}

func TestPrometheusCoresBecomeMillicores(t *testing.T) {
	body := []byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[1755432000,"0.4"],[1755432060,"1.25"]]}]}}`)
	av, cpu, _ := clusterHistory(context.Background(), stubMetrics{rangeBody: body}, "test")
	if !av.OK {
		t.Fatalf("history unavailable: %s", av.Detail)
	}
	if len(cpu.Points) != 2 {
		t.Fatalf("points = %d, want 2", len(cpu.Points))
	}
	if got, want := cpu.Points[0].Value, 400.0; got != want {
		t.Errorf("first point = %v, want %v millicores — the table beside it counts in millicores", got, want)
	}
	if cpu.Points[0].At.IsZero() {
		t.Error("the sample carries no timestamp, so a gap in scraping would be drawn as a straight line")
	}
}

func TestAnEmptyPrometheusResultIsNotAFailedOne(t *testing.T) {
	body := []byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`)
	// The probe answers that the container series exist, so the window is the
	// reason; a probe with nothing readable to say is a failure to establish
	// why, and TestEveryCombinationOfTheTwoClusterLinesSaysOneTrueThing holds it.
	probe := exported(t, metricCPU, metricMemory)
	av, cpu, _ := clusterHistory(context.Background(), stubMetrics{rangeBody: body, instantBody: probe}, "test")
	if !av.OK {
		t.Errorf("an empty window reads as an unreachable backend: %s", av.Detail)
	}
	// Said as an empty window and not as a failure: Prometheus answered, it just
	// had nothing. The line carries the sentence, so the caption can say why.
	if cpu.Missing != MissingEmpty || cpu.Unavailable != emptyWindow {
		t.Errorf("CPU = %q (%v), want the empty window", cpu.Unavailable, cpu.Missing)
	}
	if len(cpu.Points) != 0 {
		t.Error("points appeared from an empty result")
	}
}

func TestAFailingPrometheusSaysWhatFailed(t *testing.T) {
	av, cpu, memory := clusterHistory(context.Background(),
		stubMetrics{rangeErr: errors.New("dial tcp: connection refused")}, "test")
	if av.OK {
		t.Fatal("a refused connection reported as available")
	}
	if !strings.Contains(cpu.Unavailable, "connection refused") || !strings.Contains(memory.Unavailable, "connection refused") {
		t.Errorf("neither series says what went wrong: %q / %q", cpu.Unavailable, memory.Unavailable)
	}
}

func TestPrometheusLeaderboardIsUsedWhenMetricsServerIsAbsent(t *testing.T) {
	// kube-prometheus-stack installs Prometheus and not metrics-server, so
	// without this fallback the best-instrumented clusters have the emptiest
	// panel.
	body := []byte(`{"status":"success","data":{"resultType":"vector","result":[` +
		`{"metric":{"namespace":"kube-system","pod":"coredns-1"},"value":[1755432000,"0.25"]}]}}`)

	out, err := Workloads(context.Background(), accesstest.Allow(), client(
		pod("a", "worker-1", corev1.PodRunning, corev1.Container{Name: "c"}),
	), "", stubMetrics{instantBody: body})
	if err != nil {
		t.Fatalf("Workloads: %v", err)
	}
	if !out.Usage.OK {
		t.Fatalf("usage unavailable although Prometheus answered: %s", out.Usage.Detail)
	}
	if len(out.TopCPU.Pods) != 1 || out.TopCPU.Pods[0].Name != "coredns-1" {
		t.Fatalf("TopCPU = %+v, want the pod Prometheus named", out.TopCPU.Pods)
	}
	if got, want := out.TopCPU.Pods[0].CPUMilli, int64(250); got != want {
		t.Errorf("CPU = %dm, want %dm", got, want)
	}
}

func TestBothBackendsFailingKeepsBothReasons(t *testing.T) {
	// Reporting only the second sends the operator to look at a Prometheus when
	// what he is actually missing is metrics-server.
	out, err := Workloads(context.Background(), accesstest.Allow(), client(
		pod("a", "worker-1", corev1.PodRunning, corev1.Container{Name: "c"}),
	), "", stubMetrics{instantErr: errors.New("dial tcp: connection refused")})
	if err != nil {
		t.Fatalf("two absent backends must not fail the page: %v", err)
	}
	if !strings.Contains(out.Usage.Detail, "metrics-server") || !strings.Contains(out.Usage.Detail, "Prometheus") {
		t.Errorf("Detail = %q, want both backends named", out.Usage.Detail)
	}
}

func TestRejectedQueryIsReportedAndNotParsedAsEmpty(t *testing.T) {
	_, err := parseRange([]byte(`{"status":"error","error":"parse error at char 3"}`))
	if err == nil {
		t.Fatal("a rejected query parsed as an empty series — which draws as a working, quiet cluster")
	}
	if !strings.Contains(err.Error(), "parse error") {
		t.Errorf("error %q loses what Prometheus said", err)
	}
}

// --- events -------------------------------------------------------------------------

func TestWarningEventsAreNewestFirstAndGraded(t *testing.T) {
	events := []corev1.Event{
		{
			ObjectMeta:     metav1.ObjectMeta{Name: "old", Namespace: "default"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "web-1"},
			Reason:         "Unhealthy",
			LastTimestamp:  metav1.NewTime(clock.Add(-30 * time.Minute)),
			Type:           corev1.EventTypeWarning,
		},
		{
			ObjectMeta:     metav1.ObjectMeta{Name: "new", Namespace: "default"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "web-2"},
			Reason:         "FailedScheduling",
			LastTimestamp:  metav1.NewTime(clock.Add(-2 * time.Minute)),
			Type:           corev1.EventTypeWarning,
		},
	}

	lines, _ := eventLines(events, clock, 10)
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	if lines[0].Reason != "FailedScheduling" {
		t.Errorf("first line is %q, want the newest event", lines[0].Reason)
	}
	if lines[0].Age != "2m" {
		t.Errorf("age = %q, want 2m", lines[0].Age)
	}
	if lines[0].Class != ClassBad {
		t.Errorf("FailedScheduling graded %q, want %q", lines[0].Class, ClassBad)
	}
	if lines[0].Count != 1 {
		t.Errorf("count = %d, want 1 — an event with no count field still happened once", lines[0].Count)
	}
}

func TestEventWithNoLastTimestampKeepsItsPlaceInTime(t *testing.T) {
	// Events written through events.k8s.io carry EventTime and no LastTimestamp.
	// Reading only the latter dates half a busy cluster's events to the zero
	// time, which sorts them to the bottom where nobody looks. Asserted through
	// the panel rather than the reading, which lives in internal/resources.
	e := corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "modern"},
		EventTime:  metav1.NewMicroTime(clock.Add(-time.Minute)),
		Type:       corev1.EventTypeWarning,
	}
	lines, _ := eventLines([]corev1.Event{e}, clock, 10)
	if len(lines) != 1 || lines[0].When.IsZero() {
		t.Fatal("an event with only EventTime is dated to the zero time")
	}
	if lines[0].Age != "1m" {
		t.Errorf("age = %q, want 1m", lines[0].Age)
	}
}

func TestNormalEventsAreFilteredOutEvenWhenTheServerIgnoresTheSelector(t *testing.T) {
	set := fake.NewClientset(
		&corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "chatter", Namespace: "default"}, Type: corev1.EventTypeNormal, Reason: "Started"},
		&corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "trouble", Namespace: "default"}, Type: corev1.EventTypeWarning, Reason: "BackOff"},
	)
	got, _, err := listWarningEvents(context.Background(),
		resources.Reader{Gate: accesstest.Allow(), Cluster: "test", Clients: set}, "")
	if err != nil {
		t.Fatalf("listWarningEvents: %v", err)
	}
	if len(got) != 1 || got[0].Reason != "BackOff" {
		t.Errorf("got %d events %+v, want only the warning", len(got), got)
	}
}

// --- formatting -----------------------------------------------------------------------

func TestFiguresReadTheWayAnOperatorWritesThem(t *testing.T) {
	for _, tc := range []struct {
		got, want string
	}{
		{formatCores(250), "250m"},
		{formatCores(1000), "1"},
		{formatCores(2500), "2.5"},
		{formatBytes(512), "512 B"},
		{formatBytes(1 << 20), "1 MiB"},
		{formatBytes(3*(1<<30) + (1 << 29)), "3.5 GiB"},
		// This page's figures are the compact ones — "8 GiB", not "8.0 GiB" —
		// which is a choice at the call (textutil.BytesCompact), and this line
		// keeps it that way.
		{formatBytes(8 << 30), "8 GiB"},
		// Two edges a formatter must handle: a petabyte must not panic, and a
		// negative must not print raw, or a node's memory could read "-1 TiB"
		// under a chart.
		{formatBytes(1 << 50), "1024 TiB"},
		{formatBytes(-(1 << 40)), "0 B"},
	} {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
}

// Requests that could not be computed say so; a cluster that genuinely runs
// nothing still reads zero.
//
// Those are two different answers and must not share one word. A figure
// summarized from the zero value of a map lookup for a refused pod list -
// `Used: 0, Total: allocatable` is `Known()` - prints "0%" and "0m of 8 cores"
// graded ClassOK: confident green zeros under a sentence saying the figure
// could not be obtained, an empty answer rendered as a measured one. The rule
// holds for the cluster-wide figures the Capacity rings draw.
//
// The middle case is the one a careless fix breaks. A node running nothing is a
// measurement: the cluster answered, and "nothing is scheduled here" is the
// answer.
func TestRequestsThatCouldNotBeReadSaySoRatherThanZero(t *testing.T) {
	// 4 cores, 8Gi, 110 pods of allocatable, and one pod on it asking for 1 core.
	worker := func() *corev1.Node { return node("worker-1", "4", "8Gi", "110", ready(corev1.ConditionTrue)) }
	busy := pod("api", "worker-1", corev1.PodRunning, corev1.Container{Name: "app", Resources: requesting("1", "1Gi")})

	for _, c := range []struct {
		name  string
		build func() *cluster.Client
		known bool
		// percent is only read when known.
		percent int
		class   string
	}{
		{
			name:    "the pod list answered and this node has pods",
			build:   func() *cluster.Client { return client(worker(), busy) },
			known:   true,
			percent: 25,
			class:   ClassOK,
		},
		{
			// A real zero, and it must survive every fix to the case below it.
			name:    "the pod list answered and this node genuinely has none",
			build:   func() *cluster.Client { return client(worker()) },
			known:   true,
			percent: 0,
			class:   ClassOK,
		},
		{
			name:  "the pod list failed",
			build: func() *cluster.Client { return refuses("pods", worker()) },
			known: false,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := Cluster(context.Background(), accesstest.Allow(), c.build(), nil)
			if err != nil {
				t.Fatalf("the nodes answered; the page is still worth having: %v", err)
			}
			for _, cell := range []Capacity{got.CPU, got.Memory, got.Pods} {
				if cell.Known() != c.known {
					t.Errorf("%s: %s reports Known()=%v, want %v (Used=%d Total=%d)",
						c.name, cell.Name, cell.Known(), c.known, cell.Used, cell.Total)
					continue
				}
				if !c.known {
					// The three readings a template can take. Every one of them has
					// to refuse, because the row is drawn from whichever the
					// template happens to call.
					if cell.Text() != missing {
						t.Errorf("%s: %s reads %q, want %q — a figure nobody could compute must not print as 0",
							c.name, cell.Name, cell.Text(), missing)
					}
					if cell.Class() != ClassNone {
						t.Errorf("%s: %s is graded %q; an uncomputed figure graded %q is a green zero",
							c.name, cell.Name, cell.Class(), ClassOK)
					}
					if !strings.Contains(cell.Label(), "no capacity") {
						t.Errorf("%s: %s says %q to a screen reader, which does not say the figure is missing",
							c.name, cell.Name, cell.Label())
					}
				}
			}

			if !c.known {
				return
			}
			if percent := got.CPU.Percent(); percent != c.percent {
				t.Errorf("%s: CPU reads %d%%, want %d%%", c.name, percent, c.percent)
			}
			if class := got.CPU.Class(); class != c.class {
				t.Errorf("%s: CPU is graded %q, want %q", c.name, class, c.class)
			}
			// The total is a real reading of the node.
			if got.CPU.Total != 4000 {
				t.Errorf("%s: allocatable CPU = %d, want 4000 — the node answered", c.name, got.CPU.Total)
			}
			if got.CPU.Text() == missing {
				t.Errorf("%s: CPU reads %q; a node the cluster could answer for has a figure", c.name, missing)
			}
		})
	}
}

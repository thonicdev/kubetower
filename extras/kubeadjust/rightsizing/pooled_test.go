package rightsizing

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A week of usage is a workload's, not its pods': the series of the replicas a
// workload had before a rollout are pooled with its running ones, when - and
// only when - they can be tied to it and to nothing else.

// between is the core's step from sample from to sample to (exclusive), at v.
func between(from, to int, v float64) []Point {
	out := make([]Point, 0, to-from)
	for i := from; i < to; i++ {
		out = append(out, Point{T: t0 + float64(i*stepEvery), V: v})
	}
	return out
}

func controller(kind, name string) metav1.OwnerReference {
	yes := true
	return metav1.OwnerReference{Kind: kind, Name: name, Controller: &yes}
}

// live is one running replica in shop, measured.
func live(pod string, c Container) Pod {
	return Pod{Name: pod, Namespace: "shop", Containers: []Container{c}}
}

func shopWorkload(kind, name string, pods ...Pod) Workload {
	return Workload{Kind: kind, Name: name, Namespace: "shop", Pods: pods}
}

func cpuSeries(pod string, points []Point) ContainerHistory {
	return ContainerHistory{Namespace: "shop", Pod: pod, Container: "app", CPU: points}
}

func cpuReduction(t *testing.T, workloads []Workload, hist []ContainerHistory) Suggestion {
	t.Helper()
	s, ok := find(Compute(workloads, hist, nil), byResourceAction("CPU", "Reduce request"))
	if !ok {
		t.Fatal("no CPU 'Reduce request' produced")
	}
	return s
}

func TestADeploymentKeepsItsWeekAcrossARollout(t *testing.T) {
	// Rolled out on day four: the old ReplicaSet's pod ran days zero to four,
	// the new one's since. Each alone is under a week; together they are one.
	rollout := week * 4 / 7
	c := overCPU()
	c.Starts = []float64{t0 + float64(rollout*stepEvery)}
	hist := []ContainerHistory{
		cpuSeries("api-5b7f9c-xzbcd", between(0, rollout, 300)),
		cpuSeries("api-6c8d4f-bcdfg", between(rollout, week, 300)),
	}
	owners := map[string]metav1.OwnerReference{
		"ReplicaSet/shop/api-5b7f9c": controller("Deployment", "api"),
		"ReplicaSet/shop/api-6c8d4f": controller("Deployment", "api"),
	}
	alone := cpuReduction(t, []Workload{shopWorkload("Deployment", "api", live("api-6c8d4f-bcdfg", c))}, hist)
	if alone.Confidence != ConfidenceMedium {
		t.Fatalf("the new replica alone is %q; the test needs it under a week", alone.Confidence)
	}

	workloads := []Workload{shopWorkload("Deployment", "api", live("api-6c8d4f-bcdfg", c))}
	attachFormer(workloads, nil, owners, hist)
	if got := workloads[0].Former; len(got) != 1 || got[0] != "api-5b7f9c-xzbcd" {
		t.Fatalf("former = %v, want the old ReplicaSet's pod", got)
	}
	pooled := cpuReduction(t, workloads, hist)
	if pooled.Confidence != ConfidenceHigh || pooled.InsufficientData {
		t.Errorf("pooled across the rollout: %q, insufficient %v; want high", pooled.Confidence, pooled.InsufficientData)
	}
	// And the readings, which share the pool, say the same week.
	if r := Readings(workloads, hist); !r[0].Containers[0].CPU.Windowed || r[0].Containers[0].CPU.Insufficient {
		t.Errorf("the reading is not windowed from the pool: %+v", r[0].Containers[0].CPU)
	}
}

func TestACronJobPoolsItsJobs(t *testing.T) {
	c := overCPU()
	hist := []ContainerHistory{
		cpuSeries("backup-28934400-zxcvb", between(0, week/3, 300)),          // a Job still listed
		cpuSeries("backup-28932000-qwrtz", between(week/3, 2*week/3, 300)),   // a Job gone
		cpuSeries("backup-28935840-bcdfg", between(2*week/3, week, 300)),     // running now
		cpuSeries("backup-reports-28932000-qwrtz", between(0, week, 999999)), // another CronJob's
	}
	owners := map[string]metav1.OwnerReference{
		"Job/shop/backup-28934400": controller("CronJob", "backup"),
		"Job/shop/backup-28935840": controller("CronJob", "backup"),
	}
	workloads := []Workload{shopWorkload("CronJob", "backup", live("backup-28935840-bcdfg", c))}
	attachFormer(workloads, nil, owners, hist)
	if got := strings.Join(workloads[0].Former, ","); got != "backup-28932000-qwrtz,backup-28934400-zxcvb" {
		t.Errorf("former = %q, want both earlier Jobs' pods and no other CronJob's", got)
	}
	if s := cpuReduction(t, workloads, hist); s.Confidence != ConfidenceHigh {
		t.Errorf("confidence %q, want high from three Jobs over the week", s.Confidence)
	}
}

func TestAStatefulSetAndADaemonSetPoolTheirFormerPods(t *testing.T) {
	c := overCPU()
	hist := []ContainerHistory{
		cpuSeries("db-1", between(0, week, 300)),           // scaled down since
		cpuSeries("agent-zxcvb", between(0, week, 300)),    // replaced by a rollout
		cpuSeries("agent-ui-bcdfg", between(0, week, 300)), // a Deployment's? no: a name no rule ties to agent
	}
	workloads := []Workload{
		shopWorkload("StatefulSet", "db", live("db-0", c)),
		shopWorkload("DaemonSet", "agent", live("agent-bcdfg", c)),
	}
	attachFormer(workloads, nil, nil, hist)
	if got := strings.Join(workloads[0].Former, ","); got != "db-1" {
		t.Errorf("StatefulSet former = %q, want db-1", got)
	}
	if got := strings.Join(workloads[1].Former, ","); got != "agent-zxcvb" {
		t.Errorf("DaemonSet former = %q, want agent-zxcvb only", got)
	}
}

func TestASeriesThatCannotBeTiedToOneWorkloadIsNotUsed(t *testing.T) {
	c := overCPU()
	// Deployment web's old ReplicaSet is web-bcdf, and a DaemonSet is called
	// web-bcdf too: web-bcdf-zxcvb is a pod either could have had.
	owners := map[string]metav1.OwnerReference{"ReplicaSet/shop/web-bcdf": controller("Deployment", "web")}
	hist := []ContainerHistory{
		cpuSeries("web-bcdf-zxcvb", between(0, week, 300)),
		cpuSeries("loose", between(0, week, 300)),            // no pattern at all
		cpuSeries("web-7d9f8b-hello", between(0, week, 300)), // vowels: not a generated suffix
		{Namespace: "other", Pod: "web-6c8d4f-bcdfg", Container: "app", CPU: between(0, week, 300)},
	}
	owners["ReplicaSet/other/web-6c8d4f"] = controller("Deployment", "web")
	workloads := []Workload{
		shopWorkload("Deployment", "web", live("web-6c8d4f-gkzzz", c)),
		shopWorkload("DaemonSet", "web-bcdf", live("web-bcdf-mnpqr", c)),
	}
	attachFormer(workloads, nil, owners, hist)
	for _, w := range workloads {
		if len(w.Former) != 0 {
			t.Errorf("%s %s pooled %v", w.Kind, w.Name, w.Former)
		}
	}
}

func TestAFormerPodsStartIsLeftOutOfTheSizing(t *testing.T) {
	// The old replica's first samples are its warm-up at 2Gi; the request is
	// sized as though they were not there, and the limit still covers them.
	rollout := week / 2
	// It started on the window's tenth step, so its series begins there.
	old := between(10, rollout, 300*mibTest)
	old[0].V, old[1].V = 2048*mibTest, 2048*mibTest
	flat := between(10, rollout, 300*mibTest)
	c := overMemory()
	c.Starts = []float64{t0 + float64(rollout*stepEvery)}
	owners := map[string]metav1.OwnerReference{
		"ReplicaSet/shop/api-5b7f9c": controller("Deployment", "api"),
		"ReplicaSet/shop/api-6c8d4f": controller("Deployment", "api"),
	}
	request := func(oldSeries []Point, spiked bool) int64 {
		hist := []ContainerHistory{
			{Namespace: "shop", Pod: "api-5b7f9c-xzbcd", Container: "app", Memory: oldSeries},
			{Namespace: "shop", Pod: "api-6c8d4f-bcdfg", Container: "app", Memory: between(rollout, week, 300*mibTest)},
		}
		workloads := []Workload{shopWorkload("Deployment", "api", live("api-6c8d4f-bcdfg", c))}
		attachFormer(workloads, nil, owners, hist)
		for _, s := range Compute(workloads, hist, nil) {
			if strings.HasPrefix(s.Resource, "Memory") && s.PairedRequestRaw > 0 {
				if spiked && s.SuggestedRaw < 2048*mibTest {
					t.Errorf("limit %s, under the 2Gi the former replica reached starting", s.Suggested)
				}
				return s.PairedRequestRaw
			}
			if s.Resource == "Memory" && s.Action == "Reduce request" {
				return s.SuggestedRaw
			}
		}
		t.Fatal("no memory request recommended")
		return 0
	}
	if got, want := request(old, true), request(flat, false); got != want {
		t.Errorf("request %s with the former replica's warm-up, %s without", FormatMemory(got), FormatMemory(want))
	}
}

// The window is already narrowed to the namespaces a person may list; pooling
// adds nothing from elsewhere: a former pod is only tied to a workload in its
// own namespace, and a workload is only one the page drew.
func TestPoolingStaysInsideTheNamespacesOnThePage(t *testing.T) {
	c := overCPU()
	owners := map[string]metav1.OwnerReference{"ReplicaSet/billing/api-5b7f9c": controller("Deployment", "api")}
	hist := []ContainerHistory{{Namespace: "billing", Pod: "api-5b7f9c-xzbcd", Container: "app", CPU: between(0, week, 300)}}
	workloads := []Workload{shopWorkload("Deployment", "api", live("api-6c8d4f-bcdfg", c))}
	attachFormer(workloads, nil, owners, hist)
	if len(workloads[0].Former) != 0 {
		t.Errorf("shop's api pooled billing's pod: %v", workloads[0].Former)
	}
}

package rightsizing

import "testing"

// A reading is a measurement and a suggestion is a claim. These tests are about
// the measurement: that the average is per pod and not pooled, that a replica
// nothing measured is not counted as a zero, and that a workload with nothing
// wrong is still reported — the last being the whole reason readings exist
// beside suggestions.

func TestReadingsMeasureWhatIsThereRatherThanWhatIsWrong(t *testing.T) {
	// Well within its request: the engine has nothing to say about it, and it
	// must appear on the page all the same.
	c := ctr("app", opts{
		cpuReq: 250, cpuLim: 500, cpuUse: 180,
		memReq: 256 * mibTest, memLim: 512 * mibTest, memUse: 200 * mibTest,
	})
	readings := Readings([]Workload{workloadOf("api", replicasOf(1, c), "", "")}, nil)

	if len(readings) != 1 {
		t.Fatalf("%d readings, want 1", len(readings))
	}
	got := readings[0]
	if got.Name != "api" || got.Kind != "Deployment" {
		t.Errorf("got %s/%s", got.Kind, got.Name)
	}
	if len(got.Containers) != 1 {
		t.Fatalf("%d containers, want 1", len(got.Containers))
	}
	cpu := got.Containers[0].CPU
	if !cpu.Measured {
		t.Fatal("CPU is not marked measured")
	}
	if cpu.Mean != 180 || cpu.Request != 250 || cpu.Limit != 500 {
		t.Errorf("mean/request/limit = %d/%d/%d, want 180/250/500", cpu.Mean, cpu.Request, cpu.Limit)
	}
	if cpu.Status != StatusHealthy {
		t.Errorf("status = %q, want %q", cpu.Status, StatusHealthy)
	}
	if Compute([]Workload{workloadOf("api", replicasOf(1, c), "", "")}, nil, nil) != nil {
		t.Error("this workload was expected to produce no suggestion at all")
	}
}

// The average is per pod, because the request it is compared against is per pod.
// Summing three replicas and calling it usage would advise tripling every
// request on the cluster.
func TestTheAverageIsPerPodAndThePeakIsTheWorstReplica(t *testing.T) {
	pods := []Pod{
		testPod("api-1", ctr("app", opts{cpuReq: 250, cpuLim: 500, cpuUse: 100, memUse: 1})),
		testPod("api-2", ctr("app", opts{cpuReq: 250, cpuLim: 500, cpuUse: 200, memUse: 1})),
		testPod("api-3", ctr("app", opts{cpuReq: 250, cpuLim: 500, cpuUse: 300, memUse: 1})),
	}
	cpu := Readings([]Workload{workloadOf("api", pods, "", "")}, nil)[0].Containers[0].CPU

	if cpu.Mean != 200 {
		t.Errorf("mean = %d, want 200 — the average of the three replicas, not their sum", cpu.Mean)
	}
	if cpu.Peak != 300 {
		t.Errorf("peak = %d, want 300 — the worst replica", cpu.Peak)
	}
}

// One replica doing several times the work of its peers is invisible in an
// average, which is why the average can be opened up.
func TestAReadingKeepsThePerReplicaBreakdown(t *testing.T) {
	pods := []Pod{
		testPod("api-1", ctr("app", opts{cpuReq: 250, cpuUse: 50, memUse: 1})),
		testPod("api-2", ctr("app", opts{cpuReq: 250, cpuUse: 400, memUse: 1})),
	}
	got := Readings([]Workload{workloadOf("api", pods, "", "")}, nil)[0].Containers[0]

	if len(got.Pods) != 2 {
		t.Fatalf("%d pod readings, want 2", len(got.Pods))
	}
	if got.Pods[0].Pod != "api-1" || got.Pods[0].CPU != 50 {
		t.Errorf("first replica = %+v", got.Pods[0])
	}
	if got.Pods[1].CPU != 400 {
		t.Errorf("second replica = %+v", got.Pods[1])
	}
}

// What nothing measured is not zero: a container nothing reported on is not an
// idle one, and a bar drawn from a zero says the opposite of the truth.
func TestAnUnmeasuredContainerIsNotReadAsIdle(t *testing.T) {
	c := ctr("app", opts{cpuReq: 250, cpuLim: 500}) // no usage at all
	got := Readings([]Workload{workloadOf("api", replicasOf(1, c), "", "")}, nil)[0].Containers[0]

	if got.CPU.Measured {
		t.Error("CPU is marked measured though nothing reported on it")
	}
	if got.CPU.Status != StatusNone {
		t.Errorf("status = %q, want %q", got.CPU.Status, StatusNone)
	}
	if got.Pods[0].Measured {
		t.Error("the replica is marked measured though nothing reported on it")
	}
	// The spec is still worth drawing: what it asks for is known even when what
	// it uses is not.
	if got.CPU.Request != 250 || got.CPU.Limit != 500 {
		t.Errorf("the spec was lost: request %d, limit %d", got.CPU.Request, got.CPU.Limit)
	}
}

// Attach files findings on their workload and ranks the page worst-first, so an
// operator scrolling from the top meets the problems before the tidy ones.
func TestAttachRanksWorkloadsWorstFirst(t *testing.T) {
	quiet := ctr("app", opts{
		cpuReq: 250, cpuLim: 500, cpuUse: 180,
		memReq: 256 * mibTest, memLim: 512 * mibTest, memUse: 200 * mibTest,
	})
	wasteful := ctr("app", opts{cpuReq: 2000, cpuLim: 4000, cpuUse: 50, memUse: 1})
	dying := ctr("app", opts{memReq: 100 * mibTest, memLim: 200 * mibTest, memUse: 190 * mibTest, cpuUse: 1})

	workloads := []Workload{
		workloadOf("quiet", replicasOf(1, quiet), "", ""),
		workloadOf("wasteful", replicasOf(1, wasteful), "", ""),
		workloadOf("dying", replicasOf(1, dying), "", ""),
	}
	got := Attach(Readings(workloads, nil), Compute(workloads, nil, nil))

	if len(got) != 3 {
		t.Fatalf("%d readings, want 3", len(got))
	}
	if got[0].Name != "dying" {
		t.Errorf("first card is %q, want the one at risk", got[0].Name)
	}
	if got[len(got)-1].Name != "quiet" {
		t.Errorf("last card is %q, want the one with nothing wrong", got[len(got)-1].Name)
	}
	if got[0].Worst != KindDanger {
		t.Errorf("the worst finding on the first card is %q, want %q", got[0].Worst, KindDanger)
	}
	if len(got[len(got)-1].Suggestions) != 0 {
		t.Errorf("the quiet workload picked up %d findings", len(got[len(got)-1].Suggestions))
	}
	// Every finding lands somewhere: one silently dropped is one an operator
	// never sees.
	filed := 0
	for _, r := range got {
		filed += len(r.Suggestions)
	}
	if want := len(Compute(workloads, nil, nil)); filed != want {
		t.Errorf("%d findings were filed onto cards, want all %d", filed, want)
	}
}

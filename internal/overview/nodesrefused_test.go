package overview

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

// A person whose rights hold no `list nodes` - the built-in view and edit
// ClusterRoles carry none - is refused the machines and nothing else: the
// page is drawn, the version is read, the pods are listed, and what is summed
// over the machines is unknown rather than zero.
func TestAPersonRefusedTheNodesIsRefusedOnlyWhatNeedsThem(t *testing.T) {
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		return q.Attributes.Resource != "nodes", nil
	}}
	got, err := Cluster(context.Background(), reviews.Gate(), client(
		node("worker-1", "4", "8Gi", "110", ready(corev1.ConditionFalse)),
		pod("api-0", "worker-1", corev1.PodRunning, corev1.Container{Name: "api", Resources: requesting("500m", "1Gi")}),
	), nil)
	if err != nil {
		t.Fatalf("a person refused the nodes was refused the whole page: %v", err)
	}
	if !errors.Is(got.NodesRefused, access.ErrDenied) {
		t.Fatalf("NodesRefused = %v, want the person's refusal of the node list", got.NodesRefused)
	}
	if got.Version == "" {
		t.Error("the version needs no node, and was not read")
	}
	if got.CPU.Known() || got.Memory.Known() || got.Pods.Known() {
		t.Errorf("capacity is a sum over machines nobody listed, and reads as a figure: %+v %+v %+v",
			got.CPU, got.Memory, got.Pods)
	}
	if got.Total != 0 || len(got.Alerts) != 0 {
		t.Errorf("node figures were drawn from a list that was refused: total %d, alerts %v", got.Total, got.Alerts)
	}
	if got.History.OK || got.NowFrom.OK {
		t.Errorf("the machines' usage is shown to a person refused the machines: history %+v, now %+v",
			got.History, got.NowFrom)
	}
	// Said once, from NodesRefused: no second sentence about the requests in
	// what the page did not show.
	if len(got.Degraded) != 0 {
		t.Errorf("Degraded = %v, want the refusal said once and nowhere else", got.Degraded)
	}
	if got.AllWell() {
		t.Error("a page that could not look at the machines says nothing needs acting on")
	}
}

// The workloads half never read the nodes, and is drawn for the same person.
func TestTheWorkloadsOfAPersonRefusedTheNodesAreStillRead(t *testing.T) {
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		return q.Attributes.Resource != "nodes", nil
	}}
	got, err := Workloads(context.Background(), reviews.Gate(), client(
		pod("api-0", "worker-1", corev1.PodRunning, corev1.Container{Name: "api"}),
	), "", nil)
	if err != nil {
		t.Fatalf("the workloads of a person refused the nodes failed: %v", err)
	}
	if got.Pods != 1 {
		t.Errorf("pods = %d, want 1", got.Pods)
	}
}

// Any other failure of the node list is still the page's: the cluster did not
// answer, and a dashboard of what else came back would be drawn over it.
func TestANodeListThatFailedForAnotherReasonStillFailsThePage(t *testing.T) {
	if _, err := Cluster(context.Background(), accesstest.Allow(), refuses("nodes"), nil); err == nil {
		t.Fatal("a node list that failed (not refused) returned a page")
	}
}

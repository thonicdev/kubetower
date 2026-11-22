package pending

import (
	"reflect"
	"testing"
)

// The messages measured on a kind cluster running Kubernetes v1.34.0, as the
// PodScheduled condition and the FailedScheduling event carried them.
const (
	msgMemory   = "0/1 nodes are available: 1 Insufficient memory. no new claims to deallocate, preemption: 0/1 nodes are available: 1 Preemption is not helpful for scheduling."
	msgGPU      = "0/1 nodes are available: 1 Insufficient nvidia.com/gpu. no new claims to deallocate, preemption: 0/1 nodes are available: 1 Preemption is not helpful for scheduling."
	msgSelector = "0/1 nodes are available: 1 node(s) didn't match Pod's node affinity/selector. no new claims to deallocate, preemption: 0/1 nodes are available: 1 Preemption is not helpful for scheduling."
	msgAnti     = "0/1 nodes are available: 1 node(s) didn't satisfy existing pods anti-affinity rules. no new claims to deallocate, preemption: 0/1 nodes are available: 1 No preemption victims found for incoming pod."
	msgSpread   = "0/1 nodes are available: 1 node(s) didn't match pod topology spread constraints (missing required label). no new claims to deallocate, preemption: 0/1 nodes are available: 1 Preemption is not helpful for scheduling."
	msgUnbound  = "0/1 nodes are available: pod has unbound immediate PersistentVolumeClaims. not found"
	msgNoClaim  = "0/1 nodes are available: persistentvolumeclaim \"nope\" not found. not found"
	// Written from the plugins' own strings: a taint, a cordon, ports and two
	// reasons at once, on five nodes.
	msgMixed = "0/5 nodes are available: 1 node(s) were unschedulable, 2 node(s) had untolerated taint {dedicated: gpu}, 2 Insufficient cpu. preemption: 0/5 nodes are available: 5 Preemption is not helpful for scheduling."
	msgPorts = "0/3 nodes are available: 3 node(s) didn't have free ports for the requested pod ports."
	msgOld   = "0/2 nodes are available: 2 node(s) had taint {node-role.kubernetes.io/master: }, that the pod didn't tolerate."
)

func TestTheMeasuredMessagesAreFiledByTheirWords(t *testing.T) {
	for _, tc := range []struct {
		msg   string
		want  []Reason
		total int
	}{
		{msgMemory, []Reason{{"Insufficient memory", 1, CatResources}}, 1},
		{msgGPU, []Reason{{"Insufficient nvidia.com/gpu", 1, CatResources}}, 1},
		{msgSelector, []Reason{{"node(s) didn't match Pod's node affinity/selector", 1, CatNodeAffinity}}, 1},
		{msgAnti, []Reason{{"node(s) didn't satisfy existing pods anti-affinity rules", 1, CatPodAffinity}}, 1},
		{msgSpread, []Reason{{"node(s) didn't match pod topology spread constraints (missing required label)", 1, CatSpread}}, 1},
		{msgUnbound, []Reason{{"pod has unbound immediate PersistentVolumeClaims", -1, CatVolumes}}, 1},
		{msgNoClaim, []Reason{{`persistentvolumeclaim "nope" not found`, -1, CatVolumes}}, 1},
		{msgMixed, []Reason{
			{"node(s) were unschedulable", 1, CatCordon},
			{"node(s) had untolerated taint {dedicated: gpu}", 2, CatTaints},
			{"Insufficient cpu", 2, CatResources},
		}, 5},
		{msgPorts, []Reason{{"node(s) didn't have free ports for the requested pod ports", 3, CatPorts}}, 3},
		{msgOld, []Reason{{"node(s) had taint {node-role.kubernetes.io/master: }, that the pod didn't tolerate", 2, CatTaints}}, 2},
	} {
		got := ParseMessage(tc.msg)
		if got.Total != tc.total || !reflect.DeepEqual(got.Reasons, tc.want) {
			t.Errorf("%q\n got %d %+v\nwant %d %+v", tc.msg, got.Total, got.Reasons, tc.total, tc.want)
		}
	}
}

func TestThePreemptionHalfIsKeptAsWritten(t *testing.T) {
	if got := ParseMessage(msgAnti).Preemption; got != "0/1 nodes are available: 1 No preemption victims found for incoming pod." {
		t.Errorf("preemption = %q", got)
	}
}

func TestAMessageOfAnotherShapeIsKeptWhole(t *testing.T) {
	v := ParseMessage("running PreFilter plugin \"VolumeBinding\": some new failure")
	if len(v.Reasons) != 1 || v.Reasons[0].Category != CatVolumes || v.Reasons[0].Nodes != -1 {
		t.Errorf("got %+v", v)
	}
	v = ParseMessage("something nobody wrote a rule for")
	if v.Main() != CatOther || v.Reasons[0].Text != "something nobody wrote a rule for" {
		t.Errorf("an unknown phrase must stay raw, filed Other: %+v", v)
	}
}

func TestTheMainCategoryIsTheOneThatRefusedTheMostNodes(t *testing.T) {
	v := ParseMessage(msgMixed)
	if v.Main() != CatTaints { // 2 and 2: the first named wins
		t.Errorf("main = %s", v.Main())
	}
	if !reflect.DeepEqual(v.Set(), []Category{CatResources, CatTaints, CatCordon}) {
		t.Errorf("set = %v", v.Set())
	}
	// A reason with no count refused every node.
	v = ParseMessage("0/4 nodes are available: 3 Insufficient cpu. pod has unbound immediate PersistentVolumeClaims.")
	if v.Main() != CatVolumes {
		t.Errorf("main = %s", v.Main())
	}
}

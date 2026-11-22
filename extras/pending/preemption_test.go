package pending

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// The texts are the ones the scheduler wrote on a cluster, measured (reasons_test.go
// holds the full messages); the mixed one is two nodes of each kind.
func TestThePreemptionSentenceIsReadWithItsCounts(t *testing.T) {
	for _, tc := range []struct {
		name, text, verdict  string
		total, unhelpful, no int
	}{
		{"not helpful", "0/1 nodes are available: 1 Preemption is not helpful for scheduling.", PreemptUnhelpful, 1, 1, 0},
		{"no victims", "0/1 nodes are available: 1 No preemption victims found for incoming pod.", PreemptNoVictims, 1, 0, 1},
		{"both", "0/5 nodes are available: 3 Preemption is not helpful for scheduling, 2 No preemption victims found for incoming pod.", PreemptMixed, 5, 3, 2},
		{"policy never", "not eligible due to preemptionPolicy=Never.", PreemptNever, -1, 0, 0},
		{"a victim on its way out", "not eligible due to a terminating pod on the nominated node.", PreemptWaiting, -1, 0, 0},
		{"no count", "Preemption is not helpful for scheduling.", PreemptUnhelpful, -1, -1, 0},
		{"nothing said", "", PreemptUnsaid, -1, 0, 0},
		{"another sentence", "0/3 nodes are available: 3 something new.", PreemptOther, 3, 0, 0},
	} {
		got := ParsePreemption(tc.text)
		if got.Verdict != tc.verdict || got.Total != tc.total || got.Unhelpful != tc.unhelpful || got.NoVictims != tc.no || got.Text != tc.text {
			t.Errorf("%s: %+v", tc.name, got)
		}
	}
	// The measured full messages, through ParseMessage as the pod's condition is.
	if got := ParsePreemption(ParseMessage(msgMemory).Preemption); got.Verdict != PreemptUnhelpful || got.Unhelpful != 1 {
		t.Errorf("memory: %+v", got)
	}
	if got := ParsePreemption(ParseMessage(msgAnti).Preemption); got.Verdict != PreemptNoVictims || got.NoVictims != 1 {
		t.Errorf("anti: %+v", got)
	}
	if got := ParsePreemption(ParseMessage(msgMixed).Preemption); got.Verdict != PreemptUnhelpful || got.Total != 5 || got.Unhelpful != 5 {
		t.Errorf("mixed: %+v", got)
	}
}

func prioritised(p *corev1.Pod, class string, priority int32, policy corev1.PreemptionPolicy) *corev1.Pod {
	p.Spec.PriorityClassName, p.Spec.Priority = class, &priority
	p.Spec.PreemptionPolicy = &policy
	return p
}

func TestTheRowCarriesThePriorityThePolicyAndTheNominatedNode(t *testing.T) {
	p := prioritised(unschedulable("shop", "api", msgMemory, time.Minute, container("1", "1Gi")), "standard", 1000, corev1.PreemptLowerPriority)
	row := classify(p, testNow)
	got := row.Preemption
	if got == nil || got.Verdict != PreemptUnhelpful || got.Priority == nil || *got.Priority != 1000 || got.Class != "standard" || got.Policy != "PreemptLowerPriority" || got.Nominated != "" {
		t.Fatalf("%+v", got)
	}
	// A nominated node is the scheduler's choice: evicting helps, there.
	p.Status.NominatedNodeName = "node-7"
	got = classify(p, testNow).Preemption
	if got.Verdict != PreemptNominated || got.Nominated != "node-7" {
		t.Errorf("nominated: %+v", got)
	}
	// A pod that never preempts says so, whatever the sentence.
	never := prioritised(unschedulable("shop", "batch", msgMemory, time.Minute, container("1", "1Gi")), "", 0, corev1.PreemptNever)
	if got := classify(never, testNow).Preemption; got.Verdict != PreemptNever || got.Class != "" {
		t.Errorf("never: %+v", got)
	}
	// A pod the scheduler has not refused has nothing to say, a placed one neither.
	fresh := unschedulable("shop", "fresh", "", time.Minute, container("1", "1Gi"))
	fresh.Status.Conditions = nil
	if classify(fresh, testNow).Preemption != nil {
		t.Error("a pod no scheduler has answered has no preemption verdict")
	}
}

func TestTheViewAndTheBriefCarryIt(t *testing.T) {
	h := newHost(t)
	h.add("nodes", node("n1", "2", "4Gi", nil))
	p := prioritised(unschedulable("shop", "api", msgMemory, time.Minute, container("1", "64Gi")), "high", 100000, corev1.PreemptLowerPriority)
	p.Status.NominatedNodeName = "n1"
	h.add("pods", p)
	v := decode[PodView](t, serve(t, h, "/pod/shop/api"))
	if v.Row == nil || v.Row.Preemption == nil || v.Row.Preemption.Nominated != "n1" || v.Row.Preemption.Class != "high" {
		t.Errorf("view: %+v", v.Row)
	}
	b := decode[Brief](t, serve(t, h, "/brief/shop/api"))
	if b.Row == nil || b.Row.Preemption == nil || b.Row.Preemption.Verdict != PreemptNominated {
		t.Errorf("brief: %+v", b.Row)
	}
}

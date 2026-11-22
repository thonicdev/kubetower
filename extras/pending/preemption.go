package pending

import (
	"regexp"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Preemption is what the scheduler did about evicting lower-priority pods for
// a pod it could not place, and what the pod itself says about its priority.
//
// The scheduler's DefaultPreemption plugin runs after every node refused the
// pod, and appends its verdict to the message as `preemption: 0/5 nodes are
// available: 3 Preemption is not helpful for scheduling, 2 No preemption
// victims found for incoming pod.` It writes nothing when it succeeds: it
// picks a node, evicts the victims there and records the choice in the pod's
// `status.nominatedNodeName`. So "would evicting help" is answered from that
// sentence and the nominated node, never by listing victims here: nothing is
// simulated, and nothing is evicted from this page.
type Preemption struct {
	// Verdict is one of the Preempt* constants.
	Verdict string `json:"verdict"`
	// Total is the N of the sentence's "0/N nodes"; -1 when it said none.
	Total int `json:"total"`
	// Unhelpful and NoVictims are how many nodes the scheduler counted under
	// "Preemption is not helpful" (evicting could not make the pod fit: the
	// refusal is not about room) and "No preemption victims found" (nothing
	// there has a lower priority). 0 is not named; -1 is named with no count,
	// which covers every node of the sentence.
	Unhelpful int `json:"unhelpful"`
	NoVictims int `json:"noVictims"`
	// Text is the scheduler's own sentence, as written.
	Text string `json:"text,omitempty"`
	// Nominated is the node the scheduler chose and is clearing for the pod.
	Nominated string `json:"nominated,omitempty"`
	// Priority is the pod's resolved priority, Class the PriorityClass it
	// names, Policy its preemptionPolicy.
	Priority *int32 `json:"priority,omitempty"`
	Class    string `json:"class,omitempty"`
	Policy   string `json:"policy,omitempty"`
}

// The verdicts. Only PreemptNominated says evicting helps.
const (
	// PreemptNominated: the scheduler found victims and nominated a node.
	PreemptNominated = "nominated"
	// PreemptWaiting: victims are on their way out of the nominated node.
	PreemptWaiting = "waiting"
	// PreemptNever: the pod's preemptionPolicy is Never.
	PreemptNever = "never"
	// PreemptUnhelpful: on every node the refusal is not about room.
	PreemptUnhelpful = "unhelpful"
	// PreemptNoVictims: nothing of lower priority runs where room is the matter.
	PreemptNoVictims = "noVictims"
	// PreemptMixed: both, on different nodes.
	PreemptMixed = "mixed"
	// PreemptUnsaid: the message carries no preemption half.
	PreemptUnsaid = "unsaid"
	// PreemptOther: a sentence nothing here knows, kept as written.
	PreemptOther = "other"
)

var (
	unhelpfulRE = regexp.MustCompile(`(?:^|\s)(\d+)\s+Preemption is not helpful`)
	noVictimsRE = regexp.MustCompile(`(?:^|\s)(\d+)\s+No preemption victims found`)
)

// ParsePreemption reads the half of the scheduler's message after
// `preemption:`, kept as ParseMessage saved it.
func ParsePreemption(text string) Preemption {
	p := Preemption{Total: -1, Text: text}
	rest := strings.TrimSpace(text)
	if m := availableRE.FindStringSubmatch(rest); m != nil {
		p.Total, _ = strconv.Atoi(m[1])
		rest = rest[len(m[0]):]
	}
	// 0: not named; n: that many nodes; -1: named without a count.
	count := func(re *regexp.Regexp, bare string) int {
		if m := re.FindStringSubmatch(" " + rest); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n
		}
		if strings.Contains(rest, bare) {
			return -1
		}
		return 0
	}
	p.Unhelpful = count(unhelpfulRE, "Preemption is not helpful")
	p.NoVictims = count(noVictimsRE, "No preemption victims found")
	switch {
	case strings.Contains(rest, "preemptionPolicy=Never"):
		p.Verdict = PreemptNever
	case strings.Contains(rest, "terminating pod on the nominated node"):
		p.Verdict = PreemptWaiting
	case p.Unhelpful != 0 && p.NoVictims != 0:
		p.Verdict = PreemptMixed
	case p.Unhelpful != 0:
		p.Verdict = PreemptUnhelpful
	case p.NoVictims != 0:
		p.Verdict = PreemptNoVictims
	case rest == "":
		p.Verdict = PreemptUnsaid
	default:
		p.Verdict = PreemptOther
	}
	return p
}

// preemptionOf is the preemption answer for one pod: the scheduler's sentence,
// the node it nominated, and the pod's priority. Absent for a pod the
// scheduler has not refused and that has no nominated node, where there is
// nothing to say.
func preemptionOf(pod *corev1.Pod, v Verdict, stage Stage) *Preemption {
	nominated := pod.Status.NominatedNodeName
	if stage != StageUnschedulable && nominated == "" {
		return nil
	}
	p := ParsePreemption(v.Preemption)
	p.Nominated = nominated
	if nominated != "" && p.Verdict != PreemptWaiting {
		p.Verdict = PreemptNominated
	}
	if pod.Spec.Priority != nil {
		pr := *pod.Spec.Priority
		p.Priority = &pr
	}
	p.Class = pod.Spec.PriorityClassName
	if pod.Spec.PreemptionPolicy != nil {
		p.Policy = string(*pod.Spec.PreemptionPolicy)
		if *pod.Spec.PreemptionPolicy == corev1.PreemptNever && p.Verdict != PreemptNominated {
			p.Verdict = PreemptNever
		}
	}
	return &p
}

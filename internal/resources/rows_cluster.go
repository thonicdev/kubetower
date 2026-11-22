package resources

import (
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// --- cluster -------------------------------------------------------------------

func nodeRow(n *corev1.Node, now time.Time, usage *Usage) []Cell {
	status, class, rank := NodeStatus(n)

	// Allocatable, not capacity, as internal/overview reads it: it is what
	// the scheduler may hand out, so a full node draws as full.
	allocCPU := n.Status.Allocatable.Cpu().MilliValue()
	allocMemory := n.Status.Allocatable.Memory().Value()

	return []Cell{
		text(n.Name),
		graded(status, class, rank),
		nodeTaints(n),
		// Beside the status: "which node is hot" goes with "which is unwell".
		nodeCPU(usage, allocCPU),
		nodeMemory(usage, allocMemory),
		nodeDisk(usage),
		text(n.Status.NodeInfo.KubeletVersion),
		// Copyable, not a link: there is no scheme and no port.
		copyableText(nodeAddress(n, corev1.NodeInternalIP)),
		text(n.Labels["node.kubernetes.io/instance-type"]),
		age(n.CreationTimestamp, now),
	}
}

// The three gauges on the Nodes list. The bar decorates a reading and never
// replaces it: each cell keeps its text and sort key (millicores, bytes), and
// the hint is the reading over its whole (`1.2 / 8 cores allocatable`, see
// Fraction). An em dash draws no bar: an empty track would read as an idle
// machine.

func nodeCPU(usage *Usage, allocatable int64) Cell {
	cell := cpuCell(usage)
	if !measured(usage) {
		return cell
	}
	if allocatable > 0 {
		// Both halves in cores; see Fraction.
		cell.Hint = CPUFraction(float64(usage.CPUMillicores), allocatable) + " allocatable"
	}
	return withGauge(cell, usage.CPUMillicores, allocatable)
}

func nodeMemory(usage *Usage, allocatable int64) Cell {
	cell := memoryCell(usage)
	if !measured(usage) {
		return cell
	}
	if allocatable > 0 {
		cell.Hint = BytesFraction(float64(usage.MemoryBytes), allocatable) + " allocatable"
	}
	return withGauge(cell, usage.MemoryBytes, allocatable)
}

// nodeDisk is the node's own root filesystem, the one gauge whose absence is
// ordinary: only its kubelet knows, through `nodes/proxy`, which several
// managed offerings refuse. Absent draws an em dash, never an empty bar.
func nodeDisk(usage *Usage) Cell {
	if usage == nil || usage.Disk == nil || usage.Disk.CapacityBytes <= 0 {
		return Cell{Text: Missing}
	}
	disk := usage.Disk
	used := MemorySize(disk.UsedBytes)
	cell := Cell{
		Text:  used,
		Value: float64(disk.UsedBytes),
		Hint:  BytesFraction(float64(disk.UsedBytes), disk.CapacityBytes) + " on the node's own filesystem, as its kubelet measured it",
	}
	return withGauge(cell, disk.UsedBytes, disk.CapacityBytes)
}

// NodeStatus reads a node the way kubectl does, with its class and rank.
// Exported so every page reads a node the same way.
//
// The cordon is a suffix, never a case: a NotReady, cordoned node reads
// `NotReady,SchedulingDisabled`, as in kubectl. Unknown is its own answer: a
// kubelet that stopped reporting, not one that answered "no".
func NodeStatus(n *corev1.Node) (string, string, int) {
	ready := corev1.ConditionUnknown
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			ready = c.Status
		}
	}

	status, class, rank := "Unknown", ClassBad, RankBad
	switch ready {
	case corev1.ConditionTrue:
		status, class, rank = "Ready", ClassOK, RankOK
	case corev1.ConditionFalse:
		status, class, rank = "NotReady", ClassBad, RankBad
	}

	if n.Spec.Unschedulable {
		// Cordoned: a capacity leak if forgotten. On a Ready node it warns; on
		// a broken one it does not soften the grade.
		status += ",SchedulingDisabled"
		if class == ClassOK {
			class, rank = ClassWarn, RankWarn
		}
	}
	return status, class, rank
}

// NodeRoles reads the roles off the labels that encode them: the key of a
// `node-role.kubernetes.io/<role>` label (its value is usually empty), and the
// value of the older `kubernetes.io/role`, which several managed providers
// still set.
//
// Takes labels rather than a node so the drawer, holding an unstructured
// object, can call it too.
func NodeRoles(labels map[string]string) []string {
	const prefix = "node-role.kubernetes.io/"
	var roles []string
	for key, value := range labels {
		switch {
		case strings.HasPrefix(key, prefix):
			if role := strings.TrimPrefix(key, prefix); role != "" {
				roles = append(roles, role)
			}
		case key == "kubernetes.io/role" && value != "":
			roles = append(roles, value)
		}
	}
	sort.Strings(roles)
	return roles
}

func nodeAddress(n *corev1.Node, kind corev1.NodeAddressType) string {
	for _, a := range n.Status.Addresses {
		if a.Type == kind {
			return a.Address
		}
	}
	return ""
}

func namespaceRow(n *corev1.Namespace, now time.Time) []Cell {
	class, rank := gradePhase(string(n.Status.Phase), map[string]string{
		string(corev1.NamespaceActive): ClassOK,
		// Stuck Terminating is a finalizer that will not resolve on its own.
		string(corev1.NamespaceTerminating): ClassWarn,
	})
	return []Cell{
		text(n.Name),
		graded(string(n.Status.Phase), class, rank),
		age(n.CreationTimestamp, now),
	}
}

func eventRow(e *corev1.Event, now time.Time) []Cell {
	kind := graded(e.Type, ClassNone, 0)
	if e.Type == corev1.EventTypeWarning {
		kind = graded(e.Type, ClassBad, RankBad)
	}

	object := e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name
	if e.InvolvedObject.Kind == "" {
		object = e.InvolvedObject.Name
	}

	return []Cell{
		text(e.Namespace),
		kind,
		text(e.Reason),
		text(object),
		// Clipped: a message can carry a whole Go panic.
		text(Clip(e.Message, 140)),
		count32(EventCount(e)),
		age(metav1.Time{Time: EventTime(e)}, now),
	}
}

// EventTime is when an event last happened, not when its object was created.
// The fallback chain:
//
//  1. LastTimestamp, filled only by legacy core/v1 writers.
//  2. series.lastObservedTime, where an events.k8s.io/v1 writer puts the last
//     occurrence of a repeating event (eventTime is the first).
//  3. EventTime, the newer API's single-occurrence stamp.
//  4. FirstTimestamp.
//  5. CreationTimestamp, the floor, so nothing is dated 1970.
//
// internal/detail/event.go reads the same chain from an unstructured object;
// the two must agree.
func EventTime(e *corev1.Event) time.Time {
	switch {
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.Time
	case e.Series != nil && !e.Series.LastObservedTime.IsZero():
		return e.Series.LastObservedTime.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	case !e.FirstTimestamp.IsZero():
		return e.FirstTimestamp.Time
	}
	return e.CreationTimestamp.Time
}

// EventFirstTime is when an event first happened. Falls back to EventTime, so
// an event with one timestamp reads as happening once, then.
func EventFirstTime(e *corev1.Event) time.Time {
	switch {
	case !e.FirstTimestamp.IsZero():
		return e.FirstTimestamp.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	}
	return EventTime(e)
}

// nodeTaints is how many taints a node carries, with the taints on the hover.
// A taint explains why a Ready node runs nothing; the count, not the taints,
// because they are too long for a column.
func nodeTaints(n *corev1.Node) Cell {
	if len(n.Spec.Taints) == 0 {
		// An em dash rather than a zero: this is an absence.
		return Cell{Text: Missing, Hint: "no taints"}
	}

	lines := make([]string, 0, len(n.Spec.Taints))
	failure, cordon := false, false
	for _, t := range n.Spec.Taints {
		text := t.Key
		if t.Value != "" {
			text += "=" + t.Value
		}
		if t.Effect != "" {
			text += ":" + string(t.Effect)
		}
		lines = append(lines, text)

		// As internal/detail does, a taint an administrator set is ordinary and
		// one the node controller set is a symptom. Except
		// `node.kubernetes.io/unschedulable`, which mirrors a cordon and grades
		// as one: a warning.
		switch {
		case t.Key == corev1.TaintNodeUnschedulable:
			cordon = true
		case strings.HasPrefix(t.Key, "node.kubernetes.io/"):
			failure = true
		}
	}

	cell := Cell{
		Text:  strconv.Itoa(len(n.Spec.Taints)),
		Value: float64(len(n.Spec.Taints)),
		Hint:  strings.Join(lines, "\n"),
	}
	if failure {
		// A controller taint (unreachable, memory or disk pressure) is a fault.
		cell.Class, cell.Rank = ClassBad, RankBad
	} else if cordon {
		// A decision, not a fault.
		cell.Class, cell.Rank = ClassWarn, RankWarn
	}
	return cell
}

// EventCount is how many times an event says it happened. Not just `e.Count`:
// an events.k8s.io/v1 writer leaves it at zero and puts the number in
// `series.count`. internal/detail/event.go has the same fallback; keep them
// in step.
func EventCount(e *corev1.Event) int32 {
	if e.Series != nil && e.Series.Count > 0 {
		return e.Series.Count
	}
	if e.Count > 0 {
		return e.Count
	}
	// An event exists because something happened: one is the floor.
	return 1
}

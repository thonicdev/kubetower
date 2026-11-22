package detail

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/internal/resources"
)

// What a Node is asked about: can it take work, what is it made of, and where
// does it sit. The answers are spread over `spec`, `status.nodeInfo`,
// `status.addresses` and `metadata.labels`. Taints lead: they are why a node
// that looks healthy and reports no failing condition runs nothing.

// taintFacts turns `spec.taints` into rows.
//
// A taint set on purpose and one the node controller added because something
// is wrong look alike in the manifest and mean opposite things. A control-plane
// node is supposed to carry `node-role.kubernetes.io/control-plane:NoSchedule`;
// coloring it red would teach the operator to ignore the color.
func taintFacts(raw *unstructured.Unstructured) []Fact {
	items, found, err := unstructured.NestedSlice(raw.Object, "spec", "taints")
	if err != nil || !found || len(items) == 0 {
		// Said explicitly: "no taints" differs from a panel that does not
		// report them.
		return []Fact{{Label: "Taints", Value: "none", Class: "good"}}
	}

	facts := make([]Fact, 0, len(items))
	for _, item := range items {
		taint, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key, _, _ := unstructured.NestedString(taint, "key")
		value, _, _ := unstructured.NestedString(taint, "value")
		effect, _, _ := unstructured.NestedString(taint, "effect")
		if key == "" {
			continue
		}

		// `key=value:Effect`, the form `kubectl taint` takes; with no value it
		// is `key:Effect`.
		text := key
		if value != "" {
			text += "=" + value
		}
		if effect != "" {
			text += ":" + effect
		}

		facts = append(facts, Fact{
			Label: "Taint",
			Value: text,
			Class: taintClass(key, effect),
			Pill:  true,
		})
	}
	if len(facts) == 0 {
		return []Fact{{Label: "Taints", Value: "none", Class: "good"}}
	}
	return facts
}

// taintClass grades a taint by who is likely to have set it.
//
// `node.kubernetes.io/*` is the node controller's: an unreachable node, or
// memory, disk or PID pressure. Those are symptoms. Role taints and anything an
// operator chose are policy working as intended.
//
// Except `node.kubernetes.io/unschedulable`, added to every cordoned node: a
// cordon is a decision, already shown in amber on the Scheduling row, and the
// list's Taints column grades it the same way.
func taintClass(key, effect string) string {
	if key == corev1.TaintNodeUnschedulable {
		return "warn"
	}
	if strings.HasPrefix(key, "node.kubernetes.io/") {
		return "bad"
	}
	if effect == "NoExecute" {
		// NoExecute evicts running pods, stronger than refusing new ones,
		// whoever set it.
		return "warn"
	}
	return "neutral"
}

// nodeFacts is everything else a Node is asked.
func nodeFacts(raw *unstructured.Unstructured) []Fact {
	facts := taintFacts(raw)

	// Cordoned is the other reason a healthy node runs nothing, and no
	// condition reflects it.
	if unschedulable, found, _ := unstructured.NestedBool(raw.Object, "spec", "unschedulable"); found && unschedulable {
		facts = append(facts, Fact{
			Label: "Scheduling",
			Value: "cordoned - the scheduler will place nothing new here",
			Class: "warn", Pill: true,
		})
	}

	// Shared with internal/overview, so the legacy `kubernetes.io/role`
	// label shows the same role in both places.
	if roles := resources.NodeRoles(raw.GetLabels()); len(roles) > 0 {
		facts = append(facts, Fact{Label: "Roles", Value: strings.Join(roles, ", ")})
	}

	facts = append(facts, addressFacts(raw)...)

	// Capacity beside allocatable: the gap is the reserved slice that makes
	// scheduling fail on a node that looks half empty.
	for _, resource := range []struct{ path, label string }{
		{"cpu", "CPU"}, {"memory", "Memory"}, {"pods", "Pods"},
	} {
		capacity, _, _ := unstructured.NestedString(raw.Object, "status", "capacity", resource.path)
		allocatable, _, _ := unstructured.NestedString(raw.Object, "status", "allocatable", resource.path)
		// Compared as quantities before rounding: 16200000Ki and 16097600Ki
		// both print as `15.4 GiB`, and comparing the printed figures would hide
		// the reserved slice.
		same := sameQuantity(capacity, allocatable)
		if resource.path == "memory" {
			capacity, allocatable = memoryPair(capacity, allocatable)
		}
		switch {
		case capacity == "" && allocatable == "":
			continue
		case allocatable == "" || same:
			facts = append(facts, Fact{Label: resource.label, Value: capacity})
		default:
			facts = append(facts, Fact{
				Label: resource.label,
				Value: fmt.Sprintf("%s allocatable of %s", allocatable, capacity),
			})
		}
	}

	for _, info := range []struct{ path, label string }{
		{"kubeletVersion", "Kubelet"},
		{"containerRuntimeVersion", "Container runtime"},
		{"osImage", "OS image"},
		{"kernelVersion", "Kernel"},
		{"architecture", "Architecture"},
	} {
		if v, found, _ := unstructured.NestedString(raw.Object, "status", "nodeInfo", info.path); found && v != "" {
			facts = append(facts, Fact{Label: info.label, Value: v})
		}
	}

	// Where the machine is and what it costs: the labels behind "why is this
	// pod not spreading" and "why is this node expensive".
	for _, label := range []struct{ key, label string }{
		{"node.kubernetes.io/instance-type", "Instance type"},
		{"topology.kubernetes.io/zone", "Zone"},
		{"topology.kubernetes.io/region", "Region"},
		{"karpenter.sh/capacity-type", "Capacity type"},
		{"eks.amazonaws.com/capacityType", "Capacity type"},
	} {
		if v := raw.GetLabels()[label.key]; v != "" {
			facts = appendUnlessLabelPresent(facts, Fact{Label: label.label, Value: v})
		}
	}

	if cidr, found, _ := unstructured.NestedString(raw.Object, "spec", "podCIDR"); found && cidr != "" {
		facts = append(facts, Fact{Label: "Pod CIDR", Value: cidr})
	}
	if id, found, _ := unstructured.NestedString(raw.Object, "spec", "providerID"); found && id != "" {
		facts = append(facts, Fact{Label: "Provider ID", Value: id})
	}
	return facts
}

// memoryFigure is a node's memory as the Nodes list prints it (`15.4 GiB`, not
// `16144484Ki`), through the list's own `resources.MemorySize` so the two round
// alike. A quantity that does not parse is printed as written.
func memoryFigure(quantity string) string {
	if quantity == "" {
		return ""
	}
	parsed, err := apiresource.ParseQuantity(quantity)
	if err != nil {
		return quantity
	}
	return resources.MemorySize(parsed.Value())
}

// memoryPair is capacity and allocatable as `memoryFigure` prints them, and
// where the two differ but round to one figure, the capacity says by how much:
// `15.4 GiB allocatable of 15.4 GiB (100 MiB reserved)`.
func memoryPair(capacity, allocatable string) (string, string) {
	shownCapacity, shownAllocatable := memoryFigure(capacity), memoryFigure(allocatable)
	if shownCapacity != shownAllocatable || sameQuantity(capacity, allocatable) {
		return shownCapacity, shownAllocatable
	}
	whole, errWhole := apiresource.ParseQuantity(capacity)
	part, errPart := apiresource.ParseQuantity(allocatable)
	if errWhole != nil || errPart != nil {
		return shownCapacity, shownAllocatable
	}
	reserved := resources.MemorySize(whole.Value() - part.Value())
	return shownCapacity + " (" + reserved + " reserved)", shownAllocatable
}

// sameQuantity compares two quantities as quantities - `1Gi` is `1024Mi` - and
// falls back to the text where either does not parse.
func sameQuantity(one, other string) bool {
	if one == other {
		return true
	}
	left, errLeft := apiresource.ParseQuantity(one)
	right, errRight := apiresource.ParseQuantity(other)
	if errLeft != nil || errRight != nil {
		return false
	}
	return left.Cmp(right) == 0
}

// appendUnlessLabelPresent keeps the first answer when two label keys mean the
// same thing (EKS and Karpenter spell capacity type differently).
func appendUnlessLabelPresent(facts []Fact, add Fact) []Fact {
	for _, f := range facts {
		if f.Label == add.Label {
			return facts
		}
	}
	return append(facts, add)
}

// addressFacts reports the node's addresses in the order the status lists them.
func addressFacts(raw *unstructured.Unstructured) []Fact {
	items, found, err := unstructured.NestedSlice(raw.Object, "status", "addresses")
	if err != nil || !found {
		return nil
	}
	var facts []Fact
	for _, item := range items {
		address, ok := item.(map[string]any)
		if !ok {
			continue
		}
		kind, _, _ := unstructured.NestedString(address, "type")
		value, _, _ := unstructured.NestedString(address, "address")
		if value == "" {
			continue
		}
		switch kind {
		case "InternalIP":
			facts = append(facts, Fact{Label: "Internal IP", Value: value})
		case "ExternalIP":
			facts = append(facts, Fact{Label: "External IP", Value: value})
		case "Hostname":
			// Only when it differs from the node's name.
			if value != raw.GetName() {
				facts = append(facts, Fact{Label: "Hostname", Value: value})
			}
		}
	}
	return facts
}

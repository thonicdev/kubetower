package rightsizing

import (
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// The two aggregates a right-sizing page shows before any container: which
// nodes are committed past what they have, and which namespaces ask for far
// more than they use.

// NodeSummary is one node's capacity against what has been promised to the pods
// on it.
//
// Requested and Limited are sums over the pods scheduled on that node, since a
// request reserves one machine. Limited above Allocatable is ordinary, and is
// the overcommitment worth seeing.
//
// Each pod counts what the scheduler reserved for it (PodResources), so the
// figures match `kubectl describe node`.
type NodeSummary struct {
	Name  string
	Ready bool
	// Pods counts only what still holds capacity (holdsCapacity).
	Pods int

	AllocatableCPU    int64
	AllocatableMemory int64
	RequestedCPU      int64
	RequestedMemory   int64
	LimitedCPU        int64
	LimitedMemory     int64
	UsedCPU           int64
	UsedMemory        int64

	// Ephemeral storage: what the node offers, what its pods reserve and cap,
	// and, from the kubelet, what their writable layers, logs and emptyDirs
	// hold. EphemeralMeasured false is unmeasured, not a node using no disk.
	AllocatableEphemeral int64
	RequestedEphemeral   int64
	LimitedEphemeral     int64
	UsedEphemeral        int64
	EphemeralMeasured    bool
	// Measured says whether any usage was read for this node's pods.
	Measured bool
	// Containers and ContainersMeasured are how much of the node the Used
	// figures describe: Requested and Limited count every container, Used only
	// those the metrics API answered for. Measured is true from the first one.
	Containers         int
	ContainersMeasured int

	// What the machine is (role, age, taints), from the Node object already
	// listed: it changes how a capacity figure reads.
	Roles          []string
	Age            time.Duration
	OSImage        string
	Kernel         string
	KubeletVersion string
	// MaxPods is `status.allocatable.pods`: a node at its pod ceiling is full
	// whatever its CPU says.
	MaxPods int
	// Pressures are the kubelet's own alarms, worded as it words them:
	// "disk pressure", "memory pressure", "pid pressure".
	Pressures []string
	Taints    []NodeTaint
	// TopPods is the heaviest few pods on this node, and TotalPods how many
	// there were to choose from.
	TopPods   []NodePod
	TotalPods int
}

// NodeTaint is one taint, as a card draws it.
type NodeTaint struct {
	Key, Value, Effect string
}

// NodePod is one pod's share of one node.
//
// Summed over the pod's held containers, so a mesh sidecar counts, as in the
// node totals.
type NodePod struct {
	Name      string
	Namespace string
	// CPURequest and MemRequest are what the scheduler reserved; CPUUsed and
	// MemUsed what was measured, and Measured whether anything was.
	CPURequest, MemRequest int64
	CPULimit, MemLimit     int64
	CPUUsed, MemUsed       int64
	Measured               bool
	// Owner is the workload it belongs to, as "Kind/name", so a node's pod
	// list can say which workload's sizing drives it.
	Owner string
}

// topPodsPerNode is how many pods a node card names; every pod still counts
// in the node's totals.
const topPodsPerNode = 10

// NamespaceSummary is one namespace's ask against its use. The ratio, request
// ÷ usage, is what the overview is built on.
type NamespaceSummary struct {
	Name      string
	Pods      int
	Workloads int

	CPURequest int64
	CPULimit   int64
	CPUUsed    int64
	MemRequest int64
	MemLimit   int64
	MemUsed    int64
	Measured   bool

	// Containers and ContainersMeasured are how much of this namespace the Used
	// figures describe. CPURequest counts every container holding capacity,
	// CPUUsed only those the metrics API answered for, so dividing one by the
	// other would overstate the ratio whenever some pods are unscraped.
	Containers         int
	ContainersMeasured int

	// CPURatio and MemRatio are request ÷ usage over the measured containers
	// only, so both sides describe the same population; zero means "cannot
	// say", never "one". ContainersMeasured is the sample's size.
	CPURatio float64
	MemRatio float64
	// CPURequestMeasured and MemRequestMeasured are the ratio's numerator: the
	// requests of the containers that reported usage.
	CPURequestMeasured int64
	MemRequestMeasured int64
	// Band is the judgment on the worse of the two ratios: "overcommit" past
	// 5x, "high" past 2x, "ok" below — plus two answers that are not ratios at
	// all and must not be confused with each other or with them:
	//
	//   "" (empty)     nothing measured this namespace;
	//   "norequests"   it was measured, and its pods declare no requests.
	//
	// A namespace whose pods ask for nothing is not unmeasured: the scheduler
	// places it blind, which is worse and actionable.
	Band string
	// Findings is how many suggestions this namespace's workloads carry, and
	// Worst is the loudest of them.
	Findings int
	Worst    Kind
}

// holdsCapacity reports whether a pod's requests are still reserved on its
// node: a Succeeded or Failed pod has given its reservation back. The core's
// overview applies the same rule; an extra cannot import it.
func holdsCapacity(pod *corev1.Pod) bool {
	return pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed
}

// SummariseNodes totals what is promised on each node.
//
// nodes carries the capacity, pods carry the promises, and usage is keyed
// `namespace/pod/container` as everywhere else here.
func SummariseNodes(nodes []corev1.Node, pods []corev1.Pod, usage map[string]Usage) []NodeSummary {
	return summariseNodes(nodes, pods, usage, time.Now())
}

// summariseNodes takes the clock so Age is testable.
func summariseNodes(nodes []corev1.Node, pods []corev1.Pod, usage map[string]Usage, now time.Time) []NodeSummary {
	index := map[string]int{}
	out := make([]NodeSummary, 0, len(nodes))
	for i := range nodes {
		n := toNode(&nodes[i])
		summary := NodeSummary{
			Name: n.Name, Ready: n.Ready,
			AllocatableCPU: n.AllocatableCPU, AllocatableMemory: n.AllocatableMemory,
			AllocatableEphemeral: nodes[i].Status.Allocatable.StorageEphemeral().Value(),
		}
		describeNode(&summary, &nodes[i], now)
		out = append(out, summary)
		index[n.Name] = len(out) - 1
	}

	// Every pod's share of its node, so the top few are chosen after the
	// sums are complete.
	perNode := make(map[int][]NodePod, len(out))

	for i := range pods {
		pod := &pods[i]
		at, ok := index[pod.Spec.NodeName]
		if !ok || !holdsCapacity(pod) {
			continue
		}
		out[at].Pods++
		share := NodePod{Name: pod.Name, Namespace: pod.Namespace, Owner: owner(pod)}
		// What the scheduler reserved for the pod, not a sum of its containers'
		// specs - see PodResources.
		share.CPURequest, share.CPULimit = PodResources(pod, corev1.ResourceCPU)
		share.MemRequest, share.MemLimit = PodResources(pod, corev1.ResourceMemory)
		ephReq, ephLim := PodResources(pod, corev1.ResourceEphemeralStorage)
		out[at].RequestedCPU += share.CPURequest
		out[at].RequestedMemory += share.MemRequest
		out[at].LimitedCPU += share.CPULimit
		out[at].LimitedMemory += share.MemLimit
		out[at].RequestedEphemeral += ephReq
		out[at].LimitedEphemeral += ephLim
		// heldContainers for the usage: a native sidecar runs for the pod's
		// whole life and is measured like any other container.
		nodeContainers := heldContainers(pod)
		for j := range nodeContainers {
			c := &nodeContainers[j]
			out[at].Containers++
			if u, seen := usage[pod.Namespace+"/"+pod.Name+"/"+c.Name]; seen {
				out[at].ContainersMeasured++
				out[at].UsedCPU += u.CPUMillicores
				out[at].UsedMemory += u.MemoryBytes
				out[at].Measured = true
				share.Measured = true
				share.CPUUsed += u.CPUMillicores
				share.MemUsed += u.MemoryBytes
			}
		}
		perNode[at] = append(perNode[at], share)
	}

	for at, shares := range perNode {
		out[at].TotalPods = len(shares)
		out[at].TopPods = topPods(shares)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// owner is "Kind/name" of the controller that made the pod, one hop: a
// ReplicaSet's name says which Deployment, and the node card has no owner
// index to walk further.
func owner(pod *corev1.Pod) string {
	for _, ref := range pod.OwnerReferences {
		if ref.Controller != nil && *ref.Controller {
			return ref.Kind + "/" + ref.Name
		}
	}
	return ""
}

// AttachNodeEphemeral adds what the kubelets said about disk to the node
// summaries: each measured pod's containers' writable layers and logs, and its
// emptyDirs. A node none of whose pods was measured stays unmeasured.
func AttachNodeEphemeral(nodes []NodeSummary, pods []corev1.Pod, storage map[string]PodStorage) {
	index := map[string]int{}
	for i := range nodes {
		index[nodes[i].Name] = i
	}
	for i := range pods {
		pod := &pods[i]
		at, ok := index[pod.Spec.NodeName]
		if !ok || !holdsCapacity(pod) {
			continue
		}
		st, measured := storage[pod.Namespace+"/"+pod.Name]
		if !measured {
			continue
		}
		nodes[at].EphemeralMeasured = true
		for _, b := range st.Ephemeral {
			nodes[at].UsedEphemeral += b
		}
		for j := range pod.Spec.Volumes {
			if v := &pod.Spec.Volumes[j]; v.EmptyDir != nil && v.EmptyDir.Medium != corev1.StorageMediumMemory {
				nodes[at].UsedEphemeral += st.Volumes[v.Name].UsedBytes
			}
		}
	}
}

// topPods ranks a node's pods by what they are doing, measured ones first. A
// request and a usage are different quantities, so the two groups are ranked
// separately rather than merged.
func topPods(shares []NodePod) []NodePod {
	out := append([]NodePod(nil), shares...)

	// One weight per pod, computed from that pod alone, so the comparator is
	// a strict ordering.
	weight := func(p NodePod) int64 {
		if p.Measured {
			return p.CPUUsed
		}
		return p.CPURequest
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Measured != b.Measured {
			return a.Measured
		}
		if wa, wb := weight(a), weight(b); wa != wb {
			return wa > wb
		}
		return a.Name < b.Name
	})
	if len(out) > topPodsPerNode {
		out = out[:topPodsPerNode]
	}
	return out
}

// describeNode reads what the Node object says about the machine itself.
func describeNode(into *NodeSummary, node *corev1.Node, now time.Time) {
	into.OSImage = node.Status.NodeInfo.OSImage
	into.Kernel = node.Status.NodeInfo.KernelVersion
	into.KubeletVersion = node.Status.NodeInfo.KubeletVersion
	into.MaxPods = int(node.Status.Allocatable.Pods().Value())
	if ts := node.CreationTimestamp.Time; !ts.IsZero() {
		into.Age = now.Sub(ts)
	}

	// Both label spellings: `node-role.kubernetes.io/<role>` and
	// `kubernetes.io/role`, which older clusters and some managed offerings
	// write.
	for key, value := range node.Labels {
		if role, ok := strings.CutPrefix(key, "node-role.kubernetes.io/"); ok && role != "" {
			into.Roles = append(into.Roles, role)
			continue
		}
		if key == "kubernetes.io/role" && value != "" {
			into.Roles = append(into.Roles, value)
		}
	}
	sort.Strings(into.Roles)
	if len(into.Roles) == 0 {
		// A node with no role label is a worker.
		into.Roles = []string{"worker"}
	}

	// The kubelet's own alarms: the conditions that evict pods.
	for _, c := range node.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case corev1.NodeDiskPressure:
			into.Pressures = append(into.Pressures, "disk pressure")
		case corev1.NodeMemoryPressure:
			into.Pressures = append(into.Pressures, "memory pressure")
		case corev1.NodePIDPressure:
			into.Pressures = append(into.Pressures, "pid pressure")
		}
	}

	for _, t := range node.Spec.Taints {
		into.Taints = append(into.Taints, NodeTaint{
			Key: t.Key, Value: t.Value, Effect: string(t.Effect),
		})
	}
}

// SummariseNamespaces totals each namespace's ask against its use, and ranks
// them by how far apart the two are.
//
// readings supply the workload count and the findings, so the grid can say "12
// workloads, 3 findings" without a second pass over the cluster.
func SummariseNamespaces(pods []corev1.Pod, usage map[string]Usage, readings []WorkloadReading) []NamespaceSummary {
	index := map[string]int{}
	var out []NamespaceSummary

	at := func(namespace string) int {
		i, ok := index[namespace]
		if !ok {
			out = append(out, NamespaceSummary{Name: namespace})
			i = len(out) - 1
			index[namespace] = i
		}
		return i
	}

	for i := range pods {
		pod := &pods[i]
		if !holdsCapacity(pod) {
			continue
		}
		j := at(pod.Namespace)
		out[j].Pods++
		for k, containers := 0, heldContainers(pod); k < len(containers); k++ {
			c := &containers[k]
			cpuReq := c.Resources.Requests.Cpu().MilliValue()
			memReq := c.Resources.Requests.Memory().Value()
			out[j].Containers++
			out[j].CPURequest += cpuReq
			out[j].MemRequest += memReq
			out[j].CPULimit += c.Resources.Limits.Cpu().MilliValue()
			out[j].MemLimit += c.Resources.Limits.Memory().Value()
			if u, seen := usage[pod.Namespace+"/"+pod.Name+"/"+c.Name]; seen {
				out[j].ContainersMeasured++
				out[j].CPUUsed += u.CPUMillicores
				out[j].MemUsed += u.MemoryBytes
				// The ratio's numerator is gathered beside its denominator so both
				// describe the same containers.
				out[j].CPURequestMeasured += cpuReq
				out[j].MemRequestMeasured += memReq
				out[j].Measured = true
			}
		}
	}

	rank := map[Kind]int{KindDanger: 0, KindWarning: 1, KindOverkill: 2}
	for _, r := range readings {
		j := at(r.Namespace)
		out[j].Workloads++
		out[j].Findings += len(r.Suggestions)
		if r.Worst != "" && (out[j].Worst == "" || rank[r.Worst] < rank[out[j].Worst]) {
			out[j].Worst = r.Worst
		}
	}

	for i := range out {
		out[i].CPURatio = ratio(out[i].CPURequestMeasured, out[i].CPUUsed)
		out[i].MemRatio = ratio(out[i].MemRequestMeasured, out[i].MemUsed)
		out[i].Band = band(out[i])
	}

	// Worst first: the namespaces asking for the most they do not use, then the
	// ones nothing could be said about, then the tidy ones by name.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if r := bandRank(a.Band) - bandRank(b.Band); r != 0 {
			return r < 0
		}
		am, bm := maxf(a.CPURatio, a.MemRatio), maxf(b.CPURatio, b.MemRatio)
		if am != bm {
			return am > bm
		}
		return a.Name < b.Name
	})
	return out
}

func ratio(request, used int64) float64 {
	if request <= 0 || used <= 0 {
		// Nothing to divide: zero means "cannot say" (an em dash), never one.
		return 0
	}
	return float64(request) / float64(used)
}

func band(s NamespaceSummary) string {
	if !s.Measured {
		return ""
	}
	worst := maxf(s.CPURatio, s.MemRatio)
	switch {
	case worst == 0:
		// Measured, and the containers that reported declare no request: not
		// the same answer as "not measured". It rests on the sample, whose size
		// the card shows beside it.
		return "norequests"
	case worst > 5:
		return "overcommit"
	case worst > 2:
		return "high"
	default:
		return "ok"
	}
}

func bandRank(b string) int {
	switch b {
	case "overcommit":
		return 0
	// A namespace that asks for nothing sits with the high ones: the
	// scheduler places its pods blind.
	case "norequests", "high":
		return 1
	case "ok":
		return 3
	default:
		return 2 // nothing measured: above the tidy ones, because it is a gap
	}
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

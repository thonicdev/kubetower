package rightsizing

import (
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/extension"
)

// This file is the one in the package that knows what a Kubernetes object is:
// it turns pods and nodes the caller has already read into the numbers the
// engine beside it takes, and stays testable without a cluster — see the
// package comment.
//
// It is the pure half of collecting: the half that reads the cluster
// (listing pods, nodes, ReplicaSets, Jobs and claims) is the extra's,
// through extension.Host.

// Availability is whether a part of the answer could be produced, and why not.
//
// The same shape internal/overview carries, and for the same reason: a cluster
// that could not be asked must never render as a cluster with nothing to fix.
// Right-sizing is especially exposed to that reading, because "no suggestions"
// is what a well-tuned cluster looks like.
type Availability struct {
	OK     bool
	Detail string
}

// Report is one cluster's right-sizing answer, with what it was built from.
type Report struct {
	// Suggestions, danger first. Empty is a real answer when Usage.OK.
	Suggestions []Suggestion
	// Usage says whether per-container usage was readable. When it is not, there
	// are no suggestions at all and the page must say this rather than draw an
	// empty list.
	Usage Availability
	// Capacity says whether the node ceiling could be established. Suggestions
	// are still produced without it — they simply cannot be capped, and cannot
	// say how many nodes would still fit the result.
	Capacity Availability
	// Nodes is the per-node recap: what each machine has against what has been
	// promised to the pods on it. Only built for a cluster-wide read — summing
	// one namespace's pods per node would draw every node as nearly empty.
	Nodes []NodeSummary
	// Namespaces is the per-namespace recap: what each asks against what it
	// uses, worst first.
	Namespaces []NamespaceSummary
	// Storage says whether the kubelet could be asked what a pod's disks are
	// doing. It is its own answer and not part of Usage, because the two come
	// from different places and fail independently: metrics-server can be
	// perfectly healthy on a cluster whose `nodes/proxy` is refused, and the page
	// must then say that CPU and memory are measured and disk is not — rather
	// than drawing an empty Volumes section, which reads as a pod with no
	// volumes.
	Storage Availability
	// History says whether a usage window was read, and how much of the page it
	// covers. It is the single most important caveat here: without a window
	// there is no P95, no burst ratio and no trend, so every suggestion rests on
	// one instantaneous sample. Saying so is the difference between a tool and a
	// guess.
	//
	// Four states and not two — no Prometheus, a Prometheus that answered with
	// nothing, a partial window, a full one — because the first two are not the
	// same failure and the third is not the same success. See history.go.
	History Availability
	// Window is the core's state for the window read - History.Window -
	// which says whether a Prometheus was found at all.
	Window extension.WindowState
	// WindowReadAt is when that window was read - History.ReadAt.
	WindowReadAt time.Time
	// Examined is how many workloads were looked at, so "3 suggestions" can be
	// read against how much was measured.
	Examined int
	// Readings is every workload measured, worst first, with its suggestions
	// attached — including the workloads with nothing wrong. A page made only of
	// findings answers one question and hides the one underneath it: what the
	// cluster is actually doing.
	Readings []WorkloadReading
}

// Usage is one container's live usage, as the caller read it. Declared here so
// this package does not import internal/metrics — the field names are the
// contract, and metrics.Usage satisfies it by having the same two.
type Usage struct {
	CPUMillicores int64
	MemoryBytes   int64
}

// group turns a flat pod list into workloads, pooling every replica of one
// controller under the object whose pod template would be edited.
// now is threaded through rather than read from the clock inside, because
// CrashLooping asks "did this terminate recently" and a function that reads the
// wall clock cannot be given a container that terminated four minutes ago.
func group(pods []corev1.Pod, owners map[string]metav1.OwnerReference, usage map[string]Usage, now time.Time) ([]Workload, int) {
	byKey := map[string]int{}
	var out []Workload
	skipped := 0

	for i := range pods {
		pod := &pods[i]
		// A pod that is not running has no usage worth reading, and a pod that is
		// terminating is about to stop having any.
		//
		// The count is returned rather than swallowed, because the most
		// right-sizing-relevant state a pod can be in is **Pending because its
		// request exceeds every node** — and that pod's workload then appears
		// nowhere on this page at all, with nothing saying a workload was left
		// out. Sizing it is impossible (there is no usage), but disappearing
		// without a word hides the very thing that was not measured. summary.go counts
		// those same pods' requests, so the two halves of this package would
		// otherwise disagree in silence about which pods exist.
		if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
			if holdsCapacity(pod) && pod.DeletionTimestamp == nil {
				skipped++
			}
			continue
		}

		kind, name := attribute(pod, owners)
		key := pod.Namespace + "/" + kind + "/" + name
		at, seen := byKey[key]
		if !seen {
			out = append(out, Workload{Kind: kind, Name: name, Namespace: pod.Namespace})
			at = len(out) - 1
			byKey[key] = at
		}
		out[at].Pods = append(out[at].Pods, toPod(pod, usage, now))
	}

	// A stable order, so the same cluster twice draws the same page.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, skipped
}

// windowedContainers counts, over every container the engine will actually look
// at, how many have a CPU window and how many have a memory one.
//
// **Two counts and not one, and that came out of watching the page.** The first
// version answered "has a window" with an OR across the two resources, and a
// probe that removed the CPU series for one namespace still drew "a window for
// all 191 containers" — because those containers still had their memory series.
// That is not a contrived case: `rate(...[5m])` needs five minutes of a counter
// before it returns anything, while `container_memory_working_set_bytes` is a
// gauge and is there on the first scrape. So **every pod between two and five
// minutes old has a memory window and no CPU window**, and an OR hides that
// its CPU suggestions are single-scrape while its memory ones are not.
//
// Per replica and not per workload-container, because coverage is per replica:
// a Deployment of three where one pod started four minutes ago is two thirds
// windowed, and that third pod is exactly the one whose snapshot could pull a
// suggestion the wrong way.
//
// The denominator is containers with HasUsage and not every container in the
// spec. A container nothing measured is skipped by usageStats before its
// history is even looked up, so counting it would report a page as permanently
// partial for containers no suggestion was ever going to be made about — and a
// caveat that is always on is a caveat nobody reads.
//
// Two points and not one, matching usageStats: a series of one sample is a
// snapshot with a timestamp on it.
func windowedContainers(workloads []Workload, hist HistoryMap) (cpu, memory, total int) {
	for _, w := range workloads {
		for _, pod := range w.Pods {
			for i := range pod.Containers {
				c := &pod.Containers[i]
				if !c.HasUsage {
					continue
				}
				total++
				h, ok := hist[HistoryKey(pod.Namespace, pod.Name, c.Name)]
				if !ok {
					continue
				}
				if len(h.CPU) >= 2 {
					cpu++
				}
				if len(h.Memory) >= 2 {
					memory++
				}
			}
		}
	}
	return cpu, memory, total
}

// attribute names the object whose pod template holds this pod's resources.
func attribute(pod *corev1.Pod, owners map[string]metav1.OwnerReference) (kind, name string) {
	owner := metav1.GetControllerOf(pod)
	if owner == nil || owner.Kind == "Node" {
		// A bare pod. Its resources are its own, and there is no template to
		// patch — kubectlTarget refuses "Pod", so no command is offered.
		//
		// A static pod's mirror is "controlled" by its Node, and filing it
		// under the Node pooled every control-plane static pod - etcd, the
		// API server, the scheduler - into one "workload" named after the
		// machine (seen on docker-desktop). Each is its
		// own pod, sized in its manifest on the node's disk.
		return "Pod", pod.Name
	}
	if grand, ok := owners[owner.Kind+"/"+pod.Namespace+"/"+owner.Name]; ok {
		return grand.Kind, grand.Name
	}
	return owner.Kind, owner.Name
}

// oomRecency is how long a kill for memory is the alarm it is.
//
// A day. `lastState.terminated` keeps the last kill until the next termination,
// which on a healthy container can be months, and the engine read it as "now":
// a container killed once in the spring, and fine since, carried a danger to
// raise its memory limit for ever. A day is long enough that a kill on last
// night's batch is still on this morning's page, and short enough that a
// one-off stops shouting. Older kills still block every memory reduction - see
// Container.OOMKilledEarlier.
const oomRecency = 24 * time.Hour

// restartRecency is how long after a termination a container's usage is still
// treated as a dying process's rather than a working one's.
//
// Ten minutes, and the number is chosen against the kubelet rather than tuned:
// CrashLoopBackOff caps its interval at five, so a container genuinely in a
// loop terminates at least that often and never falls out of this window. A
// container that restarted once and settled leaves it after ten minutes and
// gets its suggestions back, which is the whole point: a lifetime restart count
// never gives them back.
const restartRecency = 10 * time.Minute

// heldContainers returns the containers whose requests are reserved for the pod's whole
// life: its regular containers, and its **native sidecars**.
//
// Since Kubernetes 1.29 an init container with `restartPolicy: Always` is a
// sidecar — it starts before the app, it never exits, and its requests count
// toward the pod's effective request for as long as the pod lives. Reading
// `Spec.Containers` alone therefore misses, on an Istio or Linkerd or
// vault-agent fleet, the container most often copy-pasted with the wrong
// request:
//
//   - it is never sized, so the page is silent about it;
//   - a sidecar killed for memory does not set its workload's OOMKilled flag, so
//     the package's headline refusal does not protect it;
//   - and every node total under-counts by its share — sixty pods times 100m is
//     six cores of commitment a node card does not draw, in the direction that
//     makes a machine look emptier than it is.
//
// An ordinary init container is deliberately not here. It has exited by the time
// anything on this page is looked at, and its request is released.
func heldContainers(pod *corev1.Pod) []corev1.Container {
	out := make([]corev1.Container, 0, len(pod.Spec.Containers)+1)
	for i := range pod.Spec.InitContainers {
		c := &pod.Spec.InitContainers[i]
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			out = append(out, *c)
		}
	}
	return append(out, pod.Spec.Containers...)
}

func toPod(pod *corev1.Pod, usage map[string]Usage, now time.Time) Pod {
	out := Pod{Name: pod.Name, Namespace: pod.Namespace, NodeName: pod.Spec.NodeName, QOS: string(pod.Status.QOSClass)}

	// The pod's age, for the warm-up refusal — see Pod.Age for why pod-level
	// is enough. StartTime is when the kubelet accepted the pod, which is
	// minutes-accurate for a question asked in tens of minutes.
	if st := pod.Status.StartTime; st != nil && !st.IsZero() {
		out.AgeKnown = true
		out.Age = now.Sub(st.Time)
	}

	// Restart counts, crash loops and OOM kills live on the status, keyed by
	// container name.
	type state struct {
		restarts     int
		oomKilled    bool
		oomEarlier   bool
		crashLooping bool
		starts       []float64
	}
	states := map[string]state{}
	// InitContainerStatuses carries the sidecars', and a sidecar the kernel killed
	// has to reach the OOMKilled refusal like any other container.
	statuses := make([]corev1.ContainerStatus, 0,
		len(pod.Status.ContainerStatuses)+len(pod.Status.InitContainerStatuses))
	statuses = append(statuses, pod.Status.InitContainerStatuses...)
	statuses = append(statuses, pod.Status.ContainerStatuses...)
	for _, cs := range statuses {
		s := state{restarts: int(cs.RestartCount)}
		// The starts the status still knows: this run's and the previous
		// one's. The window's samples just after them are left out of the
		// sizing (usableSamples).
		if r := cs.State.Running; r != nil && !r.StartedAt.IsZero() {
			s.starts = append(s.starts, float64(r.StartedAt.Unix()))
		}
		if t := cs.LastTerminationState.Terminated; t != nil && !t.StartedAt.IsZero() {
			s.starts = append(s.starts, float64(t.StartedAt.Unix()))
		}
		// Both the current and the previous termination count. A container killed
		// for memory and restarted reports the kill under lastState, and that is
		// the case the whole memory guard rail exists for — reading only the
		// current state would see a healthy container using very little.
		if t := cs.State.Terminated; t != nil && t.Reason == "OOMKilled" {
			s.oomKilled = true
		}
		if t := cs.LastTerminationState.Terminated; t != nil && t.Reason == "OOMKilled" {
			// Recent, or with no time on it (nothing to age it by, so it is
			// taken at its word): the alarm. Older: the quieter flag.
			if t.FinishedAt.IsZero() || now.Sub(t.FinishedAt.Time) < oomRecency {
				s.oomKilled = true
			} else {
				s.oomEarlier = true
			}
		}
		// The kubelet's own verdict, which is the only one that means "now".
		if w := cs.State.Waiting; w != nil && w.Reason == "CrashLoopBackOff" {
			s.crashLooping = true
		}
		// And the gap the verdict leaves: in the seconds after a restart the
		// container is Running, not Waiting, and its RSS is precisely the sample
		// that must not be believed.
		if t := cs.LastTerminationState.Terminated; t != nil && !t.FinishedAt.IsZero() &&
			now.Sub(t.FinishedAt.Time) < restartRecency {
			s.crashLooping = true
		}
		states[cs.Name] = s
	}

	containers := heldContainers(pod)
	for i := range containers {
		c := &containers[i]
		st := states[c.Name]
		out.Containers = append(out.Containers,
			toContainer(c, pod.Namespace, pod.Name, st.restarts, st.oomKilled, st.crashLooping, usage))
		out.Containers[len(out.Containers)-1].OOMKilledEarlier = st.oomEarlier && !st.oomKilled
		out.Containers[len(out.Containers)-1].Starts = podStarts(pod, st.starts)
	}
	return out
}

// podStarts is a container's known starts with the pod's own in front: a pod
// that started inside the window began its series warming up, whether or not
// its containers have restarted since.
func podStarts(pod *corev1.Pod, container []float64) []float64 {
	var out []float64
	if st := pod.Status.StartTime; st != nil && !st.IsZero() {
		out = append(out, float64(st.Unix()))
	}
	return append(out, container...)
}

func toContainer(c *corev1.Container, namespace, podName string, restarts int, oomKilled, crashLooping bool, usage map[string]Usage) Container {
	out := Container{
		Name:         c.Name,
		CrashLooping: crashLooping,
		CPURequest:   c.Resources.Requests.Cpu().MilliValue(),
		MemRequest:   c.Resources.Requests.Memory().Value(),
		CPULimit:     c.Resources.Limits.Cpu().MilliValue(),
		MemLimit:     c.Resources.Limits.Memory().Value(),
		RestartCount: restarts,
		OOMKilled:    oomKilled,
	}
	// The template's own two figures. The third — what the container is actually
	// using — is not in the API server at all and is filled in by attachStorage
	// from the kubelet summary, or left at zero when no kubelet answered. The
	// engine reads zero as silence rather than as a container using nothing.
	out.EphemeralRequest = c.Resources.Requests.StorageEphemeral().Value()
	out.EphemeralLimit = c.Resources.Limits.StorageEphemeral().Value()

	if u, ok := usage[namespace+"/"+podName+"/"+c.Name]; ok {
		out.HasUsage = true
		out.CPUUsage = u.CPUMillicores
		out.MemUsage = u.MemoryBytes
	}
	return out
}

func toNode(node *corev1.Node) Node {
	out := Node{
		Name:              node.Name,
		AllocatableCPU:    node.Status.Allocatable.Cpu().MilliValue(),
		AllocatableMemory: node.Status.Allocatable.Memory().Value(),
	}
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			out.Ready = c.Status == corev1.ConditionTrue
		}
	}
	out.Schedulable = schedulable(node)
	return out
}

// schedulable says whether an ordinary pod can be placed on this node: not
// cordoned, and no NoSchedule or NoExecute taint. A Ready control plane is
// usually not, and an increase capped at its size schedules nothing.
func schedulable(node *corev1.Node) bool {
	if node.Spec.Unschedulable {
		return false
	}
	for _, t := range node.Spec.Taints {
		if t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute {
			return false
		}
	}
	return true
}

// attachAutoscalers files each workload's HPAs and VPAs on it, from the index
// Autoscalers built.
func attachAutoscalers(workloads []Workload, index map[string][]Autoscaler) {
	for i := range workloads {
		w := &workloads[i]
		w.Autoscalers = index[AutoscalerKey(w.Namespace, w.Kind, w.Name)]
	}
}

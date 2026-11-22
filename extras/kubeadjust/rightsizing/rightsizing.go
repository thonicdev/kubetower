// Package rightsizing answers one question about a workload: are its resource
// requests and limits the right size, and if not, what should they be.
//
// The arithmetic here is twenty lines. **The value is in the refusals**:
//
//   - a container that was OOMKilled gets no memory reduction, ever. Its
//     post-restart RSS understates what it needs, so the honest reading of "uses
//     50 MiB of a 1 GiB limit" on a container the kernel keeps killing is that
//     the sample is worthless. An "increase the limit" danger is raised instead;
//   - a crashlooping container gets no reduction of anything, for the same
//     reason one restart level up;
//   - a bursty workload — peak at least 5x its P95 — is sized on the peak and
//     not on the mean. A JVM that needs 2 CPU for sixty seconds at startup and
//     100m afterwards must never be told to request 130m: the advice would be
//     correct on average and would fail the liveness probe on the next rollout;
//   - a Guaranteed pod has its request and limit moved together, because moving
//     one side alone silently downgrades it to Burstable and raises its eviction
//     priority — a QoS change nobody asked for, hidden inside a tuning tip.
//
// Three more refusals, each for a case the first rules never reached (see
// sample_test.go for all three):
//
//   - a workload whose every measured replica is younger than ten minutes gets
//     no reduction: RSS minutes after a start understates the steady state, so
//     a page opened right after a rollout would size caches that are not yet
//     filled;
//   - a container measured at exactly zero names no number: metrics-server
//     reports a hard zero for anything under a millicore, and "set your request
//     to 0m" is arithmetic, not advice;
//   - and one refusal that was too broad was narrowed: the OOMKilled alarm no
//     longer needs a usage sample, because the container it exists for — killed
//     so often it is rarely alive at scrape time — is precisely the one
//     metrics-server cannot see.
//
// The test files are the specification: rightsizing_test.go, and the engine
// is whatever makes it pass. If a rule here looks incidental, it is not —
// see the list above.
//
// **The rules follow current Kubernetes practice.** review_test.go holds one
// test per rule, each naming the wrong answer the engine used to give;
// targets.go holds the numbers. In short: a limit is never sized under an
// observed peak; CPU is compressible, so a missing CPU limit is not a finding
// and a CPU at its limit is a throttle (warning), not a kill (danger); one
// target per side, so applying a suggestion never triggers its opposite; no
// command the API server would refuse (a limit under its request, a request
// over its limit); an OOMKill older than a day no longer raises the alarm for
// ever; HPAs and VPAs that already size a workload are respected; a DaemonSet
// must fit on every node it runs on; and a CronJob gets its command, which
// kubectl has reached for years.
// And a memory limit raised after a kill is not lowered again once the killed
// pods are gone with the rollout (rollout.go).
//
// Two things this package deliberately is not:
//
// **It does not read a cluster.** No client-go, no HTTP, no import of
// internal/cluster or internal/metrics. It takes numbers and returns
// suggestions, so the whole of it is testable without a cluster and the caller
// decides where the numbers came from — a metrics-server snapshot or a
// Prometheus range. That is also where the caller must be honest: with no
// history there is no P95 and no burst ratio, so the guard rails that need a
// window cannot fire, and a suggestion built from one instantaneous sample must
// not be presented with the same authority as one built from a week. Each CPU
// and memory suggestion carries a Confidence level for that, and a container
// with under a day of usable series is marked InsufficientData and given no
// value at all (confidence.go). "Usable" leaves out the minutes after a start
// and every crash-looping replica: a request is sized on the P95 of CPU and the
// maximum of memory over what is left, never on the mean, while a limit stays
// above every peak observed, starts included.
//
// **It does not write.** Nothing here patches, and nothing here offers to. A
// right-sizing change is a `kubectl set resources`, which patches the pod
// template, which restarts every pod of the workload — a deployment, not a
// console convenience. ToKubectlCmd renders the command as text for the
// operator to run; whether the console should ever run it itself is not a
// question this package answers: the page is copy-only.
//
// Units are fixed and unwrapped: CPU is millicores and memory is bytes,
// everywhere, as int64, with no struct carrying an optional field per unit.
package rightsizing

import "time"

// Kind is how loudly a suggestion asks to be read. The three are ordered:
// Compute sorts danger first and overkill last, because a container about to be
// evicted outranks one wasting half a core.
type Kind string

const (
	// KindDanger is a workload at risk now: OOMKilled, at its memory limit, or
	// using twice the memory it reserved. A CPU at its limit is a throttle and
	// stops at KindWarning.
	KindDanger Kind = "danger"
	// KindWarning is a workload that will be at risk, or one whose spec leaves
	// the scheduler guessing.
	KindWarning Kind = "warning"
	// KindOverkill is money, and nothing else. It is never urgent.
	KindOverkill Kind = "overkill"
)

// Suggestion is one thing to change about one container, or — for a volume —
// one thing to change about one claim.
//
// It is workload-level and not pod-level, and that is the single most visible
// difference from reading the same numbers off a pod list: requests and limits
// live on the pod template, so a Deployment with twenty replicas produces one
// suggestion per container and resource, with usage pooled across every
// replica. Twenty identical rows is not twenty findings.
type Suggestion struct {
	// Workload is the object whose template would be edited.
	Workload string
	// WorkloadKind drives the kubectl target, and decides whether one can be
	// offered at all — see kubectlTarget.
	WorkloadKind string
	Namespace    string
	// Pod is a representative replica: a scroll target and something to match a
	// search against. The suggestion is not about this pod in particular.
	Pod string
	// PodCount is how many replicas this suggestion speaks for.
	PodCount int
	// Container is the container name, or for a volume suggestion the claim or
	// volume name — that is what the operator has to find in the manifest.
	Container string
	// Resource is what is being sized, and carries its own qualifier when the
	// finding is an absence: "CPU", "Memory — no limit", "PVC", "EmptyDir".
	Resource string
	Kind     Kind
	// Action is the verb, and it is matched on: "Reduce request", "Increase
	// limit", "Migrate to larger node". ToKubectlCmd reads it to decide which
	// flag to write.
	Action  string
	Message string
	// Current is what the spec says today, already formatted — including the
	// two cases that are not numbers, "none" and "unlimited".
	Current string
	// Suggested is SuggestedRaw formatted for reading; SuggestedRaw is the value
	// in millicores or bytes, and is what the kubectl command is built from.
	Suggested    string
	SuggestedRaw int64
	// AppliesToBoth marks a change that must move the request and the limit
	// together to keep the pod in the Guaranteed QoS class.
	AppliesToBoth bool
	// PairedLimitRaw is a limit that has to change with a request, in the same
	// command, or the request alone would be refused: an increased request
	// above the limit the container has. Zero when the request moves alone.
	PairedLimitRaw int64
	// PairedRequestRaw is a request that has to change with a limit, in the
	// same command: a limit set or reduced under the request the container has
	// now, beside the reduction of that request. Zero when the limit moves
	// alone.
	PairedRequestRaw int64
	// Warnings are caveats that do not block the suggestion: replicas that
	// disagree with each other, restarts observed, the QoS pairing above.
	Warnings []string
	// Confidence is how much the figure behind a CPU or memory suggestion
	// rests on - see confidence.go. Empty for a disk suggestion, which reads
	// a level the kubelet reports rather than sizing from a distribution.
	Confidence Confidence
	// InsufficientData marks a suggestion whose container has too little
	// usable usage to size from (confidence.go): the finding is still said,
	// and SuggestedRaw is zero, so no value is recommended and no command is
	// offered - except an OOM kill's raise, whose value comes from the limit
	// the container was killed at rather than from its usage.
	InsufficientData bool

	// fromLimit marks the OOM kill's raise: its value is computed from the
	// limit, so too little usage does not withhold it.
	fromLimit bool
}

// Workload is a Deployment, StatefulSet, DaemonSet or CronJob with its live
// pods. The kind is a string rather than an enum because it travels straight
// into a kubectl target and an unknown one must degrade to "no command offered"
// rather than to a wrong command.
type Workload struct {
	Kind      string
	Name      string
	Namespace string
	Pods      []Pod
	// Former are the names of pods this workload ran before - an earlier
	// rollout's, a finished Job's - whose series are in the window: their
	// usage is pooled with the running replicas' (pooled.go). They are not
	// replicas, and nothing else counts them.
	Former []string
	// Autoscalers are the HPAs and VPAs aimed at this workload, which change
	// what may be suggested about it - see autoscale.go.
	Autoscalers []Autoscaler
	// Rollout is what the workload's kept revisions say about its memory
	// limits, so a raise that followed a kill for memory outlives the pods
	// that were killed - see rollout.go. Nil when nothing was read: no limit
	// is then held to its history, and nothing is said about one.
	Rollout *Rollout
}

// Pod is one replica.
type Pod struct {
	Name string
	// NodeName is where the pod runs. A DaemonSet's increase is capped by the
	// smallest of its nodes, not the largest in the cluster, because it has a
	// pod on every one of them.
	NodeName string
	// Namespace is here only so a pod can find its own usage window: the index
	// is keyed namespace/pod/container, because pod names collide across
	// namespaces. Everything else about a workload's identity lives on Workload.
	Namespace string
	// QOS is the class the API server assigned: "Guaranteed", "Burstable" or
	// "BestEffort". Empty when it was not read, which suppresses the pairing
	// rule rather than guessing at it.
	QOS        string
	Containers []Container
	Volumes    []Volume
	// Age is how long the pod has been running, and AgeKnown whether the API
	// server said so at all. They exist for one refusal: usage read minutes
	// after a start understates the steady state — heaps not yet grown, caches
	// not yet filled — so a workload whose every measured replica is younger
	// than warmupWindow gets no reduction, the same shape as the post-OOM
	// refusal one level up. Unknown is deliberately not young: a refusal must
	// rest on evidence, not on a field that failed to arrive.
	//
	// Pod-level and not per-container on purpose. A container restarted moments
	// ago inside an old pod is already covered by restartRecency and
	// CrashLooping; what this catches is the fresh rollout those cannot see.
	Age      time.Duration
	AgeKnown bool
}

// Container is one container's spec and its observed state.
//
// CPU is millicores, memory is bytes, and zero means "not set in the spec" —
// which is exactly what Kubernetes means by an absent request or limit, so
// nothing is lost by collapsing them.
type Container struct {
	Name string

	CPURequest int64
	CPULimit   int64
	MemRequest int64
	MemLimit   int64

	// HasUsage separates "the kubelet did not tell us" from "it told us zero",
	// and the distinction is load-bearing: with no usage at all a container is
	// skipped entirely and its history is never consulted, because a suggestion
	// about a container nothing has measured is a guess: what nothing
	// measured is not zero.
	HasUsage bool
	CPUUsage int64
	MemUsage int64

	// EphemeralRequest, EphemeralUsage and EphemeralLimit are bytes of ephemeral
	// storage. The request and the limit come from the pod template; the usage
	// comes from the kubelet summary API and is zero on the clusters that refuse
	// nodes/proxy — and zero usage means no suggestion, so the capability
	// degrades to silence by itself. `Report.Storage` is where the page learns
	// that the silence was a refusal rather than an empty disk.
	EphemeralRequest int64
	EphemeralUsage   int64
	EphemeralLimit   int64

	// RestartCount is `ContainerStatus.RestartCount`, which is **cumulative for
	// the life of the pod and never decays**. It is a history, not a state — see
	// CrashLooping, which is the state.
	RestartCount int
	// CrashLooping is the kubelet's own verdict: the container is in
	// CrashLoopBackOff, or it terminated recently enough that its usage still
	// describes a dying process rather than a working one.
	//
	// This field exists because the engine used to read `RestartCount >= 3` as
	// "crash-looping", and that is a claim about the last six months. A
	// StatefulSet pod up since February with three restarts from node maintenance
	// was permanently crash-looping, so **every** reduction for it — CPU and
	// memory, request and limit — was suppressed for ever, silently: `db-0`
	// asking 4 cores and using 90m produced no suggestion at all and sorted into
	// the nothing-wrong group at the bottom of the page.
	//
	// Two signals rather than one, because neither covers the other. The kubelet
	// says CrashLoopBackOff while it is waiting out the backoff, which is most of
	// the time once the interval reaches five minutes — but not in the seconds
	// after a restart, when the container is Running and its RSS is exactly the
	// worthless sample the guard exists for. So a recent termination counts too.
	CrashLooping bool
	// OOMKilled is true when this container was killed for memory in its current
	// run, or in its previous one within the last day (oomRecency). It is the
	// single strongest signal in the whole package and it only ever suppresses
	// advice or raises an alarm.
	OOMKilled bool
	// OOMKilledEarlier is a kill for memory longer ago than that. The previous
	// termination stays on the status until the next one, which can be months,
	// so reading it as "now" raised a danger for ever about a container that
	// had been fine since - the same defect the restart count had. It still
	// blocks every memory reduction: the workload has reached its limit before.
	OOMKilledEarlier bool

	// Starts are the moments this container is known to have started, in unix
	// seconds: the pod's start, and the start of the current and of the
	// previous run as the status reports them. Usage in the minutes after a
	// start is a process warming up, not its working set, so the window's
	// samples there are left out of the sizing (usableSamples). Earlier
	// restarts are not on the status; a gap in the series stands in for them.
	Starts []float64
}

// Volume is one volume of one pod, as the kubelet reports its usage.
//
// PVCs stay per-pod on purpose: a StatefulSet's volumeClaimTemplates create one
// distinct physical volume per replica, so three replicas near capacity are
// three disks to expand and not one. EmptyDirs come from the pod template and
// are deduplicated by name.
type Volume struct {
	Name string
	// Type is "pvc", "emptyDir" or "other".
	Type    string
	PVCName string
	// Capacity is the claim's size in bytes; zero when it could not be read,
	// which drops the volume rather than dividing by it.
	Capacity int64
	Usage    int64
	// HasSizeLimit is whether an emptyDir declares one at all. The value itself
	// is not needed — the finding is the absence.
	HasSizeLimit bool
}

// Node is one node's allocatable capacity, which is what caps an increase.
//
// Allocatable and not capacity: the difference is what the kubelet and the
// system reserve, and suggesting a limit a pod can never be scheduled with is
// worse than suggesting nothing.
type Node struct {
	Name string
	// Ready excludes a node from the capacity ceiling when false. A NotReady
	// node's 64 cores are not somewhere a pod can go.
	Ready bool
	// Schedulable is false for a cordoned node and for one tainted NoSchedule
	// or NoExecute - a control plane, usually. The ceiling is taken over
	// schedulable nodes when there are any, because an ordinary workload
	// cannot land on the others.
	Schedulable       bool
	AllocatableCPU    int64
	AllocatableMemory int64
}

// NodeCapacity is the ceiling any increase is measured against, plus the
// per-node values needed to say how many nodes would still fit the result.
type NodeCapacity struct {
	MaxCPU      int64
	MaxMemory   int64
	ReadyCPU    []int64
	ReadyMemory []int64
	// byName is every Ready node, for a DaemonSet's ceiling - see forWorkload.
	byName map[string]Node
}

// Point is one sample of one series: T is unix seconds, V is millicores or
// bytes. Both float64 because that is what Prometheus returns, and rounding on
// the way in would throw away the only thing a trend is computed from.
type Point struct {
	T float64
	V float64
}

// ContainerHistory is one container's usage over the observed window, per
// replica. Without it there is no P95, no burst ratio and no trend — see the
// package comment on what that costs.
type ContainerHistory struct {
	// Namespace is part of the identity and not decoration. A pod name is only
	// unique inside a namespace, and a StatefulSet's are not even random:
	// `web-0` exists in staging and in production on most clusters that have
	// one. The page is read cluster-wide by default, so keying on the pod name
	// alone would give one workload the other's window — silently, and in the
	// direction that produces a suggestion from a series belonging to something
	// else.
	//
	// Empty on both sides still matches, so a unit test that does not care about
	// namespaces does not have to name one.
	Namespace string
	Pod       string
	Container string
	CPU       []Point
	Memory    []Point
}

// HistoryMap indexes history by "namespace/pod/container".
type HistoryMap map[string]ContainerHistory

// HistoryKey is the one place the index's shape is written down, so the build
// and the lookup cannot come to disagree about it.
func HistoryKey(namespace, pod, container string) string {
	return namespace + "/" + pod + "/" + container
}

// BuildHistoryMap indexes a flat history list for lookup during analysis.
func BuildHistoryMap(history []ContainerHistory) HistoryMap {
	m := make(HistoryMap, len(history))
	for _, h := range history {
		m[HistoryKey(h.Namespace, h.Pod, h.Container)] = h
	}
	return m
}

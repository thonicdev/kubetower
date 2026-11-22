package rightsizing

import (
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

// Collecting, without its reads.
//
// Listing pods, ReplicaSets, Jobs, nodes and claims, and asking the kubelets,
// is done by the extra: every read goes through extension.Host and is reviewed
// for the person behind the request. What is here is the assembly, taking what
// the extra read as Inputs.

// Inputs is what the extra read for one page, through extension.Host.
//
// Every field that can fail carries its error beside it rather than a zero
// value, because a part of the page that could not be read and a part that was
// read and held nothing must say different things.
type Inputs struct {
	// Namespace narrows the page; empty means the whole cluster.
	Namespace string
	// Now is the clock, threaded through for the crash-loop and warm-up
	// refusals - see group.
	Now time.Time

	// Usage is each container's live usage, keyed ContainerKey. UsageErr is why
	// there is none at all; a nil error with an empty map is metrics-server
	// answering with nothing in scope.
	Usage    map[string]Usage
	UsageErr error

	// Pods is every pod in scope.
	Pods []corev1.Pod
	// Owners maps `Kind/namespace/name` of a ReplicaSet or a Job onto the
	// controller that made it. OwnersErr is why it is partial.
	Owners    map[string]metav1.OwnerReference
	OwnersErr error

	// Nodes is every node, for the ceiling and the Nodes view. NodesErr is why
	// they could not be read.
	Nodes    []corev1.Node
	NodesErr error

	// Claims is each PVC's capacity, keyed `namespace/name`. ClaimsErr is why
	// the sizes could not be read.
	Claims    map[string]int64
	ClaimsErr error

	// Storage is what the kubelets said, keyed `namespace/pod`, and
	// StorageState the Disk note - see StorageFromUsage.
	Storage      map[string]PodStorage
	StorageState Availability

	// History is the usage window - see HistoryFromWindow.
	History History

	// Autoscalers are the HPAs and VPAs in scope, indexed by the workload each
	// targets (Autoscalers). AutoscalersErr is why they could not be read: the
	// page then sizes every workload as though nothing else did, and says so.
	Autoscalers    map[string][]Autoscaler
	AutoscalersErr error

	// ReplicaSets and ControllerRevisions are the workloads' kept revisions,
	// which remember a memory limit raised after a kill once the killed pods
	// are gone (rollout.go). Each error is why that list could not be read:
	// every memory limit reduction of the kinds it serves then says it could
	// not be checked.
	ReplicaSets            []appsv1.ReplicaSet
	ReplicaSetsErr         error
	ControllerRevisions    []appsv1.ControllerRevision
	ControllerRevisionsErr error
}

// Measurable is whether Assemble will read anything past the usage: with
// no usage it stops there, and the extra uses this to skip the reads the
// page would not use.
func Measurable(usage map[string]Usage, err error) bool {
	return err == nil && len(usage) > 0
}

// Assemble builds the Report from what the extra read, in the order
// the page shows it.
func Assemble(in Inputs) Report {
	var report Report

	report.History = in.History.State
	report.Window = in.History.Window
	report.WindowReadAt = in.History.ReadAt
	if report.History.Detail == "" {
		// A caller that built a History by hand. The page must never be silent
		// about the caveat, so the sentence has a default and not an empty
		// string.
		report.History = Availability{Detail: noWindow("no usage window was read")}
	}

	switch {
	case in.UsageErr != nil:
		report.Usage = Availability{Detail: ShortError(in.UsageErr)}
		return report
	case len(in.Usage) == 0:
		report.Usage = Availability{Detail: "metrics-server reported no container usage for this scope"}
		return report
	default:
		report.Usage = Availability{OK: true, Detail: "metrics-server, sampled now"}
	}

	if in.OwnersErr != nil {
		// A pod whose controller could not be looked up is still worth sizing -
		// it just reports as its own kind, and offers no kubectl command. Losing
		// the whole page over it would be the wrong trade.
		report.Usage.Detail += fmt.Sprintf(" · workload names are partial: %s", ShortError(in.OwnersErr))
	}

	var nodes []Node
	if in.NodesErr != nil {
		report.Capacity = Availability{Detail: fmt.Sprintf(
			"node capacity could not be read (%s), so no increase is capped", ShortError(in.NodesErr))}
	} else {
		for i := range in.Nodes {
			nodes = append(nodes, toNode(&in.Nodes[i]))
		}
		report.Capacity = Availability{OK: true, Detail: fmt.Sprintf("%d node(s)", len(nodes))}
	}

	workloads, skipped := group(in.Pods, in.Owners, in.Usage, in.Now)
	attachAutoscalers(workloads, in.Autoscalers)
	attachRollouts(workloads, ReadRollouts(in.ReplicaSets, in.ReplicaSetsErr, in.ControllerRevisions, in.ControllerRevisionsErr))
	report.Examined = len(workloads)
	if in.AutoscalersErr != nil {
		report.Usage.Detail += fmt.Sprintf(" · autoscalers could not be read (%s), so a workload an HPA or "+
			"a VPA already sizes is judged as if nothing did", ShortError(in.AutoscalersErr))
	}
	// Said in the Usage note rather than kept as a field of its own, because a
	// field nothing renders is a recurring defect and the note
	// is already on the page.
	if skipped > 0 {
		word := "pods"
		if skipped == 1 {
			word = "pod"
		}
		report.Usage.Detail += fmt.Sprintf(" · %d %s not running (pending, or waiting on something), "+
			"so nothing here is sized from them", skipped, word)
	}

	// Disk, before the engine runs: analyzeEphemeral and analyzeVolumes are part
	// of Compute, so a storage read that happened afterwards would produce a page
	// whose suggestion count disagreed with its own volume bars.
	storageState := in.StorageState
	if storageState.Detail == "" {
		storageState = Availability{Detail: "no kubelet was asked for disk usage"}
	}
	if in.ClaimsErr != nil && storageState.OK {
		// The kubelet answered and the claims did not, which is a real and
		// separable failure: emptyDirs are still measurable, and a PVC without
		// its declared size cannot be judged at all rather than being judged
		// against zero.
		storageState.Detail += " · claim sizes could not be read (" + ShortError(in.ClaimsErr) +
			"), so no PVC is judged"
	}
	if storageState.OK {
		attachStorage(workloads, in.Pods, in.Storage, in.Claims)
	}
	report.Storage = storageState

	// The coverage sentence, whose denominator is the page's and not
	// Prometheus's: a window over 40 containers is complete on a page showing
	// 40 and two thirds of a page showing 60.
	cpuWindow, memWindow, containers := windowedContainers(workloads, BuildHistoryMap(in.History.Containers))
	report.History = HistoryNote(report.History, cpuWindow, memWindow, containers)

	// A rollout's former pods, tied to the workload that ran them, so a week
	// of usage survives a rollout (pooled.go).
	attachFormer(workloads, in.Pods, in.Owners, in.History.Containers)
	// One memo for both: the engine and the bars take each container's
	// figures from the same pods and the same series, so they are taken once.
	memo := statsMemo{}
	report.Suggestions = compute(workloads, in.History.Containers, nodes, memo)
	report.Readings = Attach(readings(workloads, in.History.Containers, memo), report.Suggestions)
	report.Namespaces = SummariseNamespaces(in.Pods, in.Usage, report.Readings)
	if in.Namespace == "" && in.NodesErr == nil {
		// Cluster-wide only. A node recap built from one namespace's pods would
		// draw every machine as nearly empty, which is the most misleading thing
		// this page could say about capacity.
		report.Nodes = summariseNodes(in.Nodes, in.Pods, in.Usage, in.Now)
		if storageState.OK {
			AttachNodeEphemeral(report.Nodes, in.Pods, in.Storage)
		}
	}
	return report
}

// History is a usage window and whether there is one.
//
// The zero value is the honest answer for an installation with no Prometheus,
// and Assemble renders it as the sentence this page carried before any window
// was read at all.
type History struct {
	// Containers is the window, one entry per container that had one. A
	// container absent from here is not a container using nothing: it is a
	// container Prometheus said nothing about, and usageStats falls back to its
	// snapshot rather than to a zero.
	Containers []ContainerHistory
	// State is which of the four states this read landed in, in the sentence
	// the page prints. OK is true only when a window was actually read.
	State Availability
	// Window is the core's own state for the read (extension.WindowState),
	// kept so the page can say whether a Prometheus was found without parsing
	// the sentence. Empty when no window was asked for.
	Window extension.WindowState
	// ReadAt is when the window was read (extension.Window.ReadAt), so the page
	// can say how old a kept one is. Zero when unknown or not asked for.
	ReadAt time.Time
}

// HistoryFromWindow turns what Host.UsageWindow answered into this page's
// History.
//
// The core reads the window and says why there is none in Window.Detail; what
// the absence costs - no P95, no burst ratio, no trend - is this page's to add,
// and noWindow adds it. A window read and then narrowed to the namespaces the
// person may list says so after the provenance.
func HistoryFromWindow(w extension.Window) History {
	if w.State != extension.WindowRead {
		reason := w.Detail
		if reason == "" {
			reason = "no usage window was read (" + string(w.State) + ")"
		}
		return History{State: Availability{Detail: noWindow(reason)}, Window: w.State, ReadAt: w.ReadAt}
	}
	out := History{Containers: make([]ContainerHistory, 0, len(w.Containers)), Window: w.State, ReadAt: w.ReadAt}
	for key, series := range w.Containers {
		namespace, pod, container, ok := splitKey(key)
		if !ok {
			continue
		}
		h := ContainerHistory{Namespace: namespace, Pod: pod, Container: container}
		for _, s := range series.CPU {
			h.CPU = append(h.CPU, Point{T: s.T, V: s.V})
		}
		for _, s := range series.Memory {
			h.Memory = append(h.Memory, Point{T: s.T, V: s.V})
		}
		out.Containers = append(out.Containers, h)
	}
	if len(out.Containers) == 0 {
		return History{State: Availability{Detail: noWindow(
			"Prometheus answered the window query with no usable series")}, Window: extension.WindowEmpty,
			ReadAt: w.ReadAt}
	}
	out.State = Availability{OK: true, Detail: w.Detail}
	if !w.Complete && w.Note != "" {
		out.State.Detail += " · " + w.Note
	}
	return out
}

// splitKey reads extension.ContainerKey back. A pod name and a container name
// carry no slash, and a namespace carries none either, so the three parts are
// the three segments; anything else did not come from ContainerKey.
func splitKey(key string) (namespace, pod, container string, ok bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// NodesHosting is the distinct nodes the running pods in scope are placed on -
// the kubelets worth asking. See nodesHosting.
func NodesHosting(pods []corev1.Pod) []string { return nodesHosting(pods) }

// StorageFromUsage turns what the kubelets answered into this page's storage:
// what Host.Usage answered, keyed `namespace/pod`, and the Disk note.
//
// The note is OK when at least one kubelet answered, and its Detail always says
// how many of how many did - because "3 of 40 nodes answered" and "40 of 40"
// produce the same empty Volumes section on the 37 that did not, and only the
// sentence tells them apart.
func StorageFromUsage(asked []string, u extension.Usage) (map[string]PodStorage, Availability) {
	if len(asked) == 0 {
		return nil, Availability{Detail: "no running pod is placed on a node, so no kubelet was asked"}
	}
	out := make(map[string]PodStorage)
	for _, node := range u.Nodes {
		for _, pod := range node.Pods {
			out[pod.Namespace+"/"+pod.Name] = storageOf(pod)
		}
	}
	switch {
	case u.Answered == 0:
		return nil, NoStorage(u.FirstErr)
	case u.Answered < u.Asked:
		return out, Availability{OK: true, Detail: fmt.Sprintf(
			"%d of %d kubelets answered (%s); pods on the rest have no disk figures at all",
			u.Answered, u.Asked, ShortError(u.FirstErr))}
	default:
		return out, Availability{OK: true, Detail: fmt.Sprintf(
			"the kubelet summary API on %d node(s)", u.Answered)}
	}
}

// NoStorage is the Disk note when no kubelet could be asked at all: every one
// refused, or the console refused to ask them for this person.
func NoStorage(err error) Availability {
	return Availability{Detail: fmt.Sprintf(
		"no kubelet could be asked for disk usage (%s). This needs the `nodes/proxy` "+
			"permission **and** an API server that can reach each kubelet on 10250; "+
			"several managed offerings refuse the first, and a firewalled or "+
			"read-only-port-disabled kubelet answers the second with a 404 that names "+
			"neither. Ephemeral storage and volumes are therefore unmeasured rather "+
			"than empty", ShortError(err))}
}

// storageOf is one pod's entry in this package's own shape: keyed by name,
// because every reader downstream has a container or a volume in hand.
func storageOf(pod extension.PodUsage) PodStorage {
	st := PodStorage{
		Ephemeral: make(map[string]int64, len(pod.Containers)),
		Volumes:   make(map[string]VolumeStat, len(pod.Volumes)),
	}
	for _, c := range pod.Containers {
		st.Ephemeral[c.Name] = c.EphemeralBytes
	}
	for _, v := range pod.Volumes {
		st.Volumes[v.Name] = VolumeStat{UsedBytes: v.FS.UsedBytes, CapacityBytes: v.FS.CapacityBytes}
	}
	return st
}

// ShortError trims an error to something that fits on a page: its first line,
// 160 runes at most. The full text of a TLS or SSO failure is three lines of
// certificate chain. Through textutil, never a copy of it.
func ShortError(err error) string {
	if err == nil {
		return "no error was reported"
	}
	return textutil.FirstLineN(err.Error(), 160)
}

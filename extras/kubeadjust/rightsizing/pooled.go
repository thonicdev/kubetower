package rightsizing

import (
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A week of usage per workload, not per pod.
//
// Prometheus keys every series by the pod that produced it, and a rollout
// replaces every pod of a Deployment or a DaemonSet under a new name. Matched
// by the pods running now, a workload rolled out yesterday had a day of
// history and one rolled out an hour ago had none, so a week of window was a
// week only for what never changed. The series of the replicas a workload had
// before - its former pods - are therefore pooled with those of the replicas
// it has now (usageStats), and its confidence is the time the pool covers.
//
// **Which workload a former pod belonged to is read from what exists now, and
// a series that cannot be tied to exactly one workload is not used.** A pod
// the API server still lists is tied by its owner references, like any other.
// A pod that is gone has only its name, and a name is tied by the pattern its
// controller gives it, checked against the objects in its namespace:
//
//   - a Deployment's pod is `<replicaset>-<5>`, and the ReplicaSet is one the
//     listing still has - a Deployment keeps its old ReplicaSets, ten by
//     default, so the anchor is the object and not a guess at a hash;
//   - a Job's pod is `<job>-<5>`, and a listed Job leads to its CronJob;
//   - a CronJob's pod whose Job is gone is `<cronjob>-<scheduled>-<5>`, the
//     middle part digits;
//   - a StatefulSet's pod is `<statefulset>-<ordinal>`;
//   - a DaemonSet's pod is `<daemonset>-<5>`.
//
// `<5>` is the five characters the API server generates a name with, from an
// alphabet with no vowels and no 0, 1 or 3, so a word cannot pass for one.
// Every reading of a name is tried, and each is kept only when it names a
// workload on this page; a name two of them could explain - a Deployment
// `web` whose old ReplicaSet is `web-bcdf` beside a DaemonSet `web-bcdf` - is
// dropped. Using kube-state-metrics' kube_pod_owner instead was not chosen:
// it is not installed everywhere cAdvisor is, it is a second query the core
// would have to make for every page, and it also forgets a pod once the pod
// is gone.
//
// Nothing here widens what a person sees. The window the core hands over is
// already narrowed to the namespaces they may list pods in, and a former pod
// is only ever tied to a workload on this page, in its own namespace.

// suffixAlphabet is what the API server writes a generated name's random
// suffix in (k8s.io/apimachinery's rand.String).
const suffixAlphabet = "bcdfghjklmnpqrstvwxz2456789"

// windowMarker is a key no series can have (HistoryKey always holds two
// slashes). A workload's own history carries it when the page has a window,
// so a workload without a series of its own on such a page is judged as
// having too little, rather than as a page without Prometheus.
const windowMarker = "\x00window"

func generatedSuffix(s string) bool {
	if len(s) != 5 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune(suffixAlphabet, r) {
			return false
		}
	}
	return true
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// cut splits a name at its last dash: the base and the last part.
func cut(name string) (base, last string, ok bool) {
	i := strings.LastIndexByte(name, '-')
	if i <= 0 || i == len(name)-1 {
		return "", "", false
	}
	return name[:i], name[i+1:], true
}

// identity is a workload as the page knows it.
type identity struct{ kind, name string }

// formerOwners lists every workload a gone pod's name could have belonged to,
// in the objects of its namespace. More than one, or none, and the pod is not
// pooled anywhere.
func formerOwners(namespace, pod string, owners map[string]metav1.OwnerReference) map[identity]bool {
	out := map[identity]bool{}
	base, last, ok := cut(pod)
	if !ok {
		return out
	}
	if generatedSuffix(last) {
		// A ReplicaSet or a Job that is still listed, and what controls it.
		for _, kind := range []string{"ReplicaSet", "Job"} {
			if owner, listed := owners[kind+"/"+namespace+"/"+base]; listed {
				out[identity{owner.Kind, owner.Name}] = true
			}
		}
		out[identity{"DaemonSet", base}] = true
		out[identity{"Job", base}] = true
		out[identity{"ReplicaSet", base}] = true
		if cronjob, scheduled, ok := cut(base); ok && digits(scheduled) {
			out[identity{"CronJob", cronjob}] = true
		}
	}
	if digits(last) {
		out[identity{"StatefulSet", base}] = true
	}
	return out
}

// attachFormer ties the window's series of pods no workload runs now to the
// workload they belonged to, and files their names on it (Workload.Former).
//
// pods is every pod listed in scope, whatever its phase: one that is still
// listed is tied by its owner references, as group ties the running ones.
func attachFormer(workloads []Workload, pods []corev1.Pod, owners map[string]metav1.OwnerReference, history []ContainerHistory) {
	index := map[string]int{}
	running := map[string]bool{}
	for i, w := range workloads {
		index[w.Namespace+"/"+w.Kind+"/"+w.Name] = i
		for _, p := range w.Pods {
			running[p.Namespace+"/"+p.Name] = true
		}
	}
	listed := map[string]*corev1.Pod{}
	for i := range pods {
		listed[pods[i].Namespace+"/"+pods[i].Name] = &pods[i]
	}

	seen := map[string]bool{}
	for _, h := range history {
		at := h.Namespace + "/" + h.Pod
		if running[at] || seen[at] {
			continue
		}
		seen[at] = true
		var candidates map[identity]bool
		if pod, ok := listed[at]; ok {
			kind, name := attribute(pod, owners)
			candidates = map[identity]bool{{kind, name}: true}
		} else {
			candidates = formerOwners(h.Namespace, h.Pod, owners)
		}
		found := -1
		for c := range candidates {
			i, ok := index[h.Namespace+"/"+c.kind+"/"+c.name]
			if !ok {
				continue
			}
			if found >= 0 && found != i {
				found = -2 // two workloads could have had it: none gets it
				break
			}
			found = i
		}
		if found >= 0 {
			workloads[found].Former = append(workloads[found].Former, h.Pod)
		}
	}
	for i := range workloads {
		sort.Strings(workloads[i].Former)
	}
}

// workloadHistory is the part of the page's window one workload is sized
// from: its running replicas' series and its former pods', and the marker
// saying the page has a window at all.
func workloadHistory(page HistoryMap, all HistoryMap, w Workload) HistoryMap {
	out := HistoryMap{}
	if len(page) > 0 {
		out[windowMarker] = ContainerHistory{}
	}
	for _, pod := range w.Pods {
		for i := range pod.Containers {
			key := HistoryKey(pod.Namespace, pod.Name, pod.Containers[i].Name)
			if h, ok := page[key]; ok {
				out[key] = h
			}
		}
	}
	former := map[string]bool{}
	for _, name := range w.Former {
		former[name] = true
	}
	if len(former) == 0 {
		return out
	}
	for key, h := range all {
		if h.Namespace == w.Namespace && former[h.Pod] {
			out[key] = h
		}
	}
	return out
}

// formerSeries is one container's series from a workload's former pods, in a
// stable order, each without its first minutes: a former pod's series begins
// where it started, unless it was older than the window, and nothing on the
// status says which, so its first sample is taken as a start. That costs a
// pod older than the window two samples of its week.
func formerSeries(hist HistoryMap, pods []Pod, containerName string, isCPU bool) (usable, raw [][]Point) {
	live := map[string]bool{}
	for _, p := range pods {
		live[HistoryKey(p.Namespace, p.Name, containerName)] = true
	}
	keys := make([]string, 0, len(hist))
	for key, h := range hist {
		if key != windowMarker && h.Container == containerName && !live[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		points := hist[key].Memory
		if isCPU {
			points = hist[key].CPU
		}
		if len(points) < 2 {
			continue
		}
		raw = append(raw, points)
		if kept := usableSamples(points, []float64{points[0].T}); len(kept) >= 2 {
			usable = append(usable, kept)
		}
	}
	return usable, raw
}

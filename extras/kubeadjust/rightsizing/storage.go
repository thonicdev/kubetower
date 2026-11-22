package rightsizing

// Disk, the one resource neither the API server nor metrics-server can answer
// for: actual usage is known only to the kubelet, asked by the core through
// `Host.Usage` (`nodes/proxy`). Several managed offerings refuse that, so the
// capability degrades: a pod on a node that did not answer is unmeasured, never
// a pod using no disk. This file holds the right-sizing part, which reads
// nothing: which nodes to ask, trustworthy(), and how the answer lands on
// workloads.

import (
	"sort"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

// VolumeStat is what the kubelet says about one mounted volume.
type VolumeStat struct {
	UsedBytes int64
	// CapacityBytes is the *filesystem's* capacity as the kubelet measured it,
	// which is not always the volume's declared size — see trustworthy.
	CapacityBytes int64
}

// PodStorage is one pod's disk, as its own kubelet reports it.
//
// A pod absent from the map was not measured, so nothing here ever inserts an
// empty entry.
type PodStorage struct {
	// Ephemeral is bytes per container: its writable layer plus its logs, which
	// is what `ephemeral-storage` actually accounts for.
	Ephemeral map[string]int64
	// Volumes is keyed by the volume's name in the pod spec.
	Volumes map[string]VolumeStat
}

// nodesHosting is the distinct nodes the pods in scope are placed on, sorted so
// that two reads of one cluster ask in the same order.
//
// Only those nodes are asked, not every node of the cluster.
func nodesHosting(pods []corev1.Pod) []string {
	seen := map[string]bool{}
	for i := range pods {
		pod := &pods[i]
		if pod.Spec.NodeName == "" || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		seen[pod.Spec.NodeName] = true
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// trustworthy decides whether the kubelet's filesystem figures describe this
// claim.
//
// The kubelet reports what `statfs()` says about the mount point, which on a
// network filesystem is the whole share: a 5 GiB PVC on a 40 TiB NFS export
// would read as a claim at 30 %, stably wrong. So the figures are used only
// when the measured filesystem is within 10 % of the claim's declared size (a
// block device or a directory of its own); otherwise usage is unmeasured and
// the engine emits no finding. 10 %, not equality, because ext4's reserve and
// metadata make a 5 GiB device report slightly less.
//
// The rule lives in the core, `extension.Trustworthy`, which the PVC list also
// uses; this delegates to it.
func trustworthy(declared, measured int64) bool {
	return extension.Trustworthy(declared, measured)
}

// attachStorage sets each container's ephemeral usage and each pod's volumes,
// onto workloads already grouped. A pod the kubelet did not report on is left
// at zero, which the engine reads as "do not judge this".
func attachStorage(workloads []Workload, pods []corev1.Pod, storage map[string]PodStorage, claims map[string]int64) {
	spec := make(map[string]*corev1.Pod, len(pods))
	for i := range pods {
		spec[pods[i].Namespace+"/"+pods[i].Name] = &pods[i]
	}

	for w := range workloads {
		for p := range workloads[w].Pods {
			pod := &workloads[w].Pods[p]
			source := spec[workloads[w].Namespace+"/"+pod.Name]
			if source == nil {
				continue
			}
			key := source.Namespace + "/" + source.Name
			st, measured := storage[key]
			if !measured {
				continue
			}
			for c := range pod.Containers {
				pod.Containers[c].EphemeralUsage = st.Ephemeral[pod.Containers[c].Name]
			}
			pod.Volumes = volumesOf(source, st, claims)
		}
	}
}

// volumesOf turns one pod's spec and its kubelet figures into the volumes this
// package judges.
//
// Only PVCs and emptyDirs: a configMap, Secret, projected token or hostPath
// is not sized by whoever wrote the workload.
func volumesOf(pod *corev1.Pod, st PodStorage, claims map[string]int64) []Volume {
	var out []Volume
	for i := range pod.Spec.Volumes {
		v := &pod.Spec.Volumes[i]
		stat := st.Volumes[v.Name]

		switch {
		case v.PersistentVolumeClaim != nil:
			claim := v.PersistentVolumeClaim.ClaimName
			vol := Volume{
				Name:     v.Name,
				Type:     "pvc",
				PVCName:  claim,
				Capacity: claims[pod.Namespace+"/"+claim],
			}
			// The claim's own size is the ceiling; the kubelet's usage is only
			// believed when it is measuring this claim and not a shared export.
			if trustworthy(vol.Capacity, stat.CapacityBytes) {
				vol.Usage = stat.UsedBytes
			}
			out = append(out, vol)

		case v.EmptyDir != nil:
			// An emptyDir is the node's filesystem, so no guard is needed.
			vol := Volume{
				Name:         v.Name,
				Type:         "emptyDir",
				Usage:        stat.UsedBytes,
				Capacity:     stat.CapacityBytes,
				HasSizeLimit: v.EmptyDir.SizeLimit != nil,
			}
			if vol.HasSizeLimit {
				vol.Capacity = v.EmptyDir.SizeLimit.Value()
			}
			out = append(out, vol)
		}
	}
	return out
}

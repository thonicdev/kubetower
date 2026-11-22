package web

// How full each PersistentVolumeClaim is: the Used column of the two storage
// lists, read from the kubelet's stats summary through the node proxy.
//
// Not Prometheus: its `kubelet_volume_stats_*` are scraped from this same
// endpoint, many clusters have no Prometheus, reaching one needs a port forward
// per read, and `internal/kubeletstats` already reads this source for other
// columns. The cost is `nodes/proxy`, which several managed offerings refuse, so
// most of this file makes each missing figure say why.
//
// Claims on a shared filesystem (NFS, CephFS) each report the share's size under
// their own name, so every kubelet answers and no figure is about the claim;
// `kubeletstats.Trustworthy` refuses those.
//
// Cost: nothing on the render. It rides `usageFor`'s cache, one attempt per
// cluster per ten seconds in the background. Per attempt: one PVC list, one pod
// list, and one proxied request per node hosting a running pod with a claim
// mounted, `kubeletstats.Workers` at a time. Bounded by nodes, not claims.

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/thonicdev/kubetower/internal/kubeletstats"
	"github.com/thonicdev/kubetower/internal/resources"
)

// claimFacts is what the claim itself says about its own size, and which
// PersistentVolume is behind it.
type claimFacts struct {
	// declared is the claim's own capacity (bound, or requested when unbound): the
	// bar's denominator and the number a person would grow, never the filesystem the
	// kubelet found under the mount point.
	declared int64
	// volume is `spec.volumeName`, how the PersistentVolumes list finds the same
	// reading.
	volume string
}

// volumeUsage is the fullness of every claim in scope, keyed `namespace/name`.
// nil means the question could not be asked (no client, or the claims would not
// list) and the rows fall back to their own sentence. An entry with `Measured:
// false` says specifically why there is no figure.
func (s *Server) volumeUsage(ctx context.Context, cluster, namespace string) map[string]resources.Volume {
	if s.clusters == nil {
		return nil
	}
	client, err := s.clusters.Client(cluster)
	if err != nil || client == nil || client.Clients == nil {
		return nil
	}
	return readVolumeUsage(ctx, client.Clients, namespace)
}

// readVolumeUsage is volumeUsage against a bare client, so a test can drive the
// join with a fake.
func readVolumeUsage(ctx context.Context, clients kubernetes.Interface, namespace string) map[string]resources.Volume {
	claims, err := listClaims(ctx, clients, namespace)
	if err != nil || len(claims) == 0 {
		// No claims, or claims that will not list: the list page already reports that.
		return nil
	}
	return volumeUsageOf(ctx, clients, namespace, claims)
}

// volumeUsageOf is readVolumeUsage for claims already listed, so a caller that
// needs the claims itself (the PersistentVolumes list, for `spec.volumeName`)
// does not list them a second time.
func volumeUsageOf(ctx context.Context, clients kubernetes.Interface, namespace string,
	claims map[string]claimFacts) map[string]resources.Volume {

	if len(claims) == 0 {
		return nil
	}
	// Cached and Paged: with namespace "" this is every pod in the cluster on a
	// timer, and the Limit that Cached carries is only safe because Paged follows
	// the token. A truncated pod list would lose the mounts of later claims.
	pods, err := resources.Paged(ctx, resources.Cached(),
		clients.CoreV1().Pods(namespace).List,
		func(l *corev1.PodList) []corev1.Pod { return l.Items })
	if err != nil {
		// The claims are known and where they are mounted is not.
		return nil
	}
	mounts := claimMounts(pods, namespace)

	summaries, coverage := kubeletstats.ReadNodes(ctx, clients, nodesToAsk(mounts))
	return joinVolumeUsage(claims, mounts, summaries, coverage)
}

// listClaims reads what each claim says about its own size.
func listClaims(ctx context.Context, clients kubernetes.Interface, namespace string) (map[string]claimFacts, error) {
	items, err := resources.Paged(ctx, resources.Cached(),
		clients.CoreV1().PersistentVolumeClaims(namespace).List,
		func(l *corev1.PersistentVolumeClaimList) []corev1.PersistentVolumeClaim { return l.Items })
	if err != nil {
		return nil, err
	}
	out := make(map[string]claimFacts, len(items))
	for i := range items {
		pvc := &items[i]
		facts := claimFacts{volume: pvc.Spec.VolumeName}
		if size, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
			facts.declared = size.Value()
		} else if size, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
			// Not bound: what was asked for is the only figure, as in the Capacity column.
			facts.declared = size.Value()
		}
		out[pvc.Namespace+"/"+pvc.Name] = facts
	}
	return out, nil
}

// claimMounts is which nodes have a running pod with each claim mounted. A list,
// because an RWX claim is mounted on several nodes and any can answer. Running
// only: a Pending pod's node has not mounted it and a Succeeded one's has
// unmounted it. A pod speaks only for a claim in its own namespace, since a claim
// reference is namespace-local.
func claimMounts(pods []corev1.Pod, namespace string) map[string][]string {
	out := map[string][]string{}
	seen := map[string]bool{}
	for i := range pods {
		pod := &pods[i]
		if pod.Spec.NodeName == "" || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		if namespace != "" && pod.Namespace != namespace {
			continue
		}
		for v := range pod.Spec.Volumes {
			claim := pod.Spec.Volumes[v].PersistentVolumeClaim
			if claim == nil || claim.ClaimName == "" {
				continue
			}
			key := pod.Namespace + "/" + claim.ClaimName
			if seen[key+"\x00"+pod.Spec.NodeName] {
				continue
			}
			seen[key+"\x00"+pod.Spec.NodeName] = true
			out[key] = append(out[key], pod.Spec.NodeName)
		}
	}
	for key := range out {
		sort.Strings(out[key])
	}
	return out
}

// nodesToAsk is the distinct nodes worth a proxied request, sorted so a partial
// answer is reproducible.
func nodesToAsk(mounts map[string][]string) []string {
	seen := map[string]bool{}
	for _, nodes := range mounts {
		for _, node := range nodes {
			seen[node] = true
		}
	}
	out := make([]string, 0, len(seen))
	for node := range seen {
		out = append(out, node)
	}
	sort.Strings(out)
	return out
}

// joinVolumeUsage is the whole judgment as a pure function, so the refusals can
// be tested. Every claim in scope gets an entry, unlike the rest of the console:
// a list page has no banner for "3 of 40 kubelets answered", so the sentence
// travels with its rows (see resources.Volume).
func joinVolumeUsage(
	claims map[string]claimFacts,
	mounts map[string][]string,
	summaries map[string]kubeletstats.Summary,
	coverage kubeletstats.Coverage,
) map[string]resources.Volume {

	measured := volumesByClaim(summaries)
	out := make(map[string]resources.Volume, len(claims))

	for key, facts := range claims {
		nodes := mounts[key]
		if len(nodes) == 0 {
			// Nothing running has it open, so there is nothing to read and no permission
			// would change it.
			out[key] = resources.Volume{Note: "No running pod has this claim mounted, so no kubelet has measured it."}
			continue
		}

		fs, node, found := firstMeasured(measured, key, nodes)
		if !found {
			out[key] = resources.Volume{Note: unmeasuredNote(nodes, summaries, coverage)}
			continue
		}

		// On NFS and CephFS statfs() returns the share, so a small claim on a big NAS
		// reads stable, plausible and wrong (see kubeletstats.Trustworthy).
		if !kubeletstats.Trustworthy(facts.declared, fs.CapacityBytes) {
			out[key] = resources.Volume{Note: fmt.Sprintf(
				"Unmeasurable. The kubelet on %s reports the filesystem under this "+
					"mount as %s, against a claim of %s — so it is measuring a shared "+
					"export (NFS, CephFS) and not this claim. Its usage figure would be "+
					"the whole share's: stable, plausible, and about somebody else's disk.",
				node, sizeOf(fs.CapacityBytes), sizeOf(facts.declared))}
			continue
		}

		out[key] = resources.Volume{
			Measured:      true,
			UsedBytes:     fs.UsedBytes,
			CapacityBytes: facts.declared,
			// A clause, not a sentence: the hover leads with `used / total`.
			Note: fmt.Sprintf("as the kubelet on %s measured it", node),
		}
	}
	return out
}

// volumesByClaim indexes every kubelet's answer by node and by the claim the
// kubelet itself named (`pvcRef`), never by a join through the pod spec, which is
// silent when backwards (see kubeletstats.Volume.PVCRef).
func volumesByClaim(summaries map[string]kubeletstats.Summary) map[string]map[string]kubeletstats.FS {
	out := make(map[string]map[string]kubeletstats.FS, len(summaries))
	for node, summary := range summaries {
		byClaim := map[string]kubeletstats.FS{}
		for _, pod := range summary.Pods {
			for _, volume := range pod.Volumes {
				if volume.PVCRef == "" {
					continue
				}
				// A zero capacity is not a filesystem, and would divide by nothing.
				if volume.FS.CapacityBytes <= 0 {
					continue
				}
				byClaim[volume.PVCRef] = volume.FS
			}
		}
		out[node] = byClaim
	}
	return out
}

// firstMeasured is the first of a claim's sorted nodes that reported it, so an
// RWX claim does not flip between readings.
func firstMeasured(measured map[string]map[string]kubeletstats.FS, claim string, nodes []string) (kubeletstats.FS, string, bool) {
	for _, node := range nodes {
		if fs, ok := measured[node][claim]; ok {
			return fs, node, true
		}
	}
	return kubeletstats.FS{}, "", false
}

// unmeasuredNote says which silence this is: a kubelet that did not answer
// (the sentence then gives both counts, "3 of 40 kubelets answered"), or one
// that answered without this volume.
func unmeasuredNote(nodes []string, summaries map[string]kubeletstats.Summary, coverage kubeletstats.Coverage) string {
	answered := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if _, ok := summaries[node]; ok {
			answered = append(answered, node)
		}
	}

	if len(answered) > 0 {
		// Answered without this volume: a pod placed but not yet mounted, or a volume no
		// longer accounted for. Not a permission problem.
		return fmt.Sprintf("The kubelet on %s answered and reported no filesystem for "+
			"this claim. That is usually a pod that has been placed but has not "+
			"finished mounting it.", answered[0])
	}

	where := "the node holding it"
	if len(nodes) == 1 {
		where = nodes[0]
	}
	detail := ""
	if coverage.FirstErr != nil {
		detail = " (" + kubeletstats.ShortError(coverage.FirstErr) + ")"
	}
	return fmt.Sprintf("%d of %d kubelet(s) answered%s, and %s was not among them. "+
		"This read needs the `nodes/proxy` permission **and** an API server that can "+
		"reach each kubelet on 10250; several managed offerings refuse the first, and "+
		"a firewalled kubelet answers the second with a 404 that names neither. This "+
		"claim is therefore unmeasured rather than empty.",
		coverage.Answered, coverage.Asked, detail, where)
}

// sizeOf formats bytes in the binary units the Capacity column prints, as a
// claim is written `8Gi`.
func sizeOf(bytes int64) string {
	const (
		kib = 1 << 10
		mib = 1 << 20
		gib = 1 << 30
		tib = 1 << 40
	)
	switch {
	case bytes >= tib:
		return fmt.Sprintf("%.1f TiB", float64(bytes)/tib)
	case bytes >= gib:
		return fmt.Sprintf("%.1f GiB", float64(bytes)/gib)
	case bytes >= mib:
		return fmt.Sprintf("%d MiB", bytes/mib)
	default:
		return fmt.Sprintf("%d KiB", bytes/kib)
	}
}

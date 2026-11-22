package web

// How full each node's own filesystem is, the Nodes list's Disk gauge. No
// Kubernetes API answers it: only the kubelet has run `statfs()`, reached through
// the API server's node proxy (`internal/kubeletstats`).
//
// Cost: one proxied request per Ready node, at most five at a time, at most once
// every ten seconds per cluster, on `usageFor`'s cache and background goroutine,
// so never on a render's path. It scales with the node count, which is why it is
// offered on nodes and not on kinds that can have thousands of rows.

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/thonicdev/kubetower/internal/kubeletstats"
	"github.com/thonicdev/kubetower/internal/resources"
)

// nodeDisk is the node filesystem figures, keyed by node name. Every failure is
// an absent key, never a zero (refused, no `nodes/proxy` grant, NotReady), so the
// gauge draws a dash: an empty gauge would read as an idle disk.
func (s *Server) nodeDisk(ctx context.Context, cluster string) map[string]resources.Disk {
	if s.clusters == nil {
		return nil
	}
	client, err := s.clusters.Client(cluster)
	if err != nil || client == nil || client.Clients == nil {
		return nil
	}
	return readNodeDiskWith(ctx, client.Clients, func(ctx context.Context, nodes []string) (map[string]kubeletstats.Summary, kubeletstats.Coverage) {
		return s.kubeletSummaries(ctx, client.Context, client.Clients, nodes)
	})
}

// readNodeDisk is nodeDisk against a bare client, so a test can drive it with a
// fake. The node list goes through resources.Cached (`ResourceVersion: "0"`, kept
// off etcd's quorum read, since this runs on a timer) and resources.Paged, since
// Cached carries a Limit and a list without its continue token would stop
// silently at the limit.
func readNodeDisk(ctx context.Context, clients kubernetes.Interface) map[string]resources.Disk {
	return readNodeDiskWith(ctx, clients, func(ctx context.Context, nodes []string) (map[string]kubeletstats.Summary, kubeletstats.Coverage) {
		return kubeletstats.ReadNodes(ctx, clients, nodes)
	})
}

// readNodeDiskWith is readNodeDisk with the kubelet read supplied: nodeDisk hands
// it the server's kubelet cache, so the Disk column and Host.Usage share one read
// of each kubelet.
func readNodeDiskWith(ctx context.Context, clients kubernetes.Interface,
	read func(context.Context, []string) (map[string]kubeletstats.Summary, kubeletstats.Coverage)) map[string]resources.Disk {
	nodes, err := resources.Paged(ctx, resources.Cached(),
		clients.CoreV1().Nodes().List,
		func(l *corev1.NodeList) []corev1.Node { return l.Items })
	if err != nil {
		return nil
	}

	summaries, coverage := read(ctx, readyNodes(nodes))
	if coverage.Answered == 0 {
		return nil
	}

	out := make(map[string]resources.Disk, len(summaries))
	for node, summary := range summaries {
		// A kubelet that answered without a `node.fs` block is left out, not entered as
		// an empty disk.
		if !summary.NodeFSMeasured {
			continue
		}
		out[node] = resources.Disk{
			UsedBytes:     summary.NodeFS.UsedBytes,
			CapacityBytes: summary.NodeFS.CapacityBytes,
		}
	}
	return out
}

// readyNodes is which nodes are worth asking. A NotReady node's kubelet will not
// answer a proxied request either and would hold a worker slot for a timeout;
// its row already says NotReady.
func readyNodes(nodes []corev1.Node) []string {
	out := make([]string, 0, len(nodes))
	for i := range nodes {
		for _, condition := range nodes[i].Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				out = append(out, nodes[i].Name)
				break
			}
		}
	}
	return out
}

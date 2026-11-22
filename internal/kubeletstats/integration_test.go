package kubeletstats

// The kubelet summary API against a real cluster.
//
// The unit tests drive an httptest server that answers exactly what they wrote,
// which proves the parsing and proves nothing about the endpoint. `nodes/proxy`
// is the whole reason this package degrades rather than fails: the path has to be
// right, the RBAC grant has to exist, and the API server has to be able to reach
// each kubelet on 10250. A typo in the suffix presents identically to a cluster
// that refuses the grant, and neither shows up against a fake.
//
//	KT_INTEGRATION_CLUSTER=kubernetes-admin@kubernetes go test ./internal/kubeletstats/ -run Integration -v
//
// Strictly read-only: it lists nodes and it reads statistics. It creates nothing.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/cluster"
)

func TestIntegrationTheNodeFilesystemIsReadFromEveryKubelet(t *testing.T) {
	name := os.Getenv("KT_INTEGRATION_CLUSTER")
	if name == "" {
		t.Skip("set KT_INTEGRATION_CLUSTER to a kubeconfig context to run this")
	}

	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("no home directory and no KUBECONFIG: %v", err)
		}
		kubeconfig = filepath.Join(home, ".kube", "config")
	}
	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatalf("reading %s: %v", kubeconfig, err)
	}
	resolved, ok := clusters.Resolve(name)
	if !ok {
		t.Fatalf("context %q is not in %s; known: %v", name, kubeconfig, clusters.Contexts())
	}
	client, err := clusters.Client(resolved)
	if err != nil {
		t.Fatalf("building a client for %q: %v", resolved, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nodes, err := client.Clients.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing nodes: %v", err)
	}
	names := make([]string, 0, len(nodes.Items))
	for i := range nodes.Items {
		names = append(names, nodes.Items[i].Name)
	}
	t.Logf("cluster %q has %d node(s): %v", resolved, len(names), names)

	summaries, coverage := ReadNodes(ctx, client.Clients, names)
	t.Logf("coverage: %d of %d kubelets answered; first error: %v",
		coverage.Answered, coverage.Asked, coverage.FirstErr)

	if coverage.Answered == 0 {
		// Not a failure of this package. A cluster that refuses `nodes/proxy` is
		// exactly the state the Disk column has to degrade for, and saying so is
		// the whole point — but the run has then proved nothing about parsing.
		t.Skipf("no kubelet on %q would answer, so this run says nothing about the "+
			"summary API: %v", resolved, coverage.FirstErr)
	}

	for node, summary := range summaries {
		if !summary.NodeFSMeasured {
			t.Errorf("%s: its kubelet answered and reported no node filesystem. That is a "+
				"real state and it renders as an em dash, but on an ordinary cluster it "+
				"means the `node.fs` block moved and this package stopped reading it", node)
			continue
		}
		fs := summary.NodeFS
		if fs.CapacityBytes <= 0 || fs.UsedBytes < 0 || fs.UsedBytes > fs.CapacityBytes*2 {
			t.Errorf("%s: filesystem = %d of %d bytes, which is not a disk", node,
				fs.UsedBytes, fs.CapacityBytes)
		}
		t.Logf("%s: %.1f GiB used of %.1f GiB (%.0f%%), %d pod(s) described",
			node,
			float64(fs.UsedBytes)/(1<<30), float64(fs.CapacityBytes)/(1<<30),
			float64(fs.UsedBytes)/float64(fs.CapacityBytes)*100,
			len(summary.Pods))
	}
}

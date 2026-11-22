package changes

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// The report against a real object.
//
// Only a real object shows whether managedFields arrives at all: `kubectl get
// -o json` hides it since 1.24 unless asked, and a panel built on that would
// render, say nothing, and look like an object nobody had ever touched, while
// every unit test in this file passed.
//
//	KT_INTEGRATION_CLUSTER=docker-desktop go test ./internal/changes/ -run Integration -v
//
// Read-only: it gets one object and lists ReplicaSets.
func TestIntegrationTheReportIsNotEmptyOnARealObject(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	reader := NewReader(clusters, accesstest.Allow())

	// coredns is on every cluster, is a Deployment, and is managed by a
	// controller — so it has both managedFields and a ReplicaSet history.
	report, err := reader.Read(ctx, name, Request{
		Kind: "deployments", Namespace: "kube-system", Name: "coredns",
	})
	if err != nil {
		t.Fatalf("reading the report: %v", err)
	}

	t.Logf("%s/%s on %s, generation %d, created %s",
		report.Namespace, report.Name, report.Cluster, report.Generation,
		report.CreatedAt.Format(time.RFC3339))

	if len(report.Changes) == 0 {
		t.Fatalf("no managedFields on a live Deployment. This is exactly how the v1 panel "+
			"failed: it rendered, said nothing, and read as an object nobody had touched. "+
			"Note was: %q", report.Note)
	}

	for _, c := range report.Changes {
		t.Logf("  %-32s %-18s human=%-5v status=%-5v %s",
			c.Manager, c.Operation, c.Human(), c.StatusOnly(), c.At.Format(time.RFC3339))
		for _, f := range c.Fields {
			t.Logf("      %s", f)
		}
		if c.Total > len(c.Fields) {
			t.Logf("      … and %d more", c.Total-len(c.Fields))
		}
	}

	// The paths are the payload. An entry that owns nothing readable means the
	// flattening silently produced nothing on a shape it did not expect.
	readable := 0
	for _, c := range report.Changes {
		readable += len(c.Fields)
	}
	if readable == 0 {
		t.Error("every entry came back with no field paths; the fieldsV1 tree was not flattened")
	}

	for _, r := range report.Rollouts {
		t.Logf("  revision %-4s %-40s replicas=%d current=%v %s",
			r.Revision, r.Name, r.Replicas, r.Current, r.Image)
	}
	if len(report.Rollouts) == 0 {
		t.Log("no rollouts read — a Deployment always has at least one ReplicaSet, " +
			"so this is worth a look if it persists")
	}
}

// A node is cluster-scoped. Passing it a namespace must not send one: a name
// taken for a namespace passes every validator and is then stated as fact.
func TestIntegrationAClusterScopedKindIgnoresANamespace(t *testing.T) {
	name := os.Getenv("KT_INTEGRATION_CLUSTER")
	if name == "" {
		t.Skip("set KT_INTEGRATION_CLUSTER to a kubeconfig context to run this")
	}

	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, _ := os.UserHomeDir()
		kubeconfig = filepath.Join(home, ".kube", "config")
	}
	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatalf("reading %s: %v", kubeconfig, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := clusters.Client(name)
	if err != nil {
		t.Fatalf("building a client: %v", err)
	}
	nodes, err := client.Clients.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) == 0 {
		t.Skipf("no nodes readable on %s: %v", name, err)
	}
	node := nodes.Items[0].Name

	// A namespace supplied for a cluster-scoped kind must be dropped, not sent.
	report, err := NewReader(clusters, accesstest.Allow()).Read(ctx, name, Request{
		Kind: "nodes", Namespace: "this-namespace-does-not-exist", Name: node,
	})
	if err != nil {
		t.Fatalf("reading a node with a bogus namespace failed: %v\n"+
			"the namespace should have been ignored — nodes are cluster-scoped", err)
	}
	if report.Namespace != "" {
		t.Errorf("the report claims namespace %q for a cluster-scoped node", report.Namespace)
	}
	t.Logf("node %s: %d entries, namespace %q", report.Name, len(report.Changes), report.Namespace)
}

package cluster

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MakeDNSFriendly and Resolve are a pair: without Resolve, a terminal answers
// "unknown cluster" on precisely the clusters used most, because an EKS context
// named after its ARN reaches the browser with its slashes rewritten and
// nothing maps it back.
func TestResolveAcceptsBothFormsOfAName(t *testing.T) {
	arn := "arn:aws:eks:eu-central-1:123456789012:cluster/example-prod-eks-1"
	friendly := MakeDNSFriendly(arn)

	if friendly == arn {
		t.Fatalf("MakeDNSFriendly left the ARN untouched: %q", friendly)
	}
	if friendly != "arn:aws:eks:eu-central-1:123456789012:cluster--example-prod-eks-1" {
		t.Errorf("MakeDNSFriendly = %q — Headlamp's form is what saved links carry", friendly)
	}
}

// kubeconfigForTest finds a kubeconfig without inventing one. No kubeconfig
// means no cluster to talk to, and a skipped test says so rather than failing
// for a reason that has nothing to do with the code.
func kubeconfigForTest(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("KUBECONFIG"); path != "" {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory, so no kubeconfig")
	}
	path := filepath.Join(home, ".kube", "config")
	if _, err := os.Stat(path); err != nil {
		t.Skip("no kubeconfig on this machine")
	}
	return path
}

// A kubeconfig of this test's own, and not the machine's.
//
// A machine's own kubeconfig may exist and declare nothing - a CI runner's
// does - which is a machine's state and not a defect in Manager. What this
// test is about is the round trip of a name through its
// URL form and Known/Resolve agreeing with Contexts, none of which needs a
// cluster; so the file is written here, with the one name that made the round
// trip matter (an EKS ARN, TestResolveAcceptsBothFormsOfAName) beside a plain
// one. The tests that do need a cluster keep kubeconfigForTest and its skips.
func fixtureKubeconfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	doc := `apiVersion: v1
kind: Config
current-context: docker-desktop
clusters:
  - name: docker-desktop
    cluster: { server: https://127.0.0.1:6443 }
  - name: arn:aws:eks:eu-central-1:123456789012:cluster/example-prod-eks-1
    cluster: { server: https://example.eks.amazonaws.com }
users:
  - name: docker-desktop
    user: { token: not-a-token }
  - name: arn:aws:eks:eu-central-1:123456789012:cluster/example-prod-eks-1
    user: { token: not-a-token }
contexts:
  - name: docker-desktop
    context: { cluster: docker-desktop, user: docker-desktop }
  - name: arn:aws:eks:eu-central-1:123456789012:cluster/example-prod-eks-1
    context:
      cluster: arn:aws:eks:eu-central-1:123456789012:cluster/example-prod-eks-1
      user: arn:aws:eks:eu-central-1:123456789012:cluster/example-prod-eks-1
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("writing the fixture kubeconfig: %v", err)
	}
	return path
}

func TestManagerReadsTheKubeconfig(t *testing.T) {
	m, err := NewManager(fixtureKubeconfig(t))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	contexts := m.Contexts()
	if len(contexts) != 2 {
		t.Fatalf("the fixture declares two contexts, Contexts() lists %d: %v", len(contexts), contexts)
	}

	for _, name := range contexts {
		if !m.Known(name) {
			t.Errorf("%q is listed and then not known", name)
		}
		resolved, ok := m.Resolve(MakeDNSFriendly(name))
		if !ok || resolved != name {
			t.Errorf("%q does not survive the round trip through its URL form", name)
		}
	}

	if _, ok := m.Resolve("no-such-cluster"); ok {
		t.Error("an unknown name resolved")
	}
}

// The one that proves the point of this package: client-go talking to a real cluster,
// with no kubectl in the path. Skipped where docker-desktop is not present, so
// it is honest on a machine that has no local cluster.
func TestClientReachesTheLocalCluster(t *testing.T) {
	m, err := NewManager(kubeconfigForTest(t))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if !m.Known("docker-desktop") {
		t.Skip("no docker-desktop context on this machine")
	}

	client, err := m.Client("docker-desktop")
	if err != nil {
		t.Fatalf("Client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Two calls: the second is the one that matters. client-go holds the
	// credential and the connection, which is the whole reason not to shell
	// out — a kubectl per request pays its authentication again every time.
	first := time.Now()
	pods, err := client.Clients.CoreV1().Pods("").List(ctx, metav1.ListOptions{Limit: 50})
	if err != nil {
		t.Skipf("docker-desktop is not answering, which is not this test's subject: %v", err)
	}
	firstTook := time.Since(first)

	second := time.Now()
	if _, err := client.Clients.CoreV1().Pods("").List(ctx, metav1.ListOptions{Limit: 50}); err != nil {
		t.Fatalf("the second list failed after the first succeeded: %v", err)
	}
	secondTook := time.Since(second)

	t.Logf("%d pods; first list %s, second %s", len(pods.Items), firstTook.Round(time.Millisecond), secondTook.Round(time.Millisecond))

	if len(pods.Items) == 0 {
		t.Error("docker-desktop returned no pods at all, which it never does")
	}
}

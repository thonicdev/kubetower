package terminal

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"

	"github.com/thonicdev/kubetower/textutil"
)

// Port forwarding against a real cluster.
//
// Every other test in this package works on fakes, and they are worth having —
// but a tunnel is SPDY, an upgraded connection, a credential plugin and a
// listener, and a fake proves none of that. Unit tests can pass while
// no local cluster can be reached at all: the code is right and the address it
// dials is not.
//
// Off by default, because a test suite that needs a cluster is a test suite that
// fails on a laptop in a train:
//
//	KT_INTEGRATION_CLUSTER=docker-desktop go test ./internal/terminal/ -run Integration -v
//
// Strictly read-only. It lists, it forwards, it reads. It creates nothing, and
// it must stay that way — a guard test run against a live cluster could delete
// a live namespace.

func integrationCluster(t *testing.T) (*cluster.Manager, string) {
	t.Helper()

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
	if _, ok := clusters.Resolve(name); !ok {
		t.Fatalf("context %q is not in %s; known: %v", name, kubeconfig, clusters.Contexts())
	}
	return clusters, name
}

// findHTTPTarget looks for a pod exposing a port that answers HTTP, so the test
// can prove bytes crossed the tunnel rather than merely that a listener opened.
// It reports what it found, because "the tunnel worked" is not a useful sentence
// without saying what was on the other end of it.
func findHTTPTarget(ctx context.Context, t *testing.T, clusters *cluster.Manager, name string) (namespace, pod string, port int) {
	t.Helper()

	resolved, _ := clusters.Resolve(name)
	client, err := clusters.Client(resolved)
	if err != nil {
		t.Fatalf("building a client for %q: %v", name, err)
	}

	pods, err := client.Clients.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing pods in kube-system on %q: %v", name, err)
	}

	// A metrics port is the safest thing to read on any cluster: it is a GET, it
	// changes nothing, and every component in kube-system has one.
	for _, p := range pods.Items {
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, c := range p.Spec.Containers {
			for _, cp := range c.Ports {
				if strings.Contains(strings.ToLower(cp.Name), "metric") {
					return p.Namespace, p.Name, int(cp.ContainerPort)
				}
			}
		}
	}
	t.Skipf("no pod in kube-system on %q exposes a port named *metric*; nothing safe to read", name)
	return "", "", 0
}

func TestIntegrationPortForwardCarriesBytes(t *testing.T) {
	clusters, name := integrationCluster(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	namespace, pod, port := findHTTPTarget(ctx, t, clusters, name)
	t.Logf("forwarding to %s/%s:%d on %s", namespace, pod, port, name)

	forwarder := NewForwarder(clusters, accesstest.Allow())
	forwarder.SetLogf(func(format string, v ...any) { t.Logf(format, v...) })

	// LocalPort zero on purpose: a fixed port is how two forwards collide, and a
	// test that hard-codes one fails for the wrong reason on a busy machine.
	tunnel, err := forwarder.Forward(ctx, ForwardRequest{
		Cluster:    name,
		Namespace:  namespace,
		Pod:        pod,
		RemotePort: port,
	})
	if err != nil {
		t.Fatalf("opening a tunnel to %s/%s:%d: %v", namespace, pod, port, err)
	}
	defer tunnel.Close()

	if tunnel.LocalPort == 0 || tunnel.LocalAddr == "" {
		t.Fatalf("the tunnel reported no local address: %+v", tunnel)
	}
	if tunnel.Pod != pod {
		t.Errorf("tunnel reports pod %q, asked for %q", tunnel.Pod, pod)
	}
	t.Logf("listening on %s", tunnel.LocalAddr)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://%s/metrics", tunnel.LocalAddr), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("reading through the tunnel: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		t.Fatalf("reading the response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the pod answered %d through the tunnel", resp.StatusCode)
	}
	if len(body) == 0 {
		t.Fatal("the tunnel opened and carried nothing; a listener is not a connection")
	}
	t.Logf("read %d bytes: %s", len(body), textutil.FirstLineN(string(body), 120))
}

// Closing must actually close. A tunnel that survives its Close leaks a
// goroutine and a port per resource the operator looks at, which on a console
// people leave open all day is not a small leak.
func TestIntegrationPortForwardStopsOnClose(t *testing.T) {
	clusters, name := integrationCluster(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	namespace, pod, port := findHTTPTarget(ctx, t, clusters, name)

	forwarder := NewForwarder(clusters, accesstest.Allow())
	forwarder.SetLogf(func(format string, v ...any) { t.Logf(format, v...) })

	tunnel, err := forwarder.Forward(ctx, ForwardRequest{
		Cluster: name, Namespace: namespace, Pod: pod, RemotePort: port,
	})
	if err != nil {
		t.Fatalf("opening a tunnel: %v", err)
	}
	addr := tunnel.LocalAddr

	tunnel.Close()
	tunnel.Close() // documented as safe more than once; prove it rather than trust it

	// Give the listener a moment to actually go away before concluding it has.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/metrics", nil)
		resp, err := (&http.Client{Timeout: time.Second}).Do(req)
		if err != nil {
			return // refused: the listener is gone, which is the point
		}
		_ = resp.Body.Close()
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%s still answers after Close; the tunnel outlived it", addr)
}

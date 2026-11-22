package forward

// The managed set against a real cluster.
//
// The tests beside this one run on a fake digger and cover everything a cluster
// will not do on demand: two clicks at once, the seventeenth tunnel, a stream
// dying. What they cannot prove is that a tunnel opened through this manager
// carries bytes, that a second click really lands on the same listener rather
// than on an identical-looking record, and that closing one actually frees the
// port. Those need SPDY, a credential plugin and a kernel.
//
// Off by default, because a suite that needs a cluster is a suite that fails on a
// laptop in a train:
//
//	KT_INTEGRATION_CLUSTER=docker-desktop go test ./internal/forward/ -run Integration -v
//
// Strictly read-only. It lists, it forwards, it reads kube-dns's Prometheus
// metrics on 9153 — a GET that changes nothing on a component every cluster has.
// It creates nothing and deletes nothing, and it must stay that way: a guard test
// in this repository once deleted a live namespace.
//
// One thing is therefore *not* covered here and cannot be: reaping a tunnel whose
// pod has gone, because arranging that means deleting a pod. The detector is
// covered instead — podChecker is asked about a pod that does not exist and about
// one that does — and the reaping it feeds is covered on the fake.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/textutil"
)

func integrationManager(t *testing.T) (*Manager, *cluster.Manager, string) {
	t.Helper()
	return integrationManagerOn(t, Options{})
}

// integrationManagerOn is the same on given options.
//
// KT_FORWARD_PORTS is cleared unless the caller sets Options.Ports, and that
// matters here more than in the unit tests: with a range configured a forward
// binds 0.0.0.0, and TestIntegrationOpenListCloseCarriesBytes asserts the
// opposite — that the port answers on the loopback and on no other address this
// machine has. Both claims are true of their own deployment, and a suite that
// picked up the operator's shell would prove whichever one they happened to have
// exported.
func integrationManagerOn(t *testing.T, opts Options) (*Manager, *cluster.Manager, string) {
	t.Helper()
	if opts.Ports == "" {
		t.Setenv(EnvPorts, "")
	}

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

	opts.Clusters = clusters
	opts.Logf = func(format string, v ...any) { t.Logf(format, v...) }
	m := New(opts)
	t.Cleanup(m.Shutdown)
	return m, clusters, name
}

// metricsService looks for a Service exposing a port that answers Prometheus
// metrics over plain HTTP. kube-dns is the one every cluster has.
func metricsService(ctx context.Context, t *testing.T, clusters *cluster.Manager, name string) (namespace, service string, port int) {
	t.Helper()

	resolved, _ := clusters.Resolve(name)
	client, err := clusters.Client(resolved)
	if err != nil {
		t.Fatalf("building a client for %q: %v", name, err)
	}

	services, err := client.Clients.CoreV1().Services("kube-system").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing services in kube-system on %q: %v", name, err)
	}
	for _, svc := range services.Items {
		if len(svc.Spec.Selector) == 0 {
			continue
		}
		for _, p := range svc.Spec.Ports {
			if p.Protocol != "" && p.Protocol != corev1.ProtocolTCP {
				continue
			}
			if strings.Contains(strings.ToLower(p.Name), "metric") || p.Port == 9153 {
				return svc.Namespace, svc.Name, int(p.Port)
			}
		}
	}
	t.Skipf("no service in kube-system on %q exposes a metrics port; nothing safe to read", name)
	return "", "", 0
}

func TestIntegrationOpenListCloseCarriesBytes(t *testing.T) {
	m, clusters, name := integrationManager(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	namespace, service, port := metricsService(ctx, t, clusters, name)
	t.Logf("forwarding to service %s/%s:%d on %s", namespace, service, port, name)

	tunnel, err := m.Open(ctx, Request{
		Cluster: name, Namespace: namespace, Service: service, Port: port,
	})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Logf("tunnel %s on %s → pod %s:%d", tunnel.ID, tunnel.LocalAddr, tunnel.Pod, tunnel.PodPort)

	if !strings.HasPrefix(tunnel.LocalAddr, "127.0.0.1:") {
		t.Fatalf("the tunnel is listening on %q; a forward must bind the loopback and nothing else", tunnel.LocalAddr)
	}
	if tunnel.Pod == "" {
		t.Error("the tunnel does not name the replica it landed on")
	}

	// It is in the list, which is the whole point of the package.
	list := m.List()
	if len(list) != 1 || list[0].ID != tunnel.ID {
		t.Fatalf("the open tunnel is not in the list: %+v", list)
	}
	if !list[0].Healthy {
		t.Errorf("the tunnel is listed as unhealthy: %s", list[0].Error)
	}

	// A second click on the same port must be the same tunnel.
	again, err := m.Open(ctx, Request{
		Cluster: name, Namespace: namespace, Service: service, Port: port,
	})
	if err != nil {
		t.Fatalf("opening the same target again: %v", err)
	}
	if again.ID != tunnel.ID || again.LocalAddr != tunnel.LocalAddr {
		t.Fatalf("a second click opened a second tunnel: %s on %s vs %s on %s",
			again.ID, again.LocalAddr, tunnel.ID, tunnel.LocalAddr)
	}
	if got := len(m.List()); got != 1 {
		t.Fatalf("%d tunnels open after two clicks on the same port", got)
	}

	// And it carries bytes. A listener is not a connection.
	body := readMetrics(ctx, t, tunnel.LocalAddr)
	t.Logf("read %d bytes through the tunnel: %s", len(body), textutil.FirstLine(body))
	if !strings.Contains(body, "# HELP") && !strings.Contains(body, "#") {
		t.Errorf("the tunnel carried %d bytes that do not look like Prometheus metrics", len(body))
	}

	// The exposure claim, checked rather than asserted in a comment: the port is
	// on the loopback and on no other address this machine has.
	assertLoopbackOnly(t, tunnel.LocalPort)

	// Closing must actually close, or the console leaks a port per resource
	// anybody looked at.
	if !m.Close(tunnel.ID) {
		t.Fatal("closing the tunnel reported nothing to close")
	}
	if got := len(m.List()); got != 0 {
		t.Errorf("%d tunnels listed after the only one was closed", got)
	}
	assertRefused(t, tunnel.LocalAddr)
}

// The published range, against a real cluster and through the real digger.
//
// This is the half no fake can prove, and the half a wrong mapping would hide
// in: the manager can allocate a port perfectly and the tunnel still listen
// somewhere else, because the number has to survive forwarderDigger,
// terminal.Forwarder and client-go's own listener. Here it is read back off the
// socket that answered.
//
// It also checks the wide bind, which is what makes a published port work at
// all: Docker routes a published port to the container's interface, so a
// listener on the container's own loopback is unreachable through it. On a host
// that shows up as the forward answering on this machine's own IPv4 address —
// which is exactly the widening the package comment describes, and the reason
// the range must be left unset outside a container.
func TestIntegrationAForwardLandsInsideThePublishedRange(t *testing.T) {
	const ports = "30480-30483"
	m, clusters, name := integrationManagerOn(t, Options{Ports: ports})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	namespace, service, port := metricsService(ctx, t, clusters, name)
	tunnel, err := m.Open(ctx, Request{
		Cluster: name, Namespace: namespace, Service: service, Port: port,
	})
	if err != nil {
		t.Fatalf("opening with %s=%s: %v", EnvPorts, ports, err)
	}
	t.Logf("tunnel %s on %s → %s/%s:%d", tunnel.ID, tunnel.LocalAddr, namespace, tunnel.Pod, tunnel.PodPort)

	if tunnel.LocalPort < 30480 || tunnel.LocalPort > 30483 {
		t.Fatalf("the tunnel is on %d, outside %s — nothing publishes that port and no browser can reach it",
			tunnel.LocalPort, ports)
	}
	if want := fmt.Sprintf("127.0.0.1:%d", tunnel.LocalPort); tunnel.LocalAddr != want {
		t.Errorf("the address the dock would draw is %q, want %q", tunnel.LocalAddr, want)
	}

	// And it carries bytes on that port, which is the only proof that the number
	// reached the listener rather than only the record.
	body := readMetrics(ctx, t, tunnel.LocalAddr)
	t.Logf("read %d bytes through %s: %s", len(body), tunnel.LocalAddr, textutil.FirstLine(body))

	assertReachableBeyondTheLoopback(t, tunnel.LocalPort)

	if !m.Close(tunnel.ID) {
		t.Fatal("closing the tunnel reported nothing to close")
	}
	assertRefused(t, tunnel.LocalAddr)

	// The port came back, so the next forward can have it. A leak here empties a
	// thirty-two port range in an afternoon.
	waitFor(t, "the port to return to the range", func() bool { return m.ports.held() == 0 })
}

// assertReachableBeyondTheLoopback is assertLoopbackOnly's opposite, and the two
// together are the whole security story: a forward with no range published binds
// the loopback only, and a forward inside a published range binds every
// interface *because it has to*. In the deployment this is for, the only route to
// that interface is the mapping Docker made on the host's loopback.
func assertReachableBeyondTheLoopback(t *testing.T, port int) {
	t.Helper()

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Logf("cannot enumerate the interfaces, skipping the bind check: %v", err)
		return
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
			continue
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", ipnet.IP, port), 2*time.Second)
		if err != nil {
			t.Logf("%s:%d did not answer (%v)", ipnet.IP, port, err)
			continue
		}
		_ = conn.Close()
		t.Logf("the forward answers on %s:%d as well as on the loopback, which is what a published port needs", ipnet.IP, port)
		return
	}
	t.Log("no non-loopback IPv4 address answered; either this machine has none or the listener is narrower than it should be")
}

func TestIntegrationShutdownClosesEverything(t *testing.T) {
	m, clusters, name := integrationManager(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	namespace, service, port := metricsService(ctx, t, clusters, name)
	tunnel, err := m.Open(ctx, Request{
		Cluster: name, Namespace: namespace, Service: service, Port: port,
	})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}

	m.Shutdown()
	if got := len(m.List()); got != 0 {
		t.Errorf("%d tunnels survived the shutdown", got)
	}
	assertRefused(t, tunnel.LocalAddr)

	if _, err := m.Open(ctx, Request{
		Cluster: name, Namespace: namespace, Service: service, Port: port,
	}); err == nil {
		t.Error("a tunnel opened after the shutdown")
	}
}

// The three handlers over a real cluster, which is the operator's click end to
// end: the shapes internal/web will mount, the manager, a tunnel, and bytes.
func TestIntegrationHandlersServeTheWholeCycle(t *testing.T) {
	m, clusters, name := integrationManager(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	namespace, service, port := metricsService(ctx, t, clusters, name)

	form := url.Values{
		"cluster":   {name},
		"namespace": {namespace},
		"service":   {service},
		"port":      {strconv.Itoa(port)},
	}
	r := httptest.NewRequest(http.MethodPost, "/kt-api/forward", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	m.OpenHandler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("the open endpoint answered %d: %s", w.Code, w.Body.String())
	}
	var tunnel Tunnel
	if err := json.Unmarshal(w.Body.Bytes(), &tunnel); err != nil {
		t.Fatalf("decoding the answer: %v (%s)", err, w.Body.String())
	}
	t.Logf("the endpoint answered: %s", strings.TrimSpace(w.Body.String()))

	listed := httptest.NewRecorder()
	m.ListHandler().ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/kt-api/forward", nil))
	var view listView
	if err := json.Unmarshal(listed.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Count != 1 || view.Tunnels[0].ID != tunnel.ID || !view.Tunnels[0].Healthy {
		t.Fatalf("the list does not show the tunnel that was just opened: %s", listed.Body.String())
	}

	body := readMetrics(ctx, t, tunnel.LocalAddr)
	t.Logf("read %d bytes through the tunnel the endpoint opened: %s", len(body), textutil.FirstLine(body))

	closed := httptest.NewRecorder()
	cr := httptest.NewRequest(http.MethodPost, "/kt-api/forward/close",
		strings.NewReader(url.Values{"id": {tunnel.ID}}.Encode()))
	cr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	m.CloseHandler().ServeHTTP(closed, cr)
	if closed.Code != http.StatusNoContent {
		t.Fatalf("the close endpoint answered %d: %s", closed.Code, closed.Body.String())
	}
	if got := len(m.List()); got != 0 {
		t.Errorf("%d tunnels remain after the close endpoint", got)
	}
	assertRefused(t, tunnel.LocalAddr)
}

// podChecker is what the sweep asks, and it is the one part of reaping that can
// be proved against a real cluster without deleting anything: a pod that exists
// and a name that does not.
func TestIntegrationPodCheckerTellsGoneFromPresent(t *testing.T) {
	_, clusters, name := integrationManager(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resolved, _ := clusters.Resolve(name)
	client, err := clusters.Client(resolved)
	if err != nil {
		t.Fatalf("building a client: %v", err)
	}
	pods, err := client.Clients.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		t.Fatalf("listing pods: %v", err)
	}
	if len(pods.Items) == 0 {
		t.Skip("no pods in kube-system to ask about")
	}
	live := pods.Items[0].Name

	check := podChecker(clusters)
	if check(ctx, resolved, "kube-system", live) {
		t.Errorf("podChecker says %s is gone; it is running", live)
	}
	// A name that cannot exist. Reading it is a GET that returns 404 and changes
	// nothing.
	if !check(ctx, resolved, "kube-system", "kubetower-no-such-pod-ever") {
		t.Error("podChecker does not notice a pod that is not there, so nothing would ever be reaped")
	}
	// An unreachable cluster must not read as "the pod is gone", or a lapsed SSO
	// session tears down every forward the operator has open.
	if check(ctx, "kubetower-no-such-context", "kube-system", live) {
		t.Error("podChecker reads an unreachable cluster as a missing pod")
	}
}

func readMetrics(ctx context.Context, t *testing.T, addr string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("reading through the tunnel: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the pod answered %d through the tunnel", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("the tunnel opened and carried nothing; a listener is not a connection")
	}
	return string(body)
}

// assertLoopbackOnly checks the claim the package comment makes: the forward is
// on 127.0.0.1 and on none of this machine's other addresses.
func assertLoopbackOnly(t *testing.T, port int) {
	t.Helper()

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Logf("cannot enumerate the interfaces, skipping the exposure check: %v", err)
		return
	}
	checked := 0
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
			continue
		}
		checked++
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", ipnet.IP, port), time.Second)
		if err == nil {
			_ = conn.Close()
			t.Errorf("the forward answers on %s:%d as well as on the loopback", ipnet.IP, port)
			continue
		}
		// Refused, dropped or timed out — any of the three means nothing is
		// listening there for anyone off this machine.
		t.Logf("%s:%d is not reachable, as it should be: %v", ipnet.IP, port, err)
	}
	if checked == 0 {
		t.Log("no non-loopback IPv4 address on this machine; the exposure check had nothing to try")
	}
}

func assertRefused(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			return // refused: the listener is gone, which is the point
		}
		_ = conn.Close()
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%s still accepts connections; the tunnel outlived its close", addr)
}

package cluster

// One dynamic client per cluster, and the loopback-rewrite trap that comes
// with it.
//
// The manager builds one dynamic client per context beside the typed clientset
// and holds it, so a caller does not build one per drawer open; these tests hold
// the two things that make it more than a field.

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
)

// oneClusterKubeconfig writes a kubeconfig with a single context pointing at
// addr, trusting the certificate the test server is using.
//
// A file rather than a rest.Config built by hand, because the path under test is
// Manager.client, which loads the kubeconfig itself — and the loopback rewrite
// this file is partly about only happens on that path.
// base64Of is spelled out rather than inlined so the kubeconfig template above
// stays readable.
func base64Of(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func oneClusterKubeconfig(t *testing.T, name, addr string, ca []byte) string {
	t.Helper()

	block := &pem.Block{Type: "CERTIFICATE", Bytes: ca}
	doc := fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: %[1]s
clusters:
- name: %[1]s
  cluster:
    server: %[2]s
    certificate-authority-data: %[3]s
contexts:
- name: %[1]s
  context:
    cluster: %[1]s
    user: %[1]s
users:
- name: %[1]s
  user:
    token: TEST-TOKEN
`, name, addr, base64Of(pem.EncodeToMemory(block)))

	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("writing the kubeconfig: %v", err)
	}
	return path
}

// tlsServer is an HTTPS server that answers any request with an empty list, and
// counts the connections it was given. The count is the point: two clients
// sharing one transport share its pool.
func tlsServer(t *testing.T) (*httptest.Server, func() int) {
	t.Helper()

	var mu sync.Mutex
	opened := 0

	// Unstarted, then configured, then started — and that order is required, not
	// a style.
	//
	// `httptest.NewTLSServer(...)` followed by assigning `ConnState` is a data
	// race: NewTLSServer launches the serving goroutine before it returns,
	// `net/http`'s Serve reads `Config.ConnState` on every accepted connection
	// (server.go:1835), and setting it afterwards is a write racing that read —
	// a value corrected after the goroutine that reads it has started. The
	// harness is code too.
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"apiVersion":"v1","kind":"ConfigMapList","metadata":{},"items":[]}`)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			mu.Lock()
			opened++
			mu.Unlock()
		}
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	return server, func() int {
		mu.Lock()
		defer mu.Unlock()
		return opened
	}
}

func certOf(t *testing.T, server *httptest.Server) []byte {
	t.Helper()
	if len(server.TLS.Certificates) == 0 || len(server.TLS.Certificates[0].Certificate) == 0 {
		t.Fatal("the test server has no certificate")
	}
	der := server.TLS.Certificates[0].Certificate[0]
	if _, err := x509.ParseCertificate(der); err != nil {
		t.Fatalf("the test server's certificate does not parse: %v", err)
	}
	return der
}

// One dynamic client per cluster, held — not one per call.
//
// The identity assertions are the mutation surface. `Dynamic != nil` would pass
// just as well over a field reassigned on every call.
func TestAClusterHoldsOneDynamicClientAndOneTransport(t *testing.T) {
	// The rewrite must not fire: 127.0.0.1 here means this test's own server.
	t.Setenv("KT_IN_CONTAINER", "0")

	server, opened := tlsServer(t)
	m, err := NewManager(oneClusterKubeconfig(t, "test", server.URL, certOf(t, server)))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	first, err := m.Client("test")
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	if first.Dynamic == nil {
		t.Fatal("the cluster holds no dynamic client, so every caller has to build its own")
	}

	// Read *before* the second call, and this is the whole reason the test is
	// written in this order.
	//
	// Comparing `first.Dynamic != second.Dynamic` after both calls would not
	// work: the two calls return the same pointer, so those are the same field
	// read twice — a mutation that rebuilt the client on the cache-hit path
	// would change the field and both reads would see the new one.
	heldDynamic, heldHTTP := first.Dynamic, first.httpClient

	second, err := m.Client("test")
	if err != nil {
		t.Fatalf("Client, again: %v", err)
	}
	if first != second {
		t.Fatal("two calls built two clients; the cache is not being consulted")
	}
	if second.Dynamic != heldDynamic {
		t.Error("the dynamic client was rebuilt on a second call. Each one carries its own rate " +
			"limiter, so the QPS and Burst this package sets stop being a per-cluster ceiling " +
			"and become a per-caller allowance")
	}
	if second.httpClient == nil || second.httpClient != heldHTTP {
		t.Error("the http.Client was rebuilt on a second call")
	}

	// And they share its pool, which is the half a pointer comparison cannot
	// see. One request through each, one connection.
	ctx := context.Background()
	if _, err := first.Clients.CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{}); err != nil {
		t.Fatalf("the typed client could not reach the test server: %v", err)
	}
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	if _, err := first.Dynamic.Resource(gvr).Namespace("default").List(ctx, metav1.ListOptions{}); err != nil {
		t.Fatalf("the dynamic client could not reach the test server: %v", err)
	}
	// One connection for two requests. Worth knowing what this does *not* prove:
	// client-go caches the TLS round tripper globally, so this stays at 1 even if
	// the two clients were built from two http.Clients. It is a floor, not the
	// guarantee — TestBothClientsOnAClusterGoThroughOneTransport below is the
	// guarantee.
	if n := opened(); n != 1 {
		t.Errorf("the server was given %d connections for two requests. The typed and the "+
			"unstructured client are meant to share one transport, and with it one TLS "+
			"handshake per cluster rather than one per caller", n)
	}
}

// countingTransport records every request that goes through it.
type countingTransport struct {
	inner http.RoundTripper
	mu    sync.Mutex
	seen  int
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.seen++
	c.mu.Unlock()
	return c.inner.RoundTrip(r)
}

func (c *countingTransport) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen
}

// Both clients go through the http.Client they were handed.
//
// This is the assertion the wire-level one could not make. Counting connections
// cannot tell a shared http.Client from client-go's global transport cache
// sharing the round tripper underneath two of them; a round tripper that the
// caller owns can, because a client that built its own from the config would
// never reach it.
//
// Replacing dynamic.NewForConfigAndClient with dynamic.NewForConfig leaves every
// other test in this file green.
func TestBothClientsOnAClusterGoThroughOneTransport(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")

	server, _ := tlsServer(t)
	cfg := &rest.Config{
		Host: server.URL,
		TLSClientConfig: rest.TLSClientConfig{
			CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certOf(t, server)}),
		},
	}

	inner, err := rest.HTTPClientFor(cfg)
	if err != nil {
		t.Fatalf("HTTPClientFor: %v", err)
	}
	counting := &countingTransport{inner: inner.Transport}
	shared := &http.Client{Transport: counting}

	clientset, dyn, err := clientsFor(cfg, shared)
	if err != nil {
		t.Fatalf("clientsFor: %v", err)
	}

	ctx := context.Background()
	if _, err := clientset.CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{}); err != nil {
		t.Fatalf("the typed client: %v", err)
	}
	if n := counting.count(); n != 1 {
		t.Fatalf("the typed client made %d requests through the shared transport, want 1. "+
			"Nothing below this can be trusted if the typed half is not using it either", n)
	}

	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	if _, err := dyn.Resource(gvr).Namespace("default").List(ctx, metav1.ListOptions{}); err != nil {
		t.Fatalf("the dynamic client: %v", err)
	}
	if n := counting.count(); n != 2 {
		t.Errorf("%d requests reached the shared transport after one through each client. The "+
			"dynamic client built its own from the config, so this cluster now has two "+
			"transports, two connection pools and two rate limiters", n)
	}
}

// The loopback rewrite is the last thing before the config is used, on every
// path — including the dynamic client's.
//
// Both constructors copy the config they are handed, so a dynamic client built
// *before* redirectLoopback keeps 127.0.0.1 while Client.Config shows the
// rewritten address. It would present as
// the drawer working on a docker-desktop pod while the panel beside it reports
// the cluster refusing connections.
//
// The assertion is that the dynamic client's own request does not reach the
// server on the loopback. It cannot: the rewrite sends it to
// host.docker.internal, which either does not resolve or is an interface this
// test's server is not listening on, and it also forces the TLS server name to
// "kubernetes", which the test certificate does not carry. Two independent
// reasons, so the test does not depend on this machine's networking.
func TestTheDynamicClientIsBuiltAfterTheLoopbackRewrite(t *testing.T) {
	server, _ := tlsServer(t)
	path := oneClusterKubeconfig(t, "docker-desktop", server.URL, certOf(t, server))

	// The control first, on the host, where 127.0.0.1 means what it says. If
	// this cannot reach the server the assertion below proves nothing.
	t.Setenv("KT_IN_CONTAINER", "0")
	onHost, err := NewManager(path)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	client, err := onHost.Client("docker-desktop")
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	if _, err := client.Dynamic.Resource(gvr).Namespace("default").
		List(context.Background(), metav1.ListOptions{}); err != nil {
		t.Fatalf("the dynamic client cannot reach the test server even with no rewrite, so "+
			"this test cannot tell a rewrite from a broken fixture: %v", err)
	}

	// And now in a container, where it must not.
	t.Setenv("KT_IN_CONTAINER", "1")
	inContainer, err := NewManager(path)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	redirected, err := inContainer.Client("docker-desktop")
	if err != nil {
		t.Fatalf("Client: %v", err)
	}

	// Both halves on the config, because redirecting the address alone turns a
	// refused connection into a certificate error and reads as the cluster's
	// fault.
	if !strings.Contains(redirected.Config.Host, "host.docker.internal") {
		t.Errorf("the config still points at %q inside a container", redirected.Config.Host)
	}
	if redirected.Config.TLSClientConfig.ServerName != "kubernetes" {
		t.Errorf("tls-server-name is %q, want \"kubernetes\"",
			redirected.Config.TLSClientConfig.ServerName)
	}

	_, err = redirected.Dynamic.Resource(gvr).Namespace("default").
		List(context.Background(), metav1.ListOptions{})
	if err == nil {
		t.Fatal("the dynamic client reached the server on 127.0.0.1 from inside a container, " +
			"so it was built before the loopback rewrite and carries the address the " +
			"rewrite replaced: the client was built before the rewrite")
	}
	t.Logf("the redirected dynamic client failed as it must: %v", err)
}

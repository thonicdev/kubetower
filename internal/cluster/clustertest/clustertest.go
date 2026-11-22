// Package clustertest stands up one cluster for a test: an HTTPS API server on
// the loopback and a kubeconfig that names it, so the code under test reaches it
// through cluster.Manager as it reaches a real one: the bounded dial, the rate
// limiter, the held clients and all. **Tests only.**
//
// The server counts the connections it accepts, because a client built from
// Client.Config instead of the held one pays a new TLS connection per call, a
// cost only the server's side of the wire shows.
package clustertest

import (
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
)

// Cluster is one test API server and the kubeconfig that points at it.
type Cluster struct {
	// Kubeconfig is the path to a kubeconfig with one context, named as asked,
	// whose server is this one and whose CA is this server's certificate.
	Kubeconfig string
	// Server is the test server itself, closed when the test ends.
	Server *httptest.Server

	mu     sync.Mutex
	opened int
}

// Opened is the number of connections the server has accepted so far. Every
// one is a TCP connect and a TLS handshake that a client paid for.
func (c *Cluster) Opened() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opened
}

// Start serves a cluster whose context is called name.
//
// Every GET answers with a ConfigMap, or an empty ConfigMapList when the path
// ends in the collection. Deliberately no discovery endpoint: a test that needs
// a RESTMapper supplies one, so the connections counted are those of the code
// under test's reads.
//
// The caller sets KT_IN_CONTAINER=0 if the machine could be taken for a
// container: 127.0.0.1 here means this server, and the loopback rewrite would
// send the request elsewhere.
func Start(t testing.TB, name string) *Cluster {
	t.Helper()
	return Serve(t, name, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/configmaps") {
			fmt.Fprint(w, `{"apiVersion":"v1","kind":"ConfigMapList","metadata":{},"items":[]}`)
			return
		}
		fmt.Fprintf(w, `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":%q,"namespace":"default","resourceVersion":"1"}}`,
			lastSegment(r.URL.Path))
	}))
}

// Serve is Start with the answers the test writes: a cluster whose API server
// is handler, behind the same manager, kubeconfig and certificate.
func Serve(t testing.TB, name string, handler http.Handler) *Cluster {
	t.Helper()

	c := &Cluster{}

	// Set ConnState before starting: the server reads it on every accepted
	// connection, so setting it after StartTLS is a data race.
	server := httptest.NewUnstartedServer(handler)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			c.mu.Lock()
			c.opened++
			c.mu.Unlock()
		}
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	c.Server = server

	if len(server.TLS.Certificates) == 0 || len(server.TLS.Certificates[0].Certificate) == 0 {
		t.Fatal("the test server has no certificate")
	}
	der := server.TLS.Certificates[0].Certificate[0]
	if _, err := x509.ParseCertificate(der); err != nil {
		t.Fatalf("the test server's certificate does not parse: %v", err)
	}
	ca := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))

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
`, name, server.URL, ca)

	c.Kubeconfig = filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(c.Kubeconfig, []byte(doc), 0o600); err != nil {
		t.Fatalf("writing the kubeconfig: %v", err)
	}
	return c
}

func lastSegment(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// TestATunnelReachesTheClusterOnlyFromInsideItsReview drives client-go's own
// port-forward dialers - the WebSocket tunnel and the SPDY fallback - through a
// cluster manager's real config, which carries the access guard.
//
// **Why it exists**: neither dialer hands a context to the request it builds,
// so the stamp Gate.Perform puts on the closure's context never reaches the
// guard unless newForwardDialer carries it (access.Carry). Without that every
// tunnel the review allowed would be refused on its way out - and no other
// test here noticed, because nothing else dials. Removing the Carry line from
// newForwardDialer fails the second half of this test.
//
// The server answers every upgrade with a 400: what is asserted is whether the
// request reached it, not whether a tunnel opened.
func TestATunnelReachesTheClusterOnlyFromInsideItsReview(t *testing.T) {
	client, reached := refusingCluster(t)
	target := client.Clients.CoreV1().RESTClient().Post().
		Resource("pods").Namespace("shop").Name("api-1").SubResource("portforward").URL()
	const path = "/api/v1/namespaces/shop/pods/api-1/portforward"

	// Outside any review: refused before a byte is sent.
	dialer, err := newForwardDialer(context.Background(), client, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := dialer.Dial(portforward.PortForwardProtocolV1Name); err == nil ||
		!strings.Contains(err.Error(), "no access review was made") {
		t.Errorf("a tunnel dug outside Perform answered %v, want the guard's refusal", err)
	}
	if got := reached(); len(got) != 0 {
		t.Fatalf("a tunnel nobody reviewed reached the cluster: %v", got)
	}

	// Inside Perform, reviewed as what it is: the dialers' requests carry the
	// stamp and reach the cluster - which then refuses the upgrade, as this one
	// refuses everything.
	review := access.Attributes{Verb: "create", Version: "v1", Resource: "pods", Subresource: "portforward",
		Namespace: "shop", Name: "api-1"}
	_, err = accesstest.Allow().Perform(context.Background(), "tunnel", review, func(ctx context.Context) error {
		dialer, err := newForwardDialer(ctx, client, target)
		if err != nil {
			return err
		}
		_, _, err = dialer.Dial(portforward.PortForwardProtocolV1Name)
		return err
	})
	if errors.Is(err, access.ErrNotReviewed) || (err != nil && strings.Contains(err.Error(), "no access review was made")) {
		t.Fatalf("a tunnel dug inside its own review was refused by the guard: %v", err)
	}
	bothDialers(t, "a tunnel", reached(), path)
}

// bothDialers asserts what reached the cluster from inside the review: the
// WebSocket upgrade (a GET), stamped, and then the SPDY fallback (a POST) to the
// same path, stamped too - the access guard on this client's config refuses a
// request without the stamp, so a POST that arrives carried it - and nothing
// else.
//
// **The fallback is required.** client-go v0.37 reports a refused upgrade as
// k8s.io/streaming's UpgradeFailureError, and apimachinery has a different
// type of the same name; upgradeRefused asks both. The exec's SPDY POST carries the stamp
// because client-go builds it with the context Perform handed in, and the
// tunnel's because access.Carry puts it on.
func bothDialers(t *testing.T, what string, got []string, path string) {
	t.Helper()
	websocket, spdy := http.MethodGet+" "+path, http.MethodPost+" "+path
	if len(got) == 0 || got[0] != websocket {
		t.Fatalf("%s dug inside its own review: the cluster received %q, want the WebSocket upgrade %q first",
			what, got, websocket)
	}
	if len(got) < 2 || got[1] != spdy {
		t.Fatalf("%s: the cluster received %q, want the SPDY fallback %q after the refused upgrade", what, got, spdy)
	}
	for _, request := range got[2:] {
		if request != spdy {
			t.Errorf("%s: the cluster also received %q, want only the SPDY fallback %q", what, request, spdy)
		}
	}
}

// refusingCluster is a cluster manager's real client - access guard and all -
// for a server that records every request and refuses every upgrade with a
// 400, and the record.
func refusingCluster(t *testing.T) (*cluster.Client, func() []string) {
	t.Helper()
	// The loopback rewrite must not move this address; see execguard_test.go.
	t.Setenv("KT_IN_CONTAINER", "0")

	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		http.Error(w, "no upgrade here", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	reached := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}

	kubeconfig := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(kubeconfig, []byte(fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: tunnel
  cluster: {server: %q}
users:
- name: tunnel
  user: {token: not-a-real-token}
contexts:
- name: tunnel
  context: {cluster: tunnel, user: tunnel}
current-context: tunnel
`, srv.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := managerFor(t, kubeconfig).Client("tunnel")
	if err != nil {
		t.Fatal(err)
	}
	return client, reached
}

// TestAShellReachesTheClusterOnlyFromInsideItsReview is the same for exec, and
// pins the half that is client-go's: both executors (the WebSocket one and the
// SPDY fallback, newExecutor) build their request on the context
// StreamWithContext is handed, so the stamp travels without a Carry. The other
// half - that runExec hands streamExec the context Perform gave its closure -
// is one line in exec.go; an executor that stopped
// passing its context on would fail the second half here.
func TestAShellReachesTheClusterOnlyFromInsideItsReview(t *testing.T) {
	client, reached := refusingCluster(t)
	target := client.Clients.CoreV1().RESTClient().Post().
		Resource("pods").Namespace("shop").Name("api-1").SubResource("exec").URL()
	const path = "/api/v1/namespaces/shop/pods/api-1/exec"
	stream := func(ctx context.Context) error {
		executor, err := newExecutor(cluster.StreamingConfig(client.Config), target)
		if err != nil {
			return err
		}
		return executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: strings.NewReader(""), Stdout: io.Discard, Tty: true})
	}

	if err := stream(context.Background()); err == nil || !strings.Contains(err.Error(), "no access review was made") {
		t.Errorf("a shell opened outside Perform answered %v, want the guard's refusal", err)
	}
	if got := reached(); len(got) != 0 {
		t.Fatalf("a shell nobody reviewed reached the cluster: %v", got)
	}

	review := access.Attributes{Verb: "create", Version: "v1", Resource: "pods", Subresource: "exec",
		Namespace: "shop", Name: "api-1"}
	_, err := accesstest.Allow().Perform(context.Background(), "tunnel", review, stream)
	if errors.Is(err, access.ErrNotReviewed) || (err != nil && strings.Contains(err.Error(), "no access review was made")) {
		t.Fatalf("a shell opened inside its own review was refused by the guard: %v", err)
	}
	bothDialers(t, "a shell", reached(), path)
}

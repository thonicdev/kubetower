package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// The rail is the one piece of chrome on every page, and the only one with
// buttons that change something. These tests call the handlers rather than the
// functions under them, because what broke twice in v1's rail was never the
// arrangement — it was the URL between the button and the arrangement.

const arn = "arn:aws:eks:eu-central-1:123456789012:cluster/example-prod-eks-1"

// railServer is a UI over a kubeconfig with four contexts, one of them an EKS
// ARN — colons, slashes and all — because that is the name that has to survive
// a round trip through a query string.
func railServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	server, mux, _ := railServerOn(t)
	return server, mux
}

// railServerOn is the same, and says where the kubeconfig is. Hiding a cluster
// is the one thing the rail does that could conceivably be implemented by
// editing that file, so at least one test has to be able to look at it.
func railServerOn(t *testing.T) (*Server, http.Handler, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: Config
current-context: mi-k3s
clusters:
- name: unreachable
  cluster:
    server: https://127.0.0.1:59999
contexts:
- name: mi-k3s
  context: {cluster: unreachable, user: none}
- name: docker-desktop
  context: {cluster: unreachable, user: none}
- name: SHOP-EKS-1
  context: {cluster: unreachable, user: none}
- name: `+arn+`
  context: {cluster: unreachable, user: none}
users:
- name: none
  user: {token: none}
`), 0o600); err != nil {
		t.Fatalf("writing the kubeconfig: %v", err)
	}

	manager, err := cluster.NewManager(path)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	server, err := New(Options{Clusters: manager})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mux := http.NewServeMux()
	server.Mount(mux)
	return server, mux, path
}

// drawsTile says whether this HTML has a rail tile for a context.
//
// By its link and not by its name. The label on a tile is middle-truncated
// (STAGING-E…S-1_OWNER), so the text is no use; and the whole name appears in
// several other places on the page — the breadcrumb, the hidden list, the query
// string of every button — so a bare search for it says almost nothing. A tile
// is the only thing that links to the cluster's front page.
func drawsTile(body, context string) bool {
	return strings.Contains(body, `href="`+clusterPath(context)+`"`)
}

func call(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

// The default arrangement, drawn: every context on hotbar 1, in kubeconfig
// order, with a switcher over it. A context on no hotbar is a context that
// cannot be reached from the rail at all.

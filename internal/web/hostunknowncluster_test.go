package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/extras/argocd"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/metrics"
	"github.com/thonicdev/kubetower/internal/terminal"
)

// unreachableHost is a console whose kubeconfig holds one context, "down",
// pointing at an API server that has already shut: known, and unreachable.
func unreachableHost(t *testing.T) extension.Host {
	t.Helper()
	return unreachableServer(t).hostFor("unknowncheck")
}

// unreachableServer is the console behind unreachableHost, for a test that
// changes what the build carries before it asks.
func unreachableServer(t *testing.T) *Server {
	t.Helper()
	gone := httptest.NewServer(http.NotFoundHandler())
	address := gone.URL
	gone.Close()

	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: Config
current-context: down
clusters:
- name: down
  cluster: {server: `+address+`}
contexts:
- name: down
  context: {cluster: down, user: none}
users:
- name: none
  user: {token: none}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := cluster.NewManager(path)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	// The real forwarder, as internal/launch wires it, so Forward is answered
	// by the code an extension reaches. Its gate allows everything, so "down"
	// is answered by the closed address rather than by a refusal.
	tunnels := metrics.Tunnels(terminal.NewForwarder(manager, accesstest.Allow()))
	s, err := New(Options{Clusters: manager, Tunnels: tunnels})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// hostCalls is every Host method that takes a cluster name, each called as an
// extension would call it: those that go through host.client, and Forward,
// which hands the name to the forwarder instead.
func hostCalls(h extension.Host, r *http.Request) map[string]func(name string) error {
	return map[string]func(string) error{
		"Client": func(name string) error {
			_, err := h.Client(r, name)
			return err
		},
		"Definitions": func(name string) error {
			_, err := h.Definitions(r, name)
			return err
		},
		"List": func(name string) error {
			_, err := h.List(r, name, extension.ListQuery{GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}})
			return err
		},
		"Reveal": func(name string) error {
			_, err := h.Reveal(r, name, "shop", "db", "password")
			return err
		},
		"Usage": func(name string) error {
			_, err := h.Usage(r, name, []string{"node-1"})
			return err
		},
		"Forward": func(name string) error {
			tunnel, err := h.Forward(r.Context(), name, "shop", "db", 5432)
			if err == nil {
				tunnel.Stop()
			}
			return err
		},
	}
}

// An extra handed a cluster name the core's gate let
// through unresolved must be able to tell "there is no such cluster" from "the
// cluster could not be read" - 404 and 502 - without matching an error string
// from another module.
func TestEveryHostMethodWrapsErrUnknownClusterForANameNotInTheKubeconfig(t *testing.T) {
	h := unreachableHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, "/x/nope/argocd/", nil).WithContext(ctx)

	for method, call := range hostCalls(h, r) {
		t.Run(method+"/unknown", func(t *testing.T) {
			err := call("nope")
			if !errors.Is(err, extension.ErrUnknownCluster) {
				t.Fatalf("%s(%q) = %v; errors.Is(err, extension.ErrUnknownCluster) is false", method, "nope", err)
			}
			if !strings.Contains(err.Error(), `"nope"`) {
				t.Errorf("%s's error %q does not name the cluster it was asked for", method, err)
			}
		})
		// The other half, and the one that makes the sentinel worth having: a
		// cluster that is in the kubeconfig and does not answer is a failed
		// read, never "no such cluster". Client and Definitions do not dial, so
		// they succeed here; List, Usage and Forward dial and fail, and must
		// not claim the cluster is unknown.
		t.Run(method+"/known-unreachable", func(t *testing.T) {
			if err := call("down"); errors.Is(err, extension.ErrUnknownCluster) {
				t.Fatalf("%s(%q) = %v; a known, unreachable cluster answered as unknown", method, "down", err)
			}
		})
	}
}

// The sentence an extra answers with is the console's, so it carries the name
// and the two causes rather than two words.
func TestUnknownClusterTextNamesTheClusterAndWhyItMightBeGone(t *testing.T) {
	got := extension.UnknownClusterText("nope")
	for _, want := range []string{`"nope"`, "kubeconfig", "removed or renamed"} {
		if !strings.Contains(got, want) {
			t.Errorf("UnknownClusterText(%q) = %q; missing %q", "nope", got, want)
		}
	}
}

// A build without a custom-resource catalog still tells an unknown cluster
// apart from a failed read: Definitions resolves the name before it looks for
// the catalog, so the Delivery Center answers 404 cluster_unknown and not 502.
// A known cluster in that build is the catalog's failure, never
// "no such cluster".
func TestDefinitionsWithNoCatalogueAnswersAnUnknownClusterAs404(t *testing.T) {
	s := unreachableServer(t)
	s.custom = nil
	h := s.hostFor(argocd.Key)
	r := httptest.NewRequest(http.MethodGet, "/x/nope/argocd/", nil)

	if _, err := h.Definitions(r, "nope"); !errors.Is(err, extension.ErrUnknownCluster) {
		t.Fatalf("Definitions(%q) with no catalog = %v; want extension.ErrUnknownCluster", "nope", err)
	}
	if _, err := h.Definitions(r, "down"); err == nil || errors.Is(err, extension.ErrUnknownCluster) {
		t.Fatalf("Definitions(%q) with no catalog = %v; want the catalog's failure, not an unknown cluster", "down", err)
	}

	mux := http.NewServeMux()
	for _, route := range argocd.New().Routes(h) {
		mux.Handle(route.Method+" /x/{cluster}/"+argocd.Key+route.Pattern, route.Handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404; body %s", rec.Code, rec.Body)
	}
	var got struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v; body %s", err, rec.Body)
	}
	if got.Code != "cluster_unknown" {
		t.Errorf("code %q, want cluster_unknown", got.Code)
	}
}

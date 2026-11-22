package terminal

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// The read-only guard on /kt-api/exec.
//
// This endpoint opens a root-equivalent prompt inside a running workload. The
// shell terminal two clicks away refuses `kubectl exec` on the same cluster —
// `exec` is in `mutatingVerbs`, so the shim blocks it there — so this must too.
// A guardrail that refuses a gesture through one door and
// permits it through the next is worse than none: it teaches people the marking
// is noise, which is how the markings that matter come to be ignored.
//
// The test needs no cluster, which is the point — `DEFINITION-OF-DONE.md` §3
// requires every guard to have one that runs inside the image build, because the
// alternative is testing a destructive path against something real. A guard
// tested by trying it on a live cluster risks deleting a namespace.

// execKubeconfig writes a kubeconfig naming the given contexts, all pointing at
// an address that will never answer. Nothing here reaches a cluster: the guard
// must refuse before any connection is attempted, and if it ever stopped doing
// so this test would hang rather than pass.
func execKubeconfig(t *testing.T, contexts ...string) string {
	t.Helper()

	var b strings.Builder
	b.WriteString("apiVersion: v1\nkind: Config\nclusters:\n")
	for _, name := range contexts {
		fmt.Fprintf(&b, "- name: %s\n  cluster:\n    server: https://127.0.0.1:1\n", name)
	}
	b.WriteString("contexts:\n")
	for _, name := range contexts {
		fmt.Fprintf(&b, "- name: %s\n  context:\n    cluster: %s\n    user: %s\n", name, name, name)
	}
	b.WriteString("users:\n")
	for _, name := range contexts {
		fmt.Fprintf(&b, "- name: %s\n  user:\n    token: not-a-real-token\n", name)
	}
	fmt.Fprintf(&b, "current-context: %s\n", contexts[0])

	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExecIsRefusedOnAReadOnlyClusterAndAllowedElsewhere(t *testing.T) {
	// The loopback rewrite must not fire: these addresses are deliberately dead
	// and must stay dead. See internal/cluster/loopback.go.
	t.Setenv("KT_IN_CONTAINER", "0")

	kubeconfig := execKubeconfig(t, "guarded", "ordinary")

	term, err := New(nil, Options{Kubeconfig: cluster.Kubeconfig{kubeconfig}})
	if err == nil {
		t.Fatal("a terminal was built with no cluster manager")
	}
	_ = term

	clusters := managerFor(t, kubeconfig)
	term, err = New(clusters, Options{
		Kubeconfig: cluster.Kubeconfig{kubeconfig},
		ReadOnly:   func(name string) bool { return name == "guarded" },
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		cluster string
		want    int
		because string
	}{
		{
			name:    "a marked cluster is refused",
			cluster: "guarded",
			want:    http.StatusForbidden,
			because: "exec on a read-only cluster must be refused before anything is opened",
		},
		{
			// Not 200: the address is dead on purpose. What matters is that it
			// got *past* the guard and failed for a different reason — anything
			// but 403 proves the guard let it through.
			name:    "an ordinary cluster is not refused by the guard",
			cluster: "ordinary",
			want:    http.StatusBadRequest,
			because: "a cluster nobody marked must not be refused; this one fails validation instead",
		},
		{
			name:    "a cluster that is not in the kubeconfig is not found",
			cluster: "invented",
			want:    http.StatusNotFound,
			because: "resolving against the kubeconfig is the filter that keeps this endpoint bounded",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			// No namespace or pod: an ordinary cluster therefore stops at name
			// validation, which is exactly how we tell "refused by the guard"
			// from "allowed past it".
			req := httptest.NewRequest(http.MethodGet, "/kt-api/exec?cluster="+c.cluster, nil)

			term.ExecHandler().ServeHTTP(rec, req)

			if rec.Code != c.want {
				t.Errorf("got %d, want %d — %s\n  body: %s",
					rec.Code, c.want, c.because, strings.TrimSpace(rec.Body.String()))
			}
		})
	}
}

// The refusal has to say which cluster and which setting, or the operator has no
// way to know why a button stopped working.
func TestTheRefusalNamesTheClusterAndTheSetting(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")
	kubeconfig := execKubeconfig(t, "prod-eks")

	term, err := New(managerFor(t, kubeconfig), Options{
		Kubeconfig: cluster.Kubeconfig{kubeconfig},
		ReadOnly:   func(string) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	term.ExecHandler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/kt-api/exec?cluster=prod-eks&namespace=default&pod=api", nil))

	body := rec.Body.String()
	if !strings.Contains(body, "prod-eks") {
		t.Errorf("the refusal does not name the cluster: %q", body)
	}
	if !strings.Contains(body, "KT_READONLY_CLUSTERS") {
		t.Errorf("the refusal does not name the setting that caused it: %q", body)
	}
}

// The guard must hold when no ReadOnly callback was supplied at all — New
// substitutes one, and a nil check that got the polarity wrong would refuse
// everything or nothing.
func TestWithNoReadOnlyListNothingIsRefused(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")
	kubeconfig := execKubeconfig(t, "any")

	term, err := New(managerFor(t, kubeconfig), Options{Kubeconfig: cluster.Kubeconfig{kubeconfig}})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	term.ExecHandler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/kt-api/exec?cluster=any", nil))

	if rec.Code == http.StatusForbidden {
		t.Error("exec was refused on a cluster nobody marked read-only")
	}
}

// managerFor builds a cluster manager from a kubeconfig on disk.
func managerFor(t *testing.T, kubeconfig string) *cluster.Manager {
	t.Helper()
	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatalf("reading %s: %v", kubeconfig, err)
	}
	return clusters
}

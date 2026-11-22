package health

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// The classifier against the real kubeconfig.
//
// Off by default — it needs clusters, and several of them are behind an SSO
// session that expires. Run it deliberately:
//
//	KT_INTEGRATION_KUBECONFIG=1 go test ./internal/health/ -run Integration -v
//
// It asserts almost nothing, and that is on purpose: what a given cluster
// answers today depends on a VPN, an SSO session and someone else's IAM policy.
// What it proves is the part unit tests cannot — that real client-go errors fall
// into the states this package defines, rather than all landing in "ko", which
// is exactly what a classifier written against imagined error strings does.
func TestIntegrationClassifiesTheRealKubeconfig(t *testing.T) {
	if os.Getenv("KT_INTEGRATION_KUBECONFIG") == "" {
		t.Skip("set KT_INTEGRATION_KUBECONFIG=1 to probe the real clusters")
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

	h := New(Options{Clusters: clusters, Timeout: 20 * time.Second})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	started := time.Now()
	rows := h.All(ctx, true)
	elapsed := time.Since(started)

	if len(rows) == 0 {
		t.Fatal("no contexts in the kubeconfig")
	}

	t.Logf("%d contexts probed in %s", len(rows), elapsed.Round(time.Millisecond))
	for _, r := range rows {
		t.Logf("  %-8s %-46s %-9s %s", r.State, r.Name, r.Took, r.Detail)
	}
	t.Logf("summary: %v", Summary(rows))

	if s := h.SSO(); s.Found {
		t.Logf("SSO session %q is %s, expires %s", s.Session, s.State, s.ExpiresAt.Format(time.RFC3339))
	} else {
		t.Log("no SSO session in the AWS CLI cache")
	}

	// Concurrency is the whole reason this page is usable: fourteen contexts,
	// several of which wait out a timeout, must cost the slowest one rather than
	// the sum. Probed serially, the page takes half a minute.
	if budget := 20*time.Second + 15*time.Second; elapsed > budget {
		t.Errorf("probing %d contexts took %s, over the %s a concurrent probe should need — "+
			"has this become serial?", len(rows), elapsed.Round(time.Millisecond), budget)
	}
}

// The abandoned page, against the real estate.
//
// The unit test proves the property against an httptest server; this proves it
// against the real estate — fourteen contexts, a mix of local clusters that answer,
// EKS clusters behind an SSO session and one name that does not resolve. A
// canceled round must leave the cache holding what each of those really is,
// and not fourteen rows saying "no answer within 10s".
//
// Read-only: it asks /version and nothing else, like every probe in this
// package.
//
//	KT_INTEGRATION_KUBECONFIG=1 go test ./internal/health/ -run Integration -v
func TestIntegrationAnAbandonedRoundStillTellsTheTruth(t *testing.T) {
	if os.Getenv("KT_INTEGRATION_KUBECONFIG") == "" {
		t.Skip("set KT_INTEGRATION_KUBECONFIG=1 to probe the real clusters")
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

	h := New(Options{Clusters: clusters, Timeout: 20 * time.Second})

	// The browser hanging up mid-round, which is what htmx does the moment the
	// operator clicks a cluster in the rail.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rows := h.All(ctx, true)
	if len(rows) == 0 {
		t.Fatal("an abandoned round returned nothing at all")
	}

	timedOut := 0
	for _, r := range rows {
		t.Logf("  %-8s %-46s %-9s %s", r.State, r.Name, r.Took, r.Detail)
		if r.State == StateTimeout && strings.HasPrefix(r.Detail, "no answer within") {
			timedOut++
		}
	}
	if timedOut == len(rows) {
		t.Fatalf("all %d contexts came back as our own deadline expiring, on a round that was "+
			"canceled before it began — a cancellation is being reported as a cluster failure", len(rows))
	}

	// At least one context on this host answers without an SSO session
	// (docker-desktop, k3s). If none of them did, the cancellation cut the round
	// short despite the detachment.
	answered := Summary(rows)[StateOK]
	t.Logf("summary of the abandoned round: %v", Summary(rows))
	if answered == 0 {
		t.Error("not one context answered a round the caller had already abandoned; " +
			"the probe is still riding the caller's context")
	}
}

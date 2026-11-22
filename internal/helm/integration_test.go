package helm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// What this page says about a real cluster, and it checks the storage format
// rather than assuming it.
//
// Off by default, because it needs one:
//
//	KT_INTEGRATION_CLUSTER=kubernetes-admin@kubernetes go test ./internal/helm/ -run Integration -v
//
// Read-only: two `list` calls on Secrets with a label selector and a field
// selector. There is no mutating path in this package to avoid — but it is
// worth saying, because a Helm page is exactly where a stray write would be
// most damaging.
//
// It asserts *shape* and prints what it found, because a fixture that agrees
// with the reader by construction proves nothing about the field names Helm
// actually writes. The one thing it pins is that an empty answer is not a
// failed one: a cluster whose Secrets were listed must not be reported as
// unreachable, and a cluster with releases must produce a chart version and a
// revision for each of them — the two figures the page exists for, and the two
// that a wrong decode leaves empty while everything else still renders.
func TestIntegrationTheHelmReleasesOfARealClusterAreReadable(t *testing.T) {
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
	resolved, ok := clusters.Resolve(name)
	if !ok {
		t.Fatalf("%q is not in %s", name, kubeconfig)
	}

	reader := NewReader(clusters, accesstest.Allow())
	report := reader.Releases(t.Context(), resolved, nil)

	switch report.State {
	case StateUnreachable, StateUnreadable:
		t.Fatalf("%s: %s", resolved, report.Detail)
	case StateUndecodable:
		t.Fatalf("release Secrets were found and none decoded — this is the failure the "+
			"double base64 causes: %s", report.Detail)
	case StateEmpty:
		t.Logf("%s runs no Helm release: %s", resolved, report.Detail)
		return
	}

	t.Logf("%s: %d releases", resolved, len(report.Releases))
	for _, r := range report.Releases {
		t.Logf("  %-24s %-16s rev %-4d %-10s %s-%s app %s   %s",
			r.Name, r.Namespace, r.Revision, r.Status, r.Chart, r.ChartVersion,
			r.AppVersion, r.Description)
		if r.Chart == "" || r.ChartVersion == "" {
			t.Errorf("%s/%s has no chart version; the release document did not decode into "+
				"chart.metadata", r.Namespace, r.Name)
		}
		if r.Revision <= 0 {
			t.Errorf("%s/%s has revision %d", r.Namespace, r.Name, r.Revision)
		}
		if r.Updated.IsZero() {
			t.Errorf("%s/%s has no last-deployed time", r.Namespace, r.Name)
		}
	}
	if report.Note != "" {
		t.Logf("note: %s", report.Note)
	}

	// And one release's own page, on whichever release the cluster has. The
	// history is the half that reads the superseded revisions, so it exercises a
	// selector the list deliberately does not use.
	first := report.Releases[0]
	history := reader.History(t.Context(), resolved, first.Namespace, first.Name)
	if history.State != StatePresent {
		t.Fatalf("history of %s/%s: %s", first.Namespace, first.Name, history.Detail)
	}
	t.Logf("history of %s/%s: %d revisions", first.Namespace, first.Name, len(history.Revisions))
	for _, rev := range history.Revisions {
		t.Logf("  rev %-4d %-12s %-10s %s   %s", rev.Revision, rev.Status, rev.ChartVersion,
			rev.Updated.Format("2006-01-02 15:04:05"), rev.Description)
	}
	if len(history.Revisions) < 1 {
		t.Errorf("a release with no revisions on its own page")
	}
	if history.Revisions[0].Revision != first.Revision {
		t.Errorf("the list's current revision is %d and the history's newest is %d; the two "+
			"pages disagree about the same release", first.Revision, history.Revisions[0].Revision)
	}
}

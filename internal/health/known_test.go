package health

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// Known answers from what is in hand and contacts nothing.
//
// This is what a navigation to /health reads. Probing every context before
// answering costs 10.00 s against fourteen unreachable ones, and hx-boost shows
// nothing at all while it waits, so the icon reads as dead. See known.go.
func TestKnownAnswersWithoutAskingAnybody(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")

	var asked atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			http.NotFound(w, r)
			return
		}
		asked.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"gitVersion":"v1.31.4"}`)
	}))
	defer server.Close()

	kubeconfig := filepath.Join(t.TempDir(), "config")
	writeKubeconfig(t, kubeconfig, map[string]string{
		"local": server.URL,
		"other": server.URL,
	})
	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}

	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}, TTL: time.Hour})
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	// The round gate closed, so this test measures Known itself rather than the
	// round it asks for. That round is the subject of the second half below.
	h.warmed = now

	// Nothing has been probed. The answer is "nothing, and two contexts are
	// waiting" — never "two contexts, both down": not having asked is not a
	// failed answer.
	snap := h.Known()
	if len(snap.Rows) != 0 {
		t.Errorf("Known invented %d row(s) before anything was probed", len(snap.Rows))
	}
	if snap.Total != 2 || snap.Unprobed != 2 {
		t.Errorf("snapshot = %+v, want 2 contexts, 2 of them unasked", snap)
	}
	if snap.Complete() {
		t.Error("a snapshot with nothing in it reported itself complete")
	}
	if asked.Load() != 0 {
		t.Fatalf("Known contacted %d cluster(s)", asked.Load())
	}

	// One context probed, by somebody else. Known reports it and still says the
	// other has not been asked.
	h.Check(context.Background(), "local", true)
	before := asked.Load()
	for i := 0; i < 5; i++ {
		snap = h.Known()
	}
	if got := asked.Load(); got != before {
		t.Errorf("five calls to Known made %d cluster call(s). A navigation must cost "+
			"nothing: a page that costs a full probe timeout makes its icon look like "+
			"it does nothing", got-before)
	}
	if len(snap.Rows) != 1 || snap.Unprobed != 1 || snap.Total != 2 {
		t.Errorf("snapshot = %+v, want the one probed row and one still unasked", snap)
	}
	if snap.Rows[0].Name != "local" {
		t.Errorf("row is %q", snap.Rows[0].Name)
	}

	// And the other half: with the gate open, Known asks for a round behind the
	// answer it has already given. Without that nothing would ever fill the
	// cache from this page and a cold console would say "still asking" for ever.
	h.warmed = time.Time{}
	if snap := h.Known(); snap.Unprobed != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if h.Known().Complete() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("no background round filled the cache, so a page reading Known would say " +
		"'still asking' for ever — the answer has to be asked for behind the render")
}

// **/kt-healthz degrades on two conditions and Known changes neither**: the
// kubeconfig no longer reads, or nothing answered.
//
// Some clusters unreachable is 200: that is the ordinary state of an
// installation, and restarting the container over it would take away the one
// page that explains what happened. Asserted here rather than trusted, because
// the page is a second reader of the same cache and it would be easy to make
// the probe agree with the page about what "not answering" means.
func TestSomeClustersUnreachableIsStillHealthy(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")

	clusters, kubeconfig := oneAnsweringAndOneDark(t)
	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}, Timeout: 2 * time.Second})

	// A real round: one context answers, one refuses. No mutating call is made —
	// the probe asks the API server its version and nothing else.
	rows := h.All(context.Background(), true)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	answered, failed := 0, 0
	for _, row := range rows {
		if row.State == StateOK {
			answered++
		} else {
			failed++
		}
	}
	if answered != 1 || failed != 1 {
		t.Fatalf("%d answering and %d failing; this test needs one of each", answered, failed)
	}

	r := h.Readiness(context.Background())
	if !r.OK {
		t.Errorf("readiness is degraded (%q) with one cluster of two answering. Degraded "+
			"means the kubeconfig no longer reads, or *nothing* answered — some clusters "+
			"unreachable is the normal state of this installation and the reason the "+
			"health page exists", r.Reason)
	}

	// And the page's own reader agrees about what is there, without probing.
	snap := h.Known()
	if len(snap.Rows) != 2 || snap.Unprobed != 0 {
		t.Errorf("snapshot = %+v, want both rows and nothing outstanding", snap)
	}
}

// The one thing that does degrade it, kept beside the one that does not: with
// nothing answering at all, the probe must fail. Otherwise a process whose
// credential plugin is gone stays in service for ever.
func TestNothingAnsweringIsStillDegraded(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")

	kubeconfig := filepath.Join(t.TempDir(), "config")
	writeKubeconfig(t, kubeconfig, map[string]string{"dark": "https://127.0.0.1:1"})
	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}, Timeout: 2 * time.Second})
	h.All(context.Background(), true)

	if r := h.Readiness(context.Background()); r.OK {
		t.Errorf("readiness is fine (%q) with no context answering at all", r.Reason)
	}
}

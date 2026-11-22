package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// A row is stamped with when the probe *finished*, not when it started.
//
// A row is a statement about a moment, and the moment it is about is the one the
// cluster answered in. Stamping the start makes the row as old as the probe was
// long the instant it is written — ten seconds for a context that timed out —
// and it is then compared against a five-second failedTTL. `fresh` is false for
// it immediately, so it is never cached at all: every render re-probes every
// timing-out context, each spawning the ~0.55 s `aws` interpreter that
// backgroundFailedMax exists to bound, and each costing the page the full
// timeout again.
//
// The proportions here are the real ones halved into milliseconds: a probe that
// takes the whole timeout, against a failure window of half that.
func TestARowIsStampedWhenTheProbeFinishedAndNotWhenItStarted(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")

	const (
		timeout   = 400 * time.Millisecond
		failedTTL = 200 * time.Millisecond
	)

	var asked atomic.Int32
	// A server that accepts the request and never answers it. The probe therefore
	// costs exactly its own timeout, which is what an EKS context behind a lapsed
	// SSO session or a dropped VPN does.
	//
	// `release` rather than only `<-r.Context().Done()`, as a belt. probe() runs
	// the discovery call in a goroutine it abandons on its own deadline, and it
	// also passes the context: serverGitVersion asserts to
	// discovery.ServerVersionInterfaceWithContext, which client-go's own
	// *DiscoveryClient satisfies, so the request dies with probe's deadline and
	// this handler's r.Context() is done at 400 ms.
	//
	// What `release` still covers is the case where the request outlives Check.
	// serverGitVersion's fallback — a discovery implementation that is not
	// client-go's — probes uncancelably, and httptest.Server.Close waits for
	// outstanding handlers, so a handler with no other way out would make this
	// test cost client-go's own request timeout rather than this one.
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer func() {
		close(release)
		server.Close()
	}()

	kubeconfig := filepath.Join(t.TempDir(), "config")
	writeKubeconfig(t, kubeconfig, map[string]string{"slow": server.URL})
	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}

	h := New(Options{
		Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig},
		TTL: time.Minute, FailedTTL: failedTTL, Timeout: timeout,
	})

	before := time.Now()
	row := h.Check(context.Background(), "slow", false)
	after := time.Now()

	if row.State != StateTimeout {
		t.Fatalf("state is %q (%s), want a timeout — this test needs a probe that costs "+
			"its whole budget", row.State, row.Detail)
	}
	if asked.Load() != 1 {
		t.Fatalf("the cluster was asked %d times for one check", asked.Load())
	}

	// The stamp itself. Anything at or before `before` is the start of the probe,
	// which is the defect.
	if age := after.Sub(row.CheckedAt); age > timeout/2 {
		t.Errorf("the row is stamped %s before the probe returned, on a probe that took %s. "+
			"CheckedAt is when the cluster was asked, not when we started wondering: a row "+
			"born older than the window it is judged against is never fresh", age, timeout)
	}
	if row.CheckedAt.Before(before.Add(timeout / 2)) {
		t.Errorf("CheckedAt (%s) is within half a timeout of the call (%s), so it was taken "+
			"before the probe", row.CheckedAt, before)
	}

	// And the consequence, which is what actually costs the operator: the answer
	// must be reusable. With the stamp taken up front the row is 400 ms old at
	// birth against a 200 ms window, so the very next render asks the cluster
	// again — for ever, on every timing-out context, blocking each render for the
	// full timeout apiece.
	if !h.fresh(row) {
		t.Errorf("a row written this instant is already stale (checked %s, window %s)",
			time.Since(row.CheckedAt), failedTTL)
	}
	again := h.Check(context.Background(), "slow", false)
	if asked.Load() != 1 {
		t.Errorf("the second check re-probed a cluster whose answer is %s old, against a "+
			"%s window: %d calls. This is the health page's permanent latency floor — "+
			"fourteen contexts, several of them timing out, re-asked on every render",
			time.Since(again.CheckedAt), failedTTL, asked.Load())
	}
	if !again.CheckedAt.Equal(row.CheckedAt) {
		t.Errorf("the cached row was rebuilt: %s against %s", again.CheckedAt, row.CheckedAt)
	}

	// Took still measures the probe rather than becoming zero: the two readings
	// come from one call to now(), so the age and the duration cannot disagree.
	took, err := time.ParseDuration(row.Took)
	if err != nil {
		t.Fatalf("Took = %q: %v", row.Took, err)
	}
	if took < timeout/2 {
		t.Errorf("Took = %s for a probe that ran to a %s timeout", took, timeout)
	}
}

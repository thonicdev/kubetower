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

// A failure is believed for far less time than a success.
//
// Sharing one minute would make a renewed SSO session take a minute to be
// believed: `aws sso login` on the host, back to the tab, and the console
// repeats a cached "no answer" until the operator loses confidence in it.
func TestAFailureIsRetriedLongBeforeASuccessIs(t *testing.T) {
	h := &Health{ttl: time.Minute, failedTTL: 5 * time.Second}

	failed := Cluster{Name: "prod", State: StateTimeout}
	if got := h.ttlFor(failed); got >= time.Minute {
		t.Errorf("a cluster that did not answer is re-asked after %s, the same as one that did. "+
			"The unreachable one is the one somebody is fixing right now", got)
	}
	if got := h.ttlFor(failed); got != 5*time.Second {
		t.Errorf("ttlFor(failed) = %s, want the failure window", got)
	}

	ok := Cluster{Name: "prod", State: StateOK}
	if got := h.ttlFor(ok); got != time.Minute {
		t.Errorf("ttlFor(ok) = %s, want the ordinary window — a cluster that answers is not "+
			"going to stop in the next minute, and re-asking fourteen of them is a wasted "+
			"round trip", got)
	}
}

// And the cache honors it: a failure goes stale early, a success does not.
func TestTheCacheLetsAFailedClusterBeAskedAgainSooner(t *testing.T) {
	now := time.Now()
	h := &Health{
		ttl: time.Minute, failedTTL: 5 * time.Second,
		cache: map[string]Cluster{},
		now:   func() time.Time { return now },
	}

	h.cache["prod"] = Cluster{Name: "prod", State: StateTimeout, CheckedAt: now.Add(-20 * time.Second)}
	h.cache["tools"] = Cluster{Name: "tools", State: StateOK, CheckedAt: now.Add(-20 * time.Second)}

	// Twenty seconds is past the failure window and inside the success one.
	if h.fresh(h.cache["prod"]) {
		t.Error("a twenty-second-old failure is still being served, so a session renewed ten " +
			"seconds ago is not visible yet")
	}
	if !h.fresh(h.cache["tools"]) {
		t.Error("a twenty-second-old success was thrown away, which re-probes every answering " +
			"cluster on every page")
	}
	_ = context.Background()
}

// The default itself, read the way production reads it.
//
// Both tests above build `&Health{}` as a struct literal with failedTTL already
// filled in, so neither of them ever reads `defaultFailedTTL`. Without this one
// the constant could be raised to 55 seconds with the whole suite green — a
// renewed SSO session taking a minute to be believed, in the one number whose
// whole job is to prevent it.
//
// The number is written out here rather than compared against the constant.
// `h.failedTTL == defaultFailedTTL` is a tautology: raise the constant and the
// assertion raises with it, so the test would pass over precisely the change it
// exists to stop. The pattern is internal/actions/bulk_test.go's
// TestABulkRunNeverHasMoreThanItsShareInFlight.
func TestTheFailureWindowIsFiveSecondsAndComesFromTheDefault(t *testing.T) {
	const want = 5 * time.Second

	h := New(Options{})
	if h.failedTTL != want {
		t.Fatalf("a Health built with no FailedTTL believes a failure for %s, not %s. Anything "+
			"approaching a minute is the original bug: `aws sso login` on the host, back to the "+
			"tab, and the console repeats a cached \"no answer\" until the operator stops "+
			"trusting it — and clicking elsewhere and returning appears to fix it, which is why "+
			"nobody reports it", h.failedTTL, want)
	}
	// And it is reached through the path that decides, not only stored.
	if got := h.ttlFor(Cluster{Name: "prod", State: StateTimeout}); got != want {
		t.Errorf("ttlFor(a cluster that did not answer) = %s, want %s", got, want)
	}
	// The lower bound, so this cannot pass over a degenerate implementation. Zero
	// is not "always fresh" here, it is "re-probe fourteen contexts on every
	// fragment of every page", and ttlFor treats it as unset.
	if h.failedTTL < time.Second {
		t.Errorf("the failure window is %s. Below a second, drawing one page re-probes every "+
			"context in the kubeconfig", h.failedTTL)
	}
	// A failure and a success must not share a window — that sharing is what the
	// two tests above exist to stop, and it is checked here on the defaults
	// rather than on a literal.
	if h.ttl <= h.failedTTL {
		t.Errorf("a success is believed for %s and a failure for %s. The unreachable cluster is "+
			"the one somebody is fixing right now", h.ttl, h.failedTTL)
	}
}

// The background round backs off; a page and the Retry button never do.
//
// Without a backoff: the readiness probe warms every 60 s, a failure goes stale
// after 5 s, so every unreachable EKS context is re-probed every minute — each
// spawning an `aws eks get-token` whose Python interpreter costs 0.53–0.59 s of
// CPU before it does anything. With three such contexts that is **2.7–3.4 % of
// a core, permanently, with the UI closed**.
//
// The distinction is the whole fix, so it is what is asserted: the *same* cached
// failure must be left alone by the background round and re-asked by a foreground
// one. A test that only checked the window would pass over a backoff applied to
// both, which is the version that would make the health page lie.
func TestOnlyTheBackgroundRoundBacksOffOnARepeatedFailure(t *testing.T) {
	now := time.Now()
	h := &Health{
		ttl: time.Minute, failedTTL: 5 * time.Second,
		cache: map[string]Cluster{},
		fails: map[string]int{},
		now:   func() time.Time { return now },
	}

	// A cluster that has failed six times running: 5s doubled five times is 160s.
	stale := Cluster{Name: "prod", State: StateTimeout, CheckedAt: now.Add(-30 * time.Second)}
	h.cache["prod"] = stale
	h.fails["prod"] = 6

	if h.backgroundDue("prod", stale) {
		t.Error("the background round re-probes a cluster that failed six times running and was " +
			"asked thirty seconds ago. That is the every-60s spawn the backoff exists to stop")
	}
	// Thirty seconds is still past the *foreground* window, so a page re-asks.
	if h.fresh(stale) {
		t.Error("a thirty-second-old failure is being served to a page. An operator who has just " +
			"renewed a session must not read a stale refusal because a background timer says so")
	}

	// Past the backoff, the background round asks again — it backs off, it does
	// not give up.
	old := Cluster{Name: "prod", State: StateTimeout, CheckedAt: now.Add(-4 * time.Minute)}
	h.cache["prod"] = old
	if !h.backgroundDue("prod", old) {
		t.Error("a four-minute-old failure is still not re-probed in the background; the backoff " +
			"has become a refusal to look")
	}
}

// It doubles, and it stops. Both halves matter: without the doubling the cost
// never falls, and without the ceiling a cluster down all morning is asked once
// a day and the page stops being about now.
func TestTheBackoffDoublesUpToACeiling(t *testing.T) {
	now := time.Now()
	h := &Health{
		ttl: time.Minute, failedTTL: 5 * time.Second,
		cache: map[string]Cluster{}, fails: map[string]int{},
		now: func() time.Time { return now },
	}
	row := Cluster{Name: "prod", State: StateTimeout}

	// The window a given failure count buys, read by asking when a probe of that
	// age would be allowed.
	windowFor := func(fails int) time.Duration {
		h.fails["prod"] = fails
		for _, d := range []time.Duration{
			5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second,
			80 * time.Second, 160 * time.Second, 320 * time.Second,
			10 * time.Minute, 20 * time.Minute,
		} {
			row.CheckedAt = now.Add(-d)
			if h.backgroundDue("prod", row) {
				return d
			}
		}
		return time.Hour
	}

	if got := windowFor(1); got != 5*time.Second {
		t.Errorf("a first failure waits %s, want the plain failure window — the SSO case, where "+
			"the operator is fixing it right now", got)
	}
	if got := windowFor(4); got <= 5*time.Second {
		t.Errorf("a fourth consecutive failure still waits %s; the backoff is not doubling", got)
	}
	if got := windowFor(30); got > backgroundFailedMax {
		t.Errorf("thirty failures wait %s, past the %s ceiling — a cluster down all morning would "+
			"stop being re-checked often enough for this page to be about now",
			got, backgroundFailedMax)
	}
}

// One good answer restores the short window at once, rather than unwinding the
// doubling one round at a time. A cluster that comes back is the event this page
// exists to show.
func TestOneAnswerResetsTheBackoff(t *testing.T) {
	h := &Health{cache: map[string]Cluster{}, fails: map[string]int{}}
	h.fails["prod"] = 9

	h.recordOutcome("prod", StateOK)
	if n := h.fails["prod"]; n != 0 {
		t.Errorf("after answering, the failure count is %d — the next failure would inherit a "+
			"backoff earned while the cluster was down", n)
	}

	h.recordOutcome("prod", StateTimeout)
	if n := h.fails["prod"]; n != 1 {
		t.Errorf("a failure after a success counts %d, want 1", n)
	}
}

// The line that applies the backoff, which the three tests above do not reach.
//
// A backoff that never applies, or that applies to the foreground too, leaves
// all three green — because they call backgroundDue and fresh directly and never
// go through check, which is where the decision is made and to whom. A test of a
// predicate passes over the line that calls it, so this one drives `check` and
// counts probes by watching the server.
func TestOnlyABackgroundCheckHonoursTheBackoff(t *testing.T) {
	// The loopback rewrite must not apply: this test's cluster really is on this
	// machine's 127.0.0.1, and inside a container that address would be
	// redirected to the host. See internal/cluster/loopback.go.
	//
	// Without this the test passes on the host and fails in the Docker build,
	// where `go test ./...` runs with /.dockerenv present. check_test.go in this
	// package guards the same way.
	t.Setenv("KT_IN_CONTAINER", "0")

	var probes int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&probes, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"gitVersion":"v1.31.4"}`)
	}))
	defer server.Close()

	kubeconfig := filepath.Join(t.TempDir(), "config")
	writeKubeconfig(t, kubeconfig, map[string]string{"local": server.URL})
	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}, TTL: time.Minute})
	h.now = func() time.Time { return now }

	// Seed a cluster that has been failing for a while and was asked 30 s ago.
	// Its backoff at six consecutive failures is 160 s, so 30 s is inside it.
	h.cache["local"] = Cluster{
		Name: "local", State: StateTimeout, Detail: "seeded",
		CheckedAt: now.Add(-30 * time.Second),
	}
	h.fails["local"] = 6

	ctx := context.Background()

	// Background: left alone. This is the every-60s spawn the backoff exists for.
	if got := h.check(ctx, "local", false, true); got.Detail != "seeded" {
		t.Errorf("the background round re-probed a cluster inside its backoff: %+v", got)
	}
	if n := atomic.LoadInt32(&probes); n != 0 {
		t.Fatalf("the background round made %d probe(s) inside the backoff. Each one of those is "+
			"an `aws eks get-token` on a real EKS context, and 0.53-0.59 s of CPU", n)
	}

	// Foreground: asked, because 30 s is past the 5 s failure window and the page
	// must not show a stale refusal to somebody who has just fixed the cause.
	if got := h.check(ctx, "local", false, false); got.State != StateOK {
		t.Errorf("a foreground check did not re-probe; it returned %+v", got)
	}
	if n := atomic.LoadInt32(&probes); n == 0 {
		t.Error("a foreground check was refused by the backoff. An operator who has just run " +
			"`aws sso login` would read a ten-minute-old failure and conclude the tool is wrong")
	}
}

// A background round must not go looking for Prometheus.
//
// Discovery's job, once its five-minute window has expired, is to *find* the
// backend — and on an EKS context whose control plane refuses the service proxy
// that means a port-forward stood up, health-checked and torn down. On seven
// such clusters over a five-minute window that is about **98 port-forwards in
// 81 idle minutes**, 1.9-2.6 s each, with nobody looking at the UI.
//
// The health page prints the backend's name in a cell. That is not worth a tunnel,
// and an empty cell for a cluster nobody has opened a chart on is the honest
// answer — it fills in the moment somebody asks for a number.
//
// Counted rather than inspected: a test that checked which method the interface
// declares would pass over a probe that called both.
func TestABackgroundRoundReadsTheMetricsCacheAndNeverProbesIt(t *testing.T) {
	// The loopback rewrite must not apply: this test's cluster really is on this
	// machine's 127.0.0.1, and inside a container that address would be
	// redirected to the host. See internal/cluster/loopback.go.
	//
	// Without this the test passes on the host and fails in the Docker build,
	// where `go test ./...` runs with /.dockerenv present. check_test.go in this
	// package guards the same way.
	t.Setenv("KT_IN_CONTAINER", "0")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"gitVersion":"v1.31.4"}`)
	}))
	defer server.Close()

	kubeconfig := filepath.Join(t.TempDir(), "config")
	writeKubeconfig(t, kubeconfig, map[string]string{"local": server.URL})
	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}

	var discovers, cacheReads int32
	h := New(Options{
		Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}, Timeout: 5 * time.Second,
		Metrics: countingMetrics{discovers: &discovers, reads: &cacheReads},
	})
	ctx := context.Background()

	// Background: the cache is read and nothing is discovered.
	if row := h.check(ctx, "local", true, true); row.State != StateOK {
		t.Fatalf("the background probe did not reach the cluster: %+v", row)
	}
	if n := atomic.LoadInt32(&discovers); n != 0 {
		t.Errorf("a background round called Discover %d time(s). Each one of those stands up a "+
			"port-forward on an EKS context to fill in a cell nobody asked for", n)
	}
	if n := atomic.LoadInt32(&cacheReads); n == 0 {
		t.Error("the background round read nothing at all, so the metrics cell will stay empty " +
			"even once a page has discovered the backend")
	}

	// Foreground: discovery happens, because a page asking for a chart needs it.
	if row := h.check(ctx, "local", true, false); row.State != StateOK {
		t.Fatalf("the foreground probe did not reach the cluster: %+v", row)
	}
	if n := atomic.LoadInt32(&discovers); n == 0 {
		t.Error("a foreground check no longer discovers the backend, so the health page would " +
			"never learn which one a cluster has")
	}
}

// countingMetrics separates the two calls so the test can tell which was made.
type countingMetrics struct {
	discovers *int32
	reads     *int32
}

func (c countingMetrics) Discover(context.Context, string, bool) string {
	atomic.AddInt32(c.discovers, 1)
	return "prometheus"
}

func (c countingMetrics) Cached(string) string {
	atomic.AddInt32(c.reads, 1)
	return ""
}

package health

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// The cache, which is the reason this page can be opened twice in a row.
//
// Fourteen contexts is fourteen API calls, several of them behind an exec
// credential plugin, and the page refreshes itself every thirty seconds while
// the readiness probe asks the same question every thirty. Without the TTL each
// of those is a fresh round against every cluster.
//
// `h.now` exists precisely so this can be tested without sleeping a minute, and
// the count on the server is what proves it: a reused answer is one the cluster
// was never asked for.
func TestAFreshAnswerIsReusedAndAStaleOneIsNot(t *testing.T) {
	// The loopback rewrite must not apply: this test's cluster really is on this
	// machine's 127.0.0.1, and inside a container that address would be
	// redirected to the host. See internal/cluster/loopback.go.
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
	writeKubeconfig(t, kubeconfig, map[string]string{"local": server.URL})

	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}, TTL: time.Minute})
	h.now = func() time.Time { return now }

	ctx := context.Background()

	first := h.Check(ctx, "local", false)
	if first.State != StateOK {
		t.Fatalf("a cluster that answered was classified %q: %s", first.State, first.Detail)
	}
	if first.Version != "v1.31.4" {
		t.Errorf("version is %q, want v1.31.4", first.Version)
	}
	if asked.Load() != 1 {
		t.Fatalf("the first check made %d calls, want 1", asked.Load())
	}

	// Inside the TTL: the same answer, and the cluster is not asked again.
	again := h.Check(ctx, "local", false)
	if asked.Load() != 1 {
		t.Errorf("a second check inside the TTL asked the cluster again (%d calls) — "+
			"fourteen contexts times every refresh is what this cache exists to prevent", asked.Load())
	}
	if !again.CheckedAt.Equal(first.CheckedAt) {
		t.Errorf("the cached row was rebuilt: checked at %s, first at %s", again.CheckedAt, first.CheckedAt)
	}

	// force means force. This is what the "check again" button sends, and an
	// operator who has just run `aws sso login` must not be shown a minute-old
	// answer that still says their session is dead.
	h.Check(ctx, "local", true)
	if asked.Load() != 2 {
		t.Errorf("force did not reach the cluster: %d calls, want 2", asked.Load())
	}

	// Past the TTL, without sleeping for it.
	now = now.Add(time.Minute + time.Second)
	stale := h.Check(ctx, "local", false)
	if asked.Load() != 3 {
		t.Errorf("an answer older than the TTL was reused: %d calls, want 3", asked.Load())
	}
	if !stale.CheckedAt.Equal(now) {
		t.Errorf("the refreshed row is stamped %s, want %s", stale.CheckedAt, now)
	}
}

// A cluster that does not answer is classified, cached like any other, and
// never reported as absent metrics or as anything the operator did.
func TestAnUnreachableClusterIsCachedToo(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")

	// Port 1 on the loopback: nothing listens there, and the refusal is
	// immediate. No cluster is contacted by this test.
	kubeconfig := filepath.Join(t.TempDir(), "config")
	writeKubeconfig(t, kubeconfig, map[string]string{"dark": "https://127.0.0.1:1"})

	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}, TTL: time.Minute, Timeout: 5 * time.Second})
	h.now = func() time.Time { return now }

	row := h.Check(context.Background(), "dark", false)
	if row.State == StateOK {
		t.Fatal("nothing is listening on port 1 and the probe reported success")
	}
	if row.Detail == "" {
		t.Error("a failed row with no detail is a color, not an explanation")
	}

	h.mu.RLock()
	_, cached := h.cache["dark"]
	h.mu.RUnlock()
	if !cached {
		t.Error("a failure was not cached; the page would re-probe an unreachable cluster on every refresh")
	}
}

// The reader who navigates away must not take the whole estate down with them.
//
// /health refreshes itself every thirty seconds and htmx aborts the request in
// flight the moment the operator clicks a cluster in the rail. If that
// cancellation reached every probe, each would read its context being done as
// "no answer within 10s", and every row would be cached under that for the full
// minute: the next page load — very often the same person, one second later —
// would report the entire estate as down, with nothing wrong anywhere.
//
// With the probe taking the caller's context, this test caches `timeout` for
// both contexts and the assertions below fire.
func TestARequestTheBrowserAbandonedDoesNotMarkTheEstateDown(t *testing.T) {
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
	writeKubeconfig(t, kubeconfig, map[string]string{"one": server.URL, "two": server.URL})

	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}

	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}, TTL: time.Minute, Timeout: 5 * time.Second})

	// The browser hanging up, in the shape the handler sees it: r.Context(), done
	// before the round it started has finished.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rows := h.All(ctx, false)

	for _, row := range rows {
		if row.State != StateOK {
			t.Errorf("%s came back %q (%s) from a server that answers /version — "+
				"the only thing that happened is that the reader navigated away",
				row.Name, row.State, row.Detail)
		}
	}

	// The cache is what outlives the request, and it is what the next page reads.
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.cache) != 2 {
		t.Fatalf("the cache holds %d rows, want 2 — an abandoned round should still fill it, "+
			"as the CRD catalog and the usage cache do", len(h.cache))
	}
	for name, row := range h.cache {
		if row.State != StateOK {
			t.Errorf("cached %s as %q (%s); every page for the next minute would report "+
				"a healthy cluster as down", name, row.State, row.Detail)
		}
	}
	if asked.Load() != 2 {
		t.Errorf("the cluster was asked %d times, want 2 — a detached probe runs to completion", asked.Load())
	}
}

// Metrics discovery is the expensive half of a round — it may open a tunnel to
// Prometheus — and it is asked on the same abandoned request. Canceled with the
// caller it answers "no backend", which is then cached as a claim about the
// cluster made from a failure to ask it. And detached without a bound
// of its own it would be the one call in the round with nothing stopping it, so
// both halves are asserted here.
func TestMetricsDiscoveryIsDetachedAndStillBounded(t *testing.T) {
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

	// Read inside the call: the context is released the moment discovery returns,
	// so its state afterwards says nothing about the state it was handed.
	type handed struct {
		err      error
		deadline bool
	}
	seen := make(chan handed, 1)
	h := New(Options{
		Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}, Timeout: 5 * time.Second,
		Metrics: discoverFunc(func(ctx context.Context, _ string, _ bool) string {
			_, ok := ctx.Deadline()
			seen <- handed{err: ctx.Err(), deadline: ok}
			return "prometheus"
		}),
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	row := h.Check(ctx, "local", false)
	if row.Metrics != "prometheus" {
		t.Fatalf("metrics came back %q on an abandoned request; an empty one would be cached "+
			"as this cluster having no backend", row.Metrics)
	}

	var got handed
	select {
	case got = <-seen:
	default:
		t.Fatal("discovery was never called on a cluster that answered")
	}
	if got.err != nil {
		t.Errorf("discovery was handed an already-dead context (%v) — it would answer "+
			"'no backend' about a cluster it never asked", got.err)
	}
	if !got.deadline {
		t.Error("discovery was handed a context with no deadline; detaching drops the caller's " +
			"bound too, and this is the call that opens a tunnel")
	}
}

type discoverFunc func(ctx context.Context, cluster string, force bool) string

func (f discoverFunc) Discover(ctx context.Context, cluster string, force bool) string {
	return f(ctx, cluster, force)
}

// Cached answers nothing. A fake that returned a kind here would make a
// background round look like it had discovered one, which is the opposite of what
// the cache-only path is for: it reports what is known and never probes, and
// nothing is known in a test that has not called Discover.
func (f discoverFunc) Cached(string) string { return "" }

// A cluster that really was asked and really did not answer must still say so.
// The detachment above must not buy its safety by making every timeout unreportable:
// "no answer" is a fact about a cluster and one of the states this page exists
// to tell apart.
func TestAGenuineTimeoutIsStillReportedAndCached(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")

	// The handler blocks until the test lets it go, so the deadline is reached
	// for certain rather than by luck. Released before the server is closed —
	// defers run last-first — because Close waits for handlers to return.
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)

	kubeconfig := filepath.Join(t.TempDir(), "config")
	writeKubeconfig(t, kubeconfig, map[string]string{"slow": server.URL})

	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}

	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}, TTL: time.Minute, Timeout: 150 * time.Millisecond})

	row := h.Check(context.Background(), "slow", false)
	if row.State != StateTimeout {
		t.Fatalf("a cluster that never answered came back %q (%s), want %q", row.State, row.Detail, StateTimeout)
	}
	if !strings.Contains(row.Detail, "no answer within") {
		t.Errorf("detail is %q, which does not say the cluster was asked and stayed silent", row.Detail)
	}

	h.mu.RLock()
	cached, ok := h.cache["slow"]
	h.mu.RUnlock()
	if !ok || cached.State != StateTimeout {
		t.Error("a real timeout was not cached; the page would re-probe a dead cluster on every refresh")
	}
}

// The discrimination itself, at the point it is made. probe is reached with a
// canceled context only if the detachment in Check is ever removed — which is
// exactly when this matters, so it is asserted directly rather than inferred.
func TestACancelledProbeIsNotAnAnswerAboutTheCluster(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)

	kubeconfig := filepath.Join(t.TempDir(), "config")
	writeKubeconfig(t, kubeconfig, map[string]string{"local": server.URL})

	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}

	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}, Timeout: 10 * time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := h.probe(ctx, "local")
	if !got.abandoned {
		t.Fatalf("a canceled ask reported state %q (%s); nothing was learned about this cluster",
			got.state, got.detail)
	}
	if got.state != "" {
		t.Errorf("an abandoned probe carries state %q; abandoned is not a state", got.state)
	}

	// And an abandoned ask leaves the cache alone: there is nothing true to put
	// in it, and what is in it is read by every page for the next minute.
	if _, ok := h.cache["local"]; ok {
		t.Error("an abandoned probe was cached")
	}
	if row := h.lastKnown("local"); row.State != "" {
		t.Errorf("with no earlier round, lastKnown invented state %q", row.State)
	}
}

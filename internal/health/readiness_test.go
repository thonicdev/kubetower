package health

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// writeKubeconfig writes a kubeconfig naming each context and the address it
// should be reached at. Shared with check_test.go.
func writeKubeconfig(t *testing.T, path string, servers map[string]string) {
	t.Helper()

	var b strings.Builder
	b.WriteString("apiVersion: v1\nkind: Config\n")
	b.WriteString("clusters:\n")
	for name, server := range servers {
		b.WriteString("- name: " + name + "\n  cluster:\n    server: " + server + "\n")
	}
	b.WriteString("contexts:\n")
	for name := range servers {
		b.WriteString("- name: " + name + "\n  context:\n    cluster: " + name + "\n    user: " + name + "\n")
	}
	b.WriteString("users:\n")
	for name := range servers {
		b.WriteString("- name: " + name + "\n  user: {}\n")
	}
	for name := range servers {
		b.WriteString("current-context: " + name + "\n")
		break
	}

	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The condition /kt-healthz answers on, and the half of it that matters most is
// the negative one: some clusters unreachable must stay healthy. That is the
// ordinary state of this installation — an SSO session lapses, a VPN drops — and
// a container restarted over it takes away the one page that would have
// explained what happened, at the moment somebody opened it.
func TestReadinessDegradesOnlyWhenNothingAnswers(t *testing.T) {
	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "config")
	writeKubeconfig(t, kubeconfig, map[string]string{
		"answers": "https://127.0.0.1:1",
		"dark":    "https://127.0.0.1:2",
	})

	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}})
	h.now = func() time.Time { return now }
	// A round has just finished, so nothing below starts one: this test is about
	// what the cache is read as, not about probing.
	h.warmed = now

	ctx := context.Background()

	// Nothing probed yet. A container starting up is not a container that failed,
	// and reporting one would make every start look broken for as long as the
	// slowest cluster takes to answer.
	if got := h.Readiness(ctx); !got.OK {
		t.Errorf("degraded before the first round finished: %s", got.Reason)
	}

	// One answers, one does not.
	h.cache["answers"] = Cluster{Name: "answers", State: StateOK, CheckedAt: now}
	h.cache["dark"] = Cluster{Name: "dark", State: StateSSO, CheckedAt: now}

	got := h.Readiness(ctx)
	if !got.OK {
		t.Errorf("a partial outage was reported as degraded (%s) — restarting the container "+
			"over one lapsed SSO session removes the page that explains it", got.Reason)
	}
	if got.Answered != 1 || got.Total != 2 {
		t.Errorf("counted %d of %d answering, want 1 of 2", got.Answered, got.Total)
	}

	// Nothing answers at all. That is far more often this process's problem than
	// every cluster's: a credential plugin that cannot run, a lost network.
	h.cache["answers"] = Cluster{Name: "answers", State: StateDNS, CheckedAt: now}
	if got := h.Readiness(ctx); got.OK {
		t.Error("every context failed and the probe still reported healthy, so a process that can reach nothing stays in service")
	}
}

// The mount that went away. The container keeps listening, the HTTP server keeps
// answering, and the one thing this process exists to do is impossible.
func TestAKubeconfigThatNoLongerReadsIsDegraded(t *testing.T) {
	dir := t.TempDir()

	gone := filepath.Join(dir, "not-here")
	h := New(Options{Kubeconfig: cluster.Kubeconfig{gone}})
	got := h.Readiness(context.Background())
	if got.OK {
		t.Error("a missing kubeconfig reported healthy")
	}
	if !strings.Contains(got.Reason, "kubeconfig") {
		t.Errorf("reason is %q, which does not name the kubeconfig", got.Reason)
	}

	// And a file that exists but is not one. An empty bind mount is exactly this
	// shape, and it is the failure this guards.
	rubbish := filepath.Join(dir, "config")
	if err := os.WriteFile(rubbish, []byte("this is not a kubeconfig: {{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := New(Options{Kubeconfig: cluster.Kubeconfig{rubbish}}).Readiness(context.Background()); got.OK {
		t.Error("a kubeconfig that does not parse reported healthy")
	}
}

// A kubeconfig that parses and declares nothing is the same accident wearing a
// shape the parser accepts.
func TestAKubeconfigWithNoContextsIsDegraded(t *testing.T) {
	kubeconfig := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}

	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}})
	h.warmed = time.Now()

	got := h.Readiness(context.Background())
	if got.OK {
		t.Errorf("an empty kubeconfig reported healthy: %s", got.Reason)
	}
}

// Every answer carries a sentence, healthy ones included. A probe that says only
// "ok" is a probe nobody can debug from `docker inspect`.
func TestEveryReadinessAnswerSaysWhy(t *testing.T) {
	h := New(Options{Kubeconfig: cluster.Kubeconfig{filepath.Join(t.TempDir(), "gone")}})
	if got := h.Readiness(context.Background()); got.Reason == "" {
		t.Error("a degraded answer with no reason")
	}
}

// The background round the readiness probe asks for.
//
// It exists because nothing else in this binary probes on a timer: without it
// the cache is only ever filled by an operator opening the health page, and
// /kt-healthz would report "none probed yet" for the life of the container. It
// is throttled to the TTL because the probe is called every thirty seconds and a
// round is one API call per cluster.
func TestTheBackgroundRoundHappensOncePerTTL(t *testing.T) {
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

	// The clock is shared with the round's own goroutine, so it is read and
	// written under a lock rather than plainly: `go test -race` is a gate here.
	var clock sync.Mutex
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	advance := func(d time.Duration) {
		clock.Lock()
		now = now.Add(d)
		clock.Unlock()
	}

	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}, TTL: time.Minute})
	h.now = func() time.Time {
		clock.Lock()
		defer clock.Unlock()
		return now
	}

	waitFor := func(what string, done func() bool) {
		t.Helper()
		for range 200 {
			if done() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	idle := func() bool {
		h.mu.RLock()
		defer h.mu.RUnlock()
		return !h.warming
	}

	h.warm()
	// Both conditions: the round happened, and it has finished — so what stops
	// the next one below is the TTL and not the one-at-a-time guard.
	waitFor("the first round to finish", func() bool { return asked.Load() == 1 && idle() })

	// A round would refill this. The throttle means one does not run.
	h.mu.Lock()
	delete(h.cache, "local")
	h.mu.Unlock()

	h.warm()
	time.Sleep(150 * time.Millisecond)
	if asked.Load() != 1 {
		t.Errorf("a second round ran inside the TTL (%d calls) — /kt-healthz is asked every "+
			"thirty seconds and a round is one API call per cluster", asked.Load())
	}

	// Past the TTL it runs again, so the reading cannot go stale.
	advance(time.Minute + time.Second)
	h.warm()
	waitFor("a round past the TTL", func() bool { return asked.Load() == 2 })
}

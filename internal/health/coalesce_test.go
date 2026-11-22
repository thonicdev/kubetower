package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// Forced probes of one cluster arriving together are one probe.
//
// A forced Check skips the cache, so without coalescing they would stack:
// arriving on a stale-red cluster several times within its timeout would start
// one probe each - for an EKS context, one `aws` interpreter each. Five callers here,
// against an API server that holds `/version` until every one of them is
// inside Check, and the server must be asked once, with all five handed its
// answer.
//
// Released only once all five are inside `joined` - counted from their stacks,
// as probeGoroutines counts probes - so the test does not depend on how fast a
// loaded runner starts a goroutine.
func TestConcurrentForcedProbesOfOneClusterAreOneProbe(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")

	var asked atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			http.NotFound(w, r)
			return
		}
		asked.Add(1)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"major":"1","minor":"31","gitVersion":"v1.31.0"}`))
	}))
	t.Cleanup(server.Close)

	kubeconfig := filepath.Join(t.TempDir(), "config")
	writeKubeconfig(t, kubeconfig, map[string]string{"stale-red": server.URL})
	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}, Timeout: 5 * time.Second})

	const callers = 5
	rows := make([]Cluster, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows[i] = h.Check(context.Background(), "stale-red", true)
		}()
	}

	deadline := time.Now().Add(5 * time.Second)
	for joinedGoroutines() < callers {
		if time.Now().After(deadline) {
			close(release)
			t.Fatalf("only %d of %d callers reached Check's probe within 5s", joinedGoroutines(), callers)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	wg.Wait()

	if n := asked.Load(); n != 1 {
		t.Errorf("the API server was asked %d times for %d forced probes arriving together; want once", n, callers)
	}
	for i, row := range rows {
		if row.State != StateOK || row.Version != "v1.31.0" {
			t.Errorf("caller %d got %q (%s, %q); every caller is handed the one answer", i, row.State, row.Detail, row.Version)
		}
	}

	// And one at a time is still one probe each: a forced probe after the
	// last one finished asks again, which is what Refresh is for.
	h.Check(context.Background(), "stale-red", true)
	if n := asked.Load(); n != 2 {
		t.Errorf("a forced probe after the first round asked %d times in all; want 2 - it must ask again", n)
	}
}

// joinedGoroutines counts the goroutines inside joined, the leader and the
// callers waiting on it - by stack frame, for the reason probeGoroutines gives.
func joinedGoroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), "health.(*Health).joined(")
		}
		buf = make([]byte, 2*len(buf))
	}
}

// A caller waiting on a probe in flight answers to its own context: a request
// that went away stops waiting at once, is answered
// from the last real round - here none, so a row with no state, which All
// drops and Probe reports as not probed - and does not start a probe of its
// own. The probe it was waiting on runs on, and its answer is cached.
func TestAWaiterWhoseRequestWentAwayStopsWaiting(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")

	var asked atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			http.NotFound(w, r)
			return
		}
		asked.Add(1)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"major":"1","minor":"31","gitVersion":"v1.31.0"}`))
	}))
	t.Cleanup(server.Close)

	kubeconfig := filepath.Join(t.TempDir(), "config")
	writeKubeconfig(t, kubeconfig, map[string]string{"stale-red": server.URL})
	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}, Timeout: 5 * time.Second})

	leader := make(chan Cluster, 1)
	go func() { leader <- h.Check(context.Background(), "stale-red", true) }()
	deadline := time.Now().Add(5 * time.Second)
	for asked.Load() == 0 {
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("the leading probe never reached the API server")
		}
		time.Sleep(5 * time.Millisecond)
	}

	gone, cancel := context.WithCancel(context.Background())
	cancel()
	waited := make(chan Cluster, 1)
	go func() { waited <- h.Check(gone, "stale-red", true) }()
	select {
	case row := <-waited:
		if row.State != "" {
			t.Errorf("the waiter that went away was answered %q; with no earlier round it has nothing to claim", row.State)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("a caller whose request went away is still waiting on the probe in flight after 2s")
	}

	close(release)
	if row := <-leader; row.State != StateOK {
		t.Errorf("the leading probe answered %q (%s); it runs on whoever stopped waiting", row.State, row.Detail)
	}
	if n := asked.Load(); n != 1 {
		t.Errorf("the API server was asked %d times; the waiter that went away must not have probed", n)
	}
}

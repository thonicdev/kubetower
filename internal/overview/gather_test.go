package overview

// What the two dashboards cost, rather than what they say.
//
// Everything else in this package's tests is about the figures being right. The
// three below are about the page being affordable to draw: one pod list per
// refresh instead of two, and a control plane that has stopped answering not
// being able to hold the render open.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// Cluster sums what the pods have reserved; Workloads counts them by phase. The
// console runs both at once for the same cluster, every fifteen seconds, per
// open tab — and a cluster-wide pod list is the largest call either page makes.
// Two of them is one too many.
func TestTheTwoDashboardsListThePodsOnce(t *testing.T) {
	set := fake.NewClientset(
		node("node-1", "4", "16Gi", "110", ready(corev1.ConditionTrue)),
		pod("web-0", "node-1", corev1.PodRunning, corev1.Container{Name: "app", Resources: requesting("100m", "128Mi")}),
	)

	var (
		mu      sync.Mutex
		lists   int
		release = make(chan struct{})
	)
	set.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		lists++
		mu.Unlock()
		// Held open so both dashboards are certainly inside listPods together,
		// which is the situation this is about. Without it the fake answers
		// instantly and the test would be measuring the scheduler.
		<-release
		return false, nil, nil
	})
	client := &cluster.Client{Context: "test", Clients: set}

	var wg sync.WaitGroup
	wg.Add(2)
	var machErr, workErr error
	go func() {
		defer wg.Done()
		_, machErr = Cluster(context.Background(), accesstest.Allow(), client, nil)
	}()
	go func() {
		defer wg.Done()
		_, workErr = Workloads(context.Background(), accesstest.Allow(), client, "", nil)
	}()

	time.Sleep(250 * time.Millisecond)
	close(release)
	wg.Wait()

	if machErr != nil || workErr != nil {
		t.Fatalf("cluster: %v; workloads: %v", machErr, workErr)
	}
	mu.Lock()
	defer mu.Unlock()
	if lists != 1 {
		t.Fatalf("the pods were listed %d times for one refresh of the page, want 1", lists)
	}
}

// The share must not turn one dashboard's refusal into the other's silence, nor
// hand two callers different answers.
func TestBothDashboardsSeeTheSameSharedList(t *testing.T) {
	fetches := 0
	share := &podShare{waiting: map[string]*podFetch{}}
	got, err := share.list(context.Background(), "test", "", func(context.Context) ([]corev1.Pod, error) {
		fetches++
		return []corev1.Pod{{}}, nil
	})
	if err != nil || len(got) != 1 {
		t.Fatalf("first call: %v, %d pods", err, len(got))
	}

	// Nothing is held between refreshes: a share that cached would have to decide
	// how stale a dashboard may be, and this one deliberately does not.
	if _, err := share.list(context.Background(), "test", "", func(context.Context) ([]corev1.Pod, error) {
		fetches++
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if fetches != 2 {
		t.Fatalf("%d fetches for two separate refreshes, want 2: the share is caching", fetches)
	}
}

// hangs is an API server that accepts the connection and then says nothing —
// which is what a black-holed route looks like from in here, and is not the same
// as a refused one.
func hangs(t *testing.T) kubernetes.Interface {
	t.Helper()
	block := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-block
	}))
	t.Cleanup(func() {
		close(block)
		server.Close()
	})

	api, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatalf("building a client: %v", err)
	}
	return api
}

// The version is printed in a corner and its absence is already reported as a
// degraded panel. Without a bound on the call, a cluster that stops answering
// holds the whole render for the 30 s of rest.Config.Timeout while the
// fifteen-second refresh queues two more behind it.
func TestTheVersionGivesUpRatherThanHoldingTheRender(t *testing.T) {
	restore := versionTimeout
	versionTimeout = 100 * time.Millisecond
	t.Cleanup(func() { versionTimeout = restore })

	start := time.Now()
	version, err := serverVersion(context.Background(), hangs(t))
	waited := time.Since(start)

	if err == nil {
		t.Fatalf("a cluster that never answered reported version %q", version)
	}
	if !strings.Contains(err.Error(), "did not state its version") {
		t.Errorf("the reason is not sayable on the page: %v", err)
	}
	if waited > 30*versionTimeout {
		t.Fatalf("waited %s for a call bounded at %s", waited, versionTimeout)
	}
}

// Giving up is not the same as letting go.
//
// The deadline above stops the *caller* waiting. The request must stop too:
// handed to an unstoppable goroutine, the socket would run on to client-go's
// own 30 s timeout with nobody left to read the answer.
//
// This asserts the fact rather than the mechanism: the *server* sees its request
// canceled once our deadline fires. A goroutine count would pass against a
// leaked connection, and a mock would only pin the assertion we wrote.
func TestTheVersionCancelsTheRequestAndNotOnlyTheWait(t *testing.T) {
	restore := versionTimeout
	versionTimeout = 150 * time.Millisecond
	t.Cleanup(func() { versionTimeout = restore })

	cancelled := make(chan struct{})
	var once sync.Once
	block := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			once.Do(func() { close(cancelled) })
		case <-block:
		}
	}))
	t.Cleanup(func() {
		close(block)
		server.Close()
	})

	api, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatalf("building a client: %v", err)
	}

	if _, err := serverVersion(context.Background(), api); err == nil {
		t.Fatal("a cluster that never answered reported a version")
	}

	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the API server was still holding the request five seconds after " +
			"the call gave up; the deadline bounded the caller and nothing else")
	}
}

// And it must let go when the request it belongs to does, whatever its own
// bound says.
func TestTheVersionLeavesWithItsRequest(t *testing.T) {
	api := hangs(t)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := serverVersion(ctx, api); err == nil {
			t.Error("a canceled request came back with a version")
		}
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the call outlived the request that asked for it")
	}
}

// --- a panel that panicked, and the page beside it ------------------------------

// A panic inside the shared pod fetch must not leave the `waiting` entry in
// place with `done` never closed, or every later overview request for that
// cluster and namespace blocks until its own context expires: the dashboard
// wedged for that cluster until the process restarts. There is no window and
// no eviction here, so nothing would clear it.
func TestAPanickingPodListDoesNotWedgeEveryLaterRequest(t *testing.T) {
	share := &podShare{waiting: map[string]*podFetch{}}

	pods, err := share.list(context.Background(), "test", "", func(context.Context) ([]corev1.Pod, error) {
		panic("the client-go transport did something unexpected")
	})
	if err == nil {
		t.Fatalf("a fetch that panicked came back with %d pods and no error — an empty answer and a "+
			"failed one are different answers", len(pods))
	}
	if !strings.Contains(err.Error(), "panicked") {
		t.Errorf("the reason does not say what happened: %v", err)
	}

	// The entry must be gone, or the next reader waits on a channel nobody will
	// ever close.
	share.mu.Lock()
	left := len(share.waiting)
	share.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d entries left behind; the next request for this cluster blocks until its own context expires", left)
	}

	// And the proof of that: the very next request is served.
	done := make(chan struct{})
	go func() {
		defer close(done)
		got, err := share.list(context.Background(), "test", "", func(context.Context) ([]corev1.Pod, error) {
			return []corev1.Pod{{}}, nil
		})
		if err != nil || len(got) != 1 {
			t.Errorf("the request after the panic got %d pods, %v", len(got), err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the dashboard is wedged for this cluster: a later request never returned")
	}
}

// The other dashboard is inside the same fetch at the same instant — that is the
// whole reason the share exists. It must be told the fetch failed, not handed an
// empty slice: a cluster running 429 pods reported as running none is the exact
// defect this package's first paragraph is about.
func TestTheWaiterOnAPanickingFetchIsToldItFailed(t *testing.T) {
	share := &podShare{waiting: map[string]*podFetch{}}
	inside := make(chan struct{})
	release := make(chan struct{})

	var leaderErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, leaderErr = share.list(context.Background(), "test", "", func(context.Context) ([]corev1.Pod, error) {
			close(inside)
			<-release
			panic("boom")
		})
	}()

	<-inside
	wg.Add(1)
	var waiterPods []corev1.Pod
	var waiterErr error
	go func() {
		defer wg.Done()
		waiterPods, waiterErr = share.list(context.Background(), "test", "", func(context.Context) ([]corev1.Pod, error) {
			t.Error("the second dashboard issued its own list rather than joining the one in flight")
			return nil, nil
		})
	}()

	// Both are certainly in the share now; let the leader blow up.
	time.Sleep(100 * time.Millisecond)
	close(release)

	waited := make(chan struct{})
	go func() { wg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter never came back: done was not closed on the panic path")
	}

	if leaderErr == nil {
		t.Error("the dashboard that led the fetch was told nothing went wrong")
	}
	if waiterErr == nil {
		t.Fatalf("the waiting dashboard was handed %d pods and no error, which renders as a cluster "+
			"with nothing running on it", len(waiterPods))
	}
}

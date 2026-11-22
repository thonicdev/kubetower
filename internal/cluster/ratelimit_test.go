package cluster

import (
	"context"
	"encoding/pem"
	"errors"
	"strings"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/flowcontrol"
)

// One allowance per cluster, not one per caller.
//
// The identity assertions in dynamic_test.go say each cluster holds one dynamic
// client. They cannot say what this file is about, because what is per-caller
// is not the client, it is the token bucket *inside* it: with
// `rest.Config.RateLimiter` left nil, client-go mints a fresh one in every REST
// client it builds (rest/config.go:370, kubernetes/clientset.go:498). Two here,
// two more for the streaming pair, one more for anything built later from
// Client.Config. So `clientQPS = 50` would mean fifty per client, and the number
// of clients is not a number anybody chose.
//
// These tests drive it through Manager.client — the code that actually decides —
// and observe it where a caller would: a request made through one client
// consumes the allowance the other one has to ask for.

var errAllowanceSpent = errors.New("the cluster's allowance is spent")

// countingLimiter is a real limiter that records who asked it.
type countingLimiter struct {
	inner flowcontrol.RateLimiter
	mu    sync.Mutex
	seen  int
}

func (c *countingLimiter) record() {
	c.mu.Lock()
	c.seen++
	c.mu.Unlock()
}

func (c *countingLimiter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen
}

func (c *countingLimiter) TryAccept() bool                { c.record(); return c.inner.TryAccept() }
func (c *countingLimiter) Accept()                        { c.record(); c.inner.Accept() }
func (c *countingLimiter) Wait(ctx context.Context) error { c.record(); return c.inner.Wait(ctx) }
func (c *countingLimiter) Stop()                          { c.inner.Stop() }
func (c *countingLimiter) QPS() float32                   { return c.inner.QPS() }

// oneTokenLimiter holds exactly one token and never refills it. A bucket that
// small is not a plausible setting; it is what makes "they share it" observable
// in one request each rather than in a timing measurement.
type oneTokenLimiter struct {
	mu   sync.Mutex
	left int
}

func (l *oneTokenLimiter) take() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.left <= 0 {
		return false
	}
	l.left--
	return true
}

func (l *oneTokenLimiter) TryAccept() bool { return l.take() }
func (l *oneTokenLimiter) Accept()         { l.take() }
func (l *oneTokenLimiter) Wait(context.Context) error {
	if l.take() {
		return nil
	}
	return errAllowanceSpent
}
func (l *oneTokenLimiter) Stop()        {}
func (l *oneTokenLimiter) QPS() float32 { return 1 }

// managerWithLimiter builds a Manager whose contexts get the limiter build
// returns. Set before the first Client call, because that is when a context's
// bucket is made.
func managerWithLimiter(t *testing.T, kubeconfig string, build func() flowcontrol.RateLimiter) *Manager {
	t.Helper()
	m, err := NewManager(kubeconfig)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	m.newLimiter = build
	return m
}

// Both of a cluster's clients ask the cluster's own bucket.
//
// The count is the assertion a pointer comparison cannot make: `Config.
// RateLimiter` being set proves what the config says, and the config is exactly
// what a dynamic client copies and then ignores if it was built the other way.
func TestBothClientsOnAClusterChargeTheSameAllowance(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")

	server, _ := tlsServer(t)
	limiter := &countingLimiter{inner: flowcontrol.NewTokenBucketRateLimiter(clientQPS, clientBurst)}
	m := managerWithLimiter(t,
		oneClusterKubeconfig(t, "test", server.URL, certOf(t, server)),
		func() flowcontrol.RateLimiter { return limiter })

	client, err := m.Client("test")
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	if client.Config.RateLimiter == nil {
		t.Fatal("the config carries no rate limiter, so client-go mints one per REST client " +
			"and this cluster's QPS ceiling is per caller")
	}

	ctx := context.Background()
	if _, err := client.Clients.CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{}); err != nil {
		t.Fatalf("the typed client: %v", err)
	}
	if n := limiter.count(); n != 1 {
		t.Fatalf("the typed client charged the cluster's allowance %d times for one request, "+
			"want 1. Nothing below this means anything if the typed half is not using it", n)
	}

	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	if _, err := client.Dynamic.Resource(gvr).Namespace("default").List(ctx, metav1.ListOptions{}); err != nil {
		t.Fatalf("the dynamic client: %v", err)
	}
	if n := limiter.count(); n != 2 {
		t.Errorf("the cluster's allowance was charged %d times after one request through each "+
			"client, want 2. The dynamic client is spending a bucket of its own", n)
	}

	// The streaming client of the same context is a third REST client and a
	// fourth; it shares the one bucket too. A stream costs a token when it starts
	// and nothing while its body is open, which is why sharing is right here.
	stream, err := m.StreamingClient("test")
	if err != nil {
		t.Fatalf("StreamingClient: %v", err)
	}
	if stream.Config.RateLimiter != client.Config.RateLimiter {
		t.Error("the streaming client carries a different bucket, so a cluster has two ceilings")
	}
	if _, err := stream.Clients.CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{}); err != nil {
		t.Fatalf("the streaming client: %v", err)
	}
	if n := limiter.count(); n != 3 {
		t.Errorf("the allowance was charged %d times after a request through the streaming "+
			"client as well, want 3", n)
	}
}

// One caller spends it, the next is refused: two callers, one allowance.
//
// This is the observable form of the shared bucket. Without it the dynamic
// client carries its own bucket at the package's QPS and Burst, so it never
// notices that the typed client has just used the cluster's last token, and the
// list below succeeds.
func TestOneCallerSpendsTheAllowanceTheOtherNeeds(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")

	server, _ := tlsServer(t)
	m := managerWithLimiter(t,
		oneClusterKubeconfig(t, "test", server.URL, certOf(t, server)),
		func() flowcontrol.RateLimiter { return &oneTokenLimiter{left: 1} })

	client, err := m.Client("test")
	if err != nil {
		t.Fatalf("Client: %v", err)
	}

	ctx := context.Background()
	if _, err := client.Clients.CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{}); err != nil {
		t.Fatalf("the first caller could not spend the one token there was: %v", err)
	}

	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	_, err = client.Dynamic.Resource(gvr).Namespace("default").List(ctx, metav1.ListOptions{})
	if err == nil {
		t.Fatal("the second caller was served after the cluster's only token had been spent by " +
			"the first. It is drawing on a bucket of its own, so this package's QPS and Burst " +
			"are a per-caller allowance rather than a per-cluster ceiling")
	}
	if !errors.Is(err, errAllowanceSpent) && !strings.Contains(err.Error(), errAllowanceSpent.Error()) {
		t.Fatalf("the second caller failed for a reason that is not the allowance: %v", err)
	}
	t.Logf("the second caller was refused by the cluster's own bucket: %v", err)
}

// Two clusters, two allowances. The ceiling is per cluster: a busy one must not
// throttle a quiet one, which is what a single shared limiter would do — the
// same mistake pointing the other way.
func TestTwoClustersDoNotShareOneAllowance(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")

	server, _ := tlsServer(t)
	ca := base64Of(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certOf(t, server)}))
	m := managerWithLimiter(t,
		kubeconfigWithContexts(t, server.URL, ca, "one", "two"),
		func() flowcontrol.RateLimiter { return &oneTokenLimiter{left: 1} })

	first, err := m.Client("one")
	if err != nil {
		t.Fatalf("Client(one): %v", err)
	}
	second, err := m.Client("two")
	if err != nil {
		t.Fatalf("Client(two): %v", err)
	}
	if first.Config.RateLimiter == second.Config.RateLimiter {
		t.Fatal("two clusters were given one bucket: a busy cluster now throttles a quiet one")
	}

	ctx := context.Background()
	if _, err := first.Clients.CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{}); err != nil {
		t.Fatalf("the first cluster: %v", err)
	}
	if _, err := second.Clients.CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{}); err != nil {
		t.Fatalf("a request to the second cluster was refused after the first cluster spent its "+
			"own token: %v", err)
	}
}

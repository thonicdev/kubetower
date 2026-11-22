package health

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// A credential plugin that never returns holds the ask that ran it, and no
// context reaches it. Probes of that cluster still answer at their deadline,
// one after the other, and none of them starts a second ask: they wait on the
// one already hung. Once it returns, the next probe asks again.
func TestSequentialProbesOfAHungClusterDoNotStack(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")
	kubeconfig := filepath.Join(t.TempDir(), "config")
	writeKubeconfig(t, kubeconfig, map[string]string{"hung": "https://127.0.0.1:1"})
	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}

	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}, Timeout: 50 * time.Millisecond})
	var asked atomic.Int32
	release := make(chan struct{})
	h.ask = func(context.Context, *cluster.Client) (string, error) {
		asked.Add(1)
		<-release // what a hung plugin does: ignore every context
		return "v1.31.0", nil
	}
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
	})

	for i := 0; i < 5; i++ {
		if got := h.Check(context.Background(), "hung", true); got.State != StateTimeout {
			t.Fatalf("probe %d came back %q (%s), want %q", i+1, got.State, got.Detail, StateTimeout)
		}
	}
	if n := asked.Load(); n != 1 {
		t.Fatalf("five probes of a hung cluster started %d asks, want 1: each one is a goroutine and a "+
			"plugin process that never ends", n)
	}

	close(release)
	released = true
	waitUntil(t, "the hung ask to end", func() bool {
		h.mu.RLock()
		defer h.mu.RUnlock()
		return len(h.asks) == 0
	})
	if got := h.Check(context.Background(), "hung", true); got.State != StateOK {
		t.Fatalf("after the ask returned, a probe came back %q (%s), want %q", got.State, got.Detail, StateOK)
	}
	if n := asked.Load(); n != 2 {
		t.Errorf("the probe after the hung ask ended asked %d times in all, want 2", n)
	}
}

// A kubeconfig on a mount that has stopped answering: the read blocks and
// nothing can call it off. Readiness says so within its wait instead of
// hanging with it, and answers asked while the read is still blocked wait on
// that read rather than starting one each.
func TestAKubeconfigThatHangsIsDegradedAndReadOnce(t *testing.T) {
	old := surveyWait
	surveyWait = 50 * time.Millisecond
	t.Cleanup(func() { surveyWait = old })

	h := New(Options{Kubeconfig: cluster.Kubeconfig{filepath.Join(t.TempDir(), "config")}})
	var reads atomic.Int32
	release := make(chan struct{})
	h.survey = func() cluster.KubeconfigSurvey {
		reads.Add(1)
		<-release
		return cluster.KubeconfigSurvey{}
	}
	defer close(release)

	for i := 0; i < 3; i++ {
		got := h.Readiness(context.Background())
		if got.OK || !strings.Contains(got.Reason, "has not finished") {
			t.Fatalf("answer %d: ok=%t %q, want degraded because the kubeconfig did not read in time", i+1, got.OK, got.Reason)
		}
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("three answers started %d reads of a hung kubeconfig, want 1", n)
	}
}

// The cache is as recent as the last round that finished. A round that has
// overrun its bound is held up, and the rows it would have refreshed are not
// reported as if they were current.
func TestReadinessSaysWhenItsAnswerHasStoppedMoving(t *testing.T) {
	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "config")
	writeKubeconfig(t, kubeconfig, map[string]string{"answers": "https://127.0.0.1:1"})
	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig}})
	h.now = func() time.Time { return now }
	h.cache["answers"] = Cluster{Name: "answers", State: StateOK, CheckedAt: now.Add(-time.Hour)}

	// A round in progress, within its bound: the answer stands.
	h.warming, h.warmStarted = true, now.Add(-h.roundBound()/2)
	if got := h.Readiness(context.Background()); !got.OK {
		t.Fatalf("a round still within its bound was read as held up: %s", got.Reason)
	}

	// The same round, past it.
	h.warmStarted = now.Add(-h.roundBound() - time.Second)
	got := h.Readiness(context.Background())
	if got.OK || !strings.Contains(got.Reason, "has not finished") {
		t.Errorf("a round held up past its bound left readiness at ok=%t %q, the last thing it knew", got.OK, got.Reason)
	}
}

func waitUntil(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

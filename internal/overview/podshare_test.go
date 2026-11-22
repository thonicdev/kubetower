package overview

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A leader that goes away must not take the waiters' answer with it.
//
// **`podLists` is package-level and keyed on cluster and namespace alone**, so it
// collapses calls across *different* HTTP requests — two browser tabs on the
// same cluster, each polling on its own timer, overlap sooner or later on the
// largest call either page makes. The first to arrive leads. Navigate away in
// that tab and net/http cancels its request; were the shared fetch to die with
// it, the *other* tab would render `context canceled` over a cluster that is
// perfectly healthy.
//
// A cancellation is a fact about a browser, not about a
// cluster, and drawing one as the other is how an operator is sent looking at a
// cluster that is fine.
func TestACancelledLeaderDoesNotPoisonTheWaiters(t *testing.T) {
	const cluster = "poison-test"

	release := make(chan struct{})
	started := make(chan struct{})

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()

	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		_, _ = podLists.list(leaderCtx, cluster, "", func(ctx context.Context) ([]corev1.Pod, error) {
			close(started)
			// Hold until the test says, then answer — but leave on the context
			// if the detachment is absent, so a broken build fails fast rather than
			// hanging the package.
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "api-1"}}}, nil
		})
	}()

	<-started

	// A second request arrives while the first is in flight, and waits.
	type answer struct {
		pods []corev1.Pod
		err  error
	}
	waiter := make(chan answer, 1)
	go func() {
		pods, err := podLists.list(context.Background(), cluster, "", func(context.Context) ([]corev1.Pod, error) {
			t.Error("the second caller ran its own fetch; the share did not collapse them, " +
				"so this test is no longer about what it says it is")
			return nil, nil
		})
		waiter <- answer{pods, err}
	}()

	// Give the waiter a moment to reach the share, then take the leader away —
	// the tab navigating.
	time.Sleep(50 * time.Millisecond)
	cancelLeader()

	// The fetch must still be alive. Were it not detached, it would receive the
	// cancellation and return an error to everybody.
	close(release)

	select {
	case got := <-waiter:
		if got.err != nil {
			if errors.Is(got.err, context.Canceled) {
				t.Fatalf("the waiter was handed %v — the leader's browser tab decided that "+
					"this cluster had failed", got.err)
			}
			t.Fatalf("the waiter got an unexpected error: %v", got.err)
		}
		if len(got.pods) != 1 || got.pods[0].Name != "api-1" {
			t.Fatalf("the waiter got %d pods, want the one the fetch returned", len(got.pods))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter never returned; the share is wedged")
	}

	<-leaderDone
}

// A waiter still leaves on its own cancellation, immediately.
//
// The other half of the same design, and it must not be lost to the one above:
// detaching the *fetch* from the leader is not the same as making a waiter sit
// through a fetch its own request no longer wants.
func TestAWaiterLeavesOnItsOwnCancellation(t *testing.T) {
	const cluster = "waiter-leaves-test"

	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{})

	go func() {
		_, _ = podLists.list(context.Background(), cluster, "", func(context.Context) ([]corev1.Pod, error) {
			close(started)
			<-release
			return nil, nil
		})
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := podLists.list(ctx, cluster, "", func(context.Context) ([]corev1.Pod, error) {
			return nil, nil
		})
		done <- err
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("a waiter whose own request was canceled returned %v; it should leave "+
				"at once rather than sit through a fetch nobody is waiting for", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter did not leave when its own context was canceled")
	}
}

// And the detachment keeps a deadline, or a fetch that never returns wedges
// the share.
//
// `context.WithoutCancel` strips the parent's deadline as well as its
// cancellation. A fetch left with neither would never return on a hung API
// server, so the entry would stay in `waiting` with `done` never closed and
// every later request for that cluster would block until its own context
// expires — the dashboard wedged until the process restarts.
func TestTheDetachedFetchStillHasADeadline(t *testing.T) {
	const cluster = "deadline-test"

	// A caller with a short deadline: the share must use the tighter of that and
	// its own ceiling, so this returns in milliseconds rather than in 30s.
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := podLists.list(ctx, cluster, "", func(ctx context.Context) ([]corev1.Pod, error) {
		<-ctx.Done() // a hung API server
		return nil, ctx.Err()
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a fetch that never answered returned no error")
	}
	if elapsed > 5*time.Second {
		t.Errorf("the fetch ran for %v against an 80ms deadline; WithoutCancel stripped the "+
			"deadline and nothing put one back, so a hung API server wedges the share",
			elapsed.Round(time.Millisecond))
	}
}

package logs

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// lateTimer is a context whose deadline has passed and whose timer has not
// fired yet: the moment a loaded machine stretches. Its Err is nil until fire is
// called, as a
// real context's is until its timer runs.
type lateTimer struct {
	context.Context
	deadline time.Time
	done     chan struct{}
	once     sync.Once
	mu       sync.Mutex
	err      error
}

func newLateTimer() *lateTimer {
	return &lateTimer{Context: context.Background(), deadline: time.Now().Add(-time.Millisecond), done: make(chan struct{})}
}

func (c *lateTimer) Deadline() (time.Time, bool) { return c.deadline, true }
func (c *lateTimer) Done() <-chan struct{}       { return c.done }
func (c *lateTimer) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// fire is the timer running at last.
func (c *lateTimer) fire() {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = context.DeadlineExceeded
		c.mu.Unlock()
		close(c.done)
	})
}

// The wait reaches its deadline in the gap before the context's timer fires.
// client-go's rate limiter refuses the pod's read there, with ctx.Err() still
// nil. Read as the cluster's answer, the pane would say "client rate limiter Wait
// returned an error" where it should say it stopped waiting. It says the
// ceiling, as the timer-first case does.
func TestAWaitThatReachesItsDeadlineBeforeTheTimerFiresSaysTheDeadline(t *testing.T) {
	p := newStartingPod(t, nil, waitingFor("ContainerCreating", ""))
	s := streamerOn(t, p)
	s.deadline = 80 * time.Millisecond

	ctx := newLateTimer()
	time.AfterFunc(30*time.Millisecond, ctx.fire)
	t.Cleanup(ctx.fire)

	var got []Event
	_, _, err := s.awaitStart(ctx, Request{Cluster: "docker-desktop", Namespace: "n", Pod: "p", Container: "app", Follow: true},
		func(e Event) error { got = append(got, e); return nil })
	if err != errSaid {
		t.Fatalf("the wait answered %v, want the sentence said", err)
	}
	if kinds(got) != "end" {
		t.Fatalf("events %q, want \"end\": %+v", kinds(got), got)
	}
	if !strings.Contains(got[0].Text, "stopped waiting after 80ms") {
		t.Errorf("the wait ended on %q", got[0].Text)
	}
}

// What the cluster answers is still the cluster's answer: an error that is not
// the deadline, before the deadline, does not wait on the context.
func TestAnErrorBeforeTheDeadlineIsNotTheDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	if deadlineCame(ctx, context.Canceled) {
		t.Error("an error an hour before the deadline was read as the deadline")
	}
	if deadlineCame(ctx, nil) {
		t.Error("no error was read as the deadline")
	}
	if deadlineCame(context.Background(), errorString("rate: Wait(n=1) would exceed context deadline")) {
		t.Error("a context with no deadline was read as having reached one")
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

// A stream still talking when the deadline arrives says so. The read that was
// in flight succeeded - the pod kept printing - and the stream must not stop on
// it in silence, with no end event and no word on why it went quiet.
func TestAStreamTalkingThroughItsDeadlineSaysItStopped(t *testing.T) {
	s := New(nil, nil, nil, nil)
	s.deadline = 300 * time.Millisecond
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Millisecond))
	defer cancel()

	var got []Event
	err := s.drive(ctx, strings.NewReader(strings.Repeat("a line\n", 50)), Request{Namespace: "n", Pod: "p"}, nil,
		func(e Event) error { got = append(got, e); return nil })
	if err != nil {
		t.Fatalf("drive answered %v", err)
	}
	if kinds(got) != "end" || !strings.Contains(got[0].Text, "stopped after 300ms") {
		t.Fatalf("events %q, want the deadline's end: %+v", kinds(got), got)
	}
}

// The limiter's sentence about some other deadline - client-go's own
// per-request timeout on a retry - while this context has minutes left is the
// cluster's answer, said at once, never a silent wait for our ceiling.
func TestTheLimitersSentenceFarFromTheDeadlineIsNotTheDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	done := make(chan bool, 1)
	go func() {
		done <- deadlineCame(ctx, errorString("client rate limiter Wait returned an error: rate: Wait(n=1) would exceed context deadline"))
	}()
	select {
	case came := <-done:
		if came {
			t.Error("a refusal an hour before this deadline was read as the deadline")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deadlineCame waited on a context with an hour left")
	}
}

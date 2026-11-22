package actions

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/edit"
)

// The eviction stage always answers, even where the API server does not
// answer an eviction at all. These tests make an eviction slow and assert the
// stage still returns, naming what it did not reach.
//
// # On a clock the test drives, not on the scheduler
//
// A test that sleeps - an eviction taking 100ms against a budget of 50ms -
// depends on a timer firing on time and on the review taking less than the
// slack, and on a loaded CI runner neither holds. Time passes on drainClock,
// and only when an eviction says it does; the contexts' own deadlines are set
// on that clock minutes away, so no real timer fires during a test and nothing
// here depends on how fast the machine is.

// testClock is drainClock, advanced by hand.
type testClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// driveClock makes drainClock the test's own, starting now.
func driveClock(t *testing.T) *testClock {
	t.Helper()
	clock := &testClock{at: time.Now()}
	old := drainClock
	drainClock = clock.now
	t.Cleanup(func() { drainClock = old })
	return clock
}

// slowEvictions makes every eviction take `each` on clock, and answer
// `answer`. It takes no real time.
func (h *harness) slowEvictions(clock *testClock, each time.Duration, answer error) {
	h.dyn.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create, ok := action.(k8stesting.CreateAction)
		if !ok || create.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		clock.advance(each)
		return true, create.GetObject(), answer
	})
}

func shorten(t *testing.T, eviction, budget time.Duration) {
	t.Helper()
	oldEviction, oldBudget := evictionTimeout, drainBudget
	evictionTimeout, drainBudget = eviction, budget
	t.Cleanup(func() { evictionTimeout, drainBudget = oldEviction, oldBudget })
}

func TestAnEvictionStagePastItsBudgetAnswersAndNamesWhatItDidNotReach(t *testing.T) {
	clock := driveClock(t)
	shorten(t, time.Hour, time.Minute)
	h := newHarness(t, nil, node("node-a"),
		pod("prod", "a-1", ownedBy("ReplicaSet", "a")),
		pod("prod", "a-2", ownedBy("ReplicaSet", "a")),
		pod("prod", "a-3", ownedBy("ReplicaSet", "a")),
		pod("prod", "a-4", ownedBy("ReplicaSet", "a")),
	)
	// a-1 is sent at 0s and a-2 at 40s, inside the minute; a-3 would be at 80s.
	h.slowEvictions(clock, 40*time.Second, nil)

	result, err := h.runner.Run(context.Background(), Request{
		Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "node-a", Stage: StageEvict,
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	plan := result.Plan
	if plan.Requested != 2 || plan.NotAttempted != 2 || plan.Failed != 2 {
		t.Fatalf("expected two evicted and two not attempted, got %+v", plan)
	}
	for _, ref := range plan.Evict[2:] {
		if !ref.Failed || !strings.Contains(ref.Outcome, "not attempted — the drain stopped after 1m0s") {
			t.Errorf("%s was not reached and does not say so: %+v", ref.Name, ref)
		}
	}
	if !strings.Contains(result.Message, "2 were not attempted") || strings.Contains(result.Message, "were refused") {
		t.Errorf("the sentence does not tell the not attempted from the refused: %q", result.Message)
	}
}

func TestAnEvictionTheAPIServerDoesNotAnswerIsReportedAsSuch(t *testing.T) {
	clock := driveClock(t)
	shorten(t, time.Minute, time.Hour)
	h := newHarness(t, nil, node("node-a"), pod("prod", "a-1", ownedBy("ReplicaSet", "a")))
	// What client-go returns when the context it was handed runs out.
	h.slowEvictions(clock, 90*time.Second, context.DeadlineExceeded)

	result, err := h.runner.Run(context.Background(), Request{
		Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "node-a", Stage: StageEvict,
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	ref := result.Plan.Evict[0]
	if !ref.Failed || !strings.Contains(ref.Outcome, "did not answer this eviction within 1m0s") {
		t.Errorf("an eviction that timed out reads %+v", ref)
	}
}

// The bound must not relabel a refusal. Reading the eviction's context after
// canceling it would make every PodDisruptionBudget refusal read "the API
// server did not answer this eviction within 15s" - the refusal the stage
// exists to report, misreported.
func TestABudgetsRefusalInADrainIsStillReportedAsTheBudgets(t *testing.T) {
	h := newHarness(t, nil, node("node-a"), pod("prod", "a-1", ownedBy("ReplicaSet", "a")))
	h.evictErr = apierrors.NewTooManyRequests("Cannot evict pod as it would violate the pod's disruption budget.", 0)

	result, err := h.runner.Run(context.Background(), Request{
		Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "node-a", Stage: StageEvict,
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	ref := result.Plan.Evict[0]
	if !ref.Failed || !strings.Contains(ref.Outcome, "PodDisruptionBudget refused") ||
		strings.Contains(ref.Outcome, "did not answer") {
		t.Errorf("a budget's refusal reads %q", ref.Outcome)
	}
	if result.Plan.NotAttempted != 0 || !strings.Contains(result.Message, "1 were refused") {
		t.Errorf("plan %+v, message %q", result.Plan, result.Message)
	}
}

// drainLine is the stage's `drain:` summary line.
func (h *harness) drainLine(t *testing.T) string {
	t.Helper()
	h.logMu.Lock()
	defer h.logMu.Unlock()
	for _, line := range h.logged {
		if strings.HasPrefix(line, "drain: ") {
			return line
		}
	}
	t.Fatalf("no drain: line in %v", h.logged)
	return ""
}

// An eviction the API server did not answer is neither a refusal nor the
// budget working: the summary, the counts and the log line say what it was,
// never "1 were refused … a PodDisruptionBudget refusing one is the budget
// working".
func TestAnUnansweredEvictionIsNotSummarisedAsARefusal(t *testing.T) {
	clock := driveClock(t)
	shorten(t, time.Minute, time.Hour)
	h := newHarness(t, nil, node("node-a"), pod("prod", "a-1", ownedBy("ReplicaSet", "a")))
	h.slowEvictions(clock, 90*time.Second, context.DeadlineExceeded)

	result, err := h.runner.Run(context.Background(), Request{
		Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "node-a", Stage: StageEvict,
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	plan := result.Plan
	if plan.Unanswered != 1 || plan.NotAttempted != 0 || plan.Failed != 1 || !plan.Evict[0].Unanswered {
		t.Errorf("an unanswered eviction is counted as %+v", plan)
	}
	if strings.Contains(result.Message, "refused") || strings.Contains(result.Message, "budget working") ||
		!strings.Contains(result.Message, "1 were sent and not answered in time") {
		t.Errorf("the summary does not say the eviction went unanswered: %q", result.Message)
	}
	if line := h.drainLine(t); !strings.Contains(line, " evicted=0 refused=0 unanswered=1 not_attempted=0 ") {
		t.Errorf("drain line = %q", line)
	}
}

// The stage's budget running out while an eviction waits is the budget's
// clock, not the eviction's: "did not answer within 1h" over a cut at 1m
// names the wrong one. And a cut is never a refusal: never "the cluster
// refused the change", however loaded the runner.
func TestAnEvictionCutByTheStageBudgetNamesTheBudget(t *testing.T) {
	clock := driveClock(t)
	shorten(t, time.Hour, time.Minute)
	h := newHarness(t, nil, node("node-a"), pod("prod", "a-1", ownedBy("ReplicaSet", "a")))
	// What client-go answers when the context it was handed runs out.
	h.slowEvictions(clock, 90*time.Second, context.DeadlineExceeded)

	result, err := h.runner.Run(context.Background(), Request{
		Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "node-a", Stage: StageEvict,
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	ref := result.Plan.Evict[0]
	if !ref.Unanswered || !strings.Contains(ref.Outcome, "when the drain's 1m0s ran out") ||
		strings.Contains(ref.Outcome, "within 1h") {
		t.Errorf("an eviction cut by the stage's budget reads %+v", ref)
	}
	if strings.Contains(result.Message, "refused") {
		t.Errorf("the summary calls a cut a refusal: %q", result.Message)
	}
	if line := h.drainLine(t); !strings.Contains(line, " refused=0 unanswered=1 not_attempted=0 ") {
		t.Errorf("drain line = %q", line)
	}
}

// The error decides what happened, and the clocks only which one ran out: a
// DeadlineExceeded that none of the drain's clocks explains - the client's own
// timeout, say - was sent and not answered, and is never a refusal. Putting
// the clocks in front of the error would read it "the cluster refused the
// change" and count it under refused=.
func TestAnUnexplainedDeadlineIsUnansweredNotRefused(t *testing.T) {
	clock := driveClock(t)
	shorten(t, time.Hour, time.Hour)
	h := newHarness(t, nil, node("node-a"), pod("prod", "a-1", ownedBy("ReplicaSet", "a")))
	// No time passes, so no clock of the drain's runs out.
	h.slowEvictions(clock, 0, context.DeadlineExceeded)

	result, err := h.runner.Run(context.Background(), Request{
		Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "node-a", Stage: StageEvict,
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	ref := result.Plan.Evict[0]
	if !ref.Unanswered || ref.NotAttempted || !strings.Contains(ref.Outcome, "did not answer this eviction in time (") {
		t.Errorf("an unexplained deadline reads %+v", ref)
	}
	if strings.Contains(result.Message, "refused") {
		t.Errorf("the summary calls an unanswered eviction a refusal: %q", result.Message)
	}
	if line := h.drainLine(t); !strings.Contains(line, " refused=0 unanswered=1 not_attempted=0 ") {
		t.Errorf("drain line = %q", line)
	}
}

// An eviction whose time ran out while the console was still asking whether
// it may be sent was never sent. The review is asked on its own timeout, so it
// answers after the eviction's deadline; client-go then fails the request
// without sending it, with the DeadlineExceeded an unanswered one gets. It is
// "not attempted", not "the API server did not answer".
//
// On the real clock, because what holds the eviction back is edit.Evict,
// which reads time.Now. The sleep is a lower bound and nothing more: however
// late it wakes, the 10ms deadline set before the review began has passed,
// and edit.Evict reads that deadline rather than waiting for the context's
// timer to fire.
func TestAnEvictionCutDuringItsReviewIsNotAttemptedAndNothingIsSent(t *testing.T) {
	shorten(t, 10*time.Millisecond, time.Hour)
	h := newHarness(t, nil, node("node-a"), pod("prod", "a-1", ownedBy("ReplicaSet", "a")))
	h.reviews.Answer = func(q accesstest.Question) (bool, error) {
		if q.Attributes.Subresource == "eviction" {
			time.Sleep(30 * time.Millisecond)
		}
		return true, nil
	}
	// client-go refusing a request whose context has run out. Reached only
	// when the eviction is handed to the client, which an expired budget prevents.
	h.evictErr = context.DeadlineExceeded

	result, err := h.runner.Run(context.Background(), Request{
		Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "node-a", Stage: StageEvict,
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	ref := result.Plan.Evict[0]
	if !ref.NotAttempted || ref.Unanswered || !strings.Contains(ref.Outcome, "this eviction's 10ms ran out") ||
		!strings.Contains(ref.Outcome, "nothing was sent") ||
		strings.Contains(ref.Outcome, "did not answer") {
		t.Errorf("an eviction cut during its review reads %+v", ref)
	}
	for _, write := range h.writes() {
		if write.Subresource == "eviction" {
			t.Errorf("an eviction whose time had run out was handed to the client: %+v", write)
		}
	}
	if line := h.drainLine(t); !strings.Contains(line, " refused=0 unanswered=0 not_attempted=1 ") {
		t.Errorf("drain line = %q", line)
	}
}

// What counts as "not sent" and what counts as an answer, including the
// server profile's refusal of a review it could not complete, which these
// tests cannot reach without a signed-in identity and an audit log.
func TestWhatCountsAsNotSentAndWhatAsAnAnswer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		sent     bool
		answered bool
	}{
		{"a budget's 429", apierrors.NewTooManyRequests("budget", 0), true, true},
		{"a context that ran out on the wire", context.DeadlineExceeded, true, false},
		{"a canceled request", context.Canceled, true, false},
		{"the door holding back an expired context", fmt.Errorf("%w: %w", edit.ErrNotSent, context.DeadlineExceeded), false, false},
		{"a review that could not be made", edit.Refuse(edit.RefusedUnreviewable, "context deadline exceeded"), false, false},
		{"a record that could not be written", edit.Refuse(edit.RefusedUnrecorded, "no"), false, false},
		{"a denial", edit.Refuse(edit.RefusedByAccess, "no"), true, true},
	} {
		if got := !notSent(tc.err); got != tc.sent {
			t.Errorf("%s: sent = %v, want %v", tc.name, got, tc.sent)
		}
		if got := answered(tc.err); got != tc.answered {
			t.Errorf("%s: answered = %v, want %v", tc.name, got, tc.answered)
		}
	}
}

package actions

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/edit"
)

// An eviction that never left the console, as the transport reports it
// (edit.NotSent). The fake dynamic client here has
// no transport, so the reactor answers with what edit.Evict makes of one -
// edit/evictwire_test.go is where that is established through a real client.

// throttled is client-go's wrapping of the rate limiter's refusal: matching
// neither context error, it would read as the API server's answer.
var throttled = errors.New("client rate limiter Wait returned an error: rate: Wait(n=1) would exceed context deadline")

func heldAt(at edit.HeldAt, err error) error { return &edit.NotSent{At: at, Err: err} }

// Through the whole drain: an eviction the limiter held is
// not attempted and not counted as a refusal.
func TestAnEvictionTheRateLimiterHeldIsNotAttemptedNotRefused(t *testing.T) {
	h := newHarness(t, nil, node("node-a"), pod("prod", "a-1", ownedBy("ReplicaSet", "a")))
	h.evictErr = heldAt(edit.HeldBeforeTransport, throttled)

	result, err := h.runner.Run(context.Background(), Request{
		Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "node-a", Stage: StageEvict,
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	ref := result.Plan.Evict[0]
	if !ref.NotAttempted || ref.Unanswered || !strings.Contains(ref.Outcome, "request limit") ||
		!strings.Contains(ref.Outcome, "nothing was sent") || strings.Contains(ref.Outcome, "still checking") ||
		strings.Contains(ref.Outcome, "refused") {
		t.Errorf("an eviction the rate limiter held reads %+v", ref)
	}
	if line := h.drainLine(t); !strings.Contains(line, " refused=0 unanswered=0 not_attempted=1 ") {
		t.Errorf("drain line = %q", line)
	}
}

// And one for which no connection was made, with no clock of the drain's run
// out: a dial refused.
func TestAnEvictionWithNoConnectionIsNotAttempted(t *testing.T) {
	h := newHarness(t, nil, node("node-a"), pod("prod", "a-1", ownedBy("ReplicaSet", "a")))
	h.evictErr = heldAt(edit.HeldBeforeConnection, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED})

	result, err := h.runner.Run(context.Background(), Request{
		Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "node-a", Stage: StageEvict,
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	ref := result.Plan.Evict[0]
	if !ref.NotAttempted || !strings.Contains(ref.Outcome, "no connection to the API server") ||
		!strings.Contains(ref.Outcome, "connection refused") || strings.Contains(ref.Outcome, "still checking") {
		t.Errorf("an eviction with no connection reads %+v", ref)
	}
	if line := h.drainLine(t); !strings.Contains(line, " refused=0 unanswered=0 not_attempted=1 ") {
		t.Errorf("drain line = %q", line)
	}
}

// Each place an eviction can be held has its own sentence. "While the console
// was still checking whether it may evict this pod" is true of the review, and
// not of a record that could not be written.
func TestEachPlaceAnEvictionIsHeldSaysWhere(t *testing.T) {
	for _, tc := range []struct {
		name, why string
		err       error
		want      string
		not       string
	}{
		{"cut after the review", "this eviction's 15s ran out", heldAt(edit.HeldAfterReview, context.DeadlineExceeded),
			"this eviction's 15s ran out while the console was still checking", ""},
		{"at the rate limit, no clock run out", "", heldAt(edit.HeldBeforeTransport, throttled),
			"would have held it past its deadline", "still checking"},
		{"at the rate limit, a clock run out", "the drain's 1m0s ran out", heldAt(edit.HeldBeforeTransport, context.DeadlineExceeded),
			"the drain's 1m0s ran out while it waited its turn", "still checking"},
		{"before a connection", "this eviction's 15s ran out", heldAt(edit.HeldBeforeConnection, context.DeadlineExceeded),
			"before a connection to the API server was made", "still checking"},
		{"a record not written", "this eviction's 15s ran out", edit.Refuse(edit.RefusedUnrecorded, "no record"),
			"its audit record could not be written first", "still checking"},
		{"a review not made", "this eviction's 15s ran out", edit.Refuse(edit.RefusedUnreviewable, "no review"),
			"while the console was still checking", ""},
	} {
		got := notSentOutcome(tc.err, tc.why)
		if !strings.Contains(got, tc.want) || !strings.Contains(got, "nothing was sent") ||
			(tc.not != "" && strings.Contains(got, tc.not)) {
			t.Errorf("%s: %q", tc.name, got)
		}
	}
	// With no clock run out, the console's own refusal says it better.
	if got := notSentOutcome(edit.Refuse(edit.RefusedUnrecorded, "no record"), ""); got != "" {
		t.Errorf("a record not written, no clock run out: %q, want the refusal's own sentence", got)
	}
}

func TestWhatTheTransportHeldIsNotSentAndNotAnAnswer(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"the limiter's refusal", heldAt(edit.HeldBeforeTransport, throttled)},
		{"the limiter's bare deadline", heldAt(edit.HeldBeforeTransport, context.DeadlineExceeded)},
		{"no connection", heldAt(edit.HeldBeforeConnection, context.DeadlineExceeded)},
		{"as the Evict button refuses it", edit.RefuseFrom(heldAt(edit.HeldBeforeTransport, throttled),
			edit.RefusedNotSent, "not sent")},
	} {
		if !notSent(tc.err) || answered(tc.err) {
			t.Errorf("%s: notSent=%v answered=%v", tc.name, notSent(tc.err), answered(tc.err))
		}
	}
	// The limiter's refusal on its own - what the drain was handed before the
	// transport said anything - is an answer, which is the defect.
	if !answered(throttled) {
		t.Error("the bare limiter error no longer reads as an answer; this test's premise is stale")
	}
}

// The single Evict button, its context run out during the review - a browser
// that went away. It must not say "the cluster refused the change: not
// sent…", a refusal and a 403 about a request the cluster never saw.
func TestTheEvictButtonSaysAnUnsentEvictionWasNotSent(t *testing.T) {
	h := newHarness(t, nil, pod("prod", "api-1"))
	h.reviews.Answer = func(q accesstest.Question) (bool, error) {
		if q.Attributes.Subresource == "eviction" {
			time.Sleep(30 * time.Millisecond)
		}
		return true, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := h.runner.Run(ctx, Request{Target: target("pods", "prod", "api-1"), Verb: Evict, Confirm: ConfirmToken})
	out := refusal(t, err)
	if out.Kind != edit.RefusedNotSent || !errors.Is(err, edit.ErrNotSent) {
		t.Fatalf("an eviction cut during its review answered %v (kind %v), want RefusedNotSent", err, out.Kind)
	}
	if !strings.Contains(out.Reason, "was not sent") || !strings.Contains(out.Reason, "Nothing was removed") ||
		strings.Contains(out.Reason, "cluster refused") {
		t.Errorf("the Evict button says %q", out.Reason)
	}
	for _, write := range h.writes() {
		if write.Subresource == "eviction" {
			t.Errorf("an eviction whose time had run out was sent: %+v", write)
		}
	}
}

// The same button, for each place the transport can hold it.
func TestTheEvictButtonSaysWhereAnEvictionWasHeld(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{heldAt(edit.HeldBeforeTransport, throttled), "request limit"},
		{heldAt(edit.HeldBeforeConnection, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}),
			"no connection to the API server"},
	} {
		h := newHarness(t, nil, pod("prod", "api-1"))
		h.evictErr = tc.err
		_, err := h.runner.Run(context.Background(), Request{Target: target("pods", "prod", "api-1"), Verb: Evict, Confirm: ConfirmToken})
		out := refusal(t, err)
		if out.Kind != edit.RefusedNotSent || !strings.Contains(out.Reason, tc.want) || strings.Contains(out.Reason, "cluster refused") {
			t.Errorf("held %v: %v says %q", tc.err, out.Kind, out.Reason)
		}
	}
}

// dropped is an eviction written to the API server whose connection ended
// before the answer, as edit.Evict reports it from the transport's record.
func dropped() error {
	return &edit.Unanswered{Err: &url.Error{Op: "Post", URL: "https://prod.example/api/v1/namespaces/prod/pods/a-1/eviction", Err: io.EOF}}
}

// A sent eviction whose answer was lost is not the API server's answer - not
// "the cluster refused the change: Post ...: EOF", counted under refused=. It
// is sent and not answered.
func TestAnEvictionWhoseConnectionDroppedIsUnansweredInTheDrain(t *testing.T) {
	h := newHarness(t, nil, node("node-a"), pod("prod", "a-1", ownedBy("ReplicaSet", "a")))
	h.evictErr = dropped()

	result, err := h.runner.Run(context.Background(), Request{
		Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "node-a", Stage: StageEvict,
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	ref := result.Plan.Evict[0]
	if !ref.Unanswered || ref.NotAttempted || !strings.Contains(ref.Outcome, "ended before it answered") ||
		!strings.Contains(ref.Outcome, "EOF") || !strings.Contains(ref.Outcome, "is not known") ||
		strings.Contains(ref.Outcome, "refused") || strings.Contains(ref.Outcome, "no answer came back") {
		t.Errorf("an eviction whose connection dropped reads %+v", ref)
	}
	if line := h.drainLine(t); !strings.Contains(line, " refused=0 unanswered=1 not_attempted=0 ") {
		t.Errorf("drain line = %q", line)
	}
}

// And the Evict button says the same, never "the cluster refused the change".
func TestTheEvictButtonSaysADroppedEvictionWasNotAnswered(t *testing.T) {
	h := newHarness(t, nil, pod("prod", "api-1"))
	h.evictErr = dropped()

	_, err := h.runner.Run(context.Background(), Request{Target: target("pods", "prod", "api-1"), Verb: Evict, Confirm: ConfirmToken})
	out := refusal(t, err)
	if out.Kind != edit.RefusedUnanswered || !errors.Is(err, edit.ErrUnanswered) ||
		!strings.Contains(out.Reason, "no answer came back") || !strings.Contains(out.Reason, "is not known") ||
		strings.Contains(out.Reason, "cluster refused") {
		t.Errorf("a dropped eviction: %v says %q", out.Kind, out.Reason)
	}
}

func TestWhatWasSentAndNotAnsweredIsNotAnAnswer(t *testing.T) {
	if answered(dropped()) || notSent(dropped()) {
		t.Errorf("a dropped connection: answered=%v notSent=%v, want neither", answered(dropped()), notSent(dropped()))
	}
}

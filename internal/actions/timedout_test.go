package actions

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/edit"
)

// An eviction the API server answered by timing out: a 504 Timeout, or a 500
// ServerTimeout once client-go stopped retrying. It is neither "the cluster
// refused the change" nor counted under refused=: the answer says the eviction
// may still happen. The fake client here has no
// transport, so these are the API server's own errors as client-go decodes
// them, classified by edit.Evict on their way back; edit/timedout_test.go
// sends the same answers from an httptest server through a real client.

type timeoutAnswer struct {
	name string
	err  error
	// what and says are the sentence's account of the answer: "timed out
	// on" only where the answer says so, and the answer named no further
	// than it is known.
	what, says string
}

func timeoutAnswers() []timeoutAnswer {
	return []timeoutAnswer{
		{"504 Timeout", apierrors.NewTimeoutError("request did not complete within requested timeout", 0),
			"timed out on", "504 Timeout"},
		{"500 ServerTimeout", apierrors.NewServerTimeout(schema.GroupResource{Resource: "pods"}, "create", 0),
			"timed out on", "500 ServerTimeout"},
		// The budget's refusal of client-go's retry, after a first attempt
		// answered 500 with Retry-After: that attempt may have evicted the
		// pod, and what the 500 was is not known.
		{"a 500 with Retry-After, then the budget's 429", &edit.TimedOut{Status: 500,
			Err: apierrors.NewTooManyRequests("Cannot evict pod as it would violate the pod's disruption budget.", 0)},
			"did not finish", "500 Internal Server Error with Retry-After"},
		// A proxy's 502 with Retry-After, then the budget's 429.
		{"a 502 with Retry-After, then the budget's 429", &edit.TimedOut{Status: 502,
			Err: apierrors.NewTooManyRequests("Cannot evict pod as it would violate the pod's disruption budget.", 0)},
			"did not finish", "502 Bad Gateway with Retry-After"},
	}
}

func TestAnEvictionTheAPIServerTimedOutOnIsNotARefusalInTheDrain(t *testing.T) {
	for _, tc := range timeoutAnswers() {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil, node("node-a"), pod("prod", "a-1", ownedBy("ReplicaSet", "a")))
			h.evictErr = tc.err

			result, err := h.runner.Run(context.Background(), Request{
				Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "node-a", Stage: StageEvict,
			})
			if err != nil {
				t.Fatalf("drain: %v", err)
			}
			ref := result.Plan.Evict[0]
			if !ref.Failed || !ref.TimedOut || ref.Unanswered || ref.NotAttempted ||
				!strings.Contains(ref.Outcome, tc.what) || !strings.Contains(ref.Outcome, tc.says) ||
				!strings.Contains(ref.Outcome, "may still happen") || strings.Contains(ref.Outcome, "refused") ||
				strings.Contains(ref.Outcome, "PodDisruptionBudget") {
				t.Errorf("an eviction the API server timed out on reads %+v", ref)
			}
			if got := result.Plan.Refused(); got != 0 {
				t.Errorf("Refused() = %d, want 0", got)
			}
			if line := h.drainLine(t); !strings.Contains(line, " refused=0 unanswered=0 not_attempted=0 timed_out=1") {
				t.Errorf("drain line = %q", line)
			}
			if !strings.Contains(result.Message, "1 were answered by the API server timing out") ||
				strings.Contains(result.Message, "refused") {
				t.Errorf("the drain says %q", result.Message)
			}
		})
	}
}

// The Evict button says the same: not known, never "the cluster refused the
// change" and never the budget's refusal.
func TestTheEvictButtonSaysATimedOutEvictionMayStillHappen(t *testing.T) {
	for _, tc := range timeoutAnswers() {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil, pod("prod", "api-1"))
			h.evictErr = tc.err

			_, err := h.runner.Run(context.Background(), Request{Target: target("pods", "prod", "api-1"), Verb: Evict, Confirm: ConfirmToken})
			out := refusal(t, err)
			if out.Kind != edit.RefusedUnanswered || !errors.Is(err, edit.ErrTimedOut) ||
				!strings.Contains(out.Reason, tc.what) || !strings.Contains(out.Reason, tc.says) ||
				!strings.Contains(out.Reason, "may still happen") || strings.Contains(out.Reason, "refused") ||
				strings.Contains(out.Reason, "PodDisruptionBudget") {
				t.Errorf("a timed-out eviction: %v says %q", out.Kind, out.Reason)
			}
		})
	}
}

func TestATimedOutAnswerIsNotARefusalAndWasSent(t *testing.T) {
	for _, tc := range timeoutAnswers() {
		classified := &edit.TimedOut{Status: 504, Err: tc.err}
		if answered(classified) || notSent(classified) {
			t.Errorf("%s: answered=%v notSent=%v, want neither", tc.name, answered(classified), notSent(classified))
		}
	}
}

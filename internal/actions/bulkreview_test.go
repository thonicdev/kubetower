package actions

import (
	"context"
	"testing"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/edit"
)

// TestReviewBulkMarksOnlyTheRowsThePersonMayNotTouch: the preview of a set asks
// each row the single door's questions, so a row in a namespace the person has
// no rights in is refused before any token exists, and the others are not.
func TestReviewBulkMarksOnlyTheRowsThePersonMayNotTouch(t *testing.T) {
	h := newHarness(t, func(string) bool { return false }, pod("team-a", "a-0"), pod("team-b", "b-0"))
	h.reviews.Answer = func(q accesstest.Question) (bool, error) {
		return q.Attributes.Namespace == "team-a", nil
	}
	reviews := h.runner.ReviewBulk(context.Background(), BulkRequest{
		Cluster: "prod", Kind: "pods", Verb: Delete, Targets: targets("team-a/a-0", "team-b/b-0"),
	})
	if len(reviews) != 2 {
		t.Fatalf("got %d reviews, want 2", len(reviews))
	}
	if reviews[0].Err != nil {
		t.Errorf("team-a/a-0 refused: %v", reviews[0].Err)
	}
	if refusal, ok := edit.AsRefusal(reviews[1].Err); !ok || refusal.Kind != edit.RefusedByAccess {
		t.Errorf("team-b/b-0: %v, want the access refusal", reviews[1].Err)
	}
	h.noWrites()
}

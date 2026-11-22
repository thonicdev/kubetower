package web

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/edit"
)

// A selection is previewed with the single door's review: a row the person may
// not touch is marked refused and carries the reason, and a set in which every
// row is refused is refused whole, so no token is minted for it.
func TestABulkPreviewMarksTheRowsThePersonMayNotTouch(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{fakePodGVR: "PodList"},
		fakePod("team-a", "a-0"), fakePod("team-b", "b-0"))
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		return q.Attributes.Namespace == "team-a", nil
	}}
	writer := edit.New(edit.Options{
		Resolve: fakeActsResolver{dyn: dyn},
		Logf:    func(string, ...any) {},
		Access:  reviews.Gate(),
	})
	acts := apiActs{s: &Server{act: actions.New(actions.Options{Writer: writer})}}
	ctx := context.Background()

	facts, err := acts.PreviewBulk(ctx, "prod", api.BulkRequest{
		Verb: "delete", Kind: "pods",
		Targets: []api.PlanTarget{{Namespace: "team-a", Name: "a-0"}, {Namespace: "team-b", Name: "b-0"}},
	})
	if err != nil {
		t.Fatalf("a set with one row the person may touch: %v", err)
	}
	if facts.Targets[0].Refused || !facts.Targets[1].Refused || facts.Targets[1].Note == "" {
		t.Errorf("targets = %+v, want only team-b/b-0 refused, with its reason", facts.Targets)
	}

	_, err = acts.PreviewBulk(ctx, "prod", api.BulkRequest{
		Verb: "delete", Kind: "pods",
		Targets: []api.PlanTarget{{Namespace: "team-b", Name: "b-0"}},
	})
	if !errors.Is(err, api.ErrRefused) {
		t.Errorf("a set of refused rows answered %v, want the refusal", err)
	}
}

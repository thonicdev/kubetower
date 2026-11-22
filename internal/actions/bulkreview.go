package actions

import (
	"context"
	"sync"

	"github.com/thonicdev/kubetower/internal/edit"
	"github.com/thonicdev/kubetower/safego"
)

// BulkReview is the review's answer for one target of a selection, asked
// before a bulk preview mints its token. Err is nil for a target the person
// may act on and the refusal otherwise.
type BulkReview struct {
	Target BulkTarget
	Err    error
}

// ReviewBulk asks, for every target of the request and in its order, the
// questions the single door's preview asks (reviewFirst), so a selection is
// previewed with the same review a single object gets. It reads nothing of
// the objects beyond what that review needs.
func (r *Runner) ReviewBulk(ctx context.Context, req BulkRequest) []BulkReview {
	out := make([]BulkReview, len(req.Targets))
	var wg sync.WaitGroup
	slots := make(chan struct{}, BulkConcurrency)
	for i, target := range req.Targets {
		out[i].Target = target
		wg.Add(1)
		slots <- struct{}{}
		safego.Go("bulk preview review", func() error {
			out[i].Err = r.reviewFirst(ctx, req.Verb, edit.Target{
				Cluster: req.Cluster, Kind: req.Kind,
				Namespace: target.Namespace, Name: target.Name,
			})
			return nil
		}, func(err error) {
			// A panic is this row's refusal, never a row the person may act on.
			if err != nil {
				out[i].Err = edit.Refuse(edit.RefusedInput, "this target could not be reviewed")
			}
			<-slots
			wg.Done()
		})
	}
	wg.Wait()
	return out
}

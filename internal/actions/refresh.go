package actions

import (
	"context"

	"github.com/thonicdev/kubetower/internal/edit"
)

// The one annotation an extension may ask the console to set, and the one kind
// it may set it on. Argo CD watches the annotation: `normal` makes it compare
// the Application against git again at once instead of at its next
// reconciliation, and removes the annotation when it is done.
const (
	refreshAnnotation = "argocd.argoproj.io/refresh"
	refreshValue      = "normal"
	// ApplicationKind is the kind as the console's catalogue spells a custom
	// resource: plural.group.
	ApplicationKind = "applications.argoproj.io"
)

// RefreshApplication asks Argo CD to compare one Application again.
//
// Narrow by construction: it sets the single annotation above on one object of
// one kind and nothing else, so the door cannot be turned into a general patch.
// It goes through the same guarded path as every verb here: the read-only
// guardrail (Locate), the person's access review for `patch` on the
// Application, a dry run, then the write, each recorded.
//
// The object is read first and its metadata.uid and metadata.resourceVersion
// ride in the patch, so the API server refuses the write, as a conflict, when
// the Application was changed or recreated after it was read.
func (r *Runner) RefreshApplication(ctx context.Context, cluster, namespace, name string) error {
	target := Target{Cluster: cluster, Kind: ApplicationKind, Namespace: namespace, Name: name}
	where, err := r.writer.Locate(ctx, target)
	if err != nil {
		return err
	}
	object, err := edit.Live(ctx, where, target)
	if err != nil {
		return edit.WrapGet(err, where.Kind)
	}
	raw, err := marshal(map[string]any{"metadata": map[string]any{
		"uid":             string(object.GetUID()),
		"resourceVersion": object.GetResourceVersion(),
		"annotations":     map[string]any{refreshAnnotation: refreshValue},
	}})
	if err != nil {
		return err
	}
	if _, _, err := edit.Send(ctx, where, target, raw, true); err != nil {
		return err
	}
	_, decision, err := edit.Send(ctx, where, target, raw, false)
	if err != nil {
		return err
	}
	r.writer.Logf(ctx, "write: cluster=%q kind=%s namespace=%s name=%s action=refresh decision=%s outcome=applied",
		where.Cluster, where.Resource.Resource, namespace, name, decision)
	return nil
}

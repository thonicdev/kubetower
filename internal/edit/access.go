package edit

import (
	"context"

	"github.com/thonicdev/kubetower/internal/identity"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/internal/access"
)

// What internal/actions reaches this package through. Its verbs are writes,
// and every write resolves the context name, checks `KT_READONLY_CLUSTERS`
// against the resolved name (an EKS ARN reaching the URL as
// `...cluster--example-prod-eks` matches nothing there) and refuses in this
// package's vocabulary. A second copy of that guardrail would be a second
// chance to stop matching, on exactly the clusters it protects. Each function
// is the unexported one of the same name.

// Locate resolves a target and applies the read-only guardrail. Every action in
// `internal/actions` goes through it before it touches anything.
func (w *Writer) Locate(ctx context.Context, target Target) (Location, error) {
	return w.locate(ctx, target)
}

// Logf writes the one greppable line that says a cluster was changed, so a
// `write:` grep finds every write.
//
// It ends with who asked, read from ctx (`who="alice@example.com" via=sso`,
// `who=- via=unattributed`): the API server's audit sees only this console's
// ServiceAccount. The name is an argument, never part of the format, so a
// `%` in it cannot rewrite the line, and the rendered line has every control
// character escaped (identity.Line), so no argument can forge the next.
func (w *Writer) Logf(ctx context.Context, format string, args ...any) {
	w.logf("%s", identity.Line(ctx, format, args...))
}

// Live fetches the object a write is about to be built on, and the interface to
// send it through.
//
// The object only: a caller holding the dynamic.ResourceInterface could
// write through it without the review. Every write goes through Send,
// Create, Delete or Evict, each of which asks internal/access first.
func Live(ctx context.Context, where Location, target Target) (*unstructured.Unstructured, error) {
	return live(ctx, where, target)
}

// Send patches, as a dry run or for real; both are reviewed first. The dry
// run runs admission and quota and changes nothing, so a patch a webhook
// will refuse is refused before anything is committed.
func Send(ctx context.Context, where Location, target Target, patch []byte, dryRun bool) (*unstructured.Unstructured, access.Decision, error) {
	return send(ctx, where, target, patch, dryRun)
}

// Refuse builds a refusal in this package's vocabulary.
func Refuse(kind RefusalKind, format string, args ...any) error {
	return refuse(kind, format, args...)
}

// RefuseFrom is Refuse, wrapping the error the refusal was made from so that
// errors.Is still finds it - ErrNotSent, a context's error.
func RefuseFrom(cause error, kind RefusalKind, format string, args ...any) error {
	return refuseFrom(cause, kind, format, args...)
}

// APIRefusal turns what the API server said into what the panel says.
func APIRefusal(err error) error { return apiRefusal(err) }

// WrapGet keeps a "not found" readable rather than letting it arrive as an
// unexplained failure of an action.
func WrapGet(err error, kind string) error { return wrapGet(err, kind) }

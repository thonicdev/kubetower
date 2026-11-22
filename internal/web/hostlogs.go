package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"k8s.io/apimachinery/pkg/api/validate/content"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
)

// MayReadLogs is the review of `get pods/log` in one namespace for the person
// behind r, through the same gate as the console's own log stream: under the
// server profile a review that could not be made refuses. It reads nothing from
// the cluster, so it is safe to hand an extension.
//
// An empty namespace is refused rather than asked about every namespace, and so
// is one that is not a path segment.
func (h host) MayReadLogs(r *http.Request, name, namespace string) error {
	if namespace == "" {
		return errors.New("a log is read in one namespace; this asks about none")
	}
	if problems := content.IsPathSegmentName(namespace); len(problems) != 0 {
		return fmt.Errorf("the namespace %q is not one path segment (%s), so nothing is asked",
			namespace, strings.Join(problems, "; "))
	}
	client, err := h.client(r, name)
	if err != nil {
		return err
	}
	err = h.s.gate.Check(r.Context(), client.Context, access.Attributes{
		Verb: "get", Version: "v1", Resource: "pods", Subresource: "log", Namespace: namespace})
	var refused *access.Refused
	if errors.As(err, &refused) {
		if refused.Answer.Decision == access.Denied {
			return fmt.Errorf("%w: %w", extension.ErrLogsDenied, refused)
		}
		return fmt.Errorf("%w: %w", extension.ErrLogsUnreviewed, refused)
	}
	return err
}

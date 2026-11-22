package web

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/edit"
)

// RefreshApplication is the console's one write door handed to an extension:
// the annotation that makes Argo CD compare an Application again, set through
// actions.Runner, so it is reviewed for the person, dry-run, recorded and
// logged like every other write. Answered only to a mounted extension.
func (h host) RefreshApplication(r *http.Request, name, namespace, application string) error {
	if h.ext == "" || h.s == nil || h.s.act == nil {
		return errors.New("RefreshApplication is answered only to a mounted extension")
	}
	if namespace == "" || application == "" {
		return errors.New("a refresh names one Application in one namespace")
	}
	if h.s.clusters != nil {
		if _, ok := h.s.clusters.Resolve(name); !ok {
			return fmt.Errorf("%w: %q is not a context in the kubeconfig", extension.ErrUnknownCluster, name)
		}
	}
	err := h.s.act.RefreshApplication(r.Context(), name, namespace, application)
	if refusal, ok := edit.AsRefusal(err); ok {
		switch refusal.Kind {
		case edit.RefusedByAccess:
			return fmt.Errorf("%w: %s", extension.ErrDenied, refusal.Reason)
		case edit.RefusedConflict:
			return fmt.Errorf("%w: %s", extension.ErrChanged, refusal.Reason)
		case edit.RefusedUnreviewable:
			return fmt.Errorf("%w: %s", extension.ErrUnreviewable, refusal.Reason)
		}
	}
	return err
}

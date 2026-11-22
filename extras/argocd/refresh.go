package argocd

import (
	"errors"
	"net/http"

	"github.com/thonicdev/kubetower/extension"
)

// Refresh: ask Argo CD to compare one Application again, now, instead of at
// its next reconciliation. It sets one annotation on one Application through
// Host.RefreshApplication, the console's reviewed write door; nothing here
// writes. Held to the confirmation word first (Host.ConfirmToken), as every
// guarded write is.

// CodeNotConfirmed is a refresh asked for without the confirmation word.
const CodeNotConfirmed = "not_confirmed"

// CodeRefreshFailed is a refresh the console could not make, other than the
// answers above it.
const CodeRefreshFailed = "refresh_failed"

// CodeRefreshDenied is a refresh the cluster does not allow this person, and
// CodeRefreshUnreviewed one the console could not ask about.
const (
	CodeRefreshDenied     = "refresh_denied"
	CodeRefreshUnreviewed = "refresh_unreviewed"
	// CodeRefreshChanged is an Application that changed after the console read it.
	CodeRefreshChanged = "refresh_changed"
)

func (d Delivery) refresh(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("confirm") != h.ConfirmToken(r) {
			writeProblem(w, http.StatusBadRequest, CodeNotConfirmed, "A refresh is made only once it is confirmed.", nil)
			return
		}
		cluster, namespace, name := r.PathValue("cluster"), r.PathValue("namespace"), r.PathValue("name")
		err := h.RefreshApplication(r, cluster, namespace, name)
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, map[string]bool{"refreshed": true})
		case unknownCluster(w, cluster, err):
		case errors.Is(err, extension.ErrDenied):
			writeProblem(w, http.StatusForbidden, CodeRefreshDenied, "The cluster does not allow you to refresh this Application.", err)
		case errors.Is(err, extension.ErrUnreviewable):
			writeProblem(w, http.StatusServiceUnavailable, CodeRefreshUnreviewed, "The console could not check whether you may refresh this Application.", err)
		case errors.Is(err, extension.ErrChanged):
			writeProblem(w, http.StatusConflict, CodeRefreshChanged, "The Application changed in the meantime. Read it again, then refresh.", err)
		default:
			writeProblem(w, http.StatusBadGateway, CodeRefreshFailed, "The refresh could not be made.", err)
		}
	})
}

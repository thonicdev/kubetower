package argocd

import (
	"errors"
	"net/http"

	"github.com/thonicdev/kubetower/extension"
)

// Suggest is whether the cluster serves what the page reads, for the banner
// that offers to turn it on (extension.Suggester): the Argo CD Application
// CRD, or Flux CD's Kustomization or HelmRelease CRDs.
//
// From the catalog alone, no object is listed. A kind no CRD answers to, or
// that no version serves, is absent and says nothing; a catalog or a lookup
// that failed is an error, never "absent" - the person sees no banner either
// way, and the log says which. A kind found suggests, whatever the others did.
func (Delivery) Suggest(r *http.Request, h extension.Host, cluster string) (string, error) {
	registry, err := h.Definitions(r, cluster)
	if err != nil {
		return "", err
	}
	var argo, flux bool
	var unreadable error
	for _, kind := range []string{ApplicationKind, KustomizationKind, HelmReleaseKind} {
		_, err := registry.Lookup(r.Context(), kind)
		switch {
		case err == nil:
			if kind == ApplicationKind {
				argo = true
			} else {
				flux = true
			}
		case errors.Is(err, extension.ErrUnknownKind) || errors.Is(err, extension.ErrNotServed):
		default:
			unreadable = err
		}
	}
	switch {
	case argo && flux:
		return "Argo CD and Flux CD are installed", nil
	case argo:
		return "Argo CD is installed", nil
	case flux:
		return "Flux CD is installed", nil
	}
	return "", unreadable
}

var _ extension.Suggester = Delivery{}

// Component names what Suggest looks for (extension.Component).
func (Delivery) Component() string { return "Argo CD or Flux CD" }

var _ extension.Component = Delivery{}

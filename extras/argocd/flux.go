package argocd

import (
	"errors"
	"net/http"

	"github.com/thonicdev/kubetower/extension"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Flux CD beside Argo CD, on its own route so that one engine absent, refused
// or unreadable never hides the other.
//
// Flux has no single "application" kind: Kustomizations and HelmReleases are
// looked up separately, since a cluster may run either without the other, and
// an absent one is a note, not a failure.

// The two kinds, qualified for the same reason ApplicationKind is.
const (
	KustomizationKind = "kustomizations.kustomize.toolkit.fluxcd.io"
	HelmReleaseKind   = "helmreleases.helm.toolkit.fluxcd.io"
)

// FluxObject is one row, in Flux's own words.
type FluxObject struct {
	// Kind is `Kustomization` or `HelmRelease`.
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Ready is the `Ready` condition's status as Flux writes it (`True`,
	// `False`, `Unknown`), or empty when unreported; Reason and Message are the
	// same condition's. They are never mapped onto Argo CD's words.
	Ready   string `json:"ready"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	// Reconciling and Stalled are Flux's kstatus conditions, true when set.
	Reconciling bool `json:"reconciling"`
	Stalled     bool `json:"stalled"`
	// Suspended is `spec.suspend`: Flux will not reconcile it until resumed.
	Suspended bool `json:"suspended"`
	// Revision is the last revision Flux applied.
	Revision string `json:"revision,omitempty"`
	Deleting bool   `json:"deleting"`
}

type fluxAnswer struct {
	Objects  []FluxObject `json:"objects"`
	Complete bool         `json:"complete"`
	Note     string       `json:"note,omitempty"`
}

func (d Delivery) flux(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")

		registry, err := h.Definitions(r, cluster)
		if unknownCluster(w, cluster, err) {
			return
		}
		if err != nil {
			writeProblem(w, http.StatusBadGateway, CodeCatalogueUnreadable, "The custom resource catalog could not be read, so whether this cluster serves Flux CD is unknown.", err)
			return
		}

		var crds []extension.CRD
		var missing, unserved []string
		for _, kind := range []string{KustomizationKind, HelmReleaseKind} {
			crd, err := registry.Lookup(r.Context(), kind)
			switch {
			case err == nil:
				crds = append(crds, crd)
			case errors.Is(err, extension.ErrUnknownKind):
				missing = append(missing, kind)
			case errors.Is(err, extension.ErrNotServed):
				unserved = append(unserved, kind)
			case errors.Is(err, extension.ErrAmbiguousKind):
				writeAmbiguous(w, kind, err)
				return
			default:
				writeProblem(w, http.StatusBadGateway, CodeKindUnresolved, "Resolving "+kind+" failed.", err)
				return
			}
		}
		if len(crds) == 0 {
			if len(unserved) > 0 {
				writeProblem(w, http.StatusNotFound, CodeKindNotServed, "Flux CD's custom resource definitions are present on this cluster and no version of them is served, so nothing can be read.", nil)
				return
			}
			writeProblem(w, http.StatusNotFound, CodeKindUnknown, "This cluster serves neither Kustomizations nor HelmReleases, so Flux CD is not installed on it.", nil)
			return
		}

		out := fluxAnswer{Objects: []FluxObject{}, Complete: true}
		notes := []string{}
		for _, kind := range missing {
			notes = append(notes, "this cluster serves no "+kind)
		}
		for _, kind := range unserved {
			notes = append(notes, kind+" is defined and served by no version")
		}
		for _, crd := range crds {
			page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR()})
			if writeRefused(w, err, func(who string) string {
				return "Flux CD is installed on this cluster, and " + who + " may not list its " + crd.Plural + "."
			}) {
				return
			}
			if err != nil {
				writeProblem(w, http.StatusBadGateway, CodeListFailed, "Flux CD's "+crd.Plural+" could not be read.", err)
				return
			}
			if !page.Complete {
				out.Complete = false
			}
			if page.Note != "" {
				notes = append(notes, page.Note)
			}
			for i := range page.Items {
				out.Objects = append(out.Objects, fluxObjectFrom(page.Items[i]))
			}
		}
		out.Note = joinNotes(notes)
		writeJSON(w, http.StatusOK, out)
	})
}

func fluxObjectFrom(obj unstructured.Unstructured) FluxObject {
	o := FluxObject{
		Kind:      obj.GetKind(),
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
		Revision:  nestedString(obj, "status", "lastAppliedRevision"),
		Deleting:  obj.GetDeletionTimestamp() != nil,
	}
	o.Suspended, _, _ = unstructured.NestedBool(obj.Object, "spec", "suspend")
	for _, c := range slice(nestedMap(obj.Object, "status"), "conditions") {
		switch str(c, "type") {
		case "Ready":
			o.Ready = str(c, "status")
			o.Reason = str(c, "reason")
			o.Message = str(c, "message")
		case "Reconciling":
			o.Reconciling = str(c, "status") == "True"
		case "Stalled":
			o.Stalled = str(c, "status") == "True"
		}
	}
	return o
}

// joinNotes is one sentence out of several, each written lower-case to sit
// inside it, or empty.
func joinNotes(notes []string) string {
	out := ""
	for i, n := range notes {
		if i > 0 {
			out += "; "
		}
		out += n
	}
	if out == "" {
		return ""
	}
	return out + "."
}

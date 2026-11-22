package cnpg

import (
	"encoding/json"
	"errors"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

// The problem codes the page branches on. Protocol, not display text:
// web/app/extras/cnpg/data.ts names each of them.
const (
	// CodeUnknownCluster: the cluster is not in the kubeconfig.
	CodeUnknownCluster = "unknown_cluster"
	// CodeKindUnknown: no CRD answers to CloudNativePG's Cluster kind, so
	// the operator is not installed. An absence, drawn as one.
	CodeKindUnknown = "kind_unknown"
	// CodeKindNotServed: the CRD is there and serves no version.
	CodeKindNotServed = "kind_not_served"
	// CodeCatalogueUnreadable and CodeKindUnresolved: whether the operator
	// is installed could not be told. Failures to ask, never an absence.
	CodeCatalogueUnreadable = "catalogue_unreadable"
	CodeKindUnresolved      = "kind_unresolved"
	// CodeListFailed: the list was asked and did not come back.
	CodeListFailed = "list_failed"
	// CodeListForbidden and CodeListUnreviewed: the operator is installed
	// and the person may not read it, or the console could not ask.
	CodeListForbidden  = extension.CodeListForbidden
	CodeListUnreviewed = extension.CodeListUnreviewed
	// CodeNotFound: no Cluster of that name the person may read. Absent and
	// unreadable give the same answer, so it reveals nothing.
	CodeNotFound = "not_found"
	// CodeBadRequest: the address does not name what a route reads.
	CodeBadRequest = "bad_request"
	// CodeNotInstance: the pod is not an instance of a CloudNativePG cluster.
	CodeNotInstance = "not_instance"
)

type problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// The status line is written, so an encoding error has nowhere to go.
	_ = json.NewEncoder(w).Encode(body)
}

func writeProblem(w http.ResponseWriter, status int, code, message string, err error) {
	body := problem{Code: code, Message: message}
	if err != nil {
		body.Detail = textutil.FirstLineN(err.Error(), 300)
	}
	writeJSON(w, status, body)
}

// kinds is the operator's kinds as this cluster serves them, resolved once
// per request. A kind that is not served is absent from it.
type kinds struct {
	registry extension.CRDRegistry
	r        *http.Request
}

// resolveClusters resolves the Cluster kind and answers the request itself
// when it cannot be: not installed (404), not served (404), or the catalog
// unreadable (502). ok is false when it wrote.
func resolveClusters(w http.ResponseWriter, r *http.Request, h extension.Host, cluster string) (kinds, extension.CRD, bool) {
	registry, err := h.Definitions(r, cluster)
	if errors.Is(err, extension.ErrUnknownCluster) {
		writeProblem(w, http.StatusNotFound, CodeUnknownCluster, extension.UnknownClusterText(cluster), err)
		return kinds{}, extension.CRD{}, false
	}
	if writeResolutionRefused(w, err) {
		return kinds{}, extension.CRD{}, false
	}
	if err != nil {
		writeProblem(w, http.StatusBadGateway, CodeCatalogueUnreadable,
			"The custom resource catalog could not be read, so whether CloudNativePG is installed is unknown.", err)
		return kinds{}, extension.CRD{}, false
	}
	k := kinds{registry: registry, r: r}
	crd, err := registry.Lookup(r.Context(), ClusterKind)
	switch {
	case errors.Is(err, extension.ErrUnknownKind):
		writeProblem(w, http.StatusNotFound, CodeKindUnknown,
			"CloudNativePG is not installed on this cluster: no custom resource kind answers to "+ClusterKind+".", nil)
		return k, crd, false
	case errors.Is(err, extension.ErrNotServed):
		writeProblem(w, http.StatusNotFound, CodeKindNotServed,
			"CloudNativePG's Cluster kind is defined on this cluster, and no version of it is served.", err)
		return k, crd, false
	case writeResolutionRefused(w, err):
		return k, crd, false
	case err != nil:
		writeProblem(w, http.StatusBadGateway, CodeKindUnresolved, "Resolving "+ClusterKind+" failed.", err)
		return k, crd, false
	}
	return k, crd, true
}

// writeResolutionRefused answers a refusal of the catalog or kind read (a 403
// on the definitions or discovery, or the person's own denial) as refused, with
// the API server's message in the detail, and reports whether it wrote. A
// timeout or a 5xx is not a refusal and stays the caller's failure.
func writeResolutionRefused(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	const msg = "You may not read the custom resource definitions, so whether CloudNativePG is installed is unknown."
	if refused, ok := extension.RefusedRead(err, msg); ok {
		writeJSON(w, refused.Status, problem{Code: refused.Code, Message: refused.Message, Detail: textutil.FirstLineN(err.Error(), 300)})
		return true
	}
	if apierrors.IsForbidden(err) {
		writeProblem(w, http.StatusForbidden, CodeListForbidden, msg, err)
		return true
	}
	return false
}

// lookup is a secondary kind: ok false when it is not served here, which is
// an absence and not a failure.
func (k kinds) lookup(kind string) (extension.CRD, bool, error) {
	crd, err := k.registry.Lookup(k.r.Context(), kind)
	switch {
	case errors.Is(err, extension.ErrUnknownKind), errors.Is(err, extension.ErrNotServed), errors.Is(err, extension.ErrAmbiguousKind):
		return crd, false, nil
	case err != nil:
		return crd, false, err
	}
	return crd, true, nil
}

// writeRefused answers a Cluster list that was refused rather than failed,
// and reports whether it wrote: the person's refusal (403 or 503), or a 403
// the cluster gave the console's own credentials.
func writeRefused(w http.ResponseWriter, err error) bool {
	// The installation's missing grant, said as that: neither the person's 403
	// nor a gateway error.
	if refused, ok := extension.ConsoleNotGranted(err); ok {
		writeJSON(w, refused.Status, problem{Code: refused.Code, Message: refused.Message, Detail: textutil.FirstLineN(err.Error(), 300)})
		return true
	}
	if refused, ok := extension.RefusedRead(err, "CloudNativePG is installed on this cluster, and you may not list its Clusters."); ok {
		writeJSON(w, refused.Status, problem{Code: refused.Code, Message: refused.Message, Detail: textutil.FirstLineN(err.Error(), 300)})
		return true
	}
	if apierrors.IsForbidden(err) {
		writeProblem(w, http.StatusForbidden, CodeListForbidden,
			"CloudNativePG is installed on this cluster, and the account this console reads with may not list its Clusters.", err)
		return true
	}
	return false
}

// Source is what one secondary read gave, for the page's notes: read, not
// served here, refused for the person, or failed.
type Source struct {
	Kind string `json:"kind"`
	// State is read, absent, refused or failed.
	State string `json:"state"`
	Note  string `json:"note,omitempty"`
	// Complete is false when the list was cut short or narrowed.
	Complete bool `json:"complete"`
}

// sourceOf classifies a secondary read's error. A refusal or a failure of
// one kind is that kind's note, never the page's failure.
func sourceOf(kind string, page extension.Page, err error) Source {
	switch {
	case err == nil:
		return Source{Kind: kind, State: "read", Note: page.Note, Complete: page.Complete}
	case errors.Is(err, extension.ErrDenied):
		return Source{Kind: kind, State: "refused", Note: "You may not list " + kind + "."}
	case errors.Is(err, extension.ErrUnreviewable):
		return Source{Kind: kind, State: "refused", Note: extension.UnreviewedText}
	case apierrors.IsForbidden(err):
		return Source{Kind: kind, State: "refused", Note: "The account this console reads with may not list " + kind + "."}
	}
	return Source{Kind: kind, State: "failed", Note: textutil.FirstLineN(err.Error(), 200)}
}

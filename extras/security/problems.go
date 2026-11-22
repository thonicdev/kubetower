package security

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

// The problem codes the page branches on. Protocol, not display text.
const (
	CodeUnknownCluster      = "unknown_cluster"
	CodeBadRequest          = "bad_request"
	CodeCatalogueUnreadable = "catalogue_unreadable"
	CodeListFailed          = "list_failed"
	// A read refused for the person: 403, and 503 when it could not be asked.
	CodeListForbidden  = extension.CodeListForbidden
	CodeListUnreviewed = extension.CodeListUnreviewed
)

// problem is a failure's body: Code is what the page branches on, Message
// what it prints.
type problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, contentType string, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		http.Error(w, "the answer could not be encoded", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}

func writeAnswer(w http.ResponseWriter, body any) {
	writeJSON(w, http.StatusOK, "application/json; charset=utf-8", body)
}

func writeProblem(w http.ResponseWriter, status int, code, message string, err error) {
	body := problem{Code: code, Message: message}
	if err != nil {
		body.Detail = textutil.FirstLineN(err.Error(), 300)
	}
	writeJSON(w, status, "application/problem+json; charset=utf-8", body)
}

// stepCatalogue marks a failure to read the custom resource catalog, which
// leaves whether trivy-operator is installed unknown.
type catalogueError struct{ err error }

func (e catalogueError) Error() string { return "the custom resource catalog: " + e.err.Error() }
func (e catalogueError) Unwrap() error { return e.err }

// writeFailure answers a request that could not be read at all: 404 for a
// cluster that is not in the kubeconfig, 403 or 503 for a read refused for the
// person, 502 for a read that failed. Never an empty answer. A request that is
// gone gets nothing.
func writeFailure(w http.ResponseWriter, cluster string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	if errors.Is(err, extension.ErrUnknownCluster) {
		writeProblem(w, http.StatusNotFound, CodeUnknownCluster, extension.UnknownClusterText(cluster), nil)
		return
	}
	if refused, ok := extension.RefusedRead(err,
		"The cluster says you may not list trivy-operator's reports here, so what they found is not shown."); ok {
		writeProblem(w, refused.Status, refused.Code, refused.Message, err)
		return
	}
	var cat catalogueError
	if errors.As(err, &cat) {
		writeProblem(w, http.StatusBadGateway, CodeCatalogueUnreadable,
			"The cluster's custom resource catalog could not be read, so whether trivy-operator is installed is unknown.", err)
		return
	}
	writeProblem(w, http.StatusBadGateway, CodeListFailed,
		"trivy-operator's reports could not be read, so what they found on this cluster is unknown.", err)
}

func quote(s string) string {
	return `"` + textutil.FirstLineN(s, 80) + `"`
}

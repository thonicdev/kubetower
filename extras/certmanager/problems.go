package certmanager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/thonicdev/kubetower/extension"
)

// The codes are protocol, not display text: the front end names each of them.
const (
	CodeUnknownCluster      = "unknown_cluster"
	CodeBadNamespace        = "bad_namespace"
	CodeCatalogueUnreadable = "catalogue_unreadable"
	CodeListFailed          = "list_failed"
	// A read refused for the person: 403, and 503 when it could not be asked.
	CodeListForbidden  = extension.CodeListForbidden
	CodeListUnreviewed = extension.CodeListUnreviewed
)

type problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

// The step a request failed at, which decides its sentence.
const (
	stepCatalogue    = "catalogue"
	stepCertificates = "certificates"
	stepIngresses    = "ingresses"
)

// failure is a read the request cannot answer without.
type failure struct {
	step string
	err  error
}

func (f *failure) Error() string { return f.step + ": " + f.err.Error() }
func (f *failure) Unwrap() error { return f.err }

// writeFailure answers a request that could not be read: an unknown cluster
// is a 404 (no API server was asked), a refusal for the person a 403 or 503,
// anything else a 502 saying what is now unknown. A request that is gone gets
// nothing.
func writeFailure(w http.ResponseWriter, cluster string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	if errors.Is(err, extension.ErrUnknownCluster) {
		writeProblem(w, http.StatusNotFound, CodeUnknownCluster, extension.UnknownClusterText(cluster), nil)
		return
	}
	if refused, ok := extension.ConsoleNotGranted(err); ok {
		writeProblem(w, refused.Status, refused.Code, refused.Message, err)
		return
	}
	if refused, ok := extension.RefusedRead(err,
		"The cluster says you may not list cert-manager's Certificates here, so what expires is not shown."); ok {
		writeProblem(w, refused.Status, refused.Code, refused.Message, err)
		return
	}
	var f *failure
	if errors.As(err, &f) && f.step == stepCatalogue {
		writeProblem(w, http.StatusBadGateway, CodeCatalogueUnreadable,
			"The cluster's custom resource catalog could not be read, so whether cert-manager is installed is unknown.", err)
		return
	}
	if errors.As(err, &f) && f.step == stepIngresses {
		writeProblem(w, http.StatusBadGateway, CodeListFailed,
			"The Ingresses could not be read, so which certificate each one serves is unknown.", err)
		return
	}
	writeProblem(w, http.StatusBadGateway, CodeListFailed,
		"The Certificates could not be read, so what expires on this cluster is unknown.", err)
}

func writeProblem(w http.ResponseWriter, status int, code, message string, err error) {
	body := problem{Code: code, Message: message}
	if err != nil {
		body.Detail = shortError(err)
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// shortError is an error's text, bounded: a detail line, not a dump.
func shortError(err error) string {
	return clip(strings.TrimSpace(err.Error()))
}

func sortIssuers(is []Issuer) {
	sort.SliceStable(is, func(i, j int) bool {
		a, b := is[i], is[j]
		if a.Broken != b.Broken {
			return a.Broken
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
}

func sortACME(as []ACME) {
	sort.SliceStable(as, func(i, j int) bool {
		a, b := as[i], as[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
}

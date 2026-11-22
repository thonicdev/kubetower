package web

import (
	"net/http"

	"github.com/thonicdev/kubetower/internal/edit"
)

// RefusalHeader is the response header naming why a request was refused: a
// contract with every client that is not a browser, which would otherwise read a
// 200 as success. `internal/logs` writes the literal too, since it cannot import
// this package.
const RefusalHeader = "X-KubeTower-Refusal"

// The refusal words. Clients may match on them, so they change like an API, not
// like a sentence.
const (
	// RefusalUnknownCluster is a request naming a context this console does not
	// serve: understood and answered, not "not found".
	RefusalUnknownCluster = "unknown-cluster"
	// RefusalInput is a request that does not describe a change this console makes,
	// such as a replica count that is not a number or a missing key.
	RefusalInput = "input"
	// RefusalNotSaved is not a refusal of the change: the change happened, and
	// writing it down failed, so it will be gone at the next start. A client must be
	// able to tell "did nothing" from "did it, not durably".
	RefusalNotSaved = "not-saved"
	// RefusalUnclassified is a refusal that carried no kind; not "input", which
	// would claim a cause nobody classified.
	RefusalUnclassified = "unclassified"
)

// RefusalRenderedHeader marks a response whose status is a refusal and whose
// body is nevertheless meant to be drawn: the status stays the truth for
// scripts, and the header tells the browser the body is the sentence to show.
const RefusalRenderedHeader = "X-KubeTower-Refusal-Rendered"

// refuseWith answers a malformed request outright: the word, the status, and a
// plain-text sentence. It has no production caller; it is the shape for a bare
// 4xx.
func refuseWith(w http.ResponseWriter, name, reason string, status int) {
	w.Header().Set(RefusalHeader, name)
	http.Error(w, reason, status)
}

func refusalName(kind edit.RefusalKind) string {
	switch kind {
	case edit.RefusedInput:
		return RefusalInput
	case edit.RefusedReadOnly:
		return "read-only"
	case edit.RefusedImmutable:
		return "immutable"
	case edit.RefusedConflict:
		return "conflict"
	case edit.RefusedByAPI:
		return "rejected"
	case edit.RefusedNoChange:
		return "no-change"
	case edit.RefusedByAccess:
		return "denied"
	case edit.RefusedUnreviewable:
		return "unreviewable"
	case edit.RefusedUnrecorded:
		return "unrecorded"
	case edit.RefusedUnsent:
		return "unsent"
	case edit.RefusedNotSent:
		return "not-sent"
	case edit.RefusedUnanswered:
		return "unanswered"
	case edit.RefusedNotInProfile:
		return "not-in-profile"
	}
	// Not RefusalInput: RefusedInput is the zero value, so an unset kind would read
	// as a stated cause.
	return RefusalUnclassified
}

// The sentence for an unknown context is extension.UnknownClusterText, so an
// extension can say it too.

// known reports whether a name arriving from outside resolves to a context this
// console serves, through cluster.Manager.Resolve: a context name arrives in
// three spellings, two of them not the kubeconfig's.
func (s *Server) known(name string) bool {
	_, ok := s.clusters.Resolve(name)
	return ok
}

package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/sharedstate"
)

// Problem is RFC 9457, carried on every 4xx and 5xx as
// `application/problem+json`.
//
// Distinct from an envelope on purpose (`api/errors.md`): the HTTP status
// answers "did this request get an answer?", the envelope's state "what is the
// answer?". A request that produced a considered answer is 200 even when the
// data could not be obtained; one that never did (no session, unknown route,
// malformed body, refused at the door) is a problem object.
type Problem struct {
	Type     string      `json:"type,omitempty"`
	Title    string      `json:"title"`
	Status   int         `json:"status"`
	Detail   string      `json:"detail,omitempty"`
	Instance string      `json:"instance,omitempty"`
	Code     ProblemCode `json:"code"`
	Scope    *Scope      `json:"scope,omitempty"`
	// Plan is the fresh plan carried on a 409, so the front end can show the
	// operator what changed rather than simply failing.
	Plan *ConfirmationPlan `json:"plan,omitempty"`
}

// ProblemCode is stable and branchable, as ReasonCode is for states.
type ProblemCode string

// The codes the spec declares.
const (
	ProblemSessionExpired ProblemCode = "session_expired"
	ProblemNotAuthed      ProblemCode = "not_authenticated"
	ProblemOriginRejected ProblemCode = "origin_rejected"
	ProblemReadOnly       ProblemCode = "read_only"
	ProblemNotInProfile   ProblemCode = "not_available_in_this_profile"
	ProblemRBACDenied     ProblemCode = "rbac_denied"
	ProblemNotFound       ProblemCode = "not_found"
	ProblemInvalidRequest ProblemCode = "invalid_request"
	// A list asked with a label selector that does not parse: a 400, and the
	// only one a client may draw as "the selector is wrong".
	ProblemInvalidSelector ProblemCode = "invalid_selector"
	ProblemPlanExpired     ProblemCode = "plan_expired"
	ProblemPlanChanged     ProblemCode = "plan_changed"
	ProblemConflict        ProblemCode = "conflict"
	ProblemLimitReached    ProblemCode = "limit_reached"
	ProblemRateLimited     ProblemCode = "rate_limited"
	ProblemInternal        ProblemCode = "internal"
	// ProblemStoreUnreachable is the console's own shared store (sessions and
	// plans) not answering. A 503 with its own code, because "sign in again"
	// and "preview again" both need the store.
	ProblemStoreUnreachable ProblemCode = "session_store_unreachable"
	// ProblemAccessUnreviewable is a write not attempted because the console
	// could not ask whether the person may make it, or (titled "Audit record
	// could not be written") could not record it first. A 503, never
	// `rbac_denied`: nobody said no.
	ProblemAccessUnreviewable ProblemCode = "access_unreviewable"
	// ProblemNotSent is a write that never left the console: out of time, held
	// by the rate limiter, or no connection. A 503; running it again is safe.
	ProblemNotSent ProblemCode = "not_sent"
	// ProblemNotAnswered is a write that left and whose answer never came. A
	// 504: whether it happened is unknown, so a client must not retry alone.
	ProblemNotAnswered ProblemCode = "not_answered"
	// ProblemNotSaved is a preference write that took effect but could not be
	// saved to the state directory, so it is gone at the next start. A 500,
	// never `invalid_request`, which would say nothing happened.
	ProblemNotSaved ProblemCode = "not_saved"
	// ProblemRBACNotInProfile is a change to RBAC refused under the server
	// profile before any review (ErrRBACNotInProfile). A 403, never
	// `rbac_denied`: nobody's rights said no.
	ProblemRBACNotInProfile ProblemCode = "rbac_not_in_profile"
)

// problemMediaType lets a client tell a problem from an envelope without
// parsing either.
const problemMediaType = "application/problem+json"

// WriteProblem answers with a problem object. Exported because `internal/auth`
// answers this API's 401 in the same shape: an HTML login page would render in
// a fetch as a blank screen.
func WriteProblem(w http.ResponseWriter, status int, code ProblemCode, title, detail string) {
	writeProblem(w, Problem{Status: status, Code: code, Title: title, Detail: detail})
}

func writeProblem(w http.ResponseWriter, p Problem) {
	if p.Type == "" {
		p.Type = "about:blank"
	}
	body, err := json.Marshal(p)
	if err != nil {
		// Problem cannot fail to marshal, but a 500 with no body beats a panic
		// in an error path.
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", problemMediaType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(p.Status)
	_, _ = w.Write(body)
}

// The refusals that come up on nearly every operation, each pairing status
// and code so a handler cannot mismatch them.

func notFound(w http.ResponseWriter, detail string) {
	WriteProblem(w, http.StatusNotFound, ProblemNotFound, "Not found", detail)
}

// UnknownCluster answers a cluster name that is not in the kubeconfig: 404
// not_found, with extension.UnknownClusterText as the detail. Exported so the
// extras' gate (internal/web) answers it in this API's shape and code.
func UnknownCluster(w http.ResponseWriter, name string) {
	notFound(w, extension.UnknownClusterText(name))
}

func badRequest(w http.ResponseWriter, detail string) {
	WriteProblem(w, http.StatusBadRequest, ProblemInvalidRequest, "Bad request", detail)
}

func refused(w http.ResponseWriter, code ProblemCode, detail string) {
	WriteProblem(w, http.StatusForbidden, code, "Refused", detail)
}

func unauthenticated(w http.ResponseWriter, code ProblemCode, detail string) {
	WriteProblem(w, http.StatusUnauthorized, code, "Not signed in", detail)
}

func limitReached(w http.ResponseWriter, detail string) {
	WriteProblem(w, http.StatusConflict, ProblemLimitReached, "Limit reached", detail)
}

// planStale carries the fresh plan in the body, so the confirmation dialog
// can show what changed instead of a dead end.
func planStale(w http.ResponseWriter, code ProblemCode, detail string, plan *ConfirmationPlan) {
	writeProblem(w, Problem{
		Status: http.StatusConflict,
		Code:   code,
		Title:  "The plan no longer describes what would happen",
		Detail: detail,
		Plan:   plan,
	})
}

// planStoreFailure answers a plan that could not be written or read: a 409
// `limit_reached` where the plan store's ceiling refused it, a 503
// naming the store where the store did not answer, a 500 otherwise.
func planStoreFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrLimitReached) {
		limitReached(w, err.Error())
		return
	}
	if errors.Is(err, sharedstate.ErrUnreachable) {
		WriteProblem(w, http.StatusServiceUnavailable, ProblemStoreUnreachable, "Session store unreachable",
			"the console's session store did not answer, so the plan could not be kept or read; try again "+
				"in a moment")
		return
	}
	internalProblem(w, err.Error())
}

func internalProblem(w http.ResponseWriter, detail string) {
	WriteProblem(w, http.StatusInternalServerError, ProblemInternal, "Internal error", detail)
}

// Package api serves the console's JSON contract, written against the
// hand-written `api/openapi.yaml`.
//
// Nothing regenerates the spec; the conformance test in this package puts real
// handler responses through its schemas. Where the two disagree the spec is
// right, except where a schema cannot describe what the console knows: that is
// a spec defect, said where it happens.
//
// The contract lives here; the data comes from the domain packages through the
// one adapter in internal/web/apimount.go.
package api

import (
	"encoding/json"
	"fmt"
	"time"
)

// State is the five-state vocabulary, plus `ok`.
//
// `{"items": []}` is a well-formed success that would report "no pods" for a
// cluster running hundreds; collapsing any two states is the defect this type
// prevents.
type State string

const (
	// StateOK — read, and there is something to show.
	StateOK State = "ok"
	// StateEmpty — read, and there is nothing here.
	StateEmpty State = "empty"
	// StatePending — nobody has asked yet. Not `unreadable`, which asserts a
	// read was tried: a failure invites a retry, this invites waiting.
	StatePending State = "pending"
	// StateUnreadable — it exists and could not be read.
	StateUnreadable State = "unreadable"
	// StateAbsent — the thing that would serve this is not installed.
	StateAbsent State = "absent"
	// StateUnreachable — the cluster could not be asked at all.
	StateUnreachable State = "unreachable"
	// StateForbidden — the API server refused this specific verb. Not HTTP
	// 403, which is this console refusing the caller: here the console asked,
	// got a definite answer, and that answer is part of the page.
	StateForbidden State = "forbidden"
)

// ReasonCode is protocol. A client branches on it.
type ReasonCode string

// The codes this package builds. The spec declares more (the health page's
// classification); a client branches on the string, so none may be invented.
const (
	CodeEmptyScope         ReasonCode = "empty_scope"
	CodeReadDenied         ReasonCode = "read_denied"
	CodeUndecodable        ReasonCode = "undecodable"
	CodeNotInstalled       ReasonCode = "not_installed"
	CodeNoSuchKind         ReasonCode = "no_such_kind"
	CodeClusterUnreachable ReasonCode = "cluster_unreachable"
	CodeTimeout            ReasonCode = "timeout"
	CodeVerbDenied         ReasonCode = "verb_denied"
	CodeNotSupportedHere   ReasonCode = "not_supported_here"
	// CodeNoSuchObject goes with StateAbsent: the kind is served but this
	// object is not there. Not CodeNoSuchKind, whose remedy is an install.
	CodeNoSuchObject ReasonCode = "no_such_object"
	// CodeNotProbedYet goes with StatePending: a question nobody has put,
	// unlike CodeTimeout, put and not answered. An ActResult carries it for a
	// write that never left the console (a drain eviction or bulk row whose
	// budget ran out, whose review or audit record failed, held by the rate
	// limiter, or with no connection); one sent and not answered carries
	// CodeTimeout.
	CodeNotProbedYet ReasonCode = "not_probed_yet"
	// CodeStaleReading is the one reason carried on `ok`: the payload is a
	// reading from before the source stopped answering.
	CodeStaleReading ReasonCode = "stale_reading"
	// CodeTruncated is the log stream's 8 MiB ceiling: there is more and this
	// console will not read it, distinct from `end of log` and from a read
	// error.
	CodeTruncated ReasonCode = "truncated"
	// CodeContainerNotStarted is a followed log whose container has not
	// started. On `pending` the stream waits and lines follow once it runs;
	// on `unreadable` the wait has ended (it will not start on its own, or the
	// pod went away).
	CodeContainerNotStarted ReasonCode = "container_not_started"
	// CodeAccessUnreviewable is a control drawn disabled because the console
	// could not ask whether the person may use it. Not `verb_denied`: nobody
	// said no.
	CodeAccessUnreviewable ReasonCode = "access_unreviewable"
	// CodeRBACNotInProfile is a bulk row refused because the server profile
	// does not change RBAC. The single door's problem code; not `verb_denied`.
	CodeRBACNotInProfile ReasonCode = "rbac_not_in_profile"

	// Why a cluster did not answer, classified. An unresolvable name, a lapsed
	// session and an IAM refusal are different people's problems; these carry
	// the fact, `HealthCheck.remedy` the sentences, and `Reason.message` is
	// prose a client must not branch on.
	CodeCredentialsExpired ReasonCode = "credentials_expired"
	CodeCredentialsRefused ReasonCode = "credentials_refused"
	CodeNameNotResolved    ReasonCode = "name_not_resolved"
	CodeConnectionRefused  ReasonCode = "connection_refused"
	CodeUnclassified       ReasonCode = "unclassified"

	// CodeNoSession is the health page's AWS SSO check on a machine with no
	// SSO session, carried with `empty`: neither an install nor a failure.
	CodeNoSession ReasonCode = "no_session"
)

// Scope is what was asked, so "unreachable" says unreachable for what.
type Scope struct {
	Cluster   string `json:"cluster,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Resource  string `json:"resource,omitempty"`
	Name      string `json:"name,omitempty"`
	Verb      string `json:"verb,omitempty"`
}

// Reason says why the state is not `ok`. Message is one sentence for an
// operator and may be reworded in any release; branch on Code.
type Reason struct {
	Code    ReasonCode `json:"code"`
	Message string     `json:"message"`
	Detail  string     `json:"detail,omitempty"`
	Scope   *Scope     `json:"scope,omitempty"`
	Since   *time.Time `json:"since,omitempty"`
	// Retryable drives a retry button, not a retry loop.
	Retryable *bool `json:"retryable,omitempty"`
}

// Because is the ordinary way to build a Reason; the schema requires the
// message, so no constructor omits it.
func Because(code ReasonCode, message string) Reason {
	return Reason{Code: code, Message: message}
}

// WithDetail carries the underlying message verbatim, such as a Kubernetes
// 403's wording. Never a stack trace or a secret value.
func (r Reason) WithDetail(detail string) Reason { r.Detail = detail; return r }

// In names what was asked.
func (r Reason) In(s Scope) Reason { r.Scope = &s; return r }

// Retry states whether refetching could plausibly give a different answer.
func (r Reason) Retry(ok bool) Reason { r.Retryable = &ok; return r }

// Remedy is who fixes it, and what they would do.
//
// Not a member of Reason, so handlers are not invited to compose one without
// knowing the answer. The classification stays in internal/web beside the
// health page, so the rail's tooltip and that page describe a cluster the same
// way. Actionable marks rows worth acting on, which a client could not
// recompute without re-implementing the classification.
type Remedy struct {
	Who        string `json:"who"`
	What       string `json:"what"`
	Actionable bool   `json:"actionable,omitempty"`
	// Command is the one command that fixes it, to copy, where there is one,
	// such as `aws sso login --profile p`.
	Command string `json:"command,omitempty"`
}

// --- The arms ---------------------------------------------------------------
//
// The spec writes the arm rules as JSON Schema if/then. In Go the fields are
// unexported, so only constructors build an envelope and none can produce a
// forbidden shape:
//
// 	state: ok         → items, with at least one element
// 	state: empty      → items, empty
// 	otherwise         → reason, and NO items member at all
//
// Items with an empty slice returns `empty`, never `ok` with `[]`.

// List is a collection envelope.
type List[T any] struct {
	state  State
	reason *Reason
	items  []T
}

// Items is the successful arm, deciding between ok and empty from the slice.
func Items[T any](items []T) List[T] {
	if len(items) == 0 {
		return List[T]{state: StateEmpty, items: []T{}}
	}
	return List[T]{state: StateOK, items: items}
}

// NothingBecause is `empty` with a reason, so "nothing in this namespace"
// can say which namespace.
func NothingBecause[T any](r Reason) List[T] {
	return List[T]{state: StateEmpty, reason: &r, items: []T{}}
}

// Kept is the one `ok` answer that carries a reason: a reading held from
// before the source stopped answering, qualified with CodeStaleReading. It
// keeps a usable list rather than showing nothing, and a client ignoring
// `reason` sees it as slightly out of date rather than wrong. An empty slice
// panics, as `ok` with nothing in it is the collapse.
func Kept[T any](items []T, r Reason) List[T] {
	if len(items) == 0 {
		panic("api: a kept reading with no items is not `ok`; it is the earlier failure")
	}
	return List[T]{state: StateOK, reason: &r, items: items}
}

// ListFailed is the five states that carry no items: the four failures and
// `pending`, which shares their shape but not what a client draws.
// It panics on ok or empty, so a caller that lost the distinction fails
// loudly in a test.
func ListFailed[T any](state State, r Reason) List[T] {
	switch state {
	case StatePending, StateUnreadable, StateAbsent, StateUnreachable, StateForbidden:
		return List[T]{state: state, reason: &r}
	}
	panic(fmt.Sprintf("api: %q is not a failure state; use Items or NothingBecause", state))
}

// State is which arm this envelope is on.
func (l List[T]) State() State { return l.state }

// Reason is why, or nil.
func (l List[T]) Reason() *Reason { return l.reason }

// Len is how many items it carries.
func (l List[T]) Len() int { return len(l.items) }

// arm is what gets written, decided in one place: NamespaceList marshals the
// same members plus one of its own, and must not decide the arm itself.
func (l List[T]) arm() (State, *Reason, *[]T) {
	if l.state != StateOK && l.state != StateEmpty {
		return l.state, l.reason, nil
	}
	items := l.items
	if items == nil {
		items = []T{}
	}
	return l.state, l.reason, &items
}

// MarshalJSON writes the arm. `items` is absent on a failure, so it is
// optional in the generated TypeScript and `data.items.map(…)` does not
// compile until the other states are handled.
func (l List[T]) MarshalJSON() ([]byte, error) {
	state, reason, items := l.arm()
	return json.Marshal(struct {
		State  State   `json:"state"`
		Reason *Reason `json:"reason,omitempty"`
		Items  *[]T    `json:"items,omitempty"`
	}{State: state, Reason: reason, Items: items})
}

// Value is a single-value envelope. It has no `empty`: a single thing that is
// not there is `absent`, and one fact gets one spelling.
type Value[T any] struct {
	state  State
	reason *Reason
	data   *T
}

// Data is the successful arm.
func Data[T any](v T) Value[T] { return Value[T]{state: StateOK, data: &v} }

// ValueFailed is the five states a value can be in without data, `pending`
// included.
func ValueFailed[T any](state State, r Reason) Value[T] {
	switch state {
	case StatePending, StateUnreadable, StateAbsent, StateUnreachable, StateForbidden:
		return Value[T]{state: state, reason: &r}
	}
	panic(fmt.Sprintf("api: %q is not a value failure state; a value is never empty", state))
}

// State is which arm this envelope is on.
func (v Value[T]) State() State { return v.state }

// Reason is why, or nil.
func (v Value[T]) Reason() *Reason { return v.reason }

// MarshalJSON writes the arm.
func (v Value[T]) MarshalJSON() ([]byte, error) {
	aux := struct {
		State  State   `json:"state"`
		Reason *Reason `json:"reason,omitempty"`
		Data   *T      `json:"data,omitempty"`
	}{State: v.state, Reason: v.reason, Data: v.data}
	return json.Marshal(aux)
}

// Failure is what a backend hands back instead of a payload: the state it
// established, and why. Not a bare error, because which way it went wrong is
// the answer (`api/errors.md`), and this package must not guess.
type Failure struct {
	State  State
	Reason Reason
	// invalid marks a request at fault rather than a cluster: answered as a
	// bad request, since no state of the thing is true of a question that
	// could not be asked.
	invalid bool
	// selector marks an invalid request whose fault is its label selector,
	// answered with its own problem code so a client can say so.
	selector bool
}

// InvalidRequest is a request the backend refuses as malformed, such as a
// label selector only it can parse. Handlers that accept it answer 400 with
// the message as detail; for others, state and code are a console-side
// failure.
func InvalidRequest(message string) *Failure {
	return &Failure{State: StateUnreadable, Reason: Because(CodeUnclassified, message), invalid: true}
}

// Invalid reports whether this failure is the request's own (InvalidRequest).
func (f *Failure) Invalid() bool { return f != nil && f.invalid }

// InvalidSelector is an InvalidRequest whose fault is the label selector, which
// does not parse. Answered 400 `invalid_selector`, apart from every other bad
// request, so a list can say the selector is wrong and nothing else.
func InvalidSelector(message string) *Failure {
	f := InvalidRequest(message)
	f.selector = true
	return f
}

// SelectorInvalid reports whether this failure is InvalidSelector's.
func (f *Failure) SelectorInvalid() bool { return f.Invalid() && f.selector }

// Fail is the general constructor, for a state and code pairing with no
// named constructor below.
func Fail(state State, code ReasonCode, message string) *Failure {
	return &Failure{State: state, Reason: Because(code, message)}
}

// Unreachable — the cluster could not be asked at all.
func Unreachable(message string) *Failure {
	return &Failure{State: StateUnreachable, Reason: Because(CodeClusterUnreachable, message).Retry(true)}
}

// Unreadable — it exists and could not be read.
func Unreadable(message string) *Failure {
	return &Failure{State: StateUnreadable, Reason: Because(CodeReadDenied, message)}
}

// Absent — the thing that would serve this is not installed.
func Absent(message string) *Failure {
	return &Failure{State: StateAbsent, Reason: Because(CodeNotInstalled, message).Retry(false)}
}

// Forbidden — the API server refused this specific verb.
func Forbidden(message string) *Failure {
	return &Failure{State: StateForbidden, Reason: Because(CodeVerbDenied, message).Retry(false)}
}

// NotProbed — nobody has asked yet. The answer is expected to arrive
// without the caller doing anything.
func NotProbed(message string) *Failure {
	return &Failure{State: StatePending, Reason: Because(CodeNotProbedYet, message)}
}

// Within adds the scope to a failure's reason.
func (f *Failure) Within(s Scope) *Failure {
	if f == nil {
		return nil
	}
	f.Reason = f.Reason.In(s)
	return f
}

// Retryable marks a failure whose answer could plausibly change: a retry
// button, not a retry loop.
func (f *Failure) Retryable() *Failure {
	if f == nil {
		return nil
	}
	f.Reason = f.Reason.Retry(true)
	return f
}

// Detailed carries the underlying wording.
func (f *Failure) Detailed(detail string) *Failure {
	if f == nil {
		return nil
	}
	f.Reason = f.Reason.WithDetail(detail)
	return f
}

// listOf turns a backend answer into a collection envelope.
func listOf[T any](items []T, f *Failure) List[T] {
	if f != nil {
		return ListFailed[T](f.State, f.Reason)
	}
	return Items(items)
}

// valueOf turns a backend answer into a value envelope.
func valueOf[T any](v T, f *Failure) Value[T] {
	if f != nil {
		return ValueFailed[T](f.State, f.Reason)
	}
	return Data(v)
}

package edit

// The last step of every request this package sends: the access review, then
// the request, then the line that records both.
//
// Every write leaves through perform: a patch (send), a create (Create), a
// delete (Delete) and an eviction (Evict). Each hands perform a closure;
// perform asks internal/access whether the person behind ctx may do that
// verb on that resource and runs the closure only on an allowed answer.
// internal/access/choke_test.go fails the build on a write-capable call
// outside such a closure, so "every write is reviewed" is a property of the
// tree.
//
// The audit record is the two structured records access.Gate.Perform writes.
// What is written here is the process log's human `write:` line. A commit
// refused by the review or failed after it is written here, the only place
// both halves are known. A commit that succeeded is written by its caller,
// which knows what it changed, with the decision the door answered: on the
// desktop a request can go on an answer short of allowed
// (access.Gate.LetsThrough), and the line must say so. A dry run writes
// nothing.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/textutil"
)

// perform reviews one request and makes it. verb and subresource are the
// request's own; resource, namespace and name come from where and target,
// which are what the closure sends to, so the question asked is the request
// made.
func perform(ctx context.Context, where Location, target Target, verb, subresource string, dryRun bool, do func(context.Context) error) (access.Decision, error) {
	if !where.guarded || where.door == nil {
		return access.Unreviewable, refuse(RefusedInput,
			"this write was built on a location that never went through Locate; call Locate rather "+
				"than assembling a Location")
	}
	w := where.door
	attrs := attributesOf(where, target, verb, subresource)
	if dryRun {
		// So that the audit record says the request changed nothing.
		ctx = access.DryRun(ctx)
	}
	answer, err := w.access.Perform(ctx, where.Cluster, attrs, do)

	var refused *access.Refused
	if errors.As(err, &refused) {
		if !dryRun {
			w.Logf(ctx, "write: cluster=%q %s%s decision=%s outcome=refused audit=%s",
				where.Cluster, attrs, forceField(ctx), answer.Decision, recordOf(answer))
		}
		return answer.Decision, refuse(refusalKindOf(answer.Decision), "%s", refused.Error())
	}
	// Not sent because its audit record could not be written: the console
	// unable to act, not the person forbidden. Logged as refused, since
	// nothing reached the cluster.
	if errors.Is(err, access.ErrUnrecorded) {
		if !dryRun {
			w.Logf(ctx, "write: cluster=%q %s%s decision=%s outcome=refused reason=unrecorded",
				where.Cluster, attrs, forceField(ctx), answer.Decision)
		}
		return answer.Decision, refuseFrom(err, RefusedUnrecorded, "%s", err.Error())
	}
	if err != nil && !dryRun {
		// A word, never the API server's message: a validation error or a
		// webhook may quote the value it refused.
		w.Logf(ctx, "write: cluster=%q %s%s decision=%s outcome=failed reason=%s audit=%s",
			where.Cluster, attrs, forceField(ctx), answer.Decision, failureWord(err), recordOf(answer))
	}
	return answer.Decision, err
}

// forceField is ` force=true` on the `write:` line of a forced delete and
// nothing otherwise, as the audit record omits `force` when false.
func forceField(ctx context.Context) string {
	if access.IsForced(ctx) {
		return " force=true"
	}
	return ""
}

// Read runs do, a read of where, behind the access review for the person
// behind ctx: by name for a `get`, without one for a `list` (which RBAC's
// `resourceNames` cannot narrow). The read twin of perform.
func Read(ctx context.Context, where Location, target Target, verb string, do func(context.Context) error) error {
	if !where.guarded || where.door == nil {
		return refuse(RefusedInput,
			"this read was built on a location that never went through Locate; call Locate rather "+
				"than assembling a Location")
	}
	attrs := attributesOf(where, target, verb, "")
	if verb == "list" {
		attrs.Name = ""
	}
	_, err := where.door.access.Read(ctx, where.Cluster, attrs, do)
	return err
}

// Check asks the access review one question about where, for the person
// behind ctx, without making any request: nil when the answer lets it go.
func Check(ctx context.Context, where Location, attrs access.Attributes) error {
	if !where.guarded || where.door == nil {
		return refuse(RefusedInput,
			"this review was built on a location that never went through Locate; call Locate rather "+
				"than assembling a Location")
	}
	return where.door.access.Check(ctx, where.Cluster, attrs)
}

// CheckRecorded is Check for a refusal that must leave a trace: the same
// review, and a refusal is also recorded as a refused write (person, verb,
// target, decision), as the write door records its own. A preview passes
// preview true, so its record is marked a dry run like the other previews'.
// The review's answer alone is recorded, never a body or a value.
func CheckRecorded(ctx context.Context, where Location, attrs access.Attributes, preview bool) error {
	err := Check(ctx, where, attrs)
	var refused *access.Refused
	if errors.As(err, &refused) {
		if gate := where.door.access; gate != nil {
			rec := ctx
			if preview {
				rec = access.DryRun(ctx)
			}
			pending, _ := gate.Begin(rec, gate.Record(rec, audit.Write, where.Cluster, attrs, refused.Answer.Decision))
			gate.End(rec, pending, audit.Refused, "")
		}
	}
	return err
}

// reviewBeforeReading asks whether the person may patch target, and get it,
// before anything of the object is read, and refuses as the write door would.
//
// The previews read the object with the console's own rights and can refuse
// five ways (absent, immutable, no such key, binary, stale) before the dry
// run reviews the person. Unreviewed, a person with no rights would learn
// which Secrets exist and their key names. Asked first, they get one refusal
// whatever the object holds.
//
// `get` as well as `patch`, because a preview is also a read (the diff's
// before side, whether a value is unchanged), and RBAC allows patch without
// get.
//
// A refusal here is recorded as a refused dry-run write. The dry run still
// reviews again inside Gate.Perform: this only moves the first answer ahead
// of the read.
func reviewBeforeReading(ctx context.Context, where Location, target Target) error {
	for _, verb := range []string{"patch", "get"} {
		attrs := attributesOf(where, target, verb, "")
		err := Check(ctx, where, attrs)
		var refused *access.Refused
		if errors.As(err, &refused) {
			if gate := where.door.access; gate != nil {
				dry := access.DryRun(ctx)
				pending, _ := gate.Begin(dry, gate.Record(dry, audit.Write, where.Cluster, attrs, refused.Answer.Decision))
				gate.End(dry, pending, audit.Refused, "")
			}
			return refuse(refusalKindOf(refused.Answer.Decision), "%s", refused.Error())
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// refusalKindOf is the refusal a review's answer short of allowed is.
func refusalKindOf(decision access.Decision) RefusalKind {
	switch decision {
	case access.Denied:
		return RefusedByAccess
	case access.NotInProfile:
		return RefusedNotInProfile
	}
	return RefusedUnreviewable
}

// attributesOf is the review's question for a request to where/target.
//
// A create carries no name: kube-apiserver authorizes it with an empty
// name, so a Role granting `create` on named objects grants no create. A
// review naming the object would answer yes where the request is refused,
// an over-grant. A subresource create (an eviction) keeps the name.
//
// A Namespace is reviewed in itself (access.RequestNamespace), as the API
// server authorizes it.
func attributesOf(where Location, target Target, verb, subresource string) access.Attributes {
	name := target.Name
	if verb == "create" && subresource == "" {
		name = ""
	}
	return access.Attributes{
		Verb: verb, Group: where.Resource.Group, Version: where.Resource.Version,
		Resource: where.Resource.Resource, Subresource: subresource,
		Namespace: access.RequestNamespace(where.Resource.Resource, where.Namespaced, target.Namespace, name),
		Name:      name,
	}
}

// Attributes is the question a request of verb/subresource on target would be
// reviewed with, so what a page offers is predicted from the same function
// the write is reviewed through.
func Attributes(where Location, target Target, verb, subresource string) access.Attributes {
	return attributesOf(where, target, verb, subresource)
}

// recordOf is the audit record's id as the human line writes it, `-` where
// none was written (the desktop, or a refusal whose record failed too). Only
// on the lines this file writes; the `applied` line is its caller's, which
// is handed the decision and not the answer.
func recordOf(answer access.Answer) string {
	if answer.Record == "" {
		return "-"
	}
	return answer.Record
}

// failureWord names what went wrong without quoting anything.
func failureWord(err error) string {
	// The guard's refusal first, classified (a patch or a create) or raw (a
	// delete or an eviction): a request the console held back is not the
	// cluster's error.
	if errors.Is(err, access.ErrNotReviewed) {
		return "unsent"
	}
	if refusal, ok := AsRefusal(err); ok {
		switch refusal.Kind {
		case RefusedUnsent:
			return "unsent"
		case RefusedNotSent, RefusedUnanswered:
			// The audit record's own word, never `refused-by-the-cluster` for a
			// write the cluster did not refuse.
			return access.FailureWord(err)
		case RefusedConflict:
			return "conflict"
		case RefusedByAPI:
			return "refused-by-the-cluster"
		case RefusedInput:
			return "input"
		}
		return "refused"
	}
	// The same words the audit record's outcome uses.
	return access.FailureWord(err)
}

// Delete removes one object, after the review. options carry the caller's
// preconditions (uid, resourceVersion) and propagation. The API server's
// error comes back as it came, so a caller can tell a 404 from a 409. A
// forced delete (grace period zero) is marked on ctx so its audit records
// say `force`.
func Delete(ctx context.Context, where Location, target Target, options metav1.DeleteOptions) (access.Decision, error) {
	if ForcedDelete(options) {
		ctx = access.Forced(ctx)
	}
	return perform(ctx, where, target, "delete", "", false, func(ctx context.Context) error {
		iface, err := resourceOf(where, target)
		if err != nil {
			return err
		}
		return onTheWire(ctx, changesObject, iface.Delete(ctx, target.Name, options))
	})
}

// ForcedDelete says whether options are a forced delete's (a grace period of
// zero). Delete marks ctx by this one rule, so a caller's own line about the
// delete cannot disagree with the door's lines and the audit record.
func ForcedDelete(options metav1.DeleteOptions) bool {
	return options.GracePeriodSeconds != nil && *options.GracePeriodSeconds == 0
}

// ErrNotSent is what an eviction that never left the console unwraps to:
// access.ErrNotSent, so its audit record says `held` and a caller reports a
// request that never left, not an unanswered one. Every one is a *NotSent.
var ErrNotSent = access.ErrNotSent

// ErrUnanswered is what an eviction that left the console, and whose answer
// never came back, unwraps to: access.ErrUnanswered. Whether it happened is not
// known. Every one is an *Unanswered.
var ErrUnanswered = access.ErrUnanswered

// Unanswered is a request that left the console, and the error it came back
// with instead of an answer: the deadline, a dropped connection, a retry cut
// after an answer that settled nothing.
type Unanswered struct {
	Err error
}

func (u *Unanswered) Error() string { return "sent, not answered: " + u.Err.Error() }

// Is makes an Unanswered ErrUnanswered.
func (u *Unanswered) Is(target error) bool { return target == ErrUnanswered }

// Unwrap is the error the request came back with.
func (u *Unanswered) Unwrap() error { return u.Err }

// AsUnanswered reports whether err is a request sent and not answered.
func AsUnanswered(err error) (*Unanswered, bool) {
	var unanswered *Unanswered
	if errors.As(err, &unanswered) {
		return unanswered, true
	}
	return nil, false
}

// ErrTimedOut is what a write the API server answered by timing out unwraps
// to: access.ErrTimedOut. The change may still happen. Every one is a
// *TimedOut.
var ErrTimedOut = access.ErrTimedOut

// TimedOut is a request the API server answered without finishing it (504
// Timeout, 500 ServerTimeout, or any 5xx with Retry-After that client-go
// retried), and the error that ended it. Answered, and not a no: the change
// may still happen, so it is neither "the cluster refused the change" nor
// counted among a drain's refusals.
type TimedOut struct {
	// Status is the unfinished answer's: 504, or a 5xx carrying Retry-After.
	Status int
	// Reason is its StatusReason where the error is that answer, and empty
	// where client-go dropped an earlier attempt's body when it retried.
	Reason metav1.StatusReason
	Err    error
}

func (t *TimedOut) Error() string {
	return "answered, the API server " + t.What() + " it (" + t.Answer() + "): " + t.Err.Error()
}

// What is what the answer said, as a verb: "timed out on" for a 504 or a
// ServerTimeout, "did not finish" for any other 5xx asking to be retried.
func (t *TimedOut) What() string {
	if t.Status == http.StatusGatewayTimeout || t.Reason == metav1.StatusReasonServerTimeout {
		return "timed out on"
	}
	return "did not finish"
}

// Is makes a TimedOut ErrTimedOut.
func (t *TimedOut) Is(target error) bool { return target == ErrTimedOut }

// Unwrap is the error the request came back with.
func (t *TimedOut) Unwrap() error { return t.Err }

// Answer names the answer, and no more than is known of it: "504 Timeout",
// "500 ServerTimeout" when the error is that answer, otherwise the status
// "with Retry-After". A 500 with Retry-After has a ServerTimeout's shape
// without proving one.
func (t *TimedOut) Answer() string {
	switch {
	case t.Status == http.StatusGatewayTimeout:
		return "504 Timeout"
	case t.Reason == metav1.StatusReasonServerTimeout:
		return "500 ServerTimeout"
	}
	return fmt.Sprintf("%d %s with Retry-After", t.Status, http.StatusText(t.Status))
}

// AsTimedOut reports whether err is a request the API server answered by
// timing out.
func AsTimedOut(err error) (*TimedOut, bool) {
	var timedOut *TimedOut
	if errors.As(err, &timedOut) {
		return timedOut, true
	}
	return nil, false
}

// HeldAt is where a request that never left the console was held.
type HeldAt int

const (
	// HeldAfterReview is a context that had run out by the time the access
	// review let the request go: it was not handed to the client.
	HeldAfterReview HeldAt = iota
	// HeldBeforeTransport is a request client-go never handed to the
	// transport: the cluster's rate limiter could not give it a token before
	// its deadline, or its context ran out while it waited.
	HeldBeforeTransport
	// HeldBeforeConnection is a request handed to the transport for which no
	// connection to the API server was obtained: a dial or a TLS handshake that
	// failed or timed out, or a context that ran out while it waited for one.
	HeldBeforeConnection
)

func (h HeldAt) String() string {
	switch h {
	case HeldBeforeTransport:
		return "it was still waiting for the cluster's rate limit"
	case HeldBeforeConnection:
		return "no connection to the API server was made for it"
	}
	return "its time ran out before the request left the console"
}

// NotSent is a request that never left the console, the error it came back
// with, and where it was held. errors.Is(err, ErrNotSent) finds it.
type NotSent struct {
	At  HeldAt
	Err error
}

func (n *NotSent) Error() string { return "not sent: " + n.At.String() + ": " + n.Err.Error() }

// Is makes a NotSent ErrNotSent.
func (n *NotSent) Is(target error) bool { return target == ErrNotSent }

// Unwrap is the error the request came back with: DeadlineExceeded, the rate
// limiter's refusal, the dial's.
func (n *NotSent) Unwrap() error { return n.Err }

// AsNotSent reports whether err is a request that never left, and where.
func AsNotSent(err error) (*NotSent, bool) {
	var notSent *NotSent
	if errors.As(err, &notSent) {
		return notSent, true
	}
	return nil, false
}

// expired is ctx.Err(), or context.DeadlineExceeded once ctx's deadline has
// passed and its timer has not fired yet.
//
// On time.Now on purpose, unlike internal/actions' drain clock: this
// predicts what the client will do with ctx, and the rate limiter and
// net/http read the deadline against the real clock. A test clock here
// would hold back a request the client would have sent.
func expired(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

// Evict posts an Eviction to a pod's `eviction` subresource after the review
// (`create` on `pods/eviction`, by name). The API server's error comes back
// as it came: a 429 is a PodDisruptionBudget working.
//
// A context that ran out during the review sends nothing and says so. The
// review runs on its own timeout and can answer after the caller's deadline,
// and on the desktop an unreviewable answer still goes; client-go would then
// fail with the same DeadlineExceeded an unanswered request gets. The
// deadline is read, not only ctx.Err(), because the timer that sets Err
// fires late on a loaded machine. So ErrNotSent always means nothing was
// sent.
func Evict(ctx context.Context, where Location, target Target, eviction *unstructured.Unstructured) (access.Decision, error) {
	return perform(ctx, where, target, "create", "eviction", false, func(ctx context.Context) error {
		if err := expired(ctx); err != nil {
			return &NotSent{At: HeldAfterReview, Err: err}
		}
		iface, err := resourceOf(where, target)
		if err != nil {
			return err
		}
		_, err = iface.Create(ctx, eviction, metav1.CreateOptions{}, "eviction")
		// A POST that creates nothing that could already exist: an eviction's
		// 409 is a Conflict with the pod as it stands.
		return onTheWire(ctx, changesObject, err)
	})
}

// onTheWire is err as what became of the request on its way out, from what
// the client path recorded on ctx's stamp (access.Delivery), never from the
// error's text:
//
//   - Nothing left: a *NotSent. The rate limiter's refusal reads as neither
//     context error, and its bare ctx.Err() or a timed-out dial would read as
//     sent and unanswered.
//   - An answer that the API server timed out: a *TimedOut, whatever came
//     after it (a retry lost to the deadline, a 409 for the create the first
//     attempt may have made). The request may still be carried out.
//   - The API server's own answer: err as it came.
//   - Every attempt that left answered 429: the 429. client-go retries a 429
//     with Retry-After (a PodDisruptionBudget's), and a retry cut by the
//     deadline loses it; the budget refused every time, so the pod is not
//     going. Every answer, not the last: "500 ServerTimeout, then 429" may
//     have been carried out.
//   - Every attempt answered and the last body lost: that answer, from its
//     status (a cut 403 is still a 403). A cut 2xx stays unknown.
//   - Anything else that left: an *Unanswered (a connection dropped after
//     the write), never a refusal.
//   - Nothing seen at all (only a test's fake has no Guard): err as it came.
//
// The transport counts every doubt as sent (access/wire.go).
//
// what is what the request does to its object: a 409 rebuilt from a status
// is AlreadyExists for a create and a Conflict otherwise.
func onTheWire(ctx context.Context, what requestKind, err error) error {
	if err == nil || errors.Is(err, access.ErrNotReviewed) {
		// The guard's own refusal is already "not sent".
		return err
	}
	delivery := access.DeliveryOf(ctx)
	switch {
	case delivery.NotSent():
		at := HeldBeforeConnection
		if delivery.HeldBeforeTransport() {
			at = HeldBeforeTransport
		}
		return &NotSent{At: at, Err: err}
	case delivery.AnsweredTimedOut():
		return &TimedOut{Status: delivery.TimedOutStatus, Reason: timedOutReason(err, delivery.TimedOutStatus), Err: err}
	case apierrors.IsTimeout(err):
		return &TimedOut{Status: http.StatusGatewayTimeout, Reason: metav1.StatusReasonTimeout, Err: err}
	case apierrors.IsServerTimeout(err):
		return &TimedOut{Status: http.StatusInternalServerError, Reason: metav1.StatusReasonServerTimeout, Err: err}
	case isAPIStatus(err):
		return err
	case delivery.AnsweredOnly(http.StatusTooManyRequests):
		attempts := "the request"
		if delivery.Answered > 1 {
			attempts = fmt.Sprintf("each of the %d attempts client-go made", delivery.Answered)
		}
		return apierrors.NewTooManyRequests(fmt.Sprintf("the API server answered %s with 429 Too Many Requests, "+
			"and the time ran out while client-go waited to ask again", attempts), 0)
	}
	if status, ok := delivery.LastStatus(); ok && status >= http.StatusBadRequest {
		return lostBody(status, what, delivery.Cut, err)
	}
	if delivery.Left > 0 {
		return &Unanswered{Err: err}
	}
	return err
}

// requestKind is what a write does to its object, as far as reading an
// answer rebuilt from its status needs to know (onTheWire, lostBody).
type requestKind int

const (
	// changesObject is a patch, a delete or an eviction: a request about an
	// object that exists.
	changesObject requestKind = iota
	// createsObject is a create, whose 409 says the name is taken.
	createsObject
)

// timedOutReason is the reason of the answer that left the request
// unfinished, where err is that answer; empty where it is not.
func timedOutReason(err error, status int) metav1.StatusReason {
	var answer apierrors.APIStatus
	if errors.As(err, &answer) && int(answer.Status().Code) == status {
		return answer.Status().Reason
	}
	return ""
}

// lostBody is the API server's answer of status when the rest of it cannot be
// read (its body cut, or dropped by client-go): the status and the reason
// apierrors reads from an empty body, with a message saying so. err is not
// wrapped: the answer is the status, and a caller finding a context error
// would read an answered request as unanswered.
//
// what decides a 409: AlreadyExists for a create, a Conflict otherwise (an
// eviction is a POST too, but creates nothing that could exist). A cut body's
// details (a 422's causes) cannot be rebuilt.
func lostBody(status int, what requestKind, cut bool, err error) error {
	verb := ""
	if what == createsObject {
		// apierrors reads a 409 to a POST as AlreadyExists.
		verb = http.MethodPost
	}
	answer := apierrors.NewGenericServerResponse(status, verb, schema.GroupResource{}, "", "", 0, false)
	rest := "nothing more came of it"
	if cut {
		rest = "the rest of its answer was cut off"
	}
	answer.ErrStatus.Message = fmt.Sprintf("the API server answered %d %s, and %s (%s)",
		status, answer.ErrStatus.Reason, rest, textutil.FirstLineN(err.Error(), refusalLine))
	return answer
}

// isAPIStatus reports whether err is something the API server said.
func isAPIStatus(err error) bool {
	var status apierrors.APIStatus
	return errors.As(err, &status)
}

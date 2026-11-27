package access

// The audit record, written at the access choke point.
//
// Perform is the one place that holds the identity, the verb, the resource,
// the review's answer and the outcome together, so it writes the records
// itself and no write path has to remember to. internal/edit's `write:` lines
// are for an operator reading the process log; they are not the record.
//
// Two acts that are not a Perform write through the same functions: a reveal
// (a value the console can see and the person may not, recorded before it is
// read), and an exec refused before its stream, reviewed early so the refusal
// reaches the browser as a sentence.
//
// On the desktop a nil log writes nothing (stdout is a NULL handle on
// Windows). Under the server profile a gate with no log refuses every act as
// unrecorded: a server wired without its log stops acting, loudly, rather
// than acting with no record.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/identity"
)

type dryRunKey struct{}

// DryRun is ctx marked as carrying a request sent with `dryRun=All`, so that
// Perform's records do not claim a change that did not happen.
func DryRun(ctx context.Context) context.Context {
	return context.WithValue(ctx, dryRunKey{}, true)
}

func isDryRun(ctx context.Context) bool {
	dry, _ := ctx.Value(dryRunKey{}).(bool)
	return dry
}

type forceKey struct{}

// Forced is ctx marked as carrying a delete with a grace period of zero
// ("Force delete"), so that Perform's records tell it from an ordinary
// delete: they are different acts on a running workload.
func Forced(ctx context.Context) context.Context {
	return context.WithValue(ctx, forceKey{}, true)
}

// IsForced says whether ctx was marked by Forced. Both the audit record and
// the process log's `write:` lines read it, so the two cannot disagree.
func IsForced(ctx context.Context) bool {
	forced, _ := ctx.Value(forceKey{}).(bool)
	return forced
}

// actOf names the act a reviewed request is: a shell or a tunnel by its pod
// subresource, a write otherwise.
func actOf(attrs Attributes) string {
	if attrs.Group == "" && attrs.Resource == "pods" {
		switch attrs.Subresource {
		case "exec", "attach":
			return audit.Exec
		case "portforward":
			return audit.PortForward
		}
	}
	return audit.Write
}

// Record is the audit record of act on cluster, about attrs, as decision
// answered it, attributed to the identity ctx carries. Begin writes it; a
// caller may add what only it knows (a reveal's key) in between.
//
// An act the console makes for itself (AsConsole) is recorded as the
// console's, `via: console` with the person as `onBehalfOf`: it was reviewed
// as the console's rights, and written as the person's it would read as them
// acting on an answer about somebody else.
func (g *Gate) Record(ctx context.Context, act, cluster string, attrs Attributes, decision Decision) audit.Record {
	r := audit.Record{
		Act: act, Cluster: cluster,
		Verb: attrs.Verb, Group: attrs.Group, Version: attrs.Version, Resource: attrs.Resource,
		Subresource: attrs.Subresource, Namespace: attrs.Namespace, Name: attrs.Name,
		DryRun:   isDryRun(ctx),
		Force:    IsForced(ctx),
		Decision: string(decision),
	}
	if n, ok := ctx.Value(noteKey{}).(Note); ok {
		r.Extension, r.Command = n.Extension, n.Command
		r.Image, r.Target, r.Container = n.Image, n.Target, n.Container
	}
	if actingAsConsole(ctx) {
		r.AttributeToConsole(ctx)
	} else {
		r.Attribute(ctx)
	}
	return r
}

// Note is what only the caller of an act knows and its records carry: the
// extension that asked, and the command it ran. Perform builds its records
// itself, so a note travels on the context it is handed.
type Note struct {
	// Extension is the key of the extension that asked, through extension.Host.
	Extension string
	// Command is the argv of a command run in a pod, as recorded. Never a
	// secret: whatever is here is written to the audit log.
	Command string
	// Image, Target and Container describe a debug container being added: its
	// image, the container it targets, and its own name (audit.Record).
	Image     string
	Target    string
	Container string
}

type noteKey struct{}

// WithNote has every record made on ctx carry n.
func WithNote(ctx context.Context, n Note) context.Context {
	return context.WithValue(ctx, noteKey{}, n)
}

// Audited is whether the gate writes audit records at all.
func (g *Gate) Audited() bool { return g != nil && g.audit != nil }

// AuditBroken is why the gate's audit log stopped writing, or nil while it
// writes or where there is none. The readiness probe reads it.
func (g *Gate) AuditBroken() error {
	if g == nil {
		return nil
	}
	return g.audit.Broken()
}

// Begin writes r's attempt record. A non-nil error refuses the act: it is an
// *Unrecorded, already logged, and the caller must not make the request.
// Under the server profile a gate with no log answers one for every act.
func (g *Gate) Begin(ctx context.Context, r audit.Record) (*audit.Pending, error) {
	var log *audit.Log
	if g != nil {
		log = g.audit
	}
	if log == nil && g != nil && g.inCluster {
		return nil, g.unrecorded(ctx, r, errNoLog)
	}
	pending, err := log.Begin(r)
	if err != nil {
		return nil, g.unrecorded(ctx, r, err)
	}
	return pending, nil
}

// End writes the outcome record of an act Begin recorded. A failure is only
// logged: the act has happened, and refusing its result would say it did not.
func (g *Gate) End(ctx context.Context, p *audit.Pending, outcome, reason string) {
	if err := p.End(outcome, reason); err != nil && g != nil && g.logf != nil {
		g.logf("%s", identity.Line(ctx, "audit: the outcome record of act %s (outcome=%s) could not be written; "+
			"the request had been sent: %s", p.ID(), outcome, firstLine(err.Error())))
	}
}

var errNoLog = errors.New("this console was assembled without an audit log, and under the server profile it does not act unrecorded")

func (g *Gate) unrecorded(ctx context.Context, r audit.Record, err error) error {
	u := &Unrecorded{Cluster: r.Cluster, Attributes: attributesOfRecord(r), Err: err}
	if g != nil && g.logf != nil {
		// After the attributes, as the write door's lines say it.
		forced := ""
		if r.Force {
			forced = " force=true"
		}
		g.logf("%s", identity.Line(ctx, "unrecorded: cluster=%q act=%s %s%s decision=%s reason=%s",
			r.Cluster, r.Act, u.Attributes, forced, r.Decision, firstLine(err.Error())))
	}
	return u
}

func attributesOfRecord(r audit.Record) Attributes {
	return Attributes{Verb: r.Verb, Group: r.Group, Version: r.Version, Resource: r.Resource,
		Subresource: r.Subresource, Namespace: r.Namespace, Name: r.Name}
}

// ErrUnrecorded is what an act refused for want of its record unwraps to.
var ErrUnrecorded = errors.New("the audit record could not be written")

// Unrecorded is an act refused because its attempt record could not be
// written. Not a Refused: nobody said no. It reads as the console being
// unable to act (a 503), like a review that could not be made.
type Unrecorded struct {
	Cluster    string
	Attributes Attributes
	Err        error
}

func (u *Unrecorded) Error() string {
	what := strings.TrimSpace(u.Attributes.Verb + " " + qualified(u.Attributes))
	if u.Attributes.Name != "" {
		what += " " + u.Attributes.Name
	}
	if u.Attributes.Namespace != "" {
		what += " in namespace " + u.Attributes.Namespace
	}
	return fmt.Sprintf("the console did not %s on cluster %q: it could not write the audit record of it first, "+
		"and it does nothing it has not recorded: %s", what, u.Cluster, firstLine(u.Err.Error()))
}

func (u *Unrecorded) Is(target error) bool { return target == ErrUnrecorded }

func (u *Unrecorded) Unwrap() error { return u.Err }

// Status is the refusal as an outage: a 503, so readers that tell a 403 from
// an outage read it as the second.
func (u *Unrecorded) Status() metav1.Status {
	return metav1.Status{Status: metav1.StatusFailure, Code: http.StatusServiceUnavailable,
		Reason: metav1.StatusReasonServiceUnavailable, Message: u.Error()}
}

// knownReasons are the StatusReasons the API server itself writes.
//
// Only these, because `reason` is open on the wire: an admission webhook's
// Reason is whatever it wrote, possibly a value it refused (`password
// hunter2 is too short`), which a record must not copy. Anything else is
// `error`.
var knownReasons = map[metav1.StatusReason]bool{
	metav1.StatusReasonUnauthorized: true, metav1.StatusReasonForbidden: true,
	metav1.StatusReasonNotFound: true, metav1.StatusReasonAlreadyExists: true,
	metav1.StatusReasonConflict: true, metav1.StatusReasonGone: true,
	metav1.StatusReasonInvalid: true, metav1.StatusReasonServerTimeout: true,
	metav1.StatusReasonStoreReadError: true, metav1.StatusReasonTimeout: true,
	metav1.StatusReasonTooManyRequests: true, metav1.StatusReasonBadRequest: true,
	metav1.StatusReasonMethodNotAllowed: true, metav1.StatusReasonNotAcceptable: true,
	metav1.StatusReasonRequestEntityTooLarge: true, metav1.StatusReasonUnsupportedMediaType: true,
	metav1.StatusReasonInternalError: true, metav1.StatusReasonExpired: true,
	metav1.StatusReasonServiceUnavailable: true,
}

// Unsent is the failure word for a request Guard held back because no review
// covered it. A security signal (a defect in the console or an extension
// writing around the door), so a request held for ordinary reasons is Held.
const Unsent = "unsent"

// Held is the failure word for a reviewed request that never left this
// process for an ordinary reason (ErrNotSent): its time ran out, the rate
// limit held it, or no connection was made. Routine on a busy cluster, so
// not Unsent, which an alert may be keyed on.
const Held = "held"

// Unanswered is the failure word for a request that left and whose answer
// never came back, with no context of ours run out (ErrUnanswered).
const Unanswered = "unanswered"

// TimedOut is the failure word for a request the API server answered without
// finishing it (ErrTimedOut). Not Unanswered, and not a refusal: the change
// may still happen, which the answer's own `Timeout` reason would hide from
// a reader counting refusals.
const TimedOut = "timed-out"

// FailureWord names what went wrong with a request without quoting anything:
// the API server's reason from the closed list it writes (knownReasons), or
// `unsent`, `held`, `unrecorded`, `timed-out`, `cancelled` (sent and cut by a
// context), `unanswered`, or `error`.
//
// Order matters. `timed-out` comes before the API reasons and context errors:
// an attempt that timed out and was retried and cut carries DeadlineExceeded,
// and the change may still happen. ErrNotSent comes before the context errors
// because it wraps one, and `cancelled` would record a request never made.
// ErrUnanswered comes after them, so one cut by our deadline stays
// `cancelled`.
//
// A word, never the message: a validation error or a webhook may quote the
// value it refused.
func FailureWord(err error) string {
	switch {
	case errors.Is(err, ErrNotReviewed):
		return Unsent
	case errors.Is(err, ErrNotSent):
		return Held
	case errors.Is(err, ErrUnrecorded):
		return "unrecorded"
	case errors.Is(err, ErrTimedOut), apierrors.IsTimeout(err), apierrors.IsServerTimeout(err):
		return TimedOut
	}
	if reason := apierrors.ReasonForError(err); knownReasons[reason] {
		return string(reason)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "cancelled"
	}
	if errors.Is(err, ErrUnanswered) {
		return Unanswered
	}
	return "error"
}

// Administer reviews and records a write to the console's own state that one
// person makes for everybody on a cluster (an extension turned on or off, or
// one of its settings) as Perform does a request: a refusal, an unreviewable
// answer or an unwritable record refuses the act before do runs.
//
// change says in words what the act changes. do answers whether the write
// took effect, which is not the same as no error: a choice in effect that
// could not be written down is applied, and the record says so.
func (g *Gate) Administer(ctx context.Context, cluster string, attrs Attributes, change string,
	do func() (applied bool, err error)) (Answer, error) {
	return g.AdministerAnswered(ctx, cluster, attrs, g.Review(ctx, cluster, attrs), change, do)
}

// AdministerAnswered is Administer for a caller that has already reviewed
// attrs and holds the answer: the write is recorded and done on it, with no
// second review (#157).
func (g *Gate) AdministerAnswered(ctx context.Context, cluster string, attrs Attributes, answer Answer, change string,
	do func() (applied bool, err error)) (Answer, error) {
	r := g.Record(ctx, audit.Extension, cluster, attrs, answer.Decision)
	r.Change = change
	pending, unrecorded := g.Begin(ctx, r)
	answer.Record = pending.ID()
	if !g.lets(answer) {
		g.End(ctx, pending, audit.Refused, "")
		return answer, &Refused{Cluster: cluster, Attributes: attrs, Answer: answer}
	}
	if unrecorded != nil {
		return answer, unrecorded
	}
	applied, err := do()
	switch {
	case applied:
		g.End(ctx, pending, audit.Applied, "")
	default:
		g.End(ctx, pending, audit.Failed, "error")
	}
	return answer, err
}

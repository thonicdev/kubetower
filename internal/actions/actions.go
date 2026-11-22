// Package actions is the verbs: the things an operator does *to* a resource
// rather than reads about it.
//
// internal/edit sends patches an operator typed; here the patch is implied by
// a word ("restart" is one timestamped annotation, "cordon" one boolean). The
// operator never sees the JSON, so each one is spelled out here and pinned by
// a test, byte for byte, against what kubectl writes.
//
//   - Every action goes through edit.Writer.Locate first, the one
//     implementation of the `KT_READONLY_CLUSTERS` guardrail; tests assert a
//     marked cluster is refused and that nothing was sent.
//   - Evict is not delete. Evict POSTs a `policy/v1` Eviction, which the API
//     server refuses with 429 when a PodDisruptionBudget would be violated; an
//     Evict that deleted anyway would betray the safety asked for. The two
//     never share a code path, and a test asserts each sends its own request.
//   - A patch is dry-run before it is committed; a delete and an eviction are
//     not. If a server quietly ignored DryRun on those, the "validation" would
//     be the deletion, so they are one deliberate request each behind a
//     confirmation.
//   - Confirmation is checked here, not only in the form: Run refuses a
//     request whose Confirm does not match SingleConfirm.
package actions

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/edit"
	"github.com/thonicdev/kubetower/textutil"
)

// Verb is one action. The strings are what the URL carries, so they are stable.
type Verb string

const (
	// Delete removes the object. Irreversible, typed confirmation.
	Delete Verb = "delete"
	// Restart means "start this again", and what it *is* depends on the kind: a
	// rollout restart on a controller, a delete on a Pod. See restart().
	Restart Verb = "restart"
	// Evict asks the API server to remove a pod subject to its PodDisruptionBudget.
	Evict Verb = "evict"
	// Cordon and Uncordon set spec.unschedulable on a Node.
	Cordon   Verb = "cordon"
	Uncordon Verb = "uncordon"
	// Drain is cordon, then a plan, then a second confirmation, then evictions.
	Drain Verb = "drain"
	// Suspend and Resume set spec.suspend on a CronJob.
	Suspend Verb = "suspend"
	Resume  Verb = "resume"
	// Trigger creates a Job from a CronJob's jobTemplate, the way
	// `kubectl create job --from=cronjob/x` does.
	Trigger Verb = "trigger"
	// Scale sets spec.replicas.
	Scale Verb = "scale"
)

// RestartedAtAnnotation is what `kubectl rollout restart` writes. A rollout
// restart is this annotation changing value, so a typo here would patch an
// annotation nothing reads and report success.
const RestartedAtAnnotation = "kubectl.kubernetes.io/restartedAt"

// InstantiateAnnotation is what `kubectl create job --from=cronjob/x` puts on
// the Job it makes, so a manually triggered run is distinguishable from one the
// schedule produced.
const InstantiateAnnotation = "cronjob.kubernetes.io/instantiate"

// ConfirmToken is what the second step of a non-typed action sends back. Not
// a secret: it tells a request from the server-rendered confirmation from a
// single click, which the UI must be unable to skip.
const ConfirmToken = "confirm"

// StageEvict is the second stage of a drain, the one that removes pods.
// Without it a drain cordons and reports: the URL that shows what would
// happen cannot be the one that makes it happen.
const StageEvict = "evict"

// Target is where an action lands. Aliased rather than redeclared so a caller
// holds one type across `internal/edit` and this package.
type Target = edit.Target

// Request is one action, as it arrives from the panel.
type Request struct {
	Target
	Verb Verb
	// Confirm is SingleConfirm's answer for this object; see checkConfirm.
	Confirm string
	// Stage is "" for the first step of a drain and StageEvict for the second.
	Stage string
	// Replicas is the target for Scale, a pointer because nil is not zero:
	// a request that never stated a count must not scale a workload to
	// nothing. nil is refused before anything is sent; a deliberate zero is a
	// pointer to zero, and patches zero.
	Replicas *int64
	// Precondition is the pin's value the panel was drawn from (PinOf's
	// Value when the plan was made). Empty means no precondition.
	//
	// The resourceVersion for a pod and a kind with no generation; the
	// scheduling fields for a node; the generation otherwise, because the
	// version moves on every status write and a confirmation refused for a
	// heartbeat protects nothing (pin.go). Preview keeps it in Recap.Pin, the
	// plan states it, and the act carries it back here. Every verb checks it
	// through stale.
	//
	// Optional: a bulk run (as many versions as objects) and a drain's second
	// stage pass nothing and rely on the UID precondition.
	Precondition string
	// PreconditionUID is the `metadata.uid` of the object the plan was made on
	// (PinOf's UID). Empty means none. stale refuses another UID, and
	// deleteObject sends it as the delete's UID precondition: a recreated
	// object can have the same generation, and a re-registered node the same
	// taints.
	PreconditionUID string
	// IncludeUnowned is a drain's `--force`: evict the pods nothing owns as
	// well (drain.go). Refused on every other verb by internal/web.
	IncludeUnowned bool
	// Force deletes with a grace period of zero, as `kubectl delete
	// --grace-period=0`. A single delete's only: Run refuses it on any other
	// verb, so a pod restart (a delete) never carries it by accident.
	Force bool
}

// Result is what happened, in the words the panel puts on screen.
type Result struct {
	Target
	Verb Verb
	// Message is the sentence, in the past tense. A Restart on a Pod says
	// "deleted" (see restart).
	Message string
	// Plan is filled by a drain: what would be evicted, what would not, and
	// after StageEvict what each eviction came back with.
	Plan *DrainPlan
}

// Options are what a Runner needs.
type Options struct {
	// Writer is the write path and the guardrail. A nil Writer makes every
	// action refuse rather than reach a cluster unguarded.
	Writer *edit.Writer
	// Now is the clock for the rollout-restart stamp and a triggered Job's
	// name. Nil means time.Now; tests fix it to assert exact bytes.
	Now func() time.Time
}

type Runner struct {
	writer *edit.Writer
	now    func() time.Time
}

func New(opts Options) *Runner {
	r := &Runner{writer: opts.Writer, now: opts.Now}
	if r.now == nil {
		r.now = time.Now
	}
	return r
}

// Typed reports whether this verb, on this kind, is confirmed by typing
// rather than by pressing a button.
//
// Typing fails when the drawer is open on the wrong object, the mistake that
// actually happens; a yes/no gets clicked through. Delete and drain are
// typed. Restart is typed on a Pod because restarting a pod is deleting it:
// judged by the verb alone, one pod would need a typed name to delete and a
// click to restart, and in bulk two hundred pods likewise. An unknown kind
// is read as needing the typing, the careful reading.
func Typed(verb Verb, kind string) bool {
	switch verb {
	case Delete, Drain:
		return true
	case Restart:
		return kind == "" || looksLikePod(kind)
	}
	return false
}

// ByCount reports whether one object's typed confirmation is the number of
// objects rather than the object's own name: pods only, so one pod types
// "1".
//
// A pod's name (`api-7d9f4c8b6d-x4k2p`) is unreadable, so it gets copied and
// pasted, which is as true of the wrong row as the right one. A count is
// already how a bulk delete of pods is confirmed (BulkConfirm). Every kind
// whose name a person chose keeps the name, which typing back is a real
// check.
//
// It depends on (verb, kind), not the object, because the confirmation is
// drawn without reading the object; a hand-named pod (`kubectl run nginx`)
// is therefore included, accepted since the dialog names it either way.
// Drain is excluded explicitly: Typed(Drain, "pods") is true, and a drain's
// subject is a node with a chosen name.
func ByCount(verb Verb, kind string) bool {
	if !Typed(verb, kind) {
		return false
	}
	switch verb {
	case Delete, Restart:
		return looksLikePod(kind)
	}
	return false
}

// SingleConfirm is what one object's confirmation has to carry: the count for a
// pod, the object's own name for everything else typed, and the token for the
// rest.
//
// One function for the confirmation drawn and the check the commit meets,
// so the box cannot ask for one thing while the server insists on another.
func SingleConfirm(verb Verb, kind, name string) string {
	switch {
	case !TypedOne(verb, kind):
		return ConfirmToken
	case ByCount(verb, kind):
		// One object, spelled as a number: the shape BulkConfirm produces
		// for a selection of one.
		return "1"
	default:
		return name
	}
}

// TypedOne reports whether one object's confirmation is typed: Typed, less a
// single delete, which is confirmed by a button. A delete over a selection
// keeps its typed count (BulkConfirm reads Typed), as do a drain and a pod
// restart.
func TypedOne(verb Verb, kind string) bool {
	return verb != Delete && Typed(verb, kind)
}

// looksLikePod accepts both vocabularies: isPod compares the API's singular
// Kind ("Pod") after a Locate, but confirmation happens before, on the URL's
// plural slug ("pods"). isPod("pods") is silently false, which would wave a
// bulk restart of two hundred pods through on a click.
func looksLikePod(kind string) bool {
	lower := strings.ToLower(strings.TrimSpace(kind))
	return lower == "pod" || lower == "pods" || lower == "po"
}

// Known reports whether this is a verb at all, so a handler can refuse an
// unknown one before it looks anything up.
func Known(verb Verb) bool {
	switch verb {
	case Delete, Restart, Evict, Cordon, Uncordon, Drain, Suspend, Resume, Trigger, Scale:
		return true
	}
	return false
}

// Run performs one action.
//
// Resolve and apply the guardrail, then check the confirmation, then act.
// Checking the confirmation first would answer a request against a
// read-only cluster with "type the name to confirm", inviting it.
func (r *Runner) Run(ctx context.Context, req Request) (Result, error) {
	if r.writer == nil {
		return Result{}, edit.Refuse(edit.RefusedInput, "this build has no write path, so no action can be performed")
	}
	if !Known(req.Verb) {
		return Result{}, edit.Refuse(edit.RefusedInput, "%q is not an action this console performs", string(req.Verb))
	}
	if req.Force && req.Verb != Delete {
		return Result{}, edit.Refuse(edit.RefusedInput, "force is a delete's, and %q is not a delete", string(req.Verb))
	}

	// Resolves the context name and refuses a read-only cluster: nothing
	// below is reachable on one.
	where, err := r.writer.Locate(ctx, req.Target)
	if err != nil {
		return Result{}, err
	}
	if err := checkConfirm(req); err != nil {
		return Result{}, err
	}

	switch req.Verb {
	case Delete:
		return r.deleteObject(ctx, where, req)
	case Restart:
		return r.restart(ctx, where, req)
	case Evict:
		return r.evict(ctx, where, req)
	case Cordon:
		return r.cordon(ctx, where, req, true)
	case Uncordon:
		return r.cordon(ctx, where, req, false)
	case Drain:
		return r.drain(ctx, where, req)
	case Suspend:
		return r.suspend(ctx, where, req, true)
	case Resume:
		return r.suspend(ctx, where, req, false)
	case Trigger:
		return r.trigger(ctx, where, req)
	case Scale:
		return r.scale(ctx, where, req)
	}
	return Result{}, edit.Refuse(edit.RefusedInput, "%q is not an action this console performs", string(req.Verb))
}

// checkConfirm is the second half of the confirmation, the half a hand-made
// POST cannot skip.
func checkConfirm(req Request) error {
	if TypedOne(req.Verb, req.Kind) {
		want := SingleConfirm(req.Verb, req.Kind, req.Name)
		if strings.TrimSpace(req.Confirm) == want {
			return nil
		}
		if ByCount(req.Verb, req.Kind) {
			return edit.Refuse(edit.RefusedInput,
				"this is not undoable, so it is confirmed by size: type %s — the number of "+
					"objects this %ss — to confirm", want, string(req.Verb))
		}
		return edit.Refuse(edit.RefusedInput,
			"this is not undoable, so it is confirmed by name: type %q exactly to %s it",
			req.Name, string(req.Verb))
	}
	if req.Confirm != ConfirmToken {
		return edit.Refuse(edit.RefusedInput,
			"this action was not confirmed. Open it from the panel, read what it will do, and confirm there")
	}
	return nil
}

// stale is the precondition every single-object verb checks, on the object
// it has already read: the act carries the version the confirmation was
// drawn from, so a cordon confirmed against a node since rewritten does not
// go through. Empty means none (a bulk run, a drain's second stage).
//
// It compares the pin (pin.go), not the resourceVersion: a kubelet status
// write since the plan does not make it another node. The UID is compared
// first: a recreate restarts the generation and a re-registered node keeps
// its taints.
func stale(req Request, object *unstructured.Unstructured, where edit.Location) error {
	if live := string(object.GetUID()); req.PreconditionUID != "" && live != req.PreconditionUID {
		return edit.Refuse(edit.RefusedConflict,
			"this %s changed while you had it open: it was deleted and created again under the "+
				"same name, so it is not the object the plan was made on — its metadata.uid was %q "+
				"and is %q now. Read the fresh plan and confirm it again",
			strings.ToLower(where.Kind), req.PreconditionUID, live)
	}
	if req.Precondition == "" {
		return nil
	}
	pin := PinOf(where.Kind, object)
	if pin.Value != req.Precondition {
		return edit.Refuse(edit.RefusedConflict,
			"this %s changed since the plan was made: its %s %s %q and %s %q now. "+
				"Read the fresh plan and confirm it again",
			strings.ToLower(where.Kind), pin.On, wasOrWere(pin.On), req.Precondition, isOrAre(pin.On), pin.Value)
	}
	return nil
}

// wasOrWere and isOrAre agree the verb with what the pin is on: "its
// metadata.generation was", "its spec.unschedulable and spec.taints were".
func wasOrWere(on string) string {
	if strings.Contains(on, " and ") {
		return "were"
	}
	return "was"
}

func isOrAre(on string) string {
	if strings.Contains(on, " and ") {
		return "are"
	}
	return "is"
}

// --- delete and evict ---------------------------------------------------------

// controllerKind is the Kind of the ownerReference marked controller, or "" when
// nothing owns this object. Only the controller counts: a pod can carry several
// ownerReferences and at most one of them is the thing that refills the count.
func controllerKind(object *unstructured.Unstructured) string {
	for _, owner := range object.GetOwnerReferences() {
		if owner.Controller != nil && *owner.Controller {
			return owner.Kind
		}
	}
	return ""
}

// cannotRestart says why, in the operator's terms rather than the API's, and says
// what to do instead. A refusal that only says no teaches nothing.
func cannotRestart(name, owner string) string {
	switch owner {
	case "":
		return fmt.Sprintf("nothing owns pod %s, so deleting it — which is all restarting a pod "+
			"can mean — would remove it for good. Delete it deliberately if that is what you want.", name)
	case "Job":
		return fmt.Sprintf("pod %s belongs to a Job. Deleting it spends one of the Job's retries "+
			"and at its backoffLimit the Job fails, so this would not restart anything — it could "+
			"end the run. Delete the Job's pod deliberately, or use Run now on the CronJob.", name)
	}
	return fmt.Sprintf("pod %s is owned by a %s, which does not create a replacement when a pod "+
		"goes. Restarting it would delete it and leave nothing behind.", name, owner)
}

// deleteObject removes the object.
//
//   - The UID is a precondition: a pod deleted a second ago whose controller
//     made a replacement has the same name and is a different object. The
//     planned UID is sent, not the re-read one, which would be the
//     successor's after a recreate. With no planned UID (a bulk run) the
//     re-read one still refuses a recreate between the read and the delete.
//   - There is no dry run: if any server ignored DeleteOptions.DryRun, the
//     "validation" would be the deletion.
func (r *Runner) deleteObject(ctx context.Context, where edit.Location, req Request) (Result, error) {
	object, err := edit.Live(ctx, where, req.Target)
	if err != nil {
		return Result{}, edit.WrapGet(err, where.Kind)
	}
	// The version from the plan, carried through the confirmation, refuses an
	// object changed while the dialog was open, which the UID cannot see.
	if err := stale(req, object, where); err != nil {
		return Result{}, err
	}

	// stale has refused a different re-read UID, so the two are equal here;
	// the planned one is sent because it is the one the operator agreed to.
	uid := object.GetUID()
	if req.PreconditionUID != "" {
		uid = types.UID(req.PreconditionUID)
	}
	// Background, kubectl's default: the object goes now and its dependents
	// are collected behind it. Foreground would leave it Terminating on
	// screen, which reads as a delete that did not work.
	policy := deletePropagationBackground
	options := deleteOptions(&uid, &policy, req.Force)

	decision, err := edit.Delete(ctx, where, req.Target, options)
	if err != nil {
		if _, refused := edit.AsRefusal(err); refused {
			return Result{}, err
		}
		return Result{}, edit.APIRefusal(err)
	}
	// ` force=true` as on the door's lines, read from the options sent by
	// edit's own rule, so the lines cannot disagree.
	forced := ""
	if edit.ForcedDelete(options) {
		forced = " force=true"
	}
	r.writer.Logf(ctx, "write: cluster=%q kind=%s namespace=%s name=%s action=delete%s decision=%s outcome=applied",
		where.Cluster, where.Resource.Resource, req.Namespace, req.Name, forced, decision)

	return Result{
		Target: req.Target, Verb: req.Verb,
		Message: fmt.Sprintf("Deleted %s %s. Its dependents are collected in the background.",
			where.Kind, req.Name),
	}, nil
}

// evict asks the API server to remove a pod *subject to its PodDisruptionBudget*.
//
// A POST of a `policy/v1` Eviction to the pod's `eviction` subresource. When
// a PDB would be violated the API server answers 429 and the pod stays,
// which is why an operator reaches for this: a 429 is the feature working.
// No dry run (see the package comment).
func (r *Runner) evict(ctx context.Context, where edit.Location, req Request) (Result, error) {
	if !isPod(where.Kind) {
		return Result{}, edit.Refuse(edit.RefusedInput,
			"eviction is a pod thing: %s has no eviction subresource. To remove it, use Delete.", where.Kind)
	}
	object, err := edit.Live(ctx, where, req.Target)
	if err != nil {
		return Result{}, edit.WrapGet(err, where.Kind)
	}
	if err := stale(req, object, where); err != nil {
		return Result{}, err
	}
	decision, err := evictPod(ctx, where, req.Target)
	if err != nil {
		return Result{}, err
	}
	r.writer.Logf(ctx, "write: cluster=%q kind=%s namespace=%s name=%s action=evict decision=%s outcome=applied",
		where.Cluster, where.Resource.Resource, req.Namespace, req.Name, decision)

	return Result{
		Target: req.Target, Verb: req.Verb,
		Message: fmt.Sprintf("Evicted %s. The API server accepted the eviction, "+
			"so no PodDisruptionBudget was violated by it.", req.Name),
	}, nil
}

// evictPod is the one place an Eviction is built, so drain and the Evict
// button send the identical request, through edit.Evict's review.
func evictPod(ctx context.Context, pods edit.Location, target edit.Target) (access.Decision, error) {
	decision, err := edit.Evict(ctx, pods, target, newEviction(target.Namespace, target.Name))
	if err == nil {
		return decision, nil
	}
	// Never sent, said as such, not as a refusal and a 403 about a request
	// the cluster never saw.
	if held, ok := edit.AsNotSent(err); ok {
		return decision, edit.RefuseFrom(err, edit.RefusedNotSent, "%s", notSentSentence(held))
	}
	// Answered that the API server timed out: the eviction may still happen.
	// Before the 429 below, which a timed-out answer can wrap: the budget
	// refusing a retry does not undo the first try.
	if timedOut, ok := edit.AsTimedOut(err); ok {
		return decision, edit.RefuseFrom(err, edit.RefusedUnanswered,
			"The API server %s the eviction (%s), so it may still happen and whether the pod is "+
				"going is not known; its own page says.", timedOut.What(), timedOut.Answer())
	}
	// Sent, and no answer came back: whether the pod is going is not known.
	if unanswered, ok := edit.AsUnanswered(err); ok {
		return decision, edit.RefuseFrom(err, edit.RefusedUnanswered,
			"The eviction was sent and no answer came back (%s), so whether the pod is going is not "+
				"known; its own page says.", textutil.FirstLineN(unanswered.Err.Error(), refusalLine))
	}
	// The review's own refusal is already in this package's vocabulary.
	if _, refused := edit.AsRefusal(err); refused {
		return decision, err
	}
	// The refusal this endpoint exists to produce, in its own words, since
	// "the cluster refused the change" would read as a fault.
	if apierrors.IsTooManyRequests(err) {
		return decision, edit.Refuse(edit.RefusedByAPI,
			"a PodDisruptionBudget refused this eviction: %s. Nothing was removed — that is what "+
				"Evict is for. Delete removes the pod regardless of the budget.",
			textutil.FirstLineN(err.Error(), refusalLine))
	}
	return decision, edit.APIRefusal(err)
}

// notSentSentence is what the Evict button says of an eviction that never
// left the console, by where it was held.
func notSentSentence(held *edit.NotSent) string {
	switch held.At {
	case edit.HeldBeforeTransport:
		return "The eviction was not sent: the console's request limit for this cluster would have held it " +
			"past its deadline. Nothing was removed; evict it again."
	case edit.HeldBeforeConnection:
		return "The eviction was not sent: no connection to the API server could be made for it (" +
			textutil.FirstLineN(held.Err.Error(), refusalLine) + "). Nothing was removed."
	}
	return "The eviction was not sent: the request ended while the console was still checking whether " +
		"it may evict this pod. Nothing was removed; evict it again."
}

// --- the patch verbs ----------------------------------------------------------

// recreatingOwners are the controllers that make a replacement pod when one
// goes: they keep a count and refill it. A Job does not: deleting its pod
// spends a `backoffLimit` retry and can end the run. A pod with no
// controller is worse: Restart would be a permanent delete.
var recreatingOwners = map[string]bool{
	"ReplicaSet":            true,
	"StatefulSet":           true,
	"DaemonSet":             true,
	"ReplicationController": true,
}

// RestartsAPod reports whether deleting this pod would actually restart it.
//
// Exported so the drawer decides whether to draw the button from the same
// answer. Takes an ownerReference's kind ("Job", "ReplicaSet"); empty means
// no controller.
func RestartsAPod(ownerKind string) bool { return recreatingOwners[ownerKind] }

// restart is two different requests, and the wording says which.
//
// On a controller it is `kubectl rollout restart`: one timestamped
// annotation on the pod template, so the controller rolls its pods
// gradually, with a rollback available.
//
// A pod is not restarted but deleted, and its controller makes another. The
// confirmation and the result say "delete": thinking Restart gentler than
// Delete on a Pod costs a pod.
func (r *Runner) restart(ctx context.Context, where edit.Location, req Request) (Result, error) {
	if isPod(where.Kind) {
		// Refused server-side, not merely undrawn: a stale tab, a bookmarked
		// POST or curl reach here with no button.
		object, err := edit.Live(ctx, where, req.Target)
		if err != nil {
			return Result{}, edit.WrapGet(err, where.Kind)
		}
		if owner := controllerKind(object); !RestartsAPod(owner) {
			return Result{}, edit.Refuse(edit.RefusedInput, "%s", cannotRestart(req.Name, owner))
		}

		result, err := r.deleteObject(ctx, where, req)
		if err != nil {
			return Result{}, err
		}
		result.Message = fmt.Sprintf("Deleted pod %s — that is what restarting a pod is. "+
			"If a controller owns it, a replacement is being created now; if nothing owns it, nothing will.",
			req.Name)
		return result, nil
	}
	if !isRollout(where.Kind) {
		return Result{}, edit.Refuse(edit.RefusedInput,
			"a rollout restart applies to a Deployment, a StatefulSet or a DaemonSet; %s is none of those",
			where.Kind)
	}

	// Exactly what kubectl writes. RFC3339 to the second, so two restarts in
	// one second are a no-op, as with kubectl.
	stamp := r.now().Format(time.RFC3339)
	patch := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]any{RestartedAtAnnotation: stamp},
				},
			},
		},
	}
	if _, err := r.patch(ctx, where, req, patch, "restart"); err != nil {
		return Result{}, err
	}
	return Result{
		Target: req.Target, Verb: req.Verb,
		Message: fmt.Sprintf("Rollout restarted. %s %s was stamped %s=%s, so its controller replaces "+
			"its pods gradually rather than all at once.", where.Kind, req.Name, RestartedAtAnnotation, stamp),
	}, nil
}

// cordon sets spec.unschedulable. Uncordon is the same request with false,
// in one function so the path is spelled once.
func (r *Runner) cordon(ctx context.Context, where edit.Location, req Request, on bool) (Result, error) {
	if !isNode(where.Kind) {
		return Result{}, edit.Refuse(edit.RefusedInput,
			"cordoning is a node thing; %s has no spec.unschedulable", where.Kind)
	}
	patch := map[string]any{"spec": map[string]any{"unschedulable": on}}
	if _, err := r.patch(ctx, where, req, patch, string(req.Verb)); err != nil {
		return Result{}, err
	}
	if on {
		return Result{Target: req.Target, Verb: req.Verb, Message: fmt.Sprintf(
			"Cordoned %s. Nothing new will be scheduled onto it; what is already running stays where it is.",
			req.Name)}, nil
	}
	return Result{Target: req.Target, Verb: req.Verb, Message: fmt.Sprintf(
		"Uncordoned %s. It is schedulable again.", req.Name)}, nil
}

// suspend sets spec.suspend on a CronJob. Resume is the same field, false.
//
// Suspending stops new runs; it does nothing to a running Job, and the
// message says so because "suspend" sounds like pausing.
func (r *Runner) suspend(ctx context.Context, where edit.Location, req Request, on bool) (Result, error) {
	if !isCronJob(where.Kind) {
		return Result{}, edit.Refuse(edit.RefusedInput,
			"suspending is a CronJob thing; %s has no spec.suspend", where.Kind)
	}
	patch := map[string]any{"spec": map[string]any{"suspend": on}}
	if _, err := r.patch(ctx, where, req, patch, string(req.Verb)); err != nil {
		return Result{}, err
	}
	if on {
		return Result{Target: req.Target, Verb: req.Verb, Message: fmt.Sprintf(
			"Suspended %s. No new runs will start. A run already in flight is not affected — "+
				"delete its Job if you need it stopped.", req.Name)}, nil
	}
	return Result{Target: req.Target, Verb: req.Verb, Message: fmt.Sprintf(
		"Resumed %s. It will run on its schedule again; missed runs are not made up.", req.Name)}, nil
}

// maxReplicas is a typo guard (100 typed for 10), not a policy: the manifest
// editor takes any number.
const maxReplicas = 1000

func (r *Runner) scale(ctx context.Context, where edit.Location, req Request) (Result, error) {
	if !isScalable(where.Kind) {
		return Result{}, edit.Refuse(edit.RefusedInput,
			"%s has no spec.replicas to set", where.Kind)
	}
	// Before the bounds: "not said" is not a number, and both bounds pass a
	// zero nobody chose (see Request.Replicas).
	if req.Replicas == nil {
		return Result{}, edit.Refuse(edit.RefusedInput,
			"no replica count was stated, and a scale with no target is not a scale to zero. "+
				"Say how many replicas this should have")
	}
	replicas := *req.Replicas
	if replicas < 0 {
		return Result{}, edit.Refuse(edit.RefusedInput, "a replica count cannot be negative")
	}
	if replicas > maxReplicas {
		return Result{}, edit.Refuse(edit.RefusedInput,
			"%d replicas is past this field's ceiling of %d, which is here to catch a mis-typed digit. "+
				"If you mean it, set spec.replicas in the manifest editor", replicas, maxReplicas)
	}
	patch := map[string]any{"spec": map[string]any{"replicas": replicas}}
	if _, err := r.patch(ctx, where, req, patch, "scale"); err != nil {
		return Result{}, err
	}
	return Result{Target: req.Target, Verb: req.Verb, Message: fmt.Sprintf(
		"Scaled %s %s to %d replica(s).", where.Kind, req.Name, replicas)}, nil
}

// patch is validate-then-commit, as in internal/edit: a scale past a
// ResourceQuota or a restart blocked by an admission webhook is refused with
// the API server's sentence before anything changes.
func (r *Runner) patch(ctx context.Context, where edit.Location, req Request, body map[string]any, action string) (string, error) {
	object, err := edit.Live(ctx, where, req.Target)
	if err != nil {
		return "", edit.WrapGet(err, where.Kind)
	}
	// The object is in hand, so every patch verb gets the precondition.
	if err := stale(req, object, where); err != nil {
		return "", err
	}
	raw, err := marshal(body)
	if err != nil {
		return "", err
	}
	if _, _, err := edit.Send(ctx, where, req.Target, raw, true); err != nil {
		return "", err
	}
	applied, decision, err := edit.Send(ctx, where, req.Target, raw, false)
	if err != nil {
		return "", err
	}
	r.writer.Logf(ctx, "write: cluster=%q kind=%s namespace=%s name=%s action=%s decision=%s outcome=applied",
		where.Cluster, where.Resource.Resource, req.Namespace, req.Name, action, decision)
	return applied.GetResourceVersion(), nil
}

// --- kinds --------------------------------------------------------------------
//
// Matched on the singular Kind the REST mapper returned, so "pods", "Pod"
// and "pod" all arrive here as "Pod".

func isPod(kind string) bool     { return strings.EqualFold(kind, "Pod") }
func isNode(kind string) bool    { return strings.EqualFold(kind, "Node") }
func isCronJob(kind string) bool { return strings.EqualFold(kind, "CronJob") }

func isRollout(kind string) bool {
	return strings.EqualFold(kind, "Deployment") ||
		strings.EqualFold(kind, "StatefulSet") ||
		strings.EqualFold(kind, "DaemonSet")
}

// A DaemonSet is absent: it has no spec.replicas, so scaling it would write
// a field the API server drops.
func isScalable(kind string) bool {
	return strings.EqualFold(kind, "Deployment") ||
		strings.EqualFold(kind, "StatefulSet") ||
		strings.EqualFold(kind, "ReplicaSet") ||
		strings.EqualFold(kind, "ReplicationController")
}

// refusalLine bounds what the API server said when it refused a write: 300
// runes, longer than a status report's 200, because an admission or
// PodDisruptionBudget denial names the policy and the fix. Bounded because
// it lands in table cells (bulk results, the drain plan).
const refusalLine = 300

package actions

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/edit"
	"github.com/thonicdev/kubetower/textutil"
)

// Drain: the one action here that is several requests and can take a cluster
// down, so it is the hardest to do by accident. Three steps:
//
//  1. Cordon, always first: evicting before cordoning races the scheduler,
//     which puts the pods back. Idempotent and reversible, so the first
//     click may do it.
//  2. Plan: list what is on the node, say what would go and what would not,
//     and stop. Every pod is named, so an operator one row off in the node
//     list notices unexpected namespaces before anything is evicted.
//  3. Evict, only on a request carrying StageEvict and the node's name typed
//     out (a Typed verb, in both stages).
//
// What is skipped follows kubectl, whose behavior operators expect: pods
// owned by a DaemonSet (recreated at once on this node), mirror pods (the
// kubelet's static pods, which the API server cannot remove) and finished
// pods.
//
// A pod nothing owns is left alone unless the drain includes such pods
// (Request.IncludeUnowned, kubectl's `--force`). kubectl refuses the whole
// drain; here the plan is on screen, so the drain goes ahead without them and
// says the node is not empty. Evicting one deletes it for good (on
// docker-desktop, `storage-provisioner` and `vpnkit-controller`): spared by
// default, a wrong default costs a manual eviction instead of a pod nobody
// can bring back.
//
// A pod using emptyDir is evicted with a warning, where kubectl asks for
// `--delete-emptydir-data`: its controller makes another, the lost scratch
// space is named in the plan the operator is already reading.

// PodRef is one pod on the node being drained.
type PodRef struct {
	Namespace string
	Name      string
	// Controller is "DaemonSet/fluent-bit", or empty when nothing owns this pod.
	Controller string
	// Skip is why this pod will not be evicted. Empty means it will be.
	Skip string
	// Warning is why it deserves a second look first. Empty means it does
	// not.
	Warning string
	// Outcome is filled by the eviction stage: "evicted", or what the API server
	// said instead.
	Outcome string
	// Failed tells a refusal outcome from a success, so the template does
	// not parse English.
	Failed bool
	// NotAttempted, Unanswered and TimedOut say which kind of non-refusal
	// failure: never sent (the stage ran out or was canceled, before this
	// pod or during its review); sent and not answered in time; or answered
	// by the API server timing out (edit.TimedOut), which may still happen.
	// For the last two, whether the pod is going is not known.
	NotAttempted bool
	Unanswered   bool
	TimedOut     bool
}

// evictionTimeout bounds one eviction and drainBudget the whole eviction stage.
// Variables, so a test can shorten them; see evictAll.
var (
	evictionTimeout = 15 * time.Second
	drainBudget     = 2 * time.Minute
)

// DrainPlan is what the confirmation screen shows.
type DrainPlan struct {
	Node string
	// Cordoned is true once step 1 is done, which is before the plan is
	// computed.
	Cordoned bool
	// Evict are the pods that will go, Skip the ones that will not.
	Evict []PodRef
	Skip  []PodRef
	// Requested, Failed, NotAttempted, Unanswered and TimedOut are filled by
	// the eviction stage. Each of the last three is also Failed and none is
	// counted twice, so Refused is what remains.
	Requested    int
	Failed       int
	NotAttempted int
	Unanswered   int
	TimedOut     int
	// Note is filled when the list could be incomplete, which must never read
	// as "the node is empty".
	Note string
}

// Refused is the evictions the API server refused, and nothing else.
func (p DrainPlan) Refused() int { return p.Failed - p.NotAttempted - p.Unanswered - p.TimedOut }

// Warnings reports whether anything in the plan carries one, so the panel
// need not walk the list in a template.
func (p DrainPlan) Warnings() bool {
	for _, pod := range p.Evict {
		if pod.Warning != "" {
			return true
		}
	}
	return false
}

func (r *Runner) drain(ctx context.Context, where edit.Location, req Request) (Result, error) {
	if !isNode(where.Kind) {
		return Result{}, edit.Refuse(edit.RefusedInput, "only a node can be drained; %s is not one", where.Kind)
	}

	// Step 1, on both stages: a plan read while the node still accepts new
	// pods is out of date before it is read.
	if _, err := r.cordon(ctx, where, req, true); err != nil {
		return Result{}, err
	}

	plan, err := r.plan(ctx, req)
	if err != nil {
		return Result{}, err
	}
	plan.Cordoned = true

	// Step 2 shows and does not do: no eviction without a second request
	// that says so.
	if req.Stage != StageEvict {
		return Result{
			Target: req.Target, Verb: req.Verb, Plan: &plan,
			Message: firstStageMessage(req.Name, plan),
		}, nil
	}

	// Step 3. Each eviction is its own reviewed request with its own
	// `write:` line, so the drain's line is a summary, not spelled `write:`.
	r.evictAll(ctx, req, &plan)
	// refused= counts only the API server's refusals, so a line read for
	// PodDisruptionBudget refusals is not inflated by a clock or a 504.
	// timed_out= is last so earlier fields keep their places.
	refused := plan.Refused()
	r.writer.Logf(ctx, "drain: cluster=%q kind=%s name=%s pods=%d evicted=%d refused=%d unanswered=%d not_attempted=%d timed_out=%d",
		req.Cluster, "nodes", req.Name, len(plan.Evict), plan.Requested, refused, plan.Unanswered, plan.NotAttempted, plan.TimedOut)

	message := fmt.Sprintf("Requested %d eviction(s) on %s.", plan.Requested, req.Name)
	if refused > 0 {
		message += fmt.Sprintf(" %d were refused — see the list; a PodDisruptionBudget refusing one "+
			"is the budget working, and the drain can be run again once the budget allows it.", refused)
	}
	if plan.Unanswered > 0 {
		message += fmt.Sprintf(" %d were sent and not answered in time, so whether those pods are going "+
			"is not known — see the list; each pod's own page says.", plan.Unanswered)
	}
	if plan.TimedOut > 0 {
		message += fmt.Sprintf(" %d were answered by the API server timing out, so those evictions may still "+
			"happen and whether those pods are going is not known — see the list; each pod's own page says.", plan.TimedOut)
	}
	if plan.NotAttempted > 0 {
		message += fmt.Sprintf(" %d were not attempted: nothing was sent for them — see the list, and run "+
			"the drain again for them.", plan.NotAttempted)
	}
	if plan.Requested > 0 {
		message += " An accepted eviction is a deletion in progress, not a pod already gone: " +
			"each one still has its termination grace period to run out."
	}
	if spared := plan.SparedUnowned(); len(spared) > 0 {
		message += fmt.Sprintf(" %d pod(s) nothing owns stayed on %s, so it is not empty: %s. "+
			"Plan the drain again including the pods nothing owns to evict them.",
			len(spared), req.Name, strings.Join(spared, ", "))
	}
	return Result{Target: req.Target, Verb: req.Verb, Plan: &plan, Message: message}, nil
}

// SparedUnowned names (namespace/name) the pods this plan leaves alone only
// because nothing owns them, which a drain including unowned pods would
// evict.
func (p DrainPlan) SparedUnowned() []string {
	var names []string
	for _, pod := range p.Skip {
		if pod.Skip == UnownedSkip {
			names = append(names, pod.Namespace+"/"+pod.Name)
		}
	}
	return names
}

// firstStageMessage is what the cordon stage reports. It promises a second
// stage only where there is one (one is minted only when there is something
// to evict), so a complete drain does not look half-finished.
//
// It tells apart pods every drain leaves (DaemonSet, mirror, finished) from
// pods only this drain leaves (unowned): a node holding the latter names them
// and says how to include them.
func firstStageMessage(node string, plan DrainPlan) string {
	head := fmt.Sprintf("Cordoned %s — nothing new will be scheduled onto it. ", node)
	if len(plan.Evict) > 0 {
		return head + fmt.Sprintf("Nothing has been evicted yet: %d pod(s) would be, %d would be left alone. "+
			"Read the list, then confirm again to evict.", len(plan.Evict), len(plan.Skip))
	}
	if spared := plan.SparedUnowned(); len(spared) > 0 {
		message := head + fmt.Sprintf("Nothing has been evicted, and it is not empty: %d pod(s) nothing "+
			"owns are left on it (%s). Nothing would create them again, so a drain evicts them only when "+
			"it is planned again to include the pods nothing owns, as kubectl drain --force does.",
			len(spared), strings.Join(spared, ", "))
		if never := len(plan.Skip) - len(spared); never > 0 {
			message += fmt.Sprintf(" The other %d pod(s) are left alone by every drain.", never)
		}
		return message
	}
	if len(plan.Skip) > 0 {
		return head + fmt.Sprintf("There is nothing to evict: %d pod(s) are on it and every one is left "+
			"alone by a drain. That is as far as a drain takes this node.", len(plan.Skip))
	}
	// An empty list that could be incomplete is not "the node is empty".
	return head + "There is nothing to evict. " + plan.Note
}

// DrainPlan is the second stage's preview: what a drain would evict from this
// node and what it would leave alone, read now, cordoning and evicting
// nothing.
//
// Exported for the JSON API, whose second stage is its own plan re-derived
// just before the act, so the pod list must be producible without redoing
// the first stage (a preview that cordoned would be one that writes). It goes
// through Locate, so a read-only cluster refuses it.
//
// It reads the node too, because "no pods on a node that does not exist" and
// "no pods on a node" are different answers the field selector cannot tell
// apart.
func (r *Runner) DrainPlan(ctx context.Context, req Request) (DrainPlan, error) {
	if r.writer == nil {
		return DrainPlan{}, edit.Refuse(edit.RefusedInput,
			"this build has no write path, so there is nothing to plan")
	}
	where, err := r.writer.Locate(ctx, req.Target)
	if err != nil {
		return DrainPlan{}, err
	}
	if !isNode(where.Kind) {
		return DrainPlan{}, edit.Refuse(edit.RefusedInput, "only a node can be drained; %s is not one", where.Kind)
	}
	node, err := edit.Live(ctx, where, req.Target)
	if err != nil {
		return DrainPlan{}, edit.WrapGet(err, where.Kind)
	}
	plan, err := r.plan(ctx, req)
	if err != nil {
		return DrainPlan{}, err
	}
	// As the node stands: whether the first stage's cordon still holds is a
	// fact the list is read against.
	plan.Cordoned, _, _ = unstructured.NestedBool(node.Object, "spec", "unschedulable")
	return plan, nil
}

// plan lists the node's pods and sorts them into what goes and what stays.
func (r *Runner) plan(ctx context.Context, req Request) (DrainPlan, error) {
	plan := DrainPlan{Node: req.Name}

	// A second Locate, so the pod list passes the guardrail on its own
	// account.
	pods, err := r.writer.Locate(ctx, edit.Target{Cluster: req.Cluster, Kind: "pods", Name: req.Name})
	if err != nil {
		return plan, err
	}

	// The field selector makes this one request; nodeName is checked again
	// below, because a server ignoring the selector would hand back the
	// whole cluster for eviction.
	//
	// Reviewed as a list of every namespace, not narrowed: a plan drawn from
	// the namespaces the person may list would call a node drained that still
	// runs the rest. A person who may not list pods everywhere is refused.
	var list *unstructured.UnstructuredList
	err = edit.Read(ctx, pods, edit.Target{Cluster: req.Cluster, Kind: "pods"}, "list", func(ctx context.Context) error {
		var err error
		list, err = pods.Dynamic.Resource(pods.Resource).Namespace(metav1.NamespaceAll).
			List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + req.Name})
		return err
	})
	if err != nil {
		kind := edit.RefusedByAPI
		if errors.Is(err, access.ErrUnreviewable) {
			// The review could not be made: its own refusal, a 503 not a 403.
			kind = edit.RefusedUnreviewable
		}
		return plan, edit.Refuse(kind,
			"the pods on %s could not be listed, so there is no plan to confirm: %s",
			req.Name, textutil.FirstLineN(err.Error(), refusalLine))
	}

	for i := range list.Items {
		item := list.Items[i]
		node, _, _ := unstructured.NestedString(item.Object, "spec", "nodeName")
		if node != req.Name {
			// The field selector was not honored: dropped, so it is not
			// evicted.
			continue
		}
		ref := classify(item, req.IncludeUnowned)
		if ref.Skip != "" {
			plan.Skip = append(plan.Skip, ref)
		} else {
			plan.Evict = append(plan.Evict, ref)
		}
	}
	sortPods(plan.Evict)
	sortPods(plan.Skip)

	if len(plan.Evict) == 0 && len(plan.Skip) == 0 {
		plan.Note = "No pods are on this node. That is either a node that is already empty or a " +
			"kubelet that has not registered any — the node's own page says which."
	}
	return plan, nil
}

// UnownedSkip is why a pod nothing owns is left alone by a drain that was not
// asked to include such pods, exported so the plan can tell these from the
// pods no drain evicts.
const UnownedSkip = "nothing owns this pod, so nothing would create it again: a drain leaves it " +
	"unless it is planned to include the pods nothing owns, as kubectl drain does without --force"

// classify decides what happens to one pod, and why. includeUnowned is the
// drain's `--force`: a pod nothing owns is evicted, with its warning, only when
// it is set.
func classify(pod unstructured.Unstructured, includeUnowned bool) PodRef {
	ref := PodRef{Namespace: pod.GetNamespace(), Name: pod.GetName()}

	if owner := controllerOf(pod); owner != "" {
		ref.Controller = owner
	}

	if _, mirror := pod.GetAnnotations()["kubernetes.io/config.mirror"]; mirror {
		ref.Skip = "a mirror pod — the kubelet runs it from a file on the node, and the API server cannot remove it"
		return ref
	}
	if strings.HasPrefix(ref.Controller, "DaemonSet/") {
		ref.Skip = "managed by a DaemonSet, which would put it straight back on this node"
		return ref
	}
	if phase, _, _ := unstructured.NestedString(pod.Object, "status", "phase"); phase == "Succeeded" || phase == "Failed" {
		ref.Skip = "already finished (" + phase + ")"
		return ref
	}

	if ref.Controller == "" {
		if !includeUnowned {
			ref.Skip = UnownedSkip
			return ref
		}
		ref.Warning = "nothing owns this pod, so nothing will create it again anywhere else"
	} else if names := emptyDirVolumes(pod); len(names) > 0 {
		ref.Warning = "uses emptyDir storage (" + strings.Join(names, ", ") + "), whose contents are lost"
	}
	return ref
}

// controllerOf is "DaemonSet/fluent-bit", or "" when nothing owns the pod.
func controllerOf(pod unstructured.Unstructured) string {
	for _, owner := range pod.GetOwnerReferences() {
		if owner.Controller != nil && *owner.Controller {
			return owner.Kind + "/" + owner.Name
		}
	}
	return ""
}

// emptyDirVolumes names the volumes whose contents a drain destroys.
func emptyDirVolumes(pod unstructured.Unstructured) []string {
	volumes, found, err := unstructured.NestedSlice(pod.Object, "spec", "volumes")
	if err != nil || !found {
		return nil
	}
	var names []string
	for _, raw := range volumes {
		volume, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if _, has := volume["emptyDir"]; !has {
			continue
		}
		name, _ := volume["name"].(string)
		if name == "" {
			name = "(unnamed)"
		}
		names = append(names, name)
	}
	return names
}

// evictAll sends one eviction per pod and records what each came back with.
//
// Serial, because a burst against a PodDisruptionBudget is a burst of
// uninformative 429s; not waiting for pods to terminate, because thirty
// seconds of grace each would time the browser out halfway with no way to
// tell which evictions were sent.
//
// Bounded, so the stage always answers: an admission webhook that hangs
// would otherwise hold the response open with "Working…" on the button.
// Each eviction has evictionTimeout and the stage drainBudget. Three
// failures are kept apart because each calls for something different:
// refused (the API server said no, typically a PodDisruptionBudget),
// unanswered (sent, whether it is going is unknown) and not attempted
// (nothing sent; edit.NotSent). Each names the clock that ran out. A
// healthy stage of 30-odd pods answers in a few seconds.
func (r *Runner) evictAll(ctx context.Context, req Request, plan *DrainPlan) {
	pods, err := r.writer.Locate(ctx, edit.Target{Cluster: req.Cluster, Kind: "pods", Name: req.Name})
	if err != nil {
		plan.Note = "the pods could not be reached to evict them: " + textutil.FirstLineN(err.Error(), refusalLine)
		return
	}
	stage, stop := context.WithDeadline(ctx, drainClock().Add(drainBudget))
	defer stop()
	stageEnd, _ := stage.Deadline()
	for i := range plan.Evict {
		ref := &plan.Evict[i]
		if expired(ctx) {
			notAttempted(plan, ref, "not attempted — the request was canceled before this pod was reached")
			continue
		}
		if expired(stage) {
			notAttempted(plan, ref, fmt.Sprintf("not attempted — the drain stopped after %s so that it "+
				"could answer; run it again to evict the rest", drainBudget))
			continue
		}
		target := edit.Target{Cluster: req.Cluster, Kind: "pods", Namespace: ref.Namespace, Name: ref.Name}
		own := drainClock().Add(evictionTimeout)
		one, done := context.WithDeadline(stage, own)
		decision, err := evictPod(one, pods, target)
		// Which clock ran out, read before done() sets Err everywhere. `one` is
		// the stage's child, so the clock that ran out first is the one named.
		cancelled := expired(ctx)
		spent := expired(stage) && !own.Before(stageEnd)
		timedOut := expired(one)
		done()
		// The error decides what happened; the clocks only say which ran out.
		// Errs are set by timers that fire late on a loaded machine, so asking
		// them first could report a cut eviction as refused by the cluster.
		if err != nil && !answered(err) {
			why := ""
			switch {
			case cancelled:
				why = "the request was canceled"
			case spent:
				why = fmt.Sprintf("the drain's %s ran out", drainBudget)
			case timedOut:
				why = fmt.Sprintf("this eviction's %s ran out", evictionTimeout)
			}
			if notSent(err) {
				if outcome := notSentOutcome(err, why); outcome != "" {
					// Nothing reached the API server; the sentence says where it
					// stopped.
					notAttempted(plan, ref, outcome)
					continue
				}
				// A review that could not be made or a record that could not be
				// written, with no clock run out: reported below as the console's
				// own refusal.
			} else if timeout, ok := edit.AsTimedOut(err); ok {
				// Answered by a timeout: it may still happen. A clock that ran
				// out did so afterwards, while client-go waited to retry.
				ref.Outcome = fmt.Sprintf("the API server %s this eviction (%s), so it may still "+
					"happen and whether the pod is going is not known; its own page says", timeout.What(), timeout.Answer())
				ref.Failed = true
				ref.TimedOut = true
				plan.Failed++
				plan.TimedOut++
				continue
			} else {
				// Sent and not answered in time: whether it went is not known.
				switch {
				case cancelled:
					ref.Outcome = "the request was canceled before the API server answered this eviction, " +
						"so whether the pod is going is not known; its own page says"
				case spent:
					ref.Outcome = fmt.Sprintf("the API server had not answered this eviction when the drain's %s "+
						"ran out, so whether the pod is going is not known; its own page says", drainBudget)
				case timedOut:
					ref.Outcome = fmt.Sprintf("the API server did not answer this eviction within %s, so whether "+
						"the pod is going is not known; its own page says", evictionTimeout)
				case !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled):
					// The connection ended before an answer: no clock is named.
					ref.Outcome = fmt.Sprintf("the connection to the API server ended before it answered this "+
						"eviction (%s), so whether the pod is going is not known; its own page says",
						textutil.FirstLineN(causeOf(err).Error(), refusalLine))
				default:
					// A deadline that is none of these clocks (the client's own).
					ref.Outcome = fmt.Sprintf("the API server did not answer this eviction in time (%s), so "+
						"whether the pod is going is not known; its own page says",
						textutil.FirstLineN(causeOf(err).Error(), refusalLine))
				}
				ref.Failed = true
				ref.Unanswered = true
				plan.Failed++
				plan.Unanswered++
				continue
			}
		}
		if err != nil {
			ref.Outcome = refusalReason(err)
			ref.Failed = true
			plan.Failed++
			continue
		}
		r.writer.Logf(ctx, "write: cluster=%q kind=%s namespace=%s name=%s action=evict node=%s decision=%s outcome=applied",
			pods.Cluster, pods.Resource.Resource, ref.Namespace, ref.Name, req.Name, decision)
		ref.Outcome = "evicted"
		plan.Requested++
	}
}

// drainClock is the clock the eviction stage's deadlines are set and read
// on, a variable so a test can drive it rather than sleep.
var drainClock = time.Now

// expired reports whether ctx is done, or its deadline has passed on
// drainClock. The second half makes the answer exact: a context's Err is set
// by a timer, which fires late on a loaded machine.
func expired(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	deadline, ok := ctx.Deadline()
	return ok && !drainClock().Before(deadline)
}

func notAttempted(plan *DrainPlan, ref *PodRef, outcome string) {
	ref.Outcome = outcome
	ref.Failed = true
	ref.NotAttempted = true
	plan.Failed++
	plan.NotAttempted++
}

// answered reports whether err is something the API server said, rather than
// a context running out under the request: a PodDisruptionBudget's 429 that
// lands as the deadline passes is still a refusal. Not a request sent and
// unanswered (edit.Unanswered), nor a timeout answer (edit.TimedOut), which
// may still happen: neither is counted under refused=.
func answered(err error) bool {
	return !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) && !notSent(err) &&
		!errors.Is(err, edit.ErrUnanswered) && !errors.Is(err, edit.ErrTimedOut)
}

// causeOf is what a request sent and not answered came back with: the drain
// words its rows itself.
func causeOf(err error) error {
	if unanswered, ok := edit.AsUnanswered(err); ok {
		return unanswered.Err
	}
	return err
}

// notSent reports whether an eviction was held back before it left: by
// edit.Evict (its context ran out before the review let it go), by a review
// that could not be completed, by a record that could not be written, or by
// the client as its transport recorded it (rate limit, no connection).
// Nothing reached the API server, so "not answered" would be a guess about a
// request never made.
func notSent(err error) bool {
	if errors.Is(err, edit.ErrNotSent) {
		return true
	}
	if refusal, ok := edit.AsRefusal(err); ok {
		return refusal.Kind == edit.RefusedUnreviewable || refusal.Kind == edit.RefusedUnrecorded
	}
	return false
}

// notSentOutcome is the row's sentence for an eviction never sent, saying
// where it stopped; why is the drain clock that ran out, or "". Empty where
// the console's own refusal says it better (no clock run out). Each place
// has its own sentence because each is a different thing.
func notSentOutcome(err error, why string) string {
	const again = ", so nothing was sent; run the drain again to evict it"
	if held, ok := edit.AsNotSent(err); ok {
		switch held.At {
		case edit.HeldBeforeTransport:
			// x/time/rate refuses a wait that would overrun the deadline at
			// once, so no clock need have run out yet.
			if why == "" {
				return "not attempted — the console's request limit for this cluster would have held it " +
					"past its deadline" + again
			}
			return "not attempted — " + why + " while it waited its turn under the console's request limit " +
				"for this cluster" + again
		case edit.HeldBeforeConnection:
			if why == "" {
				return "not attempted — no connection to the API server could be made for it (" +
					textutil.FirstLineN(held.Err.Error(), refusalLine) + ")" + again
			}
			return "not attempted — " + why + " before a connection to the API server was made for it" + again
		}
		// Held by edit.Evict: the review answered after one of the drain's
		// deadlines, so why is set; "" only where a test's drainClock and
		// edit's time.Now disagree.
		if why == "" {
			why = "its time ran out"
		}
		return "not attempted — " + why + " while the console was still checking whether it may evict this pod" + again
	}
	if why == "" {
		return ""
	}
	if refusal, ok := edit.AsRefusal(err); ok && refusal.Kind == edit.RefusedUnrecorded {
		return "not attempted — " + why + " and its audit record could not be written first" + again
	}
	return "not attempted — " + why + " while the console was still checking whether it may evict this pod" + again
}

func sortPods(refs []PodRef) {
	sort.Slice(refs, func(a, b int) bool {
		if refs[a].Namespace != refs[b].Namespace {
			return refs[a].Namespace < refs[b].Namespace
		}
		return refs[a].Name < refs[b].Name
	})
}

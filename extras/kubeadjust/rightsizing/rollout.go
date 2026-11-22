package rightsizing

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Remembering a kill for memory across the rollout that fixed it.
//
// "Increase limit" after an OOMKill is applied with `kubectl set resources`,
// which replaces every pod: the new pods carry no `lastState` and their usage
// window starts empty, so a quiet window would soon read as "Reduce limit"
// under the limit the container was killed at.
//
// The evidence is the workload's kept revision history (a Deployment's old
// ReplicaSets, a StatefulSet's or DaemonSet's ControllerRevisions): each holds
// the pod template it rolled out, so the newest change of a container's memory
// limit can be read after the killed pods are gone. Old pods are deleted, Events
// expire and the kubelet records no per-pod OOM event, and any other source
// would need a new Host door.
//
// The rule: when the newest change of a container's memory limit across the
// kept revisions is a raise, and the pods measured run at the raised value, no
// reduction of that memory limit is suggested (no "Reduce limit", no
// Guaranteed "Reduce request + limit" on memory, no "Set limit" where the
// raise removed the limit), and the card says why. A later reduction is a new
// newest change and ends the rule, so a manual lowering is believed.
//
// When the history cannot be read (the list was refused, the newest revision
// does not match the pods, or the kept revisions never changed the limit),
// nothing is refused, since a refusal needs evidence: every memory limit
// reduction carries a caveat instead.
//
// Memory limits only: a lowered request is an eviction priority, not a kill,
// and the warm-up refusal covers the new pods' first minutes.

// revisionAnnotation is where a Deployment numbers the ReplicaSet it rolled out.
const revisionAnnotation = "deployment.kubernetes.io/revision"

// MemoryRaise is the newest change of one container's memory limit, when it was
// a raise. To is zero when the raise removed the limit.
type MemoryRaise struct {
	From     int64
	To       int64
	Revision int64
}

// Rollout is what one workload's kept revisions say about its memory limits.
type Rollout struct {
	// Unknown is why the history could not be read for the whole workload, in
	// a clause the caveat completes. Empty when it was read.
	Unknown string
	// Containers is, per container of the newest revision, what its history
	// says.
	Containers map[string]LimitHistory
}

// LimitHistory is one container's memory limit across the kept revisions.
type LimitHistory struct {
	// Newest is the limit of the newest kept revision, zero for none.
	Newest int64
	// Raise is the newest change, when it was a raise; nil when the newest
	// change was a reduction or there was none.
	Raise *MemoryRaise
	// Unchecked is why the history did not reach far enough to answer.
	Unchecked string
}

// raise is the raise the rule holds this container to: there is one, and the
// pods measured run at its value.
func (r *Rollout) raise(container string, lim int64) (MemoryRaise, bool) {
	if r == nil || r.Unknown != "" {
		return MemoryRaise{}, false
	}
	h, ok := r.Containers[container]
	if !ok || h.Unchecked != "" || h.Raise == nil || h.Raise.To != lim {
		return MemoryRaise{}, false
	}
	return *h.Raise, true
}

// unknown is why this container's history could not be checked, empty when it
// was - or when nothing was asked (a nil Rollout: a workload built by hand, or
// a kind that is never rolled out by `set resources`).
func (r *Rollout) unknown(container string, lim int64) string {
	if r == nil {
		return ""
	}
	if r.Unknown != "" {
		return r.Unknown
	}
	h, ok := r.Containers[container]
	switch {
	case !ok:
		return "the container is not in the newest kept revision"
	case h.Unchecked != "":
		return h.Unchecked
	case h.Newest != lim:
		return fmt.Sprintf("the pods measured run at %s and the newest revision at %s, so a rollout is under way",
			limitWord(lim), limitWord(h.Newest))
	}
	return ""
}

// raiseCaveat is said on every memory row of a container the rule holds.
func raiseCaveat(m MemoryRaise) string {
	return fmt.Sprintf("The memory limit was raised from %s to %s at revision %d, and the pods that were killed "+
		"are gone with that rollout - no reduction of this limit is suggested while that raise is its newest change",
		limitWord(m.From), limitWord(m.To), m.Revision)
}

// unknownHistoryCaveat rides on a memory limit reduction the rule could not
// check.
func unknownHistoryCaveat(reason string) string {
	return "Whether this limit was raised after a kill for memory could not be checked (" + reason +
		"): the pods killed before a rollout leave no trace on the new ones"
}

func limitWord(v int64) string {
	if v == 0 {
		return "unlimited"
	}
	return fmtRaw(float64(v), false)
}

// revision is one kept revision: its number and its containers' memory limits.
type revision struct {
	number int64
	// limits is container name → memory limit in bytes, zero for none.
	limits map[string]int64
	// readable is false for a revision whose template could not be decoded.
	readable bool
}

// Rollouts is the kept revisions of every workload in scope, indexed by the
// workload that owns them (AutoscalerKey's shape), and why each list could not
// be read.
type Rollouts struct {
	byWorkload     map[string][]revision
	replicaSetsErr error
	revisionsErr   error
}

// ReadRollouts indexes what the extra listed: the ReplicaSets (already listed
// to find a pod's Deployment) and the ControllerRevisions. Each error is why
// that list could not be read; it turns every workload of the kinds it serves
// into an unknown history, never into an empty one.
func ReadRollouts(replicaSets []appsv1.ReplicaSet, replicaSetsErr error,
	revisions []appsv1.ControllerRevision, revisionsErr error) Rollouts {
	out := Rollouts{byWorkload: map[string][]revision{}, replicaSetsErr: replicaSetsErr, revisionsErr: revisionsErr}
	for i := range replicaSets {
		rs := &replicaSets[i]
		owner := controllerOf(rs.OwnerReferences)
		if owner == nil || owner.Kind != "Deployment" {
			continue
		}
		number, err := strconv.ParseInt(rs.Annotations[revisionAnnotation], 10, 64)
		if err != nil {
			// A ReplicaSet the Deployment controller did not number is not one
			// of its revisions.
			continue
		}
		key := AutoscalerKey(rs.Namespace, owner.Kind, owner.Name)
		out.byWorkload[key] = append(out.byWorkload[key],
			revision{number: number, limits: memoryLimits(rs.Spec.Template), readable: true})
	}
	for i := range revisions {
		cr := &revisions[i]
		owner := controllerOf(cr.OwnerReferences)
		if owner == nil || (owner.Kind != "StatefulSet" && owner.Kind != "DaemonSet") {
			continue
		}
		rev := revision{number: cr.Revision}
		if template, ok := revisionTemplate(cr); ok {
			rev.limits, rev.readable = memoryLimits(template), true
		}
		key := AutoscalerKey(cr.Namespace, owner.Kind, owner.Name)
		out.byWorkload[key] = append(out.byWorkload[key], rev)
	}
	return out
}

// For is one workload's Rollout. Nil for a kind `set resources` does not roll
// out (a bare pod, a Job, a bare ReplicaSet: their pods keep their state), so
// nothing is said about them.
func (rs Rollouts) For(kind, namespace, name string) *Rollout {
	var listErr error
	switch kind {
	case "Deployment":
		listErr = rs.replicaSetsErr
	case "StatefulSet", "DaemonSet":
		listErr = rs.revisionsErr
	case "CronJob":
		// Each run is new pods, and a CronJob keeps no revision of its
		// template: the kill was on a pod of an earlier Job.
		return &Rollout{Unknown: "a CronJob keeps no revision of its template"}
	default:
		return nil
	}
	if listErr != nil {
		return &Rollout{Unknown: "its revisions could not be listed: " + ShortError(listErr)}
	}
	return rolloutOf(rs.byWorkload[AutoscalerKey(namespace, kind, name)])
}

// rolloutOf walks one workload's revisions newest first, and finds for each
// container the newest change of its memory limit.
func rolloutOf(revs []revision) *Rollout {
	if len(revs) == 0 {
		return &Rollout{Unknown: "no kept revision of it was found"}
	}
	revs = append([]revision(nil), revs...)
	sort.SliceStable(revs, func(i, j int) bool { return revs[i].number > revs[j].number })
	newest := revs[0]
	if !newest.readable {
		return &Rollout{Unknown: fmt.Sprintf("its newest revision (%d) could not be read", newest.number)}
	}
	oldest := revs[len(revs)-1].number
	out := &Rollout{Containers: make(map[string]LimitHistory, len(newest.limits))}
	for name, lim := range newest.limits {
		h := LimitHistory{Newest: lim}
		answered := false
		for i := 1; i < len(revs) && !answered; i++ {
			older := revs[i]
			switch old, present := older.limits[name]; {
			case !older.readable:
				h.Unchecked = fmt.Sprintf("revision %d could not be read", older.number)
				answered = true
			case !present:
				// The container was added after this revision: its whole history
				// has been walked, and its limit never changed.
				answered = true
			case old != lim:
				if raised(old, lim) {
					h.Raise = &MemoryRaise{From: old, To: lim, Revision: revs[i-1].number}
				}
				answered = true
			}
		}
		if !answered && oldest > 1 {
			// The change, if there was one, is in a revision no longer kept.
			h.Unchecked = fmt.Sprintf("only revisions %d to %d are kept, and the limit is the same in each", oldest, newest.number)
		}
		out.Containers[name] = h
	}
	return out
}

// raised says whether going from old to new raised a memory limit. Zero is no
// limit, which is above every limit.
func raised(old, new int64) bool {
	if new == 0 {
		return old > 0
	}
	return old > 0 && new > old
}

// memoryLimits is each container's memory limit in a pod template.
func memoryLimits(t corev1.PodTemplateSpec) map[string]int64 {
	out := make(map[string]int64, len(t.Spec.Containers))
	for _, c := range t.Spec.Containers {
		var lim int64
		if q, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
			lim = q.Value()
		}
		out[c.Name] = lim
	}
	return out
}

// revisionTemplate decodes the pod template a ControllerRevision holds. The
// StatefulSet and DaemonSet controllers store it as a patch,
// `{"spec":{"template":{…,"$patch":"replace"}}}`, so `data.spec.template` is the
// whole template.
func revisionTemplate(cr *appsv1.ControllerRevision) (corev1.PodTemplateSpec, bool) {
	raw := cr.Data.Raw
	if len(raw) == 0 && cr.Data.Object != nil {
		var err error
		if raw, err = json.Marshal(cr.Data.Object); err != nil {
			return corev1.PodTemplateSpec{}, false
		}
	}
	var data struct {
		Spec struct {
			Template *corev1.PodTemplateSpec `json:"template"`
		} `json:"spec"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &data) != nil || data.Spec.Template == nil {
		return corev1.PodTemplateSpec{}, false
	}
	return *data.Spec.Template, true
}

func controllerOf(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	return nil
}

// attachRollouts files each workload's Rollout on it.
func attachRollouts(workloads []Workload, rollouts Rollouts) {
	for i := range workloads {
		w := &workloads[i]
		w.Rollout = rollouts.For(w.Kind, w.Namespace, w.Name)
	}
}

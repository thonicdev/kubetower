package actions

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/edit"
)

// What the confirmation says is about to change, read from the object.
//
// A recap where one is possible, and only there: an invented recap is worse
// than none. `spec.unschedulable: false → true` on a node already cordoned
// tells the operator something false, and they will believe it.
//
// So for verbs whose change is one field, this reads the live object and
// states that field now and after; others say nothing plausible instead.
// Every verb takes its pin from the read (Recap.Pin, pin.go).
//
// It lives here, not in internal/web, because the field paths are the same
// strings actions.go patches: a second copy would drift and go on describing
// a field the verb stopped writing.

// Recap is one field's before and after, or a reason there is none.
type Recap struct {
	// Field is the dotted path, exactly as the patch writes it.
	Field string
	// Before is the value in effect now: "false" for an absent boolean, as
	// the API server reads it.
	Before string
	After  string
	// Absent says the field is not written on the object at all. Before then
	// carries the default in effect, and the panel says both: the same to
	// Kubernetes, not to somebody about to press the button on production.
	Absent bool
	// Unchanged is true when before and after are the same value, so a no-op
	// (uncordoning a schedulable node) is said rather than reported as done.
	Unchanged bool
	// Note is why there is no before/after. Set only when Field is empty.
	Note string
	// ResourceVersion is the version this recap was read from, filled for
	// every verb. The act is bound to the plan drawn from this preview, so what
	// the operator agreed against has to travel in the answer; the
	// precondition the act carries is Pin.
	ResourceVersion string
	// Pin is what the act is pinned to, read from the same object (pin.go).
	// The plan states Pin.Value as its precondition and Pin.On as its field.
	Pin Pin
	// Subject is what the confirmation's line says about this object, read
	// off the same object so it costs no request (subject.go).
	Subject Subject
}

// Stated reports whether this recap has a field change to show.
func (r Recap) Stated() bool { return r.Field != "" }

// Preview reads the object a verb will act on, and for the verbs whose whole
// change is one field, that field before and after.
//
// It goes through Locate, so a read-only cluster refuses it (there is no
// change to preview there). It reads and never writes.
//
// Every verb reads the object, the note-only ones included: otherwise a
// delete would preview cleanly against an object the cluster does not have,
// with no pin for the act. A read failure is returned, because a
// confirmation that silently loses its recap looks like a no-op.
func (r *Runner) Preview(ctx context.Context, req Request) (Recap, error) {
	if r.writer == nil {
		return Recap{}, edit.Refuse(edit.RefusedInput,
			"this build has no write path, so there is nothing to preview")
	}
	where, err := r.writer.Locate(ctx, req.Target)
	if err != nil {
		return Recap{}, err
	}
	// The plan of a write is shown only to whoever may make it: the preview
	// shows a field and the version, so it is reviewed first with the verb's
	// own questions (Needs). Refused, the person is shown nothing of it.
	for _, question := range Needs(req.Verb, where, req.Target) {
		if err := edit.CheckRecorded(ctx, where, question, true); err != nil {
			return Recap{}, previewRefused(err)
		}
	}
	object, err := edit.Live(ctx, where, req.Target)
	if err != nil {
		return Recap{}, edit.WrapGet(err, where.Kind)
	}
	version := object.GetResourceVersion()
	pin := PinOf(where.Kind, object)
	subject := SubjectOf(object)

	field, after, ok := FieldFor(req.Verb)
	if !ok {
		return Recap{Note: previewNote(req), ResourceVersion: version, Pin: pin, Subject: subject}, nil
	}
	// Only where a count was stated. Otherwise FieldFor's placeholder stands,
	// rather than the preview hiding the missing count behind a number.
	if req.Verb == Scale && req.Replicas != nil {
		after = strconv.FormatInt(*req.Replicas, 10)
	}

	before, absent := readField(object, field)
	return Recap{
		Field: field, Before: before, After: after,
		Absent: absent,
		// Compared on the effective value: uncordoning a node with no
		// spec.unschedulable changes nothing.
		Unchanged:       before == after,
		ResourceVersion: version,
		Pin:             pin,
		Subject:         subject,
	}, nil
}

// previewRefused is the review's refusal in this package's vocabulary, as the
// write door answers it.
func previewRefused(err error) error {
	switch {
	case errors.Is(err, access.ErrDenied):
		return edit.Refuse(edit.RefusedByAccess, "%s", err.Error())
	case errors.Is(err, access.ErrUnreviewable):
		return edit.Refuse(edit.RefusedUnreviewable, "%s", err.Error())
	case errors.Is(err, access.ErrRBACNotInProfile):
		return edit.Refuse(edit.RefusedNotInProfile, "%s", err.Error())
	}
	return err
}

// FieldFor is the single field a verb writes, and the value it will write.
//
// These are the strings actions.go patches, and a test walks both: a recap
// describing a field the verb no longer writes reads as an answer and is
// false.
//
// Scale's value is not fixed; the caller substitutes the count. Exported
// for the bulk confirmation, which states the field over a set without
// reading the objects.
func FieldFor(verb Verb) (field, after string, ok bool) {
	switch verb {
	case Cordon:
		return "spec.unschedulable", "true", true
	case Uncordon:
		return "spec.unschedulable", "false", true
	case Suspend:
		return "spec.suspend", "true", true
	case Resume:
		return "spec.suspend", "false", true
	case Scale:
		// The caller replaces this (see Preview). Not a number, so a caller
		// that forgets prints something obviously unfinished instead of "0".
		return "spec.replicas", "the number below", true
	}
	return "", "", false
}

// previewNote is what to say when there is no field to show.
//
// Drain depends on the stage: the first cordons and lists, the second
// evicts, and the first does not speak of the pod list it has not read yet.
//
// Delete says nothing: a single-target confirmation does not list its
// target, and the dialog already opens with the verb's own explanation. An
// empty note draws nothing (recapOf). It is empty here, not suppressed in
// the dialog, because the server composes the sentence and a client must not
// edit it. Restart's and Evict's overlap their explanations too but are not
// false, so they stay; preview_test.go pins both halves.
//
// These are folded under "Details" in the dialog, below the one line about
// the object (Recap.Subject), for whoever wants to know why.
func previewNote(req Request) string {
	switch req.Verb {
	case Delete:
		return ""
	case Restart:
		return "Nothing of the object's own spec changes except the timestamp that triggers " +
			"the rollout — on a Pod, not even that: the pod is deleted and something else makes another."
	case Evict:
		return "There is no field to change: the API server is asked to remove the pod, and " +
			"refuses if a PodDisruptionBudget would be violated."
	case Drain:
		if req.Stage == StageEvict {
			return "The list above is the change: every pod that will be evicted, by name. " +
				"An eviction a PodDisruptionBudget refuses leaves that pod where it is."
		}
		return "This step cordons the node — nothing new is scheduled onto it, and Uncordon " +
			"undoes that. Nothing is evicted by it: the pods that would be are listed next, " +
			"and they go only once that list is confirmed on its own."
	case Trigger:
		return "Nothing on the CronJob changes. A new Job is created from its template."
	}
	return ""
}

// readField reads a dotted path out of the live object, as text.
//
// Only the shapes the verbs write: a bool and an integer. A missing path
// comes back as the value in effect with absent set, reported apart because
// "changes nothing" must compare the effective value.
func readField(object *unstructured.Unstructured, field string) (value string, absent bool) {
	switch field {
	case "spec.unschedulable":
		return readBool(object, "spec", "unschedulable")
	case "spec.suspend":
		return readBool(object, "spec", "suspend")
	case "spec.replicas":
		found, ok, err := unstructured.NestedInt64(object.Object, "spec", "replicas")
		if err != nil || !ok {
			// No default worth asserting: the controller supplies it, and "1"
			// would be a number this console did not read.
			return "", true
		}
		return strconv.FormatInt(found, 10), false
	}
	return "", true
}

func readBool(object *unstructured.Unstructured, path ...string) (string, bool) {
	found, ok, err := unstructured.NestedBool(object.Object, path...)
	if err != nil || !ok {
		return "false", true
	}
	return fmt.Sprintf("%t", found), false
}

package actions

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// What a confirmation is pinned to.
//
// A plan pinned to `metadata.resourceVersion` is refused on every write to
// the object, status included, and some statuses change constantly: a
// Node's kubelet heartbeat, a Deployment's controller during a rollout.
// Neither refusal would protect anything. So a plan is pinned to what its act
// depends on:
//
//  1. A Node: `spec.unschedulable` and `spec.taints`. A Node carries no
//     `metadata.generation`, and every node verb writes or rests on these
//     two. A drain's second stage is its own plan over the pods it evicts.
//  2. A Pod: `metadata.resourceVersion`, because its labels and annotations
//     are what select it: a pod relabeled out of its Service is not the pod
//     the plan described. A decision, not a fallback.
//  3. Any other object with a `metadata.generation`: that generation, which
//     the API server bumps on spec changes and never on status writes. A
//     custom resource with no status subresource bumps it on status writes
//     too, the cautious direction.
//  4. Anything else: `metadata.resourceVersion` (a ConfigMap, a Secret, a
//     Service: no controller writes their status).
//
// Every pin also carries `metadata.uid`, compared first: a generation
// restarts at 1 when an object is recreated under the same name (an Argo CD
// replace sync, `helm --force`), and a plan to delete the old object must not
// delete its successor.
//
// The cost: the pin is compared in the console between a re-read and the
// write (the patch carries no resourceVersion), so a spec change in those
// milliseconds is not seen. Under rule 3 a change to labels or annotations
// alone does not refuse a plan; none of those verbs reads them.

// Pin is what an act on one object is pinned to: which fields, and their value
// when the plan was made.
type Pin struct {
	// On names what is compared (`metadata.resourceVersion`,
	// `metadata.generation`, or `spec.unschedulable and spec.taints`), so the
	// dialog says what the act is pinned to and the 409 what moved.
	On string
	// Value is the value compared. For a node it is the two fields written
	// out (`unschedulable=false; taints=none`), so a 409 names the new taint.
	Value string
	// UID is the object's `metadata.uid` when the plan was made: Value says
	// whether the object changed, UID whether it is the same object. Empty
	// means the object carried none.
	UID string
}

// The three things a pin can be on, spelt once for the plan and the dialog.
const (
	PinResourceVersion = "metadata.resourceVersion"
	PinGeneration      = "metadata.generation"
	PinNodeScheduling  = "spec.unschedulable and spec.taints"
)

// PinOf is what an act on this object is pinned to. kind is the singular Kind
// the REST mapper returned (`edit.Location.Kind`), the vocabulary isNode and
// isPod are written for.
func PinOf(kind string, object *unstructured.Unstructured) Pin {
	pin := pinValue(kind, object)
	pin.UID = string(object.GetUID())
	return pin
}

// pinValue is what PinOf compares besides the UID, by the rule above.
func pinValue(kind string, object *unstructured.Unstructured) Pin {
	switch {
	case isNode(kind):
		return Pin{On: PinNodeScheduling, Value: nodeScheduling(object)}
	case isPod(kind):
		return Pin{On: PinResourceVersion, Value: object.GetResourceVersion()}
	}
	if generation := object.GetGeneration(); generation > 0 {
		return Pin{On: PinGeneration, Value: strconv.FormatInt(generation, 10)}
	}
	return Pin{On: PinResourceVersion, Value: object.GetResourceVersion()}
}

// nodeScheduling writes out the two fields a node verb depends on in one
// canonical, readable form. Taints are sorted (the same set in another
// order keeps the same pods off), and `timeAdded` is left out: it says when,
// not what.
func nodeScheduling(object *unstructured.Unstructured) string {
	unschedulable, _, _ := unstructured.NestedBool(object.Object, "spec", "unschedulable")
	raw, _, _ := unstructured.NestedSlice(object.Object, "spec", "taints")
	taints := make([]string, 0, len(raw))
	for _, item := range raw {
		taint, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key, _ := taint["key"].(string)
		value, _ := taint["value"].(string)
		effect, _ := taint["effect"].(string)
		text := key
		if value != "" {
			text += "=" + value
		}
		taints = append(taints, text+":"+effect)
	}
	sort.Strings(taints)
	written := "none"
	if len(taints) > 0 {
		written = strings.Join(taints, ", ")
	}
	return fmt.Sprintf("unschedulable=%t; taints=%s", unschedulable, written)
}

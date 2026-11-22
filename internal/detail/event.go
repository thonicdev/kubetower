package detail

// What an Event says, in full. The Events list clips the message to 140
// characters, yet the message is the event's whole content (a scheduling
// reason, an image-pull error, a webhook's refusal) and often has the answer at
// the end.
//
// Two API shapes are read as one: `core/v1` carries `message`, `involvedObject`
// and `source`; `events.k8s.io/v1` carries `note`, `regarding` and
// `reportingController`, with the repeat count under `series`. Which one a
// cluster serves as `events` depends on its preferred version.

import (
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// EventDetail is one event, as a person reads it.
type EventDetail struct {
	// Message is never clipped; that is why this type exists.
	Message string
	Reason  string
	// Type is "Normal" or "Warning". A controller can write anything, so it is
	// passed through rather than mapped.
	Type string
	// Action is what the reporter was doing: "Binding", "Pulling". Empty on most
	// legacy events.
	Action string
	// Source is who said it: "kubelet", "default-scheduler". Node is set on a
	// kubelet event, since "which node" is the next question.
	Source string
	Node   string
	// Count is how many times this has happened.
	Count int32

	// First and Last are when it started and when it last happened; only the
	// pair tells two days of repeats from a burst in a minute.
	First time.Time
	Last  time.Time

	// The object the event is about.
	ObjectKind      string // "Pod", singular, as the API writes it
	ObjectName      string
	ObjectNamespace string
	// ObjectField is `involvedObject.fieldPath`, e.g.
	// `spec.containers{istio-proxy}`: on a pod with a sidecar it tells the app
	// from the mesh.
	ObjectField string
}

// Warning reports whether this is the kind of event worth coloring.
func (e EventDetail) Warning() bool { return strings.EqualFold(e.Type, "Warning") }

// Repeated reports whether this happened more than once.
func (e EventDetail) Repeated() bool { return e.Count > 1 }

// Object is "Pod/api-7d9f" for a heading, or just the name when the reporter did
// not say what kind of thing it was.
func (e EventDetail) Object() string {
	if e.ObjectKind == "" {
		return e.ObjectName
	}
	if e.ObjectName == "" {
		return e.ObjectKind
	}
	return e.ObjectKind + "/" + e.ObjectName
}

// IsEventKind reports whether this kind is one whose message panel applies.
// Both API groups spell the kind `Event`.
func IsEventKind(kind string) bool { return strings.EqualFold(kind, "Event") }

// eventDetailOf reads the panel out of an object already in hand, or returns nil
// for anything that is not an Event.
// Nil rather than a zero value: the drawer branches on the pointer.
func eventDetailOf(raw *unstructured.Unstructured, kind string) *EventDetail {
	if raw == nil || !IsEventKind(kind) {
		return nil
	}

	detail := &EventDetail{
		// `message` is core/v1, `note` is events.k8s.io/v1; reading only one
		// leaves the panel blank on the other.
		Message: firstString(raw, []string{"message"}, []string{"note"}),
		Reason:  firstString(raw, []string{"reason"}),
		Type:    firstString(raw, []string{"type"}),
		Action:  firstString(raw, []string{"action"}),
		Source: firstString(raw,
			[]string{"source", "component"},
			[]string{"reportingComponent"},
			[]string{"reportingController"}),
		Node: firstString(raw, []string{"source", "host"}),
	}

	// `involvedObject` (core/v1) and `regarding` (events.k8s.io/v1) are the
	// same ObjectReference.
	for _, field := range []string{"involvedObject", "regarding"} {
		kind := firstString(raw, []string{field, "kind"})
		name := firstString(raw, []string{field, "name"})
		if kind == "" && name == "" {
			continue
		}
		detail.ObjectKind = kind
		detail.ObjectName = name
		detail.ObjectNamespace = firstString(raw, []string{field, "namespace"})
		detail.ObjectField = firstString(raw, []string{field, "fieldPath"})
		break
	}
	// Deliberately not defaulted to the event's own namespace: an event about
	// a Node is filed in `default` with no namespace on its reference, so that
	// would place a cluster-scoped object in a namespace. The caller knows the
	// kind's scope and owns the fallback.

	detail.Count = firstInt32(raw,
		[]string{"count"},
		[]string{"series", "count"},
		[]string{"deprecatedCount"})
	// Zero is a single occurrence in the newer API; "0 times" did not happen.
	if detail.Count == 0 {
		detail.Count = 1
	}

	detail.First = firstTime(raw,
		[]string{"firstTimestamp"},
		[]string{"eventTime"},
		[]string{"deprecatedFirstTimestamp"})
	detail.Last = firstTime(raw,
		[]string{"lastTimestamp"},
		[]string{"series", "lastObservedTime"},
		[]string{"eventTime"},
		[]string{"deprecatedLastTimestamp"})
	// The newer API leaves the legacy timestamps empty; without a fallback a
	// fresh event would read as 1970.
	if detail.Last.IsZero() {
		detail.Last = raw.GetCreationTimestamp().Time
	}
	if detail.First.IsZero() {
		detail.First = detail.Last
	}

	return detail
}

// firstString returns the first of these paths that holds a non-empty string.
func firstString(raw *unstructured.Unstructured, paths ...[]string) string {
	for _, path := range paths {
		if value, found, err := unstructured.NestedString(raw.Object, path...); err == nil && found && value != "" {
			return value
		}
	}
	return ""
}

// firstInt32 returns the first non-zero number among these paths.
//
// Both `int64` and `float64`: the dynamic client decodes an integral JSON
// number to int64, while plain `encoding/json` leaves a float64, which
// `NestedInt64` reports as not found.
func firstInt32(raw *unstructured.Unstructured, paths ...[]string) int32 {
	for _, path := range paths {
		value, found, err := unstructured.NestedFieldNoCopy(raw.Object, path...)
		if err != nil || !found {
			continue
		}
		switch number := value.(type) {
		case int64:
			if number != 0 {
				return int32(number)
			}
		case float64:
			if number != 0 {
				return int32(number)
			}
		case int32:
			if number != 0 {
				return number
			}
		}
	}
	return 0
}

// firstTime parses the first of these paths that holds an RFC 3339 timestamp.
//
// Parsed from a string: an unstructured object holds what the JSON held.
func firstTime(raw *unstructured.Unstructured, paths ...[]string) time.Time {
	for _, path := range paths {
		stamp := firstString(raw, path)
		if stamp == "" {
			continue
		}
		if at, err := time.Parse(time.RFC3339, stamp); err == nil {
			return at
		}
		// RFC3339 accepts the newer API's fractional seconds; any other format
		// is skipped rather than failing the panel.
	}
	return time.Time{}
}

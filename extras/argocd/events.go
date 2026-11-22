package argocd

import (
	"net/http"
	"sort"
	"time"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/extras/argocd/report"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// An Application's project and its Events, read for the person through
// Host.List. A failure to read either is a note, never the page's failure.

// MaxEvents bounds the Events drawn; the newest are kept.
const MaxEvents = 20

var eventsGVR = schema.GroupVersionResource{Version: "v1", Resource: "events"}

// Event is one Kubernetes Event about the Application.
type Event struct {
	Type    string `json:"type,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	Count   int64  `json:"count,omitempty"`
	// At is the last time it was seen, RFC 3339.
	At string `json:"at,omitempty"`
}

// projectNamespace is where the Application's AppProject lives, so the page
// can open it, or empty when it could not be told.
func projectNamespace(r *http.Request, h extension.Host, cluster, appNamespace, project string) string {
	if project == "" {
		return ""
	}
	registry, err := h.Definitions(r, cluster)
	if err != nil {
		return ""
	}
	crd, err := registry.Lookup(r.Context(), report.AppProjectKind)
	if err != nil {
		return ""
	}
	page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR(), FieldSelector: "metadata.name=" + project})
	if err != nil {
		return ""
	}
	found := ""
	for i := range page.Items {
		if page.Items[i].GetName() != project {
			continue
		}
		if page.Items[i].GetNamespace() == appNamespace {
			return appNamespace
		}
		if found != "" {
			// Two of that name and neither beside the Application: which one
			// Argo CD means depends on its configuration, so do not guess.
			return ""
		}
		found = page.Items[i].GetNamespace()
	}
	return found
}

// eventsOf reads the Application's Events, newest first, and a note when they
// could not be read or were cut.
func eventsOf(r *http.Request, h extension.Host, cluster, namespace, name string) ([]Event, string) {
	page, err := h.List(r, cluster, extension.ListQuery{
		GVR:           eventsGVR,
		Namespace:     namespace,
		FieldSelector: "involvedObject.kind=Application,involvedObject.name=" + name,
	})
	if err != nil {
		return []Event{}, "The Application's Events could not be read for you: " + err.Error()
	}
	out := make([]Event, 0, len(page.Items))
	at := make([]time.Time, 0, len(page.Items))
	for i := range page.Items {
		e := page.Items[i]
		if kind, _, _ := unstructured.NestedString(e.Object, "involvedObject", "kind"); kind != "Application" {
			continue
		}
		if n, _, _ := unstructured.NestedString(e.Object, "involvedObject", "name"); n != name {
			continue
		}
		when := lastSeen(e)
		ev := Event{Type: str(e.Object, "type"), Reason: str(e.Object, "reason"), Message: str(e.Object, "message")}
		if c, ok, _ := unstructured.NestedInt64(e.Object, "count"); ok {
			ev.Count = c
		}
		if !when.IsZero() {
			ev.At = when.UTC().Format(time.RFC3339)
		}
		out = append(out, ev)
		at = append(at, when)
	}
	order := make([]int, len(out))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return at[order[a]].After(at[order[b]]) })
	sorted := make([]Event, 0, len(out))
	for _, i := range order {
		sorted = append(sorted, out[i])
	}
	note := ""
	if len(sorted) > MaxEvents {
		note = "Only the newest Events are shown."
		sorted = sorted[:MaxEvents]
	}
	if !page.Complete && page.Note != "" {
		note = page.Note
	}
	return sorted, note
}

// lastSeen is when an Event last happened: lastTimestamp, the newer API's
// series or eventTime, then when it was first recorded.
func lastSeen(e unstructured.Unstructured) time.Time {
	for _, path := range [][]string{{"lastTimestamp"}, {"series", "lastObservedTime"}, {"eventTime"}, {"firstTimestamp"}} {
		if s, ok, _ := unstructured.NestedString(e.Object, path...); ok && s != "" {
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				return t
			}
		}
	}
	return e.GetCreationTimestamp().Time
}

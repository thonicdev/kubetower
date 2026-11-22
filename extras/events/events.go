// Package events is Event Intelligence: KubeTower's page that makes the
// cluster's Kubernetes Events readable. It is an extension of the console,
// part of the core and behind its own per-cluster switch, off by default.
//
// # What it does with an Event
//
//   - **Reads both shapes.** The kubelet writes the old shape (count,
//     firstTimestamp, lastTimestamp), the scheduler the new one (eventTime,
//     series, a null count). A count is series.count, else count when above
//     zero, else 1 (read.go: readEvent).
//   - **Deduplicates per object**: (involvedObject.uid, reason, normalized
//     message) is one row, however many Events carried it.
//   - **Groups per cause**: (reason, normalized message) across objects. The
//     normalization replaces the object's name, UIDs, numbers, IP addresses
//     and sha256 digests with `*`, so "Back-off restarting failed container
//     app in pod crasher-7474496469-kzs5j_demo(10847df6-...)" on thirty pods is
//     one cause.
//
// # What changed just before
//
// Changes are read from what the cluster already holds (changes.go): a
// Deployment's rollouts and rollbacks from its ReplicaSets, a ConfigMap's
// latest edit from managedFields (never its data), scaling from the
// ScalingReplicaSet and SuccessfulRescale Events, nodes from their creation
// and NodeNotReady. A group of Events is related to the most recent change that
// precedes it, concerns the same workload, a ConfigMap the workload
// references or the node carrying its pods, and was first seen within thirty
// minutes after it (correlate.go). It is a correlation in time; the answer
// says "since", never "because".
//
// # Who sees what
//
// Every read goes through Host.List, reviewed for the person behind the
// request and narrowed to the namespaces they may list; Host.Client is never
// called and nothing here writes. Secrets are not read. A secondary read that
// is refused (nodes, ConfigMaps, ReplicaSets, pods) marks its source refused
// in the answer and never reads as an empty one.
//
// # What it costs
//
// Events, ReplicaSets, pods and ConfigMaps are walked page by page
// (ListQuery.Each) and only what is aggregated is kept: never a raw Event, a
// pod's body or a ConfigMap's data. The aggregate of a (person, cluster,
// namespace, window) is held for fifteen seconds, 32 of them at most, so the
// page, the tab and the panel lines open together read the cluster once
// (cache.go). Its size is capped (limits in aggregate.go) and an answer cut
// short says `truncated`.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

// Key is `events`: the page's URL segment, the `KT_EXTRAS` name and the key
// its per-cluster switch is stored under, so it is not free to change.
const Key = "events"

// Label is the menu entry.
const Label = "Event Intelligence"

var (
	eventsGVR     = schema.GroupVersionResource{Version: "v1", Resource: "events"}
	podsGVR       = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	nodesGVR      = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	configMapsGVR = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	replicaSets   = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
)

// Events is the extension `cmd/kubetower` registers.
type Events struct {
	now   func() time.Time
	cache *cache
}

// New is the constructor `cmd/kubetower` calls.
func New() *Events { return &Events{now: time.Now, cache: newCache(CacheEntries, CacheTTL)} }

func (*Events) Key() string      { return Key }
func (*Events) Label() string    { return Label }
func (*Events) Legacy() []string { return nil }

func (*Events) Summary() string {
	return "Kubernetes Events deduplicated per object and grouped per cause, related to what changed just before: rollouts, ConfigMap edits, scaling and new nodes."
}

func (*Events) Needs() string {
	return "Nothing beyond the cluster itself; the changes need list on replicasets, configmaps, pods and nodes"
}

// Routes are three reads, all GETs: nothing here changes a cluster.
func (e *Events) Routes(h extension.Host) []extension.Route {
	return []extension.Route{
		{Method: http.MethodGet, Pattern: "/{$}", Handler: e.page(h)},
		{Method: http.MethodGet, Pattern: "/object/{kind}/{namespace}/{name}", Handler: e.object(h)},
		{Method: http.MethodGet, Pattern: "/line/{kind}/{namespace}/{name}", Handler: e.line(h)},
	}
}

// The windows `?window=` takes. A longer window than the API server's event
// TTL (one hour by default) holds only the Events that were refreshed.
var windows = map[string]time.Duration{"15m": 15 * time.Minute, "1h": time.Hour, "3h": 3 * time.Hour}

// DefaultWindow is the window of a request that names none.
const DefaultWindow = "1h"

// objectKinds are the `{kind}` segments of the object and line routes, with
// the Kubernetes kind each stands for.
var objectKinds = map[string]string{
	"pods": "Pod", "deployments": "Deployment", "statefulsets": "StatefulSet",
	"daemonsets": "DaemonSet", "replicasets": "ReplicaSet", "jobs": "Job",
}

// The codes these routes answer with. Protocol: the page names them too.
const (
	CodeUnknownCluster = "unknown_cluster"
	CodeBadNamespace   = "bad_namespace"
	CodeBadName        = "bad_name"
	CodeBadKind        = "bad_kind"
	CodeBadWindow      = "bad_window"
	CodeListFailed     = "list_failed"
)

type problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

func firstLine(s string) string { return textutil.FirstLineN(s, 300) }

func writeProblem(w http.ResponseWriter, status int, code, message string, err error) {
	body := problem{Code: code, Message: message}
	if err != nil {
		body.Detail = firstLine(err.Error())
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		http.Error(w, "the answer could not be encoded", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}

// writeReadFailure answers a request whose Events could not be read.
func writeReadFailure(w http.ResponseWriter, cluster string, err error) {
	switch {
	case errors.Is(err, extension.ErrUnknownCluster):
		writeProblem(w, http.StatusNotFound, CodeUnknownCluster, extension.UnknownClusterText(cluster), err)
	case errors.Is(err, context.Canceled):
		// The page went away.
	default:
		if refused, ok := extension.RefusedRead(err,
			"The cluster says you may not list events here, so none can be shown."); ok {
			writeProblem(w, refused.Status, refused.Code, refused.Message, err)
			return
		}
		writeProblem(w, http.StatusBadGateway, CodeListFailed,
			"This cluster's events could not be listed, so nothing about them can be said.", err)
	}
}

func validNamespace(w http.ResponseWriter, ns string) bool {
	if ns != "" && len(validation.IsDNS1123Label(ns)) > 0 {
		writeProblem(w, http.StatusBadRequest, CodeBadNamespace, `"`+ns+`" is not a namespace name.`, nil)
		return false
	}
	return true
}

func validName(w http.ResponseWriter, name string) bool {
	if len(validation.IsDNS1123Subdomain(name)) > 0 {
		writeProblem(w, http.StatusBadRequest, CodeBadName, `"`+name+`" is not an object name.`, nil)
		return false
	}
	return true
}

// windowOf reads `?window=`; a value that is not one of the three is a 400.
func windowOf(w http.ResponseWriter, r *http.Request) (string, time.Duration, bool) {
	name := r.URL.Query().Get("window")
	if name == "" {
		name = DefaultWindow
	}
	d, ok := windows[name]
	if !ok {
		writeProblem(w, http.StatusBadRequest, CodeBadWindow, `"`+name+`" is not a window: 15m, 1h or 3h.`, nil)
	}
	return name, d, ok
}

// page answers `GET /`: the tiles, the changes and the causes, for one
// namespace or every one the person may list.
func (e *Events) page(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, ns := r.PathValue("cluster"), r.URL.Query().Get("namespace")
		if !validNamespace(w, ns) {
			return
		}
		name, d, ok := windowOf(w, r)
		if !ok {
			return
		}
		a, err := e.aggregateFor(r, h, cluster, ns, name, d)
		if err != nil {
			writeReadFailure(w, cluster, err)
			return
		}
		writeJSON(w, http.StatusOK, a.view(nil))
	})
}

// object answers `GET /object/{kind}/{ns}/{name}`: the page's answer reduced
// to an object and its pods.
func (e *Events) object(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, ns, name := r.PathValue("cluster"), r.PathValue("namespace"), r.PathValue("name")
		kind, ok := objectKinds[r.PathValue("kind")]
		if !ok {
			writeProblem(w, http.StatusBadRequest, CodeBadKind, `"`+r.PathValue("kind")+`" is not a kind this view reads: `+kindList()+`.`, nil)
			return
		}
		if !validNamespace(w, ns) || !validName(w, name) {
			return
		}
		win, d, ok := windowOf(w, r)
		if !ok {
			return
		}
		a, err := e.aggregateFor(r, h, cluster, ns, win, d)
		if err != nil {
			writeReadFailure(w, cluster, err)
			return
		}
		v := a.view(&objectFilter{kind: kind, name: name})
		v.Object = &ObjectRef{Kind: kind, Namespace: ns, Name: name}
		writeJSON(w, http.StatusOK, v)
	})
}

func kindList() string {
	var names []string
	for k := range objectKinds {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// ObjectRef names the object an `/object` answer is about.
type ObjectRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// Line is the answer of `GET /line/...`: what the panel's compact line says.
type Line struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Warnings is the Warning Events of the last hour on the object and its
	// pods, counted as the page counts them.
	Warnings int `json:"warnings"`
	// LastChange is the most recent change to the object's workload (a
	// rollout, a rollback, a scaling, a referenced ConfigMap, its node), or
	// null when none was found.
	LastChange *Change `json:"lastChange"`
	// LastChangeAgeSeconds is LastChange's age at GeneratedAt; null with it.
	LastChangeAgeSeconds *int64    `json:"lastChangeAgeSeconds"`
	GeneratedAt          time.Time `json:"generatedAt"`
	// Sources says which reads were made, refused or failed, by name.
	Sources map[string]Section `json:"sources"`
	// Complete is false when the person's read was narrowed or cut short; Note
	// says why.
	Complete bool   `json:"complete"`
	Note     string `json:"note,omitempty"`
}

// Section is a part of the answer that was read, refused or failed apart from
// the rest: a person who may list events but not nodes still gets the Events,
// with the node half saying why it is missing.
type Section struct {
	// State is "read", "denied" (the cluster refused this person),
	// "unreviewable", or "failed".
	State string `json:"state"`
	// Complete is false when the read was narrowed or cut short; Note says why.
	Complete bool   `json:"complete"`
	Note     string `json:"note,omitempty"`
}

// Section states.
const (
	StateRead         = "read"
	StateDenied       = "denied"
	StateUnreviewable = "unreviewable"
	StateFailed       = "failed"
)

func sectionOf(complete bool, note string, err error) Section {
	switch {
	case err == nil:
		return Section{State: StateRead, Complete: complete, Note: note}
	case errors.Is(err, extension.ErrDenied):
		return Section{State: StateDenied, Note: "The cluster says you may not make this read."}
	case errors.Is(err, extension.ErrUnreviewable):
		return Section{State: StateUnreviewable, Note: extension.UnreviewedText}
	}
	return Section{State: StateFailed, Note: firstLine(err.Error())}
}

// ends is an error that answers the whole request rather than one section.
func ends(r *http.Request, err error) bool {
	return errors.Is(err, extension.ErrUnknownCluster) || errors.Is(err, context.Canceled) || r.Context().Err() != nil
}

// lineWindow is the panel line's window: the Events' own TTL.
const lineWindow = time.Hour

// line answers `GET /line/{kind}/{ns}/{name}`. A pod reads its own Events and
// its ReplicaSet and nothing else; any other kind is read off the namespace's
// cached aggregate.
func (e *Events) line(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, ns, name := r.PathValue("cluster"), r.PathValue("namespace"), r.PathValue("name")
		kind, ok := objectKinds[r.PathValue("kind")]
		if !ok {
			writeProblem(w, http.StatusBadRequest, CodeBadKind, `"`+r.PathValue("kind")+`" is not a kind this view reads: `+kindList()+`.`, nil)
			return
		}
		if !validNamespace(w, ns) || !validName(w, name) {
			return
		}
		var (
			out Line
			err error
		)
		if kind == "Pod" {
			out, err = e.podLine(r, h, cluster, ns, name)
		} else {
			var a *aggregate
			a, err = e.aggregateFor(r, h, cluster, ns, DefaultWindow, lineWindow)
			if err == nil {
				out = a.line(kind, ns, name)
			}
		}
		if err != nil {
			writeReadFailure(w, cluster, err)
			return
		}
		if out.LastChange != nil {
			age := int64(out.GeneratedAt.Sub(out.LastChange.Time) / time.Second)
			if age < 0 {
				age = 0
			}
			out.LastChangeAgeSeconds = &age
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// podLine reads one pod's own Events (by field selector on the name), the
// pod, and its ReplicaSet for the rollout that made it.
func (e *Events) podLine(r *http.Request, h extension.Host, cluster, ns, name string) (Line, error) {
	now := e.now()
	since := now.Add(-lineWindow)
	out := Line{Kind: "Pod", Namespace: ns, Name: name, GeneratedAt: now, Sources: map[string]Section{}, Complete: true}

	var uid, rsName string
	page, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR, Namespace: ns, FieldSelector: "metadata.name=" + name, Limit: 1})
	if err != nil && ends(r, err) {
		return out, err
	}
	out.Sources["pods"] = sectionOf(page.Complete, page.Note, err)
	if err == nil && len(page.Items) > 0 {
		u := page.Items[0]
		uid = string(u.GetUID())
		for _, o := range u.GetOwnerReferences() {
			if o.Kind == "ReplicaSet" {
				rsName = o.Name
			}
		}
	}

	page, err = h.List(r, cluster, extension.ListQuery{GVR: eventsGVR, Namespace: ns,
		FieldSelector: "involvedObject.name=" + name,
		Each: func(u unstructured.Unstructured) error {
			ev := readEvent(&u)
			if ev.kind != "Pod" || ev.name != name || (uid != "" && ev.uid != "" && ev.uid != uid) {
				return nil
			}
			if ev.last.Before(since) || ev.typ != "Warning" {
				return nil
			}
			out.Warnings += ev.count
			return nil
		}})
	if err != nil {
		// The Events are the line's point: without them there is no answer.
		return out, err
	}
	out.Sources["events"] = sectionOf(page.Complete, page.Note, nil)
	if !page.Complete {
		out.Complete, out.Note = false, page.Note
	}

	if rsName != "" {
		page, err = h.List(r, cluster, extension.ListQuery{GVR: replicaSets, Namespace: ns, FieldSelector: "metadata.name=" + rsName, Limit: 1})
		if err != nil && ends(r, err) {
			return out, err
		}
		out.Sources["replicasets"] = sectionOf(page.Complete, page.Note, err)
		if err == nil && len(page.Items) > 0 {
			rs := readReplicaSet(&page.Items[0])
			if rs.deploy != "" && !rs.created.Before(since.Add(-correlationWindow)) {
				out.LastChange = &Change{ID: fmt.Sprintf("rollout/%s/%s/%d", ns, rs.deploy, rs.revision),
					Kind: ChangeRollout, ObjectKind: "Deployment", Namespace: ns, Name: rs.deploy,
					Time: rs.created, Revision: rs.revision, ImagesAfter: rs.images}
			}
		}
	}
	return out, nil
}

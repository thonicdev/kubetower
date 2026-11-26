package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
)

// Nothing here starts a goroutine; a route that did and left it behind fails
// every test of the package.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

var testNow = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

// fakeHost is extension.Host with List made real: field selectors, a
// namespace, Each, Columns, a narrowed person and a refused one. Every other
// door fails the test when called.
type fakeHost struct {
	t  *testing.T
	mu sync.Mutex

	objects map[string][]unstructured.Unstructured // by resource
	// denied refuses the person a resource; unreviewable cannot ask.
	denied       map[string]bool
	unreviewable map[string]bool
	// allowed narrows the person to these namespaces: an all-namespaces list
	// comes back incomplete, with a Note.
	allowed []string
	queries []extension.ListQuery
	// eachCalls is how many items Each was called with, per resource.
	eachCalls map[string]int
}

func newHost(t *testing.T) *fakeHost {
	return &fakeHost{t: t, objects: map[string][]unstructured.Unstructured{}, denied: map[string]bool{},
		unreviewable: map[string]bool{}, eachCalls: map[string]int{}}
}

func (h *fakeHost) add(resource string, docs ...map[string]any) {
	for _, d := range docs {
		h.objects[resource] = append(h.objects[resource], unstructured.Unstructured{Object: d})
	}
}

// listsOf is how many List calls asked for resource.
func (h *fakeHost) listsOf(resource string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, q := range h.queries {
		if q.GVR.Resource == resource {
			n++
		}
	}
	return n
}

var fieldPath = map[string][]string{
	"metadata.name":       {"metadata", "name"},
	"involvedObject.name": {"involvedObject", "name"},
	"involvedObject.kind": {"involvedObject", "kind"},
	"involvedObject.uid":  {"involvedObject", "uid"},
}

func (h *fakeHost) matches(u *unstructured.Unstructured, selector string) bool {
	if selector == "" {
		return true
	}
	for _, req := range strings.Split(selector, ",") {
		key, want, _ := strings.Cut(req, "=")
		path, ok := fieldPath[key]
		if !ok {
			h.t.Fatalf("field selector on %q, which the fake does not know", key)
		}
		if got, _, _ := unstructured.NestedString(u.Object, path...); got != want {
			return false
		}
	}
	return true
}

func (h *fakeHost) List(_ *http.Request, _ string, q extension.ListQuery) (extension.Page, error) {
	h.mu.Lock()
	h.queries = append(h.queries, q)
	h.mu.Unlock()
	if h.denied[q.GVR.Resource] {
		return extension.Page{}, fmt.Errorf("%w: you may not list %s", extension.ErrDenied, q.GVR.Resource)
	}
	if h.unreviewable[q.GVR.Resource] {
		return extension.Page{}, errors.Join(extension.ErrUnreviewable, errors.New("no review"))
	}
	if q.GVR.Resource == "secrets" {
		h.t.Error("secrets were listed")
		return extension.Page{}, extension.ErrSecretsNotListed
	}
	page := extension.Page{Complete: true}
	clusterScoped := q.GVR.Resource == "nodes"
	if len(h.allowed) > 0 && q.Namespace == "" && !clusterScoped {
		page.Complete, page.Note = false, "Shown only from the namespaces you may list."
	}
	for i := range h.objects[q.GVR.Resource] {
		u := &h.objects[q.GVR.Resource][i]
		if q.Namespace != "" && u.GetNamespace() != q.Namespace {
			continue
		}
		if len(h.allowed) > 0 && u.GetNamespace() != "" && !contains(h.allowed, u.GetNamespace()) {
			continue
		}
		if !h.matches(u, q.FieldSelector) {
			continue
		}
		if q.Limit > 0 && int64(len(page.Items)) >= q.Limit {
			page.Complete, page.Note = false, "Cut at the limit."
			break
		}
		switch {
		case q.Each != nil:
			h.mu.Lock()
			h.eachCalls[q.GVR.Resource]++
			h.mu.Unlock()
			if err := q.Each(*u); err != nil {
				return page, err
			}
		case q.Columns:
			page.Columns = []string{"Name"}
			created, _ := time.Parse(time.RFC3339, u.GetCreationTimestamp().UTC().Format(time.RFC3339))
			page.Rows = append(page.Rows, extension.Row{Name: u.GetName(), Created: created, Cells: []any{u.GetName()}})
		default:
			page.Items = append(page.Items, *u.DeepCopy())
		}
	}
	return page, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (h *fakeHost) fail(name string) {
	h.t.Errorf("Host.%s was called: this extra reads only through List", name)
}

func (h *fakeHost) Client(*http.Request, string) (extension.Clients, error) {
	h.fail("Client")
	return extension.Clients{}, errors.New("not used")
}
func (h *fakeHost) Forward(context.Context, string, string, string, int) (extension.Tunnel, error) {
	h.fail("Forward")
	return extension.Tunnel{}, errors.New("not used")
}
func (h *fakeHost) ForwardAsConsole(*http.Request, string, string, string, int) (extension.Tunnel, error) {
	h.fail("ForwardAsConsole")
	return extension.Tunnel{}, errors.New("not used")
}
func (h *fakeHost) Reveal(*http.Request, string, string, string, string) (string, error) {
	h.fail("Reveal")
	return "", errors.New("not used")
}
func (h *fakeHost) Definitions(*http.Request, string) (extension.CRDRegistry, error) {
	h.fail("Definitions")
	return nil, errors.New("not used")
}
func (h *fakeHost) Exec(*http.Request, string, extension.ExecQuery) (extension.ExecResult, error) {
	h.fail("Exec")
	return extension.ExecResult{}, errors.New("not used")
}
func (h *fakeHost) RefreshApplication(*http.Request, string, string, string) error {
	return errors.New("RefreshApplication is not used here")
}

func (h *fakeHost) ConfirmToken(*http.Request) string { h.fail("ConfirmToken"); return "" }
func (h *fakeHost) Setting(*http.Request, string, string) string {
	return ""
}
func (h *fakeHost) MayReadLogs(*http.Request, string, string) error {
	h.fail("MayReadLogs")
	return errors.New("not used")
}
func (h *fakeHost) Usage(*http.Request, string, []string) (extension.Usage, error) {
	h.fail("Usage")
	return extension.Usage{}, errors.New("not used")
}
func (h *fakeHost) ContainerUsage(*http.Request, string, string) (extension.LiveUsage, error) {
	h.fail("ContainerUsage")
	return extension.LiveUsage{}, errors.New("not used")
}
func (h *fakeHost) UsageWindow(*http.Request, string, string) (extension.Window, error) {
	h.fail("UsageWindow")
	return extension.Window{}, errors.New("not used")
}

func (h *fakeHost) DNSWindow(*http.Request, string, extension.DNSSpan) (extension.DNS, error) {
	return extension.DNS{}, nil
}

func (h *fakeHost) VolumeFill(*http.Request, string, []extension.ClaimRef) (extension.VolumeFill, error) {
	return extension.VolumeFill{State: extension.WindowNoPrometheus}, nil
}
func (h *fakeHost) ConfiguredSecret(*http.Request, string, string, string, string) (string, error) {
	h.fail("ConfiguredSecret")
	return "", extension.ErrNotConfigured
}
func (h *fakeHost) ConfiguredService(*http.Request, string, string, string) (corev1.Service, error) {
	h.fail("ConfiguredService")
	return corev1.Service{}, extension.ErrNotConfigured
}

// --- objects ---------------------------------------------------------------

func rfc(t time.Time) string   { return t.UTC().Format(time.RFC3339) }
func micro(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000Z") }

// oldEvent is an Event as the kubelet writes it: a count and both timestamps.
func oldEvent(ns, kind, name, reason, msg, typ string, count int, first, last time.Time) map[string]any {
	return map[string]any{"apiVersion": "v1", "kind": "Event",
		"metadata":       map[string]any{"namespace": ns, "name": fmt.Sprintf("%s.%s.%d", name, reason, last.UnixNano()), "creationTimestamp": rfc(first)},
		"involvedObject": map[string]any{"kind": kind, "namespace": ns, "name": name, "uid": "uid-" + name},
		"reason":         reason, "message": msg, "type": typ, "count": int64(count),
		"firstTimestamp": rfc(first), "lastTimestamp": rfc(last),
		"source": map[string]any{"component": "kubelet"}}
}

// newEvent is an Event as the scheduler writes it through events.k8s.io: no
// count, no first or last timestamp, an eventTime, a series when it repeats.
func newEvent(ns, kind, name, reason, msg, typ string, at time.Time, series int) map[string]any {
	e := map[string]any{"apiVersion": "v1", "kind": "Event",
		"metadata":       map[string]any{"namespace": ns, "name": fmt.Sprintf("%s.%s.%d", name, reason, at.UnixNano()), "creationTimestamp": rfc(at)},
		"involvedObject": map[string]any{"kind": kind, "namespace": ns, "name": name, "uid": "uid-" + name},
		"reason":         reason, "message": msg, "type": typ, "count": nil,
		"eventTime": micro(at), "reportingComponent": "default-scheduler"}
	if series > 1 {
		e["series"] = map[string]any{"count": int64(series), "lastObservedTime": micro(at.Add(time.Minute))}
	}
	return e
}

func replicaSetDoc(ns, name, deploy string, revision int, created time.Time, image string, configs ...string) map[string]any {
	var vols []any
	for _, c := range configs {
		vols = append(vols, map[string]any{"name": c, "configMap": map[string]any{"name": c}})
	}
	meta := map[string]any{"namespace": ns, "name": name, "uid": "uid-" + name, "creationTimestamp": rfc(created),
		"annotations": map[string]any{revisionAnnotation: fmt.Sprint(revision)}}
	if deploy != "" {
		meta["ownerReferences"] = []any{map[string]any{"kind": "Deployment", "name": deploy, "controller": true}}
	}
	return map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": meta,
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{"name": "app", "image": image}}, "volumes": vols}}}}
}

func podDoc(ns, name, rs, node string, extra func(map[string]any)) map[string]any {
	meta := map[string]any{"namespace": ns, "name": name, "uid": "uid-" + name, "creationTimestamp": rfc(testNow.Add(-time.Hour))}
	if rs != "" {
		meta["ownerReferences"] = []any{map[string]any{"kind": "ReplicaSet", "name": rs, "controller": true}}
	}
	d := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": meta, "spec": map[string]any{"nodeName": node},
		"status": map[string]any{"phase": "Running"}}
	if extra != nil {
		extra(d)
	}
	return d
}

func crashLooping(d map[string]any) {
	d["status"] = map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"name": "app",
		"state": map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff"}}}}}
}

func oomKilled(at time.Time) func(map[string]any) {
	return func(d map[string]any) {
		d["status"] = map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"name": "app",
			"lastState": map[string]any{"terminated": map[string]any{"reason": "OOMKilled", "finishedAt": rfc(at)}}}}}
	}
}

func configMapDoc(ns, name, manager string, at time.Time) map[string]any {
	return map[string]any{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"namespace": ns, "name": name, "creationTimestamp": rfc(at.Add(-100 * time.Hour)),
			"managedFields": []any{
				map[string]any{"manager": "old-manager", "operation": "Update", "time": rfc(at.Add(-50 * time.Hour))},
				map[string]any{"manager": manager, "operation": "Update", "time": rfc(at)}}},
		"data": map[string]any{"secretish": "NEVER-READ-ME"}}
}

func nodeDoc(name string, created time.Time) map[string]any {
	return map[string]any{"apiVersion": "v1", "kind": "Node",
		"metadata": map[string]any{"name": name, "creationTimestamp": rfc(created)}}
}

// --- requests ----------------------------------------------------------------

// newExt is the extension with a fixed clock.
func newExt() *Events {
	e := New()
	e.now = func() time.Time { return testNow }
	return e
}

// serve answers one GET on the routes as the console mounts them, as the
// person whose session cookie is cookie (none when empty).
func serve(t *testing.T, e *Events, h extension.Host, path, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	for _, r := range e.Routes(h) {
		mux.Handle(r.Method+" /x/{cluster}/events"+r.Pattern, r.Handler)
	}
	req := httptest.NewRequest(http.MethodGet, "/x/kind/events"+path, nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	return out
}

// page is the page's answer for a path, which must be a 200.
func page(t *testing.T, e *Events, h extension.Host, path string) Response {
	t.Helper()
	rec := serve(t, e, h, path, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
	}
	return decode[Response](t, rec)
}

func causeOf(r Response, reason string) *Cause {
	for i := range r.Causes {
		if r.Causes[i].Reason == reason {
			return &r.Causes[i]
		}
	}
	return nil
}

func changeOf(r Response, id string) *Change {
	for i := range r.Changes {
		if r.Changes[i].ID == id {
			return &r.Changes[i]
		}
	}
	return nil
}

package pending

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thonicdev/kubetower/extension"
)

// Nothing here starts a goroutine; a route that did and left it behind
// fails every test of the package.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

var testNow = time.Date(2026, 11, 11, 14, 30, 0, 0, time.UTC)

// fakeHost is extension.Host with List made real - field selectors on the
// fields this extra asks about, a namespace, a limit, a narrowed person and a
// refused one - and every other door failing the test when called.
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
	unknown bool
	queries []extension.ListQuery
}

func newHost(t *testing.T) *fakeHost {
	return &fakeHost{t: t, objects: map[string][]unstructured.Unstructured{}, denied: map[string]bool{}, unreviewable: map[string]bool{}}
}

func (h *fakeHost) add(resourceName string, objs ...any) {
	for _, o := range objs {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
		if err != nil {
			h.t.Fatal(err)
		}
		h.objects[resourceName] = append(h.objects[resourceName], unstructured.Unstructured{Object: m})
	}
}

func (h *fakeHost) Client(*http.Request, string) (extension.Clients, error) {
	h.t.Error("Host.Client was called: this extra reads only through the reviewed doors")
	return extension.Clients{}, errors.New("not used")
}
func (h *fakeHost) Forward(context.Context, string, string, string, int) (extension.Tunnel, error) {
	h.t.Error("Host.Forward was called")
	return extension.Tunnel{}, errors.New("not used")
}
func (h *fakeHost) ForwardAsConsole(*http.Request, string, string, string, int) (extension.Tunnel, error) {
	h.t.Error("Host.ForwardAsConsole was called")
	return extension.Tunnel{}, errors.New("not used")
}
func (h *fakeHost) Reveal(*http.Request, string, string, string, string) (string, error) {
	h.t.Error("Host.Reveal was called")
	return "", errors.New("not used")
}
func (h *fakeHost) Definitions(*http.Request, string) (extension.CRDRegistry, error) {
	h.t.Error("Host.Definitions was called")
	return nil, errors.New("not used")
}
func (h *fakeHost) Exec(*http.Request, string, extension.ExecQuery) (extension.ExecResult, error) {
	h.t.Error("Host.Exec was called")
	return extension.ExecResult{}, errors.New("not used")
}
func (h *fakeHost) RefreshApplication(*http.Request, string, string, string) error {
	return errors.New("RefreshApplication is not used here")
}

func (h *fakeHost) ConfirmToken(*http.Request) string {
	h.t.Error("Host.ConfirmToken was called: nothing here writes")
	return ""
}
func (h *fakeHost) Setting(*http.Request, string, string) string { return "" }
func (h *fakeHost) MayReadLogs(*http.Request, string, string) error {
	h.t.Error("Host.MayReadLogs was called")
	return errors.New("not used")
}
func (h *fakeHost) Usage(*http.Request, string, []string) (extension.Usage, error) {
	h.t.Error("Host.Usage was called")
	return extension.Usage{}, errors.New("not used")
}
func (h *fakeHost) ContainerUsage(*http.Request, string, string) (extension.LiveUsage, error) {
	h.t.Error("Host.ContainerUsage was called")
	return extension.LiveUsage{}, errors.New("not used")
}
func (h *fakeHost) UsageWindow(*http.Request, string, string) (extension.Window, error) {
	h.t.Error("Host.UsageWindow was called")
	return extension.Window{}, errors.New("not used")
}

func (h *fakeHost) DNSWindow(*http.Request, string, extension.DNSSpan) (extension.DNS, error) {
	return extension.DNS{}, nil
}

// fieldPath is where each selectable field lives in the object.
var fieldPath = map[string][]string{
	"status.phase":        {"status", "phase"},
	"spec.nodeName":       {"spec", "nodeName"},
	"metadata.name":       {"metadata", "name"},
	"reason":              {"reason"},
	"involvedObject.kind": {"involvedObject", "kind"},
	"involvedObject.name": {"involvedObject", "name"},
}

func (h *fakeHost) matches(u *unstructured.Unstructured, selector string) bool {
	if selector == "" {
		return true
	}
	for _, req := range strings.Split(selector, ",") {
		neg := strings.Contains(req, "!=")
		key, want, _ := strings.Cut(strings.Replace(req, "!=", "=", 1), "=")
		path, ok := fieldPath[key]
		if !ok {
			h.t.Fatalf("field selector on %q, which the fake does not know", key)
		}
		got, _, _ := unstructured.NestedString(u.Object, path...)
		if (got == want) == neg {
			return false
		}
	}
	return true
}

func (h *fakeHost) List(_ *http.Request, _ string, q extension.ListQuery) (extension.Page, error) {
	h.mu.Lock()
	h.queries = append(h.queries, q)
	h.mu.Unlock()
	if h.unknown {
		return extension.Page{}, errors.Join(extension.ErrUnknownCluster, errors.New("unknown"))
	}
	if h.denied[q.GVR.Resource] {
		return extension.Page{}, errors.Join(extension.ErrDenied, errors.New("forbidden"))
	}
	if h.unreviewable[q.GVR.Resource] {
		return extension.Page{}, errors.Join(extension.ErrUnreviewable, errors.New("no review"))
	}
	page := extension.Page{Complete: true}
	if len(h.allowed) > 0 && q.Namespace == "" && q.GVR.Resource != "nodes" && q.GVR.Resource != "persistentvolumes" && q.GVR.Resource != "storageclasses" {
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
		page.Items = append(page.Items, *u.DeepCopy())
	}
	return page, nil
}

func quantities(pairs ...string) corev1.ResourceList {
	out := corev1.ResourceList{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out[corev1.ResourceName(pairs[i])] = resource.MustParse(pairs[i+1])
	}
	return out
}

func node(name, cpu, mem string, labels map[string]string, taints ...corev1.Taint) *corev1.Node {
	return &corev1.Node{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Node"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec:       corev1.NodeSpec{Taints: taints},
		Status: corev1.NodeStatus{
			Allocatable: quantities("cpu", cpu, "memory", mem, "pods", "110"),
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

func container(cpu, mem string) corev1.Container {
	return corev1.Container{Name: "app", Image: "app:1", Resources: corev1.ResourceRequirements{Requests: quantities("cpu", cpu, "memory", mem)}}
}

func placed(namespace, name, nodeName string, c ...corev1.Container) *corev1.Pod {
	return &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, CreationTimestamp: metav1.NewTime(testNow.Add(-time.Hour))},
		Spec:       corev1.PodSpec{NodeName: nodeName, Containers: c},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

// unschedulable is a pod the scheduler refused, with its message.
func unschedulable(namespace, name, message string, age time.Duration, c ...corev1.Container) *corev1.Pod {
	return &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID("uid-" + name), CreationTimestamp: metav1.NewTime(testNow.Add(-age))},
		Spec:       corev1.PodSpec{Containers: c},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
				Message: message, LastTransitionTime: metav1.NewTime(testNow.Add(-age)),
			}},
		},
	}
}

func event(namespace, kind, name, reason, message string, at time.Time, count int32) *corev1.Event {
	return &corev1.Event{
		TypeMeta:       metav1.TypeMeta{APIVersion: "v1", Kind: "Event"},
		ObjectMeta:     metav1.ObjectMeta{Namespace: namespace, Name: name + "." + reason + at.Format("150405")},
		InvolvedObject: corev1.ObjectReference{Kind: kind, Namespace: namespace, Name: name},
		Reason:         reason, Message: message, Type: corev1.EventTypeWarning,
		EventTime:           metav1.NewMicroTime(at),
		Series:              seriesOf(count, at),
		ReportingController: "default-scheduler",
	}
}

func seriesOf(count int32, at time.Time) *corev1.EventSeries {
	if count < 2 {
		return nil
	}
	return &corev1.EventSeries{Count: count, LastObservedTime: metav1.NewMicroTime(at.Add(time.Minute))}
}

// serve answers one GET on the extension's routes, as the console mounts
// them under /x/{cluster}/pending.
func serve(t *testing.T, h extension.Host, path string) *httptest.ResponseRecorder {
	t.Helper()
	p := Pending{now: func() time.Time { return testNow }}
	mux := http.NewServeMux()
	for _, r := range p.Routes(h) {
		mux.Handle(r.Method+" /x/{cluster}/pending"+r.Pattern, r.Handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x/kind/pending"+path, nil))
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

// ConfiguredSecret and ConfiguredService are the console's reads of what its
// configuration names, which this extra never makes: it names nothing.
func (h *fakeHost) ConfiguredSecret(*http.Request, string, string, string, string) (string, error) {
	h.t.Error("Host.ConfiguredSecret was called: this extra reads no configured Secret")
	return "", extension.ErrNotConfigured
}

func (h *fakeHost) ConfiguredService(*http.Request, string, string, string) (corev1.Service, error) {
	h.t.Error("Host.ConfiguredService was called: this extra reads no configured Service")
	return corev1.Service{}, extension.ErrNotConfigured
}

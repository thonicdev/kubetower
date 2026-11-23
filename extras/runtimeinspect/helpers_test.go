package runtimeinspect

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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/thonicdev/kubetower/extension"
)

// fakeHost is extension.Host with List made real, and every other door
// failing the test when called: this extra reads pods through the reviewed
// list and nothing else.
type fakeHost struct {
	t    *testing.T
	pods []unstructured.Unstructured
	// listErr fails every List.
	listErr error
	// narrowed answers every list with Complete false and a Note.
	narrowed bool
	// queries is every List asked, in order.
	queries []extension.ListQuery
	// live is what ContainerUsage answers, keyed extension.ContainerKey;
	// nil with usageErr nil is a cluster with no metrics-server.
	live     map[string]extension.Resources
	usageErr error
	// usageAsked is every namespace ContainerUsage was asked for.
	usageAsked []string
	// usageHangs makes ContainerUsage answer only once the request is gone,
	// as a metrics-server that does not answer would.
	usageHangs bool
	// window is what UsageWindow answers; nil with windowErr nil fails the
	// test when it is called. windowHangs answers only once the request is
	// gone.
	window      *extension.Window
	windowErr   error
	windowHangs bool
	windowAsked []string
	mu          sync.Mutex
	// execs answers Exec by the argv joined with spaces; an argv it does not
	// hold fails the test. Nil fails every Exec: the routes that read the
	// spec run nothing.
	execs map[string]execAnswer
	// ran is every argv Exec was asked to run, in order.
	ran [][]string
	// timeouts is every ExecQuery.Timeout, in order.
	timeouts []time.Duration
	// block holds an argv until its request is canceled, and closes blocked
	// when it starts waiting.
	block   map[string]bool
	blocked chan struct{}
}

// execAnswer is what one argv answers.
type execAnswer struct {
	res extension.ExecResult
	err error
}

func newHost(t *testing.T, pods ...*corev1.Pod) *fakeHost {
	h := &fakeHost{t: t}
	for _, p := range pods {
		obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(p)
		if err != nil {
			t.Fatal(err)
		}
		h.pods = append(h.pods, unstructured.Unstructured{Object: obj})
	}
	return h
}

func (h *fakeHost) List(_ *http.Request, _ string, q extension.ListQuery) (extension.Page, error) {
	h.mu.Lock()
	h.queries = append(h.queries, q)
	h.mu.Unlock()
	if h.listErr != nil {
		return extension.Page{}, h.listErr
	}
	if q.GVR != podsGVR {
		h.t.Errorf("List of %v: this extra lists pods only", q.GVR)
	}
	selector, err := fields.ParseSelector(q.FieldSelector)
	if err != nil {
		return extension.Page{}, err
	}
	var out []unstructured.Unstructured
	for _, p := range h.pods {
		if q.Namespace != "" && p.GetNamespace() != q.Namespace {
			continue
		}
		if !selector.Matches(fields.Set{"metadata.name": p.GetName(), "metadata.namespace": p.GetNamespace()}) {
			continue
		}
		out = append(out, p)
	}
	if h.narrowed {
		return extension.Page{Items: out, Note: "You may not list pods in every namespace."}, nil
	}
	if q.Limit > 0 && int64(len(out)) > q.Limit {
		return extension.Page{Items: out[:q.Limit], Note: "The list stopped at its limit."}, nil
	}
	return extension.Page{Items: out, Complete: true}, nil
}

func (h *fakeHost) Client(*http.Request, string) (extension.Clients, error) {
	h.t.Error("Host.Client was called: this extra reads only through the reviewed list")
	return extension.Clients{}, errors.New("not used")
}

func (h *fakeHost) Definitions(*http.Request, string) (extension.CRDRegistry, error) {
	h.t.Error("Host.Definitions was called")
	return nil, errors.New("not used")
}

func (h *fakeHost) Reveal(*http.Request, string, string, string, string) (string, error) {
	h.t.Error("Host.Reveal was called: nothing here reads a Secret")
	return "", errors.New("not used")
}

func (h *fakeHost) MayReadLogs(*http.Request, string, string) error {
	h.t.Error("Host.MayReadLogs was called")
	return errors.New("not used")
}

func (h *fakeHost) Usage(*http.Request, string, []string) (extension.Usage, error) {
	h.t.Error("Host.Usage was called")
	return extension.Usage{}, errors.New("not used")
}

func (h *fakeHost) ContainerUsage(r *http.Request, _ string, namespace string) (extension.LiveUsage, error) {
	h.mu.Lock()
	h.usageAsked = append(h.usageAsked, namespace)
	h.mu.Unlock()
	if h.usageHangs {
		<-r.Context().Done()
		return extension.LiveUsage{}, r.Context().Err()
	}
	if h.usageErr != nil {
		return extension.LiveUsage{}, h.usageErr
	}
	if h.live == nil {
		return extension.LiveUsage{}, &extension.MetricsUnavailable{Reason: extension.MetricsAbsent,
			Detail: "the cluster has no metrics.k8s.io API"}
	}
	return extension.LiveUsage{Containers: h.live, Complete: true}, nil
}

func (h *fakeHost) Exec(r *http.Request, _ string, q extension.ExecQuery) (extension.ExecResult, error) {
	h.ran = append(h.ran, q.Command)
	h.timeouts = append(h.timeouts, q.Timeout)
	if h.block[strings.Join(q.Command, " ")] {
		close(h.blocked)
		<-r.Context().Done()
		return extension.ExecResult{}, r.Context().Err()
	}
	if h.execs == nil {
		h.t.Errorf("Host.Exec(%q) was called: this route runs nothing in a container", q.Command)
		return extension.ExecResult{}, errors.New("not used")
	}
	answer, ok := h.execs[strings.Join(q.Command, " ")]
	if !ok {
		h.t.Errorf("Host.Exec(%q): an argv the test does not expect", q.Command)
		return extension.ExecResult{}, errors.New("unexpected argv")
	}
	return answer.res, answer.err
}

func (h *fakeHost) UsageWindow(r *http.Request, _ string, namespace string) (extension.Window, error) {
	if h.window == nil && h.windowErr == nil && !h.windowHangs {
		h.t.Error("Host.UsageWindow was called")
		return extension.Window{}, errors.New("not used")
	}
	h.mu.Lock()
	h.windowAsked = append(h.windowAsked, namespace)
	h.mu.Unlock()
	if h.windowHangs {
		<-r.Context().Done()
		return extension.Window{}, r.Context().Err()
	}
	if h.windowErr != nil {
		return extension.Window{}, h.windowErr
	}
	return *h.window, nil
}

func (h *fakeHost) DNSWindow(*http.Request, string, extension.DNSSpan) (extension.DNS, error) {
	return extension.DNS{}, nil
}

func (h *fakeHost) ForwardAsConsole(*http.Request, string, string, string, int) (extension.Tunnel, error) {
	h.t.Error("Host.ForwardAsConsole was called: nothing here opens a tunnel")
	return extension.Tunnel{}, errors.New("not used")
}

func (h *fakeHost) Forward(context.Context, string, string, string, int) (extension.Tunnel, error) {
	h.t.Error("Host.Forward was called: nothing here opens a tunnel")
	return extension.Tunnel{}, errors.New("not used")
}

func (h *fakeHost) Setting(*http.Request, string, string) string { return "" }

func (h *fakeHost) RefreshApplication(*http.Request, string, string, string) error {
	return errors.New("RefreshApplication is not used here")
}

func (h *fakeHost) ConfirmToken(*http.Request) string {
	h.t.Error("Host.ConfirmToken was called: nothing here writes")
	return ""
}

// pod is a running pod with containers.
func pod(namespace, name string, containers ...corev1.Container) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       corev1.PodSpec{Containers: containers},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

// memory sets a container's memory request and limit; "" leaves one unset.
func memory(c corev1.Container, request, limit string) corev1.Container {
	if request != "" {
		if c.Resources.Requests == nil {
			c.Resources.Requests = corev1.ResourceList{}
		}
		c.Resources.Requests[corev1.ResourceMemory] = resource.MustParse(request)
	}
	if limit != "" {
		if c.Resources.Limits == nil {
			c.Resources.Limits = corev1.ResourceList{}
		}
		c.Resources.Limits[corev1.ResourceMemory] = resource.MustParse(limit)
	}
	return c
}

func env(pairs ...string) []corev1.EnvVar {
	var out []corev1.EnvVar
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, corev1.EnvVar{Name: pairs[i], Value: pairs[i+1]})
	}
	return out
}

// serve answers path through the extra's own routes, mounted as the console
// mounts them under /x/{cluster}/runtime.
func serve(t *testing.T, h extension.Host, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	for _, r := range New().Routes(h) {
		mux.Handle(r.Method+" /x/{cluster}/"+Key+r.Pattern, r.Handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("answered %s, not JSON: %s", rec.Header().Get("Content-Type"), rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("not the documented JSON: %v (%s)", err, rec.Body)
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

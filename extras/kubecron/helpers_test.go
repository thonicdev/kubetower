package kubecron

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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/thonicdev/kubetower/extension"
)

// The clock every test stands at.
var testNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

const testToken = "s3cret-token"

// fakeHost is extension.Host with what this extra uses made real: List answers
// from the objects it holds (filtered by namespace and metadata.name, as the
// console's list does), Client hands a fake clientset, and Forward hands the
// address of a fake collector.
type fakeHost struct {
	objects     map[string][]unstructured.Unstructured // by resource
	listErr     error
	clients     *fake.Clientset
	forwardTo   string
	forwardErr  error
	forwards    int
	forwardedTo []string // namespace/service of every forward opened
	stopped     int
	hiddenNames map[string]bool       // CronJobs the person may not list
	logsErr     error                 // what MayReadLogs answers; nil lets the person read logs
	logsAsked   []string              // the namespace of every MayReadLogs
	settings    map[string]string     // what Setting answers, by key
	listed      []extension.ListQuery // every List asked, in order
	// hiddenNamespaces are namespaces the person may not list anything in:
	// List leaves their rows out and says the answer was narrowed, as the
	// console's own List does for a person refused there.
	hiddenNamespaces map[string]bool
	// consoleReads is every ConfiguredService and ConfiguredSecret asked,
	// "service ns/name" or "secret ns/name#key"; revealed every Reveal.
	consoleReads []string
	revealed     []string
}

// Client answers discovery only, as the core's does: this extra reads no
// object through it, so any read through it fails the test.
func (h *fakeHost) Client(*http.Request, string) (extension.Clients, error) {
	return extension.Clients{}, errors.New("the clients handed to an extension answer discovery only")
}

// ConfiguredService and ConfiguredSecret are the core's doors for what the
// configuration names, read here from the fake clientset as the console's
// credentials would, with the core's sentinels.
func (h *fakeHost) ConfiguredService(r *http.Request, _, namespace, name string) (corev1.Service, error) {
	h.consoleReads = append(h.consoleReads, "service "+namespace+"/"+name)
	svc, err := viaClientset(h.clients).service(r.Context(), namespace, name)
	if err != nil {
		return corev1.Service{}, err
	}
	return *svc, nil
}

func (h *fakeHost) ConfiguredSecret(r *http.Request, _, namespace, name, key string) (string, error) {
	h.consoleReads = append(h.consoleReads, "secret "+namespace+"/"+name+"#"+key)
	return viaClientset(h.clients).token(r.Context(), namespace, name, key, true)
}
func (h *fakeHost) Definitions(*http.Request, string) (extension.CRDRegistry, error) {
	return nil, errors.New("not used")
}
func (h *fakeHost) Usage(*http.Request, string, []string) (extension.Usage, error) {
	return extension.Usage{}, errors.New("not used")
}
func (h *fakeHost) ContainerUsage(*http.Request, string, string) (extension.LiveUsage, error) {
	return extension.LiveUsage{}, errors.New("not used")
}
func (h *fakeHost) UsageWindow(*http.Request, string, string) (extension.Window, error) {
	return extension.Window{}, errors.New("not used")
}

func (h *fakeHost) DNSWindow(*http.Request, string, extension.DNSSpan) (extension.DNS, error) {
	return extension.DNS{}, nil
}
func (h *fakeHost) Exec(*http.Request, string, extension.ExecQuery) (extension.ExecResult, error) {
	return extension.ExecResult{}, errors.New("this fake runs no command")
}

func (h *fakeHost) RefreshApplication(*http.Request, string, string, string) error {
	return errors.New("RefreshApplication is not used here")
}

func (h *fakeHost) ConfirmToken(*http.Request) string             { return "" }
func (h *fakeHost) Setting(_ *http.Request, _, key string) string { return h.settings[key] }
func (h *fakeHost) MayReadLogs(_ *http.Request, _, namespace string) error {
	h.logsAsked = append(h.logsAsked, namespace)
	return h.logsErr
}

// Forward is the person's tunnel, which this extra never opens: the collector
// is the console's backend, reached through ForwardAsConsole. A session that
// came this way would fail to open.
func (h *fakeHost) Forward(context.Context, string, string, string, int) (extension.Tunnel, error) {
	return extension.Tunnel{}, errors.New("the collector is reached through the console's tunnel, never the person's")
}

// ForwardAsConsole hands the address of the fake collector, and counts it.
func (h *fakeHost) ForwardAsConsole(_ *http.Request, _, namespace, service string, _ int) (extension.Tunnel, error) {
	if h.forwardErr != nil {
		return extension.Tunnel{}, h.forwardErr
	}
	h.forwards++
	h.forwardedTo = append(h.forwardedTo, namespace+"/"+service)
	return extension.Tunnel{Addr: h.forwardTo, Pod: "collector-0", PodKnown: true, Stop: func() { h.stopped++ }}, nil
}

// Reveal is the person's read of the token of a collector found by its label.
func (h *fakeHost) Reveal(r *http.Request, _, namespace, name, key string) (string, error) {
	h.revealed = append(h.revealed, namespace+"/"+name+"#"+key)
	return viaClientset(h.clients).token(r.Context(), namespace, name, key, false)
}

func (h *fakeHost) List(_ *http.Request, _ string, q extension.ListQuery) (extension.Page, error) {
	h.listed = append(h.listed, q)
	if h.listErr != nil {
		return extension.Page{}, h.listErr
	}
	var out []unstructured.Unstructured
	objects := h.objects[q.GVR.Resource]
	if q.GVR.Resource == "services" && h.clients != nil {
		objects = append(append([]unstructured.Unstructured(nil), objects...), clientsetServices(h.clients)...)
	}
	narrowed := false
	for _, o := range objects {
		if h.hiddenNamespaces[o.GetNamespace()] {
			narrowed = true
			continue
		}
		if q.Namespace != "" && o.GetNamespace() != q.Namespace {
			continue
		}
		if name, ok := strings.CutPrefix(q.FieldSelector, "metadata.name="); ok && o.GetName() != name {
			continue
		}
		if key, value, ok := strings.Cut(q.LabelSelector, "="); ok && o.GetLabels()[key] != value {
			continue
		}
		if q.GVR.Resource == "cronjobs" && h.hiddenNames[o.GetNamespace()+"/"+o.GetName()] {
			continue
		}
		out = append(out, o)
	}
	if q.Limit > 0 && int64(len(out)) > q.Limit {
		return extension.Page{Items: out[:q.Limit], Note: "limited"}, nil
	}
	if narrowed || len(h.hiddenNamespaces) > 0 && q.Namespace == "" {
		return extension.Page{Items: out, Note: "You may not list " + q.GVR.Resource + " in every namespace, so this holds only the namespaces where you may."}, nil
	}
	return extension.Page{Items: out, Complete: true}, nil
}

// clientsetServices is every Service the fake clientset holds, as List answers
// them.
func clientsetServices(clients *fake.Clientset) []unstructured.Unstructured {
	list, err := clients.Tracker().List(corev1.SchemeGroupVersion.WithResource("services"),
		corev1.SchemeGroupVersion.WithKind("Service"), "")
	if err != nil {
		return nil
	}
	var out []unstructured.Unstructured
	for _, svc := range list.(*corev1.ServiceList).Items {
		raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&svc)
		if err != nil {
			continue
		}
		out = append(out, unstructured.Unstructured{Object: raw})
	}
	return out
}

// clientsetReads is reads over a fake clientset, answering as the console's
// doors answer: the core's sentinels for a missing object or key, and a
// refusal wrapped as one. discover's tests hand it the cluster they build.
type clientsetReads struct{ clients kubernetes.Interface }

func viaClientset(clients kubernetes.Interface) clientsetReads {
	return clientsetReads{clients: clients}
}

func (c clientsetReads) service(ctx context.Context, namespace, name string) (*corev1.Service, error) {
	svc, err := c.clients.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil, fmt.Errorf("%w: %s/%s", extension.ErrNoSuchService, namespace, name)
	case apierrors.IsForbidden(err):
		return nil, fmt.Errorf("%w: %w", extension.ErrDenied, err)
	}
	return svc, err
}

func (c clientsetReads) labelled(ctx context.Context) ([]corev1.Service, string, error) {
	list, err := c.clients.CoreV1().Services("").List(ctx, metav1.ListOptions{LabelSelector: collectorLabel})
	if apierrors.IsForbidden(err) {
		return nil, "", fmt.Errorf("%w: %w", extension.ErrDenied, err)
	}
	if err != nil {
		return nil, "", err
	}
	return list.Items, "", nil
}

func (c clientsetReads) token(ctx context.Context, namespace, name, key string, _ bool) (string, error) {
	secret, err := c.clients.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return "", fmt.Errorf("%w: %s/%s", extension.ErrNoSuchSecret, namespace, name)
	case apierrors.IsForbidden(err):
		return "", fmt.Errorf("%w: %w", extension.ErrRevealDenied, err)
	case err != nil:
		return "", err
	}
	value, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("%w: %q", extension.ErrNoSuchKey, key)
	}
	return string(value), nil
}

func obj(t *testing.T, doc map[string]any) unstructured.Unstructured {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var u unstructured.Unstructured
	if err := u.UnmarshalJSON(raw); err != nil {
		t.Fatal(err)
	}
	return u
}

func cronJob(t *testing.T, ns, name, schedule string, lastSchedule time.Time, extra map[string]any) unstructured.Unstructured {
	spec := map[string]any{"schedule": schedule, "jobTemplate": map[string]any{"spec": map[string]any{}}}
	for k, v := range extra {
		spec[k] = v
	}
	status := map[string]any{}
	if !lastSchedule.IsZero() {
		status["lastScheduleTime"] = lastSchedule.Format(time.RFC3339)
	}
	return obj(t, map[string]any{
		"apiVersion": "batch/v1", "kind": "CronJob",
		"metadata": map[string]any{"namespace": ns, "name": name},
		"spec":     spec, "status": status,
	})
}

func job(t *testing.T, ns, name, owner string, start, end time.Time, condition string) unstructured.Unstructured {
	meta := map[string]any{"namespace": ns, "name": name}
	if owner != "" {
		meta["ownerReferences"] = []any{map[string]any{"apiVersion": "batch/v1", "kind": "CronJob", "name": owner, "uid": "u"}}
	}
	status := map[string]any{"startTime": start.Format(time.RFC3339)}
	if condition != "" {
		status["conditions"] = []any{map[string]any{"type": condition, "status": "True", "lastTransitionTime": end.Format(time.RFC3339)}}
		if condition == "Complete" {
			status["completionTime"] = end.Format(time.RFC3339)
		}
	}
	return obj(t, map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": meta, "status": status})
}

// collectorService is the Service the chart writes in server mode.
func collectorService(auth string) *corev1.Service {
	ann := map[string]string{annAPIPath: "/api/v1"}
	switch auth {
	case "bearer":
		ann[annAuth], ann["kubecron.io/auth-secret"], ann["kubecron.io/auth-secret-key"] = "bearer", "kubecron-api", "api-token"
	case "none":
		ann[annAuth] = "none"
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kubecron", Name: "kubecron",
			Labels: map[string]string{"kubecron.io/collector": "true"}, Annotations: ann},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
}

func tokenSecret() *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "kubecron", Name: "kubecron-api"},
		Data: map[string][]byte{"api-token": []byte(testToken)}}
}

// fakeCollector serves the /api/v1 contract from canned answers, and refuses
// any request without the bearer token, as the real one does.
type fakeCollector struct {
	answers map[string]string // path -> JSON
	queries map[string]string // path -> raw query of the last request
	// mu guards queries: the heatmap asks about several CronJobs at once.
	mu sync.Mutex
	// slow holds a path back this long before answering, or until the
	// request is abandoned.
	slow map[string]time.Duration
}

func newFakeCollector(t *testing.T, answers map[string]string) (*httptest.Server, *fakeCollector) {
	fc := &fakeCollector{answers: answers, queries: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fc.mu.Lock()
		fc.queries[r.URL.Path] = r.URL.RawQuery
		wait := fc.slow[r.URL.Path]
		fc.mu.Unlock()
		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-r.Context().Done():
				return
			}
		}
		body, ok := fc.answers[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, fc
}

const discoveryDoc = `{"product":"kubecron","api_versions":["v1"],"read_only":true,
  "clusters":[{"id":"docker-desktop","metrics_enabled":true,"observed_since":"2026-10-01T00:00:00Z"}],
  "retention":{"run_days":90,"log_days":14}}`

func serve(t *testing.T, h http.Handler, path string, values map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.SetPathValue("cluster", "docker-desktop")
	for k, v := range values {
		req.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// testConfig is the configuration naming the chart's collector and its token:
// the Service kubecron/kubecron and the Secret kubecron/kubecron-api.
var testConfig = collectorConfig{Namespace: "kubecron", Service: "kubecron", Secret: "kubecron-api"}

func testExtra() KubeCron {
	k := New()
	k.now = func() time.Time { return testNow }
	k.collector = testConfig
	return k
}

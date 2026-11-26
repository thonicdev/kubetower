package netinspect

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.uber.org/goleak"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/thonicdev/kubetower/extension"
)

// Nothing this package does starts a goroutine; this holds it to that.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fakeHost is extension.Host with List made real: it answers from the
// objects it holds, filtered by namespace and label selector and cut at
// Limit as the console's list is, and records every query.
type fakeHost struct {
	mu       sync.Mutex
	objects  map[string][]unstructured.Unstructured // by resource
	listErr  map[string]error
	narrowed map[string]bool
	queries  []extension.ListQuery
	crds     []extension.CRD
	defsErr  error
	// The console's tunnel to Hubble Relay: where it leads, or why not.
	tunnelTo  string
	tunnelErr error
	tunnels   int
	stopped   int
	// What Host.Reveal gives, by "namespace/name/key", or why not, and the
	// order of what was asked of it and of the tunnel: "reveal:<key>" and
	// "tunnel:<service>".
	secrets   map[string]string
	revealErr error
	events    []string
	// Extension settings by key, as the Settings page stores them.
	settings map[string]string
}

func newHost() *fakeHost {
	return &fakeHost{objects: map[string][]unstructured.Unstructured{}, listErr: map[string]error{}, narrowed: map[string]bool{}}
}

func (h *fakeHost) Client(*http.Request, string) (extension.Clients, error) {
	return extension.Clients{}, errors.New("the Network Inspector must not read through Host.Client: its reads are reviewed for nobody")
}

type registry struct{ crds []extension.CRD }

func (r registry) All(context.Context) ([]extension.CRD, error) { return r.crds, nil }
func (r registry) Lookup(context.Context, string) (extension.CRD, error) {
	return extension.CRD{}, extension.ErrUnknownKind
}

func (h *fakeHost) Definitions(*http.Request, string) (extension.CRDRegistry, error) {
	if h.defsErr != nil {
		return nil, h.defsErr
	}
	return registry{h.crds}, nil
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

func (h *fakeHost) VolumeFill(*http.Request, string, []extension.ClaimRef) (extension.VolumeFill, error) {
	return extension.VolumeFill{State: extension.WindowNoPrometheus}, nil
}
func (h *fakeHost) Exec(*http.Request, string, extension.ExecQuery) (extension.ExecResult, error) {
	return extension.ExecResult{}, errors.New("the Network Inspector runs no command")
}
func (h *fakeHost) RefreshApplication(*http.Request, string, string, string) error {
	return errors.New("RefreshApplication is not used here")
}

func (h *fakeHost) ConfirmToken(*http.Request) string             { return "" }
func (h *fakeHost) Setting(_ *http.Request, _, key string) string { return h.settings[key] }
func (h *fakeHost) MayReadLogs(*http.Request, string, string) error {
	return errors.New("not used")
}
func (h *fakeHost) Reveal(_ *http.Request, _, namespace, name, key string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, "reveal:"+namespace+"/"+name+"/"+key)
	if h.revealErr != nil {
		return "", h.revealErr
	}
	if namespace != "calico-system" || name != "whisker-backend-key-pair" {
		return "", errors.New("the Network Inspector reads only Goldmane's client certificate")
	}
	value, ok := h.secrets[namespace+"/"+name+"/"+key]
	if !ok {
		return "", extension.ErrNoSuchKey
	}
	return value, nil
}
func (h *fakeHost) Forward(context.Context, string, string, string, int) (extension.Tunnel, error) {
	return extension.Tunnel{}, errors.New("the Network Inspector opens no tunnel")
}
func (h *fakeHost) ForwardAsConsole(_ *http.Request, _, namespace, service string, _ int) (extension.Tunnel, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !(namespace == "kube-system" && service == "hubble-relay") && !(namespace == "calico-system" && service == "goldmane") {
		return extension.Tunnel{}, errors.New("the Network Inspector tunnels only to Hubble Relay and Goldmane")
	}
	h.events = append(h.events, "tunnel:"+service)
	if h.tunnelErr != nil {
		return extension.Tunnel{}, h.tunnelErr
	}
	h.tunnels++
	return extension.Tunnel{Addr: h.tunnelTo, Stop: func() {
		h.mu.Lock()
		h.stopped++
		h.mu.Unlock()
	}}, nil
}

// resource is the key the fake keeps a kind under: Calico's kinds share their
// plural with Kubernetes' NetworkPolicy, so they carry their group's prefix.
func resource(g schema.GroupVersionResource) string {
	if g.Group == "crd.projectcalico.org" {
		return "calico." + g.Resource
	}
	return g.Resource
}

func (h *fakeHost) List(_ *http.Request, cluster string, q extension.ListQuery) (extension.Page, error) {
	h.mu.Lock()
	h.queries = append(h.queries, q)
	h.mu.Unlock()
	if name, ok := strings.CutPrefix(q.FieldSelector, "metadata.name="); ok {
		return h.listNamed(q, name)
	}
	if cluster == "gone" {
		return extension.Page{}, extension.ErrUnknownCluster
	}
	if err := h.listErr[resource(q.GVR)]; err != nil {
		return extension.Page{}, err
	}
	var selector labels.Selector
	if q.LabelSelector != "" {
		s, err := labels.Parse(q.LabelSelector)
		if err != nil {
			return extension.Page{}, err
		}
		selector = s
	}
	out := []unstructured.Unstructured{}
	complete := !h.narrowed[resource(q.GVR)]
	for _, o := range h.objects[resource(q.GVR)] {
		if q.Namespace != "" && o.GetNamespace() != q.Namespace {
			continue
		}
		if selector != nil && !selector.Matches(labels.Set(o.GetLabels())) {
			continue
		}
		if q.Limit > 0 && int64(len(out)) == q.Limit {
			complete = false
			break
		}
		out = append(out, o)
	}
	page := extension.Page{Items: out, Complete: complete}
	if !complete {
		page.Note = "the listing stopped before its end"
	}
	return page, nil
}

func (h *fakeHost) add(t testing.TB, resource string, object any) {
	t.Helper()
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		t.Fatal(err)
	}
	h.objects[resource] = append(h.objects[resource], unstructured.Unstructured{Object: raw})
}

func (h *fakeHost) namespace(t testing.TB, name string, set map[string]string) {
	t.Helper()
	all := map[string]string{"kubernetes.io/metadata.name": name}
	for k, v := range set {
		all[k] = v
	}
	h.add(t, "namespaces", &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: all}})
}

// pod is a running pod with an IP and labels, its named ports as "name=80".
func (h *fakeHost) pod(t testing.TB, namespace, name, ip string, set map[string]string, ports ...corev1.ContainerPort) {
	t.Helper()
	h.add(t, "pods", &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: set},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Ports: ports}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: ip},
	})
}

func (h *fakeHost) policy(t testing.TB, np *networkingv1.NetworkPolicy) {
	t.Helper()
	h.add(t, "networkpolicies", np)
}

func np(namespace, name string, selector map[string]string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: selector}},
	}
}

func podPeer(set map[string]string) networkingv1.NetworkPolicyPeer {
	return networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{MatchLabels: set}}
}

func nsPeer(set map[string]string) networkingv1.NetworkPolicyPeer {
	return networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchLabels: set}}
}

func tcp(port int) networkingv1.NetworkPolicyPort {
	p := intstr.FromInt32(int32(port))
	proto := corev1.ProtocolTCP
	return networkingv1.NetworkPolicyPort{Protocol: &proto, Port: &p}
}

func named(name string) networkingv1.NetworkPolicyPort {
	p := intstr.FromString(name)
	return networkingv1.NetworkPolicyPort{Port: &p}
}

func types(t ...networkingv1.PolicyType) []networkingv1.PolicyType { return t }

// serve answers one request against the extension's routes, mounted as the
// console mounts them (under /x/{cluster}/netinspect).
func serve(t testing.TB, h extension.Host, target string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	for _, route := range New().Routes(h) {
		mux.Handle(route.Method+" /x/{cluster}/"+Key+route.Pattern, route.Handler)
	}
	w := httptest.NewRecorder()
	method := http.MethodGet
	if strings.Contains(target, "/flows") {
		method = http.MethodPost // the route that opens a tunnel
	}
	mux.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

func decode[T any](t testing.TB, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("the answer is not JSON (%d): %v\n%s", w.Code, err, w.Body.String())
	}
	return out
}

// verdictOf asks the route.
func verdictOf(t *testing.T, h *fakeHost, from, to, port string) Verdict {
	t.Helper()
	target := "/x/kind/netinspect/verdict?from=" + from + "&to=" + to + "&port=" + port
	w := serve(t, h, target)
	if w.Code != http.StatusOK {
		t.Fatalf("%s answered %d: %s", target, w.Code, strings.TrimSpace(w.Body.String()))
	}
	return decode[Verdict](t, w)
}

// listNamed answers a listing by metadata.name, in a namespace.
func (h *fakeHost) listNamed(q extension.ListQuery, name string) (extension.Page, error) {
	if err := h.listErr[resource(q.GVR)]; err != nil {
		return extension.Page{}, err
	}
	out := []unstructured.Unstructured{}
	for _, o := range h.objects[resource(q.GVR)] {
		if o.GetName() == name && (q.Namespace == "" || o.GetNamespace() == q.Namespace) {
			out = append(out, o)
		}
	}
	return extension.Page{Items: out, Complete: true}, nil
}

// ConfiguredSecret and ConfiguredService are the console's reads of what its
// configuration names, which this extra never makes: it names nothing.
func (h *fakeHost) ConfiguredSecret(*http.Request, string, string, string, string) (string, error) {
	return "", extension.ErrNotConfigured
}

func (h *fakeHost) ConfiguredService(*http.Request, string, string, string) (corev1.Service, error) {
	return corev1.Service{}, extension.ErrNotConfigured
}

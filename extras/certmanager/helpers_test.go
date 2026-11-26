package certmanager

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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
)

// The clock every test stands at.
var testNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

// crds is the catalog a cluster with cert-manager v1 serves.
var crds = map[string]extension.CRD{
	CertificatesKind:        {Group: "cert-manager.io", Version: "v1", Plural: "certificates", Kind: "Certificate", Namespaced: true},
	IssuersKind:             {Group: "cert-manager.io", Version: "v1", Plural: "issuers", Kind: "Issuer", Namespaced: true},
	ClusterIssuersKind:      {Group: "cert-manager.io", Version: "v1", Plural: "clusterissuers", Kind: "ClusterIssuer"},
	CertificateRequestsKind: {Group: "cert-manager.io", Version: "v1", Plural: "certificaterequests", Kind: "CertificateRequest", Namespaced: true},
	OrdersKind:              {Group: "acme.cert-manager.io", Version: "v1", Plural: "orders", Kind: "Order", Namespaced: true},
	ChallengesKind:          {Group: "acme.cert-manager.io", Version: "v1", Plural: "challenges", Kind: "Challenge", Namespaced: true},
}

type fakeRegistry struct {
	served  map[string]extension.CRD
	lookErr map[string]error
}

func (f fakeRegistry) All(context.Context) ([]extension.CRD, error) {
	var out []extension.CRD
	for _, c := range f.served {
		out = append(out, c)
	}
	return out, nil
}

func (f fakeRegistry) Lookup(_ context.Context, kind string) (extension.CRD, error) {
	if err := f.lookErr[kind]; err != nil {
		return extension.CRD{}, err
	}
	c, ok := f.served[kind]
	if !ok {
		return extension.CRD{}, extension.ErrUnknownKind
	}
	return c, nil
}

// fakeHost answers List from the objects it holds, by resource, filtered by
// namespace, as the console's list does; a Secret list is refused as the
// console refuses it.
type fakeHost struct {
	mu       sync.Mutex
	registry fakeRegistry
	defErr   error
	objects  map[string][]unstructured.Unstructured // by resource plural
	listErr  map[string]error                       // by resource plural
	listed   []extension.ListQuery
}

func newHost() *fakeHost {
	return &fakeHost{registry: fakeRegistry{served: crds}, objects: map[string][]unstructured.Unstructured{}, listErr: map[string]error{}}
}

func (h *fakeHost) add(resource string, objs ...unstructured.Unstructured) {
	h.objects[resource] = append(h.objects[resource], objs...)
}

func (h *fakeHost) Client(*http.Request, string) (extension.Clients, error) {
	return extension.Clients{}, errors.New("this extra reads through List only")
}

func (h *fakeHost) Definitions(*http.Request, string) (extension.CRDRegistry, error) {
	if h.defErr != nil {
		return nil, h.defErr
	}
	return h.registry, nil
}

func (h *fakeHost) List(_ *http.Request, _ string, q extension.ListQuery) (extension.Page, error) {
	h.mu.Lock()
	h.listed = append(h.listed, q)
	h.mu.Unlock()
	if q.GVR.Group == "" && q.GVR.Resource == "secrets" {
		return extension.Page{}, errors.New("secrets are not listed")
	}
	if err := h.listErr[q.GVR.Resource]; err != nil {
		return extension.Page{}, err
	}
	var out []unstructured.Unstructured
	for _, o := range h.objects[q.GVR.Resource] {
		if q.Namespace != "" && o.GetNamespace() != q.Namespace {
			continue
		}
		if name, ok := strings.CutPrefix(q.FieldSelector, "metadata.name="); ok && o.GetName() != name {
			continue
		}
		out = append(out, o)
	}
	if q.Limit > 0 && int64(len(out)) > q.Limit {
		return extension.Page{Items: out[:q.Limit], Note: "cut at the limit"}, nil
	}
	return extension.Page{Items: out, Complete: true}, nil
}

func (h *fakeHost) Reveal(*http.Request, string, string, string, string) (string, error) {
	return "", errors.New("this extra reveals nothing")
}
func (h *fakeHost) Exec(*http.Request, string, extension.ExecQuery) (extension.ExecResult, error) {
	return extension.ExecResult{}, errors.New("this extra runs nothing")
}
func (h *fakeHost) MayReadLogs(*http.Request, string, string) error { return errors.New("not used") }
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
func (h *fakeHost) ForwardAsConsole(*http.Request, string, string, string, int) (extension.Tunnel, error) {
	return extension.Tunnel{}, errors.New("this extra opens no tunnel")
}
func (h *fakeHost) Forward(context.Context, string, string, string, int) (extension.Tunnel, error) {
	return extension.Tunnel{}, errors.New("this extra opens no tunnel")
}
func (h *fakeHost) Setting(*http.Request, string, string) string { return "" }
func (h *fakeHost) RefreshApplication(*http.Request, string, string, string) error {
	return errors.New("RefreshApplication is not used here")
}

func (h *fakeHost) ConfirmToken(*http.Request) string { return "" }

func (h *fakeHost) listedResources() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, q := range h.listed {
		out = append(out, q.GVR.Resource)
	}
	return out
}

func obj(t testing.TB, doc map[string]any) unstructured.Unstructured {
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

func cond(kind, status, reason, message string) map[string]any {
	return map[string]any{"type": kind, "status": status, "reason": reason, "message": message}
}

// certificate is a Certificate expiring at notAfter (zero: never issued),
// Ready as given.
func certificate(t testing.TB, ns, name string, notAfter time.Time, ready string) unstructured.Unstructured {
	status := map[string]any{"conditions": []any{cond("Ready", ready, "Ready", "Certificate is up to date")}}
	if !notAfter.IsZero() {
		status["notAfter"] = notAfter.Format(time.RFC3339)
		status["renewalTime"] = notAfter.Add(-30 * day).Format(time.RFC3339)
	}
	return obj(t, map[string]any{
		"apiVersion": "cert-manager.io/v1", "kind": "Certificate",
		"metadata": map[string]any{"namespace": ns, "name": name},
		"spec": map[string]any{"secretName": name + "-tls", "dnsNames": []any{name + ".example.com"},
			"issuerRef": map[string]any{"name": "letsencrypt", "kind": "ClusterIssuer"}},
		"status": status,
	})
}

func issuer(t testing.TB, kind, ns, name, ready, message string) unstructured.Unstructured {
	meta := map[string]any{"name": name}
	if ns != "" {
		meta["namespace"] = ns
	}
	return obj(t, map[string]any{
		"apiVersion": "cert-manager.io/v1", "kind": kind, "metadata": meta,
		"spec":   map[string]any{"acme": map[string]any{"server": "https://acme.example/directory"}},
		"status": map[string]any{"conditions": []any{cond("Ready", ready, "R", message)}},
	})
}

func challenge(t testing.TB, ns, name, state, reason string) unstructured.Unstructured {
	return obj(t, map[string]any{
		"apiVersion": "acme.cert-manager.io/v1", "kind": "Challenge",
		"metadata": map[string]any{"namespace": ns, "name": name},
		"spec":     map[string]any{"type": "HTTP-01", "dnsName": name + ".example.com"},
		"status":   map[string]any{"state": state, "reason": reason, "presented": true},
	})
}

func order(t testing.TB, ns, name, state, reason string) unstructured.Unstructured {
	return obj(t, map[string]any{
		"apiVersion": "acme.cert-manager.io/v1", "kind": "Order",
		"metadata": map[string]any{"namespace": ns, "name": name},
		"spec":     map[string]any{"dnsNames": []any{name + ".example.com"}},
		"status":   map[string]any{"state": state, "reason": reason},
	})
}

// serve sends target through every route of the extra, mounted as the console
// mounts them.
func serve(t testing.TB, h extension.Host, target string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	for _, route := range New().Routes(h) {
		mux.Handle(route.Method+" /x/{cluster}/"+Key+route.Pattern, route.Handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func decode[T any](t testing.TB, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.NewDecoder(strings.NewReader(rec.Body.String())).Decode(&out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

// at stands the package clock at testNow for one test.
func at(t testing.TB) {
	t.Helper()
	was := now
	now = func() time.Time { return testNow }
	t.Cleanup(func() { now = was })
}

// ConfiguredSecret and ConfiguredService are the console's reads of what its
// configuration names, which this extra never makes: it names nothing.
func (h *fakeHost) ConfiguredSecret(*http.Request, string, string, string, string) (string, error) {
	return "", extension.ErrNotConfigured
}

func (h *fakeHost) ConfiguredService(*http.Request, string, string, string) (corev1.Service, error) {
	return corev1.Service{}, extension.ErrNotConfigured
}

func ingress(t testing.TB, ns, name string, secrets ...string) unstructured.Unstructured {
	var tls []any
	for _, s := range secrets {
		tls = append(tls, map[string]any{"hosts": []any{name + ".example.com"}, "secretName": s})
	}
	return obj(t, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "Ingress",
		"metadata": map[string]any{"namespace": ns, "name": name},
		"spec":     map[string]any{"tls": tls},
	})
}

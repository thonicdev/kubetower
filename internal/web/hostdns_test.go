package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// Host.DNSWindow, reviewed for the person. The cluster holds CoreDNS in
// kube-system (two pods, found by the k8s-app label) and a lookalike in shop
// that has no label. The RBAC: carol may list pods everywhere, alice only in
// shop, bob nowhere, and dave's review cannot be made at all.

// dnsAPI is an API server that answers a pod list the way a label selector
// would, and records what it was asked.
type dnsAPI struct {
	mu    sync.Mutex
	pods  []map[string]any
	paths []string
}

func dnsPod(ns, name, ip string, labels map[string]string) map[string]any {
	return map[string]any{"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": name, "namespace": ns, "labels": labels, "resourceVersion": "5"},
		"spec":     map[string]any{"nodeName": "n-" + name},
		"status":   map[string]any{"phase": "Running", "podIP": ip}}
}

func defaultDNSPods() []map[string]any {
	kube := map[string]string{"k8s-app": "kube-dns"}
	return []map[string]any{
		dnsPod("kube-system", "coredns-b", "10.244.0.8", kube),
		dnsPod("kube-system", "coredns-a", "10.244.0.7", kube),
		dnsPod("shop", "api-1", "10.244.1.1", nil),
	}
}

func (a *dnsAPI) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.paths = append(a.paths, r.URL.Path+"?"+r.URL.RawQuery)
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api":
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "APIVersions", "versions": []string{"v1"}})
		case "/api/v1/pods":
			selector := r.URL.Query().Get("labelSelector")
			items := []any{}
			for _, p := range a.pods {
				labels, _ := p["metadata"].(map[string]any)["labels"].(map[string]string)
				if k, v, ok := strings.Cut(selector, "="); ok && labels[k] == v {
					items = append(items, p)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "PodList",
				"metadata": map[string]any{"resourceVersion": "9"}, "items": items})
		default:
			http.Error(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// dnsSeries is a Prometheus that counts what it is asked, and knows two pods.
type dnsSeries struct {
	mu    sync.Mutex
	asked []string
	err   error
}

func (s *dnsSeries) answer(promQL string, matrix bool) ([]byte, error) {
	s.mu.Lock()
	s.asked = append(s.asked, promQL)
	s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	rows := ""
	switch {
	case strings.Contains(promQL, "coredns_build_info"):
		rows = `{"metric":{"instance":"10.244.0.7:9153","version":"1.12.1"},"value":[1,"1"]},` +
			`{"metric":{"instance":"10.244.0.8:9153","version":"1.12.1"},"value":[1,"1"]}`
	case matrix && strings.Contains(promQL, "sum (rate(coredns_dns_requests_total"):
		rows = `{"metric":{},"values":[[1788200100,"120"],[1788200115,"125"]]}`
	case strings.HasPrefix(promQL, "sum by (instance) (rate(coredns_dns_requests_total"):
		rows = `{"metric":{"instance":"10.244.0.7:9153"},"value":[1,"60"]},{"metric":{"instance":"10.244.0.8:9153"},"value":[1,"65"]}`
	}
	kind := "vector"
	if matrix {
		kind = "matrix"
	}
	return fmt.Appendf(nil, `{"status":"success","data":{"resultType":%q,"result":[%s]}}`, kind, rows), nil
}

func (s *dnsSeries) Query(_ context.Context, _, q string) ([]byte, error) { return s.answer(q, false) }
func (s *dnsSeries) QueryRange(_ context.Context, _, q string, _, _ time.Time, _ time.Duration) ([]byte, error) {
	return s.answer(q, true)
}
func (s *dnsSeries) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asked...)
}

func dnsRBAC(q accesstest.Question) (bool, error) {
	if q.Self {
		return true, nil
	}
	a := q.Attributes
	for _, g := range q.Groups {
		switch g {
		case "oidc:platform":
			return a.Verb == "list" && a.Resource == "pods", nil
		case "oidc:shop-viewers":
			return a.Verb == "list" && a.Resource == "pods" && a.Namespace == "shop", nil
		case "oidc:unreviewable":
			return false, errors.New("the review could not be made")
		}
	}
	return false, nil
}

func dnsHost(t *testing.T, pods []map[string]any, series Series) (extension.Host, *dnsAPI) {
	t.Helper()
	api := &dnsAPI{pods: pods}
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte(`apiVersion: v1
kind: Config
current-context: prod
clusters:
- name: prod
  cluster: {server: `+api.start(t)+`}
contexts:
- name: prod
  context: {cluster: prod, user: sa}
users:
- name: sa
  user: {token: sa-token}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	reviews := &accesstest.Reviews{Answer: dnsRBAC}
	s, err := New(Options{Clusters: manager, InCluster: true, Series: series,
		Access: access.New(access.Options{Reviews: reviews.For, InCluster: true})})
	if err != nil {
		t.Fatal(err)
	}
	return s.Host(), api
}

func dnsAs(ctx context.Context) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/x/prod/dns/", nil).WithContext(ctx)
}

func TestDNSWindowReadsForAPersonWhoMayListThePods(t *testing.T) {
	series := &dnsSeries{}
	h, _ := dnsHost(t, defaultDNSPods(), series)
	got, err := h.DNSWindow(dnsAs(person("carol@example.com", "oidc:platform")), "prod", extension.DNSSpan15m)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != extension.WindowRead || got.Namespace != "kube-system" || got.Version != "1.12.1" {
		t.Fatalf("answer = %+v", got)
	}
	if len(got.Pods) != 2 || got.Pods[0].Pod != "coredns-a" || got.Pods[0].Node != "n-coredns-a" ||
		got.Pods[0].Figures.QPS != 60 || got.Pods[1].IP != "10.244.0.8" {
		t.Errorf("pods = %+v", got.Pods)
	}
	if got.Now.QPS != 125 || len(got.Series.QPS) != 2 || got.Series.QPS[1] != (extension.Sample{T: 1788200115, V: 125}) {
		t.Errorf("figures = %+v, series = %+v", got.Now, got.Series.QPS)
	}
	for _, q := range series.calls() {
		if strings.Contains(q, "api-1") || strings.Contains(q, `10\\.244\\.1\\.1`) {
			t.Errorf("a pod that is not CoreDNS reached a query: %s", q)
		}
	}
}

// The acceptance criterion: refused means Prometheus is never asked.
func TestDNSWindowRefusedReadsNothingFromPrometheus(t *testing.T) {
	for _, tc := range []struct {
		who  string
		ctx  context.Context
		want error
	}{
		{"nobody", person("bob@example.com", "oidc:nobody"), extension.ErrDenied},
		// Alice may list pods in shop only: the namespace CoreDNS runs in is not hers, and
		// all or nothing means no narrowing to what she may.
		{"a shop viewer", person("alice@example.com", "oidc:shop-viewers"), extension.ErrDenied},
		{"a review that cannot be made", person("dave@example.com", "oidc:unreviewable"), extension.ErrUnreviewable},
	} {
		t.Run(tc.who, func(t *testing.T) {
			series := &dnsSeries{}
			h, _ := dnsHost(t, defaultDNSPods(), series)
			got, err := h.DNSWindow(dnsAs(tc.ctx), "prod", extension.DNSSpan15m)
			if !errors.Is(err, tc.want) {
				t.Fatalf("answered %+v, %v; want %v", got, err, tc.want)
			}
			if len(got.Pods) != 0 || got.State != "" || got.Namespace != "" {
				t.Errorf("a refusal carried data: %+v", got)
			}
			if calls := series.calls(); len(calls) != 0 {
				t.Errorf("Prometheus was asked %d queries for a person who may not list the pods: %v", len(calls), calls)
			}
		})
	}
}

func TestDNSWindowStates(t *testing.T) {
	carol := person("carol@example.com", "oidc:platform")

	// No Prometheus at all: a genuine nil, as launch passes one.
	h, _ := dnsHost(t, defaultDNSPods(), nil)
	if got, err := h.DNSWindow(dnsAs(carol), "prod", extension.DNSSpan1h); err != nil || got.State != extension.WindowNoPrometheus || got.Detail == "" {
		t.Errorf("no Prometheus: %+v, %v", got, err)
	}
	// One that cannot be asked.
	h, _ = dnsHost(t, defaultDNSPods(), &dnsSeries{err: errors.New("connection refused")})
	if got, err := h.DNSWindow(dnsAs(carol), "prod", extension.DNSSpan1h); err != nil || got.State != extension.WindowUnreadable || !strings.Contains(got.Detail, "refused") {
		t.Errorf("unreadable: %+v, %v", got, err)
	}
	// No CoreDNS pod in the cluster: empty, and Prometheus is not asked.
	series := &dnsSeries{}
	h, _ = dnsHost(t, []map[string]any{dnsPod("shop", "api-1", "10.244.1.1", nil)}, series)
	if got, err := h.DNSWindow(dnsAs(carol), "prod", extension.DNSSpan6h); err != nil || got.State != extension.WindowEmpty || got.Detail == "" {
		t.Errorf("no CoreDNS: %+v, %v", got, err)
	}
	if len(series.calls()) != 0 {
		t.Error("Prometheus was asked where there is no CoreDNS")
	}
}

func TestDNSWindowFindsCoreDNSByTheHelmChartLabelToo(t *testing.T) {
	pods := []map[string]any{dnsPod("dns-system", "coredns-0", "10.244.0.7", map[string]string{"app.kubernetes.io/name": "coredns"})}
	h, _ := dnsHost(t, pods, &dnsSeries{})
	got, err := h.DNSWindow(dnsAs(person("carol@example.com", "oidc:platform")), "prod", extension.DNSSpan15m)
	if err != nil || got.Namespace != "dns-system" || got.State != extension.WindowRead {
		t.Errorf("answer = %+v, %v", got, err)
	}
}

func TestDNSWindowRefusesWhatIsNotASpanAndWrapsAnUnknownCluster(t *testing.T) {
	series := &dnsSeries{}
	h, _ := dnsHost(t, defaultDNSPods(), series)
	carol := person("carol@example.com", "oidc:platform")
	if _, err := h.DNSWindow(dnsAs(carol), "prod", extension.DNSSpan(`15m"} or up{`)); err == nil {
		t.Error("a span that is not one was accepted")
	}
	if _, err := h.DNSWindow(dnsAs(carol), "nope", extension.DNSSpan15m); !errors.Is(err, extension.ErrUnknownCluster) {
		t.Errorf("an unknown cluster: %v", err)
	}
	if len(series.calls()) != 0 {
		t.Error("a refused input reached Prometheus")
	}
}

package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/metrics"
)

// Host.ContainerUsage and Host.UsageWindow, reviewed for the person.
// The cluster is readsServer's - pods in shop,
// billing and kube-system - and its RoleBindings: alice may list pods in shop,
// bob nowhere, carol everywhere. Neither metrics backend knows who is asking,
// so these tests are what holds the per-person half.

// fakeContainers is metrics-server's per-container answer for that cluster: one
// container per pod, plus a measured zero.
type fakeContainers struct {
	mu    sync.Mutex
	asked []string
	err   error
}

func (f *fakeContainers) PodUsage(context.Context, string, string) (map[string]metrics.Usage, error) {
	return nil, errors.New("not used")
}

func (f *fakeContainers) NodeUsage(context.Context, string) (map[string]metrics.Usage, error) {
	return nil, errors.New("not used")
}

func (f *fakeContainers) ContainerUsage(_ context.Context, cluster, namespace string) (map[string]metrics.Usage, error) {
	f.mu.Lock()
	f.asked = append(f.asked, cluster+":"+namespace)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]metrics.Usage{}
	for ns, pods := range readPods {
		if namespace != "" && ns != namespace {
			continue
		}
		for _, pod := range pods {
			out[metrics.ContainerKey(ns, pod, "app")] = metrics.Usage{CPUMillicores: 120, MemoryBytes: 64 << 20}
		}
	}
	if namespace == "" || namespace == "shop" {
		// Measured, and idle: a zero that must arrive as a zero.
		out[metrics.ContainerKey("shop", "api-1", "sidecar")] = metrics.Usage{}
	}
	return out, nil
}

func (f *fakeContainers) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// fakeSeries is the cluster's Prometheus: the listing, then one series per pod's
// container for CPU and for memory.
type fakeSeries struct {
	mu    sync.Mutex
	asked []string
	count int
	// failCPU and failMemory fail that half's range query, as the HTTP client
	// does: a *url.Error carrying the query's address.
	failCPU, failMemory error
}

func (f *fakeSeries) Query(_ context.Context, _, promQL string) ([]byte, error) {
	f.mu.Lock()
	f.asked = append(f.asked, promQL)
	f.mu.Unlock()
	// The container listing: readPods' containers in the namespace asked for,
	// none when count is zero.
	var rows []string
	for ns, pods := range readPods {
		if f.count == 0 || (strings.Contains(promQL, "namespace=") && !strings.Contains(promQL, fmt.Sprintf("namespace=%q", ns))) {
			continue
		}
		for _, pod := range pods {
			rows = append(rows, fmt.Sprintf(`{"metric":{"namespace":%q,"pod":%q,"container":"app"},"value":[1,"1"]}`, ns, pod))
		}
	}
	return fmt.Appendf(nil, `{"status":"success","data":{"resultType":"vector","result":[%s]}}`, strings.Join(rows, ",")), nil
}

func (f *fakeSeries) QueryRange(_ context.Context, _, promQL string, _, _ time.Time, _ time.Duration) ([]byte, error) {
	f.mu.Lock()
	f.asked = append(f.asked, promQL)
	f.mu.Unlock()
	if fail := f.failMemory; strings.Contains(promQL, "memory") && fail != nil {
		return nil, addressed(promQL, fail)
	}
	if fail := f.failCPU; strings.Contains(promQL, "cpu") && fail != nil {
		return nil, addressed(promQL, fail)
	}
	// Every series, whatever the selector: the core keeps only its batch's.
	var rows []string
	for ns, pods := range readPods {
		for _, pod := range pods {
			rows = append(rows, fmt.Sprintf(`{"metric":{"namespace":%q,"pod":%q,"container":"app"},"values":[[1788200100,"0.1"],[1788201000,"0.2"]]}`, ns, pod))
		}
	}
	return fmt.Appendf(nil, `{"status":"success","data":{"resultType":"matrix","result":[%s]}}`, strings.Join(rows, ",")), nil
}

func (f *fakeSeries) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// usageHost is readsServer's cluster and RBAC with the two fakes behind it. A
// nil usage or series is passed as a genuine nil, as launch does when there is
// no metrics layer.
func usageHost(t *testing.T, usage Usage, series Series) extension.Host {
	t.Helper()
	fake := &readServer{}
	url := fake.start(t)
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte(`apiVersion: v1
kind: Config
current-context: prod
clusters:
- name: prod
  cluster: {server: `+url+`}
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
	reviews := &accesstest.Reviews{Answer: roleBindings}
	s, err := New(Options{Clusters: manager, InCluster: true, Metrics: usage, Series: series,
		Access: access.New(access.Options{Reviews: reviews.For, InCluster: true})})
	if err != nil {
		t.Fatal(err)
	}
	return s.Host()
}

func asPerson(ctx context.Context) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/x/prod/kubeadjust/", nil).WithContext(ctx)
}

func keysSorted[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var (
	shopViewer = func() context.Context { return person("alice@example.com", "oidc:shop-viewers") }
	nobody     = func() context.Context { return person("bob@example.com", "oidc:nobody") }
	platform   = func() context.Context { return person("carol@example.com", "oidc:platform") }
)

// --- ContainerUsage ---------------------------------------------------------------

// Every namespace asked: each person gets the containers of the namespaces
// their bindings let them list pods in, and is told when that is not all.
func TestContainerUsageShowsEachPersonTheNamespacesTheyMayList(t *testing.T) {
	h := usageHost(t, &fakeContainers{}, nil)
	for _, tc := range []struct {
		who      string
		ctx      context.Context
		keys     []string
		complete bool
	}{
		{"alice", shopViewer(), []string{"shop/api-1/app", "shop/api-1/sidecar", "shop/api-2/app"}, false},
		{"bob", nobody(), []string{}, false},
		{"carol", platform(), []string{"billing/ledger-0/app", "kube-system/coredns-1/app",
			"shop/api-1/app", "shop/api-1/sidecar", "shop/api-2/app"}, true},
	} {
		t.Run(tc.who, func(t *testing.T) {
			got, err := h.ContainerUsage(asPerson(tc.ctx), "prod", "")
			if err != nil {
				t.Fatalf("ContainerUsage: %v", err)
			}
			if keys := keysSorted(got.Containers); strings.Join(keys, ",") != strings.Join(tc.keys, ",") {
				t.Errorf("containers = %v, want %v", keys, tc.keys)
			}
			if got.Complete != tc.complete || (got.Note != "") == tc.complete {
				t.Errorf("complete = %v, note %q; want complete %v", got.Complete, got.Note, tc.complete)
			}
			if strings.Contains(got.Note, "billing") || strings.Contains(got.Note, "kube-system") {
				t.Errorf("the note names a namespace the person may not list: %q", got.Note)
			}
		})
	}
}

// The acceptance criterion's other half: a namespace the person may not list
// is refused, and metrics-server is never asked about it.
func TestContainerUsageOfANamespaceThePersonMayNotListIsRefusedAndUnread(t *testing.T) {
	usage := &fakeContainers{}
	h := usageHost(t, usage, nil)
	got, err := h.ContainerUsage(asPerson(shopViewer()), "prod", "billing")
	var refused *access.Refused
	if !errors.As(err, &refused) {
		t.Fatalf("ContainerUsage(billing) for alice = %+v, %v; want a refusal", got, err)
	}
	if len(got.Containers) != 0 {
		t.Errorf("a refusal carried containers: %v", keysSorted(got.Containers))
	}
	if calls := usage.calls(); len(calls) != 0 {
		t.Errorf("metrics-server was read for a person who may not list the namespace: %v", calls)
	}
	// And the namespace she may list is answered whole.
	shop, err := h.ContainerUsage(asPerson(shopViewer()), "prod", "shop")
	if err != nil || !shop.Complete || len(shop.Containers) != 3 {
		t.Errorf("ContainerUsage(shop) for alice = %+v, %v; want 3 containers, complete", shop, err)
	}
}

// A measured zero is a measurement: the key is there and the figures are zero.
func TestContainerUsageKeepsAMeasuredZeroAsAZero(t *testing.T) {
	h := usageHost(t, &fakeContainers{}, nil)
	got, err := h.ContainerUsage(asPerson(platform()), "prod", "shop")
	if err != nil {
		t.Fatal(err)
	}
	zero, ok := got.Containers[extension.ContainerKey("shop", "api-1", "sidecar")]
	if !ok {
		t.Fatalf("the idle sidecar is missing: %v", keysSorted(got.Containers))
	}
	if zero != (extension.Resources{}) {
		t.Errorf("the idle sidecar reads %+v, want zero", zero)
	}
	if app := got.Containers[extension.ContainerKey("shop", "api-1", "app")]; app.CPUMillicores != 120 || app.MemoryBytes != 64<<20 {
		t.Errorf("api-1/app reads %+v", app)
	}
}

// No metrics-server, a refused read and an unreachable cluster are three typed
// answers - never an empty map, which would read as nothing running.
func TestContainerUsageWithNothingToAskIsATypedAbsence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage Usage
		want  extension.MetricsReason
	}{
		{"no metrics-server", &fakeContainers{err: &metrics.Unavailable{Cluster: "prod", Kind: metrics.KindNone, Detail: "metrics.k8s.io is not there"}}, extension.MetricsAbsent},
		{"refused", &fakeContainers{err: &metrics.Unavailable{Cluster: "prod", Kind: metrics.KindUnauthorised, Detail: "403"}}, extension.MetricsRefused},
		{"unreachable", &fakeContainers{err: &metrics.Unavailable{Cluster: "prod", Kind: metrics.KindUnreachable, Detail: "dial tcp"}}, extension.MetricsUnreachable},
		{"no metrics layer at all", nil, extension.MetricsAbsent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := usageHost(t, tc.usage, nil).ContainerUsage(asPerson(platform()), "prod", "")
			var gone *extension.MetricsUnavailable
			if !errors.As(err, &gone) {
				t.Fatalf("answered %+v, %v; want a *MetricsUnavailable", got, err)
			}
			if gone.Reason != tc.want || gone.Detail == "" {
				t.Errorf("reason %q detail %q, want %q with a detail", gone.Reason, gone.Detail, tc.want)
			}
			if got.Containers != nil {
				t.Errorf("an absence carried a map: %v", got.Containers)
			}
		})
	}
}

// --- UsageWindow ------------------------------------------------------------------

func TestUsageWindowShowsEachPersonTheSeriesOfTheNamespacesTheyMayList(t *testing.T) {
	h := usageHost(t, nil, &fakeSeries{count: 4})
	for _, tc := range []struct {
		who      string
		ctx      context.Context
		state    extension.WindowState
		keys     []string
		complete bool
	}{
		{"alice", shopViewer(), extension.WindowRead, []string{"shop/api-1/app", "shop/api-2/app"}, false},
		{"bob", nobody(), extension.WindowEmpty, []string{}, false},
		{"carol", platform(), extension.WindowRead, []string{"billing/ledger-0/app", "kube-system/coredns-1/app",
			"shop/api-1/app", "shop/api-2/app"}, true},
	} {
		t.Run(tc.who, func(t *testing.T) {
			got, err := h.UsageWindow(asPerson(tc.ctx), "prod", "")
			if err != nil {
				t.Fatalf("UsageWindow: %v", err)
			}
			if got.State != tc.state {
				t.Errorf("state = %s (%q), want %s", got.State, got.Detail, tc.state)
			}
			if keys := keysSorted(got.Containers); strings.Join(keys, ",") != strings.Join(tc.keys, ",") {
				t.Errorf("containers = %v, want %v", keys, tc.keys)
			}
			if got.Complete != tc.complete || (got.Note != "") == tc.complete {
				t.Errorf("complete = %v, note %q; want complete %v", got.Complete, got.Note, tc.complete)
			}
		})
	}
	// Cores in, millicores out, through the contract.
	got, _ := h.UsageWindow(asPerson(platform()), "prod", "shop")
	cpu := got.Containers[extension.ContainerKey("shop", "api-1", "app")].CPU
	if len(cpu) != 2 || cpu[0].V != 100 || cpu[1].V != 200 {
		t.Errorf("CPU window = %+v, want 100m then 200m", cpu)
	}
}

func TestUsageWindowOfANamespaceThePersonMayNotListIsRefusedAndUnasked(t *testing.T) {
	series := &fakeSeries{count: 1}
	h := usageHost(t, nil, series)
	_, err := h.UsageWindow(asPerson(shopViewer()), "prod", "billing")
	var refused *access.Refused
	if !errors.As(err, &refused) {
		t.Fatalf("UsageWindow(billing) for alice = %v; want a refusal", err)
	}
	if calls := series.calls(); len(calls) != 0 {
		t.Errorf("Prometheus was asked for a person who may not list the namespace: %v", calls)
	}
}

// A narrowed person's containers are narrowed before any range query is sent:
// the series of a namespace they may not list are never asked for, and there
// is no size past which the window is declined.
func TestUsageWindowNeverAsksForANamespaceANarrowedPersonMayNotList(t *testing.T) {
	series := &fakeSeries{count: 4}
	got, err := usageHost(t, nil, series).UsageWindow(asPerson(shopViewer()), "prod", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != extension.WindowRead || len(got.Containers) != 2 {
		t.Fatalf("alice: %s %q, %d containers", got.State, got.Detail, len(got.Containers))
	}
	for _, q := range series.calls() {
		if strings.Contains(q, "pod=~") && (strings.Contains(q, "billing") || strings.Contains(q, "kube-system") ||
			strings.Contains(q, "ledger") || strings.Contains(q, "coredns")) {
			t.Errorf("a range query for alice names what she may not list: %s", q)
		}
	}
}

// What one read costs Prometheus, which KubeAdjust's node tab counts on:
// three queries for a scope of one batch - the listing, then CPU and memory -
// and one namespace's read carries that namespace's series only.
func TestUsageWindowCostsThreeQueriesAndOneNamespacesSeries(t *testing.T) {
	for _, tc := range []struct {
		namespace string
		keys      int
	}{{"", 4}, {"shop", 2}, {"billing", 1}} {
		series := &fakeSeries{count: 4}
		got, err := usageHost(t, nil, series).UsageWindow(asPerson(platform()), "prod", tc.namespace)
		if err != nil {
			t.Fatal(err)
		}
		if calls := series.calls(); len(calls) != 3 {
			t.Errorf("namespace %q: %d queries, want 3: %v", tc.namespace, len(calls), calls)
		}
		if len(got.Containers) != tc.keys {
			t.Errorf("namespace %q: %d containers, want %d: %v", tc.namespace, len(got.Containers), tc.keys, keysSorted(got.Containers))
		}
		for k := range got.Containers {
			if tc.namespace != "" && !strings.HasPrefix(k, tc.namespace+"/") {
				t.Errorf("namespace %q: handed %s", tc.namespace, k)
			}
		}
	}
}

func TestUsageWindowWithNoPrometheusSaysSo(t *testing.T) {
	got, err := usageHost(t, nil, nil).UsageWindow(asPerson(platform()), "prod", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != extension.WindowNoPrometheus || got.Detail == "" || got.Containers != nil {
		t.Errorf("answered %+v; want no-prometheus with a sentence and no map", got)
	}
}

func TestBothMethodsRefuseANamespaceThatCannotBeOne(t *testing.T) {
	series, usage := &fakeSeries{count: 1}, &fakeContainers{}
	h := usageHost(t, usage, series)
	if _, err := h.UsageWindow(asPerson(platform()), "prod", `shop"} or up{`); err == nil {
		t.Error("UsageWindow accepted a namespace that cannot be one")
	}
	if _, err := h.ContainerUsage(asPerson(platform()), "prod", "Not A Namespace"); err == nil {
		t.Error("ContainerUsage accepted a namespace that cannot be one")
	}
	if len(series.calls()) != 0 || len(usage.calls()) != 0 {
		t.Errorf("a bad namespace reached a backend: %v %v", series.calls(), usage.calls())
	}
}

// --- shared -----------------------------------------------------------------------

func TestBothMethodsWrapErrUnknownCluster(t *testing.T) {
	h := unreachableHost(t)
	r := httptest.NewRequest(http.MethodGet, "/x/nope/kubeadjust/", nil)
	if _, err := h.ContainerUsage(r, "nope", ""); !errors.Is(err, extension.ErrUnknownCluster) {
		t.Errorf("ContainerUsage(nope) = %v; want ErrUnknownCluster", err)
	}
	if _, err := h.UsageWindow(r, "nope", ""); !errors.Is(err, extension.ErrUnknownCluster) {
		t.Errorf("UsageWindow(nope) = %v; want ErrUnknownCluster", err)
	}
	// Known and with no metrics layer: an answer about the metrics, never "no
	// such cluster".
	if _, err := h.ContainerUsage(r, "down", ""); errors.Is(err, extension.ErrUnknownCluster) {
		t.Errorf("ContainerUsage(down) = %v; a known cluster answered as unknown", err)
	}
}

// The contract publishes the window's numbers and states; the core reads with
// its own. One edit to either must fail here, not on a page naming a window it
// was not given.
func TestTheContractsWindowIsTheOneTheCoreReads(t *testing.T) {
	if extension.WindowSpan != metrics.WindowSpan || extension.WindowStep != metrics.WindowStep ||
		extension.WindowBatch != metrics.WindowBatch {
		t.Errorf("contract %s/%s/%d, core %s/%s/%d", extension.WindowSpan, extension.WindowStep,
			extension.WindowBatch, metrics.WindowSpan, metrics.WindowStep, metrics.WindowBatch)
	}
	for core, public := range map[metrics.WindowState]extension.WindowState{
		metrics.WindowNoPrometheus: extension.WindowNoPrometheus,
		metrics.WindowUnreadable:   extension.WindowUnreadable,
		metrics.WindowEmpty:        extension.WindowEmpty,
		metrics.WindowRead:         extension.WindowRead,
	} {
		if string(core) != string(public) {
			t.Errorf("core state %q is published as %q", core, public)
		}
	}
	if got, want := extension.ContainerKey("a", "b", "c"), metrics.ContainerKey("a", "b", "c"); got != want {
		t.Errorf("ContainerKey: contract %q, core %q", got, want)
	}
}

// addressed is err as net/http returns it: the range query's whole address in
// front, the PromQL and so the namespace inside it.
func addressed(promQL string, err error) error {
	return &url.Error{Op: "Get", URL: "http://127.0.0.1:41234/api/v1/query_range?" +
		url.Values{"query": {promQL}, "step": {"120"}}.Encode(), Err: err}
}

// One half failing is said by its reason, without the query's address: the
// same timeout reads the same for every namespace, so the node tab can say it
// once, and the reason is never cut off by a URL.
func TestUsageWindowSaysAFailedHalfByItsReasonNotItsAddress(t *testing.T) {
	for _, half := range []string{"cpu", "memory"} {
		reasons := map[string]bool{}
		for _, ns := range []string{"shop", "billing"} {
			series := &fakeSeries{count: 4}
			if half == "cpu" {
				series.failCPU = context.DeadlineExceeded
			} else {
				series.failMemory = context.DeadlineExceeded
			}
			got, err := usageHost(t, nil, series).UsageWindow(asPerson(platform()), "prod", ns)
			if err != nil {
				t.Fatal(err)
			}
			failed, other := got.CPUFailed, got.MemoryFailed
			if half == "memory" {
				failed, other = other, failed
			}
			if failed != "context deadline exceeded" || other != "" {
				t.Errorf("%s half failing in %s: failed %q, other %q; want the bare reason and nothing",
					half, ns, failed, other)
			}
			reasons[failed] = true
		}
		if len(reasons) != 1 {
			t.Errorf("%s half: the same timeout reads differently per namespace: %v", half, reasons)
		}
	}
}

// The unwrap holds on its own: an address short enough for ReasonN to keep
// would still carry the namespace, so halfReason drops it before shortening.
func TestHalfReasonDropsAShortAddressToo(t *testing.T) {
	for _, ns := range []string{"shop", "billing"} {
		err := &url.Error{Op: "Get", URL: "http://prom/q?namespace=" + ns, Err: context.DeadlineExceeded}
		if got := halfReason(err); got != "context deadline exceeded" {
			t.Errorf("a short address in %s: %q, want the bare reason", ns, got)
		}
	}
}

// The window a person was handed from the server's cache is never broader
// than what they may list: carol's cluster-wide read is kept, and alice, who
// may list shop only, is not handed it; carol's second display asks nothing.
func TestAKeptWindowIsNeverAnotherPersonsBroaderOne(t *testing.T) {
	series := &fakeSeries{count: 4}
	h := usageHost(t, nil, series)
	first, err := h.UsageWindow(asPerson(platform()), "prod", "")
	if err != nil || len(first.Containers) != 4 {
		t.Fatalf("carol: %v, %d containers", err, len(first.Containers))
	}
	if first.ReadAt.IsZero() {
		t.Error("carol's window does not say when it was read")
	}
	asked := len(series.calls())
	if got, err := h.UsageWindow(asPerson(platform()), "prod", ""); err != nil || len(got.Containers) != 4 ||
		len(series.calls()) != asked {
		t.Errorf("carol's second display: %v, %d containers, %d more queries", err, len(got.Containers), len(series.calls())-asked)
	} else if !got.ReadAt.Equal(first.ReadAt) {
		// The kept window's own read, not this display's.
		t.Errorf("carol's kept window says it was read at %v, want %v", got.ReadAt, first.ReadAt)
	}
	got, err := h.UsageWindow(asPerson(shopViewer()), "prod", "")
	if err != nil {
		t.Fatal(err)
	}
	if keys := keysSorted(got.Containers); strings.Join(keys, ",") != "shop/api-1/app,shop/api-2/app" || got.Complete {
		t.Errorf("alice after carol: %v, complete %v", keys, got.Complete)
	}
}

package kubeadjust

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/thonicdev/kubetower/extension"
)

// The clock every test stands at.
var testNow = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

// fakeHost is extension.Host with what this extra uses made real, and the two
// doors it must never use - Client and Forward - failing the test when called.
type fakeHost struct {
	t  *testing.T
	mu sync.Mutex // a display's reads run together

	objects map[string][]unstructured.Unstructured // by resource
	// listErr fails List for one resource.
	listErr map[string]error
	// allowed narrows the person to these namespaces, as the core does: an
	// all-namespaces list of a namespaced kind comes back incomplete, with a
	// Note, and a namespace outside it is refused.
	allowed []string
	// nodesListable lets a narrowed person list nodes: a ClusterRole for
	// nodes with pods held to some namespaces.
	nodesListable bool
	// queries is every List asked, in order; windowAsked how often the
	// window was.
	queries     []extension.ListQuery
	windowAsked int
	// windowFor is the namespace of every UsageWindow, "" for the whole
	// cluster, in the order asked. promQueries and seriesHanded are what
	// those reads cost a Prometheus, counted as the core runs them
	// (internal/metrics.ReadWindow): the listing, then - when it found
	// something - the CPU and the memory range queries; and the series
	// handed back, one per container per half. handed is the keys.
	windowFor    []string
	promQueries  int
	seriesHanded int
	handed       []string

	usage    extension.LiveUsage
	usageErr error

	window    extension.Window
	windowErr error

	disk      extension.Usage
	diskErr   error
	diskAsked [][]string

	unknown bool // every door answers ErrUnknownCluster
}

func (h *fakeHost) Client(*http.Request, string) (extension.Clients, error) {
	h.t.Error("Host.Client was called: this extra reads only through the reviewed doors, and never " +
		"with the console's own credentials")
	return extension.Clients{}, errors.New("not used")
}

func (h *fakeHost) Forward(context.Context, string, string, string, int) (extension.Tunnel, error) {
	h.t.Error("Host.Forward was called: nothing here opens a tunnel")
	return extension.Tunnel{}, errors.New("not used")
}

func (h *fakeHost) ForwardAsConsole(*http.Request, string, string, string, int) (extension.Tunnel, error) {
	h.t.Error("Host.ForwardAsConsole was called: nothing here opens a tunnel")
	return extension.Tunnel{}, errors.New("not used")
}

func (h *fakeHost) Reveal(*http.Request, string, string, string, string) (string, error) {
	h.t.Error("Host.Reveal was called: nothing here reads a secret")
	return "", errors.New("not used")
}

func (h *fakeHost) Definitions(*http.Request, string) (extension.CRDRegistry, error) {
	h.t.Error("Host.Definitions was called")
	return nil, errors.New("not used")
}

func (h *fakeHost) Exec(*http.Request, string, extension.ExecQuery) (extension.ExecResult, error) {
	return extension.ExecResult{}, errors.New("this fake runs no command")
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
	h.t.Error("Host.MayReadLogs was called: nothing here reads a log")
	return errors.New("not used")
}

var errUnknown = errors.New("unknown")

func (h *fakeHost) gone() error {
	return errors.Join(extension.ErrUnknownCluster, errUnknown)
}

func (h *fakeHost) List(_ *http.Request, _ string, q extension.ListQuery) (extension.Page, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.unknown {
		return extension.Page{}, h.gone()
	}
	if err := h.listErr[q.GVR.Resource]; err != nil {
		return extension.Page{}, err
	}
	h.queries = append(h.queries, q)
	if h.allowed != nil && (q.GVR.Resource == "namespaces" || (q.GVR.Resource == "nodes" && !h.nodesListable)) {
		// A cluster-scoped kind has nothing to narrow to, and a denial refuses it.
		return extension.Page{}, errors.New("you may not list " + q.GVR.Resource)
	}
	if h.allowed != nil && q.Namespace != "" && !slices.Contains(h.allowed, q.Namespace) {
		return extension.Page{}, errors.New("you may not list " + q.GVR.Resource + " in " + q.Namespace)
	}
	var out []unstructured.Unstructured
	narrowed := false
	for _, o := range h.objects[q.GVR.Resource] {
		if q.Namespace != "" && o.GetNamespace() != q.Namespace {
			continue
		}
		if h.allowed != nil && o.GetNamespace() != "" && !slices.Contains(h.allowed, o.GetNamespace()) {
			narrowed = true
			continue
		}
		out = append(out, o)
	}
	if narrowed {
		return extension.Page{Items: out, Note: "You may not list " + q.GVR.Resource +
			" in every namespace, so this holds only the namespaces where you may."}, nil
	}
	return extension.Page{Items: out, Complete: true}, nil
}

func (h *fakeHost) ContainerUsage(_ *http.Request, _, namespace string) (extension.LiveUsage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.unknown {
		return extension.LiveUsage{}, h.gone()
	}
	if h.usageErr != nil {
		return extension.LiveUsage{}, h.usageErr
	}
	out := extension.LiveUsage{Containers: map[string]extension.Resources{}, Complete: h.usage.Complete, Note: h.usage.Note}
	if h.usage.Containers == nil {
		out.Complete = true
	}
	for k, v := range h.usage.Containers {
		if namespace == "" || strings.HasPrefix(k, namespace+"/") {
			out.Containers[k] = v
		}
	}
	return out, nil
}

// UsageWindow is the core's, over a fake collector: scoped by namespace as the
// contract is, a namespace outside the person's refused before anything is
// asked, and every namespace narrowed to theirs.
func (h *fakeHost) UsageWindow(_ *http.Request, _, namespace string) (extension.Window, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.windowAsked++
	h.windowFor = append(h.windowFor, namespace)
	if h.unknown {
		return extension.Window{}, h.gone()
	}
	if h.window.State == "" && h.windowErr == nil {
		return extension.Window{State: extension.WindowNoPrometheus,
			Detail: "this installation has no Prometheus configured", Complete: true}, nil
	}
	if h.windowErr != nil {
		return h.window, h.windowErr
	}
	if h.allowed != nil && namespace != "" && !slices.Contains(h.allowed, namespace) {
		return extension.Window{}, errors.New("you may not list pods in " + namespace)
	}
	h.promQueries++ // the listing
	if h.window.State != extension.WindowRead {
		return h.window, nil
	}
	out := h.window
	out.Containers = map[string]extension.ContainerWindow{}
	inScope := 0
	for k, c := range h.window.Containers {
		ns, _, _ := strings.Cut(k, "/")
		if namespace != "" && ns != namespace {
			continue
		}
		inScope++
		if h.allowed != nil && !slices.Contains(h.allowed, ns) {
			// Read, then narrowed: the core asks Prometheus for the whole
			// scope and drops what the person may not list.
			out.Complete, out.Note = false, "You may not list pods in every namespace, so this holds only the namespaces where you may."
			continue
		}
		out.Containers[k] = c
	}
	if inScope == 0 {
		return extension.Window{State: extension.WindowEmpty, Complete: true,
			Detail: "Prometheus has no container series for namespace " + namespace}, nil
	}
	h.promQueries += 2 // the CPU and the memory windows
	for k, c := range out.Containers {
		h.handed = append(h.handed, k)
		if len(c.CPU) > 0 {
			h.seriesHanded++
		}
		if len(c.Memory) > 0 {
			h.seriesHanded++
		}
	}
	if len(out.Containers) == 0 {
		out = extension.Window{State: extension.WindowEmpty, Complete: false, Note: out.Note,
			Detail: "Prometheus has no container series in the namespaces where you may list pods"}
	}
	return out, nil
}

func (h *fakeHost) DNSWindow(*http.Request, string, extension.DNSSpan) (extension.DNS, error) {
	return extension.DNS{}, nil
}

func (h *fakeHost) Usage(_ *http.Request, _ string, nodes []string) (extension.Usage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.unknown {
		return extension.Usage{}, h.gone()
	}
	h.diskAsked = append(h.diskAsked, append([]string{}, nodes...))
	if h.diskErr != nil {
		return extension.Usage{}, h.diskErr
	}
	if h.disk.Asked == 0 {
		// Every kubelet answered, with nothing on its disks.
		return extension.Usage{Nodes: map[string]extension.NodeUsage{}, Asked: len(nodes), Answered: len(nodes)}, nil
	}
	return h.disk, nil
}

var _ extension.Host = (*fakeHost)(nil)

// add puts typed objects into the fake cluster.
func (h *fakeHost) add(resourceName string, objs ...any) {
	h.t.Helper()
	if h.objects == nil {
		h.objects = map[string][]unstructured.Unstructured{}
	}
	for _, o := range objs {
		raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
		if err != nil {
			h.t.Fatal(err)
		}
		h.objects[resourceName] = append(h.objects[resourceName], unstructured.Unstructured{Object: raw})
	}
}

// --- builders ----------------------------------------------------------------

func quantities(cpu, mem string) corev1.ResourceList {
	out := corev1.ResourceList{}
	if cpu != "" {
		out[corev1.ResourceCPU] = resource.MustParse(cpu)
	}
	if mem != "" {
		out[corev1.ResourceMemory] = resource.MustParse(mem)
	}
	return out
}

// runningPod is a pod started two hours ago, owned by a ReplicaSet when rs is
// set, on node.
func runningPod(namespace, name, rs, node string, containers ...corev1.Container) *corev1.Pod {
	pod := &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       corev1.PodSpec{NodeName: node, Containers: containers},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			QOSClass:  corev1.PodQOSBurstable,
			StartTime: &metav1.Time{Time: testNow.Add(-2 * time.Hour)},
		},
	}
	if rs != "" {
		yes := true
		pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rs, UID: "u", Controller: &yes}}
	}
	for _, c := range containers {
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{Name: c.Name, Ready: true})
	}
	return pod
}

func app(cpuReq, memReq, cpuLim, memLim string) corev1.Container {
	return corev1.Container{Name: "app", Resources: corev1.ResourceRequirements{
		Requests: quantities(cpuReq, memReq), Limits: quantities(cpuLim, memLim)}}
}

func replicaSet(namespace, name, deployment string) *metav1.PartialObjectMetadata {
	yes := true
	return &metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "ReplicaSet"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, OwnerReferences: []metav1.OwnerReference{
			{APIVersion: "apps/v1", Kind: "Deployment", Name: deployment, UID: "d", Controller: &yes}}},
	}
}

func node(name, cpu, mem string) *corev1.Node {
	alloc := quantities(cpu, mem)
	alloc[corev1.ResourcePods] = resource.MustParse("110")
	return &corev1.Node{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Node"},
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.Time{Time: testNow.Add(-72 * time.Hour)}},
		Status: corev1.NodeStatus{
			Allocatable: alloc,
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

func namespaceObj(name string) *corev1.Namespace {
	return &corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func claim(namespace, name, size string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Status: corev1.PersistentVolumeClaimStatus{
			Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)}},
	}
}

func live(entries map[string][2]int64) extension.LiveUsage {
	out := extension.LiveUsage{Containers: map[string]extension.Resources{}, Complete: true}
	for k, v := range entries {
		out.Containers[k] = extension.Resources{CPUMillicores: v[0], MemoryBytes: v[1]}
	}
	return out
}

// shop is a small cluster: one node, a namespace `shop` with a three-replica
// Deployment `api`, one replica OOMKilled, and a namespace `data`.
func shop(t *testing.T) *fakeHost {
	h := &fakeHost{t: t}
	h.add("nodes", node("worker-1", "8", "16Gi"))
	h.add("namespaces", namespaceObj("shop"), namespaceObj("data"))
	h.add("replicasets", replicaSet("shop", "api-7d9f", "api"))
	var pods []any
	for _, name := range []string{"api-7d9f-a", "api-7d9f-b", "api-7d9f-c"} {
		pods = append(pods, runningPod("shop", name, "api-7d9f", "worker-1", app("250m", "512Mi", "500m", "1Gi")))
	}
	oom := pods[2].(*corev1.Pod)
	oom.Status.ContainerStatuses[0].RestartCount = 2
	oom.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
		Reason: "OOMKilled", FinishedAt: metav1.Time{Time: testNow.Add(-time.Hour)}}}
	h.add("pods", pods...)
	h.usage = live(map[string][2]int64{
		"shop/api-7d9f-a/app": {150, 900 << 20},
		"shop/api-7d9f-b/app": {210, 1000 << 20},
		"shop/api-7d9f-c/app": {180, 950 << 20},
	})
	return h
}

func testExtra() KubeAdjust {
	k := New()
	k.now = func() time.Time { return testNow }
	return k
}

// get serves the route for a query and returns the recorder.
func get(t *testing.T, h extension.Host, query url.Values) *httptest.ResponseRecorder {
	t.Helper()
	routes := testExtra().Routes(h)
	req := httptest.NewRequest(http.MethodGet, "/x/docker-desktop/kubeadjust/?"+query.Encode(), nil)
	req.SetPathValue("cluster", "docker-desktop")
	rec := httptest.NewRecorder()
	routes[0].Handler.ServeHTTP(rec, req)
	return rec
}

// page serves the route and decodes a 200.
func page(t *testing.T, h extension.Host, query url.Values) answer {
	t.Helper()
	rec := get(t, h, query)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET ?%s = %d %s", query.Encode(), rec.Code, rec.Body.String())
	}
	var out answer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("not the documented JSON: %v (%s)", err, rec.Body.String())
	}
	return out
}

// raw serves the route and decodes a 200 as a bare map, to see nulls.
func raw(t *testing.T, h extension.Host, query url.Values) map[string]json.RawMessage {
	t.Helper()
	rec := get(t, h, query)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET ?%s = %d %s", query.Encode(), rec.Code, rec.Body.String())
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func fromUnstructured(t *testing.T, obj map[string]any, into any) {
	t.Helper()
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj, into); err != nil {
		t.Fatal(err)
	}
}

func noteOf(t *testing.T, a answer, label string) note {
	t.Helper()
	for _, n := range a.Notes {
		if n.Label == label {
			return n
		}
	}
	t.Fatalf("no %q note in %+v", label, a.Notes)
	return note{}
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

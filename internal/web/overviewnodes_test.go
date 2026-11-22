package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/overview"
	"github.com/thonicdev/kubetower/internal/resources"
)

// One node, two pages, one reading.
//
// On docker-desktop, the same machine at the same minute:
//
//	Nodes list   CPU 168m              Memory 4.6 GiB             (metrics-server, of allocatable)
//	Overview     CPU 3.4 of 28 cores   Memory 3 GiB of 15.4 GiB   (the pods' requests)
//
// the Overview's reading of the machines is the Capacity tile, and it has to
// spell a reading as the Nodes list does.
//
// The fixture is that node - its allocatable, two pods requesting what its pods
// requested - and a metrics-server answer chosen where two spellings could
// differ for the same quantity: 1543m reads `1.5 cores` on the list, 615000Ki
// `600 MiB`.
// Both pages are built from it through their real producers - overview.Cluster
// and resources.List.

// What metrics-server answers for the node, in its own units: nanocores and
// kibibytes.
const (
	desktopCPUUsage    = "1543000000n"
	desktopMemoryUsage = "615000Ki"
)

func desktopNode() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "docker-desktop", Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""}},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("28"),
				corev1.ResourceMemory: resource.MustParse("16144480Ki"),
				corev1.ResourcePods:   resource.MustParse("110"),
			},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

func requesting(name, cpu, memory string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kube-system"},
		Spec: corev1.PodSpec{
			NodeName: "docker-desktop",
			Containers: []corev1.Container{{
				Name: "main",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(cpu),
					corev1.ResourceMemory: resource.MustParse(memory),
				}},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

// withMetricsServer is the fake clientset with a REST client that reaches a
// metrics-server. The fake's own discovery has none - which is why every other
// overview test sees "metrics-server cannot be read" - and the per-node reading
// is exactly what this test is about.
type withMetricsServer struct {
	*fake.Clientset
	rest rest.Interface
}

func (c withMetricsServer) Discovery() discovery.DiscoveryInterfaces {
	return metricsDiscovery{DiscoveryInterfaces: c.Clientset.Discovery(), rest: c.rest}
}

type metricsDiscovery struct {
	discovery.DiscoveryInterfaces
	rest rest.Interface
}

func (d metricsDiscovery) RESTClient() rest.Interface { return d.rest }

// nodeMetrics is metrics-server's answer naming the node, with the given usage.
func nodeMetrics(cpu, memory string) string {
	return `{"kind":"NodeMetricsList","apiVersion":"metrics.k8s.io/v1beta1","items":[` +
		`{"metadata":{"name":"docker-desktop"},"window":"20s",` +
		`"usage":{"cpu":"` + cpu + `","memory":"` + memory + `"}}]}`
}

// desktopCluster is the node and its pods, with a metrics-server that answers
// `status` and `answer` on the node metrics path.
func desktopCluster(t *testing.T, status int, answer string) (*cluster.Client, *fake.Clientset) {
	t.Helper()
	return metricsCluster(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}, 0)
}

// metricsCluster is the node and its pods, with `answer` behind the node
// metrics path and a REST client that gives up after `timeout` (none if zero).
func metricsCluster(t *testing.T, answer http.HandlerFunc, timeout time.Duration) (*cluster.Client, *fake.Clientset) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/metrics.k8s.io/v1beta1/nodes" {
			http.NotFound(w, r)
			return
		}
		answer(w, r)
	}))
	t.Cleanup(server.Close)

	client, err := rest.UnversionedRESTClientFor(&rest.Config{
		Host:          server.URL,
		Timeout:       timeout,
		ContentConfig: rest.ContentConfig{NegotiatedSerializer: scheme.Codecs.WithoutConversion()},
	})
	if err != nil {
		t.Fatalf("a REST client for the fake metrics-server: %v", err)
	}
	objects := []runtime.Object{
		desktopNode(),
		// 3405m and 3112Mi between them: what `kubectl describe node` printed.
		requesting("kube-apiserver", "3", "3000Mi"),
		requesting("coredns", "405m", "112Mi"),
	}
	clientset := fake.NewSimpleClientset(objects...)
	return &cluster.Client{Context: "docker-desktop", Clients: withMetricsServer{Clientset: clientset, rest: client}}, clientset
}

// nodesListCells is the Nodes list's row for the node, by column title, built
// by the list's own producer from the usage metrics-server gave.
func nodesListCells(t *testing.T, client *cluster.Client) map[string]resources.Cell {
	t.Helper()
	// What s.nodeUsage hands the list: metrics.NodeUsage parses metrics-server's
	// quantities with resource.ParseQuantity, MilliValue and Value, as here.
	cpu, memory := resource.MustParse(desktopCPUUsage), resource.MustParse(desktopMemoryUsage)
	usage := map[string]resources.Usage{"docker-desktop": {
		CPUMillicores: cpu.MilliValue(),
		MemoryBytes:   memory.Value(),
	}}
	table, err := resources.List(context.Background(), accesstest.Allow(), client, resources.Query{Kind: "nodes"}, usage)
	if err != nil {
		t.Fatalf("the Nodes list: %v", err)
	}
	if len(table.Rows) != 1 {
		t.Fatalf("the Nodes list has %d rows, want the one node", len(table.Rows))
	}
	cells := map[string]resources.Cell{}
	for i, column := range table.Columns {
		cells[column.Title] = table.Rows[0].Cells[i]
	}
	return cells
}

// overviewMachines is the Overview's machine half, with the reading of now's
// fallback (nil for none).
func overviewMachines(t *testing.T, client *cluster.Client, gate *access.Gate,
	elsewhere overview.NowElsewhere) overview.ClusterOverview {
	t.Helper()
	machines, err := overview.ClusterWithFallback(context.Background(), gate, client, nil, elsewhere)
	if err != nil {
		t.Fatalf("overview.Cluster: %v", err)
	}
	return machines
}

// The Capacity tile and the Nodes list spell one reading one way: on a
// one-node cluster the tile's `used` is that node's reading, `1.5 cores`
// and `600 MiB`, as on the list.
func TestTheTileSpellsAReadingAsTheNodesListDoes(t *testing.T) {
	client, _ := desktopCluster(t, http.StatusOK, nodeMetrics(desktopCPUUsage, desktopMemoryUsage))
	list := nodesListCells(t, client)
	tiles := capacityCells("docker-desktop", overviewMachines(t, client, accesstest.Allow(), nil))
	for _, label := range []string{"CPU", "Memory"} {
		listed := list[label]
		if listed.Text == "" {
			t.Fatalf("the Nodes list drew no %s figure: %+v", label, listed)
		}
		tile := cellNamed(t, tiles, label)
		if tile.Used == nil || !tile.Used.Trustworthy || tile.Used.Text != listed.Text {
			t.Errorf("%s: the tile's used reads %+v and the Nodes list %q, for one node's one reading",
				label, tile.Used, listed.Text)
		}
	}
}

// The Capacity tile names what stopped the reading of now, not the history's
// "no Prometheus, no history". A refused or failing metrics-server, or one in
// its first minute answering for no machine, must not leave the tile saying
// "not measured" with the history's sentence - true of the series under the
// tiles, false as the reason there was no figure. A metrics-server that is not
// installed is the one case where the history's reasons stand: they name the
// missing backend.
//
// Through the real path: overview.Cluster against a fake metrics-server, with
// no history backend at all, then capacityCells.
func TestTheCapacityTileSaysWhyMetricsServerGaveNoFigure(t *testing.T) {
	status := func(code int, reason, message string) string {
		return `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"` + reason +
			`","code":` + strconv.Itoa(code) + `,"message":"` + message + `"}`
	}
	for _, c := range []struct {
		name    string
		code    int
		answer  string
		gate    *access.Gate
		says    string
		history bool // the history's reason, unchanged
	}{
		{name: "answered, nothing measured yet", code: http.StatusOK,
			answer: `{"kind":"NodeMetricsList","apiVersion":"metrics.k8s.io/v1beta1","items":[]}`,
			says:   "has not measured any node yet"},
		{name: "403 from the API server", code: http.StatusForbidden,
			answer: status(403, "Forbidden", `nodes.metrics.k8s.io is forbidden: User \"jane\" cannot list resource \"nodes\"`),
			says:   `cannot list resource "nodes"`},
		{name: "the access review says no", code: http.StatusOK,
			answer: nodeMetrics(desktopCPUUsage, desktopMemoryUsage),
			gate: (&accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
				return q.Attributes.Group != "metrics.k8s.io", nil
			}}).Gate(),
			says: "nodes.metrics.k8s.io"},
		{name: "503, installed and failing", code: http.StatusServiceUnavailable,
			answer: status(503, "ServiceUnavailable", "the server is currently unable to handle the request"),
			says:   "unable to handle the request"},
		{name: "404, not installed", code: http.StatusNotFound,
			answer:  status(404, "NotFound", "the server could not find the requested resource"),
			history: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			gate := c.gate
			if gate == nil {
				gate = accesstest.Allow()
			}
			client, _ := desktopCluster(t, c.code, c.answer)
			machines := overviewMachines(t, client, gate, nil)
			if machines.NowFrom.OK {
				t.Fatalf("NowFrom is OK (%+v); this case is about metrics-server giving no figure", machines.NowFrom)
			}
			if machines.History.OK || machines.History.Detail == "" {
				t.Fatalf("History = %+v; this case needs a history that says why it has nothing", machines.History)
			}
			for _, label := range []string{"CPU", "Memory"} {
				used := cellNamed(t, capacityCells("docker-desktop", machines), label).Used
				if used == nil || used.Trustworthy || used.Value != nil {
					t.Fatalf("%s used = %+v; nothing measured it, so no figure", label, used)
				}
				if c.history {
					if used.Text == machines.NowFrom.Detail {
						t.Errorf("%s used says %q, metrics-server's 404; not installed, the history's reasons stand", label, used.Text)
					}
					continue
				}
				if used.Text != machines.NowFrom.Detail || !strings.Contains(used.Text, c.says) {
					t.Errorf("%s used says %q; want metrics-server's own reason %q, saying %q - not the history's",
						label, used.Text, machines.NowFrom.Detail, c.says)
				}
			}
		})
	}
}

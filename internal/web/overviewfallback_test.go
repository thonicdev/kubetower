package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/metrics"
	"github.com/thonicdev/kubetower/internal/overview"
)

// The Overview's Capacity tile and the Nodes list are two surfaces over the
// same machines, and must not disagree.
//
//   - On a cluster with Prometheus and no metrics-server, the tile draws
//     Prometheus's newest sample, as the Nodes list falls back to Prometheus.
//   - The tile's `used` must not sum only what metrics-server listed: a machine
//     it left out would lower `used` against a capacity that still counts
//     that machine.

// nodeDoor is the `Usage` door as the Nodes list reads it - metrics-server,
// then Prometheus - answering for nodes only, and counting how often it was
// asked.
type nodeDoor struct {
	nodes map[string]metrics.Usage
	err   error
	calls atomic.Int32
}

func (d *nodeDoor) NodeUsage(context.Context, string) (map[string]metrics.Usage, error) {
	d.calls.Add(1)
	return d.nodes, d.err
}

func (d *nodeDoor) PodUsage(context.Context, string, string) (map[string]metrics.Usage, error) {
	return nil, errors.New("not asked here")
}

func (d *nodeDoor) ContainerUsage(context.Context, string, string) (map[string]metrics.Usage, error) {
	return nil, errors.New("not asked here")
}

// statusBody is an API server Status, as metrics-server's aggregator sends one.
func statusBody(code int, reason, message string) string {
	return `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"` + reason +
		`","code":` + strconv.Itoa(code) + `,"message":"` + message + `"}`
}

// The fallback, through the real path: overview.ClusterWithFallback against a
// metrics-server answering 404, with the Nodes list's door behind it, then
// capacityCells.
func TestTheCapacityTileFallsBackToWhatTheNodesListReads(t *testing.T) {
	notInstalled := statusBody(404, "NotFound", "the server could not find the requested resource")
	cpu, memory := resource.MustParse(desktopCPUUsage), resource.MustParse(desktopMemoryUsage)

	t.Run("Prometheus answers: the tile and the Nodes list agree", func(t *testing.T) {
		client, _ := desktopCluster(t, http.StatusNotFound, notInstalled)
		door := &nodeDoor{nodes: map[string]metrics.Usage{"docker-desktop": {
			CPUMillicores: cpu.MilliValue(), MemoryBytes: memory.Value(), From: metrics.KindPrometheus,
		}}}
		reads := apiReads{&Server{usage: door}}
		machines := overviewMachines(t, client, accesstest.Allow(), reads.nodesNowElsewhere("docker-desktop"))
		list := nodesListCells(t, client)
		tiles := capacityCells("docker-desktop", machines)
		for _, title := range []string{"CPU", "Memory"} {
			// No history backend in this test, so the tile's figure is the
			// reading of now - the one the Nodes list draws from Prometheus.
			used := cellNamed(t, tiles, title).Used
			if used == nil || !used.Trustworthy || used.Text != list[title].Text {
				t.Errorf("%s: the tile's used is %+v beside a Nodes list reading %q", title, used, list[title].Text)
			}
		}
	})

	t.Run("no Prometheus either: no figure", func(t *testing.T) {
		client, _ := desktopCluster(t, http.StatusNotFound, notInstalled)
		door := &nodeDoor{err: &metrics.Unavailable{Cluster: "docker-desktop", Kind: metrics.KindNone,
			Detail: "metrics.k8s.io is not there (404) and no Prometheus was found"}}
		reads := apiReads{&Server{usage: door}}
		machines := overviewMachines(t, client, accesstest.Allow(), reads.nodesNowElsewhere("docker-desktop"))
		if !apierrors.IsNotFound(machines.NowNodes.Err) || !strings.Contains(machines.NowNodes.Err.Error(), "no Prometheus was found") {
			t.Errorf("NowNodes.Err = %v; want the 404 standing, with the fallback's sentence beside it", machines.NowNodes.Err)
		}
		used := cellNamed(t, capacityCells("docker-desktop", machines), "CPU").Used
		if used == nil || used.Trustworthy || used.Value != nil {
			t.Errorf("the tile's used is %+v; nothing measured it, so no figure", used)
		}
	})

	t.Run("Prometheus fails: a failure, never not installed", func(t *testing.T) {
		client, _ := desktopCluster(t, http.StatusNotFound, notInstalled)
		door := &nodeDoor{err: &metrics.Unavailable{Cluster: "docker-desktop", Kind: metrics.KindUnreachable,
			Detail: "the port-forward to prometheus-k8s was refused"}}
		reads := apiReads{&Server{usage: door}}
		machines := overviewMachines(t, client, accesstest.Allow(), reads.nodesNowElsewhere("docker-desktop"))
		if apierrors.IsNotFound(machines.NowNodes.Err) {
			t.Errorf("NowNodes.Err = %v: a Prometheus that did not answer is drawn as nothing installed", machines.NowNodes.Err)
		}
		used := cellNamed(t, capacityCells("docker-desktop", machines), "CPU").Used
		if used == nil || used.Value != nil || !strings.Contains(used.Text, "was refused") {
			t.Errorf("the tile's used is %+v; want no figure, and the fallback's own failure", used)
		}
	})

	t.Run("a panic in the fallback is a failure of the read, not the end of the page", func(t *testing.T) {
		client, _ := desktopCluster(t, http.StatusNotFound, notInstalled)
		boom := func(context.Context) overview.Elsewhere { panic("nil map in the fallback") }
		machines := overviewMachines(t, client, accesstest.Allow(), boom)
		used := cellNamed(t, capacityCells("docker-desktop", machines), "CPU").Used
		if used == nil || used.Value != nil || !strings.Contains(used.Text, "panicked") {
			t.Errorf("the tile's used is %+v; want the panic said as a failure of the read", used)
		}
	})

	t.Run("a refusal is not a reason to look elsewhere", func(t *testing.T) {
		client, _ := desktopCluster(t, http.StatusForbidden,
			statusBody(403, "Forbidden", `nodes.metrics.k8s.io is forbidden: User \"jane\" cannot list resource \"nodes\"`))
		door := &nodeDoor{nodes: map[string]metrics.Usage{"docker-desktop": {CPUMillicores: 1, From: metrics.KindPrometheus}}}
		reads := apiReads{&Server{usage: door}}
		machines := overviewMachines(t, client, accesstest.Allow(), reads.nodesNowElsewhere("docker-desktop"))
		if got := door.calls.Load(); got != 0 {
			t.Errorf("the fallback was asked %d times past a 403; a person refused node usage is not shown it from elsewhere", got)
		}
		used := cellNamed(t, capacityCells("docker-desktop", machines), "CPU").Used
		if used == nil || used.Value != nil || !strings.Contains(used.Text, `cannot list resource "nodes"`) {
			t.Errorf("the tile's used is %+v; want no figure, and metrics-server's own refusal", used)
		}
	})
}

// twoNodes is docker-desktop and a second machine, with `answer` behind the
// node metrics path.
func twoNodes(t *testing.T, answer string) *cluster.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/metrics.k8s.io/v1beta1/nodes" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	rc, err := rest.UnversionedRESTClientFor(&rest.Config{
		Host:          server.URL,
		ContentConfig: rest.ContentConfig{NegotiatedSerializer: scheme.Codecs.WithoutConversion()},
	})
	if err != nil {
		t.Fatalf("a REST client for the fake metrics-server: %v", err)
	}
	worker := desktopNode()
	worker.ObjectMeta = metav1.ObjectMeta{Name: "docker-worker"}
	worker.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionUnknown}}
	clientset := fake.NewSimpleClientset([]runtime.Object{desktopNode(), worker}...)
	return &cluster.Client{Context: "docker-desktop", Clients: withMetricsServer{Clientset: clientset, rest: rc}}
}

// metricsFor is metrics-server's answer naming the given machines, each with
// the same usage.
func metricsFor(names ...string) string {
	items := make([]string, 0, len(names))
	for _, name := range names {
		items = append(items, `{"metadata":{"name":"`+name+`"},"window":"20s",`+
			`"usage":{"cpu":"`+desktopCPUUsage+`","memory":"`+desktopMemoryUsage+`"}}`)
	}
	return `{"kind":"NodeMetricsList","apiVersion":"metrics.k8s.io/v1beta1","items":[` + strings.Join(items, ",") + `]}`
}

func TestTheTilesUsedCoversEveryMachineTheCapacityCounts(t *testing.T) {
	t.Run("a machine metrics-server left out: no figure, and the machine named", func(t *testing.T) {
		// docker-worker is NotReady and metrics-server has stopped measuring
		// it; the capacity still counts its 28 cores.
		machines := overviewMachines(t, twoNodes(t, metricsFor("docker-desktop")), accesstest.Allow(), nil)
		used := cellNamed(t, capacityCells("docker-desktop", machines), "CPU").Used
		if used == nil || used.Trustworthy || used.Value != nil {
			t.Fatalf("CPU used is %+v: one machine's usage drawn against two machines' capacity", used)
		}
		if !strings.Contains(used.Text, "1 of the 2 nodes") || !strings.Contains(used.Text, "docker-worker") {
			t.Errorf("CPU used says %q; want how many machines were measured, and which was not", used.Text)
		}
	})

	t.Run("every machine measured: the sum", func(t *testing.T) {
		machines := overviewMachines(t, twoNodes(t, metricsFor("docker-desktop", "docker-worker")), accesstest.Allow(), nil)
		used := cellNamed(t, capacityCells("docker-desktop", machines), "CPU").Used
		if used == nil || used.Value == nil || *used.Value != 2*1543 {
			t.Errorf("CPU used is %+v; want 3086m, both machines", used)
		}
	})

	t.Run("a reading for a machine the node list does not have is not summed", func(t *testing.T) {
		machines := overviewMachines(t, twoNodes(t, metricsFor("docker-desktop", "docker-worker", "gone-node")),
			accesstest.Allow(), nil)
		used := cellNamed(t, capacityCells("docker-desktop", machines), "CPU").Used
		if used == nil || used.Value == nil || *used.Value != 2*1543 {
			t.Errorf("CPU used is %+v; want 3086m - gone-node is in no capacity on this page", used)
		}
	})
}

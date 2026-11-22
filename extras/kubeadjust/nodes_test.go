package kubeadjust

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/extras/kubeadjust/rightsizing"
)

// The Nodes view, about the data: a figure that was measured and a figure that
// was reserved are different claims, drawn within a few pixels of each other.

// Four bands, and the fourth - nobody using it - is what makes this a
// right-sizing gauge rather than a capacity one.
func TestTheGaugeHasABandForNobodyUsingIt(t *testing.T) {
	for _, c := range []struct {
		pct  float64
		want string
	}{{95, "danger"}, {90, "danger"}, {75, "warning"}, {70, "warning"}, {45, "healthy"}, {21, "healthy"}, {20, "overkill"}, {0, "overkill"}} {
		if got := gaugeStatus(c.pct); got != c.want {
			t.Errorf("gaugeStatus(%.0f) = %q, want %q", c.pct, got, c.want)
		}
	}
}

// A node nothing measured draws no usage fill and says so, keeping the
// reservation. Never a usage fill at zero.
func TestAnUnmeasuredNodeDrawsItsReservationAndNoUsage(t *testing.T) {
	u := nodeBar("cpu", "CPU", 10000, 6200, 6200, 0, false, "", rightsizing.FormatCPU)
	if u.UsedPct != nil || u.UseBar != 0 || u.Used != "" {
		t.Errorf("an unmeasured node has a usage figure %+v: a machine whose kubelet has gone reads as idle", u)
	}
	if u.RequestedPct != 62 || u.RequestBar != 62 {
		t.Errorf("the reservation is missing: %+v", u)
	}
	m := nodeBar("cpu", "CPU", 10000, 6200, 6200, 2000, true, "", rightsizing.FormatCPU)
	if m.UsedPct == nil || *m.UsedPct != 20 || m.UseBar != 20 || m.Status != "overkill" {
		t.Errorf("measured bar = %+v", m)
	}
}

// The fill is clamped and the figure is not: limits at 150 % is the most
// useful figure on the card.
func TestOvercommitmentIsClampedOnTheTrackAndSaidBesideIt(t *testing.T) {
	b := nodeBar("cpu", "CPU", 8000, 4000, 12000, 2000, true, "", rightsizing.FormatCPU)
	if b.LimitedPct != 150 || b.LimitBar != 100 {
		t.Errorf("limits = %d%% on a bar at %v, want 150%% at the end of the track", b.LimitedPct, b.LimitBar)
	}
}

// Reserved and not used, said in points; quiet when there is nothing to say.
func TestANodeReservedAndNotUsedSaysSoInPoints(t *testing.T) {
	if gap := nodeBar("cpu", "CPU", 10000, 8000, 8000, 2000, true, "", rightsizing.FormatCPU).Gap; !strings.Contains(gap, "60 points") {
		t.Errorf("gap = %q", gap)
	}
	if gap := nodeBar("cpu", "CPU", 10000, 3000, 3000, 2500, true, "", rightsizing.FormatCPU).Gap; gap != "" {
		t.Errorf("a node reserved to 30%% and running at 25%% drew a gap: %q", gap)
	}
	if gap := nodeBar("cpu", "CPU", 10000, 8000, 8000, 0, false, "", rightsizing.FormatCPU).Gap; gap != "" {
		t.Errorf("an unmeasured node drew a gap: %q", gap)
	}
}

// The coverage caveat survives onto the bar; and an unmeasured node's says
// nothing about coverage, since it covers nothing.
func TestTheNodeBarCarriesHowMuchOfItWasMeasured(t *testing.T) {
	n := rightsizing.NodeSummary{AllocatableCPU: 8000, RequestedCPU: 4000, UsedCPU: 500, Measured: true,
		Containers: 60, ContainersMeasured: 8}
	if got := nodeBars(n, true)[0]; got.Key != "cpu" || got.Coverage != "8 of 60 containers measured" {
		t.Errorf("cpu bar = %+v", got)
	}
	if got := nodeBars(n, false)[0]; got.Coverage != "" || got.Used != "" {
		t.Errorf("unmeasured cpu bar = %+v", got)
	}
}

// What the Node object says about the machine, because a capacity figure reads
// differently once you can see it.
func TestTheNodeCardSaysWhatKindOfMachineThisIs(t *testing.T) {
	cards := nodeCards([]rightsizing.NodeSummary{{
		Name: "cp-1", Ready: true, Pods: 100, MaxPods: 110, TotalPods: 100,
		Roles: []string{"control-plane"}, Age: 72 * time.Hour,
		OSImage: "Alpine Linux v3.24", Kernel: "6.6.30-0", KubeletVersion: "v1.31.4",
		Pressures:         []string{"disk pressure"},
		Taints:            []rightsizing.NodeTaint{{Key: "node-role.kubernetes.io/control-plane", Effect: "NoSchedule"}},
		AllocatableCPU:    8000,
		AllocatableMemory: 16 << 30,
		RequestedCPU:      4000, LimitedCPU: 6000, UsedCPU: 1000, Measured: true,
		TopPods: []rightsizing.NodePod{{Name: "etcd-cp-1", Namespace: "kube-system", CPURequest: 500, CPUUsed: 420, Measured: true}},
	}})
	c := cards[0]
	if !c.ControlPlane || !c.PodsTight || c.Age != "3d" || c.Kernel != "6.6.30-0" || c.Kubelet != "v1.31.4" {
		t.Errorf("card = %+v", c)
	}
	if len(c.Pressures) != 1 || c.Pressures[0] != "disk pressure" {
		t.Errorf("pressures = %v", c.Pressures)
	}
	if len(c.Taints) != 1 || c.Taints[0].Text != "control-plane" || c.Taints[0].Effect != "NoSchedule" {
		t.Errorf("taints = %+v", c.Taints)
	}
	if len(c.Resources) != 3 || c.Resources[0].Allocatable != "8.00 cores" || c.Resources[1].Allocatable != "16.00 GiB" ||
		c.Resources[2].Key != "ephemeral-storage" {
		t.Errorf("resources = %+v, want CPU, memory and ephemeral storage against allocatable", c.Resources)
	}
	if len(c.TopPods) != 1 || c.TopPods[0].Title != "kube-system/etcd-cp-1" || c.More != "the 1 busiest of 100 pods" {
		t.Errorf("top pods = %+v more %q", c.TopPods, c.More)
	}
}

func TestATaintIsShortenedOnTheBadgeAndKeptWholeOnTheTooltip(t *testing.T) {
	for _, c := range []struct {
		taint      rightsizing.NodeTaint
		text, full string
	}{
		{rightsizing.NodeTaint{Key: "node.kubernetes.io/unreachable", Effect: "NoExecute"}, "unreachable", "node.kubernetes.io/unreachable · NoExecute"},
		{rightsizing.NodeTaint{Key: "dedicated", Value: "gpu", Effect: "NoSchedule"}, "dedicated=gpu", "dedicated=gpu · NoSchedule"},
	} {
		if got := shortTaint(c.taint); got != c.text {
			t.Errorf("badge = %q, want %q", got, c.text)
		}
		if got := fullTaint(c.taint); got != c.full {
			t.Errorf("tooltip = %q, want %q", got, c.full)
		}
	}
}

// A pod's reservation and its usage, two fills; the figure is the measurement
// where there is one and the reservation where there is not, never colored
// the same.
func TestAPodBarSeparatesWhatWasReservedFromWhatIsUsed(t *testing.T) {
	m := toPodTrack(2000, 500, 8000, true, rightsizing.FormatCPU)
	if m.Pct != "6%" || m.Status == "" || !strings.Contains(m.Title, "used 500m") || m.ReqPct != 25 || m.UsePct != 6.3 {
		t.Errorf("measured track = %+v", m)
	}
	a := toPodTrack(2000, 0, 8000, false, rightsizing.FormatCPU)
	if a.Pct != "25%" || a.Status != "" || a.UsePct != 0 || !strings.Contains(a.Title, "no usage") {
		t.Errorf("unmeasured track = %+v", a)
	}
}

// The strip's figures say whether they are usage or requests, and average over
// what was measured.
func TestTheClusterTotalsSayWhetherTheyAreUsageOrRequests(t *testing.T) {
	m := clusterTotals([]rightsizing.NamespaceSummary{
		{Name: "a", CPURequest: 2000, CPUUsed: 500, Measured: true},
		{Name: "b", CPURequest: 2000, CPUUsed: 1500, Measured: true},
	})
	if m.Source != "live usage" || m.FromRequests || m.CPUTotal != rightsizing.FormatCPU(2000) {
		t.Errorf("measured totals = %+v", m)
	}
	n := clusterTotals([]rightsizing.NamespaceSummary{{Name: "a", CPURequest: 2000}, {Name: "b", CPURequest: 2000}})
	if !n.FromRequests || n.Source != "requests" || n.CPUTotal != rightsizing.FormatCPU(4000) {
		t.Errorf("unmeasured totals = %+v", n)
	}
	p := clusterTotals([]rightsizing.NamespaceSummary{
		{Name: "a", CPUUsed: 1000, Measured: true}, {Name: "b", CPUUsed: 1000, Measured: true}, {Name: "c"}, {Name: "d"}})
	if p.CPUAvg != rightsizing.FormatCPU(1000) || !strings.Contains(p.Source, "2 of 4") {
		t.Errorf("partial totals = %+v", p)
	}
	if clusterTotals(nil) != nil {
		t.Error("totals over no namespace at all")
	}
}

func TestAnAgeIsOneUnit(t *testing.T) {
	for _, c := range []struct {
		hours int64
		want  string
	}{{0, ""}, {3, "3h"}, {50, "2d"}, {24 * 400, "1y"}} {
		if got := age(time.Duration(c.hours) * time.Hour); got != c.want {
			t.Errorf("%dh gave %q, want %q", c.hours, got, c.want)
		}
	}
}

// --- the count tiles on the Nodes view ------------------------------------

// One tile per state, each node in exactly one, the busier resource deciding.
// A zero is a zero.
func TestTheNodeTilesCountEachNodeOnce(t *testing.T) {
	h := shop(t)
	h.add("nodes", node("idle-1", "8", "16Gi"), node("hot-1", "1", "16Gi"))
	h.add("pods",
		runningPod("data", "batch-0", "", "hot-1", app("500m", "128Mi", "", "")),
		runningPod("data", "quiet-0", "", "idle-1", app("100m", "128Mi", "", "")),
	)
	h.usage.Containers["data/batch-0/app"] = extension.Resources{CPUMillicores: 950, MemoryBytes: 64 << 20}
	// idle-1 hosts a pod metrics-server did not mention: not measured, not idle.

	a := page(t, h, url.Values{"view": {"nodes"}})
	states := map[string]string{}
	for _, c := range a.NodeCards {
		states[c.Name] = c.State
	}
	if states["hot-1"] != "danger" || states["worker-1"] != "overkill" || states["idle-1"] != "none" {
		t.Errorf("states = %v, want hot-1 danger, worker-1 overkill, idle-1 none", states)
	}
	got := map[string]int{}
	total := 0
	for _, tile := range a.NodeStates {
		if tile.Count == nil {
			t.Fatalf("tile %s has no count on a cluster where nodes were measured", tile.Key)
		}
		got[tile.Key] = *tile.Count
		total += *tile.Count
	}
	if got["danger"] != 1 || got["overkill"] != 1 || got["none"] != 1 || got["warning"] != 0 || got["healthy"] != 0 {
		t.Errorf("tiles = %v", got)
	}
	if total != len(a.NodeCards) {
		t.Errorf("tiles sum to %d over %d nodes", total, len(a.NodeCards))
	}
}

// With nothing measured, no node can be placed in a usage band: those tiles are
// unknown (null), not zero, and the Not measured tile counts every node. With no
// node readable at all, every tile is unknown.
func TestAStateWithNothingMeasuredIsNotDrawnAsZero(t *testing.T) {
	cards := []nodeCard{{Name: "a", State: "none"}, {Name: "b", State: "none"}}
	for _, tile := range nodeStates(cards, true) {
		switch {
		case tile.Key == "none" && (tile.Count == nil || *tile.Count != 2):
			t.Errorf("the Not measured tile = %v, want 2", tile.Count)
		case tile.Key != "none" && tile.Count != nil:
			t.Errorf("tile %s = %d with no node measured: an unknown drawn as a zero", tile.Key, *tile.Count)
		}
	}
	for _, tile := range nodeStates(nil, false) {
		if tile.Count != nil {
			t.Errorf("tile %s = %d when no node could be read", tile.Key, *tile.Count)
		}
	}
	// A cluster with no node at all, read: every tile is a real zero.
	for _, tile := range nodeStates([]nodeCard{}, true) {
		if tile.Count == nil || *tile.Count != 0 {
			t.Errorf("tile %s = %v on a cluster read with no node, want 0", tile.Key, tile.Count)
		}
	}

	// End to end: nodes the person may not list are unknown tiles.
	h := shop(t)
	h.listErr = map[string]error{"nodes": errEvery("nodes is forbidden")}
	a := page(t, h, url.Values{"view": {"nodes"}})
	if a.NodeCards != nil {
		t.Errorf("node cards = %v, want null: nodes nobody could list are not a cluster with none", a.NodeCards)
	}
	if got := raw(t, h, url.Values{"view": {"nodes"}}); string(got["nodeCards"]) != "null" {
		t.Errorf("nodeCards = %s, want null", got["nodeCards"])
	}
	for _, tile := range a.NodeStates {
		if tile.Count != nil {
			t.Errorf("tile %s = %d although no node could be read", tile.Key, *tile.Count)
		}
	}
	if c := noteOf(t, a, "Node capacity"); c.OK || !strings.Contains(c.Detail, "forbidden") {
		t.Errorf("Node capacity note = %+v", c)
	}
}

// A node the API answered with and this page could not read is counted and
// said on the Node capacity note, never dropped in silence under "N node(s)".
func TestANodeThatCannotBeReadIsSaidNotDropped(t *testing.T) {
	h := shop(t)
	h.objects["nodes"] = append(h.objects["nodes"], unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Node",
		"metadata": map[string]any{"name": "odd-1"},
		"status":   map[string]any{"allocatable": map[string]any{"cpu": "not-a-quantity"}},
	}})
	a := page(t, h, url.Values{"view": {"nodes"}})
	n := noteOf(t, a, "Node capacity")
	if !strings.Contains(n.Detail, "1 node could not be read, so left out") || !strings.Contains(n.Detail, "1 node(s)") {
		t.Errorf("Node capacity note = %q, want the one read and the one left out", n.Detail)
	}
}

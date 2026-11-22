package kubeadjust

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/extras/kubeadjust/rightsizing"
)

// The node tab and the page's headline.

// getNode serves the node route.
func getNode(t *testing.T, h extension.Host, name string) *httptest.ResponseRecorder {
	t.Helper()
	routes := testExtra().Routes(h)
	var handler http.Handler
	for _, r := range routes {
		if r.Pattern == "/node/{name}" {
			handler = r.Handler
		}
	}
	if handler == nil {
		t.Fatal("no node route")
	}
	req := httptest.NewRequest(http.MethodGet, "/x/docker-desktop/kubeadjust/node/"+name, nil)
	req.SetPathValue("cluster", "docker-desktop")
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func nodeTab(t *testing.T, h extension.Host, name string) nodeAnswer {
	t.Helper()
	rec := getNode(t, h, name)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET node/%s = %d %s", name, rec.Code, rec.Body.String())
	}
	var out nodeAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func resourceOf(t *testing.T, a nodeAnswer, key string) nodeResource {
	t.Helper()
	for _, r := range a.Resources {
		if r.Key == key {
			return r
		}
	}
	t.Fatalf("no %s in %+v", key, a.Resources)
	return nodeResource{}
}

func TestTheNodeTabDrawsRequestsLimitsAndUsageAgainstAllocatable(t *testing.T) {
	a := nodeTab(t, shop(t), "worker-1")
	if len(a.Resources) != 3 {
		t.Fatalf("resources = %+v, want CPU, memory and ephemeral storage", a.Resources)
	}
	cpu := resourceOf(t, a, "cpu")
	// Three pods asking 250m, limited at 500m, using 150 + 210 + 180m, on 8 cores.
	if cpu.Allocatable != "8.00 cores" || cpu.Requested != "750m" || cpu.Limited != "1.50 cores" || cpu.Used != "540m" {
		t.Errorf("cpu = %+v", cpu)
	}
	if cpu.UsedPct == nil || *cpu.UsedPct != 7 || cpu.RequestedPct != 9 {
		t.Errorf("cpu percentages = used %v, requested %d", cpu.UsedPct, cpu.RequestedPct)
	}
	if len(a.PodList) != 3 || a.PodList[0].Workload != "Deployment api" || !a.PodList[0].Measured {
		t.Errorf("pods = %+v", a.PodList)
	}
	if a.Workloads == nil || *a.Workloads != 1 || len(a.Suggestions) == 0 {
		t.Errorf("workloads %v, suggestions %+v: api's findings concern this node", a.Workloads, a.Suggestions)
	}
	for _, s := range a.Suggestions {
		if s.Workload != "api" {
			t.Errorf("a suggestion about a workload not on this node: %+v", s)
		}
	}
	// Ephemeral: the kubelets answered with nothing, so it is measured and
	// empty; the node declares no ephemeral allocatable, so no percentages.
	eph := resourceOf(t, a, "ephemeral-storage")
	if !eph.NoAllocatable || eph.UsedPct != nil {
		t.Errorf("ephemeral = %+v", eph)
	}
}

// The tab draws one node, so it asks one kubelet: the page asks every node
// hosting a pod, the tab only its own, and says the disk figures are
// that node's.
func TestTheNodeTabAsksOnlyItsOwnKubelet(t *testing.T) {
	h := shop(t)
	h.add("nodes", node("worker-2", "8", "16Gi"))
	h.add("pods", runningPod("shop", "api-7d9f-d", "api-7d9f", "worker-2", app("250m", "512Mi", "500m", "1Gi")))

	page(t, h, url.Values{})
	if len(h.diskAsked) != 1 || strings.Join(h.diskAsked[0], ",") != "worker-1,worker-2" {
		t.Fatalf("the page asked the kubelets %v, want every node hosting a pod", h.diskAsked)
	}

	h.diskAsked = nil
	a := nodeTab(t, h, "worker-1")
	if len(h.diskAsked) != 1 || strings.Join(h.diskAsked[0], ",") != "worker-1" {
		t.Errorf("the node tab asked the kubelets %v, want worker-1 alone", h.diskAsked)
	}
	said := false
	for _, n := range a.Notes {
		said = said || strings.Contains(n.Detail, "this node's kubelet only")
	}
	if !said {
		t.Errorf("no note says the disk figures are this node's: %+v", a.Notes)
	}

	// A node hosting no pod in scope asks no kubelet at all.
	h.add("nodes", node("worker-3", "8", "16Gi"))
	h.diskAsked = nil
	nodeTab(t, h, "worker-3")
	if len(h.diskAsked) != 0 {
		t.Errorf("an empty node's tab asked the kubelets %v", h.diskAsked)
	}
}

func TestAnUnknownNodeIs404AndANodeWithNoPodIsEmpty(t *testing.T) {
	h := shop(t)
	if rec := getNode(t, h, "worker-9"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), CodeUnknownNode) {
		t.Errorf("unknown node: %d %s", rec.Code, rec.Body.String())
	}
	if rec := getNode(t, h, "Not_A_Node"); rec.Code != http.StatusBadRequest {
		t.Errorf("a name that cannot be a node: %d", rec.Code)
	}
	h.add("nodes", node("worker-2", "4", "8Gi"))
	a := nodeTab(t, h, "worker-2")
	if len(a.PodList) != 0 || len(a.Suggestions) != 0 || a.Workloads == nil || *a.Workloads != 0 {
		t.Errorf("an empty node: %+v", a)
	}
	if cpu := resourceOf(t, a, "cpu"); cpu.Requested != "0m" || cpu.UsedPct == nil || *cpu.UsedPct != 0 {
		t.Errorf("an empty, measured node uses 0%%: %+v", cpu)
	}
}

func TestWithoutUsageTheNodeTabStillDrawsRequestsAndSaysUsageIsUnmeasured(t *testing.T) {
	h := shop(t)
	h.usageErr = &extension.MetricsUnavailable{Reason: extension.MetricsAbsent, Detail: "the cluster has no metrics-server"}
	a := nodeTab(t, h, "worker-1")
	cpu := resourceOf(t, a, "cpu")
	if a.Measured || cpu.Measured || cpu.UsedPct != nil || cpu.Used != "" {
		t.Errorf("usage drawn on a cluster nobody measured: %+v", cpu)
	}
	if cpu.Requested != "750m" {
		t.Errorf("requested = %q: requests need no usage", cpu.Requested)
	}
	if a.Workloads != nil {
		t.Errorf("workloads = %v, want null: nothing could be judged", *a.Workloads)
	}
	if len(a.PodList) != 3 || a.PodList[0].Measured {
		t.Errorf("pods = %+v", a.PodList)
	}
}

func TestANarrowedPersonSeesTheNodeThroughTheirNamespaces(t *testing.T) {
	h := shop(t)
	h.add("pods", runningPod("data", "db-0", "", "worker-1", app("1", "2Gi", "", "")))
	h.allowed, h.nodesListable = []string{"shop"}, true
	a := nodeTab(t, h, "worker-1")
	for _, p := range a.PodList {
		if p.Namespace == "data" {
			t.Errorf("a pod outside the person's namespaces: %+v", p)
		}
	}
	if !strings.Contains(noteOf(t, answer{Notes: a.Notes}, "Node capacity").Detail, "namespaces you may list") {
		t.Errorf("the capacity note does not say the totals are narrowed: %+v", a.Notes)
	}
	// Said in its own field, for the tab to draw above the bars, and the
	// findings resting on totals withheld: db-0's 2Gi is not in them.
	if a.Partial == "" {
		t.Error("a narrowed tab carries no partial sentence")
	}
	for _, f := range a.Findings {
		if strings.Contains(f.Title, "reserved") || strings.Contains(f.Title, "limits") {
			t.Errorf("a finding resting on partial totals: %+v", f)
		}
	}
	if len(a.Findings) == 0 || a.Findings[len(a.Findings)-1].Title != "Totals over some of its pods" {
		t.Errorf("findings = %+v, want the partial totals said", a.Findings)
	}
	// The whole cluster's person sees none of that.
	if whole := nodeTab(t, shop(t), "worker-1"); whole.Partial != "" {
		t.Errorf("partial on a complete read: %q", whole.Partial)
	}
}

// Narrowed, and no metrics-server: the tab reads pods and nodes itself, and
// says so - not the page's "node capacity not read" above the bars it drew.
func TestWithoutUsageANarrowedTabSaysWhatItRead(t *testing.T) {
	h := shop(t)
	h.add("pods", runningPod("data", "db-0", "", "worker-1", app("1", "2Gi", "", "")))
	h.allowed, h.nodesListable = []string{"shop"}, true
	h.usageErr = &extension.MetricsUnavailable{Reason: extension.MetricsAbsent, Detail: "the cluster has no metrics-server"}
	a := nodeTab(t, h, "worker-1")
	capacity := noteOf(t, answer{Notes: a.Notes}, "Node capacity")
	if !capacity.OK || strings.Contains(capacity.Detail, "not read") || !strings.Contains(capacity.Detail, "namespaces you may list") {
		t.Errorf("capacity note = %+v", capacity)
	}
	if a.Partial == "" {
		t.Error("no partial sentence on a narrowed read")
	}
	if cpu := resourceOf(t, a, "cpu"); cpu.Requested != "750m" {
		t.Errorf("requested = %q, want the shop pods' 750m", cpu.Requested)
	}
}

// An empty node on a cluster nobody could measure is unmeasured, never "0
// used": the zero is a measurement only when usage was read.
func TestAnEmptyNodeIsZeroOnlyWhenUsageWasRead(t *testing.T) {
	h := shop(t)
	h.add("nodes", node("worker-2", "4", "8Gi"))
	h.usageErr = &extension.MetricsUnavailable{Reason: extension.MetricsAbsent, Detail: "the cluster has no metrics-server"}
	cpu := resourceOf(t, nodeTab(t, h, "worker-2"), "cpu")
	if cpu.Measured || cpu.UsedPct != nil || cpu.Used != "" {
		t.Errorf("an empty node on an unmeasured cluster reads as measured: %+v", cpu)
	}
}

// "Act on first" holds every finding somewhere: a cluster whose only findings
// are a limit reduction or a disk must not read as having nothing to change.
func TestActOnFirstIsEmptyOnlyWhenThereIsNothing(t *testing.T) {
	limitOnly := []row{{Kind: "overkill", Action: "Reduce limit", Resource: "Memory", Workload: "a", Namespace: "n"}}
	if got := top(limitOnly, nil); len(got) != 1 {
		t.Errorf("a limit reduction alone left out: %+v", got)
	}
	disk := []row{{Kind: "warning", Action: "Expand PVC", Resource: "PVC", Workload: "db", Namespace: "n"}}
	if got := top(disk, nil); len(got) != 1 {
		t.Errorf("a disk alone left out: %+v", got)
	}
	// A request reduction carried by a "Set limit" row is ranked with the
	// reductions, ahead of a plain warning.
	readings := []rightsizing.WorkloadReading{{Namespace: "n", Kind: "Deployment", Name: "big",
		Containers: []rightsizing.ContainerReading{{Name: "app", Memory: rightsizing.ResourceReading{Request: 8192 << 20}}}}}
	rows := []row{
		{Kind: "warning", Action: "Set request", Resource: "CPU — no request", Workload: "w", Namespace: "n"},
		{Kind: "warning", Action: "Set limit", Resource: "Memory — no limit", Workload: "big", WorkloadKind: "Deployment",
			Namespace: "n", Container: "app", PodCount: 1, suggestedRaw: 1024 << 20, pairedRequestRaw: 768 << 20},
	}
	if got := top(rows, readings); len(got) != 2 || got[0].Workload != "big" {
		t.Errorf("order = %+v, want the folded reduction first", got)
	}
}

func TestTheNodesOwnFindings(t *testing.T) {
	n := rightsizing.NodeSummary{
		Name: "n", Ready: true, Pods: 105, MaxPods: 110,
		AllocatableCPU: 4000, RequestedCPU: 3800, UsedCPU: 400,
		AllocatableMemory: 8 << 30, RequestedMemory: 2 << 30, LimitedMemory: 12 << 30, UsedMemory: 1 << 30,
		Measured: true, Containers: 10, ContainersMeasured: 10,
		Pressures: []string{"memory pressure"},
	}
	got := nodeFindings(n, true)
	titles := []string{}
	for _, f := range got {
		titles = append(titles, f.Kind+": "+f.Title)
	}
	joined := strings.Join(titles, " | ")
	for _, want := range []string{"danger: Under memory pressure", "warning: Memory limits are 150%",
		"warning: CPU is 95% reserved", "warning: 105 of 110 pods", "overkill: Reserved and not used"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	if got[0].Kind != "danger" || got[len(got)-1].Kind != "overkill" {
		t.Errorf("not loudest first: %s", joined)
	}
	// A partial usage sum invents an idle gap: no "reserved and not used" then.
	n.ContainersMeasured = 3
	for _, f := range nodeFindings(n, true) {
		if f.Kind == "overkill" {
			t.Errorf("an idle gap claimed over 3 of 10 containers: %+v", f)
		}
	}
}

func TestTheHeadlineCountsWhatRequestReductionsGiveBack(t *testing.T) {
	h := shop(t)
	// A second workload asking 2 cores and using 50m: its CPU request
	// reduction gives back cores; api's figures give back none.
	h.add("replicasets", replicaSet("shop", "idle-1", "idle"))
	h.add("pods", runningPod("shop", "idle-1-a", "idle-1", "worker-1", app("2", "", "", "")),
		runningPod("shop", "idle-1-b", "idle-1", "worker-1", app("2", "", "", "")))
	h.usage.Containers["shop/idle-1-a/app"] = extension.Resources{CPUMillicores: 50, MemoryBytes: 64 << 20}
	h.usage.Containers["shop/idle-1-b/app"] = extension.Resources{CPUMillicores: 50, MemoryBytes: 64 << 20}
	a := page(t, h, url.Values{})
	if a.Headline == nil {
		t.Fatal("no headline")
	}
	// 2 000m → 1.2 × 50 → 100m, on two replicas: 3 800m given back.
	if a.Headline.ReclaimCPU != "3.80 cores" || a.Headline.ReclaimWorkloads != 1 {
		t.Errorf("headline = %+v", *a.Headline)
	}
	if a.Headline.UsedOfRequestedCPU == nil || a.Headline.Scope != "this cluster" {
		t.Errorf("headline = %+v", *a.Headline)
	}
	// The dangers first, then the reduction that gives back the most - ahead
	// of any warning, which the namespace views carry in full.
	kinds := []string{}
	for _, r := range a.Top {
		kinds = append(kinds, r.Kind)
	}
	if len(a.Top) == 0 || (a.Headline.AtRisk > 0 && a.Top[0].Kind != "danger") {
		t.Errorf("top does not lead with the risks: %v", kinds)
	}
	firstOver, firstWarn := -1, -1
	for i, k := range kinds {
		if k == "overkill" && firstOver < 0 {
			firstOver = i
		}
		if k == "warning" && firstWarn < 0 {
			firstWarn = i
		}
	}
	if firstOver < 0 || (firstWarn >= 0 && firstWarn < firstOver) || a.Top[firstOver].Workload != "idle" {
		t.Errorf("the largest saving (idle) does not come before the warnings: %v %+v", kinds, a.Top)
	}
	// Unmeasured: no headline, never a zero.
	h.usageErr = &extension.MetricsUnavailable{Reason: extension.MetricsAbsent, Detail: "no metrics-server"}
	if r := raw(t, h, url.Values{}); string(r["headline"]) != "null" {
		t.Errorf("headline on a cluster nobody measured: %s", r["headline"])
	}
}

func TestAnHPAIsReadAndTheCardSaysWhatItWithholds(t *testing.T) {
	h := shop(t)
	pct := int32(60)
	h.add("horizontalpodautoscalers", &autoscalingv2.HorizontalPodAutoscaler{
		TypeMeta:   metav1.TypeMeta{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "api"},
			Metrics: []autoscalingv2.MetricSpec{{Type: autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricSource{Name: "cpu",
					Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: &pct}}}},
		},
	})
	// No VPA custom resource on this cluster: an answer, not a failure.
	h.listErr = map[string]error{"verticalpodautoscalers": apierrors.NewNotFound(
		schema.GroupResource{Group: "autoscaling.k8s.io", Resource: "verticalpodautoscalers"}, "")}
	a := page(t, h, url.Values{"ns": {"shop"}})
	if len(a.Cards) != 1 || len(a.Cards[0].Managed) != 1 || !strings.Contains(a.Cards[0].Managed[0], "HPA api") {
		t.Errorf("card = %+v", a.Cards)
	}
	if strings.Contains(noteOf(t, a, "Usage").Detail, "autoscalers") {
		t.Errorf("a missing VPA CRD reported as a failure: %q", noteOf(t, a, "Usage").Detail)
	}

	// A refused VPA list is a failure, and said.
	h.listErr = map[string]error{"verticalpodautoscalers": errors.New("forbidden: you may not list verticalpodautoscalers")}
	a = page(t, h, url.Values{"ns": {"shop"}})
	if !strings.Contains(noteOf(t, a, "Usage").Detail, "autoscalers could not be read") {
		t.Errorf("Usage note = %q", noteOf(t, a, "Usage").Detail)
	}
	if len(a.Cards[0].Managed) != 1 {
		t.Errorf("the HPA was lost with the VPA: %+v", a.Cards[0].Managed)
	}
}

// A request reduction carried by a limit's own command (the engine folds it
// there when the limit alone would be refused) still gives the request back.
func TestARequestCarriedByALimitRowCountsAsGivenBack(t *testing.T) {
	readings := []rightsizing.WorkloadReading{{Namespace: "shop", Kind: "Deployment", Name: "api",
		Containers: []rightsizing.ContainerReading{{Name: "app", Memory: rightsizing.ResourceReading{Request: 2048 << 20}}}}}
	s := rightsizing.Suggestion{Namespace: "shop", WorkloadKind: "Deployment", Workload: "api", Container: "app",
		Resource: "Memory — no limit", Kind: rightsizing.KindWarning, Action: "Set limit", PodCount: 3,
		SuggestedRaw: 1024 << 20, PairedRequestRaw: 768 << 20}
	got, isCPU := reclaim(s, readings)
	if isCPU || got != 3*(2048-768)<<20 {
		t.Errorf("reclaim = %d (cpu %v), want 3 × 1280Mi", got, isCPU)
	}
}

// The page's Nodes view, narrowed: no node is placed in a band from the
// person's own pods. shop plus a 6 CPU / 7Gi data/db-0 on worker-1: the whole
// cluster's person sees worker-1 busy; a person allowed only into shop must not
// see it "barely used".
func TestANarrowedNodesViewPlacesNoNodeInABand(t *testing.T) {
	cluster := func() *fakeHost {
		h := shop(t)
		h.add("pods", runningPod("data", "db-0", "", "worker-1", app("6", "7Gi", "", "")))
		h.usage.Containers["data/db-0/app"] = extension.Resources{CPUMillicores: 6000, MemoryBytes: 7 << 30}
		return h
	}
	whole := page(t, cluster(), url.Values{"view": {"nodes"}})
	if whole.Partial != "" || len(whole.NodeCards) != 1 || whole.NodeCards[0].State != "warning" {
		t.Fatalf("the whole cluster: partial %q, cards %+v", whole.Partial, whole.NodeCards)
	}

	h := cluster()
	h.allowed, h.nodesListable = []string{"shop"}, true
	a := page(t, h, url.Values{"view": {"nodes"}})
	if a.Partial == "" {
		t.Error("a narrowed page carries no partial sentence")
	}
	if len(a.NodeCards) != 1 || a.NodeCards[0].State != stateUnknown {
		t.Fatalf("narrowed cards = %+v, want worker-1 unknown", a.NodeCards)
	}
	for _, r := range a.NodeCards[0].Resources {
		if r.Measured && r.Status != stateUnknown {
			t.Errorf("the %s usage fill keeps a band color: %q", r.Key, r.Status)
		}
	}
	for _, tile := range a.NodeStates {
		if tile.Count != nil {
			t.Errorf("tile %s counts %d over a narrowed read; want unknown", tile.Key, *tile.Count)
		}
	}
	over := page(t, h, url.Values{})
	if over.Headline == nil || over.Headline.Scope != "the namespaces you may list" {
		t.Errorf("headline scope = %+v, want the namespaces the person may list", over.Headline)
	}

	// The gap line is a difference of two lower bounds, which bounds nothing:
	// shop plus a 4 CPU request using 100m, and a BestEffort data/be-0 using
	// 6 CPU. Narrowed to shop, the reserved and the
	// used make a 51-point "gap"; the whole cluster has none.
	gapped := func() *fakeHost {
		h := shop(t)
		h.add("replicasets", replicaSet("shop", "batch-1", "batch"))
		h.add("pods", runningPod("shop", "batch-1-a", "batch-1", "worker-1", app("4", "", "", "")),
			runningPod("data", "be-0", "", "worker-1", corev1.Container{Name: "app"}))
		h.usage.Containers["shop/batch-1-a/app"] = extension.Resources{CPUMillicores: 100, MemoryBytes: 64 << 20}
		h.usage.Containers["data/be-0/app"] = extension.Resources{CPUMillicores: 6000, MemoryBytes: 64 << 20}
		return h
	}
	gapLines := func(a answer) []string {
		var out []string
		for _, card := range a.NodeCards {
			for _, r := range card.Resources {
				if r.Gap != "" {
					out = append(out, r.Label+": "+r.Gap)
				}
			}
		}
		return out
	}
	if got := gapLines(page(t, gapped(), url.Values{"view": {"nodes"}})); len(got) != 0 {
		t.Fatalf("the whole cluster has a CPU gap, so this no longer tests it: %v", got)
	}
	h = gapped()
	h.allowed, h.nodesListable = []string{"shop"}, true
	if got := gapLines(page(t, h, url.Values{"view": {"nodes"}})); len(got) != 0 {
		t.Errorf("a narrowed person is shown a gap the cluster does not have: %v", got)
	}
}

// The panel names the few workloads whose pods here are furthest from what
// they reserve, and counts the rest for the page, which lists them all.
func TestTheNodeTabNamesTheFiveLargestGapsAndCountsTheRest(t *testing.T) {
	h := shop(t)
	// Six more workloads asking 1 to 6 cores and using 50m: the more they
	// ask, the larger their gap on this node.
	for i := 1; i <= 6; i++ {
		name := fmt.Sprintf("idle%d", i)
		h.add("replicasets", replicaSet("shop", name+"-1", name))
		h.add("pods", runningPod("shop", name+"-1-a", name+"-1", "worker-1", app(fmt.Sprint(i), "", "", "")))
		h.usage.Containers["shop/"+name+"-1-a/app"] = extension.Resources{CPUMillicores: 50, MemoryBytes: 64 << 20}
	}
	a := nodeTab(t, h, "worker-1")
	if a.Flagged != 7 {
		t.Errorf("flagged = %d, want api and the six idle workloads", a.Flagged)
	}
	names := []string{}
	for _, w := range a.TopWorkloads {
		names = append(names, w.Workload)
	}
	if strings.Join(names, ",") != "idle6,idle5,idle4,idle3,idle2" {
		t.Errorf("top workloads = %v, want the five largest gaps, the largest first", names)
	}
	if w := a.TopWorkloads[0]; w.Gap != "CPU 5.95 cores idle · memory 64 MiB over request" || w.Worst == "" || w.Findings == 0 {
		t.Errorf("idle6 = %+v", w)
	}
}

// About this node: the five facts a reader looks for, and the rest folded.
func TestTheNodeTabSaysWhatTheMachineIs(t *testing.T) {
	h := &fakeHost{t: t}
	n := node("worker-1", "8", "16Gi")
	n.Labels = map[string]string{"node-role.kubernetes.io/worker": ""}
	n.Status.NodeInfo = corev1.NodeSystemInfo{KubeletVersion: "v1.31.4", OSImage: "Ubuntu 24.04 LTS",
		ContainerRuntimeVersion: "containerd://1.7.24", KernelVersion: "6.8.0-45", Architecture: "amd64"}
	h.add("nodes", n)
	h.add("namespaces", namespaceObj("shop"))
	h.usage = live(map[string][2]int64{})
	a := nodeTab(t, h, "worker-1")
	if a.About.Age == "" {
		t.Error("no age for a node created three days before the test's clock")
	}
	want := nodeAbout{Kubelet: "v1.31.4", OS: "Ubuntu 24.04 LTS", Runtime: "containerd://1.7.24", Age: a.About.Age,
		Kernel: "6.8.0-45", Architecture: "amd64", Roles: []string{"worker"}, Taints: []taint{}}
	if got, _ := json.Marshal(a.About); string(got) != string(must(json.Marshal(want))) {
		t.Errorf("about = %s, want %s", got, must(json.Marshal(want)))
	}
	if a.TopWorkloads == nil || len(a.TopWorkloads) != 0 || a.Flagged != 0 {
		t.Errorf("a node with no pod has workloads: %d %+v", a.Flagged, a.TopWorkloads)
	}

}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

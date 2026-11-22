package kubeadjust

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/extras/kubeadjust/rightsizing"
)

// The page's claims about the data, held by the payload: absent, zero and
// unknown are three values here, so the page cannot draw one as another.

// Three answers where it is tempting to draw one. A namespace like
// `argocd`, whose pods declare no request, has no denominator for a ratio
// and must not read "not measured" with its usage in the same table. A
// namespace asking for nothing is not unmeasured; it is one the scheduler
// places blind. A ratio of one would be the worst answer: it claims the
// namespace is sized exactly right.
func TestANamespaceWithoutRequestsIsNotDrawnAsUnmeasured(t *testing.T) {
	cards := namespaceCards([]rightsizing.NamespaceSummary{
		{Name: "sized", Measured: true, CPURequest: 400, CPUUsed: 100, CPURatio: 4,
			MemRequest: 1 << 30, MemUsed: 1 << 29, MemRatio: 2, Band: "high"},
		{Name: "blind", Measured: true, CPUUsed: 250, MemUsed: 1 << 29, Band: "norequests"},
		{Name: "dark"},
	})
	want := map[string]struct {
		cpu, mem string
		measured bool
		band     string
	}{
		"sized": {"×4.0", "×2.0", true, "high"},
		"blind": {"none set", "none set", true, "norequests"},
		"dark":  {"—", "—", false, ""},
	}
	for _, c := range cards {
		w := want[c.Name]
		if c.CPURatio != w.cpu || c.MemRatio != w.mem || c.Measured != w.measured || c.Band != w.band {
			t.Errorf("%s: %q/%q measured %v band %q, want %q/%q %v %q",
				c.Name, c.CPURatio, c.MemRatio, c.Measured, c.Band, w.cpu, w.mem, w.measured, w.band)
		}
	}
	if cards[2].CPUUsed != "" {
		t.Errorf("an unmeasured namespace carries a used figure %q", cards[2].CPUUsed)
	}
}

// A disk nobody could measure is not an empty disk: no fill, and the claim and
// its capacity still said.
func TestAnUnmeasuredVolumeSaysSoRatherThanDrawingEmpty(t *testing.T) {
	got := volumes([]rightsizing.VolumeReading{
		{Pod: "db-0", Name: "data", Claim: "data-db-0", Type: "pvc",
			Capacity: 5 << 30, Measured: false, Status: rightsizing.StatusNone},
	})[0]
	if got.Measured || got.UsePct != nil || got.Used != "" || got.Pct != "" {
		t.Errorf("an unmeasured claim drew %+v: a disk nobody looked at is drawn as an empty one", got)
	}
	if got.Claim != "data-db-0" || got.Capacity != "5.00 GiB" || got.CeilingWord != "capacity" {
		t.Errorf("the claim is not named with its capacity: %+v", got)
	}
}

// A workload with no volumes on a cluster whose kubelets refused says so on its
// card, end to end from a refused Host.Usage.
func TestAWorkloadOnAClusterWithNoDiskFiguresSaysSo(t *testing.T) {
	h := shop(t)
	h.diskErr = errEvery("nodes \"worker-1\" is forbidden: User cannot get resource \"nodes/proxy\"")
	a := page(t, h, url.Values{"ns": {"shop"}})
	if a.Cards[0].StorageOK {
		t.Error("a card on a cluster whose kubelets refused says its disks were read")
	}
	if d := noteOf(t, a, "Disk"); d.OK || !strings.Contains(d.Detail, "nodes/proxy") {
		t.Errorf("Disk note = %+v", d)
	}
	if e := a.Cards[0].Containers[0].Ephemeral; e.Measured || e.Status != "none" {
		t.Errorf("ephemeral bar = %+v, want unmeasured", e)
	}
	// And the usage still is: the two fail independently.
	if !noteOf(t, a, "Usage").OK {
		t.Error("a refused nodes/proxy turned the Usage note off")
	}
}

type errEvery string

func (e errEvery) Error() string { return string(e) }

// An emptyDir with no sizeLimit can fill the node's disk. Three words for the
// figure beside the bar, because a claim's capacity, a sizeLimit and the node's
// own disk are three different promises.
func TestAnUnboundedEmptyDirIsNamedAsOne(t *testing.T) {
	got := volumes([]rightsizing.VolumeReading{
		{Pod: "w-0", Name: "scratch", Type: "emptyDir", Usage: 1 << 20, Capacity: 40 << 30,
			Measured: true, HasSizeLimit: false, Status: rightsizing.StatusWarning},
		{Pod: "w-0", Name: "data", Claim: "data-w-0", Type: "pvc",
			Capacity: 5 << 30, Usage: 4 << 30, Measured: true, Status: rightsizing.StatusWarning},
	})
	if !got[0].Unbounded || got[0].CeilingWord != "no sizeLimit; the node's own disk holds" {
		t.Errorf("unbounded emptyDir = %+v", got[0])
	}
	if !strings.Contains(got[0].PctOf, "node's own disk") {
		t.Errorf("the percentage of an unbounded emptyDir says it is of %q", got[0].PctOf)
	}
	if got[1].Unbounded || got[1].CeilingWord != "capacity" {
		t.Errorf("claim = %+v", got[1])
	}
	if got[1].Free != "1.00 GiB" || got[1].Pct != "80%" || got[1].UsePct == nil || *got[1].UsePct != 80 {
		t.Errorf("claim at 80%%: free %q pct %q width %v", got[1].Free, got[1].Pct, got[1].UsePct)
	}
	bounded := volumes([]rightsizing.VolumeReading{{Pod: "w-0", Name: "scratch", Type: "emptyDir",
		Usage: 1 << 20, Capacity: 2 << 30, Measured: true, HasSizeLimit: true}})
	if bounded[0].CeilingWord != "sizeLimit" {
		t.Errorf("an emptyDir with a sizeLimit calls it %q", bounded[0].CeilingWord)
	}
}

// The bar's fourth figure, against the peak: the headroom at the worst moment.
// And a measured container with no limit is unbounded, not unmeasured.
func TestABarSaysHowMuchRoomIsLeftAtTheWorstMoment(t *testing.T) {
	measured := toBar("Memory", rightsizing.ResourceReading{Request: 512 << 20, Limit: 2 << 30,
		Mean: 1 << 30, Peak: 1536 << 20, Measured: true, Status: rightsizing.StatusWarning}, rightsizing.FormatMemory)
	if measured.Headroom != rightsizing.FormatMemory(512<<20) || measured.Unbounded {
		t.Errorf("headroom %q unbounded %v, want %q against the peak", measured.Headroom, measured.Unbounded,
			rightsizing.FormatMemory(512<<20))
	}
	if measured.Pct == nil || *measured.Pct != 50 || measured.UsePct != 50 || measured.PeakPct != 75 {
		t.Errorf("geometry = %+v", measured)
	}

	unlimited := toBar("Memory", rightsizing.ResourceReading{Request: 512 << 20, Mean: 1 << 30, Peak: 1 << 30,
		Measured: true, Status: rightsizing.StatusHealthy}, rightsizing.FormatMemory)
	if !unlimited.Unbounded || unlimited.Headroom != "" || unlimited.Pct != nil {
		t.Errorf("unlimited = %+v", unlimited)
	}
	if !strings.HasPrefix(unlimited.Scale, "no limit") || unlimited.UsePct != 100 || unlimited.ReqPct != 50 {
		t.Errorf("an unlimited bar is scaled %q use %v req %v, want the larger of request and peak",
			unlimited.Scale, unlimited.UsePct, unlimited.ReqPct)
	}

	nothing := toBar("CPU", rightsizing.ResourceReading{Request: 100, Limit: 200, Status: rightsizing.StatusNone}, rightsizing.FormatCPU)
	if nothing.Measured || nothing.Mean != "" || nothing.UsePct != 0 || nothing.Pct != nil || nothing.Unbounded {
		t.Errorf("an unmeasured bar = %+v: it must carry no figure that reads as a zero", nothing)
	}
	if nothing.ReqPct != 50 {
		t.Errorf("an unmeasured bar lost its request mark: %+v", nothing)
	}
}

// Three severities with their own headings, empty groups left out, and the
// order inside one is the order of work, not the alphabet's.
func TestTheFindingsAreGroupedBySeverityAndEmptyGroupsAreNotDrawn(t *testing.T) {
	gs := groups([]row{{Kind: "overkill", Resource: "PVC"}, {Kind: "overkill", Resource: "CPU"}})
	if len(gs) != 1 || gs[0].Label != "over-provisioned" || gs[0].Mark != "▼" {
		t.Fatalf("groups = %+v", gs)
	}
	if gs[0].Rows[0].Resource != "CPU" {
		t.Errorf("the group leads with %q, want CPU before PVC", gs[0].Rows[0].Resource)
	}
}

// The chips are counted over everything.
func TestTheCategoryCountsAreOverEverything(t *testing.T) {
	rows := []row{
		{Kind: "danger", Resource: "Memory"},
		{Kind: "warning", Resource: "CPU — no limit"},
		{Kind: "warning", Resource: "EmptyDir"},
		{Kind: "warning", Resource: "Ephemeral"},
	}
	want := map[string]int{"cpu": 1, "memory": 1, "storage": 2}
	for _, c := range categories(rows, true) {
		if c.Count == nil || *c.Count != want[c.Key] {
			t.Errorf("%+v, want count %d", c, want[c.Key])
		}
	}
	for _, c := range categories(nil, false) {
		if c.Count != nil {
			t.Errorf("%s counts %d on a page nothing measured: an unknown drawn as a zero", c.Key, *c.Count)
		}
	}
}

// The page filters by category over groups the server already ordered, where
// the server used to filter first and group after. The two must draw the same
// list and the same command block: keeping a stable sort's rows to a subset
// is the stable sort of that subset.
func TestFilteringTheGroupsDrawsWhatFilteringTheRowsDrew(t *testing.T) {
	const memCmd = "kubectl set resources deployment/api -c app --limits=memory=1Gi -n shop"
	const cpuCmd = "kubectl set resources deployment/api -c app --requests=cpu=100m -n shop"
	rows := []row{
		{Kind: "overkill", Resource: "PVC", Workload: "db"},
		{Kind: "danger", Resource: "Memory", Workload: "api", Kubectl: memCmd},
		{Kind: "overkill", Resource: "CPU", Workload: "api", Kubectl: cpuCmd},
		{Kind: "warning", Resource: "Ephemeral", Workload: "web"},
		{Kind: "overkill", Resource: "Memory — no limit", Workload: "web", Kubectl: memCmd},
		{Kind: "warning", Resource: "CPU — no limit", Workload: "web"},
		{Kind: "overkill", Resource: "EmptyDir", Workload: "web"},
	}
	whole := groups(rows)
	for _, cat := range []string{"cpu", "memory", "storage"} {
		var kept []row
		for _, r := range rows {
			if categoryOf(r.Resource) == cat {
				kept = append(kept, r)
			}
		}
		before := groups(kept)
		var after []group
		for _, g := range whole {
			var in []row
			for _, r := range g.Rows {
				if categoryOf(r.Resource) == cat {
					in = append(in, r)
				}
			}
			if len(in) > 0 {
				after = append(after, group{Kind: g.Kind, Label: g.Label, Mark: g.Mark, Rows: in})
			}
		}
		if fmt.Sprint(before) != fmt.Sprint(after) {
			t.Errorf("%s: filtered first %v, filtered after %v", cat, before, after)
		}
		if fmt.Sprint(commands(before)) != fmt.Sprint(commands(after)) {
			t.Errorf("%s: commands %v against %v", cat, commands(before), commands(after))
		}
	}
}

// Every command once, in one block.
func TestTheCommandBlockIsDeduplicated(t *testing.T) {
	const cmd = "kubectl set resources deployment/api -c app --requests=memory=1Gi --limits=memory=1Gi -n shop"
	got := commands(groups([]row{
		{Kind: "overkill", Resource: "Memory", Kubectl: cmd},
		{Kind: "overkill", Resource: "CPU", Kubectl: cmd},
		{Kind: "overkill", Resource: "PVC"},
	}))
	if len(got) != 1 || got[0] != cmd {
		t.Errorf("block = %v, want the one command once", got)
	}
}

// The window's state reaches the History note, through Host.UsageWindow, in the
// words of the core plus what the absence costs.
func TestTheHistoryNoteSaysWhichOfTheFiveStatesTheWindowIsIn(t *testing.T) {
	for _, c := range []struct {
		window extension.Window
		ok     bool
		want   string
	}{
		{extension.Window{State: extension.WindowNoPrometheus, Detail: "this installation has no Prometheus configured"}, false, "no Prometheus configured"},
		{extension.Window{State: extension.WindowUnreadable, Detail: "connection refused"}, false, "connection refused"},
		{extension.Window{State: extension.WindowEmpty, Detail: "Prometheus has no container series for namespace shop"}, false, "no container series"},
	} {
		h := shop(t)
		h.window = c.window
		n := noteOf(t, page(t, h, url.Values{"ns": {"shop"}}), "History")
		if n.OK != c.ok || !strings.Contains(n.Detail, c.want) || !strings.Contains(n.Detail, "no P95") {
			t.Errorf("%s: History note = %+v", c.window.State, n)
		}
	}
}

// How much a figure rests on reaches the page: a level on every CPU and memory
// row, no value on a row whose container has under a day of usable series, and
// the window's own state, which is what says whether a Prometheus was found.
func TestEachRowSaysHowMuchItsFigureRestsOn(t *testing.T) {
	// n samples at the core's fifteen-minute step, ending now.
	series := func(n int, v float64) []extension.Sample {
		out := make([]extension.Sample, n)
		end := float64(testNow.Unix())
		for i := range out {
			out[i] = extension.Sample{T: end - float64((n-1-i)*15*60), V: v}
		}
		return out
	}
	window := func(n int) extension.Window {
		w := extension.Window{State: extension.WindowRead, Detail: "Prometheus, 7d at 15m", Complete: true,
			Containers: map[string]extension.ContainerWindow{}}
		for _, k := range []string{"shop/api-7d9f-a/app", "shop/api-7d9f-b/app", "shop/api-7d9f-c/app"} {
			w.Containers[k] = extension.ContainerWindow{CPU: series(n, 180), Memory: series(n, 950<<20)}
		}
		return w
	}
	rowsOf := func(a answer) []row {
		var out []row
		for _, g := range a.Groups {
			out = append(out, g.Rows...)
		}
		if len(out) == 0 {
			t.Fatal("the page has no rows to judge")
		}
		return out
	}
	sized := func(r row) bool {
		return strings.HasPrefix(r.Resource, "CPU") || strings.HasPrefix(r.Resource, "Memory")
	}

	t.Run("a week of Prometheus is high", func(t *testing.T) {
		h := shop(t)
		h.window = window(673)
		a := page(t, h, url.Values{"ns": {"shop"}})
		if a.Window != string(extension.WindowRead) {
			t.Errorf("window = %q, want read", a.Window)
		}
		for _, r := range rowsOf(a) {
			if sized(r) && (r.Confidence != string(rightsizing.ConfidenceHigh) || r.InsufficientData) {
				t.Errorf("%s %s: confidence %q, insufficient %v", r.Resource, r.Action, r.Confidence, r.InsufficientData)
			}
		}
	})

	t.Run("no Prometheus is low, and says so at the page level", func(t *testing.T) {
		h := shop(t)
		h.window = extension.Window{State: extension.WindowNoPrometheus, Detail: "this installation has no Prometheus configured"}
		a := page(t, h, url.Values{"ns": {"shop"}})
		if a.Window != string(extension.WindowNoPrometheus) {
			t.Errorf("window = %q, want no-prometheus", a.Window)
		}
		for _, r := range rowsOf(a) {
			if sized(r) && r.Confidence != string(rightsizing.ConfidenceLow) {
				t.Errorf("%s %s from one scrape: confidence %q", r.Resource, r.Action, r.Confidence)
			}
		}
	})

	t.Run("six hours names no value", func(t *testing.T) {
		h := shop(t)
		h.window = window(25)
		insufficient := 0
		for _, r := range rowsOf(page(t, h, url.Values{"ns": {"shop"}})) {
			if !sized(r) {
				continue
			}
			if !r.InsufficientData {
				t.Errorf("%s %s from six hours is not marked insufficient", r.Resource, r.Action)
				continue
			}
			insufficient++
			// The OOM kill's raise keeps the value it takes from the limit.
			if strings.HasPrefix(r.Message, "Container was OOMKilled") {
				continue
			}
			if r.Suggested != "" || r.Kubectl != "" {
				t.Errorf("%s %s still names %q and %q", r.Resource, r.Action, r.Suggested, r.Kubectl)
			}
		}
		if insufficient == 0 {
			t.Error("no row was judged at all")
		}
	})
}

// A reduction withheld for insufficient data has no value, and its zero is
// not a target: the headline must not count the whole request as given back.
func TestAWithheldReductionGivesNothingBack(t *testing.T) {
	readings := []rightsizing.WorkloadReading{{Kind: "Deployment", Name: "api", Namespace: "shop",
		Containers: []rightsizing.ContainerReading{{Name: "app", CPU: rightsizing.ResourceReading{Request: 2000}}}}}
	withheld := rightsizing.Suggestion{Kind: rightsizing.KindOverkill, Action: "Reduce request", Resource: "CPU",
		Namespace: "shop", WorkloadKind: "Deployment", Workload: "api", Container: "app", PodCount: 3,
		InsufficientData: true}
	if v, _ := reclaim(withheld, readings); v != 0 {
		t.Errorf("a reduction with no value gives back %dm", v)
	}
	withheld.SuggestedRaw, withheld.InsufficientData = 500, false
	if v, _ := reclaim(withheld, readings); v != 4500 {
		t.Errorf("a reduction to 500m of three 2-core requests gives back %dm, want 4500m", v)
	}
}

// Through the whole read: a pod an earlier rollout replaced, gone from the
// cluster, still counts towards its Deployment's week. The ReplicaSet listed
// for the page is what ties its name to the Deployment.
func TestAReplacedPodsWeekReachesThePage(t *testing.T) {
	end := float64(testNow.Unix())
	span := func(fromDays, toDays float64, v float64) []extension.Sample {
		var out []extension.Sample
		for at := end - fromDays*86400; at <= end-toDays*86400; at += 15 * 60 {
			out = append(out, extension.Sample{T: at, V: v})
		}
		return out
	}
	w := extension.Window{State: extension.WindowRead, Detail: "Prometheus, 7d at 15m", Complete: true,
		Containers: map[string]extension.ContainerWindow{}}
	for _, k := range []string{"shop/api-7d9f-a/app", "shop/api-7d9f-b/app", "shop/api-7d9f-c/app"} {
		w.Containers[k] = extension.ContainerWindow{CPU: span(3, 0, 180), Memory: span(3, 0, 950<<20)}
	}
	confidence := func(w extension.Window) map[string]bool {
		h := shop(t)
		h.window = w
		out := map[string]bool{}
		for _, g := range page(t, h, url.Values{"ns": {"shop"}}).Groups {
			for _, r := range g.Rows {
				if strings.HasPrefix(r.Resource, "CPU") || strings.HasPrefix(r.Resource, "Memory") {
					out[r.Confidence] = true
				}
			}
		}
		return out
	}
	if got := confidence(w); !got["medium"] || got["high"] {
		t.Fatalf("three days of the running pods alone: %v, want medium", got)
	}
	// The replaced pod ran the four days before.
	w.Containers["shop/api-7d9f-zxcvb/app"] = extension.ContainerWindow{CPU: span(7, 3, 180), Memory: span(7, 3, 950<<20)}
	if got := confidence(w); !got["high"] || got["medium"] {
		t.Errorf("with the replaced pod's four days: %v, want high", got)
	}
}

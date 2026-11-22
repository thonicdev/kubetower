package kubeadjust

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/extension"
)

// --- the extension as the console knows it ------------------------------------

// The key, its old spelling and the menu's word. The three are protocol: the
// key is in bookmarks and stored switches, the old key in state files written
// when the page was called "Right-sizing", and the label is the tool's name.
func TestTheExtensionIsKubeAdjustUnderItsOldNameToo(t *testing.T) {
	k := New()
	if k.Key() != "kubeadjust" {
		t.Errorf("key = %q, want kubeadjust", k.Key())
	}
	if !slices.Equal(k.Legacy(), []string{"rightsizing"}) {
		t.Errorf("legacy = %v, want [rightsizing]: a switch stored under the old key would read off", k.Legacy())
	}
	if k.Label() != "KubeAdjust" {
		t.Errorf("label = %q, want KubeAdjust", k.Label())
	}
	for _, want := range []string{"metrics-server", "Prometheus", "nodes/proxy"} {
		if !strings.Contains(k.Needs(), want) {
			t.Errorf("Needs() does not name %s: %q", want, k.Needs())
		}
	}
}

// The page is read-only, and what keeps it so is that the extension offers
// nothing but a GET and calls no door that could write. fakeHost fails the test
// if Client, Forward or ConfirmToken is reached.
func TestTheRouteAnswersOnlyGETAndCallsNothingThatWrites(t *testing.T) {
	routes := New().Routes(shop(t))
	if len(routes) != 4 {
		t.Fatalf("%d routes, want the page, the node tab, the workload tab and the window's state", len(routes))
	}
	for _, r := range routes {
		if r.Method != http.MethodGet {
			t.Errorf("route %s answers %q: applying a suggestion restarts every pod of the workload, "+
				"which is the operator's deployment to run and not this page's button", r.Pattern, r.Method)
		}
	}
	h := shop(t)
	for _, q := range []url.Values{{}, {"view": {"nodes"}}, {"ns": {"shop"}}, {"ns": {"shop"}, "cat": {"memory"}}} {
		page(t, h, q)
	}
}

// --- failures ------------------------------------------------------------------

// A cluster that is not in the kubeconfig is a 404 with the console's sentence;
// one whose pods cannot be read is a 502. Different people fix them.
func TestAGoneClusterIs404AndAnUnreadableOneIs502(t *testing.T) {
	h := shop(t)
	h.unknown = true
	rec := get(t, h, url.Values{})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown cluster: %d, want 404", rec.Code)
	}
	var p problem
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if p.Code != CodeUnknownCluster || p.Message != extension.UnknownClusterText("docker-desktop") {
		t.Errorf("unknown cluster answered %+v", p)
	}

	h = shop(t)
	h.listErr = map[string]error{"pods": errors.New("dial tcp 127.0.0.1:1: connection refused")}
	rec = get(t, h, url.Values{})
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != http.StatusBadGateway || p.Code != CodeListFailed || !strings.Contains(p.Detail, "connection refused") {
		t.Errorf("unreadable pods: %d %+v, want 502 list_failed with the reason", rec.Code, p)
	}
}

func TestANamespaceThatCannotBeOneIsRefused(t *testing.T) {
	rec := get(t, shop(t), url.Values{"ns": {"Not_A/Namespace"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("%d, want 400", rec.Code)
	}
}

// --- the views -------------------------------------------------------------------

// One answer per scope: `ns` is the scope, and `view` and `cat` are the
// page's to draw from that answer. The route accepts both and answers the same
// bytes whatever they say, so an older address still works and the page keeps
// one cache entry per scope.
func TestViewAndCategoryDoNotChangeTheAnswer(t *testing.T) {
	h := shop(t)
	for _, c := range []struct {
		ns   string
		asks []url.Values
	}{
		{"", []url.Values{{"view": {"nodes"}}, {"view": {"bogus"}}, {"cat": {"memory"}}, {"view": {"nodes"}, "cat": {"cpu"}}}},
		{"shop", []url.Values{{"view": {"nodes"}}, {"cat": {"memory"}}, {"cat": {"cpus"}}}},
	} {
		base := url.Values{}
		if c.ns != "" {
			base.Set("ns", c.ns)
		}
		want := get(t, h, base).Body.String()
		for _, ask := range c.asks {
			if c.ns != "" {
				ask.Set("ns", c.ns)
			}
			if got := get(t, h, ask).Body.String(); got != want {
				t.Errorf("?%s answered differently from ?%s", ask.Encode(), base.Encode())
			}
		}
	}
}

// Three views over two scopes. The whole cluster builds both of its views, the
// overview's and the Nodes one, so switching between them reads nothing; one
// namespace builds its own. JSON says which by null: a body not built is null,
// a body built empty is [].
func TestEachScopeBuildsTheBodiesOfItsViews(t *testing.T) {
	h := shop(t)
	bodies := []string{"namespaceCards", "nodeCards", "nodeStates", "totals", "cards"}
	for _, c := range []struct {
		query url.Values
		built []string
	}{
		{url.Values{}, []string{"namespaceCards", "nodeCards", "nodeStates", "totals"}},
		{url.Values{"ns": {"shop"}}, []string{"cards"}},
	} {
		got := raw(t, h, c.query)
		for _, body := range bodies {
			isNull := string(got[body]) == "null"
			if slices.Contains(c.built, body) && (isNull || string(got[body]) == "[]") {
				t.Errorf("?%s: %s is %s, want the view's body", c.query.Encode(), body, got[body])
			}
			if !slices.Contains(c.built, body) && !isNull {
				t.Errorf("?%s: %s was built (%s) in a scope that does not show it", c.query.Encode(), body, got[body])
			}
		}
		for _, gone := range []string{"view", "category"} {
			if _, ok := got[gone]; ok {
				t.Errorf("?%s: the answer still carries %q, which is the page's to choose", c.query.Encode(), gone)
			}
		}
	}

	cluster := page(t, h, url.Values{})
	if len(cluster.NamespaceCards) != 1 || cluster.NamespaceCards[0].Name != "shop" || cluster.Totals == nil {
		t.Errorf("overview: %+v totals %+v", cluster.NamespaceCards, cluster.Totals)
	}
	if len(cluster.NodeCards) != 1 || cluster.NodeCards[0].Name != "worker-1" || cluster.NodeCards[0].Pods != 3 {
		t.Errorf("nodes: %+v", cluster.NodeCards)
	}
	if r := cluster.NodeCards[0].Resources; len(r) == 0 || r[0].Allocatable != "8.00 cores" {
		t.Errorf("the node's CPU bar is %+v", r)
	}
}

// The namespace view: the numbers, and the advice beside them. End to end, from
// pods, a ReplicaSet and metrics-server's figures to the card.
func TestTheKubeAdjustCardDrawsAWorkload(t *testing.T) {
	a := page(t, shop(t), url.Values{"ns": {"shop"}})
	if len(a.Cards) != 1 {
		t.Fatalf("%d cards, want the Deployment pooled into one: %+v", len(a.Cards), a.Cards)
	}
	card := a.Cards[0]
	if card.Workload != "api" || card.Kind != "Deployment" || card.Replicas != 3 || card.QOS != "Burstable" {
		t.Errorf("card = %s %s ×%d %s, want Deployment api ×3 Burstable (the two hops: pod → "+
			"ReplicaSet → Deployment)", card.Kind, card.Workload, card.Replicas, card.QOS)
	}
	if card.Worst != "danger" {
		t.Errorf("worst = %q, want danger for an OOMKilled container", card.Worst)
	}
	c := card.Containers[0]
	if !c.OOMKilled || c.Restarts != 2 {
		t.Errorf("container: oom %v restarts %d", c.OOMKilled, c.Restarts)
	}
	if !c.CPU.Measured || c.CPU.Mean != "180m" || c.CPU.Peak != "210m" || c.CPU.Request != "250m" || c.CPU.Limit != "500m" {
		t.Errorf("CPU bar = %+v", c.CPU)
	}
	if c.CPU.UsePct != 36 || c.CPU.ReqPct != 50 || c.CPU.PeakPct != 42 || c.CPU.Pct == nil || *c.CPU.Pct != 36 {
		t.Errorf("CPU geometry = use %v req %v peak %v pct %v, want 36/50/42/36", c.CPU.UsePct, c.CPU.ReqPct, c.CPU.PeakPct, c.CPU.Pct)
	}
	if len(c.Pods) != 3 || !c.Pods[1].Measured || c.Pods[1].CPU != "210m" {
		t.Errorf("per-replica figures = %+v", c.Pods)
	}
	var increase *row
	for i := range card.Findings {
		if card.Findings[i].Action == "Increase limit" && card.Findings[i].Resource == "Memory" {
			increase = &card.Findings[i]
		}
	}
	if increase == nil {
		t.Fatalf("no memory increase on an OOMKilled container: %+v", card.Findings)
	}
	// The replicas use nearly twice their 512Mi request, so the OOM row raises
	// the request in the same command as the limit: one command, one limit.
	if !strings.HasPrefix(increase.Kubectl, "kubectl set resources deployment/api -c app --requests=memory=") ||
		!strings.Contains(increase.Kubectl, " --limits=memory=") ||
		!strings.HasSuffix(increase.Kubectl, " -n shop") {
		t.Errorf("command = %q", increase.Kubectl)
	}
	if !slices.Contains(a.Commands, increase.Kubectl) {
		t.Errorf("the command block %v does not carry the card's command", a.Commands)
	}
	if len(a.Groups) == 0 || a.Groups[0].Label != "critical" {
		t.Errorf("groups = %+v, want critical first", a.Groups)
	}
	if a.Counts[0].Kind != "danger" || a.Counts[0].Count == nil || *a.Counts[0].Count == 0 {
		t.Errorf("counts = %+v", a.Counts)
	}
}

// Every suggestion is in the answer, whatever `cat` says, and the chips are
// counted over all of them: the page filters, and a chip's count reads the same
// whichever chip is selected.
func TestEverySuggestionIsAnsweredAndTheChipsCountThemAll(t *testing.T) {
	h := shop(t)
	all := page(t, h, url.Values{"ns": {"shop"}})
	memory := page(t, h, url.Values{"ns": {"shop"}, "cat": {"memory"}})
	if len(memory.Commands) != len(all.Commands) || len(memory.Groups) != len(all.Groups) {
		t.Errorf("cat=memory filtered the answer: %d groups against %d", len(memory.Groups), len(all.Groups))
	}
	seen := map[string]int{}
	for _, g := range all.Groups {
		for _, r := range g.Rows {
			seen[categoryOf(r.Resource)]++
		}
	}
	if len(seen) == 0 {
		t.Fatal("the fixture has no finding to count")
	}
	for _, c := range all.Categories {
		if c.Count == nil || *c.Count != seen[c.Key] {
			t.Errorf("chip %s counts %v, want %d: the rows the answer carries", c.Key, c.Count, seen[c.Key])
		}
	}
}

// The four notes, always, each with a sentence, and disk on its own line.
func TestTheFourNotesAreAlwaysThereAndNeverEmpty(t *testing.T) {
	for name, h := range map[string]*fakeHost{
		"measured": shop(t),
		"unmeasured": func() *fakeHost {
			h := shop(t)
			h.usageErr = &extension.MetricsUnavailable{Reason: extension.MetricsAbsent, Detail: "the cluster has no metrics-server"}
			return h
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			for _, q := range []url.Values{{}, {"view": {"nodes"}}, {"ns": {"shop"}}} {
				a := page(t, h, q)
				var labels []string
				for _, n := range a.Notes {
					labels = append(labels, n.Label)
					if strings.TrimSpace(n.Detail) == "" {
						t.Errorf("?%s: the %s note is empty, which reads as nothing to say", q.Encode(), n.Label)
					}
				}
				if !slices.Equal(labels, []string{"Usage", "Node capacity", "Disk", "History"}) {
					t.Errorf("?%s: notes are %v", q.Encode(), labels)
				}
			}
		})
	}
}

// A cluster nobody could measure is not a cluster with nothing to change: Ready
// is false, the Usage note says why, and the counts are not a clean bill.
func TestAnUnmeasuredClusterIsNotDrawnAsAWellTunedOne(t *testing.T) {
	h := shop(t)
	h.usageErr = &extension.MetricsUnavailable{Reason: extension.MetricsAbsent, Detail: "the cluster has no metrics-server"}
	a := page(t, h, url.Values{})
	if a.Ready {
		t.Fatal("ready with no usage at all")
	}
	if u := noteOf(t, a, "Usage"); u.OK || !strings.Contains(u.Detail, "metrics-server") {
		t.Errorf("Usage note = %+v", u)
	}
	for _, c := range a.Counts {
		if c.Count != nil {
			t.Errorf("%s counts %d on a cluster nobody measured: that is a clean bill it did not earn",
				c.Kind, *c.Count)
		}
	}
	// The namespace selector still works: the page is navigable while it says
	// it cannot measure.
	if !slices.Contains(a.Namespaces, "shop") {
		t.Errorf("selector = %v", a.Namespaces)
	}
}

// The selected namespace is always in the selector, even one the list has not
// caught up with.
func TestTheSelectedNamespaceIsAlwaysOffered(t *testing.T) {
	a := page(t, shop(t), url.Values{"ns": {"made-a-minute-ago"}})
	if !slices.Contains(a.Namespaces, "made-a-minute-ago") {
		t.Errorf("selector = %v", a.Namespaces)
	}
}

// A person allowed into one namespace sees only it, and the notes say the
// answer is partial.
func TestANarrowedPersonSeesTheirNamespaceAndIsToldItIsPartial(t *testing.T) {
	h := shop(t)
	h.add("pods", runningPod("data", "db-0", "", "worker-1", app("100m", "128Mi", "", "")))
	h.usage.Containers["data/db-0/app"] = extension.Resources{CPUMillicores: 50, MemoryBytes: 64 << 20}
	h.allowed = []string{"shop"}
	h.usage.Complete, h.usage.Note = false, "You may not list pods in every namespace, so this holds only the namespaces where you may."

	a := page(t, h, url.Values{})
	for _, c := range a.NamespaceCards {
		if c.Name == "data" {
			t.Error("a namespace the person may not list is on the page")
		}
	}
	if u := noteOf(t, a, "Usage"); !strings.Contains(u.Detail, "only the namespaces where you may") {
		t.Errorf("the Usage note does not say the answer is partial: %q", u.Detail)
	}
	if !slices.Equal(a.Namespaces, []string{"shop"}) {
		t.Errorf("selector = %v, want the one namespace", a.Namespaces)
	}
}

// The pods are asked for in the namespace the page shows, and nowhere wider:
// a page about `shop` sized from every namespace's pods is a page about the
// cluster wearing one namespace's name.
func TestThePodsAreListedInTheNamespaceAsked(t *testing.T) {
	for _, ns := range []string{"shop", ""} {
		h := shop(t)
		page(t, h, url.Values{"ns": {ns}})
		var asked []string
		for _, q := range h.queries {
			if q.GVR.Resource == "pods" {
				asked = append(asked, q.Namespace)
			}
		}
		if !slices.Equal(asked, []string{ns}) {
			t.Errorf("ns=%q: pods listed in %q, want exactly [%q]", ns, asked, ns)
		}
	}
}

// A person who may list nodes but only some namespaces' pods sees node totals
// summed over their pods alone, and the Node capacity note says so.
func TestANarrowedPersonIsToldTheNodeTotalsArePartial(t *testing.T) {
	h := shop(t)
	h.add("pods", runningPod("data", "db-0", "", "worker-1", app("100m", "128Mi", "", "")))
	h.allowed, h.nodesListable = []string{"shop"}, true
	n := noteOf(t, page(t, h, url.Values{"view": {"nodes"}}), "Node capacity")
	if !n.OK || !strings.Contains(n.Detail, "node totals count only the pods in the namespaces you may list") {
		t.Errorf("Node capacity note = %+v", n)
	}
}

// With no usage, nothing is sized, so the window is not read for
// nothing, and the History note says why there is none.
func TestNoUsageAsksForNoWindow(t *testing.T) {
	h := shop(t)
	h.usageErr = &extension.MetricsUnavailable{Reason: extension.MetricsAbsent, Detail: "no metrics-server"}
	a := page(t, h, url.Values{})
	if h.windowAsked != 0 {
		t.Errorf("the window was asked %d time(s) with no usage to size", h.windowAsked)
	}
	if n := noteOf(t, a, "History"); n.OK || !strings.Contains(n.Detail, "not asked for") {
		t.Errorf("History note = %+v", n)
	}
	h = shop(t)
	page(t, h, url.Values{})
	if h.windowAsked != 1 {
		t.Errorf("the window was asked %d time(s) with usage, want 1", h.windowAsked)
	}
}

// Nothing measured is nothing built: every body and the workload count are
// null, not empty, so no view reads as "none".
func TestNothingMeasuredBuildsNoBody(t *testing.T) {
	h := shop(t)
	h.usageErr = &extension.MetricsUnavailable{Reason: extension.MetricsAbsent, Detail: "no metrics-server"}
	for _, q := range []url.Values{{}, {"view": {"nodes"}}, {"ns": {"shop"}}} {
		got := raw(t, h, q)
		for _, member := range []string{"workloads", "totals", "namespaceCards", "nodeCards", "nodeStates", "cards"} {
			if string(got[member]) != "null" {
				t.Errorf("?%s: %s = %s with nothing measured, want null", q.Encode(), member, got[member])
			}
		}
	}
}

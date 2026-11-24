package netinspect

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/extension"
)

func overviewOf(t *testing.T, h *fakeHost, query string) Overview {
	t.Helper()
	w := serve(t, h, "/x/kind/netinspect/"+query)
	if w.Code != http.StatusOK {
		t.Fatalf("%s answered %d: %s", query, w.Code, w.Body.String())
	}
	return decode[Overview](t, w)
}

func TestTheOverviewCountsWhatTheTilesFilter(t *testing.T) {
	h := shop(t)
	h.policy(t, defaultDenyIngress("shop"))
	h.policy(t, apiFromWeb())
	h.policy(t, webEgress(80))
	h.policy(t, np("shop", "legacy-db", map[string]string{"app": "db"}))
	o := overviewOf(t, h, "")
	if o.Mode != "rules" {
		t.Errorf("mode %q: everything here is the rules, and says so", o.Mode)
	}
	want := Summary{Pods: 4, IsolatedIn: 3, IsolatedOut: 1, Open: 1, Policies: 4, DefaultDeny: 1, Idle: 1}
	if o.Summary != want {
		t.Errorf("summary %+v, want %+v", o.Summary, want)
	}
	if len(o.Policies) != 4 || o.PoliciesTotal != 4 {
		t.Errorf("policies %d of %d", len(o.Policies), o.PoliciesTotal)
	}
	for _, p := range o.Policies {
		if p.Name == "legacy-db" && p.Selects != 0 {
			t.Errorf("legacy-db selects nothing, got %d", p.Selects)
		}
		if p.Name == "api-from-web" && (p.Selects != 1 || len(p.Ingress) != 1 || !strings.Contains(p.Ingress[0], "http")) {
			t.Errorf("api-from-web: %+v", p)
		}
	}
	var shopRow NSRow
	for _, n := range o.Namespaces {
		if n.Name == "shop" {
			shopRow = n
		}
	}
	if !shopRow.DefaultDenyIn || shopRow.DefaultDenyOut || shopRow.Pods != 3 {
		t.Errorf("shop: %+v", shopRow)
	}

	// The tiles' filters.
	for show, want := range map[string]int{"in": 3, "out": 1, "open": 1, "host": 0, "": 4} {
		if got := overviewOf(t, h, "?show="+show).Pods.Total; got != want {
			t.Errorf("show=%s: %d pods, want %d", show, got, want)
		}
	}
	if o := overviewOf(t, h, "?q=strang"); o.Pods.Total != 1 || o.Pods.Rows[0].Name != "stranger" {
		t.Errorf("search: %+v", o.Pods)
	}
	if o := overviewOf(t, h, "?ns=ops"); o.Summary.Pods != 1 || o.Summary.Policies != 0 || o.Graph.Scope != "namespace" || len(o.Graph.Nodes) != 0 {
		t.Errorf("one namespace: %+v %s", o.Summary, o.Graph.Scope)
	}
}

// Under one namespace the answer still names every namespace, so the page's
// menu can move to another without going back through the cluster.
func TestOneNamespaceStillOffersEveryNamespace(t *testing.T) {
	h := shop(t)
	for _, query := range []string{"", "?ns=shop", "?ns=ops"} {
		if got := strings.Join(overviewOf(t, h, query).Choices, ","); got != "ops,shop" {
			t.Errorf("%q: choices %q, want ops,shop", query, got)
		}
	}
}

// The graph draws an edge only where a rule speaks, named by its ports and
// policies; an open namespace is a node with no edge.
func TestTheGraphDrawsTheRules(t *testing.T) {
	h := shop(t)
	h.policy(t, apiFromWeb())
	p := np("shop", "api-from-ops", map[string]string{"app": "api"})
	p.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{nsPeer(map[string]string{"team": "ops"})}, Ports: []networkingv1.NetworkPolicyPort{tcp(9090)}}}
	h.policy(t, p)

	cluster := overviewOf(t, h, "").Graph
	if len(cluster.Edges) != 1 || cluster.Edges[0].From != "ns:ops" || cluster.Edges[0].To != "ns:shop" ||
		cluster.Edges[0].Side != "ingress" || strings.Join(cluster.Edges[0].Ports, ",") != "9090/TCP" {
		t.Fatalf("cluster edges: %+v", cluster.Edges)
	}
	for _, n := range cluster.Nodes {
		if n.ID == "ns:shop" && n.Internal != 1 {
			t.Errorf("shop's rule between its own pods is drawn on its node, got %+v", n)
		}
	}
}

// The graph is bounded however many namespaces the rules join, and never by
// leaving out an edge between what it keeps: a missing edge reads as a pair
// no rule allows. Sparse namespaces are sent whole up to maxNodes; dense ones
// (each letting every namespace in) are folded until every edge fits.
func TestTheGraphIsBounded(t *testing.T) {
	sparse := overviewOf(t, teams(t, 120, false), "").Graph
	if len(sparse.Nodes) != 120 || sparse.Folded != 0 || sparse.Truncated {
		t.Errorf("120 sparse namespaces: %d nodes (%d folded, truncated %v), want 120, none, false", len(sparse.Nodes), sparse.Folded, sparse.Truncated)
	}
	capped := overviewOf(t, teams(t, maxNodes+20, false), "").Graph
	if len(capped.Nodes) != maxNodes || capped.Folded != 21 || capped.Truncated {
		t.Errorf("%d sparse namespaces: %d nodes (%d folded)", maxNodes+20, len(capped.Nodes), capped.Folded)
	}
	if last := capped.Nodes[len(capped.Nodes)-1]; last.ID != "others" || last.Label != "21 others" {
		t.Errorf("last node %+v, want the 21 others", last)
	}
	for _, n := range []int{60, 120, 300} {
		g := overviewOf(t, teams(t, n, true), "").Graph
		if g.Truncated || len(g.Edges) > maxEdges || g.Folded == 0 {
			t.Errorf("%d dense namespaces: %d nodes, %d folded, %d edges, truncated %v", n, len(g.Nodes), g.Folded, len(g.Edges), g.Truncated)
		}
		// A namespace letting every namespace in lets its own pods in too.
		for _, node := range g.Nodes {
			if node.Kind == nodeNamespace && node.Internal == 0 {
				t.Fatalf("%d dense namespaces: %s has no internal rule", n, node.ID)
			}
		}
		// Every pair of nodes kept, "others" included, still carries its allow.
		allowed := map[string]bool{}
		for _, e := range g.Edges {
			if e.Kind == "allow" {
				allowed[e.From+">"+e.To] = true
			}
		}
		for _, a := range g.Nodes {
			for _, b := range g.Nodes {
				if a.ID != b.ID && !allowed[a.ID+">"+b.ID] {
					t.Fatalf("%d dense namespaces: %s → %s lost its allow (%d nodes, %d edges)", n, a.ID, b.ID, len(g.Nodes), len(g.Edges))
				}
			}
		}
	}
}

// teams is n namespaces, each with a pod and a policy letting in every
// namespace (dense) or only its own pods (sparse).
func teams(t *testing.T, n int, dense bool) *fakeHost {
	t.Helper()
	h := newHost()
	for i := range n {
		ns := fmt.Sprintf("team-%03d", i)
		h.namespace(t, ns, nil)
		h.pod(t, ns, "app", fmt.Sprintf("10.%d.%d.1", 1+i/250, i%250), map[string]string{"app": "app"})
		p := np(ns, "from-all", nil)
		peer := podPeer(nil)
		if dense {
			peer = nsPeer(nil)
		}
		p.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{peer}}}
		h.policy(t, p)
	}
	return h
}

func TestPodsArePaged(t *testing.T) {
	h := newHost()
	for i := range podsPerPage + 50 {
		h.pod(t, "big", fmt.Sprintf("p-%04d", i), "", map[string]string{"i": fmt.Sprint(i)})
	}
	first := overviewOf(t, h, "").Pods
	second := overviewOf(t, h, fmt.Sprintf("?offset=%d", podsPerPage)).Pods
	if first.Total != podsPerPage+50 || len(first.Rows) != podsPerPage || len(second.Rows) != 50 || second.Rows[0].Name != fmt.Sprintf("p-%04d", podsPerPage) {
		t.Errorf("pages: %d/%d rows, then %d from %s", len(first.Rows), first.Total, len(second.Rows), second.Rows[0].Name)
	}
	if w := serve(t, h, "/x/kind/netinspect/?offset=-1"); w.Code != http.StatusBadRequest {
		t.Errorf("a negative offset answered %d", w.Code)
	}
	if w := serve(t, h, "/x/kind/netinspect/?show=everything"); w.Code != http.StatusBadRequest {
		t.Errorf("an unknown filter answered %d", w.Code)
	}
}

// Every listing is capped, and none goes through Host.Client (the fake
// refuses it).
func TestEveryListingIsCapped(t *testing.T) {
	h := shop(t)
	overviewOf(t, h, "")
	if len(h.queries) == 0 {
		t.Fatal("nothing was listed")
	}
	for _, q := range h.queries {
		if q.Limit <= 0 {
			t.Errorf("%s was listed without a Limit", q.GVR.Resource)
		}
	}
}

// R5 §5: refused for the person, failed, and empty are three answers.
func TestRefusedFailedAndEmptyAreThreeAnswers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resource string
		err      error
		status   int
		code     string
		says     string
	}{
		{"policies denied", "networkpolicies", fmt.Errorf("list: %w", extension.ErrDenied), http.StatusForbidden, CodeListForbidden, "not a cluster without rules"},
		{"policies unreviewable", "networkpolicies", fmt.Errorf("list: %w", extension.ErrUnreviewable), http.StatusServiceUnavailable, CodeListUnreviewed, "could not ask"},
		{"policies failed", "networkpolicies", errorString("connection refused"), http.StatusBadGateway, CodeListFailed, "not a cluster without rules"},
		{"pods denied", "pods", fmt.Errorf("list: %w", extension.ErrDenied), http.StatusForbidden, CodeListForbidden, "may not list its pods"},
		{"pods failed", "pods", errorString("timeout"), http.StatusBadGateway, CodeListFailed, "could not be read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := shop(t)
			h.listErr[tc.resource] = tc.err
			for _, target := range []string{"/x/kind/netinspect/", "/x/kind/netinspect/verdict?from=shop/web&to=shop/api&port=80"} {
				w := serve(t, h, target)
				p := decode[problem](t, w)
				if w.Code != tc.status || p.Code != tc.code || !strings.Contains(p.Message, tc.says) {
					t.Errorf("%s answered %d %+v, want %d %s saying %q", target, w.Code, p, tc.status, tc.code, tc.says)
				}
			}
		})
	}
	t.Run("empty", func(t *testing.T) {
		o := overviewOf(t, shop(t), "")
		if o.Summary.Policies != 0 || o.Summary.Open != 4 || !o.Complete {
			t.Errorf("a cluster with no policy is every pod open: %+v", o.Summary)
		}
	})
	t.Run("unknown cluster", func(t *testing.T) {
		w := serve(t, shop(t), "/x/gone/netinspect/")
		if p := decode[problem](t, w); w.Code != http.StatusNotFound || p.Code != CodeUnknownCluster {
			t.Errorf("answered %d %+v", w.Code, p)
		}
	})
	t.Run("narrowed", func(t *testing.T) {
		h := shop(t)
		h.narrowed["pods"] = true
		o := overviewOf(t, h, "")
		if o.Complete || len(o.Notes) == 0 || !strings.Contains(o.Notes[0], "Pods") {
			t.Errorf("a narrowed listing is partial and says so: %v %v", o.Complete, o.Notes)
		}
	})
}

func daemonSet(name string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: name}}
}

func TestTheNetworkPluginIsRecognisedAsAPresumption(t *testing.T) {
	for _, tc := range []struct {
		sets     []string
		err      error
		name     string
		enforces string
	}{
		{[]string{"kindnet"}, nil, "kindnet", "yes"},
		{[]string{"kube-flannel-ds"}, nil, "flannel", "no"},
		{[]string{"kube-flannel-ds", "calico-node"}, nil, "Calico", "yes"},
		{[]string{"aws-node", "aws-network-policy-agent"}, nil, "Amazon VPC CNI with its network policy agent", "yes"},
		{[]string{"something-else"}, nil, "", "unknown"},
		{nil, fmt.Errorf("list: %w", extension.ErrDenied), "", "unknown"},
	} {
		h := shop(t)
		for _, s := range tc.sets {
			h.add(t, "daemonsets", daemonSet(s))
		}
		h.listErr["daemonsets"] = tc.err
		o := overviewOf(t, h, "")
		if o.CNI.Name != tc.name || o.CNI.Enforces != tc.enforces || o.CNI.Detail == "" {
			t.Errorf("%v: %+v", tc.sets, o.CNI)
		}
	}
	// And the verdict says when nothing enforces it.
	h := shop(t)
	h.add(t, "daemonsets", daemonSet("kube-flannel-ds"))
	if v := verdictOf(t, h, "shop/web", "shop/api", "80"); !strings.Contains(v.Summary, "nothing enforces") {
		t.Errorf("flannel: %q", v.Summary)
	}
}

func TestAKindThatCannotBeReadIsListedAsNotEvaluated(t *testing.T) {
	h := shop(t)
	h.crds = []extension.CRD{
		{Group: "cilium.io", Version: "v2", Plural: "ciliumnetworkpolicies", Kind: "CiliumNetworkPolicy", Namespaced: true},
		{Group: "policy.networking.k8s.io", Version: "v1alpha1", Plural: "adminnetworkpolicies", Kind: "AdminNetworkPolicy"},
	}
	h.objects["ciliumnetworkpolicies"] = nil
	h.listErr["adminnetworkpolicies"] = extension.ErrDenied
	o := overviewOf(t, h, "")
	if len(o.Unevaluated) != 1 || o.Unevaluated[0].Kind != "AdminNetworkPolicy" || o.Unevaluated[0].Count != "" {
		t.Errorf("unevaluated: %+v", o.Unevaluated)
	}
	if o.Complete {
		t.Error("a refused listing makes the read partial")
	}
	if v := verdictOf(t, h, "shop/web", "shop/api", "80"); !strings.Contains(v.Summary, "may be wrong") {
		t.Errorf("a verdict beside unevaluated policies says it may be wrong: %q", v.Summary)
	}
}

func TestSuggestCountsThePolicies(t *testing.T) {
	h := shop(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if s, err := New().Suggest(r, h, "kind"); err != nil || s != "" {
		t.Errorf("no policy: %q %v", s, err)
	}
	h.policy(t, defaultDenyIngress("shop"))
	h.policy(t, apiFromWeb())
	h.policy(t, defaultDenyIngress("ops"))
	if s, err := New().Suggest(r, h, "kind"); err != nil || s != "3 NetworkPolicies in 2 namespaces" {
		t.Errorf("three: %q %v", s, err)
	}
	h.listErr["networkpolicies"] = fmt.Errorf("list: %w", extension.ErrDenied)
	if _, err := New().Suggest(r, h, "kind"); err == nil {
		t.Error("a refused listing is an error, never nothing found")
	}
}

// A peer naming everyone is never folded into "others", however little it is
// connected: the page spreads its rules over every pair it covers.
func TestAnyoneIsNeverFolded(t *testing.T) {
	// Dense namespaces, each far more connected than the one Anyone edge.
	h := teams(t, 120, true)
	h.namespace(t, "zz-open", nil)
	h.pod(t, "zz-open", "app", "10.9.9.1", map[string]string{"app": "app"})
	p := np("zz-open", "open-to-all", nil)
	p.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{}}
	h.policy(t, p)
	g := overviewOf(t, h, "").Graph
	if g.Folded == 0 {
		t.Fatalf("nothing folded: %d nodes", len(g.Nodes))
	}
	found := false
	for _, n := range g.Nodes {
		found = found || n.ID == "anyone"
	}
	if !found {
		t.Errorf("anyone was folded into others (%d nodes, %d folded)", len(g.Nodes), g.Folded)
	}
}

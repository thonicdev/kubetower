package netinspect

import (
	"fmt"
	"sort"
	"strings"
)

// What the page draws, built from one model. Every list is bounded: a
// cluster of any size answers in a few hundred kilobytes.
const (
	// podsPerPage is the pod rows in one answer; the page asks for the next
	// ones with offset.
	podsPerPage = 300
	// maxNodes is the cluster graph's nodes; the least connected beyond it
	// are folded into one "others" node, and more of them when the edges
	// between the rest would not fit in maxEdges (finish). The page draws
	// only the busiest of them and folds the rest itself, so this bounds the
	// answer, not the picture: a namespace kept here can be found and drawn.
	maxNodes = 300
	// maxEdges is the graph's edges, the busiest kept.
	maxEdges = 2_000
	// maxRows caps the namespace and policy tables.
	maxRows = 1_000
	// labelsPerEdge caps the ports and policies an edge names.
	labelsPerEdge = 6
)

// Overview is `GET /`: the cluster, or one namespace.
type Overview struct {
	// Mode is "rules": what the NetworkPolicies allow, never observed
	// traffic. The page draws it as such.
	Mode        string        `json:"mode"`
	Namespace   string        `json:"namespace"`
	Complete    bool          `json:"complete"`
	Notes       []string      `json:"notes"`
	CNI         CNI           `json:"cni"`
	Unevaluated []Unevaluated `json:"unevaluated"`
	Summary     Summary       `json:"summary"`
	Namespaces  []NSRow       `json:"namespaces"`
	// NamespacesTotal is every namespace with a pod or a policy, when the
	// table is capped.
	NamespacesTotal int `json:"namespacesTotal"`
	// Choices is every namespace with a pod or a policy, whatever the
	// scope asked, so the page's menu can move from one namespace to
	// another. Capped at maxChoices.
	Choices       []string    `json:"choices"`
	Policies      []PolicyRow `json:"policies"`
	PoliciesTotal int         `json:"policiesTotal"`
	Pods          PodPage     `json:"pods"`
	Graph         Graph       `json:"graph"`
	// View is one namespace's sides and workloads, under `?ns=` only.
	View *NamespaceView `json:"view,omitempty"`
	// Cilium is what was read of Cilium's policies, evaluated beside the
	// NetworkPolicies.
	Cilium CiliumRead `json:"cilium"`
	// Calico and Admin are what was read of Calico's policies and of the
	// AdminNetworkPolicies, evaluated with them.
	Calico PolicyRead `json:"calico"`
	Admin  PolicyRead `json:"admin"`
}

// Summary is the tiles' counts, over the scope (the cluster or one
// namespace). HostNetwork pods are counted apart and in none of the three
// isolation counts.
type Summary struct {
	Pods        int `json:"pods"`
	IsolatedIn  int `json:"isolatedIn"`
	IsolatedOut int `json:"isolatedOut"`
	Open        int `json:"open"`
	HostNetwork int `json:"hostNetwork"`
	Policies    int `json:"policies"`
	// DefaultDeny is the namespaces where a policy selects every pod and
	// allows nothing in a direction.
	DefaultDeny int `json:"defaultDeny"`
	// Idle is the policies whose podSelector matches no pod.
	Idle int `json:"idle"`
}

// NSRow is one namespace.
type NSRow struct {
	Name           string `json:"name"`
	Pods           int    `json:"pods"`
	IsolatedIn     int    `json:"isolatedIn"`
	IsolatedOut    int    `json:"isolatedOut"`
	Policies       int    `json:"policies"`
	DefaultDenyIn  bool   `json:"defaultDenyIn"`
	DefaultDenyOut bool   `json:"defaultDenyOut"`
}

// PolicyRow is one policy.
type PolicyRow struct {
	// Kind is NetworkPolicy, CiliumNetworkPolicy, CiliumClusterwideNetworkPolicy, Calico NetworkPolicy, Calico
	// GlobalNetworkPolicy, AdminNetworkPolicy or BaselineAdminNetworkPolicy.
	Kind      string   `json:"kind"`
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Selector  string   `json:"selector"`
	Types     []string `json:"types"`
	Selects   int      `json:"selects"`
	Ingress   []string `json:"ingress"`
	Egress    []string `json:"egress"`
	Broken    []string `json:"broken"`
}

// PodPage is one page of pods.
type PodPage struct {
	Total  int      `json:"total"`
	Offset int      `json:"offset"`
	Rows   []PodRow `json:"rows"`
}

// PodRow is one pod.
type PodRow struct {
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	Workload    string `json:"workload"`
	IP          string `json:"ip"`
	In          bool   `json:"in"`
	Out         bool   `json:"out"`
	HostNetwork bool   `json:"hostNetwork"`
	Policies    int    `json:"policies"`
}

// Graph is the rules drawn: who a rule lets talk to whom. An edge exists
// only where a rule speaks; a node no policy isolates is drawn open, not
// joined to everything.
type Graph struct {
	Scope string `json:"scope"`
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
	// Folded is how many nodes went into "others".
	Folded int `json:"folded"`
	// EdgesTotal is every edge before the nodes were folded.
	EdgesTotal int `json:"edgesTotal"`
	// Truncated says the edges between the nodes kept did not all fit and
	// some were left out: the page then draws no verdict on a pair with no
	// edge. Folding normally makes them fit, so this is the last resort.
	Truncated bool `json:"truncated"`
}

// everyone are the nodes that name every pod: a rule's empty peer, and
// Cilium's all and cluster entities.
var everyone = map[string]bool{"anyone": true, "entity:all": true, "entity:cluster": true}

// Node kinds.
const (
	nodeNamespace = "namespace"
	nodeCIDR      = "cidr"
	nodeAnyone    = "anyone"
	nodeOthers    = "others"
	// nodeEntity is a Cilium entity (world, cluster, kube-apiserver…).
	nodeEntity = "entity"
	// nodeUnevaluated stands for what a rule names that is not evaluated:
	// FQDNs, Services, layer-7 rules.
	nodeUnevaluated = "unevaluated"
)

// Node is one vertex.
type Node struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Kind        string `json:"kind"`
	Namespace   string `json:"namespace,omitempty"`
	Pods        int    `json:"pods"`
	IsolatedIn  int    `json:"isolatedIn"`
	IsolatedOut int    `json:"isolatedOut"`
	// Internal is rules whose both ends fall inside this node (a namespace
	// allowing its own pods), drawn as a count rather than a loop.
	Internal int `json:"internal"`
	// Ends is, on "others", how many of the nodes it holds are not
	// namespaces (a CIDR, world, an entity): such an end never refuses, so
	// "others" cannot be called isolated when it holds one.
	Ends int `json:"ends,omitempty"`
}

// Edge is "a rule lets From reach To". Side says which end's policy says so:
// "ingress" (the destination's), "egress" (the source's) or "both".
type Edge struct {
	// Kind is "allow", "deny" or "unevaluated": a deny rule, or one this
	// engine cannot decide, is drawn apart and never as an allow.
	Kind     string   `json:"kind"`
	From     string   `json:"from"`
	To       string   `json:"to"`
	Side     string   `json:"side"`
	Ports    []string `json:"ports"`
	Policies []string `json:"policies"`
	Rules    int      `json:"rules"`
}

// show filters the pod rows: the tiles' keys.
const (
	showIsolatedIn  = "in"
	showIsolatedOut = "out"
	showOpen        = "open"
	showHostNetwork = "host"
)

// overview builds `GET /` for namespace ("" for the cluster), with pod rows
// filtered by show and search, from offset.
func (rd *read) overview(namespace, show, search string, offset int) Overview {
	m := rd.model
	o := Overview{
		Mode: "rules", Namespace: namespace, Complete: rd.complete, Notes: nonNil(rd.notes),
		CNI: rd.cni, Unevaluated: rd.unevaluate, Cilium: rd.cilium, Calico: rd.calico, Admin: rd.admin,
		Namespaces: []NSRow{}, Policies: []PolicyRow{},
	}
	if o.Unevaluated == nil {
		o.Unevaluated = []Unevaluated{}
	}
	in := func(ns string) bool { return namespace == "" || ns == namespace }

	rows := map[string]*NSRow{}
	row := func(ns string) *NSRow {
		r, ok := rows[ns]
		if !ok {
			r = &NSRow{Name: ns}
			rows[ns] = r
		}
		return r
	}
	for i := range m.Pods {
		p := &m.Pods[i]
		r := row(p.Namespace)
		r.Pods++
		ident := &m.Identities[p.identity]
		if p.HostNetwork {
			if in(p.Namespace) {
				o.Summary.HostNetwork++
				o.Summary.Pods++
			}
			continue
		}
		if len(ident.In) > 0 {
			r.IsolatedIn++
		}
		if len(ident.Out) > 0 {
			r.IsolatedOut++
		}
		if !in(p.Namespace) {
			continue
		}
		o.Summary.Pods++
		switch {
		case len(ident.In) > 0 && len(ident.Out) > 0:
			o.Summary.IsolatedIn++
			o.Summary.IsolatedOut++
		case len(ident.In) > 0:
			o.Summary.IsolatedIn++
		case len(ident.Out) > 0:
			o.Summary.IsolatedOut++
		default:
			o.Summary.Open++
		}
	}
	for pi := range m.Policies {
		p := &m.Policies[pi]
		if p.clusterwide {
			// A clusterwide policy has no namespace of its own: it counts in
			// the cluster, and in a namespace whose pods it selects.
			if namespace != "" && !m.selectsIn(p, namespace) {
				continue
			}
		} else {
			r := row(p.Namespace)
			r.Policies++
			if p.Selector == "" && p.selector != nil {
				if p.Ingress && len(p.In) == 0 {
					r.DefaultDenyIn = true
				}
				if p.Egress && len(p.Out) == 0 {
					r.DefaultDenyOut = true
				}
			}
			if !in(p.Namespace) {
				continue
			}
		}
		o.Summary.Policies++
		selects := 0
		for _, id := range p.Selected {
			selects += len(m.Identities[id].Pods)
		}
		if selects == 0 {
			o.Summary.Idle++
		}
		o.PoliciesTotal++
		if len(o.Policies) < maxRows {
			o.Policies = append(o.Policies, policyRow(p, selects))
		}
	}
	o.Choices = make([]string, 0, min(len(rows), maxChoices))
	for name := range rows {
		o.Choices = append(o.Choices, name)
	}
	sort.Strings(o.Choices)
	if len(o.Choices) > maxChoices {
		o.Choices = o.Choices[:maxChoices]
	}
	names := make([]string, 0, len(rows))
	for name, r := range rows {
		if !in(name) {
			continue
		}
		if r.DefaultDenyIn || r.DefaultDenyOut {
			o.Summary.DefaultDeny++
		}
		names = append(names, name)
	}
	sort.Strings(names)
	o.NamespacesTotal = len(names)
	for _, name := range names {
		if len(o.Namespaces) == maxRows {
			break
		}
		o.Namespaces = append(o.Namespaces, *rows[name])
	}
	sort.SliceStable(o.Policies, func(i, j int) bool {
		a, b := o.Policies[i], o.Policies[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})

	o.Pods = m.podPage(in, show, search, offset)
	if namespace == "" {
		o.Graph = m.clusterGraph(rows)
	} else {
		// One namespace is drawn from its view; the graph stays empty.
		o.Graph = Graph{Scope: "namespace", Nodes: []Node{}, Edges: []Edge{}}
		view := m.namespaceView(namespace)
		o.View = &view
	}
	return o
}

// maxChoices bounds the namespace menu's answer.
const maxChoices = 2000

func policyRow(p *policy, selects int) PolicyRow {
	row := PolicyRow{Kind: p.Kind, Namespace: p.Namespace, Name: p.Name, Selector: p.Selector, Selects: selects,
		Types: []string{}, Ingress: []string{}, Egress: []string{}, Broken: nonNil(p.Broken)}
	if p.Ingress {
		row.Types = append(row.Types, "Ingress")
	}
	if p.Egress {
		row.Types = append(row.Types, "Egress")
	}
	for i := range p.In {
		row.Ingress = append(row.Ingress, "from "+ruleText(&p.In[i]))
	}
	for i := range p.Out {
		row.Egress = append(row.Egress, "to "+ruleText(&p.Out[i]))
	}
	return row
}

// podPage is the pods in scope that show and search keep, sorted, one page.
func (m *model) podPage(in func(string) bool, show, search string, offset int) PodPage {
	search = strings.ToLower(strings.TrimSpace(search))
	keep := make([]int, 0, 64)
	for i := range m.Pods {
		p := &m.Pods[i]
		if !in(p.Namespace) {
			continue
		}
		ident := &m.Identities[p.identity]
		switch show {
		case showIsolatedIn:
			if p.HostNetwork || len(ident.In) == 0 {
				continue
			}
		case showIsolatedOut:
			if p.HostNetwork || len(ident.Out) == 0 {
				continue
			}
		case showOpen:
			if p.HostNetwork || len(ident.In) > 0 || len(ident.Out) > 0 {
				continue
			}
		case showHostNetwork:
			if !p.HostNetwork {
				continue
			}
		}
		if search != "" && !strings.Contains(strings.ToLower(p.Namespace+"/"+p.Name+" "+p.Workload), search) {
			continue
		}
		keep = append(keep, i)
	}
	sort.Slice(keep, func(a, b int) bool {
		pa, pb := &m.Pods[keep[a]], &m.Pods[keep[b]]
		if pa.Namespace != pb.Namespace {
			return pa.Namespace < pb.Namespace
		}
		return pa.Name < pb.Name
	})
	page := PodPage{Total: len(keep), Offset: offset, Rows: []PodRow{}}
	if offset < 0 || offset > len(keep) {
		page.Offset = 0
	}
	for _, i := range keep[page.Offset:min(len(keep), page.Offset+podsPerPage)] {
		page.Rows = append(page.Rows, m.podRow(i))
	}
	return page
}

func (m *model) podRow(i int) PodRow {
	p := &m.Pods[i]
	ident := &m.Identities[p.identity]
	seen := map[int]bool{}
	for _, pi := range ident.In {
		seen[pi] = true
	}
	for _, pi := range ident.Out {
		seen[pi] = true
	}
	for _, pi := range append(append([]int{}, ident.InAlso...), ident.OutAlso...) {
		seen[pi] = true
	}
	return PodRow{Namespace: p.Namespace, Name: p.Name, Workload: p.Workload, IP: p.IP,
		In: !p.HostNetwork && len(ident.In) > 0, Out: !p.HostNetwork && len(ident.Out) > 0,
		HostNetwork: p.HostNetwork, Policies: len(seen)}
}

// --- the graph ------------------------------------------------------------------

// edgeAcc gathers one edge's rules before it is drawn.
type edgeAcc struct {
	kind      string
	from, to  string
	ingress   bool
	egress    bool
	ports     map[string]bool
	policies  map[string]bool
	rules     int
	weightKey string
}

type graphBuilder struct {
	nodes map[string]*Node
	edges map[string]*edgeAcc
}

func newGraphBuilder() *graphBuilder {
	return &graphBuilder{nodes: map[string]*Node{}, edges: map[string]*edgeAcc{}}
}

func (g *graphBuilder) node(id, label, kind, namespace string) *Node {
	n, ok := g.nodes[id]
	if !ok {
		n = &Node{ID: id, Label: label, Kind: kind, Namespace: namespace}
		g.nodes[id] = n
	}
	return n
}

func (g *graphBuilder) edge(from, to, side string, r *rule, policyName string) {
	if from == to {
		if n := g.nodes[from]; n != nil {
			n.Internal++
		}
		return
	}
	kind := ruleKind(r)
	key := kind + "\x00" + from + "\x00" + to
	e, ok := g.edges[key]
	if !ok {
		e = &edgeAcc{kind: kind, from: from, to: to, ports: map[string]bool{}, policies: map[string]bool{}}
		g.edges[key] = e
	}
	if side == "ingress" {
		e.ingress = true
	} else {
		e.egress = true
	}
	e.rules++
	if len(e.policies) < labelsPerEdge {
		e.policies[policyName] = true
	}
	if r.AnyPort {
		e.ports["every port"] = true
	}
	for _, p := range r.Ports {
		if len(e.ports) >= labelsPerEdge {
			break
		}
		e.ports[p.String()] = true
	}
}

// ruleEdges adds every edge one policy's rules draw. nodeOf names the node
// an identity is drawn as ("" leaves it out of this graph).
func (m *model) ruleEdges(g *graphBuilder, p *policy, nodeOf func(id int) string) {
	targets := map[string]bool{}
	for _, id := range p.Selected {
		if n := nodeOf(id); n != "" {
			targets[n] = true
		}
	}
	if len(targets) == 0 {
		return
	}
	draw := func(r *rule, side string) {
		if r.Action == "Pass" {
			// A Pass hands the flow to the next layer: it allows nothing.
			return
		}
		peers := map[string]bool{}
		if r.Everyone {
			g.node("anyone", "Anyone", nodeAnyone, "")
			peers["anyone"] = true
		}
		if len(r.Opaque) > 0 {
			id := "unevaluated:" + strings.Join(r.Opaque, ",")
			g.node(id, strings.Join(r.Opaque, ", "), nodeUnevaluated, "")
			peers[id] = true
		}
		for i := range r.Peers {
			pe := &r.Peers[i]
			if pe.Entity != "" {
				id := "entity:" + pe.Entity
				g.node(id, pe.Entity, nodeEntity, "")
				peers[id] = true
				continue
			}
			if !pe.Pods {
				id := "cidr:" + pe.Label()
				g.node(id, pe.Label(), nodeCIDR, "")
				peers[id] = true
				continue
			}
			m.eachPeerIdentity(pe, p.Namespace, func(ident int) {
				if n := nodeOf(ident); n != "" {
					peers[n] = true
				}
			})
		}
		for t := range targets {
			for peerNode := range peers {
				if side == "ingress" {
					g.edge(peerNode, t, side, r, p.Name)
				} else {
					g.edge(t, peerNode, side, r, p.Name)
				}
			}
		}
	}
	for i := range p.In {
		draw(&p.In[i], "ingress")
	}
	for i := range p.Out {
		draw(&p.Out[i], "egress")
	}
}

// clusterGraph is namespaces joined by the rules between them.
func (m *model) clusterGraph(rows map[string]*NSRow) Graph {
	g := newGraphBuilder()
	for name, r := range rows {
		n := g.node("ns:"+name, name, nodeNamespace, name)
		n.Pods, n.IsolatedIn, n.IsolatedOut = r.Pods, r.IsolatedIn, r.IsolatedOut
	}
	// Each identity's node, named once: the graph asks for it per rule.
	names := make([]string, len(m.Identities))
	for id := range m.Identities {
		names[id] = "ns:" + m.Identities[id].Namespace
	}
	nodeOf := func(id int) string { return names[id] }
	for pi := range m.Policies {
		m.ruleEdges(g, &m.Policies[pi], nodeOf)
	}
	return g.finish("cluster", "")
}

// finish caps the graph: the most connected nodes, the rest folded into
// "others", and **every edge between what is kept**. Where the edges of the
// first maxNodes nodes would not fit in maxEdges, fewer nodes are kept until
// they do: an edge left out would read, on the page, as a pair no rule
// allows. Only if even two nodes and "others" carried more than maxEdges is
// the list cut, and Truncated says so. Sorted, so the same cluster draws the
// same picture.
func (g *graphBuilder) finish(scope, focus string) Graph {
	degree := map[string]int{}
	for _, e := range g.edges {
		degree[e.from] += e.rules
		degree[e.to] += e.rules
	}
	ids := make([]string, 0, len(g.nodes))
	for id := range g.nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := g.nodes[ids[i]], g.nodes[ids[j]]
		// A peer naming everyone is never folded: its rules speak for every
		// pair it covers, which the page can only draw if it can tell them
		// from the rules of the namespaces folded beside it.
		if ei, ej := everyone[ids[i]], everyone[ids[j]]; ei != ej {
			return ei
		}
		if degree[ids[i]] != degree[ids[j]] {
			return degree[ids[i]] > degree[ids[j]]
		}
		if a.Pods != b.Pods {
			return a.Pods > b.Pods
		}
		return ids[i] < ids[j]
	})
	// The most nodes whose merged edges fit, found by halving: folding one
	// more node into "others" never adds an edge, so the count only falls
	// as fewer are kept.
	keep := min(len(ids), maxNodes)
	if !g.fits(ids, keep) {
		lo, hi := 2, keep // fits(lo) is assumed; hi does not fit
		for lo+1 < hi {
			mid := (lo + hi) / 2
			if g.fits(ids, mid) {
				lo = mid
			} else {
				hi = mid
			}
		}
		keep = lo
	}
	out := g.foldAt(scope, ids, keep)
	if len(out.Edges) > maxEdges {
		out.Edges = out.Edges[:maxEdges]
		out.Truncated = true
	}
	return out
}

// fits says whether keeping the first keep of ids leaves at most maxEdges
// merged edges. It only counts, and stops as soon as the count is over.
func (g *graphBuilder) fits(ids []string, keep int) bool {
	if len(ids) <= keep {
		keep = len(ids) + 1 // nothing folded
	}
	rank := make(map[string]int, len(ids))
	for i, id := range ids {
		if i < keep-1 {
			rank[id] = i
		} else {
			rank[id] = len(ids) // "others"
		}
	}
	kinds := map[string]uint64{}
	seen := make(map[uint64]struct{}, maxEdges+1)
	for _, e := range g.edges {
		from, to := rank[e.from], rank[e.to]
		if from == to {
			continue
		}
		k, ok := kinds[e.kind]
		if !ok {
			k = uint64(len(kinds))
			kinds[e.kind] = k
		}
		seen[k<<42|uint64(from)<<21|uint64(to)] = struct{}{}
		if len(seen) > maxEdges {
			return false
		}
	}
	return true
}

// foldAt is the graph keeping the first keep of ids (already in order) and
// folding the rest into "others", with every edge between what is left,
// merged, busiest first.
func (g *graphBuilder) foldAt(scope string, ids []string, keep int) Graph {
	out := Graph{Scope: scope, Nodes: []Node{}, Edges: []Edge{}, EdgesTotal: len(g.edges)}
	kept := map[string]string{}
	others := &Node{ID: "others", Kind: nodeOthers}
	for i, id := range ids {
		if i < keep-1 || len(ids) <= keep {
			kept[id] = id
			out.Nodes = append(out.Nodes, *g.nodes[id])
			continue
		}
		kept[id] = others.ID
		n := g.nodes[id]
		if n.Kind != nodeNamespace {
			others.Ends++
		}
		others.Pods += n.Pods
		others.IsolatedIn += n.IsolatedIn
		others.IsolatedOut += n.IsolatedOut
		out.Folded++
	}
	if out.Folded > 0 {
		others.Label = fmt.Sprintf("%d others", out.Folded)
		out.Nodes = append(out.Nodes, *others)
	}
	merged := map[string]*edgeAcc{}
	for _, e := range g.edges {
		from, to := kept[e.from], kept[e.to]
		if from == to {
			continue
		}
		key := e.kind + "\x00" + from + "\x00" + to
		m, ok := merged[key]
		if !ok {
			m = &edgeAcc{kind: e.kind, from: from, to: to, ports: map[string]bool{}, policies: map[string]bool{}}
			merged[key] = m
		}
		m.ingress = m.ingress || e.ingress
		m.egress = m.egress || e.egress
		m.rules += e.rules
		for p := range e.ports {
			if len(m.ports) < labelsPerEdge {
				m.ports[p] = true
			}
		}
		for p := range e.policies {
			if len(m.policies) < labelsPerEdge {
				m.policies[p] = true
			}
		}
	}
	for _, e := range merged {
		side := "ingress"
		switch {
		case e.ingress && e.egress:
			side = "both"
		case e.egress:
			side = "egress"
		}
		out.Edges = append(out.Edges, Edge{Kind: e.kind, From: e.from, To: e.to, Side: side, Ports: sortedKeys(e.ports),
			Policies: sortedKeys(e.policies), Rules: e.rules})
	}
	sort.Slice(out.Edges, func(i, j int) bool {
		a, b := out.Edges[i], out.Edges[j]
		if a.Rules != b.Rules {
			return a.Rules > b.Rules
		}
		if a.From != b.From {
			return a.From < b.From
		}
		return a.To < b.To
	})
	return out
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// selectsIn is whether a policy selects a pod of namespace.
func (m *model) selectsIn(p *policy, namespace string) bool {
	for _, id := range p.Selected {
		if m.Identities[id].Namespace == namespace {
			return true
		}
	}
	return false
}

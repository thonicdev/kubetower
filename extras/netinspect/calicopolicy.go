package netinspect

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

// Calico's own policies, NetworkPolicy and GlobalNetworkPolicy, read from
// crd.projectcalico.org/v1 (the storage of projectcalico.org/v3, whose spec
// they hold), evaluated with Calico's tiers:
//
//   - tiers run in order of their spec.order; inside a tier, policies by order
//     (absent is after every number), then name. A policy names its tier in
//     spec.tier (empty is `default`); the object name of a policy outside the
//     default tier is prefixed `<tier>.`;
//   - the first rule that matches decides: Allow lets the flow through, Deny
//     refuses it, Pass leaves the tier for the next one, Log does nothing and
//     the next rule is tried;
//   - a tier where a policy selected the pod and no rule matched applies the
//     tier's defaultAction: Deny (the default) refuses, Pass goes on;
//   - a pod no policy selects, or that every tier passed on, meets Calico's
//     default profile, which allows;
//   - Kubernetes NetworkPolicies are policies of the `default` tier at order
//     1000: they are evaluated beside the Calico policies there, not apart.
//
// A policy selects a pod by spec.selector over the labels Calico gives the
// endpoint (calicoLabels); a namespaced policy selects in its own namespace
// only. A rule entity is an AND of its selector, notSelector, namespaceSelector,
// serviceAccounts, nets and notNets, and ports and notPorts on the
// destination. What is not evaluated is named and answered "not evaluated":
// NetworkSets (a selector that could be satisfied by one, when the peer is an
// address outside every pod), http, icmp, services, ipVersion, source ports,
// a serviceAccountSelector, and host endpoint policies (preDNAT, doNotTrack,
// applyOnForward). An unreadable selector makes the policy, or the rule,
// not evaluated.

const (
	// calicoDefaultTier is where NetworkPolicies and a policy with no tier live.
	calicoDefaultTier = "default"
	// calicoDefaultTierOrder is the default tier's order in Calico 3.x.
	calicoDefaultTierOrder = 1_000_000.0
	// calicoNPOrder is the order Calico gives a Kubernetes NetworkPolicy.
	calicoNPOrder = 1000.0
)

const (
	calicoNSLabel = "projectcalico.org/namespace"
	calicoSALabel = "projectcalico.org/serviceaccount"
	calicoOrch    = "projectcalico.org/orchestrator"
	calicoNSPref  = "pcns."
)

// calicoLabels is an identity's labels as Calico matches them.
func calicoLabels(ident *identity, nsLabels labels.Set) labels.Set {
	out := make(labels.Set, len(ident.Labels)+len(nsLabels)+3)
	for k, v := range ident.Labels {
		out[k] = v
	}
	for k, v := range nsLabels {
		out[calicoNSPref+k] = v
	}
	out[calicoNSLabel] = ident.Namespace
	out[calicoOrch] = "k8s"
	if ident.ServiceAccount != "" {
		out[calicoSALabel] = ident.ServiceAccount
	}
	return out
}

// calicoTier is one tier.
type calicoTier struct {
	Name          string
	Order         float64
	DefaultAction string
}

func tierOrder(t map[string]calicoTier, name string) float64 {
	if tier, ok := t[name]; ok {
		return tier.Order
	}
	if name == calicoDefaultTier {
		return calicoDefaultTierOrder
	}
	return math.Inf(1)
}

// --- compiled rules -------------------------------------------------------------

type calicoEntity struct {
	nets, notNets []*net.IPNet
	sel, notSel   calicoSel
	nsSel         calicoSel
	saNames       []string
	// scopeNS restricts the entity to a namespace: a namespaced policy's rule
	// that names a selector and no namespaceSelector.
	scopeNS string
	// unknown: a part of the entity is not evaluated (a serviceAccounts
	// selector).
	unknown bool
	// srcPorts: ports on the source side, which a flow's unknown source port
	// cannot answer.
	srcPorts bool
	// byLabels is true when the entity designates endpoints by labels, not
	// only by address.
	byLabels bool
}

type calicoRule struct {
	proto, notProto []string
	src, dst        calicoEntity
	notPorts        []portSpec
	// icmp: the rule is about ICMP and never matches a TCP, UDP or SCTP flow.
	icmp bool
}

type calRuleJ struct {
	Action      string          `json:"action"`
	Protocol    json.RawMessage `json:"protocol"`
	NotProtocol json.RawMessage `json:"notProtocol"`
	ICMP        json.RawMessage `json:"icmp"`
	NotICMP     json.RawMessage `json:"notICMP"`
	IPVersion   json.RawMessage `json:"ipVersion"`
	HTTP        json.RawMessage `json:"http"`
	Source      calEntityJ      `json:"source"`
	Destination calEntityJ      `json:"destination"`
}

type calEntityJ struct {
	Nets              []string          `json:"nets"`
	NotNets           []string          `json:"notNets"`
	Selector          string            `json:"selector"`
	NotSelector       string            `json:"notSelector"`
	NamespaceSelector string            `json:"namespaceSelector"`
	Ports             []json.RawMessage `json:"ports"`
	NotPorts          []json.RawMessage `json:"notPorts"`
	ServiceAccounts   *struct {
		Names    []string `json:"names"`
		Selector string   `json:"selector"`
	} `json:"serviceAccounts"`
	Services json.RawMessage `json:"services"`
}

type calSpecJ struct {
	Tier                   string            `json:"tier"`
	Order                  *float64          `json:"order"`
	Selector               string            `json:"selector"`
	NamespaceSelector      string            `json:"namespaceSelector"`
	ServiceAccountSelector string            `json:"serviceAccountSelector"`
	Types                  []string          `json:"types"`
	Ingress                []json.RawMessage `json:"ingress"`
	Egress                 []json.RawMessage `json:"egress"`
	DoNotTrack             bool              `json:"doNotTrack"`
	PreDNAT                bool              `json:"preDNAT"`
	ApplyOnForward         bool              `json:"applyOnForward"`
}

// compileCalico turns one object into a policy. skipped counts what it left
// out, by reason, for the page's "not evaluated" line.
func compileCalico(u *unstructured.Unstructured, global bool, skipped map[string]int) policy {
	kind := "Calico NetworkPolicy"
	if global {
		kind = "Calico GlobalNetworkPolicy"
	}
	p := policy{Kind: kind, Name: u.GetName(), family: "calico", clusterwide: global, order: math.Inf(1), tier: calicoDefaultTier}
	if !global {
		p.Namespace = u.GetNamespace()
	}
	var spec calSpecJ
	data, err := json.Marshal(u.Object["spec"])
	if err == nil {
		err = json.Unmarshal(data, &spec)
	}
	if err != nil {
		p.Broken = append(p.Broken, "its spec: "+err.Error())
		p.unreadable = true
		return p
	}
	if spec.Tier != "" {
		p.tier = spec.Tier
	}
	if spec.Order != nil {
		p.order = *spec.Order
	}
	p.Selector = strings.TrimSpace(spec.Selector)
	if p.Selector == "all()" {
		p.Selector = ""
	}
	p.calSel, err = parseCalicoSelector(spec.Selector)
	if err != nil {
		p.Broken = append(p.Broken, "its selector: "+err.Error())
		p.unreadable = true
	}
	if global && strings.TrimSpace(spec.NamespaceSelector) != "" {
		p.calNS, err = parseCalicoSelector(spec.NamespaceSelector)
		if err != nil {
			p.Broken = append(p.Broken, "its namespaceSelector: "+err.Error())
			p.unreadable = true
		}
		p.Selector += " in namespaces " + strings.TrimSpace(spec.NamespaceSelector)
		p.Selector = strings.TrimSpace(p.Selector)
	}
	if spec.ServiceAccountSelector != "" || spec.PreDNAT || spec.DoNotTrack || spec.ApplyOnForward {
		// A policy about a service account's labels, or about the host's
		// traffic: not about the pods' flows as evaluated here.
		why := "serviceAccountSelector"
		if spec.PreDNAT || spec.DoNotTrack || spec.ApplyOnForward {
			why = "host endpoint policy"
		}
		p.Broken = append(p.Broken, why+": not evaluated")
		skipped[why]++
		p.unreadable = true
	}
	types := spec.Types
	if len(types) == 0 {
		p.Ingress = true
		p.Egress = len(spec.Egress) > 0
	}
	for _, t := range types {
		switch strings.ToLower(t) {
		case "ingress":
			p.Ingress = true
		case "egress":
			p.Egress = true
		}
	}
	scope := ""
	if !global {
		scope = p.Namespace
	}
	if p.Ingress {
		for i, raw := range spec.Ingress {
			if r, ok := compileCalicoRule(&p, i, raw, true, scope, skipped); ok {
				p.In = append(p.In, r)
			}
		}
	}
	if p.Egress {
		for i, raw := range spec.Egress {
			if r, ok := compileCalicoRule(&p, i, raw, false, scope, skipped); ok {
				p.Out = append(p.Out, r)
			}
		}
	}
	return p
}

// compileCalicoRule compiles one rule; ok is false for a Log rule, which does
// nothing.
func compileCalicoRule(p *policy, index int, raw json.RawMessage, ingress bool, scope string, skipped map[string]int) (rule, bool) {
	r := rule{Index: index, AnyPort: true}
	var j calRuleJ
	if err := json.Unmarshal(raw, &j); err != nil {
		p.Broken = append(p.Broken, fmt.Sprintf("rule %d: %v", index+1, err))
		r.Opaque = append(r.Opaque, "an unreadable rule")
		r.Everyone = true
		return r, true
	}
	switch strings.ToLower(j.Action) {
	case "allow":
		r.Action = "Allow"
	case "deny":
		r.Action, r.Deny = "Deny", true
	case "pass":
		r.Action = "Pass"
	case "log":
		return r, false
	default:
		p.Broken = append(p.Broken, fmt.Sprintf("rule %d: the action %q", index+1, j.Action))
		r.Opaque = append(r.Opaque, "an unknown action")
	}
	c := &calicoRule{}
	r.cal = c
	opaque := func(why string) {
		r.Opaque = append(r.Opaque, why)
		skipped[why]++
	}
	var err error
	if c.proto, err = calProtocols(j.Protocol); err != nil {
		opaque("protocol")
	}
	if c.notProto, err = calProtocols(j.NotProtocol); err != nil {
		opaque("notProtocol")
	}
	for _, x := range c.proto {
		if x == "ICMP" || x == "ICMPV6" {
			c.icmp = true
		}
	}
	if !c.icmp && (len(j.ICMP) > 0 || len(j.NotICMP) > 0) {
		opaque("icmp")
	}
	if len(j.IPVersion) > 0 {
		opaque("ipVersion")
	}
	if len(j.HTTP) > 0 && string(j.HTTP) != "null" {
		opaque("http")
	}
	var ports []portSpec
	c.src, _, err = compileCalicoEntity(p, index, j.Source, scope, opaque)
	if err != nil {
		opaque("a source")
	}
	c.dst, ports, err = compileCalicoEntity(p, index, j.Destination, scope, opaque)
	if err != nil {
		opaque("a destination")
	}
	c.notPorts = nil
	if len(j.Destination.NotPorts) > 0 {
		var np []portSpec
		np, err = calPorts(j.Destination.NotPorts)
		if err != nil {
			opaque("notPorts")
		}
		c.notPorts = np
	}
	if len(j.Source.Ports) > 0 || len(j.Source.NotPorts) > 0 {
		c.src.srcPorts = true
		opaque("source ports")
	}
	r.Ports, r.AnyPort = ports, len(ports) == 0
	sort.Strings(r.Opaque)
	// For the page and the graph: the other end of the flow.
	other := &c.src
	if !ingress {
		other = &c.dst
	}
	r.Everyone = !other.byLabels && len(other.nets) == 0 && len(other.notNets) == 0 && len(r.Opaque) == 0
	if other.byLabels {
		r.Peers = append(r.Peers, peer{Pods: true, PodText: otherText(other, j, ingress), custom: func(m *model, id int) bool {
			return other.matchesIdentity(m, id) != matchNo
		}})
	}
	for _, n := range other.nets {
		r.Peers = append(r.Peers, peer{CIDR: n.String(), block: n})
	}
	return r, true
}

func otherText(e *calicoEntity, j calRuleJ, ingress bool) string {
	ent := j.Destination
	if ingress {
		ent = j.Source
	}
	var parts []string
	if s := strings.TrimSpace(ent.Selector); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(ent.NamespaceSelector); s != "" {
		parts = append(parts, "namespaces "+s)
	}
	if ent.ServiceAccounts != nil && len(ent.ServiceAccounts.Names) > 0 {
		parts = append(parts, "service accounts "+strings.Join(ent.ServiceAccounts.Names, ","))
	}
	if len(parts) == 0 {
		return "endpoints"
	}
	return "endpoints " + strings.Join(parts, " and ")
}

func compileCalicoEntity(p *policy, index int, j calEntityJ, scope string, opaque func(string)) (calicoEntity, []portSpec, error) {
	var e calicoEntity
	var err error
	for _, text := range j.Nets {
		n, perr := parseCalicoNet(text)
		if perr != nil {
			p.Broken = append(p.Broken, fmt.Sprintf("rule %d: the net %q", index+1, text))
			e.unknown = true
			continue
		}
		e.nets = append(e.nets, n)
	}
	for _, text := range j.NotNets {
		n, perr := parseCalicoNet(text)
		if perr != nil {
			p.Broken = append(p.Broken, fmt.Sprintf("rule %d: the net %q", index+1, text))
			e.unknown = true
			continue
		}
		e.notNets = append(e.notNets, n)
	}
	parse := func(text, what string) calicoSel {
		sel, perr := parseCalicoSelector(text)
		if perr != nil {
			p.Broken = append(p.Broken, fmt.Sprintf("rule %d: its %s: %v", index+1, what, perr))
			e.unknown = true
			return nil
		}
		return sel
	}
	hasSel := strings.TrimSpace(j.Selector) != ""
	if hasSel {
		e.sel = parse(j.Selector, "selector")
	}
	if strings.TrimSpace(j.NotSelector) != "" {
		e.notSel = parse(j.NotSelector, "notSelector")
	}
	if strings.TrimSpace(j.NamespaceSelector) != "" {
		e.nsSel = parse(j.NamespaceSelector, "namespaceSelector")
	}
	if sa := j.ServiceAccounts; sa != nil {
		e.saNames = sa.Names
		if strings.TrimSpace(sa.Selector) != "" {
			e.unknown = true
			opaque("serviceAccounts selector")
		}
	}
	if len(j.Services) > 0 && string(j.Services) != "null" {
		e.unknown = true
		opaque("services")
	}
	if scope != "" && e.nsSel == nil && (hasSel || len(e.saNames) > 0) {
		e.scopeNS = scope
	}
	e.byLabels = hasSel || e.notSel != nil || e.nsSel != nil || len(e.saNames) > 0 || e.unknown
	var ports []portSpec
	if len(j.Ports) > 0 {
		ports, err = calPorts(j.Ports)
		if err != nil {
			opaque("ports")
		}
	}
	return e, ports, nil
}

func parseCalicoNet(text string) (*net.IPNet, error) {
	if !strings.Contains(text, "/") {
		ip := net.ParseIP(text)
		if ip == nil {
			return nil, fmt.Errorf("not an address")
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		text += "/" + strconv.Itoa(bits)
	}
	_, n, err := net.ParseCIDR(text)
	return n, err
}

// calProtocols reads a protocol field: a name or a number, as a string or a
// number; normalized to TCP, UDP, SCTP, ICMP, ICMPV6 or the number's text.
func calProtocols(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		var n int
		if err2 := json.Unmarshal(raw, &n); err2 != nil {
			return nil, err
		}
		s = strconv.Itoa(n)
	}
	switch strings.ToUpper(s) {
	case "TCP", "6":
		return []string{"TCP"}, nil
	case "UDP", "17":
		return []string{"UDP"}, nil
	case "SCTP", "132":
		return []string{"SCTP"}, nil
	case "ICMP", "1":
		return []string{"ICMP"}, nil
	case "ICMPV6", "58":
		return []string{"ICMPV6"}, nil
	}
	return []string{strings.ToUpper(s)}, nil
}

// calPorts reads ports: numbers, "a:b" ranges and names. The protocol is the
// rule's, so a port here is protocol ANY.
func calPorts(raws []json.RawMessage) ([]portSpec, error) {
	var out []portSpec
	for _, raw := range raws {
		var n int
		if err := json.Unmarshal(raw, &n); err == nil {
			out = append(out, portSpec{Protocol: "ANY", Num: int32(n), End: int32(n)})
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		if a, b, ok := strings.Cut(s, ":"); ok {
			lo, e1 := strconv.Atoi(a)
			hi, e2 := strconv.Atoi(b)
			if e1 != nil || e2 != nil {
				return nil, fmt.Errorf("the port range %q", s)
			}
			out = append(out, portSpec{Protocol: "ANY", Num: int32(lo), End: int32(hi)})
			continue
		}
		if n, err := strconv.Atoi(s); err == nil {
			out = append(out, portSpec{Protocol: "ANY", Num: int32(n), End: int32(n)})
			continue
		}
		out = append(out, portSpec{Protocol: "ANY", Name: s})
	}
	return out, nil
}

// --- matching -------------------------------------------------------------------

// tri is a three-valued answer: the engine never turns "unknown" into yes or no.
type tri int

const (
	matchNo tri = iota
	matchUnknown
	matchYes
)

func and3(a, b tri) tri {
	if a == matchNo || b == matchNo {
		return matchNo
	}
	if a == matchUnknown || b == matchUnknown {
		return matchUnknown
	}
	return matchYes
}

// matchesIdentity is whether the entity's label constraints designate a pod
// of the identity (addresses are not looked at).
func (e *calicoEntity) matchesIdentity(m *model, id int) tri {
	ident := &m.Identities[id]
	res := matchYes
	if e.unknown {
		res = matchUnknown
	}
	if e.scopeNS != "" && ident.Namespace != e.scopeNS {
		return matchNo
	}
	if e.sel != nil && !matchSet(e.sel, ident.Cal) {
		return matchNo
	}
	if e.notSel != nil && matchSet(e.notSel, ident.Cal) {
		return matchNo
	}
	if e.nsSel != nil && !matchSet(e.nsSel, m.nsLabels(ident.Namespace)) {
		return matchNo
	}
	if len(e.saNames) > 0 {
		switch {
		case ident.ServiceAccount == "":
			res = and3(res, matchUnknown)
		case !contains(e.saNames, ident.ServiceAccount):
			return matchNo
		}
	}
	return res
}

// matches is whether the entity designates the endpoint.
func (e *calicoEntity) matches(m *model, ep endpoint) tri {
	res := matchYes
	var ip net.IP
	if ep.pod >= 0 {
		pd := &m.Pods[ep.pod]
		ip = net.ParseIP(pd.IP)
	} else {
		ip = ep.ip
	}
	if len(e.nets) > 0 || len(e.notNets) > 0 {
		if ip == nil {
			res = matchUnknown
		} else {
			if len(e.nets) > 0 {
				in := false
				for _, n := range e.nets {
					if n.Contains(ip) {
						in = true
					}
				}
				if !in {
					return matchNo
				}
			}
			for _, n := range e.notNets {
				if n.Contains(ip) {
					return matchNo
				}
			}
		}
	}
	if e.byLabels {
		if ep.pod < 0 {
			// A selector over an address outside every pod could be
			// satisfied by a NetworkSet, which is not read here.
			return and3(res, matchUnknown)
		}
		res = and3(res, e.matchesIdentity(m, m.Pods[ep.pod].identity))
	}
	return res
}

// matchesFlow is whether the rule matches the flow from -> to on port.
func (c *calicoRule) matchesFlow(m *model, r *rule, from, to endpoint, port flowPort) tri {
	if c.icmp {
		return matchNo
	}
	if len(c.proto) > 0 && !contains(c.proto, port.Protocol) {
		return matchNo
	}
	if contains(c.notProto, port.Protocol) {
		return matchNo
	}
	res := matchYes
	if r.Opaque != nil {
		res = matchUnknown
	}
	if !m.portMatches(r, port, to) {
		return matchNo
	}
	if len(c.notPorts) > 0 && m.portMatches(&rule{Ports: c.notPorts}, port, to) {
		return matchNo
	}
	res = and3(res, c.src.matches(m, from))
	res = and3(res, c.dst.matches(m, to))
	return res
}

// --- the layer ------------------------------------------------------------------

type tierEntry struct {
	pi    int
	order float64
	name  string
	// np is a Kubernetes NetworkPolicy, evaluated as the default tier does.
	np bool
}

// calicoLayer evaluates Calico's tiers for one direction of one pod. ok is
// false when no Calico policy selects the pod there: the NetworkPolicy and
// Cilium layer answers then.
func (m *model) calicoLayer(direction string, ident *identity, self, other, from, to endpoint, port flowPort) (layer, bool) {
	list := ident.In
	if direction == "egress" {
		list = ident.Out
	}
	var entries []tierEntry
	hasCalico, hasCilium := false, false
	for _, pi := range list {
		pol := &m.Policies[pi]
		switch {
		case pol.family == "calico":
			hasCalico = true
			entries = append(entries, tierEntry{pi: pi, order: pol.order, name: pol.Name})
		case pol.family == "" && !pol.cilium:
			entries = append(entries, tierEntry{pi: pi, order: calicoNPOrder, name: pol.Namespace + "/" + pol.Name, np: true})
		case pol.cilium:
			hasCilium = true
		}
	}
	if !hasCalico {
		return layer{}, false
	}
	tierOf := func(e tierEntry) string {
		if e.np {
			return calicoDefaultTier
		}
		return m.Policies[e.pi].tier
	}
	byTier := map[string][]tierEntry{}
	for _, e := range entries {
		byTier[tierOf(e)] = append(byTier[tierOf(e)], e)
	}
	names := make([]string, 0, len(byTier))
	for n := range byTier {
		names = append(names, n)
	}
	sort.Slice(names, func(a, b int) bool {
		oa, ob := tierOrder(m.tiers, names[a]), tierOrder(m.tiers, names[b])
		if oa != ob {
			return oa < ob
		}
		return names[a] < names[b]
	})
	refOf := func(e tierEntry) PolicyRef {
		pol := &m.Policies[e.pi]
		if e.np {
			return PolicyRef{Namespace: pol.Namespace, Name: pol.Name}
		}
		return PolicyRef{Namespace: pol.Namespace, Name: pol.Name, PolicyKind: pol.Kind}
	}
	res := layer{isolating: []PolicyRef{}}
	for _, e := range entries {
		res.isolating = append(res.isolating, refOf(e))
	}
	if hasCilium {
		res.caveats = append(res.caveats, "Cilium policies select this pod too; they are not combined with Calico's here.")
	}
	var trail []string
	for _, tn := range names {
		tier := byTier[tn]
		sort.SliceStable(tier, func(a, b int) bool {
			if tier[a].order != tier[b].order {
				return tier[a].order < tier[b].order
			}
			return tier[a].name < tier[b].name
		})
		passed := false
	policies:
		for _, e := range tier {
			pol := &m.Policies[e.pi]
			rules := pol.In
			if direction == "egress" {
				rules = pol.Out
			}
			for ri := range rules {
				r := &rules[ri]
				var verdict tri
				var action string
				if e.np {
					verdict = matchNo
					if m.portMatches(r, port, to) {
						// Calico turns an ipBlock into nets, which it matches
						// against a pod's own address.
						if match, ipOnly := m.ruleMatches(r, pol, other); match || ipOnly {
							verdict = matchYes
						}
					}
					action = "Allow"
				} else {
					verdict = r.cal.matchesFlow(m, r, from, to, port)
					action = r.Action
					if len(r.Opaque) > 0 && verdict == matchYes {
						verdict = matchUnknown
					}
				}
				if verdict == matchNo {
					continue
				}
				ref := &RuleRef{PolicyRef: refOf(e), Kind: ruleKind(r), Rule: r.Index + 1, Text: ruleText(r)}
				if e.np {
					ref.Kind = "allow"
				}
				where := pol.Kind + " " + pol.Name
				if e.np {
					where = "NetworkPolicy " + pol.Namespace + "/" + pol.Name
				}
				where += ", " + direction + " rule " + strconv.Itoa(ref.Rule) + " (tier " + tn + ")"
				if verdict == matchUnknown {
					res.state, res.decided = sideUnevaluated, ref
					res.reason = joinTrail(trail) + "Only " + where + " could decide it, and it uses " +
						strings.Join(whyUnknown(r), ", ") + ", which is not evaluated here."
					return res, true
				}
				switch action {
				case "Allow":
					res.state, res.decided = sideAllowed, ref
					res.reason = joinTrail(trail) + "Allowed by " + where + "."
					return res, true
				case "Deny":
					res.state, res.decided = sideDenied, ref
					res.reason = joinTrail(trail) + "Denied by " + where + "."
					return res, true
				case "Pass":
					trail = append(trail, "Passed by "+pol.Kind+" "+pol.Name+" to the next tier")
					passed = true
					break policies
				}
			}
		}
		if passed {
			continue
		}
		action := "Deny"
		if t, ok := m.tiers[tn]; ok && t.DefaultAction != "" {
			action = t.DefaultAction
		}
		if strings.EqualFold(action, "Pass") {
			trail = append(trail, "Tier "+tn+" has no matching rule and passes on")
			continue
		}
		var who []PolicyRef
		for _, e := range tier {
			who = append(who, refOf(e))
		}
		res.state, res.noFix = sideDenied, tn != calicoDefaultTier
		res.reason = joinTrail(trail) + "Denied at the end of tier " + tn + ": " + refNames(who) + " select this pod for " + direction + " and no rule of theirs matches."
		return res, true
	}
	res.state = sideOpen
	res.reason = joinTrail(trail) + "No tier decided it: Calico's default profile allows it."
	return res, true
}

func joinTrail(t []string) string {
	if len(t) == 0 {
		return ""
	}
	return strings.Join(t, "; ") + ". "
}

// whyUnknown names what made a match unknown.
func whyUnknown(r *rule) []string {
	if len(r.Opaque) > 0 {
		return r.Opaque
	}
	if r.cal != nil && (r.cal.src.unknown || r.cal.dst.unknown) {
		return []string{"a selector or net that could not be read"}
	}
	return []string{"a peer that could be a NetworkSet"}
}

// --- reading --------------------------------------------------------------------

// PolicyRead is what was read of one family of policies: whether the cluster
// serves it, how many, and what in them is not evaluated.
type PolicyRead struct {
	Served       bool     `json:"served"`
	Policies     int      `json:"policies"`
	NotEvaluated []string `json:"notEvaluated"`
}

func countsText(counts map[string]int) []string {
	out := []string{}
	for k, n := range counts {
		out = append(out, fmt.Sprintf("%s: %d", k, n))
	}
	sort.Strings(out)
	return out
}

// addCalico adds the compiled Calico policies and tiers to the model and
// selects their endpoints.
func (m *model) addCalico(policies []policy, tiers map[string]calicoTier) {
	m.tiers = tiers
	for id := range m.Identities {
		ident := &m.Identities[id]
		ident.Cal = calicoLabels(ident, m.nsLabelsOnly(ident.Namespace))
	}
	for _, p := range policies {
		pi := len(m.Policies)
		m.Policies = append(m.Policies, p)
		pol := &m.Policies[pi]
		if pol.unreadable {
			m.unreadable++
			continue
		}
		var candidates []int
		if pol.clusterwide {
			for id := range m.Identities {
				candidates = append(candidates, id)
			}
		} else {
			candidates = m.byNamespace[pol.Namespace]
		}
		for _, id := range candidates {
			ident := &m.Identities[id]
			if ident.HostNetwork {
				continue
			}
			if !matchSet(pol.calSel, ident.Cal) {
				continue
			}
			if pol.calNS != nil && !matchSet(pol.calNS, m.nsLabels(ident.Namespace)) {
				continue
			}
			pol.Selected = append(pol.Selected, id)
			if pol.Ingress {
				ident.In = append(ident.In, pi)
			}
			if pol.Egress {
				ident.Out = append(ident.Out, pi)
			}
		}
	}
}

package netinspect

import "strings"

// PodAnswer is `GET /pods/{namespace}/{name}`: one pod as the rules see it.
type PodAnswer struct {
	Mode        string        `json:"mode"`
	Complete    bool          `json:"complete"`
	Notes       []string      `json:"notes"`
	CNI         CNI           `json:"cni"`
	Unevaluated []Unevaluated `json:"unevaluated"`
	Pod         PodRow        `json:"pod"`
	Ingress     Direction     `json:"ingress"`
	Egress      Direction     `json:"egress"`
}

// Direction is one direction of one pod: isolated or not, by which policies,
// and what their rules allow.
type Direction struct {
	Isolated bool        `json:"isolated"`
	Policies []PolicyRef `json:"policies"`
	Rules    []RuleView  `json:"rules"`
}

// RuleView is one rule: whom it allows (peers), on what.
type RuleView struct {
	RuleRef
	Peers []PeerView `json:"peers"`
	Ports []string   `json:"ports"`
}

// PeerView is one peer of a rule, with the pods it designates now.
type PeerView struct {
	// Kind is "pods", "cidr" or "anyone".
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Pods is how many pods it designates now (pods peers only).
	Pods int `json:"pods"`
	// Namespaces are the namespaces those pods are in, a few.
	Namespaces []string `json:"namespaces"`
}

// podAnswer builds the answer for one pod; ok false when the model has no
// such pod.
func (rd *read) podAnswer(namespace, name string) (PodAnswer, bool) {
	m := rd.model
	i, ok := m.podIndex[namespace+"/"+name]
	if !ok {
		return PodAnswer{}, false
	}
	p := &m.Pods[i]
	ident := &m.Identities[p.identity]
	a := PodAnswer{Mode: "rules", Complete: rd.complete, Notes: nonNil(rd.notes), CNI: rd.cni,
		Unevaluated: rd.unevaluate, Pod: m.podRow(i)}
	if a.Unevaluated == nil {
		a.Unevaluated = []Unevaluated{}
	}
	a.Ingress = m.direction(ident.In, ident.InAlso, true, p.HostNetwork)
	a.Egress = m.direction(ident.Out, ident.OutAlso, false, p.HostNetwork)
	return a, true
}

func (m *model) direction(isolating, also []int, ingress, hostNetwork bool) Direction {
	d := Direction{Isolated: !hostNetwork && len(isolating) > 0, Policies: []PolicyRef{}, Rules: []RuleView{}}
	for i, pi := range append(append([]int{}, isolating...), also...) {
		pol := &m.Policies[pi]
		if i < len(isolating) {
			d.Policies = append(d.Policies, pol.ref())
		}
		rules := pol.Out
		if ingress {
			rules = pol.In
		}
		for ri := range rules {
			view := m.ruleView(pol, &rules[ri])
			d.Rules = append(d.Rules, view)
		}
	}
	return d
}

// ruleView is one rule of one policy as the page reads it: its peers, with
// the pods they designate now, and its ports.
func (m *model) ruleView(pol *policy, r *rule) RuleView {
	view := RuleView{RuleRef: RuleRef{PolicyRef: pol.ref(),
		Kind: ruleKind(r), Rule: r.Index + 1, Text: ruleText(r)}, Peers: []PeerView{}, Ports: []string{}}
	if r.Everyone {
		view.Peers = append(view.Peers, PeerView{Kind: "anyone", Label: "everyone", Namespaces: []string{}})
	}
	if len(r.Opaque) > 0 {
		view.Peers = append(view.Peers, PeerView{Kind: "unevaluated", Label: strings.Join(r.Opaque, ", "), Namespaces: []string{}})
	}
	for pe := range r.Peers {
		peer := &r.Peers[pe]
		if peer.Entity != "" {
			view.Peers = append(view.Peers, PeerView{Kind: "entity", Label: peer.Entity, Namespaces: []string{}})
			continue
		}
		if !peer.Pods {
			view.Peers = append(view.Peers, PeerView{Kind: "cidr", Label: peer.Label(), Namespaces: []string{}})
			continue
		}
		pv := PeerView{Kind: "pods", Label: peer.Label(), Namespaces: []string{}}
		seen := map[string]bool{}
		for _, id := range m.peerIdentities(peer, pol.Namespace) {
			pv.Pods += len(m.Identities[id].Pods)
			ns := m.Identities[id].Namespace
			if !seen[ns] && len(pv.Namespaces) < labelsPerEdge {
				seen[ns] = true
				pv.Namespaces = append(pv.Namespaces, ns)
			}
		}
		view.Peers = append(view.Peers, pv)
	}
	if r.AnyPort {
		view.Ports = append(view.Ports, "every port")
	}
	for _, port := range r.Ports {
		view.Ports = append(view.Ports, port.String())
	}
	return view
}

// --- the column -------------------------------------------------------------------

// Cell is one pod's pill in the Pods list's column.
type Cell struct {
	Text string `json:"text"`
	Tone string `json:"tone,omitempty"`
	Hint string `json:"hint"`
}

// cells is every pod's isolation, keyed namespace/name.
func (rd *read) cells() map[string]Cell {
	m := rd.model
	out := make(map[string]Cell, len(m.Pods))
	for i := range m.Pods {
		row := m.podRow(i)
		var c Cell
		switch {
		case row.HostNetwork:
			c = Cell{Text: "host network", Hint: "hostNetwork: plugin-defined"}
		case row.In && row.Out:
			c = Cell{Text: "in · out", Tone: "ok", Hint: "isolated: ingress and egress"}
		case row.In:
			c = Cell{Text: "in", Hint: "isolated: ingress"}
		case row.Out:
			c = Cell{Text: "out", Hint: "isolated: egress"}
		default:
			c = Cell{Text: "open", Hint: "no NetworkPolicy selects it"}
		}
		out[row.Namespace+"/"+row.Name] = c
	}
	return out
}

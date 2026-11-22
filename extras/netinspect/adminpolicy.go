package netinspect

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

// AdminNetworkPolicy and BaselineAdminNetworkPolicy
// (policy.networking.k8s.io/v1alpha1), evaluated around the NetworkPolicies as
// the API defines them:
//
//   - an AdminNetworkPolicy is evaluated before every NetworkPolicy: its rules
//     run in order, policies by priority (lower first); Allow and Deny decide
//     the flow, Pass hands it to the NetworkPolicies;
//   - a BaselineAdminNetworkPolicy applies last, and only to a pod that no
//     NetworkPolicy (or Calico or Cilium policy) selects in that direction:
//     Allow and Deny decide, and nothing else lets it through but the default,
//     which allows;
//   - a subject is a set of namespaces, or pods in namespaces; a peer is
//     `namespaces` or `pods`; a port is a number, a name or a range.
//
// Two AdminNetworkPolicies with the same priority are undefined by the API:
// they are ordered by name here, and every answer they take part in says so.
// What is not evaluated: `networks`, `nodes` and any other peer kind, answered
// "not evaluated" when the rule's ports match.

type aSpec struct {
	Priority *int64 `json:"priority"`
	Subject  struct {
		Namespaces *metav1.LabelSelector `json:"namespaces"`
		Pods       *struct {
			NamespaceSelector metav1.LabelSelector `json:"namespaceSelector"`
			PodSelector       metav1.LabelSelector `json:"podSelector"`
		} `json:"pods"`
	} `json:"subject"`
	Ingress []aRule `json:"ingress"`
	Egress  []aRule `json:"egress"`
}

type aRule struct {
	Name   string                       `json:"name"`
	Action string                       `json:"action"`
	From   []map[string]json.RawMessage `json:"from"`
	To     []map[string]json.RawMessage `json:"to"`
	Ports  []aPort                      `json:"ports"`
}

type aPort struct {
	PortNumber *struct {
		Protocol string `json:"protocol"`
		Port     int32  `json:"port"`
	} `json:"portNumber"`
	NamedPort *string `json:"namedPort"`
	PortRange *struct {
		Protocol string `json:"protocol"`
		Start    int32  `json:"start"`
		End      int32  `json:"end"`
	} `json:"portRange"`
}

type aPodPeer struct {
	NamespaceSelector metav1.LabelSelector `json:"namespaceSelector"`
	PodSelector       metav1.LabelSelector `json:"podSelector"`
}

// subjectOf is who an AdminNetworkPolicy applies to.
type subjectOf struct {
	ns  labels.Selector
	pod labels.Selector // nil: every pod of the namespaces
}

// compileAdmin turns one AdminNetworkPolicy or BaselineAdminNetworkPolicy
// into a policy and its subject.
func compileAdmin(u *unstructured.Unstructured, baseline bool) (policy, *subjectOf) {
	kind, family := "AdminNetworkPolicy", "anp"
	if baseline {
		kind, family = "BaselineAdminNetworkPolicy", "banp"
	}
	p := policy{Kind: kind, Name: u.GetName(), family: family, clusterwide: true}
	var spec aSpec
	data, err := json.Marshal(u.Object["spec"])
	if err == nil {
		err = json.Unmarshal(data, &spec)
	}
	if err != nil {
		p.Broken = append(p.Broken, "its spec: "+err.Error())
		p.unreadable = true
		return p, nil
	}
	if spec.Priority != nil {
		p.priority = *spec.Priority
	}
	var subj subjectOf
	switch {
	case spec.Subject.Namespaces != nil:
		p.Selector = "namespaces " + selectorText(spec.Subject.Namespaces)
		subj.ns, err = metav1.LabelSelectorAsSelector(spec.Subject.Namespaces)
	case spec.Subject.Pods != nil:
		p.Selector = "pods " + selectorText(&spec.Subject.Pods.PodSelector) + " in namespaces " + selectorText(&spec.Subject.Pods.NamespaceSelector)
		subj.ns, err = metav1.LabelSelectorAsSelector(&spec.Subject.Pods.NamespaceSelector)
		if err == nil {
			subj.pod, err = metav1.LabelSelectorAsSelector(&spec.Subject.Pods.PodSelector)
		}
	default:
		err = fmt.Errorf("no subject")
	}
	if err != nil {
		p.Broken = append(p.Broken, "its subject: "+err.Error())
		p.unreadable = true
		return p, nil
	}
	p.Ingress, p.Egress = len(spec.Ingress) > 0, len(spec.Egress) > 0
	for i, r := range spec.Ingress {
		p.In = append(p.In, compileAdminRule(&p, i, r, r.From))
	}
	for i, r := range spec.Egress {
		p.Out = append(p.Out, compileAdminRule(&p, i, r, r.To))
	}
	return p, &subj
}

func compileAdminRule(p *policy, index int, in aRule, peers []map[string]json.RawMessage) rule {
	r := rule{Index: index, Everyone: len(peers) == 0, AnyPort: len(in.Ports) == 0}
	switch strings.ToLower(in.Action) {
	case "allow":
		r.Action = "Allow"
	case "deny":
		r.Action, r.Deny = "Deny", true
	case "pass":
		r.Action = "Pass"
	default:
		p.Broken = append(p.Broken, fmt.Sprintf("rule %d: the action %q", index+1, in.Action))
		r.Opaque = append(r.Opaque, "an unknown action")
	}
	for _, each := range peers {
		matched := false
		if raw, ok := each["namespaces"]; ok {
			var sel metav1.LabelSelector
			if err := json.Unmarshal(raw, &sel); err != nil {
				p.Broken = append(p.Broken, fmt.Sprintf("rule %d: a namespaces peer: %v", index+1, err))
				r.Opaque = append(r.Opaque, "an unreadable peer")
			} else {
				ns, err := metav1.LabelSelectorAsSelector(&sel)
				if err != nil {
					p.Broken = append(p.Broken, fmt.Sprintf("rule %d: a namespaces peer: %v", index+1, err))
					ns = labels.Nothing()
				}
				r.Peers = append(r.Peers, peer{Pods: true, nsSelector: ns, NSText: selectorText(&sel)})
			}
			matched = true
		}
		if raw, ok := each["pods"]; ok {
			var pp aPodPeer
			if err := json.Unmarshal(raw, &pp); err != nil {
				p.Broken = append(p.Broken, fmt.Sprintf("rule %d: a pods peer: %v", index+1, err))
				r.Opaque = append(r.Opaque, "an unreadable peer")
			} else {
				ns, err1 := metav1.LabelSelectorAsSelector(&pp.NamespaceSelector)
				ps, err2 := metav1.LabelSelectorAsSelector(&pp.PodSelector)
				if err1 != nil || err2 != nil {
					p.Broken = append(p.Broken, fmt.Sprintf("rule %d: a pods peer selector", index+1))
					ns, ps = labels.Nothing(), labels.Nothing()
				}
				r.Peers = append(r.Peers, peer{Pods: true, nsSelector: ns, podSelector: ps,
					NSText: selectorText(&pp.NamespaceSelector), PodText: selectorText(&pp.PodSelector)})
			}
			matched = true
		}
		for key := range each {
			if key != "namespaces" && key != "pods" {
				r.Opaque = append(r.Opaque, key)
				matched = true
			}
		}
		if !matched {
			r.Opaque = append(r.Opaque, "an empty peer")
		}
	}
	sort.Strings(r.Opaque)
	for _, port := range in.Ports {
		switch {
		case port.PortNumber != nil:
			r.Ports = append(r.Ports, portSpec{Protocol: strings.ToUpper(port.PortNumber.Protocol), Num: port.PortNumber.Port, End: port.PortNumber.Port})
		case port.NamedPort != nil:
			r.Ports = append(r.Ports, portSpec{Protocol: "ANY", Name: *port.NamedPort})
		case port.PortRange != nil:
			r.Ports = append(r.Ports, portSpec{Protocol: strings.ToUpper(port.PortRange.Protocol), Num: port.PortRange.Start, End: port.PortRange.End})
		default:
			r.Opaque = append(r.Opaque, "an unknown port kind")
		}
	}
	return r
}

// addAdmin adds the compiled policies to the model and selects the pods of
// their subjects. They are listed in InAlso and OutAlso: their rules count
// and they isolate nothing.
func (m *model) addAdmin(policies []policy, subjects []*subjectOf) {
	for i := range policies {
		pi := len(m.Policies)
		m.Policies = append(m.Policies, policies[i])
		pol := &m.Policies[pi]
		if pol.unreadable || subjects[i] == nil {
			m.unreadable++
			continue
		}
		subj := subjects[i]
		for ns, ids := range m.byNamespace {
			if !subj.ns.Matches(m.nsLabels(ns)) {
				continue
			}
			for _, id := range ids {
				if subj.pod != nil && !subj.pod.Matches(m.Identities[id].Labels) {
					continue
				}
				pol.Selected = append(pol.Selected, id)
				if pol.Ingress {
					m.Identities[id].InAlso = append(m.Identities[id].InAlso, pi)
				}
				if pol.Egress {
					m.Identities[id].OutAlso = append(m.Identities[id].OutAlso, pi)
				}
			}
		}
	}
}

// layer is one layer's answer for one direction.
type layer struct {
	state     string
	decided   *RuleRef
	reason    string
	isolating []PolicyRef
	caveats   []string
	noFix     bool
}

func (l layer) apply(s Side) Side {
	s.State, s.Decided, s.Reason, s.noFix = l.state, l.decided, l.reason, l.noFix
	if l.isolating != nil {
		s.Isolating = l.isolating
	}
	s.Caveats = append(s.Caveats, l.caveats...)
	return s
}

// adminLayer evaluates the AdminNetworkPolicies, or with baseline the
// BaselineAdminNetworkPolicy, for one direction. ok is false when it decides
// nothing: no policy selects the pod, no rule matches, or a rule Passes.
func (m *model) adminLayer(direction string, ident *identity, self, other, from, to endpoint, port flowPort, baseline bool) (layer, bool) {
	want := "anp"
	if baseline {
		want = "banp"
	}
	list := ident.InAlso
	if direction == "egress" {
		list = ident.OutAlso
	}
	var pols []int
	for _, pi := range list {
		if m.Policies[pi].family == want {
			pols = append(pols, pi)
		}
	}
	if len(pols) == 0 {
		return layer{}, false
	}
	sort.SliceStable(pols, func(a, b int) bool {
		pa, pb := &m.Policies[pols[a]], &m.Policies[pols[b]]
		if pa.priority != pb.priority {
			return pa.priority < pb.priority
		}
		return pa.Name < pb.Name
	})
	var caveats []string
	if !baseline {
		for i := 1; i < len(pols); i++ {
			a, b := &m.Policies[pols[i-1]], &m.Policies[pols[i]]
			if a.priority == b.priority {
				caveats = append(caveats, fmt.Sprintf("AdminNetworkPolicies %s and %s share priority %d, which the API leaves undefined: they are ordered by name here.",
					a.Name, b.Name, a.priority))
			}
		}
	}
	for _, pi := range pols {
		pol := &m.Policies[pi]
		rules := pol.In
		if direction == "egress" {
			rules = pol.Out
		}
		for ri := range rules {
			r := &rules[ri]
			if !m.portMatches(r, port, to) {
				continue
			}
			ref := &RuleRef{PolicyRef: PolicyRef{Name: pol.Name, PolicyKind: pol.Kind}, Kind: ruleKind(r), Rule: r.Index + 1, Text: ruleText(r)}
			match, _ := m.ruleMatches(r, pol, other)
			if !match {
				if len(r.Opaque) > 0 {
					return layer{state: sideUnevaluated, decided: ref, caveats: caveats, reason: "Only " + pol.Kind + " " + pol.Name + ", rule " +
						strconv.Itoa(ref.Rule) + ", could decide it, and it names " + strings.Join(r.Opaque, ", ") + ", which is not evaluated here."}, true
				}
				continue
			}
			where := pol.Kind + " " + pol.Name + ", " + direction + " rule " + strconv.Itoa(ref.Rule)
			tail := "it is decided before any NetworkPolicy."
			if baseline {
				tail = "no NetworkPolicy selects this pod, so the baseline applies."
			}
			switch r.Action {
			case "Allow":
				return layer{state: sideAllowed, decided: ref, caveats: caveats, reason: "Allowed by " + where + ": " + tail}, true
			case "Deny":
				return layer{state: sideDenied, decided: ref, noFix: !baseline, caveats: caveats, reason: "Denied by " + where + ": " + tail}, true
			case "Pass":
				// Handed to the NetworkPolicies, which decide.
				return layer{}, false
			}
		}
	}
	return layer{}, false
}

package netinspect

import (
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
)

// Cilium's own policies, CiliumNetworkPolicy (namespaced) and
// CiliumClusterwideNetworkPolicy, evaluated at layers 3 and 4 beside the
// NetworkPolicies, as Cilium itself combines them:
//
//   - a policy selects endpoints with endpointSelector, over the endpoint's
//     labels: the pod's, plus io.kubernetes.pod.namespace, the namespace's as
//     io.cilium.k8s.namespace.labels.<key>, and the service account as
//     io.cilium.k8s.policy.serviceaccount. A namespaced policy selects in its
//     own namespace only, and its fromEndpoints/toEndpoints too unless they
//     name a namespace;
//   - a policy with an ingress (or ingressDeny) section puts what it selects
//     in default-deny for ingress, the same for egress, unless
//     enableDefaultDeny turns that off for the direction;
//   - peers are endpoints, entities (world, cluster, host, remote-node,
//     kube-apiserver, all…), CIDRs (toCIDR, toCIDRSet with except and
//     cidrGroupRef), which never select a pod of the cluster; ports are
//     toPorts, protocol ANY when unset, port 0 for every port;
//   - a deny rule (ingressDeny, egressDeny) wins over every allow, whatever
//     policy the allow is in, NetworkPolicies included.
//
// What is not evaluated is named on the rule and answered "not evaluated":
// toFQDNs, toServices, toGroups, toNodes/fromNodes, toRequires/fromRequires,
// layer-7 rules (HTTP, Kafka, DNS), serverNames, authentication, ICMP, and
// a policy with a nodeSelector (a host policy, about nodes, not pods).

// Cilium's label for an endpoint's namespace, and the prefixes of the others.
const (
	ciliumNamespaceLabel = "io.kubernetes.pod.namespace"
	ciliumNSLabelPrefix  = "io.cilium.k8s.namespace.labels."
	ciliumSALabel        = "io.cilium.k8s.policy.serviceaccount"
)

// ciliumLabels is an identity's labels as Cilium matches them.
func ciliumLabels(ident *identity, nsLabels labels.Set) labels.Set {
	out := make(labels.Set, len(ident.Labels)+len(nsLabels)+2)
	for k, v := range ident.Labels {
		out[k] = v
	}
	for k, v := range nsLabels {
		out[ciliumNSLabelPrefix+k] = v
	}
	out[ciliumNamespaceLabel] = ident.Namespace
	if ident.ServiceAccount != "" {
		out[ciliumSALabel] = ident.ServiceAccount
	}
	return out
}

// The spec, as much of it as is read. Unknown fields are ignored on purpose:
// a field this engine does not know is a rule it cannot evaluate, and the
// rule says so through cIngress/cEgress's `Unknown`.
type cSpec struct {
	EndpointSelector  *metav1.LabelSelector `json:"endpointSelector"`
	NodeSelector      *metav1.LabelSelector `json:"nodeSelector"`
	Ingress           []json.RawMessage     `json:"ingress"`
	IngressDeny       []json.RawMessage     `json:"ingressDeny"`
	Egress            []json.RawMessage     `json:"egress"`
	EgressDeny        []json.RawMessage     `json:"egressDeny"`
	EnableDefaultDeny *struct {
		Ingress *bool `json:"ingress"`
		Egress  *bool `json:"egress"`
	} `json:"enableDefaultDeny"`
}

type cCIDR struct {
	CIDR         string   `json:"cidr"`
	CIDRGroupRef string   `json:"cidrGroupRef"`
	Except       []string `json:"except"`
}

type cPort struct {
	Port     string `json:"port"`
	EndPort  int32  `json:"endPort"`
	Protocol string `json:"protocol"`
}

type cPortRule struct {
	Ports       []cPort         `json:"ports"`
	Rules       json.RawMessage `json:"rules"`
	ServerNames []string        `json:"serverNames"`
}

// cRule is one ingress or egress element, both directions' fields: the
// from/to pairs are read by direction.
type cRule struct {
	FromEndpoints []metav1.LabelSelector `json:"fromEndpoints"`
	ToEndpoints   []metav1.LabelSelector `json:"toEndpoints"`
	FromEntities  []string               `json:"fromEntities"`
	ToEntities    []string               `json:"toEntities"`
	FromCIDR      []string               `json:"fromCIDR"`
	ToCIDR        []string               `json:"toCIDR"`
	FromCIDRSet   []cCIDR                `json:"fromCIDRSet"`
	ToCIDRSet     []cCIDR                `json:"toCIDRSet"`
	ToPorts       []cPortRule            `json:"toPorts"`
}

// opaqueFields are the rule fields this engine does not evaluate.
var opaqueFields = []string{
	"toFQDNs", "toServices", "toGroups", "toNodes", "fromNodes", "toRequires", "fromRequires",
	"icmps", "authentication", "fromGroups",
}

// compileCilium turns one CiliumNetworkPolicy or CiliumClusterwideNetworkPolicy
// into policies (one per spec: `specs` holds several). groups resolves a
// cidrGroupRef to its CIDRs.
func compileCilium(u *unstructured.Unstructured, clusterwide bool, groups map[string][]string) (out []policy, hostPolicies int) {
	kind := "CiliumNetworkPolicy"
	if clusterwide {
		kind = "CiliumClusterwideNetworkPolicy"
	}
	var specs []any
	if s, ok := u.Object["spec"]; ok && s != nil {
		specs = append(specs, s)
	}
	if list, ok := u.Object["specs"].([]any); ok {
		specs = append(specs, list...)
	}
	for i, raw := range specs {
		name := u.GetName()
		if len(specs) > 1 {
			name = fmt.Sprintf("%s#%d", name, i+1)
		}
		p := policy{Kind: kind, Namespace: u.GetNamespace(), Name: name, cilium: true, clusterwide: clusterwide}
		data, err := json.Marshal(raw)
		var spec cSpec
		if err == nil {
			err = json.Unmarshal(data, &spec)
		}
		if err != nil {
			p.Broken = append(p.Broken, "its spec: "+err.Error())
			out = append(out, p)
			continue
		}
		if spec.NodeSelector != nil {
			// A host policy: it is about nodes, and no pod is selected.
			hostPolicies++
			continue
		}
		if spec.EndpointSelector != nil {
			p.Selector = selectorText(spec.EndpointSelector)
			if s, err := ciliumSelector(spec.EndpointSelector); err == nil {
				p.selector = s
			} else {
				p.Broken = append(p.Broken, "its endpointSelector: "+err.Error())
			}
		}
		inSection := spec.Ingress != nil || spec.IngressDeny != nil
		outSection := spec.Egress != nil || spec.EgressDeny != nil
		in, out2 := true, true
		if spec.EnableDefaultDeny != nil {
			if spec.EnableDefaultDeny.Ingress != nil {
				in = *spec.EnableDefaultDeny.Ingress
			}
			if spec.EnableDefaultDeny.Egress != nil {
				out2 = *spec.EnableDefaultDeny.Egress
			}
		}
		p.Ingress, p.rulesIn = inSection && in, inSection && !in
		p.Egress, p.rulesOut = outSection && out2, outSection && !out2
		implicit := ""
		if !clusterwide {
			implicit = p.Namespace
		}
		idx := 0
		add := func(raws []json.RawMessage, ingress, deny bool) {
			for _, r := range raws {
				compiled := compileCiliumRule(&p, idx, r, ingress, deny, implicit, groups)
				idx++
				if ingress {
					p.In = append(p.In, compiled)
				} else {
					p.Out = append(p.Out, compiled)
				}
			}
		}
		add(spec.Ingress, true, false)
		add(spec.IngressDeny, true, true)
		idx = 0
		add(spec.Egress, false, false)
		add(spec.EgressDeny, false, true)
		out = append(out, p)
	}
	return out, hostPolicies
}

func compileCiliumRule(p *policy, index int, raw json.RawMessage, ingress, deny bool, implicitNS string, groups map[string][]string) rule {
	r := rule{Index: index, Deny: deny}
	var c cRule
	if err := json.Unmarshal(raw, &c); err != nil {
		p.Broken = append(p.Broken, fmt.Sprintf("rule %d: %v", index+1, err))
		r.Opaque = append(r.Opaque, "an unreadable rule")
		return r
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for _, f := range opaqueFields {
		if _, ok := fields[f]; ok {
			r.Opaque = append(r.Opaque, f)
		}
	}
	endpoints, entities, cidrs, sets := c.FromEndpoints, c.FromEntities, c.FromCIDR, c.FromCIDRSet
	if !ingress {
		endpoints, entities, cidrs, sets = c.ToEndpoints, c.ToEntities, c.ToCIDR, c.ToCIDRSet
	}
	for i := range endpoints {
		sel := &endpoints[i]
		pe := peer{Pods: true, cilium: true, PodText: ciliumSelectorText(sel)}
		if reserved := reservedEntity(sel); reserved != "" {
			r.Peers = append(r.Peers, peer{Entity: reserved})
			continue
		}
		s, err := ciliumSelector(sel)
		if err != nil {
			p.Broken = append(p.Broken, fmt.Sprintf("rule %d: an endpoint selector: %v", index+1, err))
			s = labels.Nothing()
		}
		pe.epSelector = s
		if implicitNS != "" && !namesNamespace(sel) {
			pe.implicitNS = implicitNS
		}
		r.Peers = append(r.Peers, pe)
	}
	for _, e := range entities {
		r.Peers = append(r.Peers, peer{Entity: e})
	}
	for _, text := range cidrs {
		r.Peers = append(r.Peers, ciliumCIDR(p, index, text, nil, ""))
	}
	for _, set := range sets {
		switch {
		case set.CIDRGroupRef != "":
			cs, ok := groups[set.CIDRGroupRef]
			if !ok {
				r.Opaque = append(r.Opaque, "cidrGroupRef "+set.CIDRGroupRef+" (no such CiliumCIDRGroup could be read)")
				continue
			}
			for _, c := range cs {
				r.Peers = append(r.Peers, ciliumCIDR(p, index, c, set.Except, set.CIDRGroupRef))
			}
		case set.CIDR != "":
			r.Peers = append(r.Peers, ciliumCIDR(p, index, set.CIDR, set.Except, ""))
		}
	}
	// No peer at all, and nothing opaque: Cilium's empty rule selects no one
	// (`ingress: [{}]` is a default deny), unlike a NetworkPolicy's.
	r.AnyPort = len(c.ToPorts) == 0
	for _, pr := range c.ToPorts {
		if len(pr.Rules) > 0 && string(pr.Rules) != "null" && string(pr.Rules) != "{}" {
			r.L7 = append(r.L7, "layer-7 rules ("+l7Kinds(pr.Rules)+")")
		}
		if len(pr.ServerNames) > 0 {
			r.L7 = append(r.L7, "serverNames")
		}
		if len(pr.Ports) == 0 {
			r.AnyPort = true
		}
		for _, port := range pr.Ports {
			r.Ports = append(r.Ports, ciliumPort(port))
		}
	}
	return r
}

// ciliumPort is one toPorts port: protocol ANY when unset, port 0 or unset
// for every port, a name resolved on the destination as a NetworkPolicy's.
func ciliumPort(c cPort) portSpec {
	spec := portSpec{Protocol: strings.ToUpper(c.Protocol)}
	if spec.Protocol == "" {
		spec.Protocol = "ANY"
	}
	n, err := strconv.Atoi(c.Port)
	switch {
	case c.Port == "" || (err == nil && n == 0):
		spec.Any = true
	case err != nil:
		spec.Name = c.Port
	default:
		spec.Num, spec.End = int32(n), int32(n)
		if c.EndPort > spec.Num {
			spec.End = c.EndPort
		}
	}
	return spec
}

func ciliumCIDR(p *policy, index int, text string, except []string, group string) peer {
	pe := peer{CIDR: text, Except: except, cidrCilium: true}
	if group != "" {
		pe.CIDR = text + " (" + group + ")"
	}
	if !strings.Contains(text, "/") {
		if ip := net.ParseIP(text); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			text = fmt.Sprintf("%s/%d", text, bits)
		}
	}
	if _, block, err := net.ParseCIDR(text); err == nil {
		pe.block = block
	} else {
		p.Broken = append(p.Broken, fmt.Sprintf("rule %d: the CIDR %q", index+1, text))
	}
	for _, e := range except {
		if _, n, err := net.ParseCIDR(e); err == nil {
			pe.except = append(pe.except, n)
		}
	}
	return pe
}

// l7Kinds names the layer-7 protocols a rules block holds: http, kafka, dns…
func l7Kinds(raw json.RawMessage) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || len(m) == 0 {
		return "unknown"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// ciliumKey strips the source prefix Cilium allows on a selector key
// ("k8s:app" is "app"); "reserved:" keys are entities, read by reservedEntity.
func ciliumKey(k string) string {
	for _, prefix := range []string{"k8s:", "any:"} {
		if rest, ok := strings.CutPrefix(k, prefix); ok {
			return rest
		}
	}
	return k
}

// ciliumSelector is a label selector over endpoint labels, its keys
// stripped of their source prefix.
func ciliumSelector(sel *metav1.LabelSelector) (labels.Selector, error) {
	clean := metav1.LabelSelector{}
	if len(sel.MatchLabels) > 0 {
		clean.MatchLabels = map[string]string{}
		for k, v := range sel.MatchLabels {
			clean.MatchLabels[ciliumKey(k)] = v
		}
	}
	for _, e := range sel.MatchExpressions {
		e.Key = ciliumKey(e.Key)
		clean.MatchExpressions = append(clean.MatchExpressions, e)
	}
	return metav1.LabelSelectorAsSelector(&clean)
}

// namesNamespace is whether a selector says which namespace, so the policy's
// own namespace is not implied.
func namesNamespace(sel *metav1.LabelSelector) bool {
	for k := range sel.MatchLabels {
		if ciliumKey(k) == ciliumNamespaceLabel {
			return true
		}
	}
	for _, e := range sel.MatchExpressions {
		if ciliumKey(e.Key) == ciliumNamespaceLabel {
			return true
		}
	}
	return false
}

// reservedEntity reads `reserved:<entity>` in an endpoint selector as that
// entity.
func reservedEntity(sel *metav1.LabelSelector) string {
	for k := range sel.MatchLabels {
		if rest, ok := strings.CutPrefix(k, "reserved:"); ok {
			return rest
		}
	}
	return ""
}

// ciliumSelectorText is an endpoint selector as a person reads it, its
// namespace label as "namespace X".
func ciliumSelectorText(sel *metav1.LabelSelector) string {
	var parts []string
	keys := make([]string, 0, len(sel.MatchLabels))
	for k := range sel.MatchLabels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ns := ""
	for _, k := range keys {
		if ciliumKey(k) == ciliumNamespaceLabel {
			ns = "namespace " + sel.MatchLabels[k]
			continue
		}
		parts = append(parts, ciliumKey(k)+"="+sel.MatchLabels[k])
	}
	for _, e := range sel.MatchExpressions {
		key := ciliumKey(e.Key)
		if key == ciliumNamespaceLabel {
			key = "namespace"
		}
		switch e.Operator {
		case metav1.LabelSelectorOpExists:
			parts = append(parts, key)
		case metav1.LabelSelectorOpDoesNotExist:
			parts = append(parts, "!"+key)
		case metav1.LabelSelectorOpNotIn:
			parts = append(parts, key+" notin ("+strings.Join(e.Values, ",")+")")
		default:
			parts = append(parts, key+" in ("+strings.Join(e.Values, ",")+")")
		}
	}
	text := strings.Join(parts, ",")
	switch {
	case text == "" && ns == "":
		return "all"
	case text == "":
		return "all in " + ns
	case ns != "":
		return text + " in " + ns
	}
	return text
}

var _ = selection.Equals

// addCilium adds compiled Cilium policies to the model and selects their
// endpoints: a namespaced policy in its namespace, a clusterwide one in
// every namespace.
func (m *model) addCilium(policies []policy) {
	for id := range m.Identities {
		ident := &m.Identities[id]
		ident.EP = ciliumLabels(ident, m.nsLabelsOnly(ident.Namespace))
	}
	for _, p := range policies {
		pi := len(m.Policies)
		m.Policies = append(m.Policies, p)
		pol := &m.Policies[pi]
		if pol.selector == nil {
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
			if !pol.selector.Matches(ident.EP) {
				continue
			}
			pol.Selected = append(pol.Selected, id)
			if pol.Ingress {
				ident.In = append(ident.In, pi)
			} else if pol.rulesIn {
				ident.InAlso = append(ident.InAlso, pi)
			}
			if pol.Egress {
				ident.Out = append(ident.Out, pi)
			} else if pol.rulesOut {
				ident.OutAlso = append(ident.OutAlso, pi)
			}
		}
	}
}

// nsLabelsOnly is a namespace's own labels, without the name label added
// when it could not be read (Cilium adds the name as io.kubernetes.pod.namespace).
func (m *model) nsLabelsOnly(name string) labels.Set {
	if set, ok := m.Namespaces[name]; ok {
		return set
	}
	return labels.Set{"kubernetes.io/metadata.name": name}
}

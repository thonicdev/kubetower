package netinspect

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// The engine: what the cluster's NetworkPolicies allow, from the objects
// alone. It holds nothing between requests; one model is built per request
// from what Host.List returned to the person, and dropped with the answer.
//
// The semantics are networking.k8s.io/v1's, as its API reference states
// them, and nothing an implementation adds:
//
//   - a pod is isolated in a direction when at least one policy of its
//     namespace selects it with that direction in its policyTypes;
//   - policyTypes absent is Ingress always, and Egress when the policy has an
//     egress section - so a policy holding only egress rules isolates the pod
//     for ingress too, allowing nothing there;
//   - policies add up: what is allowed is the union of what every policy
//     selecting the pod allows. There is no order and no deny rule;
//   - a peer is a podSelector (in the policy's namespace), a
//     namespaceSelector (every pod of the namespaces it matches), both in one
//     element (AND), or an ipBlock; an absent or empty from/to is everyone,
//     and an empty rule list is no one;
//   - ports: absent is every port; otherwise protocol (TCP when unset) AND
//     port, a named port resolved on the destination pod's containers, endPort
//     a range.
//
// What the API leaves to the network plugin is answered "depends", never
// "allowed" or "refused": an ipBlock that covers a pod's address, and a
// hostNetwork pod.

// pod is one pod as the engine reads it.
type pod struct {
	Namespace string
	Name      string
	IP        string
	// HostNetwork pods share their node's network: what NetworkPolicy does to
	// them is the plugin's to decide.
	HostNetwork bool
	Labels      map[string]string
	// Ports are the containers' named ports, for a policy that names one.
	Ports    map[string]namedPort
	Workload string
	// ServiceAccount is part of a pod's Cilium identity.
	ServiceAccount string
	identity       int
}

type namedPort struct {
	Port     int32
	Protocol string
}

// identity is every pod of one namespace that carries the same labels (and
// the same hostNetwork): no policy can tell them apart, so selection and
// isolation are computed once for all of them.
type identity struct {
	Namespace      string
	Labels         labels.Set
	HostNetwork    bool
	ServiceAccount string
	Pods           []int
	// In and Out are the policies isolating it in each direction (indexes).
	In  []int
	Out []int
	// InAlso and OutAlso select it with rules in that direction without
	// isolating it: a Cilium policy with enableDefaultDeny off. Their rules
	// count, their selection does not isolate.
	InAlso  []int
	OutAlso []int
	// EP is its labels as Cilium matches them (ciliumLabels).
	EP labels.Set
	// Cal is its labels as Calico matches them (calicoLabels).
	Cal labels.Set
}

// policy is one NetworkPolicy, compiled.
type policy struct {
	// Kind is NetworkPolicy, CiliumNetworkPolicy or
	// CiliumClusterwideNetworkPolicy.
	Kind      string
	Namespace string
	Name      string
	Selector  string
	selector  labels.Selector
	Ingress   bool
	Egress    bool
	In        []rule
	Out       []rule
	// Selected are the identities its podSelector matches.
	Selected []int
	// Broken is why a part of it could not be compiled (a selector the API
	// server would not have accepted); such a part matches nothing.
	Broken []string
	// cilium: the selector is over Cilium's endpoint labels (EP), and a
	// clusterwide policy selects in every namespace.
	cilium      bool
	clusterwide bool
	// rulesIn and rulesOut: a Cilium policy whose rules count in a
	// direction where it does not isolate (enableDefaultDeny off).
	rulesIn  bool
	rulesOut bool

	// family is "" for NetworkPolicy and Cilium, "calico", "anp" or "banp":
	// the policies the layers of calicopolicy.go and adminpolicy.go evaluate.
	family string
	// Calico: the tier, the order inside it (+Inf when absent), the compiled
	// endpoint selector and, for a GlobalNetworkPolicy, its namespace selector.
	tier   string
	order  float64
	calSel calicoSel
	calNS  calicoSel
	// priority is an AdminNetworkPolicy's (lower wins).
	priority int64
	// unreadable: the policy's selector could not be read, so which pods it
	// selects is unknown and it is not evaluated.
	unreadable bool
}

// ref names the policy; PolicyKind only for the families beyond NetworkPolicy
// and Cilium.
func (p *policy) ref() PolicyRef {
	r := PolicyRef{Namespace: p.Namespace, Name: p.Name}
	if p.family != "" {
		r.PolicyKind = p.Kind
	}
	return r
}

// rule is one ingress or egress element.
type rule struct {
	Index int
	// Everyone is a rule with no from/to: every source or destination.
	Everyone bool
	Peers    []peer
	// AnyPort is a rule with no ports.
	AnyPort bool
	Ports   []portSpec
	// Deny is a Cilium ingressDeny or egressDeny rule: it wins over every
	// allow, whatever policy it is in.
	Deny bool
	// Opaque names what makes the rule's peers unknown here (toFQDNs,
	// toServices…): it is drawn and answered as "not evaluated".
	Opaque []string
	// L7 names layer-7 rules on its ports: the peer and port are known, the
	// outcome depends on the request's content.
	L7 []string
	// Action is "Allow", "Deny" or "Pass" for a Calico or AdminNetworkPolicy
	// rule; empty for the others.
	Action string
	// cal is a Calico rule's own matching.
	cal *calicoRule
}

// peer is one from/to element.
type peer struct {
	// Pods: a pod selector and/or a namespace selector. Nil selector is
	// "not set", which differs from an empty one.
	Pods        bool
	podSelector labels.Selector
	nsSelector  labels.Selector
	// The selectors as the policy wrote them, for the page.
	PodText string
	NSText  string

	// IPBlock.
	CIDR   string
	block  *net.IPNet
	except []*net.IPNet
	Except []string
	// cidrCilium: a Cilium CIDR, which never matches a pod of the cluster.
	cidrCilium bool

	// Entity is a Cilium entity: world, cluster, host, remote-node,
	// kube-apiserver, all…
	Entity string
	// cilium pods: a selector over endpoint labels; implicitNS is the
	// policy's namespace when the selector names none.
	cilium     bool
	epSelector labels.Selector
	implicitNS string

	// custom designates identities by a rule of its own (a Calico entity).
	custom func(m *model, id int) bool
}

type portSpec struct {
	Protocol string
	// Any is a port entry with a protocol and no port.
	Any  bool
	Num  int32
	End  int32
	Name string
}

// String is the port as the page draws it: "80/TCP", "http", "8000-8080/TCP",
// "all UDP".
func (p portSpec) String() string {
	switch {
	case p.Any && p.Protocol == "ANY":
		return "every port"
	case p.Any:
		return "all " + p.Protocol
	case p.Protocol == "ANY" && p.Name == "":
		if p.End > p.Num {
			return fmt.Sprintf("%d-%d", p.Num, p.End)
		}
		return fmt.Sprintf("%d", p.Num)
	case p.Name != "":
		if p.Protocol != "TCP" && p.Protocol != "ANY" {
			return p.Name + "/" + p.Protocol
		}
		return p.Name
	case p.End > p.Num:
		return fmt.Sprintf("%d-%d/%s", p.Num, p.End, p.Protocol)
	}
	return fmt.Sprintf("%d/%s", p.Num, p.Protocol)
}

// model is one request's whole picture.
type model struct {
	Pods       []pod
	Identities []identity
	Policies   []policy
	// Namespaces is every namespace's labels, keyed by name. One the person
	// could not read (or that the listing did not reach) is answered with the
	// one label the API server always sets on it.
	Namespaces map[string]labels.Set
	// byNamespace is the identities of each namespace.
	byNamespace map[string][]int
	// unreadable counts the Calico and AdminNetworkPolicy objects whose
	// selector could not be read.
	unreadable int
	// tiers are Calico's, by name.
	tiers    map[string]calicoTier
	podIndex map[string]int  // "namespace/name"
	finished map[string]bool // "namespace/name" of a Succeeded or Failed pod: it exists, and has no network
	ipIndex  map[string]int
}

// build compiles the objects into a model. Nothing in it fails: a part of a
// policy that cannot be compiled matches nothing and says why (policy.Broken).
func build(pods []corev1.Pod, policies []networkingv1.NetworkPolicy, namespaces map[string]map[string]string) *model {
	m := &model{
		Namespaces:  make(map[string]labels.Set, len(namespaces)),
		byNamespace: map[string][]int{},
		podIndex:    make(map[string]int, len(pods)),
		finished:    map[string]bool{},
		ipIndex:     make(map[string]int, len(pods)),
	}
	for name, set := range namespaces {
		m.Namespaces[name] = labels.Set(set)
	}
	idKeys := map[string]int{}
	m.Pods = make([]pod, 0, len(pods))
	for i := range pods {
		p := &pods[i]
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			// A finished pod has no network: it would only inflate the counts.
			// It is remembered so a question about it is not answered "gone".
			m.finished[p.Namespace+"/"+p.Name] = true
			continue
		}
		entry := pod{
			Namespace:   p.Namespace,
			Name:        p.Name,
			IP:          p.Status.PodIP,
			HostNetwork: p.Spec.HostNetwork,
			Labels:      p.Labels,
			Workload:    workloadOf(p),

			ServiceAccount: p.Spec.ServiceAccountName,
		}
		for _, c := range p.Spec.Containers {
			for _, port := range c.Ports {
				if port.Name == "" {
					continue
				}
				if entry.Ports == nil {
					entry.Ports = map[string]namedPort{}
				}
				entry.Ports[port.Name] = namedPort{Port: port.ContainerPort, Protocol: protocolOf(port.Protocol)}
			}
		}
		key := identityKey(p.Namespace, p.Labels, p.Spec.HostNetwork) + "\x00sa=" + p.Spec.ServiceAccountName
		id, ok := idKeys[key]
		if !ok {
			id = len(m.Identities)
			idKeys[key] = id
			m.Identities = append(m.Identities, identity{Namespace: p.Namespace, Labels: labels.Set(p.Labels),
				HostNetwork: p.Spec.HostNetwork, ServiceAccount: p.Spec.ServiceAccountName})
			m.byNamespace[p.Namespace] = append(m.byNamespace[p.Namespace], id)
		}
		entry.identity = id
		m.Identities[id].Pods = append(m.Identities[id].Pods, len(m.Pods))
		m.podIndex[p.Namespace+"/"+p.Name] = len(m.Pods)
		if entry.IP != "" && !entry.HostNetwork {
			m.ipIndex[entry.IP] = len(m.Pods)
		}
		m.Pods = append(m.Pods, entry)
	}

	m.Policies = make([]policy, 0, len(policies))
	for i := range policies {
		m.Policies = append(m.Policies, compile(&policies[i]))
	}
	for pi := range m.Policies {
		p := &m.Policies[pi]
		if p.selector == nil {
			continue
		}
		for _, id := range m.byNamespace[p.Namespace] {
			if !p.selector.Matches(m.Identities[id].Labels) {
				continue
			}
			p.Selected = append(p.Selected, id)
			if p.Ingress {
				m.Identities[id].In = append(m.Identities[id].In, pi)
			}
			if p.Egress {
				m.Identities[id].Out = append(m.Identities[id].Out, pi)
			}
		}
	}
	return m
}

// identityKey is the namespace, the labels sorted, and hostNetwork.
func identityKey(namespace string, set map[string]string, hostNetwork bool) string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(namespace)
	if hostNetwork {
		b.WriteString("\x00host")
	}
	for _, k := range keys {
		b.WriteByte(0)
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(set[k])
	}
	return b.String()
}

// workloadOf names the pod's workload as people say it: a ReplicaSet's
// Deployment, or the controlling owner's own name; for a pod no controller
// owns, its app.kubernetes.io/name or app label, or its own name.
func workloadOf(p *corev1.Pod) string {
	for _, ref := range p.OwnerReferences {
		if ref.Controller == nil || !*ref.Controller {
			continue
		}
		if ref.Kind == "ReplicaSet" {
			if hash := p.Labels["pod-template-hash"]; hash != "" {
				if name, ok := strings.CutSuffix(ref.Name, "-"+hash); ok {
					return name
				}
			}
		}
		return ref.Name
	}
	for _, key := range []string{"app.kubernetes.io/name", "app"} {
		if v := p.Labels[key]; v != "" {
			return v
		}
	}
	return p.Name
}

func protocolOf(p corev1.Protocol) string {
	if p == "" {
		return "TCP"
	}
	return string(p)
}

// compile turns one NetworkPolicy into a policy.
func compile(np *networkingv1.NetworkPolicy) policy {
	p := policy{Kind: "NetworkPolicy", Namespace: np.Namespace, Name: np.Name, Selector: metav1.FormatLabelSelector(&np.Spec.PodSelector)}
	if p.Selector == "<none>" {
		p.Selector = ""
	}
	sel, err := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
	if err != nil {
		p.Broken = append(p.Broken, "its podSelector: "+err.Error())
	} else {
		p.selector = sel
	}
	if len(np.Spec.PolicyTypes) == 0 {
		p.Ingress = true
		p.Egress = len(np.Spec.Egress) > 0
	}
	for _, t := range np.Spec.PolicyTypes {
		switch t {
		case networkingv1.PolicyTypeIngress:
			p.Ingress = true
		case networkingv1.PolicyTypeEgress:
			p.Egress = true
		}
	}
	if p.Ingress {
		for i, r := range np.Spec.Ingress {
			p.In = append(p.In, compileRule(&p, i, r.From, r.Ports))
		}
	}
	if p.Egress {
		for i, r := range np.Spec.Egress {
			p.Out = append(p.Out, compileRule(&p, i, r.To, r.Ports))
		}
	}
	return p
}

func compileRule(p *policy, index int, peers []networkingv1.NetworkPolicyPeer, ports []networkingv1.NetworkPolicyPort) rule {
	r := rule{Index: index, Everyone: len(peers) == 0, AnyPort: len(ports) == 0}
	for _, each := range peers {
		var out peer
		switch {
		case each.IPBlock != nil:
			out.CIDR = each.IPBlock.CIDR
			_, block, err := net.ParseCIDR(each.IPBlock.CIDR)
			if err != nil {
				p.Broken = append(p.Broken, fmt.Sprintf("rule %d: the CIDR %q", index+1, each.IPBlock.CIDR))
			} else {
				out.block = block
			}
			for _, e := range each.IPBlock.Except {
				out.Except = append(out.Except, e)
				if _, n, err := net.ParseCIDR(e); err == nil {
					out.except = append(out.except, n)
				}
			}
		case each.PodSelector != nil || each.NamespaceSelector != nil:
			out.Pods = true
			if each.PodSelector != nil {
				out.PodText = selectorText(each.PodSelector)
				s, err := metav1.LabelSelectorAsSelector(each.PodSelector)
				if err != nil {
					p.Broken = append(p.Broken, fmt.Sprintf("rule %d: a podSelector: %v", index+1, err))
					s = labels.Nothing()
				}
				out.podSelector = s
			}
			if each.NamespaceSelector != nil {
				out.NSText = selectorText(each.NamespaceSelector)
				s, err := metav1.LabelSelectorAsSelector(each.NamespaceSelector)
				if err != nil {
					p.Broken = append(p.Broken, fmt.Sprintf("rule %d: a namespaceSelector: %v", index+1, err))
					s = labels.Nothing()
				}
				out.nsSelector = s
			}
		default:
			// An element with nothing set is refused by the API server; one
			// that got through matches nothing.
			continue
		}
		r.Peers = append(r.Peers, out)
	}
	for _, port := range ports {
		spec := portSpec{Protocol: "TCP"}
		if port.Protocol != nil {
			spec.Protocol = string(*port.Protocol)
		}
		switch {
		case port.Port == nil:
			spec.Any = true
		case port.Port.StrVal != "" && port.Port.IntVal == 0:
			spec.Name = port.Port.StrVal
		default:
			spec.Num = port.Port.IntVal
			spec.End = spec.Num
			if port.EndPort != nil && *port.EndPort > spec.Num {
				spec.End = *port.EndPort
			}
		}
		r.Ports = append(r.Ports, spec)
	}
	return r
}

// selectorText is a selector as kubectl prints it, "all" for an empty one.
func selectorText(s *metav1.LabelSelector) string {
	text := metav1.FormatLabelSelector(s)
	if text == "<none>" || text == "" {
		return "all"
	}
	return text
}

// nsLabels is a namespace's labels, or the one the API server always sets.
func (m *model) nsLabels(name string) labels.Set {
	if set, ok := m.Namespaces[name]; ok {
		return set
	}
	return labels.Set{"kubernetes.io/metadata.name": name}
}

// matchesIdentity is whether a pod peer of a policy in namespace policyNS
// designates identity id.
func (m *model) matchesIdentity(pe *peer, policyNS string, id int) bool {
	if !pe.Pods {
		return false
	}
	ident := &m.Identities[id]
	if pe.custom != nil {
		return pe.custom(m, id)
	}
	if pe.cilium {
		if pe.implicitNS != "" && ident.Namespace != pe.implicitNS {
			return false
		}
		return pe.epSelector.Matches(ident.EP)
	}
	if pe.nsSelector == nil {
		if ident.Namespace != policyNS {
			return false
		}
	} else if !pe.nsSelector.Matches(m.nsLabels(ident.Namespace)) {
		return false
	}
	return pe.podSelector == nil || pe.podSelector.Matches(ident.Labels)
}

// peerIdentities is every identity a pod peer designates: its own namespace's
// for a pod selector alone, the matching namespaces' otherwise.
func (m *model) peerIdentities(pe *peer, policyNS string) []int {
	var out []int
	m.eachPeerIdentity(pe, policyNS, func(id int) { out = append(out, id) })
	return out
}

// eachPeerIdentity calls f for every identity a pod peer designates, with
// nothing allocated: the graph walks every rule of every policy through it.
func (m *model) eachPeerIdentity(pe *peer, policyNS string, f func(id int)) {
	if !pe.Pods {
		return
	}
	if pe.custom != nil {
		for id := range m.Identities {
			if pe.custom(m, id) {
				f(id)
			}
		}
		return
	}
	if pe.cilium {
		if pe.implicitNS != "" {
			for _, id := range m.byNamespace[pe.implicitNS] {
				if pe.epSelector.Matches(m.Identities[id].EP) {
					f(id)
				}
			}
			return
		}
		for id := range m.Identities {
			if pe.epSelector.Matches(m.Identities[id].EP) {
				f(id)
			}
		}
		return
	}
	if pe.nsSelector == nil {
		for _, id := range m.byNamespace[policyNS] {
			if pe.podSelector == nil || pe.podSelector.Matches(m.Identities[id].Labels) {
				f(id)
			}
		}
		return
	}
	for ns, ids := range m.byNamespace {
		if !pe.nsSelector.Matches(m.nsLabels(ns)) {
			continue
		}
		for _, id := range ids {
			if pe.podSelector == nil || pe.podSelector.Matches(m.Identities[id].Labels) {
				f(id)
			}
		}
	}
}

// inBlock is whether ip is in the peer's CIDR and in none of its excepts.
func (pe *peer) inBlock(ip net.IP) bool {
	if pe.block == nil || ip == nil || !pe.block.Contains(ip) {
		return false
	}
	for _, e := range pe.except {
		if e.Contains(ip) {
			return false
		}
	}
	return true
}

// --- the verdict -------------------------------------------------------------

// endpoint is one end of a flow: a pod, or an address outside every pod.
type endpoint struct {
	pod int // -1 for an address
	ip  net.IP
}

// Side states.
const (
	sideOpen    = "open"    // not isolated in this direction: everything passes
	sideAllowed = "allowed" // isolated, and a rule allows it
	sideDenied  = "denied"  // isolated, and no rule allows it
	sideDepends = "depends" // the network plugin decides
	sideOutside = "outside" // not a pod: no policy of this cluster applies
	// sideUnevaluated: only a rule this engine does not evaluate could allow it.
	sideUnevaluated = "unevaluated"
)

// Side is one direction's answer: the source's egress or the destination's
// ingress.
type Side struct {
	Direction string `json:"direction"`
	Pod       string `json:"pod"`
	State     string `json:"state"`
	// Isolating are the policies that select the pod in this direction.
	Isolating []PolicyRef `json:"isolating"`
	// Decided is the rule that allows it, when one does.
	Decided *RuleRef `json:"decided,omitempty"`
	Reason  string   `json:"reason"`
	Caveats []string `json:"caveats"`
	// noFix: no NetworkPolicy to copy would lift this refusal.
	noFix bool
}

// PolicyRef names a policy.
type PolicyRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// PolicyKind is set for Calico and AdminNetworkPolicy objects only.
	PolicyKind string `json:"policyKind,omitempty"`
}

// RuleRef is one rule of one policy, as the page names it.
type RuleRef struct {
	PolicyRef
	// Kind is "allow", "deny" or "unevaluated".
	Kind string `json:"kind"`
	// Rule is one-based, as a person counts the list in the YAML.
	Rule int    `json:"rule"`
	Text string `json:"text"`
}

// flowPort is the port a flow asks about, resolved on its destination.
type flowPort struct {
	Num      int32
	Protocol string
}

// side answers one direction of a flow from `from` to `to` on port.
//
// The order is Cilium's: a deny rule that matches wins over everything; then
// a direction no policy isolates is open; then an allow rule that matches; a
// rule this engine cannot decide (toFQDNs, layer-7 rules…) makes the answer
// "not evaluated", never allowed or denied; an ipBlock over a pod's own
// address leaves it to the plugin; otherwise it is refused.
func (m *model) side(direction string, from, to endpoint, port flowPort) Side {
	self, other := to, from
	if direction == "egress" {
		self, other = from, to
	}
	s := Side{Direction: direction, Isolating: []PolicyRef{}, Caveats: []string{}}
	if self.pod < 0 {
		s.State = sideOutside
		s.Pod = self.ip.String()
		s.Reason = "Not a pod of this cluster: no policy here decides its " + direction + "."
		return s
	}
	p := &m.Pods[self.pod]
	s.Pod = p.Namespace + "/" + p.Name
	if p.HostNetwork {
		s.State = sideDepends
		s.Reason = "This pod uses its node's network (hostNetwork): what NetworkPolicy does to it is the network plugin's to decide."
		return s
	}
	ident := &m.Identities[p.identity]
	if m.unreadable > 0 {
		s.Caveats = append(s.Caveats, fmt.Sprintf("%d Calico or AdminNetworkPolicy %s a selector or a setting this engine cannot read and is not evaluated: the answer may be incomplete.",
			m.unreadable, plural(m.unreadable, "object has", "objects have")))
	}
	// The layers, in the order the network plugins combine them: an
	// AdminNetworkPolicy decides first, then Calico's tiers (which hold the
	// NetworkPolicies), else NetworkPolicy and Cilium, and only when nothing
	// of those selects the pod a BaselineAdminNetworkPolicy.
	if l, ok := m.adminLayer(direction, ident, self, other, from, to, port, false); ok {
		return l.apply(s)
	}
	if l, ok := m.calicoLayer(direction, ident, self, other, from, to, port); ok {
		return l.apply(s)
	}
	s = m.policySide(s, direction, ident, other, to, port)
	if s.State == sideOpen {
		if l, ok := m.adminLayer(direction, ident, self, other, from, to, port, true); ok {
			return l.apply(s)
		}
	}
	return s
}

// plain keeps the policies the NetworkPolicy and Cilium layer evaluates.
func (m *model) plain(list []int) []int {
	var out []int
	for _, pi := range list {
		if m.Policies[pi].family == "" {
			out = append(out, pi)
		}
	}
	return out
}

// policySide is the NetworkPolicy and Cilium layer of one direction.
func (m *model) policySide(s Side, direction string, ident *identity, other, to endpoint, port flowPort) Side {
	isolating, also := m.plain(ident.In), m.plain(ident.InAlso)
	if direction == "egress" {
		isolating, also = m.plain(ident.Out), m.plain(ident.OutAlso)
	}
	var allowed, denied, opaque, dependsOn *RuleRef
	opaqueWhy := ""
	consider := func(pi int) {
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
			ref := &RuleRef{PolicyRef: PolicyRef{Namespace: pol.Namespace, Name: pol.Name}, Kind: ruleKind(r), Rule: r.Index + 1, Text: ruleText(r)}
			if len(r.Opaque) > 0 {
				if r.Deny {
					s.Caveats = append(s.Caveats, "A deny rule of "+pol.Name+" uses "+strings.Join(r.Opaque, ", ")+", not evaluated here.")
				} else if opaque == nil {
					opaque, opaqueWhy = ref, strings.Join(r.Opaque, ", ")
				}
				continue
			}
			match, ipOnly := m.ruleMatches(r, pol, other)
			switch {
			case match && r.Deny:
				if denied == nil {
					denied = ref
				}
			case match && len(r.L7) > 0:
				if opaque == nil {
					opaque, opaqueWhy = ref, strings.Join(r.L7, ", ")
				}
			case match:
				if allowed == nil {
					allowed = ref
				}
			case ipOnly && dependsOn == nil:
				dependsOn = ref
			}
		}
	}
	for _, pi := range isolating {
		pol := &m.Policies[pi]
		s.Isolating = append(s.Isolating, PolicyRef{Namespace: pol.Namespace, Name: pol.Name})
		consider(pi)
	}
	for _, pi := range also {
		consider(pi)
	}
	if denied == nil && allowed == nil && other.pod < 0 && len(isolating) > 0 && m.namesAPIServer(append(append([]int{}, isolating...), also...), direction) {
		s.Caveats = append(s.Caveats, "A rule allows the kube-apiserver entity, whose addresses are not known here: if this address is the API server's, it is allowed.")
	}
	switch {
	case denied != nil:
		s.State, s.Decided = sideDenied, denied
		s.Reason = "Denied by " + denied.Name + ", " + direction + " deny rule " + strconv.Itoa(denied.Rule) + ": a deny wins over every allow."
	case len(isolating) == 0:
		s.State = sideOpen
		s.Reason = "No policy selects this pod for " + direction + ": everything passes in this direction."
	case allowed != nil:
		s.State, s.Decided = sideAllowed, allowed
		s.Reason = "Allowed by " + allowed.Name + ", " + direction + " rule " + strconv.Itoa(allowed.Rule) + "."
	case opaque != nil:
		s.State, s.Decided = sideUnevaluated, opaque
		s.Reason = "Only " + opaque.Name + ", rule " + strconv.Itoa(opaque.Rule) + ", could allow it, and it uses " + opaqueWhy + ", which is not evaluated here."
	case dependsOn != nil:
		s.State = sideDepends
		s.Reason = "No rule selects this peer as a pod, but an ipBlock of " + dependsOn.Name + " (rule " +
			strconv.Itoa(dependsOn.Rule) + ") covers its address: whether it applies to a pod's address is the network plugin's to decide."
		s.Caveats = append(s.Caveats, "ipBlock is meant for addresses outside the cluster; against a pod's address the result depends on the network plugin.")
	default:
		s.State = sideDenied
		s.Reason = "Isolated for " + direction + " by " + refNames(s.Isolating) + ", and no rule of theirs allows this peer on this port."
	}
	return s
}

// ruleKind is "allow", "deny" or "unevaluated", as the page draws a rule.
func ruleKind(r *rule) string {
	switch {
	case len(r.Opaque) > 0 || len(r.L7) > 0:
		return "unevaluated"
	case r.Deny:
		return "deny"
	case r.Action == "Pass":
		return "pass"
	}
	return "allow"
}

// ruleMatches is whether a rule's peers designate other. ipOnly reports a
// pod matched only by a NetworkPolicy ipBlock covering its address. The
// kube-apiserver entity matches no address: which addresses are the API
// server's is not read here, and the side says so (apiserverCaveat).
func (m *model) ruleMatches(r *rule, pol *policy, other endpoint) (match, ipOnly bool) {
	if r.Everyone {
		return true, false
	}
	for i := range r.Peers {
		pe := &r.Peers[i]
		if pe.Entity != "" {
			if m.entityMatches(pe.Entity, other) {
				return true, false
			}
			continue
		}
		if other.pod < 0 {
			if pe.inBlock(other.ip) {
				return true, false
			}
			continue
		}
		p := &m.Pods[other.pod]
		if m.matchesIdentity(pe, pol.Namespace, p.identity) {
			return true, false
		}
		// A Cilium CIDR never selects a pod of the cluster; a NetworkPolicy
		// ipBlock over a pod's address is the plugin's call.
		if !pe.cidrCilium && pe.inBlock(net.ParseIP(p.IP)) {
			ipOnly = true
		}
	}
	return false, ipOnly
}

// namesAPIServer is whether an allow rule of these policies, in direction,
// names the kube-apiserver entity.
func (m *model) namesAPIServer(policies []int, direction string) bool {
	for _, pi := range policies {
		rules := m.Policies[pi].In
		if direction == "egress" {
			rules = m.Policies[pi].Out
		}
		for _, r := range rules {
			for _, pe := range r.Peers {
				if pe.Entity == "kube-apiserver" && !r.Deny {
					return true
				}
			}
		}
	}
	return false
}

// entityMatches is whether a Cilium entity designates the other end: a pod
// is in "cluster" and "all" (a hostNetwork pod is also "host" or
// "remote-node", which node is not known here); an address outside every pod
// is "world" and "all".
func (m *model) entityMatches(entity string, other endpoint) bool {
	switch entity {
	case "all":
		return true
	case "cluster":
		return other.pod >= 0
	case "world":
		return other.pod < 0
	case "host", "remote-node":
		return other.pod >= 0 && m.Pods[other.pod].HostNetwork
	}
	return false
}

// portMatches is whether a rule's ports admit the flow's port, a named port
// resolved on the destination pod.
func (m *model) portMatches(r *rule, port flowPort, to endpoint) bool {
	if r.AnyPort {
		return true
	}
	for _, spec := range r.Ports {
		if spec.Protocol != port.Protocol && spec.Protocol != "ANY" {
			continue
		}
		switch {
		case spec.Any:
			return true
		case spec.Name != "":
			if to.pod < 0 {
				continue
			}
			if named, ok := m.Pods[to.pod].Ports[spec.Name]; ok && named.Port == port.Num && (named.Protocol == port.Protocol || spec.Protocol == "ANY") {
				return true
			}
		case port.Num >= spec.Num && port.Num <= spec.End:
			return true
		}
	}
	return false
}

// ruleText is a rule in one line: "from app=web in any namespace, on http".
func ruleText(r *rule) string {
	peers := "everyone"
	if !r.Everyone && len(r.Peers) == 0 {
		peers = "no one"
		if len(r.Opaque) > 0 {
			peers = strings.Join(r.Opaque, ", ")
		}
	} else if !r.Everyone {
		parts := make([]string, 0, len(r.Peers))
		for i := range r.Peers {
			parts = append(parts, r.Peers[i].Label())
		}
		peers = strings.Join(parts, " or ")
	}
	ports := "every port"
	if !r.AnyPort {
		parts := make([]string, 0, len(r.Ports))
		for _, p := range r.Ports {
			parts = append(parts, p.String())
		}
		ports = strings.Join(parts, ", ")
	}
	text := peers + ", on " + ports
	if r.Deny {
		text = "deny " + text
	} else if r.Action == "Pass" {
		text = "pass " + text
	}
	if len(r.Opaque) > 0 || len(r.L7) > 0 {
		text += " [not evaluated: " + strings.Join(append(append([]string{}, r.Opaque...), r.L7...), ", ") + "]"
	}
	return text
}

// Label is a peer in a few words.
func (pe *peer) Label() string {
	if pe.custom != nil {
		return pe.PodText
	}
	if pe.Entity != "" {
		return "entity " + pe.Entity
	}
	if pe.cilium {
		text := pe.PodText
		if pe.implicitNS != "" {
			return "endpoints " + text + " in this namespace"
		}
		return "endpoints " + text
	}
	if !pe.Pods {
		text := pe.CIDR
		if len(pe.Except) > 0 {
			text += " except " + strings.Join(pe.Except, ", ")
		}
		return text
	}
	switch {
	case pe.nsSelector == nil:
		return "pods " + pe.PodText + " in this namespace"
	case pe.podSelector == nil:
		if pe.NSText == "all" {
			return "every pod of every namespace"
		}
		return "every pod in " + pe.namespaces()
	}
	return "pods " + pe.PodText + " in " + pe.namespaces()
}

// namespaces is a namespace selector in words: "namespace shop" for the one
// label the API server sets on every namespace, "any namespace" for an empty
// one.
func (pe *peer) namespaces() string {
	switch {
	case pe.NSText == "all":
		return "any namespace"
	case strings.HasPrefix(pe.NSText, "kubernetes.io/metadata.name=") && !strings.Contains(pe.NSText, ","):
		return "namespace " + strings.TrimPrefix(pe.NSText, "kubernetes.io/metadata.name=")
	}
	return "namespaces " + pe.NSText
}

func refNames(refs []PolicyRef) string {
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		names = append(names, r.Name)
	}
	if len(names) > 3 {
		return strings.Join(names[:3], ", ") + fmt.Sprintf(" and %d more", len(names)-3)
	}
	return strings.Join(names, ", ")
}

package netinspect

import "sort"

// The namespace matrix's diagonal, a namespace to itself, as the policy
// engine answers it: the same layers, in the same order and precedence, as a
// flow the person asks about (AdminNetworkPolicy priority and Pass, Calico's
// tiers and rule order, NetworkPolicy and Cilium, then the baseline). Counts
// of rules cannot carry that precedence; the engine's own verdict does.
//
// It is asked for every identity of the namespace, each to each: one pod
// stands for the pods with its labels. A rule that names pod addresses (an
// ipBlock, Calico nets) can tell two such pods apart, and its answer for the
// others is not asked. Each pair is asked on every port whose answer can
// differ: the ports the destination declares, and the bounds of every port and
// range the rules of either end name, in every layer (AdminNetworkPolicy and
// baseline subjects, Calico's notPorts included), plus the lowest port of each
// protocol. Between two of those bounds every port meets the same rules, so it
// answers as the bound does. A namespace too large to ask in full is marked
// partial, never given a verdict drawn from a sample.

// Diagonal states, as the matrix draws them.
const (
	diagInside      = "inside"      // some flow between its pods is allowed by a rule
	diagOpen        = "open"        // nothing isolates them: every flow passes
	diagMixed       = "mixed"       // some flows pass unruled, others are refused
	diagDenied      = "denied"      // a deny rule refuses them
	diagRefused     = "refused"     // isolated on ingress, and no rule lets them in
	diagRefusedOut  = "refusedOut"  // isolated on egress, and no rule lets them out
	diagUnevaluated = "unevaluated" // only a rule this engine does not evaluate decides
	diagPartial     = "partial"     // too many pairs or ports to ask them all
)

const (
	// diagIdentities bounds the identities asked in full per namespace.
	diagIdentities = 48
	// diagPorts bounds the ports asked per pair.
	diagPorts = 64
	// diagBudget bounds the flows asked over the whole cluster, two sides
	// each; the namespaces past it, by name, are partial.
	diagBudget = 200_000
)

// diagonals answers each namespace with a pod that is not on its node's
// network, by name.
func (m *model) diagonals() map[string]string {
	reps := map[string][]int{}
	seen := map[int]bool{}
	for i := range m.Pods {
		p := &m.Pods[i]
		if p.HostNetwork || seen[p.identity] {
			continue
		}
		seen[p.identity] = true
		reps[p.Namespace] = append(reps[p.Namespace], i)
	}
	names := make([]string, 0, len(reps))
	for ns, pods := range reps {
		names = append(names, ns)
		// The same pod for an identity every time, whatever order the
		// listing gave: the first by name.
		sort.Slice(pods, func(a, b int) bool { return m.Pods[pods[a]].Name < m.Pods[pods[b]].Name })
	}
	sort.Strings(names)
	out := make(map[string]string, len(reps))
	budget := diagBudget
	for _, ns := range names {
		out[ns] = m.diagonalOf(reps[ns], &budget)
	}
	return out
}

// diagonalOf is the engine's answer for flows among pods, one per identity:
// a rule not evaluated is a mark of its own, never allowed; a namespace not
// asked in full is partial; then any flow allowed by a rule; then open where
// every flow is, and mixed where some are and others are refused; otherwise
// the refusal, a deny rule's before an isolation's.
func (m *model) diagonalOf(pods []int, budget *int) string {
	if len(pods) > diagIdentities {
		return diagPartial
	}
	ports := make([][][]flowPort, len(pods))
	cost := 0
	for i, a := range pods {
		ports[i] = make([][]flowPort, len(pods))
		for j, b := range pods {
			list, whole := m.diagPortsOf(a, b)
			if !whole {
				return diagPartial
			}
			ports[i][j] = list
			cost += len(list)
		}
	}
	if cost > *budget {
		return diagPartial
	}
	*budget -= cost
	var allowed, open, unevaluated, denied, refused, refusedOut bool
	for i, a := range pods {
		for j, b := range pods {
			from, to := endpoint{pod: a}, endpoint{pod: b}
			for _, port := range ports[i][j] {
				in := m.side("ingress", from, to, port)
				out := m.side("egress", from, to, port)
				switch {
				case denies(in) || denies(out):
					denied = true
				case decidedUnevaluated(in) || decidedUnevaluated(out):
					unevaluated = true
				case in.State == sideDenied:
					refused = true
				case out.State == sideDenied:
					refusedOut = true
				case !passes(in) || !passes(out):
					unevaluated = true
				case in.State == sideOpen && out.State == sideOpen:
					open = true
				default:
					allowed = true
				}
			}
		}
	}
	switch {
	case unevaluated:
		return diagUnevaluated
	case allowed:
		return diagInside
	case open && !denied && !refused && !refusedOut:
		return diagOpen
	case open:
		return diagMixed
	case denied:
		return diagDenied
	case refused:
		return diagRefused
	case refusedOut:
		return diagRefusedOut
	}
	return diagUnevaluated
}

// denies is a side a deny rule refused, as opposed to an isolation no rule
// lifted.
func denies(s Side) bool {
	return s.State == sideDenied && s.Decided != nil && s.Decided.Kind == "deny"
}

// decidedUnevaluated is a side refused by a rule whose peers this engine does
// not evaluate (an AdminNetworkPolicy Deny to a peer it cannot read): a
// refusal, but not one the engine can attribute.
func decidedUnevaluated(s Side) bool {
	return s.State == sideDenied && s.Decided != nil && s.Decided.Kind == "unevaluated"
}

// passes is a side that lets the flow through.
func passes(s Side) bool {
	return s.State == sideOpen || s.State == sideAllowed
}

// flowProtocols are the protocols a flow is asked on.
var flowProtocols = map[string]bool{"TCP": true, "UDP": true, "SCTP": true}

// diagPortsOf is every port whose answer can differ for a flow from one pod to
// another: the destination's declared ports and the port after each, the
// first port and the port after the last of every range the rules of either
// end name (the source's egress, the destination's ingress, every layer,
// Calico's notPorts included), and port 1, for each protocol in play: TCP,
// and every protocol a port or a rule names (all three under a Calico
// notProtocol). whole is false past diagPorts.
func (m *model) diagPortsOf(from, to int) (ports []flowPort, whole bool) {
	protocols := map[string]bool{"TCP": true}
	var bounds []portSpec
	dst := &m.Pods[to]
	for _, np := range dst.Ports {
		protocol := np.Protocol
		if protocol == "" {
			protocol = "TCP"
		}
		protocols[protocol] = true
		bounds = append(bounds, portSpec{Protocol: protocol, Num: np.Port, End: np.Port})
	}
	src, dstIdent := &m.Identities[m.Pods[from].identity], &m.Identities[dst.identity]
	for _, list := range [][]int{src.Out, src.OutAlso, dstIdent.In, dstIdent.InAlso} {
		for _, pi := range list {
			pol := &m.Policies[pi]
			for _, rules := range [][]rule{pol.In, pol.Out} {
				for ri := range rules {
					r := &rules[ri]
					specs := r.Ports
					if c := r.cal; c != nil {
						// A Calico rule keeps its protocol apart, its ports
						// under ANY, and the ports it excludes in notPorts.
						for _, p := range c.proto {
							if flowProtocols[p] {
								protocols[p] = true
							}
						}
						if len(c.notProto) > 0 {
							for p := range flowProtocols {
								protocols[p] = true
							}
						}
						specs = append(specs[:len(specs):len(specs)], c.notPorts...)
					}
					for _, spec := range specs {
						if spec.Protocol != "" && spec.Protocol != "ANY" {
							protocols[spec.Protocol] = true
						}
						if !spec.Any && spec.Name == "" {
							bounds = append(bounds, spec)
						}
					}
				}
			}
		}
	}
	names := make([]string, 0, len(protocols))
	for p := range protocols {
		names = append(names, p)
	}
	sort.Strings(names)
	seen := map[flowPort]bool{}
	add := func(num int32, protocol string) {
		fp := flowPort{Num: num, Protocol: protocol}
		if num < 1 || num > 65535 || seen[fp] {
			return
		}
		seen[fp] = true
		ports = append(ports, fp)
	}
	for _, protocol := range names {
		add(1, protocol)
		for _, b := range bounds {
			if b.Protocol != protocol && b.Protocol != "ANY" && b.Protocol != "" {
				continue
			}
			end := b.End
			if end < b.Num {
				end = b.Num
			}
			add(b.Num, protocol)
			add(end+1, protocol)
		}
	}
	sort.Slice(ports, func(a, b int) bool {
		if ports[a].Protocol != ports[b].Protocol {
			return ports[a].Protocol < ports[b].Protocol
		}
		return ports[a].Num < ports[b].Num
	})
	return ports, len(ports) <= diagPorts
}

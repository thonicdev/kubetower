package netinspect

import "sort"

// The namespace matrix's diagonal, a namespace to itself, as the policy
// engine answers it: the same layers, in the same order and precedence, as a
// flow the person asks about (AdminNetworkPolicy priority and Pass, Calico's
// tiers and rule order, NetworkPolicy and Cilium, then the baseline). Counts
// of rules cannot carry that precedence; the engine's own verdict does.
//
// It is asked for a few pods of the namespace, one per identity (pods with the
// same labels answer alike), each to each, on the ports a flow between them
// could use: the destination's declared ports and the ports the rules
// isolating it name, or one probe port when there are none.

// Diagonal states, as the matrix draws them.
const (
	diagInside      = "inside"      // some flow between its pods is allowed by a rule
	diagOpen        = "open"        // nothing isolates them: every flow passes
	diagDenied      = "denied"      // a deny rule refuses them
	diagRefused     = "refused"     // isolated on ingress, and no rule lets them in
	diagRefusedOut  = "refusedOut"  // isolated on egress, and no rule lets them out
	diagUnevaluated = "unevaluated" // only a rule this engine does not evaluate decides
)

const (
	// diagIdentities bounds the pods asked per namespace: one per identity.
	diagIdentities = 4
	// diagPorts bounds the ports asked per pair.
	diagPorts = 8
)

// diagonals answers each namespace with a pod that is not on its node's
// network, by name.
func (m *model) diagonals() map[string]string {
	order := make([]int, len(m.Pods))
	for i := range order {
		order[i] = i
	}
	// The same pods every time, whatever order the listing gave.
	sort.SliceStable(order, func(a, b int) bool {
		pa, pb := &m.Pods[order[a]], &m.Pods[order[b]]
		if pa.Namespace != pb.Namespace {
			return pa.Namespace < pb.Namespace
		}
		return pa.Name < pb.Name
	})
	reps := map[string][]int{}
	seen := map[int]bool{}
	for _, i := range order {
		p := &m.Pods[i]
		if p.HostNetwork || seen[p.identity] || len(reps[p.Namespace]) >= diagIdentities {
			continue
		}
		seen[p.identity] = true
		reps[p.Namespace] = append(reps[p.Namespace], i)
	}
	out := make(map[string]string, len(reps))
	for ns, pods := range reps {
		out[ns] = m.diagonalOf(pods)
	}
	return out
}

// diagonalOf is the engine's answer for flows among pods: a rule not
// evaluated anywhere is a mark of its own, never allowed; then any flow
// allowed by a rule; then open where every flow is; otherwise the first
// refusal, a deny rule's before an isolation's.
func (m *model) diagonalOf(pods []int) string {
	var allowed, open, unevaluated, denied, refused, refusedOut bool
	for _, a := range pods {
		for _, b := range pods {
			from, to := endpoint{pod: a}, endpoint{pod: b}
			for _, port := range m.diagPortsOf(b) {
				in := m.side("ingress", from, to, port)
				out := m.side("egress", from, to, port)
				switch {
				case denies(in) || denies(out):
					denied = true
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
		return diagInside
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

// passes is a side that lets the flow through.
func passes(s Side) bool {
	return s.State == sideOpen || s.State == sideAllowed
}

// diagPortsOf is the ports a flow to pod could use: its declared ports, and
// the ports the rules isolating it name, at most diagPorts; one probe port
// where there are none.
func (m *model) diagPortsOf(pod int) []flowPort {
	p := &m.Pods[pod]
	seen := map[flowPort]bool{}
	var out []flowPort
	add := func(fp flowPort) {
		if fp.Num <= 0 || seen[fp] || len(out) >= diagPorts {
			return
		}
		seen[fp] = true
		out = append(out, fp)
	}
	names := make([]string, 0, len(p.Ports))
	for name := range p.Ports {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		np := p.Ports[name]
		add(flowPort{Num: np.Port, Protocol: np.Protocol})
	}
	ident := &m.Identities[p.identity]
	for _, list := range [][]int{ident.In, ident.Out} {
		for _, pi := range list {
			pol := &m.Policies[pi]
			for _, rules := range [][]rule{pol.In, pol.Out} {
				for ri := range rules {
					for _, spec := range rules[ri].Ports {
						protocol := spec.Protocol
						if protocol == "" || protocol == "ANY" {
							protocol = "TCP"
						}
						add(flowPort{Num: spec.Num, Protocol: protocol})
					}
				}
			}
		}
	}
	if len(out) == 0 {
		out = append(out, flowPort{Num: 80, Protocol: "TCP"})
	}
	return out
}

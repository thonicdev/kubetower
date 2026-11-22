package detail

import (
	"net/netip"

	corev1 "k8s.io/api/core/v1"
)

// podAddressFacts is where a Pod answers on the network and the address of its
// node, both otherwise buried in the status.
//
// One row per address: a dual-stack pod gets Pod IPv4 and Pod IPv6, each with
// its own copy button; a single address is "Pod IP". `status.podIP` is read
// only when `podIPs` is empty. A pod with no address yet still gets both rows,
// empty, drawn as "—", so it does not look like a console that hides addresses.
func podAddressFacts(pod *corev1.Pod) []Fact {
	ips := make([]string, 0, len(pod.Status.PodIPs))
	for _, ip := range pod.Status.PodIPs {
		if ip.IP != "" {
			ips = append(ips, ip.IP)
		}
	}
	if len(ips) == 0 && pod.Status.PodIP != "" {
		ips = append(ips, pod.Status.PodIP)
	}

	facts := make([]Fact, 0, len(ips)+1)
	switch len(ips) {
	case 0:
		facts = append(facts, addressFact("Pod IP", ""))
	case 1:
		facts = append(facts, addressFact("Pod IP", ips[0]))
	default:
		for _, ip := range ips {
			facts = append(facts, addressFact("Pod "+familyOf(ip), ip))
		}
	}
	return append(facts, addressFact("Node IP", pod.Status.HostIP))
}

// addressFact is one address, copyable as itself. An empty one has nothing to
// copy and is drawn as "—".
func addressFact(label, ip string) Fact {
	return Fact{Label: label, Value: ip, Copy: ip}
}

// familyOf names an address's family for its row. An address that does not
// parse is called IPv4 rather than dropped, and shown as written.
func familyOf(ip string) string {
	if addr, err := netip.ParseAddr(ip); err == nil && addr.Is6() && !addr.Is4In6() {
		return "IPv6"
	}
	return "IPv4"
}

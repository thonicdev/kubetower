package resources

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
)

// --- networking ------------------------------------------------------------------

func serviceRow(s *corev1.Service, now time.Time) []Cell {
	return []Cell{
		text(s.Name),
		text(s.Namespace),
		text(string(s.Spec.Type)),
		// Copyable, not a link: it is reachable only from inside the cluster.
		copyable(text(s.Spec.ClusterIP), s.Spec.ClusterIP),
		externalAddress(s),
		text(servicePorts(s)),
		age(s.CreationTimestamp, now),
	}
}

// externalAddress answers "is this reachable from outside yet". A LoadBalancer
// without an ingress address is the one case worth coloring: the cloud
// controller has not finished, or cannot.
//
// Every address is copyable, and none is a link: a Service publishes a set of
// ports, so there is no single URL, and choosing http or https for a port is
// internal/forward's question, not to be answered twice. An Ingress does get
// links (see ingressHosts), because `spec.tls` answers the scheme.
func externalAddress(s *corev1.Service) Cell {
	addresses := ingressAddresses(s.Status.LoadBalancer.Ingress)
	addresses = append(addresses, s.Spec.ExternalIPs...)
	if len(addresses) > 0 {
		return copyable(text(JoinCapped(addresses, 2)), strings.Join(addresses, ", "))
	}
	if s.Spec.Type == corev1.ServiceTypeLoadBalancer {
		return pendingAddress("this is a LoadBalancer Service and status.loadBalancer is " +
			"empty, so nothing has assigned it an address. On a cluster with no " +
			"load-balancer controller it stays this way.")
	}
	return text("")
}

// pendingAddress is the word "pending" with the hint it needs. On a
// LoadBalancer Service or an Ingress it means `status.loadBalancer.ingress` was
// read and found empty — never that it could not be read (a failed list is an
// error, not a row) — and the hint says whose address is awaited.
func pendingAddress(why string) Cell {
	cell := graded("pending", ClassWarn, RankWarn)
	cell.Hint = "No address published yet: " + why
	return cell
}

func ingressAddresses(points []corev1.LoadBalancerIngress) []string {
	out := make([]string, 0, len(points))
	for _, p := range points {
		switch {
		case p.IP != "":
			out = append(out, p.IP)
		case p.Hostname != "":
			out = append(out, p.Hostname)
		}
	}
	return out
}

func servicePorts(s *corev1.Service) string {
	ports := make([]string, 0, len(s.Spec.Ports))
	for _, p := range s.Spec.Ports {
		port := fmt.Sprintf("%d/%s", p.Port, p.Protocol)
		if p.NodePort != 0 {
			port = fmt.Sprintf("%d:%d/%s", p.Port, p.NodePort, p.Protocol)
		}
		ports = append(ports, port)
	}
	return JoinCapped(ports, 4)
}

func ingressRow(i *networkingv1.Ingress, now time.Time) []Cell {
	return []Cell{
		text(i.Name),
		text(i.Namespace),
		text(ingressClass(i)),
		ingressHosts(i),
		ingressAddress(i),
		age(i.CreationTimestamp, now),
	}
}

// ingressHosts is the Hosts column, whose values are links.
//
// The scheme is read, not guessed: a host under `spec.tls` (see tlsHosts) is
// `https://`, any other `http://`. A wrong scheme reads as a broken Ingress.
//
// Not linked: a rule with no host (`*`), a wildcard host (a pattern, not a
// name), and the path (a rule has several; the host root is what "open" means).
//
// The clipboard gets every host, including those JoinCapped left out.
func ingressHosts(i *networkingv1.Ingress) Cell {
	tls := tlsHosts(i)

	hosts := make([]string, 0, len(i.Spec.Rules))
	for _, rule := range i.Spec.Rules {
		if rule.Host == "" {
			// A rule with no host matches everything reaching the controller.
			hosts = append(hosts, "*")
			continue
		}
		hosts = append(hosts, rule.Host)
	}

	cell := copyable(text(JoinCapped(hosts, ingressHostsShown)), strings.Join(hosts, ", "))
	if cell.Text == Missing {
		// No rules: nothing to link or copy.
		return cell
	}

	// The pieces, separators included, so their concatenation is the cell's
	// text byte for byte (see Cell.Addresses).
	shown := hosts
	if len(shown) > ingressHostsShown {
		shown = shown[:ingressHostsShown]
	}
	addresses := make([]Address, 0, 2*len(shown))
	for n, host := range shown {
		if n > 0 {
			addresses = append(addresses, Address{Text: ", "})
		}
		switch {
		case host == "*", strings.HasPrefix(host, "*."):
			// Not addresses; see above.
			addresses = append(addresses, Address{Text: host})
		case tls[host]:
			addresses = append(addresses, Address{Text: host, Href: "https://" + host})
		default:
			addresses = append(addresses, Address{Text: host, Href: "http://" + host})
		}
	}
	if len(hosts) > ingressHostsShown {
		// JoinCapped's own tail, after a space rather than a comma.
		addresses = append(addresses, Address{
			Text: fmt.Sprintf(" +%d more", len(hosts)-ingressHostsShown)})
	}
	cell.Addresses = addresses
	return cell
}

// ingressHostsShown is how many hosts the column prints, shared by the text
// (JoinCapped) and the anchors so they agree. A wildcard host counts too: the
// cap is about width.
const ingressHostsShown = 3

// tlsHosts is which of this Ingress's hosts are served over TLS. A TLS block
// with an empty `hosts` list applies to every host, as the Ingress API says,
// and is expanded here.
func tlsHosts(i *networkingv1.Ingress) map[string]bool {
	out := map[string]bool{}
	for _, block := range i.Spec.TLS {
		if len(block.Hosts) == 0 {
			for _, rule := range i.Spec.Rules {
				if rule.Host != "" {
					out[rule.Host] = true
				}
			}
			continue
		}
		for _, host := range block.Hosts {
			out[host] = true
		}
	}
	return out
}

// ingressAddress is the Address column: what a controller published for this
// Ingress, or the reason there is nothing. An Ingress naming a class waits for
// that controller; one naming none waits for the default IngressClass. Neither
// hint says the route is broken: a controller with status publishing off
// leaves this empty and still serves traffic.
//
// Copyable, not a link: dialled without the Host header, the address reaches
// the controller's default backend. The thing to open is the host.
func ingressAddress(i *networkingv1.Ingress) Cell {
	published := ingressAddresses(toLoadBalancerIngress(i.Status.LoadBalancer.Ingress))
	if cell := text(JoinCapped(published, 2)); cell.Text != Missing {
		return copyable(cell, strings.Join(published, ", "))
	}
	if class := ingressClass(i); class != "" {
		return pendingAddress("status.loadBalancer is empty, so the " + strconv.Quote(class) +
			" controller has not adopted this Ingress or does not publish addresses.")
	}
	return pendingAddress("status.loadBalancer is empty, and this Ingress names no ingress " +
		"class — only a controller whose IngressClass is marked the cluster default will " +
		"adopt it.")
}

// toLoadBalancerIngress adapts networking's ingress-point type to core's, so
// one address formatter serves both Services and Ingresses.
func toLoadBalancerIngress(points []networkingv1.IngressLoadBalancerIngress) []corev1.LoadBalancerIngress {
	out := make([]corev1.LoadBalancerIngress, 0, len(points))
	for _, p := range points {
		out = append(out, corev1.LoadBalancerIngress{IP: p.IP, Hostname: p.Hostname})
	}
	return out
}

func ingressClass(i *networkingv1.Ingress) string {
	if i.Spec.IngressClassName != nil && *i.Spec.IngressClassName != "" {
		return *i.Spec.IngressClassName
	}
	// The legacy annotation, still used by older charts.
	return i.Annotations["kubernetes.io/ingress.class"]
}

func endpointsRow(e *corev1.Endpoints, now time.Time) []Cell {
	var addresses []string
	var notReady int
	for _, subset := range e.Subsets {
		notReady += len(subset.NotReadyAddresses)
		for _, a := range subset.Addresses {
			if len(subset.Ports) == 0 {
				addresses = append(addresses, a.IP)
				continue
			}
			addresses = append(addresses, fmt.Sprintf("%s:%d", a.IP, subset.Ports[0].Port))
		}
	}

	// A Service with nothing behind it is an outage that looks configured.
	list := graded(JoinCapped(addresses, 3), ClassNone, 0)
	if len(addresses) == 0 {
		list = graded("no endpoints", ClassBad, RankBad)
	}

	return []Cell{
		text(e.Name),
		text(e.Namespace),
		list,
		count(len(addresses)),
		count(notReady),
		age(e.CreationTimestamp, now),
	}
}

// endpointSliceRow asks endpointsRow's question — is there anything behind
// this Service — of the object the cluster actually keeps. It also says which
// Service (only the `kubernetes.io/service-name` label ties a slice back),
// which address family (one empty family of a dual-stack Service is an
// outage), and how a large Service is split, which Endpoints flattens and
// truncates silently.
func endpointSliceRow(s *discoveryv1.EndpointSlice, now time.Time) []Cell {
	var ready, total int32
	for _, endpoint := range s.Endpoints {
		addresses := int32(len(endpoint.Addresses))
		total += addresses
		// Nil is ready, per the API: unset means readiness is not tracked.
		if endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready {
			ready += addresses
		}
	}

	addresses := ratio(ready, total)
	if total == 0 {
		// Unlike ratio's 0/0, a slice with no addresses is an outage.
		addresses = Cell{Text: "0/0", Class: ClassBad, Rank: RankBad}
	}

	ports := make([]string, 0, len(s.Ports))
	for _, p := range s.Ports {
		protocol := string(corev1.ProtocolTCP)
		if p.Protocol != nil {
			protocol = string(*p.Protocol)
		}
		if p.Port == nil {
			// A nil port on a slice means all ports.
			ports = append(ports, "*/"+protocol)
			continue
		}
		ports = append(ports, fmt.Sprintf("%d/%s", *p.Port, protocol))
	}

	return []Cell{
		text(s.Name),
		text(s.Namespace),
		text(s.Labels[discoveryv1.LabelServiceName]),
		text(string(s.AddressType)),
		addresses,
		// Deduplicated: two port names on one number would print twice.
		text(JoinCapped(distinct(ports), 4)),
		age(s.CreationTimestamp, now),
	}
}

func ingressClassRow(c *networkingv1.IngressClass, now time.Time) []Cell {
	return []Cell{
		text(c.Name),
		text(c.Spec.Controller),
		defaultClass(c.Annotations, "ingressclass.kubernetes.io/is-default-class"),
		age(c.CreationTimestamp, now),
	}
}

// networkPolicyRow answers what a policy applies to and what it allows. A
// direction named in policyTypes with no rules denies all of it (DNS
// included), so an empty rule list reads "deny all" while an ungoverned
// direction is an em dash.
func networkPolicyRow(p *networkingv1.NetworkPolicy, now time.Time) []Cell {
	governs := map[networkingv1.PolicyType]bool{}
	for _, t := range p.Spec.PolicyTypes {
		governs[t] = true
	}
	if len(p.Spec.PolicyTypes) == 0 {
		// The API server's default: Ingress always, Egress only with egress rules.
		governs[networkingv1.PolicyTypeIngress] = true
		governs[networkingv1.PolicyTypeEgress] = len(p.Spec.Egress) > 0
	}

	return []Cell{
		text(p.Name),
		text(p.Namespace),
		// An empty pod selector is every pod in the namespace.
		text(selectorText(&p.Spec.PodSelector, "all pods")),
		policyRules(governs[networkingv1.PolicyTypeIngress], len(p.Spec.Ingress)),
		policyRules(governs[networkingv1.PolicyTypeEgress], len(p.Spec.Egress)),
		age(p.CreationTimestamp, now),
	}
}

func policyRules(governs bool, rules int) Cell {
	if !governs {
		// Not governed: this policy has no opinion on that direction.
		return Cell{Text: Missing}
	}
	if rules == 0 {
		return Cell{Text: "deny all", Class: ClassWarn}
	}
	return count(rules)
}

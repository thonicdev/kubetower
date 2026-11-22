package netinspect

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/thonicdev/kubetower/extension"
)

// servicePrefix marks a destination that is a Service: `svc:namespace/name`.
const servicePrefix = "svc:"

// maxBackends bounds the pods answered for one Service: past it the answer
// says how many matched and that the rest were not asked.
const maxBackends = 50

// ServiceTarget is a Service as the destination of a verdict: the port it was
// asked on, and each pod behind it with the port the traffic reaches there.
type ServiceTarget struct {
	Name string `json:"name"`
	// Port is the Service's port, PortName its name when it has one.
	Port     int32  `json:"port"`
	PortName string `json:"portName,omitempty"`
	Protocol string `json:"protocol"`
	// TargetPort is what the Service says (a number or a name), resolved per
	// pod in each backend.
	TargetPort string    `json:"targetPort"`
	Selector   string    `json:"selector"`
	Backends   []Backend `json:"backends"`
	// Total is how many running pods the selector matches; Backends holds
	// at most maxBackends of them.
	Total  int  `json:"total"`
	Capped bool `json:"capped"`
}

// Backend is one pod behind the Service, answered on its own target port.
type Backend struct {
	Pod     string `json:"pod"`
	Port    int32  `json:"port"`
	Answer  string `json:"answer"`
	Egress  Side   `json:"egress"`
	Ingress Side   `json:"ingress"`
}

// serviceRead reads the Service for the person: refused, failed and absent are
// three answers. The caller has read the model already.
func serviceRead(r *http.Request, h extension.Host, cluster, text string) (*corev1.Service, error) {
	ns, name, ok := strings.Cut(strings.TrimPrefix(text, servicePrefix), "/")
	if !ok || ns == "" || name == "" {
		return nil, &badQuestion{fmt.Sprintf("The destination Service is svc:namespace/name: %q.", text)}
	}
	page, err := h.List(r, cluster, extension.ListQuery{GVR: servicesGVR, Namespace: ns, FieldSelector: "metadata.name=" + name, Limit: 1})
	if err != nil {
		return nil, &readError{what: "services", err: err}
	}
	if len(page.Items) == 0 {
		return nil, &badQuestion{fmt.Sprintf("There is no Service %s/%s that you may list.", ns, name)}
	}
	var svc corev1.Service
	if err := convert(&page.Items[0], &svc); err != nil {
		return nil, &readError{what: "services", err: err}
	}
	return &svc, nil
}

// rank orders answers from the worst: the Service's answer is its worst
// backend's.
var rank = map[string]int{"no": 0, "unevaluated": 1, "depends": 2, "yes": 3}

// serviceVerdict answers from -> Service on one of its ports, asked as the
// Service's own port number or name.
func (rd *read) serviceVerdict(fromText string, svc *corev1.Service, portText, protocol string) (Verdict, error) {
	m := rd.model
	from, fromName, err := m.endpointOf(fromText, "source")
	if err != nil {
		return Verdict{}, err
	}
	full := svc.Namespace + "/" + svc.Name
	if len(svc.Spec.Selector) == 0 {
		return Verdict{}, &badQuestion{fmt.Sprintf("The Service %s selects no pod (it has no selector), so there is no backend to answer for.", full)}
	}
	protocol = strings.ToUpper(strings.TrimSpace(protocol))
	if protocol == "" {
		protocol = "TCP"
	}
	portText = strings.TrimSpace(portText)
	var sp *corev1.ServicePort
	for i := range svc.Spec.Ports {
		p := &svc.Spec.Ports[i]
		pp := string(p.Protocol)
		if pp == "" {
			pp = "TCP"
		}
		if (p.Name == portText || strconv.Itoa(int(p.Port)) == portText) && pp == protocol {
			sp = p
			break
		}
	}
	if sp == nil {
		return Verdict{}, &badQuestion{fmt.Sprintf("The Service %s has no %s port %q.", full, protocol, portText)}
	}
	target := sp.TargetPort
	if target.Type == intstr.Int && target.IntVal == 0 {
		target = intstr.FromInt32(sp.Port)
	}
	sel := labels.SelectorFromSet(svc.Spec.Selector)
	var matched []int
	for _, id := range m.byNamespace[svc.Namespace] {
		if !sel.Matches(m.Identities[id].Labels) {
			continue
		}
		matched = append(matched, m.Identities[id].Pods...)
	}
	// A pod that does not declare a named target port is no endpoint of the
	// Service on that port: it is not one of its backends.
	resolved := matched[:0:0]
	ports := map[int]int32{}
	for _, pi := range matched {
		switch {
		case target.Type == intstr.Int:
			ports[pi] = target.IntVal
		default:
			named, ok := m.Pods[pi].Ports[target.StrVal]
			if !ok {
				continue
			}
			ports[pi] = named.Port
		}
		resolved = append(resolved, pi)
	}
	if len(resolved) == 0 {
		return Verdict{}, &badQuestion{fmt.Sprintf("No running pod behind %s that you may list serves its port %s.", full, sp.TargetPort.String())}
	}
	sort.Slice(resolved, func(a, b int) bool { return m.Pods[resolved[a]].Name < m.Pods[resolved[b]].Name })
	total := len(resolved)
	if len(resolved) > maxBackends {
		resolved = resolved[:maxBackends]
	}

	st := &ServiceTarget{Name: full, Port: sp.Port, PortName: sp.Name, Protocol: protocol, TargetPort: sp.TargetPort.String(),
		Selector: labels.Set(svc.Spec.Selector).String(), Backends: make([]Backend, 0, len(resolved)), Total: total, Capped: total > len(resolved)}
	var out Verdict
	worst := -1
	var egressPorts, ingressPorts []flowPort
	seenE, seenI := map[int32]bool{}, map[int32]bool{}
	var anyE, anyI Side
	for _, pi := range resolved {
		port := flowPort{Num: ports[pi], Protocol: protocol}
		v := rd.judge(from, endpoint{pod: pi}, fromName, full, port)
		st.Backends = append(st.Backends, Backend{Pod: m.Pods[pi].Namespace + "/" + m.Pods[pi].Name, Port: port.Num, Answer: v.Answer, Egress: v.Egress, Ingress: v.Ingress})
		if worst < 0 || rank[v.Answer] < worst {
			worst = rank[v.Answer]
			out = v
		}
		if wants(v.Egress) {
			anyE = v.Egress
			if !seenE[port.Num] {
				seenE[port.Num] = true
				egressPorts = append(egressPorts, port)
			}
		}
		if wants(v.Ingress) {
			anyI = v.Ingress
			if !seenI[port.Num] {
				seenI[port.Num] = true
				ingressPorts = append(ingressPorts, port)
			}
		}
	}
	sort.Slice(egressPorts, func(a, b int) bool { return egressPorts[a].Num < egressPorts[b].Num })
	sort.Slice(ingressPorts, func(a, b int) bool { return ingressPorts[a].Num < ingressPorts[b].Num })
	out.To, out.Port, out.Protocol, out.Service = full, sp.Port, protocol, st
	refused := 0
	for _, b := range st.Backends {
		if b.Answer == "no" {
			refused++
		}
	}
	if refused > 0 && refused < len(st.Backends) {
		out.Summary = fmt.Sprintf("The rules refuse it for %d of %d pods behind the Service.", refused, len(st.Backends))
	}
	// The Service's pods are named by its own selector, so one policy covers
	// them whichever pod the Service sends to.
	dest := party{ns: svc.Namespace, labels: svc.Spec.Selector}
	out.Policy = buildPolicy(m.partyOf(from), dest, anyE, anyI, egressPorts, ingressPorts)
	return out, nil
}

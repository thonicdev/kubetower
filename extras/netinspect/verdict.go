package netinspect

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// --- the verdict -------------------------------------------------------------------

// Verdict is `GET /verdict`: would From reach To on Port.
type Verdict struct {
	Mode string `json:"mode"`
	// Answer is "yes", "no", "depends" (the plugin decides) or
	// "unevaluated" (only a rule this engine does not evaluate could allow it).
	Answer   string   `json:"answer"`
	Summary  string   `json:"summary"`
	From     string   `json:"from"`
	To       string   `json:"to"`
	Port     int32    `json:"port"`
	Protocol string   `json:"protocol"`
	Egress   Side     `json:"egress"`
	Ingress  Side     `json:"ingress"`
	Complete bool     `json:"complete"`
	Notes    []string `json:"notes"`
	CNI      CNI      `json:"cni"`
	// Unevaluated, when not empty, means the answer may be wrong: those
	// policies apply too.
	Unevaluated []Unevaluated `json:"unevaluated"`
	// Policy is a NetworkPolicy, or two, that would allow exactly this flow,
	// as YAML to copy; empty when no policy is to be offered (the flow is
	// not refused, or a deny rule refuses it, which no allow overrides).
	// Nothing here applies it.
	Policy string `json:"policy"`
	// Service is set when the destination is a Service: the answer is then
	// the worst of its backends'.
	Service *ServiceTarget `json:"service,omitempty"`
}

// badQuestion is a verdict asked of something that is not one.
type badQuestion struct{ msg string }

func (b *badQuestion) Error() string { return b.msg }

// endpointOf reads `namespace/name` or an IP address. An address that is a
// pod's is that pod.
func (m *model) endpointOf(text, which string) (endpoint, string, error) {
	text = strings.TrimSpace(text)
	if ip := net.ParseIP(text); ip != nil {
		if i, ok := m.ipIndex[ip.String()]; ok {
			return endpoint{pod: i}, m.Pods[i].Namespace + "/" + m.Pods[i].Name, nil
		}
		return endpoint{pod: -1, ip: ip}, ip.String(), nil
	}
	ns, name, ok := strings.Cut(text, "/")
	if !ok || ns == "" || name == "" {
		return endpoint{}, "", &badQuestion{fmt.Sprintf("The %s is neither namespace/pod nor an IP address: %q.", which, text)}
	}
	i, found := m.podIndex[ns+"/"+name]
	if !found {
		return endpoint{}, "", &badQuestion{fmt.Sprintf("There is no running pod %s/%s that you may list.", ns, name)}
	}
	return endpoint{pod: i}, ns + "/" + name, nil
}

// verdict answers from, to, port ("80", or a name the destination pod
// declares) and protocol ("" is TCP).
func (rd *read) verdict(fromText, toText, portText, protocol string) (Verdict, error) {
	m := rd.model
	from, fromName, err := m.endpointOf(fromText, "source")
	if err != nil {
		return Verdict{}, err
	}
	to, toName, err := m.endpointOf(toText, "destination")
	if err != nil {
		return Verdict{}, err
	}
	if from.pod < 0 && to.pod < 0 {
		return Verdict{}, &badQuestion{"Neither end is a pod of this cluster: no NetworkPolicy here can say anything about it."}
	}
	protocol = strings.ToUpper(strings.TrimSpace(protocol))
	if protocol == "" {
		protocol = "TCP"
	}
	if protocol != "TCP" && protocol != "UDP" && protocol != "SCTP" {
		return Verdict{}, &badQuestion{fmt.Sprintf("The protocol is TCP, UDP or SCTP, not %q.", protocol)}
	}
	port := flowPort{Protocol: protocol}
	portText = strings.TrimSpace(portText)
	if n, err := strconv.Atoi(portText); err == nil {
		if n < 1 || n > 65535 {
			return Verdict{}, &badQuestion{"A port is between 1 and 65535."}
		}
		port.Num = int32(n)
	} else {
		if to.pod < 0 || portText == "" {
			return Verdict{}, &badQuestion{"Give the port as a number, or as a name the destination pod declares."}
		}
		named, ok := m.Pods[to.pod].Ports[portText]
		if !ok {
			return Verdict{}, &badQuestion{fmt.Sprintf("The destination pod declares no port named %q.", portText)}
		}
		port = flowPort{Num: named.Port, Protocol: named.Protocol}
	}
	v := rd.judge(from, to, fromName, toName, port)
	v.Policy = rd.policyFor(&v, from, to, []flowPort{port})
	return v, nil
}

// judge answers one flow between two endpoints on a resolved port.
func (rd *read) judge(from, to endpoint, fromName, toName string, port flowPort) Verdict {
	m := rd.model
	v := Verdict{Mode: "rules", From: fromName, To: toName, Port: port.Num, Protocol: port.Protocol,
		Complete: rd.complete, Notes: nonNil(rd.notes), CNI: rd.cni, Unevaluated: rd.unevaluate}
	if v.Unevaluated == nil {
		v.Unevaluated = []Unevaluated{}
	}
	v.Egress = m.side("egress", from, to, port)
	v.Ingress = m.side("ingress", from, to, port)
	passes := func(s string) bool { return s == sideOpen || s == sideAllowed || s == sideOutside }
	switch {
	case v.Egress.State == sideDenied || v.Ingress.State == sideDenied:
		v.Answer = "no"
		v.Summary = "The rules refuse it: "
		if v.Egress.State == sideDenied {
			v.Summary += "the source may not send it"
			if v.Ingress.State == sideDenied {
				v.Summary += ", and "
			}
		}
		if v.Ingress.State == sideDenied {
			v.Summary += "the destination may not receive it"
		}
		v.Summary += "."
	case v.Egress.State == sideUnevaluated || v.Ingress.State == sideUnevaluated:
		v.Answer = "unevaluated"
		v.Summary = "A rule that could allow it is not evaluated here."
	case passes(v.Egress.State) && passes(v.Ingress.State):
		v.Answer = "yes"
		v.Summary = "The rules allow it on both sides."
	default:
		v.Answer = "depends"
		v.Summary = "The rules alone do not decide it: the network plugin does."
	}
	if rd.cni.Enforces == "no" {
		v.Summary += " " + rd.cni.Name + " does not apply NetworkPolicies, so nothing enforces this answer."
	}
	if len(v.Unevaluated) > 0 {
		v.Summary += " Other policies apply too, and are not evaluated here: this may be wrong."
	}
	return v
}

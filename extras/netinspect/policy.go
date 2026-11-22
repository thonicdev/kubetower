package netinspect

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"sort"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"
)

// A NetworkPolicy to copy. It is only ever drawn as text: nothing here, nor
// anywhere in this extra, applies it.
//
// It is the smallest policy that lets the examined flow through the side
// that refuses it, and nothing else: an ingress policy on the destination
// when its ingress refuses, an egress policy on the source when its egress
// refuses. A side that is open gets none - a policy selecting a pod isolates
// it, so offering one for a side that already passes would shut every other
// flow of that pod.

// churning are labels a controller rewrites on every rollout: a selector
// built on one would stop matching at the next one.
var churning = map[string]bool{
	"pod-template-hash":                        true,
	"controller-revision-hash":                 true,
	"statefulset.kubernetes.io/pod-name":       true,
	"apps.kubernetes.io/pod-index":             true,
	"batch.kubernetes.io/job-completion-index": true,
	"controller-uid":                           true,
}

// party is one end of the flow as a policy names it.
type party struct {
	ns     string
	labels map[string]string
	ip     net.IP
}

// stable is a pod's labels without the ones a controller rewrites; all of
// them when nothing else is left, since an empty selector would select every
// pod of the namespace.
func stable(set map[string]string) map[string]string {
	out := make(map[string]string, len(set))
	for k, v := range set {
		if !churning[k] {
			out[k] = v
		}
	}
	if len(out) == 0 {
		for k, v := range set {
			out[k] = v
		}
	}
	return out
}

func (m *model) partyOf(e endpoint) party {
	if e.pod < 0 {
		return party{ip: e.ip}
	}
	p := &m.Pods[e.pod]
	return party{ns: p.Namespace, labels: stable(p.Labels)}
}

// policyFor is the policy that would allow a verdict on a pod destination.
func (rd *read) policyFor(v *Verdict, from, to endpoint, ports []flowPort) string {
	return buildPolicy(rd.model.partyOf(from), rd.model.partyOf(to), v.Egress, v.Ingress, ports, ports)
}

// wants is whether a side is refused by absence of an allow, the only
// refusal an allow rule can lift: a deny rule wins over every allow.
func wants(s Side) bool {
	return !s.noFix && s.State == sideDenied && (s.Decided == nil || s.Decided.Kind != "deny")
}

// buildPolicy writes the documents, separated by `---`; empty when no side
// is to be fixed.
func buildPolicy(from, to party, egress, ingress Side, egressPorts, ingressPorts []flowPort) string {
	var docs []string
	if wants(ingress) && to.ip == nil && len(ingressPorts) > 0 {
		docs = append(docs, document("ingress", to, from, ingressPorts))
	}
	if wants(egress) && from.ip == nil && len(egressPorts) > 0 {
		docs = append(docs, document("egress", from, to, egressPorts))
	}
	return strings.Join(docs, "---\n")
}

// document is one NetworkPolicy: self is the pod it isolates, other the peer
// it lets through.
func document(direction string, self, other party, ports []flowPort) string {
	rule := map[string]any{peerKey(direction): []any{peerDoc(other, self.ns)}, "ports": portDocs(ports)}
	spec := map[string]any{
		"podSelector": selector(self.labels),
		"policyTypes": []any{strings.ToUpper(direction[:1]) + direction[1:]},
		direction:     []any{rule},
	}
	meta := map[string]any{"name": policyName(direction, self, other, ports), "namespace": self.ns}
	out, err := yaml.Marshal(map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": meta, "spec": spec})
	if err != nil {
		return ""
	}
	return string(out)
}

func peerKey(direction string) string {
	if direction == "ingress" {
		return "from"
	}
	return "to"
}

func selector(set map[string]string) map[string]any {
	if len(set) == 0 {
		return map[string]any{}
	}
	return map[string]any{"matchLabels": set}
}

// peerDoc names the other end: an address as an ipBlock, a pod by its labels,
// with its namespace when it is not the policy's own.
func peerDoc(p party, home string) map[string]any {
	if p.ip != nil {
		bits := 32
		if p.ip.To4() == nil {
			bits = 128
		}
		return map[string]any{"ipBlock": map[string]any{"cidr": p.ip.String() + "/" + strconv.Itoa(bits)}}
	}
	out := map[string]any{}
	if p.ns != home {
		out["namespaceSelector"] = map[string]any{"matchLabels": map[string]string{"kubernetes.io/metadata.name": p.ns}}
		if len(p.labels) > 0 {
			out["podSelector"] = selector(p.labels)
		}
		return out
	}
	out["podSelector"] = selector(p.labels)
	return out
}

func portDocs(ports []flowPort) []any {
	out := make([]any, 0, len(ports))
	for _, p := range ports {
		out = append(out, map[string]any{"protocol": p.Protocol, "port": int64(p.Num)})
	}
	return out
}

// policyName is a DNS label that says what the policy does and stays the same
// for the same flow: the readable part is cut, a short hash keeps it unique.
func policyName(direction string, self, other party, ports []flowPort) string {
	label := func(p party) string {
		if p.ip != nil {
			return p.ip.String()
		}
		keys := make([]string, 0, len(p.labels))
		for k := range p.labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := []string{p.ns}
		for _, k := range keys {
			parts = append(parts, p.labels[k])
		}
		return strings.Join(parts, "-")
	}
	var port []string
	for _, p := range ports {
		port = append(port, p.Protocol+strconv.Itoa(int(p.Num)))
	}
	key := direction + "|" + label(self) + "|" + label(other) + "|" + strings.Join(port, ",")
	sum := sha256.Sum256([]byte(key))
	text := dns("allow-" + direction + "-" + label(other))
	if len(text) > 54 {
		text = strings.TrimRight(text[:54], "-")
	}
	return text + "-" + hex.EncodeToString(sum[:3])
}

// dns lower-cases and keeps letters, digits and single dashes.
func dns(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

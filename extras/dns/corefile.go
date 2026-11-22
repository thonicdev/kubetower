package dns

import (
	"net"
	"net/http"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
)

// resolvConf is the label of a forward that reads the node's resolv.conf: its
// real upstreams are known only to the pod, and Prometheus lists them by
// address once they answer.
const resolvConf = "resolv.conf"

// corefileMap is the ConfigMap CoreDNS reads its Corefile from, in the
// namespace its pods run in.
const corefileMap = "coredns"

var configMaps = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}

// forward is one `forward` line of a Corefile: the zone it serves and the
// upstreams it names, each as CoreDNS dials it (`host:port`), or resolvConf.
type forward struct {
	Zone    string
	Targets []string
}

// parseCorefile reads the `forward` directives of a Corefile. It is a reader
// of the few shapes that matter, not CoreDNS's parser: server blocks and
// snippets nest by braces, a directive is its first word, `#` starts a
// comment, and a directive's own block (`policy`, `health_check`...) is
// skipped. Anything it does not understand is left out rather than guessed.
func parseCorefile(text string) []forward {
	var out []forward
	depth := 0
	block := "."
	for _, raw := range strings.Split(text, "\n") {
		line := raw
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if depth == 0 && strings.HasSuffix(strings.TrimSpace(line), "{") {
			block = blockZone(fields[0])
		}
		// A directive is read where it starts a line inside a block, at any
		// depth: a snippet's and a server block's forward are the same thing.
		if depth >= 1 && fields[0] == "forward" {
			rest := fields[1:]
			rest = trimBrace(rest)
			if len(rest) >= 2 {
				// `.` is "whatever the block serves".
				f := forward{Zone: rest[0]}
				if f.Zone == "." {
					f.Zone = block
				}
				for _, t := range rest[1:] {
					if target, ok := normalizeTarget(t); ok {
						f.Targets = append(f.Targets, target)
					}
				}
				if len(f.Targets) > 0 {
					out = append(out, f)
				}
			}
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth < 0 {
			depth = 0
		}
	}
	return out
}

// blockZone is the zone a server block's header names: `failing.test:53` is
// `failing.test`, and `.:53` or a snippet's `(name)` is the root.
func blockZone(header string) string {
	if strings.HasPrefix(header, "(") {
		return "."
	}
	if i := strings.LastIndexByte(header, ':'); i >= 0 && !strings.Contains(header[i:], "]") {
		header = header[:i]
	}
	if header == "" {
		return "."
	}
	return header
}

// trimBrace drops a trailing `{` that opens the directive's own block.
func trimBrace(fields []string) []string {
	if n := len(fields); n > 0 && fields[n-1] == "{" {
		return fields[:n-1]
	}
	if n := len(fields); n > 0 && strings.HasSuffix(fields[n-1], "{") {
		out := append([]string{}, fields...)
		out[n-1] = strings.TrimSuffix(out[n-1], "{")
		return out
	}
	return fields
}

// normalizeTarget turns a forward target into the address Prometheus labels
// it with: `192.0.2.1` is `192.0.2.1:53`, `tls://1.1.1.1` is `1.1.1.1:853`,
// an IPv6 address is bracketed. A file path is the node's resolv.conf.
func normalizeTarget(t string) (string, bool) {
	t = strings.TrimSpace(t)
	if t == "" {
		return "", false
	}
	if strings.HasPrefix(t, "/") {
		return resolvConf, true
	}
	port := "53"
	if scheme, rest, found := strings.Cut(t, "://"); found {
		switch strings.ToLower(scheme) {
		case "dns":
		case "tls":
			port = "853"
		case "https":
			port = "443"
		default:
			return "", false
		}
		t = rest
		if i := strings.IndexByte(t, '/'); i >= 0 {
			t = t[:i]
		}
	}
	if t == "" {
		return "", false
	}
	host := t
	switch {
	case strings.HasPrefix(t, "["):
		h, p, err := net.SplitHostPort(t)
		if err != nil {
			h = strings.Trim(t, "[]")
			p = ""
		}
		host = h
		if p != "" {
			port = p
		}
	case strings.Count(t, ":") == 1:
		h, p, err := net.SplitHostPort(t)
		if err != nil {
			return "", false
		}
		host, port = h, p
	}
	if host == "" {
		return "", false
	}
	return net.JoinHostPort(host, port), true
}

// configured is the upstreams a Corefile names, by address, with the zones
// each serves in order. resolvConf is one of them when a forward reads the
// node's resolv.conf.
func configured(forwards []forward) map[string][]string {
	out := map[string][]string{}
	for _, f := range forwards {
		for _, t := range f.Targets {
			if !contains(out[t], f.Zone) {
				out[t] = append(out[t], f.Zone)
			}
		}
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// mergeConfigured lists every upstream the Corefile names beside the ones
// Prometheus has a series for. An upstream that never answers leaves no series
// at all, so the metrics cannot list it; the Corefile can. One the metrics do
// not hold reads as "no answers", and one they do keeps its figures and gains
// its zones. A forward to resolv.conf adds a row only when no other upstream
// is known: the pod's real addresses show on their own once they answer.
func mergeConfigured(have []Upstream, forwards []forward) []Upstream {
	want := configured(forwards)
	known := map[string]bool{}
	for i := range have {
		known[have[i].To] = true
		if zones, ok := want[have[i].To]; ok {
			have[i].Zones = zones
		}
	}
	targets := make([]string, 0, len(want))
	for t := range want {
		targets = append(targets, t)
	}
	sort.Strings(targets)
	explained, listed := 0, len(have)
	for _, u := range have {
		if _, ok := want[u.To]; ok {
			explained++
		}
	}
	for _, t := range targets {
		if known[t] {
			continue
		}
		if t == resolvConf && listed > explained {
			continue
		}
		have = append(have, Upstream{To: t, Zones: want[t]})
	}
	return have
}

// corefile reads the Corefile of the console's CoreDNS through the door the
// person is reviewed at, or nil: a refusal, a missing ConfigMap or a read that
// failed all leave the list as the metrics hold it, because the page said
// nothing wrong without it.
func corefile(h extension.Host, r *http.Request, cluster, namespace string) []forward {
	if namespace == "" {
		return nil
	}
	page, err := h.List(r, cluster, extension.ListQuery{GVR: configMaps, Namespace: namespace,
		FieldSelector: "metadata.name=" + corefileMap, Limit: 2})
	if err != nil {
		return nil
	}
	for i := range page.Items {
		if page.Items[i].GetName() == corefileMap {
			return forwardsOf(&page.Items[i])
		}
	}
	return nil
}

func forwardsOf(o *unstructured.Unstructured) []forward {
	text, _, _ := unstructured.NestedString(o.Object, "data", "Corefile")
	return parseCorefile(text)
}

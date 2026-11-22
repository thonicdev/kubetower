package pending

import (
	"regexp"
	"strconv"
	"strings"
)

// Category is why a pod waits, as one word the page files it under. A pod may
// carry several (the scheduler names every reason a node was refused for);
// its main one is the reason that refused the most nodes.
type Category string

const (
	CatResources    Category = "resources"
	CatTaints       Category = "taints"
	CatNodeAffinity Category = "nodeAffinity"
	CatPodAffinity  Category = "podAffinity"
	CatSpread       Category = "spread"
	CatVolumes      Category = "volumes"
	CatCordon       Category = "cordon"
	CatPorts        Category = "ports"
	CatGated        Category = "gated"
	CatNoScheduler  Category = "noScheduler"
	CatStarting     Category = "starting"
	CatOther        Category = "other"
)

// Categories is every category a pod can be filed under, in the order the
// page draws its tiles.
var Categories = []Category{
	CatResources, CatTaints, CatNodeAffinity, CatPodAffinity, CatSpread, CatVolumes,
	CatCordon, CatPorts, CatGated, CatNoScheduler, CatStarting, CatOther,
}

// Reason is one item of the scheduler's message: what refused how many nodes.
type Reason struct {
	// Text is the scheduler's own words for it, unchanged.
	Text string `json:"text"`
	// Nodes is how many nodes it refused; -1 when the scheduler gave no count,
	// which it does for a reason about the pod rather than about a node (an
	// unbound claim refuses every node).
	Nodes    int      `json:"nodes"`
	Category Category `json:"category"`
}

// Verdict is the scheduler's message, split.
type Verdict struct {
	// Total is the N of "0/N nodes are available"; -1 when the message did not
	// say.
	Total   int      `json:"total"`
	Reasons []Reason `json:"reasons"`
	// Preemption is what the scheduler said about evicting lower-priority
	// pods, kept as written.
	Preemption string `json:"preemption,omitempty"`
}

var (
	availableRE = regexp.MustCompile(`^0/(\d+) nodes are available:\s*`)
	countedRE   = regexp.MustCompile(`^(\d+) (.+)$`)
)

// noise is what the scheduler appends that says nothing about this pod:
// dynamic resource allocation's line when no claim is involved, and the
// stray "not found" a volume binder error ends with (measured on v1.34).
var noise = map[string]bool{
	"no new claims to deallocate": true,
	"not found":                   true,
	"":                            true,
}

// ParseMessage splits the message a PodScheduled condition or a
// FailedScheduling event carries:
//
//	0/5 nodes are available: 2 Insufficient cpu, 3 node(s) had untolerated taint {a: b}. preemption: …
//
// A message that does not have that shape is one Reason, filed by its words,
// so nothing the scheduler said is dropped.
func ParseMessage(message string) Verdict {
	v := Verdict{Total: -1}
	rest := strings.TrimSpace(message)
	if m := availableRE.FindStringSubmatch(rest); m != nil {
		v.Total, _ = strconv.Atoi(m[1])
		rest = rest[len(m[0]):]
	}
	if i := strings.Index(rest, "preemption:"); i >= 0 {
		v.Preemption = strings.TrimSpace(rest[i+len("preemption:"):])
		rest = rest[:i]
	}
	for _, sentence := range strings.Split(rest, ". ") {
		sentence = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sentence), "."))
		sentence = strings.TrimSuffix(sentence, ",")
		if noise[sentence] {
			continue
		}
		for _, item := range splitItems(sentence) {
			if noise[item] {
				continue
			}
			r := Reason{Text: item, Nodes: -1}
			if m := countedRE.FindStringSubmatch(item); m != nil {
				r.Nodes, _ = strconv.Atoi(m[1])
				r.Text = m[2]
			}
			r.Category = categorise(r.Text)
			v.Reasons = append(v.Reasons, r)
		}
	}
	return v
}

// splitItems splits on the commas between reasons, never on one inside a
// taint's braces.
func splitItems(sentence string) []string {
	var out []string
	depth, start := 0, 0
	for i, c := range sentence {
		switch c {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(sentence[start:i]))
				start = i + 1
			}
		}
	}
	out = append(out, strings.TrimSpace(sentence[start:]))
	// Before v1.27 the taint plugin wrote "had taint {k: v}, that the pod
	// didn't tolerate": the clause after the comma belongs to the reason.
	merged := out[:0]
	for _, item := range out {
		if strings.HasPrefix(item, "that ") && len(merged) > 0 {
			merged[len(merged)-1] += ", " + item
			continue
		}
		merged = append(merged, item)
	}
	return merged
}

// categorize files one reason by its words. The phrases are the scheduler
// plugins' own (kube-scheduler v1.34, and the older "had taint …, that the
// pod didn't tolerate"); matching is on words that survived those versions,
// and anything else is Other, drawn as written.
func categorise(text string) Category {
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "were unschedulable"), strings.Contains(t, "node.kubernetes.io/unschedulable"):
		return CatCordon
	case strings.HasPrefix(t, "insufficient "), strings.Contains(t, "too many pods"):
		return CatResources
	case strings.Contains(t, "taint"):
		return CatTaints
	case strings.Contains(t, "volume node affinity"), strings.Contains(t, "persistentvolumeclaim"),
		strings.Contains(t, "persistent volume"), strings.Contains(t, "volume count"),
		strings.Contains(t, "volume"):
		return CatVolumes
	case strings.Contains(t, "node affinity/selector"), strings.Contains(t, "node affinity"), strings.Contains(t, "node selector"):
		return CatNodeAffinity
	case strings.Contains(t, "affinity"):
		return CatPodAffinity
	case strings.Contains(t, "topology spread"):
		return CatSpread
	case strings.Contains(t, "free ports"), strings.Contains(t, "host port"):
		return CatPorts
	}
	return CatOther
}

// Main is the category that refused the most nodes: a reason with no count
// refused them all.
func (v Verdict) Main() Category {
	best, most := CatOther, -2
	for _, r := range v.Reasons {
		n := r.Nodes
		if n < 0 {
			n = 1 << 30
		}
		if n > most {
			best, most = r.Category, n
		}
	}
	return best
}

// Set is every distinct category the verdict names, in Categories' order.
func (v Verdict) Set() []Category {
	seen := map[Category]bool{}
	for _, r := range v.Reasons {
		seen[r.Category] = true
	}
	var out []Category
	for _, c := range Categories {
		if seen[c] {
			out = append(out, c)
		}
	}
	return out
}

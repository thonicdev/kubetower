package pending

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// Pod affinity, pod anti-affinity and topology spread depend on the pods
// already placed, so they were not computed at first and their cells said "per
// the scheduler". This file computes the **required** halves of them -
// `requiredDuringSchedulingIgnoredDuringExecution` terms and
// `whenUnsatisfiable: DoNotSchedule` constraints, which are the ones that
// refuse a node - for the topologies a person reads a cluster by: the node
// (`kubernetes.io/hostname`) and the zone. It follows the scheduler's own
// InterPodAffinity and PodTopologySpread filters:
//
//   - a term's namespaces are, as the plugin has them, the term's names plus
//     the namespaces its namespaceSelector selects, or the declaring pod's own
//     namespace when it names neither;
//   - matchLabelKeys and mismatchLabelKeys add `key in (value)` and
//     `key notin (value)` to the selector, from the labels of the pod that
//     declares the term;
//   - affinity counts an existing pod only when it matches **all** of the
//     pod's terms; a pod whose terms match nothing in the cluster but itself
//     is let through, as the scheduler lets a first replica through;
//   - an existing pod's own anti-affinity can exclude the pod;
//   - a spread constraint counts the pods of the pod's namespace that match
//     its selector (matchLabelKeys included) on the nodes that carry every
//     constraint's topology key and that the node-inclusion policies keep, and
//     refuses a node whose domain would exceed the least-loaded domain by more
//     than maxSkew; minDomains is honored.
//
// What is **not** computed stays gray, with the reason: preferred terms and
// ScheduleAnyway constraints (they score, they never refuse), any other
// topology key, the cluster's default spread constraints, and every case where
// the pods or the nodes, or the namespaces a selector needs, were not read
// whole - a count from half the pods would be drawn as a fact. Nothing is
// scored and the queue is not simulated.

const (
	topologyHostname = "kubernetes.io/hostname"
	topologyZone     = "topology.kubernetes.io/zone"
	topologyZoneOld  = "failure-domain.beta.kubernetes.io/zone"
)

func computedTopology(key string) bool {
	return key == topologyHostname || key == topologyZone || key == topologyZoneOld
}

// maxPlacedWork is how many distinct pods' rules one page answer computes; the
// pod's own view always computes its own. Each costs a pass over the placed
// pods and the nodes, and the answers are kept compactly (one flag per node).
const maxPlacedWork = 200

// nsLabels is the labels of every namespace, by name; nil where they were not
// read.
type nsLabels map[string]map[string]string

// podTerm is one required (anti-)affinity term, its selector built.
type podTerm struct {
	selector   labels.Selector // nil selects nothing
	names      []string
	nsSelector labels.Selector // nil: no namespaceSelector
	declaredIn string
	key        string
	text       string
}

func newTerm(t corev1.PodAffinityTerm, declaredIn string, own map[string]string) (podTerm, error) {
	out := podTerm{declaredIn: declaredIn, key: t.TopologyKey, names: t.Namespaces}
	if t.LabelSelector != nil {
		sel := t.LabelSelector.DeepCopy()
		for _, k := range t.MatchLabelKeys {
			if v, ok := own[k]; ok {
				sel.MatchExpressions = append(sel.MatchExpressions, metav1.LabelSelectorRequirement{Key: k, Operator: metav1.LabelSelectorOpIn, Values: []string{v}})
			}
		}
		for _, k := range t.MismatchLabelKeys {
			if v, ok := own[k]; ok {
				sel.MatchExpressions = append(sel.MatchExpressions, metav1.LabelSelectorRequirement{Key: k, Operator: metav1.LabelSelectorOpNotIn, Values: []string{v}})
			}
		}
		s, err := metav1.LabelSelectorAsSelector(sel)
		if err != nil {
			return out, err
		}
		out.selector, out.text = s, s.String()
	}
	if t.NamespaceSelector != nil {
		s, err := metav1.LabelSelectorAsSelector(t.NamespaceSelector)
		if err != nil {
			return out, err
		}
		out.nsSelector = s
	}
	return out, nil
}

func (t podTerm) needsNamespaces() bool { return t.nsSelector != nil && !t.nsSelector.Empty() }

// matches is whether a pod in namespace ns with these labels is selected by
// the term; known is false when that needed namespace labels nobody read.
func (t podTerm) matches(ns string, lbl map[string]string, nsl nsLabels) (match, known bool) {
	if t.selector == nil {
		return false, true
	}
	inNS := false
	switch {
	case len(t.names) == 0 && t.nsSelector == nil:
		inNS = ns == t.declaredIn
	default:
		for _, n := range t.names {
			if n == ns {
				inNS = true
				break
			}
		}
		if !inNS && t.nsSelector != nil {
			if t.nsSelector.Empty() {
				inNS = true
			} else if nsl == nil {
				return false, false
			} else {
				inNS = t.nsSelector.Matches(labels.Set(nsl[ns]))
			}
		}
	}
	return inNS && t.selector.Matches(labels.Set(lbl)), true
}

// placedPod is what the placement rules read of a pod already on a node.
type placedPod struct {
	Namespace, Name, Node string
	Labels                map[string]string
	Terminating           bool
	Anti                  []podTerm
	// BadAnti is set when one of its anti-affinity terms could not be built:
	// whether it excludes a pod is then unknown.
	BadAnti bool
}

// Placement is the placed pods as the rules need them. Anti is every placed
// pod that declares required anti-affinity, always kept; Pods is every placed
// pod, kept only when a pending pod's own rules need the counts. Whole says
// the placed read was complete, so counts from it can be trusted.
type Placement struct {
	Pods  []*placedPod
	Anti  []*placedPod
	Kept  bool
	Whole bool
	byNS  map[string][]*placedPod
}

func (pl *Placement) inNamespace(ns string) []*placedPod {
	if pl.byNS == nil {
		pl.byNS = map[string][]*placedPod{}
		for _, p := range pl.Pods {
			pl.byNS[p.Namespace] = append(pl.byNS[p.Namespace], p)
		}
	}
	return pl.byNS[ns]
}

// Rule is one placement column's answer: per node, in the nodes' order, or why
// it was not computed.
type Rule struct {
	Computed bool
	Why      string
	OK       []bool
	// Detail is, per node, what refused it, when asked for.
	Detail []string
}

// Placed is the answer for one pod.
type Placed struct {
	Affinity, Spread Rule
}

// Rules is what a pod declares that placement needs.
type Rules struct {
	Namespace string
	Labels    map[string]string
	Spec      corev1.PodSpec
}

// declaresPlacement is whether the pod's own rules need the counts of every
// placed pod: required affinity or anti-affinity, or a hard spread constraint.
func declaresPlacement(spec corev1.PodSpec) bool {
	if a := spec.Affinity; a != nil {
		if a.PodAffinity != nil && len(a.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution) > 0 {
			return true
		}
		if a.PodAntiAffinity != nil && len(a.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution) > 0 {
			return true
		}
	}
	for _, c := range spec.TopologySpreadConstraints {
		if c.WhenUnsatisfiable == corev1.DoNotSchedule {
			return true
		}
	}
	return false
}

// needsNamespaceLabels is whether answering for this pod may need the
// namespaces' labels: one of its terms, or of an existing pod's, selects
// namespaces by label.
func (pl *Placement) needsNamespaceLabels(spec corev1.PodSpec) bool {
	check := func(ts []corev1.PodAffinityTerm) bool {
		for _, t := range ts {
			if t.NamespaceSelector != nil && (len(t.NamespaceSelector.MatchLabels) > 0 || len(t.NamespaceSelector.MatchExpressions) > 0) {
				return true
			}
		}
		return false
	}
	if a := spec.Affinity; a != nil {
		if a.PodAffinity != nil && check(a.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution) {
			return true
		}
		if a.PodAntiAffinity != nil && check(a.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution) {
			return true
		}
	}
	for _, e := range pl.Anti {
		for _, t := range e.Anti {
			if t.needsNamespaces() {
				return true
			}
		}
	}
	return false
}

// hit is a count and the first pod behind it, for naming what refuses a node.
type hit struct {
	n  int
	by *placedPod
}

// pairs is topology (key, value) pairs and their hits.
type pairs map[string]map[string]*hit

func (p pairs) add(key, value string, by *placedPod) {
	m := p[key]
	if m == nil {
		m = map[string]*hit{}
		p[key] = m
	}
	h := m[value]
	if h == nil {
		h = &hit{by: by}
		m[value] = h
	}
	h.n++
}

func (p pairs) at(key, value string) *hit { return p[key][value] }

func (p pairs) empty() bool { return len(p) == 0 }

// Compute answers both columns for a pod. nodes must be the fleet's, each
// carrying its Index. wantDetail asks for the sentence behind each refusal,
// which costs an allocation per refused node: the pod's own view wants it, the
// page's ranking does not.
func (pl *Placement) Compute(r Rules, nodes []*Node, nodesWhole bool, nsl nsLabels, wantDetail bool) Placed {
	var out Placed
	why := ""
	switch {
	case !pl.Whole:
		why = "the pods on the nodes were not read whole"
	case !nodesWhole:
		why = "the nodes were not read whole"
	}
	if why != "" {
		out.Affinity, out.Spread = Rule{Why: why}, Rule{Why: why}
		return out
	}
	out.Affinity = pl.affinity(r, nodes, nsl, wantDetail)
	out.Spread = pl.spread(r, nodes, wantDetail)
	return out
}

func requiredTerms(spec corev1.PodSpec) (affinity, anti []corev1.PodAffinityTerm) {
	if a := spec.Affinity; a != nil {
		if a.PodAffinity != nil {
			affinity = a.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		}
		if a.PodAntiAffinity != nil {
			anti = a.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		}
	}
	return affinity, anti
}

func (pl *Placement) affinity(r Rules, nodes []*Node, nsl nsLabels, wantDetail bool) Rule {
	rawAff, rawAnti := requiredTerms(r.Spec)
	build := func(ts []corev1.PodAffinityTerm) ([]podTerm, string) {
		out := make([]podTerm, 0, len(ts))
		for _, t := range ts {
			if !computedTopology(t.TopologyKey) {
				return nil, "the topology key " + t.TopologyKey + " is not computed here, only the node and the zone are"
			}
			term, err := newTerm(t, r.Namespace, r.Labels)
			if err != nil {
				return nil, "a label selector of the pod could not be read"
			}
			out = append(out, term)
		}
		return out, ""
	}
	aff, why := build(rawAff)
	if why != "" {
		return Rule{Why: why}
	}
	anti, why := build(rawAnti)
	if why != "" {
		return Rule{Why: why}
	}
	if !pl.Kept && (len(aff) > 0 || len(anti) > 0) {
		return Rule{Why: "the pods on the nodes were not kept for this pod"}
	}
	byName := make(map[string]*Node, len(nodes))
	for _, n := range nodes {
		byName[n.Name] = n
	}

	// What the pod's own terms find among the pods already placed.
	affinityCounts, antiCounts := pairs{}, pairs{}
	if len(aff) > 0 || len(anti) > 0 {
		for _, e := range pl.Pods {
			n := byName[e.Node]
			if n == nil {
				continue
			}
			if len(aff) > 0 {
				all := true
				for _, t := range aff {
					m, known := t.matches(e.Namespace, e.Labels, nsl)
					if !known {
						return Rule{Why: "the labels of the namespaces were not read"}
					}
					if !m {
						all = false
						break
					}
				}
				if all {
					for _, t := range aff {
						if v, ok := n.Labels[t.key]; ok {
							affinityCounts.add(t.key, v, e)
						}
					}
				}
			}
			for _, t := range anti {
				m, known := t.matches(e.Namespace, e.Labels, nsl)
				if !known {
					return Rule{Why: "the labels of the namespaces were not read"}
				}
				if m {
					if v, ok := n.Labels[t.key]; ok {
						antiCounts.add(t.key, v, e)
					}
				}
			}
		}
	}
	// The pod matching its own terms, for a first replica.
	selfAll := len(aff) > 0
	for _, t := range aff {
		if m, known := t.matches(r.Namespace, r.Labels, nsl); !known {
			return Rule{Why: "the labels of the namespaces were not read"}
		} else if !m {
			selfAll = false
		}
	}

	// What the existing pods' own anti-affinity finds in this pod.
	existing := pairs{}
	for _, e := range pl.Anti {
		if e.BadAnti {
			return Rule{Why: "an anti-affinity selector of pod " + e.Namespace + "/" + e.Name + " could not be read"}
		}
		n := byName[e.Node]
		if n == nil {
			continue
		}
		for _, t := range e.Anti {
			m, known := t.matches(r.Namespace, r.Labels, nsl)
			if !known {
				return Rule{Why: "the labels of the namespaces were not read"}
			}
			if !m {
				continue
			}
			if !computedTopology(t.key) {
				return Rule{Why: "pod " + e.Namespace + "/" + e.Name + " has an anti-affinity on " + t.key + ", which is not computed here, only the node and the zone are"}
			}
			if v, ok := n.Labels[t.key]; ok {
				existing.add(t.key, v, e)
			}
		}
	}

	res := Rule{Computed: true, OK: make([]bool, len(nodes))}
	if wantDetail {
		res.Detail = make([]string, len(nodes))
	}
	for i, n := range nodes {
		ok, detail := true, ""
		fail := func(f func() string) {
			if ok {
				ok = false
				if wantDetail {
					detail = f()
				}
			}
		}
		for key, values := range existing {
			if v, has := n.Labels[key]; has {
				if h := values[v]; h != nil {
					fail(func() string {
						return "pod " + h.by.Namespace + "/" + h.by.Name + " has an anti-affinity that excludes it on " + key + "=" + v
					})
				}
			}
		}
		podsExist := true
		for _, t := range aff {
			v, has := n.Labels[t.key]
			if !has {
				fail(func() string { return "the node has no label " + t.key })
				continue
			}
			if h := affinityCounts.at(t.key, v); h == nil || h.n <= 0 {
				podsExist = false
			}
		}
		if ok && !podsExist && !(affinityCounts.empty() && selfAll) {
			t := aff[0]
			fail(func() string {
				v := n.Labels[t.key]
				return "no pod matching " + t.text + " on " + t.key + "=" + v
			})
		}
		for _, t := range anti {
			if v, has := n.Labels[t.key]; has {
				if h := antiCounts.at(t.key, v); h != nil && h.n > 0 {
					fail(func() string {
						return "pod " + h.by.Namespace + "/" + h.by.Name + " matches its anti-affinity on " + t.key + "=" + v
					})
				}
			}
		}
		res.OK[i] = ok
		if wantDetail {
			res.Detail[i] = detail
		}
	}
	return res
}

func (pl *Placement) spread(r Rules, nodes []*Node, wantDetail bool) Rule {
	var cs []corev1.TopologySpreadConstraint
	for _, c := range r.Spec.TopologySpreadConstraints {
		if c.WhenUnsatisfiable == corev1.DoNotSchedule {
			cs = append(cs, c)
		}
	}
	if len(cs) == 0 {
		return Rule{Why: "the pod declares no hard constraint: the cluster's own defaults are not read"}
	}
	if !pl.Kept {
		return Rule{Why: "the pods on the nodes were not kept for this pod"}
	}
	selectors := make([]labels.Selector, len(cs))
	for i, c := range cs {
		if !computedTopology(c.TopologyKey) {
			return Rule{Why: "the topology key " + c.TopologyKey + " is not computed here, only the node and the zone are"}
		}
		if c.LabelSelector == nil {
			selectors[i] = labels.Nothing()
			continue
		}
		sel := c.LabelSelector.DeepCopy()
		for _, k := range c.MatchLabelKeys {
			if v, ok := r.Labels[k]; ok {
				sel.MatchExpressions = append(sel.MatchExpressions, metav1.LabelSelectorRequirement{Key: k, Operator: metav1.LabelSelectorOpIn, Values: []string{v}})
			}
		}
		s, err := metav1.LabelSelectorAsSelector(sel)
		if err != nil {
			return Rule{Why: "a label selector of the pod could not be read"}
		}
		selectors[i] = s
	}
	hasAll := func(n *Node) bool {
		for _, c := range cs {
			if _, ok := n.Labels[c.TopologyKey]; !ok {
				return false
			}
		}
		return true
	}
	counts := make([]map[string]int, len(cs))
	counted := make([]map[string]*Node, len(cs)) // node name -> node kept for the constraint
	for i := range cs {
		counts[i] = map[string]int{}
		counted[i] = map[string]*Node{}
	}
	for _, n := range nodes {
		if !hasAll(n) {
			continue
		}
		for i, c := range cs {
			if c.NodeAffinityPolicy == nil || *c.NodeAffinityPolicy == corev1.NodeInclusionPolicyHonor {
				if ok, _ := matchesNode(r.Spec, n); !ok {
					continue
				}
			}
			if c.NodeTaintsPolicy != nil && *c.NodeTaintsPolicy == corev1.NodeInclusionPolicyHonor {
				if len(untolerated(r.Spec.Tolerations, n.Taints)) > 0 {
					continue
				}
			}
			counts[i][n.Labels[c.TopologyKey]] += 0 // the domain exists, with no pod yet
			counted[i][n.Name] = n
		}
	}
	for _, e := range pl.inNamespace(r.Namespace) {
		if e.Terminating {
			continue
		}
		for i, c := range cs {
			n := counted[i][e.Node]
			if n == nil {
				continue
			}
			if selectors[i].Matches(labels.Set(e.Labels)) {
				counts[i][n.Labels[c.TopologyKey]]++
			}
		}
	}
	mins := make([]int, len(cs))
	selves := make([]int, len(cs))
	for i, c := range cs {
		least := -1
		for _, n := range counts[i] {
			if least < 0 || n < least {
				least = n
			}
		}
		if least < 0 {
			least = 0
		}
		if c.MinDomains != nil && len(counts[i]) < int(*c.MinDomains) {
			least = 0
		}
		mins[i] = least
		if selectors[i].Matches(labels.Set(r.Labels)) {
			selves[i] = 1
		}
	}
	res := Rule{Computed: true, OK: make([]bool, len(nodes))}
	if wantDetail {
		res.Detail = make([]string, len(nodes))
	}
	for ni, n := range nodes {
		ok, detail := true, ""
		for i, c := range cs {
			v, has := n.Labels[c.TopologyKey]
			if !has {
				if ok && wantDetail {
					detail = "the node has no label " + c.TopologyKey
				}
				ok = false
				continue
			}
			skew := counts[i][v] + selves[i] - mins[i]
			if skew > int(c.MaxSkew) {
				if ok && wantDetail {
					detail = fmt.Sprintf("%s=%s would hold %d pods against %d in the emptiest domain, over maxSkew %d", c.TopologyKey, v, counts[i][v]+selves[i], mins[i], c.MaxSkew)
				}
				ok = false
			}
		}
		res.OK[ni] = ok
		if wantDetail {
			res.Detail[ni] = detail
		}
	}
	return res
}

// placementKey identifies what a pod's placement answer depends on, so the
// replicas of one workload are computed once on a page.
func placementKey(r Rules) string {
	a, b := requiredTerms(r.Spec)
	var hard []corev1.TopologySpreadConstraint
	for _, c := range r.Spec.TopologySpreadConstraints {
		if c.WhenUnsatisfiable == corev1.DoNotSchedule {
			hard = append(hard, c)
		}
	}
	keys := make([]string, 0, len(r.Labels))
	for k := range r.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString(r.Namespace)
	for _, k := range keys {
		sb.WriteString("\x00" + k + "=" + r.Labels[k])
	}
	// Encoded, never printed with %v: a pointer inside would print as an address.
	rest, _ := json.Marshal([]any{a, b, hard, r.Spec.NodeSelector, r.Spec.Tolerations, nodeAffinityOf(r.Spec)})
	sb.Write(rest)
	return sb.String()
}

func nodeAffinityOf(spec corev1.PodSpec) *corev1.NodeSelector {
	if spec.Affinity == nil || spec.Affinity.NodeAffinity == nil {
		return nil
	}
	return spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
}

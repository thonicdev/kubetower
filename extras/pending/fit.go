package pending

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Amounts is resources by name, in the API's base units: CPU in millicores,
// every other resource in its integer value (bytes, pods, devices).
type Amounts map[string]int64

func (a Amounts) add(b Amounts) {
	for k, v := range b {
		a[k] += v
	}
}

func (a Amounts) max(b Amounts) {
	for k, v := range b {
		if v > a[k] {
			a[k] = v
		}
	}
}

// amountsOf reads a ResourceList into Amounts.
func amountsOf(list corev1.ResourceList) Amounts {
	out := Amounts{}
	for name, q := range list {
		out[string(name)] = amount(string(name), q)
	}
	return out
}

func amount(name string, q resource.Quantity) int64 {
	if name == string(corev1.ResourceCPU) {
		return q.MilliValue()
	}
	return q.Value()
}

// containerRequests is a container's requests, its limits standing in where
// a request is unset - as the API server defaults it.
func containerRequests(c corev1.Container) Amounts {
	out := amountsOf(c.Resources.Requests)
	for name, q := range c.Resources.Limits {
		if _, ok := out[string(name)]; !ok {
			out[string(name)] = amount(string(name), q)
		}
	}
	return out
}

// PodRequest is what the scheduler reserves for a pod: the larger of its
// containers' sum and each init container's own need, sidecars (init
// containers that keep running) counted with everything that starts after
// them, plus the pod's overhead. It is the scheduler's own arithmetic
// (`resourcehelper.PodRequests`).
func PodRequest(spec corev1.PodSpec) Amounts {
	running := Amounts{}
	for _, c := range spec.Containers {
		running.add(containerRequests(c))
	}
	sidecars := Amounts{}
	initPeak := Amounts{}
	for _, c := range spec.InitContainers {
		req := containerRequests(c)
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			sidecars.add(req)
			peak := Amounts{}
			peak.add(sidecars)
			initPeak.max(peak)
			continue
		}
		peak := Amounts{}
		peak.add(req)
		peak.add(sidecars)
		initPeak.max(peak)
	}
	running.add(sidecars)
	running.max(initPeak)
	running.add(amountsOf(spec.Overhead))
	return running
}

// Node is one node, kept to what fitting a pod reads.
type Node struct {
	Name string
	// Index is its place in the fleet's list, where a placement answer keeps
	// one flag per node.
	Index         int
	Labels        map[string]string
	Taints        []corev1.Taint
	Unschedulable bool
	Ready         bool
	Allocatable   Amounts
	// Requested is the sum of PodRequest over the pods placed on it that have
	// not finished; Pods is how many.
	Requested Amounts
	Pods      int64
	// Ports is every host port taken, "proto/ip/port".
	Ports map[string]bool
}

func nodeOf(n *corev1.Node) *Node {
	out := &Node{
		Name:          n.Name,
		Labels:        n.Labels,
		Taints:        n.Spec.Taints,
		Unschedulable: n.Spec.Unschedulable,
		Allocatable:   amountsOf(n.Status.Allocatable),
		Requested:     Amounts{},
		Ports:         map[string]bool{},
	}
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			out.Ready = c.Status == corev1.ConditionTrue
		}
	}
	return out
}

// Predicate keys, in the matrix's column order.
const (
	PredCordon       = "cordon"
	PredTaints       = "taints"
	PredAffinity     = "nodeAffinity"
	PredPods         = "pods"
	PredPorts        = "ports"
	PredVolumes      = "volumes"
	PredPodAffinity  = "podAffinity"
	PredSpread       = "spread"
	resourcePrefix   = "resource:"
	unschedulableKey = "node.kubernetes.io/unschedulable"
)

// Cell is one node's answer to one predicate. OK is nil where nothing was
// computed: the scheduler's word is all there is, and the cell says so
// rather than being drawn as a pass.
type Cell struct {
	Key    string `json:"key"`
	OK     *bool  `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Fit is one node against one pod.
type Fit struct {
	Node  string `json:"node"`
	Cells []Cell `json:"cells"`
	// Failing is how many computed cells refused it.
	Failing int `json:"failing"`
	// Excluded is how many of those are a placement rule - a cordon, a
	// taint, the selector, a volume's zone - rather than room: someone chose
	// to keep the pod off this node, so a node short of room only comes
	// closer.
	Excluded int `json:"excluded"`
	// Deficit is the resources it lacks, each as a fraction of what the node
	// has, summed: how far it is from fitting, for ranking.
	Deficit float64 `json:"deficit"`
}

// Want is a pending pod, kept to what fitting it reads.
type Want struct {
	Spec    corev1.PodSpec
	Request Amounts
	// VolumeZones is, per bound claim with a node-restricted volume, the
	// volume's required node terms.
	VolumeZones []volumeTerm
	// Verdict is the scheduler's, for the predicates nothing here computes.
	Verdict Verdict
	// Place is the pod affinity and spread answer, absent where it was not
	// asked for: the cells then say "per the scheduler".
	Place *Placed
}

type volumeTerm struct {
	Claim string
	Terms *corev1.NodeSelector
}

func yes() *bool { t := true; return &t }
func no() *bool  { f := false; return &f }

// Columns is the matrix's columns for this pod: the ones every pod is held
// to, then a resource column per resource it asks for, then the ones only
// some pods have.
func (w *Want) Columns() []string {
	cols := []string{PredCordon, PredTaints, PredAffinity}
	for _, name := range resourceNames(w.Request) {
		cols = append(cols, resourcePrefix+name)
	}
	cols = append(cols, PredPods)
	if len(hostPorts(w.Spec)) > 0 {
		cols = append(cols, PredPorts)
	}
	if len(w.VolumeZones) > 0 {
		cols = append(cols, PredVolumes)
	}
	set := map[Category]bool{}
	for _, c := range w.Verdict.Set() {
		set[c] = true
	}
	if set[CatPodAffinity] || (w.Spec.Affinity != nil && (w.Spec.Affinity.PodAffinity != nil || w.Spec.Affinity.PodAntiAffinity != nil)) {
		cols = append(cols, PredPodAffinity)
	}
	if set[CatSpread] || len(w.Spec.TopologySpreadConstraints) > 0 {
		cols = append(cols, PredSpread)
	}
	return cols
}

// resourceNames is the resources a request names with a non-zero amount, CPU
// and memory first.
func resourceNames(req Amounts) []string {
	var names []string
	for name, v := range req {
		if v > 0 && name != string(corev1.ResourcePods) {
			names = append(names, name)
		}
	}
	rank := func(n string) int {
		switch n {
		case "cpu":
			return 0
		case "memory":
			return 1
		case "ephemeral-storage":
			return 2
		}
		return 3
	}
	sort.Slice(names, func(i, j int) bool {
		if rank(names[i]) != rank(names[j]) {
			return rank(names[i]) < rank(names[j])
		}
		return names[i] < names[j]
	})
	return names
}

// FitOn holds the pod to one node, column by column.
func (w *Want) FitOn(n *Node, cols []string) Fit {
	f := Fit{Node: n.Name}
	for _, col := range cols {
		c := Cell{Key: col}
		switch {
		case col == PredCordon:
			if n.Unschedulable && !tolerates(w.Spec.Tolerations, corev1.Taint{Key: unschedulableKey, Effect: corev1.TaintEffectNoSchedule}) {
				c.OK, c.Detail = no(), "cordoned"
			} else {
				c.OK = yes()
			}
		case col == PredTaints:
			if t := untolerated(w.Spec.Tolerations, n.Taints); len(t) > 0 {
				c.OK, c.Detail = no(), strings.Join(t, ", ")
			} else {
				c.OK = yes()
			}
		case col == PredAffinity:
			if ok, why := matchesNode(w.Spec, n); ok {
				c.OK = yes()
			} else {
				c.OK, c.Detail = no(), why
			}
		case strings.HasPrefix(col, resourcePrefix):
			name := strings.TrimPrefix(col, resourcePrefix)
			need := w.Request[name]
			alloc, has := n.Allocatable[name]
			free := alloc - n.Requested[name]
			switch {
			case !has || alloc == 0:
				c.OK, c.Detail = no(), "the node has none"
				f.Deficit++
			case need > free:
				c.OK, c.Detail = no(), fmt.Sprintf("needs %s, %s free of %s", Format(name, need), Format(name, max64(free, 0)), Format(name, alloc))
				f.Deficit += float64(need-max64(free, 0)) / float64(alloc)
			default:
				c.OK, c.Detail = yes(), fmt.Sprintf("%s free of %s", Format(name, free), Format(name, alloc))
			}
		case col == PredPods:
			alloc := n.Allocatable[string(corev1.ResourcePods)]
			if alloc > 0 && n.Pods+1 > alloc {
				c.OK, c.Detail = no(), fmt.Sprintf("%d of %d pods", n.Pods, alloc)
			} else {
				c.OK = yes()
			}
		case col == PredPorts:
			var taken []string
			for _, p := range hostPorts(w.Spec) {
				if n.Ports[p] {
					taken = append(taken, p)
				}
			}
			if len(taken) > 0 {
				c.OK, c.Detail = no(), "host port taken: "+strings.Join(taken, ", ")
			} else {
				c.OK = yes()
			}
		case col == PredVolumes:
			c.OK = yes()
			for _, v := range w.VolumeZones {
				if !nodeSelectorMatches(v.Terms, n) {
					c.OK, c.Detail = no(), "the volume of claim "+v.Claim+" cannot be reached from this node"
					break
				}
			}
		case col == PredPodAffinity, col == PredSpread:
			c = w.placeCell(col, w.rule(col), n.Index)
		default:
			c.Detail = "per the scheduler"
		}
		if c.OK != nil && !*c.OK {
			f.Failing++
			switch col {
			case PredCordon, PredTaints, PredAffinity, PredVolumes, PredPodAffinity, PredSpread:
				f.Excluded++
			}
		}
		f.Cells = append(f.Cells, c)
	}
	return f
}

// rule is the placement answer for a column, nil where there is none.
func (w *Want) rule(col string) *Rule {
	if w.Place == nil {
		return nil
	}
	if col == PredPodAffinity {
		return &w.Place.Affinity
	}
	return &w.Place.Spread
}

// placeCell is one node's cell of a placement column: its computed answer, or
// gray, "per the scheduler", where there is none.
func (w *Want) placeCell(col string, r *Rule, node int) Cell {
	if r == nil || !r.Computed || node >= len(r.OK) {
		return Cell{Key: col, Detail: "per the scheduler"}
	}
	c := Cell{Key: col}
	if r.OK[node] {
		c.OK = yes()
		return c
	}
	c.OK = no()
	if node < len(r.Detail) {
		c.Detail = r.Detail[node]
	}
	return c
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// tolerates is the scheduler's own rule (`v1helper.TolerationsTolerateTaint`):
// the effect matches or is empty, and the key matches with Exists, or the
// value too with Equal; an empty key with Exists tolerates everything.
func tolerates(tolerations []corev1.Toleration, taint corev1.Taint) bool {
	for _, t := range tolerations {
		if t.Effect != "" && t.Effect != taint.Effect {
			continue
		}
		if t.Key == "" && t.Operator == corev1.TolerationOpExists {
			return true
		}
		if t.Key != taint.Key {
			continue
		}
		switch t.Operator {
		case corev1.TolerationOpExists:
			return true
		case corev1.TolerationOpEqual, "":
			if t.Value == taint.Value {
				return true
			}
		}
	}
	return false
}

// untolerated is the node's NoSchedule and NoExecute taints the pod does not
// tolerate, written key=value:effect. The cordon's own taint is the cordon
// column's, not counted twice.
func untolerated(tolerations []corev1.Toleration, taints []corev1.Taint) []string {
	var out []string
	for _, taint := range taints {
		if taint.Effect == corev1.TaintEffectPreferNoSchedule || taint.Key == unschedulableKey {
			continue
		}
		if !tolerates(tolerations, taint) {
			s := taint.Key
			if taint.Value != "" {
				s += "=" + taint.Value
			}
			out = append(out, s+":"+string(taint.Effect))
		}
	}
	return out
}

// matchesNode is nodeSelector and the required node affinity.
func matchesNode(spec corev1.PodSpec, n *Node) (bool, string) {
	for k, v := range spec.NodeSelector {
		if got, ok := n.Labels[k]; !ok || got != v {
			return false, "no label " + k + "=" + v
		}
	}
	if spec.Affinity != nil && spec.Affinity.NodeAffinity != nil {
		req := spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		if req != nil && !nodeSelectorMatches(req, n) {
			return false, "required node affinity not met"
		}
	}
	return true, ""
}

// nodeSelectorMatches is a NodeSelector: its terms ORed, each term's
// expressions and fields ANDed. A selector with no terms matches nothing, as
// the API defines it.
func nodeSelectorMatches(sel *corev1.NodeSelector, n *Node) bool {
	if sel == nil {
		return true
	}
	for _, term := range sel.NodeSelectorTerms {
		if len(term.MatchExpressions) == 0 && len(term.MatchFields) == 0 {
			continue
		}
		ok := true
		for _, e := range term.MatchExpressions {
			v, has := n.Labels[e.Key]
			if !requirement(e.Operator, e.Values, v, has) {
				ok = false
				break
			}
		}
		for _, e := range term.MatchFields {
			if !ok {
				break
			}
			if e.Key != "metadata.name" || !requirement(e.Operator, e.Values, n.Name, true) {
				ok = false
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func requirement(op corev1.NodeSelectorOperator, values []string, v string, has bool) bool {
	switch op {
	case corev1.NodeSelectorOpIn:
		return has && contains(values, v)
	case corev1.NodeSelectorOpNotIn:
		return !has || !contains(values, v)
	case corev1.NodeSelectorOpExists:
		return has
	case corev1.NodeSelectorOpDoesNotExist:
		return !has
	case corev1.NodeSelectorOpGt, corev1.NodeSelectorOpLt:
		if !has || len(values) != 1 {
			return false
		}
		got, err1 := strconv.ParseInt(v, 10, 64)
		want, err2 := strconv.ParseInt(values[0], 10, 64)
		if err1 != nil || err2 != nil {
			return false
		}
		if op == corev1.NodeSelectorOpGt {
			return got > want
		}
		return got < want
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// hostPorts is every host port the pod asks for, "proto/ip/port", as the
// scheduler's NodePorts plugin keys them (an empty ip is 0.0.0.0).
func hostPorts(spec corev1.PodSpec) []string {
	var out []string
	add := func(cs []corev1.Container) {
		for _, c := range cs {
			for _, p := range c.Ports {
				if p.HostPort > 0 {
					out = append(out, portKey(p))
				}
			}
		}
	}
	add(spec.InitContainers)
	add(spec.Containers)
	return out
}

func portKey(p corev1.ContainerPort) string {
	proto := string(p.Protocol)
	if proto == "" {
		proto = "TCP"
	}
	ip := p.HostIP
	if ip == "" {
		ip = "0.0.0.0"
	}
	return fmt.Sprintf("%s/%s/%d", proto, ip, p.HostPort)
}

// Format writes an amount for a person: CPU in cores or millicores, bytes in
// binary units, anything else as a count.
func Format(name string, v int64) string {
	switch name {
	case "cpu":
		if v%1000 == 0 {
			return strconv.FormatInt(v/1000, 10)
		}
		if v >= 1000 {
			return strconv.FormatFloat(float64(v)/1000, 'f', 1, 64)
		}
		return strconv.FormatInt(v, 10) + "m"
	case "memory", "ephemeral-storage":
		return bytes(v)
	}
	if strings.HasPrefix(name, "hugepages-") {
		return bytes(v)
	}
	return strconv.FormatInt(v, 10)
}

func bytes(v int64) string {
	const unit = 1024
	units := []string{"", "Ki", "Mi", "Gi", "Ti"}
	f := float64(v)
	i := 0
	for f >= unit && i < len(units)-1 {
		f /= unit
		i++
	}
	if i == 0 {
		return strconv.FormatInt(v, 10)
	}
	if f >= 10 || f == float64(int64(f)) {
		return strconv.FormatFloat(f, 'f', 0, 64) + units[i]
	}
	return strconv.FormatFloat(f, 'f', 1, 64) + units[i]
}

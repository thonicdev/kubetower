package kubeadjust

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/extras/kubeadjust/rightsizing"
)

// The Nodes view and the cluster totals above the namespace grid, with the
// markup in JSX and the geometry of every bar kept here, so the page places
// what it is given.

// nodeCard is one machine.
type nodeCard struct {
	Name  string `json:"name"`
	Ready bool   `json:"ready"`
	// Pods and MaxPods are the ceiling nothing else on this page shows: a node
	// at 108 of 110 pods is full whatever its CPU gauge says. PodsTight is the
	// same fact as a judgment, at nine tenths.
	Pods      int  `json:"pods"`
	MaxPods   int  `json:"maxPods"`
	PodsTight bool `json:"podsTight"`
	// Roles, and whether one of them is the control plane.
	Roles        []string `json:"roles"`
	ControlPlane bool     `json:"controlPlane"`
	// What the machine is: a node created four hours ago at 90 % is a different
	// story from one at 90 % since March.
	Age     string `json:"age"`
	OSImage string `json:"osImage"`
	Kernel  string `json:"kernel"`
	Kubelet string `json:"kubelet"`
	// Pressures are the kubelet's own alarms.
	Pressures []string `json:"pressures"`
	Taints    []taint  `json:"taints"`

	// Resources are CPU, memory and ephemeral storage, drawn as the node tab
	// draws them (a track at allocatable, its figure beside it), so a node
	// reads the same in both places.
	Resources []nodeResource `json:"resources"`

	// TopPods is the heaviest few, and TotalPods how many there were.
	TopPods   []nodePod `json:"topPods"`
	TotalPods int       `json:"totalPods"`
	// More is said when the list is a sample, empty when it is the whole node.
	More string `json:"more"`
	// State is the node's tile on the Nodes view: one of the gauge's four bands
	// for the busier of its two resources, or "none" when nothing measured it.
	State string `json:"state"`
}

// taint is one taint as a badge.
type taint struct {
	// Text is the short form: the key past its prefix, with its value.
	Text string `json:"text"`
	// Effect weighs the badge: NoExecute is already evicting.
	Effect string `json:"effect"`
	// Title is the whole thing, because the short form is lossy.
	Title string `json:"title"`
}

// nodePod is one pod's share of one node.
type nodePod struct {
	Name string `json:"name"`
	// Title carries namespace/name: the namespace disambiguates two redis-0s.
	Title string   `json:"title"`
	CPU   podTrack `json:"cpu"`
	Mem   podTrack `json:"mem"`
}

// podTrack is one thin bar in a node's pod list: what the pod reserved, pale,
// and what it is doing, solid.
type podTrack struct {
	ReqPct float64 `json:"reqPct"`
	UsePct float64 `json:"usePct"`
	// Status colors the solid fill; "" when nothing was measured, and then
	// the pale fill is all there is.
	Status   string `json:"status"`
	Pct      string `json:"pct"`
	Measured bool   `json:"measured"`
	Title    string `json:"title"`
}

// totals is the strip above the namespace grid, with the label that says which
// of two different things its figures are sums of.
type totals struct {
	Namespaces int    `json:"namespaces"`
	CPUTotal   string `json:"cpuTotal"`
	CPUAvg     string `json:"cpuAvg"`
	MemTotal   string `json:"memTotal"`
	MemAvg     string `json:"memAvg"`
	// Source is "live usage", "live usage, over N of M namespaces", or
	// "requests" - said on the strip itself.
	Source       string `json:"source"`
	FromRequests bool   `json:"fromRequests"`
}

// nodeState is one count tile on the Nodes view, and a filter: clicking it
// narrows the node cards to that state.
type nodeState struct {
	// Key is the state the cards carry in State.
	Key   string `json:"key"`
	Label string `json:"label"`
	// Count is how many nodes are in that state, and null when it cannot be
	// said (no node measured, or none read): zero and unknown stay apart.
	Count *int `json:"count"`
}

// gaugeStatus is the band a percentage falls in. Four bands: at or under a
// fifth is its own band, because an unused node is a finding too.
func gaugeStatus(pct float64) string {
	switch {
	case pct >= 90:
		return "danger"
	case pct >= 70:
		return "warning"
	case pct <= 20:
		return "overkill"
	default:
		return "healthy"
	}
}

// clampPct keeps a fill inside its own track. Only the fill: the figure is
// not clamped, since limits past 100 % of allocatable is the useful number.
func clampPct(v float64) float64 {
	switch {
	case v < 0 || math.IsNaN(v):
		return 0
	case v > 100:
		return 100
	default:
		return v
	}
}

// pct is a ratio as a percentage, zero for no denominator.
func pct(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) / float64(whole) * 100
}

// nodeCards turns the capacity recap into cards.
func nodeCards(nodes []rightsizing.NodeSummary) []nodeCard {
	out := make([]nodeCard, 0, len(nodes))
	for _, n := range nodes {
		card := nodeCard{
			Name: n.Name, Ready: n.Ready, Pods: n.Pods, MaxPods: n.MaxPods,
			Roles: append([]string{}, n.Roles...), Age: age(n.Age),
			OSImage: n.OSImage, Kernel: n.Kernel, Kubelet: n.KubeletVersion,
			Pressures: append([]string{}, n.Pressures...), Taints: []taint{},
			TotalPods: n.TotalPods, TopPods: []nodePod{},
		}
		// Nine tenths, where a node stops being able to take a rollout: a
		// Deployment rolling with maxSurge needs room for one more pod first.
		card.PodsTight = n.MaxPods > 0 && n.Pods*10 >= n.MaxPods*9
		for _, role := range n.Roles {
			if role == "control-plane" || role == "master" {
				card.ControlPlane = true
			}
		}
		for _, t := range n.Taints {
			card.Taints = append(card.Taints, taint{Text: shortTaint(t), Effect: t.Effect, Title: fullTaint(t)})
		}

		card.Resources = nodeBars(n, n.Measured)
		card.State = stateOf(n)

		for _, p := range n.TopPods {
			card.TopPods = append(card.TopPods, nodePod{
				Name: p.Name, Title: p.Namespace + "/" + p.Name,
				CPU: toPodTrack(p.CPURequest, p.CPUUsed, n.AllocatableCPU, p.Measured, rightsizing.FormatCPU),
				Mem: toPodTrack(p.MemRequest, p.MemUsed, n.AllocatableMemory, p.Measured, rightsizing.FormatMemory),
			})
		}
		if n.TotalPods > len(card.TopPods) {
			card.More = fmt.Sprintf("the %d busiest of %d pods", len(card.TopPods), n.TotalPods)
		}
		out = append(out, card)
	}
	return out
}

// pagePartialText is the page's sentence for totals over some of the pods. It
// is drawn on every view, so it speaks of the page and not only of nodes.
const pagePartialText = "You may list pods in some namespaces only, so every total on this page counts only " +
	"the pods in those namespaces: the cluster and its nodes hold others you cannot see, and what they reserve, " +
	"cap and use is more than shown. No node is placed in a state."

// withholdStates takes every node out of the usage bands when its totals are
// over some of its pods: the state is "unknown" and the fill has no band
// color, since a band over part of the pods is unverifiable.
//
// The "reserved and not used" line goes too: reserved and used are each a
// lower bound, and their difference bounds nothing.
func withholdStates(cards []nodeCard) {
	for i := range cards {
		cards[i].State = stateUnknown
		for j := range cards[i].Resources {
			r := &cards[i].Resources[j]
			if r.Measured {
				r.Status = stateUnknown
			}
			r.Gap = ""
		}
	}
}

// stateUnknown is a node whose state nobody could establish from what the
// person may list.
const stateUnknown = "unknown"

// stateOf is the node's tile: the gauge's band for the busier of its two
// resources, so a node is "barely used" only when both are under a fifth and
// "near capacity" when either is past nine tenths. "none" means nothing
// measured it.
func stateOf(n rightsizing.NodeSummary) string {
	if !n.Measured {
		return string(rightsizing.StatusNone)
	}
	busier := math.Max(pct(n.UsedCPU, n.AllocatableCPU), pct(n.UsedMemory, n.AllocatableMemory))
	return gaugeStatus(busier)
}

// nodeStates counts the cards into the tiles, in the gauge's order. read says
// whether nodes and pods were read at all; when they were not, every count is
// unknown.
func nodeStates(cards []nodeCard, read bool) []nodeState {
	tiles := []nodeState{
		{Key: "danger", Label: "Near capacity"},
		{Key: "warning", Label: "Busy"},
		{Key: "healthy", Label: "Balanced"},
		{Key: "overkill", Label: "Barely used"},
		{Key: string(rightsizing.StatusNone), Label: "Not measured"},
	}
	if !read {
		return tiles
	}
	counts := map[string]int{}
	measured := 0
	for _, c := range cards {
		counts[c.State]++
		if c.State != string(rightsizing.StatusNone) {
			measured++
		}
	}
	for i := range tiles {
		// A usage band over nodes none of which was measured is not zero: it is
		// unknown, and drawn as such.
		if tiles[i].Key != string(rightsizing.StatusNone) && measured == 0 && len(cards) > 0 {
			continue
		}
		n := counts[tiles[i].Key]
		tiles[i].Count = &n
	}
	return tiles
}

// toPodTrack is one pod's two fills on one node's scale. The figure beside it is
// the measurement where there is one and the reservation where there is not -
// and the two are never colored the same.
func toPodTrack(request, used, allocatable int64, measured bool, format func(int64) string) podTrack {
	reqPct, usePct := clampPct(pct(request, allocatable)), clampPct(pct(used, allocatable))
	out := podTrack{ReqPct: round1(reqPct), Measured: measured}
	if measured {
		out.UsePct = round1(usePct)
		out.Status = gaugeStatus(usePct)
		out.Pct = percentText(usePct)
		out.Title = fmt.Sprintf("reserved %s (%s) · used %s (%s)",
			format(request), percentText(reqPct), format(used), percentText(usePct))
	} else {
		out.Pct = percentText(reqPct)
		out.Title = fmt.Sprintf("reserved %s (%s) · no usage was reported for this pod",
			format(request), percentText(reqPct))
	}
	return out
}

// nodeCoverage says how much of a node's containers the usage describes, and
// nothing when it describes all of them.
func nodeCoverage(n rightsizing.NodeSummary) string {
	if !n.Measured || n.ContainersMeasured >= n.Containers {
		return ""
	}
	return fmt.Sprintf("%d of %d containers measured", n.ContainersMeasured, n.Containers)
}

// percentText is a percentage as a whole number with its sign.
func percentText(v float64) string { return fmt.Sprintf("%.0f%%", v) }

// age is how long a node has existed, at one significant unit.
func age(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dy", int(d.Hours()/24/365))
	}
}

// shortTaint is the key past its domain prefix, plus the value, so badges
// sharing a prefix stay distinguishable.
func shortTaint(t rightsizing.NodeTaint) string {
	key := t.Key
	if _, after, found := strings.Cut(key, "/"); found && after != "" {
		key = after
	}
	if t.Value != "" {
		return key + "=" + t.Value
	}
	return key
}

// fullTaint is the tooltip, unabbreviated.
func fullTaint(t rightsizing.NodeTaint) string {
	out := t.Key
	if t.Value != "" {
		out += "=" + t.Value
	}
	return out + " · " + t.Effect
}

// clusterTotals sums the namespaces into the strip above the grid. Usage is
// summed and averaged over the namespaces that reported any, so a partial sum
// is never divided by a full count. With nothing measured it falls back to
// requests, and says so.
func clusterTotals(summaries []rightsizing.NamespaceSummary) *totals {
	if len(summaries) == 0 {
		return nil
	}
	var cpuUsed, memUsed, cpuReq, memReq int64
	var measured, all int
	for _, n := range summaries {
		all++
		cpuReq += n.CPURequest
		memReq += n.MemRequest
		if n.Measured {
			measured++
			cpuUsed += n.CPUUsed
			memUsed += n.MemUsed
		}
	}
	out := &totals{Namespaces: all, Source: "live usage"}
	count, cpu, mem := measured, cpuUsed, memUsed
	if measured == 0 {
		cpu, mem, count = cpuReq, memReq, all
		out.Source, out.FromRequests = "requests", true
	}
	out.CPUTotal = rightsizing.FormatCPU(cpu)
	out.MemTotal = rightsizing.FormatMemory(mem)
	if count > 0 {
		out.CPUAvg = rightsizing.FormatCPU(cpu / int64(count))
		out.MemAvg = rightsizing.FormatMemory(mem / int64(count))
	}
	if measured > 0 && measured < all {
		out.Source = fmt.Sprintf("live usage, over %d of %d namespaces", measured, all)
	}
	return out
}

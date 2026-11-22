package kubeadjust

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/extras/kubeadjust/rightsizing"
)

// The tab in a Node's panel: for one node, what it can give, what its pods
// reserve and cap, what they use where measured, the pods that drive it, and
// the recommendations that concern it - the node's own findings and the
// engine's suggestions for every workload with a pod on it.
//
// It reads what the page reads, cluster-wide, through the same reviewed doors:
// a workload's suggestion pools every replica, not only the one on this node,
// so a node-only read would size a workload from a third of its pods. **The one
// exception is the kubelets**: only this node's is asked, because a kubelet
// read is one request per node through the cluster's one rate limiter, and the
// Disk note says the figures are this node's.

// CodeUnknownNode is the answer for a node name this cluster does not have.
const CodeUnknownNode = "unknown_node"

// nodeAnswer is the tab.
type nodeAnswer struct {
	Cluster string `json:"cluster"`
	Node    string `json:"node"`
	// Ready is the node's own condition.
	Ready bool `json:"ready"`
	// Measured says whether per-container usage was read at all. When it was
	// not, the requests and limits are still drawn - they come from the pods'
	// specs - and every usage figure says it is not measured.
	Measured bool `json:"measured"`
	// Notes are the page's four, the same sentences - with Node capacity
	// saying what this tab read, which is more than the page when usage was
	// not readable.
	Notes []note `json:"notes"`
	// WindowReadAt is when the oldest of the window reads the History note
	// rests on was made, as on the page.
	WindowReadAt string `json:"windowReadAt,omitempty"`
	// Partial is said when the totals count only some of the node's pods: the
	// person may list pods in some namespaces only. Every reserved, capped and
	// used figure is then a lower bound, and the findings that rest on totals
	// are withheld. Empty when the totals are the whole node.
	Partial string `json:"partial"`
	// Resources are CPU, memory and ephemeral storage, always the three.
	Resources []nodeResource `json:"resources"`
	// Pods counts what holds capacity here, against the node's pod ceiling.
	Pods    int `json:"pods"`
	MaxPods int `json:"maxPods"`
	// About is what the machine is: the few facts a reader looks for first,
	// and the rest for the one who opens it.
	About nodeAbout `json:"about"`
	// Findings are about the node itself, loudest first.
	Findings []nodeFinding `json:"findings"`
	// Suggestions are the engine's, for every workload with a pod here, by
	// severity; Commands every kubectl among them, deduplicated. The tab does
	// not draw them: the KubeAdjust page does, narrowed to this node, where
	// there is room to review a command before running it.
	Suggestions []row    `json:"suggestions"`
	Commands    []string `json:"commands"`
	// Workloads is how many workloads have a pod here; null when nothing was
	// measured, so none could be judged.
	Workloads *int `json:"workloads"`
	// Flagged is how many of those carry at least one suggestion, and
	// TopWorkloads the few of them whose pods here are furthest from what they
	// reserve: a panel has room for a handful, and the page for the rest.
	Flagged      int            `json:"flagged"`
	TopWorkloads []nodeWorkload `json:"topWorkloads"`
	// PodList is every pod on the node, the heaviest reservation first.
	PodList []nodeTabPod `json:"podList"`
}

// nodeResource is one resource of one node, with the geometry of its bar: a
// track at allocatable, the reservation and the usage as fills, the limits as a
// mark (computed here, placed by the client).
type nodeResource struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Allocatable string `json:"allocatable"`
	Requested   string `json:"requested"`
	Limited     string `json:"limited"`
	Used        string `json:"used"`
	// The percentages of allocatable, unclamped for the text; the *Bar ones
	// are clamped to the track for drawing.
	RequestedPct int `json:"requestedPct"`
	LimitedPct   int `json:"limitedPct"`
	// UsedPct is null when nothing was measured: an unmeasured node is not an
	// idle one.
	UsedPct    *int    `json:"usedPct"`
	RequestBar float64 `json:"requestBar"`
	LimitBar   float64 `json:"limitBar"`
	UseBar     float64 `json:"useBar"`
	Measured   bool    `json:"measured"`
	// Coverage says how much of the node the usage describes, empty when all.
	Coverage string `json:"coverage"`
	// Status is the band of the usage, or of the reservation when unmeasured.
	Status string `json:"status"`
	// NoAllocatable is true when the node declares none of this resource
	// (ephemeral storage on some runtimes): the figures are then sums alone.
	NoAllocatable bool `json:"noAllocatable"`
	// Gap is the over-provisioning tell, in percentage points: a node reserved
	// to 80 % and running at 20 % has sixty points the scheduler cannot give
	// away. Empty under KubeAdjust's fifteen-point noise floor, when nothing
	// was measured, and when the totals count only some of the pods.
	Gap string `json:"gap"`
}

// nodeAbout is what the Node object says about the machine. The first five
// are drawn; the rest are folded, there for whoever opens them.
type nodeAbout struct {
	Kubelet string `json:"kubelet"`
	OS      string `json:"os"`
	// Runtime is the container runtime and its version, as the kubelet
	// reports it; empty when the Node could not be read again for it.
	Runtime      string   `json:"runtime"`
	Age          string   `json:"age"`
	Kernel       string   `json:"kernel"`
	Architecture string   `json:"architecture"`
	Roles        []string `json:"roles"`
	Taints       []taint  `json:"taints"`
}

// nodeWorkload is one workload with a pod on the node and something to change.
type nodeWorkload struct {
	Workload  string `json:"workload"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Worst     string `json:"worst"`
	Findings  int    `json:"findings"`
	// Gap says how far its pods here are from what they reserve, in words:
	// what they hold and do not use, or use past their request. Empty when
	// none of its pods here was measured.
	Gap string `json:"gap"`
	// share is the larger of the two gaps as a share of the node, the order.
	share float64
}

// topWorkloadsShown is how many workloads the tab names; the page has them all.
const topWorkloadsShown = 5

// nodeFinding is one thing to know about the node itself.
type nodeFinding struct {
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// nodeTabPod is one pod on the node.
type nodeTabPod struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// Workload is "Kind name" of what owns it, as the page files it.
	Workload   string `json:"workload"`
	CPURequest string `json:"cpuRequest"`
	CPULimit   string `json:"cpuLimit"`
	CPUUsed    string `json:"cpuUsed"`
	MemRequest string `json:"memRequest"`
	MemLimit   string `json:"memLimit"`
	MemUsed    string `json:"memUsed"`
	// Share is the larger of the pod's CPU and memory reservation as a
	// percentage of the node: what it holds of this machine.
	Share    int  `json:"share"`
	Measured bool `json:"measured"`
	// Findings is how many suggestions its workload carries.
	Findings int    `json:"findings"`
	Worst    string `json:"worst"`
}

func (k KubeAdjust) node(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, name := r.PathValue("cluster"), r.PathValue("name")
		if len(validation.IsDNS1123Subdomain(name)) > 0 {
			writeProblem(w, http.StatusBadRequest, CodeUnknownNode, "\""+name+"\" is not a node name.", nil)
			return
		}
		read, err := k.read(r, h, cluster, "", name)
		if err != nil {
			writeReadFailure(w, cluster, err)
			return
		}
		pods, nodes, scope, err := k.nodeInputs(r, h, cluster, read)
		if err != nil {
			writeReadFailure(w, cluster, err)
			return
		}
		if nodes == nil {
			// A person who may not list nodes is refused, and told so: a 403,
			// never a 502 about a cluster that answered.
			if refused, ok := extension.RefusedRead(scope.nodesErr,
				"The cluster says you may not list nodes, so no node can be drawn."); ok {
				writeProblem(w, refused.Status, refused.Code, refused.Message, scope.nodesErr)
				return
			}
			writeProblem(w, http.StatusBadGateway, CodeListFailed,
				"This cluster's nodes could not be read, so no node can be drawn.", nil)
			return
		}
		at := -1
		for i := range nodes {
			if nodes[i].Name == name {
				at = i
			}
		}
		if at < 0 {
			writeProblem(w, http.StatusNotFound, CodeUnknownNode, "No node \""+name+"\" in this cluster.", nil)
			return
		}
		mark := time.Now()
		out := shapeNode(read, cluster, nodes[at], pods, scope)
		out.About.Runtime, out.About.Architecture = k.nodeSystem(r, h, cluster, name, read.nodes)
		read.timing.since("shape", mark)
		writeTimedJSON(w, http.StatusOK, out, &read.timing)
	})
}

// nodeSystem is the node's container runtime and architecture, which the
// summaries do not keep: out of the nodes the display already read, or, when
// it read none (usage was not measurable), listed here. A failed read leaves
// both empty, and the tab says they were not read: they are details, never a
// reason to refuse the tab.
func (k KubeAdjust) nodeSystem(r *http.Request, h extension.Host, cluster, name string, nodes []corev1.Node) (runtime, arch string) {
	if nodes == nil {
		page, err := h.List(r, cluster, extension.ListQuery{GVR: nodesGVR})
		if err != nil {
			return "", ""
		}
		nodes, _ = typed[corev1.Node](page.Items)
	}
	for i := range nodes {
		if nodes[i].Name == name {
			return nodes[i].Status.NodeInfo.ContainerRuntimeVersion, nodes[i].Status.NodeInfo.Architecture
		}
	}
	return "", ""
}

// nodeScope is what the tab's totals were built from: the Node capacity note,
// and whether the pods were narrowed.
type nodeScope struct {
	// nodesErr is why the nodes could not be listed, nil when they were.
	nodesErr error
	capacity rightsizing.Availability
	complete bool
}

// partialText is the tab's sentence for totals over some of a node's pods.
const partialText = "You may list pods in some namespaces only, so these totals count only the pods in those: " +
	"the node holds others you cannot see, and what it reserves, caps and uses is more than drawn here. " +
	"The findings that rest on its totals are withheld."

// nodeInputs is the pods and the node summaries the tab draws, and what they
// were built from. When usage was read, they are the page's own; when it was
// not, the page stopped before reading pods and the tab reads them itself,
// because requests and limits need no usage - and then says what it read rather
// than the page's "not read".
func (k KubeAdjust) nodeInputs(r *http.Request, h extension.Host, cluster string, read reading) ([]corev1.Pod, []rightsizing.NodeSummary, nodeScope, error) {
	if read.podsRead {
		return read.pods, read.report.Nodes, nodeScope{capacity: read.report.Capacity, complete: read.podsComplete, nodesErr: read.nodesErr}, nil
	}
	podsPage, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR})
	if err != nil {
		return nil, nil, nodeScope{}, err
	}
	pods, _ := typed[corev1.Pod](podsPage.Items)
	nodePage, err := h.List(r, cluster, extension.ListQuery{GVR: nodesGVR})
	if err != nil {
		return pods, nil, nodeScope{nodesErr: err}, nil
	}
	nodes, bad := typed[corev1.Node](nodePage.Items)
	scope := nodeScope{complete: podsPage.Complete,
		capacity: rightsizing.Availability{OK: true, Detail: plural(len(nodes), "node")}}
	if bad > 0 {
		scope.capacity.Detail += " · " + plural(bad, "node") + " could not be read, so left out"
	}
	if !podsPage.Complete {
		scope.capacity.Detail += " · node totals count only the pods in the namespaces you may list"
	}
	return pods, rightsizing.SummariseNodes(nodes, pods, nil), scope, nil
}

// shapeNode turns one node's figures into the tab.
func shapeNode(read reading, cluster string, n rightsizing.NodeSummary, pods []corev1.Pod, scope nodeScope) nodeAnswer {
	report := read.report
	report.Capacity = scope.capacity
	out := nodeAnswer{
		Cluster: cluster, Node: n.Name, Ready: n.Ready, Measured: report.Usage.OK,
		Notes: notes(report, read.podsRead), Pods: n.Pods, MaxPods: n.MaxPods,
		WindowReadAt: readAtText(report.WindowReadAt),
		Findings:     []nodeFinding{}, Suggestions: []row{}, Commands: []string{}, PodList: []nodeTabPod{},
		TopWorkloads: []nodeWorkload{},
		About: nodeAbout{
			Kubelet: n.KubeletVersion, OS: n.OSImage, Age: age(n.Age), Kernel: n.Kernel,
			Roles: append([]string{}, n.Roles...), Taints: []taint{},
		},
	}
	for _, t := range n.Taints {
		out.About.Taints = append(out.About.Taints, taint{Text: shortTaint(t), Effect: t.Effect, Title: fullTaint(t)})
	}
	if !scope.complete {
		out.Partial = partialText
	}
	// A node running no container, on a cluster whose usage was read, uses
	// nothing: that is a measurement, not an absence of one.
	measured := n.Measured || (report.Usage.OK && n.Containers == 0)
	out.Resources = nodeBars(n, measured)
	if !scope.complete {
		// A difference of two lower bounds bounds nothing.
		for i := range out.Resources {
			out.Resources[i].Gap = ""
		}
	}
	out.Findings = nodeFindings(n, scope.complete)

	// Which workload each pod belongs to, from the readings: a workload's
	// suggestions concern this node when one of its pods runs here.
	owner := map[string]*rightsizing.WorkloadReading{}
	for i := range report.Readings {
		rd := &report.Readings[i]
		for _, c := range rd.Containers {
			for _, p := range c.Pods {
				owner[rd.Namespace+"/"+p.Pod] = rd
			}
		}
	}
	concerned := map[*rightsizing.WorkloadReading]bool{}
	for i := range pods {
		p := &pods[i]
		if p.Spec.NodeName != n.Name || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		out.PodList = append(out.PodList, tabPod(p, n, read.usage, owner[p.Namespace+"/"+p.Name]))
		if rd := owner[p.Namespace+"/"+p.Name]; rd != nil {
			concerned[rd] = true
		}
	}
	sort.SliceStable(out.PodList, func(i, j int) bool {
		if out.PodList[i].Share != out.PodList[j].Share {
			return out.PodList[i].Share > out.PodList[j].Share
		}
		return out.PodList[i].Namespace+"/"+out.PodList[i].Name < out.PodList[j].Namespace+"/"+out.PodList[j].Name
	})

	if report.Usage.OK {
		count := len(concerned)
		out.Workloads = &count
		var rows []row
		for i := range report.Readings {
			if concerned[&report.Readings[i]] {
				for _, s := range report.Readings[i].Suggestions {
					rows = append(rows, toRow(s, report.Window == WindowPending))
				}
			}
		}
		for _, g := range groups(rows) {
			out.Suggestions = append(out.Suggestions, g.Rows...)
		}
		out.Commands = commands(groups(rows))
		out.Flagged, out.TopWorkloads = topWorkloads(n, pods, read.usage, owner, concerned)
	}
	return out
}

// topWorkloads is how many workloads with a pod here carry a suggestion, and
// the few whose pods here are furthest from what they reserve, the largest gap
// first. The gap is over this node's pods only, as a share of the node, so CPU
// and memory weigh alike and a workload is ranked by what it does here.
func topWorkloads(n rightsizing.NodeSummary, pods []corev1.Pod, usage map[string]rightsizing.Usage,
	owner map[string]*rightsizing.WorkloadReading, concerned map[*rightsizing.WorkloadReading]bool) (int, []nodeWorkload) {

	type sums struct{ cpuReq, cpuUsed, memReq, memUsed int64 }
	here := map[*rightsizing.WorkloadReading]*sums{}
	for i := range pods {
		p := &pods[i]
		rd := owner[p.Namespace+"/"+p.Name]
		if rd == nil || p.Spec.NodeName != n.Name {
			continue
		}
		// Only the containers that reported usage: a request with no usage
		// beside it would read as idle when it is only unmeasured.
		for _, c := range append(append([]corev1.Container{}, p.Spec.InitContainers...), p.Spec.Containers...) {
			u, ok := usage[p.Namespace+"/"+p.Name+"/"+c.Name]
			if !ok {
				continue
			}
			s := here[rd]
			if s == nil {
				s = &sums{}
				here[rd] = s
			}
			s.cpuReq += c.Resources.Requests.Cpu().MilliValue()
			s.memReq += c.Resources.Requests.Memory().Value()
			s.cpuUsed += u.CPUMillicores
			s.memUsed += u.MemoryBytes
		}
	}

	out := []nodeWorkload{}
	for rd := range concerned {
		if len(rd.Suggestions) == 0 {
			continue
		}
		w := nodeWorkload{Workload: rd.Name, Kind: rd.Kind, Namespace: rd.Namespace, Worst: string(rd.Worst),
			Findings: len(rd.Suggestions)}
		if s := here[rd]; s != nil {
			var words []string
			if text := gapWords("CPU", s.cpuReq-s.cpuUsed, rightsizing.FormatCPU); text != "" {
				words = append(words, text)
			}
			if text := gapWords("memory", s.memReq-s.memUsed, rightsizing.FormatMemory); text != "" {
				words = append(words, text)
			}
			w.Gap = strings.Join(words, " · ")
			w.share = math.Max(math.Abs(pct(s.cpuReq-s.cpuUsed, n.AllocatableCPU)),
				math.Abs(pct(s.memReq-s.memUsed, n.AllocatableMemory)))
		}
		out = append(out, w)
	}
	rank := map[string]int{"danger": 0, "warning": 1, "overkill": 2}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.share != b.share {
			return a.share > b.share
		}
		if rank[a.Worst] != rank[b.Worst] {
			return rank[a.Worst] < rank[b.Worst]
		}
		return a.Namespace+"/"+a.Kind+"/"+a.Workload < b.Namespace+"/"+b.Kind+"/"+b.Workload
	})
	flagged := len(out)
	if len(out) > topWorkloadsShown {
		out = out[:topWorkloadsShown]
	}
	return flagged, out
}

// gapWords is one resource's gap in words: held and not used, or used past
// the request; empty when the two meet.
func gapWords(label string, gap int64, format func(int64) string) string {
	switch {
	case gap > 0:
		return label + " " + format(gap) + " idle"
	case gap < 0:
		return label + " " + format(-gap) + " over request"
	default:
		return ""
	}
}

// nodeBars is a node's three resources, as the tab and the Nodes view both
// draw them.
func nodeBars(n rightsizing.NodeSummary, measured bool) []nodeResource {
	coverage := nodeCoverage(n)
	return []nodeResource{
		nodeBar("cpu", "CPU", n.AllocatableCPU, n.RequestedCPU, n.LimitedCPU, n.UsedCPU, measured, coverage, rightsizing.FormatCPU),
		nodeBar("memory", "Memory", n.AllocatableMemory, n.RequestedMemory, n.LimitedMemory, n.UsedMemory, measured, coverage, rightsizing.FormatMemory),
		nodeBar("ephemeral-storage", "Ephemeral storage", n.AllocatableEphemeral, n.RequestedEphemeral, n.LimitedEphemeral,
			n.UsedEphemeral, n.EphemeralMeasured, "", rightsizing.FormatMemory),
	}
}

// nodeBar is one resource's figures and geometry.
func nodeBar(key, label string, allocatable, requested, limited, used int64, measured bool, coverage string,
	format func(int64) string) nodeResource {
	out := nodeResource{
		Key: key, Label: label, Requested: format(requested), Limited: format(limited),
		Measured: measured, NoAllocatable: allocatable <= 0,
	}
	if allocatable > 0 {
		out.Allocatable = format(allocatable)
	}
	if measured {
		out.Used, out.Coverage = format(used), coverage
	}
	if allocatable <= 0 {
		return out
	}
	reqPct, limPct, usePct := pct(requested, allocatable), pct(limited, allocatable), pct(used, allocatable)
	out.RequestedPct, out.LimitedPct = int(math.Round(reqPct)), int(math.Round(limPct))
	out.RequestBar, out.LimitBar = round1(clampPct(reqPct)), round1(clampPct(limPct))
	out.Status = gaugeStatus(reqPct)
	if measured {
		u := int(math.Round(usePct))
		out.UsedPct = &u
		out.UseBar = round1(clampPct(usePct))
		out.Status = gaugeStatus(usePct)
		if reqPct > usePct+15 {
			out.Gap = fmt.Sprintf("%.0f points reserved and not used", reqPct-usePct)
		}
	}
	return out
}

// nodeFindings are the node's own recommendations, loudest first. The
// thresholds are the gauge's (nine tenths, seven tenths) and a fifteen-point
// noise floor for "reserved and not used".
//
// complete is false when the totals count only the pods the person may list:
// every finding resting on a total - reserved, capped, used, the pod count -
// would then understate the node, so they are withheld and that is said. The
// node's own state (Ready, pressure) is not a total and stays.
func nodeFindings(n rightsizing.NodeSummary, complete bool) []nodeFinding {
	out := []nodeFinding{}
	add := func(kind, title, detail string) {
		out = append(out, nodeFinding{Kind: kind, Title: title, Detail: detail})
	}

	if !n.Ready {
		add("danger", "Not ready", "Nothing scheduled here runs, and its capacity is counted nowhere until it returns.")
	}
	for _, p := range n.Pressures {
		add("danger", "Under "+p, "The kubelet is evicting pods to protect the node; those using more than they requested go first.")
	}
	if !complete {
		add("warning", "Totals over some of its pods",
			"Only the pods in the namespaces you may list are counted here, so nothing is said about how full this node is.")
		rank := map[string]int{"danger": 0, "warning": 1, "overkill": 2}
		sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Kind] < rank[out[j].Kind] })
		return out
	}

	if n.AllocatableMemory > 0 && n.LimitedMemory > n.AllocatableMemory {
		add("warning", fmt.Sprintf("Memory limits are %.0f%% of what the node has", pct(n.LimitedMemory, n.AllocatableMemory)),
			"Limits are not reserved, so this is allowed - but if the pods reach them together the node runs out, "+
				"and the kernel kills whatever it picks. Bringing the largest limits nearer their requests contains that.")
	}
	for _, r := range []struct {
		label            string
		requested, alloc int64
		format           func(int64) string
	}{
		{"CPU", n.RequestedCPU, n.AllocatableCPU, rightsizing.FormatCPU},
		{"memory", n.RequestedMemory, n.AllocatableMemory, rightsizing.FormatMemory},
	} {
		if r.alloc > 0 && pct(r.requested, r.alloc) >= 90 {
			add("warning", fmt.Sprintf("%s is %.0f%% reserved", capital(r.label), pct(r.requested, r.alloc)),
				fmt.Sprintf("The scheduler places nothing here that asks more than the %s left, whatever the node is using.",
					r.format(max(r.alloc-r.requested, 0))))
		}
	}
	if n.MaxPods > 0 && n.Pods*10 >= n.MaxPods*9 {
		add("warning", fmt.Sprintf("%d of %d pods", n.Pods, n.MaxPods),
			"Near the node's pod ceiling: a rollout needing one more pod here waits, whatever the CPU and memory say.")
	}
	if n.AllocatableEphemeral > 0 && n.EphemeralMeasured && pct(n.UsedEphemeral, n.AllocatableEphemeral) >= 80 {
		add("danger", fmt.Sprintf("Pods hold %.0f%% of the node's ephemeral storage", pct(n.UsedEphemeral, n.AllocatableEphemeral)),
			"Writable layers, logs and emptyDirs: past the kubelet's threshold it reports disk pressure and evicts.")
	}

	// Reserved and idle, and the other way round: only when the usage
	// describes the whole node, since a partial sum would invent the gap.
	measuredAll := n.Measured && n.ContainersMeasured >= n.Containers
	if measuredAll && n.AllocatableMemory > 0 {
		if over := pct(n.UsedMemory, n.AllocatableMemory) - pct(n.RequestedMemory, n.AllocatableMemory); over > 15 {
			add("warning", "Pods use more memory than they reserved",
				fmt.Sprintf("%s used against %s requested: under memory pressure the kubelet evicts the pods above their requests first.",
					rightsizing.FormatMemory(n.UsedMemory), rightsizing.FormatMemory(n.RequestedMemory)))
		}
	}
	if measuredAll {
		var idle []string
		if n.AllocatableCPU > 0 && pct(n.RequestedCPU, n.AllocatableCPU)-pct(n.UsedCPU, n.AllocatableCPU) > 15 {
			idle = append(idle, rightsizing.FormatCPU(n.RequestedCPU-n.UsedCPU)+" of CPU")
		}
		if n.AllocatableMemory > 0 && pct(n.RequestedMemory, n.AllocatableMemory)-pct(n.UsedMemory, n.AllocatableMemory) > 15 {
			idle = append(idle, rightsizing.FormatMemory(n.RequestedMemory-n.UsedMemory)+" of memory")
		}
		if len(idle) > 0 {
			detail := idle[0]
			if len(idle) == 2 {
				detail += " and " + idle[1]
			}
			add("overkill", "Reserved and not used",
				detail+" is held by requests and not used: the scheduler cannot give it to anything else. "+
					"The suggestions below say which workloads hold it.")
		}
	}

	rank := map[string]int{"danger": 0, "warning": 1, "overkill": 2}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Kind] < rank[out[j].Kind] })
	return out
}

// tabPod is one pod's line in the tab.
func tabPod(p *corev1.Pod, n rightsizing.NodeSummary, usage map[string]rightsizing.Usage, rd *rightsizing.WorkloadReading) nodeTabPod {
	cpuReq, cpuLim := rightsizing.PodResources(p, corev1.ResourceCPU)
	memReq, memLim := rightsizing.PodResources(p, corev1.ResourceMemory)
	out := nodeTabPod{
		Name: p.Name, Namespace: p.Namespace,
		CPURequest: rightsizing.FormatCPU(cpuReq), MemRequest: rightsizing.FormatMemory(memReq),
		CPULimit: "none", MemLimit: "none",
		Share: int(math.Round(math.Max(pct(cpuReq, n.AllocatableCPU), pct(memReq, n.AllocatableMemory)))),
	}
	if cpuLim > 0 {
		out.CPULimit = rightsizing.FormatCPU(cpuLim)
	}
	if memLim > 0 {
		out.MemLimit = rightsizing.FormatMemory(memLim)
	}
	var cpu, mem int64
	for _, c := range append(append([]corev1.Container{}, p.Spec.InitContainers...), p.Spec.Containers...) {
		if u, ok := usage[p.Namespace+"/"+p.Name+"/"+c.Name]; ok {
			out.Measured = true
			cpu += u.CPUMillicores
			mem += u.MemoryBytes
		}
	}
	if out.Measured {
		out.CPUUsed, out.MemUsed = rightsizing.FormatCPU(cpu), rightsizing.FormatMemory(mem)
	}
	if rd != nil {
		out.Workload = rd.Kind + " " + rd.Name
		out.Findings = len(rd.Suggestions)
		out.Worst = string(rd.Worst)
	}
	return out
}

func capital(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}

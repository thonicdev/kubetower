package kubeadjust

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/thonicdev/kubetower/extras/kubeadjust/rightsizing"
)

// The answer, shaped for the page: numbers and states, no HTML concerns
// (classes, hrefs, template.CSS).
//
// **Three states for everything the page draws, and JSON carries all three.**
// A view's list is `null` when that view was not built, `[]` when it was built
// and holds nothing, and a list otherwise. One answer per scope: the whole
// cluster builds the overview's and the Nodes view's bodies, one namespace
// builds its own, and which of the cluster's two is on screen is the page's.
// A figure is a string when it was measured and `""` or `null` when it was
// not, with `measured` beside it - never a zero standing in for an absence.

// answer is the whole page.
type answer struct {
	Cluster string `json:"cluster"`
	// Namespace is the scope, empty for the whole cluster.
	Namespace string `json:"namespace"`
	// Namespaces is the selector. The selected namespace is always in it.
	Namespaces     []string `json:"namespaces"`
	NamespacesNote string   `json:"namespacesNote,omitempty"`

	// Notes are what the answer was built from, and what it could not be built
	// from: Usage, Node capacity, Disk and History, always the four, never an
	// empty sentence.
	Notes []note `json:"notes"`
	// Window is the state of the usage window, as the core answered it:
	// "read", "no-prometheus", "unreadable" or "empty", and ""
	// when none was asked for because nothing could be measured. It is what
	// says whether a Prometheus was found; the History note says the rest.
	Window string `json:"window"`
	// WindowReadAt is when that window was read, RFC 3339 in UTC: a window the
	// console kept from an earlier read is that old, and the page says so.
	// Absent when no window was read or the time is not known.
	WindowReadAt string `json:"windowReadAt,omitempty"`
	// Ready is false when nothing could be measured: no suggestion is then a
	// cluster nobody could size, not a cluster with nothing to change.
	Ready bool `json:"ready"`
	// Workloads is how many were examined, so "7 suggestions" can be read
	// against it; null when nothing could be measured, so none was examined.
	Workloads *int `json:"workloads"`
	// Counts are the three severities, counted before the filter.
	Counts []count `json:"counts"`
	// Categories are the CPU / Memory / Storage filters, each counted over
	// every suggestion. Which one is in force is the page's: it filters Groups
	// and Commands itself, so a chip costs no read of the cluster.
	Categories []category `json:"categories"`
	// Groups are every suggestion in scope, by severity, empty groups left
	// out. A filtered list is these rows kept to one category, in this order.
	Groups []group `json:"groups"`
	// Commands is every kubectl the suggestions carry, deduplicated, in the
	// order they are drawn: one block to review before any of it is run.
	Commands []string `json:"commands"`

	// Headline is what to read first, in every view: the
	// findings by severity, what the over-provisioned requests would give
	// back, and how much of what is requested is used. Null when nothing was
	// measured.
	Headline *headline `json:"headline"`
	// Top is what to act on first across the scope, for the overview: the
	// risks, then the reductions that give back the most. Each row names its
	// namespace, where its card is.
	Top []row `json:"top"`

	// The overview's body.
	Totals         *totals         `json:"totals"`
	NamespaceCards []namespaceCard `json:"namespaceCards"`
	// The Nodes view's body, and its count tiles.
	NodeCards  []nodeCard  `json:"nodeCards"`
	NodeStates []nodeState `json:"nodeStates"`
	// Partial is said when the pods were narrowed to the namespaces the person
	// may list: every node total is then a sum over some of its pods, so no
	// node is placed in a band (State "unknown", every tile's count null) and
	// the headline's scope says whose namespaces it sums. Empty when whole.
	Partial string `json:"partial"`
	// One namespace's body.
	Cards []workloadCard `json:"cards"`
}

// note is one line of provenance.
type note struct {
	// OK selects the tone: a fact when true, a caveat when false.
	OK     bool   `json:"ok"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

// count is one severity and how many of it there are. Count is null when
// nothing could be measured: no suggestion on a cluster nobody could size is
// not zero suggestions.
type count struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Count *int   `json:"count"`
}

// category is one of the three filters.
type category struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count *int   `json:"count"`
}

// group is one severity of suggestion.
type group struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Mark is the glyph, so the three are told apart without color.
	Mark string `json:"mark"`
	Rows []row  `json:"rows"`
}

// row is one suggestion, ready to draw.
type row struct {
	Workload      string   `json:"workload"`
	WorkloadKind  string   `json:"workloadKind"`
	Namespace     string   `json:"namespace"`
	Pod           string   `json:"pod"`
	PodCount      int      `json:"podCount"`
	Container     string   `json:"container"`
	Resource      string   `json:"resource"`
	Kind          string   `json:"kind"`
	Action        string   `json:"action"`
	Message       string   `json:"message"`
	Current       string   `json:"current"`
	Suggested     string   `json:"suggested"`
	AppliesToBoth bool     `json:"appliesToBoth"`
	Warnings      []string `json:"warnings"`
	// Kubectl is the command that would apply it, empty when the change cannot
	// be expressed as one - a PVC, an emptyDir, a bare pod - or when no value
	// is recommended.
	Kubectl string `json:"kubectl"`
	// Confidence is "high", "medium" or "low" for a CPU or memory row: how
	// much usage the figure rests on (rightsizing/confidence.go). Empty for a
	// disk row, which reads a level rather than sizing from a distribution.
	Confidence string `json:"confidence"`
	// InsufficientData is a row whose container has too little usable usage
	// to name a value: Suggested is then "" and there is no command.
	InsufficientData bool `json:"insufficientData"`
	// Scope is where the object is, for the row's second line.
	Scope string `json:"scope"`
	// suggestedRaw and pairedRequestRaw are kept off the wire, for ordering
	// by what a row gives back.
	suggestedRaw     int64
	pairedRequestRaw int64
}

// workloadCard is one workload, drawn whole.
type workloadCard struct {
	Workload  string `json:"workload"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Replicas  int    `json:"replicas"`
	QOS       string `json:"qos"`
	// Managed says what else sizes this workload - an HPA, a VPA - and what
	// that withholds here; empty when nothing does.
	Managed []string `json:"managed"`
	// Worst is the loudest finding on this workload, or empty.
	Worst      string      `json:"worst"`
	Containers []container `json:"containers"`
	Findings   []row       `json:"findings"`
	// Volumes are the workload's disks, per replica and never pooled.
	Volumes []volume `json:"volumes"`
	// StorageOK says whether the kubelets were asked at all, so an empty
	// Volumes list can be read: a workload with no volumes and a workload on a
	// cluster that refuses nodes/proxy draw the same nothing otherwise.
	StorageOK bool `json:"storageOK"`
}

// container is one container of one workload.
type container struct {
	Name     string `json:"name"`
	Restarts int    `json:"restarts"`
	// CrashLooping says the restarts are suppressing advice now.
	CrashLooping bool `json:"crashLooping"`
	OOMKilled    bool `json:"oomKilled"`
	// OOMKilledEarlier is a kill for memory more than a day ago: no alarm,
	// and still no memory reduction.
	OOMKilledEarlier bool `json:"oomKilledEarlier"`
	// MemoryRaised is the raise of the memory limit that holds back every
	// reduction of it (rightsizing/rollout.go), null when none does.
	MemoryRaised *memoryRaised `json:"memoryRaised"`
	CPU          bar           `json:"cpu"`
	Memory       bar           `json:"memory"`
	// Ephemeral is the writable layer plus the logs, against the
	// `ephemeral-storage` limit; unmeasured where nodes/proxy is refused.
	Ephemeral bar `json:"ephemeral"`
	// Pods is the per-replica breakdown, behind a disclosure.
	Pods []podFigure `json:"pods"`
}

// memoryRaised is a raise of a memory limit, formatted. To is "unlimited" when
// the raise removed the limit.
type memoryRaised struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Revision int64  `json:"revision"`
}

func toRaised(m *rightsizing.MemoryRaise) *memoryRaised {
	if m == nil {
		return nil
	}
	word := func(v int64) string {
		if v == 0 {
			return "unlimited"
		}
		return rightsizing.FormatMemory(v)
	}
	return &memoryRaised{From: word(m.From), To: word(m.To), Revision: m.Revision}
}

// bar is one resource of one container, ready to draw.
//
// The track is the limit, the fill is the mean use, and the request
// and the peak are marks along it. The percentages are of the track,
// 0-100, computed here.
type bar struct {
	Label string `json:"label"`
	// Measured separates a container nothing reported on from an idle one. An
	// unmeasured bar is drawn empty and labeled, never as a zero.
	Measured bool `json:"measured"`
	// Status is danger, warning, overkill, healthy, or none (not measured).
	Status string `json:"status"`
	// The four figures, formatted; Request and Limit are "" when the spec sets
	// none.
	Mean    string `json:"mean"`
	Peak    string `json:"peak"`
	Request string `json:"request"`
	Limit   string `json:"limit"`
	// UsePct, ReqPct and PeakPct place the fill and the two marks.
	UsePct  float64 `json:"usePct"`
	ReqPct  float64 `json:"reqPct"`
	PeakPct float64 `json:"peakPct"`
	// Pct is the mean as a percentage of the limit, for the badge; null when
	// there is no limit to be a percentage of, or nothing measured.
	Pct *int `json:"pct"`
	// Scale says what the track represents, because it is not always the limit.
	Scale string `json:"scale"`
	// Headroom is the limit minus the peak, or "none"; "" when there is no limit.
	Headroom string `json:"headroom"`
	// Unbounded marks a measured resource with no limit at all.
	Unbounded bool `json:"unbounded"`
	// Insufficient is too little usable usage to size this resource from: the
	// figures are drawn, and no value is recommended for it.
	Insufficient bool `json:"insufficient"`
}

// podFigure is one replica's own reading.
type podFigure struct {
	Pod      string `json:"pod"`
	CPU      string `json:"cpu"`
	Memory   string `json:"memory"`
	Measured bool   `json:"measured"`
}

// volume is one disk of one replica, ready to draw.
type volume struct {
	Pod   string `json:"pod"`
	Name  string `json:"name"`
	Claim string `json:"claim"`
	// Kind is "pvc" or "emptyDir": expanded in completely different ways.
	Kind     string `json:"kind"`
	Status   string `json:"status"`
	Pct      string `json:"pct"`
	Used     string `json:"used"`
	Free     string `json:"free"`
	Capacity string `json:"capacity"`
	// UsePct is the fill's width, null when there is no fill to draw: a disk
	// nobody measured is not an empty disk.
	UsePct *float64 `json:"usePct"`
	// Measured separates a disk the kubelet did not report on - or reported on
	// from a shared export - from an empty one.
	Measured bool `json:"measured"`
	// CeilingWord is what the capacity figure is called: a claim's capacity,
	// an emptyDir's sizeLimit, or the node's own disk when there is none.
	CeilingWord string `json:"ceilingWord"`
	// PctOf says what the percentage is a percentage of, for its tooltip.
	PctOf string `json:"pctOf"`
	// Unbounded is an emptyDir with no sizeLimit.
	Unbounded bool `json:"unbounded"`
}

// namespaceCard is one namespace in the overview grid.
type namespaceCard struct {
	Name      string `json:"name"`
	Pods      int    `json:"pods"`
	Workloads int    `json:"workloads"`
	Findings  int    `json:"findings"`
	// Measured is false when nothing measured this namespace.
	Measured bool `json:"measured"`
	// Band is "overcommit", "high", "ok", "norequests", or "" when nothing was
	// measured - two absences that must not be confused with each other.
	Band  string `json:"band"`
	Worst string `json:"worst"`
	// The two ratios, "×4.2", "none set" (measured, no request) or "—"
	// (nothing measured): never a ratio of one.
	CPURatio   string `json:"cpuRatio"`
	MemRatio   string `json:"memRatio"`
	CPURequest string `json:"cpuRequest"`
	CPUUsed    string `json:"cpuUsed"`
	MemRequest string `json:"memRequest"`
	MemUsed    string `json:"memUsed"`
	// Coverage says how much of the namespace the used figures describe, empty
	// when they describe all of it.
	Coverage string `json:"coverage"`
	// CPUIdle and MemIdle are what the measured containers reserve and do not
	// use - the namespace's waste, in the unit it is paid in. "none" when
	// usage meets the requests, "" when nothing measured it.
	CPUIdle string `json:"cpuIdle"`
	MemIdle string `json:"memIdle"`
}

// headline is the strip at the top of the page.
type headline struct {
	// Scope is what the figures are about: "this cluster" or a namespace.
	Scope string `json:"scope"`
	// AtRisk, Worth and Over are the three severities, as Counts has them.
	AtRisk int `json:"atRisk"`
	Worth  int `json:"worth"`
	Over   int `json:"over"`
	// ReclaimCPU and ReclaimMemory are what the request reductions on this
	// page would give back, summed over every replica: requests, because they
	// are what the scheduler reserves and what a cluster is sized for. A limit
	// reserves nothing, so a limit reduction gives nothing back and is not
	// counted. Empty when there is nothing to give back.
	ReclaimCPU       string `json:"reclaimCPU"`
	ReclaimMemory    string `json:"reclaimMemory"`
	ReclaimWorkloads int    `json:"reclaimWorkloads"`
	// UsedOfRequestedCPU and UsedOfRequestedMemory are usage as a share of the
	// requests, over the containers that were measured (the same population
	// for both halves); null when there is no request to divide by.
	UsedOfRequestedCPU    *int `json:"usedOfRequestedCPU"`
	UsedOfRequestedMemory *int `json:"usedOfRequestedMemory"`
}

// shape turns what was read into the page: everything one scope's views draw.
// The scope is the namespace, the one thing that changes what is read; the
// view between the overview and the Nodes one, and the category filter, are
// the page's to choose over the same answer, so switching either reads nothing.
func shape(read reading, cluster, namespace string) answer {
	report := read.report
	out := answer{
		Cluster: cluster, Namespace: namespace,
		Namespaces: selector(read.namespaces, namespace), NamespacesNote: read.namespacesNote,
		Ready:        report.Usage.OK,
		Notes:        notes(report, read.podsRead),
		Window:       string(report.Window),
		WindowReadAt: readAtText(report.WindowReadAt),
		Groups:       []group{},
		Commands:     []string{},
	}

	pending := report.Window == WindowPending
	rows := make([]row, 0, len(report.Suggestions))
	for _, s := range report.Suggestions {
		rows = append(rows, toRow(s, pending))
	}
	// The chips are counted over everything, and the page filters the list.
	out.Categories = categories(rows, report.Usage.OK)
	out.Groups = groups(rows)
	out.Commands = commands(out.Groups)
	out.Counts = severityCounts(report)

	// Nothing measured is nothing examined: no body is built, and every one
	// stays null rather than an empty list that would read as "none".
	if !report.Usage.OK {
		return out
	}
	examined := report.Examined
	out.Workloads = &examined
	partial := read.podsRead && !read.podsComplete
	if partial {
		out.Partial = pagePartialText
	}
	out.Headline = headlineOf(report, namespace, partial)
	out.Top = top(rows, report.Readings)

	if namespace != "" {
		out.Cards = cards(report.Readings, report.Storage.OK, pending)
		return out
	}
	// The whole cluster: both of its views, read once.
	out.NamespaceCards = namespaceCards(report.Namespaces)
	out.Totals = clusterTotals(report.Namespaces)
	out.NodeCards, out.NodeStates = nodesBody(report, partial)
	return out
}

// severityCounts are the three severities, counted before any filter. Each is
// null when nothing could be measured, for the reason count gives.
func severityCounts(report rightsizing.Report) []count {
	counts := map[rightsizing.Kind]int{}
	for _, s := range report.Suggestions {
		counts[s.Kind]++
	}
	out := make([]count, 0, 3)
	for _, band := range []struct {
		kind  rightsizing.Kind
		label string
	}{
		{rightsizing.KindDanger, "At risk"},
		{rightsizing.KindWarning, "Worth looking at"},
		{rightsizing.KindOverkill, "Over-provisioned"},
	} {
		c := count{Kind: string(band.kind), Label: band.label}
		if report.Usage.OK {
			n := counts[band.kind]
			c.Count = &n
		}
		out = append(out, c)
	}
	return out
}

// nodesBody is the Nodes view's cards and count tiles.
func nodesBody(report rightsizing.Report, partial bool) ([]nodeCard, []nodeState) {
	// Nodes the person could not list are not built: null, never [], which
	// would say the cluster has none. The tiles are still drawn, each count
	// unknown.
	var cardsOut []nodeCard
	if report.Nodes != nil {
		cardsOut = nodeCards(report.Nodes)
		if partial {
			withholdStates(cardsOut)
		}
	}
	// Narrowed, no node can be placed in a band: every count is unknown, as
	// for nodes nobody could read: a person allowed into one namespace would
	// otherwise see a busy node "Barely used".
	return cardsOut, nodeStates(cardsOut, cardsOut != nil && !partial)
}

// selector is the namespace list with the selected one always in it: a link
// into a namespace created a minute ago must not read as though nothing is
// selected.
func selector(names []string, current string) []string {
	out := append([]string{}, names...)
	found := current == ""
	for _, n := range out {
		if n == current {
			found = true
		}
	}
	if !found {
		out = append(out, current)
	}
	sort.Strings(out)
	return out
}

// notes are the four lines of provenance, never empty. A part the page did not
// reach - because usage failed first and the page stopped there - says so
// rather than leaving a blank, which reads as "nothing to say".
func notes(report rightsizing.Report, podsRead bool) []note {
	notRead := func(a rightsizing.Availability, what string) rightsizing.Availability {
		if a.Detail == "" && !podsRead {
			return rightsizing.Availability{Detail: what + " not read: without per-container usage nothing " +
				"on this page is sized"}
		}
		return a
	}
	capacity := notRead(report.Capacity, "node capacity")
	storage := notRead(report.Storage, "disk usage")
	return []note{
		{OK: report.Usage.OK, Label: "Usage", Detail: report.Usage.Detail},
		{OK: capacity.OK, Label: "Node capacity", Detail: capacity.Detail},
		// Its own line and not folded into Usage: CPU and memory come from
		// metrics-server and disk from the kubelet, and the two fail
		// independently.
		{OK: storage.OK, Label: "Disk", Detail: storage.Detail},
		{OK: report.History.OK, Label: "History", Detail: report.History.Detail},
	}
}

// pendingWithheld is said on a CPU or memory row drawn while the week is
// still being read, in place of its command.
const pendingWithheld = "No command yet: this figure rests on one metrics-server sample while the " +
	"last seven days are read, and a limit copied from one sample can be an OOM; the command comes with the week"

// toRow is one suggestion, with its command rendered as text. pending is a
// page drawn before its window arrived: a CPU or memory figure is then from a
// single scrape although a week exists, so it is shown and not offered to
// copy. A disk row reads a level, not the window, and keeps its command.
func toRow(s rightsizing.Suggestion, pending bool) row {
	out := row{
		Workload: s.Workload, WorkloadKind: s.WorkloadKind, Namespace: s.Namespace, Pod: s.Pod,
		PodCount: s.PodCount, Container: s.Container, Resource: s.Resource, Kind: string(s.Kind),
		Action: s.Action, Message: s.Message, Current: s.Current, Suggested: s.Suggested,
		AppliesToBoth: s.AppliesToBoth, Warnings: append([]string{}, s.Warnings...),
		Scope: s.WorkloadKind + " " + s.Workload, suggestedRaw: s.SuggestedRaw, pairedRequestRaw: s.PairedRequestRaw,
		Confidence: string(s.Confidence), InsufficientData: s.InsufficientData,
	}
	if s.Namespace != "" {
		out.Scope += " · " + s.Namespace
	}
	if cmd, ok := rightsizing.ToKubectlCmd(s); ok {
		if pending && s.Confidence != "" {
			out.Warnings = append(out.Warnings, pendingWithheld)
		} else {
			out.Kubectl = cmd
		}
	}
	return out
}

// cards turns the measurements into one namespace's body.
func cards(readings []rightsizing.WorkloadReading, storageOK, pending bool) []workloadCard {
	out := make([]workloadCard, 0, len(readings))
	for _, r := range readings {
		card := workloadCard{
			Workload: r.Name, Kind: r.Kind, Namespace: r.Namespace, Replicas: r.Replicas, QOS: r.QOS,
			Worst: string(r.Worst), StorageOK: storageOK, Volumes: volumes(r.Volumes),
			Managed:    append([]string{}, r.Managed...),
			Containers: []container{}, Findings: []row{},
		}
		for _, c := range r.Containers {
			cont := container{
				Name: c.Name, Restarts: c.Restarts, OOMKilled: c.OOMKilled, CrashLooping: c.CrashLooping,
				OOMKilledEarlier: c.OOMKilledEarlier,
				MemoryRaised:     toRaised(c.MemoryRaise),
				CPU:              toBar("CPU", c.CPU, rightsizing.FormatCPU),
				Memory:           toBar("Memory", c.Memory, rightsizing.FormatMemory),
				Ephemeral:        toBar("Ephemeral", c.Ephemeral, rightsizing.FormatMemory),
				Pods:             []podFigure{},
			}
			for _, p := range c.Pods {
				cont.Pods = append(cont.Pods, podFigure{
					Pod: p.Pod, CPU: rightsizing.FormatCPU(p.CPU), Memory: rightsizing.FormatMemory(p.Memory),
					Measured: p.Measured,
				})
			}
			card.Containers = append(card.Containers, cont)
		}
		for _, s := range r.Suggestions {
			card.Findings = append(card.Findings, toRow(s, pending))
		}
		out = append(out, card)
	}
	return out
}

// toBar computes the geometry of one resource bar.
//
// The track is the limit when there is one. When there is not, it is the larger
// of the request and the peak, and Scale says so: scaling an unlimited
// container to its own usage would draw a full bar and read as "at capacity",
// which is the opposite of what an absent limit means.
func toBar(label string, r rightsizing.ResourceReading, format func(int64) string) bar {
	out := bar{
		Label: label, Measured: r.Measured, Status: string(r.Status),
		Mean: format(r.Mean), Peak: format(r.Peak),
	}
	if !r.Measured {
		out.Mean, out.Peak = "", ""
	}
	if r.Request > 0 {
		out.Request = format(r.Request)
	}
	if r.Limit > 0 {
		out.Limit = format(r.Limit)
	}
	// Headroom against the peak and not the mean: the headroom that matters is
	// the headroom at the worst moment observed.
	if r.Limit > 0 && r.Measured {
		if left := r.Limit - r.Peak; left > 0 {
			out.Headroom = format(left)
		} else {
			out.Headroom = "none"
		}
	}
	// Measured, with no ceiling at all - its own caption, because an empty
	// Limit is also what "not measured" looks like.
	out.Unbounded = r.Measured && r.Limit <= 0
	out.Insufficient = r.Insufficient

	track := r.Limit
	out.Scale = "limit"
	if track <= 0 {
		track = max(r.Request, r.Peak)
		out.Scale = "no limit — scaled to the larger of request and peak"
	}
	if track <= 0 {
		return out
	}
	pct := func(v int64) float64 {
		if v <= 0 {
			return 0
		}
		return round1(math.Min(100, float64(v)/float64(track)*100))
	}
	out.ReqPct = pct(r.Request)
	if r.Measured {
		out.UsePct, out.PeakPct = pct(r.Mean), pct(r.Peak)
	}
	if r.Limit > 0 && r.Measured {
		p := int(math.Round(float64(r.Mean) / float64(r.Limit) * 100))
		out.Pct = &p
	}
	return out
}

// volumes turns one workload's disks into bars, stating the free figure as well
// as the used one: "94 %" and "310 MiB left" answer different questions.
func volumes(in []rightsizing.VolumeReading) []volume {
	out := make([]volume, 0, len(in))
	for _, v := range in {
		vol := volume{
			Pod: v.Pod, Name: v.Name, Claim: v.Claim, Kind: v.Type, Status: string(v.Status),
			Measured: v.Measured,
			// An emptyDir with no sizeLimit can fill the node's disk and evict
			// every pod on it; a PVC always has a ceiling, its own size.
			Unbounded: v.Type == "emptyDir" && !v.HasSizeLimit,
		}
		// What the figure beside the bar *is*. An unbounded emptyDir still has a
		// number - the node's disk the directory sits on - and calling that a
		// "sizeLimit" asserts a limit nobody set.
		switch {
		case v.Type == "pvc":
			vol.CeilingWord = "capacity"
		case !v.HasSizeLimit:
			vol.CeilingWord = "no sizeLimit; the node's own disk holds"
		default:
			vol.CeilingWord = "sizeLimit"
		}
		if v.Capacity > 0 {
			vol.Capacity = rightsizing.FormatMemory(v.Capacity)
		}
		if v.Measured {
			vol.Used = rightsizing.FormatMemory(v.Usage)
			if v.Capacity > 0 {
				used := math.Min(100, float64(v.Usage)/float64(v.Capacity)*100)
				vol.Pct = fmt.Sprintf("%.0f%%", used)
				vol.PctOf = "of this volume's " + vol.CeilingWord
				if vol.Unbounded {
					vol.PctOf = "of the node's own disk — this volume has no ceiling of its own"
				}
				width := round1(used)
				vol.UsePct = &width
				if left := v.Capacity - v.Usage; left > 0 {
					vol.Free = rightsizing.FormatMemory(left)
				} else {
					vol.Free = "none"
				}
			}
		}
		out = append(out, vol)
	}
	return out
}

// namespaceCards turns the namespace recap into the overview grid.
func namespaceCards(summaries []rightsizing.NamespaceSummary) []namespaceCard {
	out := make([]namespaceCard, 0, len(summaries))
	for _, n := range summaries {
		card := namespaceCard{
			Name: n.Name, Pods: n.Pods, Workloads: n.Workloads, Findings: n.Findings,
			Measured: n.Measured, Band: n.Band, Worst: string(n.Worst),
			CPURequest: rightsizing.FormatCPU(n.CPURequest),
			MemRequest: rightsizing.FormatMemory(n.MemRequest),
		}
		if n.Measured {
			card.CPUUsed = rightsizing.FormatCPU(n.CPUUsed)
			card.MemUsed = rightsizing.FormatMemory(n.MemUsed)
		}
		// Three answers where it is tempting to draw one. A ratio needs a
		// denominator: with none, "none set" when the pods declare no request
		// and "—" when nothing measured them. A ratio of one would claim the
		// namespace is sized exactly right.
		card.CPURatio, card.MemRatio = "—", "—"
		if n.Measured {
			if n.CPURequest == 0 {
				card.CPURatio = "none set"
			}
			if n.MemRequest == 0 {
				card.MemRatio = "none set"
			}
		}
		if n.CPURatio > 0 {
			card.CPURatio = fmt.Sprintf("×%.1f", n.CPURatio)
		}
		if n.MemRatio > 0 {
			card.MemRatio = fmt.Sprintf("×%.1f", n.MemRatio)
		}
		if n.Measured && n.ContainersMeasured < n.Containers {
			card.Coverage = fmt.Sprintf("%d of %d containers measured", n.ContainersMeasured, n.Containers)
		}
		if n.Measured {
			card.CPUIdle = idle(n.CPURequestMeasured, n.CPUUsed, rightsizing.FormatCPU)
			card.MemIdle = idle(n.MemRequestMeasured, n.MemUsed, rightsizing.FormatMemory)
		}
		out = append(out, card)
	}
	return out
}

// resourceOrder is the order findings appear in within one severity: a number
// that is wrong before a number that is missing before a disk, the order
// somebody works through a list.
var resourceOrder = []string{
	"CPU", "Memory",
	"CPU — no limit", "Memory — no limit",
	"CPU — no request", "Memory — no request",
	"Ephemeral — no limit", "Ephemeral",
	"PVC", "EmptyDir",
}

func resourceRank(resource string) int {
	for i, name := range resourceOrder {
		if name == resource {
			return i
		}
	}
	// An unrecognized resource sorts last: a finding this build has no opinion
	// about must not lead the list.
	return len(resourceOrder)
}

// categoryOf maps a finding's resource onto one of the three filters. Storage
// is ephemeral storage, claims and emptyDirs together: one question asked of
// three kinds of disk. The page filters with the same mapping (`categoryOf` in
// web/app/extras/kubeadjust/data.ts): a change here is a change there.
func categoryOf(resource string) string {
	switch {
	case strings.HasPrefix(resource, "CPU"):
		return "cpu"
	case strings.HasPrefix(resource, "Memory"):
		return "memory"
	case strings.HasPrefix(resource, "Ephemeral"), resource == "PVC", resource == "EmptyDir":
		return "storage"
	default:
		return ""
	}
}

// groups splits the findings by severity and orders each group. A group with
// nothing in it is left out: an empty "critical" heading above two warnings
// reads as a page still loading.
func groups(rows []row) []group {
	bands := []group{
		{Kind: "danger", Label: "critical", Mark: "▲"},
		{Kind: "warning", Label: "warning", Mark: "●"},
		{Kind: "overkill", Label: "over-provisioned", Mark: "▼"},
	}
	for _, r := range rows {
		for i := range bands {
			if bands[i].Kind == r.Kind {
				bands[i].Rows = append(bands[i].Rows, r)
			}
		}
	}
	out := make([]group, 0, len(bands))
	for _, band := range bands {
		if len(band.Rows) == 0 {
			continue
		}
		sort.SliceStable(band.Rows, func(i, j int) bool {
			return resourceRank(band.Rows[i].Resource) < resourceRank(band.Rows[j].Resource)
		})
		out = append(out, band)
	}
	return out
}

// categories builds the three filters with their counts, over the unfiltered
// set: the page draws them the same whichever is selected. A category with nothing in it is still there, at zero: its absence is
// information. measured false leaves every count null, for the reason count
// gives.
func categories(rows []row, measured bool) []category {
	counts := map[string]int{}
	for _, r := range rows {
		counts[categoryOf(r.Resource)]++
	}
	out := make([]category, 0, 3)
	for _, c := range []struct{ key, label string }{{"cpu", "CPU"}, {"memory", "Memory"}, {"storage", "Storage"}} {
		item := category{Key: c.key, Label: c.label}
		if measured {
			n := counts[c.key]
			item.Count = &n
		}
		out = append(out, item)
	}
	return out
}

// commands is every command the shown findings carry, deduplicated and in the
// order they are drawn: a Guaranteed pod's paired change can arrive as one
// command from two findings, and the block is meant to be read before it runs.
func commands(gs []group) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, g := range gs {
		for _, r := range g.Rows {
			if r.Kubectl == "" || seen[r.Kubectl] {
				continue
			}
			seen[r.Kubectl] = true
			out = append(out, r.Kubectl)
		}
	}
	return out
}

// idle is requested minus used, or "none".
func idle(requested, used int64, format func(int64) string) string {
	if requested-used <= 0 {
		return "none"
	}
	return format(requested - used)
}

// reclaim is what one suggestion gives back, in millicores or bytes over every
// replica: a request reduction only. Zero for anything else.
func reclaim(s rightsizing.Suggestion, readings []rightsizing.WorkloadReading) (int64, bool) {
	// A reduction is a "Reduce request" row, or the request a limit row
	// carries in its own command (rightsizing's foldRequest).
	target := s.SuggestedRaw
	switch {
	case s.PairedRequestRaw > 0:
		target = s.PairedRequestRaw
	case s.Kind != rightsizing.KindOverkill || !strings.HasPrefix(s.Action, "Reduce request"):
		return 0, false
	}
	if target <= 0 {
		// A reduction withheld for insufficient data names no value, and
		// reading its zero as the target would give back the whole request.
		return 0, false
	}
	isCPU := strings.HasPrefix(s.Resource, "CPU")
	for _, r := range readings {
		if r.Namespace != s.Namespace || r.Kind != s.WorkloadKind || r.Name != s.Workload {
			continue
		}
		for _, c := range r.Containers {
			if c.Name != s.Container {
				continue
			}
			current := c.Memory.Request
			if isCPU {
				current = c.CPU.Request
			}
			if current > target {
				return (current - target) * int64(max(s.PodCount, 1)), isCPU
			}
		}
	}
	return 0, isCPU
}

// headlineOf sums the page into its first line.
func headlineOf(report rightsizing.Report, namespace string, partial bool) *headline {
	out := &headline{Scope: "this cluster"}
	switch {
	case namespace != "":
		out.Scope = namespace
	case partial:
		// The sums are over the namespaces the person may list, not over
		// the cluster, and the scope says so.
		out.Scope = "the namespaces you may list"
	}
	var cpu, mem int64
	reclaimed := map[string]bool{}
	for _, s := range report.Suggestions {
		switch s.Kind {
		case rightsizing.KindDanger:
			out.AtRisk++
		case rightsizing.KindWarning:
			out.Worth++
		case rightsizing.KindOverkill:
			out.Over++
		}
		if v, isCPU := reclaim(s, report.Readings); v > 0 {
			if isCPU {
				cpu += v
			} else {
				mem += v
			}
			reclaimed[s.Namespace+"/"+s.WorkloadKind+"/"+s.Workload] = true
		}
	}
	if cpu > 0 {
		out.ReclaimCPU = rightsizing.FormatCPU(cpu)
	}
	if mem > 0 {
		out.ReclaimMemory = rightsizing.FormatMemory(mem)
	}
	out.ReclaimWorkloads = len(reclaimed)

	var cpuReq, cpuUsed, memReq, memUsed int64
	for _, n := range report.Namespaces {
		cpuReq += n.CPURequestMeasured
		cpuUsed += n.CPUUsed
		memReq += n.MemRequestMeasured
		memUsed += n.MemUsed
	}
	share := func(used, requested int64) *int {
		if requested <= 0 {
			return nil
		}
		v := int(math.Round(float64(used) / float64(requested) * 100))
		return &v
	}
	out.UsedOfRequestedCPU, out.UsedOfRequestedMemory = share(cpuUsed, cpuReq), share(memUsed, memReq)
	return out
}

// topRows is how many rows the overview's "act on first" list holds.
const topRows = 8

// top is what to act on first, to topRows: the dangers, then the reductions
// that give back the most, then the warnings. The warnings come last because
// on a real cluster most of them are a spec without a memory limit or a
// request, repeated per container - checked on docker-desktop, twenty of them
// pushed every saving off the list - and the namespace views carry them all.
//
// Every finding has a place in that order, so the list is empty only when there
// is nothing at all: a cluster whose only findings are a limit reduction or a
// disk must not read "nothing to change" under a headline counting them. A
// request reduction carried by a "Set limit" row is ranked with the reductions,
// by what it gives back.
func top(rows []row, readings []rightsizing.WorkloadReading) []row {
	type sized struct {
		r    row
		size float64
	}
	var dangers, rest []row
	var giving []sized
	for _, g := range groups(rows) {
		for _, r := range g.Rows {
			if r.Kind == string(rightsizing.KindDanger) {
				dangers = append(dangers, r)
				continue
			}
			v, isCPU := reclaim(rightsizing.Suggestion{Kind: rightsizing.Kind(r.Kind), Action: r.Action, Resource: r.Resource,
				Namespace: r.Namespace, WorkloadKind: r.WorkloadKind, Workload: r.Workload, Container: r.Container,
				PodCount: r.PodCount, SuggestedRaw: r.suggestedRaw, PairedRequestRaw: r.pairedRequestRaw}, readings)
			if v <= 0 {
				rest = append(rest, r)
				continue
			}
			// Cores and GiB are not one unit: a core is weighed as 4 GiB,
			// the ratio cloud machines are commonly cut in, only to order.
			size := float64(v) / 1024 / 1024 / 1024 / 4
			if isCPU {
				size = float64(v) / 1000
			}
			giving = append(giving, sized{r, size})
		}
	}
	sort.SliceStable(giving, func(i, j int) bool { return giving[i].size > giving[j].size })
	out := append([]row{}, dangers...)
	for _, g := range giving {
		out = append(out, g.r)
	}
	// What is left keeps the groups' order: warnings, then the rest of the
	// over-provisioning (limits, disks).
	out = append(out, rest...)
	if len(out) > topRows {
		out = out[:topRows]
	}
	return out
}

// round1 keeps one decimal, the precision the page draws.
func round1(v float64) float64 { return math.Round(v*10) / 10 }

// plural is "1 pod" or "3 pods".
func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

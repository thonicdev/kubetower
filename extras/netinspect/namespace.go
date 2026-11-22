package netinspect

import "sort"

// One namespace as the rules see it, in the pod tab's shape: what may come
// in, from where and by which label; what may go out, to where; and, per
// workload, whether each direction is isolated. Ingress is decided by the
// policies of the pods receiving, egress by those of the pods sending, so a
// namespace's two sides are read from its own pods' policies only.

// NamespaceView is Overview.View, under `?ns=`.
type NamespaceView struct {
	Ingress NSSide `json:"ingress"`
	Egress  NSSide `json:"egress"`
	// Workloads are the namespace's workloads, sorted by name, capped at
	// maxRows (WorkloadsTotal says how many).
	Workloads      []WorkloadView `json:"workloads"`
	WorkloadsTotal int            `json:"workloadsTotal"`
}

// NSSide is one direction of a namespace.
type NSSide struct {
	// Open is the workloads of the namespace no policy isolates in this
	// direction: anyone may reach them, or they may reach anyone.
	Open     []string `json:"open"`
	OpenPods int      `json:"openPods"`
	// Lanes are the rules of the policies selecting the namespace's pods in
	// this direction, one per rule, with the workloads it applies to.
	Lanes      []Lane `json:"lanes"`
	LanesTotal int    `json:"lanesTotal"`
	// Closed is the workloads isolated in this direction with no rule
	// allowing anything: nothing may pass.
	Closed []string `json:"closed"`
}

// Lane is one rule, as a lane from a peer to the workloads it applies to.
type Lane struct {
	RuleView
	// Targets are the namespace's workloads the rule applies to.
	Targets []string `json:"targets"`
	// Pods is how many of the namespace's pods it applies to.
	Pods int `json:"pods"`
}

// WorkloadView is one workload of the namespace: its pods, its isolation in
// each direction, and the peers its rules allow, as their labels.
type WorkloadView struct {
	Name        string   `json:"name"`
	Pods        int      `json:"pods"`
	IsolatedIn  int      `json:"isolatedIn"`
	IsolatedOut int      `json:"isolatedOut"`
	HostNetwork int      `json:"hostNetwork"`
	From        []string `json:"from"`
	To          []string `json:"to"`
}

// lanesPerSide bounds a side's lanes; peersPerWorkload a workload's peers.
const (
	lanesPerSide     = 200
	peersPerWorkload = labelsPerEdge
)

// namespaceView builds the view of one namespace.
func (m *model) namespaceView(namespace string) NamespaceView {
	v := NamespaceView{Ingress: newSide(), Egress: newSide(), Workloads: []WorkloadView{}}
	workloads := map[string]*WorkloadView{}
	type laneAcc struct {
		lane    Lane
		targets map[string]bool
	}
	in, out := map[[2]int]*laneAcc{}, map[[2]int]*laneAcc{}
	var inOrder, outOrder [][2]int
	openIn, openOut := map[string]bool{}, map[string]bool{}
	ruled := map[string][2]bool{} // workload -> has an allowing rule in, out
	isoIn, isoOut := map[string]bool{}, map[string]bool{}

	collect := func(acc map[[2]int]*laneAcc, order *[][2]int, policies []int, ingress bool, workload string, pods int) bool {
		any := false
		for _, pi := range policies {
			pol := &m.Policies[pi]
			rules := pol.Out
			if ingress {
				rules = pol.In
			}
			for ri := range rules {
				key := [2]int{pi, ri}
				a, ok := acc[key]
				if !ok {
					a = &laneAcc{lane: Lane{RuleView: m.ruleView(pol, &rules[ri]), Targets: []string{}}, targets: map[string]bool{}}
					acc[key] = a
					*order = append(*order, key)
				}
				if !a.targets[workload] {
					a.targets[workload] = true
				}
				a.lane.Pods += pods
				if a.lane.Kind == "allow" {
					any = true
				}
			}
		}
		return any
	}

	for _, id := range m.byNamespace[namespace] {
		ident := &m.Identities[id]
		byWorkload := map[string]int{}
		for _, pi := range ident.Pods {
			byWorkload[m.Pods[pi].Workload]++
		}
		for workload, pods := range byWorkload {
			w, ok := workloads[workload]
			if !ok {
				w = &WorkloadView{Name: workload, From: []string{}, To: []string{}}
				workloads[workload] = w
			}
			w.Pods += pods
			if ident.HostNetwork {
				w.HostNetwork += pods
				continue
			}
			if len(ident.In) > 0 {
				w.IsolatedIn += pods
				isoIn[workload] = true
			} else {
				openIn[workload] = true
				v.Ingress.OpenPods += pods
			}
			if len(ident.Out) > 0 {
				w.IsolatedOut += pods
				isoOut[workload] = true
			} else {
				openOut[workload] = true
				v.Egress.OpenPods += pods
			}
			r := ruled[workload]
			if collect(in, &inOrder, append(append([]int{}, ident.In...), ident.InAlso...), true, workload, pods) {
				r[0] = true
			}
			if collect(out, &outOrder, append(append([]int{}, ident.Out...), ident.OutAlso...), false, workload, pods) {
				r[1] = true
			}
			ruled[workload] = r
		}
	}

	finishSide := func(side *NSSide, acc map[[2]int]*laneAcc, order [][2]int, open, iso map[string]bool, dir int, peersOf func(*WorkloadView) *[]string) {
		side.LanesTotal = len(order)
		for _, key := range order {
			a := acc[key]
			for t := range a.targets {
				a.lane.Targets = append(a.lane.Targets, t)
			}
			sort.Strings(a.lane.Targets)
			if a.lane.Kind == "allow" {
				for _, t := range a.lane.Targets {
					peers := peersOf(workloads[t])
					for _, p := range a.lane.Peers {
						if len(*peers) < peersPerWorkload && !contains(*peers, p.Label) {
							*peers = append(*peers, p.Label)
						}
					}
				}
			}
			if len(side.Lanes) < lanesPerSide {
				side.Lanes = append(side.Lanes, a.lane)
			}
		}
		for w := range open {
			side.Open = append(side.Open, w)
		}
		sort.Strings(side.Open)
		for w := range iso {
			if !ruled[w][dir] {
				side.Closed = append(side.Closed, w)
			}
		}
		sort.Strings(side.Closed)
	}
	finishSide(&v.Ingress, in, inOrder, openIn, isoIn, 0, func(w *WorkloadView) *[]string { return &w.From })
	finishSide(&v.Egress, out, outOrder, openOut, isoOut, 1, func(w *WorkloadView) *[]string { return &w.To })

	names := make([]string, 0, len(workloads))
	for name := range workloads {
		names = append(names, name)
	}
	sort.Strings(names)
	v.WorkloadsTotal = len(names)
	for _, name := range names {
		if len(v.Workloads) == maxRows {
			break
		}
		v.Workloads = append(v.Workloads, *workloads[name])
	}
	return v
}

func newSide() NSSide {
	return NSSide{Open: []string{}, Lanes: []Lane{}, Closed: []string{}}
}

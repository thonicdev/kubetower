package rightsizing

import "sort"

// A reading is what a container asks for and what it actually uses, whether or
// not anything is wrong with it, so an operator can check a suggestion rather
// than obey it. A Suggestion is a claim about what should change; a reading is
// a measurement.

// WorkloadReading is one workload's numbers, with whatever the engine had to
// say about it.
type WorkloadReading struct {
	Kind      string
	Name      string
	Namespace string
	// Replicas is how many pods were pooled into the containers below.
	Replicas int
	// QOS is the class the API server assigned, empty when it was not read.
	QOS string
	// Managed says what else sizes this workload - an HPA on a share of its
	// request, a VPA - and what that withholds on this page. Empty when
	// nothing does. See autoscale.go.
	Managed    []string
	Containers []ContainerReading
	// Suggestions concerning this workload, danger first — the same values
	// Compute returned, indexed here so a card can draw its own findings.
	Suggestions []Suggestion
	// Worst is the loudest suggestion kind on this workload, for sorting and for
	// the card's own badge. Empty when there is nothing wrong.
	Worst Kind
	// Volumes are the workload's disks, per replica and not pooled: each
	// StatefulSet replica has its own disk and each emptyDir its own node, and
	// pooling would average away the one about to fill.
	Volumes []VolumeReading
}

// VolumeReading is one disk of one replica.
type VolumeReading struct {
	// Pod is which replica owns it, because that is the answer to "which one".
	Pod string
	// Name is the name in the pod template; Claim is the PVC it binds to, empty
	// for an emptyDir.
	Name  string
	Claim string
	// Type is "pvc" or "emptyDir". Nothing else reaches here — see volumesOf.
	Type string
	// Capacity is the claim's declared size, or the emptyDir's sizeLimit. Zero
	// is no ceiling, a finding for an emptyDir: it can fill the node's disk.
	Capacity int64
	Usage    int64
	// Measured separates a disk the kubelet did not report on from an empty one.
	// For a PVC it is also false when the kubelet's figures were measuring a
	// shared export rather than this claim — see trustworthy.
	Measured     bool
	HasSizeLimit bool
	Status       Status
}

// ContainerReading is one container across every replica.
type ContainerReading struct {
	Name string
	// Restarts and OOMKilled are the worst seen on any replica — see aggregate.
	Restarts int
	// CrashLooping is whether the restarts are happening now; the count alone
	// is a history.
	CrashLooping bool
	OOMKilled    bool
	// OOMKilledEarlier is a kill for memory more than a day ago: no alarm,
	// still no memory reduction.
	OOMKilledEarlier bool
	// MemoryRaise is the raise of the memory limit that holds back every
	// reduction of it (rollout.go), nil when none does. The card names it, so
	// a container with no memory finding says why it has none.
	MemoryRaise *MemoryRaise
	CPU         ResourceReading
	Memory      ResourceReading
	// Ephemeral is the container's writable layer plus its logs, against the
	// `ephemeral-storage` request and limit in its template. Measured is false
	// wherever `nodes/proxy` is refused, as on most managed clusters.
	Ephemeral ResourceReading
	// Pods is the per-replica breakdown, in the order the pods were listed, so
	// one replica doing more than the others shows.
	Pods []PodReading
}

// ResourceReading is one resource of one container.
//
// Every figure is in the unit Kubernetes writes it in — millicores or bytes —
// and zero means the spec does not set it, exactly as elsewhere in this package.
type ResourceReading struct {
	Request int64
	Limit   int64
	// Mean is the average across replicas: the headline figure, because the
	// request it is compared against is per pod.
	Mean int64
	// Peak is the worst replica, or the worst sample when there is history.
	Peak int64
	// P95 is what the findings judge a request and a limit against; the peak
	// itself on a single scrape, where there is no distribution to take one
	// of. Windowed says which of the two it is.
	P95      int64
	Windowed bool
	// Insufficient is too little usable usage to size this resource from
	// (confidence.go): the figures are drawn, and no value is recommended.
	Insufficient bool
	// Measured separates "nothing reported this" from "it reported zero".
	Measured bool
	// Status is the same judgment ResourceStatus makes, computed here so the
	// bar and the suggestion cannot disagree about what color this is.
	Status Status
}

// PodReading is one replica's instantaneous usage.
type PodReading struct {
	Pod    string
	CPU    int64
	Memory int64
	// Measured is false for a replica the metrics backend did not mention —
	// drawn as an em dash and never as a zero.
	Measured bool
}

// Readings measures every workload, independently of whether anything is
// wrong. It shares usageStats with the engine, so a bar and a suggestion argue
// from the same figure.
func Readings(workloads []Workload, history []ContainerHistory) []WorkloadReading {
	return readings(workloads, history, nil)
}

// readings is Readings, with the figures already taken in this read (statsMemo).
func readings(workloads []Workload, history []ContainerHistory, memo statsMemo) []WorkloadReading {
	all := BuildHistoryMap(history)
	page := pageHistory(workloads, history)

	out := make([]WorkloadReading, 0, len(workloads))
	for _, w := range workloads {
		if len(w.Pods) == 0 {
			continue
		}
		// The series this workload is sized from, its former pods' included.
		hist := workloadHistory(page, all, w)
		reading := WorkloadReading{
			Kind: w.Kind, Name: w.Name, Namespace: w.Namespace,
			Replicas: len(w.Pods),
			QOS:      w.Pods[0].QOS,
			Managed:  AutoscalerNotes(w.Autoscalers),
		}
		for _, agg := range aggregateContainers(w.Pods) {
			reading.Containers = append(reading.Containers, containerReading(agg, w.Pods, hist, w.Rollout, memo))
		}
		reading.Volumes = volumeReadings(w.Pods)
		out = append(out, reading)
	}
	return out
}

func containerReading(agg aggregate, pods []Pod, hist HistoryMap, rollout *Rollout, memo statsMemo) ContainerReading {
	out := ContainerReading{
		Name:         agg.name,
		Restarts:     agg.maxRestarts,
		CrashLooping: agg.crashLooping && agg.maxRestarts >= crashLoopRestarts,
		OOMKilled:    agg.oomKilled,
		// An older kill only when there is no recent one: the badge says the
		// louder of the two.
		OOMKilledEarlier: agg.oomEarlier && !agg.oomKilled,
		CPU:              resourceReading(agg, pods, hist, true, memo),
		Memory:           resourceReading(agg, pods, hist, false, memo),
		Ephemeral:        ephemeralReading(agg, pods),
	}
	if raise, ok := rollout.raise(agg.name, agg.spec.MemLimit); ok {
		out.MemoryRaise = &raise
	}

	for _, pod := range pods {
		for i := range pod.Containers {
			c := &pod.Containers[i]
			if c.Name != agg.name {
				continue
			}
			out.Pods = append(out.Pods, PodReading{
				Pod: pod.Name, CPU: c.CPUUsage, Memory: c.MemUsage, Measured: c.HasUsage,
			})
		}
	}
	return out
}

// ephemeralReading is one container's disk across every replica: the worst
// replica, not the mean. Ephemeral storage is a level that only grows between
// restarts, and the fullest pod is the one about to be evicted; analyzeEphemeral
// takes the max too.
func ephemeralReading(agg aggregate, pods []Pod) ResourceReading {
	var out ResourceReading
	for _, pod := range pods {
		for i := range pod.Containers {
			c := &pod.Containers[i]
			if c.Name != agg.name {
				continue
			}
			if c.EphemeralRequest > out.Request {
				out.Request = c.EphemeralRequest
			}
			if c.EphemeralLimit > out.Limit {
				out.Limit = c.EphemeralLimit
			}
			if c.EphemeralUsage > out.Peak {
				out.Peak = c.EphemeralUsage
			}
		}
	}
	out.Mean = out.Peak
	// A zero here is unmeasured, not an empty disk: the kubelet never reports
	// zero for a running container it saw.
	out.Measured = out.Peak > 0
	out.Status = StorageStatus(out.Peak, out.Limit, out.Measured, out.Limit > 0)
	return out
}

// volumeReadings flattens every replica's disks, in a stable order.
func volumeReadings(pods []Pod) []VolumeReading {
	var out []VolumeReading
	for _, pod := range pods {
		for _, v := range pod.Volumes {
			r := VolumeReading{
				Pod: pod.Name, Name: v.Name, Claim: v.PVCName, Type: v.Type,
				Capacity: v.Capacity, Usage: v.Usage,
				Measured: v.Usage > 0, HasSizeLimit: v.HasSizeLimit,
			}
			// An emptyDir's ceiling is its sizeLimit and a PVC's its declared
			// capacity; only the first can be absent, which StorageStatus warns
			// about.
			hasLimit := v.Capacity > 0
			if v.Type == "emptyDir" {
				hasLimit = v.HasSizeLimit
			}
			r.Status = StorageStatus(v.Usage, v.Capacity, r.Measured, hasLimit)
			out = append(out, r)
		}
	}
	return out
}

func resourceReading(agg aggregate, pods []Pod, hist HistoryMap, isCPU bool, memo statsMemo) ResourceReading {
	out := ResourceReading{Request: agg.spec.MemRequest, Limit: agg.spec.MemLimit}
	if isCPU {
		out.Request, out.Limit = agg.spec.CPURequest, agg.spec.CPULimit
	}

	st, ok := memo.usage(pods, agg.name, isCPU, hist)
	if !ok {
		out.Status = StatusNone
		return out
	}
	out.Measured = true
	out.Mean = int64(st.mean)
	out.Peak = int64(st.max)
	out.P95 = int64(st.p95)
	out.Windowed = st.hasHistory
	out.Insufficient = st.judged.insufficient
	// The same figures and the same functions as the findings - see
	// ResourceStatus.
	out.Status = ResourceStatus(out, isCPU)
	return out
}

// Attach files each suggestion under the workload and container it is about,
// and ranks the workloads worst-first. Suggestions keep Compute's danger-first
// order within a workload.
func Attach(readings []WorkloadReading, suggestions []Suggestion) []WorkloadReading {
	rank := map[Kind]int{KindDanger: 0, KindWarning: 1, KindOverkill: 2}

	index := map[string]int{}
	for i, r := range readings {
		index[r.Namespace+"/"+r.Kind+"/"+r.Name] = i
	}
	for _, s := range suggestions {
		at, ok := index[s.Namespace+"/"+s.WorkloadKind+"/"+s.Workload]
		if !ok {
			// A suggestion no reading covers stays in Report.Suggestions, which
			// the page also renders as a flat list.
			continue
		}
		readings[at].Suggestions = append(readings[at].Suggestions, s)
		if readings[at].Worst == "" || rank[s.Kind] < rank[readings[at].Worst] {
			readings[at].Worst = s.Kind
		}
	}

	// Worst first, then the ones with nothing wrong, each by namespace and
	// name so the order is stable between reads.
	sort.SliceStable(readings, func(i, j int) bool {
		a, b := readings[i], readings[j]
		ra, rb := rank[a.Worst], rank[b.Worst]
		if a.Worst == "" {
			ra = 3
		}
		if b.Worst == "" {
			rb = 3
		}
		if ra != rb {
			return ra < rb
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return readings
}

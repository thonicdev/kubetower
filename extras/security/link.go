package security

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Counts is findings by severity.
type Counts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Unknown  int `json:"unknown"`
}

func (c *Counts) add(o Counts) {
	c.Critical += o.Critical
	c.High += o.High
	c.Medium += o.Medium
	c.Low += o.Low
	c.Unknown += o.Unknown
}

func (c Counts) total() int { return c.Critical + c.High + c.Medium + c.Low + c.Unknown }

// serious is whether any finding is high or critical.
func (c Counts) serious() bool { return c.Critical+c.High > 0 }

// worse orders two counts the worst first: criticals, then highs, mediums,
// lows, unknowns. ok is false when they are equal.
func worse(a, b Counts) (bool, bool) {
	for _, p := range [][2]int{{a.Critical, b.Critical}, {a.High, b.High}, {a.Medium, b.Medium}, {a.Low, b.Low}, {a.Unknown, b.Unknown}} {
		if p[0] != p[1] {
			return p[0] > p[1], true
		}
	}
	return false, false
}

// WorkloadRef names a workload by its Kubernetes kind: Deployment,
// StatefulSet, DaemonSet, ReplicaSet, Job, CronJob or Pod.
type WorkloadRef struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
}

func (w WorkloadRef) key() string { return w.Namespace + "/" + w.Kind + "/" + w.Name }

// workloadKinds is every kind a workload is named by, keyed by the plural the
// console's lists use.
var workloadKinds = map[string]string{
	"pods":         "Pod",
	"deployments":  "Deployment",
	"statefulsets": "StatefulSet",
	"daemonsets":   "DaemonSet",
	"replicasets":  "ReplicaSet",
	"jobs":         "Job",
	"cronjobs":     "CronJob",
}

func knownKind(kind string) bool {
	for _, k := range workloadKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// index is what the linkage reads from pods, ReplicaSets and Jobs: which
// Deployment a ReplicaSet belongs to and at what revision, which CronJob a Job
// belongs to, and each pod's workload.
type index struct {
	sets map[string]object // namespace/name
	jobs map[string]object
	pods map[string]WorkloadRef
}

func newIndex(s *snapshot) *index {
	ix := &index{sets: make(map[string]object, len(s.sets)), jobs: make(map[string]object, len(s.jobs)),
		pods: make(map[string]WorkloadRef, len(s.pods))}
	for _, o := range s.sets {
		ix.sets[o.namespace+"/"+o.name] = o
	}
	for _, o := range s.jobs {
		ix.jobs[o.namespace+"/"+o.name] = o
	}
	for _, p := range s.pods {
		ix.pods[p.namespace+"/"+p.name] = ix.workloadOfPod(p)
	}
	return ix
}

// parentOf is the workload a ReplicaSet or a Job is part of: its Deployment
// or its CronJob, read from its own owner reference. ok false when it has
// none, or was not read.
func (ix *index) parentOf(namespace, kind, name string) (WorkloadRef, bool) {
	var o object
	var found bool
	var want string
	switch kind {
	case "ReplicaSet":
		o, found = ix.sets[namespace+"/"+name]
		want = "Deployment"
	case "Job":
		o, found = ix.jobs[namespace+"/"+name]
		want = "CronJob"
	}
	if !found {
		return WorkloadRef{}, false
	}
	if ref, ok := owner(o.owners); ok && ref.kind == want {
		return WorkloadRef{Namespace: namespace, Kind: want, Name: ref.name}, true
	}
	return WorkloadRef{}, false
}

// workloadOfPod climbs a pod's owners: Pod → ReplicaSet → Deployment,
// Pod → Job → CronJob. A pod nothing known controls is its own workload.
func (ix *index) workloadOfPod(p object) WorkloadRef {
	self := WorkloadRef{Namespace: p.namespace, Kind: "Pod", Name: p.name}
	ref, ok := owner(p.owners)
	if !ok {
		return self
	}
	switch ref.kind {
	case "ReplicaSet":
		if parent, ok := ix.parentOf(p.namespace, ref.kind, ref.name); ok {
			return parent
		}
		// The ReplicaSets were not read: the Deployment controller names a
		// set after its Deployment and the pod's template hash.
		if hash := p.labels["pod-template-hash"]; hash != "" && strings.HasSuffix(ref.name, "-"+hash) &&
			len(ref.name) > len(hash)+1 && ix.sets[p.namespace+"/"+ref.name].name == "" {
			return WorkloadRef{Namespace: p.namespace, Kind: "Deployment", Name: strings.TrimSuffix(ref.name, "-"+hash)}
		}
		return WorkloadRef{Namespace: p.namespace, Kind: "ReplicaSet", Name: ref.name}
	case "Job":
		if parent, ok := ix.parentOf(p.namespace, ref.kind, ref.name); ok {
			return parent
		}
		return WorkloadRef{Namespace: p.namespace, Kind: "Job", Name: ref.name}
	case "StatefulSet", "DaemonSet":
		return WorkloadRef{Namespace: p.namespace, Kind: ref.kind, Name: ref.name}
	}
	return self
}

// scannedOf is the object a report is about: its owner reference first, the
// trivy-operator labels when it has none. ok false when neither names it - a
// name too long for a label is only a hash there.
func scannedOf(rep *reportRow) (WorkloadRef, bool) {
	if ref, ok := owner(rep.owners); ok {
		return WorkloadRef{Namespace: rep.namespace, Kind: ref.kind, Name: ref.name}, true
	}
	kind, name := rep.labels[labelKind], rep.labels[labelName]
	if kind == "" || name == "" {
		return WorkloadRef{}, false
	}
	namespace := rep.labels[labelNamespace]
	if namespace == "" {
		namespace = rep.namespace
	}
	return WorkloadRef{Namespace: namespace, Kind: kind, Name: name}, true
}

// direct ranks a report on the workload itself above any on one of its
// ReplicaSets or Jobs.
const direct = math.MaxInt64

// originOf is the workload a report counts for, the object it was taken on,
// and its rank among that workload's: a Deployment keeps its ReplicaSet of
// the highest revision, a CronJob a report on itself, else its newest Job.
func (ix *index) originOf(rep *reportRow) (workload, scanned WorkloadRef, rank int64, ok bool) {
	scanned, ok = scannedOf(rep)
	if !ok {
		return
	}
	workload, rank = scanned, direct
	switch scanned.Kind {
	case "ReplicaSet":
		if parent, found := ix.parentOf(scanned.Namespace, scanned.Kind, scanned.Name); found {
			workload, rank = parent, ix.sets[scanned.Namespace+"/"+scanned.Name].revision
		}
	case "Job":
		if parent, found := ix.parentOf(scanned.Namespace, scanned.Kind, scanned.Name); found {
			workload, rank = parent, ix.jobs[scanned.Namespace+"/"+scanned.Name].created.Unix()
		}
	case "Pod":
		if w, found := ix.pods[scanned.Namespace+"/"+scanned.Name]; found {
			workload = w
		}
	}
	return workload, scanned, rank, true
}

// chosen is one workload's reports of one kind: those of the object of the
// highest rank.
type chosen struct {
	rank    int64
	scanned WorkloadRef
	reports []*reportRow
}

func (c *chosen) offer(rank int64, scanned WorkloadRef, rep *reportRow) {
	switch {
	case c.reports == nil || rank > c.rank:
		c.rank, c.scanned, c.reports = rank, scanned, []*reportRow{rep}
	case rank == c.rank:
		c.reports = append(c.reports, rep)
	}
}

// result is what is known of one workload, or of one scanned object.
type result struct {
	ref                       WorkloadRef
	vuln, config, secret      chosen
	images                    []string
	counts                    Counts
	scanned                   time.Time
	hasVuln, hasConf, hasSecr bool
	configCounts              Counts
	secrets                   int
}

// linked is every workload's result, and every scanned object's own.
type linked struct {
	ix        *index
	workloads map[string]*result
	objects   map[string]*result // by the object the reports were taken on
}

// link folds the reports onto their workloads.
func link(s *snapshot) *linked {
	ix := newIndex(s)
	l := &linked{ix: ix, workloads: map[string]*result{}, objects: map[string]*result{}}
	get := func(m map[string]*result, ref WorkloadRef) *result {
		r := m[ref.key()]
		if r == nil {
			r = &result{ref: ref}
			m[ref.key()] = r
		}
		return r
	}
	for _, kind := range reportKinds {
		rows := s.reports[kind]
		for i := range rows {
			rep := &rows[i]
			workload, scanned, rank, ok := ix.originOf(rep)
			if !ok {
				continue
			}
			w, o := get(l.workloads, workload), get(l.objects, scanned)
			switch kind {
			case KindVulnerability:
				w.vuln.offer(rank, scanned, rep)
				o.vuln.offer(direct, scanned, rep)
			case KindConfigAudit:
				w.config.offer(rank, scanned, rep)
				o.config.offer(direct, scanned, rep)
			case KindExposedSecret:
				w.secret.offer(rank, scanned, rep)
				o.secret.offer(direct, scanned, rep)
			}
		}
	}
	for _, m := range []map[string]*result{l.workloads, l.objects} {
		for _, r := range m {
			r.sum()
		}
	}
	return l
}

// sum totals a result's chosen reports: one image counted once in a
// workload, whatever number of containers run it.
func (r *result) sum() {
	r.hasVuln, r.hasConf, r.hasSecr = len(r.vuln.reports) > 0, len(r.config.reports) > 0, len(r.secret.reports) > 0
	byImage := map[string]Counts{}
	for _, rep := range r.vuln.reports {
		key := rep.image
		if key == "" {
			key = "\x00" + rep.name
		} else if _, seen := byImage[key]; !seen {
			r.images = append(r.images, rep.image)
		}
		byImage[key] = rep.counts
		if rep.created.After(r.scanned) {
			r.scanned = rep.created
		}
	}
	for _, c := range byImage {
		r.counts.add(c)
	}
	sort.Strings(r.images)
	for _, rep := range r.config.reports {
		r.configCounts.add(rep.counts)
	}
	for _, rep := range r.secret.reports {
		r.secrets += rep.counts.total()
	}
}

// expose marks every workload whose pod a Service of type LoadBalancer or
// NodePort selects, a Service an Ingress or a Gateway API route names as a
// backend, or that runs on its node's network; the exposure names the
// Service, the Ingress, the route and its Gateways, or the host network.
func (l *linked) expose(s *snapshot, want map[string]bool) map[string][]string {
	named := map[string][]string{} // namespace/service → the Ingresses naming it
	for _, in := range s.ingress {
		for _, b := range in.backends {
			key := in.namespace + "/" + b
			named[key] = append(named[key], "Ingress "+in.namespace+"/"+in.name)
		}
	}
	for _, rt := range s.routes {
		via := rt.kind + " " + rt.namespace + "/" + rt.name
		if len(rt.gateways) > 0 {
			via += " (Gateway " + strings.Join(rt.gateways, ", ") + ")"
		}
		for _, b := range rt.backends {
			named[b] = append(named[b], via)
		}
	}
	type exposer struct {
		selector map[string]string
		via      []string
	}
	byNamespace := map[string][]exposer{}
	for _, svc := range s.services {
		if len(svc.selector) == 0 {
			continue
		}
		var via []string
		if svc.kind == "LoadBalancer" || svc.kind == "NodePort" {
			via = append(via, "Service "+svc.namespace+"/"+svc.name+" ("+svc.kind+")")
		}
		via = append(via, named[svc.namespace+"/"+svc.name]...)
		if len(via) > 0 {
			byNamespace[svc.namespace] = append(byNamespace[svc.namespace], exposer{svc.selector, via})
		}
	}
	out := map[string][]string{}
	seen := map[string]map[string]bool{}
	for _, p := range s.pods {
		candidates := byNamespace[p.namespace]
		onHost := s.hostNetwork[p.namespace+"/"+p.name]
		if len(candidates) == 0 && !onHost {
			continue
		}
		w := l.ix.pods[p.namespace+"/"+p.name]
		if want != nil && !want[w.key()] {
			continue
		}
		add := func(v string) {
			if seen[w.key()] == nil {
				seen[w.key()] = map[string]bool{}
			}
			if !seen[w.key()][v] {
				seen[w.key()][v] = true
				out[w.key()] = append(out[w.key()], v)
			}
		}
		if onHost {
			add("Host network")
		}
		for _, e := range candidates {
			if !selects(e.selector, p.labels) {
				continue
			}
			for _, v := range e.via {
				add(v)
			}
		}
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out
}

func selects(selector, labels map[string]string) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

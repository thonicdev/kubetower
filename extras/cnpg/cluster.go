package cnpg

import (
	"errors"
	"net/http"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

// GET /clusters/{namespace}/{name}: one Cluster, read whole, with its pods,
// their disks, its Events and every object that names it.

// PodFacts is what an instance's pod says about itself.
type PodFacts struct {
	Phase     string     `json:"phase"`
	Ready     bool       `json:"ready"`
	Restarts  int64      `json:"restarts"`
	Node      string     `json:"node,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	// RoleLabel is the pod's cnpg.io/instanceRole: it can lag behind the
	// status during a switchover, and the page says so when it differs.
	RoleLabel string `json:"roleLabel,omitempty"`
}

// Disk is one volume of an instance, as its kubelet measured it.
type Disk struct {
	Claim         string `json:"claim"`
	UsedBytes     int64  `json:"usedBytes"`
	CapacityBytes int64  `json:"capacityBytes"`
	// DeclaredBytes is the size the Cluster asks for (spec.storage.size).
	DeclaredBytes int64 `json:"declaredBytes,omitempty"`
	// Trustworthy is false when the capacity is not the claim's - a network
	// share's whole size (extension.Trustworthy).
	Trustworthy bool `json:"trustworthy"`
}

// Member is one instance with its pod and its volumes.
type Member struct {
	Instance
	Pod  *PodFacts `json:"pod,omitempty"`
	Data *Disk     `json:"data,omitempty"`
	WAL  *Disk     `json:"wal,omitempty"`
}

// Event is one Kubernetes Event about the Cluster.
type Event struct {
	Type    string     `json:"type,omitempty"`
	Reason  string     `json:"reason,omitempty"`
	Message string     `json:"message,omitempty"`
	Count   int64      `json:"count,omitempty"`
	At      *time.Time `json:"at,omitempty"`
}

// Scheduled is one ScheduledBackup.
type Scheduled struct {
	Name      string     `json:"name"`
	Schedule  string     `json:"schedule"`
	Suspended bool       `json:"suspended"`
	Method    string     `json:"method,omitempty"`
	Last      *time.Time `json:"last,omitempty"`
	Next      *time.Time `json:"next,omitempty"`
}

// Pooler is one PgBouncer Pooler in front of the Cluster.
type Pooler struct {
	Name      string `json:"name"`
	Type      string `json:"type,omitempty"`
	Instances int64  `json:"instances"`
	Phase     string `json:"phase,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Declared is one Database, Publication or Subscription naming the Cluster.
type Declared struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	PGName  string `json:"pgName,omitempty"`
	Applied *bool  `json:"applied,omitempty"`
	Message string `json:"message,omitempty"`
}

// Detail is GET /clusters/{namespace}/{name}.
type Detail struct {
	Now          time.Time    `json:"now"`
	Summary      Summary      `json:"summary"`
	Members      []Member     `json:"members"`
	Conditions   []Condition  `json:"conditions"`
	Events       []Event      `json:"events"`
	Backups      []BackupMark `json:"backups"`
	Scheduled    []Scheduled  `json:"scheduled"`
	Poolers      []Pooler     `json:"poolers"`
	Declared     []Declared   `json:"declared"`
	WriteService string       `json:"writeService,omitempty"`
	ReadService  string       `json:"readService,omitempty"`
	// DiskNote is why the volumes are not measured, when they are not.
	DiskNote string   `json:"diskNote,omitempty"`
	Sources  []Source `json:"sources"`
	// Commands are lines to copy; the console runs none of them.
	Commands []Command `json:"commands"`
}

func (x CNPG) cluster(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, namespace, name := r.PathValue("cluster"), r.PathValue("namespace"), r.PathValue("name")
		k, crd, ok := resolveClusters(w, r, h, cluster)
		if !ok {
			return
		}
		c, ok := readCluster(w, r, h, cluster, crd, namespace, name)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, x.detail(r, h, cluster, k, c))
	})
}

// readCluster reads one Cluster by name, and answers the request itself when
// it cannot: refused, failed, or not there for this person.
func readCluster(w http.ResponseWriter, r *http.Request, h extension.Host, cluster string, crd extension.CRD, namespace, name string) (*unstructured.Unstructured, bool) {
	page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR(), Namespace: namespace, FieldSelector: "metadata.name=" + name, Limit: 1})
	if err != nil {
		if writeRefused(w, err) {
			return nil, false
		}
		writeProblem(w, http.StatusBadGateway, CodeListFailed, "CloudNativePG's Clusters could not be listed.", err)
		return nil, false
	}
	for i := range page.Items {
		if page.Items[i].GetName() == name && page.Items[i].GetNamespace() == namespace {
			return &page.Items[i], true
		}
	}
	writeProblem(w, http.StatusNotFound, CodeNotFound, "There is no CloudNativePG cluster "+namespace+"/"+name+" you may read.", nil)
	return nil, false
}

func (x CNPG) detail(r *http.Request, h extension.Host, cluster string, k kinds, c *unstructured.Unstructured) Detail {
	namespace, name := c.GetNamespace(), c.GetName()
	now := x.clock().UTC()
	all, backupSource := readBackups(r, h, cluster, namespace, k, name)
	backups := all[clusterKey(namespace, name)]
	scheduled, poolers, declared, sources := x.objects(r, h, cluster, k, namespace, name)
	active := 0
	for _, s := range scheduled {
		if !s.Suspended {
			active++
		}
	}
	sum := summarise(c, backups, active, len(poolers), now)
	d := Detail{
		Now: now, Summary: sum, Conditions: conditions(c),
		Sources:      append([]Source{backupSource}, sources...),
		WriteService: str(c.Object, "status", "writeService"), ReadService: str(c.Object, "status", "readService"),
		Events: []Event{}, Scheduled: scheduled, Poolers: poolers, Declared: declared,
	}
	if len(backups) > ClusterBackups {
		backups = backups[:ClusterBackups]
	}
	d.Backups = append([]BackupMark{}, backups...)

	pods, podSource := instancePods(r, h, cluster, namespace, name)
	d.Sources = append(d.Sources, podSource)
	d.Members = make([]Member, 0, len(sum.Instances))
	for _, in := range sum.Instances {
		m := Member{Instance: in}
		if p, ok := pods[in.Name]; ok {
			facts := p.facts
			m.Pod = &facts
		}
		d.Members = append(d.Members, m)
	}
	d.Commands = commandsFor(namespace, name, d.Members)
	d.DiskNote = disks(r, h, cluster, namespace, d.Members, pods, sum)

	events, source := eventsOf(r, h, cluster, namespace, name)
	d.Events = events
	d.Sources = append(d.Sources, source)
	return d
}

// objects reads the ScheduledBackups, Poolers and declarative objects that
// name the Cluster, in its namespace.
func (x CNPG) objects(r *http.Request, h extension.Host, cluster string, k kinds, namespace, name string) ([]Scheduled, []Pooler, []Declared, []Source) {
	scheduled, poolers, declared := []Scheduled{}, []Pooler{}, []Declared{}
	var sources []Source
	each := func(kind string, limit int64, keep func(item *unstructured.Unstructured)) {
		sources = append(sources, readOwned(r, h, cluster, namespace, k, name, kind, limit, func(_ string, item *unstructured.Unstructured) { keep(item) }))
	}
	each(ScheduledKind, MaxSmall, func(o *unstructured.Unstructured) {
		suspended, _ := nested(o.Object, "spec", "suspend").(bool)
		scheduled = append(scheduled, Scheduled{
			Name: o.GetName(), Schedule: str(o.Object, "spec", "schedule"), Suspended: suspended,
			Method: str(o.Object, "spec", "method"),
			Last:   timeAt(o.Object, "status", "lastScheduleTime"), Next: timeAt(o.Object, "status", "nextScheduleTime"),
		})
	})
	each(PoolerKind, MaxSmall, func(o *unstructured.Unstructured) {
		poolers = append(poolers, Pooler{
			Name: o.GetName(), Type: str(o.Object, "spec", "type"), Instances: num(o.Object, "spec", "instances"),
			Phase: str(o.Object, "status", "phase"), Reason: clip(str(o.Object, "status", "phaseReason"), 300),
		})
	})
	for _, kind := range []string{DatabaseKind, PublicationKind, SubscriptionKind} {
		label := kindLabel(kind)
		each(kind, MaxSmall, func(o *unstructured.Unstructured) {
			dd := Declared{Kind: label, Name: o.GetName(), PGName: str(o.Object, "spec", "name"), Message: clip(str(o.Object, "status", "message"), 300)}
			if applied, ok := nested(o.Object, "status", "applied").(bool); ok {
				dd.Applied = &applied
			}
			declared = append(declared, dd)
		})
	}
	sort.Slice(scheduled, func(a, b int) bool { return scheduled[a].Name < scheduled[b].Name })
	sort.Slice(poolers, func(a, b int) bool { return poolers[a].Name < poolers[b].Name })
	sort.SliceStable(declared, func(a, b int) bool {
		if declared[a].Kind != declared[b].Kind {
			return declared[a].Kind < declared[b].Kind
		}
		return declared[a].Name < declared[b].Name
	})
	return scheduled, poolers, declared, sources
}

func kindLabel(kind string) string {
	switch kind {
	case DatabaseKind:
		return "Database"
	case PublicationKind:
		return "Publication"
	case SubscriptionKind:
		return "Subscription"
	}
	return kind
}

type instancePod struct {
	namespace, name string
	facts           PodFacts
}

// instancePods reads the Cluster's instance pods, keyed by instance name.
func instancePods(r *http.Request, h extension.Host, cluster, namespace, name string) (map[string]instancePod, Source) {
	out := map[string]instancePod{}
	page, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR, Namespace: namespace, LabelSelector: "cnpg.io/cluster=" + name, Limit: MaxInstancePods})
	source := sourceOf("pods", page, err)
	for i := range page.Items {
		p := &page.Items[i]
		labels := p.GetLabels()
		if labels["cnpg.io/podRole"] != "instance" && labels["cnpg.io/instanceName"] == "" {
			continue
		}
		instance := labels["cnpg.io/instanceName"]
		if instance == "" {
			instance = p.GetName()
		}
		facts := PodFacts{
			Phase: str(p.Object, "status", "phase"), Node: str(p.Object, "spec", "nodeName"),
			StartedAt: timeAt(p.Object, "status", "startTime"), RoleLabel: labels["cnpg.io/instanceRole"],
		}
		for _, raw := range list(nested(p.Object, "status", "conditions")) {
			if m, ok := raw.(map[string]any); ok && m["type"] == "Ready" {
				facts.Ready = m["status"] == "True"
			}
		}
		for _, raw := range list(nested(p.Object, "status", "containerStatuses")) {
			if m, ok := raw.(map[string]any); ok {
				facts.Restarts += num(m, "restartCount")
			}
		}
		out[instance] = instancePod{namespace: p.GetNamespace(), name: p.GetName(), facts: facts}
	}
	return out, source
}

// disks fills each member's volumes from the kubelets of the nodes that run
// this Cluster's pods, and answers why they are not measured when they are
// not. Never drawn as empty: an unmeasured disk has no Disk at all.
func disks(r *http.Request, h extension.Host, cluster, namespace string, members []Member, pods map[string]instancePod, sum Summary) string {
	nodes := map[string]bool{}
	for _, p := range pods {
		if p.facts.Node != "" {
			nodes[p.facts.Node] = true
		}
	}
	if len(nodes) == 0 {
		return "No instance pod is scheduled on a node, so no disk was measured."
	}
	names := make([]string, 0, len(nodes))
	for n := range nodes {
		names = append(names, n)
	}
	sort.Strings(names)
	usage, err := h.Usage(r, cluster, names)
	switch {
	case errors.Is(err, extension.ErrDenied):
		return "Disks not measured: you may not list nodes, which the kubelets' figures need."
	case errors.Is(err, extension.ErrUnreviewable):
		return "Disks not measured: " + extension.UnreviewedText
	case err != nil:
		return "Disks not measured: " + textutil.FirstLineN(err.Error(), 200)
	}
	volumes := map[string]extension.FS{}
	for _, node := range usage.Nodes {
		for _, p := range node.Pods {
			if p.Namespace != namespace {
				continue
			}
			for _, v := range p.Volumes {
				if v.PVCRef != "" {
					volumes[v.PVCRef] = v.FS
				}
			}
		}
	}
	for i := range members {
		if fs, ok := volumes[namespace+"/"+members[i].Name]; ok {
			members[i].Data = disk(members[i].Name, fs, sum.StorageBytes)
		}
		if fs, ok := volumes[namespace+"/"+members[i].Name+"-wal"]; ok {
			members[i].WAL = disk(members[i].Name+"-wal", fs, sum.WALStorageBytes)
		}
	}
	note := ""
	if usage.Answered < usage.Asked {
		note = "Only some of the nodes running this cluster answered for their disks."
		if usage.FirstErr != nil {
			note += " " + textutil.FirstLineN(usage.FirstErr.Error(), 200)
		}
	}
	if !usage.Complete && usage.Note != "" {
		note = usage.Note
	}
	return note
}

func disk(claim string, fs extension.FS, declared int64) *Disk {
	d := &Disk{Claim: claim, UsedBytes: fs.UsedBytes, CapacityBytes: fs.CapacityBytes, DeclaredBytes: declared, Trustworthy: true}
	if declared > 0 {
		d.Trustworthy = extension.Trustworthy(declared, fs.CapacityBytes)
	}
	return d
}

// eventsOf reads the Cluster's Events, newest first: where the failovers
// are, for as long as the API server keeps them (an hour by default).
func eventsOf(r *http.Request, h extension.Host, cluster, namespace, name string) ([]Event, Source) {
	page, err := h.List(r, cluster, extension.ListQuery{
		GVR: eventsGVR, Namespace: namespace,
		FieldSelector: "involvedObject.kind=Cluster,involvedObject.name=" + name, Limit: 500,
	})
	source := sourceOf("events", page, err)
	out := make([]Event, 0, len(page.Items))
	for i := range page.Items {
		e := page.Items[i].Object
		if str(e, "involvedObject", "kind") != "Cluster" || str(e, "involvedObject", "name") != name {
			continue
		}
		ev := Event{Type: str(e, "type"), Reason: str(e, "reason"), Message: clip(str(e, "message"), 300), Count: num(e, "count")}
		ev.At = firstTime(timeAt(e, "lastTimestamp"), timeAt(e, "series", "lastObservedTime"), timeAt(e, "eventTime"), timeAt(e, "firstTimestamp"))
		out = append(out, ev)
	}
	sort.SliceStable(out, func(a, b int) bool {
		switch {
		case out[a].At == nil:
			return false
		case out[b].At == nil:
			return true
		}
		return out[a].At.After(*out[b].At)
	})
	if len(out) > MaxEvents {
		out = out[:MaxEvents]
		if source.Note == "" {
			source.Note = "Only the newest Events are shown."
		}
	}
	return out, source
}

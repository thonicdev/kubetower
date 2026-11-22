package events

import (
	"net/http"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
)

// The ceilings on what one aggregate holds, so a large cluster costs a
// bounded amount. Past one, what is cut is counted in the tiles and cause
// totals where it can be, and the answer says `truncated`. Groups and causes
// are capped per type (Warning, Normal): a flood of Normal Events never
// pushes the Warnings out.
const (
	// MaxGroups is the most (object, reason, message) rows kept, per type.
	MaxGroups = 2000
	// MaxCauses is the most (reason, message) causes kept, per type.
	MaxCauses = 500
	// MaxExamples is the raw messages kept per cause.
	MaxExamples = 5
	// MaxChanges is the most changes kept, newest first.
	MaxChanges = 500
	// MaxPods is the most pods whose owner and node are kept.
	MaxPods = 50000
	// MaxReplicaSets is the most ReplicaSets read.
	MaxReplicaSets = 50000
	// MaxUnhealthyPods is the most CrashLoop or OOMKilled pods listed.
	MaxUnhealthyPods = 500
	// MaxExampleBytes clips a raw example message.
	MaxExampleBytes = 400
)

// Change kinds.
const (
	ChangeRollout     = "rollout"
	ChangeRollback    = "rollback"
	ChangeConfig      = "config"
	ChangeScaling     = "scaling"
	ChangeNodeAdded   = "node-added"
	ChangeNodeNotRead = "node-notready"
)

// Change is something that changed in the cluster, read from what it already
// holds. A change is a fact with a time; whether it explains anything is the
// correlation's word (`since` on a cause or an object group), never the
// change's.
type Change struct {
	// ID is stable for the same change: "<kind>/<namespace>/<name>/<n>".
	ID string `json:"id"`
	// Kind is rollout, rollback, config, scaling, node-added or node-notready.
	Kind string `json:"kind"`
	// ObjectKind, Namespace and Name are what changed: a Deployment, a
	// ConfigMap, a HorizontalPodAutoscaler or a Node (no namespace).
	ObjectKind string    `json:"objectKind"`
	Namespace  string    `json:"namespace,omitempty"`
	Name       string    `json:"name"`
	Time       time.Time `json:"time"`
	// Revision is a rollout's or rollback's `deployment.kubernetes.io/revision`.
	Revision int64 `json:"revision,omitempty"`
	// ImagesBefore and ImagesAfter are a rollout's images against the previous
	// revision's (before is empty for a first revision).
	ImagesBefore []string `json:"imagesBefore,omitempty"`
	ImagesAfter  []string `json:"imagesAfter,omitempty"`
	// Manager is the field manager that last wrote a ConfigMap.
	Manager string `json:"manager,omitempty"`
	// Detail is a scaling's own message.
	Detail string `json:"detail,omitempty"`
	// Referenced is, for a ConfigMap, whether a Deployment's current
	// ReplicaSet in the namespace uses it. An unreferenced one explains
	// nothing and the page leaves it off the timeline unless asked.
	Referenced bool `json:"referenced"`
}

// ObjectGroup is one row of a cause: one object's Events for that cause,
// deduplicated.
type ObjectGroup struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Count is the Events' counts added together (study rule: series.count,
	// else count, else 1).
	Count     int       `json:"count"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	// WorkloadKind and WorkloadName are the workload the object belongs to
	// (a pod's Deployment, StatefulSet, DaemonSet or Job; the object itself
	// where it has none). Node is the node carrying the pod.
	WorkloadKind string `json:"workloadKind,omitempty"`
	WorkloadName string `json:"workloadName,omitempty"`
	Node         string `json:"node,omitempty"`
	// Message is one raw message of the group.
	Message string `json:"message"`
	// Since is the ID of the change this group is related to (a Change of
	// the answer's `changes`), or empty.
	Since string `json:"since,omitempty"`
}

// Cause is every Event with the same reason and normalized message.
type Cause struct {
	Reason string `json:"reason"`
	// Message is the normalized message: `*` stands for what differs.
	Message string `json:"message"`
	// Type is Warning or Normal.
	Type string `json:"type"`
	// Count is the Events' counts added together; Objects the distinct
	// objects behind them.
	Count     int       `json:"count"`
	Objects   int       `json:"objects"`
	Kinds     []string  `json:"kinds"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	// Examples are up to five raw messages, distinct.
	Examples []string `json:"examples"`
	// Since is the change most of the cause's objects are related to (ties:
	// the more recent), a Change ID, or empty.
	Since string `json:"since,omitempty"`
	// Groups is the cause's objects, most Events first.
	Groups []ObjectGroup `json:"groups"`
}

// Tile is one of the page's five figures.
type Tile struct {
	Count int `json:"count"`
	// State is the read behind it: "read", or why it could not be made
	// (denied, unreviewable, failed). A refused read is not a zero.
	State string `json:"state"`
}

// Tiles are the page's tiles. Warnings, FailedScheduling and ObjectsTouched
// come from Events; CrashLoop and OOMKilled from the pods' statuses, because
// neither is an Event.
type Tiles struct {
	Warnings         Tile `json:"warnings"`
	CrashLoop        Tile `json:"crashLoop"`
	OOMKilled        Tile `json:"oomKilled"`
	FailedScheduling Tile `json:"failedScheduling"`
	ObjectsTouched   Tile `json:"objectsTouched"`
}

// PodState is a pod that is crash-looping or was OOM-killed.
type PodState struct {
	Namespace    string `json:"namespace"`
	Name         string `json:"name"`
	Node         string `json:"node,omitempty"`
	WorkloadKind string `json:"workloadKind,omitempty"`
	WorkloadName string `json:"workloadName,omitempty"`
	CrashLoop    bool   `json:"crashLoop"`
	OOMKilled    bool   `json:"oomKilled"`
}

// Sources is each read, by name.
type Sources struct {
	Events      Section `json:"events"`
	ReplicaSets Section `json:"replicasets"`
	Pods        Section `json:"pods"`
	ConfigMaps  Section `json:"configmaps"`
	Nodes       Section `json:"nodes"`
}

// Response is the answer of `GET /` and `GET /object/...`.
type Response struct {
	Cluster string `json:"cluster,omitempty"`
	// Namespace is the namespace asked for, empty for every one the person
	// may list.
	Namespace string `json:"namespace"`
	// Window is 15m, 1h or 3h; Since the time it starts at.
	Window      string    `json:"window"`
	Since       time.Time `json:"since"`
	GeneratedAt time.Time `json:"generatedAt"`
	// Object is set on `/object` answers.
	Object  *ObjectRef `json:"object,omitempty"`
	Tiles   Tiles      `json:"tiles"`
	Causes  []Cause    `json:"causes"`
	Changes []Change   `json:"changes"`
	// Pods are the crash-looping or OOM-killed ones.
	Pods    []PodState `json:"pods"`
	Sources Sources    `json:"sources"`
	// Truncated is true when a cap was hit (groups, causes, changes, pods):
	// the totals of the tiles and the causes still count what was cut where
	// they can, the lists do not hold it.
	Truncated bool `json:"truncated"`
	// Complete is false when the person's Events read was narrowed to the
	// namespaces they may list, or cut short; Note says why.
	Complete bool   `json:"complete"`
	Note     string `json:"note,omitempty"`
}

type groupKey struct{ obj, reason, msg string }
type causeKey struct{ reason, msg string }

// group is an ObjectGroup being built.
type group struct {
	ObjectGroup
	typ, reason, norm string
	rs                string
}

type cause struct {
	Cause
	objects map[string]struct{}
	kinds   map[string]struct{}
}

// scalingEvent is a ScalingReplicaSet Event waiting for the ReplicaSets, which
// say whether it is a rollout's own noise or a scaling.
type scalingEvent struct {
	namespace, deployment, rs string
	from, to                  int
	at                        time.Time
	message                   string
}

// aggregate is everything one request computes, kept 15 s by the cache.
type aggregate struct {
	cluster, namespace, window string
	windowDur                  time.Duration
	built                      time.Time

	groups  []*group
	causes  []*cause
	changes []Change
	pods    []podInfo
	// podByKey and rsByKey resolve an object to its workload.
	podByKey map[string]*podInfo
	rsByKey  map[string]*replicaSet
	// deployConfigs is the ConfigMaps each Deployment's current ReplicaSet
	// references, by namespace/deployment.
	deployConfigs map[string]map[string]bool

	// totals over every Event read, whatever was cut from the lists.
	warnings, failedScheduling int
	touched                    map[string]struct{}
	backoffPods                map[string]struct{}

	truncated bool
	complete  bool
	note      string
	sources   Sources
}

// aggregateFor is the cached aggregate for the person on r.
func (e *Events) aggregateFor(r *http.Request, h extension.Host, cluster, ns, window string, d time.Duration) (*aggregate, error) {
	key, ok := personKey(r)
	build := func() (*aggregate, error) { return e.build(r, h, cluster, ns, window, d) }
	if !ok {
		// Nobody to key on: a read is made, and never shared.
		return build()
	}
	return e.cache.get(cacheKey{person: key, cluster: cluster, namespace: ns, window: window}, e.now(), build)
}

// build reads the cluster and aggregates it.
func (e *Events) build(r *http.Request, h extension.Host, cluster, ns, window string, d time.Duration) (*aggregate, error) {
	now := e.now()
	since := now.Add(-d)
	a := &aggregate{
		cluster: cluster, namespace: ns, window: window, windowDur: d, built: now,
		touched: map[string]struct{}{}, backoffPods: map[string]struct{}{},
		podByKey: map[string]*podInfo{}, rsByKey: map[string]*replicaSet{},
		complete: true,
	}
	b := &builder{a: a, since: since, now: now,
		groupIdx: map[groupKey]*group{}, causeIdx: map[causeKey]*cause{},
		groupsOf: map[string]int{}, causesOf: map[string]int{}}

	// The Events are the point: a failure here is the request's.
	page, err := h.List(r, cluster, extension.ListQuery{GVR: eventsGVR, Namespace: ns,
		Each: func(u unstructured.Unstructured) error { b.ingest(readEvent(&u), ns != ""); return nil }})
	if err != nil {
		return nil, err
	}
	a.sources.Events = sectionOf(page.Complete, page.Note, nil)
	if !page.Complete {
		a.complete, a.note = false, page.Note
	}
	if ns != "" {
		// A node's Events live in the default namespace, not the one asked
		// for: its NodeNotReady is read apart, and aggregates nothing else.
		_, err := h.List(r, cluster, extension.ListQuery{GVR: eventsGVR, FieldSelector: "involvedObject.kind=Node",
			Each: func(u unstructured.Unstructured) error {
				ev := readEvent(&u)
				if ev.kind == "Node" && ev.reason == "NodeNotReady" {
					b.nodeEvent(ev)
				}
				return nil
			}})
		if err != nil && ends(r, err) {
			return nil, err
		}
	}

	// The secondary reads: a refused one marks its source, never an empty one.
	var rsList []*replicaSet
	page, err = h.List(r, cluster, extension.ListQuery{GVR: replicaSets, Namespace: ns,
		Each: func(u unstructured.Unstructured) error {
			if len(rsList) >= MaxReplicaSets {
				a.truncated = true
				return nil
			}
			rs := readReplicaSet(&u)
			rsList = append(rsList, &rs)
			return nil
		}})
	if err != nil && ends(r, err) {
		return nil, err
	}
	a.sources.ReplicaSets = sectionOf(page.Complete, page.Note, err)

	page, err = h.List(r, cluster, extension.ListQuery{GVR: podsGVR, Namespace: ns,
		Each: func(u unstructured.Unstructured) error {
			p := readPod(&u, since)
			if len(a.pods) >= MaxPods {
				a.truncated = true
				if !(p.crashLoop || p.oomKilled) {
					return nil
				}
			}
			a.pods = append(a.pods, p)
			return nil
		}})
	if err != nil && ends(r, err) {
		return nil, err
	}
	a.sources.Pods = sectionOf(page.Complete, page.Note, err)

	var configs []configChange
	cutoff := since.Add(-correlationWindow)
	page, err = h.List(r, cluster, extension.ListQuery{GVR: configMapsGVR, Namespace: ns,
		Each: func(u unstructured.Unstructured) error {
			// Only the edit's time and manager are read; most ConfigMaps are
			// older than the window and are dropped here.
			if c := readConfigMap(&u); !c.at.Before(cutoff) {
				configs = append(configs, c)
			}
			return nil
		}})
	if err != nil && ends(r, err) {
		return nil, err
	}
	a.sources.ConfigMaps = sectionOf(page.Complete, page.Note, err)

	var nodes []nodeAdded
	page, err = h.List(r, cluster, extension.ListQuery{GVR: nodesGVR, Columns: true})
	if err != nil && ends(r, err) {
		return nil, err
	}
	a.sources.Nodes = sectionOf(page.Complete, page.Note, err)
	if err == nil {
		for _, row := range page.Rows {
			if !row.Created.Before(cutoff) {
				nodes = append(nodes, nodeAdded{name: row.Name, at: row.Created})
			}
		}
	}

	a.finish(b, rsList, configs, nodes)
	return a, nil
}

type nodeAdded struct {
	name string
	at   time.Time
}

// builder holds what ingesting Events needs while the aggregate is built.
type builder struct {
	a        *aggregate
	since    time.Time
	now      time.Time
	groupIdx map[groupKey]*group
	causeIdx map[causeKey]*cause
	// perType counts how many groups and causes each type holds, for the caps.
	groupsOf, causesOf map[string]int

	scaling  []scalingEvent
	rescales []Change
	notReady []Change
	nodeSeen map[string]bool
}

// ingest adds one Event to the aggregate.
func (b *builder) ingest(ev event, namespaced bool) {
	if ev.last.Before(b.since) {
		return
	}
	a := b.a
	typ := ev.typ
	if typ == "" {
		typ = "Normal"
	}
	// Changes that are Events: scaling, rescale, a node going NotReady.
	switch {
	case ev.reason == "ScalingReplicaSet" && ev.kind == "Deployment":
		if s, ok := parseScaling(ev); ok {
			b.scaling = append(b.scaling, s)
		}
	case ev.reason == "SuccessfulRescale" && ev.kind == "HorizontalPodAutoscaler":
		if len(b.rescales) < MaxChanges {
			b.rescales = append(b.rescales, Change{ID: changeID(ChangeScaling, ev.namespace, ev.name, ev.last.Unix()),
				Kind: ChangeScaling, ObjectKind: ev.kind, Namespace: ev.namespace, Name: ev.name, Time: ev.last,
				Detail: clip(ev.message, 200)})
		}
	case ev.reason == "NodeNotReady" && ev.kind == "Node":
		b.nodeEvent(ev)
		if namespaced {
			return
		}
	}

	objKey := ev.uid
	if objKey == "" {
		objKey = ev.kind + "/" + ev.namespace + "/" + ev.name
	}
	if typ == "Warning" {
		a.warnings += ev.count
		a.touched[objKey] = struct{}{}
	}
	if ev.reason == "FailedScheduling" {
		a.failedScheduling += ev.count
	}
	if ev.reason == "BackOff" && ev.kind == "Pod" {
		a.backoffPods[ev.namespace+"/"+ev.name] = struct{}{}
	}

	norm := normalise(ev.message, ev.name)
	ck := causeKey{ev.reason, norm}
	c := b.causeIdx[ck]
	if c == nil {
		if b.causesOf[typ] >= MaxCauses {
			a.truncated = true
		} else {
			b.causesOf[typ]++
			c = &cause{Cause: Cause{Reason: ev.reason, Message: norm, Type: typ, FirstSeen: ev.first, LastSeen: ev.last},
				objects: map[string]struct{}{}, kinds: map[string]struct{}{}}
			b.causeIdx[ck] = c
			a.causes = append(a.causes, c)
		}
	}
	if c != nil {
		c.Count += ev.count
		if ev.first.Before(c.FirstSeen) {
			c.FirstSeen = ev.first
		}
		if ev.last.After(c.LastSeen) {
			c.LastSeen = ev.last
		}
		c.objects[objKey] = struct{}{}
		c.kinds[ev.kind] = struct{}{}
		c.addExample(ev.message)
	}

	gk := groupKey{objKey, ev.reason, norm}
	g := b.groupIdx[gk]
	if g == nil {
		if b.groupsOf[typ] >= MaxGroups {
			a.truncated = true
			return
		}
		b.groupsOf[typ]++
		g = &group{typ: typ, reason: ev.reason, norm: norm, ObjectGroup: ObjectGroup{
			Kind: ev.kind, Namespace: ev.namespace, Name: ev.name, FirstSeen: ev.first, LastSeen: ev.last,
			Message: clip(ev.message, MaxExampleBytes)}}
		b.groupIdx[gk] = g
		a.groups = append(a.groups, g)
	}
	g.Count += ev.count
	if ev.first.Before(g.FirstSeen) {
		g.FirstSeen = ev.first
	}
	if ev.last.After(g.LastSeen) {
		g.LastSeen = ev.last
	}
}

// nodeEvent records a NodeNotReady once per node and onset.
func (b *builder) nodeEvent(ev event) {
	if b.nodeSeen == nil {
		b.nodeSeen = map[string]bool{}
	}
	id := changeID(ChangeNodeNotRead, "", ev.name, ev.first.Unix())
	if b.nodeSeen[id] || len(b.notReady) >= MaxChanges || ev.first.Before(b.since.Add(-correlationWindow)) {
		return
	}
	b.nodeSeen[id] = true
	b.notReady = append(b.notReady, Change{ID: id, Kind: ChangeNodeNotRead, ObjectKind: "Node", Name: ev.name,
		Time: ev.first, Detail: clip(ev.message, 200)})
}

func (c *cause) addExample(msg string) {
	if len(c.Examples) >= MaxExamples {
		return
	}
	msg = clip(msg, MaxExampleBytes)
	for _, e := range c.Examples {
		if e == msg {
			return
		}
	}
	c.Examples = append(c.Examples, msg)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// finish resolves workloads, derives the changes, correlates and sorts.
func (a *aggregate) finish(b *builder, rsList []*replicaSet, configs []configChange, nodes []nodeAdded) {
	for _, rs := range rsList {
		a.rsByKey[rs.namespace+"/"+rs.name] = rs
	}
	for i := range a.pods {
		p := &a.pods[i]
		a.podByKey[p.namespace+"/"+p.name] = p
	}
	a.deriveChanges(b, rsList, configs, nodes)

	for _, g := range a.groups {
		a.resolve(g)
	}
	a.correlate()

	// Order: Warnings before Normal, then the most Events.
	sort.SliceStable(a.causes, func(i, j int) bool {
		x, y := a.causes[i], a.causes[j]
		if (x.Type == "Warning") != (y.Type == "Warning") {
			return x.Type == "Warning"
		}
		if x.Count != y.Count {
			return x.Count > y.Count
		}
		return x.Reason+x.Message < y.Reason+y.Message
	})
}

func changeID(kind, ns, name string, n int64) string {
	return kind + "/" + ns + "/" + name + "/" + itoa(n)
}

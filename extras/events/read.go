package events

import (
	"sort"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// This file reads the few fields this extra aggregates out of the objects a
// list hands back. Nothing is converted to a typed object: an Event is read
// straight off its map, so 50 000 of them cost a few map lookups each, and
// nothing of an object outlives the call that read it - strings are copied
// out (a ConfigMap's data, a pod's environment are never touched).

func sub(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	return v
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func num(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

// ts reads an RFC 3339 time, whole seconds (Time) or microseconds (MicroTime);
// anything else is the zero time.
func ts(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// event is one Event as the aggregate reads it.
type event struct {
	typ, reason, message string
	// The object it is about.
	kind, namespace, name, uid string
	count                      int
	first, last                time.Time
}

// readEvent reads one core/v1 Event in either of its two shapes
// (`events.k8s.io` writes eventTime and series and leaves count, the
// timestamps and source empty; the kubelet and most controllers write the
// other).
//
//   - count is series.count, else count when above zero, else 1;
//   - first seen is firstTimestamp, else eventTime, else creationTimestamp;
//   - last seen is series.lastObservedTime, else lastTimestamp, else eventTime,
//     else first seen.
func readEvent(u *unstructured.Unstructured) event {
	o := u.Object
	inv := sub(o, "involvedObject")
	ev := event{
		typ: str(o, "type"), reason: str(o, "reason"), message: str(o, "message"),
		kind: str(inv, "kind"), namespace: str(inv, "namespace"), name: str(inv, "name"), uid: str(inv, "uid"),
	}
	series := sub(o, "series")
	switch c := num(series["count"]); {
	case c > 0:
		ev.count = int(c)
	case num(o["count"]) > 0:
		ev.count = int(num(o["count"]))
	default:
		ev.count = 1
	}
	eventTime := ts(str(o, "eventTime"))
	ev.first = ts(str(o, "firstTimestamp"))
	if ev.first.IsZero() {
		ev.first = eventTime
	}
	if ev.first.IsZero() {
		ev.first = ts(str(sub(o, "metadata"), "creationTimestamp"))
	}
	ev.last = ts(str(series, "lastObservedTime"))
	if ev.last.IsZero() {
		ev.last = ts(str(o, "lastTimestamp"))
	}
	if ev.last.IsZero() {
		ev.last = eventTime
	}
	if ev.last.IsZero() {
		ev.last = ev.first
	}
	if ev.namespace == "" {
		ev.namespace = str(sub(o, "metadata"), "namespace")
	}
	return ev
}

// replicaSet is what a rollout needs of a ReplicaSet.
type replicaSet struct {
	namespace, name string
	// deploy is the Deployment that owns it, empty for a bare ReplicaSet.
	deploy   string
	revision int64
	created  time.Time
	images   []string
	// configs are the ConfigMaps its pod template references: as a volume
	// (also projected), an env valueFrom or an envFrom.
	configs []string
}

const revisionAnnotation = "deployment.kubernetes.io/revision"

func readReplicaSet(u *unstructured.Unstructured) replicaSet {
	rs := replicaSet{namespace: u.GetNamespace(), name: u.GetName(), created: u.GetCreationTimestamp().Time}
	for _, o := range u.GetOwnerReferences() {
		if o.Kind == "Deployment" {
			rs.deploy = o.Name
		}
	}
	rs.revision, _ = strconv.ParseInt(u.GetAnnotations()[revisionAnnotation], 10, 64)
	spec := sub(sub(sub(u.Object, "spec"), "template"), "spec")
	seen := map[string]bool{}
	addConfig := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			rs.configs = append(rs.configs, name)
		}
	}
	if cs, ok := spec["containers"].([]any); ok {
		for _, c := range cs {
			cm, _ := c.(map[string]any)
			if img := str(cm, "image"); img != "" {
				rs.images = append(rs.images, img)
			}
			if env, ok := cm["env"].([]any); ok {
				for _, e := range env {
					em, _ := e.(map[string]any)
					addConfig(str(sub(sub(em, "valueFrom"), "configMapKeyRef"), "name"))
				}
			}
			if from, ok := cm["envFrom"].([]any); ok {
				for _, f := range from {
					fm, _ := f.(map[string]any)
					addConfig(str(sub(fm, "configMapRef"), "name"))
				}
			}
		}
	}
	if vols, ok := spec["volumes"].([]any); ok {
		for _, v := range vols {
			vm, _ := v.(map[string]any)
			addConfig(str(sub(vm, "configMap"), "name"))
			if srcs, ok := sub(vm, "projected")["sources"].([]any); ok {
				for _, s := range srcs {
					sm, _ := s.(map[string]any)
					addConfig(str(sub(sm, "configMap"), "name"))
				}
			}
		}
	}
	sort.Strings(rs.images)
	return rs
}

// podInfo is what the aggregate keeps of a pod: where it runs and who owns it.
type podInfo struct {
	namespace, name, node string
	// rs is the ReplicaSet that owns it; workloadKind and workloadName name
	// the owner of any other kind (StatefulSet, DaemonSet, Job).
	rs                       string
	workloadKind, workloadNm string
	crashLoop, oomKilled     bool
}

func readPod(u *unstructured.Unstructured, since time.Time) podInfo {
	p := podInfo{namespace: u.GetNamespace(), name: u.GetName(), node: str(sub(u.Object, "spec"), "nodeName")}
	for _, o := range u.GetOwnerReferences() {
		if o.Controller != nil && !*o.Controller {
			continue
		}
		if o.Kind == "ReplicaSet" {
			p.rs = o.Name
		} else {
			p.workloadKind, p.workloadNm = o.Kind, o.Name
		}
	}
	status := sub(u.Object, "status")
	for _, list := range []string{"containerStatuses", "initContainerStatuses"} {
		cs, _ := status[list].([]any)
		for _, c := range cs {
			cm, _ := c.(map[string]any)
			state, last := sub(cm, "state"), sub(cm, "lastState")
			if str(sub(state, "waiting"), "reason") == "CrashLoopBackOff" {
				p.crashLoop = true
			}
			for _, t := range []map[string]any{sub(state, "terminated"), sub(last, "terminated")} {
				if str(t, "reason") != "OOMKilled" {
					continue
				}
				// A kill older than the window is not what the window asks about.
				if at := ts(str(t, "finishedAt")); at.IsZero() || !at.Before(since) {
					p.oomKilled = true
				}
			}
		}
	}
	return p
}

// configChange is the latest edit of a ConfigMap, from managedFields.
type configChange struct {
	namespace, name, manager string
	at                       time.Time
}

// readConfigMap reads the time of a ConfigMap's latest managedFields entry and
// the manager that wrote it. `data` and `binaryData` are never looked at.
func readConfigMap(u *unstructured.Unstructured) configChange {
	c := configChange{namespace: u.GetNamespace(), name: u.GetName()}
	fields, _ := sub(u.Object, "metadata")["managedFields"].([]any)
	for _, f := range fields {
		fm, _ := f.(map[string]any)
		if at := ts(str(fm, "time")); at.After(c.at) {
			c.at, c.manager = at, str(fm, "manager")
		}
	}
	return c
}

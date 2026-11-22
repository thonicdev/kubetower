package events

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// correlationWindow is how long after a change an Event may first be seen and
// still be related to it.
const correlationWindow = 30 * time.Minute

// scaleSettle is how long after a ReplicaSet's creation a ScalingReplicaSet
// Event on it still reads as the rollout's own work (a rolling update scales
// the new one up and the old one down, step by step) rather than a scaling.
const scaleSettle = 10 * time.Minute

// MaxUnreferencedConfigs is the most ConfigMap edits no workload uses that the
// timeline keeps; they explain nothing and only the page's "all changes"
// filter shows them.
const MaxUnreferencedConfigs = 100

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

var scalingMessage = regexp.MustCompile(`^Scaled (up|down) replica set (\S+) from (\d+) to (\d+)`)

// parseScaling reads "Scaled up replica set web-5d8f from 3 to 6".
func parseScaling(ev event) (scalingEvent, bool) {
	m := scalingMessage.FindStringSubmatch(ev.message)
	if m == nil {
		return scalingEvent{}, false
	}
	from, _ := strconv.Atoi(m[3])
	to, _ := strconv.Atoi(m[4])
	return scalingEvent{namespace: ev.namespace, deployment: ev.name, rs: m[2], from: from, to: to, at: ev.last,
		message: clip(ev.message, 200)}, true
}

// deriveChanges is every change the aggregate knows of, newest first, from the
// ReplicaSets (rollouts and rollbacks), the Events kept while ingesting
// (scaling, rescale, NotReady), the ConfigMaps and the nodes.
func (a *aggregate) deriveChanges(b *builder, rsList []*replicaSet, configs []configChange, nodes []nodeAdded) {
	cutoff := b.since.Add(-correlationWindow)
	byDeploy := map[string][]*replicaSet{}
	for _, rs := range rsList {
		if rs.deploy != "" {
			byDeploy[rs.namespace+"/"+rs.deploy] = append(byDeploy[rs.namespace+"/"+rs.deploy], rs)
		}
	}
	a.deployConfigs = map[string]map[string]bool{}
	rolledBack := map[string]bool{}
	current := map[string]*replicaSet{}
	var out []Change

	keys := make([]string, 0, len(byDeploy))
	for k := range byDeploy {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		list := byDeploy[key]
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].revision != list[j].revision {
				return list[i].revision < list[j].revision
			}
			return list[i].created.Before(list[j].created)
		})
		cur := list[len(list)-1]
		current[key] = cur
		set := map[string]bool{}
		for _, c := range cur.configs {
			set[c] = true
		}
		a.deployConfigs[key] = set
		for i, rs := range list {
			var prev *replicaSet
			if i > 0 {
				prev = list[i-1]
			}
			if prev != nil && rs.created.Before(prev.created) {
				// An older ReplicaSet took a higher revision: a rollback. It
				// keeps its creation date, so the time is the Event that
				// scaled it back up.
				rolledBack[rs.namespace+"/"+rs.name] = true
				var at time.Time
				for _, s := range b.scaling {
					if s.namespace == rs.namespace && s.rs == rs.name && s.to > s.from && s.at.After(at) {
						at = s.at
					}
				}
				if !at.IsZero() && !at.Before(cutoff) {
					out = append(out, Change{ID: changeID(ChangeRollback, rs.namespace, rs.deploy, rs.revision),
						Kind: ChangeRollback, ObjectKind: "Deployment", Namespace: rs.namespace, Name: rs.deploy, Time: at,
						Revision: rs.revision, ImagesBefore: prev.images, ImagesAfter: rs.images})
				}
				continue
			}
			if rs.created.Before(cutoff) {
				continue
			}
			c := Change{ID: changeID(ChangeRollout, rs.namespace, rs.deploy, rs.revision), Kind: ChangeRollout,
				ObjectKind: "Deployment", Namespace: rs.namespace, Name: rs.deploy, Time: rs.created,
				Revision: rs.revision, ImagesAfter: rs.images}
			if prev != nil {
				c.ImagesBefore = prev.images
			}
			out = append(out, c)
		}
	}

	// Scaling: only on the Deployment's current ReplicaSet and only once the
	// rollout that made it has had time to settle.
	for _, s := range b.scaling {
		rs := a.rsByKey[s.namespace+"/"+s.rs]
		if rs == nil || rs.deploy != s.deployment || rolledBack[rs.namespace+"/"+rs.name] ||
			current[rs.namespace+"/"+rs.deploy] != rs || s.at.Before(rs.created.Add(scaleSettle)) || s.at.Before(cutoff) {
			continue
		}
		out = append(out, Change{ID: changeID(ChangeScaling, s.namespace, s.deployment, s.at.Unix()), Kind: ChangeScaling,
			ObjectKind: "Deployment", Namespace: s.namespace, Name: s.deployment, Time: s.at, Detail: s.message})
	}
	for _, c := range b.rescales {
		if !c.Time.Before(cutoff) {
			out = append(out, c)
		}
	}
	out = append(out, b.notReady...)
	for _, n := range nodes {
		out = append(out, Change{ID: changeID(ChangeNodeAdded, "", n.name, n.at.Unix()), Kind: ChangeNodeAdded,
			ObjectKind: "Node", Name: n.name, Time: n.at})
	}

	used := map[string]bool{}
	for key, set := range a.deployConfigs {
		ns, _, _ := strings.Cut(key, "/")
		for c := range set {
			used[ns+"/"+c] = true
		}
	}
	for _, c := range configs {
		out = append(out, Change{ID: changeID(ChangeConfig, c.namespace, c.name, c.at.Unix()), Kind: ChangeConfig,
			ObjectKind: "ConfigMap", Namespace: c.namespace, Name: c.name, Time: c.at, Manager: c.manager,
			Referenced: used[c.namespace+"/"+c.name]})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.After(out[j].Time)
		}
		return out[i].ID < out[j].ID
	})
	seen := map[string]bool{}
	unreferenced := 0
	for _, c := range out {
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		if c.Kind == ChangeConfig && !c.Referenced {
			if unreferenced++; unreferenced > MaxUnreferencedConfigs {
				a.truncated = true
				continue
			}
		}
		if len(a.changes) >= MaxChanges {
			a.truncated = true
			break
		}
		a.changes = append(a.changes, c)
	}
}

// rsWorkload is the workload a ReplicaSet belongs to: its Deployment, or
// itself when it has none (or is not known).
func (a *aggregate) rsWorkload(ns, rs string) (kind, name string) {
	if r := a.rsByKey[ns+"/"+rs]; r != nil && r.deploy != "" {
		return "Deployment", r.deploy
	}
	return "ReplicaSet", rs
}

// podOwner is a pod's ReplicaSet and workload, from the pod when it is still
// there. A pod that is gone (its Events outlive it) is placed by its name:
// `<replicaset>-<suffix>`.
func (a *aggregate) podOwner(ns, name string) (rs, wk, wn, node string) {
	if p := a.podByKey[ns+"/"+name]; p != nil {
		switch {
		case p.rs != "":
			wk, wn = a.rsWorkload(ns, p.rs)
			return p.rs, wk, wn, p.node
		case p.workloadKind != "":
			return "", p.workloadKind, p.workloadNm, p.node
		}
		return "", "Pod", name, p.node
	}
	if i := strings.LastIndexByte(name, '-'); i > 0 {
		if _, ok := a.rsByKey[ns+"/"+name[:i]]; ok {
			wk, wn = a.rsWorkload(ns, name[:i])
			return name[:i], wk, wn, ""
		}
	}
	return "", "Pod", name, ""
}

// resolve fills a group's workload and node.
func (a *aggregate) resolve(g *group) {
	switch g.Kind {
	case "Pod":
		g.rs, g.WorkloadKind, g.WorkloadName, g.Node = a.podOwner(g.Namespace, g.Name)
	case "ReplicaSet":
		g.rs = g.Name
		g.WorkloadKind, g.WorkloadName = a.rsWorkload(g.Namespace, g.Name)
	case "Node":
		g.WorkloadKind, g.WorkloadName, g.Node = "Node", g.Name, g.Name
	default:
		g.WorkloadKind, g.WorkloadName = g.Kind, g.Name
	}
}

// correlate relates each group to the most recent change that precedes it:
// the same workload, a ConfigMap the workload references, or the node carrying
// its pods - and the group's first seen no later than thirty minutes after the
// change. It is a correlation in time.
func (a *aggregate) correlate() {
	byWorkload := map[string][]*Change{}
	byConfig := map[string][]*Change{}
	byNode := map[string][]*Change{}
	for i := range a.changes {
		c := &a.changes[i]
		switch c.Kind {
		case ChangeConfig:
			byConfig[c.Namespace+"/"+c.Name] = append(byConfig[c.Namespace+"/"+c.Name], c)
		case ChangeNodeAdded, ChangeNodeNotRead:
			byNode[c.Name] = append(byNode[c.Name], c)
		default:
			byWorkload[c.Namespace+"/"+c.ObjectKind+"/"+c.Name] = append(byWorkload[c.Namespace+"/"+c.ObjectKind+"/"+c.Name], c)
		}
	}
	for _, g := range a.groups {
		var best *Change
		consider := func(list []*Change) {
			for _, c := range list {
				if c.Time.After(g.FirstSeen) || g.FirstSeen.Sub(c.Time) > correlationWindow {
					continue
				}
				if best == nil || c.Time.After(best.Time) {
					best = c
				}
			}
		}
		consider(byWorkload[g.Namespace+"/"+g.WorkloadKind+"/"+g.WorkloadName])
		if g.WorkloadKind == "Deployment" {
			for cm := range a.deployConfigs[g.Namespace+"/"+g.WorkloadName] {
				consider(byConfig[g.Namespace+"/"+cm])
			}
		}
		if g.Node != "" {
			consider(byNode[g.Node])
		}
		if best != nil {
			g.Since = best.ID
		}
	}
}

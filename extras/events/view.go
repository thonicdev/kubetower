package events

import "sort"

// objectFilter reduces an aggregate to one object and its pods.
type objectFilter struct{ kind, name string }

func (f *objectFilter) matchGroup(g *group) bool {
	switch f.kind {
	case "Pod":
		return g.Kind == "Pod" && g.Name == f.name
	case "ReplicaSet":
		return (g.Kind == "ReplicaSet" && g.Name == f.name) || (g.Kind == "Pod" && g.rs == f.name)
	}
	return g.WorkloadKind == f.kind && g.WorkloadName == f.name
}

func (a *aggregate) matchPod(f *objectFilter, p *podInfo) bool {
	rs, wk, wn, _ := a.podOwner(p.namespace, p.name)
	switch f.kind {
	case "Pod":
		return p.name == f.name
	case "ReplicaSet":
		return rs == f.name
	}
	return wk == f.kind && wn == f.name
}

// workloadOf is the workload an object filter stands for.
func (a *aggregate) workloadOf(f *objectFilter) (kind, name string) {
	switch f.kind {
	case "Pod":
		_, kind, name, _ = a.podOwner(a.namespace, f.name)
		return kind, name
	case "ReplicaSet":
		return a.rsWorkload(a.namespace, f.name)
	}
	return f.kind, f.name
}

// view is the aggregate as the page's answer: whole, or reduced to an object
// and its pods.
func (a *aggregate) view(f *objectFilter) Response {
	groups := make([]*group, 0, len(a.groups))
	for _, g := range a.groups {
		if f == nil || f.matchGroup(g) {
			groups = append(groups, g)
		}
	}
	resp := Response{
		Cluster: a.cluster, Namespace: a.namespace, Window: a.window, Since: a.built.Add(-a.windowDur),
		GeneratedAt: a.built, Sources: a.sources, Truncated: a.truncated, Complete: a.complete, Note: a.note,
	}
	byID := map[string]*Change{}
	for i := range a.changes {
		byID[a.changes[i].ID] = &a.changes[i]
	}

	// The pods this view is about.
	var pods []*podInfo
	for i := range a.pods {
		if f == nil || a.matchPod(f, &a.pods[i]) {
			pods = append(pods, &a.pods[i])
		}
	}

	resp.Causes = a.causesOf(groups, f == nil, byID)
	resp.Tiles = a.tiles(groups, pods, f == nil)

	nodes := map[string]bool{}
	for _, p := range pods {
		if p.node != "" {
			nodes[p.node] = true
		}
	}
	for _, g := range groups {
		if g.Node != "" {
			nodes[g.Node] = true
		}
	}
	resp.Changes = []Change{}
	if f == nil {
		resp.Changes = append(resp.Changes, a.changes...)
	} else {
		wk, wn := a.workloadOf(f)
		want := map[string]bool{}
		for _, g := range groups {
			if g.Since != "" {
				want[g.Since] = true
			}
		}
		for _, c := range a.changesFor(wk, wn, nodes) {
			want[c.ID] = true
		}
		for _, c := range a.changes {
			if want[c.ID] {
				resp.Changes = append(resp.Changes, c)
			}
		}
	}

	resp.Pods = []PodState{}
	for _, p := range pods {
		if !(p.crashLoop || p.oomKilled) {
			continue
		}
		if len(resp.Pods) >= MaxUnhealthyPods {
			resp.Truncated = true
			break
		}
		_, wk, wn, _ := a.podOwner(p.namespace, p.name)
		resp.Pods = append(resp.Pods, PodState{Namespace: p.namespace, Name: p.name, Node: p.node,
			WorkloadKind: wk, WorkloadName: wn, CrashLoop: p.crashLoop, OOMKilled: p.oomKilled})
	}
	return resp
}

// changesFor is the changes of one workload: its rollouts, rollbacks and
// scaling, the ConfigMaps it references, and the given nodes'.
func (a *aggregate) changesFor(wk, wn string, nodes map[string]bool) []Change {
	var out []Change
	for _, c := range a.changes {
		switch c.Kind {
		case ChangeConfig:
			if wk == "Deployment" && a.deployConfigs[a.namespace+"/"+wn][c.Name] {
				out = append(out, c)
			}
		case ChangeNodeAdded, ChangeNodeNotRead:
			if nodes[c.Name] {
				out = append(out, c)
			}
		default:
			if c.ObjectKind == wk && c.Name == wn {
				out = append(out, c)
			}
		}
	}
	return out
}

// line is the panel line of a workload, read off the aggregate.
func (a *aggregate) line(kind, ns, name string) Line {
	f := &objectFilter{kind: kind, name: name}
	out := Line{Kind: kind, Namespace: ns, Name: name, GeneratedAt: a.built, Complete: a.complete, Note: a.note,
		Sources: map[string]Section{"events": a.sources.Events, "replicasets": a.sources.ReplicaSets,
			"pods": a.sources.Pods, "configmaps": a.sources.ConfigMaps, "nodes": a.sources.Nodes}}
	nodes := map[string]bool{}
	for _, g := range a.groups {
		if !f.matchGroup(g) {
			continue
		}
		if g.typ == "Warning" {
			out.Warnings += g.Count
		}
		if g.Node != "" {
			nodes[g.Node] = true
		}
	}
	for i := range a.pods {
		if a.matchPod(f, &a.pods[i]) && a.pods[i].node != "" {
			nodes[a.pods[i].node] = true
		}
	}
	wk, wn := a.workloadOf(f)
	for _, c := range a.changesFor(wk, wn, nodes) {
		if out.LastChange == nil || c.Time.After(out.LastChange.Time) {
			c := c
			out.LastChange = &c
		}
	}
	return out
}

// causesOf builds the causes from groups. For the whole view the cause totals
// are those counted over every Event (a cut-off group still counts there); for
// an object they are the groups'.
func (a *aggregate) causesOf(groups []*group, whole bool, byID map[string]*Change) []Cause {
	bucket := map[causeKey][]*group{}
	for _, g := range groups {
		bucket[causeKey{g.reason, g.norm}] = append(bucket[causeKey{g.reason, g.norm}], g)
	}
	out := []Cause{}
	finish := func(c Cause, gs []*group) Cause {
		sort.SliceStable(gs, func(i, j int) bool {
			if gs[i].Count != gs[j].Count {
				return gs[i].Count > gs[j].Count
			}
			return gs[i].Namespace+"/"+gs[i].Name < gs[j].Namespace+"/"+gs[j].Name
		})
		c.Groups = make([]ObjectGroup, len(gs))
		tally := map[string]int{}
		for i, g := range gs {
			c.Groups[i] = g.ObjectGroup
			if g.Since != "" {
				tally[g.Since]++
			}
		}
		best, bestN := "", 0
		for id, n := range tally {
			better := n > bestN
			if n == bestN && best != "" && byID[id] != nil && byID[best] != nil {
				better = byID[id].Time.After(byID[best].Time)
			}
			if better {
				best, bestN = id, n
			}
		}
		c.Since = best
		if c.Kinds == nil {
			c.Kinds = []string{}
		}
		if c.Examples == nil {
			c.Examples = []string{}
		}
		return c
	}
	if whole {
		for _, c := range a.causes {
			cc := c.Cause
			cc.Objects = len(c.objects)
			cc.Kinds = make([]string, 0, len(c.kinds))
			for k := range c.kinds {
				cc.Kinds = append(cc.Kinds, k)
			}
			sort.Strings(cc.Kinds)
			out = append(out, finish(cc, bucket[causeKey{c.Reason, c.Message}]))
		}
		return out
	}
	keys := make([]causeKey, 0, len(bucket))
	for k := range bucket {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].reason+keys[i].msg < keys[j].reason+keys[j].msg })
	for _, k := range keys {
		gs := bucket[k]
		c := Cause{Reason: k.reason, Message: k.msg, Type: gs[0].typ, FirstSeen: gs[0].FirstSeen, LastSeen: gs[0].LastSeen}
		objs, kinds := map[string]bool{}, map[string]bool{}
		for _, g := range gs {
			c.Count += g.Count
			if g.FirstSeen.Before(c.FirstSeen) {
				c.FirstSeen = g.FirstSeen
			}
			if g.LastSeen.After(c.LastSeen) {
				c.LastSeen = g.LastSeen
			}
			objs[g.Kind+"/"+g.Namespace+"/"+g.Name] = true
			kinds[g.Kind] = true
			if len(c.Examples) < MaxExamples {
				c.Examples = append(c.Examples, g.Message)
			}
		}
		c.Objects = len(objs)
		for k := range kinds {
			c.Kinds = append(c.Kinds, k)
		}
		sort.Strings(c.Kinds)
		out = append(out, finish(c, gs))
	}
	sort.SliceStable(out, func(i, j int) bool {
		x, y := out[i], out[j]
		if (x.Type == "Warning") != (y.Type == "Warning") {
			return x.Type == "Warning"
		}
		return x.Count > y.Count
	})
	return out
}

// tiles are the page's five figures. Whole, Warnings, FailedScheduling and
// ObjectsTouched are totals over every Event read; reduced to an object they
// are summed from its groups.
func (a *aggregate) tiles(groups []*group, pods []*podInfo, whole bool) Tiles {
	ev, pd := a.sources.Events.State, a.sources.Pods.State
	var t Tiles
	t.Warnings.State, t.FailedScheduling.State, t.ObjectsTouched.State = ev, ev, ev
	t.CrashLoop.State, t.OOMKilled.State = pd, pd
	touched := map[string]bool{}
	backoff := map[string]bool{}
	for _, g := range groups {
		if g.typ == "Warning" {
			t.Warnings.Count += g.Count
			touched[g.Kind+"/"+g.Namespace+"/"+g.Name] = true
		}
		if g.reason == "FailedScheduling" {
			t.FailedScheduling.Count += g.Count
		}
		if g.reason == "BackOff" && g.Kind == "Pod" {
			backoff[g.Namespace+"/"+g.Name] = true
		}
	}
	t.ObjectsTouched.Count = len(touched)
	if whole {
		t.Warnings.Count, t.FailedScheduling.Count = a.warnings, a.failedScheduling
		t.ObjectsTouched.Count = len(a.touched)
		backoff = map[string]bool{}
		for k := range a.backoffPods {
			backoff[k] = true
		}
	}
	for _, p := range pods {
		if p.crashLoop {
			t.CrashLoop.Count++
		}
		if p.oomKilled {
			t.OOMKilled.Count++
		}
	}
	if pd != StateRead {
		// The pods could not be read: CrashLoop falls back to the pods whose
		// containers' restarts were Events (BackOff), and says it was not read.
		t.CrashLoop.Count = len(backoff)
	}
	return t
}

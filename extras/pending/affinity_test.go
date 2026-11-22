package pending

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// topo is a node of a zone, labeled as a cluster labels its nodes.
func topo(i int, name, zone string, extra map[string]string) *Node {
	l := map[string]string{topologyHostname: name, topologyZone: zone}
	for k, v := range extra {
		l[k] = v
	}
	return &Node{Index: i, Name: name, Labels: l, Allocatable: Amounts{}, Requested: Amounts{}}
}

// fleet3 is two nodes in zone a, one in b, one in c.
func fleet3() []*Node {
	return []*Node{topo(0, "a1", "a", nil), topo(1, "a2", "a", nil), topo(2, "b1", "b", nil), topo(3, "c1", "c", nil)}
}

func sel(kv ...string) *metav1.LabelSelector {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return &metav1.LabelSelector{MatchLabels: m}
}

func at(ns, name, node string, lbl map[string]string) *placedPod {
	return &placedPod{Namespace: ns, Name: name, Node: node, Labels: lbl}
}

func placement(pods ...*placedPod) *Placement {
	pl := &Placement{Kept: true, Whole: true, Pods: pods}
	for _, p := range pods {
		if len(p.Anti) > 0 {
			pl.Anti = append(pl.Anti, p)
		}
	}
	return pl
}

func antiTerm(t *testing.T, declaredIn string, own map[string]string, term corev1.PodAffinityTerm) podTerm {
	t.Helper()
	out, err := newTerm(term, declaredIn, own)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func pod(ns string, lbl map[string]string, spec corev1.PodSpec) Rules {
	return Rules{Namespace: ns, Labels: lbl, Spec: spec}
}

func withAffinity(aff, anti []corev1.PodAffinityTerm) corev1.PodSpec {
	a := &corev1.Affinity{}
	if aff != nil {
		a.PodAffinity = &corev1.PodAffinity{RequiredDuringSchedulingIgnoredDuringExecution: aff}
	}
	if anti != nil {
		a.PodAntiAffinity = &corev1.PodAntiAffinity{RequiredDuringSchedulingIgnoredDuringExecution: anti}
	}
	return corev1.PodSpec{Affinity: a}
}

// verdicts is each node's pass, by name.
func verdicts(nodes []*Node, r Rule) map[string]bool {
	out := map[string]bool{}
	for i, n := range nodes {
		out[n.Name] = r.OK[i]
	}
	return out
}

func wantPass(t *testing.T, what string, nodes []*Node, r Rule, pass ...string) {
	t.Helper()
	if !r.Computed {
		t.Fatalf("%s: not computed: %s", what, r.Why)
	}
	want := map[string]bool{}
	for _, n := range pass {
		want[n] = true
	}
	for _, n := range nodes {
		if r.OK[n.Index] != want[n.Name] {
			t.Errorf("%s: node %s passes = %v, want %v (%v)", what, n.Name, r.OK[n.Index], want[n.Name], verdicts(nodes, r))
		}
	}
}

func TestRequiredAffinityNeedsAMatchingPodInTheZone(t *testing.T) {
	nodes := fleet3()
	pl := placement(at("shop", "db-0", "b1", map[string]string{"app": "db"}), at("shop", "web-0", "a1", map[string]string{"app": "web"}))
	got := pl.Compute(pod("shop", map[string]string{"app": "api"}, withAffinity([]corev1.PodAffinityTerm{{LabelSelector: sel("app", "db"), TopologyKey: topologyZone}}, nil)), nodes, true, nil, true)
	wantPass(t, "affinity", nodes, got.Affinity, "b1")
	if d := got.Affinity.Detail[0]; !strings.Contains(d, "no pod matching app=db on topology.kubernetes.io/zone=a") {
		t.Errorf("detail %q", d)
	}
}

func TestRequiredAntiAffinityNamesThePodThatBlocks(t *testing.T) {
	nodes := fleet3()
	pl := placement(at("shop", "web-0", "a1", map[string]string{"app": "web"}), at("shop", "web-1", "c1", map[string]string{"app": "web"}))
	spec := withAffinity(nil, []corev1.PodAffinityTerm{{LabelSelector: sel("app", "web"), TopologyKey: topologyHostname}})
	got := pl.Compute(pod("shop", map[string]string{"app": "web"}, spec), nodes, true, nil, true)
	wantPass(t, "hostname anti", nodes, got.Affinity, "a2", "b1")
	if d := got.Affinity.Detail[0]; d != "pod shop/web-0 matches its anti-affinity on kubernetes.io/hostname=a1" {
		t.Errorf("detail %q", d)
	}
	// In the zone, the whole zone of a pod that matches is out.
	spec = withAffinity(nil, []corev1.PodAffinityTerm{{LabelSelector: sel("app", "web"), TopologyKey: topologyZone}})
	got = pl.Compute(pod("shop", map[string]string{"app": "web"}, spec), nodes, true, nil, false)
	wantPass(t, "zone anti", nodes, got.Affinity, "b1")
}

func TestAnExistingPodsAntiAffinityCanExcludeThePod(t *testing.T) {
	nodes := fleet3()
	db := at("shop", "db-0", "b1", map[string]string{"app": "db"})
	db.Anti = []podTerm{antiTerm(t, "shop", db.Labels, corev1.PodAffinityTerm{LabelSelector: sel("app", "web"), TopologyKey: topologyZone})}
	pl := placement(db)
	// The pod declares nothing of its own and is still refused in db-0's zone.
	got := pl.Compute(pod("shop", map[string]string{"app": "web"}, corev1.PodSpec{}), nodes, true, nil, true)
	wantPass(t, "existing anti", nodes, got.Affinity, "a1", "a2", "c1")
	if d := got.Affinity.Detail[2]; d != "pod shop/db-0 has an anti-affinity that excludes it on topology.kubernetes.io/zone=b" {
		t.Errorf("detail %q", d)
	}
	// Its rule is about another namespace unless it says otherwise.
	got = pl.Compute(pod("other", map[string]string{"app": "web"}, corev1.PodSpec{}), nodes, true, nil, false)
	wantPass(t, "other namespace", nodes, got.Affinity, "a1", "a2", "b1", "c1")
}

func TestTermNamespaces(t *testing.T) {
	nodes := fleet3()
	web := at("ops", "web-0", "a1", map[string]string{"app": "web"})
	pl := placement(web)
	run := func(term corev1.PodAffinityTerm, nsl nsLabels) Rule {
		term.LabelSelector, term.TopologyKey = sel("app", "web"), topologyHostname
		return pl.Compute(pod("shop", nil, withAffinity(nil, []corev1.PodAffinityTerm{term})), nodes, true, nsl, false).Affinity
	}
	// Its own namespace only: the pod in ops does not count.
	wantPass(t, "own namespace", nodes, run(corev1.PodAffinityTerm{}, nil), "a1", "a2", "b1", "c1")
	// A named namespace.
	wantPass(t, "named", nodes, run(corev1.PodAffinityTerm{Namespaces: []string{"ops"}}, nil), "a2", "b1", "c1")
	// A selector on the namespaces' labels, which have to be read.
	byLabel := corev1.PodAffinityTerm{NamespaceSelector: sel("team", "platform")}
	wantPass(t, "selected", nodes, run(byLabel, nsLabels{"ops": {"team": "platform"}, "shop": {}}), "a2", "b1", "c1")
	wantPass(t, "not selected", nodes, run(byLabel, nsLabels{"ops": {"team": "other"}}), "a1", "a2", "b1", "c1")
	if r := run(byLabel, nil); r.Computed || !strings.Contains(r.Why, "namespaces") {
		t.Errorf("unread namespaces: %+v", r)
	}
	// An empty selector is every namespace, and needs no labels.
	wantPass(t, "every namespace", nodes, run(corev1.PodAffinityTerm{NamespaceSelector: &metav1.LabelSelector{}}, nil), "a2", "b1", "c1")
}

func TestMatchLabelKeysNarrowTheSelectorToThePodsRevision(t *testing.T) {
	nodes := fleet3()
	pl := placement(
		at("shop", "web-old", "a1", map[string]string{"app": "web", "rev": "1"}),
		at("shop", "web-new", "b1", map[string]string{"app": "web", "rev": "2"}),
	)
	term := corev1.PodAffinityTerm{LabelSelector: sel("app", "web"), TopologyKey: topologyHostname, MatchLabelKeys: []string{"rev"}}
	got := pl.Compute(pod("shop", map[string]string{"app": "web", "rev": "2"}, withAffinity(nil, []corev1.PodAffinityTerm{term})), nodes, true, nil, false)
	wantPass(t, "matchLabelKeys", nodes, got.Affinity, "a1", "a2", "c1")
	mismatch := corev1.PodAffinityTerm{LabelSelector: sel("app", "web"), TopologyKey: topologyHostname, MismatchLabelKeys: []string{"rev"}}
	got = pl.Compute(pod("shop", map[string]string{"app": "web", "rev": "2"}, withAffinity(nil, []corev1.PodAffinityTerm{mismatch})), nodes, true, nil, false)
	wantPass(t, "mismatchLabelKeys", nodes, got.Affinity, "a2", "b1", "c1")
}

func TestAPodMayStartASeriesThatHasAffinityToItself(t *testing.T) {
	nodes := fleet3()
	term := []corev1.PodAffinityTerm{{LabelSelector: sel("app", "ring"), TopologyKey: topologyZone}}
	self := pod("shop", map[string]string{"app": "ring"}, withAffinity(term, nil))
	// No pod of the series yet, and the pod matches its own term: anywhere.
	wantPass(t, "first replica", nodes, placement().Compute(self, nodes, true, nil, false).Affinity, "a1", "a2", "b1", "c1")
	// One exists in zone c: the next follows it.
	wantPass(t, "second replica", nodes, placement(at("shop", "ring-0", "c1", map[string]string{"app": "ring"})).Compute(self, nodes, true, nil, false).Affinity, "c1")
	// Not matching its own term, it has no such right.
	other := pod("shop", map[string]string{"app": "x"}, withAffinity(term, nil))
	wantPass(t, "not itself", nodes, placement().Compute(other, nodes, true, nil, false).Affinity)
	// A node without the topology label never passes.
	bare := append(fleet3(), &Node{Index: 4, Name: "bare", Labels: map[string]string{}})
	r := placement().Compute(self, bare, true, nil, true).Affinity
	if r.OK[4] || r.Detail[4] != "the node has no label topology.kubernetes.io/zone" {
		t.Errorf("bare: %v %q", r.OK[4], r.Detail[4])
	}
}

func spreadSpec(maxSkew int32, key string, selector *metav1.LabelSelector, mut ...func(*corev1.TopologySpreadConstraint)) corev1.PodSpec {
	c := corev1.TopologySpreadConstraint{MaxSkew: maxSkew, TopologyKey: key, WhenUnsatisfiable: corev1.DoNotSchedule, LabelSelector: selector}
	for _, m := range mut {
		m(&c)
	}
	return corev1.PodSpec{TopologySpreadConstraints: []corev1.TopologySpreadConstraint{c}}
}

func TestTopologySpreadRefusesADomainOverMaxSkew(t *testing.T) {
	nodes := fleet3()
	web := map[string]string{"app": "web"}
	pl := placement(at("shop", "w0", "a1", web), at("shop", "w1", "a2", web), at("shop", "w2", "b1", web),
		// Another namespace's pods are not this pod's to count.
		at("other", "w3", "c1", web))
	got := pl.Compute(pod("shop", web, spreadSpec(1, topologyZone, sel("app", "web"))), nodes, true, nil, true)
	wantPass(t, "zone spread", nodes, got.Spread, "c1")
	if d := got.Spread.Detail[0]; d != "topology.kubernetes.io/zone=a would hold 3 pods against 0 in the emptiest domain, over maxSkew 1" {
		t.Errorf("detail %q", d)
	}
	// A pod that the selector does not select adds nothing of its own, but the
	// domains it joins are still held to the skew: zone a is two over zone c.
	got = pl.Compute(pod("shop", map[string]string{"app": "job"}, spreadSpec(1, topologyZone, sel("app", "web"))), nodes, true, nil, false)
	wantPass(t, "not selected", nodes, got.Spread, "b1", "c1")
	// A looser skew.
	got = pl.Compute(pod("shop", web, spreadSpec(2, topologyZone, sel("app", "web"))), nodes, true, nil, false)
	wantPass(t, "maxSkew 2", nodes, got.Spread, "b1", "c1")
}

func TestMinDomainsRaisesTheFloorToZero(t *testing.T) {
	nodes := fleet3()
	web := map[string]string{"app": "web"}
	pl := placement(at("shop", "w0", "a1", web), at("shop", "w1", "b1", web), at("shop", "w2", "c1", web))
	four := int32(4)
	flat := pl.Compute(pod("shop", web, spreadSpec(1, topologyZone, sel("app", "web"))), nodes, true, nil, false)
	wantPass(t, "even", nodes, flat.Spread, "a1", "a2", "b1", "c1")
	// Four domains wanted, three exist: the least loaded counts as empty.
	short := pl.Compute(pod("shop", web, spreadSpec(1, topologyZone, sel("app", "web"), func(c *corev1.TopologySpreadConstraint) { c.MinDomains = &four })), nodes, true, nil, false)
	wantPass(t, "minDomains", nodes, short.Spread)
}

func TestSpreadCountsOnlyTheNodesThePodMayUse(t *testing.T) {
	ssd := map[string]string{"disk": "ssd"}
	nodes := []*Node{topo(0, "a1", "a", ssd), topo(1, "b1", "b", ssd), topo(2, "c1", "c", nil)}
	web := map[string]string{"app": "web"}
	pl := placement(at("shop", "w0", "a1", web), at("shop", "w1", "b1", web))
	only := func(spec corev1.PodSpec) corev1.PodSpec { spec.NodeSelector = ssd; return spec }
	// Honoring the node selector (the default), zone c has no node the pod
	// could use, so it is not a domain and the floor is 1.
	got := pl.Compute(pod("shop", web, only(spreadSpec(1, topologyZone, sel("app", "web")))), nodes, true, nil, false)
	wantPass(t, "honour", nodes, got.Spread, "a1", "b1", "c1")
	ignore := corev1.NodeInclusionPolicyIgnore
	got = pl.Compute(pod("shop", web, only(spreadSpec(1, topologyZone, sel("app", "web"), func(c *corev1.TopologySpreadConstraint) { c.NodeAffinityPolicy = &ignore }))), nodes, true, nil, false)
	wantPass(t, "ignore", nodes, got.Spread, "c1")
	// A node without the key never takes the pod.
	bare := []*Node{topo(0, "a1", "a", nil), {Index: 1, Name: "bare", Labels: map[string]string{}, Allocatable: Amounts{}, Requested: Amounts{}}}
	r := placement().Compute(pod("shop", web, spreadSpec(1, topologyZone, sel("app", "web"))), bare, true, nil, true).Spread
	if r.OK[1] || !strings.Contains(r.Detail[1], "no label") || !r.OK[0] {
		t.Errorf("bare: %v %q", r.OK, r.Detail)
	}
}

func TestTerminatingPodsAreNotCountedBySpreadButAreByAffinity(t *testing.T) {
	nodes := fleet3()
	web := map[string]string{"app": "web"}
	gone := at("shop", "w0", "a1", web)
	gone.Terminating = true
	pl := placement(gone)
	s := pl.Compute(pod("shop", web, spreadSpec(1, topologyZone, sel("app", "web"))), nodes, true, nil, false)
	wantPass(t, "spread", nodes, s.Spread, "a1", "a2", "b1", "c1")
	a := pl.Compute(pod("shop", web, withAffinity(nil, []corev1.PodAffinityTerm{{LabelSelector: sel("app", "web"), TopologyKey: topologyHostname}})), nodes, true, nil, false)
	wantPass(t, "anti", nodes, a.Affinity, "a2", "b1", "c1")
}

// What is not handled stays gray, with the reason, never a pass.
func TestWhatIsNotComputedSaysWhy(t *testing.T) {
	nodes := fleet3()
	web := map[string]string{"app": "web"}
	pl := placement(at("shop", "w0", "a1", web))
	for name, tc := range map[string]struct {
		pl   *Placement
		spec corev1.PodSpec
		part func(Placed) Rule
		why  string
	}{
		"another key, affinity":  {pl, withAffinity([]corev1.PodAffinityTerm{{LabelSelector: sel("app", "web"), TopologyKey: "rack"}}, nil), func(p Placed) Rule { return p.Affinity }, "rack is not computed"},
		"another key, spread":    {pl, spreadSpec(1, "rack", sel("app", "web")), func(p Placed) Rule { return p.Spread }, "rack is not computed"},
		"only a soft constraint": {pl, spreadSpec(1, topologyZone, sel("app", "web"), func(c *corev1.TopologySpreadConstraint) { c.WhenUnsatisfiable = corev1.ScheduleAnyway }), func(p Placed) Rule { return p.Spread }, "defaults are not read"},
		"pods not read whole":    {&Placement{Kept: true, Pods: pl.Pods}, withAffinity(nil, []corev1.PodAffinityTerm{{LabelSelector: sel("app", "web"), TopologyKey: topologyZone}}), func(p Placed) Rule { return p.Affinity }, "not read whole"},
		"pods not kept":          {&Placement{Whole: true}, spreadSpec(1, topologyZone, sel("app", "web")), func(p Placed) Rule { return p.Spread }, "not kept"},
	} {
		r := tc.part(tc.pl.Compute(pod("shop", web, tc.spec), nodes, true, nil, false))
		if r.Computed || r.OK != nil || !strings.Contains(r.Why, tc.why) {
			t.Errorf("%s: %+v", name, r)
		}
	}
	// The nodes cut short: the same.
	r := pl.Compute(pod("shop", web, spreadSpec(1, topologyZone, sel("app", "web"))), nodes, false, nil, false).Spread
	if r.Computed || !strings.Contains(r.Why, "nodes were not read whole") {
		t.Errorf("nodes: %+v", r)
	}
	// An existing pod's anti-affinity on a key not computed, that would match.
	odd := at("shop", "odd", "a1", map[string]string{"app": "odd"})
	odd.Anti = []podTerm{antiTerm(t, "shop", nil, corev1.PodAffinityTerm{LabelSelector: sel("app", "web"), TopologyKey: "rack"})}
	if r := placement(odd).Compute(pod("shop", web, corev1.PodSpec{}), nodes, true, nil, false).Affinity; r.Computed || !strings.Contains(r.Why, "rack") {
		t.Errorf("existing: %+v", r)
	}
	// ...and one that cannot match is no reason to give up.
	if r := placement(odd).Compute(pod("shop", map[string]string{"app": "db"}, corev1.PodSpec{}), nodes, true, nil, false).Affinity; !r.Computed {
		t.Errorf("unrelated: %+v", r)
	}
}

func TestPlacementKeyIsTheSameForReplicasAndDiffersByLabelsAndRules(t *testing.T) {
	term := []corev1.PodAffinityTerm{{LabelSelector: sel("app", "web"), TopologyKey: topologyZone}}
	a := placementKey(pod("shop", map[string]string{"app": "web", "i": "1"}, withAffinity(nil, term)))
	b := placementKey(pod("shop", map[string]string{"i": "1", "app": "web"}, withAffinity(nil, term)))
	if a != b {
		t.Error("the replicas of a workload must share their answer")
	}
	for name, other := range map[string]string{
		"labels":    placementKey(pod("shop", map[string]string{"app": "web", "i": "2"}, withAffinity(nil, term))),
		"namespace": placementKey(pod("ops", map[string]string{"app": "web", "i": "1"}, withAffinity(nil, term))),
		"rules":     placementKey(pod("shop", map[string]string{"app": "web", "i": "1"}, withAffinity(term, nil))),
	} {
		if other == a {
			t.Errorf("%s must change the key", name)
		}
	}
}

// spreadCluster is three nodes in two zones, web pods placed on them, and a
// pending web pod that must not share a zone with another and must spread.
func spreadCluster(t *testing.T) *fakeHost {
	h := newHost(t)
	h.add("nodes",
		node("n-a1", "8", "32Gi", map[string]string{topologyZone: "a", topologyHostname: "n-a1"}),
		node("n-a2", "8", "32Gi", map[string]string{topologyZone: "a", topologyHostname: "n-a2"}),
		node("n-b1", "8", "32Gi", map[string]string{topologyZone: "b", topologyHostname: "n-b1"}),
	)
	web := map[string]string{"app": "web"}
	for _, p := range []struct{ name, node string }{{"web-0", "n-a1"}, {"web-1", "n-a2"}} {
		pod := placed("shop", p.name, p.node, container("100m", "64Mi"))
		pod.Labels = web
		h.add("pods", pod)
	}
	// db-0 forbids web pods in its zone.
	db := placed("shop", "db-0", "n-b1", container("100m", "64Mi"))
	db.Labels = map[string]string{"app": "db"}
	db.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
		{LabelSelector: sel("app", "web"), TopologyKey: topologyZone},
	}}}
	h.add("pods", db)
	p := unschedulable("shop", "web-2", "0/3 nodes are available: 1 node(s) didn't satisfy existing pods anti-affinity rules, 2 node(s) didn't match pod topology spread constraints.", 5*time.Minute, container("100m", "64Mi"))
	p.Labels = web
	p.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{MaxSkew: 1, TopologyKey: topologyZone, WhenUnsatisfiable: corev1.DoNotSchedule, LabelSelector: sel("app", "web")}}
	h.add("pods", p)
	return h
}

func TestThePodViewComputesAffinityAndSpreadFromThePlacedPods(t *testing.T) {
	h := spreadCluster(t)
	v := decode[PodView](t, serve(t, h, "/pod/shop/web-2"))
	var aff, spr *Column
	for i := range v.Columns {
		switch v.Columns[i].Key {
		case PredPodAffinity:
			aff = &v.Columns[i]
		case PredSpread:
			spr = &v.Columns[i]
		}
	}
	if aff == nil || spr == nil || !aff.Computed || !spr.Computed || aff.Scheduler != 1 || spr.Scheduler != 2 {
		t.Fatalf("columns %+v", v.Columns)
	}
	cells := map[string]map[string]Cell{}
	for _, f := range v.Fits {
		cells[f.Node] = map[string]Cell{}
		for _, c := range f.Cells {
			cells[f.Node][c.Key] = c
		}
	}
	// Zone b holds db-0, whose anti-affinity excludes the pod; zone a holds two
	// web pods against none in b, so spread refuses it too.
	if c := cells["n-b1"][PredPodAffinity]; c.OK == nil || *c.OK || !strings.Contains(c.Detail, "pod shop/db-0 has an anti-affinity") {
		t.Errorf("n-b1 affinity %+v", c)
	}
	if c := cells["n-a1"][PredSpread]; c.OK == nil || *c.OK || !strings.Contains(c.Detail, "would hold 3 pods against 0") {
		t.Errorf("n-a1 spread %+v", c)
	}
	if c := cells["n-a1"][PredPodAffinity]; c.OK == nil || !*c.OK {
		t.Errorf("n-a1 affinity %+v", c)
	}
	if got := strings.Join(v.WouldTake, "\n"); !strings.Contains(got, "would hold 3 pods against 0") || strings.Contains(got, "not computed here") {
		t.Errorf("would take: %v", v.WouldTake)
	}
}

func TestAPartialReadOfThePlacedPodsLeavesTheCellsGrey(t *testing.T) {
	h := spreadCluster(t)
	h.allowed = []string{"shop"} // narrowed: an all-namespaces list comes back incomplete
	v := decode[PodView](t, serve(t, h, "/pod/shop/web-2"))
	for _, c := range v.Columns {
		if (c.Key == PredPodAffinity || c.Key == PredSpread) && (c.Computed || !strings.Contains(c.Why, "not read whole")) {
			t.Errorf("%+v", c)
		}
	}
	for _, f := range v.Fits {
		for _, c := range f.Cells {
			if (c.Key == PredPodAffinity || c.Key == PredSpread) && c.OK != nil {
				t.Errorf("%s %s drawn as an answer: %+v", f.Node, c.Key, c)
			}
		}
	}
}

func TestThePageRanksTheClosestNodeWithThePlacementCells(t *testing.T) {
	h := spreadCluster(t)
	a := decode[PageAnswer](t, serve(t, h, "/"))
	if len(a.Rows) != 1 || a.Rows[0].Closest == nil {
		t.Fatalf("%+v", a.Rows)
	}
	// Every node fails something now: a1 and a2 fail spread, b1 fails both.
	if c := a.Rows[0].Closest; c.Failing != 1 || c.Node == "n-b1" {
		t.Errorf("closest %+v", c)
	}
	// The labels of every pod are read only because a pending pod needs them:
	// with no rule of its own, the placed pods' labels are not kept.
	h2 := newHost(t)
	h2.add("nodes", node("n1", "8", "32Gi", map[string]string{topologyZone: "a"}))
	h2.add("pods", placed("shop", "x", "n1", container("100m", "64Mi")), unschedulable("shop", "p", msgMemory, time.Minute, container("100m", "64Mi")))
	fleet, err := readFleet(httptest.NewRequest(http.MethodGet, "/", nil), h2, "kind", false)
	if err != nil || len(fleet.place.Pods) != 0 || fleet.place.Kept {
		t.Errorf("kept %d pods, %v", len(fleet.place.Pods), err)
	}
}

func TestNamespaceLabelsAreReadOnlyWhenASelectorNeedsThem(t *testing.T) {
	h := spreadCluster(t)
	h.add("namespaces", &corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: "shop", Labels: map[string]string{"team": "a"}}})
	reads := func() int {
		n := 0
		for _, q := range h.queries {
			if q.GVR.Resource == "namespaces" {
				n++
			}
		}
		return n
	}
	serve(t, h, "/pod/shop/web-2")
	if reads() != 0 {
		t.Errorf("a pod with no namespaceSelector read the namespaces %d times", reads())
	}
	sp := unschedulable("shop", "sel-0", "0/3 nodes are available: 3 node(s) didn't match pod anti-affinity rules.", time.Minute, container("100m", "64Mi"))
	sp.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
		{LabelSelector: sel("app", "web"), TopologyKey: topologyHostname, NamespaceSelector: sel("team", "a")},
	}}}
	h.add("pods", sp)
	v := decode[PodView](t, serve(t, h, "/pod/shop/sel-0"))
	if reads() != 1 {
		t.Errorf("namespace reads: %d", reads())
	}
	// shop is team a, so its web pods on n-a1 and n-a2 repel the pod.
	for _, f := range v.Fits {
		for _, c := range f.Cells {
			if c.Key == PredPodAffinity && (c.OK == nil || *c.OK == (f.Node != "n-b1")) {
				t.Errorf("%s: %+v", f.Node, c)
			}
		}
	}
	// Namespaces that could not be read leave the cell gray, with the reason.
	h.denied["namespaces"] = true
	v = decode[PodView](t, serve(t, h, "/pod/shop/sel-0"))
	for _, c := range v.Columns {
		if c.Key == PredPodAffinity && (c.Computed || !strings.Contains(c.Why, "namespaces")) {
			t.Errorf("%+v", c)
		}
	}
}

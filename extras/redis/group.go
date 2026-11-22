package redis

import (
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/svcgroup"
)

// # One entry per Redis deployment
//
// svcgroup folds Services onto the pods behind them, so a replicated Redis is
// still several rows (the primary's Service, the replicas', the headless one).
// The inventory folds once more: a deployment is the backends that belong to
// one installation, each kept as a member with its own pods, doors, role and
// probe, so a wrong fold misfiles a server but never hides one.
//
// The evidence, most trusted first; each fold says which it used:
//
//  1. workload: the members' pods are owned by one StatefulSet or Deployment.
//  2. pods: the members' pod sets overlap (a headless Service onto all).
//  3. release: the Services or pods share `app.kubernetes.io/instance` with
//     `app.kubernetes.io/name` (or `helm.sh/chart`'s chart), or the
//     redis-operator's `redisfailovers.databases.spotahome.com/name`.
//  4. name: the Service names share a stem once chart and operator affixes
//     (`-master`, `-headless`, `rfr-` ...) are removed. A hint, drawn as one.
//
// Never across namespaces. The weak edges (3 and 4) never join two primaries
// with pods of their own, and a name never joins what labels put apart. The
// facts (1 and 2) are not guarded: one workload or one shared pod is one
// installation by construction.
//
// Not caught: two Redises of one release with the same instance and name labels
// and no role label fold on `release`, each still its own row.

// The roles a member or a door is given.
const (
	rolePrimary  = "primary"
	roleReplica  = "replica"
	roleHeadless = "headless"
	roleSentinel = "sentinel"
)

// Where a role was read from, from the most trusted down. `info` is the
// server's own answer, set by the page when a member is probed, never here.
const (
	fromLabels = "labels"
	fromPods   = "pods"
	fromSpec   = "spec"
	fromName   = "name"
	fromPort   = "port"
)

// The evidence a fold was made on.
const (
	byWorkload = "workload"
	byPods     = "pods"
	byRelease  = "release"
	byName     = "name"
)

// deployment is one installation of a Redis or a Valkey: every backend that
// belongs to it.
type deployment struct {
	Namespace string
	// Name is what the entry is called: the stem most of its Services share.
	Name string
	// Evidence is what the members were folded on, in trust order. Empty for
	// a deployment of one member.
	Evidence []string
	// Release is the Helm release (or the operator's resource) the labels
	// name, and Chart the chart, when they say.
	Release string
	Chart   string
	// Workloads are the owners of the pods behind every member, sorted.
	Workloads []string
	Members   []member
}

// member is one backend - one server, or one set of pods - inside a
// deployment, with what it is to that deployment.
type member struct {
	Backend svcgroup.Backend
	// Sentinel marks a member reached on a Sentinel port only: drawn so the
	// deployment is whole, never offered to be opened: it holds no keys.
	Sentinel bool
	// Role is the member's: primary, replica, headless (every pod, no cluster
	// IP), sentinel, or empty when nothing said. RoleFrom says what said it.
	Role     string
	RoleFrom string
	// Doors are parallel to Backend.Services: the role each Service gives.
	Doors     []doorRole
	Workloads []string
}

type doorRole struct {
	Role string
	From string
}

// workloadRef is the owner of a pod, as a `Kind/name`.
//
// A Deployment's name is derived from its ReplicaSet's `<deployment>-<hash>`
// and the pod's `pod-template-hash` label, saving a ReplicaSet listing; a
// ReplicaSet whose name does not end in that hash stays a ReplicaSet.
//
// Only the controller counts. A static pod's controller is its Node, which is
// no workload: otherwise every static pod on a node would fold together.
func workloadRef(pod *corev1.Pod) string {
	for _, ref := range pod.OwnerReferences {
		if ref.Controller == nil || !*ref.Controller || ref.Kind == "Node" {
			continue
		}
		if ref.Kind == "ReplicaSet" {
			if hash := pod.Labels["pod-template-hash"]; hash != "" {
				if name, ok := strings.CutSuffix(ref.Name, "-"+hash); ok && name != "" {
					return "Deployment/" + name
				}
			}
		}
		return ref.Kind + "/" + ref.Name
	}
	return ""
}

// roleWord reads a role from a label value, in any of the spellings charts
// and operators use.
func roleWord(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "master", "primary", "leader":
		return rolePrimary
	case "replica", "replicas", "slave", "slaves", "follower", "secondary":
		return roleReplica
	case "sentinel":
		return roleSentinel
	}
	return ""
}

// The labels that carry a role: bitnami's component, the older `role`, and
// the redis-operator's `redisfailovers-role` on its pods.
var roleLabels = []string{"app.kubernetes.io/component", "component", "role", "redisfailovers-role"}

func roleFromLabels(labels map[string]string) string {
	for _, name := range roleLabels {
		if role := roleWord(labels[name]); role != "" {
			return role
		}
	}
	return ""
}

// The prefixes and suffixes charts and operators put round one installation's
// name, and the role each one says. The redis-operator names its Services
// `rfr-` (the Redis pods), `rfrm-` and `rfrs-` (primary and replicas, in its
// newer releases) and `rfs-` (the Sentinels).
var namePrefixes = []struct{ prefix, role string }{
	{"rfrm-", rolePrimary}, {"rfrs-", roleReplica}, {"rfs-", roleSentinel}, {"rfr-", ""},
}

var nameSuffixes = []struct{ suffix, role string }{
	{"-headless", roleHeadless},
	{"-master", rolePrimary}, {"-primary", rolePrimary}, {"-leader", rolePrimary}, {"-rw", rolePrimary},
	{"-replicas", roleReplica}, {"-replica", roleReplica}, {"-slave", roleReplica}, {"-slaves", roleReplica},
	{"-follower", roleReplica}, {"-ro", roleReplica},
	{"-sentinel", roleSentinel},
	{"-node", ""}, {"-nodes", ""}, {"-haproxy", ""}, {"-announce", ""},
}

// stem is a Service's name with those affixes taken off, and the role the
// outermost of them said. It never strips a name to nothing.
func stem(name string) (string, string) {
	out, role := name, ""
	for _, p := range namePrefixes {
		if rest, ok := strings.CutPrefix(out, p.prefix); ok && rest != "" {
			out, role = rest, p.role
			break
		}
	}
	for changed := true; changed; {
		changed = false
		for _, s := range nameSuffixes {
			if rest, ok := strings.CutSuffix(out, s.suffix); ok && rest != "" {
				if role == "" {
					role = s.role
				}
				out, changed = rest, true
				break
			}
		}
	}
	return out, role
}

// releaseKeys are what the labels say this belongs to, each a key two members
// must share to be folded on labels.
//
// `app.kubernetes.io/instance` alone is not one, since a release may hold two
// Redis subcharts; with the chart's name (`app.kubernetes.io/name`, or
// `helm.sh/chart` without its version) it is.
func releaseKeys(labels map[string]string) (keys []string, release, chart string) {
	if name := labels["redisfailovers.databases.spotahome.com/name"]; name != "" {
		keys = append(keys, "redisfailover/"+name)
		release = name
	}
	instance := firstOf(labels, "app.kubernetes.io/instance", "release")
	chart = firstOf(labels, "helm.sh/chart", "chart")
	app := firstOf(labels, "app.kubernetes.io/name", "app")
	if app == "" {
		app = chartName(chart)
	}
	if instance != "" && app != "" {
		keys = append(keys, "release/"+instance+"/"+app)
		if release == "" {
			release = instance
		}
	}
	return keys, release, chart
}

func firstOf(labels map[string]string, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(labels[name]); value != "" {
			return value
		}
	}
	return ""
}

// chartName is `redis` from `redis-19.6.4`: the chart without its version.
func chartName(chart string) string {
	if i := strings.LastIndex(chart, "-"); i > 0 && i+1 < len(chart) && chart[i+1] >= '0' && chart[i+1] <= '9' {
		return chart[:i]
	}
	return chart
}

// groupInput is what the fold reads: everything discovery and the pod listing
// found, already read. assemble reads nothing itself.
type groupInput struct {
	// services by "namespace/name".
	services map[string]*corev1.Service
	// pods by "namespace/name"; a namespace whose pods were not read has none,
	// and the fold does without workloads and pod labels there.
	pods map[string]*corev1.Pod
	// redis and sentinels are svcgroup's backends for the Redis ports and the
	// Sentinel ports, folded separately so a Sentinel port never becomes a
	// door onto a Redis.
	redis     []svcgroup.Backend
	sentinels []svcgroup.Backend
}

// node is one member while the fold runs.
type node struct {
	m         member
	keys      []string // release keys
	release   string
	chart     string
	stems     []string
	pods      map[string]bool
	workloads map[string]bool
}

// assemble folds the backends into deployments, per namespace, in namespace
// order, each deployment's members ordered primary, replicas, headless,
// Sentinels, then by Service. A Sentinel member that joins no Redis is
// dropped: it holds no keys, and on its own it is nothing this page opens.
func assemble(in groupInput) []deployment {
	byNamespace := map[string][]*node{}
	var namespaces []string
	add := func(b svcgroup.Backend, sentinel bool) {
		if sentinel {
			// svcgroup keys a backend by its pods, and a Sentinel often runs in
			// the Redis pods, so the two members would share the key the page
			// uses for its rows and probes.
			b.Key += "\x00sentinel"
		}
		n := describeMember(in, b, sentinel)
		if _, ok := byNamespace[b.Namespace]; !ok {
			namespaces = append(namespaces, b.Namespace)
		}
		byNamespace[b.Namespace] = append(byNamespace[b.Namespace], n)
	}
	for _, b := range in.redis {
		add(b, false)
	}
	for _, b := range in.sentinels {
		add(b, true)
	}
	sort.Strings(namespaces)

	var out []deployment
	for _, ns := range namespaces {
		out = append(out, foldNamespace(ns, byNamespace[ns])...)
	}
	return out
}

func describeMember(in groupInput, b svcgroup.Backend, sentinel bool) *node {
	n := &node{m: member{Backend: b, Sentinel: sentinel}, pods: map[string]bool{}, workloads: map[string]bool{}}
	var pods []*corev1.Pod
	for _, name := range b.Pods {
		n.pods[name] = true
		if pod := in.pods[b.Namespace+"/"+name]; pod != nil {
			pods = append(pods, pod)
			if w := workloadRef(pod); w != "" {
				n.workloads[w] = true
			}
		}
	}
	addKeys := func(labels map[string]string) {
		keys, release, chart := releaseKeys(labels)
		for _, k := range keys {
			if !contains(n.keys, k) {
				n.keys = append(n.keys, k)
			}
		}
		if n.release == "" {
			n.release = release
		}
		if n.chart == "" {
			n.chart = chart
		}
	}
	for _, door := range b.Services {
		service := in.services[b.Namespace+"/"+door.Service]
		if service != nil {
			addKeys(service.Labels)
		}
		s, _ := stem(door.Service)
		if !contains(n.stems, s) {
			n.stems = append(n.stems, s)
		}
		n.m.Doors = append(n.m.Doors, doorRoleOf(service, door.Service, pods, sentinel))
	}
	for _, pod := range pods {
		addKeys(pod.Labels)
	}
	n.m.Workloads = sortedKeys(n.workloads)
	n.m.Role, n.m.RoleFrom = memberRole(n.m.Doors)
	return n
}

// doorRoleOf is the role one Service gives what is behind it, from the most
// trusted source that says one: the Service's own labels and selector (the
// selector is literally which pods it routes to), its having no cluster IP,
// the labels of every pod behind it when they all agree, and last its name.
func doorRoleOf(service *corev1.Service, name string, pods []*corev1.Pod, sentinel bool) doorRole {
	if sentinel {
		return doorRole{Role: roleSentinel, From: fromPort}
	}
	if service != nil {
		if role := roleFromLabels(service.Spec.Selector); role != "" {
			return doorRole{Role: role, From: fromLabels}
		}
		if role := roleFromLabels(service.Labels); role != "" {
			return doorRole{Role: role, From: fromLabels}
		}
		if service.Spec.ClusterIP == corev1.ClusterIPNone {
			return doorRole{Role: roleHeadless, From: fromSpec}
		}
	}
	if len(pods) > 0 {
		agreed := roleFromLabels(pods[0].Labels)
		for _, pod := range pods[1:] {
			if roleFromLabels(pod.Labels) != agreed {
				agreed = ""
				break
			}
		}
		if agreed != "" {
			return doorRole{Role: agreed, From: fromPods}
		}
	}
	if _, role := stem(name); role != "" {
		return doorRole{Role: role, From: fromName}
	}
	return doorRole{}
}

// trust orders the sources, most trusted first.
var trust = map[string]int{fromPort: 0, fromLabels: 1, fromPods: 2, fromSpec: 3, fromName: 4}

// memberRole is the role of a member from its doors: a primary or a replica
// said by any door, the most trusted first, wins over headless. Headless is the
// answer only when every door is headless, since a silent ClusterIP Service
// may route to any pod.
func memberRole(doors []doorRole) (string, string) {
	best, from := "", ""
	headless := len(doors) > 0
	for _, d := range doors {
		if d.Role != roleHeadless {
			headless = false
		}
		if d.Role == "" || d.Role == roleHeadless {
			continue
		}
		if best == "" || trust[d.From] < trust[from] {
			best, from = d.Role, d.From
		}
	}
	if best == "" && headless {
		return roleHeadless, doors[0].From
	}
	return best, from
}

// foldNamespace is the union of one namespace's members on the four kinds of
// evidence, strongest first.
func foldNamespace(namespace string, nodes []*node) []deployment {
	parent := make([]int, len(nodes))
	evidence := make([]map[string]bool, len(nodes))
	for i := range parent {
		parent[i] = i
		evidence[i] = map[string]bool{}
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	members := func(root int) []*node {
		var out []*node
		for i := range nodes {
			if find(i) == root {
				out = append(out, nodes[i])
			}
		}
		return out
	}
	union := func(a, b int, kind string, guarded bool) {
		ra, rb := find(a), find(b)
		if ra == rb {
			evidence[ra][kind] = true
			return
		}
		if guarded && !compatible(members(ra), members(rb), kind) {
			return
		}
		parent[rb] = ra
		for k := range evidence[rb] {
			evidence[ra][k] = true
		}
		evidence[ra][kind] = true
	}
	pairs := func(kind string, guarded bool, linked func(a, b *node) bool) {
		for i := range nodes {
			for j := i + 1; j < len(nodes); j++ {
				if linked(nodes[i], nodes[j]) {
					union(i, j, kind, guarded)
				}
			}
		}
	}

	pairs(byWorkload, false, func(a, b *node) bool { return overlaps(a.workloads, b.workloads) })
	pairs(byPods, false, func(a, b *node) bool { return overlaps(a.pods, b.pods) })
	pairs(byRelease, true, func(a, b *node) bool { return shares(a.keys, b.keys) })
	pairs(byName, true, func(a, b *node) bool { return shares(a.stems, b.stems) })

	roots := map[int][]*node{}
	var order []int
	for i := range nodes {
		r := find(i)
		if _, ok := roots[r]; !ok {
			order = append(order, r)
		}
		roots[r] = append(roots[r], nodes[i])
	}
	var out []deployment
	for _, r := range order {
		group := roots[r]
		redis := false
		for _, n := range group {
			redis = redis || !n.m.Sentinel
		}
		if !redis {
			continue
		}
		out = append(out, deploymentOf(namespace, group, evidence[r]))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// compatible holds the weak edges back: never two primaries with pods of
// their own, and never a name joining what labels put apart.
func compatible(a, b []*node, kind string) bool {
	if hasPrimary(a) && hasPrimary(b) {
		return false
	}
	if kind == byName {
		ka, kb := allKeys(a), allKeys(b)
		if len(ka) > 0 && len(kb) > 0 && !shares(ka, kb) {
			return false
		}
	}
	return true
}

func hasPrimary(group []*node) bool {
	for _, n := range group {
		if n.m.Role == rolePrimary && len(n.pods) > 0 {
			return true
		}
	}
	return false
}

func allKeys(group []*node) []string {
	var out []string
	for _, n := range group {
		out = append(out, n.keys...)
	}
	return out
}

var roleOrder = map[string]int{rolePrimary: 0, roleReplica: 1, "": 2, roleHeadless: 3, roleSentinel: 4}

func deploymentOf(namespace string, group []*node, evidence map[string]bool) deployment {
	d := deployment{Namespace: namespace}
	workloads := map[string]bool{}
	stems := map[string]int{}
	for _, n := range group {
		d.Members = append(d.Members, n.m)
		for w := range n.workloads {
			workloads[w] = true
		}
		for _, s := range n.stems {
			stems[s]++
		}
		if d.Release == "" {
			d.Release = n.release
		}
		if d.Chart == "" {
			d.Chart = n.chart
		}
	}
	sort.SliceStable(d.Members, func(i, j int) bool {
		a, b := d.Members[i], d.Members[j]
		if a.Sentinel != b.Sentinel {
			return !a.Sentinel
		}
		if roleOrder[a.Role] != roleOrder[b.Role] {
			return roleOrder[a.Role] < roleOrder[b.Role]
		}
		return firstService(a) < firstService(b)
	})
	d.Workloads = sortedKeys(workloads)
	if len(group) > 1 {
		for _, kind := range []string{byWorkload, byPods, byRelease, byName} {
			if evidence[kind] {
				d.Evidence = append(d.Evidence, kind)
			}
		}
	}
	// The name most of its Services share; on a tie the shorter, then the
	// first in order, so the same cluster draws the same name twice.
	best, count := "", 0
	for s, c := range stems {
		if c > count || (c == count && (len(s) < len(best) || (len(s) == len(best) && s < best))) {
			best, count = s, c
		}
	}
	d.Name = best
	return d
}

func firstService(m member) string {
	if len(m.Backend.Services) == 0 {
		return ""
	}
	return m.Backend.Services[0].Service
}

func overlaps(a, b map[string]bool) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}

func shares(a, b []string) bool {
	for _, x := range a {
		if contains(b, x) {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

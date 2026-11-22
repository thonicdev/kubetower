package redis

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The fold into one entry per deployment, tested on the shapes the charts and the
// operator actually install: the Services, their labels and selectors, the
// EndpointSlices and the pods with their owners, read through the inventory
// route as the page reads them.

type deploymentBody struct {
	Name      string   `json:"name"`
	Evidence  []string `json:"evidence"`
	Release   string   `json:"release"`
	Chart     string   `json:"chart"`
	Workloads []string `json:"workloads"`
	Members   []struct {
		Pods     []string `json:"pods"`
		Role     string   `json:"role"`
		RoleFrom string   `json:"roleFrom"`
		Sentinel bool     `json:"sentinel"`
		Doors    []struct {
			Service  string `json:"service"`
			Port     int    `json:"port"`
			Role     string `json:"role"`
			RoleFrom string `json:"roleFrom"`
		} `json:"doors"`
	} `json:"members"`
}

type foldedBody struct {
	Services   int      `json:"services"`
	Members    int      `json:"members"`
	Redises    int      `json:"redises"`
	PodsNotes  []string `json:"podsNotes"`
	Namespaces []struct {
		Name        string           `json:"name"`
		Deployments []deploymentBody `json:"deployments"`
	} `json:"namespaces"`
}

func (b foldedBody) in(t *testing.T, namespace string) []deploymentBody {
	t.Helper()
	for _, ns := range b.Namespaces {
		if ns.Name == namespace {
			return ns.Deployments
		}
	}
	t.Fatalf("no namespace %q in %+v", namespace, b.Namespaces)
	return nil
}

// roles is a deployment's members as "role:service,service" strings, in the
// order drawn.
func (d deploymentBody) roles() []string {
	var out []string
	for _, m := range d.Members {
		var names []string
		for _, door := range m.Doors {
			names = append(names, door.Service)
		}
		out = append(out, m.Role+":"+strings.Join(names, ","))
	}
	return out
}

type svcShape struct {
	name     string
	labels   map[string]string
	selector map[string]string
	headless bool
	ports    []corev1.ServicePort
	pods     []string
}

func (h *fakeHost) shapeService(t *testing.T, namespace string, s svcShape) {
	t.Helper()
	spec := corev1.ServiceSpec{Ports: s.ports, Selector: s.selector}
	if s.headless {
		spec.ClusterIP = corev1.ClusterIPNone
	}
	h.add(t, "services", &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: s.name, Labels: s.labels},
		Spec:       spec,
	})
	if len(s.pods) == 0 {
		return
	}
	slice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: s.name + "-x",
		Labels: map[string]string{discoveryv1.LabelServiceName: s.name}}}
	for _, p := range s.pods {
		slice.Endpoints = append(slice.Endpoints, discoveryv1.Endpoint{Addresses: []string{"10.0.0.9"},
			TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: p, Namespace: namespace}})
	}
	h.add(t, "endpointslices", slice)
}

func (h *fakeHost) ownedPod(t *testing.T, namespace, name, kind, owner string, labels map[string]string) {
	t.Helper()
	yes := true
	h.add(t, "pods", &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: labels,
		OwnerReferences: []metav1.OwnerReference{{Kind: kind, Name: owner, Controller: &yes, APIVersion: "apps/v1"}}}})
}

func with(base map[string]string, more ...string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(more); i += 2 {
		out[more[i]] = more[i+1]
	}
	return out
}

var (
	tcpRedis    = []corev1.ServicePort{{Name: "tcp-redis", Port: 6379}}
	tcpSentinel = corev1.ServicePort{Name: "tcp-sentinel", Port: 26379}
)

// bitnamiRedis installs the bitnami `redis` chart's Services and pods for one
// release in one namespace: `standalone`, `replication` (one primary, three
// replicas) or `sentinel` (three nodes, each with a Sentinel beside it).
func bitnamiRedis(t *testing.T, h *fakeHost, namespace, release, architecture string) {
	t.Helper()
	full := release + "-redis"
	sel := map[string]string{"app.kubernetes.io/instance": release, "app.kubernetes.io/name": "redis"}
	labels := with(sel, "helm.sh/chart", "redis-19.6.4", "app.kubernetes.io/managed-by", "Helm")
	switch architecture {
	case "standalone", "replication":
		master := full + "-master-0"
		h.ownedPod(t, namespace, master, "StatefulSet", full+"-master", with(sel, "app.kubernetes.io/component", "master"))
		h.shapeService(t, namespace, svcShape{name: full + "-master", labels: with(labels, "app.kubernetes.io/component", "master"),
			selector: with(sel, "app.kubernetes.io/component", "master"), ports: tcpRedis, pods: []string{master}})
		all := []string{master}
		if architecture == "replication" {
			var replicas []string
			for _, n := range []string{"0", "1", "2"} {
				name := full + "-replicas-" + n
				replicas = append(replicas, name)
				h.ownedPod(t, namespace, name, "StatefulSet", full+"-replicas", with(sel, "app.kubernetes.io/component", "replica"))
			}
			h.shapeService(t, namespace, svcShape{name: full + "-replicas", labels: with(labels, "app.kubernetes.io/component", "replica"),
				selector: with(sel, "app.kubernetes.io/component", "replica"), ports: tcpRedis, pods: replicas})
			all = append(all, replicas...)
		}
		h.shapeService(t, namespace, svcShape{name: full + "-headless", labels: labels, selector: sel, headless: true,
			ports: tcpRedis, pods: all})
	case "sentinel":
		var nodes []string
		for _, n := range []string{"0", "1", "2"} {
			name := full + "-node-" + n
			nodes = append(nodes, name)
			h.ownedPod(t, namespace, name, "StatefulSet", full+"-node", with(sel, "app.kubernetes.io/component", "node"))
		}
		ports := append(append([]corev1.ServicePort{}, tcpRedis...), tcpSentinel)
		h.shapeService(t, namespace, svcShape{name: full, labels: with(labels, "app.kubernetes.io/component", "node"),
			selector: with(sel, "app.kubernetes.io/component", "node"), ports: ports, pods: nodes})
		h.shapeService(t, namespace, svcShape{name: full + "-headless", labels: labels, selector: sel, headless: true,
			ports: ports, pods: nodes})
	default:
		t.Fatalf("no such architecture %q", architecture)
	}
}

// bitnamiValkey is the bitnami `valkey` chart's replication shape: it says
// primary where the redis chart says master.
func bitnamiValkey(t *testing.T, h *fakeHost, namespace, release string) {
	t.Helper()
	full := release + "-valkey"
	sel := map[string]string{"app.kubernetes.io/instance": release, "app.kubernetes.io/name": "valkey"}
	labels := with(sel, "helm.sh/chart", "valkey-2.4.7")
	primary := full + "-primary-0"
	h.ownedPod(t, namespace, primary, "StatefulSet", full+"-primary", with(sel, "app.kubernetes.io/component", "primary"))
	replicas := []string{full + "-replicas-0", full + "-replicas-1"}
	for _, r := range replicas {
		h.ownedPod(t, namespace, r, "StatefulSet", full+"-replicas", with(sel, "app.kubernetes.io/component", "replica"))
	}
	h.shapeService(t, namespace, svcShape{name: full + "-primary", labels: with(labels, "app.kubernetes.io/component", "primary"),
		selector: with(sel, "app.kubernetes.io/component", "primary"), ports: tcpRedis, pods: []string{primary}})
	h.shapeService(t, namespace, svcShape{name: full + "-replicas", labels: with(labels, "app.kubernetes.io/component", "replica"),
		selector: with(sel, "app.kubernetes.io/component", "replica"), ports: tcpRedis, pods: replicas})
	h.shapeService(t, namespace, svcShape{name: full + "-headless", labels: labels, selector: sel, headless: true,
		ports: tcpRedis, pods: append([]string{primary}, replicas...)})
}

// redisFailover is the spotahome redis-operator's shape for a RedisFailover
// named `name`: a StatefulSet `rfr-<name>` of Redis pods whose role the
// operator writes into `redisfailovers-role`, Services `rfr-` (every Redis
// pod), `rfrm-` and `rfrs-` (primary and replicas), and a Deployment
// `rfs-<name>` of Sentinels behind `rfs-<name>`.
func redisFailover(t *testing.T, h *fakeHost, namespace, name string) {
	t.Helper()
	owner := map[string]string{"redisfailovers.databases.spotahome.com/name": name,
		"app.kubernetes.io/name": name, "app.kubernetes.io/part-of": "redis-failover"}
	redis := with(owner, "app.kubernetes.io/component", "redis")
	pods := []string{"rfr-" + name + "-0", "rfr-" + name + "-1", "rfr-" + name + "-2"}
	for i, p := range pods {
		role := "slave"
		if i == 0 {
			role = "master"
		}
		h.ownedPod(t, namespace, p, "StatefulSet", "rfr-"+name, with(redis, "redisfailovers-role", role))
	}
	port := []corev1.ServicePort{{Name: "redis", Port: 6379}}
	h.shapeService(t, namespace, svcShape{name: "rfr-" + name, labels: redis, selector: redis, headless: true,
		ports: port, pods: pods})
	h.shapeService(t, namespace, svcShape{name: "rfrm-" + name, labels: redis,
		selector: with(redis, "redisfailovers-role", "master"), ports: port, pods: pods[:1]})
	h.shapeService(t, namespace, svcShape{name: "rfrs-" + name, labels: redis,
		selector: with(redis, "redisfailovers-role", "slave"), ports: port, pods: pods[1:]})
	sentinel := with(owner, "app.kubernetes.io/component", "sentinel")
	var sentinels []string
	for _, s := range []string{"rfs-" + name + "-7d9c-aaaaa", "rfs-" + name + "-7d9c-bbbbb"} {
		sentinels = append(sentinels, s)
		yes := true
		h.add(t, "pods", &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: s,
			Labels:          with(sentinel, "pod-template-hash", "7d9c"),
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "rfs-" + name + "-7d9c", Controller: &yes}}}})
	}
	h.shapeService(t, namespace, svcShape{name: "rfs-" + name, labels: sentinel, selector: sentinel,
		ports: []corev1.ServicePort{{Name: "sentinel", Port: 26379}}, pods: sentinels})
}

func fold(t *testing.T, h *fakeHost) foldedBody {
	t.Helper()
	w := serve(t, h, "/x/kind/redis/")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if h.forwards != 0 {
		t.Fatalf("the inventory opened %d tunnel(s) to group what it found; it reads the API server only", h.forwards)
	}
	return decode[foldedBody](t, w)
}

func TestTheBitnamiRedisChartIsOneEntryInEachArchitecture(t *testing.T) {
	for _, c := range []struct {
		architecture string
		roles        []string
		evidence     []string
		workloads    []string
	}{
		{"standalone", []string{"primary:cache-redis-headless,cache-redis-master"}, []string{},
			[]string{"StatefulSet/cache-redis-master"}},
		{"replication",
			[]string{"primary:cache-redis-master", "replica:cache-redis-replicas", "headless:cache-redis-headless"},
			[]string{byWorkload, byPods, byRelease, byName},
			[]string{"StatefulSet/cache-redis-master", "StatefulSet/cache-redis-replicas"}},
		{"sentinel",
			[]string{":cache-redis,cache-redis-headless", "sentinel:cache-redis,cache-redis-headless"},
			[]string{byWorkload, byPods, byRelease, byName},
			[]string{"StatefulSet/cache-redis-node"}},
	} {
		t.Run(c.architecture, func(t *testing.T) {
			h := newHost()
			bitnamiRedis(t, h, "apps", "cache", c.architecture)
			body := fold(t, h)
			got := body.in(t, "apps")
			if len(got) != 1 || body.Redises != 1 {
				t.Fatalf("%d deployments (%d counted), want one: %+v", len(got), body.Redises, got)
			}
			d := got[0]
			if !slices.Equal(d.roles(), c.roles) {
				t.Errorf("members = %q, want %q", d.roles(), c.roles)
			}
			if !slices.Equal(d.Evidence, c.evidence) {
				t.Errorf("evidence = %q, want %q", d.Evidence, c.evidence)
			}
			if !slices.Equal(d.Workloads, c.workloads) {
				t.Errorf("workloads = %q, want %q", d.Workloads, c.workloads)
			}
			if d.Name != "cache-redis" || d.Release != "cache" || d.Chart != "redis-19.6.4" {
				t.Errorf("name %q, release %q, chart %q; want cache-redis, cache, redis-19.6.4", d.Name, d.Release, d.Chart)
			}
		})
	}
}

// The roles say where they came from: the Service's own labels and selector
// for a primary and its replicas, the missing cluster IP for the headless one.
func TestARoleSaysWhatItWasReadFrom(t *testing.T) {
	h := newHost()
	bitnamiRedis(t, h, "apps", "cache", "replication")
	d := fold(t, h).in(t, "apps")[0]
	from := map[string]string{}
	for _, m := range d.Members {
		from[m.Role] = m.RoleFrom
	}
	want := map[string]string{rolePrimary: fromLabels, roleReplica: fromLabels, roleHeadless: fromSpec}
	for role, source := range want {
		if from[role] != source {
			t.Errorf("%s was read from %q, want %q", role, from[role], source)
		}
	}
}

func TestTheBitnamiValkeyChartSaysPrimaryAndIsOneEntry(t *testing.T) {
	h := newHost()
	bitnamiValkey(t, h, "apps", "sess")
	got := fold(t, h).in(t, "apps")
	if len(got) != 1 {
		t.Fatalf("%d deployments, want one: %+v", len(got), got)
	}
	want := []string{"primary:sess-valkey-primary", "replica:sess-valkey-replicas", "headless:sess-valkey-headless"}
	if !slices.Equal(got[0].roles(), want) {
		t.Fatalf("members = %q, want %q", got[0].roles(), want)
	}
	if got[0].Name != "sess-valkey" {
		t.Fatalf("name = %q, want sess-valkey", got[0].Name)
	}
}

// The operator's shape: four Services on two workloads, with the role in a
// label the operator rewrites on failover and the Sentinels on a port of their
// own - joined, and never offered as a door.
func TestARedisFailoverIsOneEntryWithItsSentinels(t *testing.T) {
	h := newHost()
	redisFailover(t, h, "data", "shop")
	body := fold(t, h)
	got := body.in(t, "data")
	if len(got) != 1 {
		t.Fatalf("%d deployments, want one: %+v", len(got), got)
	}
	want := []string{"primary:rfrm-shop", "replica:rfrs-shop", "headless:rfr-shop", "sentinel:rfs-shop"}
	if !slices.Equal(got[0].roles(), want) {
		t.Fatalf("members = %q, want %q", got[0].roles(), want)
	}
	if got[0].Name != "shop" || got[0].Release != "shop" {
		t.Fatalf("name %q, release %q; want shop and shop", got[0].Name, got[0].Release)
	}
	if !slices.Contains(got[0].Workloads, "Deployment/rfs-shop") {
		t.Fatalf("workloads = %q: the Sentinels' ReplicaSet should read as its Deployment", got[0].Workloads)
	}
	sentinel := got[0].Members[3]
	if !sentinel.Sentinel || sentinel.RoleFrom != fromPort {
		t.Fatalf("the Sentinel member = %+v, want marked a Sentinel, from its port", sentinel)
	}
	// Counted as a Service of the inventory only where it can be opened.
	if body.Services != 3 || body.Members != 3 {
		t.Fatalf("services %d, members %d; want 3 and 3 - the Sentinel is drawn, not offered", body.Services, body.Members)
	}
	// And the instance route does not find it: a Sentinel port is never dialled.
	if w := serve(t, h, "/x/kind/redis/instances/data/rfs-shop/26379/probe"); w.Code != http.StatusNotFound {
		t.Fatalf("the Sentinel's port answered %d to a probe, want 404", w.Code)
	}
}

// Never across namespaces, and never two releases in one: the same release
// name in two namespaces is two Redises, and two releases side by side are
// two as well.
func TestReleasesAreNeverFoldedAcrossNamespacesOrIntoEachOther(t *testing.T) {
	h := newHost()
	bitnamiRedis(t, h, "apps", "cache", "replication")
	bitnamiRedis(t, h, "apps", "queue", "standalone")
	bitnamiRedis(t, h, "other", "cache", "replication")
	body := fold(t, h)
	if body.Redises != 3 {
		t.Fatalf("%d deployments, want 3: cache and queue in apps, cache in other", body.Redises)
	}
	apps := body.in(t, "apps")
	if len(apps) != 2 || apps[0].Name != "cache-redis" || apps[1].Name != "queue-redis" {
		t.Fatalf("apps = %+v, want cache-redis and queue-redis", apps)
	}
	if other := body.in(t, "other"); len(other) != 1 || len(other[0].Members) != 3 {
		t.Fatalf("other = %+v, want its own cache-redis with its three members", other)
	}
}

// Two aliases of one chart in one release carry the same instance and name
// labels. Two primaries with pods of their own are two Redises, whatever the
// labels say.
func TestTwoPrimariesAreNeverOneRedis(t *testing.T) {
	h := newHost()
	sel := map[string]string{"app.kubernetes.io/instance": "app", "app.kubernetes.io/name": "redis"}
	for _, alias := range []string{"cache", "queue"} {
		pod := "app-" + alias + "-master-0"
		h.ownedPod(t, "apps", pod, "StatefulSet", "app-"+alias+"-master", with(sel, "app.kubernetes.io/component", "master"))
		h.shapeService(t, "apps", svcShape{name: "app-" + alias + "-master", labels: with(sel, "app.kubernetes.io/component", "master"),
			selector: with(sel, "app.kubernetes.io/component", "master"), ports: tcpRedis, pods: []string{pod}})
	}
	if got := fold(t, h).in(t, "apps"); len(got) != 2 {
		t.Fatalf("%d deployments, want two: %+v", len(got), got)
	}
}

// A name is the last hint: it folds what nothing else speaks for, and says it
// is the only evidence. It never folds what labels put apart.
func TestANameAloneFoldsAndSaysSo(t *testing.T) {
	h := newHost()
	h.shapeService(t, "legacy", svcShape{name: "sessions", ports: tcpRedis, pods: []string{"sessions-a"}})
	h.shapeService(t, "legacy", svcShape{name: "sessions-ro", ports: tcpRedis, pods: []string{"sessions-b"}})
	h.shapeService(t, "legacy", svcShape{name: "cache", labels: map[string]string{"app.kubernetes.io/instance": "x", "app.kubernetes.io/name": "redis"},
		ports: tcpRedis, pods: []string{"cache-a"}})
	h.shapeService(t, "legacy", svcShape{name: "cache-ro", labels: map[string]string{"app.kubernetes.io/instance": "y", "app.kubernetes.io/name": "redis"},
		ports: tcpRedis, pods: []string{"cache-b"}})
	got := fold(t, h).in(t, "legacy")
	if len(got) != 3 {
		t.Fatalf("%d deployments, want sessions folded on its name and the two caches apart: %+v", len(got), got)
	}
	sessions := got[2]
	if sessions.Name != "sessions" || !slices.Equal(sessions.Evidence, []string{byName}) {
		t.Fatalf("sessions = %+v, want one entry folded on its name alone", sessions)
	}
	if !slices.Equal(sessions.roles(), []string{"replica:sessions-ro", ":sessions"}) {
		t.Fatalf("roles = %q, want the -ro one a replica by its name and the other unsaid", sessions.roles())
	}
	if sessions.Members[0].RoleFrom != fromName {
		t.Fatalf("the replica's role came from %q, want name", sessions.Members[0].RoleFrom)
	}
}

// The pods could not be read: the fold goes on with what the Services say,
// and the answer says which namespace was grouped without its pods - a
// failure, never drawn as pods that answered and were owned by nothing.
func TestPodsThatCouldNotBeReadAreSaidAndTheLabelsStillFold(t *testing.T) {
	h := newHost()
	bitnamiRedis(t, h, "apps", "cache", "replication")
	h.listErr["pods"] = errors.New(`pods is forbidden: User "ana" cannot list resource "pods"`)
	body := fold(t, h)
	if len(body.PodsNotes) != 1 || !strings.Contains(body.PodsNotes[0], "apps") {
		t.Fatalf("podsNotes = %q, want one sentence naming apps", body.PodsNotes)
	}
	got := body.in(t, "apps")
	if len(got) != 1 || len(got[0].Members) != 3 {
		t.Fatalf("apps = %+v, want one entry with three members, folded on pods and labels", got)
	}
	if len(got[0].Workloads) != 0 || slices.Contains(got[0].Evidence, byWorkload) {
		t.Fatalf("workloads %q, evidence %q: a workload was claimed with no pod read", got[0].Workloads, got[0].Evidence)
	}
}

// The derivations, on their own.
func TestTheAffixesAndTheOwnerAreReadAsChartsWriteThem(t *testing.T) {
	for name, want := range map[string][2]string{
		"cache-redis-master":   {"cache-redis", rolePrimary},
		"cache-redis-replicas": {"cache-redis", roleReplica},
		"cache-redis-headless": {"cache-redis", roleHeadless},
		"sess-valkey-primary":  {"sess-valkey", rolePrimary},
		"rfrm-shop":            {"shop", rolePrimary},
		"rfs-shop":             {"shop", roleSentinel},
		"rfr-shop":             {"shop", ""},
		"redis":                {"redis", ""},
		"-master":              {"-master", ""},
	} {
		if s, role := stem(name); s != want[0] || role != want[1] {
			t.Errorf("stem(%q) = %q, %q; want %q, %q", name, s, role, want[0], want[1])
		}
	}
	yes := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"pod-template-hash": "69cf"},
		OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "argocd-redis-69cf", Controller: &yes}}}}
	if got := workloadRef(pod); got != "Deployment/argocd-redis" {
		t.Errorf("workloadRef = %q, want Deployment/argocd-redis", got)
	}
	pod.Labels["pod-template-hash"] = "other"
	if got := workloadRef(pod); got != "ReplicaSet/argocd-redis-69cf" {
		t.Errorf("a ReplicaSet not named for its pod's hash = %q, want it left a ReplicaSet", got)
	}
	if got := chartName("redis-19.6.4"); got != "redis" {
		t.Errorf("chartName = %q", got)
	}

	// Only the controller is the workload: another owner reference says
	// nothing about the installation, and a static pod's Node owns every
	// static pod on it.
	no := false
	other := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{
		{Kind: "ConfigMap", Name: "x", Controller: &no},
		{Kind: "Secret", Name: "y"},
		{Kind: "StatefulSet", Name: "redis", Controller: &yes},
	}}}
	if got := workloadRef(other); got != "StatefulSet/redis" {
		t.Errorf("workloadRef past non-controller owners = %q, want StatefulSet/redis", got)
	}
	static := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{
		{Kind: "Node", Name: "worker-1", Controller: &yes},
	}}}
	if got := workloadRef(static); got != "" {
		t.Errorf("a static pod's workload = %q, want none: its Node owns every static pod on it", got)
	}
}

// Every member of a deployment has a key of its own - the page keys its rows
// and its probes by it. A Sentinel beside the Redis in the same pods (the
// bitnami sentinel architecture) is the case svcgroup's pod-set key cannot
// tell apart (the review of b28aa7c).
func TestEveryMemberHasAKeyOfItsOwn(t *testing.T) {
	for _, architecture := range []string{"standalone", "replication", "sentinel"} {
		h := newHost()
		bitnamiRedis(t, h, "apps", "cache", architecture)
		body := decode[struct {
			Namespaces []struct {
				Deployments []struct {
					Members []struct {
						Key      string `json:"key"`
						Sentinel bool   `json:"sentinel"`
					} `json:"members"`
				} `json:"deployments"`
			} `json:"namespaces"`
		}](t, serve(t, h, "/x/kind/redis/"))
		seen := map[string]bool{}
		for _, m := range body.Namespaces[0].Deployments[0].Members {
			if seen[m.Key] {
				t.Errorf("%s: two members share the key %q", architecture, m.Key)
			}
			seen[m.Key] = true
		}
		if architecture == "sentinel" && len(seen) != 2 {
			t.Errorf("sentinel: %d distinct keys, want the Redis's and the Sentinel's", len(seen))
		}
	}
}

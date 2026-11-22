package pending

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
)

// bigPod is a pod as a real cluster holds one: labels, annotations, an
// environment, mounts, two containers and their statuses.
func bigPod(namespace, name string, i int) *corev1.Pod {
	env := make([]corev1.EnvVar, 12)
	for j := range env {
		env[j] = corev1.EnvVar{Name: fmt.Sprintf("SETTING_%d", j), Value: fmt.Sprintf("value-%d-%d", i, j)}
	}
	c := container("250m", "256Mi")
	c.Env = env
	c.VolumeMounts = []corev1.VolumeMount{{Name: "config", MountPath: "/etc/app"}, {Name: "token", MountPath: "/var/run/secrets"}}
	c.Ports = []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}
	side := container("50m", "64Mi")
	side.Name = "proxy"
	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace, Name: name, UID: "u",
			CreationTimestamp: metav1.NewTime(testNow.Add(-time.Duration(i) * time.Minute)),
			Labels:            map[string]string{"app": name[:4], "team": "core", "pod-template-hash": "7f9c8d6b5", "tier": "web", "version": "1.2.3"},
			Annotations:       map[string]string{"prometheus.io/scrape": "true", "prometheus.io/port": "8080", "checksum/config": "0123456789abcdef0123456789abcdef"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{c, side},
			Volumes:    []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"}}}}},
			Tolerations: []corev1.Toleration{
				{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute},
				{Key: "node.kubernetes.io/unreachable", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute},
			},
		},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}, {Type: corev1.PodScheduled, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", Ready: true, Image: "registry.example.com/app:1.2.3", ImageID: "sha256:0123456789abcdef"},
				{Name: "proxy", Ready: true, Image: "registry.example.com/proxy:2", ImageID: "sha256:fedcba9876543210"},
			},
		},
	}
}

// largeCluster is the load target: 200 nodes, 5 000 pods of which 500 wait.
func largeCluster(t *testing.T) *fakeHost {
	h := newHost(t)
	for i := 0; i < 200; i++ {
		var taints []corev1.Taint
		if i%10 == 0 {
			taints = []corev1.Taint{{Key: "dedicated", Value: "batch", Effect: corev1.TaintEffectNoSchedule}}
		}
		h.add("nodes", node(fmt.Sprintf("node-%03d", i), "16", "64Gi", map[string]string{"zone": fmt.Sprintf("z%d", i%3), "kubernetes.io/hostname": fmt.Sprintf("node-%03d", i)}, taints...))
	}
	for i := 0; i < 4500; i++ {
		h.add("pods", func() *corev1.Pod {
			p := bigPod(fmt.Sprintf("ns-%02d", i%40), fmt.Sprintf("work-%05d", i), i)
			p.Spec.NodeName = fmt.Sprintf("node-%03d", i%200)
			return p
		}())
	}
	messages := []string{msgMemory, msgGPU, msgSelector, msgAnti, msgSpread, msgMixed}
	for i := 0; i < 500; i++ {
		p := bigPod(fmt.Sprintf("ns-%02d", i%40), fmt.Sprintf("wait-%05d", i), i)
		p.Status = corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
			Message: messages[i%len(messages)], LastTransitionTime: metav1.NewTime(testNow.Add(-time.Hour)),
		}}}
		if i%5 == 0 {
			p.Spec.Containers[0].Resources.Requests = quantities("cpu", "20", "memory", "1Gi")
		}
		h.add("pods", p)
	}
	for i := 0; i < 300; i++ {
		h.add("events", event(fmt.Sprintf("ns-%02d", i%40), "ReplicaSet", fmt.Sprintf("rs-%03d", i%60), "FailedCreate",
			`Error creating: pods "x" is forbidden: exceeded quota: q, requested: requests.cpu=3, used: requests.cpu=2, limited: requests.cpu=2`, testNow.Add(-time.Duration(i)*time.Second), 3))
	}
	return h
}

// measure runs one request and answers what it allocated in all, what it left
// on the heap once collected, and how long it took.
func measure(t *testing.T, h *fakeHost, path string) (alloc, retained uint64, took time.Duration) {
	t.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	rec := serve(t, h, path)
	took = time.Since(start)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String()[:200])
	}
	runtime.ReadMemStats(&after)
	alloc = after.TotalAlloc - before.TotalAlloc
	rec = nil
	runtime.GC()
	runtime.ReadMemStats(&after)
	if after.HeapAlloc > before.HeapAlloc {
		retained = after.HeapAlloc - before.HeapAlloc
	}
	return alloc, retained, took
}

// The ceilings a large cluster is held to. What one request allocates
// includes the reviewed reads themselves (the fake copies every object it
// hands back, as decoding them does), so most of it is the reads' and goes
// with the request; what stays is nothing.
const (
	maxPageAlloc = 350 << 20
	maxViewAlloc = 200 << 20
	maxRetained  = 16 << 20
	// Measured locally (4 cores, other work running): the page 163 MiB in
	// 0.5 s, a pod 71 MiB in 0.14 s. The time is a base, without -race on an
	// idle machine; timeCeiling scales it to the run.
	maxTook = 3 * time.Second
)

func TestALargeClusterCostsABoundedAmount(t *testing.T) {
	if testing.Short() {
		t.Skip("a load test")
	}
	h := largeCluster(t)
	ceiling := timeCeiling(t, maxTook)

	alloc, retained, took := measure(t, h, "/")
	t.Logf("page: allocated %d MiB, retained %d KiB, %s", alloc>>20, retained>>10, took)
	if alloc > maxPageAlloc || retained > maxRetained || took > ceiling {
		t.Errorf("page: allocated %d MiB (max %d), retained %d KiB (max %d), %s (max %s)",
			alloc>>20, maxPageAlloc>>20, retained>>10, maxRetained>>10, took, ceiling)
	}
	a := decode[PageAnswer](t, serve(t, h, "/"))
	if a.Total != 500 || len(a.Rows) != 500 || len(a.Bars) != 200 || len(a.Failures) != 120 {
		t.Errorf("total %d rows %d bars %d failures %d", a.Total, len(a.Rows), len(a.Bars), len(a.Failures))
	}

	alloc, retained, took = measure(t, h, "/pod/ns-00/wait-00000")
	t.Logf("pod view: allocated %d MiB, retained %d KiB, %s", alloc>>20, retained>>10, took)
	if alloc > maxViewAlloc || retained > maxRetained || took > ceiling {
		t.Errorf("pod view: allocated %d MiB (max %d), retained %d KiB, %s", alloc>>20, maxViewAlloc>>20, retained>>10, took)
	}
	v := decode[PodView](t, serve(t, h, "/pod/ns-00/wait-00000"))
	if v.NodesTotal != 200 || len(v.Fits) != 200 || len(v.Bars) != maxBars {
		t.Errorf("view: nodes %d fits %d bars %d", v.NodesTotal, len(v.Fits), len(v.Bars))
	}

	alloc, _, took = measure(t, h, "/cells?kind=pods")
	t.Logf("cells: allocated %d MiB, %s", alloc>>20, took)
}

// More pending pods than one answer reads: the answer is cut and says so,
// and the rows stay within their ceiling.
func TestTooManyPendingPodsAreCutAndSaidToBe(t *testing.T) {
	if testing.Short() {
		t.Skip("a load test")
	}
	h := newHost(t)
	h.add("nodes", node("n", "2", "4Gi", nil))
	for i := 0; i < MaxPending+10; i++ {
		h.add("pods", unschedulable("ns", fmt.Sprintf("p-%05d", i), msgMemory, time.Hour, container("1", "1Gi")))
	}
	a := decode[PageAnswer](t, serve(t, h, "/"))
	if !a.Capped || a.Pods.Complete || a.Total != MaxPending || len(a.Rows) > MaxRows {
		t.Errorf("capped %v complete %v total %d rows %d", a.Capped, a.Pods.Complete, a.Total, len(a.Rows))
	}
}

// placementCluster is the load target for pod affinity and topology spread:
// 300 nodes in 6 zones, 4 000 placed pods labeled by workload (150 of them
// carrying a required anti-affinity), and 1 400 pending pods: 1 000 replicas
// of 50 Deployments that spread over the zones and repel each other by node,
// and 400 StatefulSet-like pods whose labels are each their own, which is
// the worst case for sharing an answer between replicas.
func placementCluster(t testing.TB) *fakeHost {
	h := &fakeHost{objects: map[string][]unstructured.Unstructured{}, denied: map[string]bool{}, unreviewable: map[string]bool{}}
	zones := 6
	for i := 0; i < 300; i++ {
		h.addTB(t, "nodes", node(fmt.Sprintf("node-%03d", i), "16", "64Gi", map[string]string{
			topologyZone: fmt.Sprintf("z%d", i%zones), topologyHostname: fmt.Sprintf("node-%03d", i),
		}))
	}
	for i := 0; i < 4000; i++ {
		p := bigPod(fmt.Sprintf("ns-%02d", i%40), fmt.Sprintf("work-%05d", i), i)
		p.Spec.NodeName = fmt.Sprintf("node-%03d", i%300)
		p.Labels = map[string]string{"app": fmt.Sprintf("app-%02d", i%50)}
		if i%27 == 0 {
			p.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
				{LabelSelector: sel("app", fmt.Sprintf("app-%02d", (i+7)%50)), TopologyKey: topologyHostname},
			}}}
		}
		h.addTB(t, "pods", p)
	}
	msg := "0/300 nodes are available: 150 node(s) didn't match pod anti-affinity rules, 150 node(s) didn't match pod topology spread constraints."
	for i := 0; i < 1400; i++ {
		p := bigPod(fmt.Sprintf("ns-%02d", i%40), fmt.Sprintf("wait-%05d", i), i)
		p.Status = corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
			Message: msg, LastTransitionTime: metav1.NewTime(testNow.Add(-time.Hour)),
		}}}
		app := fmt.Sprintf("app-%02d", i%50)
		p.Labels = map[string]string{"app": app}
		if i >= 1000 {
			p.Labels["statefulset.kubernetes.io/pod-name"] = p.Name
		}
		p.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
			{LabelSelector: sel("app", app), TopologyKey: topologyHostname},
		}}}
		p.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{MaxSkew: 1, TopologyKey: topologyZone, WhenUnsatisfiable: corev1.DoNotSchedule, LabelSelector: sel("app", app)}}
		h.addTB(t, "pods", p)
	}
	return h
}

func (h *fakeHost) addTB(t testing.TB, resourceName string, objs ...any) {
	for _, o := range objs {
		m, err := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(o)
		if err != nil {
			t.Fatal(err)
		}
		h.objects[resourceName] = append(h.objects[resourceName], unstructured.Unstructured{Object: m})
	}
}

// The ceilings for the placement cluster. Measured on 2026-11-19 without
// -race (8 threads, load 6.4): the page 443 MiB in 1.8 s, a pod 66 MiB in
// 0.15 s. The time is a base, without -race on an idle machine; timeCeiling
// scales it to the run.
const (
	maxPlacementPageAlloc = 700 << 20
	maxPlacementViewAlloc = 250 << 20
	maxPlacementTook      = 5 * time.Second
)

func TestAffinityAndSpreadOnALargeClusterCostABoundedAmount(t *testing.T) {
	if testing.Short() {
		t.Skip("a load test")
	}
	h := placementCluster(t)
	ceiling := timeCeiling(t, maxPlacementTook)
	h.t = t

	alloc, retained, took := measure(t, h, "/")
	t.Logf("page, 1400 pending pods x 300 nodes: allocated %d MiB, retained %d KiB, %s", alloc>>20, retained>>10, took)
	if alloc > maxPlacementPageAlloc || retained > maxRetained || took > ceiling {
		t.Errorf("page: allocated %d MiB (max %d), retained %d KiB (max %d), %s (max %s)",
			alloc>>20, maxPlacementPageAlloc>>20, retained>>10, maxRetained>>10, took, ceiling)
	}
	// The reads are the ones the page made before: the nodes, the pods placed,
	// the pending pods, the events - never one per pod or per node.
	reads := map[string]int{}
	for _, q := range h.queries {
		reads[q.GVR.Resource]++
	}
	if reads["nodes"] != 1 || reads["pods"] != 2 || reads["events"] != 1 || len(reads) != 3 {
		t.Errorf("reads %v", reads)
	}

	alloc, retained, took = measure(t, h, "/pod/ns-00/wait-00000")
	t.Logf("pod view: allocated %d MiB, retained %d KiB, %s", alloc>>20, retained>>10, took)
	if alloc > maxPlacementViewAlloc || retained > maxRetained || took > ceiling {
		t.Errorf("pod view: allocated %d MiB (max %d), retained %d KiB, %s", alloc>>20, maxPlacementViewAlloc>>20, retained>>10, took)
	}
	v := decode[PodView](t, serve(t, h, "/pod/ns-00/wait-00000"))
	computed := 0
	for _, c := range v.Columns {
		if (c.Key == PredPodAffinity || c.Key == PredSpread) && c.Computed {
			computed++
		}
	}
	if computed != 2 || v.NodesTotal != 300 {
		t.Errorf("view: computed %d, nodes %d", computed, v.NodesTotal)
	}
}

// On a page, past maxPlacedWork distinct pods the cells stay gray and say why;
// replicas that share their rules and labels share one computation.
func TestAPageComputesAtMostAFixedNumberOfDistinctPods(t *testing.T) {
	h := placementCluster(t)
	h.t = t
	fl, err := readFleet(httptest.NewRequest(http.MethodGet, "/", nil), h, "kind", true)
	if err != nil {
		t.Fatal(err)
	}
	pr := &placer{r: httptest.NewRequest(http.MethodGet, "/", nil), h: h, cluster: "kind", fleet: fl, cache: map[string]*Placed{}, budget: maxPlacedWork}
	pods, _, err := readPending(httptest.NewRequest(http.MethodGet, "/", nil), h, "kind", "", "", MaxPending, testNow)
	if err != nil {
		t.Fatal(err)
	}
	grey := 0
	for _, p := range pods {
		got := pr.placed(p, false)
		if got == nil || !got.Spread.Computed {
			grey++
		}
	}
	// 50 shared workloads + 400 pods of their own = 450 distinct answers.
	t.Logf("%d answers computed, %d pods left gray", len(pr.cache), grey)
	if len(pr.cache) > maxPlacedWork {
		t.Errorf("computed %d distinct answers, ceiling %d", len(pr.cache), maxPlacedWork)
	}
	if grey == 0 {
		t.Error("nothing was left gray past the ceiling")
	}
}

// BenchmarkPlacementCompute is one pod's pod affinity and spread against 300
// nodes and 4 000 placed pods, with and without the sentence behind each
// refusal: `go test -bench Placement -benchmem ./extras/pending/`.
func BenchmarkPlacementCompute(b *testing.B) {
	h := placementCluster(b)
	fl, err := readFleet(httptest.NewRequest(http.MethodGet, "/", nil), h, "kind", true)
	if err != nil {
		b.Fatal(err)
	}
	app := "app-03"
	rules := Rules{Namespace: "ns-03", Labels: map[string]string{"app": app}, Spec: corev1.PodSpec{
		Affinity: &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
			{LabelSelector: sel("app", app), TopologyKey: topologyHostname},
		}}},
		TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{MaxSkew: 1, TopologyKey: topologyZone, WhenUnsatisfiable: corev1.DoNotSchedule, LabelSelector: sel("app", app)}},
	}}
	for _, detail := range []bool{false, true} {
		b.Run(fmt.Sprintf("detail=%v", detail), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				got := fl.place.Compute(rules, fl.nodes, true, nil, detail)
				if !got.Affinity.Computed || !got.Spread.Computed {
					b.Fatal("not computed")
				}
			}
		})
	}
}

package netinspect

import (
	"fmt"
	"net/http"
	"runtime"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// bigCluster is 5 000 pods in 100 namespaces, 20 workloads a namespace, and
// 1 000 NetworkPolicies: per namespace a default deny in each direction, and
// eight policies allowing a workload from another, from a labeled
// namespace, to an address block and to DNS.
func bigCluster(t testing.TB) *fakeHost {
	h := newHost()
	for n := range 100 {
		ns := fmt.Sprintf("team-%03d", n)
		h.namespace(t, ns, map[string]string{"tier": fmt.Sprint(n % 5)})
		for i := range 50 {
			app := fmt.Sprintf("app-%02d", i%20)
			h.add(t, "pods", &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: fmt.Sprintf("%s-%03d", app, i),
					Labels: map[string]string{"app": app, "tier": fmt.Sprint(i % 3)}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c",
					Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}}}},
				Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: fmt.Sprintf("10.%d.%d.%d", n, i/250, i%250+1)},
			})
		}
		in := np(ns, "deny-in", nil)
		in.Spec.PolicyTypes = types(networkingv1.PolicyTypeIngress)
		out := np(ns, "deny-out", nil)
		out.Spec.PolicyTypes = types(networkingv1.PolicyTypeEgress)
		h.policy(t, in)
		h.policy(t, out)
		for k := range 8 {
			target := fmt.Sprintf("app-%02d", k)
			p := np(ns, fmt.Sprintf("allow-%d", k), map[string]string{"app": target})
			p.Spec.PolicyTypes = types(networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress)
			both := networkingv1.NetworkPolicyPeer{
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": fmt.Sprintf("app-%02d", k+1)}},
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": fmt.Sprint(k % 5)}},
			}
			p.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{
				{From: []networkingv1.NetworkPolicyPeer{podPeer(map[string]string{"app": fmt.Sprintf("app-%02d", k+10)})}, Ports: []networkingv1.NetworkPolicyPort{named("http")}},
				{From: []networkingv1.NetworkPolicyPeer{both}, Ports: []networkingv1.NetworkPolicyPort{tcp(8080)}},
			}
			p.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{
				{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "192.168.0.0/16", Except: []string{"192.168.1.0/24"}}}}},
				{To: []networkingv1.NetworkPolicyPeer{nsPeer(map[string]string{"kubernetes.io/metadata.name": "kube-system"})}, Ports: []networkingv1.NetworkPolicyPort{tcp(53)}},
			}
			h.policy(t, p)
		}
	}
	return h
}

// ceilings for one request on bigCluster. Under -race everything is several
// times slower and allocates more, so the time ceiling grows with it; the
// memory one is the Go heap's, which the race detector's shadow memory is
// not part of.
const (
	loadTime        = 3 * time.Second
	loadAllocated   = 120 << 20 // bytes allocated over the request
	loadRetained    = 4 << 20   // bytes still held once the answer is written
	raceSlowdown    = 15
	raceAllocation  = 6
	loadAnswerBytes = 1 << 20
)

// measureRequest serves target once and answers how long it took, what it
// allocated, what the heap still held after, and the answer's size.
func measureRequest(t *testing.T, h *fakeHost, target string) (time.Duration, uint64, int64, int) {
	t.Helper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	w := serve(t, h, target)
	took := time.Since(start)
	if w.Code != http.StatusOK {
		t.Fatalf("%s answered %d: %s", target, w.Code, w.Body.String())
	}
	size := w.Body.Len()
	w = nil
	runtime.GC()
	runtime.ReadMemStats(&after)
	return took, after.TotalAlloc - before.TotalAlloc, int64(after.HeapAlloc) - int64(before.HeapAlloc), size
}

// The engine on 5 000 pods and 1 000 policies: every view and a verdict in
// bounded time and memory, and nothing held once answered.
func TestLoadFiveThousandPodsAThousandPolicies(t *testing.T) {
	if testing.Short() {
		t.Skip("the load test builds a cluster of 5 000 pods")
	}
	h := bigCluster(t)
	limit, allocLimit := loadTime, uint64(loadAllocated)
	if raceEnabled {
		// The race detector's own bookkeeping allocates on the Go heap too:
		// measured about five times the plain figure.
		limit *= raceSlowdown
		allocLimit *= raceAllocation
	}
	for _, target := range []string{
		"/x/big/netinspect/",
		"/x/big/netinspect/?ns=team-042",
		"/x/big/netinspect/?show=in&offset=300",
		"/x/big/netinspect/verdict?from=team-001/app-10-010&to=team-001/app-00-000&port=8080",
	} {
		took, allocated, retained, size := measureRequest(t, h, target)
		t.Logf("%s: %v, %.1f MiB allocated, %.2f MiB retained, %d KiB answer", target, took,
			float64(allocated)/(1<<20), float64(retained)/(1<<20), size>>10)
		if took > limit {
			t.Errorf("%s took %v, over %v", target, took, limit)
		}
		if allocated > allocLimit {
			t.Errorf("%s allocated %d MiB, over %d MiB", target, allocated>>20, allocLimit>>20)
		}
		if retained > loadRetained {
			t.Errorf("%s still holds %d MiB after answering, over %d MiB", target, retained>>20, loadRetained>>20)
		}
		if size > loadAnswerBytes {
			t.Errorf("%s answered %d KiB, over %d KiB", target, size>>10, loadAnswerBytes>>10)
		}
	}
	// And what it computed is right at this size: the verdict above.
	v := verdictOf(t, h, "team-001/app-10-010", "team-001/app-00-000", "8080")
	if v.Answer != "no" || v.Ingress.State != sideAllowed || v.Egress.State != sideDenied {
		t.Errorf("app-10 → app-00 in one namespace: ingress allowed by allow-0 rule 1, egress refused by deny-out: %+v / %+v", v.Ingress, v.Egress)
	}
}

// The same cluster under Cilium: a clusterwide baseline and 500 namespaced
// Cilium policies beside the 1 000 NetworkPolicies, with deny rules,
// namespace expressions, CIDR sets and an FQDN rule.
func TestLoadWithCiliumPolicies(t *testing.T) {
	if testing.Short() {
		t.Skip("the load test builds a cluster of 5 000 pods")
	}
	h := bigCluster(t)
	withCilium(h)
	h.manifest(t, baseline)
	for n := range 100 {
		ns := fmt.Sprintf("team-%03d", n)
		for k := range 5 {
			h.manifest(t, fmt.Sprintf(`
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata: {name: cilium-%d, namespace: %s}
spec:
  endpointSelector: {matchLabels: {app: app-%02d}}
  ingress:
    - fromEndpoints: [{matchLabels: {app: app-%02d}}]
      toPorts: [{ports: [{port: "8080", protocol: TCP}]}]
  ingressDeny:
    - fromEndpoints:
        - matchExpressions: [{key: io.kubernetes.pod.namespace, operator: NotIn, values: [%s, team-000]}]
  egress:
    - toCIDRSet: [{cidr: 10.0.0.0/8, except: [10.9.0.0/16]}]
    - toFQDNs: [{matchName: forge.example.com}]
      toPorts: [{ports: [{port: "443"}]}]
`, k, ns, k+10, k, ns))
		}
	}
	limit, allocLimit := loadTime, uint64(loadAllocated)
	if raceEnabled {
		limit *= raceSlowdown
		allocLimit *= raceAllocation
	}
	for _, target := range []string{
		"/x/big/netinspect/",
		"/x/big/netinspect/?ns=team-042",
		"/x/big/netinspect/verdict?from=team-001/app-00-000&to=team-002/app-10-010&port=8080",
	} {
		took, allocated, retained, size := measureRequest(t, h, target)
		t.Logf("%s: %v, %.1f MiB allocated, %.2f MiB retained, %d KiB answer", target, took,
			float64(allocated)/(1<<20), float64(retained)/(1<<20), size>>10)
		if took > limit || allocated > allocLimit || retained > loadRetained || size > loadAnswerBytes {
			t.Errorf("%s: %v, %d MiB allocated, %d MiB retained, %d KiB: over a ceiling", target, took, allocated>>20, retained>>20, size>>10)
		}
	}
}

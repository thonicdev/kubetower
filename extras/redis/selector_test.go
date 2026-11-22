package redis

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// selectedService is a Service with a selector and no EndpointSlice.
func (h *fakeHost) selectedService(t *testing.T, namespace, name string, selector map[string]string, port corev1.ServicePort) {
	t.Helper()
	h.add(t, "services", &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       corev1.ServiceSpec{Selector: selector, Ports: []corev1.ServicePort{port}},
	})
}

// runningPod is a pod with labels, an address, and Ready as given.
func runningPod(namespace, name string, podLabels map[string]string, ready bool) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: podLabels},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning, PodIP: "10.0.0.9",
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}},
		},
	}
}

func slicesRefused(h *fakeHost) {
	h.listErr = map[string]error{"endpointslices": errors.New("endpointslices is forbidden")}
}

// A person who may list pods and Services but not EndpointSlices: the pods
// behind each Service come from its selector, Ready from each pod.
func TestRefusedSlicesAttachPodsBySelectorInOneNamespace(t *testing.T) {
	h := newHost()
	slicesRefused(h)
	app := map[string]string{"app.kubernetes.io/name": "valkey"}
	h.selectedService(t, "cache", "vk", app, corev1.ServicePort{Name: "tcp", Port: 6379})
	h.add(t, "pods", runningPod("cache", "vk-0", app, true))
	h.add(t, "pods", runningPod("cache", "vk-1", app, false))
	h.add(t, "pods", runningPod("cache", "other-0", map[string]string{"app": "web"}, true))
	done := runningPod("cache", "vk-old", app, false)
	done.Status.Phase = corev1.PodSucceeded
	h.add(t, "pods", done)
	waiting := runningPod("cache", "vk-2", app, false)
	waiting.Status.PodIP = ""
	h.add(t, "pods", waiting)

	found, err := discover(httptest.NewRequest("GET", "/", nil), h, "kind", "cache")
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Instances) != 1 {
		t.Fatalf("instances = %+v", found.Instances)
	}
	got := found.Instances[0]
	if !reflect.DeepEqual(got.Pods, []string{"vk-0", "vk-1"}) || got.Ready != 1 || got.Endpointless {
		t.Fatalf("pods = %v ready = %d endpointless = %v", got.Pods, got.Ready, got.Endpointless)
	}
	if found.SlicesNote == "" {
		t.Fatal("the note must still say the slices could not be read")
	}

	// The grouping takes the pods already read: no second listing.
	before := 0
	for _, l := range h.listed {
		if l == "pods/cache" {
			before++
		}
	}
	pods := readPods(httptest.NewRequest("GET", "/", nil), h, "kind", found)
	after := 0
	for _, l := range h.listed {
		if l == "pods/cache" {
			after++
		}
	}
	if before != 1 || after != 1 || pods.Pods["cache/vk-0"] == nil {
		t.Fatalf("pod listings %d then %d, pods %v", before, after, pods.Pods)
	}
}

// The pod's tile reads the namespace's inventory: with the slices refused, the
// pod is still a member.
func TestRefusedSlicesStillNameThePodInTheInventory(t *testing.T) {
	h := newHost()
	slicesRefused(h)
	app := map[string]string{"app": "valkey"}
	h.selectedService(t, "cache", "valkey", app, corev1.ServicePort{Name: "redis", Port: 6379})
	h.add(t, "pods", runningPod("cache", "valkey-0", app, true))

	res := serve(t, h, "/x/kind/redis/?ns=cache")
	if res.Code != 200 {
		t.Fatalf("status %d: %s", res.Code, res.Body)
	}
	var answer inventoryAnswer
	if err := json.Unmarshal(res.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if len(answer.Namespaces) != 1 || len(answer.Namespaces[0].Deployments) != 1 {
		t.Fatalf("answer = %+v", answer)
	}
	member := answer.Namespaces[0].Deployments[0].Members[0]
	if !reflect.DeepEqual(member.Pods, []string{"valkey-0"}) || member.Ready != 1 || !member.Reachable {
		t.Fatalf("member = %+v", member)
	}
}

// Pods refused too: nothing is established, pods stay unknown as before.
func TestRefusedSlicesAndPodsKeepPodsUnknown(t *testing.T) {
	h := newHost()
	slicesRefused(h)
	h.listErr["pods"] = errors.New("pods is forbidden")
	h.selectedService(t, "cache", "vk", map[string]string{"app": "valkey"}, corev1.ServicePort{Name: "redis", Port: 6379})

	found, err := discover(httptest.NewRequest("GET", "/", nil), h, "kind", "cache")
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Instances) != 1 || found.Instances[0].Pods != nil || found.Instances[0].Endpointless {
		t.Fatalf("pods must stay unknown: %+v", found.Instances)
	}
}

// A partial pod listing cannot say what a selector selects.
func TestRefusedSlicesAndPartialPodsKeepPodsUnknown(t *testing.T) {
	h := newHost()
	slicesRefused(h)
	h.narrowed = map[string]bool{"pods": true}
	app := map[string]string{"app": "valkey"}
	h.selectedService(t, "cache", "vk", app, corev1.ServicePort{Name: "redis", Port: 6379})
	h.add(t, "pods", runningPod("cache", "vk-0", app, true))

	found, err := discover(httptest.NewRequest("GET", "/", nil), h, "kind", "cache")
	if err != nil {
		t.Fatal(err)
	}
	if found.Instances[0].Pods != nil {
		t.Fatalf("pods must stay unknown: %+v", found.Instances[0])
	}
}

// A Service with no selector has endpoints written by hand: only the slices
// say what is behind it.
func TestServiceWithoutSelectorKeepsPodsUnknown(t *testing.T) {
	h := newHost()
	slicesRefused(h)
	h.selectedService(t, "cache", "external", nil, corev1.ServicePort{Name: "redis", Port: 6379})
	h.add(t, "pods", runningPod("cache", "vk-0", map[string]string{"app": "valkey"}, true))

	found, err := discover(httptest.NewRequest("GET", "/", nil), h, "kind", "cache")
	if err != nil {
		t.Fatal(err)
	}
	if found.Instances[0].Pods != nil || found.Instances[0].Endpointless {
		t.Fatalf("pods must stay unknown: %+v", found.Instances[0])
	}
}

// Across the cluster the pods are not listed for this: one listing per
// namespace would be the largest read the console makes.
func TestRefusedSlicesAcrossTheClusterListNoPods(t *testing.T) {
	h := newHost()
	slicesRefused(h)
	app := map[string]string{"app": "valkey"}
	h.selectedService(t, "cache", "vk", app, corev1.ServicePort{Name: "redis", Port: 6379})
	h.add(t, "pods", runningPod("cache", "vk-0", app, true))

	found, err := discover(httptest.NewRequest("GET", "/", nil), h, "kind", "")
	if err != nil {
		t.Fatal(err)
	}
	if found.Instances[0].Pods != nil {
		t.Fatalf("pods must stay unknown: %+v", found.Instances[0])
	}
	for _, l := range h.listed {
		if l == "pods/" || l == "pods/cache" {
			t.Fatalf("listed %s", l)
		}
	}
}

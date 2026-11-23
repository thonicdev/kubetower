package redis

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// fronted is a Service with labels, a selector and ports, an EndpointSlice
// naming the pod, and the pod running image with containerPorts.
func (h *fakeHost) fronted(t *testing.T, namespace, name string, serviceLabels, selector map[string]string, ports []corev1.ServicePort, image string, containerPorts ...int32) {
	t.Helper()
	h.add(t, "services", &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: serviceLabels},
		Spec:       corev1.ServiceSpec{Selector: selector, Ports: ports},
	})
	podName := name + "-0"
	h.add(t, "endpointslices", &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name + "-abc",
			Labels: map[string]string{discoveryv1.LabelServiceName: name}},
		Endpoints: []discoveryv1.Endpoint{{
			Addresses: []string{"10.0.0.2"},
			TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: podName, Namespace: namespace},
		}},
	})
	container := corev1.Container{Name: "server", Image: image}
	for _, p := range containerPorts {
		container.Ports = append(container.Ports, corev1.ContainerPort{ContainerPort: p})
	}
	h.add(t, "pods", &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: podName, Labels: selector},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{container}},
	})
}

func doors(t *testing.T, h *fakeHost, namespace string) []string {
	t.Helper()
	found, err := discover(httptest.NewRequest("GET", "/", nil), h, "kind", namespace)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, i := range found.Instances {
		out = append(out, i.Namespace+"/"+i.Service+":"+itoa(i.Port)+" "+i.Why)
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }

var (
	cacheSelector = map[string]string{"app": "cache"}
	errForbidden  = errors.New("pods is forbidden")
)

// A StatefulSet of valkey/valkey behind a port named `client` on 6380: its
// pod runs a Valkey image on that port.
func TestAValkeyImageBehindAnotherPortIsFound(t *testing.T) {
	h := newHost()
	h.fronted(t, "rv", "valkey", nil, cacheSelector,
		[]corev1.ServicePort{{Name: "client", Port: 6380, TargetPort: intstr.FromInt32(6380)}},
		"docker.io/valkey/valkey:8.1", 6380)
	if got := doors(t, h, "rv"); !reflect.DeepEqual(got, []string{"rv/valkey:6380 its pod runs valkey"}) {
		t.Fatalf("doors = %v", got)
	}
}

// A named targetPort reaches the container port of that name.
func TestANamedTargetPortReachesTheRedisContainer(t *testing.T) {
	h := newHost()
	h.add(t, "services", &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "rv", Name: "kv"},
		Spec: corev1.ServiceSpec{Selector: cacheSelector, Ports: []corev1.ServicePort{
			{Name: "web", Port: 8080, TargetPort: intstr.FromString("ui")},
			{Name: "tcp", Port: 7000, TargetPort: intstr.FromString("db")},
		}},
	})
	h.add(t, "pods", &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "rv", Name: "kv-0", Labels: cacheSelector},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "ui", Image: "nginx:1", Ports: []corev1.ContainerPort{{Name: "ui", ContainerPort: 80}}},
			{Name: "db", Image: "bitnami/redis@sha256:abc", Ports: []corev1.ContainerPort{{Name: "db", ContainerPort: 7001}}},
		}},
	})
	if got := doors(t, h, "rv"); !reflect.DeepEqual(got, []string{"rv/kv:7000 its pod runs redis"}) {
		t.Fatalf("doors = %v", got)
	}
}

// Two ports, only one of which the Valkey container listens on: that one.
func TestOnlyThePortTheContainerListensOnIsTaken(t *testing.T) {
	h := newHost()
	h.fronted(t, "rv", "cache", nil, cacheSelector, []corev1.ServicePort{
		{Name: "admin", Port: 8081},
		{Name: "client", Port: 6380},
	}, "valkey/valkey:8.1", 6380)
	if got := doors(t, h, "rv"); !reflect.DeepEqual(got, []string{"rv/cache:6380 its pod runs valkey"}) {
		t.Fatalf("doors = %v", got)
	}
}

// Across the cluster no pod is read; the label says it, on the Service or in
// its selector, when there is one port to take.
func TestTheNameLabelIsEvidenceAcrossTheCluster(t *testing.T) {
	h := newHost()
	h.fronted(t, "rv", "cache", map[string]string{"app.kubernetes.io/name": "valkey"}, cacheSelector,
		[]corev1.ServicePort{{Name: "tcp", Port: 7000}}, "example/cache:1")
	h.fronted(t, "rv", "kv", nil, map[string]string{"app.kubernetes.io/name": "Redis"},
		[]corev1.ServicePort{{Port: 6390}}, "example/kv:1")
	got := doors(t, h, "")
	want := []string{"rv/cache:7000 it is labeled app.kubernetes.io/name=valkey", "rv/kv:6390 it is labeled app.kubernetes.io/name=Redis"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("doors = %v", got)
	}
	for _, l := range h.listed {
		if l == "pods/" || l == "pods/rv" {
			t.Fatalf("a cluster-wide read listed %s", l)
		}
	}
}

// The found door has its pod behind it, so a pod's tile names it.
func TestAPodBehindAnEvidencedServiceIsAMember(t *testing.T) {
	h := newHost()
	h.fronted(t, "rv", "valkey", nil, cacheSelector, []corev1.ServicePort{{Name: "client", Port: 6380}}, "valkey/valkey:8.1", 6380)
	res := serve(t, h, "/x/kind/redis/?ns=rv")
	var answer inventoryAnswer
	if err := json.Unmarshal(res.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if len(answer.Namespaces) != 1 {
		t.Fatalf("answer = %s", res.Body)
	}
	member := answer.Namespaces[0].Deployments[0].Members[0]
	if !reflect.DeepEqual(member.Pods, []string{"valkey-0"}) || member.Doors[0].Port != 6380 {
		t.Fatalf("member = %+v", member)
	}
}

// Nothing to tell two ports apart: neither is guessed.
func TestTwoPortsAndOnlyALabelAreLeftAlone(t *testing.T) {
	h := newHost()
	h.fronted(t, "rv", "cache", map[string]string{"app.kubernetes.io/name": "valkey"}, cacheSelector, []corev1.ServicePort{
		{Name: "a", Port: 7000},
		{Name: "b", Port: 7001},
	}, "example/cache:1")
	if got := doors(t, h, "rv"); len(got) != 0 {
		t.Fatalf("doors = %v", got)
	}
}

// An exporter, a Sentinel, the cluster bus and a web port are never the server.
func TestPortsThatAreNotTheServerAreRefused(t *testing.T) {
	h := newHost()
	label := map[string]string{"app.kubernetes.io/name": "valkey"}
	for _, port := range []corev1.ServicePort{
		{Name: "metrics", Port: 9000},
		{Name: "valkey-metrics", Port: 9121},
		{Name: "tcp-bus", Port: 17000},
		{Name: "http", Port: 8080},
		{Name: "tcp", Port: 16379},
		{Name: "dns", Port: 7000, Protocol: corev1.ProtocolUDP},
	} {
		h.fronted(t, "rv", "svc-"+itoa(int(port.Port)), label, cacheSelector, []corev1.ServicePort{port}, "valkey/valkey:8.1", port.Port)
	}
	if got := doors(t, h, "rv"); len(got) != 0 {
		t.Fatalf("doors = %v", got)
	}
}

// An unrelated Service is left alone.
func TestAnUnrelatedServiceIsLeftAlone(t *testing.T) {
	h := newHost()
	h.fronted(t, "rv", "web", map[string]string{"app.kubernetes.io/name": "nginx"}, cacheSelector,
		[]corev1.ServicePort{{Name: "tcp", Port: 7000}}, "nginx:1", 7000)
	h.fronted(t, "rv", "rediscommander", nil, map[string]string{"app": "ui"},
		[]corev1.ServicePort{{Name: "tcp", Port: 8081}}, "rediscommander/redis-commander:latest", 8081)
	if got := doors(t, h, "rv"); len(got) != 0 {
		t.Fatalf("doors = %v", got)
	}
}

// A Service the port guess took keeps only what the guess took: its other
// ports are not added by the label.
func TestAGuessedServiceIsNotWidened(t *testing.T) {
	h := newHost()
	h.fronted(t, "rv", "vk", map[string]string{"app.kubernetes.io/name": "valkey"}, cacheSelector, []corev1.ServicePort{
		{Name: "tcp-redis", Port: 6379},
		{Name: "extra", Port: 7000},
	}, "valkey/valkey:8.1", 6379, 7000)
	if got := doors(t, h, "rv"); !reflect.DeepEqual(got, []string{"rv/vk:6379 the port is named tcp-redis"}) {
		t.Fatalf("doors = %v", got)
	}
}

// Pods refused: the label is still evidence, the image is not.
func TestRefusedPodsLeaveTheLabel(t *testing.T) {
	h := newHost()
	h.listErr = map[string]error{"pods": errForbidden}
	h.fronted(t, "rv", "cache", map[string]string{"app.kubernetes.io/name": "valkey"}, cacheSelector,
		[]corev1.ServicePort{{Name: "tcp", Port: 7000}}, "valkey/valkey:8.1", 7000)
	h.fronted(t, "rv", "plain", nil, map[string]string{"app": "plain"},
		[]corev1.ServicePort{{Name: "client", Port: 6380}}, "valkey/valkey:8.1", 6380)
	if got := doors(t, h, "rv"); !reflect.DeepEqual(got, []string{"rv/cache:7000 it is labeled app.kubernetes.io/name=valkey"}) {
		t.Fatalf("doors = %v", got)
	}
}

// An image an internal registry renamed still says what it runs: the word may
// be inside a segment and in any segment of the path; a tool beside the
// server is not one.
func TestRedisImage(t *testing.T) {
	for image, want := range map[string]string{
		"valkey/valkey:8.1":                            "valkey",
		"docker.io/bitnami/valkey:8.1.3":               "valkey",
		"redis":                                        "redis",
		"registry:5000/team/redis@sha256:abc":          "redis",
		"Valkey/Valkey":                                "valkey",
		"registry.example.com/infra/infravalkey:8":     "infravalkey",
		"registry.example.com/infra/infra-valkey:8.1":  "infra-valkey",
		"registry.example.com/platform/infraredis:7.2": "infraredis",
		"docker.io/bitnami/redis-cluster:7.2":          "redis-cluster",
		"redis/redis-stack-server:7.4":                 "redis-stack-server",
		"registry.example.com/redis/server:7":          "redis",
		"ghcr.io/x/redis-exporter:1":                   "",
		"oliver006/redis_exporter:v1.62":               "",
		"rediscommander/redis-commander:latest":        "",
		"redis/redisinsight:2":                         "",
		"bitnami/redis-sentinel:7.2":                   "",
		"registry.example.com/redis/exporter:1":        "",
		"quay.io/opstree/redis-operator:v0.18":         "",
		"nginx:1.27":                                   "",
		"redis.registry.example.com/team/nginx:1":      "",
		"registry.example.com/infra/infravalky:8":      "",
	} {
		got, ok := redisImage(image)
		if ok != (want != "") || got != want {
			t.Errorf("redisImage(%q) = %q, %v; want %q", image, got, ok, want)
		}
	}
}

// A Valkey from an internal registry, renamed, behind a port named neither
// like a Redis nor on 6379.
func TestARenamedValkeyImageIsFound(t *testing.T) {
	h := newHost()
	h.fronted(t, "rv", "valkey", nil, cacheSelector,
		[]corev1.ServicePort{{Name: "tcp-6380", Port: 6380, TargetPort: intstr.FromInt32(6380)}},
		"registry.example.com/infra/infravalkey:8", 6380)
	if got := doors(t, h, "rv"); !reflect.DeepEqual(got, []string{"rv/valkey:6380 its pod runs infravalkey"}) {
		t.Fatalf("doors = %v", got)
	}
}

// An exporter's image behind the only port is not a Redis.
func TestAnExporterImageIsNotARedis(t *testing.T) {
	h := newHost()
	h.fronted(t, "rv", "exporter", nil, cacheSelector,
		[]corev1.ServicePort{{Name: "scrape", Port: 9200, TargetPort: intstr.FromInt32(9200)}},
		"registry.example.com/infra/redis-exporter:1", 9200)
	if got := doors(t, h, "rv"); len(got) != 0 {
		t.Fatalf("doors = %v", got)
	}
}

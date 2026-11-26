package redis

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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

// Labels that only contain the word, on the Service, its selector or its pod,
// say what it fronts; a tool's label does not.
func TestALabelContainingTheWordIsEvidence(t *testing.T) {
	h := newHost()
	h.fronted(t, "rv", "a", map[string]string{"app": "redis"}, map[string]string{"x": "a"},
		[]corev1.ServicePort{{Name: "client", Port: 7000}}, "example/cache:1")
	h.fronted(t, "rv", "b", nil, map[string]string{"app.kubernetes.io/name": "redis-cluster"},
		[]corev1.ServicePort{{Name: "client", Port: 7001}}, "example/cache:1")
	h.fronted(t, "rv", "c", nil, map[string]string{"app": "infra-valkey"},
		[]corev1.ServicePort{{Name: "client", Port: 7002}}, "example/cache:1")
	h.fronted(t, "rv", "d", map[string]string{"app": "redis-exporter"}, map[string]string{"x": "d"},
		[]corev1.ServicePort{{Name: "scrape", Port: 7003}}, "example/cache:1")
	want := []string{
		"rv/a:7000 it is labeled app=redis",
		"rv/b:7001 it is labeled app.kubernetes.io/name=redis-cluster",
		"rv/c:7002 it is labeled app=infra-valkey",
	}
	if got := doors(t, h, "rv"); !reflect.DeepEqual(got, want) {
		t.Fatalf("doors = %v", got)
	}
}

// With a second port, the one alone that looks like a Redis's is the server.
func TestALabelPicksTheRedisPortAmongSeveral(t *testing.T) {
	h := newHost()
	h.fronted(t, "rv", "tls", map[string]string{"app": "valkey"}, map[string]string{"x": "tls"}, []corev1.ServicePort{
		{Name: "redis-tls", Port: 7000},
		{Name: "admin-ui", Port: 7001},
	}, "example/cache:1")
	h.fronted(t, "rv", "num", map[string]string{"app": "valkey"}, map[string]string{"x": "num"}, []corev1.ServicePort{
		{Name: "a", Port: 6380},
		{Name: "b", Port: 7001},
	}, "example/cache:1")
	want := []string{"rv/num:6380 it is labeled app=valkey", "rv/tls:7000 it is labeled app=valkey"}
	if got := doors(t, h, "rv"); !reflect.DeepEqual(got, want) {
		t.Fatalf("doors = %v", got)
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
		"valkey/valkey:8.1":                             "valkey",
		"docker.io/bitnami/valkey:8.1.3":                "valkey",
		"redis":                                         "redis",
		"registry:5000/team/redis@sha256:abc":           "redis",
		"Valkey/Valkey":                                 "valkey",
		"registry.example.com/infra/infravalkey:8":      "infravalkey",
		"registry.example.com/infra/infra-valkey:8.1":   "infra-valkey",
		"registry.example.com/platform/infraredis:7.2":  "infraredis",
		"docker.io/bitnami/redis-cluster:7.2":           "redis-cluster",
		"redis/redis-stack-server:7.4":                  "redis-stack-server",
		"registry.example.com/redis/server:7":           "redis",
		"ghcr.io/x/redis-exporter:1":                    "",
		"oliver006/redis_exporter:v1.62":                "",
		"rediscommander/redis-commander:latest":         "",
		"redis/redisinsight:2":                          "",
		"bitnami/redis-sentinel:7.2":                    "",
		"registry.example.com/redis/exporter:1":         "",
		"quay.io/opstree/redis-operator:v0.18":          "",
		"nginx:1.27":                                    "",
		"redis.registry.example.com/team/nginx:1":       "",
		"registry.example.com/infra/infravalky:8":       "",
		"registry.example.com/infra/redis-build:7":      "redis-build",
		"redis/redis-cli:7":                             "",
		"registry.example.com/infra/redis-tools:7":      "",
		"registry.example.com/infra/redis-backup:1":     "",
		"registry.example.com/infra/redis-dump:1":       "",
		"registry.example.com/infra/redis-shake:4":      "",
		"p3x/p3x-redis-ui:2024":                         "",
		"registry.example.com/app/myapp-redis-client:1": "",
		"registry.example.com/redis/exporter/v2:1":      "",
		"registry.example.com/redis-exporter/build:1":   "",
		"registry.example.com/team-redis/web-app:3":     "",
		"redis/nginx:1":                                 "",
		"docker.io/redis/nginx:1":                       "",
		"registry.example.com/infra-tools/redis:7":      "redis",
		"registry.example.com/infra/redis-dashboard:1":  "",
		"registry.example.com/infra/redisui:1":          "",
		"redislabs/rebloom:2":                           "rebloom",
		"redislabs/rejson:2.6":                          "rejson",
		"ghcr.io/redis/nginx:1":                         "",
		"harbor.example/hub-proxy/redis/nginx:1":        "",
		"harbor.example/hub-proxy/redis/server:7":       "redis",
		"registry.example.com/team/redis-webui:1":       "",
		"registry.example.com/team/webapp-redis:1":      "",
		"redislabs/redisearch:2":                        "redisearch",
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

// A label names a server, not a tool beside it, whether the tool is a word of
// its own or glued to the engine's name.
// The label rule's table is shared with the UI's (shortcut.ts), which reads
// the same file.
func TestLabelSays(t *testing.T) {
	raw, err := os.ReadFile("testdata/label-rule.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Labels map[string]bool `json:"labels"`
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatal(err)
	}
	if len(table.Labels) < 20 {
		t.Fatalf("the table holds %d labels", len(table.Labels))
	}
	for value, want := range table.Labels {
		if got := labelSays(value); got != want {
			t.Errorf("labelSays(%q) = %v; want %v", value, got, want)
		}
	}
}

// labelledPod is a Service selecting a pod by selector, the pod carrying
// podLabels on top, its one container listening on containerPorts.
func (h *fakeHost) labelledPod(t *testing.T, name string, selector, podLabels map[string]string, ports []corev1.ServicePort, containerPorts ...int32) {
	t.Helper()
	h.fronted(t, "rv", name, nil, selector, ports, "example/cache:1", containerPorts...)
	h.objects["pods"] = h.objects["pods"][:len(h.objects["pods"])-1] // replaced below, with podLabels
	all := map[string]string{}
	for k, v := range selector {
		all[k] = v
	}
	for k, v := range podLabels {
		all[k] = v
	}
	container := corev1.Container{Name: "server", Image: "example/cache:1"}
	for _, p := range containerPorts {
		container.Ports = append(container.Ports, corev1.ContainerPort{ContainerPort: p})
	}
	h.add(t, "pods", &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "rv", Name: name + "-0", Labels: all},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{container}},
	})
}

// A label only on the pod, not in the selector, is evidence for the port that
// reaches the pod; a port that reaches none of its containers is not.
func TestAPodLabelCountsForThePortThatReachesThePod(t *testing.T) {
	h := newHost()
	redis := map[string]string{"app.kubernetes.io/name": "redis"}
	h.labelledPod(t, "kv", map[string]string{"x": "kv"}, redis,
		[]corev1.ServicePort{{Name: "client", Port: 7000}}, 7000)
	h.labelledPod(t, "web", map[string]string{"x": "web"}, redis,
		[]corev1.ServicePort{{Name: "web", Port: 8080}}, 6379)
	// A pod that declares no port says nothing about which port is its
	// Redis: its label counts only for a port that looks like one (#151).
	h.labelledPod(t, "bare", map[string]string{"x": "bare"}, redis,
		[]corev1.ServicePort{{Name: "client", Port: 7001}})
	h.labelledPod(t, "bare-redis", map[string]string{"x": "bare-redis"}, redis,
		[]corev1.ServicePort{{Name: "client", Port: 6380}})
	want := []string{
		"rv/bare-redis:6380 it is labeled app.kubernetes.io/name=redis",
		"rv/kv:7000 it is labeled app.kubernetes.io/name=redis",
	}
	if got := doors(t, h, "rv"); !reflect.DeepEqual(got, want) {
		t.Fatalf("doors = %v", got)
	}
}

// Two shards on 6380 and 6381: both look like a Redis's, neither is guessed.
func TestTwoRedisLikePortsAreLeftAlone(t *testing.T) {
	h := newHost()
	h.fronted(t, "rv", "shards", map[string]string{"app": "valkey"}, map[string]string{"x": "shards"}, []corev1.ServicePort{
		{Name: "a", Port: 6380},
		{Name: "b", Port: 6381},
	}, "example/cache:1")
	if got := doors(t, h, "rv"); len(got) != 0 {
		t.Fatalf("doors = %v", got)
	}
}

// A container runs the server when redis-server or valkey-server is its
// program, never when a command names it as a host or runs a Sentinel.
func TestServerCommand(t *testing.T) {
	for _, c := range []struct {
		command, args []string
		want          string
	}{
		{[]string{"redis-server"}, []string{"/etc/redis/redis.conf"}, "redis-server"},
		{[]string{"/usr/local/bin/valkey-server"}, nil, "valkey-server"},
		{nil, []string{"valkey-server", "--port", "6390"}, "valkey-server"},
		{[]string{"tini", "--", "redis-server"}, nil, "redis-server"},
		{[]string{"sh", "-c"}, []string{`exec redis-server /etc/redis.conf --port "6380"`}, "redis-server"},
		{[]string{"/bin/bash", "-ec", "cp /tmp/a /data/a && MAXMEM=1g /opt/bin/valkey-server /data/valkey.conf"}, nil, "valkey-server"},
		{[]string{"sh", "-c", "until nc -z cache 6379; do sleep 1; done\nexec redis-server"}, nil, "redis-server"},
		{[]string{"redis-server", "/etc/sentinel.conf", "--sentinel"}, nil, ""},
		{[]string{"redis-cli", "-h", "redis-server", "ping"}, nil, ""},
		{[]string{"/bin/redis_exporter"}, []string{"--redis.addr", "redis://redis-server:6379"}, ""},
		{[]string{"sh", "-c", "nc -z redis-server 6379"}, nil, ""},
		{[]string{"echo", "sh", "-c", "redis-server"}, nil, ""},
		{[]string{"sh"}, []string{"/scripts/start.sh"}, ""},
		// #72's review: a script named after the server, a version check, a
		// -c that is the script's, launchers and loops, KeyDB.
		{[]string{"redis-server.sh"}, nil, ""},
		{[]string{"/opt/redis-server.sh", "redis-server"}, nil, ""},
		{[]string{"sh", "-c", "redis-server --version; exec /app"}, nil, ""},
		{[]string{"sh", "-c", `echo "starting; redis-server"; exec /app`}, nil, ""},
		{[]string{"sh", "/start.sh", "-c", "redis-server"}, nil, ""},
		{[]string{"sh", "-c", "if [ -f /data/x ]; then exec redis-server /etc/r.conf; fi"}, nil, "redis-server"},
		{[]string{"sh", "-c", "while true; do redis-server; sleep 1; done"}, nil, "redis-server"},
		{[]string{"tini", "-s", "--", "redis-server"}, nil, "redis-server"},
		{[]string{"gosu", "redis", "redis-server"}, nil, "redis-server"},
		{[]string{"su-exec", "redis:redis", "valkey-server", "/etc/valkey.conf"}, nil, "valkey-server"},
		{[]string{"env", "-i", "A=b", "redis-server"}, nil, "redis-server"},
		{[]string{"keydb-server", "/etc/keydb/keydb.conf"}, nil, "keydb-server"},
		{nil, nil, ""},
	} {
		got, ok := serverCommand(corev1.Container{Command: c.command, Args: c.args})
		if ok != (c.want != "") || got != c.want {
			t.Errorf("serverCommand(%q, %q) = %q, %v; want %q", c.command, c.args, got, ok, c.want)
		}
	}
}

// A Service on `client` 6380 in front of a pod whose image says nothing and
// whose container runs redis-server on that port.
func TestAContainerRunningRedisServerIsFound(t *testing.T) {
	h := newHost()
	h.add(t, "services", &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "rv", Name: "kv"},
		Spec: corev1.ServiceSpec{Selector: cacheSelector, Ports: []corev1.ServicePort{
			{Name: "web", Port: 8080, TargetPort: intstr.FromInt32(8080)},
			{Name: "client", Port: 6380, TargetPort: intstr.FromInt32(6380)},
		}},
	})
	h.add(t, "pods", &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "rv", Name: "kv-0", Labels: cacheSelector},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "ui", Image: "registry.example.com/base/web:2", Ports: []corev1.ContainerPort{{ContainerPort: 8080}}},
			{Name: "kv", Image: "registry.example.com/base/cache:7", Command: []string{"sh", "-c"},
				Args: []string{"exec redis-server /etc/kv.conf --port 6380"}, Ports: []corev1.ContainerPort{{ContainerPort: 6380}}},
		}},
	})
	if got := doors(t, h, "rv"); !reflect.DeepEqual(got, []string{"rv/kv:6380 its pod runs redis-server"}) {
		t.Fatalf("doors = %v", got)
	}
}

// The same pod started as a Sentinel is not a Redis.
func TestAContainerRunningASentinelIsNotARedis(t *testing.T) {
	h := newHost()
	h.add(t, "services", &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "rv", Name: "kv"},
		Spec: corev1.ServiceSpec{Selector: cacheSelector, Ports: []corev1.ServicePort{
			{Name: "client", Port: 26380, TargetPort: intstr.FromInt32(26380)},
		}},
	})
	h.add(t, "pods", &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "rv", Name: "kv-0", Labels: cacheSelector},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "kv", Image: "registry.example.com/base/cache:7", Command: []string{"redis-server", "/etc/s.conf", "--sentinel"},
				Ports: []corev1.ContainerPort{{ContainerPort: 26380}}},
		}},
	})
	if got := doors(t, h, "rv"); len(got) != 0 {
		t.Fatalf("doors = %v", got)
	}
}

// #151, item 31: a labelled pod that declares no port does not lend its label
// to a web port.
func TestAPortlessLabelledPodDoesNotMakeAWebPortARedis(t *testing.T) {
	h := newHost()
	h.labelledPod(t, "site", map[string]string{"x": "site"}, map[string]string{"app": "redis"},
		[]corev1.ServicePort{{Name: "web", Port: 8080}})
	if got := doors(t, h, "rv"); len(got) != 0 {
		t.Fatalf("doors = %v", got)
	}
}

// #151, item 32: a Redis that declares no port beside an exporter that
// declares 9121 still has its port found.
func TestAPortlessRedisBesideAnExporterIsFound(t *testing.T) {
	h := newHost()
	h.labelledPod(t, "cache", map[string]string{"x": "cache"}, map[string]string{"app": "redis"},
		[]corev1.ServicePort{{Name: "tcp", Port: 7000}, {Name: "metrics", Port: 9121}})
	h.replaceLastPod(t, func(pod *corev1.Pod) {
		pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: "exporter", Image: "example/scraper:1",
			Ports: []corev1.ContainerPort{{ContainerPort: 9121}}})
	})
	want := []string{"rv/cache:7000 it is labeled app=redis"}
	if got := doors(t, h, "rv"); !reflect.DeepEqual(got, want) {
		t.Fatalf("doors = %v", got)
	}
}

// #151, item 33: a Service labelled redis with two ports nothing in their
// names tells apart is read by the pod it selects.
func TestALabelledServiceWithTwoPortsIsReadByItsPod(t *testing.T) {
	h := newHost()
	h.labelledPod(t, "kv", map[string]string{"app": "redis"}, nil,
		[]corev1.ServicePort{{Name: "client", Port: 7000}, {Name: "peer", Port: 7100}}, 7100)
	want := []string{"rv/kv:7100 it is labeled app=redis"}
	if got := doors(t, h, "rv"); !reflect.DeepEqual(got, want) {
		t.Fatalf("doors = %v", got)
	}
}

// #72's review, item 5: a server that declares no port is found on the port
// its command names.
func TestTheCommandsPortChoosesAmongTheServicesPorts(t *testing.T) {
	h := newHost()
	h.fronted(t, "rv", "kv", nil, map[string]string{"x": "kv"},
		[]corev1.ServicePort{{Name: "a", Port: 7000, TargetPort: intstr.FromInt32(6380)}, {Name: "b", Port: 7001}}, "example/cache:1")
	h.replaceLastPod(t, func(pod *corev1.Pod) {
		pod.Spec.Containers[0].Command = []string{"redis-server", "--port", "6380"}
	})
	want := []string{"rv/kv:7000 its pod runs redis-server"}
	if got := doors(t, h, "rv"); !reflect.DeepEqual(got, want) {
		t.Fatalf("doors = %v", got)
	}
}

// replaceLastPod changes the pod added last.
func (h *fakeHost) replaceLastPod(t *testing.T, change func(*corev1.Pod)) {
	t.Helper()
	pods := h.objects["pods"]
	var pod corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(pods[len(pods)-1].Object, &pod); err != nil {
		t.Fatal(err)
	}
	change(&pod)
	h.objects["pods"] = pods[:len(pods)-1]
	h.add(t, "pods", &pod)
}

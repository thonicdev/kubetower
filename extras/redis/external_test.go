package redis

import (
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func externalService(namespace, name, host string, ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeExternalName, ExternalName: host, Ports: ports},
	}
}

func TestExternalRedis(t *testing.T) {
	for _, c := range []struct {
		name, host string
		ports      []corev1.ServicePort
		port       int
		why        string // "" means not taken
	}{
		{"sessions", "demo.abc123.ng.0001.euw1.cache.amazonaws.com", nil, 0, "it points at demo.abc123.ng.0001.euw1.cache.amazonaws.com"},
		{"sessions", "clustercfg.demo.abc123.memorydb.eu-west-1.amazonaws.com.", []corev1.ServicePort{{Name: "tcp", Port: 6380}}, 6380,
			"it points at clustercfg.demo.abc123.memorydb.eu-west-1.amazonaws.com"},
		{"cache", "demo.redis.cache.windows.net", []corev1.ServicePort{{Name: "tls", Port: 6380}}, 6380, "it points at demo.redis.cache.windows.net"},
		{"queue", "db.example.com", []corev1.ServicePort{{Name: "client", Port: 6379}}, 6379, "it is on 6379"},
		{"queue", "db.example.com", []corev1.ServicePort{{Name: "redis", Port: 7000}}, 7000, "the port is named redis"},
		{"valkey-shared", "db.example.com", nil, 0, "its name says valkey"},
		{"payments", "api.example.com", []corev1.ServicePort{{Name: "https", Port: 443}}, 0, ""},
		{"payments", "api.example.com", nil, 0, ""},
		{"redis-exporter", "demo.abc123.cache.amazonaws.com", nil, 0, ""},
		{"sessions", "demo.abc123.cache.amazonaws.com", []corev1.ServicePort{{Name: "metrics", Port: 9121}}, 0, ""},
		{"sessions", "", nil, 0, ""},
		// Inside another word, the engine says nothing (the review of #74).
		{"search", "rediscover.example.com", nil, 0, ""},
		{"search", "credis.internal", nil, 0, ""},
		{"search", "redistribution.example.com", []corev1.ServicePort{{Name: "https", Port: 443}, {Name: "grpc", Port: 9000}}, 0, ""},
		{"predis", "db.example.com", nil, 0, ""},
		{"notredis", "db.example.com", nil, 0, ""},
		{"search", "my-redis.example.com", nil, 0, "it points at my-redis.example.com"},
		{"search", "valkey8.example.com", nil, 0, "it points at valkey8.example.com"},
		// memorydb only under amazonaws.com.
		{"search", "foo.memorydb.internal", nil, 0, ""},
		{"search", "cache.example.com", nil, 0, ""},
		// Taken on its name alone: a port only when it looks like a Redis's.
		{"redis-ha", "x.example.com", []corev1.ServicePort{{Name: "api", Port: 443}}, 0, "its name says redis"},
		{"redis-ha", "x.example.com", []corev1.ServicePort{{Name: "tcp", Port: 6390}}, 6390, "its name says redis"},
		{"redis_main", "x.example.com", nil, 0, "its name says redis"},
		// Managed hosts that name neither engine (the review of #75).
		{"sessions", "demo.redisenterprise.cache.azure.net", []corev1.ServicePort{{Name: "tls", Port: 10000}}, 10000,
			"it points at demo.redisenterprise.cache.azure.net"},
		{"sessions", "demo.abc123.cache.amazonaws.com.cn", nil, 0, "it points at demo.abc123.cache.amazonaws.com.cn"},
		{"sessions", "clustercfg.demo.memorydb.cn-north-1.amazonaws.com.cn", nil, 0,
			"it points at clustercfg.demo.memorydb.cn-north-1.amazonaws.com.cn"},
		{"sessions", "pl-12345.c1.eu-west-1.ec2.cloud.redislabs.com", []corev1.ServicePort{{Name: "tcp", Port: 12345}}, 12345,
			"it points at pl-12345.c1.eu-west-1.ec2.cloud.redislabs.com"},
		// Aiven serves every engine: its host counts only with another signal.
		{"sessions", "demo-project.aivencloud.com", nil, 0, ""},
		{"orders", "pg-demo-project.aivencloud.com", []corev1.ServicePort{{Name: "postgres", Port: 5432}}, 0, ""},
		{"events", "kafka-demo-project.aivencloud.com", []corev1.ServicePort{{Name: "kafka", Port: 9092}}, 0, ""},
		{"sessions", "cache-demo-project.aivencloud.com", []corev1.ServicePort{{Name: "tcp", Port: 6390}}, 6390,
			"it points at cache-demo-project.aivencloud.com"},
		{"sessions", "cache-demo-project.aivencloud.com", []corev1.ServicePort{{Name: "rediss", Port: 12691}}, 12691,
			"it points at cache-demo-project.aivencloud.com"},
		{"sessions", "redis-demo-project.aivencloud.com", []corev1.ServicePort{{Name: "tcp", Port: 12691}}, 12691,
			"it points at redis-demo-project.aivencloud.com"},
		{"redis-sessions", "cache-demo-project.aivencloud.com", []corev1.ServicePort{{Name: "tcp", Port: 12691}}, 0,
			"its name says redis"},
		{"sessions", "demo.cache.amazonaws.com.evil.example.com", nil, 0, ""},
		{"sessions", "demo.redislabs.com.example.com", nil, 0, ""},
		// A tool in the host's first label or in one that carries the engine,
		// never in a neutral label nor in a managed host's client part (the
		// review of #76).
		{"sessions", "app.redis-exporter.example.com", nil, 0, ""},
		{"sessions", "cache.valkey-proxy.example.com", nil, 0, ""},
		{"sessions", "metrics.example.com", []corev1.ServicePort{{Name: "tcp", Port: 6379}}, 0, ""},
		{"sessions", "master.web-sessions.abc123.use1.cache.amazonaws.com", []corev1.ServicePort{{Name: "tcp", Port: 6379}}, 6379, "it is on 6379"},
		{"sessions", "master.api-proxy.abc123.use1.cache.amazonaws.com", nil, 0, "it points at master.api-proxy.abc123.use1.cache.amazonaws.com"},
		{"sessions", "clustercfg.admin-sessions.abc123.memorydb.us-east-1.amazonaws.com", nil, 0,
			"it points at clustercfg.admin-sessions.abc123.memorydb.us-east-1.amazonaws.com"},
		{"sessions", "admin.abc123.use1.cache.amazonaws.com", nil, 0, "it points at admin.abc123.use1.cache.amazonaws.com"},
		{"sessions", "redis.backups.example.com", nil, 0, "it points at redis.backups.example.com"},
		{"sessions", "redis.prod.web.corp", nil, 0, "it points at redis.prod.web.corp"},
		// AWS's own names, not endpoints.
		{"sessions", "xcache.amazonaws.com", nil, 0, ""},
		{"sessions", "memorydb.amazonaws.com", nil, 0, ""},
		{"sessions", "memorydb.us-east-1.amazonaws.com", nil, 0, ""},
		// A role glued to the engine's name, never another word.
		{"redismain", "x.example.com", nil, 0, "its name says redis"},
		{"redismaster", "x.example.com", nil, 0, "its name says redis"},
		{"valkeyprimary", "x.example.com", nil, 0, "its name says valkey"},
		{"rediscache", "x.example.com", nil, 0, "its name says redis"},
		{"redisdata", "x.example.com", nil, 0, ""},
		{"predis", "x.example.com", nil, 0, ""},
		{"notredis", "x.example.com", nil, 0, ""},
		// The port range on a name match, at its bounds, and the TLS scheme.
		{"redis-ha", "x.example.com", []corev1.ServicePort{{Name: "tcp", Port: 6378}}, 0, "its name says redis"},
		{"redis-ha", "x.example.com", []corev1.ServicePort{{Name: "tcp", Port: 6379}}, 6379, "it is on 6379"},
		{"redis-ha", "x.example.com", []corev1.ServicePort{{Name: "tcp", Port: 6399}}, 6399, "its name says redis"},
		{"redis-ha", "x.example.com", []corev1.ServicePort{{Name: "tcp", Port: 6400}}, 0, "its name says redis"},
		{"redis-ha", "x.example.com", []corev1.ServicePort{{Name: "rediss", Port: 443}}, 443, "its name says redis"},
		// A managed host keeps its one port.
		{"sessions", "demo.abc123.cache.amazonaws.com", []corev1.ServicePort{{Name: "tls", Port: 443}}, 443, "it points at demo.abc123.cache.amazonaws.com"},
	} {
		door, ok := externalRedis(externalService("apps", c.name, c.host, c.ports...))
		if ok != (c.why != "") || door.Why != c.why || (ok && door.Port != c.port) {
			t.Errorf("%s -> %q %v: got %+v, %v; want port %d, %q", c.name, c.host, c.ports, door, ok, c.port, c.why)
		}
	}

	// A ClusterIP Service is never an external one, whatever its name.
	plain := externalService("apps", "redis", "demo.abc123.cache.amazonaws.com")
	plain.Spec.Type = corev1.ServiceTypeClusterIP
	if _, ok := externalRedis(plain); ok {
		t.Error("a ClusterIP Service was taken as an ExternalName one")
	}
}

// The inventory lists an ExternalName Service to a managed Redis apart, with
// its host and port; no instance route dials it; an unrelated one is absent.
func TestAnExternalRedisIsListedAndNeverDialled(t *testing.T) {
	h := newHost()
	h.add(t, "services", externalService("apps", "sessions", "demo.abc123.cache.amazonaws.com",
		corev1.ServicePort{Name: "tcp", Port: 6379}))
	h.add(t, "services", externalService("apps", "payments", "api.example.com",
		corev1.ServicePort{Name: "https", Port: 443}))

	type external struct {
		Namespace string `json:"namespace"`
		Service   string `json:"service"`
		Host      string `json:"host"`
		Port      int    `json:"port"`
		Why       string `json:"why"`
	}
	w := serve(t, h, "/x/kind/redis/")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	body := decode[struct {
		Services   int        `json:"services"`
		Redises    int        `json:"redises"`
		External   []external `json:"external"`
		Namespaces []any      `json:"namespaces"`
	}](t, w)
	want := external{Namespace: "apps", Service: "sessions", Host: "demo.abc123.cache.amazonaws.com", Port: 6379, Why: "it is on 6379"}
	if len(body.External) != 1 || body.External[0] != want {
		t.Fatalf("external = %+v, want only %+v", body.External, want)
	}
	if body.Services != 0 || body.Redises != 0 || len(body.Namespaces) != 0 {
		t.Fatalf("the external Service was counted as an instance: %+v", body)
	}

	w = serve(t, h, "/x/kind/redis/services/apps/sessions")
	tab := decode[struct {
		Deployments []any     `json:"deployments"`
		External    *external `json:"external"`
	}](t, w)
	if w.Code != http.StatusOK || tab.External == nil || *tab.External != want || len(tab.Deployments) != 0 {
		t.Fatalf("the Service route answered %d %+v, want the external door and no deployment", w.Code, tab)
	}
	if w := serve(t, h, "/x/kind/redis/services/apps/payments"); w.Code != http.StatusNotFound ||
		decode[problemBody](t, w).Code != CodeNotRedis {
		t.Fatalf("an unrelated ExternalName Service answered %d %s", w.Code, w.Body.String())
	}

	for _, target := range []string{
		"/x/kind/redis/instances/apps/sessions/6379",
		"/x/kind/redis/instances/apps/sessions/6379/probe",
		"/x/kind/redis/instances/apps/sessions/6379/key?key=a",
	} {
		w := serve(t, h, target)
		if w.Code != http.StatusNotFound || decode[problemBody](t, w).Code != CodeInstanceGone {
			t.Fatalf("%s answered %d %s, want instance_gone", target, w.Code, w.Body.String())
		}
	}
	if h.forwards != 0 || len(h.reveals) != 0 {
		t.Fatalf("%d tunnel(s) and %d reveal(s) for an ExternalName Service", h.forwards, len(h.reveals))
	}

	// The empty cluster still answers an empty list, not null.
	w = serve(t, newHost(), "/x/kind/redis/")
	if empty := decode[struct {
		External []external `json:"external"`
	}](t, w); empty.External == nil {
		t.Fatalf("external is %s, want []", w.Body.String())
	}
}

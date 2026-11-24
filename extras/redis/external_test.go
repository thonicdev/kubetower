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

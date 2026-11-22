package redis

import (
	"errors"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A Service the inventory would offer is the evidence, counted with its
// namespaces.
func TestASuggestionCountsTheRedisServicesAndTheirNamespaces(t *testing.T) {
	h := newHost()
	h.redisService(t, "cache", "redis-master", corev1.ServicePort{Name: "tcp-redis", Port: 6379}, "redis-master-0")
	h.redisService(t, "cache", "redis-replicas", corev1.ServicePort{Name: "tcp-redis", Port: 6379}, "redis-replicas-0")
	h.redisService(t, "sessions", "valkey", corev1.ServicePort{Name: "valkey", Port: 6379}, "valkey-0")

	got, err := New().Suggest(httptest.NewRequest("GET", "/", nil), h, "kind-kubetower")
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if want := "3 Redis or Valkey Services in 2 namespaces"; got != want {
		t.Errorf("Suggest = %q, want %q", got, want)
	}
}

// An exporter's port or a Sentinel's suggests nothing on its own - the rule
// the inventory uses - and a cluster with neither a Service nor a labeled pod
// is suggested nothing.
func TestNothingThatHoldsKeysSuggestsNothing(t *testing.T) {
	h := newHost()
	h.redisService(t, "monitoring", "redis", corev1.ServicePort{Name: "metrics", Port: 9121}, "exporter-0")
	h.redisService(t, "cache", "sentinel", corev1.ServicePort{Name: "tcp-sentinel", Port: 26379}, "sentinel-0")
	h.add(t, "pods", &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "web", Name: "nginx-0",
		Labels: map[string]string{"app.kubernetes.io/name": "nginx"}}})

	got, err := New().Suggest(httptest.NewRequest("GET", "/", nil), h, "kind-kubetower")
	if err != nil || got != "" {
		t.Errorf("Suggest = %q, %v; want nothing", got, err)
	}
}

// With no Service in front, a pod labeled as a Redis or a Valkey is the
// evidence, by either label and counted once.
func TestAPodLabelledAsARedisIsTheEvidenceWithoutAService(t *testing.T) {
	h := newHost()
	h.add(t, "pods", &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "cache", Name: "redis-0",
		Labels: map[string]string{"app.kubernetes.io/name": "redis", "app": "redis"}}})
	h.add(t, "pods", &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "cache", Name: "valkey-0",
		Labels: map[string]string{"app": "valkey"}}})

	got, err := New().Suggest(httptest.NewRequest("GET", "/", nil), h, "kind-kubetower")
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if want := "2 Redis or Valkey pods in 1 namespace"; got != want {
		t.Errorf("Suggest = %q, want %q", got, want)
	}
}

// A listing that fails is an error, never "nothing found".
func TestASuggestionThatCouldNotLookIsAnError(t *testing.T) {
	h := newHost()
	h.listErr["services"] = errors.New("services is forbidden")
	if got, err := New().Suggest(httptest.NewRequest("GET", "/", nil), h, "kind-kubetower"); err == nil {
		t.Errorf("Suggest = %q with no error, after the Services listing failed", got)
	}
}

// A listing that stopped before its end gives a floor, said as one.
func TestANarrowedListingIsCountedAsAFloor(t *testing.T) {
	h := newHost()
	h.redisService(t, "cache", "redis-master", corev1.ServicePort{Name: "tcp-redis", Port: 6379}, "redis-master-0")
	h.narrowed = map[string]bool{"services": true}
	got, err := New().Suggest(httptest.NewRequest("GET", "/", nil), h, "kind-kubetower")
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if want := "At least 1 Redis or Valkey Service in 1 namespace"; got != want {
		t.Errorf("Suggest = %q, want %q", got, want)
	}
}

// The pods' fallback says a floor too when its listing stopped early.
func TestANarrowedPodListingIsCountedAsAFloor(t *testing.T) {
	h := newHost()
	h.add(t, "pods", &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "cache", Name: "redis-0",
		Labels: map[string]string{"app.kubernetes.io/name": "redis"}}})
	h.narrowed = map[string]bool{"pods": true}
	got, err := New().Suggest(httptest.NewRequest("GET", "/", nil), h, "kind-kubetower")
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if want := "At least 1 Redis or Valkey pod in 1 namespace"; got != want {
		t.Errorf("Suggest = %q, want %q", got, want)
	}
}

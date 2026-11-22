package redis

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// What is left in this package's discovery once the Kubernetes arithmetic
// lives in `svcgroup`: **the guess**, and the sentence that explains it. The
// grouping, the endpoint reading and the three-state "what is behind this" are
// tested there, once, against a stand-in Match.

// The two things that are nearly always true of a Redis, and the reason each
// row can be told apart from the next. Valkey is matched like Redis
// (same protocol, same default port).
func TestTheGuessMatchesANameOrTheNumberAndSaysWhich(t *testing.T) {
	cases := []struct {
		name   string
		port   corev1.ServicePort
		want   bool
		reason string
	}{
		{"named redis", corev1.ServicePort{Name: "redis", Port: 6379}, true, "the port is named redis"},
		{"named valkey", corev1.ServicePort{Name: "valkey", Port: 6379}, true, "the port is named valkey"},
		{"named tcp-redis on another port", corev1.ServicePort{Name: "tcp-redis", Port: 26379}, true, "the port is named tcp-redis"},
		{"named tcp-valkey on another port", corev1.ServicePort{Name: "tcp-valkey", Port: 7000}, true, "the port is named tcp-valkey"},
		{"unnamed on 6379", corev1.ServicePort{Port: 6379}, true, "it is on 6379"},
		{"named http on 6379", corev1.ServicePort{Name: "http", Port: 6379}, true, "it is on 6379"},
		{"nothing like it", corev1.ServicePort{Name: "web", Port: 8080}, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, ok := looksLikeRedis(c.port)
			if ok != c.want {
				t.Fatalf("matched = %v, want %v", ok, c.want)
			}
			if reason != c.reason {
				t.Fatalf("reason = %q, want %q", reason, c.reason)
			}
		})
	}
}

// **An exporter is refused by name even when it sits on the right number**, and
// so is a Sentinel's port: it holds no keys, so it is not offered.
//
// A redis_exporter scrapes a Redis and speaks Prometheus text, not RESP.
// Connecting to one gets a page of metrics where a handshake was expected. The
// name is tried *before* the number for exactly this.
func TestAPortNamedAsAnExporterIsRefusedEvenOnTheRedisPort(t *testing.T) {
	for _, name := range []string{"metrics", "http-metrics", "exporter", "tcp-sentinel", "sentinel"} {
		if _, ok := looksLikeRedis(corev1.ServicePort{Name: name, Port: 6379}); ok {
			t.Errorf("a port named %q on 6379 was offered as a Redis; connecting to it gets "+
				"something other than a server holding keys", name)
		}
	}
}

// And the whole thing end to end, so the wiring between this package, Host.List
// and svcgroup is exercised rather than assumed: the Services and slices read
// through the host, the Match, and the reason arriving on the row. A Service
// named `valkey` on 6379 is found as well as a Redis.
func TestDiscoverOffersWhatItFindsWithTheReasonAttached(t *testing.T) {
	h := newHost()
	h.add(t, "services", &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "cache", Name: "redis"},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{
			{Name: "redis", Port: 6379},
			{Name: "metrics", Port: 9121},
		}},
	})
	h.add(t, "services", &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "cache", Name: "valkey"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "tcp", Port: 6379}}},
	})

	found, err := discover(httptest.NewRequest("GET", "/", nil), h, "kind", "")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(found.Instances) != 2 {
		t.Fatalf("got %d instances, want the redis port and the valkey Service: %+v", len(found.Instances), found.Instances)
	}
	for _, instance := range found.Instances {
		if instance.Port != 6379 {
			t.Fatalf("%s: port = %d, want 6379 (the exporter's 9121 must not be offered)", instance.Service, instance.Port)
		}
		if instance.Why == "" {
			t.Fatalf("%s: a row with no reason gives the operator no way to tell a Redis from an exporter", instance.Service)
		}
	}
	if found.Instances[1].Service != "valkey" {
		t.Fatalf("the Service named valkey on 6379 was not discovered: %+v", found.Instances)
	}
}

func slicesListed(h *fakeHost) []string {
	var out []string
	for _, l := range h.listed {
		if strings.HasPrefix(l, "endpointslices/") {
			out = append(out, l)
		}
	}
	return out
}

// A person who may list Services and EndpointSlices in some namespaces only
// gets a cluster-wide slice listing that stops short. What is behind a Service
// is then established namespace by namespace.
func TestNarrowedSliceListingIsReadPerNamespace(t *testing.T) {
	h := newHost()
	h.narrowedWide = map[string]bool{"endpointslices": true}
	port := corev1.ServicePort{Name: "redis", Port: 6379}
	h.redisService(t, "team-a", "cache", port, "cache-0", "cache-1")
	h.redisService(t, "team-b", "cache", port, "cache-0")
	h.redisService(t, "team-c", "cache", port, "cache-0")
	h.refusedIn = map[string]error{"endpointslices/team-b": errors.New("forbidden")}

	found, err := discover(httptest.NewRequest("GET", "/", nil), h, "kind", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Instances) != 3 {
		t.Fatalf("instances = %+v", found.Instances)
	}
	for i, ns := range []string{"team-a", "team-b", "team-c"} {
		if found.Instances[i].Namespace != ns {
			t.Fatalf("order broken at %d: %+v", i, found.Instances)
		}
	}
	if got := len(found.Instances[0].Pods); got != 2 {
		t.Fatalf("team-a pods = %d, want 2", got)
	}
	if found.Instances[1].Pods != nil {
		t.Fatalf("team-b refused: pods must stay unknown, got %+v", found.Instances[1].Pods)
	}
	if got := len(found.Instances[2].Pods); got != 1 {
		t.Fatalf("team-c pods = %d, want 1", got)
	}
	if !strings.Contains(found.SlicesNote, "only partly read") {
		t.Fatalf("the note must still say the read was partial: %q", found.SlicesNote)
	}
}

// A namespace whose own slice listing is also incomplete keeps its pods unknown.
func TestIncompletePerNamespaceSliceListingKeepsPodsUnknown(t *testing.T) {
	h := newHost()
	h.narrowed = map[string]bool{"endpointslices": true} // wide and per namespace
	h.redisService(t, "team-a", "cache", corev1.ServicePort{Name: "redis", Port: 6379}, "cache-0")
	found, err := discover(httptest.NewRequest("GET", "/", nil), h, "kind", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Instances) != 1 || found.Instances[0].Pods != nil {
		t.Fatalf("pods must stay unknown: %+v", found.Instances)
	}
}

// A whole cluster-wide listing is trusted: no per-namespace reads.
func TestCompleteSliceListingMakesNoPerNamespaceReads(t *testing.T) {
	h := newHost()
	port := corev1.ServicePort{Name: "redis", Port: 6379}
	h.redisService(t, "team-a", "cache", port, "cache-0")
	h.redisService(t, "team-b", "cache", port, "cache-0")
	found, err := discover(httptest.NewRequest("GET", "/", nil), h, "kind", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Instances) != 2 || len(found.Instances[0].Pods) != 1 || len(found.Instances[1].Pods) != 1 {
		t.Fatalf("instances = %+v", found.Instances)
	}
	if got := slicesListed(h); len(got) != 1 || got[0] != "endpointslices/" {
		t.Fatalf("slice listings = %v, want the one cluster-wide read", got)
	}
	if found.SlicesNote != "" {
		t.Fatalf("note = %q", found.SlicesNote)
	}
}

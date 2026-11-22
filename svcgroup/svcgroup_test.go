package svcgroup

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// onSixThreeSevenNine is a Match standing in for a real viewer's: the port
// number, and a refusal for anything named as an exporter. The two viewers'
// own tables are tested in their own packages — what is under test here is the
// arithmetic around the match, not the match.
func onSixThreeSevenNine(port corev1.ServicePort) (string, bool) {
	switch strings.ToLower(port.Name) {
	case "metrics", "exporter":
		return "", false
	}
	if port.Port == 6379 {
		return "it is on 6379", true
	}
	return "", false
}

func service(namespace, name string, ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       corev1.ServiceSpec{Ports: ports},
	}
}

func port(name string, number int32) corev1.ServicePort {
	return corev1.ServicePort{Name: name, Port: number}
}

// slice is one EndpointSlice pointing at pods, all ready.
func slice(namespace, forService string, pods ...string) *discoveryv1.EndpointSlice {
	out := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      forService + "-abc",
			Labels:    map[string]string{discoveryv1.LabelServiceName: forService},
		},
	}
	ready := true
	for _, pod := range pods {
		out.Endpoints = append(out.Endpoints, discoveryv1.Endpoint{
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
			TargetRef:  &corev1.ObjectReference{Kind: "Pod", Name: pod},
		})
	}
	return out
}

// listed is what a caller hands Discover after listing the scope: the
// Services and the EndpointSlices among objects, in the order given.
func listed(objects ...any) ([]corev1.Service, []discoveryv1.EndpointSlice) {
	var services []corev1.Service
	var slices []discoveryv1.EndpointSlice
	for _, object := range objects {
		switch o := object.(type) {
		case *corev1.Service:
			services = append(services, *o)
		case *discoveryv1.EndpointSlice:
			slices = append(slices, *o)
		default:
			panic("listed: neither a Service nor an EndpointSlice")
		}
	}
	return services, slices
}

// discover is Discover over objects whose EndpointSlices were read.
func discover(match Match, objects ...any) []Instance {
	services, slices := listed(objects...)
	return Discover(services, slices, true, match)
}

// The finding this whole package exists for.
//
// bitnami's redis chart installs three Services onto one process; CloudNativePG
// installs `-rw`, `-ro` and `-r` onto one Postgres cluster. Before grouping, a
// picker offered them as three equal choices with nothing on screen saying they
// were one server — so an operator who opened the second, found no key, and
// concluded the cache was empty would have been reading the same instance they
// had just looked at under another name.
func TestServicesOntoOneSetOfPodsAreOneServer(t *testing.T) {
	found := discover(onSixThreeSevenNine,
		service("cache", "redis-master", port("redis", 6379)),
		service("cache", "redis-headless", port("tcp-redis", 6379)),
		service("cache", "redis", port("redis", 6379)),
		slice("cache", "redis-master", "redis-master-0"),
		slice("cache", "redis-headless", "redis-master-0"),
		slice("cache", "redis", "redis-master-0"),
	)
	if len(found) != 3 {
		t.Fatalf("expected the three Services to be found, got %d", len(found))
	}

	backends := Group(found)
	if len(backends) != 1 {
		names := []string{}
		for _, backend := range backends {
			names = append(names, Behind(backend))
		}
		t.Fatalf("three doors onto redis-master-0 grouped into %d servers: %v", len(backends), names)
	}
	if got := len(backends[0].Services); got != 3 {
		t.Fatalf("the one server kept %d of its 3 Services", got)
	}
	if want := []string{"redis-master-0"}; !equal(backends[0].Pods, want) {
		t.Fatalf("pods = %v, want %v", backends[0].Pods, want)
	}
	if backends[0].Ready != 1 {
		t.Fatalf("ready = %d, want 1", backends[0].Ready)
	}
}

// The other half, and it is the half a name-based grouping would get wrong.
//
// A shared prefix is not a shared process. Two Redises in one namespace called
// `redis-cache` and `redis-sessions` are two caches, and merging them would hide
// one of them completely — strictly worse than the duplication the grouping was
// written to remove.
func TestServicesOntoDifferentPodsStayDifferentServers(t *testing.T) {
	found := discover(onSixThreeSevenNine,
		service("app", "redis-cache", port("redis", 6379)),
		service("app", "redis-sessions", port("redis", 6379)),
		slice("app", "redis-cache", "redis-cache-0"),
		slice("app", "redis-sessions", "redis-sessions-0"),
	)
	if backends := Group(found); len(backends) != 2 {
		t.Fatalf("two separate servers grouped into %d", len(backends))
	}
}

// **A merge that was never established is not made.**
//
// When the EndpointSlices cannot be read, every Service keeps a key of its own.
// The tempting shortcut is to fall back on the name — `redis` and
// `redis-headless` share a prefix, after all — and it is the wrong trade: a
// wrong split repeats a server on the page, where a wrong merge removes one and
// tells nobody.
func TestWithoutEndpointsNothingIsMerged(t *testing.T) {
	// The EndpointSlice listing was refused (`endpointslices.discovery.k8s.io
	// is forbidden`): the caller has no slices, and says it could not read them.
	services, _ := listed(
		service("cache", "redis-master", port("redis", 6379)),
		service("cache", "redis-headless", port("redis", 6379)),
	)
	found := Discover(services, nil, false, onSixThreeSevenNine)
	if len(found) != 2 {
		t.Fatalf("unreadable endpoints lost Services: got %d of 2", len(found))
	}
	for _, instance := range found {
		if instance.Endpointless {
			t.Fatalf("%s was called endpointless when the slices were never read; "+
				"that states as fact the very thing that could not be established",
				instance.Service)
		}
	}
	if backends := Group(found); len(backends) != 2 {
		t.Fatalf("unestablished pods merged %d Services into %d groups", len(found), len(backends))
	}
}

// A Service the slices *were* read for and that has none behind it is a
// different answer from one that could not be asked about, and the page draws
// them differently. Constraint 12, at the level of one row.
func TestAServiceWithNoEndpointsIsSaidToHaveNone(t *testing.T) {
	found := discover(onSixThreeSevenNine, service("cache", "redis", port("redis", 6379)))
	if len(found) != 1 {
		t.Fatalf("got %d instances, want 1", len(found))
	}
	if !found[0].Endpointless {
		t.Fatal("a Service the listing named no slice for must be reported as having nothing behind it")
	}
}

// And the three answers wear three different sentences, because the operator
// reads the sentence rather than the struct.
func TestBehindSaysWhichOfNothingAndCouldNotLookItMeans(t *testing.T) {
	one := Behind(Backend{Pods: []string{"redis-0"}})
	if one != "redis-0" {
		t.Fatalf("one pod = %q, want its name", one)
	}
	several := Behind(Backend{Pods: []string{"redis-0", "redis-1"}})
	if !strings.Contains(several, "redis-0") || !strings.Contains(several, "redis-1") {
		t.Fatalf("several pods = %q, want both named", several)
	}

	none := Behind(Backend{Endpointless: true})
	unknown := Behind(Backend{})
	if none == unknown {
		t.Fatalf("a Service with nothing behind it and one that could not be read both say %q; "+
			"the operator cannot tell a dead server from a missing RBAC grant", none)
	}
	if strings.Contains(unknown, "nothing") {
		t.Fatalf("%q claims nothing is there, which is exactly what was not established", unknown)
	}
}

// An unready endpoint is still an endpoint: the pod is there, it is the *serving*
// count that is zero. A Redis loading a large RDB, or a Postgres replaying WAL,
// is unready for minutes and is exactly what an operator opens the page about.
func TestAnUnreadyEndpointCountsAsAPodAndNotAsReady(t *testing.T) {
	unready := false
	endpoints := slice("cache", "redis", "redis-0")
	endpoints.Endpoints[0].Conditions.Ready = &unready

	found := discover(onSixThreeSevenNine, service("cache", "redis", port("redis", 6379)), endpoints)
	if found[0].Endpointless {
		t.Fatal("a pod that is not ready is still a pod")
	}
	if found[0].Ready != 0 {
		t.Fatalf("ready = %d, want 0", found[0].Ready)
	}
}

// A nil Ready is "no opinion", which the EndpointSlice API defines as ready.
// `internal/resources` reads it the same way and says so; getting it backwards
// reports a healthy Service as serving nothing.
func TestAnUnsetReadyConditionCountsAsReady(t *testing.T) {
	endpoints := slice("cache", "redis", "redis-0")
	endpoints.Endpoints[0].Conditions.Ready = nil

	found := discover(onSixThreeSevenNine, service("cache", "redis", port("redis", 6379)), endpoints)
	if found[0].Ready != 1 {
		t.Fatalf("ready = %d, want 1 — an unset condition is ready", found[0].Ready)
	}
}

// Two ports on one Service are two things to dial and must not collapse into
// one, even though they share every pod. Redis and Sentinel commonly sit on one
// Service in the operator's chart, and so do Postgres and PgBouncer.
func TestTwoPortsOnOneServiceAreTwoDoors(t *testing.T) {
	both := func(p corev1.ServicePort) (string, bool) {
		if p.Port == 6379 || p.Port == 6380 {
			return "matched", true
		}
		return "", false
	}
	found := discover(both,
		service("cache", "redis", port("redis", 6379), port("sentinel", 6380)),
		slice("cache", "redis", "redis-0"),
	)
	if len(found) != 2 {
		t.Fatalf("got %d doors, want 2", len(found))
	}
	backends := Group(found)
	if len(backends) != 1 || len(backends[0].Services) != 2 {
		t.Fatalf("two ports on one pod set should be one server with two doors, got %d groups", len(backends))
	}
}

// An endpoint with no pod targetRef - a hand-written slice onto a server
// outside the cluster - is identified by its address, so two Services onto
// that one address are still one server.
func TestAnEndpointWithNoPodIsIdentifiedByItsAddress(t *testing.T) {
	outside := func(forService string) *discoveryv1.EndpointSlice {
		out := slice("cache", forService)
		out.Endpoints = []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.7"}}}
		return out
	}
	found := discover(onSixThreeSevenNine,
		service("cache", "redis-a", port("redis", 6379)),
		service("cache", "redis-b", port("redis", 6379)),
		outside("redis-a"),
		outside("redis-b"),
	)
	backends := Group(found)
	if len(backends) != 1 || !equal(backends[0].Pods, []string{"10.0.0.7"}) {
		t.Fatalf("two Services onto one address gave %d groups: %+v", len(backends), backends)
	}
}

// An ExternalName Service has no endpoints at all — forwarding to one is a
// tunnel to nothing — so it is never offered, whatever its ports say.
func TestAnExternalNameServiceIsNeverOffered(t *testing.T) {
	external := service("cache", "redis-elsewhere", port("redis", 6379))
	external.Spec.Type = corev1.ServiceTypeExternalName
	external.Spec.ExternalName = "redis.example.com"

	found := discover(onSixThreeSevenNine, external)
	if len(found) != 0 {
		t.Fatalf("an ExternalName Service was offered: %+v", found)
	}
}

// Find is what makes a stale bookmark say "gone" rather than "unreachable", and
// the port is part of the identity because two ports on one Service are two
// things to dial.
func TestFindMatchesOnTheWholeAddressAndNotTheNameAlone(t *testing.T) {
	found := []Instance{
		{Namespace: "cache", Service: "redis", Port: 6379},
		{Namespace: "cache", Service: "redis-headless", Port: 6379},
	}
	if _, ok := Find(found, "cache", "redis", 6379); !ok {
		t.Fatal("the instance that is there was not found")
	}
	if _, ok := Find(found, "cache", "redis", 6380); ok {
		t.Fatal("a port nothing offers was matched")
	}
	if _, ok := Find(found, "other", "redis", 6379); ok {
		t.Fatal("a Service in another namespace was matched by name alone")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

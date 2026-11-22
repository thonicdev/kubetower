// Package svcgroup finds the Services in a cluster that look like a given kind
// of server, and folds them onto the workloads they actually reach.
//
// The rule: a Service whose pods could not be established keeps a key of its
// own. Nothing is merged on names: `redis-master` and `redis-headless` share a
// prefix, `redis` and `rfr-redisfailover` do not, and `postgres-rw`,
// `postgres-ro` and `postgres-r` are three Services onto one cluster. A wrong
// merge hides a server, where a wrong split only repeats one.
//
// It reads nothing. Its callers are extras, which may import nothing of the
// core but `extension` and its public leaves (`extras/boundary_test.go`), so
// the caller lists the Services and EndpointSlices through
// `extension.Host.List`, reviewed for the person, and hands them to Discover.
// It imports no package of this module: a public package handing on
// `internal/` would slip past the boundary test.
//
// The credential walk is not here: each viewer needs different values (one
// password, or user, password and database) with ranking tables that do not
// overlap, so each keeps its own.
package svcgroup

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
)

// Match decides whether a Service port looks like the server being looked for,
// and says why.
//
// The reason must be shown: a Service named `redis-metrics` on 9121 is an
// exporter, not a Redis, and an operator who cannot see why a row was offered
// cannot tell the two apart before connecting.
type Match func(port corev1.ServicePort) (why string, ok bool)

// Instance is one Service port that matched.
type Instance struct {
	Namespace string
	Service   string
	// Port is the *service* port. A forwarder resolves a named targetPort
	// against the pod it picked, which is the only place that name means
	// anything.
	Port int
	// PortName is what that port is called, kept so a caller can decide after
	// the match: a matcher offering PostgreSQL and MySQL alike re-derives the
	// protocol from the port's name and number, and the number alone would
	// dial a MySQL on a non-default named port as a PostgreSQL.
	PortName string
	// Why is what made this look like the server, shown on the page beside it.
	Why string

	// Pods is what this Service's EndpointSlices actually point at, sorted.
	Pods []string
	// Ready is how many of those endpoints are serving. A Service whose
	// endpoints are all unready resolves in DNS and answers nothing, which must
	// be drawn differently from "nothing here".
	Ready int
	// Endpointless marks a Service no EndpointSlice points at. It is set only
	// when the slices were read: `Pods == nil` with this false means "could not
	// ask".
	Endpointless bool
}

// Backend is one actual server, and every Service that reaches it.
type Backend struct {
	// Key is the identity — the namespace plus the sorted pod names.
	Key string
	// Namespace of every Service in this group; they cannot differ, because the
	// key contains it.
	Namespace string
	// Pods is what is actually running, sorted. Empty when Endpointless or when
	// the slices could not be read.
	Pods []string
	// Ready is the highest ready count any Service in the group reported.
	Ready int
	// Endpointless says nothing is behind these Services, established rather than
	// assumed.
	Endpointless bool
	// Services are the doors onto this one server, in the order Discover sorted
	// them.
	Services []Instance
}

// Discover is the Services whose ports match, with what is behind each, built
// from objects the caller has already listed: services, and the EndpointSlices
// of the same scope.
//
// slicesRead says whether the EndpointSlice listing answered, since an empty
// slices could mean either "nothing behind it" or "could not ask": with it
// true, a Service no slice names is Endpointless; with it false, every
// Instance's pods stay unknown, and Group keeps each to itself.
//
// A failed Services listing is the caller's to report before calling this: a
// cluster that runs none of these servers is an empty answer, not a failure.
func Discover(services []corev1.Service, slices []discoveryv1.EndpointSlice, slicesRead bool, match Match) []Instance {
	var out []Instance
	for i := range services {
		service := &services[i]
		// A headless Service is reachable through its pods, so it stays;
		// ExternalName has no endpoints, and forwarding to one is a tunnel to
		// nothing.
		if service.Spec.Type == corev1.ServiceTypeExternalName {
			continue
		}
		for _, port := range service.Spec.Ports {
			why, ok := match(port)
			if !ok {
				continue
			}
			out = append(out, Instance{
				Namespace: service.Namespace,
				Service:   service.Name,
				Port:      int(port.Port),
				PortName:  port.Name,
				Why:       why,
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Port < out[j].Port
	})

	// Endpoints are what make two Services one server; when they cannot be
	// listed, the list stands ungrouped rather than the page failing.
	if slicesRead {
		attachPods(slices, out)
	}
	// Otherwise pods stay unknown rather than empty: `Pods == nil` with
	// `Endpointless == false` means "not asked", and Group keeps such a
	// Service to itself.
	return out
}

// attachPods fills in what is behind each Service from the EndpointSlices the
// caller listed.
//
// It indexes one listing of the whole scope by the
// `kubernetes.io/service-name` label, rather than a request per Service, which
// could half-fail and draw a grouping from the answers that came back.
// EndpointSlice, because Endpoints is deprecated.
func attachPods(slices []discoveryv1.EndpointSlice, instances []Instance) {
	if len(instances) == 0 {
		return
	}

	type backing struct {
		pods  map[string]bool
		ready int
	}
	behind := map[string]*backing{}
	for i := range slices {
		slice := &slices[i]
		service := slice.Labels[discoveryv1.LabelServiceName]
		if service == "" {
			continue
		}
		key := slice.Namespace + "/" + service
		found := behind[key]
		if found == nil {
			found = &backing{pods: map[string]bool{}}
			behind[key] = found
		}
		for _, endpoint := range slice.Endpoints {
			// A nil Ready means ready, by the EndpointSlice API's definition;
			// reading it backwards reports a healthy Service as serving nothing.
			if endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready {
				found.ready++
			}
			if endpoint.TargetRef != nil && endpoint.TargetRef.Kind == "Pod" && endpoint.TargetRef.Name != "" {
				found.pods[endpoint.TargetRef.Name] = true
				continue
			}
			// No targetRef (an address outside the cluster): the address is the
			// identity, and two Services onto one external server are one server.
			for _, address := range endpoint.Addresses {
				found.pods[address] = true
			}
		}
	}

	for i := range instances {
		found := behind[instances[i].Namespace+"/"+instances[i].Service]
		if found == nil {
			// The listing succeeded and named no slice for this Service:
			// nothing is behind it.
			instances[i].Endpointless = true
			continue
		}
		pods := make([]string, 0, len(found.pods))
		for name := range found.pods {
			pods = append(pods, name)
		}
		sort.Strings(pods)
		instances[i].Pods = pods
		instances[i].Ready = found.ready
		instances[i].Endpointless = len(pods) == 0
	}
}

// Group folds the Services onto the servers they actually reach.
//
// The key is the pod set: two Services with the same pods are two doors onto
// one process, drawn as one row; different pods are two servers however alike
// their names.
//
// A Service whose pods could not be established keeps a key of its own: merging
// on a guess would state as fact what this function exists to establish.
func Group(instances []Instance) []Backend {
	index := map[string]*Backend{}
	var order []*Backend

	for _, instance := range instances {
		key := groupKey(instance)
		backend := index[key]
		if backend == nil {
			backend = &Backend{
				Key:          key,
				Namespace:    instance.Namespace,
				Pods:         instance.Pods,
				Endpointless: instance.Endpointless,
			}
			index[key] = backend
			order = append(order, backend)
		}
		if instance.Ready > backend.Ready {
			backend.Ready = instance.Ready
		}
		backend.Services = append(backend.Services, instance)
	}

	out := make([]Backend, 0, len(order))
	for _, backend := range order {
		out = append(out, *backend)
	}
	return out
}

// groupKey is the identity a Backend is folded on.
func groupKey(instance Instance) string {
	if len(instance.Pods) == 0 {
		// Nothing behind it, or nothing read: this Service speaks only for
		// itself, per port, since two ports on one Service are two things
		// to dial.
		return instance.Namespace + "\x00service=" + instance.Service + ":" + strconv.Itoa(instance.Port)
	}
	return instance.Namespace + "\x00pods=" + strings.Join(instance.Pods, ",")
}

// Find is the discovered instance matching an address. A viewer consults it
// before dialling because a bookmark outlives what it points at: an
// uninstalled server must not read as a network failure.
func Find(found []Instance, namespace, service string, port int) (Instance, bool) {
	for _, instance := range found {
		if instance.Namespace == namespace && instance.Service == service && instance.Port == port {
			return instance, true
		}
	}
	return Instance{}, false
}

// Behind is the sentence naming what is actually behind a group of Services,
// which is the group's real identity.
//
// Three answers: pods, nothing, and not established. The last is a failed
// EndpointSlice listing, and folding it into "nothing is running" would report
// a healthy server as down because of a permission the console lacks.
func Behind(backend Backend) string {
	switch {
	case len(backend.Pods) == 1:
		return backend.Pods[0]
	case len(backend.Pods) > 1:
		return fmt.Sprintf("%d pods: %s", len(backend.Pods), strings.Join(backend.Pods, ", "))
	case backend.Endpointless:
		return "nothing is behind this Service — it resolves and answers nothing"
	default:
		return "what is behind this Service could not be read"
	}
}

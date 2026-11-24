package redis

import (
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// An ExternalName Service is a DNS alias: it has no endpoints and no pod, so
// a pod port-forward cannot reach what it names. One whose target is a
// managed Redis (ElastiCache, MemoryDB, Azure Cache, a Redis Cloud host) is
// still a Redis the cluster's workloads use, so the inventory lists it, as
// outside the cluster, with its host and port. It is never an instance: the
// instance routes look among discovery's Instances only, so nothing dials it.

// managedHosts are the parts of a managed Redis's host name that say so
// without the word: AWS names ElastiCache and MemoryDB endpoints this way.
var managedHosts = []string{".cache.amazonaws.com", ".memorydb."}

// externalDoor is one ExternalName Service to a Redis outside the cluster.
type externalDoor struct {
	Namespace string
	Service   string
	Host      string
	// Port is the Service's port, 0 when it declares none or several that
	// nothing tells apart.
	Port     int
	PortName string
	Why      string
}

// externalRedis reports whether an ExternalName Service points at a Redis or
// a Valkey: a port the port guess takes, a host that says Redis or Valkey or
// names a managed cache, or a Service named for one. A Service named for a
// tool (an exporter, a web client) is not, nor one whose ports are all an
// exporter's or a Sentinel's.
func externalRedis(service *corev1.Service) (externalDoor, bool) {
	if service.Spec.Type != corev1.ServiceTypeExternalName {
		return externalDoor{}, false
	}
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(service.Spec.ExternalName)), ".")
	name := strings.ToLower(service.Name)
	if host == "" || isTool(name) || isTool(strings.SplitN(host, ".", 2)[0]) {
		return externalDoor{}, false
	}
	var ports []corev1.ServicePort
	for _, port := range service.Spec.Ports {
		if !notAServerPort(port) {
			ports = append(ports, port)
		}
	}
	if len(service.Spec.Ports) > 0 && len(ports) == 0 {
		return externalDoor{}, false
	}
	out := externalDoor{Namespace: service.Namespace, Service: service.Name, Host: host}
	if len(ports) == 1 {
		out.Port, out.PortName = int(ports[0].Port), ports[0].Name
	}
	for _, port := range ports {
		if why, ok := looksLikeRedis(port); ok {
			out.Port, out.PortName, out.Why = int(port.Port), port.Name, why
			return out, true
		}
	}
	switch {
	case hasAny(host, engines) || hasAny(host, managedHosts):
		out.Why = "it points at " + host
	case hasAny(name, engines):
		out.Why = "its name says " + engineIn(name)
	default:
		return externalDoor{}, false
	}
	return out, true
}

// engineIn is the engine word a name carries.
func engineIn(name string) string {
	for _, engine := range engines {
		if strings.Contains(name, engine) {
			return engine
		}
	}
	return ""
}

// externals is every ExternalName Service to a Redis, in namespace then name
// order.
func externals(services []corev1.Service) []externalDoor {
	var out []externalDoor
	for i := range services {
		if door, ok := externalRedis(&services[i]); ok {
			out = append(out, door)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Service < out[j].Service
	})
	return out
}

// externalWire is an externalDoor on the wire.
type externalWire struct {
	Namespace string `json:"namespace"`
	Service   string `json:"service"`
	Host      string `json:"host"`
	Port      int    `json:"port,omitempty"`
	PortName  string `json:"portName,omitempty"`
	Why       string `json:"why"`
}

func wireExternal(d externalDoor) externalWire {
	return externalWire{Namespace: d.Namespace, Service: d.Service, Host: d.Host, Port: d.Port, PortName: d.PortName, Why: d.Why}
}

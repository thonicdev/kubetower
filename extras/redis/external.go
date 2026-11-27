package redis

import (
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// An ExternalName Service is a DNS alias: it has no endpoints and no pod, so
// a pod port-forward cannot reach what it names. One whose target is a
// managed Redis (ElastiCache, MemoryDB, Azure Cache, Redis Cloud, Aiven) is
// still a Redis the cluster's workloads use, so the inventory lists it, as
// outside the cluster, with its host and port. It is never an instance: the
// instance routes look among discovery's Instances only, so nothing dials it.

// managedSuffixes are the domains of a managed Redis whose hosts need not
// name the engine, and whose first label is the customer's own name: Azure
// Cache for Redis (`myproxy.redis.cache.windows.net`) and Redis Enterprise,
// and Redis Cloud (its private-link hosts included, `pl-….redislabs.com`).
var managedSuffixes = []string{".redis.cache.windows.net", ".redisenterprise.cache.azure.net", ".redislabs.com"}

// aivenSuffix is Aiven's domain, which also serves its PostgreSQL, Kafka and
// every other service: a host there counts only with a port that looks like
// a Redis's, unless its own words or the Service's name say the engine.
const aivenSuffix = ".aivencloud.com"

// awsSuffixes are AWS's domains, China's included.
var awsSuffixes = []string{".amazonaws.com", ".amazonaws.com.cn"}

// managedHost reports whether a host is a managed Redis's that names neither
// engine: one of managedSuffixes, or an AWS ElastiCache or MemoryDB endpoint
// (`….cache.amazonaws.com`, or a `memorydb` label, under `amazonaws.com` or
// `amazonaws.com.cn`).
func managedHost(host string) bool {
	for _, suffix := range managedSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	for _, suffix := range awsSuffixes {
		rest, ok := strings.CutSuffix(host, suffix)
		if !ok {
			continue
		}
		if strings.HasSuffix(rest, ".cache") {
			return true
		}
		// A cluster's labels before it: `memorydb.amazonaws.com` alone is
		// AWS's, not an endpoint.
		for i, label := range strings.Split(rest, ".") {
			if label == "memorydb" && i > 0 {
				return true
			}
		}
	}
	return false
}

// glued are the role words a name may glue to the engine's (`redismaster`,
// `valkeyprimary`).
var glued = map[string]bool{"master": true, "main": true, "primary": true, "replica": true, "cache": true}

// engineToken is the engine a host or a name says as a token of its own,
// between `.`, `-` and `_` (`demo.redis.cache.windows.net`, `redis-ha`,
// `valkey7`), or glued to a role word (`redismain`), never inside another
// word (`rediscover`, `credis`, `predis`).
func engineToken(s string) string {
	for _, token := range strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '-' || r == '_' }) {
		for _, engine := range engines {
			rest, ok := strings.CutPrefix(token, engine)
			if ok && (strings.Trim(rest, "0123456789") == "" || glued[rest]) {
				return engine
			}
		}
	}
	return ""
}

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
	if host == "" || isTool(name) {
		return externalDoor{}, false
	}
	if hostTool(host) {
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
	for _, port := range ports {
		if why, ok := looksLikeRedis(port); ok {
			out.Port, out.PortName, out.Why = int(port.Port), port.Name, why
			return out, true
		}
	}
	switch {
	case engineToken(host) != "" || managedHost(host):
		out.Why = "it points at " + host
		if len(ports) == 1 {
			out.Port, out.PortName = int(ports[0].Port), ports[0].Name
		}
	case strings.HasSuffix(host, aivenSuffix) && aivenRedisPort(ports) != nil:
		port := aivenRedisPort(ports)
		out.Why = "it points at " + host
		out.Port, out.PortName = int(port.Port), port.Name
	case engineToken(name) != "":
		// The name alone does not say which port: only one that looks like a
		// Redis's is kept (`redis-ha` with `api:443` gets its host alone).
		out.Why = "its name says " + engineToken(name)
		if len(ports) == 1 && redisLikePort(ports[0]) {
			out.Port, out.PortName = int(ports[0].Port), ports[0].Name
		}
	default:
		return externalDoor{}, false
	}
	return out, true
}

// redisLikePort is a port whose name says the engine or its TLS scheme
// (`rediss`, `valkeys`), or whose number is in the range Redis and Valkey
// deployments use (6379 to 6399).
func redisLikePort(port corev1.ServicePort) bool {
	name := strings.ToLower(port.Name)
	return engineToken(name) != "" || name == "rediss" || name == "valkeys" || (port.Port >= 6379 && port.Port <= 6399)
}

// hostTool reports whether a host names a tool beside the server: in its
// first label, or in a label that carries the engine (`app.redis-exporter`,
// `valkey-proxy.example.com`). Never in the other labels (`redis.backups`,
// `redis.prod.web.corp`), nor in the client's part of a managed host, whose
// names are the customer's (`master.web-sessions.….cache.amazonaws.com`).
//
// A label that names a cache besides the engine is a cache's name, not a
// tool's: `redis-web-cache` is the web tier's cache, `redis-web` a web client.
func hostTool(host string) bool {
	if managedHost(host) {
		return false
	}
	for i, label := range strings.Split(host, ".") {
		if (i == 0 || engineToken(label) != "") && isTool(label) && !namesACache(label) {
			return true
		}
	}
	return false
}

// namesACache is a label whose last token is `cache` beside an engine word,
// and that names no long tool word (an exporter's or an operator's).
func namesACache(label string) bool {
	tokens := strings.FieldsFunc(label, func(r rune) bool { return r == '-' || r == '_' })
	return len(tokens) > 1 && tokens[len(tokens)-1] == "cache" && engineToken(label) != "" && !hasAny(label, tools)
}

// otherEnginePorts are the ports of the other engines Aiven serves:
// PostgreSQL and PgBouncer, MySQL, Kafka, OpenSearch, Cassandra. A port on
// one is that engine's, whatever its name (`rediss` on `pg-….:5432`).
var otherEnginePorts = map[int32]bool{5432: true, 6432: true, 3306: true, 9092: true, 9200: true, 9300: true, 9042: true}

// aivenRedisPort is redisPortIn on an Aiven host, never on another engine's
// port.
func aivenRedisPort(ports []corev1.ServicePort) *corev1.ServicePort {
	for i := range ports {
		if redisLikePort(ports[i]) && !otherEnginePorts[ports[i].Port] {
			return &ports[i]
		}
	}
	return nil
}

// redisPortIn is the first port that looks like a Redis's, or nil.
func redisPortIn(ports []corev1.ServicePort) *corev1.ServicePort {
	for i := range ports {
		if redisLikePort(ports[i]) {
			return &ports[i]
		}
	}
	return nil
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

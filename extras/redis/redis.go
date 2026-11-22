// Package redis is the Redis viewer: the keys, values and memory of the Redis
// and Valkey instances a cluster runs, read-only, behind its own per-cluster
// switch.
//
// The inventory (`GET /`) opens no connection: it comes from the API server
// alone. Only an instance, its probe and its keys dial, one tunnel each, opened
// and closed inside the request, because extension tunnels are capped together
// (`extension.Host.Forward`) and probing every row on load would exhaust the
// cap and touch every Redis whenever the menu opens.
//
// Every read is reviewed for the person: Services, EndpointSlices and pods
// through `Host.List` (never `Host.Client`, which is the console's), the tunnel
// through `Host.Forward` (`create pods/portforward`), a password through
// `Host.Reveal` (`get secrets/<name>`, recorded).
//
// Read-only is enforced: every command goes through conn.go's do, which
// refuses anything outside an allowlist before writing to the socket.
//
// A password is read only after the instance refused an unauthenticated
// connection, only the one the reached pod is started with (secret.go), and
// is sent down that tunnel alone: never in a response, a log, an error or a
// URL.
package redis

import (
	"net/http"

	"github.com/thonicdev/kubetower/extension"
)

// Key is `redis`: the page's URL segment, the KT_EXTRAS name and the key the
// per-cluster switch is stored under.
const Key = "redis"

// Redis is the extension `cmd/kubetower` registers. It holds no connection,
// tunnel or credential between requests; only, per Secret handed to a pod
// whole, the name of the key its password was found under (keyNames), so the
// audited reveals of that search are not repeated on every page turn.
type Redis struct {
	names *keyNames
}

// New is the constructor `cmd/kubetower` calls.
func New() Redis { return Redis{names: newKeyNames()} }

func (Redis) Key() string   { return Key }
func (Redis) Label() string { return "Redis viewer" }

// Summary is the catalog entry.
func (Redis) Summary() string {
	return "Browse the keys, values and memory of the Redis and Valkey instances this cluster runs, " +
		"read-only, without a port-forward in one terminal and redis-cli in another."
}

// Needs includes what a password costs: the person's own `get` on the Secret
// the Redis pod names.
func (Redis) Needs() string {
	return "a Redis or Valkey Service; for one with a password, the right to read the Secret its pod is started with"
}

// Legacy is empty: the key has never been anything else.
func (Redis) Legacy() []string { return nil }

// Routes are the reads. Nothing here changes a cluster or a Redis, yet the
// three that dial are POSTs: each opens a tunnel and may record a reveal of a
// Secret as the person's, and a cross-site link can make a browser send a GET
// with the session cookie (SameSite=Lax), which the CSRF check does not hold.
// A POST must carry the console's Origin and `X-KubeTower-Request` header. The
// routes that dial nothing stay GETs.
func (x Redis) Routes(h extension.Host) []extension.Route {
	return []extension.Route{
		{Method: http.MethodGet, Pattern: "/{$}", Handler: x.inventory(h)},
		{Method: http.MethodPost, Pattern: "/instances/{namespace}/{service}/{port}", Handler: x.instance(h)},
		{Method: http.MethodPost, Pattern: "/instances/{namespace}/{service}/{port}/probe", Handler: x.probe(h)},
		{Method: http.MethodPost, Pattern: "/instances/{namespace}/{service}/{port}/key", Handler: x.key(h)},
		// The tab on a Service's panel: what is behind it, from discovery
		// alone.
		{Method: http.MethodGet, Pattern: "/services/{namespace}/{name}", Handler: x.service(h)},
	}
}

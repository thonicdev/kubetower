package web

import (
	"log"
	"net/http"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/registry"
)

// Extensions' routes live under the top-level `/x/{cluster}/` (registry.Prefix),
// not `/c/{cluster}/x/`: a method-less route, as a WebSocket upgrade needs, under
// `/c/{cluster}/` conflicts with the console's own detail route and ServeMux
// panics at startup:
//
//	/c/{cluster}/x/alfa/socket  conflicts with  GET /c/{cluster}/{kind}/{namespace}/{name}
//	"matches more methods than ..., but has a more specific path pattern"
//
// A top-level segment cannot meet any console wildcard. The cluster is a path
// segment, not a query parameter, so the gate has one place to read it from.

// mountExtensions registers every route of every registered extension, behind
// the per-cluster gate. The core mounts them, never the extension, so an
// extension cannot forget the gate.
//
// `app.Register` refuses at startup what ServeMux would panic on (a bad or
// duplicate key, a repeated wildcard, conflicting routes). Routes are asked for
// again here with a Host, so one that now cannot be expressed is skipped and
// logged, never a panic. Patterns come from `registry.Pattern`, the recipe
// Register checked.
func (s *Server) mountExtensions(mux *http.ServeMux) {
	exts := registry.Extensions()
	if len(exts) == 0 {
		return
	}
	for _, e := range exts {
		key := e.Key()
		// Its own door, carrying its key, so what it reveals is recorded as its own.
		h := s.hostFor(key)
		for _, route := range e.Routes(h) {
			// Skipped and logged: a silent skip would be a 404 found by a person.
			pattern, err := registry.Pattern(key, route)
			if err == nil {
				err = registry.Handle(mux, pattern, s.gateExtension(key, route.Handler))
			}
			if err != nil {
				log.Printf("[kubetower] extension %q asked for a route this router cannot mount (%q %q): %v",
					key, route.Method, route.Pattern, err)
			}
		}
	}
}

// gateExtension is the per-cluster gate, applied to every extension route. It
// wraps the handler rather than being called by it, so moving a route cannot
// leave its gate behind.
func (s *Server) gateExtension(key string, handler http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if handler == nil {
			http.Error(w, extraLabel(key)+" has no handler for that address.", http.StatusNotFound)
			return
		}
		// The same CSRF and Origin rules as /api/: the session is a cookie, which a
		// cross-site form post or socket would otherwise carry.
		if api.RefuseForged(w, r) {
			return
		}
		if s.refuseExtraOff(w, r, key) {
			return
		}
		// Then the person: under the server profile, the verb use on the extra. The menu
		// leaves out what this refuses.
		if s.refuseExtraUnused(w, r, key) {
			return
		}
		handler.ServeHTTP(w, s.forExtension(r))
	}
}

// forExtension is r as an extension is handed it: under the server profile,
// marked as served by a shared console (extension.SharedConsole), so an
// extension can confine what it looks for to what the deployment names.
func (s *Server) forExtension(r *http.Request) *http.Request {
	if s.inCluster {
		return extension.MarkShared(r)
	}
	return r
}

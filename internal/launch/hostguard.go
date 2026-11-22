package launch

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/auth"
)

// The desktop's Host allowlist.
//
// It stops DNS rebinding: a page on attacker.example re-points its name at
// 127.0.0.1 and its scripts become same-origin with this console. internal/api's
// Origin-versus-Host check passes by construction (both say attacker.example),
// and the loopback desktop has no password behind the cookie. So the Host is
// checked first: a desktop console answers only the names the operator's browser
// uses for this machine, apart from the three probes.
//
// The name is checked, not the port: a rebound page sends the console's own
// port anyway, and a port check would refuse the development loop (Vite's proxy
// keeps `Host: localhost:5173` so the cookie stays same-origin) and a container
// published on another port (`-p 8080:5823`).
//
// It applies to the desktop profile wherever its listener is loopback, on the
// host or in a container (which binds every interface but is reached by a
// loopback name). Not to a desktop told to listen beyond loopback, reached by
// names this process cannot know (it keeps its password, and announceListen
// says so), and not under `--incluster`, behind an ingress.
//
// /healthz, /readyz and /kt-healthz are asked by probes, load balancers and the
// image's CI step under other names, and refusing them would report a serving
// console as dead. So a GET or HEAD of exactly those paths passes whatever its
// Host, but off loopback it gets its status and a bare word: the full reason
// names kubeconfig files, errors and context counts, and a rebound page reads
// whatever is answered. The guard replaces the body, so a field a handler adds
// later cannot leak. Every other path or method is 421.
//
// It does not stop a client that sets its own Host; that is not a browser
// attack, and what keeps other machines off a published container is the
// address its port is published on.

// hostPolicy says whether this process checks Host, and why, for the start-up
// log.
func hostPolicy(listen string, inCluster, inContainer bool) (check bool, why string) {
	switch {
	case inCluster:
		return false, "Host is not checked: the server profile is reached through an ingress, by its name"
	case inContainer:
		return true, "Host is checked: only localhost and loopback addresses are answered, on any port, " +
			"which is how the host reaches a published container; the probes answer any Host, with " +
			"their status and no reason"
	case !listensOnLoopback(listen):
		return false, "Host is not checked: KT_LISTEN is beyond loopback, so other machines reach this " +
			"console by names it cannot know"
	default:
		return true, "Host is checked: only localhost and loopback addresses are answered, which is " +
			"what refuses a DNS-rebound page; the probes answer any Host, with their status and no reason"
	}
}

// listensOnLoopback reports whether a listen address binds this machine's
// loopback only. A wildcard (":5823", "0.0.0.0", "::") is not, and neither is a
// LAN address: other machines reach the console either way.
func listensOnLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	return loopbackHost(host)
}

// loopbackHost reports whether a request's Host names this machine: `localhost`
// or a loopback literal, with or without a port.
func loopbackHost(host string) bool {
	if host == "" {
		return false
	}
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		// An IPv6 literal with no port: `[::1]`.
		name = host[1 : len(host)-1]
	}
	// `localhost.` is the same name; a subdomain (`a.localhost`) is not.
	if strings.EqualFold(strings.TrimSuffix(name, "."), "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(name)
	if err != nil {
		return false
	}
	return addr.Unmap().IsLoopback()
}

// guardHost answers 421 Misdirected Request to any request whose Host is not a
// loopback name, and passes the rest to next.
//
// 421, not 403: nothing about the caller is refused; the request reached a
// server that does not serve that name. The log says it once per process, since
// a rebinding page retries.
//
// The three probes are the exception, answered with their status and a bare
// word (terseProbe).
func guardHost(next http.Handler) http.Handler {
	var said atomic.Bool
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if loopbackHost(r.Host) {
			next.ServeHTTP(w, r)
			return
		}
		if terse := terseProbe(r); terse != nil {
			// The handler writes into a writer read only for its status.
			inner := &statusOnly{header: http.Header{}}
			next.ServeHTTP(inner, r)
			w.Header().Set("Cache-Control", "no-store")
			terse(w, inner.code())
			return
		}
		if said.CompareAndSwap(false, true) {
			log.Printf("[kubetower] refused a request for Host %q: this desktop console answers only "+
				"localhost and loopback addresses. A page that re-pointed its own name at this machine "+
				"looks like this; further refusals are not logged", r.Host)
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusMisdirectedRequest)
		_, _ = io.WriteString(w, "KubeTower answers only localhost and loopback addresses on this machine.\n")
	})
}

// terseProbe returns how a probe is answered to a Host that is not this
// machine's, or nil when r is not a probe - which the guard then refuses.
//
// Exactly the three paths, whole, and only GET or HEAD: any other method would
// only reach a 405, which has no business being answered off loopback.
func terseProbe(r *http.Request) func(w http.ResponseWriter, status int) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return nil
	}
	switch r.URL.Path {
	case api.LivenessPath:
		return terseJSON("Not live")
	case api.ReadinessPath:
		return terseJSON("Not ready")
	case auth.HealthPath:
		return terseText
	}
	return nil
}

// terseJSON is `/healthz` and `/readyz` with the detail taken out: ProbeResult
// with `ok` alone, or the problem the contract declares for a failure with its
// title and code and no detail. Both shapes are api/openapi.yaml's.
func terseJSON(title string) func(w http.ResponseWriter, status int) {
	return func(w http.ResponseWriter, status int) {
		if status >= 200 && status < 300 {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "{\"ok\":true}\n")
			return
		}
		api.WriteProblem(w, status, api.ProblemInternal, title, "")
	}
}

// terseText is `/kt-healthz` with the reason taken out: `ok` or `degraded`,
// the word its full answer starts with.
func terseText(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	if status >= 200 && status < 300 {
		_, _ = io.WriteString(w, "ok\n")
		return
	}
	_, _ = io.WriteString(w, "degraded\n")
}

// statusOnly is a ResponseWriter that keeps the status and discards the rest:
// its own header map, so nothing the handler or the session guard sets is sent.
type statusOnly struct {
	header http.Header
	status int
}

func (s *statusOnly) Header() http.Header { return s.header }

// WriteHeader keeps the first final status. A 1xx is not one: a 103 followed by
// a 503 must not read as healthy off loopback.
func (s *statusOnly) WriteHeader(code int) {
	if code < 200 {
		return
	}
	if s.status == 0 {
		s.status = code
	}
}

func (s *statusOnly) Write(b []byte) (int, error) {
	s.WriteHeader(http.StatusOK)
	return len(b), nil
}

// code is what the handler answered: 200 when it wrote without a status, as
// net/http would have sent.
func (s *statusOnly) code() int {
	if s.status == 0 {
		return http.StatusOK
	}
	return s.status
}

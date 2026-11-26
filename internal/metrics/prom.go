package metrics

// Querying Prometheus.
//
// Two transports, chosen by probing (see TransportProxy and TransportForward),
// and one session type wrapped around them so a caller asking five questions
// pays for the tunnel once. That last point is not a micro-optimization: a
// port-forward costs a SPDY upgrade and roughly 1.8 s to come up, and the panel
// that needs the session asks five questions per container.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/apiraw"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/safego"
	"github.com/thonicdev/kubetower/textutil"
)

// What one Prometheus request is allowed to cost, and to return.
const (
	instantTimeout = 30 * time.Second
	rangeTimeout   = 45 * time.Second
	healthTimeout  = 15 * time.Second
	// maxPromResponse bounds a single answer. A careless selector can ask
	// Prometheus for tens of megabytes, and this process has a UI to serve.
	//
	// **On both transports**: the service proxy, which is the transport
	// preferred wherever the API server allows it, as well as the forward. An
	// answer over the cap is refused on either, with errTooLarge, rather than
	// cut short and handed back as though it were the answer: a truncated
	// answer is not an answer, and a Prometheus that is not the cluster's own
	// can send any size it likes.
	maxPromResponse = 16 << 20
)

// errTooLarge is what an answer over maxPromResponse is refused with.
var errTooLarge = fmt.Errorf("prometheus answered with more than %d MiB, which the console does not read", maxPromResponse>>20)

// capBody reads a body up to maxPromResponse and refuses the byte after it
// with errTooLarge (internal/apiraw's Cap, shared with the raw reads).
func capBody(body io.ReadCloser) io.ReadCloser {
	return apiraw.Cap(body, maxPromResponse, errTooLarge)
}

// cappedTransport puts every response body it carries behind capBody. It is
// how the cap reaches the service proxy: client-go reads a body whole, with
// no limit of its own, so the limit has to be on the body before client-go
// gets it. See cappedREST.
type cappedTransport struct{ base http.RoundTripper }

func (t cappedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if resp != nil && resp.Body != nil {
		resp.Body = capBody(resp.Body)
	}
	return resp, err
}

// cappedREST is the cluster's raw REST client, rooted at root, with
// cappedTransport in front of its transport.
//
// **Everything else is the cluster's own**: the same http.Client underneath -
// so the same credentials, the same access guard (internal/access) and the
// same connections - the same rate limiter, so this is not a per-call
// allowance (see internal/cluster's ratelimit.go), and a decoder that reads a
// JSON Status, so a 403 keeps the API server's sentence (internal/apiraw). A
// client built per call costs a struct; what is expensive to build is the
// transport, and that is shared.
//
// root is the API server's address, which restRaw reads from the config the
// cluster's clients were built from: RESTClient keeps its own private, and
// the one public way to it is building a request.
func cappedREST(shared *rest.RESTClient, root *url.URL) (*rest.RESTClient, error) {
	base := shared.Client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	limited := *shared.Client
	limited.Transport = cappedTransport{base: base}
	return rest.NewRESTClient(root, "", rest.ClientContentConfig{
		ContentType: "application/json",
		Negotiator:  runtime.NewClientNegotiator(scheme.Codecs.WithoutConversion(), schema.GroupVersion{}),
	}, shared.GetRateLimiter(), &limited)
}

// Prometheus HTTP API endpoints, relative to whichever transport is in use.
const (
	endpointQuery  = "/api/v1/query"
	endpointRange  = "/api/v1/query_range"
	endpointHealth = "/-/healthy"
)

// EnvPrometheusService is where the server profile's Prometheus is, written
// `namespace/service:port[/path]` - the form Address prints, and the Service
// port, not the pod's. The path is where the Prometheus API is served under,
// as vmselect serves it under `/select/<tenant>/prometheus`. Read at start-up
// by internal/launch, and only under --incluster: see Options.Configured for
// why the server profile is told rather than left to look.
const EnvPrometheusService = "KT_PROMETHEUS_SERVICE"

// maxTargetPath bounds the path a target may carry: a URL prefix, not a place
// to hide a query.
const maxTargetPath = 128

// ParseTarget reads a target written as Address writes it,
// `namespace/service:port`, optionally followed by a path,
// `namespace/service:port/select/0/prometheus`.
//
// Strict, because the value is a deployment's and a typo in it is otherwise a
// console with no history and a probe failing quietly behind it: the
// namespace must be a namespace name, the service a Service name, the port
// a number a Service port can be, and the path plain segments.
func ParseTarget(raw string) (*PromTarget, error) {
	namespace, after, slash := strings.Cut(raw, "/")
	service, rest, colon := strings.Cut(after, ":")
	port, path, hasPath := strings.Cut(rest, "/")
	if !slash || !colon {
		return nil, fmt.Errorf("%q is not namespace/service:port[/path], e.g. monitoring/kube-prometheus-stack-prometheus:9090", raw)
	}
	if errs := validation.IsDNS1123Label(namespace); len(errs) > 0 {
		return nil, fmt.Errorf("%q: %q is not a namespace name: %s", raw, namespace, errs[0])
	}
	if errs := validation.IsDNS1035Label(service); len(errs) > 0 {
		return nil, fmt.Errorf("%q: %q is not a Service name: %s", raw, service, errs[0])
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port {
		return nil, fmt.Errorf("%q: %q is not a port number between 1 and 65535", raw, port)
	}
	target := &PromTarget{Namespace: namespace, Service: service, Port: number}
	if hasPath {
		target.Path = "/" + path
		if err := checkTargetPath(target.Path); err != nil {
			return nil, fmt.Errorf("%q: %w", raw, err)
		}
	}
	return target, nil
}

// checkTargetPath accepts `/a/b/c`: non-empty segments of letters, digits and
// `-._~`, no `.` or `..` segment, no trailing slash. Nothing that could leave
// the Service's proxy path or carry a query.
func checkTargetPath(path string) error {
	if len(path) > maxTargetPath {
		return fmt.Errorf("the path is longer than %d characters", maxTargetPath)
	}
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("%q is not a path of plain segments, e.g. /select/0/prometheus", path)
		}
		for _, r := range segment {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-._~", r)) {
				return fmt.Errorf("%q is not a path of plain segments, e.g. /select/0/prometheus", path)
			}
		}
	}
	return nil
}

// PromTarget is the Service a cluster's Prometheus API answers on: Prometheus
// itself, or VictoriaMetrics, which serves the same API.
type PromTarget struct {
	Namespace string `json:"namespace"`
	Service   string `json:"service"`
	Port      int    `json:"port"`
	// Path is where the API is served under, empty for the root: vmselect
	// serves it under `/select/<tenant>/prometheus`. It starts with a slash
	// and has none at the end.
	Path string `json:"path,omitempty"`
}

// Address renders the target as `namespace/service:port[/path]`, which is the
// form the operator can paste into anything else that asks for one.
func (p *PromTarget) Address() string {
	return fmt.Sprintf("%s/%s:%d%s", p.Namespace, p.Service, p.Port, p.Path)
}

// proxyPath addresses Prometheus through the API server's service proxy. Going
// that way rather than reaching Prometheus directly means the cluster's own
// authentication carries the request: no second credential to manage, and it
// works for a Prometheus with no ingress at all.
func (p *PromTarget) proxyPath(endpoint string) string {
	return p.proxyRoot(p.Path + endpoint)
}

// proxyRoot is proxyPath without the target's path: the Service's root.
func (p *PromTarget) proxyRoot(endpoint string) string {
	return fmt.Sprintf("/api/v1/namespaces/%s/services/%s:%d/proxy%s",
		p.Namespace, p.Service, p.Port, endpoint)
}

// healthPaths are where the health endpoint is asked, relative to the
// Service's root, in order. Under the target's path first, as a Prometheus
// started with --web.route-prefix serves it; then at the root, where every
// VictoriaMetrics component serves it whatever path its query API is under.
func (p *PromTarget) healthPaths() []string {
	if p.Path == "" {
		return []string{endpointHealth}
	}
	return []string{p.Path + endpointHealth, endpointHealth}
}

// Which engine answers the Prometheus API: Discovery.Engine.
const (
	EnginePrometheus      = "prometheus"
	EngineVictoriaMetrics = "victoriametrics"
)

// engineOf reads the engine from a health answer: VictoriaMetrics says
// "VictoriaMetrics is Healthy.", Prometheus "Prometheus Server is Healthy.".
// fallback is what discovery expected, for an answer that names neither.
func engineOf(health []byte, fallback string) string {
	switch {
	case strings.Contains(string(health), "VictoriaMetrics"):
		return EngineVictoriaMetrics
	case strings.Contains(string(health), "Prometheus"):
		return EnginePrometheus
	case fallback != "":
		return fallback
	}
	return EnginePrometheus
}

// probeTransport works out how this cluster lets us reach Prometheus, by trying.
//
// Asking `/-/healthy` through the service proxy and getting an error is not a
// verdict on Prometheus: on an EKS context whose pods sit on a secondary VPC
// CIDR, the API server itself answers `Address is not allowed`, because the
// managed control plane refuses to proxy there. The same call against a K3S
// cluster returns 200. So the proxy is tried, and the port-forward that follows
// is tried too rather than assumed — an unverified "port-forward" would be a
// confident wrong answer, like "none" for a cluster that could not be asked.
func (m *Metrics) probeTransport(ctx context.Context, name string, client *cluster.Client, target *PromTarget) (transport string, health []byte, err error) {
	viaProxy := func(ctx context.Context, path string, params url.Values) ([]byte, error) {
		return m.raw(ctx, client, target.proxyRoot(path), params)
	}
	body, proxyErr := target.answers(ctx, healthTimeout, viaProxy)
	var wrongPath *pathRefused
	switch {
	case proxyErr == nil:
		return TransportProxy, body, nil
	case errors.As(proxyErr, &wrongPath):
		// The proxy reached the Service; a port-forward would reach the same
		// Service and be refused the same path.
		return "", nil, proxyErr
	case m.tunnels == nil:
		return "", nil, fmt.Errorf("the API service proxy refused (%s) and no port-forward is configured", shortError(proxyErr))
	}

	// The console's own tunnel, to its own monitoring backend: reviewed as the
	// console whoever's page started the probe, because what it finds is held
	// for every person (access.AsConsole).
	tunnel, err := m.tunnels.Open(access.AsConsole(ctx), name, target.Namespace, target.Service, target.Port)
	if err != nil {
		return "", nil, err
	}
	defer tunnel.Close()

	viaForward := func(ctx context.Context, path string, params url.Values) ([]byte, error) {
		return httpGet(ctx, "http://"+tunnel.Addr()+path, params, healthTimeout)
	}
	body, err = target.answers(ctx, healthTimeout, viaForward)
	if err != nil {
		return "", nil, err
	}
	return TransportForward, body, nil
}

// pathRefused is a Service that answers at its root and not under the
// target's path: reached, and still not the Prometheus API that was named.
type pathRefused struct {
	path  string
	cause error
}

func (p *pathRefused) Error() string {
	return fmt.Sprintf("the Service answers, but not under %s: %s", p.path, shortError(p.cause))
}

func (p *pathRefused) Unwrap() error { return p.cause }

// answers asks the health endpoint through get, each place in healthPaths with
// a timeout of its own, so one that hangs leaves the next its time.
//
// Every Prometheus and VictoriaMetrics answers health at the root, whatever
// the path: so where only the root answered, a query under the path confirms
// the path, and a path with a typo in it (`/select/O/prometheus`) is reported
// as one rather than as a Prometheus reached.
func (p *PromTarget) answers(ctx context.Context, timeout time.Duration, get func(context.Context, string, url.Values) ([]byte, error)) ([]byte, error) {
	var first error
	for _, path := range p.healthPaths() {
		body, err := askWithin(ctx, timeout, func(ctx context.Context) ([]byte, error) { return get(ctx, path, nil) })
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if path == endpointHealth && p.Path != "" {
			_, err := askWithin(ctx, timeout, func(ctx context.Context) ([]byte, error) {
				return get(ctx, p.Path+endpointQuery, url.Values{"query": {"1"}})
			})
			if err != nil {
				return nil, &pathRefused{path: p.Path, cause: err}
			}
		}
		return body, nil
	}
	return nil, first
}

func askWithin(ctx context.Context, timeout time.Duration, ask func(context.Context) ([]byte, error)) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return ask(ctx)
}

// Session is one way in to one cluster's Prometheus, held open across several
// queries.
//
// Close it. On the port-forward transport it owns a listener and a stream into
// the cluster, and nothing else will take them down.
type Session struct {
	metrics *Metrics
	cluster string
	client  *cluster.Client
	target  *PromTarget

	// tunnel and base are set together, and only for the port-forward
	// transport; an empty base means the service proxy.
	tunnel Tunnel
	base   string
}

// OpenSession prepares to query a cluster's Prometheus, or explains why it
// cannot.
//
// The session belongs to the caller, who must Close it. Query and QueryRange do
// not go through here — they share one per cluster; see the pool below.
func (m *Metrics) OpenSession(ctx context.Context, name string) (*Session, error) {
	found := m.Discover(ctx, name, false)
	if err := unqueryable(found); err != nil {
		return nil, err
	}

	client, err := m.clusters.Client(found.Cluster)
	if err != nil {
		return nil, err
	}
	session := &Session{metrics: m, cluster: found.Cluster, client: client, target: found.Prometheus}
	if found.Transport != TransportForward {
		return session, nil
	}

	if m.tunnels == nil {
		return nil, fmt.Errorf("%s needs a port-forward to reach Prometheus and none is configured", found.Cluster)
	}
	// The session is shared by every person who reads a chart, so its tunnel
	// is the console's own (access.AsConsole); what each of them is shown out of
	// it is reviewed in internal/web.
	tunnel, err := m.tunnels.Open(access.AsConsole(ctx), found.Cluster, found.Prometheus.Namespace, found.Prometheus.Service, found.Prometheus.Port)
	if err != nil {
		return nil, fmt.Errorf("reaching Prometheus on %s: %w", found.Cluster, err)
	}
	session.tunnel, session.base = tunnel, "http://"+tunnel.Addr()+found.Prometheus.Path
	return session, nil
}

// Close releases whatever the session was holding. Safe on a nil Session, so a
// caller can defer it before checking the error.
func (s *Session) Close() {
	if s != nil && s.tunnel != nil {
		s.tunnel.Close()
	}
}

// Query runs an instant query: one value per series, as of now.
func (s *Session) Query(ctx context.Context, promQL string) ([]byte, error) {
	if strings.TrimSpace(promQL) == "" {
		return nil, errors.New("a PromQL expression is required")
	}
	return s.get(ctx, endpointQuery, url.Values{"query": {promQL}}, instantTimeout)
}

// QueryRange runs a query over a window — the history metrics-server cannot
// provide, and the whole reason to reach for Prometheus at all.
func (s *Session) QueryRange(ctx context.Context, promQL string, start, end time.Time, step time.Duration) ([]byte, error) {
	params, err := rangeParams(promQL, start, end, step)
	if err != nil {
		return nil, err
	}
	return s.get(ctx, endpointRange, params, rangeTimeout)
}

func (s *Session) get(ctx context.Context, endpoint string, params url.Values, timeout time.Duration) ([]byte, error) {
	if s.base == "" {
		reqCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return promAnswer(s.metrics.raw(reqCtx, s.client, s.target.proxyPath(endpoint), params))
	}
	return promAnswer(httpGet(ctx, s.base+endpoint, params, timeout))
}

// ended is whether this session's tunnel has stopped. A tunnel is pinned to the
// pod chosen when it opened; when that pod restarts, client-go's forward
// returns and the listener closes, and a session handed out after that answers
// every query with "connection refused". Retiring it only after 45 s with no
// user at all is not enough, because that never comes while one overview is
// open: every chart on that cluster would fail for every person until all of
// them left. A session on the service proxy, or on a tunnel that cannot say,
// never ends here.
func (s *Session) ended() bool {
	if s == nil || s.tunnel == nil {
		return false
	}
	finished, ok := s.tunnel.(interface{ Done() <-chan struct{} })
	if !ok {
		return false
	}
	select {
	case <-finished.Done():
		return true
	default:
		return false
	}
}

// --- One live session per cluster --------------------------------------------

// The pool bounds how many tunnels drawing the overview costs.
//
// Without it, Query and QueryRange would open a session and close it again
// around the one question they are asked. One overview render is up to four of them — two
// range queries for the history charts, two instant queries for the hottest
// pods — and the panel on a resource is two more, every fifteen seconds, per
// open tab. At roughly 1.8 s to stand a port-forward up, that is the whole
// latency of the page and a SPDY stream into the cluster torn down and rebuilt
// six times a minute for no reason.
//
// So a session is held per cluster and handed out, with a refcount and an idle
// timer. The lifecycle is the delicate part — a live session owns a listener and
// a stream into a cluster — and there are three ways one must go away:
//
//   - the last user leaves and nobody comes back within the idle window,
//   - the cluster's Prometheus moved, or its transport did, and the pooled
//     session no longer reaches what Discover now names: it is retired at once
//     and closed as soon as its last user is done with it,
//   - Close, for a process shutting down or a test finishing.
//
// A session that failed to open is never pooled. The cure for that is usually on
// the operator's side and holding the failure would tell them their fix did not
// work — the same asymmetry as failureTTL.
type promPool struct {
	mu   sync.Mutex
	live map[string]*shared
}

type shared struct {
	// opening is closed when session and err are set. A caller arriving while a
	// tunnel is being stood up waits on it rather than on the mutex, so it can
	// leave when its own request is canceled.
	opening chan struct{}
	ready   bool
	session *Session
	err     error

	// want is what this session was opened to reach, as `address|transport`. A
	// pooled session whose cluster now names something else is stale.
	want string

	users  int
	idle   *time.Timer
	closed bool
}

// acquire hands out this cluster's live session, opening one if there is none.
//
// The returned function must be called exactly once, and the session must not be
// used after it: the next caller may be the one that closes it.
func (m *Metrics) acquire(ctx context.Context, name string) (*Session, func(), error) {
	found := m.Discover(ctx, name, false)
	if err := unqueryable(found); err != nil {
		return nil, nil, err
	}
	resolved := found.Cluster
	want := found.Prometheus.Address() + "|" + found.Transport

	for {
		m.pool.mu.Lock()
		if m.pool.live == nil {
			m.pool.live = map[string]*shared{}
		}
		s := m.pool.live[resolved]

		if s != nil && s.ready && (s.want != want || s.session.ended()) {
			// The cluster answers somewhere else now, or the tunnel this
			// session stands on has stopped (its Prometheus pod restarted,
			// and the tunnel was pinned to that pod). Take it out of
			// the pool before anybody else can be handed it; whoever is still
			// using it closes it on their way out.
			delete(m.pool.live, resolved)
			var orphan *Session
			if s.users == 0 && !s.closed {
				s.closed = true
				if s.idle != nil {
					s.idle.Stop()
					s.idle = nil
				}
				orphan = s.session
			}
			m.pool.mu.Unlock()
			if orphan != nil {
				orphan.Close()
			}
			continue
		}

		if s == nil {
			s = &shared{opening: make(chan struct{}), want: want, users: 1}
			m.pool.live[resolved] = s
			m.pool.mu.Unlock()

			// Outside the lock: standing a port-forward up takes seconds, and a
			// mutex held across it would be exactly the queue this pool exists to
			// remove.
			session, err := m.OpenSession(ctx, resolved)

			m.pool.mu.Lock()
			s.session, s.err, s.ready = session, err, true
			if err != nil {
				s.users = 0
				if m.pool.live[resolved] == s {
					delete(m.pool.live, resolved)
				}
			}
			m.pool.mu.Unlock()
			close(s.opening)

			if err != nil {
				return nil, nil, err
			}
			return session, func() { m.release(resolved, s) }, nil
		}

		if !s.ready {
			opening := s.opening
			m.pool.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-opening:
			}
			continue
		}

		if s.closed {
			// Retired between the check above and here. Start again.
			m.pool.mu.Unlock()
			continue
		}
		if s.idle != nil {
			s.idle.Stop()
			s.idle = nil
		}
		s.users++
		m.pool.mu.Unlock()
		return s.session, func() { m.release(resolved, s) }, nil
	}
}

func (m *Metrics) release(name string, s *shared) {
	m.pool.mu.Lock()
	s.users--
	if s.users > 0 || s.closed {
		m.pool.mu.Unlock()
		return
	}
	if m.pool.live[name] != s {
		// Retired while we held it: nobody else can reach it, so it goes now.
		s.closed = true
		m.pool.mu.Unlock()
		s.session.Close()
		return
	}
	// Through safego: expire closes a tunnel into the cluster, and a panic on a
	// timer's goroutine would end the process.
	s.idle = safego.AfterFunc("metrics: closing an idle Prometheus session", m.idle, func() { m.expire(name, s) })
	m.pool.mu.Unlock()
}

func (m *Metrics) expire(name string, s *shared) {
	if m.retireIdle(name, s) {
		s.session.Close()
	}
}

// retireIdle takes s out of the pool if nobody took it up again between the
// timer firing and the lock. The unlock is deferred: expire runs on a timer
// through safego, which recovers a panic, and a panic that left pool.mu held
// would hang every later query to this pool instead of ending the process.
func (m *Metrics) retireIdle(name string, s *shared) bool {
	m.pool.mu.Lock()
	defer m.pool.mu.Unlock()
	if s.closed || s.users > 0 || m.pool.live[name] != s {
		return false
	}
	delete(m.pool.live, name)
	s.closed = true
	return true
}

// Close releases every session this Metrics is holding.
//
// A Metrics stays usable afterwards — the next query opens a fresh session — so
// this is a release and not a shutdown latch. It exists because a live session
// owns a listener and a stream into a cluster, and a test or a process that is
// finishing should not leave one behind.
func (m *Metrics) Close() {
	m.pool.mu.Lock()
	var closing []*Session
	for name, s := range m.pool.live {
		delete(m.pool.live, name)
		if s.users > 0 || s.closed || !s.ready {
			// In use, or still being opened. Whoever holds it closes it: it is out
			// of the pool now, so release takes the orphan path.
			continue
		}
		if s.idle != nil {
			s.idle.Stop()
			s.idle = nil
		}
		s.closed = true
		closing = append(closing, s.session)
	}
	m.pool.mu.Unlock()

	for _, session := range closing {
		session.Close()
	}
	// The connections into the tunnels just closed are of no further use.
	httpClient.CloseIdleConnections()
}

// unqueryable says why a discovery cannot be queried, in the three sentences the
// operator can act on. Shared by OpenSession and the pool so the two cannot come
// to describe the same cluster differently.
func unqueryable(found *Discovery) error {
	switch {
	case found.CouldNotAsk():
		return wrapped{
			text:  fmt.Sprintf("%s could not be asked for its metrics (%s): %s", found.Cluster, found.Kind, found.Detail),
			cause: found.cause,
		}
	case found.Kind == KindMetricsServer && found.unchosen != "":
		// Not "no Prometheus": there may well be one, and the sentence says
		// why it is not the one being asked.
		return NoHistory{Sentence: fmt.Sprintf("%s has metrics-server and no history to query. %s", found.Cluster, found.unchosen)}
	case found.Kind == KindMetricsServer:
		return NoHistory{Sentence: fmt.Sprintf("%s has metrics-server and no Prometheus, so it holds no history to query", found.Cluster)}
	case found.Kind != KindPrometheus || found.Prometheus == nil:
		return NoHistory{Sentence: fmt.Sprintf("%s has no Prometheus: %s", found.Cluster, found.Detail)}
	}
	return nil
}

// NoHistory is the error a history query gets on a cluster that was asked and
// has no Prometheus to answer it: metrics-server alone, or nothing at all.
//
// A type rather than a sentence because a caller has to tell it apart from a
// Prometheus that is there and failed. The first is a fact about the cluster -
// no span of history can be chosen, so the metrics panel grays its period
// buttons - and the second is a request that may work next time.
// internal/overview, which deliberately does not
// import this package, recognizes it by the method alone.
type NoHistory struct{ Sentence string }

func (e NoHistory) Error() string { return e.Sentence }

// NoHistory marks the error for errors.As against an interface.
func (NoHistory) NoHistory() bool { return true }

// Query runs one instant query on the cluster's live session.
func (m *Metrics) Query(ctx context.Context, name, promQL string) ([]byte, error) {
	session, release, err := m.acquire(ctx, name)
	if err != nil {
		return nil, err
	}
	defer release()
	return session.Query(ctx, promQL)
}

// QueryRange runs one range query on the cluster's live session.
func (m *Metrics) QueryRange(ctx context.Context, name, promQL string, start, end time.Time, step time.Duration) ([]byte, error) {
	session, release, err := m.acquire(ctx, name)
	if err != nil {
		return nil, err
	}
	defer release()
	return session.QueryRange(ctx, promQL, start, end, step)
}

// Range query limits.
const (
	// maxRangePoints is Prometheus's own: it refuses a range query resolving to
	// more than 11 000 points per series, with a message naming a resolution the
	// operator never typed. A UI computing a step from a zoom level reaches that
	// with an ordinary drag, so the step is widened here rather than the query
	// being rejected — a slightly coarser chart is what was wanted, an error
	// dialog is not.
	maxRangePoints = 11000
	// minRangeStep exists because a step formatted as whole seconds turns
	// anything under a second into `step=0`, and Prometheus rejects a zero
	// resolution outright.
	minRangeStep = time.Second
)

// rangeParams builds the query string for a range query, and is separated from
// the request that carries it because every mistake this code can make is in
// the arithmetic rather than in the HTTP.
func rangeParams(promQL string, start, end time.Time, step time.Duration) (url.Values, error) {
	if strings.TrimSpace(promQL) == "" {
		return nil, errors.New("a PromQL expression is required")
	}
	if !end.After(start) {
		return nil, fmt.Errorf("the window ends before it starts (%s to %s)",
			start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339))
	}

	window := end.Sub(start)
	if step < minRangeStep {
		step = minRangeStep
	}
	if window/step > maxRangePoints {
		// Rounded up to a whole second: rounding to nearest can land back over
		// the limit, and being asked again for the same chart with the same
		// error is worse than a step of 8 s where 7.8 would have done.
		step = time.Duration(math.Ceil(float64(window)/maxRangePoints/float64(time.Second))) * time.Second
	}

	return url.Values{
		"query": {promQL},
		"start": {unixSeconds(start)},
		"end":   {unixSeconds(end)},
		// Seconds, formatted without a trailing zero so a sub-minute step stays
		// readable in a log line.
		"step": {strconv.FormatFloat(step.Seconds(), 'f', -1, 64)},
	}, nil
}

// promAnswer decides whether a non-2xx is a transport failure or Prometheus
// answering.
//
// Prometheus replies to a bad query with 400 and a JSON body naming the label it
// did not recognize. That body is worth more to the caller than any error we
// could wrap around it — mcp already renders `status: error` as "Prometheus
// rejected the query" — so a body that parses as a Prometheus envelope is passed
// through verbatim, which is also what the interface promises.
func promAnswer(body []byte, err error) ([]byte, error) {
	if err == nil {
		return body, nil
	}
	var envelope struct {
		Status string `json:"status"`
	}
	if len(body) > 0 && json.Unmarshal(body, &envelope) == nil && envelope.Status != "" {
		return body, nil
	}
	return nil, err
}

// httpClient talks to a port-forward on the loopback and nothing else.
//
// Proxy is nil deliberately: the container may carry an HTTPS_PROXY for the
// cluster's sake, and consulting it for 127.0.0.1 would send a query about a
// production cluster to a proxy that has no business seeing it.
//
// IdleConnTimeout closes a kept-alive connection nobody has used for a while:
// each one is a socket into a tunnel, and a tunnel that was retired would
// otherwise keep its connection until the process ends.
var httpClient = &http.Client{Transport: &http.Transport{Proxy: nil, IdleConnTimeout: promIdleConn}}

// promIdleConn is how long an unused connection to a tunnel is kept.
const promIdleConn = 90 * time.Second

func httpGet(ctx context.Context, endpoint string, params url.Values, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(capBody(resp.Body))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		text := fmt.Sprintf("prometheus answered %d: %s", resp.StatusCode, textutil.FirstLineN(string(body), detailLine))
		// The status as a Kubernetes status error underneath the same
		// sentence, so that a 401 or 403 from an authenticating proxy in front
		// of Prometheus, or a 504, is classified by its code downstream and
		// not read as a cluster that did not answer. The proxy transport's
		// errors already are such errors.
		status := apierrors.NewGenericServerResponse(resp.StatusCode, http.MethodGet,
			schema.GroupResource{Resource: "prometheus"}, "", text, 0, false)
		return body, wrapped{text: text, cause: status}
	}
	return body, nil
}

// wrapped is a sentence for a person over an error for a classifier: Error is
// the sentence, unchanged, and errors.As reaches the cause - the status
// client-go read, or the one built above.
type wrapped struct {
	text  string
	cause error
}

func (w wrapped) Error() string { return w.text }

func (w wrapped) Unwrap() error { return w.cause }

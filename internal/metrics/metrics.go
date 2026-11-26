// Package metrics works out where a cluster's metrics live, and reaches them.
//
// metrics-server answers "what is this using now" and keeps no history;
// Prometheus answers "what was it using at three this morning", but sits behind
// a Service every chart names differently and that a managed control plane may
// not let us reach. This package finds the backend, decides how to talk to it,
// and says which one it found.
//
// The classification exists to separate "there is nothing here" from "I could
// not ask": an expired session must never read as a cluster with no metrics.
//
// If a second consumer ever needs a shared type in a method signature, extract
// a leaf package rather than make this package import its consumer.
package metrics

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/apiraw"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/textutil"
)

// What a cluster turned out to have. The last two are not backends: they say
// the question could not be put.
const (
	KindPrometheus    = "prometheus"
	KindMetricsServer = "metrics-server"
	// KindNone means the cluster answered and neither backend is installed. It
	// is only set after something on that cluster has replied.
	KindNone = "none"
	// KindUnreachable means the API server did not answer at all: the network,
	// TLS, or a credential plugin such as `aws eks get-token`.
	KindUnreachable = "unreachable"
	// KindUnauthorised means the API server answered and refused: 401 for a
	// credential it does not accept, 403 for one it accepts but has not granted
	// this read.
	KindUnauthorised = "unauthorised"
)

// How Prometheus is reached, in order of preference.
const (
	// TransportProxy goes through the API server's service proxy: one request,
	// no listener, no second credential.
	TransportProxy = "proxy"
	// TransportForward goes through a port-forward, which reaches the pod via
	// the kubelet. An EKS control plane may refuse the service proxy (`503 ...
	// Address is not allowed`) for pods on a secondary VPC CIDR, so the
	// transport is chosen by probing.
	TransportForward = "port-forward"
)

// Cache lifetimes.
const (
	// DefaultTTL is how long a discovery is trusted: the topology rarely moves,
	// and a probe costs several API calls and maybe a port-forward.
	DefaultTTL = 5 * time.Minute
	// failureTTL is shorter: the cure for "could not ask" (a fresh login) takes
	// effect at once, and a held failure would say the fix did not work.
	failureTTL = 30 * time.Second
	// DefaultIdle is how long a Prometheus session outlives its last query (see
	// the pool in prom.go): longer than a refresh interval, so a refresh does
	// not pay for a new tunnel, and not much longer, so an unwatched cluster
	// does not hold one open.
	DefaultIdle = 45 * time.Second
)

// Discovery is what one cluster turned out to have, and how that was
// established.
type Discovery struct {
	Cluster string `json:"cluster"`
	// Kind is one of the Kind* constants.
	Kind string `json:"kind"`
	// Prometheus is set only for KindPrometheus.
	Prometheus *PromTarget `json:"prometheus,omitempty"`
	// Transport is one of the Transport* constants, set only for KindPrometheus.
	Transport string `json:"transport,omitempty"`
	// Engine is what answers the Prometheus API, one of the Engine* constants,
	// set only for KindPrometheus: VictoriaMetrics serves the same API.
	Engine string `json:"engine,omitempty"`
	// Detail is one line saying which service answered, or what failed.
	Detail    string    `json:"detail"`
	CheckedAt time.Time `json:"checkedAt"`
	// startedAt is when the probe behind it began. A forced Discover takes
	// only a probe that began after it was asked: one that began before may
	// have read what was there before, such as a source a save replaced
	// (#156).
	startedAt time.Time
	// cause is the error a could-not-ask discovery was classified from, so a
	// refused history query carries its status to the edge's classifier. Nil
	// otherwise.
	cause error
	// unchosen is why no Prometheus was queried on a cluster that may run one
	// (none configured, or candidates in several namespaces). It is in Detail
	// too; kept apart so a refused history query can say it.
	unchosen string
}

// CouldNotAsk reports that this result says nothing about the cluster's metrics,
// only about our ability to look. A UI must not render it as an absence.
func (d *Discovery) CouldNotAsk() bool {
	return d != nil && (d.Kind == KindUnreachable || d.Kind == KindUnauthorised)
}

// Clusters is the part of *cluster.Manager this package needs.
// Two methods, so the probe can be tested against a fake clientset.
type Clusters interface {
	Resolve(name string) (string, bool)
	Client(name string) (*cluster.Client, error)
}

// Metrics answers the metrics questions for every cluster KubeTower knows.
type Metrics struct {
	clusters Clusters
	tunnels  Tunneller
	ttl      time.Duration
	idle     time.Duration
	logf     func(string, ...any)

	// configured and prometheus are Options.Configured and Options.Prometheus:
	// when configured is set, prometheus is the only Prometheus this Metrics
	// ever queries, and nil means none.
	configured bool
	prometheus *PromTarget
	// sources is Options.Sources.
	sources func(cluster string) *PromTarget

	// raw reaches an arbitrary API path; a field so tests can answer without a
	// cluster.
	raw rawFunc

	mu      sync.Mutex
	entries map[string]*entry

	// The live Prometheus sessions, one per cluster. See prom.go.
	pool promPool
}

// Options is what a Metrics is built from.
type Options struct {
	// Clusters is required.
	Clusters Clusters
	// Access reviews the tunnels to the monitoring backend as the console
	// (access.AsConsole), since sessions are shared by every person. Nil
	// refuses every tunnel.
	Access *access.Gate
	// Tunnels opens port-forwards. When nil, a *cluster.Manager passed as
	// Clusters gets the real one (see New).
	Tunnels Tunneller
	// TTL overrides DefaultTTL.
	TTL time.Duration
	// Idle overrides DefaultIdle: how long a Prometheus session is kept alive
	// after the last query that used it.
	Idle time.Duration
	// Logf may be nil.
	Logf func(string, ...any)

	// Configured says Prometheus comes from configuration only: Prometheus
	// below, or none. No Service is looked for. The server profile sets it.
	//
	// Discovery trusts labels, which whoever creates a Service writes. On a
	// shared console a tenant could label their own Service, outrank the real
	// Prometheus, draw their series for everybody and receive every query. So
	// there the deployment chooses, through KT_PROMETHEUS_SERVICE, and nothing
	// in the cluster can.
	Configured bool
	// Prometheus is the configured target, read only when Configured is set.
	// Nil there means no Prometheus: the charts have no history and say why.
	Prometheus *PromTarget

	// Sources is the desktop's choice per cluster (Settings › General): the
	// cluster's source, or nil where discovery decides. A source set there is
	// the only one queried on that cluster, never replaced by discovery when it
	// does not answer. Never read when Configured is set; nil chooses nothing.
	Sources func(cluster string) *PromTarget
}

// SourceSetting names a source chosen in Settings in a Discovery's detail line.
const SourceSetting = "the source set in Settings"

// entry is one cluster's discovery: the answer, whoever is working one out, and
// what was last said about it in the log.
type entry struct {
	// mu guards the three fields below and nothing else.
	//
	// It is not held across the probe, which can take up to a minute: a mutex
	// cannot be left on cancellation. Waiters block on probing instead, against
	// their own ctx.Done().
	mu     sync.Mutex
	result *Discovery
	// probing is non-nil while a probe runs and closed when it lands, so
	// concurrent callers share one probe per cluster.
	probing chan struct{}
	// reported is the last line logged about this cluster, so a repeated
	// failure is logged once rather than every failureTTL.
	reported string
}

// New builds a Metrics from opts.
func New(opts Options) *Metrics {
	m := &Metrics{
		clusters: opts.Clusters,
		tunnels:  opts.Tunnels,
		ttl:      opts.TTL,
		idle:     opts.Idle,
		logf:     opts.Logf,
		raw:      restRaw,
		entries:  map[string]*entry{},

		configured: opts.Configured,
		prometheus: opts.Prometheus,
		sources:    opts.Sources,
	}
	if m.ttl <= 0 {
		m.ttl = DefaultTTL
	}
	if m.idle <= 0 {
		m.idle = DefaultIdle
	}
	if m.logf == nil {
		m.logf = func(format string, v ...any) { log.Printf("[kubetower] metrics: "+format, v...) }
	}
	if m.tunnels == nil {
		// Without a tunneller, a cluster whose proxy refuses would read as an
		// unreachable Prometheus. A fake gets no tunnel.
		if manager, ok := opts.Clusters.(*cluster.Manager); ok {
			m.tunnels = managerTunnels(manager, opts.Access, m.logf)
		}
	}
	return m
}

// --- Discovery ---------------------------------------------------------------

// Selectors tried in order, first match wins.
//
// The order and the length matter: kube-prometheus-stack labels its Service
// under the plain `app` key, which `app.kubernetes.io/name` selectors alone
// would miss, reading a full Prometheus as "metrics-server".
var promSelectors = []string{
	// An operator pointing at a specific Service goes first. The label is
	// Headlamp's, which clusters already carry. It ranks first inside one
	// namespace only: candidates in two namespaces are refused whatever their
	// labels (findPrometheus), and the server profile reads no label.
	"headlamp-prometheus=true",
	// The canonical labels, most specific first.
	"app.kubernetes.io/name=prometheus,app.kubernetes.io/component=server",
	"app.kubernetes.io/name=prometheus",
	"app=prometheus",
	// kube-prometheus-stack.
	"app=kube-prometheus-stack-prometheus",
	// Set by the Prometheus operator on its Service. Last, because it can
	// also match a Thanos sidecar's Service, which the port check refuses.
	"operated-prometheus=true",
}

// vmSelector is a VictoriaMetrics Service that serves the Prometheus API: the
// label that finds it, the ports the API may be on, and the path it is under.
type vmSelector struct {
	selector string
	ports    []int
	path     string
}

// vmSelectors are tried, in order, only where no Prometheus was found: a
// cluster running both keeps Prometheus.
//
// Single-node first, then vmselect, which serves the API under a tenant's
// path; tenant 0 is the default one. Each is listed as vm-operator labels its
// Service (`vmsingle-<name>`, `vmselect-<name>`; the victoria-metrics-k8s-stack
// chart goes through vm-operator) and as the victoria-metrics-single and
// victoria-metrics-cluster charts do.
var vmSelectors = []vmSelector{
	// vm-operator publishes vmsingle on 8429, with 8428 kept as an alias.
	{selector: "app.kubernetes.io/name=vmsingle", ports: []int{8428, 8429}},
	{selector: "app.kubernetes.io/name=victoria-metrics-single", ports: []int{8428}},
	{selector: "app.kubernetes.io/name=vmselect", ports: []int{8481}, path: vmselectPath},
	{selector: "app.kubernetes.io/name=victoria-metrics-cluster,app.kubernetes.io/component=vmselect", ports: []int{8481}, path: vmselectPath},
	// Older victoria-metrics-cluster charts label vmselect under the plain
	// `app` key only.
	{selector: "app=vmselect", ports: []int{8481}, path: vmselectPath},
}

// vmselectPath is where vmselect serves tenant 0's Prometheus API.
const vmselectPath = "/select/0/prometheus"

// Discover works out what a cluster has, from cache unless force says otherwise.
// It never returns an error: a failure to reach the cluster is one of the
// classified answers.
func (m *Metrics) Discover(ctx context.Context, name string, force bool) *Discovery {
	resolved, ok := m.clusters.Resolve(name)
	if !ok {
		// Not cached: a caller mistake, not a property of a cluster.
		return &Discovery{
			Cluster:   name,
			Kind:      KindUnreachable,
			Detail:    fmt.Sprintf("no context named %q in the kubeconfig", name),
			CheckedAt: time.Now(),
		}
	}

	e := m.entry(resolved)
	asked := time.Now()
	for {
		e.mu.Lock()
		switch {
		case !force && e.result != nil && time.Since(e.result.CheckedAt) < m.ttlFor(e.result):
			found := e.result
			e.mu.Unlock()
			return found

		case force && e.result != nil && !e.result.startedAt.Before(asked):
			// Somebody began a probe after we asked, which is what force
			// wanted; one that only ended after may have read the old state.
			found := e.result
			e.mu.Unlock()
			return found
		}

		if probing := e.probing; probing != nil {
			e.mu.Unlock()
			select {
			case <-ctx.Done():
				// Nothing cached or claimed: we only stopped looking.
				return &Discovery{
					Cluster:   resolved,
					Kind:      KindUnreachable,
					Detail:    "a probe of this cluster was still running when the request was canceled: " + shortError(ctx.Err()),
					CheckedAt: time.Now(),
				}
			case <-probing:
			}
			continue
		}

		probing := make(chan struct{})
		e.probing = probing
		e.mu.Unlock()

		// Outside the lock: see entry.mu.
		started := time.Now()
		found := m.probe(ctx, resolved)
		found.Cluster = resolved
		found.CheckedAt = time.Now()
		found.startedAt = started

		// A could-not-ask from a probe whose own request went away is about our
		// deadline, not the cluster, so it is not cached: the next caller probes
		// with its own. A verdict a canceled probe did reach is a fact, and is
		// cached.
		cancelled := ctx.Err() != nil && found.CouldNotAsk()

		line := fmt.Sprintf("%s: %s (%s)", resolved, found.Kind, found.Detail)
		e.mu.Lock()
		say := false
		if !cancelled {
			e.result = found
			say = e.reported != line
			e.reported = line
		}
		e.probing = nil
		e.mu.Unlock()
		close(probing)

		if say {
			m.logf("%s", line)
		}
		return found
	}
}

func (m *Metrics) ttlFor(d *Discovery) time.Duration {
	if d.CouldNotAsk() {
		return failureTTL
	}
	return m.ttl
}

// Cached answers from what is already known and **never probes**.
//
// It serves the health page's background round, which only prints the
// backend's name and must not stand up a port-forward on every cluster when
// nobody is looking.
//
// nil means "nothing known yet", which the caller renders as unknown, not none.
func (m *Metrics) Cached(name string) *Discovery {
	resolved, ok := m.clusters.Resolve(name)
	if !ok {
		return nil
	}
	e := m.entry(resolved)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.result
}

func (m *Metrics) entry(name string) *entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[name]; ok {
		return e
	}
	e := &entry{}
	m.entries[name] = e
	return e
}

func (m *Metrics) probe(ctx context.Context, name string) *Discovery {
	client, err := m.clusters.Client(name)
	if err != nil {
		// Building a client reads only the kubeconfig: a broken context.
		return &Discovery{Kind: KindUnreachable, Detail: shortError(err)}
	}

	look := m.lookForPrometheus(ctx, name, client)
	target, selector, refused := look.target, look.via, look.refused
	switch {
	case target != nil:
		transport, health, err := m.probeTransport(ctx, name, client, target)
		var wrongPath *pathRefused
		if errors.As(err, &wrongPath) {
			return &Discovery{
				Kind:       KindUnreachable,
				Prometheus: target,
				Detail:     fmt.Sprintf("service %s found via %s: %s", target.Address(), selector, shortError(err)),
			}
		}
		if err != nil {
			// Found but unreachable is not an absence. The line names the
			// transports tried, so the operator knows where to look.
			return &Discovery{
				Kind:       KindUnreachable,
				Prometheus: target,
				Detail: fmt.Sprintf("service %s found via %s, but neither the API service proxy nor a port-forward reached it: %s",
					target.Address(), selector, shortError(err)),
			}
		}
		engine := engineOf(health, look.engine)
		detail := fmt.Sprintf("service %s found via %s, reached by %s", target.Address(), selector, transport)
		if engine == EngineVictoriaMetrics {
			detail += ", answered by VictoriaMetrics"
		}
		return &Discovery{
			Kind:       KindPrometheus,
			Prometheus: target,
			Transport:  transport,
			Engine:     engine,
			Detail:     detail,
		}

	case refused != nil && refused.total:
		// Nothing on this cluster will answer; metrics-server's silence must not
		// read as an absence.
		return &Discovery{Kind: refused.kind, Detail: refused.detail, cause: refused.err}
	}

	// metrics-server next. A 404 here is the one honest "none": the cluster
	// says the aggregated API is not registered.
	switch err := m.hasMetricsServer(ctx, client); {
	case err == nil:
		detail := "metrics-server answers; instant usage available, no history"
		if refused != nil {
			// Say that Services could not be listed, or a Prometheus this
			// identity may not see would be hidden.
			detail += fmt.Sprintf(". Prometheus could not be looked for: %s", refused.detail)
		}
		if look.unchosen != "" {
			detail += ". " + look.unchosen
		}
		return &Discovery{Kind: KindMetricsServer, Detail: detail, unchosen: look.unchosen}

	case apierrors.IsNotFound(err):
		if refused != nil {
			return &Discovery{Kind: refused.kind, Detail: refused.detail, cause: refused.err}
		}
		if look.unchosen != "" {
			// None to query, not necessarily none installed: the sentence says.
			return &Discovery{
				Kind:     KindNone,
				Detail:   "the cluster answered: metrics.k8s.io is not registered. " + look.unchosen,
				unchosen: look.unchosen,
			}
		}
		return &Discovery{
			Kind:   KindNone,
			Detail: "the cluster answered: no Prometheus service matched, and metrics.k8s.io is not registered",
		}

	default:
		if f := classify(err); f != nil {
			return &Discovery{Kind: f.kind, Detail: f.detail, cause: f.err}
		}
		// An unclassified error is still not "none".
		return &Discovery{
			Kind:   KindUnreachable,
			Detail: fmt.Sprintf("metrics.k8s.io answered unusably: %s", shortError(err)),
		}
	}
}

// promQueries are the selectors sent to the API server: fewer cluster-wide
// lists than promSelectors, covering the same Services, with the preference
// order applied in memory afterwards. TestEverySelectorIsCoveredByAQuery pins
// the covering.
var promQueries = []string{
	"headlamp-prometheus=true",
	// Covers both `app.kubernetes.io/name=prometheus` selectors, and every
	// VictoriaMetrics one: one list rather than one more per probe.
	"app.kubernetes.io/name in (prometheus,vmsingle,vmselect,victoria-metrics-single,victoria-metrics-cluster)",
	"app in (prometheus,kube-prometheus-stack-prometheus,vmselect)",
	"operated-prometheus=true",
}

// lookup is what looking for Prometheus came back with. At most one of target,
// refused and unchosen says something; all three empty is a cluster where
// nothing matched.
type lookup struct {
	target *PromTarget
	// via names how target was chosen: the selector that matched, or the
	// variable that configured it. It goes into the Detail line.
	via string
	// refused is a reason the cluster could not be asked.
	refused *failure
	// unchosen is a sentence saying why no Prometheus was chosen on a cluster
	// that may run one. See Discovery.unchosen.
	unchosen string
	// engine is what the chosen Service is expected to be, one of the Engine*
	// constants; the health answer has the last word (engineOf).
	engine string
}

// lookForPrometheus is configuration under the server profile and discovery
// elsewhere. They never mix: falling back to discovery would hand the choice
// back to whoever writes labels (Options.Configured).
func (m *Metrics) lookForPrometheus(ctx context.Context, name string, client *cluster.Client) lookup {
	if !m.configured {
		if m.sources != nil {
			if target := m.sources(name); target != nil {
				return lookup{target: target, via: SourceSetting}
			}
		}
		return m.findPrometheus(ctx, client)
	}
	if m.prometheus == nil {
		return lookup{unchosen: "No Prometheus is configured (" + EnvPrometheusService +
			"), and the server profile does not look for one"}
	}
	// A copy: the configured target is shared by every probe.
	target := *m.prometheus
	return lookup{target: &target, via: EnvPrometheusService}
}

// findPrometheus lists the candidate Services, applies the selectors in order
// and returns the first with a port worth querying, unless candidates are in
// more than one namespace, in which case it chooses none and says so.
//
// Labels are written by whoever creates a Service, so across namespaces a
// tenant's decoy cannot be told from the real one. Inside one namespace the
// order decides: it has one owner, and kube-prometheus-stack always puts two
// matching Services there.
//
// A refusal is returned rather than raised: a 403 on listing Services says
// nothing about whether metrics-server would answer.
func (m *Metrics) findPrometheus(ctx context.Context, client *cluster.Client) lookup {
	candidates, refused := listCandidates(ctx, client)
	if refused != nil {
		return lookup{refused: refused}
	}

	// Prometheus first; VictoriaMetrics only where no Prometheus Service is
	// there, so a VictoriaMetrics Service never makes a cluster with one
	// Prometheus ambiguous.
	if chosen, found := choose(candidates, promChoices()); found {
		return chosen
	}
	chosen, _ := choose(candidates, vmChoices())
	return chosen
}

// listCandidates is every Service a selector of promQueries matches, or the
// refusal of the first list that was classified.
func listCandidates(ctx context.Context, client *cluster.Client) ([]corev1.Service, *failure) {
	var candidates []corev1.Service
	for _, query := range promQueries {
		list, err := client.Clients.CoreV1().Services(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
			LabelSelector: query,
			// From the watch cache rather than a quorum read: the topology does
			// not move between probes, and this runs for every cluster.
			ResourceVersion: "0",
		})
		if err != nil {
			f := classify(err)
			if f == nil {
				// Unclassified: try the next query.
				continue
			}
			// The remaining queries would fail the same way.
			return nil, f
		}
		candidates = append(candidates, list.Items...)
	}
	return candidates, nil
}

func promChoices() []choice {
	out := make([]choice, 0, len(promSelectors))
	for _, selector := range promSelectors {
		out = append(out, choice{selector: selector, port: prometheusPort, engine: EnginePrometheus})
	}
	return out
}

func vmChoices() []choice {
	out := make([]choice, 0, len(vmSelectors))
	for _, s := range vmSelectors {
		out = append(out, choice{selector: s.selector, port: portFor(s.ports...), path: s.path, engine: EngineVictoriaMetrics})
	}
	return out
}

// choice is one selector findPrometheus tries: the label, which port of a
// matching Service is the API's, and the path it is under.
type choice struct {
	selector string
	port     func(*corev1.Service) int
	path     string
	engine   string
}

// choose applies choices in order to candidates. found says some candidate
// matched with a usable port, whether or not one was chosen: candidates in
// several namespaces are refused (notChosen).
//
// A Service matching two selectors is reported under the first. Every
// candidate is walked, because the refusal reads all their namespaces; one
// with no port worth querying (an exporter on 8080) does not count.
func choose(candidates []corev1.Service, choices []choice) (chosen lookup, found bool) {
	namespaces := map[string]bool{}
	for _, c := range choices {
		parsed, err := labels.Parse(c.selector)
		if err != nil {
			// Pinned parseable by a test; skipping keeps one typo from hiding the
			// rest.
			continue
		}
		for i := range candidates {
			svc := &candidates[i]
			if !parsed.Matches(labels.Set(svc.Labels)) {
				continue
			}
			port := c.port(svc)
			if port == 0 {
				continue
			}
			namespaces[svc.Namespace] = true
			if chosen.target == nil {
				chosen = lookup{target: &PromTarget{
					Namespace: svc.Namespace,
					Service:   svc.Name,
					Port:      port,
					Path:      c.path,
				}, via: c.selector, engine: c.engine}
			}
		}
	}
	if len(namespaces) > 1 {
		return lookup{unchosen: notChosen(namespaces)}, true
	}
	return chosen, len(namespaces) > 0
}

// portFor finds the first Service port that is one of want, or forwards to
// one.
func portFor(want ...int) func(*corev1.Service) int {
	return func(svc *corev1.Service) int {
		for _, port := range svc.Spec.Ports {
			for _, w := range want {
				if int(port.Port) == w ||
					port.TargetPort.Type == intstr.Int && int(port.TargetPort.IntVal) == w {
					return int(port.Port)
				}
			}
		}
		return 0
	}
}

// prometheusPort is the first port of svc worth querying, or 0.
func prometheusPort(svc *corev1.Service) int {
	for _, port := range svc.Spec.Ports {
		if isPrometheusPort(port) {
			return int(port.Port)
		}
	}
	return 0
}

// notChosen is the sentence for Services claiming to be Prometheus in several
// namespaces, naming at most three, sorted, to fit detailLine.
func notChosen(namespaces map[string]bool) string {
	names := make([]string, 0, len(namespaces))
	for name := range namespaces {
		names = append(names, name)
	}
	sort.Strings(names)
	listed := strings.Join(names, ", ")
	if len(names) > 3 {
		listed = fmt.Sprintf("%s and %d more", strings.Join(names[:3], ", "), len(names)-3)
	}
	return fmt.Sprintf("Prometheus was not chosen: Services in %d namespaces claim to be it (%s), "+
		"and a label cannot say which is real", len(names), listed)
}

// hasMetricsServer asks whether metrics.k8s.io is registered, and returns the
// error verbatim so the caller can tell a 404 from a 403.
func (m *Metrics) hasMetricsServer(ctx context.Context, client *cluster.Client) error {
	_, err := m.raw(ctx, client, metricsAPIPath, nil)
	return err
}

// isPrometheusPort does not accept a port merely called "http": every exporter
// in a kube-prometheus-stack release has one, and querying it answers empty.
// Port 9090 counts, and so does a port forwarding to 9090, as the
// prometheus-community/prometheus chart publishes its server (`http`, 80 to
// 9090). A named targetPort is not followed.
func isPrometheusPort(port corev1.ServicePort) bool {
	if port.Port == 9090 {
		return true
	}
	if port.TargetPort.Type == intstr.Int && port.TargetPort.IntVal == 9090 {
		return true
	}
	switch port.Name {
	case "web", "http-web", "prometheus":
		return true
	}
	return false
}

// --- Telling "nothing there" from "could not ask" ----------------------------

// failure is a reason we could not look, as opposed to something we saw.
type failure struct {
	// kind is KindUnreachable or KindUnauthorised.
	kind   string
	detail string
	// err is what was classified, for Discovery.cause.
	err error
	// total says nothing else on this cluster will answer either. A network or
	// credential failure is total; a 403 is not, since the same identity may
	// still read metrics.k8s.io.
	total bool
}

// classify decides whether an error is evidence about the cluster or about us.
// Everything it returns nil for may be described as an absence.
func classify(err error) *failure {
	if err == nil {
		return nil
	}
	detail := shortError(err)

	switch {
	// A 404 is the only status that is evidence about the cluster.
	case apierrors.IsNotFound(err):
		return nil

	case apierrors.IsUnauthorized(err):
		return &failure{kind: KindUnauthorised, detail: detail, err: err, total: true}

	case apierrors.IsForbidden(err):
		return &failure{kind: KindUnauthorised, detail: detail, err: err}

	case apierrors.IsTimeout(err), apierrors.IsServerTimeout(err),
		apierrors.IsServiceUnavailable(err), apierrors.IsTooManyRequests(err),
		apierrors.IsInternalError(err):
		return &failure{kind: KindUnreachable, detail: detail, err: err, total: true}
	}

	// What is left is mostly plain text, notably from an exec credential plugin
	// whose session has lapsed before anything reached the cluster.
	lower := strings.ToLower(err.Error())
	for _, needle := range []string{
		"exec plugin", "getting credentials", "executable", "exit code",
		"unauthorized", "forbidden", "connection refused", "no such host",
		"i/o timeout", "deadline exceeded", "timeout", "tls", "certificate",
		"eof", "network is unreachable", "no route to host", "context canceled",
	} {
		if strings.Contains(lower, needle) {
			return &failure{kind: reachKind(lower), detail: detail, err: err, total: true}
		}
	}
	return nil
}

func reachKind(lower string) string {
	if strings.Contains(lower, "unauthorized") || strings.Contains(lower, "forbidden") {
		return KindUnauthorised
	}
	return KindUnreachable
}

// detailLine bounds every string this package puts in a Detail field.
// The same 200 runes as internal/forward, internal/logs and internal/health,
// since it is the same cluster error read in several places.
const detailLine = 200

// shortError trims a client-go error, which runs to several lines (on EKS
// naming the IAM ARN and account), down to something a UI can show.
func shortError(err error) string {
	if err == nil {
		return ""
	}
	// ReasonN rather than FirstLineN: the URL is what overflows, and cutting
	// it keeps the reason. The address is kept whenever the message fits.
	return textutil.ReasonN(err.Error(), detailLine)
}

// --- Raw API access ----------------------------------------------------------

// metricsAPIPath is the aggregated API's discovery document, the cheapest
// question that tells an installed metrics-server from an absent one.
const metricsAPIPath = "/apis/metrics.k8s.io/v1beta1"

// rawFunc reads an arbitrary API path: the metrics.k8s.io aggregated API, and
// Prometheus behind the service proxy.
type rawFunc func(ctx context.Context, client *cluster.Client, path string, params url.Values) ([]byte, error)

// restRaw is the rawFunc a Metrics uses outside its tests.
// The answer is capped at maxPromResponse (cappedREST).
func restRaw(ctx context.Context, client *cluster.Client, path string, params url.Values) ([]byte, error) {
	shared, ok := client.Clients.Discovery().RESTClient().(*rest.RESTClient)
	if !ok || shared == nil || shared.Client == nil || client.Config == nil {
		return nil, fmt.Errorf("this cluster's client cannot make raw API requests")
	}
	// The address the cluster's clients were built for.
	root, _, err := rest.DefaultServerUrlFor(client.Config)
	if err != nil {
		return nil, err
	}
	capped, err := cappedREST(shared, root)
	if err != nil {
		return nil, err
	}
	req := capped.Get().AbsPath(path)
	for key, values := range params {
		for _, value := range values {
			req = req.Param(key, value)
		}
	}
	// Not DoRaw, whose error says "unknown" for a JSON Status body. apiraw
	// decodes it and still returns the body, which promAnswer reads.
	body, err := apiraw.Do(ctx, req)
	if errors.Is(err, errTooLarge) {
		// client-go's wrapping says "Please retry", which is wrong advice here.
		return nil, errTooLarge
	}
	return body, err
}

// unixSeconds is how Prometheus is given a timestamp: seconds since the epoch
// cannot be misread as a local time.
func unixSeconds(t time.Time) string {
	return strconv.FormatInt(t.Unix(), 10)
}

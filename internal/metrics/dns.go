package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thonicdev/kubetower/safego"
)

// Reading CoreDNS's own metrics out of the cluster's Prometheus.
//
// This serves `extension.Host.DNSWindow`. As for the usage window, **Prometheus
// has no RBAC**, so the queries are fixed here, parameterised by nothing the
// person typed except the span - an enum - and the namespace and pod IPs the
// caller found in the cluster and reviewed. The caller (internal/web,
// hostdns.go) has already refused a person who may not list the pods.
//
// The fourteen queries, by what they ask:
//
//	5 instant  per pod: queries, answers by code, p50, p99, cache hit rate
//	5 range    cluster-wide: queries, answers by code, p50, p99, cache hit rate
//	3 upstream per `to`: answers by code, p99, health-check failures
//	1 version  the pods Prometheus knows in the namespace, and their version
//
// A pod is told from a stale series by its IP: `instance` is `<podIP>:port`
// with the default `kubernetes-service-endpoints` job, which sets no `pod`
// label. Every query selects the live pods' IPs, so a restarted pod's series,
// which Prometheus keeps for five minutes, never enters a quantile; the version
// query alone is not narrowed, and what it finds that no live pod owns is
// counted, not drawn.

// DNSSpan is how far back a DNS read goes. An enum: the only thing of the
// person's that reaches a query.
type DNSSpan string

// The spans.
const (
	DNSSpan15m DNSSpan = "15m"
	DNSSpan1h  DNSSpan = "1h"
	DNSSpan6h  DNSSpan = "6h"
)

// Duration and Step are zero for a span that is not one.
func (s DNSSpan) Duration() time.Duration {
	switch s {
	case DNSSpan15m:
		return 15 * time.Minute
	case DNSSpan1h:
		return time.Hour
	case DNSSpan6h:
		return 6 * time.Hour
	}
	return 0
}

// Step is the distance between two points over s: 15 s, 30 s, 2 min.
func (s DNSSpan) Step() time.Duration {
	switch s {
	case DNSSpan15m:
		return 15 * time.Second
	case DNSSpan1h:
		return 30 * time.Second
	case DNSSpan6h:
		return 2 * time.Minute
	}
	return 0
}

// rate is what rate() averages over: three minutes, or twice the step when
// that is longer. A rate needs two samples and Prometheus advises four scrape
// intervals, so a one-minute window returns nothing at a 60 s scrape.
func (s DNSSpan) rate() time.Duration { return max(3*time.Minute, 2*s.Step()) }

// ErrBadDNSSpan is a span that is not one of the three.
var ErrBadDNSSpan = errors.New("not a DNS span")

const (
	// dnsMaxPods bounds the regex a query carries. A cluster with more
	// CoreDNS pods than this is read for the first of them, and says so.
	dnsMaxPods = 200
	// dnsParallel is how many of one read's queries run at once.
	dnsParallel = 4
	// dnsBudget is how long one read may take, every query included.
	dnsBudget = 45 * time.Second
)

// DNSPodRef is a CoreDNS pod the caller found and reviewed.
type DNSPodRef struct {
	Name string
	Node string
	IP   string
}

// DNSFigures is one rate window of CoreDNS. See extension.DNSFigures.
type DNSFigures struct {
	QPS, NXDomain, ServFail, P50, P99, CacheHit float64
	HasShares, HasLatency, HasCache             bool
}

// DNSSeries is the cluster-wide figures over the span.
type DNSSeries struct {
	Step     int
	QPS      []Point
	ByRcode  map[string][]Point
	P50, P99 []Point
	CacheHit []Point
}

// DNSPod is one pod's figures.
type DNSPod struct {
	DNSPodRef
	Figures DNSFigures
	Scraped bool
}

// DNSUpstream is one upstream's figures.
type DNSUpstream struct {
	To           string
	QPS, ErrQPS  float64
	P99          float64
	HasLatency   bool
	HealthFailPS float64
	Answers      bool
}

// DNSRead is one read. Everything but State and Detail is set only for
// WindowRead.
type DNSRead struct {
	State     WindowState
	Detail    string
	Now       DNSFigures
	Series    DNSSeries
	Pods      []DNSPod
	Upstreams []DNSUpstream
	Version   string
	Partial   string
	ReadAt    time.Time
}

// Query templates. %s is the selector, %s the rate window. The selector always
// carries the namespace and the live pods' instances.
const (
	dnsReq      = `coredns_dns_requests_total%s`
	dnsResp     = `coredns_dns_responses_total%s`
	dnsBucket   = `coredns_dns_request_duration_seconds_bucket%s`
	dnsHits     = `coredns_cache_hits_total%s`
	dnsCacheReq = `coredns_cache_requests_total%s`
)

// dnsQuery is one of the fixed queries: a name for a failure's sentence and its
// PromQL.
type dnsQuery struct {
	name string
	ql   string
}

// dnsQueries are the thirteen that take the pods' selector, in the order
// DNS reads them back; the fourteenth is the version (dnsVersionQuery).
func dnsQueries(sel, rw string) (instant, ranged []dnsQuery) {
	rate := func(metric string) string { return fmt.Sprintf("rate("+metric+"[%s])", sel, rw) }
	quantile := func(q, by string) string {
		return fmt.Sprintf("histogram_quantile(%s, sum by (%s) (%s))", q, by, rate(dnsBucket))
	}
	cache := func(by string) string {
		group := ""
		if by != "" {
			group = " by (" + by + ")"
		}
		return fmt.Sprintf("sum%s (%s) / sum%s (%s)", group, rate(dnsHits), group, rate(dnsCacheReq))
	}
	instant = []dnsQuery{
		{"queries per pod", fmt.Sprintf("sum by (instance) (%s)", rate(dnsReq))},
		{"answers per pod", fmt.Sprintf("sum by (instance, rcode) (%s)", rate(dnsResp))},
		{"p50 per pod", quantile("0.5", "le, instance")},
		{"p99 per pod", quantile("0.99", "le, instance")},
		{"cache hit rate per pod", cache("instance")},
	}
	ranged = []dnsQuery{
		{"queries", fmt.Sprintf("sum (%s)", rate(dnsReq))},
		{"answers by code", fmt.Sprintf("sum by (rcode) (%s)", rate(dnsResp))},
		{"p50", quantile("0.5", "le")},
		{"p99", quantile("0.99", "le")},
		{"cache hit rate", cache("")},
	}
	return instant, ranged
}

// dnsUpstreamQueries are the three upstream questions, for the metric family
// named: coredns_proxy (CoreDNS 1.11 and later, label proxy_name) or
// coredns_forward (1.10 and older, no such label).
func dnsUpstreamQueries(family, sel, rw string) []dnsQuery {
	if family == "coredns_proxy" {
		sel = strings.TrimSuffix(sel, "}") + `,proxy_name="forward"}`
	}
	count := fmt.Sprintf("rate(%s_request_duration_seconds_count%s[%s])", family, sel, rw)
	bucket := fmt.Sprintf("rate(%s_request_duration_seconds_bucket%s[%s])", family, sel, rw)
	fails := fmt.Sprintf("rate(%s_healthcheck_failures_total%s[%s])", family, sel, rw)
	return []dnsQuery{
		{"upstream answers", fmt.Sprintf("sum by (to, rcode) (%s)", count)},
		{"upstream p99", fmt.Sprintf("histogram_quantile(0.99, sum by (le, to) (%s))", bucket)},
		{"upstream health failures", fmt.Sprintf("sum by (to) (%s)", fails)},
	}
}

// dnsVersionQuery lists the instances Prometheus knows in the namespace and
// the version each reports. Not narrowed to the live pods: what it finds that
// none owns is what the read says it left out.
func dnsVersionQuery(namespace string) dnsQuery {
	return dnsQuery{"version", fmt.Sprintf(`count by (instance, version) (coredns_build_info{namespace=%s})`,
		strconv.Quote(namespace))}
}

// dnsSelector is the label matcher every pod query carries: the namespace, and
// the instances of exactly these pods. The IPs are validated and escaped, then
// quoted as a PromQL string, so nothing that is not an address can reach it.
func dnsSelector(namespace string, ips []string) string {
	return fmt.Sprintf("{namespace=%s,instance=~%s}", strconv.Quote(namespace), strconv.Quote(instanceRegex(ips)))
}

// instanceRegex matches `<ip>:<port>` for each IP, and nothing else. An IPv6
// instance is bracketed. Prometheus anchors the expression, so the whole label
// has to match.
func instanceRegex(ips []string) string {
	alts := make([]string, 0, len(ips))
	for _, ip := range ips {
		host := ip
		if a, err := netip.ParseAddr(ip); err == nil && a.Is6() {
			host = "[" + ip + "]"
		}
		alts = append(alts, regexp.QuoteMeta(host))
	}
	return "(" + strings.Join(alts, "|") + "):[0-9]+"
}

// validPods keeps the pods with an address, once each, by name, to at most
// dnsMaxPods; dropped counts those left out.
func validPods(pods []DNSPodRef) (kept []DNSPodRef, dropped int) {
	seen := map[string]bool{}
	sorted := slices.Clone(pods)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, p := range sorted {
		a, err := netip.ParseAddr(p.IP)
		if err != nil || seen[a.String()] {
			dropped++
			continue
		}
		seen[a.String()] = true
		p.IP = a.String()
		if len(kept) == dnsMaxPods {
			dropped++
			continue
		}
		kept = append(kept, p)
	}
	return kept, dropped
}

// ReadDNS asks a cluster's Prometheus for CoreDNS's figures over span, for
// these pods in this namespace, or says why there are none.
//
// Its errors are the caller's: a span that is not one, a namespace that cannot
// be one. Everything about Prometheus is a state.
func ReadDNS(ctx context.Context, q RangeQuerier, cluster string, span DNSSpan, namespace string,
	pods []DNSPodRef) (DNSRead, error) {
	if span.Duration() == 0 {
		return DNSRead{}, fmt.Errorf("%w: %q", ErrBadDNSSpan, span)
	}
	if namespace == "" || !validName(namespace) {
		return DNSRead{}, fmt.Errorf("%w: %q, so no query was built from it", ErrBadNamespace, namespace)
	}
	if q == nil {
		return DNSRead{State: WindowNoPrometheus, Detail: "this installation has no Prometheus configured"}, nil
	}
	live, dropped := validPods(pods)
	if len(live) == 0 {
		return DNSRead{State: WindowEmpty, Detail: "no CoreDNS pod has an address", ReadAt: time.Now()}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, dnsBudget)
	defer cancel()

	ips := make([]string, len(live))
	for i, p := range live {
		ips[i] = p.IP
	}
	sel, rw := dnsSelector(namespace, ips), short(span.rate())
	instant, ranged := dnsQueries(sel, rw)
	upstream := dnsUpstreamQueries("coredns_proxy", sel, rw)
	version := dnsVersionQuery(namespace)

	step := span.Step()
	end := time.Now().Truncate(step)
	start := end.Add(-span.Duration())

	// One slot per query, filled by its own goroutine and read after they all
	// ended; at most dnsParallel in flight.
	var slots []*dnsSlot
	for _, x := range instant {
		slots = append(slots, &dnsSlot{q: x})
	}
	for _, x := range ranged {
		slots = append(slots, &dnsSlot{q: x, rng: true})
	}
	for _, x := range upstream {
		slots = append(slots, &dnsSlot{q: x})
	}
	slots = append(slots, &dnsSlot{q: version})
	run := func(batch []*dnsSlot) {
		var wg sync.WaitGroup
		gate := make(chan struct{}, dnsParallel)
		for _, s := range batch {
			wg.Add(1)
			gate <- struct{}{}
			safego.Go("metrics: a DNS query", func() error {
				var raw []byte
				if s.rng {
					raw, s.err = q.QueryRange(ctx, cluster, s.q.ql, start, end, step)
				} else {
					raw, s.err = q.Query(ctx, cluster, s.q.ql)
				}
				if s.err == nil {
					s.rows, s.err = parseRows(raw)
				}
				return s.err
			}, func(error) { <-gate; wg.Done() })
		}
		wg.Wait()
	}
	run(slots)

	// The old family, only when the new one is empty everywhere: CoreDNS 1.10
	// and older named the forward plugin's metrics coredns_forward_*.
	up := slots[10:13]
	if emptyUpstream(up) {
		old := dnsUpstreamQueries("coredns_forward", sel, rw)
		var again []*dnsSlot
		for _, x := range old {
			again = append(again, &dnsSlot{q: x})
		}
		run(again)
		if !emptyUpstream(again) {
			copy(up, again)
		}
	}

	failed := 0
	var firstErr error
	for _, s := range slots {
		if s.err != nil {
			failed++
			if firstErr == nil {
				firstErr = s.err
			}
		}
	}
	if failed == len(slots) {
		out := DNSRead{ReadAt: time.Now()}
		f := failedWindow(firstErr)
		out.State, out.Detail = f.State, f.Detail
		return out, nil
	}

	out := DNSRead{State: WindowRead, ReadAt: time.Now()}
	var notes []string
	if failed > 0 {
		notes = append(notes, fmt.Sprintf("%d of %d queries could not be read: %s", failed, len(slots), shortError(firstErr)))
	}
	if dropped > 0 {
		notes = append(notes, fmt.Sprintf("%d pods were left out: no address, a repeat, or beyond %d", dropped, dnsMaxPods))
	}
	assembleDNS(&out, live, slots[0:5], slots[5:10], up, slots[13], int(step/time.Second), &notes)
	if !out.hasData() {
		out.State = WindowEmpty
		out.Detail = fmt.Sprintf("Prometheus has no CoreDNS series for the pods in %s", namespace)
		out.Pods, out.Upstreams, out.Series, out.Now = nil, nil, DNSSeries{}, DNSFigures{}
		return out, nil
	}
	out.Detail = fmt.Sprintf("Prometheus, %s at %s, rate %s", short(span.Duration()), short(step), rw)
	out.Partial = strings.Join(notes, "; ")
	return out, nil
}

// dnsSlot is one query of a read and what it answered.
type dnsSlot struct {
	q    dnsQuery
	rng  bool
	rows []row
	err  error
}

// emptyUpstream is whether none of the upstream queries returned a series: not
// failed, answered with nothing.
func emptyUpstream(slots []*dnsSlot) bool {
	for _, s := range slots {
		if s.err != nil {
			return false
		}
		if len(s.rows) > 0 {
			return false
		}
	}
	return true
}

// --- Reading the answers ----------------------------------------------------

// row is one series of an answer: its labels, and its value (an instant
// query) or its points (a range query). A NaN or an infinity is a gap, never a
// zero: p99 of no samples is not 0 ms.
type row struct {
	labels map[string]string
	value  float64
	ok     bool
	points []Point
}

type promAny struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []any             `json:"value"`
			Values [][]any           `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// parseRows reads a vector or a matrix. An answer that is not a success is an
// error, and so is one that is not JSON.
func parseRows(raw []byte) ([]row, error) {
	var resp promAny
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("Prometheus answered with something unreadable: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("Prometheus rejected the query: %s", resp.Error)
	}
	out := make([]row, 0, len(resp.Data.Result))
	for _, r := range resp.Data.Result {
		x := row{labels: r.Metric}
		if len(r.Value) >= 2 {
			x.value, x.ok = sampleValue(r.Value)
		}
		for _, v := range r.Values {
			at, isNum := v[0].(float64)
			if len(v) < 2 || !isNum {
				continue
			}
			if val, ok := sampleValue(v); ok {
				x.points = append(x.points, Point{T: at, V: val})
			}
		}
		out = append(out, x)
	}
	return out, nil
}

// instanceIP is the address in an `instance` label, `ip:port` or `[ip]:port`,
// in the form netip prints, or "" when it is not an address.
func instanceIP(instance string) string {
	host, _, err := net.SplitHostPort(instance)
	if err != nil {
		host = instance
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	return a.String()
}

// lastBy is the last point of a series.
func lastPoint(points []Point) (Point, bool) {
	if len(points) == 0 {
		return Point{}, false
	}
	return points[len(points)-1], true
}

// total sums the one-series answer of a query that aggregates to a single row.
func single(rows []row) []Point {
	var best []Point
	for _, r := range rows {
		if len(r.points) > len(best) {
			best = r.points
		}
	}
	return best
}

func (r DNSRead) hasData() bool {
	return len(r.Series.QPS) > 0 || len(r.Series.ByRcode) > 0 || len(r.Series.P99) > 0 || len(r.Series.CacheHit) > 0 ||
		len(r.Upstreams) > 0 || r.scraped()
}

func (r DNSRead) scraped() bool {
	for _, p := range r.Pods {
		if p.Scraped {
			return true
		}
	}
	return false
}

// assembleDNS turns the answers into the read. A query that failed left its
// slot with no rows, and the caller has said so; nothing here is drawn from it.
func assembleDNS(out *DNSRead, live []DNSPodRef, inst, rng, up []*dnsSlot, ver *dnsSlot, step int, notes *[]string) {
	byIP := make(map[string]*DNSPod, len(live))
	out.Pods = make([]DNSPod, len(live))
	for i, p := range live {
		out.Pods[i] = DNSPod{DNSPodRef: p}
		byIP[p.IP] = &out.Pods[i]
	}
	podOf := func(r row) *DNSPod { return byIP[instanceIP(r.labels["instance"])] }

	for _, r := range inst[0].rows {
		if p := podOf(r); p != nil && r.ok {
			p.Figures.QPS, p.Scraped = r.value, true
		}
	}
	type codes struct{ total, nx, sf float64 }
	answered := map[*DNSPod]*codes{}
	for _, r := range inst[1].rows {
		p := podOf(r)
		if p == nil || !r.ok {
			continue
		}
		p.Scraped = true
		c := answered[p]
		if c == nil {
			c = &codes{}
			answered[p] = c
		}
		c.total += r.value
		switch r.labels["rcode"] {
		case "NXDOMAIN":
			c.nx += r.value
		case "SERVFAIL":
			c.sf += r.value
		}
	}
	for p, c := range answered {
		if c.total > 0 {
			p.Figures.NXDomain, p.Figures.ServFail, p.Figures.HasShares = c.nx/c.total, c.sf/c.total, true
		}
	}
	for _, r := range inst[2].rows {
		if p := podOf(r); p != nil && r.ok {
			p.Figures.P50, p.Scraped = r.value, true
		}
	}
	for _, r := range inst[3].rows {
		if p := podOf(r); p != nil && r.ok {
			p.Figures.P99, p.Figures.HasLatency, p.Scraped = r.value, true, true
		}
	}
	for _, r := range inst[4].rows {
		if p := podOf(r); p != nil && r.ok {
			p.Figures.CacheHit, p.Figures.HasCache, p.Scraped = r.value, true, true
		}
	}

	// The instances Prometheus knows in the namespace: the live ones carry
	// the version, the others are what was left out.
	versions := map[string]bool{}
	gone := map[string]bool{}
	for _, r := range ver.rows {
		if p := podOf(r); p != nil {
			p.Scraped = true
			if v := r.labels["version"]; v != "" {
				versions[v] = true
			}
			continue
		}
		gone[r.labels["instance"]] = true
	}
	if len(versions) == 1 {
		for v := range versions {
			out.Version = v
		}
	}
	if len(gone) > 0 {
		*notes = append(*notes, fmt.Sprintf("%d series of pods that no longer run were left out", len(gone)))
	}

	out.Series = DNSSeries{Step: step, QPS: single(rng[0].rows), P50: single(rng[2].rows), P99: single(rng[3].rows),
		CacheHit: single(rng[4].rows)}
	for _, r := range rng[1].rows {
		if len(r.points) == 0 {
			continue
		}
		if out.Series.ByRcode == nil {
			out.Series.ByRcode = map[string][]Point{}
		}
		code := r.labels["rcode"]
		if code == "" {
			code = "other"
		}
		out.Series.ByRcode[code] = r.points
	}
	out.Now = nowDNS(out.Series)

	ups := map[string]*DNSUpstream{}
	upstream := func(r row) *DNSUpstream {
		to := r.labels["to"]
		if to == "" {
			return nil
		}
		u := ups[to]
		if u == nil {
			u = &DNSUpstream{To: to}
			ups[to] = u
		}
		return u
	}
	for _, r := range up[0].rows {
		if u := upstream(r); u != nil && r.ok {
			u.QPS += r.value
			u.Answers = true
			if c := r.labels["rcode"]; c == "SERVFAIL" || c == "REFUSED" {
				u.ErrQPS += r.value
			}
		}
	}
	for _, r := range up[1].rows {
		if u := upstream(r); u != nil && r.ok {
			u.P99, u.HasLatency, u.Answers = r.value, true, true
		}
	}
	for _, r := range up[2].rows {
		if u := upstream(r); u != nil && r.ok {
			u.HealthFailPS = r.value
		}
	}
	for _, u := range ups {
		out.Upstreams = append(out.Upstreams, *u)
	}
	sort.Slice(out.Upstreams, func(i, j int) bool { return out.Upstreams[i].To < out.Upstreams[j].To })
}

// nowDNS is the cluster's last rate window, read off the end of the series: a
// range query's point is the rate over the window that ends at it.
func nowDNS(s DNSSeries) DNSFigures {
	var f DNSFigures
	if p, ok := lastPoint(s.QPS); ok {
		f.QPS = p.V
	}
	var at float64
	for _, pts := range s.ByRcode {
		if p, ok := lastPoint(pts); ok && p.T > at {
			at = p.T
		}
	}
	var total, nx, sf float64
	for code, pts := range s.ByRcode {
		if p, ok := lastPoint(pts); ok && p.T == at {
			total += p.V
			switch code {
			case "NXDOMAIN":
				nx = p.V
			case "SERVFAIL":
				sf = p.V
			}
		}
	}
	if total > 0 {
		f.NXDomain, f.ServFail, f.HasShares = nx/total, sf/total, true
	}
	if p, ok := lastPoint(s.P99); ok {
		f.P99, f.HasLatency = p.V, true
	}
	if p, ok := lastPoint(s.P50); ok {
		f.P50 = p.V
	}
	if p, ok := lastPoint(s.CacheHit); ok {
		f.CacheHit, f.HasCache = p.V, true
	}
	return f
}

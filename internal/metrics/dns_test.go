package metrics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// dnsProm is a Prometheus for the DNS queries: a rule is a substring of the
// PromQL and the rows it answers, the first rule that matches wins, and
// anything unmatched is an empty answer.
type dnsProm struct {
	mu    sync.Mutex
	asked []string
	rules []dnsRule
	err   func(promQL string) error
	delay time.Duration
}

type dnsRule struct{ contains, rows string }

func (p *dnsProm) answer(promQL string, matrix bool) ([]byte, error) {
	p.mu.Lock()
	p.asked = append(p.asked, promQL)
	p.mu.Unlock()
	if p.delay > 0 {
		time.Sleep(p.delay)
	}
	if p.err != nil {
		if err := p.err(promQL); err != nil {
			return nil, err
		}
	}
	rows := ""
	for _, r := range p.rules {
		if strings.Contains(promQL, r.contains) {
			rows = r.rows
			break
		}
	}
	kind := "vector"
	if matrix {
		kind = "matrix"
	}
	return fmt.Appendf(nil, `{"status":"success","data":{"resultType":%q,"result":[%s]}}`, kind, rows), nil
}

func (p *dnsProm) Query(_ context.Context, _, promQL string) ([]byte, error) {
	return p.answer(promQL, false)
}

func (p *dnsProm) QueryRange(_ context.Context, _, promQL string, _, _ time.Time, _ time.Duration) ([]byte, error) {
	return p.answer(promQL, true)
}

func (p *dnsProm) queries() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.asked...)
}

func vec(labels, value string) string {
	return fmt.Sprintf(`{"metric":{%s},"value":[1788200100,%q]}`, labels, value)
}

func mat(labels string, values ...string) string {
	var pts []string
	for i, v := range values {
		pts = append(pts, fmt.Sprintf(`[%d,%q]`, 1788200100+i*15, v))
	}
	return fmt.Sprintf(`{"metric":{%s},"values":[%s]}`, labels, strings.Join(pts, ","))
}

var dnsPods = []DNSPodRef{
	{Name: "coredns-b", Node: "n2", IP: "10.244.0.8"},
	{Name: "coredns-a", Node: "n1", IP: "10.244.0.7"},
}

// healthyDNS is two pods and one upstream, answered under the coredns_proxy
// family.
func healthyDNS() *dnsProm {
	return &dnsProm{rules: []dnsRule{
		{`coredns_build_info`, strings.Join([]string{
			vec(`"instance":"10.244.0.7:9153","version":"1.12.1"`, "1"),
			vec(`"instance":"10.244.0.8:9153","version":"1.12.1"`, "1"),
			vec(`"instance":"10.244.0.99:9153","version":"1.11.0"`, "1"), // a pod that is gone
		}, ",")},
		{`sum by (instance, rcode)`, strings.Join([]string{
			vec(`"instance":"10.244.0.7:9153","rcode":"NOERROR"`, "90"),
			vec(`"instance":"10.244.0.7:9153","rcode":"SERVFAIL"`, "10"),
			vec(`"instance":"10.244.0.8:9153","rcode":"NOERROR"`, "50"),
			vec(`"instance":"10.244.0.8:9153","rcode":"NXDOMAIN"`, "50"),
		}, ",")},
		{`sum by (instance) (rate(coredns_dns_requests_total`, strings.Join([]string{
			vec(`"instance":"10.244.0.7:9153"`, "100"), vec(`"instance":"10.244.0.8:9153"`, "100")}, ",")},
		{`histogram_quantile(0.5, sum by (le, instance)`, vec(`"instance":"10.244.0.7:9153"`, "0.0002")},
		{`histogram_quantile(0.99, sum by (le, instance)`, strings.Join([]string{
			vec(`"instance":"10.244.0.7:9153"`, "1.9"), vec(`"instance":"10.244.0.8:9153"`, "NaN")}, ",")},
		{`sum by (instance) (rate(coredns_cache_hits_total`, vec(`"instance":"10.244.0.7:9153"`, "0.33")},

		{`sum by (rcode)`, strings.Join([]string{
			mat(`"rcode":"NOERROR"`, "140", "140"), mat(`"rcode":"SERVFAIL"`, "10", "30"), mat(`"rcode":"NXDOMAIN"`, "50", "30")}, ",")},
		{`histogram_quantile(0.5, sum by (le) (`, mat(``, "0.0002", "0.0003")},
		{`histogram_quantile(0.99, sum by (le) (`, mat(``, "1.5", "1.9")},
		{`sum (rate(coredns_cache_hits_total`, mat(``, "0.3", "0.4")},
		{`sum (rate(coredns_dns_requests_total`, mat(``, "190", "200")},

		{`sum by (to, rcode)`, strings.Join([]string{
			vec(`"to":"172.18.0.1:53","rcode":"NOERROR"`, "0.8"), vec(`"to":"172.18.0.1:53","rcode":"SERVFAIL"`, "0.2")}, ",")},
		{`histogram_quantile(0.99, sum by (le, to)`, vec(`"to":"172.18.0.1:53"`, "0.0146")},
		{`sum by (to) (rate(coredns_proxy_healthcheck`, strings.Join([]string{
			vec(`"to":"172.18.0.1:53"`, "0"), vec(`"to":"192.0.2.1:53"`, "0.81")}, ",")},
	}}
}

func readDNS(t *testing.T, q RangeQuerier, span DNSSpan, pods []DNSPodRef) DNSRead {
	t.Helper()
	got, err := ReadDNS(context.Background(), q, "prod", span, "kube-system", pods)
	if err != nil {
		t.Fatalf("ReadDNS: %v", err)
	}
	return got
}

func TestReadDNSHasFourStates(t *testing.T) {
	var nilQ RangeQuerier
	if got := readDNS(t, nilQ, DNSSpan15m, dnsPods); got.State != WindowNoPrometheus || got.Detail == "" {
		t.Errorf("no Prometheus: %+v", got)
	}
	down := &dnsProm{err: func(string) error { return errors.New("dial tcp: connection refused") }}
	if got := readDNS(t, down, DNSSpan15m, dnsPods); got.State != WindowUnreadable || !strings.Contains(got.Detail, "refused") {
		t.Errorf("unreadable: %+v", got)
	}
	none := NoHistory{Sentence: "no Prometheus found in prod"}
	absent := &dnsProm{err: func(string) error { return none }}
	if got := readDNS(t, absent, DNSSpan15m, dnsPods); got.State != WindowNoPrometheus {
		t.Errorf("a cluster with no Prometheus: %+v", got)
	}
	empty := &dnsProm{}
	got := readDNS(t, empty, DNSSpan15m, dnsPods)
	if got.State != WindowEmpty || got.Detail == "" || got.Pods != nil {
		t.Errorf("empty: %+v", got)
	}
	if n := len(empty.queries()); n != 17 {
		// 14 fixed queries, and the three upstream ones again under the old family.
		t.Errorf("an empty read asked %d queries, want 14 + 3 for the old family", n)
	}
	read := readDNS(t, healthyDNS(), DNSSpan15m, dnsPods)
	if read.State != WindowRead || !strings.Contains(read.Detail, "15m at 15s") {
		t.Fatalf("read: %+v", read)
	}
}

func TestReadDNSFigures(t *testing.T) {
	prom := healthyDNS()
	got := readDNS(t, prom, DNSSpan15m, dnsPods)
	if n := len(prom.queries()); n != 14 {
		t.Errorf("a read with the proxy family present asked %d queries, want 14", n)
	}
	if got.Version != "1.12.1" {
		t.Errorf("version %q: the gone pod's 1.11.0 must not count", got.Version)
	}
	if got.Now.QPS != 200 || !got.Now.HasLatency || got.Now.P99 != 1.9 || !got.Now.HasCache || got.Now.CacheHit != 0.4 {
		t.Errorf("now = %+v", got.Now)
	}
	// 30 of 90 at the last point: SERVFAIL 30, NXDOMAIN 30, NOERROR 140.
	if !got.Now.HasShares || got.Now.ServFail != 0.15 || got.Now.NXDomain != 0.15 {
		t.Errorf("shares = %+v", got.Now)
	}
	if len(got.Series.ByRcode) != 3 || len(got.Series.QPS) != 2 || got.Series.Step != 15 {
		t.Errorf("series = %+v", got.Series)
	}
	if len(got.Pods) != 2 || got.Pods[0].Name != "coredns-a" || got.Pods[1].Name != "coredns-b" {
		t.Fatalf("pods = %+v, want a and b by name", got.Pods)
	}
	a, b := got.Pods[0], got.Pods[1]
	if a.Figures.QPS != 100 || a.Figures.ServFail != 0.1 || !a.Figures.HasLatency || a.Figures.P99 != 1.9 ||
		!a.Figures.HasCache || a.Figures.CacheHit != 0.33 {
		t.Errorf("pod a = %+v", a)
	}
	// A NaN p99 and no cache series are absent, never zero.
	if b.Figures.HasLatency || b.Figures.HasCache || b.Figures.NXDomain != 0.5 {
		t.Errorf("pod b = %+v", b)
	}
	if !a.Scraped || !b.Scraped {
		t.Errorf("scraped: %v %v", a.Scraped, b.Scraped)
	}
	// The stale instance is dropped and counted.
	if !strings.Contains(got.Partial, "1 series of pods that no longer run") {
		t.Errorf("partial = %q", got.Partial)
	}
	if len(got.Upstreams) != 2 {
		t.Fatalf("upstreams = %+v", got.Upstreams)
	}
	good, dead := got.Upstreams[0], got.Upstreams[1]
	if good.To != "172.18.0.1:53" || good.QPS != 1 || good.ErrQPS != 0.2 || !good.HasLatency || good.P99 != 0.0146 || !good.Answers {
		t.Errorf("good upstream = %+v", good)
	}
	// Seen only in its health-check failures: listed, with no latency.
	if dead.To != "192.0.2.1:53" || dead.Answers || dead.HasLatency || dead.HealthFailPS != 0.81 {
		t.Errorf("dead upstream = %+v", dead)
	}
}

func TestReadDNSFallsBackToTheForwardFamily(t *testing.T) {
	prom := healthyDNS()
	// 1.10 and older: the coredns_proxy_* names answer nothing, the old ones do.
	for i, r := range prom.rules {
		if strings.Contains(r.contains, "to") {
			prom.rules[i].rows = ""
		}
	}
	prom.rules = append([]dnsRule{
		{`coredns_forward_request_duration_seconds_count`, vec(`"to":"10.0.0.2:53","rcode":"NOERROR"`, "2")},
		{`coredns_forward_request_duration_seconds_bucket`, vec(`"to":"10.0.0.2:53"`, "0.02")},
		{`coredns_forward_healthcheck_failures_total`, vec(`"to":"10.0.0.2:53"`, "0")},
	}, prom.rules...)
	got := readDNS(t, prom, DNSSpan15m, dnsPods)
	if len(got.Upstreams) != 1 || got.Upstreams[0].To != "10.0.0.2:53" || got.Upstreams[0].QPS != 2 || got.Upstreams[0].P99 != 0.02 {
		t.Fatalf("upstreams = %+v", got.Upstreams)
	}
	var forward, proxy int
	for _, q := range prom.queries() {
		if strings.Contains(q, "coredns_forward_") {
			forward++
			if strings.Contains(q, "proxy_name") {
				t.Errorf("the old family has no proxy_name label: %s", q)
			}
		}
		if strings.Contains(q, "coredns_proxy_") {
			proxy++
			if !strings.Contains(q, `proxy_name="forward"`) {
				t.Errorf("the new family is pinned to the forward plugin: %s", q)
			}
		}
	}
	if forward != 3 || proxy != 3 {
		t.Errorf("asked %d old and %d new upstream queries, want 3 and 3", forward, proxy)
	}
}

func TestTheOldFamilyIsNotAskedWhenTheNewOneAnswers(t *testing.T) {
	prom := healthyDNS()
	readDNS(t, prom, DNSSpan15m, dnsPods)
	for _, q := range prom.queries() {
		if strings.Contains(q, "coredns_forward_") {
			t.Errorf("the old family was asked although the new one answered: %s", q)
		}
	}
}

func TestPodAddressesAreEscapedBeforeTheyReachPromQL(t *testing.T) {
	if got, want := instanceRegex([]string{"10.244.0.7", "fd00::7"}), `(10\.244\.0\.7|\[fd00::7\]):[0-9]+`; got != want {
		t.Errorf("regex = %s, want %s", got, want)
	}
	// An address is never a regex: a dot is a dot.
	prom := healthyDNS()
	readDNS(t, prom, DNSSpan15m, []DNSPodRef{{Name: "a", IP: "10.244.0.7"}, {Name: "b", IP: `10.2";}) or up{x=~".*`},
		{Name: "c", IP: "10.244.0.7"}, {Name: "d", IP: "fd00::7"}})
	asked := prom.queries()
	for _, q := range asked {
		if strings.Contains(q, "up{x") || strings.Contains(q, ` or `) {
			t.Fatalf("a hostile address reached a query: %s", q)
		}
	}
	want := `instance=~"(10\\.244\\.0\\.7|\\[fd00::7\\]):[0-9]+"`
	var carried int
	for _, q := range asked {
		if strings.Contains(q, "{namespace=") && !strings.Contains(q, "coredns_build_info") {
			carried++
			if !strings.Contains(q, want) {
				t.Errorf("query lacks the escaped selector %s:\n%s", want, q)
			}
		}
	}
	if carried == 0 {
		t.Fatal("no query carried a selector")
	}
	got := readDNS(t, healthyDNS(), DNSSpan15m, []DNSPodRef{{Name: "a", IP: "10.244.0.7"}, {Name: "b", IP: "nope"}})
	if !strings.Contains(got.Partial, "1 pods were left out") {
		t.Errorf("the pod with no address was not counted: %q", got.Partial)
	}
}

func TestOnlyTheSpanAndAValidatedNamespaceBuildAQuery(t *testing.T) {
	prom := healthyDNS()
	if _, err := ReadDNS(context.Background(), prom, "prod", DNSSpan(`15m"} or up{`), "kube-system", dnsPods); !errors.Is(err, ErrBadDNSSpan) {
		t.Errorf("a span that is not one: %v", err)
	}
	for _, ns := range []string{"", `kube-system"} or up{`, "Kube System"} {
		if _, err := ReadDNS(context.Background(), prom, "prod", DNSSpan15m, ns, dnsPods); !errors.Is(err, ErrBadNamespace) {
			t.Errorf("namespace %q: %v", ns, err)
		}
	}
	if n := len(prom.queries()); n != 0 {
		t.Errorf("a refused input still asked %d queries", n)
	}
	for _, s := range []DNSSpan{DNSSpan15m, DNSSpan1h, DNSSpan6h} {
		if s.Duration() == 0 || s.Step() == 0 || s.rate() < 3*time.Minute || s.rate() < 2*s.Step() {
			t.Errorf("span %s: %s at %s, rate %s", s, s.Duration(), s.Step(), s.rate())
		}
	}
}

func TestAFailedQueryIsPartialNotEmpty(t *testing.T) {
	prom := healthyDNS()
	prom.err = func(q string) error {
		if strings.Contains(q, "coredns_cache_hits_total") && !strings.Contains(q, "by (instance)") {
			return errors.New("query timed out")
		}
		return nil
	}
	got := readDNS(t, prom, DNSSpan15m, dnsPods)
	if got.State != WindowRead || got.Now.HasCache || !strings.Contains(got.Partial, "1 of 14 queries could not be read: query timed out") {
		t.Errorf("got %s, cache %v, partial %q", got.State, got.Now.HasCache, got.Partial)
	}
}

func TestADNSReadLeavesNoGoroutineBehind(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	prom := healthyDNS()
	prom.delay = 2 * time.Millisecond
	readDNS(t, prom, DNSSpan1h, dnsPods)
	readDNS(t, &dnsProm{}, DNSSpan6h, dnsPods)
}

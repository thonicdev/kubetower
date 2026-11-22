package extension

import (
	"fmt"
	"time"
)

// DNSSpan is how far back DNSWindow reads: an enum, never a free range, so
// nothing the person typed reaches a query but one of these three.
type DNSSpan string

// The spans, and the step each is read at: 15 s, 30 s and 2 min.
const (
	DNSSpan15m DNSSpan = "15m"
	DNSSpan1h  DNSSpan = "1h"
	DNSSpan6h  DNSSpan = "6h"
)

// Valid is whether s is one of the three spans.
func (s DNSSpan) Valid() bool {
	switch s {
	case DNSSpan15m, DNSSpan1h, DNSSpan6h:
		return true
	}
	return false
}

// Duration is how far back the span reads, and Step the distance between two
// points. Both are zero for a span that is not one.
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

// Step is the distance between two points of a series over s.
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

// ParseDNSSpan reads a span from text, refusing anything that is not one.
func ParseDNSSpan(text string) (DNSSpan, error) {
	if s := DNSSpan(text); s.Valid() {
		return s, nil
	}
	return "", fmt.Errorf("%q is not a DNS span; it is one of %s, %s or %s", text, DNSSpan15m, DNSSpan1h, DNSSpan6h)
}

// DNSFigures is what CoreDNS did over one rate window. A share is 0..1.
// HasLatency and HasCache are false when the window held no sample to compute
// them from: the figure is then absent, never a zero (a p99 of nothing is not
// 0 ms).
type DNSFigures struct {
	QPS      float64
	NXDomain float64
	ServFail float64
	P50      float64 // seconds
	P99      float64 // seconds
	CacheHit float64

	HasShares  bool
	HasLatency bool
	HasCache   bool
}

// DNSSeries is the cluster-wide figures over the span, oldest sample first.
type DNSSeries struct {
	// Step is the seconds between two samples.
	Step     int
	QPS      []Sample
	ByRcode  map[string][]Sample // queries per second by answer code
	P50      []Sample            // seconds
	P99      []Sample            // seconds
	CacheHit []Sample            // 0..1
}

// DNSPod is one CoreDNS pod's figures.
type DNSPod struct {
	Pod     string
	Node    string
	IP      string
	Figures DNSFigures
	// Scraped is whether Prometheus held a series for this pod. A pod with
	// none is listed, not drawn as idle.
	Scraped bool
}

// DNSUpstream is one upstream the forward plugin talks to.
type DNSUpstream struct {
	To           string // address as CoreDNS dials it, "172.18.0.1:53"
	QPS          float64
	ErrQPS       float64 // answers that were SERVFAIL or REFUSED
	P99          float64 // seconds
	HasLatency   bool
	HealthFailPS float64 // health-check failures per second
	// Answers is false when the upstream is listed only from its health-check
	// failures: it never answered, so it has no latency to draw.
	Answers bool
}

// DNS is CoreDNS's own metrics over a span, from the cluster's Prometheus.
type DNS struct {
	State WindowState
	// Detail is a short reason for every state, and for DNSWindow's read the
	// window and step it covers.
	Detail    string
	Now       DNSFigures
	Series    DNSSeries
	Pods      []DNSPod      // sorted by name
	Upstreams []DNSUpstream // sorted by address
	// Version is CoreDNS's version when every pod agrees, else empty.
	Version string
	// Namespace is where the CoreDNS pods were found.
	Namespace string
	// Partial says what could not be read: series dropped for lack of a live
	// pod, or queries that failed. Empty when nothing was left out.
	Partial string
	ReadAt  time.Time
}

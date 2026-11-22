package dns

import (
	"fmt"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/extension"
)

// The ceilings a large cluster is held to: 200 CoreDNS pods against three
// upstreams over the longest span, answered in under 1 MiB and under a second.
// The read itself (and the one Prometheus read shared by 50 concurrent
// displays) is the door's, held by its own test in internal/web.
const (
	loadPods    = 200
	loadUps     = 3
	maxBytes    = 1 << 20
	maxLoadTime = time.Second
)

func loadAnswer() extension.DNS {
	d := extension.DNS{State: extension.WindowRead, Namespace: "kube-system",
		Now:    extension.DNSFigures{QPS: 9e3, HasShares: true, HasLatency: true, HasCache: true},
		Series: extension.DNSSeries{Step: 120, ByRcode: map[string][]extension.Sample{}}}
	span := extension.DNSSpan6h
	n := int(span.Duration() / span.Step())
	line := func(f func(i int) float64) []extension.Sample {
		out := make([]extension.Sample, n)
		for i := range out {
			out[i] = extension.Sample{T: float64(1e9 + i*120), V: f(i)}
		}
		return out
	}
	val := func(i int) float64 { return 0.123456789 * float64(i%97) }
	d.Series.QPS, d.Series.P50, d.Series.P99, d.Series.CacheHit = line(val), line(val), line(val), line(val)
	for _, code := range []string{"NOERROR", "NXDOMAIN", "SERVFAIL", "REFUSED", "FORMERR"} {
		d.Series.ByRcode[code] = line(val)
	}
	for i := 0; i < loadPods; i++ {
		d.Pods = append(d.Pods, extension.DNSPod{Pod: fmt.Sprintf("coredns-%03d-7f9c8d6b5-abcde", i), Node: fmt.Sprintf("ip-10-0-%d-%d.eu-west-1.compute.internal", i%250, i%200),
			Scraped: true, Figures: extension.DNSFigures{QPS: 1.5, NXDomain: .1, ServFail: .01, P50: .001, P99: .03, CacheHit: .9,
				HasShares: true, HasLatency: true, HasCache: true}})
	}
	for i := 0; i < loadPods*loadUps; i++ {
		d.Upstreams = append(d.Upstreams, extension.DNSUpstream{To: fmt.Sprintf("10.%d.%d.%d:53", i%250, i/250, i%7), QPS: 1, P99: .02, HasLatency: true, Answers: true})
	}
	return d
}

func TestLoadAnAnswerForTwoHundredPodsIsSmallAndFast(t *testing.T) {
	h := &fakeHost{answer: loadAnswer()}
	start := time.Now()
	rec, v := get(t, h, "?span=6h")
	if took := time.Since(start); took > maxLoadTime {
		t.Fatalf("handler took %v, want under %v", took, maxLoadTime)
	}
	if rec.Body.Len() > maxBytes {
		t.Fatalf("answer is %d bytes, want under %d", rec.Body.Len(), maxBytes)
	}
	if len(v.Pods) != loadPods || len(v.Upstreams) != loadPods*loadUps {
		t.Fatalf("pods %d upstreams %d", len(v.Pods), len(v.Upstreams))
	}
	t.Logf("answer: %d bytes", rec.Body.Len())
}

// Package dns is the DNS page: CoreDNS's own health, read from the cluster's
// Prometheus through Host.DNSWindow. It is an extension of the console, part
// of the core and behind its own per-cluster switch, off by default.
//
// It reads and nothing else. The queries are the core's, fixed and selected by
// the live CoreDNS pods; no PromQL is written here, and the person is reviewed
// by the door (list pods in the namespace CoreDNS runs in). The answer is
// compact: figures that are absent are null, never zero, and a state other
// than read carries no figures. It has no goroutine and no cache of its own:
// the door holds the window and shares one read between displays.
package dns

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

// Key is `dns`: the page's URL segment, the `KT_EXTRAS` name and the key of its
// per-cluster switch.
const Key = "dns"

// Label is the menu entry.
const Label = "DNS"

// DefaultSpan is the span of a request that names none.
const DefaultSpan = extension.DNSSpan1h

// The codes the route answers with.
const (
	CodeUnknownCluster = "unknown_cluster"
	CodeBadSpan        = "bad_span"
	CodeReadFailed     = "read_failed"
)

// DNS is the extension `cmd/kubetower` registers.
type DNS struct{}

// New is the constructor `cmd/kubetower` calls.
func New() *DNS { return &DNS{} }

func (*DNS) Key() string      { return Key }
func (*DNS) Label() string    { return Label }
func (*DNS) Legacy() []string { return nil }

func (*DNS) Summary() string {
	return "CoreDNS health from its Prometheus metrics: queries per second, NXDOMAIN and SERVFAIL share, p50 and p99 latency, cache hit rate, per pod and per upstream."
}

func (*DNS) Needs() string {
	return "CoreDNS with its metrics scraped by a Prometheus; list pods in the namespace CoreDNS runs in"
}

// Component names what the suggestion looks for.
func (*DNS) Component() string { return "CoreDNS metrics" }

// Routes is one read: nothing here changes a cluster.
func (*DNS) Routes(h extension.Host) []extension.Route {
	return []extension.Route{{Method: http.MethodGet, Pattern: "/stats", Handler: stats(h)}}
}

type problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		http.Error(w, "the answer could not be encoded", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}

func writeProblem(w http.ResponseWriter, status int, code, message string, err error) {
	p := problem{Code: code, Message: message}
	if err != nil {
		p.Detail = textutil.FirstLineN(err.Error(), 300)
	}
	writeJSON(w, status, p)
}

// stats answers `GET /stats?span=15m|1h|6h`.
func stats(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		text := r.URL.Query().Get("span")
		if text == "" {
			text = string(DefaultSpan)
		}
		span, err := extension.ParseDNSSpan(text)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, CodeBadSpan, `"`+text+`" is not a span: 15m, 1h or 6h.`, nil)
			return
		}
		got, err := h.DNSWindow(r, cluster, span)
		if err != nil {
			switch {
			case errors.Is(err, extension.ErrUnknownCluster):
				writeProblem(w, http.StatusNotFound, CodeUnknownCluster, extension.UnknownClusterText(cluster), err)
			case r.Context().Err() != nil:
				// The page went away.
			default:
				if refused, ok := extension.RefusedRead(err, "The cluster says you may not list the CoreDNS pods, so nothing about DNS can be shown."); ok {
					writeProblem(w, refused.Status, refused.Code, refused.Message, err)
					return
				}
				writeProblem(w, http.StatusBadGateway, CodeReadFailed, "The DNS figures could not be read.", err)
			}
			return
		}
		view := build(got, span)
		if got.State == extension.WindowRead {
			// The Corefile names the upstreams that never answered, which
			// leave no series; a read that does not come back changes nothing.
			view.Upstreams = mergeConfigured(view.Upstreams, corefile(h, r, cluster, got.Namespace))
			sort.Slice(view.Upstreams, func(i, j int) bool { return view.Upstreams[i].To < view.Upstreams[j].To })
		}
		writeJSON(w, http.StatusOK, view)
	})
}

// Figures is a window's figures; a null is a figure the window did not hold.
type Figures struct {
	QPS      float64  `json:"qps"`
	NXDomain *float64 `json:"nxdomain"`
	ServFail *float64 `json:"servfail"`
	P50      *float64 `json:"p50"`
	P99      *float64 `json:"p99"`
	CacheHit *float64 `json:"cacheHit"`
}

// Pod is one CoreDNS pod. Figures is null for a pod Prometheus has no series
// for: listed, not drawn as idle.
type Pod struct {
	Pod     string   `json:"pod"`
	Node    string   `json:"node,omitempty"`
	Figures *Figures `json:"figures"`
}

// Upstream is one upstream. P99 is null for one that never answered, and
// Answers says so: either its health checks fail, or it is listed only because
// the Corefile names it.
type Upstream struct {
	To       string   `json:"to"`
	QPS      float64  `json:"qps"`
	ErrQPS   float64  `json:"errQps"`
	P99      *float64 `json:"p99"`
	HealthPS float64  `json:"healthFailPs"`
	Answers  bool     `json:"answers"`
	// Zones are the zones the Corefile forwards to it, when it was read.
	Zones []string `json:"zones,omitempty"`
}

// Series is the cluster-wide figures over the span as [unix seconds, value]
// pairs, oldest first. A point that was not finite is left out.
type Series struct {
	Step     int                     `json:"step"`
	QPS      [][2]float64            `json:"qps"`
	ByRcode  map[string][][2]float64 `json:"byRcode"`
	P50      [][2]float64            `json:"p50"`
	P99      [][2]float64            `json:"p99"`
	CacheHit [][2]float64            `json:"cacheHit"`
}

// View is the answer.
type View struct {
	State     string     `json:"state"`
	Detail    string     `json:"detail,omitempty"`
	Span      string     `json:"span"`
	Namespace string     `json:"namespace,omitempty"`
	Version   string     `json:"version,omitempty"`
	Partial   string     `json:"partial,omitempty"`
	ReadAt    *time.Time `json:"readAt,omitempty"`
	Now       *Figures   `json:"now,omitempty"`
	Series    *Series    `json:"series,omitempty"`
	Pods      []Pod      `json:"pods"`
	Upstreams []Upstream `json:"upstreams"`
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func ptr(ok bool, v float64) *float64 {
	if !ok || !finite(v) {
		return nil
	}
	return &v
}

func figures(f extension.DNSFigures) *Figures {
	return &Figures{QPS: num(f.QPS),
		NXDomain: ptr(f.HasShares, f.NXDomain), ServFail: ptr(f.HasShares, f.ServFail),
		P50: ptr(f.HasLatency, f.P50), P99: ptr(f.HasLatency, f.P99), CacheHit: ptr(f.HasCache, f.CacheHit)}
}

func num(v float64) float64 {
	if !finite(v) {
		return 0
	}
	return v
}

func points(in []extension.Sample) [][2]float64 {
	out := make([][2]float64, 0, len(in))
	for _, s := range in {
		if finite(s.T) && finite(s.V) {
			out = append(out, [2]float64{s.T, s.V})
		}
	}
	return out
}

func build(got extension.DNS, span extension.DNSSpan) View {
	v := View{State: string(got.State), Detail: textutil.FirstLineN(got.Detail, 300), Span: string(span),
		Namespace: got.Namespace, Pods: []Pod{}, Upstreams: []Upstream{}}
	if got.State != extension.WindowRead {
		return v
	}
	v.Version, v.Partial = got.Version, textutil.FirstLineN(got.Partial, 300)
	if !got.ReadAt.IsZero() {
		at := got.ReadAt
		v.ReadAt = &at
	}
	v.Now = figures(got.Now)
	s := &Series{Step: got.Series.Step, QPS: points(got.Series.QPS), P50: points(got.Series.P50),
		P99: points(got.Series.P99), CacheHit: points(got.Series.CacheHit),
		ByRcode: make(map[string][][2]float64, len(got.Series.ByRcode))}
	for code, in := range got.Series.ByRcode {
		s.ByRcode[code] = points(in)
	}
	v.Series = s
	for _, p := range got.Pods {
		pod := Pod{Pod: p.Pod, Node: p.Node}
		if p.Scraped {
			pod.Figures = figures(p.Figures)
		}
		v.Pods = append(v.Pods, pod)
	}
	sort.Slice(v.Pods, func(i, j int) bool { return v.Pods[i].Pod < v.Pods[j].Pod })
	for _, u := range got.Upstreams {
		v.Upstreams = append(v.Upstreams, Upstream{To: u.To, QPS: num(u.QPS), ErrQPS: num(u.ErrQPS),
			P99: ptr(u.HasLatency && u.Answers, u.P99), HealthPS: num(u.HealthFailPS), Answers: u.Answers})
	}
	sort.Slice(v.Upstreams, func(i, j int) bool { return v.Upstreams[i].To < v.Upstreams[j].To })
	return v
}

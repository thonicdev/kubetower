package jvm

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/safego"
	"github.com/thonicdev/kubetower/textutil"
)

// A Java pod's memory history: seven days of each container's working set,
// from the cluster's Prometheus, through Host.UsageWindow.
//
// **Through that door and no other.** Prometheus has no RBAC, so a query built
// from a request would read any series for anyone: the core runs its own fixed
// queries, narrowed to the namespaces the person may list pods in, and this
// route only picks the pod's own containers out of the answer. The pod itself
// is read first, by name, through the reviewed list, so a person who cannot
// list the pod gets no history of it. Nothing about the JVM's own metrics
// (heap and non-heap pools, whose names depend on the metrics library) is
// read: that needs a typed method of its own on Host, which does not exist.
//
// The scale and the points are computed here and the page draws them as
// given: the chart's arithmetic stays in Go, and a pod's answer
// holds at most MaxHistorySeries x MaxHistoryPoints points whatever the
// window's size.

const (
	// MaxHistoryPoints is the most points one container's line holds: a week at
	// fifteen minutes is 672, and each bucket of the downsampling keeps its
	// peak, so a spike is never averaged away.
	MaxHistoryPoints = 336
	// MaxHistorySeries is the most containers drawn.
	MaxHistorySeries = 16
)

// historyWait is how long the request waits for the window before it answers
// `pending`: the core's first read of a week on a large cluster takes far
// longer, goes on reading on a context of its own and keeps the result, so
// the page asks again. A variable so a test can shorten it.
var historyWait = 12 * time.Second

// The states of a history. "read" has at least one container's line; every
// other state has none and says why, so an unread history is never drawn as a
// pod that used nothing.
const (
	historyRead       = "read"
	historyAbsent     = "absent"     // no Prometheus: not configured, or the cluster runs none
	historyUnreadable = "unreadable" // a Prometheus there that could not be asked or refused
	historyEmpty      = "empty"      // Prometheus answered, with nothing for this pod
	historyPending    = "pending"    // still being read: ask again
)

const CodeHistoryFailed = "history_failed"

// tick is a horizontal gridline: Y is its place on the 0..100 scale from the
// top, Text its value.
type tick struct {
	Y    float64 `json:"y"`
	Text string  `json:"text"`
}

// line is one container's memory, oldest first.
type line struct {
	Container string `json:"container"`
	// Points are [x, y] on a 0..100 box, x from the window's start, y from the
	// top: the page draws them as given.
	Points [][2]float64 `json:"points"`
	Last   *Figure      `json:"last"`
	Min    *Figure      `json:"min"`
	Max    *Figure      `json:"max"`
	// Limit is the container's memory limit, with its place on the scale when
	// it is on it; null when none is set.
	Limit   *Memory  `json:"limit"`
	LimitAt *float64 `json:"limitAt"`
}

// historyAnswer is `GET /pod/{namespace}/{name}/history`.
type historyAnswer struct {
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	State     string `json:"state"`
	// Detail is the core's one line on the window (what it covers, or why
	// there is none); it is for a tooltip.
	Detail string `json:"detail,omitempty"`
	// Partial is set when some of the window could not be read.
	Partial string `json:"partial,omitempty"`
	// From and To are the window's span in Unix seconds, for the axis.
	From   int64   `json:"from,omitempty"`
	To     int64   `json:"to,omitempty"`
	Scale  *Figure `json:"scale,omitempty"`
	Ticks  []tick  `json:"ticks,omitempty"`
	Series []line  `json:"series"`
	// Cut is set when the pod has more containers than MaxHistorySeries.
	Cut bool `json:"cut,omitempty"`
}

// stateOfWindow is the history state of a window that holds no line of the pod.
func stateOfWindow(w extension.Window) string {
	switch w.State {
	case extension.WindowNoPrometheus:
		return historyAbsent
	case extension.WindowUnreadable:
		return historyUnreadable
	case extension.WindowRead:
		if w.MemoryFailed != "" {
			return historyUnreadable
		}
	}
	return historyEmpty
}

// downsample keeps at most max points of s, the largest of each bucket, in
// time order.
func downsample(s []extension.Sample, max int) []extension.Sample {
	if len(s) <= max {
		return s
	}
	out := make([]extension.Sample, 0, max)
	for i := 0; i < max; i++ {
		lo, hi := i*len(s)/max, (i+1)*len(s)/max
		best := s[lo]
		for _, p := range s[lo:hi] {
			if p.V > best.V {
				best = p
			}
		}
		out = append(out, best)
	}
	return out
}

// buildHistory is the answer for pod p out of window w: one line per
// container of the spec that the window holds memory for.
func buildHistory(p *corev1.Pod, w extension.Window) historyAnswer {
	out := historyAnswer{Cluster: "", Namespace: p.Namespace, Pod: p.Name, State: historyRead,
		Detail: textutil.FirstLineN(w.Detail, 300), Partial: textutil.FirstLineN(w.Partial, 300), Series: []line{}}
	type found struct {
		name    string
		samples []extension.Sample
		limit   *Memory
	}
	var got []found
	for _, c := range p.Spec.Containers {
		cw, ok := w.Containers[extension.ContainerKey(p.Namespace, p.Name, c.Name)]
		if !ok || len(cw.Memory) == 0 {
			continue
		}
		if len(got) == MaxHistorySeries {
			out.Cut = true
			break
		}
		got = append(got, found{c.Name, downsample(cw.Memory, MaxHistoryPoints), memoryOf(c.Resources.Limits)})
	}
	if len(got) == 0 {
		out.State = stateOfWindow(w)
		return out
	}
	var t0, t1, top float64 = math.MaxFloat64, 0, 0
	for _, g := range got {
		t0, t1 = math.Min(t0, g.samples[0].T), math.Max(t1, g.samples[len(g.samples)-1].T)
		for _, s := range g.samples {
			top = math.Max(top, s.V)
		}
		if g.limit != nil {
			top = math.Max(top, float64(g.limit.Bytes))
		}
	}
	scale := niceCeil(top)
	out.From, out.To, out.Scale = int64(t0), int64(t1), figure(int64(scale))
	for _, f := range []float64{1, 0.5, 0} {
		out.Ticks = append(out.Ticks, tick{Y: round1((1 - f) * 100), Text: textutil.Bytes(int64(scale * f))})
	}
	span := t1 - t0
	for _, g := range got {
		l := line{Container: g.name, Points: make([][2]float64, 0, len(g.samples)), Limit: g.limit}
		lo, hi := g.samples[0].V, g.samples[0].V
		for _, s := range g.samples {
			x := 0.0
			if span > 0 {
				x = (s.T - t0) / span * 100
			}
			l.Points = append(l.Points, [2]float64{round1(x), round1((1 - s.V/scale) * 100)})
			lo, hi = math.Min(lo, s.V), math.Max(hi, s.V)
		}
		l.Last, l.Min, l.Max = figure(int64(g.samples[len(g.samples)-1].V)), figure(int64(lo)), figure(int64(hi))
		if g.limit != nil && g.limit.Bytes > 0 {
			at := round1((1 - float64(g.limit.Bytes)/scale) * 100)
			l.LimitAt = &at
		}
		out.Series = append(out.Series, l)
	}
	sort.SliceStable(out.Series, func(i, j int) bool { return out.Series[i].Max.Bytes > out.Series[j].Max.Bytes })
	return out
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// niceCeil rounds v up to the next eighth of a power of two (a half, five
// eighths, three quarters, seven eighths or the whole), so the gridlines read
// as round binary figures: 256 MiB, 384 MiB, 512 MiB.
func niceCeil(v float64) float64 {
	if v <= 0 {
		return 1
	}
	mag := math.Pow(2, math.Ceil(math.Log2(v)))
	for _, f := range []float64{0.5, 0.625, 0.75, 0.875, 1} {
		if mag*f >= v {
			return mag * f
		}
	}
	return mag
}

func (JVM) history(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, namespace, name := r.PathValue("cluster"), r.PathValue("namespace"), r.PathValue("name")
		p, ok := lookupPod(w, r, h, cluster, namespace, name)
		if !ok {
			return
		}
		type read struct {
			win extension.Window
			err error
		}
		var got read
		done := make(chan struct{})
		wait, cancel := context.WithTimeout(r.Context(), historyWait)
		defer cancel()
		// Through safego, so a panic in the read is this request's failure and
		// not the end of the process; always waited for, so nothing outlives it.
		safego.Go("jvm: the memory history", func() error {
			got.win, got.err = h.UsageWindow(r.WithContext(wait), cluster, namespace)
			return nil
		}, func(crash error) {
			if crash != nil {
				got = read{err: crash}
			}
			close(done)
		})
		<-done
		if r.Context().Err() != nil {
			return
		}
		if errors.Is(wait.Err(), context.DeadlineExceeded) {
			// Not a failure: the core keeps reading on a context of its own.
			writeJSON(w, http.StatusOK, historyAnswer{Cluster: cluster, Namespace: namespace, Pod: name,
				State: historyPending, Series: []line{}})
			return
		}
		if got.err != nil {
			switch {
			case errors.Is(got.err, extension.ErrUnknownCluster):
				writeProblem(w, http.StatusNotFound, CodeUnknownCluster, extension.UnknownClusterText(cluster), got.err)
			default:
				if refused, ok := extension.RefusedRead(got.err, "The cluster says you may not list pods here, so their history is not shown."); ok {
					writeProblem(w, refused.Status, refused.Code, refused.Message, got.err)
					return
				}
				writeProblem(w, http.StatusBadGateway, CodeHistoryFailed, "The memory history could not be read.", got.err)
			}
			return
		}
		out := buildHistory(p, got.win)
		out.Cluster = cluster
		writeJSON(w, http.StatusOK, out)
	})
}

package overview

// The full-size, explorable version of a sparkline: room, axes, and each
// sample reachable one at a time. Everything is prefixed Zoom so it cannot be
// confused with the sparkline's helpers in this package.
//
// The rules of charts.go hold here too, plus one: the geometry is computed once,
// in Go, and shipped to the browser. The script following the pointer never
// recomputes a scale, because a second implementation of the mapping puts the
// crosshair a few pixels off the line.

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"
)

// --- which series ---------------------------------------------------------------

// The series a zoom can be of. Strings because they travel through a query
// parameter and back. Network is missing on purpose; see ZoomSeriesFor.
const (
	ZoomSeriesCPU    = "cpu"
	ZoomSeriesMemory = "memory"
	ZoomSeriesDisk   = "disk"
)

// ZoomSeriesFor reads a series out of a chart's title or key: "CPU" or "CPU
// used", "Memory" or "Memory used", "Container filesystem", "Node filesystem"
// or `disk`.
//
// The network chart is not zoomable here: it is one chart of two Histories,
// and ZoomFor takes one, so zooming it would drop the comparison. The SPA zooms
// client-side over the series it already holds, network included. Anything
// else is unrecognized rather than guessed: a zoom of the wrong series looks
// right and is about something else.
func ZoomSeriesFor(hint string) (string, bool) {
	h := strings.ToLower(strings.TrimSpace(hint))
	switch {
	case h == "":
		return "", false
	case strings.Contains(h, "cpu"):
		return ZoomSeriesCPU, true
	case strings.Contains(h, "mem"):
		return ZoomSeriesMemory, true
	case strings.Contains(h, "filesystem"), strings.Contains(h, "disk"):
		return ZoomSeriesDisk, true
	default:
		return "", false
	}
}

// --- which window ------------------------------------------------------------------

// ZoomWindows are the ranges the full-size view offers. The first five match
// Windows label for label and range for range, so opening a chart never
// changes the period (TestZoomWindowsAgreeWithThePanel). The step is finer,
// since the view is about four times as wide, and a 30d window is added for
// capacity questions.
var ZoomWindows = []Window{
	{Label: "15m", Range: 15 * time.Minute, Step: 5 * time.Second},
	{Label: "1h", Range: time.Hour, Step: 10 * time.Second},
	{Label: "6h", Range: 6 * time.Hour, Step: time.Minute},
	{Label: "24h", Range: 24 * time.Hour, Step: 4 * time.Minute},
	{Label: "7d", Range: 7 * 24 * time.Hour, Step: 30 * time.Minute},
	{Label: "30d", Range: 30 * 24 * time.Hour, Step: 2 * time.Hour},
}

// ZoomWindowFor resolves a label, falling back exactly where WindowFor does,
// so an unknown label lands on the same period in the panel and the zoom.
func ZoomWindowFor(label string) Window {
	for _, w := range ZoomWindows {
		if strings.EqualFold(w.Label, label) {
			return w
		}
	}
	fallback := WindowFor(label)
	for _, w := range ZoomWindows {
		if w.Label == fallback.Label {
			return w
		}
	}
	return ZoomWindows[1]
}

// --- the geometry ---------------------------------------------------------------

const (
	zoomWidth  = 960.0
	zoomHeight = 400.0
	// The plot band: value labels to its left, times under it.
	zoomLeft   = 72.0
	zoomRight  = 946.0
	zoomTop    = 18.0
	zoomBottom = 330.0
	// zoomGridLines is how many horizontal rules the value axis gets, ends
	// included.
	zoomGridLines = 5
	// zoomTimeLabels is how many stamps fit under the axis without touching.
	zoomTimeLabels = 6
	// zoomMarkerLimit is markerLimit for this wider chart; above it the dots
	// merge into a band.
	zoomMarkerLimit = 150
)

// zoomBucketCount is the size of the lookup table shipped to the browser: for
// each of these positions across the plot, the nearest sample, so following
// the pointer is one array index rather than a search or a server round trip.
// Over the 874-unit band a bucket is under two units, inside a pixel.
const zoomBucketCount = 480

// --- what the view needs ----------------------------------------------------------

// ZoomPoint is one sample, already placed and formatted. X and Y are viewBox
// coordinates; T and V are the readout's strings, formatted here so the
// browser never needs a second formatter.
type ZoomPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	T string  `json:"t"`
	V string  `json:"v"`
}

// ZoomPayload is everything the script is given. Only the horizontal geometry
// is sent: every point already carries its own y, t and v.
type ZoomPayload struct {
	Left  float64 `json:"left"`
	Right float64 `json:"right"`
	// Width is the viewBox width the script scales a client coordinate
	// against, sent so a change of geometry cannot offset the crosshair.
	Width   float64     `json:"width"`
	Points  []ZoomPoint `json:"points"`
	Buckets []int       `json:"buckets"`
}

// ZoomView is the model the fragment renders.
type ZoomView struct {
	Title string
	// Window is the label of the range drawn, for the heading.
	Window string
	// Empty is the reason there is no chart, as a sentence: most often a pod
	// younger than the window.
	Empty string
	// Summary is the sparkline's line: max, min, last, sample count.
	Summary string
	// LastTime and LastValue seed the readout before the pointer moves and
	// after it leaves.
	LastTime  string
	LastValue string
	Samples   int
	Chart     template.HTML
	// Data is the payload, marshalled. template.JS because it sits inside a
	// <script type="application/json">; json.Marshal escapes < > &, so a
	// closing script tag in a message cannot end the element.
	Data template.JS
}

// Drawable reports whether there is a chart rather than a sentence.
func (v ZoomView) Drawable() bool { return v.Empty == "" && v.Samples > 0 }

// ZoomFor builds the full-size view of one series: either a line, or a
// sentence saying why there is none.
func ZoomFor(h History, window Window) ZoomView {
	title := h.Title
	if title == "" {
		title = "Series"
	}

	view := ZoomView{Title: title, Window: window.Label, Samples: len(h.Points)}

	if len(h.Points) == 0 {
		view.Empty = ShortState(h)
		return view
	}

	low, high, last := extent(h.Points)
	view.Summary = fmt.Sprintf("max %s · min %s · last %s · %s · times in UTC",
		format(int64(high), h.Unit), format(int64(low), h.Unit), format(int64(last), h.Unit),
		plural(len(h.Points), "sample", "samples"))

	xs := zoomPositions(h.Points)
	points := make([]ZoomPoint, len(h.Points))
	for i, p := range h.Points {
		points[i] = ZoomPoint{
			X: xs[i],
			Y: zoomY(p.Value, low, high),
			T: zoomStamp(p.At),
			V: format(int64(p.Value), h.Unit),
		}
	}

	view.LastTime = points[len(points)-1].T
	view.LastValue = points[len(points)-1].V
	view.Chart = zoomSVG(h, points, low, high)

	payload := ZoomPayload{
		Left: zoomLeft, Right: zoomRight,
		Width:   zoomWidth,
		Points:  points,
		Buckets: ZoomBucketTable(xs),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		// Unreachable with these types; without a payload the chart is still
		// readable, only the crosshair stops working.
		return view
	}
	view.Data = template.JS(encoded)
	return view
}

// --- the lookup the pointer uses ------------------------------------------------

// ZoomNearest is the index of the sample closest to a position across the
// plot. A linear scan: it runs once per bucket at render time, and it does not
// assume Prometheus returned the samples sorted. Ties go to the earlier
// sample. An empty series is -1.
func ZoomNearest(xs []float64, x float64) int {
	if len(xs) == 0 {
		return -1
	}
	best, bestDistance := 0, zoomAbs(xs[0]-x)
	for i := 1; i < len(xs); i++ {
		if d := zoomAbs(xs[i] - x); d < bestDistance {
			best, bestDistance = i, d
		}
	}
	return best
}

// ZoomBucketTable precomputes ZoomNearest for evenly spaced positions across the
// plot band, so the browser does no searching at all.
func ZoomBucketTable(xs []float64) []int {
	buckets := make([]int, zoomBucketCount)
	if len(xs) == 0 {
		return buckets
	}
	span := zoomRight - zoomLeft
	for i := range buckets {
		at := zoomLeft + span*float64(i)/float64(zoomBucketCount-1)
		buckets[i] = ZoomNearest(xs, at)
	}
	return buckets
}

func zoomAbs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// --- placing the samples ------------------------------------------------------------

// zoomPositions is positions over this chart's band; the sparkline's bounds
// are its own constants.
func zoomPositions(points []Point) []float64 {
	xs := make([]float64, len(points))
	if len(points) == 0 {
		return xs
	}
	if len(points) == 1 {
		xs[0] = (zoomLeft + zoomRight) / 2
		return xs
	}

	first, last := points[0].At, points[len(points)-1].At
	span := last.Sub(first)
	if span <= 0 {
		step := (zoomRight - zoomLeft) / float64(len(points)-1)
		for i := range points {
			xs[i] = zoomLeft + step*float64(i)
		}
		return xs
	}
	for i, p := range points {
		offset := float64(p.At.Sub(first)) / float64(span)
		xs[i] = zoomLeft + offset*(zoomRight-zoomLeft)
	}
	return xs
}

// zoomY maps a value into the plot band, with a flat series in the middle, as
// plotY does.
func zoomY(value, low, high float64) float64 {
	if high <= low {
		return (zoomTop + zoomBottom) / 2
	}
	share := (value - low) / (high - low)
	return zoomBottom - share*(zoomBottom-zoomTop)
}

// zoomStamp is a sample's time in the readout, in UTC (said in the summary),
// so the timestamps do not depend on whose clock drew them.
func zoomStamp(at time.Time) string {
	return at.UTC().Format("Jan 2 15:04:05 MST")
}

// zoomAxisFormat is the shorter form under the axis, with the date only when
// the span reaches a day.
func zoomAxisFormat(span time.Duration) string {
	if span < 24*time.Hour {
		return "15:04"
	}
	return "Jan 2 15:04"
}

// --- the drawing -------------------------------------------------------------------

// The shapes below carry no class: their colors and widths are presentation
// attributes, and a class nothing selects would suggest a stylesheet paints
// them. cz-cursor and cz-marker are the exception: hooks the pointer script
// selects to move the crosshair.

func zoomSVG(h History, points []ZoomPoint, low, high float64) template.HTML {
	class := ClassOK
	if h.Unit == UnitBytes || h.Unit == UnitBytesPerSecond {
		class = ""
	}
	stroke := colour(class)

	var b strings.Builder
	fmt.Fprintf(&b,
		`<svg viewBox="0 0 %s %s" preserveAspectRatio="xMidYMid meet" class="chart chart-full" role="img" aria-label="%s">`,
		n(zoomWidth), n(zoomHeight), esc(h.Title+" over time, "+plural(len(points), "sample", "samples")))
	fmt.Fprintf(&b, `<title>%s</title>`, esc(h.Title))

	zoomValueAxis(&b, h.Unit, low, high)
	zoomTimeAxis(&b, h, points)

	line := make([]string, len(points))
	for i, p := range points {
		line[i] = n(p.X) + "," + n(p.Y)
	}
	joined := strings.Join(line, " ")

	if len(points) > 1 {
		fmt.Fprintf(&b, `<polygon points="%s %s,%s %s,%s" fill="%s" fill-opacity="0.12"/>`,
			joined, n(points[len(points)-1].X), n(zoomBottom), n(points[0].X), n(zoomBottom), stroke)
	}
	fmt.Fprintf(&b,
		`<polyline points="%s" fill="none" stroke="%s" stroke-width="1.8" stroke-linejoin="round" stroke-linecap="round"/>`,
		joined, stroke)

	if len(points) <= zoomMarkerLimit {
		for _, p := range points {
			fmt.Fprintf(&b, `<circle cx="%s" cy="%s" r="2" fill="%s"/>`, n(p.X), n(p.Y), stroke)
		}
	}

	// The crosshair, last so it sits over the line, seeded on the last sample
	// so chart and readout agree before the pointer moves. pointer-events="none"
	// so it is never what the pointer is over.
	final := points[len(points)-1]
	fmt.Fprintf(&b,
		`<line class="cz-cursor" x1="%s" y1="%s" x2="%s" y2="%s" stroke="var(--muted)" stroke-width="1" stroke-dasharray="3 3" pointer-events="none"/>`,
		n(final.X), n(zoomTop), n(final.X), n(zoomBottom))
	fmt.Fprintf(&b,
		`<circle class="cz-marker" cx="%s" cy="%s" r="4" fill="%s" stroke="var(--bg)" stroke-width="1.5" pointer-events="none"/>`,
		n(final.X), n(final.Y), stroke)

	b.WriteString("</svg>")
	return template.HTML(b.String())
}

// zoomValueAxis draws the horizontal rules and the figures beside them.
func zoomValueAxis(b *strings.Builder, unit Unit, low, high float64) {
	if high <= low {
		y := (zoomTop + zoomBottom) / 2
		fmt.Fprintf(b, `<line x1="%s" y1="%s" x2="%s" y2="%s" stroke="var(--line)"/>`,
			n(zoomLeft), n(y), n(zoomRight), n(y))
		fmt.Fprintf(b, `<text x="%s" y="%s" text-anchor="end" dominant-baseline="middle" font-size="11" fill="var(--muted)">%s</text>`,
			n(zoomLeft-8), n(y), esc(format(int64(low), unit)))
		return
	}

	for i := 0; i < zoomGridLines; i++ {
		share := float64(i) / float64(zoomGridLines-1)
		value := low + share*(high-low)
		y := zoomBottom - share*(zoomBottom-zoomTop)
		fmt.Fprintf(b, `<line x1="%s" y1="%s" x2="%s" y2="%s" stroke="var(--line)"/>`,
			n(zoomLeft), n(y), n(zoomRight), n(y))
		fmt.Fprintf(b, `<text x="%s" y="%s" text-anchor="end" dominant-baseline="middle" font-size="11" fill="var(--muted)">%s</text>`,
			n(zoomLeft-8), n(y), esc(format(int64(value), unit)))
	}
}

// zoomTimeAxis writes the stamps under the plot at evenly spaced times across
// the samples' span.
func zoomTimeAxis(b *strings.Builder, h History, points []ZoomPoint) {
	if len(h.Points) == 0 {
		return
	}
	first, last := h.Points[0].At, h.Points[len(h.Points)-1].At
	layout := zoomAxisFormat(last.Sub(first))

	if len(h.Points) == 1 {
		fmt.Fprintf(b, `<text x="%s" y="%s" text-anchor="middle" font-size="11" fill="var(--muted)">%s</text>`,
			n(points[0].X), n(zoomBottom+20), esc(first.UTC().Format(layout)))
		return
	}

	for i := 0; i < zoomTimeLabels; i++ {
		share := float64(i) / float64(zoomTimeLabels-1)
		x := zoomLeft + share*(zoomRight-zoomLeft)
		at := first.Add(time.Duration(share * float64(last.Sub(first))))
		anchor := "middle"
		switch i {
		case 0:
			anchor = "start"
		case zoomTimeLabels - 1:
			anchor = "end"
		}
		fmt.Fprintf(b, `<text x="%s" y="%s" text-anchor="%s" font-size="11" fill="var(--muted)">%s</text>`,
			n(x), n(zoomBottom+20), anchor, esc(at.UTC().Format(layout)))
	}
}

// --- the cluster's own series, over a chosen window --------------------------------

// ZoomClusterHistory is the dashboard's two history charts over a window the
// reader picked. It goes through clusterLines, the dashboard's own, so the
// zoom inherits its queries (and their double-scrape correction) rather than
// a copy.
func ZoomClusterHistory(ctx context.Context, metrics Metrics, cluster string, window Window) ObjectHistory {
	if metrics == nil {
		return noBackend(ObjectHistory{Window: window})
	}
	// CPU and memory only: network and disk are a resource panel's charts. A
	// caller must refuse ZoomSeriesDisk before this, or an untouched History
	// would draw "no samples in the last 30d" about a series nobody fetched.
	return clusterLines(ctx, metrics, cluster, window, "CPU", "Memory")
}

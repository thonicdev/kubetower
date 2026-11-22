package overview

// Several series on one pair of axes, with one Y scale: two charts side by side
// would each scale to their own maximum, so 800 KiB/s of egress would draw as
// tall as 9 MiB/s of ingress. The accessibility rules of charts.go hold here
// too, and each series is told apart by a dash pattern as well as a color, so
// the chart survives color blindness and grayscale. HistorySparkline stays a
// separate chart because its filled area makes overlapping lines illegible.

import (
	"fmt"
	"html/template"
	"strings"
)

const (
	linesWidth  = 240.0
	linesTop    = 6.0
	linesBottom = 40.0
	linesLeft   = 3.0
	linesRight  = 237.0
	// The legend's first baseline and pitch: 9px text needs 11 units of leading.
	linesLegendTop   = 51.0
	linesLegendPitch = 11.0
	// The characters left for words on a legend row: at 9px about 56 fit in
	// 240 units, less the swatch and some slack.
	linesLegendChars = 48
)

// lineClass is the class each series is drawn in, in order. Not chosen for
// meaning (neither direction of traffic is the bad one) but for being
// distinguishable in both themes; past the fourth series everything is muted.
var lineClass = []string{"", ClassWarn, ClassOK, ClassBad}

// lineDash is the stroke pattern per series, so the chart survives grayscale
// and color blindness. The first, for a single series, is solid.
var lineDash = []string{"", "5 3", "2 2", "6 2 1 2"}

// HistoryLines draws several series on one pair of axes, with one Y scale
// across all of them and a legend naming each.
//
// Nothing to draw becomes a sentence, as in HistorySparkline. When only some
// series are drawable, each missing one gets a legend row carrying its reason:
// an omitted line is indistinguishable from a line at zero.
func HistoryLines(series ...History) template.HTML {
	if len(series) == 0 {
		return noData(linesWidth, linesHeight(1), "there are no series to draw", "Series")
	}

	label := linesLabel(series)

	// Laid out before the chart is sized: a series with no samples carries a
	// sentence, which may need several rows rather than being clipped.
	legend := linesLegend(series)

	low, high, drawable := linesExtent(series)
	if !drawable {
		// One sentence: the first series' reason is almost always shared by all.
		return noData(linesWidth, linesHeight(len(series)), linesEmptyReason(series), label)
	}

	height := linesHeight(len(legend))

	var b strings.Builder
	openSVG(&b, linesWidth, height, "chart chart-lines", label)

	for i, h := range series {
		if len(h.Points) == 0 {
			continue
		}
		class, dash := lineStyle(i)
		xs := linesPositions(h.Points)
		coords := make([]string, len(h.Points))
		for j, p := range h.Points {
			coords[j] = n(xs[j]) + "," + n(linesY(p.Value, low, high))
		}
		joined := strings.Join(coords, " ")

		fmt.Fprintf(&b,
			`<polyline points="%s" fill="none" stroke="%s" stroke-width="1.6" stroke-linejoin="round" stroke-linecap="round"%s/>`,
			joined, colour(class), dashAttr(dash))

		// Below markerLimit samples each one is drawn, so six points cannot be
		// mistaken for a smoothed sixty.
		if len(h.Points) <= markerLimit {
			for j := range h.Points {
				fmt.Fprintf(&b, `<circle cx="%s" cy="%s" r="1.7" fill="%s"/>`,
					n(xs[j]), n(linesY(h.Points[j].Value, low, high)), colour(class))
			}
		}
	}

	for i, row := range legend {
		y := linesLegendTop + float64(i)*linesLegendPitch
		if row.swatch {
			// A short length of line rather than a square, to carry the dash.
			fmt.Fprintf(&b,
				`<line x1="%s" y1="%s" x2="%s" y2="%s" stroke="%s" stroke-width="1.6"%s/>`,
				n(linesLeft), n(y-3), n(linesLeft+11), n(y-3), colour(row.class), dashAttr(row.dash))
		}
		fmt.Fprintf(&b, `<text x="%s" y="%s" font-size="9" fill="var(--muted)">%s</text>`,
			n(linesLeft+15), n(y), esc(row.text))
	}

	b.WriteString("</svg>")
	return template.HTML(b.String())
}

// linesLegendRow is one line of text under the plot. A sentence may take
// several rows; only the first gets the swatch.
type linesLegendRow struct {
	class  string
	dash   string
	text   string
	swatch bool
}

// linesLegend lays the legend out, wrapping rather than clipping: the end of a
// sentence such as "Prometheus does not export <counter>" is the part that
// names what to go and look for.
func linesLegend(series []History) []linesLegendRow {
	rows := make([]linesLegendRow, 0, len(series)+1)
	for i, h := range series {
		class, dash := lineStyle(i)
		for j, line := range wrap(linesLegendText(h), linesLegendChars) {
			rows = append(rows, linesLegendRow{class: class, dash: dash, text: line, swatch: j == 0})
		}
	}
	return rows
}

// linesHeight grows the chart to fit its legend.
func linesHeight(rows int) float64 {
	if rows < 1 {
		rows = 1
	}
	return linesLegendTop + float64(rows)*linesLegendPitch - 3
}

func lineStyle(i int) (class, dash string) {
	if i < len(lineClass) {
		return lineClass[i], lineDash[i]
	}
	return ClassMuted, "1 3"
}

func dashAttr(dash string) string {
	if dash == "" {
		return ""
	}
	return fmt.Sprintf(` stroke-dasharray="%s"`, dash)
}

// linesExtent is the low and the high across every series, which is what makes
// the scale shared. ok is separate because (0, 0) is a legitimate extent for
// idle series and must draw a flat line, not "no data".
func linesExtent(series []History) (low, high float64, ok bool) {
	for _, h := range series {
		if len(h.Points) == 0 {
			continue
		}
		seriesLow, seriesHigh, _ := extent(h.Points)
		if !ok {
			low, high, ok = seriesLow, seriesHigh, true
			continue
		}
		if seriesLow < low {
			low = seriesLow
		}
		if seriesHigh > high {
			high = seriesHigh
		}
	}
	return low, high, ok
}

// linesY is plotY over this chart's band; the sparkline's bounds are its own
// constants, so this chart has its own copy.
func linesY(value, low, high float64) float64 {
	if high <= low {
		return (linesTop + linesBottom) / 2
	}
	share := (value - low) / (high - low)
	return linesBottom - share*(linesBottom-linesTop)
}

// linesPositions places samples along x by time, as the sparkline does, so a
// gap in scraping shows as a gap. Each series spans its own first and last
// sample: stretching a shorter series is the lesser wrong, and the legend's
// sample count makes it visible.
func linesPositions(points []Point) []float64 {
	xs := make([]float64, len(points))
	switch {
	case len(points) == 0:
		return xs
	case len(points) == 1:
		xs[0] = (linesLeft + linesRight) / 2
		return xs
	}

	first, last := points[0].At, points[len(points)-1].At
	span := last.Sub(first)
	if span <= 0 {
		step := (linesRight - linesLeft) / float64(len(points)-1)
		for i := range points {
			xs[i] = linesLeft + step*float64(i)
		}
		return xs
	}
	for i, p := range points {
		offset := float64(p.At.Sub(first)) / float64(span)
		xs[i] = linesLeft + offset*(linesRight-linesLeft)
	}
	return xs
}

// linesLegendText is one series' row: its name and its figures, or its name and
// the reason it has none.
func linesLegendText(h History) string {
	title := h.Title
	if title == "" {
		title = "Series"
	}
	if len(h.Points) == 0 {
		return title + " — " + ShortState(h)
	}
	_, high, last := extent(h.Points)
	// Max and last, without the sparkline's minimum: the legend row is
	// short, and a network rate's minimum is rarely what the reader came for.
	return fmt.Sprintf("%s — max %s · last %s",
		title, format(int64(high), h.Unit), format(int64(last), h.Unit))
}

// linesLabel is the sentence a screen reader is given: every series, named,
// with its figures.
func linesLabel(series []History) string {
	spoken := make([]string, 0, len(series))
	names := make([]string, 0, len(series))
	for _, h := range series {
		title := h.Title
		if title == "" {
			title = "Series"
		}
		names = append(names, title)
		spoken = append(spoken, linesLegendText(h)+" · "+plural(len(h.Points), "sample", "samples"))
	}
	return strings.Join(names, " and ") + ". " + strings.Join(spoken, ". ")
}

// linesEmptyReason is the one short state for a chart with nothing on it at all.
func linesEmptyReason(series []History) string {
	for _, h := range series {
		if h.Missing == MissingFailed || h.Missing == MissingBackend {
			return StateUnavailable
		}
	}
	return StateNoMetrics
}

// The two short states a chart with nothing on it shows. The sentence that
// says why stays on the History (Unavailable) for a tooltip or a log, never
// on screen. A failed read is not an empty one (R5 section 5).
const (
	StateNoMetrics   = "No metrics"
	StateUnavailable = "Metrics unavailable"
)

// ShortState is the on-screen state of a series with no points: a failed read
// is "Metrics unavailable", anything else "No metrics".
func ShortState(h History) string {
	if h.Missing == MissingFailed || h.Missing == MissingBackend {
		return StateUnavailable
	}
	return StateNoMetrics
}

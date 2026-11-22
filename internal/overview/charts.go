package overview

// The chart vocabulary, drawn as SVG by the server. Rules for every chart:
//
//   - role="img", an aria-label and a <title>, so it can be read aloud.
//   - Every figure appears as text as well as a shape; nothing depends on
//     hover, script or judging an angle.
//   - Colors are CSS custom properties (fill="var(--ok)"), so the theme
//     decides.
//   - A viewBox and no pixel size: the stylesheet sizes the element.
//   - No element carries an id: the same chart can appear twice on a page, and
//     a duplicated id silently applies a clipPath or gradient to the wrong one.

import (
	"fmt"
	"html/template"
	"strconv"
	"strings"

	"github.com/thonicdev/kubetower/internal/resources"
)

// --- the donut ------------------------------------------------------------------

// DonutData is one "x of y used" ring.
type DonutData struct {
	Used, Total float64
	// Center is the big figure in the middle: "62%".
	Centre string
	// Sub is the line under it: "3.2 of 8 cores".
	Sub string
	// Class is ok/warn/bad; empty draws in the accent color.
	Class string
	// Label is the sentence a screen reader is given.
	Label string
	// Empty replaces the whole chart when there is nothing to draw; blank
	// falls back to "no data".
	Empty string
}

const (
	donutSize   = 120.0
	donutRadius = 46.0
	// The circumference the dash array is expressed in.
	donutCircumference = 2 * 3.14159265358979 * donutRadius
)

// Donut draws "used of total" as a ring with the figure in the middle. A
// fraction over 1 fills the ring and still prints its real percentage:
// over-commitment is a normal state worth seeing.
func Donut(d DonutData) template.HTML {
	if d.Total <= 0 {
		message := d.Empty
		if message == "" {
			message = "no capacity reported"
		}
		return noData(donutSize, donutSize, message, d.Label)
	}

	fraction := d.Used / d.Total
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	filled := fraction * donutCircumference

	var b strings.Builder
	openSVG(&b, donutSize, donutSize, "chart chart-donut", d.Label)
	// The track first, then the arc over it.
	fmt.Fprintf(&b, `<circle cx="%s" cy="%s" r="%s" fill="none" stroke="var(--line)" stroke-width="13"/>`,
		n(donutSize/2), n(donutSize/2), n(donutRadius))
	fmt.Fprintf(&b,
		`<circle cx="%s" cy="%s" r="%s" fill="none" stroke="%s" stroke-width="13" `+
			`stroke-dasharray="%s %s" transform="rotate(-90 %s %s)"/>`,
		n(donutSize/2), n(donutSize/2), n(donutRadius), colour(d.Class),
		n(filled), n(donutCircumference-filled), n(donutSize/2), n(donutSize/2))

	fmt.Fprintf(&b,
		`<text x="%s" y="57" text-anchor="middle" dominant-baseline="central" font-size="24" font-weight="600" fill="var(--fg)">%s</text>`,
		n(donutSize/2), esc(d.Centre))
	if d.Sub != "" {
		// The sub-label must fit the hole (79 units across); a long one such as
		// "666 MiB of 7.6 GiB" would cross the ring on a large cluster.
		// textLength is emitted only when the label is estimated too wide, since
		// it forces text to exactly that length and would stretch a short one.
		// spacingAndGlyphs condenses the glyphs too, so they do not touch.
		attrs := ""
		if estimateWidth(d.Sub, subLabelSize) > donutHoleWidth {
			attrs = fmt.Sprintf(` textLength="%s" lengthAdjust="spacingAndGlyphs"`, n(donutHoleWidth))
		}
		fmt.Fprintf(&b,
			`<text x="%s" y="79" text-anchor="middle" font-size="10" fill="var(--muted)"%s>%s</text>`,
			n(donutSize/2), attrs, esc(d.Sub))
	}
	b.WriteString("</svg>")
	return template.HTML(b.String())
}

// CapacityDonut is a Capacity's donut, so no caller decides for itself what
// color 91% is.
func CapacityDonut(c Capacity) template.HTML {
	name := c.Name
	if name == "" {
		name = "Capacity"
	}
	return Donut(DonutData{
		Used:   float64(c.Used),
		Total:  float64(c.Total),
		Centre: strconv.Itoa(c.Percent()) + "%",
		Sub:    c.Text(),
		Class:  c.Class(),
		Label:  c.Label(),
		Empty:  name + ": the cluster reported no capacity",
	})
}

// --- the sparkline ---------------------------------------------------------------

// SparkData is one series over time.
type SparkData struct {
	Points []Point
	Unit   Unit
	Class  string
	// Label is the sentence a screen reader is given, before the figures this
	// function appends to it.
	Label string
	// Empty is the reason drawn instead when there are no points.
	Empty string
}

const (
	sparkWidth  = 240.0
	sparkHeight = 64.0
	sparkTop    = 6.0
	sparkBottom = 40.0
	sparkLeft   = 3.0
	sparkRight  = 237.0
)

// markerLimit is the point count up to which each sample is drawn as a dot,
// so six samples cannot be mistaken for a smoothed sixty.
const markerLimit = 30

// Sparkline draws a series over a window. Points are placed by timestamp, not
// index, so a gap in scraping shows as a gap rather than a steeper line.
func Sparkline(s SparkData) template.HTML {
	label := s.Label
	if label == "" {
		label = "Series"
	}

	if len(s.Points) == 0 {
		message := s.Empty
		if message == "" {
			message = "no data in this window"
		}
		return noData(sparkWidth, sparkHeight, message, label)
	}

	low, high, last := extent(s.Points)
	caption := fmt.Sprintf("max %s · min %s · last %s · %s",
		format(int64(high), s.Unit), format(int64(low), s.Unit), format(int64(last), s.Unit),
		plural(len(s.Points), "sample", "samples"))

	var b strings.Builder
	openSVG(&b, sparkWidth, sparkHeight, "chart chart-spark", label+". "+caption)

	xs := positions(s.Points)
	ys := make([]float64, len(s.Points))
	for i, p := range s.Points {
		ys[i] = plotY(p.Value, low, high)
	}

	line := make([]string, len(xs))
	for i := range xs {
		line[i] = n(xs[i]) + "," + n(ys[i])
	}
	joined := strings.Join(line, " ")

	// The filled area first and faint: it makes the line legible without
	// reading as a second series.
	if len(xs) > 1 {
		fmt.Fprintf(&b, `<polygon points="%s %s,%s %s,%s" fill="%s" fill-opacity="0.12"/>`,
			joined, n(xs[len(xs)-1]), n(sparkBottom), n(xs[0]), n(sparkBottom), colour(s.Class))
	}
	fmt.Fprintf(&b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="1.6" stroke-linejoin="round" stroke-linecap="round"/>`,
		joined, colour(s.Class))

	if len(xs) <= markerLimit {
		for i := range xs {
			fmt.Fprintf(&b, `<circle cx="%s" cy="%s" r="1.7" fill="%s"/>`, n(xs[i]), n(ys[i]), colour(s.Class))
		}
	}

	// The figures as text, readable without color, hover or eyesight.
	fmt.Fprintf(&b, `<text x="%s" y="57" font-size="9" fill="var(--muted)">%s</text>`, n(sparkLeft), esc(caption))
	b.WriteString("</svg>")
	return template.HTML(b.String())
}

// HistorySparkline is a History's sparkline, carrying the reason there is no
// line into the chart, so a panel never draws an empty box.
func HistorySparkline(h History) template.HTML {
	empty := h.Unavailable
	if empty == "" {
		empty = "no samples in the last " + resources.ShortDuration(historyWindow)
	}
	class := ClassOK
	if h.Unit == UnitBytes || h.Unit == UnitBytesPerSecond {
		class = ""
	}
	return Sparkline(SparkData{
		Points: h.Points,
		Unit:   h.Unit,
		Class:  class,
		Label:  h.Title,
		Empty:  empty,
	})
}

// extent is the low, high and last of a series, in one pass.
func extent(points []Point) (low, high, last float64) {
	low, high = points[0].Value, points[0].Value
	for _, p := range points {
		if p.Value < low {
			low = p.Value
		}
		if p.Value > high {
			high = p.Value
		}
	}
	return low, high, points[len(points)-1].Value
}

// positions places the samples along x by time, falling back to even spacing
// when the timestamps span nothing, so the points are not stacked on the left.
func positions(points []Point) []float64 {
	xs := make([]float64, len(points))
	if len(points) == 1 {
		xs[0] = (sparkLeft + sparkRight) / 2
		return xs
	}

	first, last := points[0].At, points[len(points)-1].At
	span := last.Sub(first)
	if span <= 0 {
		step := (sparkRight - sparkLeft) / float64(len(points)-1)
		for i := range points {
			xs[i] = sparkLeft + step*float64(i)
		}
		return xs
	}

	for i, p := range points {
		offset := float64(p.At.Sub(first)) / float64(span)
		xs[i] = sparkLeft + offset*(sparkRight-sparkLeft)
	}
	return xs
}

// plotY maps a value into the plot band. A flat series sits in the middle: at
// the top it would read as a maximum, at the bottom as an outage.
func plotY(value, low, high float64) float64 {
	if high <= low {
		return (sparkTop + sparkBottom) / 2
	}
	share := (value - low) / (high - low)
	return sparkBottom - share*(sparkBottom-sparkTop)
}

// --- the stacked bar --------------------------------------------------------------

const (
	barWidth     = 240.0
	barTop       = 4.0
	barHeight    = 14.0
	legendTop    = 30.0
	legendRow    = 13.0
	legendPerRow = 3
	legendPitch  = 80.0
)

// StackedBar draws a breakdown by status as one bar, with every count spelled
// out beneath it. The legend is not optional: the segment that matters (one
// Failed among four hundred Running) is often too thin to see.
func StackedBar(segments []Segment, label string) template.HTML {
	if label == "" {
		label = "Breakdown"
	}

	total := 0
	for _, s := range segments {
		if s.Value > 0 {
			total += s.Value
		}
	}
	if total == 0 {
		return noData(barWidth, legendTop+legendRow, "nothing to break down", label)
	}

	rows := (len(segments) + legendPerRow - 1) / legendPerRow
	height := legendTop + float64(rows)*legendRow

	var spoken []string
	for _, s := range segments {
		spoken = append(spoken, fmt.Sprintf("%s %d", s.Label, s.Value))
	}

	var b strings.Builder
	openSVG(&b, barWidth, height, "chart chart-bar", label+": "+strings.Join(spoken, ", "))

	x := 0.0
	for _, s := range segments {
		if s.Value <= 0 {
			continue
		}
		width := barWidth * float64(s.Value) / float64(total)
		// A segment thinner than a unit is widened to one, so a single
		// failure still paints.
		if width < 1 {
			width = 1
		}
		fmt.Fprintf(&b, `<rect x="%s" y="%s" width="%s" height="%s" fill="%s"><title>%s: %d</title></rect>`,
			n(x), n(barTop), n(width), n(barHeight), colour(s.Class), esc(s.Label), s.Value)
		x += width
	}

	for i, s := range segments {
		column := float64(i % legendPerRow)
		row := float64(i / legendPerRow)
		lx := 1 + column*legendPitch
		ly := legendTop + row*legendRow
		fmt.Fprintf(&b, `<rect x="%s" y="%s" width="7" height="7" rx="1.5" fill="%s"/>`,
			n(lx), n(ly-6), colour(s.Class))
		fmt.Fprintf(&b, `<text x="%s" y="%s" font-size="9" fill="var(--muted)">%s %d</text>`,
			n(lx+10), n(ly), esc(s.Label), s.Value)
	}

	b.WriteString("</svg>")
	return template.HTML(b.String())
}

// --- the shared plumbing ------------------------------------------------------------

// openSVG writes the element every chart starts with, so the accessibility
// attributes cannot be left off one chart.
func openSVG(b *strings.Builder, width, height float64, class, label string) {
	fmt.Fprintf(b,
		`<svg viewBox="0 0 %s %s" preserveAspectRatio="xMidYMid meet" class="%s" role="img" aria-label="%s">`,
		n(width), n(height), class, esc(label))
	fmt.Fprintf(b, `<title>%s</title>`, esc(label))
}

// noData is a chart with nothing in it that says so in words: a blank box
// reads as a broken panel.
func noData(width, height float64, message, label string) template.HTML {
	spoken := label
	if spoken == "" {
		spoken = "No data"
	}
	spoken += ": " + message

	var b strings.Builder
	openSVG(&b, width, height, "chart chart-empty", spoken)
	fmt.Fprintf(&b,
		`<rect x="0.5" y="0.5" width="%s" height="%s" rx="4" fill="none" stroke="var(--line)" stroke-dasharray="3 3"/>`,
		n(width-1), n(height-1))

	lines := wrap(message, charsPerLine(width))
	// The em dash first and larger: the mark for "the cluster did not say",
	// readable from across a room.
	fmt.Fprintf(&b,
		`<text x="%s" y="%s" text-anchor="middle" font-size="16" fill="var(--muted)">%s</text>`,
		n(width/2), n(height/2-float64(len(lines))*4), missing)
	for i, line := range lines {
		fmt.Fprintf(&b,
			`<text x="%s" y="%s" text-anchor="middle" font-size="9" fill="var(--muted)">%s</text>`,
			n(width/2), n(height/2+6+float64(i)*10), esc(line))
	}
	b.WriteString("</svg>")
	return template.HTML(b.String())
}

// charsPerLine is roughly how much 9px text fits across a viewBox of this
// width; the server has no text metrics.
func charsPerLine(width float64) int {
	chars := int(width / 4.6)
	if chars < 12 {
		return 12
	}
	return chars
}

// wrap breaks a sentence into lines of at most width characters, counted in
// runes: the em dashes and interpuncts in these sentences are multi-byte, and
// a byte count would wrap legends early.
func wrap(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{missing}
	}

	var lines []string
	current := words[0]
	length := len([]rune(current))
	for _, word := range words[1:] {
		wordLength := len([]rune(word))
		if length+1+wordLength > width {
			lines = append(lines, current)
			current, length = word, wordLength
			continue
		}
		current += " " + word
		length += 1 + wordLength
	}
	return append(lines, current)
}

// color maps a health class to the stylesheet's variable, so the palette
// follows the theme; nothing in this package writes a color.
func colour(class string) string {
	switch class {
	case ClassOK:
		return "var(--ok)"
	case ClassWarn:
		return "var(--warn)"
	case ClassBad:
		return "var(--bad)"
	case ClassMuted:
		return "var(--muted)"
	default:
		return "var(--accent)"
	}
}

func esc(s string) string { return template.HTMLEscapeString(s) }

// n writes a coordinate with at most two decimals: a 240-unit viewBox needs
// no more, and %v would write 23.333333333333332.
func n(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// The geometry the donut's sub-label has to fit, and a way to guess whether it
// will.
const (
	// donutHoleWidth is the clear space inside the ring: the diameter less the
	// stroke, less a unit at each end.
	donutHoleWidth = (donutRadius * 2) - 13 - 4
	subLabelSize   = 10.0
	// averageAdvance is the mean glyph width as a fraction of the font size,
	// measured at 0.455 for the UI sans-serif and rounded up: over-estimating
	// only condenses a label needlessly, under-estimating lets it overlap.
	averageAdvance = 0.47
)

// estimateWidth guesses how wide a string renders, in runes times an average
// advance; the server has no font metrics. It only decides whether to
// constrain the text, so being a few per cent out costs nothing.
func estimateWidth(s string, fontSize float64) float64 {
	return float64(len([]rune(s))) * fontSize * averageAdvance
}

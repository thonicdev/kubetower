package overview

// What a multi-series chart has to get right, and every one of these is a way of
// drawing two lines that says something false.

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// lineSeries builds a series at one-minute intervals.
func lineSeries(title string, unit Unit, values ...float64) History {
	base := time.Date(2026, 8, 21, 4, 0, 0, 0, time.UTC)
	points := make([]Point, len(values))
	for i, v := range values {
		points[i] = Point{At: base.Add(time.Duration(i) * time.Minute), Value: v}
	}
	return History{Title: title, Unit: unit, Points: points, Step: time.Minute}
}

var polylineRE = regexp.MustCompile(`<polyline points="([^"]+)"`)

var textRE = regexp.MustCompile(`<text[^>]*>([^<]*)</text>`)

// drawnText is what a reader sees, and only that.
//
// It matters that this is not the whole SVG. Every series' name and figures also
// go into the aria-label, so searching the markup for a title finds it whether or
// not the legend drew a row for it — so an assertion on the markup would pass
// TestAMissingSeriesIsNamedRatherThanOmitted with the legend row deleted. An
// assertion that cannot fail is not coverage.
func drawnText(svg string) []string {
	var out []string
	for _, m := range textRE.FindAllStringSubmatch(svg, -1) {
		out = append(out, m[1])
	}
	return out
}

// polylineBands returns, per drawn line and in order, the highest and lowest y
// coordinate it occupies. SVG y grows downward, so a band nearer zero is nearer
// the top of the chart.
func polylineBands(t *testing.T, svg string) [][2]float64 {
	t.Helper()
	var bands [][2]float64
	for _, match := range polylineRE.FindAllStringSubmatch(svg, -1) {
		low, high := 0.0, 0.0
		for i, pair := range strings.Fields(match[1]) {
			parts := strings.Split(pair, ",")
			if len(parts) != 2 {
				t.Fatalf("unreadable coordinate %q", pair)
			}
			y, err := strconv.ParseFloat(parts[1], 64)
			if err != nil {
				t.Fatalf("unreadable y in %q: %v", pair, err)
			}
			if i == 0 || y < low {
				low = y
			}
			if i == 0 || y > high {
				high = y
			}
		}
		bands = append(bands, [2]float64{low, high})
	}
	return bands
}

// The reason the two series share a chart at all.
//
// Give each its own scale and 800 KiB/s of egress is drawn exactly as tall as
// 9 MiB/s of ingress — which is the picture saying the opposite of the truth,
// on the one chart whose whole question is which way the traffic is going.
func TestOneScaleAcrossEverySeries(t *testing.T) {
	big := lineSeries("Network in", UnitBytesPerSecond, 9_000_000, 9_400_000, 9_200_000)
	small := lineSeries("Network out", UnitBytesPerSecond, 800_000, 810_000, 790_000)

	bands := polylineBands(t, string(HistoryLines(big, small)))
	if len(bands) != 2 {
		t.Fatalf("drew %d lines, want 2", len(bands))
	}

	// The whole of the small series must sit below the whole of the big one.
	// Per-series scaling makes both span the band, and this comparison is then
	// false in one direction or the other whichever way round they are read.
	if bands[1][0] <= bands[0][1] {
		t.Errorf("the two series are scaled independently: the big one occupies y %v–%v and the small "+
			"one y %v–%v, so a twelfth of the traffic is drawn as tall as all of it",
			bands[0][0], bands[0][1], bands[1][0], bands[1][1])
	}
}

// Two unlabeled lines are worse than one line.
func TestEverySeriesIsNamedAndDistinguishableWithoutColour(t *testing.T) {
	svg := string(HistoryLines(
		lineSeries("Network in", UnitBytesPerSecond, 1000, 2000),
		lineSeries("Network out", UnitBytesPerSecond, 300, 400),
	))

	rows := strings.Join(drawnText(svg), " | ")
	for _, want := range []string{"Network in", "Network out"} {
		if !strings.Contains(rows, want) {
			t.Errorf("the legend does not name %q: %q", want, rows)
		}
	}
	// Color is not enough: a reader who cannot tell two of them apart, and a
	// grayscale print of a dashboard, both need the second cue.
	if !strings.Contains(svg, "stroke-dasharray") {
		t.Error("the series are told apart by color alone")
	}
	// And the figures are in the text, so the chart is readable without eyesight.
	if !strings.Contains(rows, "max ") || !strings.Contains(rows, "last ") {
		t.Errorf("the legend carries no figures: %q", rows)
	}
	if !strings.Contains(rows, "/s") {
		t.Error("a rate is printed without its per-second unit, which reads as a total")
	}
}

func TestAChartWithNothingOnItIsASentence(t *testing.T) {
	reason := "this cluster's Prometheus does not export container_network_receive_bytes_total"
	svg := string(HistoryLines(
		History{Title: "Network in", Unit: UnitBytesPerSecond, Unavailable: reason, Missing: MissingCounter},
		History{Title: "Network out", Unit: UnitBytesPerSecond, Unavailable: reason, Missing: MissingCounter},
	))

	if strings.Contains(svg, "<polyline") {
		t.Error("a chart with no samples drew a line")
	}
	if !strings.Contains(svg, StateNoMetrics) || strings.Contains(svg, "does not export") {
		t.Errorf("the chart must say %q and never the sentence:\n%s", StateNoMetrics, svg)
	}
}

func TestNoSeriesAtAllIsStillASentence(t *testing.T) {
	svg := string(HistoryLines())
	if !strings.Contains(svg, "no series") || strings.Contains(svg, "<polyline") {
		t.Errorf("an empty call drew something other than an explanation:\n%s", svg)
	}
}

// One line present and one missing. The missing one must be *named* as missing:
// an omitted line and a line at zero are opposite claims, and for network
// traffic they are the difference between "we are not measuring egress" and
// "nothing was sent".
func TestAMissingSeriesIsNamedRatherThanOmitted(t *testing.T) {
	svg := string(HistoryLines(
		lineSeries("Network in", UnitBytesPerSecond, 1000, 2000),
		History{Title: "Network out", Unit: UnitBytesPerSecond, Unavailable: "the transmit counter is not exported", Missing: MissingFailed},
	))

	if bands := polylineBands(t, svg); len(bands) != 1 {
		t.Fatalf("drew %d lines, want 1", len(bands))
	}
	// Two series, and the second's sentence wraps onto as many rows as it needs
	// rather than being clipped: the half of "this cluster's Prometheus does not
	// export container_network_transmit_bytes_total" past the forty-sixth
	// character is the half naming what to go and look for.
	rows := drawnText(svg)
	if len(rows) < 2 {
		t.Fatalf("the legend has %d visible rows for 2 series: %q", len(rows), rows)
	}
	joined := strings.Join(rows, " ")
	if !strings.Contains(joined, "Network out") {
		t.Errorf("the series with no samples was dropped from the legend, so its absence reads as zero: %q", joined)
	}
	if !strings.Contains(joined, StateUnavailable) || strings.Contains(joined, "transmit counter") {
		t.Errorf("a failed series must read %q, with no sentence: %q", StateUnavailable, joined)
	}
}

// The rules charts.go states, over this shape too.
func TestTheMultiLineChartFollowsTheHouseRules(t *testing.T) {
	svg := string(HistoryLines(
		lineSeries("Network in", UnitBytesPerSecond, 1000, 2000),
		lineSeries("Network out", UnitBytesPerSecond, 300, 400),
	))

	for _, want := range []string{`role="img"`, `aria-label=`, `<title>`, `viewBox=`, "var(--"} {
		if !strings.Contains(svg, want) {
			t.Errorf("the chart is missing %s:\n%s", want, svg)
		}
	}
	if strings.Contains(svg, ` id="`) {
		t.Error("an element carries an id; two of these charts on one page would then interfere")
	}
	if strings.Contains(svg, "#") {
		t.Error("a color is baked in rather than taken from the stylesheet")
	}
	if strings.Contains(svg, `width="240px"`) || strings.Contains(svg, "height=\"") {
		t.Error("the chart sizes itself in pixels rather than leaving it to the stylesheet")
	}
}

// A flat pair sits in the middle of the band rather than being pinned to an
// edge: at the top a steady series reads as a maximum, at the bottom as an
// outage, and it is neither.
func TestTwoIdleSeriesAreDrawnFlatRatherThanAsNoData(t *testing.T) {
	svg := string(HistoryLines(
		lineSeries("Network in", UnitBytesPerSecond, 0, 0, 0),
		lineSeries("Network out", UnitBytesPerSecond, 0, 0, 0),
	))

	bands := polylineBands(t, svg)
	if len(bands) != 2 {
		t.Fatalf("drew %d lines, want 2 — two idle series are a fact, not an absence", len(bands))
	}
	middle := (linesTop + linesBottom) / 2
	for i, band := range bands {
		if band[0] != middle || band[1] != middle {
			t.Errorf("series %d is at y %v–%v, want a flat line at %v", i, band[0], band[1], middle)
		}
	}
}

// The sparkline's own rule, kept: below thirty samples every one is drawn, so six
// points cannot be mistaken for a smoothed sixty.
func TestFewSamplesAreDrawnAsSamples(t *testing.T) {
	svg := string(HistoryLines(lineSeries("Network in", UnitBytesPerSecond, 1, 2, 3)))
	if strings.Count(svg, "<circle") != 3 {
		t.Errorf("%d markers for 3 samples:\n%s", strings.Count(svg, "<circle"), svg)
	}
}

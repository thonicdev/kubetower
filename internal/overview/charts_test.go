package overview

import (
	"strings"
	"testing"
	"time"
)

// samples builds a series a minute apart, so the placement by time is exercised
// rather than the even-spacing fallback.
func samples(values ...float64) []Point {
	points := make([]Point, len(values))
	for i, v := range values {
		points[i] = Point{At: clock.Add(time.Duration(i) * time.Minute), Value: v}
	}
	return points
}

// everyChart is the set an invariant has to hold for. Adding a chart function
// without adding it here is the way the accessibility rules quietly stop
// applying to one of them.
func everyChart() map[string]string {
	return map[string]string{
		"donut":       string(CapacityDonut(Capacity{Name: "CPU", Unit: UnitCores, Used: 2000, Total: 4000})),
		"donut/empty": string(CapacityDonut(Capacity{Name: "CPU", Unit: UnitCores})),
		"spark": string(Sparkline(SparkData{
			Points: samples(1, 4, 2), Unit: UnitCores, Class: ClassOK, Label: "CPU used",
		})),
		"spark/empty": string(Sparkline(SparkData{Label: "CPU used", Empty: "no metrics backend is configured"})),
		"bar": string(StackedBar([]Segment{
			{Label: "Running", Value: 40, Class: ClassOK},
			{Label: "Failed", Value: 1, Class: ClassBad},
		}, "Pods by phase")),
		"bar/empty": string(StackedBar(nil, "Pods by phase")),
	}
}

// --- the rules that apply to every chart ---------------------------------------

func TestEveryChartCanBeReadAloud(t *testing.T) {
	// A dashboard is the page people put on a wall screen and describe to
	// somebody over a phone. A chart with no text label is a chart half the
	// operators cannot use.
	for name, svg := range everyChart() {
		if !strings.Contains(svg, `role="img"`) {
			t.Errorf("%s: no role=\"img\"", name)
		}
		if !strings.Contains(svg, `aria-label="`) {
			t.Errorf("%s: no aria-label", name)
		}
		if !strings.Contains(svg, "<title>") {
			t.Errorf("%s: no <title>", name)
		}
	}
}

func TestEveryChartScalesRatherThanFixingItsSize(t *testing.T) {
	for name, svg := range everyChart() {
		open := svg[:strings.IndexByte(svg, '>')+1]
		if !strings.Contains(open, "viewBox=") {
			t.Errorf("%s: no viewBox on the svg element: %s", name, open)
		}
		if !strings.Contains(open, "preserveAspectRatio=") {
			t.Errorf("%s: no preserveAspectRatio: %s", name, open)
		}
		if strings.Contains(open, "width=") || strings.Contains(open, "height=") {
			t.Errorf("%s: the svg element fixes its own size, so the stylesheet cannot: %s", name, open)
		}
	}
}

func TestEveryChartTakesItsColoursFromTheStylesheet(t *testing.T) {
	// A hex value baked into an SVG is the one element on the page that will not
	// follow a change of theme.
	for name, svg := range everyChart() {
		if strings.Contains(svg, "#") {
			t.Errorf("%s: a literal color reached the markup: %s", name, svg)
		}
		if !strings.Contains(svg, "var(--") {
			t.Errorf("%s: no custom property anywhere in the chart", name)
		}
	}
}

func TestChartLabelsAreEscaped(t *testing.T) {
	// An event message reaches a chart label, and a Kubernetes message is
	// arbitrary text from a controller nobody here wrote.
	svg := string(Sparkline(SparkData{Label: `<script>alert(1)</script>`, Empty: `a & b`}))
	if strings.Contains(svg, "<script>") {
		t.Fatalf("a label went into the markup unescaped: %s", svg)
	}
	if !strings.Contains(svg, "&amp;") {
		t.Errorf("an ampersand was not escaped: %s", svg)
	}
}

// --- no data is a readable answer, not an empty box ----------------------------

func TestAChartWithNoDataSaysSoInWords(t *testing.T) {
	// A blank rectangle sends the operator to the browser console. A sentence
	// does not.
	svg := string(Sparkline(SparkData{
		Label: "CPU used",
		Empty: "no metrics backend is configured",
	}))

	for _, word := range []string{"no", "metrics", "backend", "configured"} {
		if !strings.Contains(svg, word) {
			t.Errorf("the reason does not survive into the chart (%q missing): %s", word, svg)
		}
	}
	if !strings.Contains(svg, missing) {
		t.Errorf("no em dash — the agreed mark for \"the cluster did not say\": %s", svg)
	}
	if strings.Contains(svg, "<polyline") {
		t.Error("a line was drawn for a series with no points")
	}
}

func TestADonutWithNoTotalDrawsNothingRatherThanZeroPerCent(t *testing.T) {
	svg := string(CapacityDonut(Capacity{Name: "Memory", Unit: UnitBytes}))
	if strings.Contains(svg, "0%") {
		t.Errorf("a capacity nobody reported is drawn as 0%%, which is a claim: %s", svg)
	}
	if !strings.Contains(svg, "Memory") {
		t.Errorf("the chart does not say what it is about: %s", svg)
	}
}

func TestAnEmptyBreakdownSaysSo(t *testing.T) {
	svg := string(StackedBar([]Segment{{Label: "Running", Value: 0, Class: ClassOK}}, "Pods by phase"))
	if strings.Contains(svg, "<rect x=\"0\" y=\"4\"") {
		t.Error("a bar was drawn for a breakdown of nothing")
	}
	if !strings.Contains(svg, "nothing to break down") {
		t.Errorf("no explanation: %s", svg)
	}
}

func TestAnUnavailableHistoryCarriesItsReasonIntoTheChart(t *testing.T) {
	svg := string(HistorySparkline(History{
		Title:       "CPU used",
		Unit:        UnitCores,
		Unavailable: "Prometheus did not answer",
	}))
	if !strings.Contains(svg, "Prometheus") {
		t.Errorf("the reason was dropped between the data and the drawing: %s", svg)
	}
}

func TestAnEmptyWindowAndAnAbsentBackendReadDifferently(t *testing.T) {
	// The whole rule in one assertion: Prometheus answering with nothing and
	// Prometheus not answering at all must not produce the same picture.
	empty := string(HistorySparkline(History{Title: "CPU used", Unit: UnitCores}))
	absent := string(HistorySparkline(History{Title: "CPU used", Unit: UnitCores, Unavailable: "no metrics backend is configured"}))
	if empty == absent {
		t.Fatal("an empty answer and a missing backend draw the same chart")
	}
	if !strings.Contains(empty, "no samples") {
		t.Errorf("an empty window does not say it is empty: %s", empty)
	}
}

// --- resolution ------------------------------------------------------------------

func TestASeriesOfSixPointsIsDrawnAsSixPoints(t *testing.T) {
	// Six samples joined by a line look exactly like sixty. A dot on each says
	// plainly that six is all there were.
	svg := string(Sparkline(SparkData{Points: samples(1, 2, 3, 2, 1, 4), Unit: UnitCores, Label: "CPU used"}))

	if got, want := strings.Count(svg, "<circle"), 6; got != want {
		t.Errorf("markers = %d, want %d — one per sample: %s", got, want, svg)
	}
	if !strings.Contains(svg, "6 samples") {
		t.Errorf("the sample count is not stated in text: %s", svg)
	}
	if strings.Contains(svg, "<path") {
		t.Error("a path was used, which is how a smooth curve gets drawn through six points")
	}
}

func TestALongSeriesDropsTheMarkersRatherThanTheHonesty(t *testing.T) {
	values := make([]float64, 60)
	for i := range values {
		values[i] = float64(i)
	}
	svg := string(Sparkline(SparkData{Points: samples(values...), Unit: UnitCores, Label: "CPU used"}))
	if strings.Contains(svg, "<circle") {
		t.Error("sixty markers were drawn, which is a solid bar rather than a series")
	}
	if !strings.Contains(svg, "60 samples") {
		t.Errorf("the sample count is not stated: %s", svg)
	}
}

func TestSamplesArePlacedByTimeAndNotByIndex(t *testing.T) {
	// A gap in scraping is information. Spacing evenly redraws a ten-minute hole
	// as a steeper line and loses the fact that nothing was collected.
	gapped := []Point{
		{At: clock, Value: 1},
		{At: clock.Add(time.Minute), Value: 2},
		{At: clock.Add(11 * time.Minute), Value: 3},
	}
	xs := positions(gapped)
	first, second := xs[1]-xs[0], xs[2]-xs[1]
	if second <= first*5 {
		t.Errorf("positions %v space a ten-minute gap like a one-minute one", xs)
	}
}

func TestASeriesWithoutTimestampsIsStillSpreadAcrossTheChart(t *testing.T) {
	flat := []Point{{Value: 1}, {Value: 2}, {Value: 3}}
	xs := positions(flat)
	if xs[0] == xs[2] {
		t.Fatalf("every point landed on the same x: %v", xs)
	}
}

func TestAFlatSeriesSitsInTheMiddle(t *testing.T) {
	// Pinned to the top it reads as a maximum, pinned to the bottom as an
	// outage, and it is neither.
	y := plotY(5, 5, 5)
	if y <= sparkTop || y >= sparkBottom {
		t.Errorf("y = %v, want it inside the band %v..%v", y, sparkTop, sparkBottom)
	}
}

// --- the figures appear as text -----------------------------------------------------

func TestTheDonutPrintsItsFigureAsWellAsDrawingIt(t *testing.T) {
	svg := string(CapacityDonut(Capacity{Name: "CPU", Unit: UnitCores, Used: 2500, Total: 4000}))
	if !strings.Contains(svg, ">63%<") {
		t.Errorf("the percentage is not in the middle as text: %s", svg)
	}
	if !strings.Contains(svg, "2.5 of 4 cores") {
		t.Errorf("the raw figures are missing, so the chart means nothing without the angle: %s", svg)
	}
	if !strings.Contains(svg, "var(--ok)") {
		t.Errorf("63%% should still be the healthy color: %s", svg)
	}
}

func TestAnOverCommittedDonutFillsTheRingAndKeepsTheNumber(t *testing.T) {
	svg := string(CapacityDonut(Capacity{Name: "CPU", Unit: UnitCores, Used: 5000, Total: 4000}))
	if !strings.Contains(svg, ">125%<") {
		t.Errorf("the real percentage was capped with the shape: %s", svg)
	}
	if !strings.Contains(svg, "var(--bad)") {
		t.Errorf("an over-committed cluster is not drawn as a problem: %s", svg)
	}
}

func TestTheStackedBarSpellsOutEveryCount(t *testing.T) {
	// The segment that matters is the one too thin to see: four hundred Running
	// and one Failed. A legend is not optional and a tooltip is not a legend.
	svg := string(StackedBar([]Segment{
		{Label: "Running", Value: 400, Class: ClassOK},
		{Label: "Failed", Value: 1, Class: ClassBad},
	}, "Pods by phase"))

	if !strings.Contains(svg, ">Running 400<") {
		t.Errorf("Running is not spelled out: %s", svg)
	}
	if !strings.Contains(svg, ">Failed 1<") {
		t.Errorf("Failed is not spelled out: %s", svg)
	}
	if !strings.Contains(svg, "var(--bad)") {
		t.Errorf("the failed segment has no color of its own: %s", svg)
	}
	// One in four hundred is 0.6 of a unit wide and would not paint at all.
	if !strings.Contains(svg, `width="1"`) {
		t.Errorf("the one-pod segment was drawn thinner than a unit and disappears: %s", svg)
	}
}

func TestCoordinatesDoNotCarryFloatingPointNoise(t *testing.T) {
	if got, want := n(23.333333333333332), "23.33"; got != want {
		t.Errorf("n() = %q, want %q", got, want)
	}
	if got, want := n(100), "100"; got != want {
		t.Errorf("n() = %q, want %q — trimming zeros must not eat the integer", got, want)
	}
	if got, want := n(0), "0"; got != want {
		t.Errorf("n() = %q, want %q", got, want)
	}
}

func TestNoChartCarriesAnIdThatCouldCollide(t *testing.T) {
	// The same donut appears twice on a workloads page. Duplicated ids are how a
	// clipPath silently applies to the wrong chart — a bug that renders
	// perfectly.
	for name, svg := range everyChart() {
		if strings.Contains(svg, " id=") || strings.Contains(svg, "url(#") {
			t.Errorf("%s: carries an id or a reference to one: %s", name, svg)
		}
	}
}

// The label in the middle of a donut must not cross the ring.
//
// The hole is 79 units across and "666 MiB of 7.6 GiB" renders 82 wide, so an
// unconstrained label overlaps the arc. It depends on the numbers the cluster
// holds, so it is invisible on a small one and appears on a large one — which
// is why this is pinned rather than eyeballed.
func TestALongSubLabelIsConstrainedAndAShortOneIsLeftAlone(t *testing.T) {
	long := string(Donut(DonutData{
		Used: 666, Total: 7600, Centre: "9%", Sub: "666 MiB of 7.6 GiB", Label: "Memory",
	}))
	if !strings.Contains(long, "textLength=") {
		t.Errorf("a sub-label wider than the hole was not constrained; it will cross the ring:\n%s", long)
	}
	if !strings.Contains(long, `lengthAdjust="spacingAndGlyphs"`) {
		t.Error("the text is constrained by spacing alone, which leaves the glyphs touching")
	}

	// And the other half, which matters just as much: textLength forces text to
	// *exactly* the length given, so applying it to a short label would stretch
	// it right across the hole.
	short := string(Donut(DonutData{
		Used: 14, Total: 110, Centre: "13%", Sub: "14 of 110", Label: "Pods",
	}))
	if strings.Contains(short, "textLength=") {
		t.Errorf("a short sub-label was constrained, which stretches it across the hole:\n%s", short)
	}
}

func TestTheWidthEstimateCountsCharactersNotBytes(t *testing.T) {
	// A multi-byte character must not be judged several times too wide, or a
	// label with an em dash in it would be condensed for no reason.
	ascii := estimateWidth("aaaaaa", 10)
	unicode := estimateWidth("ééééér", 10)
	if ascii != unicode {
		t.Errorf("six characters measured %v and %v; the estimate is counting bytes", ascii, unicode)
	}
}

// The estimate has to be conservative in the right direction: over-estimating
// condenses a label that would have fitted, under-estimating brings the overlap
// back. Checked against the one string that was measured in a browser.
func TestTheEstimateDoesNotUnderstateTheStringThatOverflowed(t *testing.T) {
	const measuredInChrome = 82.0
	got := estimateWidth("666 MiB of 7.6 GiB", subLabelSize)
	if got < measuredInChrome {
		t.Errorf("estimated %.1f for a string Chrome renders at %.1f; under-estimating is what "+
			"lets the label cross the ring", got, measuredInChrome)
	}
	if got > donutHoleWidth*1.6 {
		t.Errorf("estimated %.1f, which is far enough over that short labels will be condensed "+
			"needlessly", got)
	}
}

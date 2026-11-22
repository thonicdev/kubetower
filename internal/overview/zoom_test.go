package overview

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The zoom must not invent a second vocabulary of time ranges.
//
// The panel already has one, and an operator who picks 6h there and then opens
// the chart has picked 6h — if the overlay quietly meant something else by the
// word, a figure read off it and written into an incident note would be about a
// period nobody selected. So every window the panel offers exists here, with the
// same label and the same range.
//
// The step is the one thing that may differ, and only in one direction: this
// chart is roughly four times as wide, so it can show more samples, and showing
// fewer would make the zoom worse than the thing it zooms.
func TestZoomWindowsAgreeWithThePanel(t *testing.T) {
	for _, panel := range Windows {
		var found *Window
		for i := range ZoomWindows {
			if ZoomWindows[i].Label == panel.Label {
				found = &ZoomWindows[i]
				break
			}
		}
		if found == nil {
			t.Errorf("the panel offers %q and the zoom does not — opening that chart would change the range under the reader", panel.Label)
			continue
		}
		if found.Range != panel.Range {
			t.Errorf("%q means %v on the panel and %v in the zoom; one word, two periods",
				panel.Label, panel.Range, found.Range)
		}
		if found.Step >= panel.Step {
			t.Errorf("%q samples every %v in the zoom and every %v on the panel — a chart four times the width showing no more detail is not a zoom",
				panel.Label, found.Step, panel.Step)
		}
	}
}

// The extra window is the reason a zoom is worth having at all for a capacity
// question, and it must not be so fine that it asks Prometheus for a series
// nobody can see. Every window lands in the band the chart can draw.
func TestZoomWindowsAskForASampleCountTheChartCanShow(t *testing.T) {
	for _, w := range ZoomWindows {
		points := int(w.Range / w.Step)
		if points < 120 || points > 500 {
			t.Errorf("%q asks for %d points; the plot is %v units wide and the useful band is 120–500",
				w.Label, points, zoomRight-zoomLeft)
		}
	}
}

func TestZoomWindowForFallsBackWhereThePanelDoes(t *testing.T) {
	// A label nobody recognizes is a stale bookmark, and it must land on the
	// same hour in both views. Two different fallbacks would put the overlay and
	// the panel behind it on different periods with no sign of it anywhere.
	if got, want := ZoomWindowFor("nonsense").Label, WindowFor("nonsense").Label; got != want {
		t.Errorf("an unknown label falls back to %q in the zoom and %q on the panel", got, want)
	}
	if got := ZoomWindowFor("6H").Label; got != "6h" {
		t.Errorf("ZoomWindowFor(%q) = %q; the label is matched without regard to case everywhere else", "6H", got)
	}
	if got := ZoomWindowFor("30d"); got.Label != "30d" || got.Range != 30*24*time.Hour {
		t.Errorf("ZoomWindowFor(%q) = %+v; the month is the zoom's own window and has to resolve", "30d", got)
	}
}

func TestZoomSeriesForReadsBothSpellings(t *testing.T) {
	for _, c := range []struct {
		hint string
		want string
		ok   bool
	}{
		{"CPU", ZoomSeriesCPU, true},
		{"CPU used", ZoomSeriesCPU, true},
		{"cpu", ZoomSeriesCPU, true},
		{"Memory", ZoomSeriesMemory, true},
		{"Memory used", ZoomSeriesMemory, true},
		{"  memory  ", ZoomSeriesMemory, true},
		{"", "", false},
		// The filesystem chart, under all three names it can arrive as: the pod
		// panel's caption, the node panel's, and the bare key a rebuilt link
		// carries.
		{"Container filesystem", ZoomSeriesDisk, true},
		{"Node filesystem", ZoomSeriesDisk, true},
		{"Filesystem", ZoomSeriesDisk, true},
		{"disk", ZoomSeriesDisk, true},
		{"Disk", ZoomSeriesDisk, true},
		// Guessed rather than refused, each of these is a chart that looks right
		// and is about something else. The network ones are refused *by design*
		// and not by omission — see TestTheNetworkChartStaysUnzoomable.
		{"Network", "", false},
		{"Network in", "", false},
		{"Pod network", "", false},
		{"Pods by phase", "", false},
	} {
		got, ok := ZoomSeriesFor(c.hint)
		if got != c.want || ok != c.ok {
			t.Errorf("ZoomSeriesFor(%q) = %q, %v; want %q, %v", c.hint, got, ok, c.want, c.ok)
		}
	}
}

// The captions this resolves are not written here — they are written by the
// series specs in object.go, and a rename there is invisible to the test above.
//
// So the labels come from `Options()` on a real history, which is the same
// method the panel's selector reads and the same string it puts in
// `data-chart-zoom`. Rename "Container filesystem" and this fails; add a fifth
// chart and it fails too, which is the point — a chart the panel offers must
// either open or be knowingly listed here as one that does not.
func TestEveryChartThePanelOffersEitherOpensOrIsKnowinglyRefused(t *testing.T) {
	// Non-nil metrics so `fill` runs and stamps each series with its real title.
	// What it answers does not matter; nothing here reads a point.
	m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "1"}), probe: everyCounter(t)}

	want := map[string]string{
		ChartCPU:    ZoomSeriesCPU,
		ChartMemory: ZoomSeriesMemory,
		ChartDisk:   ZoomSeriesDisk,
		// Empty means "must not resolve". See TestTheNetworkChartStaysUnzoomable.
		ChartNetwork: "",
	}

	for _, h := range []ObjectHistory{
		PodHistory(context.Background(), m, "any", "kube-system", "coredns-abc", WindowFor("1h")),
		NodeHistory(context.Background(), m, "any", "ip-10-0-1-7", WindowFor("1h")),
	} {
		for _, option := range h.Options() {
			got, ok := ZoomSeriesFor(option.Label)
			expected, listed := want[option.Key]
			if !listed {
				t.Errorf("the panel offers a chart called %q (%s) that this test has never heard of; "+
					"decide whether the overlay opens it before shipping it", option.Key, option.Label)
				continue
			}
			if expected == "" {
				if ok {
					t.Errorf("%q (%s) resolved to %q; it is refused on purpose and now opens an overlay",
						option.Key, option.Label, got)
				}
				continue
			}
			if !ok || got != expected {
				t.Errorf("ZoomSeriesFor(%q) = %q, %v; the panel marks this chart %s and the overlay must open it",
					option.Label, got, ok, expected)
			}
		}
	}
}

// Network is one chart and two Histories, and that is why it does not open.
//
// `ZoomFor` takes a single History and `ZoomPayload` carries a single points
// array, so zooming it would mean picking in or out and discarding the
// comparison the shared axes exist for. This is deliberate, and it is a test
// because the obvious extension is to add network beside the filesystem chart.
func TestTheNetworkChartStaysUnzoomable(t *testing.T) {
	for _, label := range []string{"Network", "Network in", "Network out", "Pod network", "Pod network in"} {
		if series, ok := ZoomSeriesFor(label); ok {
			t.Errorf("ZoomSeriesFor(%q) = %q; the overlay draws one series and this chart is two, "+
				"so opening it throws away the in-versus-out reading that is the only reason they share axes",
				label, series)
		}
	}
}

// --- the lookup the pointer uses ---------------------------------------------

func TestZoomNearestPicksTheClosestSample(t *testing.T) {
	xs := []float64{10, 20, 30}
	for _, c := range []struct {
		x    float64
		want int
	}{
		{-100, 0},
		{9, 0},
		{14, 0},
		{16, 1},
		{20, 1},
		// Exactly between two samples. The earlier one wins, so the answer does
		// not depend on which direction the pointer arrived from.
		{25, 1},
		{29, 2},
		{1000, 2},
	} {
		if got := ZoomNearest(xs, c.x); got != c.want {
			t.Errorf("ZoomNearest(%v, %v) = %d, want %d", xs, c.x, got, c.want)
		}
	}

	if got := ZoomNearest(nil, 5); got != -1 {
		t.Errorf("ZoomNearest(nil, 5) = %d; an empty series has no nearest sample and must say so rather than panic", got)
	}
}

// The table is what makes following the pointer free. It has to cover the plot
// from edge to edge: a reader running the pointer to the far right and getting
// the second-to-last sample would read the wrong figure at exactly the moment
// they are looking for the latest one.
func TestZoomBucketTableCoversThePlotEndToEnd(t *testing.T) {
	points := series(t, 40)
	xs := zoomPositions(points)
	buckets := ZoomBucketTable(xs)

	if len(buckets) != zoomBucketCount {
		t.Fatalf("the table has %d entries, want %d", len(buckets), zoomBucketCount)
	}
	if buckets[0] != 0 {
		t.Errorf("the left edge of the plot resolves to sample %d, want the first", buckets[0])
	}
	if got, want := buckets[len(buckets)-1], len(points)-1; got != want {
		t.Errorf("the right edge of the plot resolves to sample %d, want the last (%d)", got, want)
	}

	previous := -1
	for i, at := range buckets {
		if at < 0 || at >= len(points) {
			t.Fatalf("bucket %d points at sample %d, which does not exist", i, at)
		}
		if at < previous {
			t.Fatalf("bucket %d goes back to sample %d after %d — moving the pointer rightwards would move the crosshair leftwards", i, at, previous)
		}
		previous = at
	}

	// And it agrees with the function it was built from, which is the property
	// that lets the browser do no searching at all.
	for i := 0; i < len(buckets); i += 37 {
		at := zoomLeft + (zoomRight-zoomLeft)*float64(i)/float64(len(buckets)-1)
		if want := ZoomNearest(xs, at); buckets[i] != want {
			t.Errorf("bucket %d says sample %d, ZoomNearest says %d", i, buckets[i], want)
		}
	}
}

// --- the view ------------------------------------------------------------------

// The case an operator hits most, and the one that decides whether this feature
// reads as finished: a pod younger than the window has nothing in it, and an
// overlay opening onto an empty chart frame is read as broken every time.
func TestZoomForSaysWhyWhenThereAreNoSamples(t *testing.T) {
	view := ZoomFor(History{Title: "CPU", Unit: UnitCores}, ZoomWindowFor("7d"))

	if view.Drawable() {
		t.Fatal("a series with no samples reported itself as drawable")
	}
	if view.Empty == "" {
		t.Fatal("an empty series produced no explanation; the overlay would open onto nothing")
	}
	if view.Empty != StateNoMetrics {
		t.Errorf("Empty = %q, want the short state %q", view.Empty, StateNoMetrics)
	}
	if view.Chart != "" {
		t.Errorf("an empty series still drew a chart:\n%s", view.Chart)
	}
	if view.Data != "" {
		t.Error("an empty series shipped a payload; the script would find a chart with no samples in it")
	}

	// A failed read is a different short state. "Empty" and "we could not look"
	// must never be merged.
	reason := "no metrics backend is configured, so there is no history to draw"
	told := ZoomFor(History{Title: "CPU", Unit: UnitCores, Unavailable: reason, Missing: MissingFailed}, ZoomWindowFor("1h"))
	if told.Empty != StateUnavailable {
		t.Errorf("Empty = %q, want %q", told.Empty, StateUnavailable)
	}
}

func TestZoomForPlacesSamplesByTimeAndFormatsThem(t *testing.T) {
	points := series(t, 6)
	view := ZoomFor(History{Title: "CPU", Unit: UnitCores, Points: points}, ZoomWindowFor("1h"))

	if !view.Drawable() {
		t.Fatalf("a series of %d samples was not drawable: %q", len(points), view.Empty)
	}

	var payload ZoomPayload
	if err := json.Unmarshal([]byte(view.Data), &payload); err != nil {
		t.Fatalf("the payload is not JSON: %v\n%s", err, view.Data)
	}
	if len(payload.Points) != len(points) {
		t.Fatalf("the payload carries %d samples, the series has %d", len(payload.Points), len(points))
	}
	if payload.Points[0].X != zoomLeft {
		t.Errorf("the first sample sits at x=%v, want the left edge of the plot (%v)", payload.Points[0].X, zoomLeft)
	}
	if payload.Points[len(payload.Points)-1].X != zoomRight {
		t.Errorf("the last sample sits at x=%v, want the right edge (%v)", payload.Points[len(payload.Points)-1].X, zoomRight)
	}
	if payload.Width != zoomWidth {
		t.Errorf("the payload says the viewBox is %v wide; it is %v, and the script scales client coordinates by it", payload.Width, zoomWidth)
	}

	// The figures are formatted here, in the units the tables beside the chart
	// use. A second formatter in the browser is how a chart comes to read 0.4
	// where the row next to it says 400m.
	if got := payload.Points[0].V; got != "100m" {
		t.Errorf("the first sample reads %q, want %q — millicores, as everywhere else in this package", got, "100m")
	}
	if !strings.Contains(payload.Points[0].T, "UTC") {
		t.Errorf("the stamp %q does not say which clock it is on", payload.Points[0].T)
	}

	// The highest sample is at the top of the band and the lowest at the bottom.
	var high, low ZoomPoint
	high, low = payload.Points[0], payload.Points[0]
	for _, p := range payload.Points {
		if p.Y < high.Y {
			high = p
		}
		if p.Y > low.Y {
			low = p
		}
	}
	if high.Y != zoomTop || low.Y != zoomBottom {
		t.Errorf("the band is %v..%v; the series was drawn in %v..%v", zoomTop, zoomBottom, high.Y, low.Y)
	}

	if view.LastValue != payload.Points[len(payload.Points)-1].V {
		t.Errorf("the readout is seeded with %q and the last sample is %q", view.LastValue, payload.Points[len(payload.Points)-1].V)
	}
	if !strings.Contains(view.Summary, "samples") {
		t.Errorf("the summary does not say how many samples were drawn: %q", view.Summary)
	}
	if !strings.Contains(string(view.Chart), "cz-marker") || !strings.Contains(string(view.Chart), "cz-cursor") {
		t.Error("the chart has no crosshair for the script to move")
	}
}

// A steady series is the ordinary state of a healthy pod, and it must not be
// pinned to an edge: at the top it reads as a maximum, at the bottom as an
// outage, and it is neither.
func TestZoomForDrawsAFlatSeriesDownTheMiddle(t *testing.T) {
	at := time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC)
	points := []Point{
		{At: at, Value: 512},
		{At: at.Add(time.Minute), Value: 512},
		{At: at.Add(2 * time.Minute), Value: 512},
	}
	view := ZoomFor(History{Title: "CPU", Unit: UnitCores, Points: points}, ZoomWindowFor("15m"))

	var payload ZoomPayload
	if err := json.Unmarshal([]byte(view.Data), &payload); err != nil {
		t.Fatalf("the payload is not JSON: %v", err)
	}
	middle := (zoomTop + zoomBottom) / 2
	for i, p := range payload.Points {
		if p.Y != middle {
			t.Errorf("sample %d of a flat series is at y=%v, want the middle of the band (%v)", i, p.Y, middle)
		}
	}
}

// One sample is a real answer from a young pod, and it must not divide by a
// span of zero or land the crosshair off the plot.
func TestZoomForHandlesASingleSample(t *testing.T) {
	points := []Point{{At: time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC), Value: 1024}}
	view := ZoomFor(History{Title: "Memory", Unit: UnitBytes, Points: points}, ZoomWindowFor("15m"))

	if !view.Drawable() {
		t.Fatalf("a single sample was reported as nothing to draw: %q", view.Empty)
	}
	var payload ZoomPayload
	if err := json.Unmarshal([]byte(view.Data), &payload); err != nil {
		t.Fatalf("the payload is not JSON: %v", err)
	}
	if len(payload.Points) != 1 {
		t.Fatalf("the payload carries %d samples, want 1", len(payload.Points))
	}
	x := payload.Points[0].X
	if x < zoomLeft || x > zoomRight {
		t.Errorf("the only sample sits at x=%v, outside the plot band %v..%v", x, zoomLeft, zoomRight)
	}
	for i, at := range payload.Buckets {
		if at != 0 {
			t.Fatalf("bucket %d points at sample %d; there is only one sample", i, at)
		}
	}
	if view.LastValue != "1 KiB" {
		t.Errorf("the readout reads %q, want %q", view.LastValue, "1 KiB")
	}
}

// A title is the one string in this chart that comes from further away than
// this package, and the chart is written as raw HTML into the page.
func TestZoomForEscapesWhatItIsToldToDraw(t *testing.T) {
	points := series(t, 4)
	view := ZoomFor(History{Title: `<script>alert(1)</script>`, Unit: UnitCores, Points: points}, ZoomWindowFor("1h"))

	chart := string(view.Chart)
	if strings.Contains(chart, "<script>") {
		t.Errorf("the title was written into the chart unescaped:\n%s", chart)
	}
	if !strings.Contains(chart, "&lt;script&gt;") {
		t.Errorf("the title does not appear escaped either, so it is not being drawn at all:\n%s", chart)
	}
	// json.Marshal turns < > & into \u escapes, which is what stops anything in
	// the payload from ending the <script type="application/json"> it sits in.
	if strings.ContainsAny(string(view.Data), "<>") {
		t.Errorf("the payload carries a raw angle bracket and could close its own element:\n%s", view.Data)
	}
}

// series is a run of samples a minute apart with a distinct maximum and
// minimum, so a test can tell the top of the band from the bottom.
func series(t *testing.T, n int) []Point {
	t.Helper()
	at := time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC)
	points := make([]Point, n)
	for i := range points {
		points[i] = Point{At: at.Add(time.Duration(i) * time.Minute), Value: float64(100 + i*50)}
	}
	return points
}

// The payload carries what the script reads, and nothing else.
//
// `top` and `bottom` are not sent: `internal/web/static/chartzoom.js` uses
// `left`, `right`, `width`, `points` and `buckets`, and converts no Y at all —
// every point arrives carrying its own y, t and v. Two numbers crossing the
// wire to be ignored is cheap; the cost is that a reader of the struct would
// believe the script scales against a vertical band, and reasons about the
// crosshair from a field that has no effect.
//
// A key set rather than a field list, because the JSON is the interface: renaming
// a tag would slip past a test that only touched the Go names.
func TestTheZoomPayloadCarriesOnlyWhatTheScriptReads(t *testing.T) {
	view := ZoomFor(History{Title: "CPU", Unit: UnitCores, Points: series(t, 6)}, ZoomWindowFor("1h"))
	if !view.Drawable() {
		t.Fatalf("the series was not drawable: %q", view.Empty)
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(view.Data), &raw); err != nil {
		t.Fatalf("the payload is not JSON: %v\n%s", err, view.Data)
	}

	// Exactly these. chartzoom.js reads d.left, d.right, d.width, d.points and
	// d.buckets; add a key here and either the script reads it or it should not
	// be sent.
	want := map[string]bool{"left": true, "right": true, "width": true, "points": true, "buckets": true}
	for key := range raw {
		if !want[key] {
			t.Errorf("the payload sends %q, which chartzoom.js does not read. Either wire it up "+
				"in the script or stop sending it: an unread field is read as an explanation "+
				"of how the crosshair works", key)
		}
	}
	for key := range want {
		if _, ok := raw[key]; !ok {
			t.Errorf("the payload no longer sends %q, which chartzoom.js reads. The crosshair "+
				"stops resolving a bucket and the chart looks inert rather than broken", key)
		}
	}
}

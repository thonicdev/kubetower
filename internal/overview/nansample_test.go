package overview

import (
	"fmt"
	"math"
	"testing"
)

// A NaN or a `+Inf` is dropped rather than drawn as zero: a gap in the line is
// honest, a zero is a claim. `strconv.ParseFloat` accepts `"NaN"`, `"+Inf"` and
// `"-Inf"` and returns **no error** for any of them, so an error check is not a
// finiteness check — and Prometheus writes a gap in a series as the literal
// `NaN` and a division by zero as `+Inf`.
//
// This test puts a `NaN` through the whole read rather than asserting on
// `sampleValue`'s three lines, because
// what has to hold is a property of the *series* the chart is handed: no point
// in it may be non-finite. A unit test on the helper would pass the day somebody
// adds a second reader that does its own parsing.
func TestNoNonFinitePrometheusSampleReachesTheChart(t *testing.T) {
	// Prometheus's own wire shape: values are [unixSeconds, "quoted string"],
	// quoted precisely so a large counter survives a float64.
	body := []byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[
		[1788294000,"1.5"],
		[1788294060,"NaN"],
		[1788294120,"2.5"],
		[1788294180,"+Inf"],
		[1788294240,"-Inf"],
		[1788294300,"3.5"]
	]}]}}`)

	points, err := parseRange(body)
	if err != nil {
		t.Fatalf("parseRange: %v", err)
	}

	for i, p := range points {
		if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			t.Errorf("point %d at %s carries %v. A non-finite sample reaching the series is "+
				"what parseRange's loop says cannot happen — "+
				"`strconv.ParseFloat` returns no error for \"NaN\" or \"+Inf\", so an error "+
				"check is not a finiteness check", i, p.At.Format("15:04:05"), p.Value)
		}
	}

	// And the honest half: the three finite samples are still there. A guard that
	// dropped everything would satisfy the loop above, and a chart with no line
	// in it is the failure this guard is supposed to avoid, not the guard.
	if len(points) != 3 {
		var got []string
		for _, p := range points {
			got = append(got, fmt.Sprintf("%v", p.Value))
		}
		t.Errorf("got %d points %v, want the 3 finite ones. Dropping the non-finite samples "+
			"must not drop the readings around them: a gap in the line is the intent, an "+
			"absent line is not", len(points), got)
	}
}

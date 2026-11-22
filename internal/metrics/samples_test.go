package metrics

import (
	"encoding/json"
	"reflect"
	"testing"
)

func decodeSamples(t *testing.T, raw string) []Point {
	t.Helper()
	var s samples
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return s
}

// The shape Prometheus sends is read in place, and a gap is left out rather than
// turned into a zero.
func TestSamplesAreReadAsPrometheusSendsThem(t *testing.T) {
	got := decodeSamples(t, `[[1788294085.36,"0.5"],[1788294985,"NaN"], [1788295885 , "+Inf"],[1788296785,"1e3"],[1788297685,"-2"]]`)
	want := []Point{{1788294085.36, 0.5}, {1788296785, 1000}, {1788297685, -2}}
	if !reflect.DeepEqual([]Point(got), want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := decodeSamples(t, `[]`); got == nil || len(got) != 0 {
		t.Errorf("an empty list is %v, want empty and non-nil", got)
	}
	if got := decodeSamples(t, " [ ] "); len(got) != 0 {
		t.Errorf("a spaced empty list is %v", got)
	}
}

// What the fast scan does not recognise keeps the rules it always had.
func TestSamplesTheScanDoesNotRecognizeKeepTheOldRules(t *testing.T) {
	for name, raw := range map[string]string{
		"an entry with one item":       `[[1788294085],[1788294985,"2"]]`,
		"a timestamp that is text":     `[["x","1"],[1788294985,"2"]]`,
		"an unquoted value":            `[[1788294085,3],[1788294985,"2"]]`,
		"an escaped quote":             `[[1788294085,"\"1"],[1788294985,"2"]]`,
		"a value that is not a number": `[[1788294085,"abc"],[1788294985,"2"]]`,
	} {
		got := decodeSamples(t, raw)
		if want := []Point{{1788294985, 2}}; !reflect.DeepEqual([]Point(got), want) {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
	var s samples
	if err := json.Unmarshal([]byte(`{"not":"a list"}`), &s); err == nil {
		t.Error("an object is read as samples")
	}
}

// A series is scaled once, from the unscaled points.
func TestASeriesIsScaledOnceAndGapsAreNotZeros(t *testing.T) {
	raw := `{"status":"success","data":{"resultType":"matrix","result":[` +
		`{"metric":{"namespace":"a","pod":"p","container":"c"},"values":[[10,"0.25"],[20,"NaN"],[30,"0.5"]]}]}}`
	got, err := parseSeries([]byte(raw), 1000)
	if err != nil {
		t.Fatal(err)
	}
	want := []Point{{10, 250}, {30, 500}}
	if !reflect.DeepEqual(got["a/p/c"], want) {
		t.Errorf("got %v, want %v", got["a/p/c"], want)
	}
}

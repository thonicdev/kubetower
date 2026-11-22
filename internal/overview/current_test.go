package overview

import (
	"testing"
)

// The machines' usage now is the sum of what metrics-server reports per node:
// the figure `kubectl top nodes` prints one line of per machine.
func TestCurrentUsageIsTheSumOfEveryNodesReading(t *testing.T) {
	raw := []byte(`{"kind":"NodeMetricsList","items":[
		{"metadata":{"name":"desktop-control-plane"},"usage":{"cpu":"230145812n","memory":"4194304Ki"}},
		{"metadata":{"name":"desktop-worker"},"usage":{"cpu":"70m","memory":"1Gi"}}
	]}`)
	readings, err := parseNodeUsage(raw)
	if err != nil {
		t.Fatalf("a well-formed answer did not parse: %v", err)
	}
	total, from := sumCurrent(readings)
	if !from.OK {
		t.Fatalf("two measured machines summed to no answer: %q", from.Detail)
	}
	// 230145812n is 230.1m, which a millicore count rounds up the way
	// resource.Quantity does for `kubectl top`: 231m.
	if total.CPUMilli != 231+70 || total.MemoryBytes != (4<<30)+(1<<30) || total.Nodes != 2 {
		t.Errorf("summed to %+v; want 301m, 5 GiB over 2 nodes", total)
	}
	if total.CPUText() != "301m" || total.MemoryText() != "5 GiB" {
		t.Errorf("spelt %q and %q; want the console's own 301m and 5 GiB", total.CPUText(), total.MemoryText())
	}
}

// A metrics-server that has measured no machine yet has measured nothing. A
// sum over nothing is 0m, which reads as an idle cluster - DoD §3's zero.
func TestNoMeasuredNodeIsNotAZero(t *testing.T) {
	readings, err := parseNodeUsage([]byte(`{"kind":"NodeMetricsList","items":[]}`))
	if err != nil {
		t.Fatalf("an empty list did not parse: %v", err)
	}
	total, from := sumCurrent(readings)
	if from.OK {
		t.Fatalf("an empty answer was summed into a measurement: %+v", total)
	}
	if from.Detail == "" {
		t.Error("the absence carries no sentence")
	}
}

// An answer that cannot be read is an error, never an empty list.
func TestAnUnreadableNodeAnswerIsAnError(t *testing.T) {
	if _, err := parseNodeUsage([]byte(`<html>`)); err == nil {
		t.Fatal("an unreadable answer parsed")
	}
}

// The empty answer is non-nil readings: that is the whole of how the tile tells
// "answered, nothing measured" from "could not be read".
func TestAnEmptyAnswerIsAnAnswer(t *testing.T) {
	readings, err := parseNodeUsage([]byte(`{"kind":"NodeMetricsList","items":[]}`))
	if err != nil {
		t.Fatalf("an empty list did not parse: %v", err)
	}
	if byName := readingsByNode(readings); byName == nil {
		t.Fatal("an empty answer became nil readings, which the tile reads as metrics-server not answering")
	}
}

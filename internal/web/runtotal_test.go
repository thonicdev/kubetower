package web

import (
	"testing"

	"github.com/thonicdev/kubetower/internal/overview"
)

// A finished pod's run total reaches the wire as a reading in bytes, spelled
// as every byte figure in the console is; a line without one sends none.
func TestARunTotalIsSentAsABytesReading(t *testing.T) {
	got := runTotalReading(overview.History{RunTotal: &overview.RunTotal{Value: 3 << 20, Samples: 1}})
	if got == nil || got.Value == nil || *got.Value != 3<<20 || !got.Trustworthy {
		t.Fatalf("got %+v, want a trustworthy 3 MiB", got)
	}
	if want := (overview.Capacity{Unit: overview.UnitBytes, Used: 3 << 20}).UsedText(); got.Text != want {
		t.Errorf("text %q, want %q", got.Text, want)
	}
	if runTotalReading(overview.History{}) != nil {
		t.Error("a line with no run total sends one")
	}
}

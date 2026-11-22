package resources

import "testing"

// A figure is shown in the largest readable band: `1759m · 7.6 GiB · 43.8 GiB`
// would leave CPU the one figure in that row never scaled.
//
// One case per band and one at each boundary, because the rule is a band and
// the only way a band goes wrong is at its edge.
func TestCPUIsBandedAtOneCoreAndMillicoresBelowIt(t *testing.T) {
	for _, c := range []struct {
		milli int64
		want  string
		why   string
	}{
		{0, "0m", "a pod using nothing says so, and is not an em dash"},
		{264, "264m", "the low end is where an unconditional conversion does its damage"},
		{999, "999m", "still under a core"},
		{1000, "1.0 cores", "the band, at exactly one core"},
		{1759, "1.8 cores", "a real node's reading"},
		{8000, "8.0 cores", "a whole node, in the same shape as the GiB beside it"},
		{64_000, "64.0 cores", "and the figure the band is actually for: never 64000m"},
	} {
		if got := CPUSize(c.milli); got != c.want {
			t.Errorf("CPUSize(%d) = %q, want %q — %s", c.milli, got, c.want, c.why)
		}
	}
}

// The text is display; Value is what the column sorts on, and it stays in
// millicores whatever band the text lands in.
func TestBandingTheCPUTextDoesNotChangeWhatItSortsBy(t *testing.T) {
	for _, milli := range []int64{264, 999, 1000, 64_000} {
		cell := cpuCell(&Usage{CPUMillicores: milli})
		if cell.Value != float64(milli) {
			t.Errorf("cpuCell(%d).Value = %v, want %v — the sort must not follow the band",
				milli, cell.Value, float64(milli))
		}
	}
}

// MemorySize does not stop at GiB, or a 40 TiB mount would print
// `40960.0 GiB` — the same six-digit complaint one unit along.
func TestMemorySizeHasATopBand(t *testing.T) {
	for _, c := range []struct {
		bytes int64
		want  string
	}{
		{512, "0 KiB"},
		{600 << 10, "600 KiB"},
		{900 << 20, "900 MiB"},
		{2 << 30, "2.0 GiB"},
		{1023 << 30, "1023.0 GiB"},
		{1 << 40, "1.0 TiB"},
		{40 << 40, "40.0 TiB"},
	} {
		if got := MemorySize(c.bytes); got != c.want {
			t.Errorf("MemorySize(%d) = %q, want %q", c.bytes, got, c.want)
		}
	}
	// Named rather than hidden: the ceiling is TiB, the same as
	// textutil.BytesCompact's, so a petabyte prints four digits of TiB.
	if got := MemorySize(1 << 50); got != "1024.0 TiB" {
		t.Errorf("MemorySize(1 PiB) = %q, want 1024.0 TiB — if this changes, so should the "+
			"comment on MemorySize and textutil.byteUnits", got)
	}
}

package resources

import (
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/textutil"
)

// Missing, Dash, ShortDuration, JoinCapped and Clip are exported because
// internal/customresources renders into the same table and must print an
// absent value, an age and an over-long field exactly as the built-in kinds do.

// Missing is what an absent value prints as. A blank cell reads as a rendering
// fault; an em dash reads as "the cluster did not say".
const Missing = "—"

func col(title string) Column    { return Column{Title: title} }
func numCol(title string) Column { return Column{Title: title, Numeric: true} }

// filterCol is a column the filter control offers. See Column.Filterable.
func filterCol(title string) Column { return Column{Title: title, Filterable: true} }

// sideCol is a column drawn only when asked for. See Column.Secondary.
func sideCol(title string) Column { return Column{Title: title, Secondary: true} }

// addrCol is a column holding a value to paste elsewhere: a host, an external
// address, a cluster IP. See Column.Copyable.
func addrCol(title string) Column { return Column{Title: title, Copyable: true} }

// secondary makes any column one drawn only when asked for, keeping its other
// flags (an address that still copies once shown). See Column.Secondary.
func secondary(c Column) Column { c.Secondary = true; return c }

// copyable puts the full reading on a cell's copy control, which may hold more
// than the capped Text. An absent value gets no control: an em dash is not
// worth copying.
func copyable(cell Cell, full string) Cell {
	if cell.Text == Missing || strings.TrimSpace(full) == "" {
		return cell
	}
	cell.Copy = full
	return cell
}

// copyableText is text() with the same value on its copy control, for a cell
// whose whole reading fits in its column and needs no second form.
func copyableText(s string) Cell { return copyable(text(s), s) }

// gaugeCol is a numeric column whose cells draw a bar beside their reading.
// See Column.Gauged.
func gaugeCol(title string) Column { return Column{Title: title, Numeric: true, Gauged: true} }

func text(s string) Cell   { return Cell{Text: Dash(s)} }
func count(n int) Cell     { return Cell{Text: strconv.Itoa(n), Value: float64(n)} }
func count32(n int32) Cell { return count(int(n)) }
func graded(s, class string, rank int) Cell {
	return Cell{Text: Dash(s), Class: class, Rank: rank}
}

// Dash renders an absent value as Missing rather than as an empty cell.
func Dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return Missing
	}
	return s
}

// restarts is a graded count: restarts are the cheapest signal that something
// is wrong, even while the pod is Running.
func restarts(n int32) Cell {
	cell := count32(n)
	switch {
	case n == 0:
	case n < 5:
		cell.Class = ClassWarn
	default:
		cell.Class, cell.Rank = ClassBad, RankWarn
	}
	return cell
}

// ratio renders "2/3" and grades it.
//
// want == 0 is not a fault: a Deployment scaled to zero is a decision.
func ratio(ready, want int32) Cell {
	cell := Cell{Text: fmt.Sprintf("%d/%d", ready, want), Value: completeness(ready, want)}
	switch {
	case want == 0 || ready >= want:
		cell.Class, cell.Rank = ClassOK, RankOK
	case ready == 0:
		cell.Class, cell.Rank = ClassBad, RankBad
	default:
		cell.Class, cell.Rank = ClassWarn, RankWarn
	}
	return cell
}

// completeness is what a ratio column sorts by: how far along, not how many,
// so "3/10" sorts below "1/1". Nothing wanted is complete, as ratio grades it.
func completeness(ready, want int32) float64 {
	if want <= 0 {
		return 1
	}
	return float64(ready) / float64(want)
}

func age(t metav1.Time, now time.Time) Cell {
	if t.IsZero() {
		return Cell{Text: Missing}
	}
	return elapsed(now.Sub(t.Time))
}

// elapsed is a duration as a cell: printed to one unit, sorted in seconds.
func elapsed(d time.Duration) Cell {
	return Cell{Text: ShortDuration(d), Value: d.Seconds()}
}

// cpuCell and memoryCell are the two live columns. Anything unmeasured (no
// metrics backend, a pod the backend has not seen yet) prints an em dash; only
// a pod genuinely using nothing prints "0m".
func cpuCell(usage *Usage) Cell {
	if !measured(usage) {
		return Cell{Text: Missing}
	}
	return Cell{
		Text:  CPUSize(usage.CPUMillicores),
		Value: float64(usage.CPUMillicores),
	}
}

func memoryCell(usage *Usage) Cell {
	if !measured(usage) {
		return Cell{Text: Missing}
	}
	return Cell{Text: MemorySize(usage.MemoryBytes), Value: float64(usage.MemoryBytes)}
}

// measured says the CPU and memory figures on this entry were taken: there is
// an entry, and it is not one present only for its disk (see
// Usage.ComputeUnmeasured).
func measured(usage *Usage) bool { return usage != nil && !usage.ComputeUnmeasured }

// withGauge puts a bar on a cell that already carries its reading. used and
// whole share any unit. A whole of zero gets no bar (a proportion of nothing is
// not a proportion), and an em dash never gains one.
func withGauge(cell Cell, used, whole int64) Cell {
	if cell.Text == Missing || whole <= 0 || used < 0 {
		return cell
	}
	cell.Gauge = gauge(float64(used) / float64(whole) * 100)
	return cell
}

// The bands a gauge is colored in: three quarters leaves little room for the
// scheduler, nine tenths nears kubelet eviction. Neither is a rank (see
// Gauge.Class).
const (
	gaugeWarnAt = 75
	gaugeBadAt  = 90
)

// gauge clamps a percentage into a bar and grades it. Over 100% is a real
// reading (usage and allocatable are sampled at different moments), so the bar
// fills and the cell's text still states the figure. Rounded to one decimal
// because it becomes a CSS width, re-sent on every refresh.
func gauge(percent float64) *Gauge {
	switch {
	case percent < 0:
		percent = 0
	case percent > 100:
		percent = 100
	}
	percent = math.Round(percent*10) / 10
	class := ClassOK
	switch {
	case percent >= gaugeBadAt:
		class = ClassBad
	case percent >= gaugeWarnAt:
		class = ClassWarn
	}
	return &Gauge{Percent: percent, Class: class}
}

// volumeCell is the Used column on the two storage lists: how full a claim's
// filesystem is, with a bar, or an em dash and the reason there is none. The
// absences are told apart (see Volume; internal/web/volumeusage.go writes the
// sentences).
//
// The cell sorts by bytes used rather than by percentage: a 90%-full 1 MiB
// claim is not the one to reclaim.
func volumeCell(usage *Usage) Cell {
	if usage == nil || usage.Volume == nil {
		// Nothing was said about this claim. The row function supplies the
		// sentence, since only the claim knows whether it is bound.
		return Cell{Text: Missing}
	}
	volume := usage.Volume
	if !volume.Measured || volume.CapacityBytes <= 0 {
		// A reason and no reading, so no bar: an empty track would read as a
		// claim with room.
		return Cell{Text: Missing, Hint: volume.Note}
	}
	// The hover is `used / declared size`, then the note naming who measured.
	used := MemorySize(volume.UsedBytes)
	hint := BytesFraction(float64(volume.UsedBytes), volume.CapacityBytes)
	if volume.Note != "" {
		hint += ", " + volume.Note
	}
	cell := Cell{
		Text:  used,
		Value: float64(volume.UsedBytes),
		Hint:  hint,
	}
	return withGauge(cell, volume.UsedBytes, volume.CapacityBytes)
}

// Fraction is what hovering a gauged reading says: the reading over its whole,
// both halves in the whole's unit and to one precision rule, the unit once at
// the end — `0.37 / 8 cores`, `3.2 / 16 GiB`. The cell draws only the bar, so
// the hover carries the quantity (Cell.Hint), and the client never composes a
// unit.
//
// One unit for the pair: CPU always in cores, bytes in the band MemorySize
// would choose for the whole (byteBand). The precision rule (fractionFigure)
// matches `figure()` in web/ui/src/clusters/fleet.ts, which composes the
// cluster chooser's hover in the browser; change one, change both.
func Fraction(used, whole float64, unit string) string {
	out := fractionFigure(used) + " / " + fractionFigure(whole)
	if unit != "" {
		out += " " + unit
	}
	return out
}

// CPUFraction is Fraction for millicores, in cores on both sides.
//
// usedMilli is a float because a metrics sample is one, and it is never
// rounded before dividing, or it would disagree with the chooser's figure.
func CPUFraction(usedMilli float64, wholeMilli int64) string {
	return Fraction(usedMilli/1000, float64(wholeMilli)/1000, "cores")
}

// BytesFraction is Fraction for bytes, both halves in the whole's band. used
// is a float for CPUFraction's reason.
func BytesFraction(used float64, whole int64) string {
	unit, size := byteBand(whole)
	return Fraction(used/size, float64(whole)/size, unit)
}

// byteBand is the band MemorySize would choose for a figure: its unit and
// that unit's size.
func byteBand(bytes int64) (string, float64) {
	switch {
	case bytes >= 1<<40:
		return "TiB", 1 << 40
	case bytes >= 1<<30:
		return "GiB", 1 << 30
	case bytes >= 1<<20:
		return "MiB", 1 << 20
	default:
		return "KiB", 1 << 10
	}
}

// fractionFigure is the precision rule both halves share: one decimal place
// from one up, two below it, never a trailing zero (`8`, not `8.0`), and a real
// figure that rounds to nothing is `<0.01`, never `0`, which reads as idle.
// It mirrors fleet.ts' figure().
func fractionFigure(n float64) string {
	places := 1
	if n < 1 {
		places = 2
	}
	text := toFixed(n, places)
	text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	if text == "0" && n > 0 {
		return "<0.01"
	}
	if text == "-0" {
		return "0"
	}
	return text
}

// toFixed is JavaScript's Number.prototype.toFixed, which fleet.ts' figure()
// uses: the exact binary value rounded to `places`, an exact tie going up.
// strconv rounds an exact tie to even, so only ties are corrected here.
func toFixed(n float64, places int) string {
	text := strconv.FormatFloat(n, 'f', places, 64)
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return text
	}
	if n < 0 {
		// toFixed rounds the magnitude, so -0.125 is `-0.13` as 0.125 is `0.13`.
		magnitude := toFixed(-n, places)
		if strings.Trim(magnitude, "0.") == "" {
			return magnitude
		}
		return "-" + magnitude
	}
	// Every finite float64 has a finite decimal expansion; 1100 places holds it.
	exact := new(big.Float).SetFloat64(n).Text('f', 1100)
	dot := strings.IndexByte(exact, '.')
	if strings.TrimRight(exact[dot+1+places:], "0") != "5" {
		return text // not an exact tie
	}
	upper, _ := new(big.Rat).SetString(exact[:dot+1+places])
	upper.Add(upper, new(big.Rat).SetFrac64(1, int64(math.Pow10(places))))
	return upper.FloatString(places)
}

// CPUSize prints millicores the way MemorySize prints bytes: in millicores
// (the unit requests and limits are written in) below one core, and in cores
// to one decimal from one core up, matching MemorySize's precision in the same
// row and overview.formatCores' band. Cell.Value still carries millicores for
// sorting.
//
// Exported, with MemorySize, so the Overview's node table spells the same
// readings the same way.
func CPUSize(milli int64) string {
	const core = 1000
	if milli < core {
		return strconv.FormatInt(milli, 10) + "m"
	}
	return fmt.Sprintf("%.1f cores", float64(milli)/core)
}

// MemorySize prints bytes in binary units, the ones a limit is written in
// (`512Mi`). The top band is TiB, the same ceiling as textutil.BytesCompact.
func MemorySize(bytes int64) string {
	const (
		kib = 1 << 10
		mib = 1 << 20
		gib = 1 << 30
		tib = 1 << 40
	)
	switch {
	case bytes >= tib:
		return fmt.Sprintf("%.1f TiB", float64(bytes)/tib)
	case bytes >= gib:
		return fmt.Sprintf("%.1f GiB", float64(bytes)/gib)
	case bytes >= mib:
		return fmt.Sprintf("%d MiB", bytes/mib)
	default:
		// Under a mebibyte: "0 MiB" would read as a broken column.
		return fmt.Sprintf("%d KiB", bytes/kib)
	}
}

// ShortDuration is the one unit that matters and no more: 12s, 4m, 3h, 21d.
// Kubernetes ages are read to answer "recently or not", never to the minute.
func ShortDuration(d time.Duration) string {
	if d < 0 {
		// A future timestamp is clock skew with the API server.
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// keyNames lists the keys of a ConfigMap or a Secret, sorted, and never their
// values.
//
// For a Secret that is the point (see secretRow); a ConfigMap's values can be
// far too large for a cell.
func keyNames[T any](data map[string]T, limit int) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return JoinCapped(keys, limit)
}

// JoinCapped joins with ", " and says how many it left out, rather than
// trailing off into an ellipsis that could mean anything.
func JoinCapped(values []string, limit int) string {
	if len(values) <= limit {
		return strings.Join(values, ", ")
	}
	return strings.Join(values[:limit], ", ") + fmt.Sprintf(" +%d more", len(values)-limit)
}

// Clip shortens free text for a table cell: it flattens whitespace onto one
// line and truncates rune-safely (textutil.Clip). internal/customresources
// calls it for every CRD printer column.
func Clip(s string, limit int) string {
	return textutil.Clip(strings.Join(strings.Fields(s), " "), limit)
}

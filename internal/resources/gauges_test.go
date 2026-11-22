package resources

// The three bars on the Nodes list, and the one thing they must never do.
//
// Every test here is about an *absence*. The arithmetic — used over allocatable,
// times a hundred — is four characters wide and would survive any rewrite; what
// would not survive is the rule that a reading nobody took draws no bar at all.
// An empty track reads as 0%, 0% reads as an idle machine, and on the one
// occasion that matters — a kubelet that has stopped answering — the machine is
// the opposite of idle. An empty answer and a failed answer are different
// answers.

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

// benchmarkable node with the two allocatable figures the gauges divide by.
func gaugeNode(name string, cpuMillis, memoryBytes int64) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(cpuMillis, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(memoryBytes, resource.BinarySI),
			},
		},
	}
}

func nodeTable(t *testing.T, node *corev1.Node, usage map[string]Usage) Table {
	t.Helper()
	table, err := List(context.Background(), accesstest.Allow(), client(node), Query{Kind: "nodes"}, usage)
	if err != nil {
		t.Fatalf("listing nodes: %v", err)
	}
	return table
}

func TestTheNodeGaugesMeasureUsageAgainstWhatTheSchedulerMayHandOut(t *testing.T) {
	table := nodeTable(t, gaugeNode("ip-10-0-1-5", 8000, 16<<30), map[string]Usage{
		"ip-10-0-1-5": {
			CPUMillicores: 2000,
			MemoryBytes:   12 << 30,
			Disk:          &Disk{UsedBytes: 95 << 30, CapacityBytes: 100 << 30},
		},
	})

	for _, c := range []struct {
		column  string
		percent float64
		class   string
		text    string
	}{
		// Banded at one core, the same shape as the memory cell beside it.
		{"CPU", 25, ClassOK, "2.0 cores"},
		{"Memory", 75, ClassWarn, "12.0 GiB"},
		{"Disk", 95, ClassBad, "95.0 GiB"},
	} {
		cell := cellOf(t, table, "ip-10-0-1-5", c.column)
		if cell.Gauge == nil {
			t.Fatalf("%s: no gauge on a measured node, so the operator sees a bare number "+
				"where the column promises a bar", c.column)
		}
		if cell.Gauge.Percent != c.percent {
			t.Errorf("%s: bar is %.1f%% full, want %.1f%% — allocatable is the denominator, "+
				"not capacity", c.column, cell.Gauge.Percent, c.percent)
		}
		if cell.Gauge.Class != c.class {
			t.Errorf("%s: bar is %q at %.0f%%, want %q", c.column, cell.Gauge.Class, c.percent, c.class)
		}
		// The reading survives the decoration. It is what sorts, what a screen
		// reader says and what somebody copies out of the cell.
		if cell.Text != c.text {
			t.Errorf("%s: text = %q, want %q — the bar is drawn over the reading, not "+
				"instead of it", c.column, cell.Text, c.text)
		}
		if cell.Value == 0 {
			t.Errorf("%s: Value is zero, so the column no longer sorts", c.column)
		}
	}
}

// The whole point of the feature, stated as the case that must not regress.
func TestANodeNobodyCouldMeasureDrawsNoBarAtAll(t *testing.T) {
	// No usage map at all: a cluster with no metrics-server and no `nodes/proxy`
	// grant, which is a great many of them.
	table := nodeTable(t, gaugeNode("ip-10-0-1-5", 8000, 16<<30), nil)

	for _, column := range []string{"CPU", "Memory", "Disk"} {
		cell := cellOf(t, table, "ip-10-0-1-5", column)
		if cell.Gauge != nil {
			t.Errorf("%s: a node nothing measured draws a bar %.0f%% full. An empty track "+
				"reads as 0%%, 0%% reads as an idle machine, and a machine whose kubelet "+
				"stopped answering is the opposite of idle", column, cell.Gauge.Percent)
		}
		if cell.Text != Missing {
			t.Errorf("%s: text = %q, want an em dash — a blank cell reads as a rendering "+
				"fault and a zero reads as a measurement", column, cell.Text)
		}
	}
}

// The half that arrives by arithmetic rather than by a missing key: the Nodes
// list merges two independent sources, so a node can be in the usage map because
// its *disk* was read while metrics-server has never heard of it. Nobody writes
// the zero — it is what an int64 field defaults to.
func TestANodeMeasuredOnlyForDiskStillSaysNothingAboutCPU(t *testing.T) {
	table := nodeTable(t, gaugeNode("ip-10-0-1-5", 8000, 16<<30), map[string]Usage{
		"ip-10-0-1-5": {
			ComputeUnmeasured: true,
			Disk:              &Disk{UsedBytes: 40 << 30, CapacityBytes: 100 << 30},
		},
	})

	for _, column := range []string{"CPU", "Memory"} {
		cell := cellOf(t, table, "ip-10-0-1-5", column)
		if cell.Text != Missing {
			t.Errorf("%s = %q on a node the metrics backend never answered for. The entry "+
				"exists because the kubelet described its disk; reading its zeroed CPU field "+
				"as a measurement states a reading nobody took", column, cell.Text)
		}
		if cell.Gauge != nil {
			t.Errorf("%s: a bar was drawn from a figure that was never measured", column)
		}
	}

	disk := cellOf(t, table, "ip-10-0-1-5", "Disk")
	if disk.Gauge == nil || disk.Gauge.Percent != 40 {
		t.Fatalf("Disk gauge = %+v, want a bar 40%% full — the source that *did* answer "+
			"must still be drawn", disk.Gauge)
	}
}

// The kubelet answered about the pods on this node and said nothing about the
// node's own filesystem, which is a different failure from not being asked and
// renders the same way.
func TestANodeWhoseKubeletSaidNothingAboutItsDiskDrawsNoDiskBar(t *testing.T) {
	for name, usage := range map[string]Usage{
		"no disk block at all": {CPUMillicores: 100, MemoryBytes: 1 << 30},
		"a filesystem of no size": {
			CPUMillicores: 100, MemoryBytes: 1 << 30,
			Disk: &Disk{UsedBytes: 0, CapacityBytes: 0},
		},
	} {
		t.Run(name, func(t *testing.T) {
			table := nodeTable(t, gaugeNode("ip-10-0-1-5", 8000, 16<<30),
				map[string]Usage{"ip-10-0-1-5": usage})
			cell := cellOf(t, table, "ip-10-0-1-5", "Disk")
			if cell.Text != Missing || cell.Gauge != nil {
				t.Errorf("Disk = %q with gauge %+v, want an em dash and no bar. A node "+
					"drawn at 0%% disk is a node with room; a node nobody could ask may be "+
					"minutes from evicting every pod on it", cell.Text, cell.Gauge)
			}
		})
	}
}

// A node reporting no allocatable CPU is not a node running at infinity. The
// division has no denominator, so there is no proportion to draw — but the
// reading itself is real and stays.
func TestANodeWithNoAllocatableFiguresKeepsItsReadingAndLosesItsBar(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "ip-10-0-1-5"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
	table := nodeTable(t, node, map[string]Usage{
		"ip-10-0-1-5": {CPUMillicores: 250, MemoryBytes: 1 << 30},
	})

	cpu := cellOf(t, table, "ip-10-0-1-5", "CPU")
	if cpu.Text != "250m" {
		t.Errorf("CPU = %q, want 250m — the figure was measured whatever the node says "+
			"it may hand out", cpu.Text)
	}
	if cpu.Gauge != nil {
		t.Errorf("CPU drew a bar %.0f%% full against an allocatable of nothing; a "+
			"proportion of zero is not a proportion", cpu.Gauge.Percent)
	}
}

// Usage above allocatable is ordinary — the two are sampled at different moments
// — and the bar has nowhere to draw it. It fills; the text still states the
// figure, which is where the operator finds out.
func TestABarCannotDrawPastItsOwnTrack(t *testing.T) {
	table := nodeTable(t, gaugeNode("ip-10-0-1-5", 1000, 1<<30), map[string]Usage{
		"ip-10-0-1-5": {CPUMillicores: 1400, MemoryBytes: 1 << 30},
	})
	cpu := cellOf(t, table, "ip-10-0-1-5", "CPU")
	if cpu.Gauge == nil || cpu.Gauge.Percent != 100 {
		t.Fatalf("gauge = %+v, want a full bar and not 140%%, which would draw outside "+
			"its track", cpu.Gauge)
	}
	if cpu.Text != "1.4 cores" {
		t.Errorf("text = %q, want 1.4 cores — the clamp is the bar's, not the reading's", cpu.Text)
	}
}

// A gauge colors a cell and must not promote its row.
//
// Cell.Class is what makes the template draw a pill and what worstRank reads, so
// the grade lives on the Gauge instead. A node at 91% memory is a well-packed
// machine; sorting it above a NotReady one in a list ordered worst-first says it
// is an incident.
func TestAFullNodeIsNotRankedAsABrokenOne(t *testing.T) {
	table := nodeTable(t, gaugeNode("ip-10-0-1-5", 1000, 1<<30), map[string]Usage{
		"ip-10-0-1-5": {CPUMillicores: 990, MemoryBytes: 1 << 30},
	})
	for _, column := range []string{"CPU", "Memory"} {
		cell := cellOf(t, table, "ip-10-0-1-5", column)
		if cell.Class != "" {
			t.Errorf("%s carries Cell.Class %q, so the template draws it as a pill instead "+
				"of a figure — the grade belongs on Cell.Gauge", column, cell.Class)
		}
		if cell.Rank != 0 {
			t.Errorf("%s claims rank %d, so a busy node climbs above a broken one in the "+
				"default order", column, cell.Rank)
		}
	}
}

// The width goes straight into a CSS declaration, and an unrounded division
// writes eighteen characters of float noise into every gauge cell of every
// refresh: `width:28.249999999999996%`.
func TestAGaugeWidthIsNotEighteenDigitsOfFloatNoise(t *testing.T) {
	// 1130 of 4000 millicores is 28.25%, which has no finite binary form.
	table := nodeTable(t, gaugeNode("ip-10-0-1-5", 4000, 16<<30), map[string]Usage{
		"ip-10-0-1-5": {CPUMillicores: 1130, MemoryBytes: 8 << 30},
	})
	got := cellOf(t, table, "ip-10-0-1-5", "CPU").Gauge
	if got == nil {
		t.Fatal("no gauge")
	}
	if got.Percent != 28.2 && got.Percent != 28.3 {
		t.Errorf("Percent = %v, want it rounded to one decimal. The template writes this "+
			"into a CSS width three times a row on a fragment re-sent every two seconds; "+
			"the extra digits move the bar by a ten-thousandth of a pixel", got.Percent)
	}
	if len(fmt.Sprintf("%v", got.Percent)) > 5 {
		t.Errorf("Percent renders as %q, which is longer than any width needs to be",
			fmt.Sprintf("%v", got.Percent))
	}
}

// The hint is the half the cell does not draw. A gauged cell shows its bar and
// its percentage, and the quantity is in the hover as `used / total` —
// written here, on the server, because Cell.Gauge
// arrives already divided and the client never composes a unit.
func TestAGaugeSaysUsedOverTotalOnHover(t *testing.T) {
	table := nodeTable(t, gaugeNode("ip-10-0-1-5", 8000, 16<<30), map[string]Usage{
		"ip-10-0-1-5": {
			CPUMillicores: 2000,
			MemoryBytes:   8 << 30,
			Disk:          &Disk{UsedBytes: 50 << 30, CapacityBytes: 100 << 30},
		},
	})
	for column, want := range map[string]string{
		"CPU":    "2 / 8 cores allocatable",
		"Memory": "8 / 16 GiB allocatable",
		"Disk":   "50 / 100 GiB on the node's own filesystem, as its kubelet measured it",
	} {
		if got := cellOf(t, table, "ip-10-0-1-5", column).Hint; got != want {
			t.Errorf("%s hint = %q, want %q", column, got, want)
		}
	}
}

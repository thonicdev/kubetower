package resources

// The Used column on the two storage lists, and — as with the node gauges — every
// test here is about an *absence*.
//
// The arithmetic is used over declared, times a hundred: one line, and it would
// survive any rewrite. What would not survive is that a claim nobody measured
// draws **no bar at all**, and that each of the ways to have no figure says which
// one it was. An empty track reads as 0 %, 0 % reads as a claim with room, and a
// claim nobody could ask about may be the one that is full — an empty answer
// and a failed answer are different answers, with a filesystem in it.
//
// Measured on a real cluster where all six kubelets
// answer: nineteen of thirty-one claims still draw no bar, because the
// filesystem the kubelet measured is the NAS export and not the claim. So the
// unmeasured path is not the exceptional one. It is most of the column.

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

func boundClaim(namespace, name, size, volume string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: volume},
		Status: corev1.PersistentVolumeClaimStatus{
			Phase:    corev1.ClaimBound,
			Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)},
		},
	}
}

func claimTable(t *testing.T, pvc *corev1.PersistentVolumeClaim, usage map[string]Usage) Table {
	t.Helper()
	table, err := List(context.Background(), accesstest.Allow(), client(pvc), Query{Kind: "persistentvolumeclaims"}, usage)
	if err != nil {
		t.Fatalf("listing claims: %v", err)
	}
	return table
}

func TestAMeasuredClaimDrawsItsBarAgainstTheSizeItDeclares(t *testing.T) {
	const gib = int64(1) << 30
	table := claimTable(t, boundClaim("store", "db-data", "8Gi", "pv-1"), map[string]Usage{
		"store/db-data": {ComputeUnmeasured: true, Volume: &Volume{
			Measured:      true,
			UsedBytes:     6 * gib,
			CapacityBytes: 8 * gib,
			Note:          "as the kubelet on node-a measured it",
		}},
	})

	cell := cellOf(t, table, "db-data", "Used")
	if cell.Gauge == nil {
		t.Fatalf("no gauge on a measured claim, so the operator sees a bare number "+
			"where the column promises a bar (cell = %+v)", cell)
	}
	if cell.Gauge.Percent != 75 {
		t.Errorf("bar is %.1f%% full, want 75%% — the denominator is the size the "+
			"*claim* declares and never the filesystem the kubelet found, which on a "+
			"shared export is the whole export", cell.Gauge.Percent)
	}
	if cell.Gauge.Class != ClassWarn {
		t.Errorf("bar is %q at 75%%, want %q", cell.Gauge.Class, ClassWarn)
	}
	// The reading survives the decoration: it is what sorts, what a screen reader
	// says, and what somebody reads out of the cell.
	if cell.Text != "6.0 GiB" {
		t.Errorf("text = %q, want %q — the bar is drawn over the reading, not "+
			"instead of it", cell.Text, "6.0 GiB")
	}
	if cell.Value != float64(6*gib) {
		t.Errorf("Value = %v, want the bytes used — sorting by percentage would put a "+
			"90%%-full 1 MiB claim above a 60%%-full terabyte, and the second is the "+
			"one worth reclaiming", cell.Value)
	}
	// The cell draws the bar and its percentage, so the
	// quantity is the hover — `used / total` against the declared size, then who
	// measured it.
	if want := "6 / 8 GiB, as the kubelet on node-a measured it"; cell.Hint != want {
		t.Errorf("hint = %q, want %q — the quantity the cell does not draw", cell.Hint, want)
	}
}

// The same reading one layer up: a PersistentVolume's Used is its claim's, and
// its hover is the same `used / total`.
func TestABoundVolumeSaysUsedOverTotalOnHover(t *testing.T) {
	const gib = int64(1) << 30
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-1"},
		Spec: corev1.PersistentVolumeSpec{
			ClaimRef: &corev1.ObjectReference{Namespace: "store", Name: "db-data"},
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
	}
	table, err := List(context.Background(), accesstest.Allow(), client(pv), Query{Kind: "persistentvolumes"},
		map[string]Usage{"pv-1": {ComputeUnmeasured: true, Volume: &Volume{
			Measured:      true,
			UsedBytes:     3 * gib,
			CapacityBytes: 16 * gib,
			Note:          "as the kubelet on node-b measured it",
		}}})
	if err != nil {
		t.Fatalf("listing volumes: %v", err)
	}
	cell := cellOf(t, table, "pv-1", "Used")
	if cell.Gauge == nil || cell.Gauge.Percent != 18.8 {
		t.Fatalf("gauge = %+v, want a bar at 18.8%% (3 of 16 GiB)", cell.Gauge)
	}
	if want := "3 / 16 GiB, as the kubelet on node-b measured it"; cell.Hint != want {
		t.Errorf("hint = %q, want %q", cell.Hint, want)
	}
}

// Fraction is the one place the server writes the slash, and both halves are
// in the whole's unit, to one precision rule (the chooser's, `fleet.ts`
// figure()). The trailing comments name the two-unit spelling each case avoids.
func TestFractionWritesBothHalvesInTheWholesUnit(t *testing.T) {
	const mib, gib, tib = int64(1) << 20, int64(1) << 30, int64(1) << 40
	for _, c := range []struct{ got, want string }{
		{CPUFraction(368, 8000), "0.37 / 8 cores"},                   // not 368m / 8.0 cores
		{CPUFraction(1163, 4000), "1.2 / 4 cores"},                   // not 1.2 cores / 4.0 cores
		{CPUFraction(8400, 8000), "8.4 / 8 cores"},                   // past the whole: a measurement
		{CPUFraction(0, 8000), "0 / 8 cores"},                        // a real zero
		{CPUFraction(1, 64000), "<0.01 / 64 cores"},                  // never 0, which reads as idle
		{BytesFraction(float64(512*mib), 16*gib), "0.5 / 16 GiB"},    // not 512 MiB / 16.0 GiB
		{BytesFraction(3435973837, 16*gib), "3.2 / 16 GiB"},          // 3.2 GiB
		{BytesFraction(float64(900*gib), 3*tib/2), "0.88 / 1.5 TiB"}, // not 900.0 GiB / 1.5 TiB
		{BytesFraction(float64(100*mib), 512*mib), "100 / 512 MiB"},
		{Fraction(12, 110, ""), "12 / 110"},
		// An exact tie rounds up, as `fleet.ts` figure()'s toFixed does, so the
		// chooser and a list read the same cluster alike.
		{CPUFraction(125, 8000), "0.13 / 8 cores"},
		{Fraction(0.625, 2.25, ""), "0.63 / 2.3"},
		{Fraction(1.25, 1500.25, ""), "1.3 / 1500.3"},
		// And a near-tie that is below the half in binary stays down, as in JS.
		{Fraction(1.005, 0.995, ""), "1 / 0.99"},
		// A series' sample has a fraction, and it is divided as it is, never
		// rounded first: the chooser divides the same value.
		{CPUFraction(124.6, 8000), "0.12 / 8 cores"},
		{CPUFraction(1249.7, 8000), "1.2 / 8 cores"},
		{CPUFraction(0.397, 8000), "<0.01 / 8 cores"},
		// toFixed rounds the magnitude, so a negative tie goes away from zero.
		{Fraction(-0.125, 1, ""), "-0.13 / 1"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

// The four refusals, each of which must draw no bar and say which one it was.
func TestAClaimNobodyMeasuredDrawsNoBarAndSaysWhy(t *testing.T) {
	for name, c := range map[string]struct {
		usage map[string]Usage
		want  []string
	}{
		"nothing in the map at all": {
			nil,
			// The generic sentence, which has to name all three causes: from here
			// the console genuinely cannot tell them apart, and picking one would
			// be a guess presented as a diagnosis.
			[]string{"nodes/proxy", "10250", "mounted"},
		},
		"an entry that is only a reason": {
			map[string]Usage{"store/db-data": {ComputeUnmeasured: true, Volume: &Volume{
				Note: "No running pod has this claim mounted, so no kubelet has measured it.",
			}}},
			[]string{"No running pod"},
		},
		"a shared export": {
			map[string]Usage{"store/db-data": {ComputeUnmeasured: true, Volume: &Volume{
				Note: "Unmeasurable. The kubelet on node-a reports the filesystem under " +
					"this mount as 40.0 TiB, against a claim of 8.0 GiB",
			}}},
			[]string{"40.0 TiB", "8.0 GiB"},
		},
		"a partial answer": {
			map[string]Usage{"store/db-data": {ComputeUnmeasured: true, Volume: &Volume{
				Note: "3 of 40 kubelet(s) answered, and node-a was not among them.",
			}}},
			[]string{"3 of 40"},
		},
		// The zero value is the one that would slip through: `Volume{}` has
		// Measured false and two zero integers, and a renderer reading the
		// integers first would draw a full-width empty bar and call it 0%.
		"a zero-value entry": {
			map[string]Usage{"store/db-data": {ComputeUnmeasured: true, Volume: &Volume{}}},
			nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			table := claimTable(t, boundClaim("store", "db-data", "8Gi", "pv-1"), c.usage)
			cell := cellOf(t, table, "db-data", "Used")

			if cell.Gauge != nil {
				t.Fatalf("a bar (%.1f%%) on a claim nobody measured — an empty track "+
					"reads as 0%%, 0%% reads as a claim with room, and this claim's "+
					"fullness is unknown", cell.Gauge.Percent)
			}
			if cell.Text != Missing {
				t.Errorf("text = %q, want an em dash — a figure here would be a "+
					"measurement nobody took", cell.Text)
			}
			if cell.Hint == "" {
				t.Fatal("no hint: the operator sees an em dash with nothing saying " +
					"which of five different things happened, which is the defect " +
					"this column was built to avoid")
			}
			for _, want := range c.want {
				if !strings.Contains(cell.Hint, want) {
					t.Errorf("hint %q does not mention %q", cell.Hint, want)
				}
			}
		})
	}
}

// A claim that is not bound has not failed to be measured — there is nothing to
// measure, and no permission would change that. Telling somebody to go and look
// at `nodes/proxy` for it would send them after a grant that does nothing.
func TestAnUnboundClaimIsToldApartFromOneNobodyCouldAsk(t *testing.T) {
	pending := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "waiting", Namespace: "store"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
	}
	cell := cellOf(t, claimTable(t, pending, nil), "waiting", "Used")

	if cell.Gauge != nil {
		t.Fatal("a bar on an unbound claim")
	}
	if !strings.Contains(cell.Hint, "not bound") {
		t.Errorf("hint = %q, want it to say the claim is not bound", cell.Hint)
	}
	if strings.Contains(cell.Hint, "nodes/proxy") {
		t.Errorf("hint = %q, which sends the operator after an RBAC grant that would "+
			"change nothing: no kubelet has mounted this claim because none can", cell.Hint)
	}
}

// A PersistentVolume nothing has claimed is the same shape one layer up.
func TestAnUnclaimedVolumeIsToldApartFromOneNobodyCouldAsk(t *testing.T) {
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-spare"},
		Status:     corev1.PersistentVolumeStatus{Phase: corev1.VolumeAvailable},
	}
	table, err := List(context.Background(), accesstest.Allow(), client(pv), Query{Kind: "persistentvolumes"}, nil)
	if err != nil {
		t.Fatalf("listing volumes: %v", err)
	}
	cell := cellOf(t, table, "pv-spare", "Used")

	if cell.Gauge != nil {
		t.Fatal("a bar on a volume nothing has mounted")
	}
	if !strings.Contains(cell.Hint, "Nothing has claimed") {
		t.Errorf("hint = %q, want it to say nothing has claimed this volume", cell.Hint)
	}
	if strings.Contains(cell.Hint, "nodes/proxy") {
		t.Errorf("hint = %q, which blames a permission for a volume that is simply "+
			"not mounted anywhere", cell.Hint)
	}
}

// A width derived from data that has not arrived is a guess — as a property of
// the descriptor rather than of a render. The kubelet's answer arrives after
// the first paint, so a column sized from the em dash that stood in for it
// clips the reading and the bar when they land: measured on the Nodes list at
// 5.5, 6.2 and 13.2 pixels of overflow onto the column beside it.
func TestTheUsedColumnDeclaresThatItDrawsABar(t *testing.T) {
	for _, kind := range []string{"persistentvolumeclaims", "persistentvolumes"} {
		t.Run(kind, func(t *testing.T) {
			for _, c := range ColumnsOf(kind) {
				if c.Title != "Used" {
					continue
				}
				if !c.Gauged {
					t.Fatal("the Used column is not Gauged, so internal/web's " +
						"defaultWidths sizes it for whatever stood in for the reading " +
						"— which on the first paint is an em dash")
				}
				if !c.Numeric {
					t.Error("the Used column is not Numeric, so it sorts as a string " +
						"and \"900 MiB\" lands above \"6.0 GiB\"")
				}
				return
			}
			t.Fatal("no Used column")
		})
	}
}

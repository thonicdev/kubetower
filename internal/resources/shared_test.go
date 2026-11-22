package resources

// The four readings this package owns for the whole console, pinned at the
// case where separate copies would *disagree*. Each of these is a difference an
// operator could see on one screen.

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A node both NotReady and cordoned. Returning on `ready != True` before ever
// looking at the cordon would drop the one fact somebody watching a
// half-finished drain is there to find.
func TestNodeStatusNamesBothTheFaultAndTheCordon(t *testing.T) {
	for name, c := range map[string]struct {
		ready         corev1.ConditionStatus
		unschedulable bool
		want          string
		class         string
	}{
		"ready":               {corev1.ConditionTrue, false, "Ready", ClassOK},
		"cordoned":            {corev1.ConditionTrue, true, "Ready,SchedulingDisabled", ClassWarn},
		"broken":              {corev1.ConditionFalse, false, "NotReady", ClassBad},
		"broken and cordoned": {corev1.ConditionFalse, true, "NotReady,SchedulingDisabled", ClassBad},
		"silent":              {corev1.ConditionUnknown, false, "Unknown", ClassBad},
		"silent and cordoned": {corev1.ConditionUnknown, true, "Unknown,SchedulingDisabled", ClassBad},
	} {
		t.Run(name, func(t *testing.T) {
			n := &corev1.Node{
				Spec: corev1.NodeSpec{Unschedulable: c.unschedulable},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: c.ready}},
				},
			}
			got, class, _ := NodeStatus(n)
			if got != c.want {
				t.Errorf("NodeStatus = %q, want %q — this is what kubectl prints", got, c.want)
			}
			if class != c.class {
				t.Errorf("class = %q, want %q", class, c.class)
			}
		})
	}
}

// Skipping series.lastObservedTime falls through to eventTime — the *first*
// occurrence — so an event that has been firing all night reads as last seen
// when it started. Omitting the creation-time floor dates an event carrying
// none of the legacy stamps to 1970 in a table and, worse, in the archive.
func TestEventTimeReadsWhicheverStampTheWriterFilledIn(t *testing.T) {
	base := time.Date(2026, 8, 20, 3, 0, 0, 0, time.UTC)
	stamp := func(offset time.Duration) metav1.Time { return metav1.NewTime(base.Add(offset)) }

	t.Run("a series event is dated by its last observation", func(t *testing.T) {
		e := &corev1.Event{
			ObjectMeta: metav1.ObjectMeta{CreationTimestamp: stamp(0)},
			EventTime:  metav1.NewMicroTime(base),
			Series: &corev1.EventSeries{
				Count:            42,
				LastObservedTime: metav1.NewMicroTime(base.Add(time.Hour)),
			},
		}
		if got := EventTime(e); !got.Equal(base.Add(time.Hour)) {
			t.Errorf("EventTime = %s, want the last observation %s — an event repeating for an hour "+
				"must not be dated from when it started", got, base.Add(time.Hour))
		}
		if got := EventFirstTime(e); !got.Equal(base) {
			t.Errorf("EventFirstTime = %s, want %s", got, base)
		}
	})

	t.Run("only creation is still not 1970", func(t *testing.T) {
		e := &corev1.Event{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: stamp(0)}}
		if got := EventTime(e); !got.Equal(base) {
			t.Errorf("EventTime = %s, want the creation time %s", got, base)
		}
		if got := EventFirstTime(e); !got.Equal(base) {
			t.Errorf("EventFirstTime = %s, want the creation time %s", got, base)
		}
	})

	t.Run("the legacy stamps still win when they are there", func(t *testing.T) {
		e := &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{CreationTimestamp: stamp(0)},
			FirstTimestamp: stamp(time.Minute),
			LastTimestamp:  stamp(2 * time.Minute),
		}
		if got := EventTime(e); !got.Equal(base.Add(2 * time.Minute)) {
			t.Errorf("EventTime = %s, want LastTimestamp", got)
		}
		if got := EventFirstTime(e); !got.Equal(base.Add(time.Minute)) {
			t.Errorf("EventFirstTime = %s, want FirstTimestamp", got)
		}
	})
}

// A reader that skips the older spelling shows a node labeled the legacy way
// with its role in one place and "<none>" in another. The legacy case carries
// its answer in the *value*.
func TestNodeRolesReadsBothSpellings(t *testing.T) {
	for name, c := range map[string]struct {
		labels map[string]string
		want   []string
	}{
		"modern": {map[string]string{"node-role.kubernetes.io/worker": ""}, []string{"worker"}},
		"legacy": {map[string]string{"kubernetes.io/role": "spot-worker"}, []string{"spot-worker"}},
		"both, sorted": {
			map[string]string{"node-role.kubernetes.io/control-plane": "", "kubernetes.io/role": "master"},
			[]string{"control-plane", "master"},
		},
		"an empty legacy value is not a role":  {map[string]string{"kubernetes.io/role": ""}, nil},
		"an empty modern suffix is not a role": {map[string]string{"node-role.kubernetes.io/": ""}, nil},
		"none at all":                          {map[string]string{"kubernetes.io/os": "linux"}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			got := NodeRoles(c.labels)
			if len(got) != len(c.want) {
				t.Fatalf("NodeRoles = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("NodeRoles = %v, want %v", got, c.want)
				}
			}
		})
	}
}

// Clip flattens because a cell is one line, and truncates in runes because
// `s[:limit] + "…"` cuts a character in half.
func TestClipFlattensAndCutsWholeRunes(t *testing.T) {
	if got := Clip("first line\nsecond line", 40); got != "first line second line" {
		t.Errorf("Clip = %q, want the newline flattened — a table cell is one line", got)
	}
	// Ten Han characters, cut to four: three of them plus the ellipsis, and no
	// broken rune anywhere in it.
	got := Clip("配置配置配置配置配置", 4)
	if got != "配置配"+"…" {
		t.Errorf("Clip = %q, want three whole characters and an ellipsis", got)
	}
	for _, r := range got {
		if r == '�' {
			t.Fatalf("Clip = %q, which contains a replacement character — a rune was cut in half", got)
		}
	}
}

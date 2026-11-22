package overview

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

// The page draws the restarting and warning lists 20 rows at a time, so
// the server has to send more than 20 for a second page to exist, and still
// bound what it sends.
const pageSize = 20

func TestTheRestartingListCarriesMoreThanOnePageAndIsBounded(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	var pods []corev1.Pod
	for i := range restartsShown + 50 {
		pods = append(pods, restartingPod(fmt.Sprintf("pod-%03d", i), int32(i+1),
			terminatedAt("Error", now.Add(-time.Hour))))
	}

	rows, total := restarting(pods, restartsShown, now)

	if len(rows) <= pageSize {
		t.Fatalf("%d restarting rows reach the page; a cap of %d or fewer leaves no second page",
			len(rows), pageSize)
	}
	if len(rows) != restartsShown {
		t.Errorf("%d rows for %d crashing containers, want the cap of %d", len(rows), len(pods), restartsShown)
	}
	if total != len(pods) {
		t.Errorf("total = %d, want every crashing container (%d), not the rows sent", total, len(pods))
	}
	if restartsShown != 200 {
		t.Errorf("restartsShown = %d; the payload bound is 200", restartsShown)
	}
	if rows[0].Restarts != int32(len(pods)) {
		t.Errorf("first row has %d restarts, want the worst (%d): the cap must drop the quietest",
			rows[0].Restarts, len(pods))
	}
}

func TestTheWarningFeedCarriesMoreThanOnePageAndIsBounded(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	var events []corev1.Event
	for i := range maxEvents + 50 {
		events = append(events, corev1.Event{
			ObjectMeta:    metav1.ObjectMeta{Name: fmt.Sprintf("e%03d", i), Namespace: "default"},
			LastTimestamp: metav1.NewTime(now.Add(-time.Duration(i) * time.Minute)),
			Reason:        "BackOff",
			Type:          corev1.EventTypeWarning,
		})
	}

	lines, total := eventLines(events, now, maxEvents)

	if len(lines) <= pageSize {
		t.Fatalf("%d warning lines reach the page; a cap of %d or fewer leaves no second page",
			len(lines), pageSize)
	}
	if len(lines) != maxEvents {
		t.Errorf("%d lines for %d events, want the cap of %d", len(lines), len(events), maxEvents)
	}
	if total != len(events) {
		t.Errorf("total = %d, want every warning (%d), not the lines sent", total, len(events))
	}
	if maxEvents != 200 {
		t.Errorf("maxEvents = %d; the payload bound is 200", maxEvents)
	}
	if !lines[0].When.Equal(now) {
		t.Errorf("first line is dated %v, want the newest (%v)", lines[0].When, now)
	}
}

// The two tests above call `restarting` and `eventLines` directly, so the cap
// they pin is the one the test passes, not the one the page applies - a
// literal at the call sites in Workloads and Cluster would leave them green.
// These two go through the public reads instead, over
// more rows than the cap, and pin both the cut and the total the page is told
// about, so that the pager can say the list was cut rather than end on what
// reads as the last row.

func TestWorkloadsCutsTheRestartingListAtItsCapAndSaysHowManyThereWere(t *testing.T) {
	const crashing = 250
	var objects []runtime.Object
	for i := range crashing {
		p := restartingPod(fmt.Sprintf("pod-%03d", i), int32(i+1),
			terminatedAt("Error", clock.Add(-time.Hour)))
		objects = append(objects, &p)
	}

	out, err := Workloads(context.Background(), accesstest.Allow(), client(objects...), "", nil)
	if err != nil {
		t.Fatalf("Workloads: %v", err)
	}

	if len(out.Restarting) != 200 {
		t.Errorf("Workloads sent %d restarting rows for %d crashing containers, want the cap of 200",
			len(out.Restarting), crashing)
	}
	if out.RestartingTotal != crashing {
		t.Errorf("RestartingTotal = %d, want %d: the page cannot say the list was cut without it",
			out.RestartingTotal, crashing)
	}
	if len(out.Restarting) > 0 && out.Restarting[0].Restarts != crashing {
		t.Errorf("first row has %d restarts, want the worst (%d)", out.Restarting[0].Restarts, crashing)
	}
}

func TestClusterCutsTheWarningFeedAtItsCapAndSaysHowManyThereWere(t *testing.T) {
	const warnings = 250
	objects := []runtime.Object{node("node-1", "4", "16Gi", "110", ready(corev1.ConditionTrue))}
	for i := range warnings {
		objects = append(objects, &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: fmt.Sprintf("e%03d", i), Namespace: "default"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: fmt.Sprintf("pod-%03d", i)},
			LastTimestamp:  metav1.NewTime(clock.Add(-time.Duration(i) * time.Minute)),
			Reason:         "BackOff",
			Type:           corev1.EventTypeWarning,
		})
	}
	// A Normal event is not a warning and is not counted: the total is the
	// feed's, not the cluster's event count.
	objects = append(objects, &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "normal", Namespace: "default"},
		Reason:     "Started",
		Type:       corev1.EventTypeNormal,
	})

	out, err := Cluster(context.Background(), accesstest.Allow(), client(objects...), nil)
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}

	if len(out.Events) != 200 {
		t.Errorf("Cluster sent %d warning lines for %d warnings, want the cap of 200", len(out.Events), warnings)
	}
	if out.EventsTotal != warnings {
		t.Errorf("EventsTotal = %d, want %d: the page cannot say the feed was cut without it",
			out.EventsTotal, warnings)
	}
}

// Below the cap nothing is cut, and the total says so by being the row count.
func TestAListUnderItsCapCarriesItsOwnLengthAsItsTotal(t *testing.T) {
	p := restartingPod("pod-0", 3, terminatedAt("Error", clock.Add(-time.Hour)))
	out, err := Workloads(context.Background(), accesstest.Allow(), client(&p), "", nil)
	if err != nil {
		t.Fatalf("Workloads: %v", err)
	}
	if len(out.Restarting) != 1 || out.RestartingTotal != 1 {
		t.Errorf("%d rows, total %d; want 1 and 1", len(out.Restarting), out.RestartingTotal)
	}
}

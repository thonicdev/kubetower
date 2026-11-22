package overview

// This page's readings agree with the ones beside it. Every case here is
// visible to an operator on one screen: the table and the panel below it must
// not say different things about the same object.

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The one that matters most: the *Trouble* panel exists to name what is wrong,
// and for an OOMKilled pod it must not say "Running, not ready" while the pod
// table beside it says "OOMKilled".
func TestTroublePanelNamesTheFaultRatherThanThePhase(t *testing.T) {
	oomKilled := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "hungry", Namespace: "default"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "app",
				Ready: false,
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					Reason: "OOMKilled", ExitCode: 137,
				}},
			}},
		},
	}
	status, class := podStatus(oomKilled)
	if status != "OOMKilled" {
		t.Errorf("Trouble detail = %q, want %q — the table says OOMKilled, and %q "+
			"is the wrong answer on the one panel whose job is naming the fault",
			status, "OOMKilled", "Running, not ready")
	}
	if class != ClassBad {
		t.Errorf("class = %q, want %q", class, ClassBad)
	}

	evicted := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pushed-off", Namespace: "default"},
		Status:     corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"},
	}
	if status, _ := podStatus(evicted); status != "Evicted" {
		t.Errorf("Trouble detail = %q, want Evicted — %q is the phase, not the reason", status, "Failed")
	}

	// Two init containers, the first finished, the second still going. A naive
	// reading gives "PodInitializing" in red; a pod starting normally is not a
	// fault, and how far through it got is the useful part.
	starting := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "starting", Namespace: "default"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{
				{Name: "one", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}}},
				{Name: "two", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}},
			},
		},
	}
	status, class = podStatus(starting)
	if status != "Init:1/2" {
		t.Errorf("Trouble detail = %q, want Init:1/2", status)
	}
	if class != ClassWarn {
		t.Errorf("class = %q, want %q — a pod still starting is not a failure", class, ClassWarn)
	}
}

// The readiness note this panel adds on top of the shared reading. It is a
// suffix and not a second derivation: this panel lists only unhealthy pods, so a
// pod whose shared status is the bare "Running" is here because a container is
// not ready, and "Running" alone would name nothing.
func TestARunningButUnreadyPodStillSaysSo(t *testing.T) {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "slow", Namespace: "default"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: false, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
		},
	}
	status, class := podStatus(p)
	if status != "Running, not ready" {
		t.Errorf("Trouble detail = %q, want %q", status, "Running, not ready")
	}
	if class != ClassWarn {
		t.Errorf("class = %q, want %q", class, ClassWarn)
	}
}

// The node reading is shared with the Nodes list, so the cordon
// survives a NotReady node here too. This is the page somebody watching a drain
// is looking at.
func TestNodeStatusHereKeepsTheCordonOnABrokenNode(t *testing.T) {
	n := node("draining", "4", "8Gi", "110", ready(corev1.ConditionFalse))
	n.Spec.Unschedulable = true

	status, class := nodeStatus(n)
	if status != "NotReady,SchedulingDisabled" {
		t.Errorf("status = %q, want %q — kubectl says both and so must this", status, "NotReady,SchedulingDisabled")
	}
	if class != ClassBad {
		t.Errorf("class = %q, want %q — the cordon must not soften a fault", class, ClassBad)
	}
}

// A repeating events.k8s.io event is aged from its last observation, not from
// its EventTime, or it would sort below events that are older than it.
func TestEventPanelAgesASeriesEventFromItsLastObservation(t *testing.T) {
	now := time.Date(2026, 8, 20, 4, 0, 0, 0, time.UTC)
	series := corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "repeating", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-3 * time.Hour))},
		EventTime:  metav1.NewMicroTime(now.Add(-3 * time.Hour)),
		Series:     &corev1.EventSeries{Count: 90, LastObservedTime: metav1.NewMicroTime(now.Add(-time.Minute))},
		Reason:     "BackOff",
		Type:       corev1.EventTypeWarning,
	}
	older := corev1.Event{
		ObjectMeta:    metav1.ObjectMeta{Name: "older", Namespace: "default"},
		LastTimestamp: metav1.NewTime(now.Add(-2 * time.Hour)),
		Reason:        "FailedScheduling",
		Type:          corev1.EventTypeWarning,
	}

	lines, _ := eventLines([]corev1.Event{older, series}, now, 10)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if lines[0].Object != "/repeating" && lines[0].Reason != "BackOff" {
		t.Fatalf("newest first put %q on top; the repeating event was last seen a minute ago", lines[0].Reason)
	}
	if lines[0].Age != "1m" {
		t.Errorf("age = %q, want 1m — dated from the first occurrence this read 3h", lines[0].Age)
	}
}

package overview

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/internal/resources"
)

// The stacked bar grades a phase the way the list and the drawer do.
//
// Three graders hold this mapping and they must agree. Unknown is bad in
// `resources.GradePodStatus` and in `internal/detail`, so here too: a pod whose
// node has stopped reporting would otherwise be amber on the dashboard and red
// on the page reached by clicking it. Succeeded is the deliberate exception and
// is asserted as one, because an override nobody wrote down is
// indistinguishable from a disagreement.
func TestTheBarGradesAPhaseTheWayTheRestOfTheConsoleDoes(t *testing.T) {
	for _, c := range []struct {
		phase corev1.PodPhase
		want  string
		why   string
	}{
		{corev1.PodUnknown, ClassBad,
			"a phase of Unknown means the node stopped reporting. This table called it warn " +
				"while the list and the drawer called it bad, so the same pod changed color " +
				"between two pages one click apart"},
		{corev1.PodFailed, ClassBad, "a failed pod is the reason this bar is looked at"},
		{corev1.PodRunning, ClassOK, ""},
		{corev1.PodPending, ClassWarn, "pending is in flight, not broken"},
		{corev1.PodSucceeded, ClassMuted,
			"a finished pod is deliberately not green here — see ClassMuted. If this now " +
				"grades ok, the override was lost when the grading moved"},
	} {
		if got := phaseClass(c.phase); got != c.want {
			t.Errorf("phaseClass(%s) = %q, want %q. %s", c.phase, got, c.want, c.why)
		}
	}
}

// And the same three inputs through the one grader, so the assertion above is
// pinned to `resources.GradePodStatus` rather than to a second table that agrees
// with it today.
func TestTheBarDoesNotKeepItsOwnOpinionOfAPhase(t *testing.T) {
	for _, phase := range []corev1.PodPhase{
		corev1.PodRunning, corev1.PodPending, corev1.PodFailed, corev1.PodUnknown,
	} {
		want, _ := resources.GradePodStatus(string(phase))
		if got := phaseClass(phase); got != want {
			t.Errorf("phaseClass(%s) = %q while resources.GradePodStatus says %q. Two graders "+
				"again, which is how Unknown came to be two colors", phase, got, want)
		}
	}
}

// The color reaches the segment, not only the function.
//
// A test of the grader alone would pass while the bar drew a class of its own.
func TestTheSegmentCarriesTheGradedClass(t *testing.T) {
	pods := []corev1.Pod{
		{Status: corev1.PodStatus{Phase: corev1.PodUnknown}},
		{Status: corev1.PodStatus{Phase: corev1.PodSucceeded}},
	}
	got := map[string]string{}
	for _, seg := range phaseBreakdown(pods) {
		got[seg.Label] = seg.Class
	}
	if got["Unknown"] != ClassBad {
		t.Errorf("the Unknown segment is drawn %q, want %q — %+v", got["Unknown"], ClassBad, got)
	}
	if got["Succeeded"] != ClassMuted {
		t.Errorf("the Succeeded segment is drawn %q, want %q — %+v", got["Succeeded"], ClassMuted, got)
	}
}

package detail

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/internal/resources"
)

// The drawer grades a pod phase through the listing layer's grader, and keeps its
// own answers only for the phases that are not a pod's.
//
// Every copy of this mapping must agree on Unknown — bad in the list, bad here,
// bad in `internal/overview`'s bar — or the same pod is red in the drawer and
// amber on the dashboard it was opened from. The cases below are the ones that
// separate the copies, plus the non-pod phases that are the only reason this
// function exists.
func TestThePhasePillGradesPodPhasesThroughOneGrader(t *testing.T) {
	for _, c := range []struct {
		phase string
		want  string
		why   string
	}{
		{"Unknown", resources.ClassBad,
			"the phase the three copies disagreed about. The API server has lost contact with " +
				"the node; that is not `in flight`"},
		{"Succeeded", resources.ClassOK, ""},
		{"Running", resources.ClassOK, ""},
		{"Pending", resources.ClassWarn, ""},
		{"Failed", resources.ClassBad, ""},

		// Not pod phases at all, and the reason this function is kept rather
		// than deleted: a PersistentVolume's and a Namespace's own words.
		{"Bound", resources.ClassOK, "a PersistentVolumeClaim that found its volume"},
		{"Available", resources.ClassOK, ""},
		{"Active", resources.ClassOK, "a Namespace's ordinary state"},
		{"Released", resources.ClassWarn, ""},
		{"Terminating", resources.ClassWarn,
			"a Namespace really does report this as its phase, and it is the one an operator " +
				"stares at while a finalizer holds it"},
		{"Lost", resources.ClassBad, "a claim whose volume is gone"},

		// A container state reason, never a `status.phase`. An arm for it could
		// never fire, and would read as a case somebody considered and handled.
		{"CrashLoopBackOff", resources.ClassNone,
			"phaseClass is fed raw status.phase and this is a container reason. An arm for it " +
				"here is dead code dressed as diligence"},

		// A CRD's own word. No color rather than a guess.
		{"Reconciling", resources.ClassNone, ""},
		{"", resources.ClassNone, ""},
	} {
		if got := phaseClass(c.phase); got != c.want {
			t.Errorf("phaseClass(%q) = %q, want %q. %s", c.phase, got, c.want, c.why)
		}
	}
}

// Pinned to the grader and not to a second table that agrees with it today.
func TestThePhasePillDoesNotKeepItsOwnOpinionOfAPodPhase(t *testing.T) {
	for _, phase := range []string{"Running", "Pending", "Succeeded", "Failed", "Unknown"} {
		want, _ := resources.GradePodStatus(phase)
		if got := phaseClass(phase); got != want {
			t.Errorf("phaseClass(%q) = %q while resources.GradePodStatus says %q. Two graders "+
				"again, which is how Unknown came to be two colors one click apart", phase, got, want)
		}
	}
}

// The graded class reaches the drawer's Phase fact, not only the function.
//
// Read through Reader.Get, which is the path the drawer takes: a unit test on
// phaseClass would pass with the fact built from something else. Phase Unknown
// is the case the three graders must agree about.
func TestTheDrawerPaintsAnUnknownPhaseTheSameColourAsTheList(t *testing.T) {
	reader, _ := readerFor(t, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "lost-node", "namespace": "prod", "resourceVersion": "1"},
		"status":   map[string]any{"phase": "Unknown"},
	}})

	object, err := reader.Get(t.Context(), "prod", Request{Kind: "Pod", Namespace: "prod", Name: "lost-node"})
	if err != nil {
		t.Fatalf("reading the pod: %v", err)
	}

	var found bool
	for _, f := range object.Facts {
		if f.Label != "Phase" {
			continue
		}
		found = true
		if f.Value != "Unknown" {
			t.Errorf("the Phase fact says %q, want Unknown", f.Value)
		}
		if f.Class != resources.ClassBad {
			t.Errorf("the drawer draws phase Unknown as %q while the list draws it %q. The pod "+
				"changes color between two pages one click apart", f.Class, resources.ClassBad)
		}
		t.Logf("Reader.Get returned the Phase fact %+v", f)
	}
	if !found {
		t.Error("the object carries no Phase fact at all, so this test proves nothing about its color")
	}
}

package web

import (
	"context"
	"testing"

	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
)

// The change carried as fields, and the two flags a sentence cannot hold.
//
// # What this guards
//
// `recap` is declared by the contract, drawn by `PlanDialog`, and must be
// produced by this layer. Flattening the recap into one warning:
//
//	facts.Warnings = []string{recap.Field + ": " + recap.Before + " → " + recap.After}
//
// reads correctly and loses both of the things the structure exists for:
//
//   - **`unchanged`** — uncordoning a node that is already schedulable is a
//     no-op. The flattened form says `spec.unschedulable: false → false`, and a
//     confirmation that promises a change is wrong about it. That is worse than
//     silence, because it is the sentence the reader believes.
//   - **`absent`** — a field not written on the object and the same field
//     written `false` are indistinguishable to an API server and different
//     things to read before acting on a production node.
//
// `internal/actions` computes both. This asserts they survive
// the mapping.
func TestTheRecapCarriesBothFlagsToTheWire(t *testing.T) {
	for _, one := range []struct {
		name  string
		given actions.Recap
		want  func(t *testing.T, got *api.PlanRecap)
	}{{
		name:  "a field that is not written on the object",
		given: actions.Recap{Field: "spec.unschedulable", Before: "false", After: "true", Absent: true},
		want: func(t *testing.T, got *api.PlanRecap) {
			if !got.Absent {
				t.Error("absent was dropped; the client then cannot say the field is not set")
			}
			if got.Before != "false" {
				t.Errorf("before = %q, want the value in effect", got.Before)
			}
		},
	}, {
		name:  "a change that changes nothing",
		given: actions.Recap{Field: "spec.unschedulable", Before: "false", After: "false", Unchanged: true},
		want: func(t *testing.T, got *api.PlanRecap) {
			if !got.Unchanged {
				t.Error("unchanged was dropped; the dialog then promises a change that will not happen")
			}
		},
	}, {
		name:  "a verb with no field at all",
		given: actions.Recap{Note: "There is no before and after to show: the object is removed."},
		want: func(t *testing.T, got *api.PlanRecap) {
			if got.Note == "" {
				t.Error("the note was dropped; a delete then has nothing to say for itself")
			}
			if got.Field != "" {
				t.Errorf("field = %q on a verb that changes none", got.Field)
			}
		},
	}} {
		t.Run(one.name, func(t *testing.T) {
			got := recapOf(one.given)
			if got == nil {
				t.Fatal("nothing was carried")
			}
			one.want(t, got)
		})
	}
}

// And a recap with nothing in it is absent rather than an empty object: a client
// drawing `{}` would render a heading over nothing.
func TestAnEmptyRecapIsNotCarried(t *testing.T) {
	if got := recapOf(actions.Recap{}); got != nil {
		t.Fatalf("an empty recap was carried as %+v", got)
	}
}

// A Delete's own recap, asserted at the composition rather than at recapOf's
// door — because the door is where it is already asserted, with a shape nothing
// produces.
//
// # What the case above does not cover
//
// TestAnEmptyRecapIsNotCarried passes `actions.Recap{}`: the zero value, built
// here with a literal. A test that builds the
// input itself keeps passing whatever the producer does.
//
// What a delete actually produces is not the zero value. `preview.go` answers
// every verb with no single field to show as
//
//	Recap{Note: previewNote(req), ResourceVersion: version}
//
// and Delete's arm of `previewNote` is empty, so the real shape is an
// empty note, no field, **and a resourceVersion that is set**. `Stated()` reads
// `r.Field != ""` alone, so that recap maps to nil - and nothing would
// fail if somebody made recapOf carry a recap whenever a version is set, which
// is the plausible edit precisely because the version is sitting in the struct.
//
// # Why the two assertions are one case
//
// `facts.Recap` nil and `facts.Precondition` filled, from one `Recap`, on
// adjacent lines of PreviewAction. The pairing is the assertion: it is what
// distinguishes *there is nothing to show* from *nothing was read*. A nil recap
// on its own is also what a preview that read nothing would produce, and that
// case must not look like this one.
func TestADeleteCarriesNoRecapAndStillCarriesItsPrecondition(t *testing.T) {
	h := newActsHarness(t, fakePod("argo", "api-0"))

	facts, err := h.acts.PreviewAction(context.Background(), "prod", api.ActionRequest{
		Verb:   string(actions.Delete),
		Target: api.PlanTarget{Kind: "pods", Namespace: "argo", Name: "api-0"},
	})
	if err != nil {
		t.Fatalf("previewing a delete on a pod: %v", err)
	}

	if facts.Recap != nil {
		t.Errorf("a delete's facts carry recap %+v. A recap with neither a field nor a note is "+
			"absent rather than an empty object — the comment above recapOf has the reason, and "+
			"the client draws a heading over nothing. The version is not one: it travels as the "+
			"precondition, which is asserted below", facts.Recap)
	}
	// 100 is fakePod's resourceVersion. Asserted by value rather than as
	// non-empty: a precondition is a version the act is bound to, and "some
	// string arrived" is not that.
	if facts.Precondition != "100" {
		t.Errorf("the plan states precondition %q, and the pod it was read from is at 100. The "+
			"recap above is nil because it has nothing to *show*, never because nothing was "+
			"read — an act with no version stated is one with none carried", facts.Precondition)
	}
}

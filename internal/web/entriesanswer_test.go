package web

// The entries half of the Changes panel gets its own state and reason, so an
// object that records no history is not an object that has not changed.
//
// `Changes.tsx`'s own doc comment says `entriesState` exists precisely so that
// *the store is off*, *the window holds nothing* and *it could not be read*
// stay three sentences. The contract declares the member, the client branches
// on it and `internal/changes` composes the sentence in `noteFor`; the mapper
// in between must carry them, or the branch is unreachable and every emptiness
// draws "Nothing here".
//
// Two answers here and three there, and the asymmetry is the point: a failed read
// of the object itself fails the whole panel and travels on the envelope, because
// there is no half of this left to draw. `rolloutsAnswer` has a third arm because
// its read is a second one, against a different resource, that can fail alone.

import (
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/changes"
)

// noManagedFields is `noteFor`'s own sentence, quoted so this test fails if that
// producer stops composing one - which is the state in which the panel would go
// back to saying "nothing here" and nothing would notice.
const noManagedFields = "This object carries no managedFields. Objects created before " +
	"server-side apply, or by a controller that does not set them, record no history."

func TestAnObjectThatRecordsNoHistoryIsAbsentAndNotEmpty(t *testing.T) {
	state, reason := entriesAnswer(changes.Report{Note: noManagedFields})

	// The whole finding in one assertion: `empty` here is the console telling an
	// operator nothing has changed, about an object that has never been able to
	// say whether anything changed.
	if state == api.StateEmpty || state == api.StateOK {
		t.Fatalf("state = %q; an object with no managedFields must not share an arm with one that has changed nothing", state)
	}
	if state != api.StateAbsent {
		t.Errorf("state = %q, want %q", state, api.StateAbsent)
	}
	if reason == nil {
		t.Fatalf("reason = nil; the point is that the panel says which emptiness it means")
	}
	if reason.Code != api.CodeNotSupportedHere {
		t.Errorf("code = %q, want %q", reason.Code, api.CodeNotSupportedHere)
	}
	// The producer's sentence, carried rather than re-composed here: two wordings
	// of one fact is how two panels come to describe it two ways.
	if reason.Message != noManagedFields {
		t.Errorf("message = %q, want `internal/changes`'s own sentence verbatim", reason.Message)
	}
}

func TestAnObjectThatRecordsChangesAndHasNoneIsEmpty(t *testing.T) {
	state, reason := entriesAnswer(changes.Report{})
	if state != api.StateEmpty {
		t.Errorf("state = %q, want %q", state, api.StateEmpty)
	}
	if reason != nil {
		t.Errorf("reason = %+v, want none: there is nothing to explain", reason)
	}
}

func TestAnObjectWithChangesIsOK(t *testing.T) {
	state, reason := entriesAnswer(changes.Report{
		Changes: []changes.Change{{At: time.Now(), Manager: "kubectl", Operation: "Update"}},
	})
	if state != api.StateOK || reason != nil {
		t.Errorf("entriesAnswer = %q, %+v; want ok with no reason", state, reason)
	}
}

// And the mapper actually carries it. The unit above can be right while the
// panel stays empty if nothing reads `noteFor`'s sentence.
func TestTheChangesPanelSaysWhichEmptinessItMeans(t *testing.T) {
	panel := api.ChangesPanel{}
	report := changes.Report{Note: noManagedFields}
	panel.EntriesState, panel.EntriesReason = entriesAnswer(report)

	if panel.EntriesState != api.StateAbsent || panel.EntriesReason == nil {
		t.Fatalf("panel = %+v; the member the contract declares has to reach the panel struct", panel)
	}
	// `omitempty` on a `State`, so an unset one is absent from the JSON and the
	// client's `state === undefined` arm folds to `empty`. That fold is correct
	// only while a filled state is genuinely non-empty.
	if panel.EntriesState == "" {
		t.Error("an empty state is omitted from the JSON, which is the absence the client reads as `empty`")
	}
}

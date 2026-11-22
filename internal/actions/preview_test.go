package actions

import (
	"context"
	"testing"
)

// --- what the note-only verbs say, and the one that says nothing -----------------

// `previewNote`'s Delete arm is empty, and this pins it.
//
// `internal/web/recap_test.go` looks like it covers this and does not — it
// builds an `actions.Recap` with a sentence as a literal and asserts `recapOf`
// carries it through, which pins `recapOf` and is independent of what
// `previewNote` returns.
//
// The assertion is an empty string, and an empty string with no reason beside it
// is the first thing a future reader deletes. The reason: a delete has no before
// and after, a single target is not listed on the dialog, and "the object is
// removed" is the opening clause of the verb's own explanation on the dialog. An
// absent note says exactly what an honest one would, and `recapOf` draws no
// frame at all for it.
func TestPreviewOfADeleteSaysNothing(t *testing.T) {
	h := newHarness(t, nil, pod("prod", "api-1"))

	recap, err := h.runner.Preview(context.Background(), Request{
		Target: target("pods", "prod", "api-1"), Verb: Delete,
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if recap.Stated() {
		t.Fatalf("a delete previewed a field change: %q %q -> %q",
			recap.Field, recap.Before, recap.After)
	}
	if recap.Note != "" {
		t.Errorf("the preview of a delete carries a note: %q\n"+
			"It is empty deliberately: a single target is not listed on the dialog, and "+
			"\"the object is removed\" is the first line of the dialog's own "+
			"explanation. Read the comment above previewNote before restoring it", recap.Note)
	}
	// The read still happened — the version is the precondition the act carries —
	// so an empty note is silence about the change and not a preview that skipped
	// the object.
	if recap.ResourceVersion == "" {
		t.Error("the preview carries no resourceVersion, so it did not read the object")
	}
	h.noWrites()
}

// And the counter-half, which is what keeps the test above from being read as a
// rule about note-only verbs.
//
// Restart's and Evict's notes stay although they overlap their explanations
// too: neither is *false*. A test asserting "every arm with no field is empty"
// would pin the opposite of the intent, and would go green on a change
// nobody agreed to. Delete is empty because it has nothing true left to say —
// not because a verb with no field to show has nothing to say.
func TestTheOtherNoteOnlyVerbsStillExplainThemselves(t *testing.T) {
	for _, c := range []struct {
		what    string
		fixture func() *harness
		request Request
	}{
		{"restart", func() *harness { return newHarness(t, nil, deployment("prod", "api")) },
			Request{Target: target("deployments", "prod", "api"), Verb: Restart}},
		{"evict", func() *harness { return newHarness(t, nil, pod("prod", "api-1")) },
			Request{Target: target("pods", "prod", "api-1"), Verb: Evict}},
	} {
		t.Run(c.what, func(t *testing.T) {
			h := c.fixture()
			recap, err := h.runner.Preview(context.Background(), c.request)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			if recap.Stated() {
				t.Fatalf("a %s previewed a field change: %q", c.what, recap.Field)
			}
			if recap.Note == "" {
				t.Errorf("the preview of a %s carries no note. Only Delete's arm is empty; "+
					"this one overlaps its explanation and is kept on purpose, so "+
					"emptying it is a decision to take rather than a tidy-up", c.what)
			}
			h.noWrites()
		})
	}
}

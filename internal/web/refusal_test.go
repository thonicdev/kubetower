package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thonicdev/kubetower/internal/edit"
)

// One answer for a name that is not a context, and the tests that hold it.
//
// The convention: a refusal has two readers, and each must get something.
// Handlers that answer 404 and render nothing, or 200 and render the reason, give
// **one of the two readers of a refusal nothing at all.**
//
// The two readers want different things and both are legitimate:
//
//   - a `curl`, a monitor, or a script wants the **status**.
//     A 200 is a claim that the thing asked for happened, and a replayed POST
//     against a context that has left the kubeconfig must not read as a cluster
//     having been hidden.
//   - the browser wants the **sentence**, and a status cannot carry it to one:
//     htmx's default responseHandling is `{code:"[45]..",swap:false,error:true}`,
//     so the body of a 4xx is discarded. Nothing swaps, nothing is drawn, nothing
//     is logged — the drag, the click or the two-second refresh simply appears not
//     to have happened, and the person repeats it.
//
// They are not exclusive, and that is what closes the finding rather than trading
// one reader off against the other. htmx fires `htmx:beforeSwap` before it decides
// what to do with a response, and a listener may set `shouldSwap`. So:
//
//	Resolve fails                        → 4xx, and the body is a rendered fragment
//	                                       marked with RefusalRenderedHeader
//	Resolve succeeds, cluster won't answer → 200 with the reason (a state; it changes)
//	The change happened but was not saved  → 200 (it happened) plus what will be lost
//
// And the clause that is about a hand rather than a header: **a fragment renders
// the reason instead of the control, never beside it.** /fragments/action must not draw a
// fully-formed modal headed "Delete pod api-1" with "unknown cluster" in gray
// underneath. Nothing on it could act — `modal()` does not arm an unwritable
// target — but that is not what a hand reads off a screen.

// --- the two helpers that must not become one -----------------------------------

// refuseWith renders nothing and must never claim it did.
//
// The marker header is what tells the browser to swap a body it would otherwise
// discard. On a response whose body is `http.Error`'s plain text, that would put a
// bare sentence — with no id and no markup — wherever the request that failed
// happened to be pointing. The two helpers exist to keep those apart, and this is
// the assertion that stops them merging.
func TestAPlainRefusalIsNotMarkedAsSwappable(t *testing.T) {
	rec := httptest.NewRecorder()
	refuseWith(rec, RefusalUnknownCluster, "there is no context called x", http.StatusNotFound)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get(RefusalHeader); got != RefusalUnknownCluster {
		t.Errorf("%s = %q, want %q", RefusalHeader, got, RefusalUnknownCluster)
	}
	if got := rec.Header().Get(RefusalRenderedHeader); got != "" {
		t.Errorf("%s = %q on a plain-text refusal. htmx would swap `http.Error`'s sentence "+
			"into whichever target asked, losing that element's id and everything on it.",
			RefusalRenderedHeader, got)
	}
}

// --- the vocabulary --------------------------------------------------------------

// An unclassified refusal must not claim to be a classified one.
//
// `edit.RefusedInput` is the zero value of its enum and refusalName's fallthrough
// must not return its word: a refusal carrying no kind at all — a future kind nobody
// added a case for, a zero-valued Refusal built by mistake — would otherwise be
// reported to everything reading this header as a specific, confident cause. A
// header that is sometimes a guess is a header nobody can act on.
func TestAnUnclassifiedRefusalSaysSo(t *testing.T) {
	// A kind past the end of the enum: what a future RefusedSomething looks like
	// before somebody remembers to add a case for it here.
	if got := refusalName(edit.RefusalKind(99)); got != RefusalUnclassified {
		t.Errorf("refusalName(unknown kind) = %q, want %q. Reporting a named cause for a "+
			"refusal nobody classified makes a specific claim that was never determined.",
			got, RefusalUnclassified)
	}
	// And the classified one still says its own word, so the fix above is not the
	// vocabulary collapsing onto one value.
	if got := refusalName(edit.RefusedInput); got != RefusalInput {
		t.Errorf("refusalName(RefusedInput) = %q, want %q", got, RefusalInput)
	}
}

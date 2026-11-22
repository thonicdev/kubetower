package web

import (
	"errors"
	"fmt"
	"testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/edit"
)

// TestARequestTheGuardHeldBackIsNeitherAForbiddenNorABadRequest: the access
// guard's refusal is a defect in the console. A write answers it as a 500 -
// no sentinel, which the router turns into an internal problem - and never as
// the cluster's 403 (ErrRefused) or the caller's 400 (ErrInvalid), raw or
// classified by internal/edit. An enveloped read says `unclassified`, never
// `forbidden`.
func TestARequestTheGuardHeldBackIsNeitherAForbiddenNorABadRequest(t *testing.T) {
	raw := fmt.Errorf(`Delete "https://cluster/api/v1/namespaces/shop/configmaps/settings": %w`,
		&access.Unreviewed{Cluster: "prod", Method: "DELETE", Path: "/api/v1/namespaces/shop/configmaps/settings"})
	classified := edit.Refuse(edit.RefusedUnsent, "the console did not send delete configmaps settings")

	for name, err := range map[string]error{"raw": raw, "classified": classified} {
		mapped := writeError(err)
		for _, sentinel := range []error{api.ErrRefused, api.ErrInvalid, api.ErrReadOnly, api.ErrUnreviewable,
			api.ErrConflict, api.ErrNotFound} {
			if errors.Is(mapped, sentinel) {
				t.Errorf("%s: a held-back write maps to %v, want no sentinel (a 500)", name, sentinel)
			}
		}
		failure := stateOf(err, api.Scope{Cluster: "prod"})
		if failure == nil || failure.State == api.StateForbidden || failure.Reason.Code != api.CodeUnclassified {
			t.Errorf("%s: a held-back request reads as %+v, want unclassified and not forbidden", name, failure)
		}
	}
	if got := refusalName(edit.RefusedUnsent); got != "unsent" {
		t.Errorf("refusalName(RefusedUnsent) = %q, want %q", got, "unsent")
	}
}

// A write refused because its audit record could not be written reaches the
// contract as its own sentinel and refusal word, and an enveloped answer as
// the retryable "could not act" code - never as a denial.
func TestAnUnrecordedWriteIsItsOwnRefusal(t *testing.T) {
	refused := edit.Refuse(edit.RefusedUnrecorded, "the console did not delete pods api-1: it could not write the audit record of it first")
	if mapped := writeError(refused); !errors.Is(mapped, api.ErrUnrecorded) || errors.Is(mapped, api.ErrRefused) {
		t.Errorf("an unrecorded write maps to %v", mapped)
	}
	failure := stateOf(refused, api.Scope{Cluster: "prod"})
	if failure == nil || failure.Reason.Code != api.CodeAccessUnreviewable || failure.State == api.StateForbidden {
		t.Errorf("an unrecorded write reads as %+v", failure)
	}
	if got := refusalName(edit.RefusedUnrecorded); got != "unrecorded" {
		t.Errorf("refusalName(RefusedUnrecorded) = %q", got)
	}
}

// A write that never left the console reaches the contract as its own
// sentinel - never ErrRefused, the 403 the Evict button would answer it with - and
// a bulk row as `not_probed_yet`, the code a drain's unsent eviction carries,
// never `verb_denied`.
func TestAWriteNeverSentIsNotARefusal(t *testing.T) {
	refused := edit.RefuseFrom(&edit.NotSent{At: edit.HeldBeforeTransport, Err: errors.New("rate")},
		edit.RefusedNotSent, "The eviction was not sent")
	if mapped := writeError(refused); !errors.Is(mapped, api.ErrNotSent) || errors.Is(mapped, api.ErrRefused) {
		t.Errorf("a write never sent maps to %v", mapped)
	}
	if failure := stateOf(refused, api.Scope{Cluster: "prod"}); failure == nil || failure.State == api.StateForbidden {
		t.Errorf("a write never sent reads as %+v", failure)
	}
	if got := refusalName(edit.RefusedNotSent); got != "not-sent" {
		t.Errorf("refusalName(RefusedNotSent) = %q", got)
	}
}

// One sent with no answer back is its own sentinel too - a 504, never the 403
// it would be answered with if a dropped connection read as the cluster's no.
func TestAWriteNeverAnsweredIsNotARefusal(t *testing.T) {
	refused := edit.RefuseFrom(&edit.Unanswered{Err: errors.New("EOF")}, edit.RefusedUnanswered,
		"The eviction was sent and no answer came back")
	if mapped := writeError(refused); !errors.Is(mapped, api.ErrUnanswered) || errors.Is(mapped, api.ErrRefused) {
		t.Errorf("a write never answered maps to %v", mapped)
	}
	if failure := stateOf(refused, api.Scope{Cluster: "prod"}); failure == nil || failure.State == api.StateForbidden ||
		failure.Reason.Code != api.CodeTimeout {
		t.Errorf("a write never answered reads as %+v", failure)
	}
	if got := refusalName(edit.RefusedUnanswered); got != "unanswered" {
		t.Errorf("refusalName(RefusedUnanswered) = %q", got)
	}
}

package web

import (
	"fmt"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/resources"
)

// TestAListTheConsoleDeclinedToNarrowIsNotRetryable: the answer is the
// console's own ceiling, `not_supported_here` and not retryable, so the list
// page asks once instead of repeating the whole read every two seconds, and
// not `access_unreviewable`, which would name a cause that did not happen.
func TestAListTheConsoleDeclinedToNarrowIsNotRetryable(t *testing.T) {
	failure := stateOf(&access.NotNarrowed{Why: "this list spans 412 namespaces, more than the 100"}, api.Scope{})
	if failure == nil {
		t.Fatal("no failure")
	}
	if failure.State != api.StateUnreadable || failure.Reason.Code != api.CodeNotSupportedHere || (failure.Reason.Retryable != nil && *failure.Reason.Retryable) {
		t.Errorf("answered %+v, want unreadable / not_supported_here / not retryable", failure)
	}
}

// TestAListPastTheConsolesCeilingIsNotRetryable has the same
// shape: a list Paged stopped at resources.MaxRows is the console's own
// ceiling, so the list page asks once rather than reading a hundred pages
// every two seconds to fail again.
func TestAListPastTheConsolesCeilingIsNotRetryable(t *testing.T) {
	failure := stateOf(fmt.Errorf("the list of secrets: %w", &resources.TooLarge{}), api.Scope{})
	if failure == nil {
		t.Fatal("no failure")
	}
	if failure.State != api.StateUnreadable || failure.Reason.Code != api.CodeNotSupportedHere || (failure.Reason.Retryable != nil && *failure.Reason.Retryable) {
		t.Errorf("answered %+v, want unreadable / not_supported_here / not retryable", failure)
	}
	if !strings.Contains(failure.Reason.Message, "choose a namespace") {
		t.Errorf("the sentence %q does not say what to do", failure.Reason.Message)
	}
}

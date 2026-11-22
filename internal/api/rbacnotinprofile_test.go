package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An RBAC write under the server profile is a 403 with its own code, never
// `rbac_denied`: nobody's rights said no. The detail is the sentence the
// console shows.
func TestAnRBACWriteNotInThisProfileIsItsOwn403(t *testing.T) {
	rec := httptest.NewRecorder()
	fail(rec, fmt.Errorf("%w, so this console did not delete rolebindings.rbac.authorization.k8s.io admin in namespace shop",
		ErrRBACNotInProfile))
	var problem Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("the body is not a problem: %s", rec.Body.String())
	}
	if rec.Code != http.StatusForbidden || problem.Code != ProblemRBACNotInProfile {
		t.Errorf("answered %d %s, want 403 rbac_not_in_profile", rec.Code, problem.Code)
	}
	if !strings.HasPrefix(problem.Detail, "RBAC changes are not available in the server profile in this version") {
		t.Errorf("detail %q", problem.Detail)
	}
}

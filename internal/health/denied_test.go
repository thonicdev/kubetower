package health

import (
	"errors"
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Denied is the one opinion on 401-versus-403 that this package and
// internal/web share, so its contract is pinned here rather than inferred from
// the two callers' tests: the typed status through any wrapping, the words only
// where no status exists, and unauthorized before forbidden in both halves.
func TestDeniedTellsTheIdentityFromTheRequest(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "",
		errors.New(`User "dev" cannot list resource "pods"`))

	for name, c := range map[string]struct {
		err  error
		want Denial
	}{
		"nothing":                  {nil, DenialNone},
		"a 401":                    {apierrors.NewUnauthorized("Unauthorized"), DenialUnauthorized},
		"a 401, wrapped by a read": {fmt.Errorf("listing pods: %w", apierrors.NewUnauthorized("Unauthorized")), DenialUnauthorized},
		"a 403":                    {forbidden, DenialForbidden},
		"a 403, wrapped by a read": {fmt.Errorf("listing pods: %w", forbidden), DenialForbidden},
		// The 401 that never reaches a status: the exec plugin refused inside
		// client-go, and kubectl's own sentence is all that comes back.
		"kubectl's words for a 401, no status":        {errors.New("You must be logged in to the server (Unauthorized)"), DenialUnauthorized},
		"the API server's words for a 403, no status": {errors.New(`pods is forbidden: User "dev" cannot list resource "pods"`), DenialForbidden},
		// The word must be in the failure, not in the address it was sent to.
		"a URL is not a refusal":                        {errors.New(`Get "https://x/api/v1/forbidden?timeout=32s": dial tcp: i/o timeout`), DenialNone},
		"both words is the session, not the permission": {errors.New("Unauthorized: forbidden until the session is renewed"), DenialUnauthorized},
		"a status that is neither":                      {apierrors.NewServiceUnavailable("upstream closed"), DenialNone},
		"text that is neither":                          {errors.New("the flux capacitor has come loose"), DenialNone},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Denied(c.err); got != c.want {
				t.Errorf("Denied(%v) = %d, want %d — a 401 is the operator's ten seconds and a 403 "+
					"is somebody else's ticket; the two must never be confused", c.err, got, c.want)
			}
		})
	}
}

package access_test

import (
	"errors"
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
)

// TestARefusalAnswersTheContractsSentinels: an extra cannot name
// *access.Refused, so the test it is given is errors.Is against
// extension.ErrDenied and extension.ErrUnreviewable. Each refusal answers its
// own and not the other, wrapped as a Host door wraps it, and a denial is also
// forbidden to apierrors while a review that could not be made is not.
func TestARefusalAnswersTheContractsSentinels(t *testing.T) {
	list := access.Attributes{Verb: "list", Version: "v1", Resource: "configmaps", Namespace: "shop"}
	for _, tc := range []struct {
		decision  access.Decision
		want      error
		notWant   error
		forbidden bool
	}{
		{access.Denied, extension.ErrDenied, extension.ErrUnreviewable, true},
		{access.Unreviewable, extension.ErrUnreviewable, extension.ErrDenied, false},
	} {
		err := fmt.Errorf("reading: %w", &access.Refused{Cluster: "kind", Attributes: list,
			Answer: access.Answer{Decision: tc.decision, Subject: access.Person}})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: errors.Is(%v, %v) is false", tc.decision, err, tc.want)
		}
		if errors.Is(err, tc.notWant) {
			t.Errorf("%s: errors.Is(%v, %v) is true", tc.decision, err, tc.notWant)
		}
		if got := apierrors.IsForbidden(err); got != tc.forbidden {
			t.Errorf("%s: apierrors.IsForbidden = %t, want %t", tc.decision, got, tc.forbidden)
		}
	}
}

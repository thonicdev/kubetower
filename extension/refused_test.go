package extension_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/thonicdev/kubetower/extension"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestRefusedReadSortsTheTwoRefusalsFromAFailure: a denial is a 403 saying the
// page's sentence, a review that could not be made a 503 saying the console's,
// wrapped or not; a failed read, an unknown cluster and the console's own 403
// are the caller's.
func TestRefusedReadSortsTheTwoRefusalsFromAFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		ok      bool
		status  int
		code    string
		message string
	}{
		{"denied", fmt.Errorf("listing: %w", extension.ErrDenied), true,
			http.StatusForbidden, extension.CodeListForbidden, "You may not list CronJobs here."},
		{"unreviewable", fmt.Errorf("listing: %w", extension.ErrUnreviewable), true,
			http.StatusServiceUnavailable, extension.CodeListUnreviewed, extension.UnreviewedText},
		{"failed", errors.New("connection refused"), false, 0, "", ""},
		{"unknown cluster", fmt.Errorf("x: %w", extension.ErrUnknownCluster), false, 0, "", ""},
		{"console's 403", apierrors.NewForbidden(schema.GroupResource{Resource: "cronjobs"}, "", errors.New("no")),
			false, 0, "", ""},
		{"nil", nil, false, 0, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := extension.RefusedRead(tc.err, "You may not list CronJobs here.")
			if ok != tc.ok || got.Status != tc.status || got.Code != tc.code || got.Message != tc.message {
				t.Errorf("RefusedRead = %+v, %v; want {%d %s %q}, %v", got, ok, tc.status, tc.code, tc.message, tc.ok)
			}
		})
	}
}

package logs

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// A Kubernetes error is classified by its status, not by its prose.
//
// Sniffing error text with separate needles and fallbacks here and in
// internal/forward's namesake would let the same 403 leave here as a 403 and
// leave the port-forward endpoint as a 502. The text matching is
// still here and still needed: "unknown cluster" and "a container name must be
// specified" are sentences this package writes itself, and they have no status
// to read.
//
// The cases below are typed errors whose message says none of the words, which
// is the half that text matching lets fall through to 502 — a bad gateway is "the cluster
// would not answer", and a 403 is the cluster answering very clearly.
func TestATypedErrorIsClassifiedByItsStatus(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		want int
	}{
		"a 404 whose sentence says nothing": {
			&apierrors.StatusError{ErrStatus: metav1.Status{
				Code: 404, Reason: metav1.StatusReasonNotFound, Message: "gone"}},
			http.StatusNotFound},
		"a 403 whose sentence says nothing": {
			&apierrors.StatusError{ErrStatus: metav1.Status{
				Code: 403, Reason: metav1.StatusReasonForbidden, Message: "rejected by policy 7"}},
			http.StatusForbidden},
		"a 400 whose sentence says nothing": {
			apierrors.NewBadRequest("malformed"), http.StatusBadRequest},
		"a wrapped 404, because client-go wraps": {
			fmt.Errorf("reading the container list: %w",
				apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "api-7d9f")),
			http.StatusNotFound},

		// And this package's own sentences, which are not API errors at all and are
		// the reason the text matching stays.
		"our own unknown cluster":   {errors.New("unknown cluster"), http.StatusBadRequest},
		"our own container refusal": {errors.New("a container name must be specified"), http.StatusBadRequest},
		"something with no status":  {errors.New("the flux capacitor has come loose"), http.StatusBadGateway},
	} {
		t.Run(name, func(t *testing.T) {
			if got := statusFor(c.err); got != c.want {
				t.Errorf("statusFor(%v) = %d, want %d", c.err, got, c.want)
			}
		})
	}
}

package web

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/overview"
)

// A metrics line whose query failed carries the error, and its code is the one
// stateOf gives every other read, so the pod panel and the capacity
// tile cannot name one failure two ways. Flattening the error to its first line
// before anything classified it would make every failure `cluster_unreachable`,
// retryable: a refused discovery - the console's credential lapsed, or this
// identity may not list services - and a 401 or 403 from a proxy in front of
// Prometheus would all say "ask again".
func TestAFailedMetricsLineIsClassifiedByTheClassifierEveryReadUses(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "",
		errors.New(`User "dev" cannot list resource "services" in API group "" at the cluster scope`))
	gateway := apierrors.NewGenericServerResponse(http.StatusGatewayTimeout, http.MethodGet,
		schema.GroupResource{Resource: "prometheus"}, "", "prometheus answered 504", 0, false)

	cases := []struct {
		name string
		err  error
		// want, where the code is a fact this test states; empty where the test
		// only states that the line agrees with stateOf.
		want api.ReasonCode
	}{
		{"a lapsed credential, as discovery recorded it", fmt.Errorf("prod could not be asked for its metrics (unauthorized): %w",
			apierrors.NewUnauthorized("Unauthorized")), api.CodeCredentialsExpired},
		{"a refused list of services, as discovery recorded it", fmt.Errorf("prod could not be asked for its metrics (unauthorized): %w",
			forbidden), api.CodeVerbDenied},
		{"a 401 from a proxy in front of Prometheus", fmt.Errorf("prometheus answered 401: %w",
			apierrors.NewGenericServerResponse(http.StatusUnauthorized, http.MethodGet, schema.GroupResource{Resource: "prometheus"}, "", "", 0, false)),
			api.CodeCredentialsExpired},
		{"a 403 from a proxy in front of Prometheus", fmt.Errorf("prometheus answered 403: %w",
			apierrors.NewGenericServerResponse(http.StatusForbidden, http.MethodGet, schema.GroupResource{Resource: "prometheus"}, "", "", 0, false)),
			api.CodeVerbDenied},
		{"a deadline", errors.New(`Get "http://127.0.0.1:41234/api/v1/query_range": context deadline exceeded`), api.CodeTimeout},
		{"a refused connection", errors.New("dial tcp 127.0.0.1:41234: connect: connection refused"), api.CodeClusterUnreachable},
		// The code here is whatever stateOf says of a 504 on the tree this runs
		// on - which is what "one classifier" means.
		{"a gateway timeout", gateway, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sentence := "the CPU query did not answer"
			got := lineReason(overview.History{Unavailable: sentence, Missing: overview.MissingFailed, Err: c.err})

			classified := stateOf(c.err, api.Scope{})
			if got.Code != classified.Reason.Code {
				t.Errorf("the line says %q and stateOf says %q of the same failure", got.Code, classified.Reason.Code)
			}
			if c.want != "" && got.Code != c.want {
				t.Errorf("the line says %q; want %q", got.Code, c.want)
			}
			if (got.Retryable == nil) != (classified.Reason.Retryable == nil) ||
				(got.Retryable != nil && *got.Retryable != *classified.Reason.Retryable) {
				t.Errorf("retryable %v; stateOf says %v", got.Retryable, classified.Reason.Retryable)
			}
			// The sentence the line carried, not the classifier's first line, and
			// no scope the line did not have.
			if got.Message != sentence || got.Scope != nil {
				t.Errorf("the reason is %+v; want the line's own sentence and no scope", got)
			}
		})
	}

	// A failure with no error to read
	// is cluster_unreachable, retryable.
	got := lineReason(overview.History{Unavailable: "x", Missing: overview.MissingFailed})
	if got.Code != api.CodeClusterUnreachable || got.Retryable == nil || !*got.Retryable {
		t.Errorf("a failed line with no error is %+v; want cluster_unreachable, retryable", got)
	}
}

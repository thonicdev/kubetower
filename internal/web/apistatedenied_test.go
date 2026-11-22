package web

import (
	"errors"
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/api"
)

// An expired session on a cluster read is `credentials_expired`, retryable —
// never `read_denied`.
//
// `stateOf` is the one classifier behind every cluster read the SPA makes, and
// it must not match `"unauthorized"` in the same arm as `"forbidden"`. A 401,
// which is the API server saying *I do not know who you are*, must not come out
// as `verb_denied` with `retryable: false`: wrong permissions, and retrying will
// not help. `clusterState` says in its own words why that is the opposite
// advice - an `aws sso login` that lapsed is *"the one row on this page the
// operator fixes themselves, in one command, in ten seconds"* - and
// `internal/health` classifies the identical condition with the typed
// predicate. Two opinions, one package apart, must agree.
//
// The cases come in pairs on purpose. client-go hands back a `*StatusError`, but
// most of the read paths wrap it (`fmt.Errorf("listing %s: %w", …)`) before it
// reaches `stateOf`, and a classifier that only reads the top of the chain would
// pass the typed test and fail the real one. The last pair has no type at all:
// a log stream's failure arrives as text (`stateOf(errors.New(event.Text), …)`)
// and must still be told apart.
func TestAnExpiredSessionOnAReadIsCredentialsExpiredAndRetryable(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "",
		errors.New(`User "dev" cannot list resource "pods" in API group "" in the namespace "prod"`))

	cases := []struct {
		name      string
		err       error
		state     api.State
		code      api.ReasonCode
		retryable bool
	}{{
		name: "a Kubernetes 401", err: apierrors.NewUnauthorized("Unauthorized"),
		state: api.StateUnreadable, code: api.CodeCredentialsExpired, retryable: true,
	}, {
		name:  "a Kubernetes 401, wrapped by the read that hit it",
		err:   fmt.Errorf("listing pods: %w", apierrors.NewUnauthorized("Unauthorized")),
		state: api.StateUnreadable, code: api.CodeCredentialsExpired, retryable: true,
	}, {
		name: "a Kubernetes 403", err: forbidden,
		state: api.StateForbidden, code: api.CodeVerbDenied, retryable: false,
	}, {
		name:  "a Kubernetes 403, wrapped by the read that hit it",
		err:   fmt.Errorf("listing pods: %w", forbidden),
		state: api.StateForbidden, code: api.CodeVerbDenied, retryable: false,
	}, {
		name:  "a 401 that lost its type on the way — a log stream's event text",
		err:   errors.New("You must be logged in to the server (Unauthorized)"),
		state: api.StateUnreadable, code: api.CodeCredentialsExpired, retryable: true,
	}, {
		name:  "a 403 that lost its type on the way",
		err:   errors.New(`pods is forbidden: User "dev" cannot list resource "pods"`),
		state: api.StateForbidden, code: api.CodeVerbDenied, retryable: false,
	}, {
		// Nothing reached the cluster: the SDK signing an EKS context in this
		// process could not refresh the SSO token. Not `cluster_unreachable`.
		name: "an SSO session that lapsed under the in-process signer",
		err: errors.New(`listing pods: Get "https://x/api/v1/pods?limit=500": signing a token for "demo": ` +
			`get credentials: failed to refresh cached credentials, refresh cached SSO token failed`),
		state: api.StateUnreadable, code: api.CodeCredentialsExpired, retryable: true,
	}, {
		// The AWS CLI run by client-go: its words are lost, the step is not.
		name:  "an exec plugin that refused",
		err:   errors.New(`listing pods: Get "https://x/api/v1/pods": getting credentials: exec: executable aws failed with exit code 255`),
		state: api.StateUnreadable, code: api.CodeCredentialsRefused, retryable: true,
	}}

	scope := api.Scope{Cluster: "prod", Verb: "list"}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := stateOf(c.err, scope)
			if f == nil {
				t.Fatalf("stateOf returned nil for %v; a failed read is not a successful one", c.err)
			}
			retryable := f.Reason.Retryable != nil && *f.Reason.Retryable
			if f.State != c.state || f.Reason.Code != c.code || retryable != c.retryable {
				t.Errorf("classified as %s/%s retryable=%t, want %s/%s retryable=%t\n  from: %v\n"+
					"  a 401 is the operator's to fix in ten seconds and a 403 is somebody else's; "+
					"telling them the wrong one sends them to an IAM policy over a lapsed login",
					f.State, f.Reason.Code, retryable, c.state, c.code, c.retryable, c.err)
			}
			if f.Reason.Scope == nil || f.Reason.Scope.Cluster != scope.Cluster {
				t.Errorf("the failure lost its scope: %+v", f.Reason.Scope)
			}
		})
	}
}

// And the order between the two arms, stated as its own test because it is the
// one thing about this pairing a later edit could quietly undo: a message that
// carries both words is a 401 whose sentence mentions what was refused, and it
// must read as the session, not the permission. Same rule `internal/health`
// protects between its SSO and IAM arms.
func TestAnUnauthorizedThatAlsoSaysForbiddenIsStillTheSession(t *testing.T) {
	err := errors.New("Unauthorized: forbidden until the session is renewed")
	f := stateOf(err, api.Scope{Cluster: "prod"})
	if f == nil || f.Reason.Code != api.CodeCredentialsExpired {
		t.Fatalf("classified as %+v, want %s — the unauthorized arm is tested before the forbidden one",
			f, api.CodeCredentialsExpired)
	}
}

// A timeout is a timeout by its status, not only by its words. A 504 from the
// aggregator in front of metrics-server carries client-go's "unable to return a
// response in the time allotted", with no "timeout" in it, and must not come out
// `cluster_unreachable`. Each case is paired with the neighbors a later
// edit could swallow: a 503 is still the cluster not answering, and a 404 is
// still absent.
func TestATimeoutByItsStatusIsATimeout(t *testing.T) {
	metricsNodes := schema.GroupResource{Group: "metrics.k8s.io", Resource: "nodes"}
	gateway := apierrors.NewGenericServerResponse(504, "GET", metricsNodes, "", "", 0, true)

	cases := []struct {
		name  string
		err   error
		state api.State
		code  api.ReasonCode
	}{
		{"a 504 with client-go's own sentence", gateway, api.StateUnreachable, api.CodeTimeout},
		{"a 504, wrapped by the read that hit it", fmt.Errorf("metrics-server did not answer: %w", gateway),
			api.StateUnreachable, api.CodeTimeout},
		{"a 504 whose type was lost on the way", errors.New("metrics-server did not answer: " + gateway.Error()),
			api.StateUnreachable, api.CodeTimeout},
		{"a Status with reason Timeout", apierrors.NewTimeoutError("request did not complete", 0),
			api.StateUnreachable, api.CodeTimeout},
		{"a server timeout", apierrors.NewServerTimeout(metricsNodes, "list", 0),
			api.StateUnreachable, api.CodeTimeout},
		{"a 503 is still the cluster not answering", apierrors.NewServiceUnavailable("the server is currently unable to handle the request"),
			api.StateUnreachable, api.CodeClusterUnreachable},
		{"a 404 is still absent", apierrors.NewNotFound(metricsNodes, ""),
			api.StateAbsent, api.CodeNoSuchKind},
	}
	scope := api.Scope{Cluster: "prod", Verb: "list"}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := stateOf(c.err, scope)
			if f == nil {
				t.Fatalf("stateOf returned nil for %v", c.err)
			}
			if f.State != c.state || f.Reason.Code != c.code {
				t.Errorf("classified as %s/%s, want %s/%s\n  from: %v", f.State, f.Reason.Code, c.state, c.code, c.err)
			}
			if c.state == api.StateUnreachable && (f.Reason.Retryable == nil || !*f.Reason.Retryable) {
				t.Errorf("%s is not retryable; a read that did not answer in time is worth asking again", c.name)
			}
		})
	}
}

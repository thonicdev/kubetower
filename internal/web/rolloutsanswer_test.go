package web

// The rollout half of the Changes panel gets its own state and
// reason, so a history that could not be read is not a workload that has never
// shipped.
//
// Three answers rather than one empty list:
//
//   - the kind has no revisions at all - a ConfigMap. No state, no heading.
//   - the kind has revisions and there are none. `empty`.
//   - the read was refused. `forbidden`, with the reason saying so.

import (
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/changes"
)

var changesRef = api.ObjectRef{
	Cluster: "prod-eks-1", Kind: "deployments", Namespace: "prod", Name: "api",
}

func TestAKindWithNoRevisionsCarriesNoRolloutState(t *testing.T) {
	state, reason := rolloutsAnswer(changes.Report{Kind: "configmaps"}, changesRef)
	if state != "" || reason != nil {
		t.Errorf("rolloutsAnswer = %q, %+v; a ConfigMap has no rollout history, which is not an empty one", state, reason)
	}
}

func TestAWorkloadThatHasNeverShippedIsEmpty(t *testing.T) {
	state, reason := rolloutsAnswer(changes.Report{Revisioned: true}, changesRef)
	if state != api.StateEmpty {
		t.Errorf("state = %q, want %q", state, api.StateEmpty)
	}
	if reason != nil {
		t.Errorf("reason = %+v, want none: the read succeeded and held nothing", reason)
	}
}

func TestARolloutHistoryThatWasRefusedSaysSo(t *testing.T) {
	refusal := apierrors.NewForbidden(
		schema.GroupResource{Group: "apps", Resource: "replicasets"}, "",
		errors.New(`User "system:serviceaccount:kubetower:console" cannot list resource "replicasets" in API group "apps"`),
	)
	state, reason := rolloutsAnswer(changes.Report{Revisioned: true, RolloutsErr: refusal}, changesRef)

	if state == api.StateEmpty || state == api.StateOK {
		t.Fatalf("state = %q; a refused read must not share an arm with a read that worked", state)
	}
	if state != api.StateForbidden {
		t.Errorf("state = %q, want %q", state, api.StateForbidden)
	}
	if reason == nil {
		t.Fatalf("reason = nil; the whole point is that the panel says which read was refused")
	}
	if reason.Code != api.CodeVerbDenied {
		t.Errorf("code = %q, want %q", reason.Code, api.CodeVerbDenied)
	}
	// The scope names the resource that was actually refused. A person told
	// "forbidden on deployments", beside a drawer full of the Deployment, would
	// go and check the wrong rule.
	if reason.Scope == nil || reason.Scope.Resource != "replicasets" {
		t.Errorf("scope = %+v, want the replicasets read that was refused", reason.Scope)
	}
}

// A rollout history the cluster could not be asked for at all is a third answer
// again, and it is retryable where a refusal is not.
func TestARolloutHistoryTheClusterCouldNotAnswerIsRetryable(t *testing.T) {
	state, reason := rolloutsAnswer(changes.Report{
		Revisioned:  true,
		RolloutsErr: errors.New("dial tcp 10.0.0.1:443: i/o timeout"),
	}, changesRef)

	if state != api.StateUnreachable {
		t.Errorf("state = %q, want %q", state, api.StateUnreachable)
	}
	if reason == nil || reason.Retryable == nil || !*reason.Retryable {
		t.Errorf("reason = %+v, want retryable: asking again is exactly what might work", reason)
	}
}

func TestAWorkloadWithRevisionsIsOK(t *testing.T) {
	state, reason := rolloutsAnswer(changes.Report{
		Revisioned: true,
		Rollouts:   []changes.Rollout{{Revision: "2"}},
	}, changesRef)
	if state != api.StateOK || reason != nil {
		t.Errorf("rolloutsAnswer = %q, %+v; want ok with no reason", state, reason)
	}
}

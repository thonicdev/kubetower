package web

import (
	"errors"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
)

// A refusal longer than the notice's one line keeps the rest in the detail:
// the page must not cut
// `in API group "demo.example.com…` with no way to read the rest.
func TestARefusalCutToALineCarriesItsWholeTextAsTheDetail(t *testing.T) {
	whole := `deployhooks.demo.example.com is forbidden: User "system:serviceaccount:kubetower:kubetower" ` +
		`cannot list resource "deployhooks" in API group "demo.example.com" at the cluster scope: ` +
		`RBAC: clusterrole.rbac.authorization.k8s.io "kubetower" not found or not granting this verb`
	if len([]rune(whole)) <= detailLine {
		t.Fatalf("precondition: the message is %d runes, not longer than the %d of a line", len([]rune(whole)), detailLine)
	}

	failure := stateOf(errors.New(whole), api.Scope{Cluster: "in-cluster", Verb: "list"})
	if failure == nil {
		t.Fatal("no failure for a refusal")
	}
	if failure.State != api.StateForbidden {
		t.Errorf("state %q, want forbidden", failure.State)
	}
	if !strings.HasSuffix(failure.Reason.Message, "…") {
		t.Errorf("the sentence %q is not the line cut", failure.Reason.Message)
	}
	if failure.Reason.Detail != whole {
		t.Errorf("the detail is %q; want the whole refusal", failure.Reason.Detail)
	}
}

func TestARefusalThatFitsCarriesNoCopyOfItself(t *testing.T) {
	failure := stateOf(errors.New(`pods is forbidden: cannot list resource "pods"`), api.Scope{Verb: "list"})
	if failure.Reason.Detail != "" {
		t.Errorf("a sentence that fitted carries the detail %q", failure.Reason.Detail)
	}
}

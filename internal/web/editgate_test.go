package web

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/edit"
)

// Edit on a manifest, a ConfigMap's or a Secret's value follows the verbs:
// `patch` on the object is asked for the person and for the console before
// the control is drawn, so the refusal is said up front rather than at Review.

var configMapsWhere = edit.Location{Cluster: "prod",
	Resource: schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, Kind: "ConfigMap", Namespaced: true}

var shopSettings = edit.Target{Cluster: "prod", Kind: "configmaps", Namespace: "shop-staging", Name: "settings"}

func editAnswer(ctx context.Context, reviews *accesstest.Reviews) (bool, string) {
	gate := access.New(access.Options{Reviews: reviews.For, InCluster: true})
	return editOffer(editVerdictAt(ctx, gate, configMapsWhere, shopSettings))
}

// A person granted only `view` is offered no Edit at all: not editable, and no
// sentence, which the panels read as no control.
func TestEditIsAbsentForAPersonWhoMayNotPatch(t *testing.T) {
	editable, note := editAnswer(as(bob), grants(consoleMayAll))
	if editable || note != "" {
		t.Errorf("bob (view) is answered editable=%v note=%q, want no edit control (false, no note)", editable, note)
	}
	// The cluster's own wording of the person's refusal is not a sentence for
	// a control: it would draw one, disabled, for a person who has no write.
	if editable, note := editOffer(verdict{decision: access.Denied, reason: "RBAC: access denied"}); editable || note != "" {
		t.Errorf("a person's refusal with a reason is answered editable=%v note=%q, want no edit control", editable, note)
	}
}

// The shared account, whose review is the console's ServiceAccount, under a
// ServiceAccount without patch: disabled, naming the missing grant.
func TestEditIsDisabledWithTheReasonWhereTheConsoleMayNotPatch(t *testing.T) {
	noPatch := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		return q.Attributes.Verb != "patch", nil
	}}
	editable, note := editAnswer(sharedAccount(), noPatch)
	if editable {
		t.Fatal("the shared account is offered Edit although its ServiceAccount may not patch configmaps")
	}
	for _, want := range []string{"ServiceAccount", "patch on configmaps in shop-staging", "rbac.write"} {
		if !strings.Contains(note, want) {
			t.Errorf("the reason %q does not say %q", note, want)
		}
	}

	// A single sign-on person who may, on a console that may not: disabled,
	// saying the person may and the console may not.
	editable, note = editAnswer(as(alice), grants(func(a access.Attributes) bool { return a.Verb != "patch" }))
	if editable || !strings.Contains(note, "you may, but this console may not") {
		t.Errorf("alice may, the console may not: editable=%v note=%q, want disabled with the console's reason", editable, note)
	}
}

// A person who may patch, on a console that may: editable as before, with no
// sentence beside it - and the question asked is the write's own.
func TestEditIsOfferedToAPersonWhoMayPatch(t *testing.T) {
	reviews := grants(consoleMayAll)
	editable, note := editAnswer(as(alice), reviews)
	if !editable || note != "" {
		t.Errorf("alice (edit in shop-staging) is answered editable=%v note=%q, want editable with no note", editable, note)
	}
	want := edit.Attributes(configMapsWhere, shopSettings, "patch", "")
	asked := false
	for _, q := range reviews.Asked() {
		if !q.Self && q.User == alice.Name && q.Attributes == want {
			asked = true
		}
	}
	if !asked {
		t.Errorf("alice was never asked %+v: %+v", want, reviews.Asked())
	}
}

// A review that could not be made, under the server profile: disabled and
// saying so, as an unknown verb is.
func TestAnUnknownEditAnswerIsDrawnDisabled(t *testing.T) {
	editable, note := editOffer(verdict{decision: access.Unreviewable, reason: "i/o timeout"})
	if editable || !strings.Contains(note, "could not ask the cluster") {
		t.Errorf("an unknown answer is editable=%v note=%q, want disabled with its sentence", editable, note)
	}
	if editable, note := editOffer(verdict{decision: access.Unreviewable, through: true}); !editable || note != "" {
		t.Errorf("the desktop's let-through answer is editable=%v note=%q, want editable", editable, note)
	}
}

// The guardrail keeps its own sentence and is answered before any review.
func TestTheGuardrailStillAnswersEditFirst(t *testing.T) {
	s := serverFor(t, true)
	s.readOnly = func(name string) bool { return name == "prod" }
	reviews := &accesstest.Reviews{}
	s.gate = reviews.Gate()
	editable, note := s.editableFor(context.Background(), api.ObjectRef{Cluster: "prod", Kind: "configmaps", Namespace: "shop", Name: "settings"})
	if editable || note == "" {
		t.Errorf("a read-only cluster is answered editable=%v note=%q, want the guardrail's sentence", editable, note)
	}
	if n := len(reviews.Asked()); n != 0 {
		t.Errorf("a read-only cluster asked %d reviews", n)
	}
}

package web

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/detail"
	"github.com/thonicdev/kubetower/internal/edit"
	"github.com/thonicdev/kubetower/internal/identity"
)

// Shell is decided for the pod being
// shown, and a control is drawn only where both the person and the console's
// own ServiceAccount may use it.

var (
	alice = identity.Who{Name: "alice@example.com", Method: identity.SSO, Groups: []string{"oidc:dev"}}
	bob   = identity.Who{Name: "bob@example.com", Method: identity.SSO, Groups: []string{"oidc:viewers"}}
)

func as(who identity.Who) context.Context { return identity.With(context.Background(), who) }

// grants answers the reviews the way the measured cluster did: alice is bound
// to `edit` in shop-staging only, bob to `view` (no exec anywhere), and the
// console's own ServiceAccount answers through console.
func grants(console func(access.Attributes) bool) *accesstest.Reviews {
	return &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		if q.Self {
			return console(q.Attributes), nil
		}
		return q.User == alice.Name && q.Attributes.Namespace == "shop-staging", nil
	}}
}

func consoleMayAll(access.Attributes) bool { return true }

// A grant in one namespace draws Shell on that namespace's pods and
// not on another's, and a person granted no exec sees it on neither - asked
// for the pod being shown, which is the question the socket is reviewed with.
func TestShellIsDrawnPerNamespaceForANamespacedGrant(t *testing.T) {
	reviews := grants(consoleMayAll)
	s := serverFor(t, true)
	s.gate = access.New(access.Options{Reviews: reviews.For, InCluster: true})

	staging := api.ObjectRef{Cluster: "prod", Kind: "pods", Namespace: "shop-staging", Name: "web-1"}
	production := api.ObjectRef{Cluster: "prod", Kind: "pods", Namespace: "shop-prod", Name: "web-1"}

	if offer := s.shellFor(as(alice), staging); offer == nil || !offer.Permitted || offer.Reason != nil {
		t.Errorf("alice on a shop-staging pod: Shell is %+v, want offered", offer)
	}
	if offer := s.shellFor(as(alice), production); offer != nil {
		t.Errorf("alice on a shop-prod pod: Shell is %+v, want withheld", offer)
	}
	for _, ref := range []api.ObjectRef{staging, production} {
		if offer := s.shellFor(as(bob), ref); offer != nil {
			t.Errorf("bob on %s: Shell is %+v, want withheld", ref.Namespace, offer)
		}
	}
	// And the capability does not take it away from alice everywhere.
	if caps := (apiProfile{s}).Capabilities(signedIn(alice)); !caps.PodExec {
		t.Error("podExec is false for a person granted exec in one namespace")
	}

	// The person's question is the socket's own: by namespace and name.
	want := execAttributes("shop-staging", "web-1")
	found := false
	for _, q := range reviews.Asked() {
		if q.Attributes.Namespace == "" {
			t.Errorf("a question was asked about every namespace: %+v", q)
		}
		if !q.Self && q.User == alice.Name && q.Attributes == want {
			found = true
		}
	}
	if !found {
		t.Errorf("alice was never asked %+v: %+v", want, reviews.Asked())
	}
}

// Cached by the gate like the verbs: the same pod asked twice within the
// minute is one review for the person and one for the console.
func TestShellIsAnsweredFromTheGatesHeldAnswers(t *testing.T) {
	reviews := grants(consoleMayAll)
	s := serverFor(t, true)
	s.gate = access.New(access.Options{Reviews: reviews.For, InCluster: true})
	staging := api.ObjectRef{Cluster: "prod", Kind: "pods", Namespace: "shop-staging", Name: "web-1"}

	for range 3 {
		s.shellFor(as(alice), staging)
	}
	if n := len(reviews.Asked()); n != 2 {
		t.Errorf("three reads of one pod asked %d reviews, want 2 (the person's and the console's): %+v", n, reviews.Asked())
	}
}

// Shell is only asked about under the server profile: on the desktop
// `podExec` alone decides, and nothing is asked.
func TestTheDesktopPageCarriesNoShellAnswer(t *testing.T) {
	reviews := &accesstest.Reviews{}
	s := serverFor(t, false)
	s.gate = reviews.Gate()
	if offer := s.shellFor(context.Background(), api.ObjectRef{Cluster: "prod", Kind: "pods", Namespace: "shop", Name: "api-1"}); offer != nil {
		t.Errorf("the desktop's page carries Shell %+v", offer)
	}
	if n := len(reviews.Asked()); n != 0 {
		t.Errorf("the desktop asked %d reviews for Shell", n)
	}
}

// The Shell case. The person may and the console may not: drawn disabled,
// naming the console's grant - never withheld, which would say "you may not".
func TestShellIsDisabledWhereOnlyTheConsoleMayNotExec(t *testing.T) {
	noExec := func(a access.Attributes) bool { return a.Subresource != "exec" }
	reviews := grants(noExec)
	s := serverFor(t, true)
	s.gate = access.New(access.Options{Reviews: reviews.For, InCluster: true})
	staging := api.ObjectRef{Cluster: "prod", Kind: "pods", Namespace: "shop-staging", Name: "web-1"}

	offer := s.shellFor(as(alice), staging)
	if offer == nil || offer.Permitted {
		t.Fatalf("alice may, the console may not: Shell is %+v, want drawn disabled", offer)
	}
	if offer.Reason == nil || offer.Reason.Code != api.CodeVerbDenied ||
		!strings.Contains(offer.Reason.Message, "this console may not") || !strings.Contains(offer.Reason.Message, "rbac.exec") {
		t.Errorf("the reason is %+v, want the console's missing grant, rbac.exec", offer.Reason)
	}
	// bob may not: withheld, as today, and the console is not asked about him.
	before := len(reviews.Asked())
	if offer := s.shellFor(as(bob), staging); offer != nil {
		t.Errorf("bob may not exec: Shell is %+v, want withheld", offer)
	}
	for _, q := range reviews.Asked()[before:] {
		if q.Self {
			t.Errorf("the console was asked about a question bob was refused: %+v", q)
		}
	}
}

var nodesWhere = edit.Location{Cluster: "prod", Resource: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, Kind: "Node"}

// The node verbs. The measured case: alice has `patch nodes` and is
// offered Cordon, which the API server refuses because the ServiceAccount
// lacked it. Cordon is drawn disabled, saying the console's grant is
// missing; granted to the console, it is offered.
func TestAVerbThePersonMayAndTheConsoleMayNotIsDisabledWithTheReason(t *testing.T) {
	object := detail.Object{Kind: "nodes"}
	offered := objectActions("nodes", object, true, "")
	verbs := []actions.Verb{}
	for _, action := range offered {
		verbs = append(verbs, actions.Verb(action.Verb))
	}
	if len(verbs) == 0 {
		t.Fatal("precondition: a node offers verbs")
	}
	target := edit.Target{Cluster: "prod", Kind: "nodes", Name: "worker-1"}
	personMay := func(console bool) *accesstest.Reviews {
		return &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
			if q.Self {
				return console, nil
			}
			return q.User == alice.Name, nil
		}}
	}

	reviews := personMay(false)
	gate := access.New(access.Options{Reviews: reviews.For, InCluster: true})
	got := gateActions(offered, verdictsAt(as(alice), gate, nodesWhere, target, verbs))
	if len(got) != len(offered) {
		t.Fatalf("the console's missing grant withheld verbs: %v of %v", verbNames(got), verbNames(offered))
	}
	cordon := false
	for _, action := range got {
		if action.Verb == string(actions.Cordon) {
			cordon = true
		}
		if action.Permitted {
			t.Errorf("%s is offered as working where the console may not", action.Verb)
		}
		if action.Reason == nil || action.Reason.Code != api.CodeVerbDenied ||
			!strings.Contains(action.Reason.Message, "this console may not") || !strings.Contains(action.Reason.Message, "rbac.write") {
			t.Errorf("%s carries %+v, want the console's missing grant, rbac.write", action.Verb, action.Reason)
		}
	}
	if !cordon {
		t.Errorf("Cordon is not among the node's verbs: %v", verbNames(got))
	}
	selfAsked := false
	for _, q := range reviews.Asked() {
		if q.Self {
			selfAsked = true
			if q.Attributes.Resource != "nodes" && q.Attributes.Subresource != "eviction" {
				t.Errorf("the console was asked about %+v", q.Attributes)
			}
		}
	}
	if !selfAsked {
		t.Error("the console's own rights were never asked")
	}

	// Granted to the console: enabled, and applying is the write door's.
	gate = access.New(access.Options{Reviews: personMay(true).For, InCluster: true})
	for _, action := range gateActions(offered, verdictsAt(as(alice), gate, nodesWhere, target, verbs)) {
		if !action.Permitted || action.Reason != nil {
			t.Errorf("both may: %s is drawn %+v", action.Verb, action.Reason)
		}
	}

	// A person who may not keeps "you may not": withheld, whatever the console may.
	gate = access.New(access.Options{Reviews: personMay(false).For, InCluster: true})
	if got := gateActions(offered, verdictsAt(as(bob), gate, nodesWhere, target, verbs)); len(got) != 0 {
		t.Errorf("bob may not and was offered %v", verbNames(got))
	}
}

// The selection bar draws the console's missing grant the same way, in a list
// of one namespace and in a list of every namespace.
func TestTheSelectionBarDrawsTheConsolesMissingGrant(t *testing.T) {
	offered := bulkAffordance("prod", "pods", false)
	verbs := []actions.Verb{}
	for _, verb := range offered.Verbs {
		verbs = append(verbs, actions.Verb(verb.Verb))
	}
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) { return !q.Self, nil }}
	gate := access.New(access.Options{Reviews: reviews.For, InCluster: true})
	for _, namespaces := range [][]string{{"shop"}, nil} {
		verdicts := acrossNamespaces(bulkScopes(true, namespaces), func(namespace string) map[actions.Verb]verdict {
			return verdictsAt(as(alice), gate, podsWhere, edit.Target{Cluster: "prod", Kind: "pods", Namespace: namespace}, verbs)
		})
		got := gateBulk(offered, acrossEveryNamespace(true, namespaces, verdicts))
		if got == nil || len(got.Verbs) != len(offered.Verbs) {
			t.Fatalf("%v: the console's missing grant withheld bulk verbs: %+v", namespaces, got)
		}
		for _, verb := range got.Verbs {
			if verb.Permitted || verb.Reason == nil || !strings.Contains(verb.Reason.Message, "this console may not") {
				t.Errorf("%v: %s is drawn %+v, want disabled with the console's sentence", namespaces, verb.Verb, verb.Reason)
			}
		}
	}
}

// The console is asked only where the review was the person's. On the desktop
// and for the shared password account it already was the console's own self
// review: nothing is asked twice.
func TestTheConsoleIsNotAskedTwiceWhereTheReviewWasItsOwn(t *testing.T) {
	object := detail.Object{Kind: "pods", Relations: []detail.Relation{controlledBy("ReplicaSet")}}
	offered := objectActions("pods", object, true, "")
	target := edit.Target{Cluster: "prod", Kind: "pods", Namespace: "shop", Name: "api-1"}
	verbs := []actions.Verb{actions.Restart, actions.Evict, actions.Delete}

	desktop := &accesstest.Reviews{}
	gateActions(offered, verdictsAt(context.Background(), desktop.Gate(), podsWhere, target, verbs))
	password := &accesstest.Reviews{}
	server := access.New(access.Options{Reviews: password.For, InCluster: true})
	gateActions(offered, verdictsAt(as(identity.Who{Name: "admin", Method: identity.Password}), server, podsWhere, target, verbs))

	for name, reviews := range map[string]*accesstest.Reviews{"desktop": desktop, "password": password} {
		// Restart and Delete are one question on a pod, Evict another.
		if n := len(reviews.Asked()); n != 2 {
			t.Errorf("%s: %d reviews for two distinct questions: %+v", name, n, reviews.Asked())
		}
	}
}

// A console self review that could not be made is not "the console may not":
// the person's answer stands, and the API server checks the console's rights
// when the request is sent.
func TestAConsoleReviewThatFailedChangesNothing(t *testing.T) {
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		if q.Self {
			return false, errors.New("i/o timeout")
		}
		return true, nil
	}}
	gate := access.New(access.Options{Reviews: reviews.For, InCluster: true})
	object := detail.Object{Kind: "pods", Relations: []detail.Relation{controlledBy("ReplicaSet")}}
	offered := objectActions("pods", object, true, "")
	target := edit.Target{Cluster: "prod", Kind: "pods", Namespace: "shop", Name: "api-1"}
	for _, action := range gateActions(offered, verdictsAt(as(alice), gate, podsWhere, target,
		[]actions.Verb{actions.Restart, actions.Evict, actions.Delete})) {
		if !action.Permitted || action.Reason != nil {
			t.Errorf("%s is drawn %+v after the console's own review failed", action.Verb, action.Reason)
		}
	}
	if offer := shellOffer(reviewBoth(as(alice), gate, "prod", []access.Attributes{execAttributes("shop", "api-1")})[0]); offer == nil || !offer.Permitted {
		t.Errorf("Shell is %+v after the console's own review failed", offer)
	}
}

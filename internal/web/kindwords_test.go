package web

import (
	"context"
	"errors"
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/detail"
)

// A kind is named as discovery spells it: the Kind for one, the plural with
// the Kind's capitals for several - never the URL's lower-case slug, and never
// a singular derived from it ("Endpoint" of Endpoints).
func TestAKindIsNamedAsDiscoverySpellsIt(t *testing.T) {
	for _, c := range []struct{ kind, plural, one, many string }{
		{"ConfigMap", "configmaps", "ConfigMap", "ConfigMaps"},
		{"Endpoints", "endpoints", "Endpoints", "Endpoints"},
		{"NetworkPolicy", "networkpolicies", "NetworkPolicy", "NetworkPolicies"},
		{"Ingress", "ingresses", "Ingress", "Ingresses"},
		{"ApplicationSet", "applicationsets", "ApplicationSet", "ApplicationSets"},
		// A custom resource whose plural is not its Kind's: discovery's word.
		{"Widget", "gadgets", "Widget", "gadgets"},
	} {
		got := wordsFor(c.kind, c.plural)
		if got.one != c.one || got.many != c.many {
			t.Errorf("%s/%s reads %q and %q, want %q and %q", c.kind, c.plural, got.one, got.many, c.one, c.many)
		}
	}
}

// A selection's sentence names the kind as the button does, and a namespace
// deleted says what goes with it.
func TestASelectionsLineNamesTheKindAndWhatGoesWithANamespace(t *testing.T) {
	two := []api.PlanTarget{{Namespace: "shop", Name: "a"}, {Namespace: "shop", Name: "b"}}
	if got, want := bulkLine(actions.Delete, "configmaps", two, wordsFor("ConfigMap", "configmaps")),
		"Delete 2 ConfigMaps. This cannot be undone."; got != want {
		t.Errorf("reads %q, want %q", got, want)
	}
	one := []api.PlanTarget{{Namespace: "shop", Name: "a"}}
	if got, want := bulkLine(actions.Delete, "endpoints", one, wordsFor("Endpoints", "endpoints")),
		"Delete Endpoints shop/a."; got != want {
		t.Errorf("reads %q, want %q", got, want)
	}
	spaces := []api.PlanTarget{{Name: "a"}, {Name: "b"}}
	if got, want := bulkLine(actions.Delete, "namespaces", spaces, wordsFor("Namespace", "namespaces")),
		"Delete 2 Namespaces. This cannot be undone. Everything in them is deleted with them."; got != want {
		t.Errorf("reads %q, want %q", got, want)
	}
	if got, want := bulkLine(actions.Delete, "namespaces", spaces[:1], wordsFor("Namespace", "namespaces")),
		"Delete Namespace a. Everything in it is deleted with it."; got != want {
		t.Errorf("reads %q, want %q", got, want)
	}
}

// Where discovery cannot be asked, the slug's rule still names the kind.
func TestAKindWordFallsBackToTheSlugsRule(t *testing.T) {
	acts := apiActs{s: &Server{}}
	if got := acts.kindWord(context.Background(), "prod", "configmaps", 2); got != "configmaps" {
		t.Errorf("reads %q, want the slug's word", got)
	}
}

// A 404 naming the object asked for is the object gone; a 404 naming
// nothing is a path nothing serves, which stays the kind's absence.
func TestA404NamingTheObjectIsTheObjectGone(t *testing.T) {
	configmaps := schema.GroupResource{Resource: "configmaps"}
	gone := apierrors.NewNotFound(configmaps, "web")
	for _, c := range []struct {
		what string
		err  error
		want bool
	}{
		{"the object's 404", gone, true},
		{"the object's 404, wrapped", fmt.Errorf("reading: %w", gone), true},
		{"a custom resource's 404",
			apierrors.NewNotFound(schema.GroupResource{Group: "example.com", Resource: "widgets"}, "web"), true},
		{"a 404 about another name", apierrors.NewNotFound(configmaps, "api"), false},
		{"a 404 naming nothing", apierrors.NewNotFound(configmaps, ""), false},
		{"the words without the status", errors.New(gone.Error()), false},
		{"a refusal", apierrors.NewForbidden(configmaps, "web", errors.New("no")), false},
	} {
		if got := objectGone(c.err, "web"); got != c.want {
			t.Errorf("%s: gone is %v, want %v", c.what, got, c.want)
		}
	}
}

// An object being deleted - a pod in its grace period, anything a finalizer
// holds - offers its verbs disabled with the reason, and says it is being
// deleted, so the client reads it again until it is gone.
func TestAnObjectBeingDeletedTakesNoVerb(t *testing.T) {
	object := detail.Object{Kind: "pods", Name: "api-0", Facts: []detail.Fact{
		{Label: "Deleting since", Value: "2026-11-05T10:00:00Z — held by no finalizer"},
	}}
	offered := objectActions("pods", object, true, "")
	if len(offered) == 0 {
		t.Fatal("a pod being deleted offers no verb at all; they are drawn disabled, not withheld")
	}
	for _, action := range offered {
		if action.Permitted || action.Reason == nil || action.Reason.Message != deletingReason {
			t.Errorf("%s on a pod being deleted is %+v, want refused with the reason", action.Verb, action)
		}
	}
	if !detailPageOf(api.ObjectRef{Cluster: "prod", Kind: "pods", Namespace: "argo", Name: "api-0"}, object).Deleting {
		t.Error("the page of a pod being deleted does not say so, so nothing reads it again")
	}

	alive := detail.Object{Kind: "pods", Name: "api-0"}
	for _, action := range objectActions("pods", alive, true, "") {
		if !action.Permitted {
			t.Errorf("%s on a live pod is refused: %+v", action.Verb, action)
		}
	}
	if detailPageOf(api.ObjectRef{Cluster: "prod", Kind: "pods", Namespace: "argo", Name: "api-0"}, alive).Deleting {
		t.Error("a live pod's page says it is being deleted")
	}
}

package web

import (
	"context"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/customresources"
)

// The menu says which of its entries open a namespaced kind's list, and the
// browser carries the namespace chosen on one list to exactly those.
// A cluster-scoped kind must not be given one - its list
// has no namespace to be read in - and an entry that is not a kind at all
// (Overview, an extra's page) is neither, so it carries nothing. Helm's list is
// not a kind's but is read by namespace, with the same picker and the same
// memory, so it is one of them.
func TestTheMenuSaysWhichEntriesOpenANamespacedList(t *testing.T) {
	server := navFixture(t, "prod")

	menu, fail := apiClusters{server}.Nav(context.Background(), "prod")
	if fail != nil {
		t.Fatalf("the menu failed: %+v", fail.Reason)
	}
	byHref := map[string]api.Ref{}
	var walk func(groups []api.NavGroup)
	walk = func(groups []api.NavGroup) {
		for _, group := range groups {
			for _, item := range group.Items {
				byHref[item.Href] = item
			}
			walk(group.Groups)
		}
	}
	walk(menu.Groups)

	for href, want := range map[string]bool{
		"/c/prod/pods":              true,
		"/c/prod/configmaps":        true,
		"/c/prod/helm":              true,
		"/c/prod/nodes":             false,
		"/c/prod/namespaces":        false,
		"/c/prod/persistentvolumes": false,
		"/c/prod":                   false,
	} {
		item, ok := byHref[href]
		if !ok {
			t.Errorf("the menu has no entry at %s; the case measures nothing", href)
			continue
		}
		if item.Namespaced != want {
			t.Errorf("%s (%s) says namespaced %v; want %v", item.Text, href, item.Namespaced, want)
		}
	}
	for href, item := range byHref {
		if item.Namespaced && !strings.HasPrefix(href, "/c/prod/") {
			t.Errorf("%s (%s) is marked namespaced and is not a kind's list", item.Text, href)
		}
	}
}

// A custom resource's entry takes its scope from the definition's own
// `spec.scope`, the authority on it, and not from anything about its name.
func TestACustomResourceEntryCarriesItsDefinitionsScope(t *testing.T) {
	definitions := []customresources.Definition{
		{Group: "argoproj.io", Version: "v1alpha1", Plural: "applications", Kind: "Application", Namespaced: true, Served: true},
		{Group: "cert-manager.io", Version: "v1", Plural: "clusterissuers", Kind: "ClusterIssuer", Namespaced: false, Served: true},
	}
	group := navGroupOf(customNavGroup("prod", "", len(definitions), definitions, 0))

	got := map[string]bool{}
	for _, sub := range group.Groups {
		for _, item := range sub.Items {
			got[item.Text] = item.Namespaced
		}
	}
	if namespaced, ok := got["Applications"]; !ok || !namespaced {
		t.Errorf("Applications is %v (listed %v); want namespaced", namespaced, ok)
	}
	if namespaced, ok := got["ClusterIssuers"]; !ok || namespaced {
		t.Errorf("ClusterIssuers is %v (listed %v); want cluster-scoped", namespaced, ok)
	}
}

package web

import (
	"context"
	"slices"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/customresources"
)

// Ctrl+K must answer "svc" with the Services kind. The jump box
// matches what the menu sends, so each entry
// that opens a kind's list carries kubectl's names for it.
func TestTheMenuCarriesKubectlsNamesForEachKind(t *testing.T) {
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

	for href, short := range map[string]string{
		"/c/prod/services":               "svc",
		"/c/prod/deployments":            "deploy",
		"/c/prod/pods":                   "po",
		"/c/prod/configmaps":             "cm",
		"/c/prod/namespaces":             "ns",
		"/c/prod/persistentvolumeclaims": "pvc",
	} {
		item, ok := byHref[href]
		if !ok {
			t.Errorf("the menu has no entry at %s; the case measures nothing", href)
			continue
		}
		if !slices.Contains(item.Aliases, short) {
			t.Errorf("%s (%s) carries %v; want %q among them", item.Text, href, item.Aliases, short)
		}
	}
	if overview := byHref["/c/prod"]; len(overview.Aliases) != 0 {
		t.Errorf("Overview is not a kind and carries %v", overview.Aliases)
	}
}

// A custom resource's entry carries the CRD's own `shortNames`.
func TestACustomResourceEntryCarriesItsShortNames(t *testing.T) {
	definitions := []customresources.Definition{
		{Group: "cert-manager.io", Version: "v1", Plural: "certificates", Singular: "certificate",
			Kind: "Certificate", ShortNames: []string{"cert", "certs"}, Namespaced: true, Served: true},
	}
	group := navGroupOf(customNavGroup("prod", "", len(definitions), definitions, 0))

	var found bool
	for _, sub := range group.Groups {
		for _, item := range sub.Items {
			if item.Text != "Certificates" {
				continue
			}
			found = true
			for _, name := range []string{"cert", "certs", "certificate"} {
				if !slices.Contains(item.Aliases, name) {
					t.Errorf("Certificates carries %v; want %q among them", item.Aliases, name)
				}
			}
		}
	}
	if !found {
		t.Fatal("no Certificates entry; the case measures nothing")
	}
}

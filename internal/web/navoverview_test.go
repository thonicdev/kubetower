package web

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// The menu's first entry is the dashboard, and the API says so.
//
// The `Cluster` group opens with the Overview, the one page carrying a refusing
// cluster's notice and the pointer to
// Health. It is prepended on
// the server, so that the menu and the API agree on what the menu is; a
// client-side insertion would be a second place the menu is composed.
func TestOverviewIsTheFirstItemOfTheClusterMenu(t *testing.T) {
	server := navFixture(t, "prod")

	menu, fail := apiClusters{server}.Nav(context.Background(), "prod")
	if fail != nil {
		t.Fatalf("the menu failed: %+v", fail.Reason)
	}
	if len(menu.Groups) == 0 {
		t.Fatal("the menu has no groups at all; there is nothing to measure")
	}

	first := menu.Groups[0]
	if first.Key != "cluster" {
		t.Fatalf("the first group is %q, want cluster", first.Key)
	}
	if len(first.Items) < 2 {
		t.Fatalf("the cluster group holds %d items; want Overview and at least one kind", len(first.Items))
	}
	if got := first.Items[0]; got.Text != "Overview" || got.Href != "/c/prod" {
		t.Errorf("the first item is %+v, want Overview at /c/prod - the bare cluster route the tile opens", got)
	}
	// Once, and first. A second Overview further down would be the menu
	// composed in two places, which is the defect this test pins against.
	for at, item := range first.Items[1:] {
		if item.Text == "Overview" || item.Href == "/c/prod" {
			t.Errorf("item %d is %+v, a second Overview", at+1, item)
		}
	}
	// The kinds still follow it, in their own spelling of the cluster segment.
	if got := first.Items[1].Href; len(got) <= len("/c/prod/") || got[:len("/c/prod/")] != "/c/prod/" {
		t.Errorf("the item after Overview links to %q, want a kind under /c/prod/", got)
	}
}

// A context whose name carries characters the URL cannot: the Overview href is
// spelled through the same `clusterPath` every other ref in the group uses,
// or the first item of the menu would be the one row spelled differently.
func TestTheOverviewHrefSpellsTheClusterAsTheKindsDo(t *testing.T) {
	server := navFixture(t, "arn:aws:eks:eu-west-1:1234/prod cluster")

	menu, fail := apiClusters{server}.Nav(context.Background(), "arn:aws:eks:eu-west-1:1234/prod cluster")
	if fail != nil {
		t.Fatalf("the menu failed: %+v", fail.Reason)
	}
	items := menu.Groups[0].Items
	overview, kind := items[0].Href, items[1].Href
	if overview+"/" != kind[:len(overview)+1] {
		t.Errorf("Overview links to %q and the next item to %q; the cluster segment differs", overview, kind)
	}
}

// navFixture is a server over a kubeconfig naming one context. The menu asks
// nothing of the cluster with no current page - the custom-resource heading is
// deferred - so a context nothing answers for is enough.
func navFixture(t *testing.T, context string) *Server {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: Config
current-context: "`+context+`"
clusters:
- name: remote
  cluster:
    server: https://prod.example:6443
contexts:
- name: "`+context+`"
  context: {cluster: remote, user: none}
users:
- name: none
  user: {token: none}
`), 0o600); err != nil {
		t.Fatalf("writing the kubeconfig: %v", err)
	}

	manager, err := cluster.NewManager(path)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	server, err := New(Options{Clusters: manager})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return server
}

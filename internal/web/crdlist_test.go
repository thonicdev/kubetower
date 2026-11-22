package web

import (
	"context"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
)

const crdsSlug = "customresourcedefinitions.apiextensions.k8s.io"

// The cluster's CustomResourceDefinitions are a list of their own: the address
// lists them, with the group, scope and versions each defines, rather than
// answering that no such kind is installed.
func TestTheCustomResourceDefinitionsAddressListsTheClustersCRDs(t *testing.T) {
	server := projectServer(t, apiServer{gatewayAPI: true})

	page, fail := apiReads{server}.ListResources(context.Background(), api.ListQuery{Cluster: "prod", Kind: crdsSlug})
	if fail != nil {
		t.Fatalf("the list failed: %+v", fail.Reason)
	}
	if page.Label != "Custom resource definitions" || page.Namespaced {
		t.Errorf("label %q, namespaced %t; want Custom resource definitions, cluster-scoped", page.Label, page.Namespaced)
	}
	var labels []string
	keys := map[string]string{}
	for _, c := range page.Columns {
		labels = append(labels, c.Label)
		keys[c.Label] = c.Key
	}
	if want := "Name,Group,Scope,Versions,Age"; strings.Join(labels, ",") != want {
		t.Errorf("columns %v, want %s", labels, want)
	}
	rows := map[string]api.Row{}
	for _, row := range page.Rows {
		rows[row.Name] = row
	}
	if len(rows) != 3 {
		t.Fatalf("%d rows, want the cluster's three CRDs: %v", len(rows), rows)
	}
	gateways, ok := rows["gateways.gateway.networking.k8s.io"]
	if !ok {
		t.Fatalf("the Gateway CRD is not listed: %v", rows)
	}
	for label, want := range map[string]string{"Group": "gateway.networking.k8s.io", "Scope": "Namespaced", "Versions": "v1"} {
		if got := gateways.Cells[keys[label]].Text; got != want {
			t.Errorf("%s reads %q, want %q", label, got, want)
		}
	}

	// Its drawer: the definition itself, read by name.
	detail, fail := apiReads{server}.GetResource(context.Background(),
		api.ObjectRef{Cluster: "prod", Kind: crdsSlug, Name: "gateways.gateway.networking.k8s.io"})
	if fail != nil {
		t.Fatalf("the drawer failed: %+v", fail.Reason)
	}
	if detail.Name != "gateways.gateway.networking.k8s.io" || detail.Namespace != "" || len(detail.Tabs) == 0 {
		t.Errorf("the drawer opened %q in %q with tabs %v", detail.Name, detail.Namespace, detail.Tabs)
	}
}

// The kinds column links the list, beside the APIServices and the webhooks.
func TestTheMenuLinksTheCustomResourceDefinitionsUnderConfiguration(t *testing.T) {
	menu, custom := readMenu(t, projectServer(t, apiServer{}))

	if !holds(hrefsUnder(menuHeading(menu, "Configuration")), "/c/prod/"+crdsSlug) {
		t.Errorf("Configuration does not link the CRDs: %v", hrefsUnder(menuHeading(menu, "Configuration")))
	}
	if holds(hrefsUnder(&custom), "/c/prod/"+crdsSlug) {
		t.Error("Custom Resources links the CRDs too; a kind is offered once")
	}
}

// A selector that does not parse is refused as the request's fault before the
// cluster is asked, so the handler answers it as a bad request.
func TestAMalformedSelectorIsAnInvalidRequest(t *testing.T) {
	server := projectServer(t, apiServer{})

	_, fail := apiReads{server}.ListResources(context.Background(),
		api.ListQuery{Cluster: "prod", Kind: crdsSlug, Selector: "app in ("})
	if !fail.Invalid() {
		t.Fatalf("a malformed selector answered %+v; want an invalid request", fail)
	}
	if !strings.Contains(fail.Reason.Message, "app in (") {
		t.Errorf("the refusal %q does not name the selector", fail.Reason.Message)
	}
}

// The menu's occupancy answer covers the CRDs as it covers every kind the menu
// offers.
func TestOccupancyCoversTheCustomResourceDefinitions(t *testing.T) {
	server := projectServer(t, apiServer{})
	client, err := server.clusters.Client("prod")
	if err != nil {
		t.Fatalf("the cluster: %v", err)
	}
	kinds, note := server.occupancyKinds(context.Background(), "prod", client)
	if note != "" {
		t.Errorf("a cluster that answered says %q", note)
	}
	for _, k := range kinds {
		if k.slug == crdsSlug {
			if k.question.Namespaced || k.question.Attrs.Group != "apiextensions.k8s.io" ||
				k.question.Attrs.Resource != "customresourcedefinitions" {
				t.Errorf("the CRDs are asked about with %+v", k.question)
			}
			return
		}
	}
	t.Errorf("the occupancy answer leaves the CRDs out: %v", kinds)
}

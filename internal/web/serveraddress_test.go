package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/detail"
)

// resolvedClusters is the estate as far as `api.API.cluster` asks it: it records
// the name the router decoded out of the `{cluster}` segment and knows none, so
// the handler stops there with a 404 that says the address did reach it. The
// rest of the interface is never called on that path and is left nil.
type resolvedClusters struct {
	api.Clusters
	mu   sync.Mutex
	seen []string
}

func (r *resolvedClusters) Known(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, name)
	return false
}

// The API addresses this package builds for a detail page carry the cluster as
// `clusterPath` spells it, and the API's own router reads the same name back.
//
// `tabsFor` (Changes, Manifest), the data address and
// `metricsAddressFor` wrote `"/api/c/" + ref.Cluster` unescaped. An EKS context
// is an ARN holding `/`, so the address had one segment too many and matched no
// operation; `#` cut the rest of the address off as a fragment, and `%` made it
// unparseable. Every name in the shared table is resolved through the real
// router, which is the only check that the round trip holds rather than that
// two spellings agree.
func TestTheDetailPageAPIAddressesResolveBackToTheCluster(t *testing.T) {
	clusters := &resolvedClusters{}
	router := api.New(api.Deps{Clusters: clusters})
	for _, c := range readClusterPathCases(t) {
		pod := api.ObjectRef{Cluster: c.Name, Kind: "pods", Namespace: "ns", Name: "p"}
		configMap := api.ObjectRef{Cluster: c.Name, Kind: "configmaps", Namespace: "ns", Name: "cm"}

		addresses := map[string]string{
			"metricsAddressFor": metricsAddressFor(pod),
			"dataAddressFor":    dataAddressFor(configMap, detail.Object{Kind: "configmaps", Title: "ConfigMap"}),
		}
		for _, tab := range tabsFor(pod, detail.Object{Kind: "pods", Title: "Pod"}) {
			if tab.Endpoint != "" {
				addresses["tabsFor "+tab.Key] = tab.Endpoint
			}
		}
		if len(addresses) < 4 {
			t.Fatalf("%q: only %d addresses were built, so the four were not all compared: %v",
				c.Name, len(addresses), addresses)
		}

		for builder, address := range addresses {
			if address == "" {
				t.Errorf("%s built no address for %q", builder, c.Name)
				continue
			}
			clusters.seen = nil
			request, err := http.NewRequest(http.MethodGet, "http://console"+address, nil)
			if err != nil {
				t.Errorf("%s for %q built %q, which is not an address: %v", builder, c.Name, address, err)
				continue
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if len(clusters.seen) != 1 || clusters.seen[0] != c.Name {
				t.Errorf("%s for %q built %q, and the router read the cluster as %q (answer %d: %s)",
					builder, c.Name, address, clusters.seen, recorder.Code, recorder.Body.String())
			}
		}
	}
}

// The object named in the same addresses' query string is escaped too, so a
// value holding `&`, `#`, `%`, `+` or `=` reads back as itself rather than
// ending the query or adding a parameter. Kubernetes names
// cannot hold most of these today; the address is right whatever the name is.
func TestTheDetailPageAPIAddressesCarryTheObjectAsItIs(t *testing.T) {
	ref := api.ObjectRef{
		Cluster:   "prod",
		Kind:      "pods",
		Namespace: "a&namespace=b",
		Name:      "c#d%e+f=g h",
	}
	addresses := map[string]string{
		"metricsAddressFor": metricsAddressFor(ref),
		"dataAddressFor": dataAddressFor(
			api.ObjectRef{Cluster: ref.Cluster, Kind: "configmaps", Namespace: ref.Namespace, Name: ref.Name},
			detail.Object{Kind: "configmaps", Title: "ConfigMap"}),
	}
	for _, tab := range tabsFor(ref, detail.Object{Kind: "pods", Title: "Pod"}) {
		if tab.Endpoint != "" {
			addresses["tabsFor "+tab.Key] = tab.Endpoint
		}
	}
	if len(addresses) < 4 {
		t.Fatalf("only %d addresses were built: %v", len(addresses), addresses)
	}
	for builder, address := range addresses {
		parsed, err := url.Parse(address)
		if err != nil {
			t.Errorf("%s built %q, which is not an address: %v", builder, address, err)
			continue
		}
		query := parsed.Query()
		if len(query) != 3 {
			t.Errorf("%s built %q, whose query has %d parameters, want 3: %v", builder, address, len(query), query)
		}
		if got := query["namespace"]; len(got) != 1 || got[0] != ref.Namespace {
			t.Errorf("%s built %q, and its namespace reads back as %q", builder, address, got)
		}
		if got := query["name"]; len(got) != 1 || got[0] != ref.Name {
			t.Errorf("%s built %q, and its name reads back as %q", builder, address, got)
		}
	}
}

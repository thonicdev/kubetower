package access

import (
	"net/http"
	"testing"
)

// TestTheParserReadsARequestAsTheAPIServerDoes: every URL shape the console
// sends - namespaced and cluster-scoped, core and grouped, with and without a
// subresource - read into the attributes kube-apiserver would authorize.
//
// The expectations are written from kube-apiserver's RequestInfoFactory, not
// from this parser: a case where the two disagree is a request the guard would
// compare against a question the API server is not asking.
func TestTheParserReadsARequestAsTheAPIServerDoes(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         Attributes
	}{
		// Core, namespaced: edit's patch, delete and create; the eviction.
		{http.MethodPatch, "/api/v1/namespaces/shop/configmaps/settings",
			Attributes{Verb: "patch", Version: "v1", Resource: "configmaps", Namespace: "shop", Name: "settings"}},
		{http.MethodDelete, "/api/v1/namespaces/shop/pods/api-1",
			Attributes{Verb: "delete", Version: "v1", Resource: "pods", Namespace: "shop", Name: "api-1"}},
		{http.MethodPost, "/api/v1/namespaces/shop/configmaps",
			Attributes{Verb: "create", Version: "v1", Resource: "configmaps", Namespace: "shop"}},
		{http.MethodPost, "/api/v1/namespaces/shop/pods/api-1/eviction",
			Attributes{Verb: "create", Version: "v1", Resource: "pods", Subresource: "eviction", Namespace: "shop", Name: "api-1"}},
		{http.MethodPut, "/api/v1/namespaces/shop/pods/api-1/status",
			Attributes{Verb: "update", Version: "v1", Resource: "pods", Subresource: "status", Namespace: "shop", Name: "api-1"}},
		// Grouped, namespaced: a deployment's scale, a custom resource.
		{http.MethodPatch, "/apis/apps/v1/namespaces/shop/deployments/api/scale",
			Attributes{Verb: "patch", Group: "apps", Version: "v1", Resource: "deployments", Subresource: "scale", Namespace: "shop", Name: "api"}},
		{http.MethodDelete, "/apis/argoproj.io/v1alpha1/namespaces/argocd/applications/shop",
			Attributes{Verb: "delete", Group: "argoproj.io", Version: "v1alpha1", Resource: "applications", Namespace: "argocd", Name: "shop"}},
		// Cluster-scoped, core and grouped.
		{http.MethodPatch, "/api/v1/nodes/worker-1",
			Attributes{Verb: "patch", Version: "v1", Resource: "nodes", Name: "worker-1"}},
		{http.MethodDelete, "/apis/rbac.authorization.k8s.io/v1/clusterroles/viewer",
			Attributes{Verb: "delete", Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles", Name: "viewer"}},
		{http.MethodPost, "/apis/authorization.k8s.io/v1/subjectaccessreviews",
			Attributes{Verb: "create", Group: "authorization.k8s.io", Version: "v1", Resource: "subjectaccessreviews"}},
		// A namespace is in itself, as the API server reads it; its own
		// subresources stay its own.
		{http.MethodDelete, "/api/v1/namespaces/shop",
			Attributes{Verb: "delete", Version: "v1", Resource: "namespaces", Namespace: "shop", Name: "shop"}},
		{http.MethodPut, "/api/v1/namespaces/shop/finalize",
			Attributes{Verb: "update", Version: "v1", Resource: "namespaces", Subresource: "finalize", Namespace: "shop", Name: "shop"}},
		{http.MethodPost, "/api/v1/namespaces",
			Attributes{Verb: "create", Version: "v1", Resource: "namespaces"}},
		// No name: a collection delete.
		{http.MethodDelete, "/api/v1/namespaces/shop/configmaps",
			Attributes{Verb: "deletecollection", Version: "v1", Resource: "configmaps", Namespace: "shop"}},
		// A shell and a tunnel: create, whatever the method - SPDY posts,
		// a WebSocket upgrade gets.
		{http.MethodPost, "/api/v1/namespaces/shop/pods/api-1/exec",
			Attributes{Verb: "create", Version: "v1", Resource: "pods", Subresource: "exec", Namespace: "shop", Name: "api-1"}},
		{http.MethodGet, "/api/v1/namespaces/shop/pods/api-1/exec",
			Attributes{Verb: "create", Version: "v1", Resource: "pods", Subresource: "exec", Namespace: "shop", Name: "api-1"}},
		{http.MethodGet, "/api/v1/namespaces/shop/pods/api-1/portforward",
			Attributes{Verb: "create", Version: "v1", Resource: "pods", Subresource: "portforward", Namespace: "shop", Name: "api-1"}},
		{http.MethodGet, "/api/v1/namespaces/shop/pods/api-1/attach",
			Attributes{Verb: "create", Version: "v1", Resource: "pods", Subresource: "attach", Namespace: "shop", Name: "api-1"}},
		// Reads, for completeness: a get, the kubelet through the node proxy.
		{http.MethodGet, "/api/v1/namespaces/shop/pods/api-1/log",
			Attributes{Verb: "get", Version: "v1", Resource: "pods", Subresource: "log", Namespace: "shop", Name: "api-1"}},
		{http.MethodGet, "/api/v1/nodes/worker-1/proxy/stats/summary",
			Attributes{Verb: "get", Version: "v1", Resource: "nodes", Subresource: "proxy", Name: "worker-1"}},
		// The special verbs take the verb from the path, and a proxy has no
		// subresource.
		{http.MethodGet, "/api/v1/watch/namespaces/shop/pods",
			Attributes{Verb: "watch", Version: "v1", Resource: "pods", Namespace: "shop"}},
		{http.MethodPost, "/api/v1/proxy/namespaces/shop/pods/api-1/anything",
			Attributes{Verb: "proxy", Version: "v1", Resource: "pods", Namespace: "shop", Name: "api-1"}},
		// A method with no verb gets none, so no review covers it.
		{"OPTIONS", "/api/v1/namespaces/shop/pods/api-1",
			Attributes{Version: "v1", Resource: "pods", Namespace: "shop", Name: "api-1"}},
	} {
		got, ok := parseRequest(tc.method, tc.path)
		if !ok || got != tc.want {
			t.Errorf("%s %s: read as %+v (resource=%t), want %+v", tc.method, tc.path, got, ok, tc.want)
		}
	}
}

// Discovery, the version, health and anything off /api and /apis are not
// resource requests. A change to one is refused, since nothing reviews it.
func TestAPathThatIsNotAResourceIsNotReadAsOne(t *testing.T) {
	for _, path := range []string{
		"/", "/version", "/healthz", "/openapi/v3", "/api", "/api/v1", "/apis", "/apis/apps", "/apis/apps/v1",
		"/api/v1/watch", "/metrics",
	} {
		if got, ok := parseRequest(http.MethodPost, path); ok {
			t.Errorf("POST %s was read as a resource request: %+v", path, got)
		}
	}
}

// The guard under a prefix: a path outside the root is not the API server's.
func TestTheRootIsStrippedAndNothingOutsideItIsAResource(t *testing.T) {
	g := &guard{root: "/k8s/clusters/c-1"}
	for _, tc := range []struct {
		in, want string
		under    bool
	}{
		{"/k8s/clusters/c-1/api/v1/pods", "/api/v1/pods", true},
		{"/k8s/clusters/c-1", "/", true},
		{"/k8s/clusters/c-10/api/v1/pods", "/k8s/clusters/c-10/api/v1/pods", false},
		{"/api/v1/pods", "/api/v1/pods", false},
	} {
		got, under := g.relative(tc.in)
		if got != tc.want || under != tc.under {
			t.Errorf("relative(%q) = %q, %t; want %q, %t", tc.in, got, under, tc.want, tc.under)
		}
	}
}

// TestWhatCountsAsAChange: the GETs that are not reads. A kubelet stream
// through a node's proxy is authorized by kube-apiserver as `get nodes/proxy` -
// the grant the kubelet-stats read needs - and runs a command in any pod on the
// node, so it is a change; the proxy's figures and logs are reads. Any upgrade
// is a change, and so is the deprecated proxy prefix.
func TestWhatCountsAsAChange(t *testing.T) {
	upgrade := http.Header{"Connection": {"keep-alive, Upgrade"}, "Upgrade": {"websocket"}}
	for _, tc := range []struct {
		method, path string
		header       http.Header
		want         bool
	}{
		// The kubelet's streams, in its own spellings.
		{http.MethodGet, "/api/v1/nodes/worker-1/proxy/exec/shop/api-1/app", nil, true},
		{http.MethodGet, "/api/v1/nodes/worker-1/proxy/attach/shop/api-1/app", nil, true},
		{http.MethodGet, "/api/v1/nodes/worker-1/proxy/portForward/shop/api-1", nil, true},
		{http.MethodGet, "/api/v1/nodes/worker-1:10250/proxy/run/shop/api-1/app", nil, true},
		{http.MethodGet, "/api/v1/nodes/worker-1/proxy/cri/exec/abcdef", nil, true},
		// The node proxy's reads: the kubelet-stats read the console makes, and
		// the rest of what that grant is for.
		{http.MethodGet, "/api/v1/nodes/worker-1/proxy/stats/summary", nil, false},
		{http.MethodGet, "/api/v1/nodes/worker-1/proxy/metrics", nil, false},
		{http.MethodGet, "/api/v1/nodes/worker-1/proxy/containerLogs/shop/api-1/app", nil, false},
		// A pod's own subresources and an ordinary read.
		{http.MethodGet, "/api/v1/namespaces/shop/pods/api-1/exec", nil, true},
		{http.MethodGet, "/api/v1/namespaces/shop/pods/api-1/log", nil, false},
		{http.MethodGet, "/api/v1/namespaces/shop/configmaps/settings", nil, false},
		// Any upgrade, whatever it names.
		{http.MethodGet, "/api/v1/namespaces/shop/configmaps/settings", upgrade, true},
		{http.MethodGet, "/api/v1/nodes/worker-1/proxy/stats/summary", http.Header{"Upgrade": {"SPDY/3.1"}}, true},
		// The deprecated proxy prefix, whatever it names.
		{http.MethodGet, "/api/v1/proxy/nodes/worker-1/exec/shop/api-1/app", nil, true},
		{http.MethodGet, "/api/v1/proxy/nodes/worker-1/stats/summary", nil, true},
		// Every method but GET and HEAD.
		{http.MethodPost, "/api/v1/nodes/worker-1/proxy/stats/summary", nil, true},
		{http.MethodHead, "/api/v1/namespaces/shop/configmaps/settings", nil, false},
	} {
		req, err := http.NewRequest(tc.method, "https://cluster"+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		for name, values := range tc.header {
			req.Header[name] = values
		}
		attrs, _ := parseRequest(tc.method, tc.path)
		if got := changes(req, tc.path, attrs); got != tc.want {
			t.Errorf("%s %s (%v): changes = %t, want %t", tc.method, tc.path, tc.header, got, tc.want)
		}
	}
}

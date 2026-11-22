package detail

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/access"
)

// TestANamespacePageIsReviewedInTheNamespace: reading one Namespace is
// reviewed in that namespace, as kube-apiserver authorizes `kubectl get ns x`,
// so a Role in `x` granting get on namespaces opens x's page. The list stays
// cluster-wide. Every other cluster-scoped kind is still reviewed with no
// namespace, and a namespaced kind in its own.
func TestANamespacePageIsReviewedInTheNamespace(t *testing.T) {
	namespaces := Location{Resource: schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}}
	nodes := Location{Resource: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}}
	pods := Location{Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespaced: true}

	for _, tc := range []struct {
		why  string
		got  access.Attributes
		want access.Attributes
	}{
		{"a namespace", namespaces.Attributes("get", "", "x"),
			access.Attributes{Verb: "get", Version: "v1", Resource: "namespaces", Namespace: "x", Name: "x"}},
		{"a namespace, a namespace passed", namespaces.Attributes("get", "y", "x"),
			access.Attributes{Verb: "get", Version: "v1", Resource: "namespaces", Namespace: "x", Name: "x"}},
		{"the list of namespaces", namespaces.Attributes("list", "", ""),
			access.Attributes{Verb: "list", Version: "v1", Resource: "namespaces"}},
		{"a node", nodes.Attributes("get", "x", "worker-1"),
			access.Attributes{Verb: "get", Version: "v1", Resource: "nodes", Name: "worker-1"}},
		{"a pod", pods.Attributes("get", "shop", "api-1"),
			access.Attributes{Verb: "get", Version: "v1", Resource: "pods", Namespace: "shop", Name: "api-1"}},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: reviewed %+v, want %+v", tc.why, tc.got, tc.want)
		}
	}
}

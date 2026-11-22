package access

import (
	"net/http"
	"testing"
)

// TestANamespaceIsReviewedInItself: the namespace a review of one Namespace
// carries is the namespace's own name, as kube-apiserver reads the request, so
// a Role in `x` granting get or delete on namespaces answers for `x`. A list or a create names no object and stays
// cluster-wide; every other kind keeps its own scope.
func TestANamespaceIsReviewedInItself(t *testing.T) {
	for _, tc := range []struct {
		why                   string
		resource              string
		namespaced            bool
		namespace, name, want string
	}{
		{"a namespace, by name", "namespaces", false, "", "x", "x"},
		{"a namespace, whatever namespace the caller passed", "namespaces", false, "other", "x", "x"},
		{"the list of namespaces", "namespaces", false, "", "", ""},
		{"a cluster-scoped kind", "nodes", false, "x", "worker-1", ""},
		{"a namespaced kind", "pods", true, "shop", "api-1", "shop"},
		{"a namespaced list", "pods", true, "shop", "", "shop"},
	} {
		if got := RequestNamespace(tc.resource, tc.namespaced, tc.namespace, tc.name); got != tc.want {
			t.Errorf("%s: RequestNamespace(%q, %v, %q, %q) = %q, want %q",
				tc.why, tc.resource, tc.namespaced, tc.namespace, tc.name, got, tc.want)
		}
	}
}

// TestANamespaceReviewIsTheRequestTheAPIServerAuthorises: for every request
// the console sends on one Namespace, the review built with RequestNamespace is
// exactly what parseRequest - kube-apiserver's reading - makes of the request,
// and so covers it; a review of another namespace does not.
func TestANamespaceReviewIsTheRequestTheAPIServerAuthorises(t *testing.T) {
	for _, tc := range []struct {
		method, path, verb, name string
	}{
		{http.MethodGet, "/api/v1/namespaces/x", "get", "x"},
		{http.MethodDelete, "/api/v1/namespaces/x", "delete", "x"},
		{http.MethodPatch, "/api/v1/namespaces/x", "patch", "x"},
		{http.MethodPost, "/api/v1/namespaces", "create", ""},
	} {
		request, ok := parseRequest(tc.method, tc.path)
		if !ok {
			t.Fatalf("%s %s is not read as a resource request", tc.method, tc.path)
		}
		reviewed := Attributes{
			Verb: tc.verb, Version: "v1", Resource: "namespaces",
			Namespace: RequestNamespace("namespaces", false, "", tc.name), Name: tc.name,
		}
		if reviewed != request {
			t.Errorf("%s %s: reviewed %+v, the API server authorizes %+v", tc.method, tc.path, reviewed, request)
		}
		if !covers(reviewed, request) {
			t.Errorf("%s %s: the review %+v does not cover the request", tc.method, tc.path, reviewed)
		}
		if tc.name != "" {
			elsewhere := reviewed
			elsewhere.Namespace = "y"
			if covers(elsewhere, request) {
				t.Errorf("%s %s: a review in namespace y covers it", tc.method, tc.path)
			}
		}
	}
}

package access

// RequestNamespace is the namespace kube-apiserver authorizes a request on one
// object in: namespace for a namespaced kind, none for a cluster-scoped one -
// **except a Namespace itself, which is in itself.**
//
// kube-apiserver reads `/api/v1/namespaces/{name}` (and its `status` and
// `finalize`) as in namespace `name`, so a Role in `x` granting `get` and
// `delete` on `namespaces` lets `kubectl get ns x` through. A review that
// dropped the namespace would ask the cluster-wide question and refuse what
// kubectl reads. parseRequest reads the path the same way, so the question
// asked is the request sent.
//
// No name is no object: a list or create of namespaces stays cluster-wide.
// The group is not consulted, because the API server's reading of the path
// does not consult it either.
func RequestNamespace(resource string, namespaced bool, namespace, name string) string {
	if namespaced {
		return namespace
	}
	if resource == "namespaces" && name != "" {
		return name
	}
	return ""
}

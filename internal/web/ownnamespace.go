package web

import (
	"fmt"

	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/detail"
)

// Under `--incluster` the console's own namespace, the one holding its own
// Deployment, Service and state volume, offers no Delete; every other namespace
// is unchanged. The namespace is read once, in New, from the file the kubelet
// mounts beside the ServiceAccount token (`podNamespace`).
//
// Two halves, as for Restart on a pod nothing recreates:
//   - offeredActions withholds Delete from the page, drawer and row menu
//     (withheld, not disabled: the verb does not exist here);
//   - refuseOwnNamespace refuses a plan or act that would delete it, singly or
//     in a bulk set, before anything reaches the cluster.
//
// Other system namespaces are not covered.

// ownNamespaceOf is what New keeps as Server.ownNamespace: the pod's namespace
// under the server profile, nil outside it.
func ownNamespaceOf(inCluster bool) func() string {
	if !inCluster {
		return nil
	}
	return podNamespace
}

// isOwnNamespace reports a Namespace named name that is the one this console
// runs in. False outside the server profile and where the namespace file could
// not be read, so an unknown namespace withholds nothing rather than everything.
func (s *Server) isOwnNamespace(kind, name string) bool {
	if s == nil || s.ownNamespace == nil || !matchKind(kind, "namespaces") {
		return false
	}
	own := s.ownNamespace()
	return own != "" && name == own
}

// offeredActions is objectActions for this object on this console: the verbs
// its kind offers, less a Delete of the console's own namespace.
func (s *Server) offeredActions(ref api.ObjectRef, object detail.Object, writable bool, note string) []api.DetailAction {
	offered := objectActions(ref.Kind, object, writable, note)
	if !s.isOwnNamespace(ref.Kind, ref.Name) {
		return offered
	}
	out := make([]api.DetailAction, 0, len(offered))
	for _, action := range offered {
		if action.Verb != string(actions.Delete) {
			out = append(out, action)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// refuseOwnNamespace refuses a Delete of the console's own namespace, nil for
// anything else. It is `api.ErrNotInProfile` (a 403), not an RBAC refusal: the
// server profile does not offer this delete at all.
func (s *Server) refuseOwnNamespace(verb actions.Verb, kind string, names ...string) error {
	if verb != actions.Delete {
		return nil
	}
	for _, name := range names {
		if s.isOwnNamespace(kind, name) {
			return fmt.Errorf("%w: namespace %s is the one this console runs in, and deleting it would "+
				"delete the console itself; it is not deleted from here", api.ErrNotInProfile, name)
		}
	}
	return nil
}

// targetNames is the names of a bulk request's targets.
func targetNames(targets []api.PlanTarget) []string {
	names := make([]string, 0, len(targets))
	for _, target := range targets {
		names = append(names, target.Name)
	}
	return names
}

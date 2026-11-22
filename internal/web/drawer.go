package web

import (
	"strings"

	"github.com/thonicdev/kubetower/internal/detail"
)

// isPodKind reports whether this resource has containers to read logs from.
// Owners of pods do not: picking one of a Deployment's pods would be a guess.
func isPodKind(kind string) bool {
	return strings.EqualFold(kind, "pods") || strings.EqualFold(kind, "pod")
}

// controllerKindOf reads the owning controller's Kind off the relation graph
// (the RoleControlledBy relation, labeled "Kind/Name"), so the drawer and its
// panel cannot disagree about the owner. Empty means a bare pod, the case that
// most needs Restart withheld.
func controllerKindOf(object *detail.Object) string {
	for _, rel := range object.Relations {
		if rel.Role != detail.RoleControlledBy {
			continue
		}
		if kind, _, found := strings.Cut(rel.Label, "/"); found {
			return kind
		}
	}
	return ""
}

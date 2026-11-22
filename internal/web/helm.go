package web

import (
	"context"

	"github.com/thonicdev/kubetower/internal/helm"
)

// The Helm section: the releases installed on this cluster, at which chart
// version and revision, and each release's history on a page of its own. A Helm
// release is a core Secret on nearly every cluster, so this is core navigation,
// not an extra. It is read-only: a rollback is not a manifest edit and would be a
// second write path beside `internal/edit`.
const (
	helmNavKey = "helm"
	// The label says "releases" because the page reads neither charts nor
	// repositories.
	helmNavLabel = "Helm releases"
)

// helmReader is what this package needs of internal/helm, as an interface so a
// test can hand the handler a report in each absence state: those keep an empty
// answer apart from a failed one.
type helmReader interface {
	Releases(ctx context.Context, cluster string, namespaces []string) helm.Report
	History(ctx context.Context, cluster, namespace, name string) helm.History
}

// helmLink is marked Namespaced although Helm is not a kind: the list is read by
// namespace, so the menu carries the namespace chosen on another list to it.
func helmLink(cluster, current string) navLink {
	return navLink{
		Label:      helmNavLabel,
		Href:       helmPageURL(cluster),
		Current:    current == helmNavKey,
		Namespaced: true,
	}
}

// helmPageURL is the list. The cluster is a path segment, so it goes through
// `clusterPath`.
func helmPageURL(cluster string) string {
	return clusterPath(cluster) + "/" + helmNavKey
}

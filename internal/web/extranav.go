package web

import (
	"github.com/thonicdev/kubetower/internal/extras"
)

// extraNavLabel is the heading at the very bottom of the left menu holding the
// pages that are not kinds.
const extraNavLabel = "Extra"

// extraPageURL is where an extra's page lives: `/c/{cluster}/x/{key}`, a route
// of the SPA. It is not `registry.Prefix` (`/x/{cluster}/{key}/`), where the
// extension's HTTP routes are mounted: a menu entry there would open its handler
// root instead of its page.
//
// The menu reads this href, not the TypeScript `Extension.nav.path`, so where the
// entry points is decided in the same place as whether it exists (the server-side
// gate). An extra's page route must therefore be `x/<key>` under `/c/:cluster`.
// The key is a URL segment that bookmarks carry.
func extraPageURL(cluster, key string) string {
	return clusterPath(cluster) + "/x/" + key
}

// extraNav is the heading at the very bottom of the left menu, for pages that
// answer a question about the whole cluster. Its entries come from the catalog:
// each extra registers itself, and the key the store is asked about is the key
// the link points at. `Server.nav` appends it, so every page's menu carries it.
func extraNav(cluster, current string, enabled func(string) bool) navGroup {
	group := navGroup{
		Label: extraNavLabel,
		Extra: true,
	}
	for _, e := range extras.Catalogue() {
		if !enabled(e.Key) {
			continue
		}
		link := navLink{
			Label:   e.Label,
			Href:    extraPageURL(cluster, e.Key),
			Current: current == e.Key,
		}
		if link.Current {
			// Following a link must never land on a page whose entry is folded away.
			group.Open = true
		}
		group.Links = append(group.Links, link)
	}
	return group
}

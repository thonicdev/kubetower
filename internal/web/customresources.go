package web

// The custom resource kinds, on the same pages as the built-in ones. A CRD's
// rows arrive as the same `resources.Table` a pod list produces; this file adds
// what the listing layer cannot know: which kinds this cluster serves, how to
// spell one in a URL, where a row leads, and how often to re-read it.

import (
	"context"
	"net/url"
	"strconv"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/customresources"
	"github.com/thonicdev/kubetower/internal/resources"
)

// resolveKind finds the kind a URL segment names, built-in first: a CRD whose
// plural is `services` must not displace the built-in kind. The qualified form,
// `services.acme.io`, still reaches the CRD.
func (s *Server) resolveKind(ctx context.Context, name, kind string) (listKind, error) {
	if descriptor, ok := resources.Lookup(kind); ok {
		return s.builtInKind(descriptor), nil
	}

	client, err := s.clusters.Client(name)
	if err != nil {
		return listKind{}, err
	}
	definition, err := s.custom.Definition(ctx, client, kind)
	if err != nil {
		return listKind{}, err
	}
	return s.customKind(definition), nil
}

// customResourceRefresh is how often a custom resource list re-reads itself.
// Nothing in a CRD says whether its objects are a controller's status written
// every few seconds or a policy applied once, and the things people watch while
// they change (an Argo Application, a Certificate) are custom resources. Fifteen
// seconds, as overviewRefresh: a whole-cluster read of entire objects, slow
// enough to afford and fast enough to see reconciliation move.
const customResourceRefresh = 15 * time.Second

func (s *Server) builtInKind(d resources.Descriptor) listKind {
	return listKind{
		slug:  d.Kind,
		title: d.Title,
		// The kind's own interval; a descriptor declaring none yields the default.
		refresh: d.Refresh(),
		fetch: func(ctx context.Context, client *cluster.Client, ask listAsk) (resources.Table, error) {
			// usageFor never blocks (usagecache.go), so the rows are never held behind a
			// metrics backend.
			return resources.List(ctx, s.gate, client, resources.Query{
				Kind:       d.Kind,
				Namespace:  ask.namespace,
				Selector:   ask.selector,
				Filter:     ask.search,
				Filters:    ask.filters,
				Within:     ask.within,
				Sort:       ask.order.sort,
				Descending: ask.order.descending,
			}, s.usageFor(ctx, client.Context, d.Kind, ask.namespace))
		},
	}
}

func (s *Server) customKind(d customresources.Definition) listKind {
	slug := d.Slug()
	// A native project kind knows its band (internal/customresources, native.go);
	// a CRD takes customResourceRefresh.
	refresh := customResourceRefresh
	if d.Refresh > 0 {
		refresh = d.Refresh
	}
	return listKind{
		slug:    slug,
		title:   d.Title(),
		refresh: refresh,
		fetch: func(ctx context.Context, client *cluster.Client, ask listAsk) (resources.Table, error) {
			// `within` too, as for a built-in kind, so a selection's filter offers only its
			// own rows' values.
			table, err := s.custom.List(ctx, s.gate, client, slug, ask.namespace, ask.within, ask.selector, ask.search, ask.filters)
			if err != nil {
				return resources.Table{}, err
			}
			// The registry sorts by name; the order the person chose is applied here with
			// the built-in kinds' function.
			table.OrderBy(ask.order.sort, ask.order.descending)
			return table, nil
		},
		href: func(name, namespace, object string) string {
			return detailURL(name, slug, namespace, object)
		},
	}
}

// detailURL addresses one custom object. Whether the path carries a namespace
// depends on the object having one (from the CRD's `scope`, not the request), so
// a name can never be read as a namespace.
func detailURL(name, kind, namespace, object string) string {
	path := clusterPath(name) + "/" + url.PathEscape(kind)
	if namespace != "" {
		path += "/" + url.PathEscape(namespace)
	}
	return path + "/" + url.PathEscape(object)
}

// nav is the whole left menu: the kinds this console knows, then the kinds this
// cluster defines, and the discovery state when the project's kinds this
// cluster serves are not all known (projectnav.go). Every page's menu is
// built here, so no page can lack an entry.
func (s *Server) nav(ctx context.Context, name, current string) ([]navLink, []navGroup, *api.NavDiscovery) {
	// Less the kinds the in-cluster console may not list anywhere (nil elsewhere),
	// and less the typed kinds this cluster does not serve, from discovery's held
	// answer: a kind served after an upgrade is missing for up to the catalog's
	// TTL.
	project, unserved, discovery := s.projectNav(ctx, name)
	top, groups := s.kindNav(name, current, withoutUnserved(s.unlistedKinds(ctx, name), unserved))
	// The project's own kinds this cluster serves, each under its heading.
	groups = projectNavGroups(name, current, groups, project)
	// Helm goes beside Overview, not under "Extra": a release is a core Secret
	// nearly every cluster has, so it is part of the console, not an extra.
	top = append(top, helmLink(name, current))
	// The port-forwards, last under Network, where the console opens them.
	if s.offersForwards() {
		groups = withForwardsNav(name, current, groups)
	}
	groups = append(groups, s.customNav(ctx, name, current)...)
	// "Extra" last: it holds the pages that are not kinds, after the custom
	// resources. Bound to this cluster and to the person (under the server profile,
	// the verb use on the extra), so the menu and the route answer the same question
	// (extrasInMenu, refuseExtraUnused).
	enabled, unreviewable := s.extrasInMenu(ctx, name)
	group := extraNav(name, current, enabled)
	// An extra left out because its review could not be made is said on the heading,
	// not dropped as if refused.
	group.Note = unreviewable
	if len(group.Links) == 0 && group.Note == "" {
		// Every extra off, the default, means no heading: an empty "Extra" reads as a
		// failed load. A heading with a note is kept.
		return top, groups, discovery
	}
	return top, append(groups, group), discovery
}

// customNavLabel is the one heading whose contents cost a cluster read, the only
// deferred group. The API's key is derived from it (`navKey` in apimount.go) and
// the handler compares against that key, so it is spelt once.
const customNavLabel = "Custom Resources"

// customNavDeadline bounds the one menu group that makes an API call: a menu
// still loading after this has stopped being a menu.
const customNavDeadline = 30 * time.Second

// customNav lists the cluster's CRDs under one "Custom Resources" heading, with a
// nested disclosure per API group: a person looks for a Certificate under
// cert-manager, but twenty vendor headings at the top would push the built-in
// groups below the fold. A cluster whose CRDs cannot be listed gets the heading
// with the reason, never an empty menu.
func (s *Server) customNav(ctx context.Context, name, current string) []navGroup {
	// Deferred unless the current page is itself a custom resource: listing the CRDs
	// can cost megabytes and over half a second on a large cluster, and would
	// otherwise sit on the critical path of every menu draw. On a custom resource
	// page the entry must be present and open, and the catalog is warm anyway.
	if !s.onACustomKind(current) {
		// Deferred is the fact the contract carries (navGroup.Deferred); Lazy is the
		// address that fills it.
		return []navGroup{{
			Label:    customNavLabel,
			Lazy:     customNavURL(name, current),
			Deferred: true,
		}}
	}

	return []navGroup{s.customNavBody(ctx, name, current)}
}

// customNavBody builds the heading with its contents: what the deferred fetch
// answers and what a custom resource page carries inline, in one function so the
// two cannot differ.
func (s *Server) customNavBody(ctx context.Context, name, current string) navGroup {
	const heading = customNavLabel

	client, err := s.clusters.Client(name)
	if err != nil {
		return navGroup{Label: heading, Note: firstLine(err.Error()), Open: true}
	}
	definitions, err := s.custom.Definitions(ctx, client)
	if err != nil {
		return navGroup{Label: heading, Note: firstLine(err.Error()), Open: true}
	}
	// The project's own kinds are filed under their headings (projectnav.go), so a
	// kind is offered once.
	all := len(definitions)
	definitions = thirdParty(definitions)
	if all > 0 && len(definitions) == 0 {
		// It serves some, filed in the headings above: no sentence here.
		return navGroup{Label: heading, Open: true}
	}
	served := len(definitions)
	definitions, left := s.listableDefinitions(ctx, name, definitions)
	return customNavGroup(name, current, served, definitions, left)
}

// customNavGroup is the heading for the kinds the menu keeps, out of served the
// cluster serves, with left of them left out because this console may not list
// them. Separate from customNavBody so a test can hold it without a cluster.
func customNavGroup(name, current string, served int, definitions []customresources.Definition, left int) navGroup {
	const heading = customNavLabel

	if served > 0 && len(definitions) == 0 {
		// The in-cluster console may list none of them: an access note in the warning
		// tone, since serving no custom resources is a different fact.
		return navGroup{Label: heading, Open: true, AccessNote: noneListableNote}
	}
	if len(definitions) == 0 {
		// Said rather than left blank: a deferred heading with an empty body reads as a
		// failed load.
		return navGroup{Label: heading, Open: true,
			Note: "This cluster serves no custom resource kinds."}
	}

	var sub []navGroup
	index := map[string]int{}
	open := false
	for _, d := range definitions {
		link := navLink{
			Label:   d.Title(),
			Href:    pageURL(name, d.Slug(), "", "", listOrder{}),
			Current: d.Slug() == current,
			// The definition's `spec.scope`, the authority the list itself is read through.
			Namespaced: d.Namespaced,
			Aliases:    d.Aliases(),
		}
		at, seen := index[d.Group]
		if !seen {
			sub = append(sub, navGroup{Label: d.Group})
			at = len(sub) - 1
			index[d.Group] = at
		}
		sub[at].Links = append(sub[at].Links, link)
		if link.Current {
			// Both levels open, so a followed link never lands on a folded-away entry.
			sub[at].Open = true
			open = true
		}
	}
	group := navGroup{Label: heading, Sub: sub, Open: open}
	if left > 0 {
		// Said, and first, so a kind looked for and not found is not taken for one the
		// cluster does not serve.
		group.AccessNote = leftOutNote(left)
	}
	return group
}

// grantedClause is what a hidden kind needs. It names "the identity it reads
// with", which is true wherever the console runs: its ServiceAccount in a
// cluster, the kubeconfig's identity on a desktop.
const grantedClause = "the identity it reads with has to be granted the read on "

// noneListableNote is the access note of a cluster whose every custom resource
// kind this console may not list.
const noneListableNote = "This console may list none of the custom resource kinds this cluster serves: " +
	grantedClause + "them."

// leftOutNote says how many custom resource kinds the in-cluster menu left out,
// and what would show them.
func leftOutNote(left int) string {
	if left == 1 {
		return "One kind this console may not list is not shown: " + grantedClause + "it."
	}
	return strconv.Itoa(left) + " kinds this console may not list are not shown: " + grantedClause + "them."
}

// onACustomKind reports whether the kind on screen is one of the cluster's own,
// asked of resources.Lookup rather than guessed from a dot in the slug.
func (s *Server) onACustomKind(current string) bool {
	if current == "" {
		return false
	}
	_, ok := resources.Lookup(current)
	return !ok
}

// customNavURL addresses the deferred group. `url.Values.Encode` escapes a
// cluster name carrying an ampersand as a parameter.
func customNavURL(cluster, current string) string {
	params := url.Values{}
	params.Set("cluster", cluster)
	if current != "" {
		params.Set("current", current)
	}
	return "/fragments/menu/custom?" + params.Encode()
}

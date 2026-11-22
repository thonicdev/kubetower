package web

// The Kubernetes project's own kinds beyond the typed ones, filed in the menu
// headings where a person looks for them. Discovery says which the cluster serves
// (the API server's own, such as VolumeAttachments, and the project's CRDs, such
// as the Gateway API); each is appended to its group's heading (project.go),
// opens on the generic custom resource list, and is left out of the Custom
// Resources heading so no kind is offered twice. Called from `Server.nav`.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/customresources"
	"github.com/thonicdev/kubetower/internal/resources"
)

// projectNavWait bounds what a first menu draw on a cold cluster waits for
// discovery. Past it the menu is drawn without these kinds and says it is pending
// (the menu's discovery state); the answer keeps being fetched and later draws are served
// from it.
var projectNavWait = 3 * time.Second

// projectNav is what discovery says this cluster serves, for the menu: the
// project's kinds to file under their headings, the console's own kinds it does
// not serve, and the discovery state when that is not complete.
//
// A kind this console may not list is treated as a typed kind is: left out of
// the in-cluster menu by the same review, offered elsewhere. Only what discovery
// stated is hidden as unserved; a failed discovery hides nothing.
func (s *Server) projectNav(ctx context.Context, name string) (kinds []customresources.Definition, unserved []string, state *api.NavDiscovery) {
	client, err := s.clusters.Client(name)
	if err != nil {
		// The cluster cannot be had: every page says so already.
		return nil, nil, nil
	}
	wait, cancel := context.WithTimeout(ctx, projectNavWait)
	defer cancel()
	found, err := s.custom.ProjectKinds(wait, client)
	if err != nil {
		if errors.Is(err, customresources.ErrDiscoveryPending) {
			return nil, nil, &api.NavDiscovery{State: api.NavDiscoveryPending, Detail: "Discovering the kinds this cluster serves"}
		}
		return nil, nil, &api.NavDiscovery{State: api.NavDiscoveryFailed, Detail: firstLine(err.Error())}
	}
	kinds, _ = s.listableDefinitions(ctx, name, found.Kinds)
	if len(found.Unread) > 0 {
		state = &api.NavDiscovery{State: api.NavDiscoveryFailed, Detail: "Discovery could not read " + strings.Join(found.Unread, ", ")}
	}
	return kinds, found.Unserved, state
}

// withoutUnserved is hidden plus the console's own kinds this cluster does not
// serve, as a new map: hidden may be the access review's, shared with other menus.
func withoutUnserved(hidden map[string]bool, unserved []string) map[string]bool {
	if len(unserved) == 0 {
		return hidden
	}
	out := make(map[string]bool, len(hidden)+len(unserved))
	for kind, left := range hidden {
		out[kind] = left
	}
	for _, kind := range unserved {
		out[kind] = true
	}
	return out
}

// projectNavGroups appends each kind to its heading, in order. A heading left
// out of an in-cluster menu is added where kinds.go would put it, holding these
// alone. Separate from projectNav so a test can hold it without a cluster.
func projectNavGroups(name, current string, groups []navGroup, kinds []customresources.Definition) []navGroup {
	for _, d := range kinds {
		link := navLink{
			Label:      d.Title(),
			Href:       pageURL(name, d.Slug(), "", "", listOrder{}),
			Current:    d.Slug() == current,
			Namespaced: d.Namespaced,
			Aliases:    d.Aliases(),
		}
		at := -1
		for i := range groups {
			if groups[i].Label == d.Section {
				at = i
				break
			}
		}
		if at < 0 {
			at = headingAt(groups, d.Section)
			groups = slices.Insert(groups, at, navGroup{Label: d.Section})
		}
		groups[at].Links = append(groups[at].Links, link)
		if link.Current {
			groups[at].Open = true
		}
	}
	return groups
}

// headingAt is where a heading missing from groups goes: before the first
// heading kinds.go declares after it, or last.
func headingAt(groups []navGroup, label string) int {
	order := map[string]int{}
	for _, d := range resources.Kinds() {
		if _, seen := order[d.Group]; !seen && d.Group != "" {
			order[d.Group] = len(order)
		}
	}
	want, known := order[label]
	for i, g := range groups {
		if at, ok := order[g.Label]; known && ok && at > want {
			return i
		}
	}
	return len(groups)
}

// thirdParty is the CRDs the Custom Resources heading lists: those whose API group
// the Kubernetes project does not own.
func thirdParty(definitions []customresources.Definition) []customresources.Definition {
	out := make([]customresources.Definition, 0, len(definitions))
	for _, d := range definitions {
		if !customresources.ProjectGroup(d.Group) {
			out = append(out, d)
		}
	}
	return out
}

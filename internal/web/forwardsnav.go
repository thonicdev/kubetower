package web

// The menu's entry for the port-forwards this console holds. A forward is
// process state, not a Kubernetes kind (`GET /api/forwards` answers a plain
// array), so it is not a descriptor in `internal/resources/kinds.go`: a
// descriptor would offer a list read, a drawer and an access review that mean
// nothing for it. It is appended under Network in `Server.nav`, as Helm is
// beside Overview.
//
// The address `/c/{cluster}/port-forwards` is a page, which the front end lists
// in `NON_KIND_SEGMENTS` so the menu never reads it as a custom resource.
const (
	forwardsNavKey   = "port-forwards"
	forwardsNavLabel = "Port forwards"
	// forwardsNavGroup is the heading the entry joins: the label the Network
	// descriptors in `kinds.go` declare.
	forwardsNavGroup = "Network"
)

// offersForwards is whether this console opens port-forwards for the browser.
// Capabilities.portForward and the menu entry both read it, so the entry is drawn
// exactly where the forward operations are registered (`!InCluster &&
// !Replicated` in `internal/api/router.go`) and a forwarder was handed in.
func (s *Server) offersForwards() bool {
	return s.forwards != nil && !s.inCluster && !s.replicated
}

// withForwardsNav appends the entry to the Network heading, after the kinds
// already in it. A menu with no Network heading - every Network kind left out,
// which only the in-cluster console does, and it offers no forwards - gains one
// holding the entry alone rather than losing it.
func withForwardsNav(cluster, current string, groups []navGroup) []navGroup {
	link := navLink{
		Label:   forwardsNavLabel,
		Href:    clusterPath(cluster) + "/" + forwardsNavKey,
		Current: current == forwardsNavKey,
	}
	for at := range groups {
		if groups[at].Label != forwardsNavGroup {
			continue
		}
		groups[at].Links = append(groups[at].Links, link)
		if link.Current {
			groups[at].Open = true
		}
		return groups
	}
	return append(groups, navGroup{Label: forwardsNavGroup, Links: []navLink{link}, Open: link.Current})
}

package web

import (
	"context"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
)

// Where the console opens port-forwards, the menu's
// Network heading ends with an entry listing them - after every Network kind,
// at the address the front end mounts the page on.
func TestThePortForwardsEntryIsUnderNetwork(t *testing.T) {
	server := serverFor(t, false)
	server.forwards = forwardsFixture{}

	menu := navOf(t, server)
	network := groupHolding(menu, "/c/prod/services")
	if network == nil {
		t.Fatal("no group holds the Services list; the case measures nothing")
	}
	if network.Key != "network" {
		t.Errorf("Services is under %q, want network", network.Key)
	}
	if len(network.Items) < 2 {
		t.Fatalf("the Network group holds %d items; want its kinds and the entry", len(network.Items))
	}
	last := network.Items[len(network.Items)-1]
	if last.Text != "Port forwards" || last.Href != "/c/prod/port-forwards" {
		t.Errorf("the Network group ends with %+v, want Port forwards at /c/prod/port-forwards", last)
	}
	// Not a namespaced list: the page lists every namespace's forwards, and a
	// namespace carried onto it from another list would narrow nothing.
	if last.Namespaced {
		t.Error("the Port forwards entry is marked namespaced; it is not a kind's list")
	}
	// Once in the whole menu, so the jump box and the arrangement see one row.
	if n := countHref(menu, "/c/prod/port-forwards"); n != 1 {
		t.Errorf("the menu links to the forwards %d times, want once", n)
	}
}

// Where the console does not open forwards, the entry is not in the menu, and
// the capability says the same thing: the two read one answer. Under the server
// profile the three forward operations are not registered, whether or not a
// forward set was handed in, nor where several replicas share one address;
// with no forwarder there is nothing to list.
func TestThePortForwardsEntryFollowsTheCapability(t *testing.T) {
	cases := []struct {
		name       string
		inCluster  bool
		replicated bool
		forwards   Forwards
		want       bool
	}{
		{"the desktop with a forwarder", false, false, forwardsFixture{}, true},
		{"the desktop with no forwarder", false, false, nil, false},
		{"the server profile, a forwarder handed in anyway", true, false, forwardsFixture{}, false},
		{"the server profile", true, false, nil, false},
		// The routes are not registered for several replicas whatever is
		// handed in, so neither is the entry nor the capability.
		{"several replicas, a forwarder handed in anyway", false, true, forwardsFixture{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := serverFor(t, c.inCluster)
			server.forwards = c.forwards
			server.replicated = c.replicated

			listed := countHref(navOf(t, server), "/c/prod/port-forwards") > 0
			offered := (apiProfile{server}).Capabilities(nil).PortForward
			if listed != c.want {
				t.Errorf("the menu lists Port forwards: %v, want %v", listed, c.want)
			}
			if listed != offered {
				t.Errorf("the menu lists Port forwards: %v, and capabilities.portForward is %v; they must agree", listed, offered)
			}
		})
	}
}

// A menu with no Network heading still gets the entry, under a heading of its
// own, rather than a console that opens forwards and offers no way to list them.
func TestThePortForwardsEntryMakesItsHeadingWhenThereIsNone(t *testing.T) {
	groups := withForwardsNav("prod", "", []navGroup{{Label: "Workloads"}})
	if len(groups) != 2 || groups[1].Label != "Network" || len(groups[1].Links) != 1 {
		t.Fatalf("groups are %+v, want Workloads then a Network heading holding the entry", groups)
	}
	if got := groups[1].Links[0].Href; got != "/c/prod/port-forwards" {
		t.Errorf("the entry links to %q", got)
	}
}

func navOf(t *testing.T, server *Server) api.NavMenu {
	t.Helper()
	menu, fail := apiClusters{server}.Nav(context.Background(), "prod")
	if fail != nil {
		t.Fatalf("the menu failed: %+v", fail.Reason)
	}
	return menu
}

func groupHolding(menu api.NavMenu, href string) *api.NavGroup {
	for at := range menu.Groups {
		for _, item := range menu.Groups[at].Items {
			if item.Href == href {
				return &menu.Groups[at]
			}
		}
	}
	return nil
}

func countHref(menu api.NavMenu, href string) int {
	n := 0
	var walk func(groups []api.NavGroup)
	walk = func(groups []api.NavGroup) {
		for _, group := range groups {
			for _, item := range group.Items {
				if item.Href == href {
					n++
				}
			}
			walk(group.Groups)
		}
	}
	walk(menu.Groups)
	return n
}

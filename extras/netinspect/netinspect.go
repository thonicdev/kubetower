// Package netinspect is the Network Inspector: which NetworkPolicies select a
// pod, what they let it send and receive, and whether a flow from one pod to
// another would pass - read from the rules alone, behind its own per-cluster
// switch.
//
// **Rules, never traffic.** Everything here is computed from the policy
// objects - Kubernetes NetworkPolicies, Cilium's, Calico's (with its tiers) and
// the AdminNetworkPolicies - what would be allowed, not what passed. Every answer
// carries `mode: "rules"`, and the page draws it as a rule (dashed, "allowed
// by the rule"), so nobody reads YAML and believes they saw a flow. Observed
// traffic (Hubble, Calico) is a different source and a different mode.
//
// Every read goes through Host.List, reviewed for the person behind the
// request; never Host.Client, which reads with the console's own
// credentials. A person who may list pods in two namespaces gets an analysis
// of those two, marked partial. Nothing is kept between requests: each one
// reads, builds its model, answers and drops it. Nothing here writes.
package netinspect

import (
	"fmt"
	"net/http"

	"github.com/thonicdev/kubetower/extension"
)

// Key is `netinspect`: the page's URL segment, the KT_EXTRAS name and the key
// the per-cluster switch is stored under.
const Key = "netinspect"

// NetInspect is the extension `cmd/kubetower` registers. It holds nothing.
type NetInspect struct{}

// New is the constructor `cmd/kubetower` calls.
func New() NetInspect { return NetInspect{} }

func (NetInspect) Key() string   { return Key }
func (NetInspect) Label() string { return "Network Inspector" }

// Summary is the catalog entry.
func (NetInspect) Summary() string {
	return "See which policies select each pod - Kubernetes NetworkPolicies, Cilium and Calico policies, " +
		"AdminNetworkPolicies - which peers and ports their rules allow, and whether one pod would reach another, " +
		"from the rules, not from observed traffic. Where Hubble UI or Whisker runs, a button opens it."
}

// Needs says that the rules mean something only where the network plugin
// applies them.
func (NetInspect) Needs() string {
	return "NetworkPolicies (or Cilium or Calico policies), and a network plugin that applies them (Cilium, Calico, Antrea, kindnet…; flannel alone does not); Hubble UI or Whisker, optionally, for the buttons that open them"
}

// Legacy is empty: the key has never been anything else.
func (NetInspect) Legacy() []string { return nil }

// Routes are reads, every one a GET: nothing here changes a cluster or
// dials anything but the API server.
func (x NetInspect) Routes(h extension.Host) []extension.Route {
	return []extension.Route{
		{Method: http.MethodGet, Pattern: "/{$}", Handler: overviewHandler(h)},
		{Method: http.MethodGet, Pattern: "/verdict", Handler: verdictHandler(h)},
		// A pod's panel: its tab and its shortcut tile; and the Pods list's
		// column, one read for the whole list.
		{Method: http.MethodGet, Pattern: "/pods/{namespace}/{name}", Handler: podHandler(h)},
		{Method: http.MethodGet, Pattern: "/cells", Handler: cellsHandler(h)},
		// Observed traffic from Hubble Relay: a POST, because it opens a
		// tunnel recorded as the console acting for the person, and a
		// cross-site link must not be able to make a browser open one.
		{Method: http.MethodPost, Pattern: "/flows", Handler: flowsHandler(h)},
		// The flow UIs (Hubble UI, Whisker) this cluster can open.
		{Method: http.MethodGet, Pattern: "/uis", Handler: uisHandler(h)},
	}
}

// Suggest offers the extra where the cluster holds a NetworkPolicy the person
// may list (extension.Suggester), with how many and where: one listing,
// capped, and nothing else read.
func (NetInspect) Suggest(r *http.Request, h extension.Host, cluster string) (string, error) {
	page, err := h.List(r, cluster, extension.ListQuery{GVR: policiesGVR, Limit: suggestCap})
	if err != nil {
		return "", err
	}
	if len(page.Items) == 0 {
		return "", nil
	}
	namespaces := map[string]bool{}
	for i := range page.Items {
		namespaces[page.Items[i].GetNamespace()] = true
	}
	floor := ""
	if !page.Complete {
		floor = "At least "
	}
	return fmt.Sprintf("%s%d %s in %d %s", floor, len(page.Items), plural(len(page.Items), "NetworkPolicy", "NetworkPolicies"),
		len(namespaces), plural(len(namespaces), "namespace", "namespaces")), nil
}

// suggestCap bounds the suggestion's listing: past it the count is a floor.
const suggestCap = 500

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

var _ extension.Suggester = NetInspect{}

// Component names what Suggest looks for (extension.Component).
func (NetInspect) Component() string { return "NetworkPolicies" }

var _ extension.Component = NetInspect{}

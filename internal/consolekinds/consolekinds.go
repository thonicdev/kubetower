// Package consolekinds is the console's address for a kind: the segment after
// `/c/{cluster}/` that its menu links.
//
// The kinds internal/resources types by hand are linked by their bare plural
// (`/c/{cluster}/deployments`); every other kind as `plural.group`, whoever
// owns it. A core-group kind has no group to add, so it is its bare plural too.
//
// It is a leaf so that the extension contract (`extension.KindSegment`) can
// delegate to it without pulling the console's list code into every extra and
// into `boundary/`. internal/resources keeps its own table of the same kinds
// (listquestion.go); TestTheTypedKindsAreTheConsolesOwn fails when they differ.
package consolekinds

// Kind is one kind the console types by hand, by API group and resource.
type Kind struct{ Group, Resource string }

// typed maps resource to group. Each plural is typed for one group only, which
// is what lets the bare plural be its address.
var typed = map[string]string{
	"pods":                            "",
	"deployments":                     "apps",
	"statefulsets":                    "apps",
	"daemonsets":                      "apps",
	"replicasets":                     "apps",
	"jobs":                            "batch",
	"cronjobs":                        "batch",
	"replicationcontrollers":          "",
	"services":                        "",
	"ingresses":                       "networking.k8s.io",
	"endpoints":                       "",
	"endpointslices":                  "discovery.k8s.io",
	"ingressclasses":                  "networking.k8s.io",
	"networkpolicies":                 "networking.k8s.io",
	"configmaps":                      "",
	"secrets":                         "",
	"resourcequotas":                  "",
	"limitranges":                     "",
	"horizontalpodautoscalers":        "autoscaling",
	"poddisruptionbudgets":            "policy",
	"priorityclasses":                 "scheduling.k8s.io",
	"runtimeclasses":                  "node.k8s.io",
	"leases":                          "coordination.k8s.io",
	"mutatingwebhookconfigurations":   "admissionregistration.k8s.io",
	"validatingwebhookconfigurations": "admissionregistration.k8s.io",
	"serviceaccounts":                 "",
	"clusterroles":                    "rbac.authorization.k8s.io",
	"roles":                           "rbac.authorization.k8s.io",
	"clusterrolebindings":             "rbac.authorization.k8s.io",
	"rolebindings":                    "rbac.authorization.k8s.io",
	"persistentvolumeclaims":          "",
	"persistentvolumes":               "",
	"storageclasses":                  "storage.k8s.io",
	"nodes":                           "",
	"namespaces":                      "",
	"events":                          "",
}

// Typed is every kind the console types by hand, in no particular order.
func Typed() []Kind {
	out := make([]Kind, 0, len(typed))
	for resource, group := range typed {
		out = append(out, Kind{Group: group, Resource: resource})
	}
	return out
}

// TypedGroup is the API group of the kind the console types by hand at this
// plural, and whether it types one there.
func TypedGroup(plural string) (string, bool) {
	group, ok := typed[plural]
	return group, ok
}

// IsTyped is whether the console types this group's resource by hand.
func IsTyped(group, resource string) bool {
	g, ok := typed[resource]
	return ok && g == group
}

// Segment is the kind's address under `/c/{cluster}/`, as the console's menu
// links it: the bare plural for a kind the console types by hand or a kind of
// the core group, `plural.group` for every other.
// It is a spelling, not a promise that a page answers there: an untyped kind
// opens only where the cluster's catalog resolves the segment.
func Segment(group, plural string) string {
	if group == "" || IsTyped(group, plural) {
		return plural
	}
	return plural + "." + group
}

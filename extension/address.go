package extension

import "github.com/thonicdev/kubetower/internal/consolekinds"

// KindSegment is a kind's segment in the console's address,
// `/c/{cluster}/{segment}`, spelled as the console's own menu links it: the
// bare plural for a kind the console types by hand (`deployments`) or a kind of
// the core group (`podtemplates`), and `plural.group` for every other kind
// (`applications.argoproj.io`, `apiservices.apiregistration.k8s.io`). group and
// plural are the API's own, as in a GroupVersionResource.
//
// It is the spelling, not a promise that a page answers there. A kind the
// console types by hand (TypedKind) always has its page; any other opens only
// where the cluster's catalog (CRDRegistry.Lookup) resolves the segment to
// that kind, and some never do (events.k8s.io's Events, the metrics APIs, a
// vendor's aggregated API). A page that links a row asks Lookup and leaves the
// row unlinked when the answer is another kind or none.
//
// It is on the contract because which kinds the console types is the console's
// to say: a guess from the group's name links an APIService to
// `/c/{cluster}/apiservices`, which the console answers "unknown kind". It
// delegates to `internal/consolekinds`, held to the console's list of kinds by
// a test, so there is one table.
func KindSegment(group, plural string) string {
	return consolekinds.Segment(group, plural)
}

// TypedKind is the API group of the kind the console types by hand at this
// plural, and whether it types one there: `deployments` is apps's, `pods` the
// core group's. Its page needs no catalog, and its bare plural names no other
// group's kind in the console's address - not metrics.k8s.io's `pods`, not a
// vendor CRD's `nodes`.
func TypedKind(plural string) (group string, ok bool) {
	return consolekinds.TypedGroup(plural)
}

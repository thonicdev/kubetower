package extension

import "net/http"

// Suggester is an Extension that can tell whether a cluster runs what it reads,
// so the console can suggest turning it on there: every extra is off by
// default, and where the cluster runs the software an extension is about, the
// console says so in one line and offers the switch. Optional: an extension
// that does not implement it is never suggested.
//
// The core asks only when the answer can be acted on: the extension is off on
// that cluster, the per-cluster switch is registered (not several replicas),
// KT_EXTRAS does not pin it, the chart's values allow it under the server
// profile, and the person signed in may turn it on there (the `use`
// permission, under the server profile).
//
// What Suggest reads goes through h - Host.List, reviewed for the person behind
// r - so somebody who may not list the thing it looks for is suggested nothing
// rather than told it exists. Nothing is dialled. It must be cheap: it runs
// when a cluster's pages are opened.
type Suggester interface {
	Extension
	// Suggest answers one short sentence of evidence - what was found, for
	// instance "2 Redis or Valkey Services in 2 namespaces" - or "" when the
	// cluster runs nothing the extension reads. An error is not a suggestion:
	// the console draws nothing for it, as for "".
	Suggest(r *http.Request, h Host, cluster string) (string, error)
}

// Component is an optional addition to a Suggester: the short name of what it
// looks for ("cert-manager"). Where a Suggester answers "" with no error - the
// cluster runs nothing it reads - and has a Component, Settings > Extra keeps
// its tile, grayed, with "<component> absent" and the switch still pressable.
// A Suggester with no Component is simply not drawn as absent.
type Component interface {
	Component() string
}

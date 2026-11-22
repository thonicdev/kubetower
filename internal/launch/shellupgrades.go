package launch

import "net/http"

// shellUpgrades is the three shell endpoints, reduced to what registering them
// needs.
//
// An interface rather than *terminal.Terminal so the decision below can be
// tested without a cluster, a pseudo-terminal or a Claude binary.
type shellUpgrades interface {
	ShellHandler() http.Handler
	ExecHandler() http.Handler
	ClaudeHandler() http.Handler
}

// mountShellUpgrades registers the three upgrades, and under the server profile
// registers none of them.
//
// A function rather than lines inline in the start-up so a test can walk the
// router and establish the absence, as the server-mode threat model asks.
//
// `/kt-api/terminal` serves a pseudo-terminal on the machine this process runs
// on: in a pod, the pod, whose environment carries the mounted ServiceAccount
// token. Such a shell reads every Secret the ServiceAccount can, whatever the
// chart's rbac.exec and rbac.write say: RBAC bounds the pod, and this shell is
// the pod.
//
// `/kt-api/claude` is that shell with an assistant in front, on one person's
// subscription; the server profile ships it absent rather than refused.
//
// `/kt-api/exec` goes with them here, though the SPA's `exec` socket channel
// stays: that channel reaches the kubelet and is bounded by `pods/exec`, while
// this address is unused by the SPA and costs nothing to remove.
func mountShellUpgrades(mux *http.ServeMux, shells shellUpgrades, inCluster bool) {
	if inCluster {
		return
	}
	mux.Handle("/kt-api/terminal", shells.ShellHandler())
	mux.Handle("/kt-api/exec", shells.ExecHandler())
	// Claude Code in the dock, inside the session guard like every
	// long-lived endpoint: it hands out a shell on a production cluster.
	mux.Handle("/kt-api/claude", shells.ClaudeHandler())
}

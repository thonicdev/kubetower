package api

import "net/http"

// ChannelID names one of the socket channels `api/websocket.md` describes.
type ChannelID string

const (
	// ChannelTerminal is a shell on the machine running the console, scoped
	// to a cluster's kubeconfig context. Gated by capabilities.localShell and
	// disabled in the server profile, where it would run with the process's
	// credentials rather than the caller's.
	ChannelTerminal ChannelID = "terminal"
	// ChannelExec is an interactive shell inside a pod, through the exec
	// subresource. Refused on a cluster marked read-only: `exec` is one of the
	// local shell's mutating verbs, and the fence must hold at every door.
	ChannelExec ChannelID = "exec"
	// ChannelDebug is a shell in a debug container: an ephemeral container added
	// to a running pod, then exec-ed into. Offered wherever exec is, since its
	// shell is an exec; refused on a cluster marked read-only, where adding a
	// container is a change.
	ChannelDebug ChannelID = "debug"
	// ChannelNodeShell is a shell on a node, after a plan has been agreed.
	ChannelNodeShell ChannelID = "nodeshell"
	// ChannelAssistant is a long-lived control channel, absent (not disabled)
	// in the server profile, which is why Offers exists.
	ChannelAssistant ChannelID = "assistant"
)

// Channels is the socket surface, and the one place this contract hands a raw
// ResponseWriter to a backend: an upgrade is a handshake then a conversation,
// with no response object to build.
//
// Offers is separate from the Serve methods because absent and disabled
// differ: a channel not offered is never registered, so its address is the
// ordinary JSON 404. It is read once, when the router is built.
//
// Each Serve method still checks its own condition (read-only fence, session
// ceiling, plan token) at the upgrade, before or right after the handshake,
// and says why in words: a close with no reason tells nothing. The capability
// flag is only the front end's hint.
type Channels interface {
	// Offers says whether this build serves the channel at all.
	Offers(id ChannelID) bool

	// ServeTerminal upgrades to a shell on this machine, scoped to cluster.
	ServeTerminal(w http.ResponseWriter, r *http.Request, cluster string)
	// ServeExec upgrades to a shell inside a pod. The backend reads and
	// validates the pod, namespace, container and command off the query.
	ServeExec(w http.ResponseWriter, r *http.Request, cluster string)
	// ServeDebug upgrades to a shell in a new debug container. The backend reads
	// and validates the pod, namespace, image and target off the query.
	ServeDebug(w http.ResponseWriter, r *http.Request, cluster string)
	// ServeNodeShell upgrades to a shell on node, taken from the agreed plan
	// and never from the URL.
	ServeNodeShell(w http.ResponseWriter, r *http.Request, cluster, node string)
	// ServeAssistant upgrades to the assistant control channel.
	ServeAssistant(w http.ResponseWriter, r *http.Request)
}

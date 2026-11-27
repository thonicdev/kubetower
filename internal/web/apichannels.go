package web

import (
	"net/http"

	"github.com/thonicdev/kubetower/internal/api"
)

// apiChannels registers the socket channels advertised in
// `api/openapi.yaml` (`x-channels`) onto `internal/terminal`, which owns the
// sessions. Offers is asked once, when the router is built, and reads the same
// facts `apiProfile.Capabilities` reports, so a capability and its route cannot
// disagree. A channel the profile does not offer is never registered: absent,
// not disabled behind a condition.
type apiChannels struct{ s *Server }

// Offers reports whether this build serves a channel at all. Every channel is
// off on a build without a terminal layer (Server.claude is nil-valid), since a
// nil handler on a socket route would panic rather than 404.
func (c apiChannels) Offers(id api.ChannelID) bool {
	if c.s.claude == nil {
		return false
	}
	if c.s.inCluster {
		// The server profile. Terminal and node shell are a pseudo-terminal on the
		// machine running the console, the pod, and inherit its ServiceAccount
		// credentials, whatever RBAC says about the console's own controls. The
		// assistant would run with those credentials on one person's subscription.
		// Exec stays: it reaches the kubelet through remotecommand, bounded by the
		// ServiceAccount's `pods/exec`, which the chart leaves off by default.
		switch id {
		case api.ChannelTerminal, api.ChannelNodeShell, api.ChannelAssistant:
			return false
		}
	}
	switch id {
	case api.ChannelTerminal:
		// A pseudo-terminal on this machine. False only where `CreatePseudoConsole` is
		// missing (Windows older than 10 1809). `capabilities.localShell` reads the same
		// PTYSupported, so the contract and the channel cannot disagree.
		return c.s.claude.PTYSupported()
	case api.ChannelExec:
		// The kubelet allocates the pseudo-terminal in the pod, so not behind
		// PTYSupported. The read-only fence is per cluster and stays in the handler.
		return true
	case api.ChannelDebug:
		// Exactly where exec is: its shell is an exec, into a container the API
		// server adds, bounded by the ServiceAccount's `pods/ephemeralcontainers`
		// and `pods/exec` under the server profile. The read-only fence stays in
		// the handler, as for exec.
		return true
	case api.ChannelNodeShell:
		return true
	case api.ChannelAssistant:
		// The same fact the front end is told.
		return c.s.claude.HasClaude()
	}
	return false
}

func (c apiChannels) ServeTerminal(w http.ResponseWriter, r *http.Request, cluster string) {
	c.withCluster(c.s.claude.FramedShellHandler(), w, r, cluster)
}

func (c apiChannels) ServeExec(w http.ResponseWriter, r *http.Request, cluster string) {
	c.withCluster(c.s.claude.FramedExecHandler(), w, r, cluster)
}

func (c apiChannels) ServeDebug(w http.ResponseWriter, r *http.Request, cluster string) {
	c.withCluster(c.s.claude.FramedDebugHandler(), w, r, cluster)
}

// ServeNodeShell takes the node from the caller, which has taken it from the
// plan the person agreed to. Nothing here reads it off the query.
func (c apiChannels) ServeNodeShell(w http.ResponseWriter, r *http.Request, cluster, node string) {
	c.s.claude.FramedNodeShell(w, withCluster(r, cluster), node)
}

func (c apiChannels) ServeAssistant(w http.ResponseWriter, r *http.Request) {
	// No cluster in this address: `internal/terminal` reads one off the query.
	c.s.claude.FramedAssistantHandler().ServeHTTP(w, r)
}

func (c apiChannels) withCluster(h http.Handler, w http.ResponseWriter, r *http.Request, cluster string) {
	h.ServeHTTP(w, withCluster(r, cluster))
}

// withCluster puts the cluster where `internal/terminal.clusterParam` looks for
// it. `r.PathValue` is filled by whichever ServeMux matched, and the terminal
// layer sits behind a nested one; relying on two routers sharing a wildcard name
// would break silently on a rename, pointing a shell at the wrong cluster.
func withCluster(r *http.Request, cluster string) *http.Request {
	query := r.URL.Query()
	query.Set("cluster", cluster)
	next := r.Clone(r.Context())
	url := *r.URL
	url.RawQuery = query.Encode()
	next.URL = &url
	return next
}

package api

import (
	"net/http"
	"strings"
)

// The socket channels, which differ from every other route here in two ways.
//
// They are registered without a method. A handshake is a GET carrying
// `Connection: Upgrade`; pinning the method gains nothing, and a pattern more
// specific in path but more general in method than an existing route makes
// ServeMux panic at startup (`api/websocket.md` warns of it). `ws` is a literal
// segment no other operation uses, and ServeHTTP's miss path still answers
// JSON 404 for any other address.
//
// A channel this profile does not offer is not registered at all (the server
// profile has no assistant): `Channels.Offers` is asked once, when the router
// is built, so the address is an ordinary 404 and a walk of the router proves
// the absence.
func (a *API) channels(m *http.ServeMux) {
	if a.deps.Channels == nil {
		return
	}
	if a.deps.Channels.Offers(ChannelTerminal) {
		m.HandleFunc("/api/c/{cluster}/ws/terminal", a.upgrade(a.channelTerminal))
	}
	if a.deps.Channels.Offers(ChannelExec) {
		m.HandleFunc("/api/c/{cluster}/ws/exec", a.upgrade(a.channelExec))
	}
	if a.deps.Channels.Offers(ChannelDebug) {
		m.HandleFunc("/api/c/{cluster}/ws/debug", a.upgrade(a.channelDebug))
	}
	if a.offersNodeShell() {
		m.HandleFunc("/api/c/{cluster}/ws/nodeshell", a.upgrade(a.channelNodeShell))
	}
	if a.deps.Channels.Offers(ChannelAssistant) {
		m.HandleFunc("/api/ws/assistant", a.upgrade(a.channelAssistant))
	}
}

// offersNodeShell is whether this build serves the node-shell channel; the
// channel, the preview and the setting all ask it, so no profile offers to
// plan or configure a shell whose address does not exist.
func (a *API) offersNodeShell() bool {
	return a.deps.Channels != nil && a.deps.Channels.Offers(ChannelNodeShell)
}

// upgrade is the mutation guard's socket half.
//
// A browser cannot set a header on a WebSocket handshake
// (`Sec-WebSocket-Protocol` is negotiated content, not a marker), so the
// header half of guard is unavailable here. The Origin half matters more: the
// same-origin policy does not cover a handshake, so any page could open a
// socket carrying the session cookie, with only `SameSite=Lax` in the way of a
// root-equivalent shell. An Origin that is sent is checked on every upgrade and
// a mismatch refused before anything is allocated.
//
// A missing Origin is accepted, matching internal/terminal: browsers always
// send one, so its absence means a non-browser client (curl, wscat) that a
// hostile page cannot drive and that still needed a live session.
//
// The refusal is an HTTP 403 `application/problem+json` before the upgrade,
// not a socket that opens and closes, which a front end would read as an
// exited container and retry.
func (a *API) upgrade(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && !a.originAllowed(r, origin) {
			refused(w, ProblemOriginRejected, "origin "+origin+" is not this console")
			return
		}
		next(w, r)
	}
}

func (a *API) channelTerminal(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	a.deps.Channels.ServeTerminal(w, r, name)
}

func (a *API) channelExec(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	a.deps.Channels.ServeExec(w, r, name)
}

func (a *API) channelDebug(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	a.deps.Channels.ServeDebug(w, r, name)
}

// channelNodeShell is the one place a plan token crosses protocols: the
// preview (`POST /api/c/{cluster}/nodeshell/preview`) answers a token and this
// channel's address, and the token binds the agreed sentence to the pod then
// created.
//
//  1. Spent exactly once: `planStore.take` deletes it whether or not the
//     upgrade succeeds, so a failed upgrade leaves nothing to replay.
//  2. A missing, expired or other-cluster token is a 403 and no socket is
//     opened, established before the handshake (`api/websocket.md`).
//  3. The node comes from the plan, never the URL, so a shell agreed on one
//     node cannot be opened on another by editing the query.
func (a *API) channelNodeShell(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		refused(w, ProblemPlanExpired,
			"a node shell is opened from an agreed plan; this upgrade carries no token")
		return
	}
	plan, ok, err := a.plans.take(r.Context(), token, planNodeShell)
	if err != nil {
		planStoreFailure(w, err)
		return
	}
	if !ok {
		refused(w, ProblemPlanExpired,
			"this plan has expired or has already been used; preview the node shell again")
		return
	}
	if plan.cluster != name {
		refused(w, ProblemPlanChanged,
			"this plan was agreed for "+plan.cluster+" and this upgrade is for "+name)
		return
	}
	// `PreviewNodeShell` puts the node in the single target; reading it back
	// is the re-derivation, not a second source.
	if len(plan.facts.Targets) != 1 || plan.facts.Targets[0].Name == "" {
		refused(w, ProblemPlanChanged, "this plan names no node")
		return
	}
	a.deps.Channels.ServeNodeShell(w, r, name, plan.facts.Targets[0].Name)
}

func (a *API) channelAssistant(w http.ResponseWriter, r *http.Request) {
	a.deps.Channels.ServeAssistant(w, r)
}

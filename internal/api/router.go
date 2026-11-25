package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/internal/extras"
)

// Prefix is where every operation of this contract lives, except the probes.
//
// Exported for internal/auth: an expired session under this prefix answers
// `401 application/problem+json`, never an HTML login page, because the front
// end routes to its login screen on the status and a fetch shows an HTML page
// as a blank screen.
const Prefix = "/api/"

// The probe paths sit outside Prefix: they answer a kubelet, and keep their
// address when the contract changes.
const (
	LivenessPath = "/healthz"

	// LaunchPath and HandoffPath are the desktop's launch routes, outside /api/
	// and registered by Mount only where SignIn offers them.
	LaunchPath    = "/auth/launch"
	HandoffPath   = "/auth/handoff"
	ReadinessPath = "/readyz"
)

// requestMarker is the header every mutation carries. The session is a cookie,
// which a cross-site form post would carry; a custom header cannot be set
// cross-origin without a preflight. Paired with an Origin check, because
// either alone has a known hole.
const requestMarker = "X-KubeTower-Request"

// maxBody is the ceiling on a request body that is not a manifest. The
// manifest write has a larger one, above the four megabytes the schema allows.
const (
	maxBody         = 1 << 20
	maxManifestBody = 5 << 20
)

// API is the router. Build it with New and mount it with Mount.
type API struct {
	deps  Deps
	mux   *http.ServeMux
	plans *planStore
	now   func() time.Time
}

// New builds the router over whatever the caller could supply. Every group in
// Deps may be nil; see Deps.
func New(deps Deps) *API {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	a := &API{deps: deps, mux: http.NewServeMux(), plans: newPlanStore(now, deps.Shared), now: now}
	if deps.InCluster {
		// One person's share of the plan store, where the store is shared.
		a.plans.shared()
	}
	a.routes()
	return a
}

// Mount registers this contract on the process's router.
// Mount registers this contract on the process's router: the probes on their
// literal paths, everything else under one prefix served by this package's mux,
// so a miss under /api/ answers JSON 404 rather than the front end's index.html
// or net/http's plain-text 405.
func (a *API) Mount(mux *http.ServeMux) {
	mux.Handle(Prefix, a)
	mux.HandleFunc("GET "+LivenessPath, a.liveness)
	mux.HandleFunc("GET "+ReadinessPath, a.readiness)
	// The launch routes exist only where offered: the one-time window code on
	// the desktop, the handoff where a launcher secret was installed (loopback
	// desktop), never under --incluster. Unregistered rather than refusing, so
	// a walk of the router shows it.
	if s := a.deps.SignIn; s != nil {
		if s.OffersLaunch() {
			mux.HandleFunc("GET "+LaunchPath, s.LaunchSignIn)
		}
		if s.AcceptsHandoffs() {
			// Every method, so a GET reaches the handler's 405 rather than the
			// front end's catch-all serving the SPA shell.
			mux.HandleFunc(HandoffPath, s.Handoff)
		}
	}
}

// ServeHTTP delegates to the API's mux, and answers the miss itself.
//
// Every answer under /api/ is `Cache-Control: no-store`, set here once: these
// answers carry Helm values, ConfigMap data and environment values, which a
// browser's disk cache would keep past sign-out. One line at the door, because
// a per-handler header goes missing from the next handler written.
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if _, pattern := a.mux.Handler(r); pattern == "" {
		// No operation at this address or by this method: JSON either way, so a
		// client can tell a missing endpoint from a logged-out session.
		notFound(w, "no operation at "+r.Method+" "+r.URL.Path)
		return
	}
	a.mux.ServeHTTP(w, r)
}

func (a *API) routes() {
	m := a.mux

	// Session. The two public mutations still carry the marker.
	//
	// The password route exists only where a password is offered (an identity
	// provider removes it unless the local account is kept), unregistered rather
	// than refusing, so it answers as `/api/never-existed` does.
	if a.deps.SignIn == nil || a.deps.SignIn.OffersPassword() {
		m.HandleFunc("POST /api/auth/login", a.guard(a.login))
	}
	m.HandleFunc("POST /api/auth/logout", a.guard(a.logout))
	m.HandleFunc("GET /api/auth/methods", a.getSignInMethods)
	// Single sign-on, only where a provider is configured. Both are
	// navigations, so neither can carry the marker, and neither needs it: start
	// changes nothing a forged navigation could use, and the callback requires
	// the `state` held in an HttpOnly cookie. See internal/auth.
	if a.deps.SignIn != nil && a.deps.SignIn.OffersSSO() {
		m.HandleFunc("GET /api/auth/oidc/start", a.deps.SignIn.StartSSO)
		m.HandleFunc("GET /api/auth/oidc/callback", a.deps.SignIn.FinishSSO)
	}
	m.HandleFunc("GET /api/session", a.getSession)
	m.HandleFunc("GET /api/capabilities", a.getCapabilities)

	// Clusters, navigation, extensions.
	m.HandleFunc("GET /api/clusters", a.listClusters)
	m.HandleFunc("GET /api/c/{cluster}/nav", a.getNav)
	m.HandleFunc("GET /api/c/{cluster}/nav/{group}", a.getNavGroup)
	// The literal segment wins over {group}; no menu group is keyed `occupancy`.
	m.HandleFunc("GET /api/c/{cluster}/nav/occupancy", a.getNavOccupancy)
	m.HandleFunc("GET /api/c/{cluster}/extensions", a.listExtensions)
	// A POST because it runs the probe now rather than reading a known state.
	m.HandleFunc("POST /api/c/{cluster}/probe", a.guard(a.probeCluster))
	// Registered only where extras.ProfileOf (shared with the start-up line and
	// internal/web) says the switch is kept: not with several replicas (each
	// write is one pod's file, see Deps.Replicated), and not under the server
	// profile without a persistent state directory (Deps.StateEphemeral), where
	// a forgotten choice would turn a page off for everybody. Under the server
	// profile with one replica the switch is everybody's on that cluster, so it
	// is reviewed and audited: KT_EXTRAS says which extras may be on, the `use`
	// verb says who may switch them. An extension's settings follow the same
	// rule.
	if extras.ProfileOf(a.deps.InCluster, a.deps.Replicated, a.deps.StateEphemeral).Switchable() {
		// The suggestion only makes sense where the switch exists.
		m.HandleFunc("GET /api/c/{cluster}/extensions/suggestions", a.listExtensionSuggestions)
		m.HandleFunc("PUT /api/c/{cluster}/extensions/{key}", a.guard(a.setExtensionEnabled))
		m.HandleFunc("PUT /api/c/{cluster}/extensions/{key}/settings/{setting}", a.guard(a.setExtensionSetting))
	}

	// Settings.
	m.HandleFunc("GET /api/settings", a.getSettings)
	// No settings write and no clear where the state directory is not the
	// console's to keep: not persistent (Deps.StateEphemeral), or shared by
	// several replicas (Deps.Replicated). And none under the server profile,
	// where the archive's settings are everybody's and emptying the archive
	// would be one person's gesture for all; KT_ARCHIVE_* decides there.
	kept := !a.deps.StateEphemeral && !a.deps.Replicated && !a.deps.InCluster
	if kept {
		m.HandleFunc("PUT /api/settings", a.guard(a.saveSettings))
		m.HandleFunc("POST /api/settings/clear", a.guard(a.clearState))
	}
	// Only where the node-shell channel exists: the setting configures that
	// shell and the preview mints its plan. See offersNodeShell.
	if a.offersNodeShell() && kept {
		m.HandleFunc("PUT /api/settings/nodeshell", a.guard(a.saveNodeShellSettings))
	}
	// Not under the server profile, which never checks: its image tag decides.
	// Guarded because it spends GitHub's rate limit on the person's behalf.
	if a.deps.Updates != nil && !a.deps.InCluster {
		m.HandleFunc("POST /api/settings/update-check", a.guard(a.checkUpdates))
	}
	// The desktop's download and install (#47): never under the server profile,
	// and only where internal/launch built an installer.
	if a.deps.Installer != nil && !a.deps.InCluster {
		m.HandleFunc("GET /api/settings/update", a.getUpdateInstall)
		m.HandleFunc("PUT /api/settings/update", a.guard(a.saveUpdateInstall))
		m.HandleFunc("POST /api/settings/update/download", a.guard(a.downloadUpdate))
		m.HandleFunc("POST /api/settings/update/install", a.guard(a.installUpdate))
		m.HandleFunc("POST /api/settings/update/open-folder", a.guard(a.openUpdateFolder))
	}
	m.HandleFunc("GET /api/health", a.getHealth)

	// Resources.
	m.HandleFunc("GET /api/c/{cluster}/namespaces", a.listNamespaces)
	m.HandleFunc("GET /api/c/{cluster}/r/{kind}", a.listResources)
	m.HandleFunc("GET /api/c/{cluster}/r/{kind}/{name}", a.getClusterScopedResource)
	m.HandleFunc("GET /api/c/{cluster}/r/{kind}/{namespace}/{name}", a.getNamespacedResource)
	m.HandleFunc("GET /api/c/{cluster}/helm", a.listHelmReleases)
	m.HandleFunc("GET /api/c/{cluster}/helm/{namespace}/{name}", a.getHelmRelease)
	m.HandleFunc("GET /api/c/{cluster}/helm/{namespace}/{name}/values", a.revealHelmValues)
	m.HandleFunc("GET /api/c/{cluster}/helm/{namespace}/{name}/notes", a.revealHelmNotes)

	// Panels.
	m.HandleFunc("GET /api/c/{cluster}/overview", a.getOverview)
	m.HandleFunc("GET /api/c/{cluster}/metrics", a.getMetricsPanel)
	m.HandleFunc("GET /api/c/{cluster}/changes", a.getChangesPanel)
	m.HandleFunc("GET /api/c/{cluster}/env", a.revealEnvValue)

	// Edit.
	m.HandleFunc("GET /api/c/{cluster}/data", a.getDataPanel)
	m.HandleFunc("POST /api/c/{cluster}/data", a.guard(a.applyData))
	m.HandleFunc("POST /api/c/{cluster}/data/preview", a.guard(a.previewData))
	m.HandleFunc("GET /api/c/{cluster}/manifest", a.getManifest)
	m.HandleFunc("POST /api/c/{cluster}/manifest/preview", a.guard(a.previewManifest))
	m.HandleFunc("POST /api/c/{cluster}/manifest/apply", a.guard(a.applyManifest))
	m.HandleFunc("GET /api/c/{cluster}/create/template", a.getCreateTemplate)
	m.HandleFunc("POST /api/c/{cluster}/create/preview", a.guard(a.previewCreate))
	m.HandleFunc("POST /api/c/{cluster}/create/apply", a.guard(a.applyCreate))

	// Act.
	m.HandleFunc("POST /api/c/{cluster}/actions/preview", a.guard(a.previewAction))
	m.HandleFunc("POST /api/c/{cluster}/actions", a.guard(a.runAction))
	m.HandleFunc("POST /api/c/{cluster}/bulk/preview", a.guard(a.previewBulk))
	m.HandleFunc("POST /api/c/{cluster}/bulk", a.guard(a.runBulk))
	if a.offersNodeShell() {
		m.HandleFunc("POST /api/c/{cluster}/nodeshell/preview", a.guard(a.previewNodeShell))
	}

	// Prerequisites: an operator an extra page reads from, installed through
	// the write door above. On every profile, as the create and the verbs
	// are; handlers_prereq.go says why.
	m.HandleFunc("GET /api/c/{cluster}/prereq/{key}", a.getPrereq)
	m.HandleFunc("POST /api/c/{cluster}/prereq/{key}/preview", a.guard(a.previewPrereq))
	m.HandleFunc("POST /api/c/{cluster}/prereq/{key}/install", a.guard(a.installPrereq))

	// The kinds column's choices, per person, on every profile: a store that
	// keeps none answers not_available_in_this_profile.
	m.HandleFunc("GET /api/me/kinds", a.getKindPreferences)
	m.HandleFunc("PUT /api/me/kinds", a.guard(a.setKindPreferences))

	// Preferences: twelve writes, one cell each, desktop only.
	m.HandleFunc("GET /api/preferences", a.getPreferences)
	// None of the writes with several replicas (each is one pod's file); the
	// read stays, so every replica draws the same defaults. None under the
	// server profile either: they write the console's arrangement, not the
	// person's, so the first rename or hide would apply to everybody, and Hide
	// could hide the profile's only cluster. Absent rather than refusing, so a
	// router walk shows it; the front end keeps columns in the browser instead
	// (`Capabilities.preferences`).
	if !a.deps.InCluster && !a.deps.Replicated {
		m.HandleFunc("POST /api/preferences/rail/bar", a.guard(a.setRailBar))
		m.HandleFunc("POST /api/preferences/rail/assign", a.guard(a.setRailAssign))
		m.HandleFunc("POST /api/preferences/rail/move", a.guard(a.setRailMove))
		m.HandleFunc("POST /api/preferences/rail/place", a.guard(a.setRailPlace))
		m.HandleFunc("POST /api/preferences/rail/hide", a.guard(a.setRailHide))
		m.HandleFunc("POST /api/preferences/rail/reveal", a.guard(a.setRailReveal))
		m.HandleFunc("POST /api/preferences/rail/nameplate", a.guard(a.setRailNameplate))
		m.HandleFunc("POST /api/preferences/columns/size", a.guard(a.setColumnSize))
		m.HandleFunc("POST /api/preferences/columns/place", a.guard(a.setColumnPlace))
		m.HandleFunc("POST /api/preferences/columns/show", a.guard(a.setColumnShow))
		m.HandleFunc("POST /api/preferences/columns/density", a.guard(a.setColumnDensity))
		m.HandleFunc("POST /api/preferences/columns/reset", a.guard(a.resetColumns))
		// What's new (#80): the release whose highlights this desktop has seen.
		m.HandleFunc("POST /api/preferences/seen-version", a.guard(a.setSeenVersion))
	}

	// Dock.
	//
	// The desktop terminal is registered only under the desktop profile,
	// decided when the router is built so a walk of the mux shows it; a
	// refusal inside the handler would be invisible to that walk (server-mode
	// threat model, §9.1). The condition is the profile, not
	// `capabilities.localShell`, which is also false on a desktop that cannot
	// open a pseudo-terminal, where the route exists and refuses.
	if !a.deps.InCluster {
		m.HandleFunc("POST /api/dock/terminals", a.guard(a.openDesktopTerminal))
	}
	m.HandleFunc("GET /api/logs/containers", a.listLogContainers)
	m.HandleFunc("GET /api/logs", a.streamLogs)
	// Nor the forwards under the server profile: a forward binds a port in this
	// process and hands back `127.0.0.1:<port>`, which in a browser talking to
	// a pod is the person's own machine. There `capabilities.portForward` is
	// false and the page draws the `kubectl port-forward` command instead. A
	// forward is also a listener in one pod, so none with several replicas.
	if !a.deps.InCluster && !a.deps.Replicated {
		m.HandleFunc("GET /api/forwards", a.listForwards)
		m.HandleFunc("POST /api/forwards", a.guard(a.openForward))
		m.HandleFunc("DELETE /api/forwards/{id}", a.guard(a.closeForward))
		// A forward's flow UI in a desktop window (#65): only where one can
		// be drawn.
		if a.deps.ToolWindows != nil {
			m.HandleFunc("POST /api/forwards/{id}/window", a.guard(a.openForwardWindow))
		}
	}

	// The socket channels are registered without a method and depend on the
	// profile; see handlers_channels.go.
	a.channels(m)
}

// --- The mutation guard -----------------------------------------------------

// guard is the custom header and the Origin check, in front of every mutation.
// Either alone has a hole: a cookie API with no custom header is forgeable
// cross-site, and an Origin check alone trusts a header the server did not
// set. The socket channels do the same at the upgrade (api/websocket.md),
// since the same-origin policy does not cover a WebSocket handshake.
func (a *API) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(requestMarker) != "1" {
			refused(w, ProblemOriginRejected,
				"every mutation carries "+requestMarker+": 1")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !a.originAllowed(r, origin) {
			refused(w, ProblemOriginRejected, "origin "+origin+" is not this console")
			return
		}
		next(w, r)
	}
}

// RefuseForged holds a route served outside this router, an extension's, to
// guard's rules, and reports whether it refused: a foreign Origin is refused
// whatever the method, and any method but GET, HEAD and OPTIONS also needs the
// marker. A WebSocket handshake is a GET, so it meets the Origin check alone.
// Without it an extension's POST or socket route would be served to any page
// carrying the cookie.
func RefuseForged(w http.ResponseWriter, r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" && !sameHost(r, origin) {
		refused(w, ProblemOriginRejected, "origin "+origin+" is not this console")
		return true
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	if r.Header.Get(requestMarker) != "1" {
		refused(w, ProblemOriginRejected, "every mutation carries "+requestMarker+": 1")
		return true
	}
	return false
}

func (a *API) originAllowed(r *http.Request, origin string) bool {
	if a.deps.AllowOrigin != nil {
		return a.deps.AllowOrigin(r, origin)
	}
	return sameHost(r, origin)
}

// sameHost is the default origin policy: same origin only, since one process
// serves both this API and the front end.
func sameHost(r *http.Request, origin string) bool {
	host := r.Host
	if host == "" {
		return false
	}
	trimmed := origin
	if i := strings.Index(trimmed, "://"); i >= 0 {
		trimmed = trimmed[i+3:]
	}
	return trimmed == host
}

// --- Writing ----------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		internalProblem(w, "the response could not be encoded: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeOK(w http.ResponseWriter, v any) { writeJSON(w, http.StatusOK, v) }

func writeNoContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// --- Reading ----------------------------------------------------------------

// decode reads a body strictly: `DisallowUnknownFields` holds the schemas'
// `additionalProperties: false` at run time. A preference body is one cell,
// and silently ignoring a second would accept what the schema refuses.
func decode(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		badRequest(w, "the body could not be read: "+err.Error())
		return false
	}
	return true
}

// requireAll reports a missing member by name. A nil pointer is how this
// package tells "not said" from "said false", which matters on every boolean
// preference.
func requireAll(w http.ResponseWriter, named ...any) bool {
	for i := 0; i+1 < len(named); i += 2 {
		name, _ := named[i].(string)
		switch v := named[i+1].(type) {
		case *bool:
			if v == nil {
				badRequest(w, name+" is required")
				return false
			}
		case *int:
			if v == nil {
				badRequest(w, name+" is required")
				return false
			}
		case *string:
			if v == nil {
				badRequest(w, name+" is required")
				return false
			}
		case string:
			if v == "" {
				badRequest(w, name+" is required")
				return false
			}
		}
	}
	return true
}

// --- Path and query parameters ---------------------------------------------

// The spec's patterns: a route accepting what the schema refuses is reachable
// by a hand-written client but not a generated one.
var (
	kindPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,126}$`)
	// namespacePattern is a DNS-1123 label. Stricter than the spec's
	// NamespaceQuery (maxLength 253), and held only where the value is written
	// into a document: getCreateTemplate.
	namespacePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	// The spec's ExtensionKey, the registry's own pattern (internal/extras).
	extensionPattern = extras.KeyPattern
	rangePattern     = regexp.MustCompile(`^[0-9]{1,4}[mhd]$`)
)

// cluster reads the {cluster} segment and establishes that it exists. An
// unknown name is UnknownCluster, the answer the extras' gate gives too.
//
// net/http has already decoded the segment once (a context is often an ARN
// with colons and slashes); decoding again would change a name containing `%`.
func (a *API) cluster(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("cluster")
	if name == "" || len(name) > 253 {
		badRequest(w, "a cluster is required")
		return "", false
	}
	if a.deps.Clusters != nil && !a.deps.Clusters.Known(name) {
		UnknownCluster(w, name)
		return "", false
	}
	return name, true
}

// clusterQuery is the same name from the query string, for endpoints about an
// object rather than a page.
func (a *API) clusterQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.URL.Query().Get("cluster")
	if name == "" || len(name) > 253 {
		badRequest(w, "a cluster is required")
		return "", false
	}
	if a.deps.Clusters != nil && !a.deps.Clusters.Known(name) {
		UnknownCluster(w, name)
		return "", false
	}
	return name, true
}

func kindOf(w http.ResponseWriter, value string) (string, bool) {
	if !kindPattern.MatchString(value) {
		badRequest(w, "a kind is a discovery plural, optionally group-qualified")
		return "", false
	}
	return value, true
}

func nameOf(w http.ResponseWriter, value string) (string, bool) {
	if value == "" || len(value) > 253 {
		badRequest(w, "a name is required")
		return "", false
	}
	return value, true
}

// objectFromQuery reads the kind/namespace/name triple that the panel endpoints
// take in the query string.
func (a *API) objectFromQuery(w http.ResponseWriter, r *http.Request, cluster string) (ObjectRef, bool) {
	q := r.URL.Query()
	kind, ok := kindOf(w, q.Get("kind"))
	if !ok {
		return ObjectRef{}, false
	}
	name, ok := nameOf(w, q.Get("name"))
	if !ok {
		return ObjectRef{}, false
	}
	ns := q.Get("namespace")
	if len(ns) > 253 {
		badRequest(w, "namespace is too long")
		return ObjectRef{}, false
	}
	return ObjectRef{Cluster: cluster, Kind: kind, Namespace: ns, Name: name}, true
}

func queryInt(q map[string][]string, key string, def, min, max int) int {
	values, ok := q[key]
	if !ok || len(values) == 0 || values[0] == "" {
		return def
	}
	n, err := strconv.Atoi(values[0])
	if err != nil || n < min || n > max {
		return def
	}
	return n
}

func queryBool(q map[string][]string, key string, def bool) bool {
	values, ok := q[key]
	if !ok || len(values) == 0 || values[0] == "" {
		return def
	}
	b, err := strconv.ParseBool(values[0])
	if err != nil {
		return def
	}
	return b
}

// --- Mapping a write's error onto a status ---------------------------------

// fail turns a backend error into the problem the spec declares for it, in one
// place, so a refusal never reaches a client as a 500: a Kubernetes 403 must
// arrive with its message intact.
func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		notFound(w, err.Error())
	case errors.Is(err, ErrInvalid):
		badRequest(w, err.Error())
	case errors.Is(err, ErrReadOnly):
		refused(w, ProblemReadOnly, err.Error())
	case errors.Is(err, ErrNotInProfile):
		refused(w, ProblemNotInProfile, err.Error())
	case errors.Is(err, ErrRBACNotInProfile):
		refused(w, ProblemRBACNotInProfile, err.Error())
	case errors.Is(err, ErrRefused):
		refused(w, ProblemRBACDenied, err.Error())
	case errors.Is(err, ErrConflict):
		WriteProblem(w, http.StatusConflict, ProblemConflict, "Conflict", err.Error())
	case errors.Is(err, ErrLimitReached):
		limitReached(w, err.Error())
	case errors.Is(err, ErrUnrecorded):
		WriteProblem(w, http.StatusServiceUnavailable, ProblemAccessUnreviewable, "Audit record could not be written", err.Error())
	case errors.Is(err, ErrUnreviewable):
		WriteProblem(w, http.StatusServiceUnavailable, ProblemAccessUnreviewable, "Access could not be reviewed", err.Error())
	case errors.Is(err, ErrNotSent):
		WriteProblem(w, http.StatusServiceUnavailable, ProblemNotSent, "Not sent", err.Error())
	case errors.Is(err, ErrUnanswered):
		WriteProblem(w, http.StatusGatewayTimeout, ProblemNotAnswered, "Not answered", err.Error())
	case errors.Is(err, ErrNotSaved):
		WriteProblem(w, http.StatusInternalServerError, ProblemNotSaved, "Applied, not saved", err.Error())
	default:
		internalProblem(w, err.Error())
	}
}

// missing is the answer when a group of Deps is nil and the operation is not
// enveloped. A 500, not an empty success: "this build has no preferences store"
// and "you have no preferences" must stay different answers.
func missing(w http.ResponseWriter, what string) {
	internalProblem(w, "this build was assembled without "+what)
}

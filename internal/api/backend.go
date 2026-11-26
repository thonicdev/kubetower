package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/thonicdev/kubetower/internal/sharedstate"
)

// The interfaces this package needs, declared here rather than imported.
//
// Every one of them is implemented in internal/web/apimount.go over the domain
// packages the console reads — internal/resources, internal/detail,
// internal/helm, internal/health and the preference stores. That split is the
// point: **this package owns the contract and nothing else.** It knows what a
// response must look like and what a state means; it does not know how a cluster
// is reached, and it must not learn, or the two interfaces diverge on the day
// somebody fixes a bug in one of them.
//
// Two conventions run through all of it:
//
//   - **An enveloped read returns `(payload, *Failure)`, never `error`.** An
//     error says only that something went wrong, and the whole argument of
//     `api/errors.md` is that *which way* it went wrong is the answer. A backend
//     handing back a bare error forces this package to guess, and a guess here is
//     the collapse arriving through the back door.
//   - **A write returns `error`**, wrapped around one of the sentinels below, so
//     that a refusal, a limit and a conflict reach the right status without this
//     package matching on message text.

// The sentinels. A backend wraps one of these; anything else is a 500.
var (
	// ErrNotFound — no such cluster, object or route.
	ErrNotFound = errors.New("not found")
	// ErrInvalid — the request itself could not be understood.
	ErrInvalid = errors.New("invalid request")
	// ErrRefused — this console refuses the caller this operation: a read-only
	// fence, a profile that disables it, or the cluster's own authorization
	// saying no to the whole request.
	ErrRefused = errors.New("refused")
	// ErrReadOnly is ErrRefused with the reason named, because the person needs
	// to know it is the guardrail and not their credentials.
	ErrReadOnly = errors.New("read-only")
	// ErrNotInProfile — the running profile does not offer this at all.
	ErrNotInProfile = errors.New("not available in this profile")
	// ErrRBACNotInProfile — a change to RBAC (roles, cluster roles and their
	// bindings) under the server profile, which this version does not make:
	// refused before any review, whoever asks. A 403 with its own code,
	// `rbac_not_in_profile`, never `rbac_denied`: nobody's rights said no, and
	// no RoleBinding changes the answer.
	ErrRBACNotInProfile = errors.New("RBAC changes are not available in the server profile in this version")
	// ErrConflict — the object moved under the write.
	ErrConflict = errors.New("conflict")
	// ErrLimitReached — a process-wide limit is full.
	ErrLimitReached = errors.New("limit reached")
	// ErrUnreviewable — the console could not ask the cluster whether the
	// person may do this, so it did not. A 503 and not a 403: nobody said no,
	// and the difference sends the reader to a different person (the cluster's
	// reachability, not their RBAC). See internal/access.
	ErrUnreviewable = errors.New("access could not be reviewed")
	// ErrUnrecorded — the console could not write the audit record of the act
	// first, so it did not make it. The same 503 and problem code as
	// ErrUnreviewable, which is what the contract has for "the console could
	// not act", under a title that says which of the two it was.
	ErrUnrecorded = errors.New("the audit record could not be written")
	// ErrNotSent — the write never left the console: its time ran out before
	// it was handed to the client, the cluster's rate limit held it, or no
	// connection to the API server was made. A 503 with its own code,
	// `not_sent`: nobody said no and nothing reached the cluster, so it is safe
	// to run again; never the 403 a refusal is.
	ErrNotSent = errors.New("not sent")
	// ErrUnanswered — the write left the console and no answer came back: the
	// deadline cut it, or the connection ended after it was written; or the
	// API server answered that it timed out, so it may still happen. A 504
	// with its own code, `not_answered`: whether it happened is not known, so
	// it is **not** safe to run again without a look; never the 403 a refusal
	// is.
	ErrUnanswered = errors.New("not answered")
	// ErrNotSaved — a write to the console's own preferences **took effect**
	// and could not be written down, so it will be gone at the next start
	// (statefile.ErrNotSaved, which this package does not import). A 500 with
	// its own code, `not_saved`: never the 400 a refusal is, because a client
	// that reads "nothing happened" stops half-way through a gesture the
	// console has already half-applied.
	ErrNotSaved = errors.New("not saved")
)

// Deps is everything the router is given. Every field may be nil, and a nil
// field is answered honestly rather than by pretending: an enveloped read says
// `absent`, and anything else says so with a problem object. Nil happens in
// tests and in a build assembled without a piece; it does not happen in the
// binary, where internal/launch fills all of it.
type Deps struct {
	Sessions Sessions
	// SignIn is how a person may sign in. Nil means the shared password alone and
	// no single sign-on, which is what every test that does not care gets. **It
	// decides registrations**: the password route exists only where a password
	// is offered, and the two single sign-on routes only where an identity
	// provider is configured.
	SignIn   SignIn
	Profile  Profile
	Clusters Clusters
	Settings SettingsStore
	Prefs    PreferenceStore
	// KindPrefs is the person's choices about the kinds column, kept per
	// person.
	KindPrefs KindPreferenceStore
	Reads     Reads
	Acts      Acts
	// Prereqs installs an operator an extra page reads from. Nil answers the
	// read `absent` and the preview and install with a 500, as Acts does.
	Prereqs  Prereqs
	Dock     Dock
	Channels Channels
	Probe    Probe
	// Updates is Settings › Infos' Check now. Nil where
	// nothing checks, and the route is then not registered.
	Updates UpdateCheck
	// Installer downloads and installs an update on the desktop (#47). Nil
	// where nothing installs, and the routes are then not registered.
	Installer UpdateInstaller
	// ToolWindows draws a forward's flow UI in a desktop window (#65). Nil
	// where no native window can be drawn, and the route is then not
	// registered.
	ToolWindows ToolWindows
	// MetricsSources is each cluster's metrics source (Settings › General).
	// Nil where there is no metrics layer, and the routes are then not
	// registered; its PUT only where the settings writes are.
	MetricsSources MetricsSources

	// Now is the clock, held here so the plan store's expiry can be tested at
	// its boundaries rather than by waiting.
	Now func() time.Time

	// InCluster is the server profile: a console shared by several people
	// inside a cluster. **It decides registrations, not refusals** — see
	// `router.go` and `channels`.
	//
	// A field of its own rather than a capability read back off Profile.
	// `Capabilities.LocalShell` is also false on a **desktop that cannot open a
	// pseudo-terminal** — an old Windows — where the address exists and answers
	// `ErrNotInProfile`. Keying registration off that field would remove the
	// route on those machines too. What decides here is the profile; what
	// `LocalShell` decides is what the front end draws.
	InCluster bool

	// Shared is the store every replica of this console shares - here, the
	// confirmation plans between a preview and its act, which are two requests
	// a Service may send to two pods. Nil means one in this process's memory,
	// which is all one replica needs.
	Shared sharedstate.Store

	// Replicated is the deployment saying this process is **one of several
	// behind one address** (`KT_REPLICATED`, which the chart sets whenever
	// `replicaCount` is above one). Like InCluster, **it decides
	// registrations**: what only one pod can hold is not offered at all.
	//
	//   - The preference writes - the rail, the nameplates, the column
	//     layouts, the density - and the per-cluster extension switches. Each
	//     is a file in the pod's own state directory and a copy in its memory,
	//     read once at start: a width saved through one pod would be drawn one
	//     request in two. So every replica draws the defaults, and nobody's
	//     change is kept by one pod and not the other.
	//   - Port-forwards. A forward is a listener inside one pod, and the next
	//     request - the list, the stop - lands wherever the Service sends it.
	//
	// Sharing them properly is its own piece of work, and per person rather
	// than per console once it is shared; see the server-mode
	// threat model. Until that exists, **absent, not half-working**.
	Replicated bool

	// StateEphemeral is the deployment saying the state directory does not
	// survive a restart - the chart's `persistence.enabled: false`, handed on
	// as `KT_STATE_PERSISTENT=false`. **Like InCluster, it decides
	// registrations, not refusals**: `PUT /api/settings` writes only the
	// archive's settings, `PUT /api/settings/nodeshell` an image kept in the
	// same directory, and `POST /api/settings/clear` discards what that
	// directory keeps, so without persistence none of the three is registered.
	//
	// The polarity is the zero value's: false is persistent, which is every
	// desktop.
	StateEphemeral bool

	// AllowOrigin decides the server-side Origin check every mutation passes.
	// Nil means same-origin only, worked out from the request's own Host, which
	// is what a single process serving both halves from one origin wants.
	AllowOrigin func(r *http.Request, origin string) bool
}

// Sessions is the front door. Implemented by internal/auth, which owns the
// cookie, the rate limit and the failure delay; this package only routes to it
// and applies the mutation guard in front.
type Sessions interface {
	// LoginJSON answers 204 with the cookie set, or 401, or 429. It never
	// returns a token in the body: one cross-site scripting flaw must not be
	// able to exfiltrate a credential that reaches a cluster.
	LoginJSON(w http.ResponseWriter, r *http.Request)
	// LogoutJSON answers 204 whether or not a session was live. A logout that
	// fails on an already-expired session leaves a front end unable to reach the
	// login screen.
	LogoutJSON(w http.ResponseWriter, r *http.Request)
	// Current is the live session, or false.
	Current(r *http.Request) (Session, bool)
}

// SignIn is what the sign-in page offers and the single sign-on flow behind it.
// Implemented by internal/auth.
//
// **The two Offers methods are asked once, when the router is built**, and never
// per request. That is what makes "the route is not registered" a property of the
// router rather than of a handler: a deployment that configures an
// identity provider has no `POST /api/auth/login` at all, which `mux.Handler`
// can establish and a refusal inside the handler could not (threat model §9.1).
type SignIn interface {
	// OffersPassword is whether the shared local account exists.
	OffersPassword() bool
	// OffersSSO is whether an identity provider is configured - configured, not
	// answering. An unreachable provider keeps its routes, so that the start
	// route can say it is unreachable rather than 404.
	OffersSSO() bool
	// Methods is the sign-in page's state now, including whether the provider
	// is answering.
	Methods() SignInMethods
	// StartSSO answers a navigation with a redirect to the identity provider,
	// or back to the sign-in page saying why it could not.
	StartSSO(w http.ResponseWriter, r *http.Request)
	// FinishSSO is the redirect URI: it verifies what came back and answers
	// with the session cookie and a redirect, or with a redirect to the sign-in
	// page carrying a fixed reason code - never the provider's own words.
	FinishSSO(w http.ResponseWriter, r *http.Request)
	// OffersLaunch is whether windows the application opens arrive signed in
	// through a one-time code: the desktop profile, where the shared account
	// exists. LaunchSignIn spends such a code.
	OffersLaunch() bool
	LaunchSignIn(w http.ResponseWriter, r *http.Request)
	// AcceptsHandoffs is whether a second launch may be handed a code by
	// proving the launcher secret: the loopback desktop only. Handoff answers it.
	AcceptsHandoffs() bool
	Handoff(w http.ResponseWriter, r *http.Request)
}

// Profile is what the running build offers.
//
// **Handed the request**, though no member reads it. Whether the person may open
// a shell is asked on each pod's page (`DetailPage.shell`), because asked about
// every namespace at once it answers no to a person granted some. The request
// stays in the signature so an answer about the person can be added here without
// changing the interface.
type Profile interface {
	Capabilities(r *http.Request) Capabilities
}

// Clusters is the estate: which clusters exist, what each one's menu is, and
// which extensions are on.
type Clusters interface {
	// List carries each cluster's own state. The envelope's state covers reading
	// the configuration — a KUBECONFIG naming five files, one of which cannot be
	// parsed, is `unreadable` here, and that is a different fact from five
	// clusters that are all down.
	List(ctx context.Context) ([]Cluster, *Failure)
	// Known reports whether this context name exists at all, so an unknown one is
	// a 404 rather than an envelope about nothing.
	Known(cluster string) bool
	// Nav is the menu, including the custom-resource groups that come from
	// discovery — so a cluster whose discovery is refused shows its built-in menu
	// and says why the rest is missing, rather than showing a menu that is
	// quietly short.
	Nav(ctx context.Context, cluster string) (NavMenu, *Failure)
	// NavGroup fills a group Nav answered with `deferred: true` — the custom
	// resource heading, which costs a list of the cluster's
	// CustomResourceDefinitions and is therefore not built for a disclosure that
	// is closed.
	//
	// A key naming no deferred group is `absent` rather than an error: the client
	// asked for a group this menu does not fetch separately, which is a fault in
	// this product and not a fact about the cluster.
	NavGroup(ctx context.Context, cluster, group string) (NavGroup, *Failure)
	// NavOccupancy is, per kind of the menu, whether it holds an object the
	// person behind ctx may see. A kind whose answer could
	// not be had is OccupancyUnknown, never none.
	NavOccupancy(ctx context.Context, cluster string) (NavOccupancy, *Failure)
	// Extensions is what this build carries, each with its state on this cluster.
	Extensions(ctx context.Context, cluster string) ([]Extension, *Failure)
	// ExtensionSuggestions is every extension that is off on this cluster, could
	// be turned on here, and says the cluster runs what it reads
	// (extension.Suggester). What each reads to say so is
	// reviewed for the person behind r, which is why it takes the request.
	ExtensionSuggestions(r *http.Request, cluster string) ([]ExtensionSuggestion, *Failure)
	// SetExtension enables or disables one extension on one cluster. Under the
	// server profile it is reviewed as the `use` verb on the extension for the
	// person ctx carries, and recorded in the audit log: a refusal is ErrRefused.
	SetExtension(ctx context.Context, cluster, key string, enabled bool) error
	// SetExtensionSetting writes one setting of one extension on one cluster
	// (extension.Configurable), on SetExtension's terms. A key or a setting
	// this build does not carry is ErrNotFound.
	SetExtensionSetting(ctx context.Context, cluster, key, setting string, value bool) error
	// SetExtensionText writes one text setting of one extension on one
	// cluster, or forgets it when text is empty. As SetExtensionSetting, plus
	// ErrInvalid for a value the setting refuses or one its environment
	// variable decides, and ErrNotInProfile under the server profile.
	SetExtensionText(cluster, key, setting, text string) error
	// Probe asks this one cluster again, now, and answers its entry as List
	// would draw it. The overview's Refresh, without a round over every cluster.
	Probe(ctx context.Context, cluster string) (Cluster, *Failure)
}

// SettingsStore is the console's own file. It cannot be unreachable, so nothing
// here is enveloped.
type SettingsStore interface {
	Settings() Settings
	SaveGeneral(SettingsWrite) (Settings, error)
	SaveNodeShell(NodeShellSettings) (NodeShellSettings, error)
	// PreviewClear says what discarding this scope would lose.
	PreviewClear(scope string) (PlanFacts, error)
	// Clear discards it.
	Clear(scope string) (ActOutcome, error)
	// Health is the console's own diagnosis, per cluster and per dependency.
	// Health takes `force` because the page has two gestures and they cost
	// different things: a navigation reads the probe's last round, and *Check
	// again* runs one. Serving the cached answer to the second would tell a person
	// who has just signed in that the sign-in failed.
	Health(ctx context.Context, force bool) (HealthReport, *Failure)
}

// KindPreferenceStore keeps each person's choices about the kinds column.
// Per person: the person is the one ctx carries, and a
// store that cannot tell who that is refuses rather than keep one choice for
// everybody.
//
// A store that keeps nothing answers ErrNotInProfile to both, and
// Capabilities.KindPreferences false, and a client keeps the choices in its
// own browser.
type KindPreferenceStore interface {
	KindPreferences(ctx context.Context) (KindPreferences, error)
	// SetKindPreferences replaces them, and answers what was kept.
	SetKindPreferences(ctx context.Context, prefs KindPreferences) (KindPreferences, error)
}

// UpdateCheck asks GitHub now, past the update check's cache, and answers once
// the check has settled. It makes no request where the check is off or does not
// apply; that is decided below it, in internal/update, before any socket.
type UpdateCheck interface {
	CheckUpdates(ctx context.Context) SettingsUpdate
}

// UpdateInstaller is the desktop's two clicks: Download, then Restart to
// update. A refusal (turned off, nothing to install here, busy, nothing
// verified) is ErrConflict with its reason.
type UpdateInstaller interface {
	UpdateInstall() UpdateInstall
	// SetInstallUpdates writes the automatic setting; an empty when keeps the
	// moment set. The handler refuses an unknown when before it.
	SetInstallUpdates(on bool, when string) (UpdateInstall, error)
	DownloadUpdate() (UpdateInstall, error)
	InstallUpdate(ctx context.Context) (UpdateInstall, error)
	// OpenUpdateFolder shows the verified file's folder, in the manual phase.
	OpenUpdateFolder() (UpdateInstall, error)
}

// ToolWindows is the desktop's windows for a forward's flow UI. Available is
// whether the console is in the native window now; OpenToolWindow refuses with
// ErrConflict where it is not, ErrNotFound for a forward that is not live and
// ErrInvalid for a tool or a path it does not take.
type ToolWindows interface {
	Available() bool
	OpenToolWindow(forwardID, tool, path string, closeTunnel bool) error
}

// PreferenceStore is the read half and the twelve writes.
//
// One method per intention rather than one `Save(Preferences)`, and that is the
// whole point: a body carrying the whole object means somebody unticking one
// column re-asserts every other, freezing the grid at whatever the page happened
// to hold. The shape enforces what a comment would only ask for.
//
// Thirteen methods over twelve routes, because two of the routes carry two arms
// each — `/rail/move` and `/columns/place` each accept a neighbor **or** an
// absolute position, and the two reach the store by different paths. One method
// taking both and deciding between them would put that choice somewhere no
// implementation can be held to it.
type PreferenceStore interface {
	Preferences() Preferences

	// SetRailBar puts one hotbar on screen, 1-based. Not a show/hide: the rail
	// has several hotbars and this selects which is drawn.
	SetRailBar(bar int) error
	SetRailAssign(cluster string, bar int) error
	// SetRailMove puts a tile at an absolute position within its hotbar.
	SetRailMove(cluster string, order int) error
	// SetRailStep nudges a tile one place along its hotbar: -1 up, +1 down. The
	// arm the two arrows send, and the one that needs no agreement about the
	// list the browser last saw.
	SetRailStep(cluster string, delta int) error
	// SetRailPlace drops a tile beside another, on that other one's hotbar.
	SetRailPlace(cluster, target string, after bool) error
	SetRailHidden(cluster string, hidden bool) error
	// SetRailNameplate takes the write itself, because it is the one preference
	// with optional members: a nil member was not said and is left alone, which
	// no positional signature can express.
	SetRailNameplate(RailNameplateWrite) error

	SetColumnWidth(kind, column string, width *int) error
	// SetColumnOrder moves a column to an absolute position.
	SetColumnOrder(kind, column string, order int) error
	// SetColumnPlace moves a column beside another one.
	SetColumnPlace(kind, column, target string, after bool) error
	SetColumnVisible(kind, column string, visible bool) error
	// SetColumnDensity carries no kind: row height is one setting for every
	// table, which is where the store keeps it.
	SetColumnDensity(density string) error
	// SetSeenVersion records the release whose "What's new" was seen (#80).
	SetSeenVersion(version string) error
	// ResetColumns forgets one scope of one kind's arrangement: "all", "widths",
	// "order" or "hidden". The scope is always one of the four by the time it
	// arrives — the handler defaults an absent one to "all" and refuses anything
	// else, so an implementation never sees "".
	ResetColumns(kind, scope string) error
}

// ListQuery is everything `listResources` was asked.
type ListQuery struct {
	Cluster   string
	Kind      string
	Namespace string
	Selector  string
	Search    string
	// Filters is the repeatable `filter` parameter, each `<column>:<value>` as
	// it was sent. Parsed where the columns are known.
	Filters []string
}

// ObjectRef names one object.
type ObjectRef struct {
	Cluster   string
	Kind      string
	Namespace string
	Name      string
}

// MetricsQuery is one object at one zoom level.
type MetricsQuery struct {
	ObjectRef
	Range  string
	Points int
}

// EnvQuery reveals one variable. The key travels here and the value only ever
// travels back in a body.
type EnvQuery struct {
	ObjectRef
	Container string
	Key       string
}

// Reads is every enveloped read that is about a cluster.
type Reads interface {
	// Namespaces is the selector every list page carries. Its own read rather
	// than a member of ListPage: the answer is the same for every kind on a
	// cluster and is cached, while a list payload is refetched on an interval.
	//
	// The reading carries the moment and the staleness because a list kept from
	// before a cluster stopped answering is a **success that has to say it is
	// old** — see NamespaceReading. A backend that has not asked this cluster at
	// all returns NotProbed rather than an empty list.
	Namespaces(ctx context.Context, cluster string) (NamespaceReading, *Failure)

	ListResources(ctx context.Context, q ListQuery) (ListPage, *Failure)
	GetResource(ctx context.Context, ref ObjectRef) (DetailPage, *Failure)

	// HelmReleases takes a **selection** and not a namespace. `Namespace` is a
	// repeatable parameter in the contract — "?namespace=kube-system&namespace=ingress
	// narrows to both" — and the namespace selector every list page carries is a
	// multiple selection. A single string could express none of that: a handler
	// reading only the first value would turn a two-namespace selection into a
	// one-namespace answer with nothing saying so.
	//
	// Empty means every namespace the caller may read, which is not `default`.
	HelmReleases(ctx context.Context, cluster string, namespaces []string) (HelmReleaseReading, *Failure)
	HelmRelease(ctx context.Context, cluster, namespace, name string) (HelmReleaseDetail, *Failure)
	// RevealHelmValues is the one door a release's supplied values reach a
	// view through: reviewed for the person and recorded,
	// like RevealEnv.
	RevealHelmValues(ctx context.Context, q HelmRevealQuery) (HelmValues, *Failure)
	// RevealHelmNotes is the same door for the notes Helm rendered: charts
	// print generated passwords in them.
	RevealHelmNotes(ctx context.Context, q HelmRevealQuery) (HelmNotes, *Failure)

	Overview(ctx context.Context, cluster string) (Overview, *Failure)
	Metrics(ctx context.Context, q MetricsQuery) (MetricsPanel, *Failure)
	Changes(ctx context.Context, ref ObjectRef) (ChangesPanel, *Failure)
	RevealEnv(ctx context.Context, q EnvQuery) (EnvValue, *Failure)
	DataPanel(ctx context.Context, ref ObjectRef) (DataPanel, *Failure)
	Manifest(ctx context.Context, ref ObjectRef) (ManifestPanel, *Failure)
	// CreateTemplate is the starting document for a new object of a kind. It
	// reviews nothing beyond the kind's existence:
	// reading a template is not permission to create.
	CreateTemplate(ctx context.Context, q CreateTemplateQuery) (CreateTemplate, *Failure)
}

// Acts is confirm-then-act. Every method comes in a pair, and the preview half
// is safe by contract: it performs a dry run and writes nothing.
//
// The facts are re-derived through the preview half immediately before the act,
// by this package rather than by the backend, so that binding a confirmation to
// what actually happens is one mechanism and not five. See plans.go.
type Acts interface {
	PreviewAction(ctx context.Context, cluster string, req ActionRequest) (PlanFacts, error)
	// RunAction carries what the person typed, because for one gesture that
	// string **is the act**: `number` is documented as *the value the act will
	// use, not a token proving they meant it*, and Scale's replica count has
	// nowhere else to arrive from — it is typed into the dialog, after the plan
	// was minted. For `name` and `count` it is the proof this package has
	// already checked, and the backend may ignore it.
	RunAction(ctx context.Context, cluster string, req ActionRequest, confirmed string) (ActOutcome, error)

	PreviewBulk(ctx context.Context, cluster string, req BulkRequest) (PlanFacts, error)
	RunBulk(ctx context.Context, cluster string, req BulkRequest) (ActOutcome, error)

	PreviewData(ctx context.Context, cluster string, req DataWrite) (PlanFacts, error)
	ApplyData(ctx context.Context, cluster string, req DataWrite) (ActOutcome, error)

	PreviewManifest(ctx context.Context, cluster string, req ManifestWrite) (PlanFacts, error)
	ApplyManifest(ctx context.Context, cluster string, req ManifestWrite) (ActOutcome, error)

	// PreviewCreate and ApplyCreate create one object that does not exist yet: a
	// dry run of a **create**, never of a patch, with
	// the kind, name and namespace read out of the parsed document, as for
	// the manifest. An object that exists is refused, not overwritten.
	PreviewCreate(ctx context.Context, cluster string, req ManifestWrite) (PlanFacts, error)
	ApplyCreate(ctx context.Context, cluster string, req ManifestWrite) (ActOutcome, error)

	PreviewNodeShell(ctx context.Context, cluster string, req NodeShellRequest) (PlanFacts, error)
}

// LogQuery is what the stream was asked for.
type LogQuery struct {
	ObjectRef
	Container string
	Follow    bool
	TailLines int
	Previous  bool
	// Timestamps asks the API server to prefix every line with the instant it
	// was written. A property of the request, not of the rendering: there is no
	// way to add one afterwards to lines that arrived without, so the panel's
	// toggle re-opens the stream. Where it is on, the stamp travels as
	// LogEvent.At and is taken off LogEvent.Text — a client should neither
	// re-parse it nor have to shift its color offsets past it.
	Timestamps bool
}

// Dock is terminals, the log stream and port-forwards.
type Dock interface {
	// OpenDesktopTerminal is available only where capabilities.desktopTerminal
	// is true, and returns ErrNotInProfile everywhere else. Not `localShell`,
	// which is the in-page pane and is true in a container. In a shared profile
	// this shell would run with the process's own credentials rather than the
	// caller's.
	OpenDesktopTerminal(req DesktopTerminalRequest) (TerminalSession, error)

	LogContainers(ctx context.Context, ref ObjectRef) ([]Container, *Failure)
	// StreamLogs drives the stream, calling emit for every frame. It returns a
	// Failure only for a condition established *before* the stream opened;
	// anything that happens mid-stream is emitted as a `state` frame, because a
	// stream that cannot say why it stopped renders a deleted pod, a denied read
	// and a quiet container identically.
	StreamLogs(ctx context.Context, q LogQuery, emit func(LogEvent) error) *Failure
	// EarlierLogs is one page backwards through the same container's log.
	//
	// Separate from StreamLogs and not a mode of it, because the two answer
	// different shapes of question: a stream is unbounded and framed, and this is
	// a bounded read whose refusal can be an ordinary status. `before` is how many
	// lines the caller already holds and `page` how many earlier ones it wants;
	// both are clamped by the backend, so a client that overstates either buys
	// nothing.
	//
	// "There is nothing earlier" is an **answer** and comes back in LogPage.Stop
	// with its sentence. A Failure here means the question could not be asked.
	EarlierLogs(ctx context.Context, q LogQuery, before, page int) (LogPage, *Failure)

	Forwards() ([]Forward, error)
	OpenForward(ctx context.Context, req ForwardRequest) (Forward, error)
	CloseForward(id string) error
}

// Probe answers the two kubelet endpoints.
type Probe interface {
	// Ready has to be able to fail; that is the whole reason it is separate from
	// liveness. A readiness probe that always says yes is a decoration.
	Ready(ctx context.Context) (ok bool, detail string)
}

package api

import (
	"time"

	"github.com/thonicdev/kubetower/internal/identity"
)

// The payload types, one per schema in `api/openapi.yaml`.
//
// Two rules run through all of them and both are load-bearing:
//
//   - **A field the spec marks `required` carries no `omitempty`.** The zero
//     value is then emitted rather than dropped, which is what the schema says
//     and what a generated client expects. An omitted required member is a
//     response that fails its own contract while looking tidier.
//   - **A field the spec marks nullable is a pointer with no `omitempty`**, so a
//     gap is `null` and never an absence. Omitting a sample from a series shifts
//     every later one onto the wrong moment and the chart still draws.

// ProbeResult is what a kubelet reads. Not an envelope: a probe answers a
// machine and the machine reads the status.
type ProbeResult struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// --- Session and profile ----------------------------------------------------

// LoginRequest is the credential. `additionalProperties: false` in the spec, so
// the handler rejects an unknown member rather than ignoring it.
type LoginRequest struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

// Session is who is signed in and until when.
//
// Both bounds, because the front end needs both: an idle one it can push back by
// acting, and an absolute one it cannot. A console that shows neither signs the
// operator out mid-sentence.
type Session struct {
	Identity string `json:"identity"`
	// Method is how this session was opened: `password` for the shared local
	// account, `sso` for a person the identity provider vouched for. Said
	// because the two are not the same kind of identity - "admin" is whoever
	// holds the password, and nothing downstream can attribute it to a person.
	Method string `json:"method"`
	// Groups are the identity provider's groups as the console will assert them,
	// prefix included, as of the sign-in. Empty for the local account, and empty
	// for a person whose token carried no groups claim.
	Groups            []string  `json:"groups"`
	IdleExpiresAt     time.Time `json:"idleExpiresAt"`
	AbsoluteExpiresAt time.Time `json:"absoluteExpiresAt"`
}

// The three values of Session.Method - the same three strings the write log
// uses, so a session and the line it leaves cannot spell its method differently.
const (
	SessionMethodPassword = identity.Password
	SessionMethodSSO      = identity.SSO
	// SessionMethodLaunch is the shared account reached through the
	// application's one-time launch code.
	SessionMethodLaunch = identity.Launch
)

// SignInMethods is what the sign-in page may offer, asked before anybody is
// signed in.
//
// **Public, and it says nothing a browser would not learn by being redirected**:
// whether a password form exists, and whether single sign-on is configured and
// answering. The issuer's address is not a member of its own, although the
// browser learns it on the first click and an unavailable provider's Detail may
// name it - that sentence is what an operator needs, and the address is not a
// secret.
type SignInMethods struct {
	// Password is whether the shared local account is offered. False once an
	// identity provider is configured, unless the deployment kept it on purpose.
	Password bool `json:"password"`
	SSO      SSO  `json:"sso"`
}

// SSO is the identity provider's state, as the sign-in page must draw it.
//
// Four states and not a boolean, because "not configured", "not asked yet",
// "asked and answering" and "asked and not answering" are four different pages -
// and a sign-in button that does nothing when clicked is the failure to avoid:
// *nothing measured it* and *it measured nothing* are different answers.
type SSO struct {
	State string `json:"state"`
	// Detail says why, for `pending` and `unavailable`. Never a secret: it is
	// built from the discovery attempt's error, which names an address and a
	// status and nothing the console holds.
	Detail string `json:"detail,omitempty"`
}

// The values of SSO.State.
const (
	SSOOff         = "off"
	SSOPending     = "pending"
	SSOReady       = "ready"
	SSOUnavailable = "unavailable"
)

// ReadOnlyState is the guardrail: the profile's configured default, and the
// terminal override as measured at start-up.
//
// **Three states for the fence and not two**: a fence that could not be probed
// is not one that is off, and the pair of booleans alone says "off" for both.
// `Fence` is the word, `Measured` is the contract's other spelling of
// "not unknown", and both are always sent.
type ReadOnlyState struct {
	WritesRefused       bool `json:"writesRefused"`
	DetailWritesRefused bool `json:"detailWritesRefused"`
	// Fence is the read-only override in front of a session's kubectl and
	// helm, as measured: FenceOn, FenceOff or FenceUnknown.
	Fence string `json:"fence"`
	// Measured is false exactly when Fence is FenceUnknown.
	Measured bool `json:"measured"`
	// Detail is the measurement's own sentence, in all three states.
	Detail string `json:"detail,omitempty"`
	// Unmatched is the KT_READONLY_CLUSTERS entries that named no context the
	// administrator believes those clusters are protected and nothing is.
	// Absent when every entry matched.
	Unmatched []string `json:"unmatched,omitempty"`
}

// The values of ReadOnlyState.Fence.
const (
	FenceOn      = "on"
	FenceOff     = "off"
	FenceUnknown = "unknown"
)

// Capabilities is what the running profile offers.
//
// Every false here is also refused at its own endpoint. Hiding a control is
// cosmetic; the server refusing is the enforcement, or the permission model is
// made of CSS.
type Capabilities struct {
	Mode string `json:"mode"`
	// LocalShell is a pseudo-terminal on the machine running the console - the
	// `terminal` channel, drawn as a pane in the page. **Not** a window on the
	// user's desktop, which is DesktopTerminal below.
	LocalShell bool `json:"localShell"`
	// DesktopTerminal is a terminal window on the user's own machine, which
	// only a console sharing their desktop can open. The gate on
	// `/api/dock/terminals`.
	DesktopTerminal  bool `json:"desktopTerminal,omitempty"`
	PodExec          bool `json:"podExec"`
	NodeShell        bool `json:"nodeShell"`
	Assistant        bool `json:"assistant"`
	PortForward      bool `json:"portForward"`
	PortForwardLimit int  `json:"portForwardLimit,omitempty"`
	// Preferences is whether this console keeps what a person arranges - the
	// rail, the nameplates, the column layouts, the density - which is whether
	// the twelve preference writes are registered. False under the server
	// profile (Deps.InCluster: a shared console would keep one person's
	// arrangement for everybody) and where it runs as several replicas
	// behind one address (Deps.Replicated). Never omitted: an absent false would
	// read as the contract's default, true.
	Preferences bool `json:"preferences"`
	// Replicated is this process being one of several behind one address
	// (Deps.Replicated). With `Mode: "shared"` it is what takes the settings
	// writes off the page: several replicas remove them on any profile, and the
	// server profile removes them on one replica too. Never omitted, for the
	// reason Preferences gives.
	Replicated bool `json:"replicated"`
	// KindPreferences is whether this console keeps each person's choices
	// about the kinds column on the server (getKindPreferences); false, the
	// browser keeps them. Never omitted, for the reason Preferences gives.
	KindPreferences bool          `json:"kindPreferences"`
	ReadOnly        ReadOnlyState `json:"readOnly"`
	Version         string        `json:"version,omitempty"`
	Revision        string        `json:"revision,omitempty"`
	// Release is this build's bare version (`0.2.0`), as CHANGELOG.md and the
	// tags spell it. Absent on a development build: no "What's new" there (#80).
	Release string `json:"release,omitempty"`
}

// --- Clusters, navigation, extensions ---------------------------------------

// Cluster carries its own state, so one unreachable cluster leaves the other
// nine `ok`.
//
// The front end reads every member here: the rail sorts on `bar` and draws a
// seam between bars, so an absent `bar` would leave the arrangement inert with
// nothing reporting a fault. An absent optional member conforms - the response
// schema declares no `additionalProperties: false` and requires only three of
// these - so `TestTheClusterListCarriesEveryMemberTheContractDeclares` is what
// fails instead.
type Cluster struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	// Initials is the nameplate's short form, drawn on the tile. Carried rather
	// than derived in the client so that the tile, the naming form and the
	// chooser cannot disagree about a cluster nobody has renamed. Required by
	// the contract and never empty: the tile draws nothing
	// else, so a cluster with no initials would be a blank square.
	Initials string `json:"initials"`
	// Color is the tile's color **key** on the nameplate palette, never a CSS
	// value, and never empty: the automatic color
	// derived from the context name is a palette key too. A key because the
	// value ends up in a class name, which is the whole of why it is not one.
	Colour string `json:"colour"`
	Hidden bool   `json:"hidden,omitempty"`
	// Order is where the tile sits, and it is a pointer for the reason every
	// other position in this file is: `0` is the first tile and absent is "this
	// tile has no position", which is the state a tile on no hotbar is in. An
	// `int` with `omitempty` collapses those two, and collapses them on the one
	// tile that is first — which the front end then sorts last, because it reads
	// an absent order as "no arrangement".
	Order *int `json:"order,omitempty"`
	// Bar is which hotbar the tile is on, 1-based. Absent means it is on none,
	// which is what Hidden also says and is the state a hidden tile is in. A
	// plain int is safe here where Order is not: the schema's minimum is 1, so
	// zero is never a position and absence is unambiguous.
	Bar int `json:"bar,omitempty"`
	// ReadOnly is writes to **this cluster** being refused, which is where this
	// console's guardrail actually is — `KT_READONLY_CLUSTERS`, one name at a
	// time. `Capabilities.ReadOnly` is the profile-wide pair and is not a
	// substitute: it can only report "every configured cluster is fenced", so a
	// console where one production cluster of fourteen is guarded answers false
	// there and true here.
	ReadOnly bool    `json:"readOnly,omitempty"`
	State    State   `json:"state"`
	Reason   *Reason `json:"reason,omitempty"`
	// Remedy is who fixes this cluster, where the classification produced an
	// answer. Absent on a cluster that answered and on one nobody has probed
	// yet — neither has a failure to attribute, and inventing a sentence for
	// them is the defect Remedy exists to prevent.
	Remedy   *Remedy `json:"remedy,omitempty"`
	Loopback bool    `json:"loopback,omitempty"`
	// AWS is the context signing in through the AWS CLI: its kubeconfig user
	// runs `aws` as its credential plugin. It is what lets the home put the
	// AWS SSO session's notice on the cards it can concern, and on no GKE or
	// OIDC context that failed for its own sign-in.
	//
	// Read from the exec command's basename only (`aws`, a path to it,
	// `aws.exe`), never from its arguments: a context signing in through a
	// wrapper — `aws-vault`, `aws-iam-authenticator`, `granted`, a shell
	// script — is false here even when the wrapper runs the AWS CLI underneath,
	// and its card carries no SSO notice.
	AWS bool `json:"aws,omitempty"`
	// CheckedAt is when the probe established State, absent where nothing was
	// probed. A client reads it to know whether a red verdict is fresh enough
	// to stand in for a read.
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
}

// Ref is a thing you can click through to — the most reused shape in the whole
// view layer.
type Ref struct {
	Text string `json:"text"`
	Href string `json:"href,omitempty"`
	Hint string `json:"hint,omitempty"`
	// Namespaced is set on a menu entry that opens the list of a namespaced
	// kind, or the Helm releases, which are read by namespace too, and on
	// nothing else: it is what lets the browser carry the namespace chosen on
	// one list to the next one without guessing a kind's
	// scope from its name. Absent is not "cluster-scoped" - it is also every
	// Ref that is not such a list - and the browser reads absent as "do not
	// carry", which is the safe answer for both.
	Namespaced bool `json:"namespaced,omitempty"`
	// Aliases are the other names kubectl answers to for the kind a menu entry
	// opens - `svc`, `deploy`, a CRD's own `shortNames` - lower-cased. The jump
	// box matches them as well as Text. Absent on every Ref
	// that is not a kind's list.
	Aliases []string `json:"aliases,omitempty"`
}

// NavGroup is one section of the menu.
type NavGroup struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Note says why a group is short or empty, when it is. Two facts arrive here
	// and the sentence is what separates them: a cluster serving no custom
	// resource kinds, and a cluster whose kinds could not be listed. Both leave
	// Items empty.
	Note string `json:"note,omitempty"`
	// AccessNote says, in one sentence, that kinds were left out of this group
	// because this console may not list them, and that the read has to be
	// granted. Drawn first in the group, where it is read; Note is not.
	AccessNote string `json:"accessNote,omitempty"`
	Items      []Ref  `json:"items"`
	// Groups nests one further level, for the custom resource kinds grouped by
	// API group inside a single heading. One level and no more — see the schema.
	Groups []NavGroup `json:"groups,omitempty"`
	// Deferred says this group's Items are not in this response and are asked for
	// separately, through getNavGroup.
	//
	// Marshalled unconditionally, because it carries a `default` in the contract
	// and is therefore **required** in the generated TypeScript: an omitted member
	// is `undefined` on a type that promised a value, and a client reading
	// `undefined` as false draws an empty heading — which tells the person this
	// cluster has no custom resources. That is the defect this member exists to
	// close, so an `omitempty` here would reintroduce it.
	Deferred bool `json:"deferred"`
}

// NavMenu is the menu for one cluster.
type NavMenu struct {
	Groups []NavGroup `json:"groups"`
	// Discovery is set while the kinds this cluster serves are not all known:
	// absent when discovery is complete. The menu still lists everything it knows.
	Discovery *NavDiscovery `json:"discovery,omitempty"`
}

// NavDiscoveryState is where the menu's discovery of served kinds stands.
type NavDiscoveryState string

const (
	// NavDiscoveryPending: discovery is still running; ask again shortly.
	NavDiscoveryPending NavDiscoveryState = "pending"
	// NavDiscoveryFailed: discovery answered with an error, or not all of it.
	NavDiscoveryFailed NavDiscoveryState = "failed"
)

// NavDiscovery is the structured state of the menu's discovery, with one short
// line for a tooltip.
type NavDiscovery struct {
	State  NavDiscoveryState `json:"state"`
	Detail string            `json:"detail"`
}

// Occupancy is whether a kind holds an object the person asking may see.
// Three values and not a count: the column hides `none`
// and never `unknown`, and a number would invite a client to read a failed
// count as zero.
type Occupancy string

const (
	OccupancySome    Occupancy = "some"
	OccupancyNone    Occupancy = "none"
	OccupancyUnknown Occupancy = "unknown"
)

// KindOccupancy is one kind's answer. Reason says why it is unknown, and is
// empty otherwise.
type KindOccupancy struct {
	State  Occupancy `json:"state"`
	Reason string    `json:"reason,omitempty"`
}

// NavOccupancy is every kind of one cluster's menu, keyed by the kind's slug -
// the last path segment of its menu entry's href. A kind the menu draws and
// this does not name is unknown to a client, never none.
type NavOccupancy struct {
	Kinds map[string]KindOccupancy `json:"kinds"`
	// Note says why kinds are missing from Kinds, when some are: the custom
	// resource kinds could not be listed.
	Note string `json:"note,omitempty"`
}

// Extension is one of the additive pages, with its state on this cluster.
type Extension struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Summary and Needs are the extension's own sentences, drawn on its tile:
	// what turning it on adds, and what the cluster must run for it to say
	// anything (extension.Extension).
	Summary string `json:"summary,omitempty"`
	Needs   string `json:"needs,omitempty"`
	Enabled bool   `json:"enabled"`
	// Pinned: KT_EXTRAS decided Enabled, so nothing on the console changes it.
	// On the desktop and in a container only: under the server profile
	// KT_EXTRAS names what may be turned on and pins nothing.
	Pinned bool `json:"pinned"`
	// Writable is whether the person asking may turn it on or off on this
	// cluster, and change its settings: the write is registered, nothing pins
	// it, the chart's values allow it, and - under the server profile - the
	// `use` verb on it was reviewed and allowed. The
	// writes refuse whatever this says no to; it is the page's hint, not the
	// gate. Always sent.
	Writable bool `json:"writable"`
	// Locked says why Writable is false, as a word the page draws by: see
	// the ExtensionLock constants. Empty when it is writable.
	Locked string `json:"locked,omitempty"`
	// Reason is the sentence the Settings page draws beside the switch when
	// Writable is false. Empty when it is writable.
	Reason string `json:"reason,omitempty"`
	// Settings is what the extension offers beyond its switch, each at its
	// value on this cluster (extension.Configurable). Always sent, empty for
	// one that offers none, so a client never has to tell absent from none.
	Settings []ExtensionSetting `json:"settings"`
	Nav      *Ref               `json:"nav,omitempty"`
}

// Why an extension's switch is not the person's to press (Extension.Locked).
const (
	// ExtensionPinned: KT_EXTRAS pins it, on the desktop or in a container.
	// The page draws the button, disabled, as for values and unregistered.
	ExtensionPinned = "pinned"
	// ExtensionNotInValues: under the server profile, the chart's values do not
	// name it. The page draws the button, disabled.
	ExtensionNotInValues = "values"
	// ExtensionNoUse: under the server profile, the `use` verb on it was not
	// allowed for this person (denied, or the review could not be made). The
	// page offers no button.
	ExtensionNoUse = "use"
	// ExtensionNoSwitch: the write is not registered here - several replicas,
	// or the server profile with no state volume.
	ExtensionNoSwitch = "unregistered"
)

// ExtensionSuggestion is an extension that is off on a cluster that runs what it
// reads (extension.Suggester): the banner offering to turn it on.
type ExtensionSuggestion struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Evidence is the extension's own sentence saying what it found, or, with
	// Absent, "<component> absent".
	Evidence string `json:"evidence"`
	// Absent is an extension that looked and found nothing it reads: no banner,
	// but the Settings tile says so, grayed and still switchable.
	Absent bool `json:"absent,omitempty"`
}

// ExtensionSetting is one setting of one extension, on one cluster.
type ExtensionSetting struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Summary string `json:"summary,omitempty"`
	// Value is what was chosen on this cluster, or Default where nothing was.
	Value   bool `json:"value"`
	Default bool `json:"default"`
	// Pinned is true where KT_EXTRA_SETTINGS decides it for every cluster:
	// the write refuses it, and the Settings page draws it with no control.
	// For a line of text, where its Env is set.
	Pinned bool `json:"pinned,omitempty"`
	// Kind is "bool" (a checkbox, Value), "text" (a line of text, Text) or
	// "choice" (one of Options, kept in Text).
	Kind string `json:"kind"`
	// Text is a text setting's value on this cluster, empty where nothing is
	// set; Placeholder is what the extension uses then, and Env the variable
	// that decides it on every cluster when set.
	Text        string `json:"text,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Env         string `json:"env,omitempty"`
	// Options is what a "choice" setting accepts, in the order drawn.
	Options []ExtensionOption `json:"options,omitempty"`
}

// ExtensionOption is one value a choice setting accepts, and its words.
type ExtensionOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// --- Settings ---------------------------------------------------------------

// SettingsWrite is the general settings, and the body of the write.
//
// What the General tab writes is the archive, and these three are it. The node
// shell has its own body and its own route, deliberately, so that a build
// with no archive does not refuse the node-shell save along with the archive's.
//
// Pointers throughout, because the schema requires none of them and a write that
// says nothing about a field must not re-assert its zero: `false` and "not said"
// are different instructions about keeping events.
type SettingsWrite struct {
	// ArchiveEnabled keeps a window of cluster events. Turning it off stops
	// writing; it discards nothing, which is why emptying the archive is a
	// separate, confirmed act rather than a consequence of this checkbox.
	ArchiveEnabled *bool `json:"archiveEnabled,omitempty"`
	// CaptureLogs captures pod output alongside the events. It is the expensive
	// half.
	CaptureLogs *bool `json:"captureLogs,omitempty"`
	// RetentionDays is one of Settings.retentionChoices. A choice and not a free
	// number: a typo on a page about disk usage silently means "keep nothing" or
	// "keep a year".
	RetentionDays *int `json:"retentionDays,omitempty"`
}

// NodeShellSettings is its own write because it carries an image reference that
// is validated differently from everything else on that page.
type NodeShellSettings struct {
	Enabled   bool   `json:"enabled"`
	Image     string `json:"image,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	// Available says this build has a terminal layer at all. **False means the
	// section is left off the page entirely** rather than drawn as a field that
	// refuses: a control that cannot work is worse than an absent one. Not
	// omitted, because `false` is the answer that decides that.
	Available bool `json:"available"`
	// DefaultImage is what emptying the field goes back to, shown **so an
	// operator can see what they are moving away from before they do**. Without
	// it the placeholder is blank and "leave it empty to restore the default" is
	// an instruction about a value nobody can see.
	DefaultImage string `json:"defaultImage,omitempty"`
	// Persisted and Note are the write's own half of Settings.applied: a
	// validated image is in effect whether or not it could be written down, and
	// the two failures are reported separately. A pointer, so a plain read says
	// nothing about a write that did not happen.
	Persisted *bool  `json:"persisted,omitempty"`
	Note      string `json:"note,omitempty"`
}

// KubeconfigShadow is one context a file declared and another already had.
//
// **The merge keeps the first file's entry whole and discards the second's**, so
// the loser is not a duplicate row and not an error — it is an absence: nothing
// in the rail, nothing in an address, nothing to click. It is the only
// explanation of a context that exists in a file and nowhere in the console.
//
// The sentence travels **composed**, so that the page and the start-up log
// cannot describe one merge two ways.
type KubeconfigShadow struct {
	Context    string `json:"context"`
	KeptFrom   string `json:"keptFrom,omitempty"`
	ShadowedIn string `json:"shadowedIn,omitempty"`
	Sentence   string `json:"sentence"`
}

// SettingsInfo is the first-run information, a tab of the settings page rather
// than a page of its own.
type SettingsInfo struct {
	StatePath string `json:"statePath,omitempty"`
	// StatePersistent is whether the state directory survives a restart, **as
	// the deployment says** - the console cannot detect a volume and does not
	// guess. False is what takes the archive and the node-shell image off the
	// page and their writes off the router; see Deps.StateEphemeral.
	//
	// Never omitted: an absent member is a client deciding for itself what
	// silence means, and here the silence is the difference between an archive
	// and a promise nothing keeps.
	StatePersistent bool `json:"statePersistent"`
	// KubeconfigFiles is every file the configuration names, in order. A list,
	// not a path: a console that shows only the first one is wrong on most
	// machines.
	KubeconfigFiles    []string           `json:"kubeconfigFiles,omitempty"`
	KubeconfigShadowed []KubeconfigShadow `json:"kubeconfigShadowed,omitempty"`
	ListenAddress      string             `json:"listenAddress,omitempty"`
	// WithoutPassword says this console asks nobody for a password: the
	// loopback desktop, whose windows open signed in. Not omitted when false,
	// so a client never decides for itself what silence means.
	WithoutPassword bool `json:"withoutPassword"`
	// Version is the whole line the binary's `--version` prints, as **one opaque
	// string**. Not four fields: four would let a page compose a second spelling
	// of the same fact, and the two would eventually disagree.
	//
	// It is the first question of every real support exchange.
	Version string `json:"version,omitempty"`
	// Development says this binary carries no stamped version. A separate flag
	// rather than something read back out of Version, because a page parsing a
	// string it was handed is a second opinion about it.
	//
	// Not omitted when false: "this is a stamped build" is the answer the
	// version line beside it is read against, and an absent member is a client
	// deciding for itself what silence means.
	Development bool `json:"development"`
	// StateWrittenBy and StateIsNewer are the warning that explains a rail which
	// silently reset itself. On the page and not only in a log, because the
	// failure it warns about is silent: every store here loads defensively, so an
	// older build meeting a newer format replaces the arrangement with defaults
	// rather than complaining.
	StateWrittenBy string `json:"stateWrittenBy,omitempty"`
	StateIsNewer   bool   `json:"stateIsNewer"`
	// RepositoryURL is where KubeTower lives, beside the version.
	//
	// Sent rather than written into the page, because the update check asks the
	// same repository: two spellings of one address are two addresses the day
	// somebody moves it. Present whether or not the check ran — it is a link
	// beside the version, not a part of the check.
	RepositoryURL string `json:"repositoryUrl,omitempty"`
	// DocumentationURL is the public documentation, beside the repository.
	//
	// Sent rather than written into the page, because Help ▸ KubeTower Help
	// opens the same address and it has one spelling, in internal/launch.
	DocumentationURL string `json:"documentationUrl,omitempty"`
	// Update is whether a newer release exists. Nil when nothing checks.
	Update *SettingsUpdate `json:"update,omitempty"`
}

// SettingsUpdate is the update check's answer, as the Infos tab receives it.
//
// **A plain struct here and not internal/update's own.** This package owns the
// contract and nothing else; it knows what a response must look like and must
// not learn how a release is asked for, or the two diverge on the day somebody
// fixes a bug in one of them.
type SettingsUpdate struct {
	// State is one of `off`, `notApplicable`, `current`, `newer`, `failed`.
	//
	// **`failed` is not `current`.** A page that draws them the same way is a
	// page confirming a version it never checked, which is
	// `docs/DEFINITION-OF-DONE.md` §3 on a new surface.
	State string `json:"state"`
	// Latest is the release GitHub named, without the git tag's leading `v`.
	Latest string `json:"latest,omitempty"`
	// ReleaseURL is that release's own page.
	ReleaseURL string `json:"releaseUrl,omitempty"`
	// CheckedAt is when the request was made, and absent when none was — so a
	// page knows not to date an answer nothing asked for.
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
	// Note is the one sentence about this outcome, composed on the server so the
	// start-up log and this page cannot describe one check two ways.
	Note string `json:"note,omitempty"`
	// Checking is whether a request to GitHub is in flight right now: at
	// startup, on the schedule, or after Check now. The
	// other members are the previous answer until it lands.
	Checking bool `json:"checking"`
	// Install is the desktop's downloader and installer (#47). Absent where
	// nothing installs: the server profile, a container, a test build.
	Install *UpdateInstall `json:"install,omitempty"`
}

// UpdateInstall is where an update download and install stands, and what the
// restart would end. internal/updater.Status, with the open work counted.
type UpdateInstall struct {
	// Phase is `idle`, `downloading`, `verifying`, `ready`, `installing`,
	// `failed` or `manual` (the installation's folder is not writable).
	Phase string `json:"phase"`
	// Enabled is the "Install updates" setting.
	Enabled bool `json:"enabled"`
	// Supported says this installation can be updated from the console.
	Supported bool `json:"supported"`
	// Installation is how it was installed (`linux-appimage`, `windows-setup`…).
	Installation string `json:"installation"`
	Version      string `json:"version,omitempty"`
	Asset        string `json:"asset,omitempty"`
	Done         int64  `json:"done,omitempty"`
	Total        int64  `json:"total,omitempty"`
	Reason       string `json:"reason,omitempty"`
	// Folder holds the verified file, in the manual phase.
	Folder string `json:"folder,omitempty"`
	// Ends is what the restart closes for good.
	Ends UpdateEnds `json:"ends"`
}

// UpdateEnds counts the terminals and port-forwards a restart ends.
type UpdateEnds struct {
	Terminals int `json:"terminals"`
	Forwards  int `json:"forwards"`
}

// UpdateInstallWrite is the "Install updates" setting's write.
type UpdateInstallWrite struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// RetentionChoice is one entry of the retention list.
//
// **A list and not a range, and it always contains the value it is editing.** A
// control that cannot represent its own current state does not merely look
// wrong: it changes it. With retention at 21 and no option carrying `selected`,
// the browser would submit the first, and a Save opened to tick an unrelated
// checkbox would destroy twenty days of archived events while saying "Saved."
type RetentionChoice struct {
	Days  int    `json:"days"`
	Label string `json:"label"`
}

// ArchiveFacts is what the archive currently holds and where.
//
// It is **its own envelope** on Settings, because an archive with nowhere to
// write is a state and not a zero — and a page reporting 0 bytes over a refused
// write is the most reassuring possible way to be wrong.
type ArchiveFacts struct {
	Path  string `json:"path,omitempty"`
	Bytes int64  `json:"bytes"`
	Days  int    `json:"days,omitempty"`
	// OldestAt is the oldest day the archive still holds, which is what says
	// whether the window is actually as long as the setting claims.
	OldestAt *time.Time `json:"oldestAt,omitempty"`
	// WritesRefused is the archive having stopped accepting writes. Deliberately
	// separate from "logs suspended", which is the size ceiling stopping the
	// expensive half while events keep going in: a page reporting that as "not
	// writable" sends an operator looking for a full volume when what they need is
	// a shorter window.
	WritesRefused bool   `json:"writesRefused"`
	Note          string `json:"note,omitempty"`
}

// SettingsApplied is what the last write did. Filled on the response to a write
// and absent on a plain read.
//
// **`Persisted: false` is not a failure and must not be drawn as one.** Every
// store here applies in memory first and then writes; a value that validated is
// in effect whether or not the file could be written. Report that as an error
// and the operator retypes a setting that is already working. What they need to
// be told is the narrower true thing: it will not survive a restart.
type SettingsApplied struct {
	Persisted bool   `json:"persisted"`
	Note      string `json:"note,omitempty"`
}

// Settings is not enveloped. It is this process's own file, so it cannot be
// unreachable, and a file the console cannot read at all is a broken console.
type Settings struct {
	General SettingsWrite `json:"general"`
	// RetentionChoices is the list the General tab's dropdown is built from, and
	// it always contains the value it is editing. See RetentionChoice.
	RetentionChoices []RetentionChoice `json:"retentionChoices,omitempty"`
	// Archive is an envelope of its own. See ArchiveFacts.
	Archive   Value[ArchiveFacts] `json:"archive"`
	NodeShell NodeShellSettings   `json:"nodeShell"`
	Applied   *SettingsApplied    `json:"applied,omitempty"`
	Info      SettingsInfo        `json:"info"`
}

// ClearRequest discards the console's stored state. Destructive, and the one
// destructive act with no cluster behind it — so it takes a plan token like
// every other.
type ClearRequest struct {
	Scope   string `json:"scope"`
	Preview *bool  `json:"preview,omitempty"`
	Token   string `json:"token,omitempty"`
	// Confirmed is what the person typed, for a plan asking for more than an
	// acknowledgement. Checked against the plan's own gesture.
	Confirmed string `json:"confirmed,omitempty"`
}

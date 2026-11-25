// Package web serves the UI: the compiled front-end bundle, the JSON contract
// it talks to, the installable-application assets, and the routes extensions
// carry.
package web

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/auth"
	"github.com/thonicdev/kubetower/internal/changes"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/columns"
	"github.com/thonicdev/kubetower/internal/customresources"
	"github.com/thonicdev/kubetower/internal/detail"
	"github.com/thonicdev/kubetower/internal/edit"
	"github.com/thonicdev/kubetower/internal/extras"
	"github.com/thonicdev/kubetower/internal/forward"
	"github.com/thonicdev/kubetower/internal/helm"
	"github.com/thonicdev/kubetower/internal/hotbars"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/kindprefs"
	"github.com/thonicdev/kubetower/internal/logs"
	"github.com/thonicdev/kubetower/internal/metrics"
	"github.com/thonicdev/kubetower/internal/nameplates"
	"github.com/thonicdev/kubetower/internal/seen"
	"github.com/thonicdev/kubetower/internal/sharedstate"
	"github.com/thonicdev/kubetower/textutil"
)

// The installable-application assets, and nothing else: the console is the
// compiled bundle, which `spa.go` embeds separately, and the only thing under
// `static/` that a browser asks for is the icon, the manifest and the service
// worker.
//
//go:embed all:static/pwa
var assets embed.FS

// Options are what the UI needs from the rest of the binary. Everything except
// Clusters may be nil: a page that depends on a missing piece says so rather
// than failing, because a console that will not start is worse than one that
// admits it cannot draw a chart.
type Options struct {
	Clusters *cluster.Manager
	// UI is the front end served at `/`: the root of a Vite build, with
	// `index.html` at its top and the files it loads under `assets/`. Nil is
	// the bundle embedded in this binary, which is what `cmd/kubetower` serves.
	// A binary that carries extras brings its own, because its pages are in
	// its build and not in this one. See bundle.go.
	UI      fs.FS
	Metrics Usage
	// MetricsSources is each cluster's metrics source in Settings › General.
	// Nil leaves its routes unregistered.
	MetricsSources api.MetricsSources
	// Tunnels opens a local address onto a Service inside a cluster. Nil is
	// valid and means `extension.Host.Forward` answers with a sentence rather
	// than an address — a page that reaches a workload on a build with no
	// forwarder must say so, not draw the workload as empty.
	//
	// New puts maxExtensionTunnels in front of it (tunnelcap.go), which is the
	// only ceiling the tunnels extensions dig have: internal/forward's counts
	// the forwards a browser opens, through a forwarder of its own. Under
	// InCluster each person has perPersonExtensionTunnels of it.
	Tunnels  metrics.Tunneller
	Series   Series
	Terminal Terminals
	// Setup is what the Infos tab in Settings answers with: where the state lives, and
	// whether that state was last written by a newer build. See setup.go.
	Setup SetupInfo
	// Hotbars is the rail's arrangement. Nil is valid and means an arrangement
	// that lives as long as the process: the rail still works, it simply forgets
	// where things were on the next start.
	Hotbars *hotbars.Store
	// Columns is how wide each table's columns are and what order they come in.
	// Nil is valid and means the same bargain the hotbars are on — the tables
	// are still resizable and still reorderable, they simply go back to the
	// defaults on the next start.
	Columns *columns.Store
	// Nameplates is what a person calls each cluster and what color they
	// gave it. Nil is valid and means the same bargain again — every cluster is
	// drawn under its context name, which is what it is drawn under until
	// somebody renames one anyway.
	Nameplates *nameplates.Store
	// Extras is which of the additive pages this installation
	// runs. Nil is valid and means none of them: no Extra heading in the menu
	// and a 404 on their routes, which is what a fresh install looks like
	// before anybody has ticked anything.
	Extras *extras.Store
	// Seen is the last release whose "What's new" this desktop has seen (#80).
	// Nil keeps it in memory: it shows again on the next start.
	Seen *seen.Store
	// KindPrefs is each person's choices about the kinds column.
	// Nil keeps none: the API answers
	// not_available_in_this_profile and the browser keeps them. Kept only
	// where kindPrefsOffered says, even when one is handed in.
	KindPrefs *kindprefs.Store
	// Archive is the kept window of events and logs. Nil is valid and means the
	// settings page says so rather than the console refusing to start.
	Archive Archives
	// Forwards is the live set of port forwards. Nil is valid and means the
	// panel offers no forward button rather than offering one that fails.
	Forwards Forwards
	// Health says which contexts answer and who fixes the rest. Nil is valid and
	// means the page says this build has no probe — the console still starts,
	// which is the same bargain every other optional piece here is on.
	Health Health
	// Recorder is the event poller, shown on the health page. Nil is valid and
	// means that panel is absent rather than reporting a collector that does not
	// exist as idle.
	Recorder Recorders
	// Sessions is the front door, handed over so the JSON contract's three
	// session operations answer from the same cookie, the same failure delay and
	// the same sliding expiry as the login page. Nil is valid and means those
	// three answer a problem object saying this build has no door — which is a
	// state a test produces and the binary does not.
	Sessions api.Sessions
	// SignIn is what the sign-in page offers - the password, single sign-on, or
	// both - handed to the JSON contract, which decides from it which session
	// routes to register. Nil is valid and means the password alone.
	SignIn api.SignIn
	// Probe answers /readyz. Nil is valid and means readiness cannot be
	// established, which is answered 503 rather than 200: not knowing whether
	// this process can reach anything is not the same as knowing it can.
	Probe api.Probe
	// ReadOnly says which clusters may not be changed — the same callback the
	// terminal takes, from the same KT_READONLY_CLUSTERS.
	//
	// Nil is valid and means no cluster is marked, which is what an empty
	// KT_READONLY_CLUSTERS means too. It is *not* an excuse to skip the check:
	// `internal/edit` substitutes the same default and refuses on its own
	// account, so the guardrail cannot be lost by a caller forgetting a field.
	ReadOnly func(string) bool
	// ReadOnlyUnmatched is the KT_READONLY_CLUSTERS entries that named no
	// context (`cluster.ReadOnly.Unmatched`), carried to the page so a typo is
	// said where the read-only state is shown rather than only in the boot log.
	// Nil is valid and means every entry matched, or none was set.
	ReadOnlyUnmatched []string
	// InCluster selects the server profile: a console shared by several people
	// inside a cluster, rather than one person's application on their own
	// machine. `--incluster` selects it and everything else is desktop.
	//
	// **What it does here is take routes away, and the distinction matters more
	// than the list.** There are two ways
	// to turn a route off, and this is the first:
	//
	//	Not registering the route is a property of the router. It is true for
	//	every method, every header, every path-matching subtlety, and it is
	//	visible by walking the router.
	//
	//	Refusing inside the handler is a property of one code path. It is true
	//	only where the condition is evaluated, it has to be re-established for
	//	every early return added later, and no walk of the router can see it.
	//
	// So the profile decides at **registration**. The capability fields this
	// server already reports stay, and stay honest, but they are the cosmetic
	// half — `Capabilities`' own header says so: *"Every false here is also
	// refused at its own endpoint … Hiding a control is cosmetic."* Under this
	// profile there is no endpoint left to refuse at.
	//
	// **False is the desktop.** A build without the flag registers the
	// desktop's routes, which is asserted rather than assumed.
	InCluster bool

	// LogFollowMax is how long a log follow may run. Zero keeps the logs
	// package's default (the server profile's); internal/launch sets it per
	// profile and from KT_LOG_FOLLOW_MAX.
	LogFollowMax time.Duration

	// Shared is the store every replica shares - for this package, the
	// confirmation plans `internal/api` holds between a preview and its act. Nil
	// is one in this process's memory, which is all one replica needs.
	Shared sharedstate.Store

	// Replicated is the deployment saying this process is one of several behind
	// one address. Like InCluster it takes routes away, at mount time; see
	// `api.Deps.Replicated` for which and why.
	Replicated bool

	// Access is the choke point every write is reviewed at, and what the pages
	// ask before they offer a verb (internal/access). **One for the process**:
	// the terminal's exec and the forward manager are handed the same one, so
	// a page and the write it offers share the held answers. Nil builds one
	// over Clusters - never a Server that writes without a review.
	Access *access.Gate
}

// Forwards is the port-forward manager, reduced to the three things the UI
// mounts. An interface rather than the package so this file does not depend on
// how a tunnel is dug.
type Forwards interface {
	OpenHandler() http.Handler
	ListHandler() http.Handler
	CloseHandler() http.Handler
}

// ForwardSet is the same manager asked directly rather than through a handler,
// and it is what the framed `/api/forwards` operations need: those answer JSON
// this package composes, so a handler that writes its own response is no use to
// them.
//
// **Discovered rather than required.** `Forwards` stays three handler methods so
// that mounting the template dock does not depend on how a tunnel is dug, and a
// forwarder that cannot be asked for its live set is a legitimate build — it
// answers `ErrNotInProfile`, which is the truth, rather than `[]`, which would
// draw a failure as an empty answer. `*forward.Manager` satisfies both.
type ForwardSet interface {
	List() []forward.Tunnel
	Open(ctx context.Context, req forward.Request) (forward.Tunnel, error)
	Close(id string) bool
}

// Usage is the slice of the metrics layer the tables need: live CPU and memory,
// keyed by name. Declared here rather than imported so the UI depends on what it
// uses, not on how metrics are discovered.
type Usage interface {
	PodUsage(ctx context.Context, cluster, namespace string) (map[string]metrics.Usage, error)
	NodeUsage(ctx context.Context, cluster string) (map[string]metrics.Usage, error)
	// ContainerUsage is the same reading one level down, keyed
	// `namespace/pod/container`. An additive page needs it and a table does not: a
	// suggestion is about one container's request, and attributing a pod's whole
	// memory to whichever of its containers sorted first is precisely the wrong
	// number to advise on.
	ContainerUsage(ctx context.Context, cluster, namespace string) (map[string]metrics.Usage, error)
}

// Series is the other slice of the metrics layer: raw PromQL, which is what the
// overview's history charts are drawn from.
//
// Separate from Usage because the two backends are separate. A cluster with
// metrics-server and no Prometheus answers Usage and not this; a cluster with
// kube-prometheus-stack and no metrics-server answers this and not Usage.
// Folding them into one interface would put an em dash on whichever half a
// given cluster can in fact produce.
//
// Its signature is `overview.Metrics` spelled out. Written here rather than
// imported so this package's Options depend on two methods and not on how a
// Prometheus was found; nil is valid and means there is none.
type Series interface {
	Query(ctx context.Context, cluster, promQL string) ([]byte, error)
	QueryRange(ctx context.Context, cluster, promQL string, start, end time.Time, step time.Duration) ([]byte, error)
}

// Terminals is the slice of the terminal layer this package uses.
//
// Everything below is called from this package. The shell and exec handlers
// internal/launch mounts on the concrete type are not listed: an interface
// listing methods nobody uses is a wrong claim about who owns a route.
type Terminals interface {
	// HasClaude reports whether Claude Code is in this image. A false answer
	// leaves the control off the drawer entirely: a button that opens a terminal
	// whose only content is "not installed" is worse than no button.
	HasClaude() bool

	// PTYSupported reports whether this build can open a pseudo-terminal **on
	// the machine this process runs on**.
	//
	// A false answer leaves the cluster shell and the dock's Claude control off
	// the page, for the reason the drawer already gives twice: about a read-only
	// cluster, and about an image built without Claude Code. An icon that opens a
	// terminal which dies at the upgrade leaves a browser reporting no status and
	// a person with nothing to read.
	//
	// It is a question about the *operating system*: on Windows the
	// pseudo-terminal is ConPTY (`internal/terminal/pty_windows.go`), so this
	// is false only where `CreatePseudoConsole` is missing from kernel32, which
	// means a Windows older than 10 1809.
	//
	// It gates only the controls that need a PTY on this machine. Pod `exec`
	// and the node shell get their pseudo-terminal from the kubelet, in a pod,
	// over client-go's `remotecommand`, so they are not behind this.
	//
	// The endpoints still refuse in words for anyone who reaches them another
	// way: a bookmark, a script, a curl. This only stops the console
	// *offering* what it cannot do.
	PTYSupported() bool

	// DesktopTerminalSupported reports whether this build can open a terminal
	// window on the person's own machine, rather than draw one in the page.
	//
	// True only on the Windows desktop binary, because that is the only build
	// where the server and the person are on the same machine. In the container
	// a window would open on a machine with no display, and the console would
	// report success while nobody saw anything.
	//
	// Independent of `PTYSupported`: on Windows 10 1809 or later both are true,
	// and the drawer prefers the dock, since a panel survives navigation. The
	// window is the fallback where there is no ConPTY, and is kept because a
	// real Windows Terminal tab has the person's own font, scrollback and key
	// bindings; internal/terminal/localterm.go carries that argument.
	DesktopTerminalSupported() bool

	// ReadOnlyFence reports whether the read-only guardrail is actually in
	// front of kubectl and helm in a dock session, as measured at start-up
	// rather than as intended.
	//
	// Asked because this page *promises* it. The Infos tab in Settings says
	// that `KT_CLAUDE_READONLY` fences every dock session by default, and that
	// holds only if the override a session resolves is KubeTower's — a shim is
	// only written when
	// kubectl was on this process's PATH at start-up, and
	// `writeReadOnlyShims` logs one line and continues when it was not. A
	// guardrail believed and absent is worse than one absent and known, so the
	// page says which of three things is true instead of asserting the
	// comfortable one.
	//
	// State is "intact", "absent" or "unmeasured" — three because a probe that
	// could not run is not a fence known to be missing. Two
	// plain strings rather than the struct, for this interface's usual reason:
	// this package declares what it needs from the terminal layer instead of
	// importing it.
	ReadOnlyFence() (state, detail string)

	// OpenDesktopTerminal opens that window, on one cluster: a plain shell.
	//
	// Plain strings rather than a request struct because this package declares
	// what it needs from the terminal layer instead of importing it, and a struct
	// in the signature would have dragged that import in.
	//
	// **It refuses a read-only cluster on its own account**, whatever the drawer
	// chose to draw. The guardrail there is a `#!/bin/sh` file named `kubectl` on
	// the session PATH, and Windows resolves executables through `PATHEXT` — so
	// the shim would be walked straight past. Two opinions, because the failure is
	// silent and the console would look exactly the same either way.
	OpenDesktopTerminal(cluster, namespace, kind, name string) error

	// NodeShellHandler serves a shell on a node: a debug pod created on it,
	// exec-ed into, and deleted when the socket closes.
	NodeShellHandler() http.Handler

	// The same four sessions over api/websocket.md's frame vocabulary, for the
	// SPA. **This package mounts all four**, through internal/api's router, so
	// listing them here is a true claim about who owns the route.
	//
	// The node shell's is the odd one and deliberately so. It takes the node as
	// an argument because internal/api has already spent the plan token and read
	// the node out of the facts the person agreed to — a client that agreed to
	// a shell on one node cannot open one on another by editing a query string,
	// because the query string is not read.
	FramedShellHandler() http.Handler
	FramedExecHandler() http.Handler
	FramedAssistantHandler() http.Handler
	FramedNodeShell(w http.ResponseWriter, r *http.Request, node string)

	// NodeShellImage is the image that pod runs, and SetNodeShellImage is the
	// settings page writing it. The error is advisory in the same way
	// archive.SetSettings's is — a value that validated is in effect whether or
	// not it could be written down, and the page has to be able to say so.
	NodeShellImage() string
	SetNodeShellImage(string) error
}

type Server struct {
	clusters *cluster.Manager
	// ui is Options.UI, resolved by Bundle. Nil on a Server built as a
	// literal, which serves the embedded bundle.
	ui     fs.FS
	usage  Usage
	series Series
	// metricsSources is Options.MetricsSources.
	metricsSources api.MetricsSources
	// windows keeps what usage-window reads came to (metrics.WindowCache).
	// Nil on a Server built as a literal, which reads every window afresh.
	windows *metrics.WindowCache
	// dns keeps what CoreDNS reads came to (metrics.DNSCache), under the same
	// read ceiling. Nil reads afresh.
	dns *metrics.DNSCache
	// claude answers one question: can this image open a Claude session. Nil is
	// valid and means no — a build assembled without the terminal layer.
	claude Terminals
	// setup is what the Infos tab in Settings answers with: where the state lives, and
	// whether a newer build wrote it. See setup.go.
	setup SetupInfo
	// inCluster is Options.InCluster, kept because the decisions it makes are at
	// mount time and the mount happens later than New. Read it there - and in
	// what the console *says about itself*, the capabilities and the health
	// page, which remove no address and so guard nothing. A handler that
	// consults it to refuse is a handler enforcing the profile inside itself,
	// which is the shape docs/SERVER-MODE-THREAT-MODEL.md §9.1 says cannot be
	// established by a walk of the router.
	inCluster bool
	// ownNamespace is the namespace this console's pod runs in, under the
	// server profile; nil outside it. Decided in New from the profile, so the
	// handlers that withhold and refuse a Delete of it read this and never
	// inCluster. See ownnamespace.go.
	ownNamespace func() string
	// gate is the cluster's answer to *may this person do this*: the one the
	// writer reviews every write through, and the one the pages ask before
	// they offer a verb. See access.go and internal/access.
	gate *access.Gate
	// shared and replicated are Options.Shared and Options.Replicated, kept for
	// the mount for the reason inCluster is.
	shared     sharedstate.Store
	replicated bool
	// bars is which hotbar each cluster is on. Never nil — New substitutes an
	// unpersisted one — so the rail has nothing to check before it draws.
	bars *hotbars.Store
	// visited is where the person was, per cluster. In memory; see visited.go.
	visited *visits
	// namespaces is the selector's list, held per cluster. See namespaces.go.
	namespaces *namespaceCache
	// menuAccess is the in-cluster menu's verdicts on which kinds the console
	// may list (listable.go).
	menuAccess *menuAccess
	// occupancy is whether each kind holds any object, as the console reads
	// it, held per cluster (navoccupancy.go).
	occupancy *occupancyHold
	// views is what the person last did to each list, so a menu link comes back
	// to the order and the filter they left. In memory only — see views.go for why
	// this one is not a file like every other arrangement here.
	views *viewMemory
	// columns is how wide each table's columns are and in what order. Never nil,
	// for the same reason as bars: every list page reads it, and a nil check per
	// table is a nil check somebody will forget on the nineteenth kind.
	columns *columns.Store
	// seen is the last release whose "What's new" was seen. Never nil.
	seen *seen.Store
	// plates is what a person calls each cluster and what color they gave
	// it. Never nil, for the same reason as bars and columns: the rail reads it
	// on every page, and a nil check per tile is one somebody will forget.
	//
	// A label store and nothing more. Nothing in this package ever asks it which
	// cluster a name belongs to, because it cannot be asked — see the package
	// comment on internal/nameplates.
	plates *nameplates.Store
	// extras is which of the additive pages this installation runs — what
	// the deployment pinned (KT_EXTRAS), then what a person checked, then off. Never
	// nil, for the same reason as bars and columns: the menu asks it on every
	// page and four routes ask it before they answer.
	extras *extras.Store
	// kindPrefs is Options.KindPrefs; see kindPrefsOffered.
	kindPrefs *kindprefs.Store
	// extrasWrites makes reading what an extra's switch or setting was and
	// writing it one critical section, so each audit record's `was` is the
	// value its own write replaced. Its own lock, not the
	// store's: it is held across the `use` review and the record, and the
	// store's is taken by every page that asks whether an extra is on. Writes
	// are rare, one person's act each; serializing them costs nothing.
	extrasWrites sync.Mutex
	// tunnels is what extension.Host.Forward opens with, or nil.
	tunnels metrics.Tunneller
	// execs counts the commands extensions run through extension.Host.Exec;
	// made on first use (execShare).
	execs     *countedExecs
	execsOnce sync.Once
	// archive is the kept window of events and logs, or nil.
	archive Archives
	// forwards is the live set of port forwards, or nil.
	forwards Forwards
	// health is the per-cluster probe, or nil; recorder is the event poller, or
	// nil. Both are read by one page and by nothing else.
	health   Health
	recorder Recorders

	// Live CPU and memory, held briefly and fetched once per key. See
	// usagecache.go — fetching them on every render would make clicking slow.
	usageMu   sync.Mutex
	usageHeld map[string]*usageEntry
	// kubelets is what each kubelet said lately, shared by the Disk column and
	// Host.Usage. See kubeletcache.go. Nil on a Server not built by New, which
	// then reads every time.
	kubelets *kubeletCache
	// custom is the CRD catalog. Built here rather than passed in: it holds
	// nothing but a per-cluster cache and a dynamic client, has nothing to
	// configure, and every page needs it — the navigation cannot draw itself
	// without knowing what this cluster serves.
	custom *customresources.Registry

	// helm is the Helm release reader. Built in New rather than passed in,
	// because it holds nothing but the cluster manager and this page is not
	// optional — the same argument as the CRD registry and the drawer's three
	// readers below. A one-method-pair interface rather than the concrete
	// `*helm.Reader` for the same reason an additive page has one: the four
	// absence sentences are what keeps this page from drawing a failure as an
	// empty list, and a handler holding a concrete reader cannot be handed a
	// report to draw.
	helm helmReader

	// The detail drawer's three sources: the object itself with its events, what
	// changed on it, and what its containers said.
	objects *detail.Reader
	changes *changes.Reader
	logs    *logs.Streamer

	// The one thing in this binary that changes a cluster, and the callback that
	// says where it may not. Never nil — see New.
	writer   *edit.Writer
	readOnly func(string) bool
	// readOnlyUnmatched is Options.ReadOnlyUnmatched, read by Capabilities.
	readOnlyUnmatched []string
	// The verbs — delete, evict, restart, cordon, drain, suspend, trigger. Built
	// on the writer above rather than beside it, so every one of them is refused
	// on a cluster in KT_READONLY_CLUSTERS by the same `Locate` a manifest save
	// goes through, and there is one guardrail rather than two.
	act *actions.Runner

	// sessions and probe are the JSON contract's two pieces that live outside
	// this package: the front door and the readiness probe. Both may be nil.
	sessions api.Sessions
	signIn   api.SignIn
	probe    api.Probe
}

func New(opts Options) (*Server, error) {
	if opts.Clusters == nil {
		return nil, fmt.Errorf("web: a cluster manager is required")
	}
	readOnly := opts.ReadOnly
	if readOnly == nil {
		readOnly = func(string) bool { return false }
	}
	bars := opts.Hotbars
	if bars == nil {
		// An arrangement with nowhere to write itself. The rail must draw either
		// way — a console that will not start because it could not find a state
		// directory is worse than one that forgets where the tiles were.
		bars = hotbars.New("", nil)
	}
	cols := opts.Columns
	if cols == nil {
		// Same bargain as the rail's: a table that forgets its widths beats a
		// console that will not draw one.
		cols = columns.New("", nil)
	}
	plates := opts.Nameplates
	if plates == nil {
		// And again: a rail that forgets what somebody called a cluster still
		// draws every cluster, under the name the kubeconfig gives it.
		plates = nameplates.New("", nil)
	}
	if opts.InCluster || opts.Replicated {
		// **Where the preference writes are not registered, the preference files
		// are not read either.** Under the server profile and under
		// several replicas nobody can change the rail or the columns, so a
		// `hotbars`, `nameplates.json` or `columns.json` left on the volume -
		// written by a desktop that shared the directory, say - would be one
		// person's arrangement frozen into everybody's
		// defaults, and the list payload's stored `visible` would contradict the
		// column's own `secondary`, which is what the browser-kept columns take as
		// the default. So these three are the process's own memory, empty, and
		// every read draws the defaults the contract promises. The files are left
		// where they are: reading nothing is enough, and deleting is not this
		// process's to decide. The extras stay: they are per cluster, not per
		// person, and their switch is still registered on one replica.
		bars, cols, plates = hotbars.New("", nil), columns.New("", nil), nameplates.New("", nil)
	}
	xtras := opts.Extras
	if xtras == nil {
		// And again, with one difference worth stating: forgetting the extras
		// means forgetting them *off*, because off is the default. A console
		// with no state directory draws no Extra heading at all, which is the
		// same thing a fresh install draws.
		xtras = extras.New("", nil, nil)
	}
	gate := opts.Access
	if gate == nil {
		// With no audit log: a Server built without the process's gate is a
		// test or a desktop, and under the server profile that gate refuses
		// every act rather than acting unrecorded.
		gate = NewGate(opts.Clusters, opts.InCluster, func(format string, args ...any) { log.Printf("[kubetower] "+format, args...) }, nil)
	}
	// Every reader below reads through the same gate the writes go through: a
	// list, a drawer, a log or a release is reviewed for the person asking
	// before it reaches them, and the answers are the same held minute.
	objects := detail.NewReader(opts.Clusters, gate)
	// The CRD catalog, built before the literal because the security reader is
	// built *on* it: one catalog, one cache, one answer to "what does this
	// cluster serve".
	catalogue := customresources.NewRegistry()
	// The write path, built before the server rather than inside its literal
	// because the verbs in `internal/actions` are built *on* it: delete, evict,
	// cordon and the rest all go through this Writer's Locate, which is the one
	// implementation of the KT_READONLY_CLUSTERS guardrail.
	writer := edit.New(edit.Options{
		Resolve:  editResolver{objects: objects},
		ReadOnly: readOnly,
		Logf:     func(format string, args ...any) { log.Printf("[kubetower] "+format, args...) },
		Access:   gate,
	})
	windows := metrics.NewWindowCache()
	s := &Server{
		clusters: opts.Clusters,
		ui:       opts.UI,
		usage:    opts.Metrics,
		tunnels:  countTunnels(opts.Tunnels, maxExtensionTunnels),

		series:         opts.Series,
		metricsSources: opts.MetricsSources,
		windows:        windows,
		dns:            metrics.NewDNSCache(windows),
		claude:         opts.Terminal,
		setup:          opts.Setup,
		inCluster:      opts.InCluster,
		shared:         opts.Shared,
		replicated:     opts.Replicated,
		bars:           bars,
		visited:        newVisits(),
		namespaces:     newNamespaceCache(),
		menuAccess:     newMenuAccess(),
		occupancy:      newOccupancyHold(),
		views:          newViewMemory(),
		columns:        cols,
		plates:         plates,
		extras:         xtras,
		kindPrefs:      opts.KindPrefs,
		seen:           seenOrMemory(opts.Seen),
		archive:        opts.Archive,
		forwards:       opts.Forwards,
		health:         opts.Health,
		recorder:       opts.Recorder,
		usageHeld:      map[string]*usageEntry{},
		kubelets:       newKubeletCache(),
		custom:         catalogue,
		// Helm needs no catalog: a release is a core Secret, which is the
		// whole reason this page is core and the other two are extras.
		helm: helm.NewReader(opts.Clusters, gate),
		// The three readers behind the detail drawer. Built here rather than
		// passed in for the same reason as the CRD registry: each holds nothing
		// but a client and a cache, has nothing to configure, and the drawer is
		// not optional.
		objects: objects,
		changes: changes.NewReader(opts.Clusters, gate),
		// The write path. Built here for the same reason as the readers, and
		// given the same resolver they use so no second REST mapper exists.
		readOnly:          readOnly,
		readOnlyUnmatched: append([]string(nil), opts.ReadOnlyUnmatched...),
		writer:            writer,
		gate:              gate,
		act:               actions.New(actions.Options{Writer: writer}),
		// The archive is handed in so a stream can be teed into it. Nil is
		// valid and means nothing is kept — see internal/logs, which nil-checks
		// it. Passing it here rather than a separate field keeps the one
		// interface this package already declares for the archive.
		logs: logs.New(opts.Clusters, gate, nil, opts.Archive),

		sessions: opts.Sessions,
		signIn:   opts.SignIn,
		probe:    opts.Probe,

		// The console's own namespace offers no Delete under the server
		// profile.
		ownNamespace: ownNamespaceOf(opts.InCluster),
	}
	// One person's share of the sixteen follows, under the server profile only:
	// there the sixteen are every person's.
	if opts.LogFollowMax > 0 {
		s.logs.SetFollowMax(opts.LogFollowMax)
	}
	if opts.InCluster {
		s.logs.SharePerPerson(logs.PerPersonStreams)
		// And of the sixteen tunnels extensions dig.
		if counted, ok := s.tunnels.(*countedTunnels); ok {
			counted.sharePerPerson(perPersonExtensionTunnels)
		}
	}
	return s, nil
}

// EndStreamsOf ends every log stream the named person follows; see
// logs.Streamer.EndStreamsOf. For the sign-out hook internal/launch installs.
func (s *Server) EndStreamsOf(who identity.Who) int { return s.logs.EndStreamsOf(who) }

// OpenLogStreams is how many log follows are open; see logs.Streamer.Following. For
// the macOS window's App Nap guard, which internal/launch installs.
func (s *Server) OpenLogStreams() int { return s.logs.Following() }

// Clusters is the cluster list exactly as the console's own `GET` of it answers
// - the same function, `apiClusters.List`, called in-process rather than over
// HTTP. For the macOS window's Cluster menu, so that the menu and the rail
// cannot disagree about which clusters there are, what they are called, which
// are hidden and in what order: a second reading of the kubeconfig in
// internal/launch would be a second opinion on all four.
//
// It carries the list's failure too, and that is the point of calling this
// rather than `Manager.Contexts()`: a kubeconfig that would not parse is
// `unreadable` here and an empty slice there, and the menu says two different
// things for the two.
func (s *Server) Clusters(ctx context.Context) ([]api.Cluster, *api.Failure) {
	return apiClusters{s: s}.List(ctx)
}

// Mount registers the UI on a router the caller owns, so the front door can wrap
// everything at once and a route added later cannot escape it.
func (s *Server) Mount(mux *http.ServeMux) {
	// The installable-application assets: the icon, the manifest and the service
	// worker. What makes this a window with its own taskbar entry rather than a
	// tab, and it is served from the one prefix the front door lets through
	// unauthenticated - a browser fetches a manifest without the session cookie,
	// and guarded it is handed the login page where a manifest belongs, with no
	// error anywhere and the install offer simply absent.
	//
	// **The SPA's own document links them**: `web/index.html` carries the three
	// links and the registration. Losing them there would remove the
	// installable application with nothing failing.
	mux.Handle("GET "+auth.PWAPrefix, http.StripPrefix(auth.PWAPrefix, s.pwa()))

	// The third-party notices, for a signed-in person; see notices.go for the
	// desktop's copy outside the guard.
	mux.Handle("GET "+NoticesPath, s.NoticesHandler())

	// Everything an extension carries, behind the per-cluster gate. The core
	// mounts, so an extension cannot forget the gate.
	s.mountExtensions(mux)

	// The JSON contract: the only surface a browser talks to that is not the
	// bundle below.
	//
	// See internal/web/apimount.go for the adapter and internal/api for the
	// contract itself.
	s.mountAPI(mux)

	// The compiled console, at `/`.
	//
	// See internal/web/spa.go for the fallback, the caching, and what this
	// answers when no bundle was built.
	s.mountSPA(mux)
}

// pwa serves the installable-application assets out of the embedded tree.
//
// The one header that matters is Service-Worker-Allowed. A worker's scope
// defaults to the directory it was served from, so one at /kt-pwa/sw.js would
// control /kt-pwa/ and nothing else — it would register without complaint,
// control no page, and the install offer would never appear. The header widens
// the scope to the whole site; the registration asks for it explicitly, and both
// halves are required.
func (s *Server) pwa() http.Handler {
	files, err := fs.Sub(assets, "static/pwa")
	if err != nil {
		// The directory is embedded at compile time, so this cannot happen at
		// run time — but returning a handler that says so beats a nil one.
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "the application assets are missing from this build", http.StatusInternalServerError)
		})
	}
	server := http.FileServerFS(files)

	// The manifest is served from memory rather than from the tree, because what
	// it should say depends on which icons this build carries: the bitmaps exist
	// only in the container image. See internal/web/pwamanifest.go — computed
	// once here, since an embedded FS cannot change under a running process.
	manifest := []byte(nil)
	if raw, err := fs.ReadFile(files, "manifest.webmanifest"); err == nil {
		var dropped []string
		manifest, dropped = manifestNamingWhatExists(files, raw)
		if len(dropped) > 0 {
			s.log("PWA: the manifest does not name %s this build does not carry (%s). "+
				"That is the ordinary state of a build made without Docker — icon.svg is "+
				"committed and the bitmaps are rasterized by the image's `icons` stage",
				plural(len(dropped), "icon", "icons"), strings.Join(dropped, ", "))
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "sw.js"):
			w.Header().Set("Service-Worker-Allowed", "/")
			// A cached worker is a worker that cannot be replaced.
			w.Header().Set("Cache-Control", "no-cache")

		case strings.HasSuffix(r.URL.Path, ".webmanifest"):
			// Go's type table has no entry for this extension, so the file
			// server sniffs it and answers text/plain. Chromium tolerates that;
			// not every browser does, and the symptom when one does not is the
			// install offer quietly failing to appear with nothing logged
			// anywhere. Being explicit costs one line.
			w.Header().Set("Content-Type", "application/manifest+json")
			if manifest != nil {
				// No ETag and no Last-Modified, same as the file server would
				// send for an embedded file, plus the no-cache
				// the rest of /kt-pwa/ already gets for the worker.
				w.Header().Set("Cache-Control", "no-cache")
				w.Write(manifest)
				return
			}
		}
		server.ServeHTTP(w, r)
	})
}

func (s *Server) log(format string, args ...any) {
	log.Printf("[kubetower] "+format, args...)
}

// --- the page ----------------------------------------------------------------

// navGroup is one heading in the left menu and the kinds under it.
type navGroup struct {
	Label string
	Links []navLink
	// Sub nests one further level, which exists for exactly one thing: the
	// cluster's custom resources, grouped by API group inside a single "Custom
	// Resources" heading. Flat, a cluster running cert-manager, Calico, Argo and
	// Trivy contributes twenty-odd top-level headings and buries the built-in
	// kinds a person uses hourly under vendors they touch twice a year.
	//
	// One level and no more. A menu that can nest arbitrarily is a menu that
	// will, and this one has to stay readable at a glance.
	Sub []navGroup
	// Open forces the disclosure open. A group holding the current page is
	// always open: following a link must never land you on a page whose entry
	// is folded away.
	Open bool
	// Note is why this group is empty, on the one group that can fail to fill
	// itself: the custom resource kinds come from an API call, and a cluster
	// whose CRDs cannot be listed must not look like a cluster that has none.
	Note string
	// AccessNote says kinds were left out of this group because this console
	// may not list them, and that the read has to be granted. Apart from Note
	// because it is drawn apart: first in the group and in the warning tone,
	// where Note is the quiet line under it.
	AccessNote string
	// Extra marks the one heading at the very bottom of the menu that holds
	// pages rather than kinds: a question about the whole cluster whose answer is
	// assembled from several kinds at once.
	//
	// A flag and not a position, because the template draws it differently and
	// "the last group" is a rule that breaks the first time something is
	// appended after it. See extraNav in extranav.go.
	Extra bool
	// Lazy is the URL that fills this group the first time it is opened, and it
	// is set on exactly one group: Custom Resources.
	//
	// The reason is cost. Every boosted navigation carries
	// `hx-select-oob="#menu,#rail"`, so the menu is rebuilt on every click — and
	// building the custom group means listing the cluster's CRDs, which on a
	// cluster with many of them is **over 20 MB uncompressed and over half a
	// second**, for a disclosure that is *closed*. On such a cluster that list
	// is most of what a cluster switch costs.
	//
	// Set only when the group cannot already be needed. A page that *is* a custom
	// resource builds the group in full, because the entry has to be visible and
	// open — following a link must never land you on a page folded away — and the
	// catalog is warm anyway, the page having just used it.
	//
	// Empty means the group is complete as rendered.
	Lazy string
	// Deferred is the same fact as Lazy without the transport: this group's
	// contents are not here and are fetched separately. It is what crosses into
	// the contract as `NavGroup.deferred`.
	//
	// **Two fields rather than one, deliberately, and there is exactly one place
	// that sets either.** Deriving `deferred` from `Lazy != ""` would tie the
	// contract to an htmx attribute: without `Lazy` every group would cross as
	// complete — which draws the custom resource heading empty and tells the
	// person this cluster has no custom resources.
	Deferred bool
}

// navLink is one entry in a row of links — the kind switcher above a list, and
// the tabs on a detail page. Deliberately dumb: the template must not have to
// know what a Kubernetes kind is to draw one.
type navLink struct {
	Label   string
	Href    string
	Current bool
	// Namespaced is true on the link to a namespaced kind's list, and on the
	// Helm releases', which are read by namespace too. It crosses
	// into `api.Ref.Namespaced` for the browser's menu, which carries the
	// namespace chosen on one list to every other such link; the templates
	// never read it.
	Namespaced bool
	// Aliases are kubectl's other names for the kind, `svc` for Services. They
	// cross into `api.Ref.Aliases`, which the menu's jump box matches; the
	// templates never read them.
	Aliases []string
}

// firstLine keeps an error to what fits on a page. A Kubernetes error can be a
// paragraph, and the first line is the sentence that names the problem.
// firstLine is textutil's, kept under the local name for this package's callers.
//
// Those callers put its result into table reason cells, menu notes, drawer
// errors and `http.Error` bodies, so it cuts on a bare "\r" as well as "\n",
// and it caps: a `.grid td` is `white-space: nowrap`, so an uncapped
// 4 000-character refusal would not wrap, it would widen the table past its
// panel, where `overflow: hidden` takes the rest.
func firstLine(s string) string { return textutil.FirstLineN(s, detailLine) }

// detailLine is 200, the cap the other packages use for status and detail
// text. The refusal path is deliberately not this: internal/actions and
// internal/edit cap at 300, because the actionable half of an API server's
// rejection runs past 200 and this cell is not where that is read.
const detailLine = 200

// seenOrMemory is the store handed in, or one in memory.
func seenOrMemory(store *seen.Store) *seen.Store {
	if store == nil {
		return seen.New("", nil)
	}
	return store
}

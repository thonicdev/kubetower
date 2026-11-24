package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/columns"
	"github.com/thonicdev/kubetower/internal/customresources"
	"github.com/thonicdev/kubetower/internal/edit"
	"github.com/thonicdev/kubetower/internal/extras"
	"github.com/thonicdev/kubetower/internal/health"
	"github.com/thonicdev/kubetower/internal/hotbars"
	"github.com/thonicdev/kubetower/internal/nameplates"
	"github.com/thonicdev/kubetower/internal/nodeshell"
	"github.com/thonicdev/kubetower/internal/overview"
	"github.com/thonicdev/kubetower/internal/registry"
	"github.com/thonicdev/kubetower/internal/resources"
	"github.com/thonicdev/kubetower/internal/statefile"
	"github.com/thonicdev/kubetower/textutil"
)

// The JSON API.
//
// # Why the adapter is here and the contract is not
//
// `internal/api` owns what a response must look like and what a state means. It
// knows nothing about client-go, about how a page is drawn, or about how a
// cluster is reached — and it must not learn, because the moment it does there
// are two places that answer "what does unreachable mean" and they diverge on
// the first bug fixed in one of them.
//
// This file is the other half: it knows both sides. Everything below reads the
// domain packages — `internal/resources`, `internal/detail`, `internal/helm`,
// `internal/health`, the preference stores — and translates.

// mountAPI registers the JSON contract. Called from Mount, last.
func (s *Server) mountAPI(mux *http.ServeMux) {
	api.New(api.Deps{
		Sessions: s.sessions,
		SignIn:   s.signIn,
		Profile:  apiProfile{s},
		// The profile itself, so `internal/api` decides its registrations rather
		// than inferring them from a capability that means something else.
		InCluster: s.inCluster,
		// The store the plans live in, and whether this is one of several
		// replicas - which takes the pod-local writes away. See api.Deps.
		Shared:     s.shared,
		Replicated: s.replicated,
		// And the state directory's own answer, for the same reason: the
		// settings writes are absent where nothing they save would survive.
		StateEphemeral: s.setup.StateEphemeral,
		Clusters:       apiClusters{s},
		Settings:       apiSettings{s},
		Updates:        s.updateCheck(),
		Installer:      s.updateInstaller(),
		ToolWindows:    s.toolWindows(),
		Prefs:          apiPrefs{s},
		KindPrefs:      apiKindPrefs{s},
		Reads:          apiReads{s},
		Acts:           apiActs{s},
		Prereqs:        apiPrereqs{s: s},
		Dock:           apiDock{s},
		Channels:       apiChannels{s},
		Probe:          s.probe,
	}).Mount(mux)
}

// --- Translating a cluster read into one of the five states ------------------

// stateOf classifies an error from a domain package.
//
// One function, because the classification is the contract's load-bearing
// decision and five copies of a switch is five opinions. Where a package already
// answers with its own vocabulary — `internal/helm` and `internal/health` both
// do — that vocabulary is mapped rather than re-derived, since those packages
// already draw their own distinctions.
func stateOf(err error, scope api.Scope) *api.Failure {
	if err == nil {
		return nil
	}
	return withWholeLine(classifyRead(err, scope), err)
}

// detailLimit is the contract's ceiling on `Reason.detail` (api/openapi.yaml).
const detailLimit = 8192

// withWholeLine carries the error's whole text as the detail where the sentence
// was cut.
//
// `firstLine` stops at detailLine runes, and an API server's refusal is often
// longer: `... cannot list resource "deployhooks" in API group
// "demo.example.com" at the cluster scope` would reach the page cut inside the
// group's name, with no way to read the rest. The sentence stays one line, for
// the notice, and the cluster's own words go in `detail`, which the notice
// draws whole beneath it, as the contract says detail is for. Only where the
// sentence is this error's, cut: an arm that wrote its own sentence, or set a
// detail already, is left alone, and a message that fitted carries no copy of
// itself.
func withWholeLine(f *api.Failure, err error) *api.Failure {
	if f == nil || f.Reason.Detail != "" {
		return f
	}
	whole := strings.TrimSpace(err.Error())
	if f.Reason.Message != firstLine(whole) || f.Reason.Message == textutil.FirstLine(whole) {
		return f
	}
	return f.Detailed(textutil.Clip(whole, detailLimit))
}

// classifyRead is stateOf's arms, one per state, with the sentence cut to a line.
func classifyRead(err error, scope api.Scope) *api.Failure {
	message := firstLine(err.Error())
	// **The access review's own answer, before anything reads the sentence.**
	// A person's denial is the cluster saying no to them - forbidden, and
	// retrying will not help; a review that could not be made is neither a no
	// nor an outage, and has its own code, as the write door answers it.
	// **A list the console declined to narrow**: its own ceiling, the same
	// answer every time until a namespace is chosen - not retryable, and not a
	// review that failed.
	var notNarrowed *access.NotNarrowed
	if errors.As(err, &notNarrowed) {
		return api.Fail(api.StateUnreadable, api.CodeNotSupportedHere, message).Within(scope)
	}
	// **A list past the console's ceiling on any one list**, for the same
	// reason: re-polled, it would read a hundred pages every two seconds to
	// fail again.
	// **A read that panicked** is a defect in this console, confined to its
	// panel by internal/overview's gather: neither the cluster's answer nor an
	// absence, and never `not_installed`, which a page may draw as nothing.
	// Retryable: the next read may not take the same path.
	var crash overview.Crash
	if errors.As(err, &crash) {
		return api.Fail(api.StateUnreadable, api.CodeUnclassified, message).Within(scope).Retryable()
	}
	// **A label selector that does not parse** is the request's fault, and
	// asking again cannot change it.
	var invalid *resources.InvalidSelector
	if errors.As(err, &invalid) {
		return api.InvalidSelector(message).Within(scope)
	}
	var tooLarge *resources.TooLarge
	if errors.As(err, &tooLarge) {
		return api.Fail(api.StateUnreadable, api.CodeNotSupportedHere, message).Within(scope)
	}
	var refused *access.Refused
	if errors.As(err, &refused) {
		if refused.Answer.Decision == access.Denied || refused.Answer.Decision == access.NotInProfile {
			return api.Forbidden(message).Within(scope)
		}
		return api.Fail(api.StateUnreadable, api.CodeAccessUnreviewable, message).Within(scope).Retryable()
	}
	// **The console's own access guard holding a request back** is neither the
	// cluster's no nor the person's: a defect in the console, which no retry
	// and no RoleBinding changes (internal/access/transport.go). Unclassified
	// rather than forbidden, raw or already classified by internal/edit.
	if errors.Is(err, access.ErrNotReviewed) {
		return api.Fail(api.StateUnreadable, api.CodeUnclassified, message).Within(scope)
	}
	if refusal, ok := edit.AsRefusal(err); ok {
		switch refusal.Kind {
		case edit.RefusedReadOnly:
			return api.Forbidden(refusal.Reason).Within(scope)
		case edit.RefusedByAPI, edit.RefusedByAccess, edit.RefusedNotInProfile:
			return api.Forbidden(refusal.Reason).Within(scope)
		case edit.RefusedUnreviewable, edit.RefusedUnrecorded, edit.RefusedNotSent:
			// The same code for all three - the contract has one for "the
			// console could not act" - and each with its own sentence.
			return api.Fail(api.StateUnreadable, api.CodeAccessUnreviewable, refusal.Reason).Within(scope).Retryable()
		case edit.RefusedUnsent:
			return api.Fail(api.StateUnreadable, api.CodeUnclassified, refusal.Reason).Within(scope)
		case edit.RefusedUnanswered:
			// Sent, no answer back: the code for a question put and not
			// answered. Not retryable - it may have happened.
			return api.Fail(api.StateUnreadable, api.CodeTimeout, refusal.Reason).Within(scope)
		}
		return api.Unreadable(refusal.Reason).Within(scope)
	}
	// **The credential step failed, so nothing reached the cluster.** Not
	// `cluster_unreachable`: the cluster may be fine, and the person renews a
	// session rather than looks at a VPN. The codes are the cluster list's own
	// (`clusterState`), so the page can tell that every read of this cluster
	// will fail the same way and ask the probe for the cluster's verdict.
	// Retryable both: a renewed session answers the next read.
	switch health.Credential(err) {
	case health.StateSSO:
		return api.Fail(api.StateUnreadable, api.CodeCredentialsExpired, message).Within(scope).Retryable()
	case health.StatePlugin:
		return api.Fail(api.StateUnreadable, api.CodeCredentialsRefused, message).Within(scope).Retryable()
	}
	lower := strings.ToLower(message)
	// The 401-or-403 question is `internal/health`'s to answer, not this file's
	// to answer again: `health.Denied` reads the status client-go sent, through
	// whatever wrapped it, and falls back to the words only where an error
	// arrives with no status at all — the log stream's event text is one such
	// caller of this function.
	denial := health.Denied(err)
	switch {
	case errors.Is(err, resources.ErrUnknownKind):
		return api.Fail(api.StateAbsent, api.CodeNoSuchKind, message).Within(scope)
	case errors.Is(err, customresources.ErrUnknownKind), errors.Is(err, customresources.ErrNotServed):
		// **A path that names no kind is a not-found, not an outage.** A
		// segment that is no built-in kind is looked up among the cluster's
		// CRDs, and the catalog's answer - no CRD by that name, or one no
		// version serves - is the cluster having answered. Its sentence carries
		// neither "not found" nor "no matches for", so without this arm a path
		// like `/c/in-cluster/settings` would fall to the last line below and
		// draw "The cluster did not answer" under a healthy node.
		// `ErrAmbiguousKind` is not this arm's: that kind exists, twice, and
		// "nothing serves this kind" would be false of it.
		return api.Fail(api.StateAbsent, api.CodeNoSuchKind, message).Within(scope)
	case denial == health.DenialUnauthorized:
		// **Retryable, and the one failure on a read a person fixes
		// themselves** — `aws sso login`, ten seconds. Kept out of the
		// forbidden arm below, so a lapsed session on a list, drawer, overview
		// or log read is not reported as wrong permissions with *retry will not
		// help*: the collapse clusterState, further down this file, refuses for
		// the cluster list too. Tested before the forbidden arm on purpose: a
		// 401 that explains itself can carry the other word too.
		return api.Fail(api.StateUnreadable, api.CodeCredentialsExpired, message).
			Within(scope).Retryable()
	case denial == health.DenialForbidden, strings.Contains(lower, "not allowed"),
		strings.Contains(lower, "cannot list"):
		return api.Forbidden(message).Within(scope)
	case apierrors.IsTimeout(err), apierrors.IsServerTimeout(err):
		// **A timeout by its status**, ahead of the word arms below: a 504 from
		// the aggregator in front of metrics-server says "the server was unable
		// to return a response in the time allotted", which carries no
		// "timeout" for a word match and would otherwise read as
		// `cluster_unreachable`. The same two predicates internal/health and
		// internal/metrics answer a timeout with.
		return api.Fail(api.StateUnreachable, api.CodeTimeout, message).Within(scope).Retryable()
	case strings.Contains(lower, "not found"), strings.Contains(lower, "could not find"),
		strings.Contains(lower, "no matches for"):
		return api.Fail(api.StateAbsent, api.CodeNoSuchKind, message).Within(scope)
	case health.TimedOut(err):
		// **`internal/health`'s answer, not a word match of this file's own.**
		// client-go puts `?timeout=30s` into every request URL
		// (internal/cluster/manager.go sets cfg.Timeout), so a refused
		// connection quoting its address, or a cluster named
		// `prod-timeout-eks`, would match a plain `timeout`. TimedOut is
		// `classify`'s phrases, after the address is removed, behind the typed
		// checks - a context deadline, a network timeout, and client-go's
		// sentence for a 504 that lost its type.
		return api.Fail(api.StateUnreachable, api.CodeTimeout, message).Within(scope).Retryable()
	}
	// Everything else is the cluster not answering. It is the safe reading:
	// `unreachable` says "ask again", `unreadable` says "your credentials are
	// wrong", and telling an operator the second when the first is true sends
	// them to look at an IAM policy over a dropped VPN.
	return api.Unreachable(message).Within(scope)
}

// --- Session and profile -----------------------------------------------------

type apiProfile struct{ s *Server }

// Capabilities is what the running build offers.
//
// Every false here is also refused at its own endpoint: `openDesktopTerminal`
// checks localShell through the same fields, the node-shell upgrade checks its
// own, and the forward endpoints check theirs. Hiding a control is cosmetic.
//
// Nothing here is the cluster's answer about the person: whether Shell is
// drawn on a pod is that pod's page's (DetailPage.shell, access.go), since one
// question about every namespace answered no to a person granted some of them.
func (p apiProfile) Capabilities(_ *http.Request) api.Capabilities {
	s := p.s
	caps := api.Capabilities{
		Mode: "container",
		// Pod exec reaches the kubelet through client-go's remotecommand and
		// touches no pseudo-terminal on this machine, so it is not behind
		// PTYSupported (internal/web/drawer.go says so at both call sites).
		PodExec: true,
		// The menu's Port forwards entry reads the same answer
		// (forwardsnav.go), so the two cannot disagree about a profile.
		PortForward: s.offersForwards(),
		// What `mountAPI` registered, described rather than enforced: under
		// the server profile and under several replicas the preference writes
		// do not exist.
		Preferences: !s.inCluster && !s.replicated,
		Replicated:  s.replicated,
		// Whether each person's choices about the kinds column are kept here
		// (apikindprefs.go).
		KindPreferences: s.kindPrefsOffered(),
		Version:         s.setup.Version,
	}
	if s.inCluster {
		// The server profile, and what it reports is what it registered.
		//
		// **Nothing here is a refusal.** `mountAPI` did not register the
		// desktop-terminal route and `internal/launch` did not register the three
		// `/kt-api/` upgrades, so these zero values *describe* the router rather
		// than guard it: a route that is not registered is a property of the
		// router, a refusal is a property of one code path, and that is why the tests
		// for this walk the mux instead of serving a request.
		//
		// Three members move, and only in this branch: `Mode` here, and
		// `podExec` and `portForward` below, which are answers rather than
		// registrations. Elsewhere the mode is `"container"`, or `"desktop"`
		// through the override below.
		//
		// `shared` is the contract's own word for this profile: "a console
		// several people sign in to", which "disables everything that would
		// otherwise run with the process's own credentials". A value
		// api/openapi.yaml does not declare would give every generated client a
		// type that cannot hold it.
		caps.Mode = "shared"
		caps.ReadOnly = p.readOnly()
		// **Two members that are answers rather than registrations**: on a pod
		// neither can work.
		//
		// `podExec` is whether this build can open a shell in a pod at all.
		// Whether it is drawn on one is asked for that pod, for the person and
		// for the console, and rides on its page (Server.shellFor in
		// access.go); the exec is reviewed for the person again when it opens
		// (internal/terminal). One review of `create pods/exec` across every
		// namespace would draw Shell nowhere for a person granted it in some.
		//
		// `portForward` false is *show the command*: a forward binds a port in
		// this process, and the browser's `127.0.0.1` is not the pod. The three
		// forward operations are not registered under the profile, so false
		// describes the router, whatever `s.forwards` holds.
		caps.PodExec = s.mayExec()
		caps.PortForward = false
		return caps
	}
	if s.claude != nil {
		// **Two facts, two fields.** `localShell` is the `terminal` channel - a
		// pane in the page, served wherever a pseudo-terminal can be opened
		// beside the process - and `apiChannels.Offers` gates that channel on
		// exactly this. `DesktopTerminalSupported` is a window on the desktop
		// the process runs on and is false in a container: reported here, it
		// would withhold the Topbar's Shell control on every deployment that is
		// not a laptop while the socket behind it works.
		caps.LocalShell = s.claude.PTYSupported()
		caps.DesktopTerminal = s.claude.DesktopTerminalSupported()
		caps.Assistant = s.claude.HasClaude()
		caps.NodeShell = true
		if caps.DesktopTerminal {
			// The one build where the server and the person using it are on the
			// same machine.
			caps.Mode = "desktop"
		}
	}
	if limited, ok := s.forwards.(interface{ Limit() int }); ok {
		caps.PortForwardLimit = limited.Limit()
	}
	// Asked on every read: the native window is drawn after the server
	// starts, and a page in the browser fallback has none.
	if tools := s.toolWindows(); tools != nil {
		caps.ToolWindow = tools.Available()
	}
	caps.ReadOnly = p.readOnly()
	return caps
}

// readOnly reports the guardrail: the profile's default, the terminal
// override as measured, and the markings that matched nothing.
//
// **The honest limit, stated rather than buried:** this console's guardrail is
// *per cluster* — `KT_READONLY_CLUSTERS` — and `Capabilities.readOnly` in the
// contract is a property of the profile. The two are not the same shape, so what
// is reported here is the only profile-wide reading that is true: writes are
// refused when they are refused on **every** configured cluster. A console where
// one cluster of fourteen is fenced answers false, which is correct for the
// question asked and does not answer the question an operator actually has.
//
// **This is not the only reading on the wire.** `Cluster.readOnly` is the
// per-cluster fact and `apiClusters.List` fills it from this same callback, so
// a client holding the cluster list knows which of the fourteen are fenced.
// This pair stays what it is: the profile default, and what a control ungated
// by any cluster should obey.
//
// `fence`, `measured` and `detail` are `internal/terminal`'s three-valued
// `Fence` through `Terminals.ReadOnlyFence`: a guardrail that could not be
// probed is `unknown`, never collapsed into `off`. `unmatched` is
// `cluster.ReadOnly.Unmatched`, the markings that matched no cluster.
func (p apiProfile) readOnly() api.ReadOnlyState {
	state := p.writesRefused()
	state.Fence, state.Detail = p.fence()
	state.Measured = state.Fence != api.FenceUnknown
	if len(p.s.readOnlyUnmatched) > 0 {
		state.Unmatched = append([]string(nil), p.s.readOnlyUnmatched...)
	}
	return state
}

// writesRefused is the profile-wide pair: refused when every cluster is marked.
func (p apiProfile) writesRefused() api.ReadOnlyState {
	s := p.s
	if s.readOnly == nil || s.clusters == nil {
		return api.ReadOnlyState{}
	}
	contexts := s.clusters.Contexts()
	if len(contexts) == 0 {
		return api.ReadOnlyState{}
	}
	for _, name := range contexts {
		if !s.readOnly(name) {
			return api.ReadOnlyState{}
		}
	}
	return api.ReadOnlyState{WritesRefused: true, DetailWritesRefused: true}
}

// fence maps the terminal layer's three states onto the contract's three words.
//
// No terminal layer is `unknown` and says so: nothing was probed, which is not
// the same as an override found missing. A state this file has no word for is
// `unknown` too, with the word in the sentence, rather than read as either.
func (p apiProfile) fence() (word, detail string) {
	if p.s.claude == nil {
		return api.FenceUnknown, "No terminal session opens on this console, so no read-only override was probed."
	}
	state, detail := p.s.claude.ReadOnlyFence()
	switch state {
	case "intact":
		return api.FenceOn, detail
	case "absent":
		return api.FenceOff, detail
	case "unmeasured":
		return api.FenceUnknown, detail
	}
	return api.FenceUnknown, fmt.Sprintf("The terminal layer reported the guardrail as %q, which this console has no word for. %s", state, detail)
}

// --- Clusters, navigation, extensions ----------------------------------------

type apiClusters struct{ s *Server }

func (c apiClusters) Known(name string) bool { return c.s.known(name) }

// kubeconfigUnreadable is the failure to report when there are no clusters
// *because a kubeconfig file would not parse*, and nil when there is genuinely
// nothing configured.
//
// The two must not render the same. "You have not set up a cluster yet" invites
// an operator to write a kubeconfig; "~/.kube/config line 14: mapping values
// are not allowed in this context" invites them to fix the one they have.
// Without this the only other signal is a line in the startup log, which in a
// container has scrolled.
//
// `undecodable` and not a new code: it is the same fact the Helm panel reports
// when a release secret will not decode - the thing is there and this process
// could not make sense of it.
//
// Not retryable, and that is a statement about this process rather than about
// the file. The survey and every client were built from one merge at startup, so
// a kubeconfig fixed now is not picked up until the console is restarted, and a
// retry button would return the identical answer.
func kubeconfigUnreadable(survey cluster.KubeconfigSurvey) *api.Failure {
	var bad []cluster.KubeconfigFile
	for _, file := range survey.Files {
		if file.Problem != "" {
			bad = append(bad, file)
		}
	}
	if len(bad) == 0 {
		return nil
	}

	// The path, always, because "a kubeconfig failed to parse" is not actionable
	// when KUBECONFIG names three files.
	paths := make([]string, 0, len(bad))
	details := make([]string, 0, len(bad))
	for _, file := range bad {
		paths = append(paths, file.Path)
		details = append(details, file.Path+": "+firstLine(file.Problem))
	}

	message := fmt.Sprintf(
		"%s could not be read, so this console has no clusters to offer. The start-up log names every kubeconfig file this process read, and /kt-healthz reports the same fact. A file fixed now is picked up when the console restarts.",
		strings.Join(paths, " and "))

	return api.Fail(api.StateUnreadable, api.CodeUndecodable, message).
		Within(api.Scope{Resource: "clusters"}).
		Detailed(strings.Join(details, "; "))
}

func (c apiClusters) List(ctx context.Context) ([]api.Cluster, *api.Failure) {
	s := c.s
	if s.clusters == nil {
		return nil, api.Absent("this build has no cluster manager")
	}
	contexts := s.clusters.Contexts()

	// **A kubeconfig that would not parse is not a kubeconfig with no clusters
	// in it.** `Manager.Contexts()` is a bare walk of the merged config, so a
	// file that failed to load contributes nothing and zero comes back looking
	// exactly like an operator who has never configured a cluster. This is the
	// console's front door, the one screen where being wrong costs most,
	// because it is the screen opened when nothing works.
	//
	// The fact that tells them apart is already on the Server: `Survey` reads
	// each file on its own and distinguishes `Absent`, which is ordinary and
	// which the merge skips as kubectl does, from `Problem`, which is a file
	// that is there and stopped reading.
	//
	// Checked only when there is nothing to show. A file that failed to parse
	// while four others read fine leaves a list an operator can still work from,
	// and this interface has no arm for a list carried with a reason - see the
	// note on `Clusters.List` in internal/api/backend.go, which asks for
	// `unreadable` whenever any file failed. Answering that literally would
	// replace four working clusters with an error page. Named rather than
	// silently narrowed.
	if len(contexts) == 0 {
		if fail := kubeconfigUnreadable(s.setup.Kubeconfig); fail != nil {
			return nil, fail
		}
	}

	layout := s.bars.Arrangement(contexts)

	// The probe is read from what is already in hand and never probed inline: a
	// list that waited for fourteen clusters would time out and report a failure
	// it invented itself.
	rows := map[string]health.Cluster{}
	if s.health != nil {
		for _, row := range s.health.Known().Rows {
			rows[row.Name] = row
		}
	}

	out := make([]api.Cluster, 0, len(contexts))
	for _, name := range contexts {
		short := shortName(name)
		plate := s.plates.Get(name)
		entry := api.Cluster{
			Name:  name,
			Label: plateLabel(name, plate),
			// The same derivation the tile uses, through the same two
			// functions. Carried rather than left to the client because the
			// tile, the naming form and the chooser all draw these, and a
			// second derivation written in TypeScript is how a rail and a
			// heading end up disagreeing about which cluster is on screen.
			Initials: plateInitials(short, plate),
			Colour:   plateColourKey(name, plate),
			Hidden:   layout.IsHidden(name),
			Bar:      layout.BarOf(name),
			// Per cluster, which is where this console's guardrail is. Never
			// nil — New substitutes a callback answering false — so this is the
			// configured fence and not a guess at one.
			ReadOnly: s.readOnly(name),
			Loopback: s.clusters.Loopback(name),
			AWS:      s.clusters.AWS(name),
		}
		if entry.Bar > 0 {
			// Only a tile that is on a hotbar has a position. See
			// api.Cluster.Order: absent is "no position", and `0` is the first
			// tile, which is why the field is a pointer.
			order := positionOf(layout, name)
			entry.Order = &order
		}
		row, probed := rows[name]
		stampVerdict(&entry, row, probed)
		out = append(out, entry)
	}
	return out, nil
}

// stampVerdict writes the probe's answer onto one entry: its state, the reason,
// who fixes it, and when it was established. One function for List and Probe,
// so the rail and the overview's Refresh cannot read one row two ways.
func stampVerdict(entry *api.Cluster, row health.Cluster, probed bool) {
	entry.State, entry.Reason = clusterState(entry.Name, row, probed)
	entry.Remedy = clusterRemedy(row, probed)
	if probed && row.State != "" && !row.CheckedAt.IsZero() {
		at := row.CheckedAt.UTC()
		entry.CheckedAt = &at
	}
}

// Probe asks one cluster again, now, and answers its entry as List draws it -
// the overview's Refresh.
//
// `Health.Check` with force: the cache is skipped, and so is the background
// backoff, because this is an operator who has just pressed a button. Only this
// cluster is asked; `/api/health?force=1` asks every one, which against a
// kubeconfig of fourteen was measured at ten seconds.
func (c apiClusters) Probe(ctx context.Context, name string) (api.Cluster, *api.Failure) {
	s := c.s
	if s.health == nil {
		return api.Cluster{}, api.Absent("this build has no health probe")
	}
	// A kubeconfig corrected a second ago is what the operator is retrying, so
	// it is read now rather than at the next tick of Manager.Watch. A file that
	// does not parse keeps the previous one, and the probe answers from it.
	if s.clusters != nil {
		if _, err := s.clusters.ReloadIfChanged(); err != nil {
			log.Printf("[kubetower] kubeconfig changed and could not be read, the previous one stays: %v", err)
		}
	}
	entries, fail := c.List(ctx)
	if fail != nil {
		return api.Cluster{}, fail
	}
	for _, entry := range entries {
		if entry.Name != name {
			continue
		}
		row := s.health.Check(ctx, name, true)
		stampVerdict(&entry, row, row.State != "")
		return entry, nil
	}
	// Unreachable through the API: the router answers 404 for a name this
	// console does not serve before this runs, so only a kubeconfig that
	// changed between that check and List lands here - and a name that is not
	// served is never dialled. `no_such_kind` is the word the client's own "not
	// configured for that context" notice already uses (Overview.tsx,
	// ClusterHome.tsx); the contract has no code for an unknown cluster, and
	// adding one is a contract change for the client's two notices and this
	// line together, not for this line alone.
	return api.Cluster{}, api.Fail(api.StateAbsent, api.CodeNoSuchKind,
		"this console is not configured for a cluster called "+name).Within(api.Scope{Cluster: name})
}

// verdictFresh is how long the probe's red verdict stands in for a read.
// Younger than this, an unreachable cluster's overview is answered from the
// verdict and nothing dials it; older, it is read, because a verdict from
// minutes ago is a reason to look again rather than an answer.
const verdictFresh = 30 * time.Second

// freshRefusal is the probe's own verdict on a cluster it found unreachable
// within verdictFresh, as the failure a read would have produced - or nil,
// and the caller reads. It never probes.
//
// Without it a cluster the probe had just found down would be dialled again by
// every overview read, up to ten seconds each, to learn what was already known.
func freshRefusal(s *Server, name string, now time.Time) *api.Failure {
	if s.health == nil {
		return nil
	}
	for _, row := range s.health.Known().Rows {
		if row.Name != name {
			continue
		}
		if row.CheckedAt.IsZero() || now.Sub(row.CheckedAt) >= verdictFresh {
			return nil
		}
		state, reason := clusterState(name, row, true)
		if state != api.StateUnreachable || reason == nil {
			return nil
		}
		return &api.Failure{State: state, Reason: *reason}
	}
	return nil
}

// plateColourKey is the tile's color as a palette **key**, for a JSON response
// rather than for a style attribute - and it is never empty.
//
// The chosen swatch where there is one, and otherwise the automatic color,
// which is a palette key too (`nameplates.AutoColour`): the contract declares
// `Cluster.colour` as the palette's enum and requires it, so a client draws
// every tile from one key-to-token map and has no "absent" arm to invent a
// color for. A color computed on the client would be a second derivation of
// the same fact, which is how a rail and a chooser come to disagree.
//
// A stored key that is not on the palette falls back to the automatic one
// rather than being sent. `Set` refuses one, so the only way to hold it is a
// hand-edited file, and sending it would hand the front end a value outside the
// enum it was promised.
func plateColourKey(context string, plate nameplates.Nameplate) string {
	if _, ok := nameplates.ColourOf(plate.Colour); ok {
		return plate.Colour
	}
	return nameplates.AutoColour(context)
}

// clusterRemedy is who fixes this cluster, where the classification produced an
// answer.
//
// The same two sentences the health page renders, from the same function, which
// is the point: the rail's tooltip and that page must not describe one cluster
// two ways. Nothing new is composed here.
//
// **Absent is three different things and all three are correct.** A cluster that
// answered has no failure to attribute; a cluster nobody has probed yet has not
// failed at all — and `whoFixes` would hand it the "not one of the known shapes"
// arm, which is a sentence about a failure that has not happened; and a state
// with no classification is left to say so through `reason` rather than through
// an invented remedy.
func clusterRemedy(row health.Cluster, probed bool) *api.Remedy {
	if !probed || row.State == "" || row.State == health.StateOK {
		return nil
	}
	who, what := whoFixes(row.State)
	if who == "" && what == "" {
		return nil
	}
	return &api.Remedy{Who: who, What: what, Actionable: row.Actionable(), Command: row.Command}
}

// clusterState maps the probe's vocabulary onto the contract's.
//
// **A cluster nobody has probed yet is `pending`**, which is the ordinary state
// of every cluster at start-up: the probe runs on its own loop and this list
// answers out of what is already known rather than waiting for fourteen clusters
// to time out.
//
// Not `unreadable`: that asserts the cluster is there and could not be read,
// when nothing was tried. `CodeNotProbedYet` is the code that goes with it, and
// it is deliberately not `CodeTimeout` — nothing timed out.
func clusterState(name string, row health.Cluster, probed bool) (api.State, *api.Reason) {
	if !probed || row.State == "" {
		r := api.Because(api.CodeNotProbedYet, "this cluster has not been probed yet").
			Retry(true).In(api.Scope{Cluster: name})
		return api.StatePending, &r
	}
	scope := api.Scope{Cluster: name}
	switch row.State {
	case health.StateOK:
		return api.StateOK, nil

	case health.StateMissing:
		// Nothing refused anything; there was no program to ask. Explicitly not
		// retryable: asking again runs the same missing executable, and the fix is
		// to install it or to start the console where it can be seen. A nil
		// `retryable` would leave a client to guess, and the guess a retry button
		// makes is "try again".
		r := api.Because(api.CodeNotInstalled, row.Detail).Retry(false).In(scope)
		return api.StateAbsent, &r

	case health.StateSSO:
		// **Retryable, and it is the one row on this page a person fixes
		// themselves** — in one command, in ten seconds. `read_denied` with
		// `retryable: false` would be the opposite advice.
		r := api.Because(api.CodeCredentialsExpired, row.Detail).Retry(true).In(scope)
		return api.StateUnreadable, &r

	case health.StateIAM:
		// The role cannot be assumed by this identity. It fails identically from
		// the host and nothing in this console can change it, so it is not
		// retryable and the remedy names somebody else.
		r := api.Because(api.CodeReadDenied, row.Detail).Retry(false).In(scope)
		return api.StateUnreadable, &r

	case health.StatePlugin:
		// The credential plugin ran and refused. Its own words are not in the
		// error, which is why this has a code of its own rather than sharing
		// `read_denied` with the IAM case: they send an operator to two different
		// places.
		r := api.Because(api.CodeCredentialsRefused, row.Detail).Retry(false).In(scope)
		return api.StateUnreadable, &r

	case health.StateDNS:
		// **An arm of its own**, so a name that does not resolve — a VPN that
		// is down — is told apart from a cluster that is genuinely unreachable.
		// `unreachable` is still the right *state*; the code is what says which
		// of the two, and the remedy names whoever runs the network rather than
		// the cluster's owner.
		r := api.Because(api.CodeNameNotResolved, row.Detail).Retry(true).In(scope)
		return api.StateUnreachable, &r

	case health.StateRefused:
		// Something answered the address and said no, which is a different fact
		// from nothing answering at all.
		r := api.Because(api.CodeConnectionRefused, row.Detail).Retry(true).In(scope)
		return api.StateUnreachable, &r

	case health.StateTimeout:
		r := api.Because(api.CodeTimeout, row.Detail).Retry(true).In(scope)
		return api.StateUnreachable, &r
	}

	// None of the known shapes. Said as such rather than asserted to be
	// unreachable: the error is carried in the message, and a client that reads
	// `unclassified` knows not to draw a conclusion from the code.
	r := api.Because(api.CodeUnclassified, row.Detail).Retry(true).In(scope)
	return api.StateUnreachable, &r
}

func positionOf(layout hotbars.Layout, name string) int {
	position := 0
	for _, bar := range layout.Bars {
		for _, context := range bar {
			if context == name {
				return position
			}
			position++
		}
	}
	return position
}

func (c apiClusters) Nav(ctx context.Context, name string) (api.NavMenu, *api.Failure) {
	top, groups, discovery := c.s.nav(ctx, name, "")
	menu := api.NavMenu{Groups: make([]api.NavGroup, 0, len(groups)+1), Discovery: discovery}
	// Overview first, then the ungrouped kinds and Helm. The group is never
	// empty: the dashboard is a page every cluster has.
	//
	// **Here and not in the browser**: a client-side insertion would make the
	// menu composed in two places, which is how the menu and the API come to
	// disagree about what the menu is. The href is the bare cluster route,
	// spelled the way every other ref in this group spells its cluster, and the
	// same route the active tile links to.
	items := make([]api.Ref, 0, len(top)+1)
	items = append(items, api.Ref{Text: overviewNavLabel, Href: overviewPageURL(name)})
	items = append(items, refsOf(top)...)
	menu.Groups = append(menu.Groups, api.NavGroup{Key: "cluster", Label: "Cluster", Items: items})
	for _, group := range groups {
		menu.Groups = append(menu.Groups, navGroupOf(group))
	}
	return menu, nil
}

// overviewNavLabel is the menu's word for the dashboard.
const overviewNavLabel = "Overview"

// overviewPageURL is the dashboard: the bare `/c/{cluster}`, through
// `clusterPath`, as `helmPageURL` and `kindNav` spell the segment.
func overviewPageURL(cluster string) string {
	return clusterPath(cluster)
}

// NavGroup fills the one group Nav defers.
//
// **The same `customNavBody` the menu is built with** (customresources.go),
// which is the property worth keeping and the reason that function exists at
// all: a heading that arrived with the menu and one that arrived a moment later
// must not be able to differ, because the difference is a bug nobody could
// describe.
//
// The three answers, and why one of them is not a note. A cluster that cannot be
// asked *at all* is not a fact about custom resources — it carries a code, a
// scope and whether asking again is worth it, none of which fits in a sentence
// under a heading — so it is the envelope's failure arm, classified by the one
// `stateOf` every other cluster read goes through. A cluster that answered and
// serves no CRDs, and a cluster whose CRD list was refused, both leave the
// heading in the menu with its reason on it, which is the bargain `getNav`
// already strikes: say why the rest is missing rather than be quietly short.
//
// The client acquisition is repeated here rather than read out of
// `customNavBody`'s note, because a sentence is not a state. `Manager.Client`
// caches per context, so the call `customNavBody` makes next is a map lookup.
func (c apiClusters) NavGroup(ctx context.Context, name, key string) (api.NavGroup, *api.Failure) {
	if key != navKey(customNavLabel) {
		// Said, not logged. The client asked for a group this menu does not fetch
		// separately, which is a fault in this product; `absent` is the arm that
		// says "there is no such thing" without blaming the cluster.
		return api.NavGroup{}, api.Absent("no menu group named " + key + " is fetched separately")
	}
	if _, err := c.s.clusters.Client(name); err != nil {
		return api.NavGroup{}, stateOf(err, api.Scope{Cluster: name, Verb: "list"})
	}

	// Its own deadline, customNavDeadline.
	ctx, cancel := context.WithTimeout(ctx, customNavDeadline)
	defer cancel()

	// `current` is empty because the API has no page: which entry is the one on
	// screen is decided in the browser from the location, against these hrefs.
	// See web/ui/src/shell/menuMemory.ts.
	return navGroupOf(c.s.customNavBody(ctx, name, "")), nil
}

// navGroupOf crosses one menu group into the contract.
//
// **`Sub` and `Deferred` are carried and not dropped, and that is this
// function's whole reason to exist.** The custom resource heading's links are
// all one level down in `Sub`, and it carries no links of its own until it is
// filled: mapped as `Items: refsOf(group.Links)` alone, it would cross as a
// group with zero items, a heading drawn onto nothing with no way to reach a
// custom resource.
func navGroupOf(group navGroup) api.NavGroup {
	out := api.NavGroup{
		Key:        navKey(group.Label),
		Label:      group.Label,
		Note:       group.Note,
		AccessNote: group.AccessNote,
		Items:      refsOf(group.Links),
		Deferred:   group.Deferred,
	}
	// One level down, which is where the CRDs live, grouped by API group. The
	// recursion is bounded by the data rather than by a depth counter: nothing
	// builds a `Sub` inside a `Sub`, and the contract says one level and no more.
	for _, sub := range group.Sub {
		out.Groups = append(out.Groups, navGroupOf(sub))
	}
	return out
}

// navKey is a group's stable identifier for the client.
//
// The label lowercased, with spaces hyphenated — because the key travels as a
// URL path segment in `getNavGroup` and is the identity the browser stores the
// person's arrangement under, and "Custom Resources" must not cross as `custom
// resources`, a key with a space in a path.
//
// Derived rather than declared, so a group cannot exist without one. The cost is
// that renaming a heading resets that heading's remembered open state, which is
// one disclosure opened again — against a table of keys that can disagree with
// the labels beside them.
func navKey(label string) string {
	return strings.ReplaceAll(strings.ToLower(label), " ", "-")
}

func refsOf(links []navLink) []api.Ref {
	out := make([]api.Ref, 0, len(links))
	for _, link := range links {
		out = append(out, api.Ref{Text: link.Label, Href: link.Href, Namespaced: link.Namespaced, Aliases: link.Aliases})
	}
	return out
}

// Extensions is every extension the build carries, each with its state on this
// cluster and whether the person asking may change it: Settings > Extra lists
// every one, with a button where `writable` says so and the reason where it
// does not.
func (c apiClusters) Extensions(ctx context.Context, name string) ([]api.Extension, *api.Failure) {
	if c.s.extras == nil {
		return nil, api.Absent("this build carries no extensions")
	}
	states := c.s.extras.State(name)
	locks := c.s.extensionLocks(ctx, name, states)
	out := make([]api.Extension, 0, len(states))
	for i, state := range states {
		settings := make([]api.ExtensionSetting, 0, len(state.Settings))
		for i, one := range state.Settings {
			setting := api.ExtensionSetting{
				Key: one.Key, Label: one.Label, Summary: one.Summary, Kind: "bool",
				Value: state.Values[i], Default: one.Default, Pinned: state.SettingsPinned[i],
			}
			if one.Text {
				setting.Kind, setting.Text, setting.Placeholder, setting.Env = "text", state.Texts[i], one.Placeholder, one.Env
				if len(one.Choices) > 0 {
					setting.Kind = "choice"
					for _, choice := range one.Choices {
						setting.Options = append(setting.Options, api.ExtensionOption{Value: choice.Value, Label: choice.Label})
					}
				}
				// Under the server profile the chart's values decide a line of
				// text: it can name a Secret the console reads with its own
				// credentials, which a person's `use` does not cover.
				setting.Pinned = setting.Pinned || c.s.extras.IsInCluster()
			}
			settings = append(settings, setting)
		}
		out = append(out, api.Extension{
			Key: state.Key, Label: state.Label, Summary: state.Summary, Needs: state.Needs,
			Enabled: state.On, Pinned: state.Pinned, Settings: settings,
			Writable: locks[i].locked == "", Locked: locks[i].locked, Reason: locks[i].reason,
		})
	}
	return out, nil
}

// ExtensionSuggestions asks each extension that is off on this cluster, and
// that nothing pins, whether the cluster runs what it reads
// (extension.Suggester). The api route is registered only where the switch is
// the console's, so what is offered here can be pressed.
//
// Through the extension's own Host and the person's request, as its routes are:
// what it lists to answer is reviewed for whoever asked. An answer that failed -
// a refusal, a cluster that did not answer - is left out and logged, never sent
// as a suggestion or as the list's failure: one extension unable to look is not
// a fact about the others.
//
// **Only what the person could turn on**: an extension that is off, that
// nothing pins, that the chart's values allow, and - under the server profile -
// on which the person holds `use`. The same answer the listing's `writable`
// gives, from the same function.
func (c apiClusters) ExtensionSuggestions(r *http.Request, name string) ([]api.ExtensionSuggestion, *api.Failure) {
	if c.s.extras == nil {
		return nil, api.Absent("this build carries no extensions")
	}
	states := c.s.extras.State(name)
	locks := c.s.extensionLocks(r.Context(), name, states)
	off := map[string]bool{}
	for i, state := range states {
		off[state.Key] = !state.On && locks[i].locked == ""
	}
	out := []api.ExtensionSuggestion{}
	for _, e := range registry.Extensions() {
		suggester, ok := e.(extension.Suggester)
		if !ok || !off[e.Key()] {
			continue
		}
		evidence, err := suggester.Suggest(c.s.forExtension(r), c.s.hostFor(e.Key()), name)
		if err != nil {
			log.Printf("[kubetower] extension %q could not say whether %q runs what it reads: %v",
				e.Key(), name, firstLine(err.Error()))
			continue
		}
		if evidence != "" {
			out = append(out, api.ExtensionSuggestion{Key: e.Key(), Label: e.Label(), Evidence: evidence})
		} else if named, ok := e.(extension.Component); ok && named.Component() != "" {
			// Looked and found nothing: the banner says nothing, the Settings
			// tile says "absent". An error above is unknown, never absent.
			out = append(out, api.ExtensionSuggestion{
				Key: e.Key(), Label: e.Label(), Evidence: named.Component() + " absent", Absent: true,
			})
		}
	}
	return out, nil
}

// SetExtension ticks one extra on one cluster.
//
// A key this build does not carry is a 404, which is what the contract
// declares: the route's pattern has already checked that the key is well
// formed, so it is not a malformed request - it names nothing here.
//
// **Under the server profile it is reviewed and recorded**: `use` on the
// extension, for the person ctx carries, at the one choke point - a denial is a
// 403 and the store is not touched - and the audit record says who turned which
// extension on or off on which cluster (useOrRefuse).
func (c apiClusters) SetExtension(ctx context.Context, name, key string, enabled bool) error {
	if c.s.extras == nil {
		return fmt.Errorf("%w: this build carries no extensions", api.ErrNotFound)
	}
	// The record says what it was too: a write that changes nothing reads as
	// one. Read under the lock the write is made under, or two switches at once
	// both read the same `was` and one record names a value its write did not
	// replace.
	c.s.extrasWrites.Lock()
	defer c.s.extrasWrites.Unlock()
	was := c.s.extras.EnabledOn(name, extras.Canonical(key))
	err := c.s.useOrRefuse(ctx, name, key, fmt.Sprintf("enabled=%t, was %t", enabled, was), func() error {
		return c.s.extras.SetOn(name, key, enabled)
	})
	return extensionWriteAnswer(err)
}

// extensionWriteAnswer is what SetExtension and SetExtensionSetting answer for
// the store's error, or the review's: a key or a setting this build does not
// carry is a 404, an extension the chart's values do not name a 403
// (not_available_in_this_profile), a `use` the review refused a 403 and one it
// could not make a 503.
func extensionWriteAnswer(err error) error {
	var refused *access.Refused
	switch {
	case err == nil:
		return nil
	case errors.Is(err, extras.ErrUnknown), errors.Is(err, extras.ErrUnknownSetting):
		return fmt.Errorf("%w: %s", api.ErrNotFound, firstLine(err.Error()))
	case errors.Is(err, extras.ErrNotInValues):
		return fmt.Errorf("%w: %s", api.ErrNotInProfile, firstLine(err.Error()))
	case errors.Is(err, access.ErrUnreviewable):
		return fmt.Errorf("%w: %s", api.ErrUnreviewable, firstLine(err.Error()))
	case errors.As(err, &refused), errors.Is(err, access.ErrDenied):
		return fmt.Errorf("%w: %s", api.ErrRefused, firstLine(err.Error()))
	case errors.Is(err, access.ErrUnrecorded):
		return fmt.Errorf("%w: %s", api.ErrUnrecorded, firstLine(err.Error()))
	}
	return storeAnswer(err)
}

// SetExtensionSetting writes one setting of one extra on one cluster. A key or
// a setting this build does not carry is a 404, as for SetExtension. Reviewed
// and recorded under the server profile as SetExtension is: whoever may turn an
// extension on may configure it.
func (c apiClusters) SetExtensionSetting(ctx context.Context, name, key, setting string, value bool) error {
	if c.s.extras == nil {
		return fmt.Errorf("%w: this build carries no extensions", api.ErrNotFound)
	}
	// Under the switch's lock, for the switch's reason.
	c.s.extrasWrites.Lock()
	defer c.s.extrasWrites.Unlock()
	was := c.s.extras.Setting(name, extras.Canonical(key), setting)
	err := c.s.useOrRefuse(ctx, name, key, fmt.Sprintf("setting %s=%t, was %t", setting, value, was), func() error {
		return c.s.extras.SetSetting(name, key, setting, value)
	})
	return extensionWriteAnswer(err)
}

// SetExtensionText writes one text setting of one extra on one cluster,
// answered as SetExtensionSetting is; a value the setting refuses, one its
// environment variable decides and a checkbox are a 400, and any write under
// the server profile a 403 (not_available_in_this_profile).
func (c apiClusters) SetExtensionText(name, key, setting, text string) error {
	if c.s.extras == nil {
		return fmt.Errorf("%w: this build carries no extensions", api.ErrNotFound)
	}
	if err := c.s.extras.SetText(name, key, setting, text); err != nil {
		if errors.Is(err, extras.ErrUnknown) || errors.Is(err, extras.ErrUnknownSetting) {
			return fmt.Errorf("%w: %s", api.ErrNotFound, firstLine(err.Error()))
		}
		if errors.Is(err, extras.ErrTextInCluster) {
			return fmt.Errorf("%w: %s", api.ErrNotInProfile, firstLine(err.Error()))
		}
		return storeAnswer(err)
	}
	return nil
}

// storeAnswer is what a write to one of the console's own stores - the rail,
// the columns, the nameplates, the extras - answers when the store returned an
// error: a refusal, or a change it applied and could not write down.
//
// The second is `statefile.ErrNotSaved`, and its contract is that the change
// **is in effect now** and will be gone at the next start. Wrapped as
// `api.ErrInvalid` it would read as a 400 - nothing happened, the request was
// wrong - for a change the console made, and a client that believes a 400
// stops: `RailArrange.tsx`'s swap is two writes, and the second would never be
// sent, leaving half a swap applied in memory. `api.ErrNotSaved` is its own
// answer, a 500 `not_saved`, and the sentence the store wrote - which already
// says "in effect now, but it will not survive a restart" and names the file -
// goes with it.
//
// Everything else a store returns is a refusal of the request itself, and
// stays a 400.
func storeAnswer(err error) error {
	if errors.Is(err, statefile.ErrNotSaved) {
		return fmt.Errorf("%w: %s", api.ErrNotSaved, firstLine(err.Error()))
	}
	return fmt.Errorf("%w: %s", api.ErrInvalid, firstLine(err.Error()))
}

// --- Settings ----------------------------------------------------------------

type apiSettings struct{ s *Server }

// apiUpdates is Check now, over SetupInfo.CheckUpdates.
type apiUpdates struct{ s *Server }

func (u apiUpdates) CheckUpdates(ctx context.Context) api.SettingsUpdate {
	return *settingsUpdate(u.s.setup.CheckUpdates(ctx))
}

// updateCheck is api.Deps.Updates: nil, **as an interface**, where nothing
// checks - a nil func wrapped in apiUpdates would be a non-nil interface, and
// the route would be registered over a call that panics.
func (s *Server) updateCheck() api.UpdateCheck {
	if s.setup.CheckUpdates == nil {
		return nil
	}
	return apiUpdates{s}
}

// apiInstaller is the desktop's download and install, over SetupInfo.Installer.
type apiInstaller struct{ s *Server }

func (i apiInstaller) UpdateInstall() api.UpdateInstall {
	return updateInstallOf(i.s, i.s.setup.Installer.Status())
}

func (i apiInstaller) SetInstallUpdates(on bool) (api.UpdateInstall, error) {
	status, err := i.s.setup.Installer.SetEnabled(on)
	out := updateInstallOf(i.s, status)
	if err != nil {
		return out, fmt.Errorf("%w: %s", api.ErrNotSaved, firstLine(err.Error()))
	}
	return out, nil
}

func (i apiInstaller) DownloadUpdate() (api.UpdateInstall, error) {
	status, err := i.s.setup.Installer.Download()
	if err != nil {
		return api.UpdateInstall{}, fmt.Errorf("%w: %s", api.ErrConflict, firstLine(err.Error()))
	}
	return updateInstallOf(i.s, status), nil
}

func (i apiInstaller) InstallUpdate(ctx context.Context) (api.UpdateInstall, error) {
	status, err := i.s.setup.Installer.Install(ctx)
	if err != nil {
		return api.UpdateInstall{}, fmt.Errorf("%w: %s", api.ErrConflict, firstLine(err.Error()))
	}
	return updateInstallOf(i.s, status), nil
}

func (i apiInstaller) OpenUpdateFolder() (api.UpdateInstall, error) {
	status, err := i.s.setup.Installer.OpenFolder()
	if err != nil {
		return updateInstallOf(i.s, status), fmt.Errorf("%w: %s", api.ErrConflict, firstLine(err.Error()))
	}
	return updateInstallOf(i.s, status), nil
}

// toolWindows is SetupInfo.ToolWindows, nil under the server profile and
// where there are no forwards: a window shows a forward of this process.
func (s *Server) toolWindows() api.ToolWindows {
	if s.setup.ToolWindows == nil || s.inCluster || !s.offersForwards() {
		return nil
	}
	return s.setup.ToolWindows
}

// updateInstaller is api.Deps.Installer: nil, as an interface, where nothing
// installs, so the routes are not registered.
func (s *Server) updateInstaller() api.UpdateInstaller {
	if s.setup.Installer == nil {
		return nil
	}
	return apiInstaller{s}
}

// Settings answers with what this console actually holds.
//
// The three settings the General tab writes — `archiveEnabled`, `captureLogs`,
// `retentionDays` — are read back out of the archive, along with
// `retentionChoices`, the `archive` envelope and `info`. `apisettings.go` is
// the mapping.
func (a apiSettings) Settings() api.Settings {
	return settingsOf(a.s)
}

// SaveGeneral writes the three settings the General tab has.
//
// **`applied` and not an error on a write that could not be persisted.** Every
// store here applies in memory first and then writes; a value that validated is
// in effect whether or not the file could be written down. Reporting that as a
// failure makes an operator retype a setting that is already working — what they
// need is the narrower true thing, which is that it will not survive a restart.
func (a apiSettings) SaveGeneral(write api.SettingsWrite) (api.Settings, error) {
	if a.s.archive == nil {
		return api.Settings{}, fmt.Errorf(
			"%w: this console's general settings are the archive's — kept events, captured "+
				"logs and a retention in days — and this build has no archive",
			api.ErrNotInProfile)
	}

	applied, err := saveGeneral(a.s.archive, write)
	out := settingsOf(a.s)
	// The response carries the settings **as they now stand**, whatever happened
	// to the file: the store has already applied them.
	out.Applied = &api.SettingsApplied{Persisted: err == nil, Note: appliedNote(write, applied)}
	if err != nil {
		out.Applied.Note = "This is in effect now and will not survive a restart: " +
			firstLine(err.Error())
	}
	return out, nil
}

func (a apiSettings) SaveNodeShell(want api.NodeShellSettings) (api.NodeShellSettings, error) {
	if a.s.claude == nil {
		return api.NodeShellSettings{}, fmt.Errorf("%w: this build has no node shell", api.ErrNotInProfile)
	}
	if err := a.s.claude.SetNodeShellImage(want.Image); err != nil {
		// The image validated or it did not; a value that validated is in effect
		// whether or not it could be written down, which is why this reports the
		// sentence rather than swallowing it.
		return api.NodeShellSettings{}, fmt.Errorf("%w: %s", api.ErrInvalid, firstLine(err.Error()))
	}
	// **`persisted` is a fact about the file and not about the setting.** The
	// image is in effect now; `SetNodeShellImage` returns an error only where the
	// value itself was refused, which is the branch above, so reaching here means
	// both applied and written.
	return api.NodeShellSettings{
		Enabled:      true,
		Available:    true,
		Image:        a.s.claude.NodeShellImage(),
		DefaultImage: nodeshell.DefaultImage,
		Persisted:    boolOf(true),
	}, nil
}

func (a apiSettings) PreviewClear(scope string) (api.PlanFacts, error) {
	if scope == "archive" {
		return a.previewClearArchive()
	}
	targets := []api.PlanTarget{}
	switch scope {
	case "preferences", "all":
		targets = append(targets,
			api.PlanTarget{Kind: "preference", Name: "rail", Note: "hotbars, tiles and nameplates"},
			api.PlanTarget{Kind: "preference", Name: "columns", Note: "widths, order and visibility"},
		)
	}
	if scope == "clusterState" || scope == "all" {
		targets = append(targets, api.PlanTarget{Kind: "preference", Name: "extras",
			Note: "which additive pages are on, per cluster"})
	}
	return api.PlanFacts{
		Verb:    "discard",
		Targets: targets,
		Count:   len(targets),
		// **How many, and of what** — composed from the targets this call just
		// resolved rather than from the scope it was asked for, so a scope that
		// grows a fourth preference says four without anything here changing.
		// The noun is `api.KindWord` on the targets' own kind, which is where
		// the sentence drawn above this button gets it, so the two agree by
		// construction and not by two people writing the same word.
		Button:       fmt.Sprintf("Discard %d %s", len(targets), api.KindWord("preference", len(targets))),
		Destructive:  true,
		Irreversible: boolTrue(),
		Confirmation: &api.Confirmation{Kind: "acknowledge"},
	}, nil
}

// previewClearArchive is what emptying the archive would actually destroy.
//
// # The summary is read, never composed
//
// Every figure here comes from one walk of the disk - `RefreshUsage` - and the
// gesture is checked against the same reading. A modal that asks for a number
// it worked out separately from the number it checks is a confirmation nobody
// can satisfy, on a page whose whole subject is deleting things.
//
// # The gesture is the day count, and it is not an acknowledgement
//
// The confirmation is typing how many days are in the archive. It cannot be
// answered by muscle memory, because the answer is different on every console
// and on every day - which is exactly what a confirmation on an irreversible
// act is for. The other scopes here ask for an acknowledgement because losing a
// column width is not the same class of act.
//
// # One target, and it is the archive
//
// Not one per day. `count` is documented as *the number of targets the server
// resolved*, and ninety of them would put ninety rows in a dialog to say one
// thing. How many days there are is in the note and in the gesture, where a
// person reads it.
func (a apiSettings) previewClearArchive() (api.PlanFacts, error) {
	// The state directory first, and not only because internal/launch opens no
	// archive there: the reason is a missing volume, not a missing feature, and
	// it is the sentence the settings read already gives (`fillEphemeralArchive`).
	if a.s.setup.StateEphemeral {
		return api.PlanFacts{}, fmt.Errorf("%w: %s", api.ErrNotInProfile, ephemeralArchive)
	}
	if a.s.archive == nil {
		return api.PlanFacts{}, fmt.Errorf("%w: this build has no archive", api.ErrNotInProfile)
	}
	usage := a.s.archive.RefreshUsage()
	days := len(usage.Days)

	facts := api.PlanFacts{
		Verb: "empty",
		Targets: []api.PlanTarget{{
			Kind: "archive", Name: "this console's archive",
			Note: fmt.Sprintf("%s across %s and %s",
				humanBytes(usage.Bytes), plural(days, "day", "days"), plural(usage.Files, "file", "files")),
		}},
		Count: 1,
		// One target and it is the archive, so the phrase names it rather than
		// counting it — counting here would say "1 archive", which is a number
		// nobody needed. The figures that matter are the days and the bytes,
		// and they are in the note and in the gesture where a person reads
		// them.
		//
		// The phrase matters most here: falling through to "Apply" would hide
		// that the act destroys the only copy of everything the archive holds.
		Button:       "Empty this archive",
		Destructive:  true,
		Irreversible: boolTrue(),
		Confirmation: &api.Confirmation{
			Kind:     "count",
			Expected: strconv.Itoa(days),
			Label: fmt.Sprintf(
				"Type %d - the number of days this archive holds - to empty it.", days),
		},
	}

	// **What is lost that nothing else has.** The events are gone from the
	// cluster within the hour; this is the only copy, and a warning that says so
	// is the difference between an operator agreeing to tidy up and an operator
	// agreeing to that.
	facts.Warnings = append(facts.Warnings,
		"These events and log lines are gone from the cluster. This archive is the only copy.")
	if usage.Note != "" {
		// The walk could not finish, so the figures above are partial - and a
		// summary that does not admit it is the lie this contract exists to
		// refuse.
		facts.Warnings = append(facts.Warnings, "The figures are partial: "+usage.Note)
	}
	return facts, nil
}

func (a apiSettings) Clear(scope string) (api.ActOutcome, error) {
	if scope != "archive" {
		// Not a file deletion here: clearing preferences is not wired to this
		// contract, and a second implementation over the same stores would be
		// two ways to discard the same state, where the one that forgets a
		// store is the one nobody runs. Reported as not done rather than done
		// badly.
		return api.ActOutcome{}, fmt.Errorf(
			"%w: clearing preferences is not wired to this contract yet; the console's own "+
				"Settings page still does it", api.ErrNotInProfile)
	}
	// previewClearArchive's order and its sentence, so the act refuses exactly
	// what its preview refused, for the same reason.
	if a.s.setup.StateEphemeral {
		return api.ActOutcome{}, fmt.Errorf("%w: %s", api.ErrNotInProfile, ephemeralArchive)
	}
	if a.s.archive == nil {
		return api.ActOutcome{}, fmt.Errorf("%w: this build has no archive", api.ErrNotInProfile)
	}

	target := api.PlanTarget{Kind: "archive", Name: "this console's archive"}
	result, err := a.s.archive.Clear()
	if err != nil {
		// **A failure is an outcome, not an error.** `ActOutcome` is per target
		// and says so: one refused removal reported as a 500 loses the sentence
		// that says which part of the disk would not go.
		return api.ActOutcome{
			Failed: 1,
			Results: []api.ActResult{{
				Target: target, OK: false,
				// `unclassified` and not a guess: the failure came off the file
				// system, and this layer knows it went wrong rather than what
				// class of thing it was.
				Reason: reasonPtr(api.Because(api.CodeUnclassified,
					"the archive could not be emptied: "+firstLine(err.Error()))),
			}},
		}, nil
	}

	// `Skipped` is the archive saying nothing happened and why - there is no
	// directory at all, on a console configured with none. It succeeded, and the
	// sentence is the whole answer.
	message := "Emptied - " + humanBytes(result.Bytes) + " freed, " + plural(len(result.Days), "day", "days") + " removed."
	if result.Skipped != "" {
		message = "Nothing to empty: " + result.Skipped + "."
	}
	return api.ActOutcome{
		Succeeded: 1,
		Results:   []api.ActResult{{Target: target, OK: true, Message: message}},
	}, nil
}

// Health is the console's own diagnosis, one envelope per check.
//
// **A plain read answers out of what is already known; `force` runs a round.**
// Probing every context before answering costs one cluster timeout — ten
// seconds against fourteen contexts — during which the page shows nothing,
// which reads as the control doing nothing.
//
// `force` is the *Check again* gesture, attached to a control that can show it
// is working, and it is the answer to the one question this page is opened with
// after an `aws sso login`. An operator who has just signed in, presses the
// button, and is served the answer from before they ran it concludes the sign-in
// failed.
//
// The counts, the notices and the per-check facts are `apihealth.go`.
func (a apiSettings) Health(ctx context.Context, force bool) (api.HealthReport, *api.Failure) {
	s := a.s
	if s.health == nil {
		return api.HealthReport{}, api.Absent("this build has no health probe")
	}

	var (
		rows     []health.Cluster
		unprobed int
		total    int
	)
	if force {
		rows = s.health.All(ctx, true)
		total = len(rows)
	} else {
		snapshot := s.health.Known()
		rows, unprobed, total = snapshot.Rows, snapshot.Unprobed, snapshot.Total
	}
	return healthReport(s, s.health, rows, unprobed, total, force), nil
}

// --- Preferences -------------------------------------------------------------

type apiPrefs struct{ s *Server }

func (p apiPrefs) contexts() []string {
	if p.s.clusters == nil {
		return nil
	}
	return p.s.clusters.Contexts()
}

// Preferences reads the three stores the rail and the grids are kept in.
//
// **The bars are sent as bars**, one entry per hotbar in bar order, because
// that is what the store holds and what the switcher draws. A flat list of
// slots with a tile carrying a group could not express an empty hotbar at all:
// a bar with nothing on it is still a bar, and its digit is still drawn.
func (p apiPrefs) Preferences() api.Preferences {
	s := p.s
	out := api.Preferences{}

	contexts := p.contexts()
	layout := s.bars.Arrangement(contexts)
	out.Rail.BarCount = hotbars.Count
	out.Rail.ActiveBar = layout.Active
	out.Rail.Bars = make([][]string, 0, len(layout.Bars))
	for _, bar := range layout.Bars {
		// Copied rather than aliased, and never left nil: the store's slice is its
		// own, and a nil bar would travel as `null` where the schema says array.
		out.Rail.Bars = append(out.Rail.Bars, append([]string{}, bar...))
	}
	out.Rail.Hidden = append([]string{}, layout.Hidden...)
	if plates := s.plates.All(); len(plates) > 0 {
		out.Rail.Nameplates = make(map[string]api.RailNameplate, len(plates))
		for name, plate := range plates {
			out.Rail.Nameplates[name] = api.RailNameplate{
				Name: plate.Name, Initials: plate.Initials, Colour: plate.Colour, Icon: plate.Icon,
			}
		}
	}

	// Density is one setting for every table, which is where the store keeps it.
	out.Columns.Density = s.columns.Density()
	out.Columns.Kinds = map[string]api.KindColumns{}
	for _, kind := range s.columns.Kinds() {
		arrangement := s.columns.Arrangement(kind, titlesOf(kind))
		if arrangement.Sized() {
			out.Columns.WidthsAreDeliberate = true
		}
		cells := make([]api.ColumnCell, 0, len(arrangement.Columns))
		for order, column := range arrangement.Columns {
			cell := api.ColumnCell{Column: column.Title, Order: order}
			if column.Width > 0 {
				width := column.Width
				cell.Width = &width
			}
			if column.Decided {
				visible := !column.Hidden
				cell.Visible = &visible
			}
			cells = append(cells, cell)
		}
		out.Columns.Kinds[kind] = api.KindColumns{Cells: cells}
	}
	return out
}

// titlesOf is the column list a kind declares, which every store call needs in
// order to reconcile what is stored against what the table now has.
func titlesOf(kind string) []string {
	declared := declaredColumns(kind)
	titles := make([]string, 0, len(declared))
	for _, column := range declared {
		titles = append(titles, column.Title)
	}
	return titles
}

// SetRailBar puts one hotbar on screen.
//
// The write is `{"bar": <n>}`: it **selects which hotbar is drawn**, out of
// several, which a boolean could not say.
//
// The bound is the store's: Show refuses a bar that does not exist and names how
// many there are, which is the same number `preferences.rail.barCount` reports.
func (p apiPrefs) SetRailBar(bar int) error {
	if _, err := p.s.bars.Show(bar, p.contexts()); err != nil {
		return storeAnswer(err)
	}
	return nil
}

func (p apiPrefs) SetRailAssign(name string, bar int) error {
	if _, err := p.s.bars.Assign(name, bar, p.contexts()); err != nil {
		return storeAnswer(err)
	}
	return nil
}

// SetRailMove converts an absolute position into the walk the store performs.
//
// A faithful translation rather than a reinterpretation: `order` is a position in
// the rail and the store moves a tile by a delta, so the delta is the difference
// between where the tile is and where it was asked to be.
func (p apiPrefs) SetRailMove(name string, order int) error {
	contexts := p.contexts()
	layout := p.s.bars.Arrangement(contexts)
	delta := order - positionOf(layout, name)
	if delta == 0 {
		return nil
	}
	if _, err := p.s.bars.Move(name, delta, contexts); err != nil {
		return storeAnswer(err)
	}
	return nil
}

// SetRailStep is the other arm of the same route, and the direct one: the store
// already moves a tile by a delta, so the arrows' up and down reach it without
// anybody having to agree about positions first.
func (p apiPrefs) SetRailStep(name string, delta int) error {
	if _, err := p.s.bars.Move(name, delta, p.contexts()); err != nil {
		return storeAnswer(err)
	}
	return nil
}

// SetRailPlace drops one tile beside another.
//
// **The rail has no groups**: the write names the tile to drop beside, and this
// is the store call it names. A `group` read as a target would be a field whose
// name says one thing and whose value means another.
func (p apiPrefs) SetRailPlace(name, target string, after bool) error {
	if _, err := p.s.bars.Place(name, target, after, p.contexts()); err != nil {
		return storeAnswer(err)
	}
	return nil
}

func (p apiPrefs) SetRailHidden(name string, hidden bool) error {
	contexts := p.contexts()
	var err error
	if hidden {
		_, err = p.s.bars.Hide(name, contexts)
	} else {
		_, err = p.s.bars.Reveal(name, contexts)
	}
	if err != nil {
		return storeAnswer(err)
	}
	return nil
}

// SetRailNameplate applies a partial write to the three things a nameplate is.
//
// A nil member is not said and is left alone; a member that is present and
// empty returns that part to the form derived from the context name — which is
// why the write arrives here whole rather than as three strings, since three
// strings cannot tell the two apart.
//
// `reset` is applied first and the named members on top of it, so a body that
// carries both says what it looks like it says: clear the lot, then set this.
func (p apiPrefs) SetRailNameplate(body api.RailNameplateWrite) error {
	plate := p.s.plates.Get(body.Cluster)
	if body.Reset != nil && *body.Reset {
		plate = nameplates.Nameplate{}
	}
	if body.Label != nil {
		plate.Name = *body.Label
	}
	if body.Initials != nil {
		plate.Initials = *body.Initials
	}
	if body.Colour != nil {
		plate.Colour = *body.Colour
	}
	if body.Icon != nil {
		plate.Icon = *body.Icon
	}
	if _, err := p.s.plates.Set(body.Cluster, plate, p.contexts()); err != nil {
		return storeAnswer(err)
	}
	return nil
}

// columnTitle is the seam between the two names a column has, and it is the
// only place the two meet.
//
// The list payload names a column by a **key** — `columnKey(title, index)`,
// `cpu-7` — because a title is a label and may be reworded; the column menu
// and the resize grip post that key back. `internal/columns` files widths,
// order and hidden flags by **title**, because that is what the store on disk
// holds and re-keying stored state buys nothing. Handing the key straight to
// the store would fail both ways: `SetVisible` accepts any non-empty string, so
// unticking a column would store `hidden["cpu-7"]` next to titles like `CPU`,
// which the read side never pairs with a column, and `Resize` refuses an
// unknown title, so a dragged width would be a 400. The two `ByItsKey` tests
// read this seam back.
//
// A key the kind does not declare is a refusal, not a silent no-op or a stored
// entry: it is a stale page or a bug, and both deserve a sentence.
func columnTitle(kind, key string) (string, error) {
	for i, column := range declaredColumns(kind) {
		if columnKey(column.Title, i) == key {
			return column.Title, nil
		}
	}
	return "", fmt.Errorf("%w: %q is not a column of %s", api.ErrInvalid, key, kind)
}

func (p apiPrefs) SetColumnWidth(kind, column string, width *int) error {
	title, err := columnTitle(kind, column)
	if err != nil {
		return err
	}
	size := columns.Column{Title: title}
	if width != nil {
		size.Width = *width
	}
	if _, err := p.s.columns.Resize(kind, []columns.Column{size}, titlesOf(kind)); err != nil {
		return storeAnswer(err)
	}
	return nil
}

// SetColumnOrder converts a position into the "beside which column" the store
// takes.
//
// The same faithful translation as SetRailMove: a position is an index into the
// list of titles, so the column currently at that index is the one to sit beside,
// and which side depends on which way the column is traveling.
func (p apiPrefs) SetColumnOrder(kind, column string, order int) error {
	column, err := columnTitle(kind, column)
	if err != nil {
		return err
	}
	titles := titlesOf(kind)
	arrangement := p.s.columns.Arrangement(kind, titles)
	current := -1
	ordered := make([]string, 0, len(arrangement.Columns))
	for i, c := range arrangement.Columns {
		ordered = append(ordered, c.Title)
		if c.Title == column {
			current = i
		}
	}
	if current < 0 {
		return fmt.Errorf("%w: %q is not a column of %s", api.ErrInvalid, column, kind)
	}
	if order < 0 || order >= len(ordered) || order == current {
		return nil
	}
	if _, err := p.s.columns.Place(kind, column, ordered[order], order > current, titles); err != nil {
		return storeAnswer(err)
	}
	return nil
}

// SetColumnPlace is the other arm of the same route, and the direct one: the
// store's operation is already "beside this column", so nothing is translated
// and nothing depends on the list the browser last drew.
func (p apiPrefs) SetColumnPlace(kind, column, target string, after bool) error {
	column, err := columnTitle(kind, column)
	if err != nil {
		return err
	}
	target, err = columnTitle(kind, target)
	if err != nil {
		return err
	}
	if _, err := p.s.columns.Place(kind, column, target, after, titlesOf(kind)); err != nil {
		return storeAnswer(err)
	}
	return nil
}

func (p apiPrefs) SetColumnVisible(kind, column string, visible bool) error {
	title, err := columnTitle(kind, column)
	if err != nil {
		return err
	}
	defaultHidden := false
	for _, declared := range declaredColumns(kind) {
		if declared.Title == title {
			defaultHidden = declared.Secondary
		}
	}
	if _, err := p.s.columns.SetVisible(kind, title, visible, defaultHidden, titlesOf(kind)); err != nil {
		return storeAnswer(err)
	}
	return nil
}

// SetColumnDensity writes a preference that is console-wide.
//
// The body carries no `kind`: row height is one setting for every table,
// deliberately, because a grid that is comfortable on pods and compact on
// services is two grids.
func (p apiPrefs) SetColumnDensity(density string) error {
	if _, err := p.s.columns.SetDensity(density); err != nil {
		return storeAnswer(err)
	}
	return nil
}

// ResetColumns forgets one scope of one kind's arrangement.
//
// The scope arrives as a string because `internal/api` is the contract and
// deliberately knows nothing about the store — the same shape SetColumnDensity
// is on. It is converted rather than trusted: `columns.Scope.Valid` refuses
// anything but the four, so a vocabulary that drifted between the handler and
// the store is a refusal here and not a silent "forget everything".
func (p apiPrefs) ResetColumns(kind, scope string) error {
	if _, err := p.s.columns.Reset(kind, columns.Scope(scope), titlesOf(kind)); err != nil {
		return storeAnswer(err)
	}
	return nil
}

func boolTrue() *bool { t := true; return &t }

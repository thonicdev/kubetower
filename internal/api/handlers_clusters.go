package api

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/thonicdev/kubetower/internal/identity"
)

// --- Clusters, navigation, extensions ---------------------------------------

func (a *API) listClusters(w http.ResponseWriter, r *http.Request) {
	if a.deps.Clusters == nil {
		writeOK(w, ListFailed[Cluster](StateAbsent,
			Because(CodeNotInstalled, "this build has no cluster manager")))
		return
	}
	items, f := a.deps.Clusters.List(r.Context())
	writeOK(w, listOf(items, f))
}

func (a *API) getNav(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	if a.deps.Clusters == nil {
		writeOK(w, ValueFailed[NavMenu](StateAbsent,
			Because(CodeNotInstalled, "this build has no cluster manager")))
		return
	}
	menu, f := a.deps.Clusters.Nav(r.Context(), name)
	writeOK(w, valueOf(menu, f))
}

// getNavGroup fills a group getNav marked `deferred`.
//
// The key is validated for shape here and for existence in the backend, because
// only the backend knows which groups this menu has. An unusable string is a
// 400; a well-formed one naming no deferred group is `absent` in the envelope,
// which is a client fault said out loud rather than a 404 that reads as "this
// cluster has no menu".
func (a *API) getNavGroup(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	group := r.PathValue("group")
	if group == "" || len(group) > 253 {
		badRequest(w, "a menu group key is required")
		return
	}
	if a.deps.Clusters == nil {
		writeOK(w, ValueFailed[NavGroup](StateAbsent,
			Because(CodeNotInstalled, "this build has no cluster manager")))
		return
	}
	filled, f := a.deps.Clusters.NavGroup(r.Context(), name, group)
	writeOK(w, valueOf(filled, f))
}

// getNavOccupancy answers which of the menu's kinds hold an object the person
// may see. Its own operation so getNav stays cheap: this
// one lists.
func (a *API) getNavOccupancy(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	if a.deps.Clusters == nil {
		writeOK(w, ValueFailed[NavOccupancy](StateAbsent,
			Because(CodeNotInstalled, "this build has no cluster manager")))
		return
	}
	occupancy, f := a.deps.Clusters.NavOccupancy(r.Context(), name)
	if f == nil && occupancy.Kinds == nil {
		// `kinds` is required: an empty object, never null.
		occupancy.Kinds = map[string]KindOccupancy{}
	}
	writeOK(w, valueOf(occupancy, f))
}

// probeCluster re-probes one cluster and answers its entry - the overview's
// Refresh on a cluster the probe found red.
//
// The answer is the same shape as one item of listClusters, so the page and the
// rail read one verdict. A probe that ran and found the cluster down is `ok`
// with that entry: the request succeeded, and the cluster's own state says what
// it found.
func (a *API) probeCluster(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	if a.deps.Clusters == nil {
		writeOK(w, ValueFailed[Cluster](StateAbsent,
			Because(CodeNotInstalled, "this build has no cluster manager")))
		return
	}
	entry, f := a.deps.Clusters.Probe(r.Context(), name)
	writeOK(w, valueOf(entry, f))
}

func (a *API) listExtensions(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	if a.deps.Clusters == nil {
		writeOK(w, ListFailed[Extension](StateAbsent,
			Because(CodeNotInstalled, "this build carries no extensions")))
		return
	}
	items, f := a.deps.Clusters.Extensions(r.Context(), name)
	writeOK(w, listOf(items, f))
}

// listExtensionSuggestions answers the banner's question: which extensions that
// are off here does this cluster give a reason to turn on.
func (a *API) listExtensionSuggestions(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	if a.deps.Clusters == nil {
		writeOK(w, ListFailed[ExtensionSuggestion](StateAbsent,
			Because(CodeNotInstalled, "this build carries no extensions")))
		return
	}
	items, f := a.deps.Clusters.ExtensionSuggestions(r, name)
	writeOK(w, listOf(items, f))
}

func (a *API) setExtensionEnabled(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	if !extensionPattern.MatchString(key) {
		badRequest(w, "an extension key is lower-case and hyphenated")
		return
	}
	var body ExtensionEnabledWrite
	if !decode(w, r, maxBody, &body) {
		return
	}
	if !requireAll(w, "enabled", body.Enabled) {
		return
	}
	if a.deps.Clusters == nil {
		missing(w, "a cluster manager")
		return
	}
	if err := a.deps.Clusters.SetExtension(r.Context(), name, key, *body.Enabled); err != nil {
		if errors.Is(err, ErrNotSaved) {
			// It took effect, so who made it is owed the same line as a write
			// that was also kept; only the file is missing.
			consoleWrite(r, "extension %q on %q set enabled=%t, not saved", key, name, *body.Enabled)
		}
		fail(w, err)
		return
	}
	consoleWrite(r, "extension %q on %q set enabled=%t", key, name, *body.Enabled)
	writeNoContent(w)
}

func (a *API) setExtensionSetting(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	key, setting := r.PathValue("key"), r.PathValue("setting")
	if !extensionPattern.MatchString(key) || !extensionPattern.MatchString(setting) {
		badRequest(w, "an extension key and a setting key are lower-case and hyphenated")
		return
	}
	var body ExtensionSettingWrite
	if !decode(w, r, maxBody, &body) {
		return
	}
	if (body.Value == nil) == (body.Text == nil) {
		badRequest(w, "send exactly one of value (a checkbox) and text (a line of text)")
		return
	}
	if a.deps.Clusters == nil {
		missing(w, "a cluster manager")
		return
	}
	if body.Text != nil {
		// The value is not logged: it is an extension's configuration, and a
		// line of text may be anything an operator typed.
		if err := a.deps.Clusters.SetExtensionText(name, key, setting, *body.Text); err != nil {
			if errors.Is(err, ErrNotSaved) {
				consoleWrite(r, "extension %q on %q set text %s, not saved", key, name, setting)
			}
			fail(w, err)
			return
		}
		consoleWrite(r, "extension %q on %q set text %s", key, name, setting)
		writeNoContent(w)
		return
	}
	if err := a.deps.Clusters.SetExtensionSetting(r.Context(), name, key, setting, *body.Value); err != nil {
		if errors.Is(err, ErrNotSaved) {
			consoleWrite(r, "extension %q on %q set %s=%t, not saved", key, name, setting, *body.Value)
		}
		fail(w, err)
		return
	}
	consoleWrite(r, "extension %q on %q set %s=%t", key, name, setting, *body.Value)
	writeNoContent(w)
}

// consoleWrite is the line a write to the console's own state leaves: who, and
// what. These writes change what every person of the console sees. On the
// desktop the name is the one user's. Under the server profile the extension
// writes also leave an audit record, written where they are reviewed
// (access.Gate.Administer).
func consoleWrite(r *http.Request, format string, args ...any) {
	log.Printf("[kubetower] %s", identity.Line(r.Context(), "console write: "+format, args...))
}

// --- Settings ---------------------------------------------------------------

func (a *API) getSettings(w http.ResponseWriter, r *http.Request) {
	if a.deps.Settings == nil {
		missing(w, "a settings store")
		return
	}
	writeOK(w, a.deps.Settings.Settings())
}

// checkUpdates is Check now: the update check past its cache, answered once it
// has settled. The route exists only where a.deps.Updates is set.
func (a *API) checkUpdates(w http.ResponseWriter, r *http.Request) {
	writeOK(w, a.deps.Updates.CheckUpdates(r.Context()))
}

// getUpdateInstall is the download's progress, read while it runs.
func (a *API) getUpdateInstall(w http.ResponseWriter, _ *http.Request) {
	writeOK(w, a.deps.Installer.UpdateInstall())
}

// saveUpdateInstall writes the "Install updates automatically" setting.
func (a *API) saveUpdateInstall(w http.ResponseWriter, r *http.Request) {
	var body UpdateInstallWrite
	if !decode(w, r, maxBody, &body) {
		return
	}
	if body.Enabled == nil {
		badRequest(w, "enabled is required")
		return
	}
	when := ""
	if body.When != nil {
		when = *body.When
		if when != "quit" && when != "now" {
			badRequest(w, `when must be "quit" or "now"`)
			return
		}
	}
	out, err := a.deps.Installer.SetInstallUpdates(*body.Enabled, when)
	if err != nil {
		fail(w, err)
		return
	}
	writeOK(w, out)
}

// downloadUpdate is the first click: it starts the download and answers at once.
func (a *API) downloadUpdate(w http.ResponseWriter, _ *http.Request) {
	out, err := a.deps.Installer.DownloadUpdate()
	if err != nil {
		fail(w, err)
		return
	}
	writeOK(w, out)
}

// openUpdateFolder shows the verified file where the installation's folder is
// not writable.
func (a *API) openUpdateFolder(w http.ResponseWriter, _ *http.Request) {
	out, err := a.deps.Installer.OpenUpdateFolder()
	if err != nil {
		fail(w, err)
		return
	}
	writeOK(w, out)
}

// installUpdate is the second click: the installer runs and the console restarts.
func (a *API) installUpdate(w http.ResponseWriter, r *http.Request) {
	out, err := a.deps.Installer.InstallUpdate(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeOK(w, out)
}

func (a *API) saveSettings(w http.ResponseWriter, r *http.Request) {
	var body SettingsWrite
	if !decode(w, r, maxBody, &body) {
		return
	}
	if a.deps.Settings == nil {
		missing(w, "a settings store")
		return
	}
	out, err := a.deps.Settings.SaveGeneral(body)
	if err != nil {
		fail(w, err)
		return
	}
	consoleWrite(r, "the archive's general settings were saved")
	writeOK(w, out)
}

func (a *API) saveNodeShellSettings(w http.ResponseWriter, r *http.Request) {
	var body NodeShellSettings
	if !decode(w, r, maxBody, &body) {
		return
	}
	if a.deps.Settings == nil {
		missing(w, "a settings store")
		return
	}
	out, err := a.deps.Settings.SaveNodeShell(body)
	if err != nil {
		fail(w, err)
		return
	}
	writeOK(w, out)
}

// clearState is the one destructive act with no cluster behind it, and it takes
// a plan token like every other. What is being agreed to is the loss of
// preferences rather than of objects, and that sentence is still not the front
// end's to invent unilaterally.
func (a *API) clearState(w http.ResponseWriter, r *http.Request) {
	var body ClearRequest
	if !decode(w, r, maxBody, &body) {
		return
	}
	switch body.Scope {
	case "preferences", "clusterState", "archive", "all":
	default:
		badRequest(w, "scope is one of preferences, clusterState, archive, all")
		return
	}
	if a.deps.Settings == nil {
		missing(w, "a settings store")
		return
	}

	// Preview defaults to true. A body that forgot to say must not clear
	// anything: the safe reading of silence is the one that changes nothing.
	preview := true
	if body.Preview != nil {
		preview = *body.Preview
	}
	if preview {
		facts, err := a.deps.Settings.PreviewClear(body.Scope)
		if err != nil {
			fail(w, err)
			return
		}
		out, err := a.planFrom(r.Context(), &plan{kind: planClear, facts: facts, clear: body.Scope})
		if err != nil {
			planStoreFailure(w, err)
			return
		}
		writeOK(w, out)
		return
	}

	// The gesture is checked before the token is spent, as every act's is
	// (spendConfirmed): a wrong number of days is typed
	// again in the same dialog rather than costing the plan.
	agreed, ok := a.spendConfirmed(w, r, body.Token, planClear, body.Confirmed, false)
	if !ok {
		return
	}
	fresh, err := a.deps.Settings.PreviewClear(agreed.clear)
	if err != nil {
		fail(w, err)
		return
	}
	if !sameFacts(agreed.facts, fresh) {
		a.reissue(w, r, &plan{kind: planClear, facts: fresh, clear: agreed.clear},
			"what would be discarded has changed since the plan was made")
		return
	}
	// **The gesture** is checked in spendConfirmed above, before the token is
	// taken, so a front end that never drew the box is not obeyed: a scope may
	// ask the person to type how many days of archive they are about to delete.
	outcome, err := a.deps.Settings.Clear(agreed.clear)
	if err != nil {
		fail(w, err)
		return
	}
	consoleWrite(r, "cleared %s (succeeded=%d failed=%d)", agreed.clear, outcome.Succeeded, outcome.Failed)
	writeOK(w, outcome)
}

// --- Health -----------------------------------------------------------------

// getHealth is the console's own diagnosis.
//
// The page exists precisely to say which of the five states is true, so every
// check carries its own and the page is never a single verdict. A check
// answering `absent` — the thing that would serve it is not installed — is a
// different sentence from one answering `unreachable`, and the whole value of
// the page is that it refuses to merge them.
func (a *API) getHealth(w http.ResponseWriter, r *http.Request) {
	if a.deps.Settings == nil {
		writeOK(w, ValueFailed[HealthReport](StateAbsent,
			Because(CodeNotInstalled, "this build has no health probe")))
		return
	}
	// `?force=1` is the *Check again* gesture. Anything else is a read of the
	// last round, including a malformed value: this parameter cannot fail a
	// request, because the failure mode of refusing it is a health page that will
	// not open, which is the page an operator reaches for when nothing else does.
	report, f := a.deps.Settings.Health(r.Context(), booleanQuery(r, "force"))
	writeOK(w, valueOf(report, f))
}

// reissue answers a 409 with a fresh plan, so the front end can show the
// operator what changed rather than simply failing.
func (a *API) reissue(w http.ResponseWriter, r *http.Request, p *plan, why string) {
	fresh, err := a.planFrom(r.Context(), p)
	if err != nil {
		planStoreFailure(w, err)
		return
	}
	planStale(w, ProblemPlanChanged, why, &fresh)
}

// booleanQuery reads a flag from the query string, and **never fails a request
// over one**.
//
// `strconv.ParseBool` accepts the spellings a client library might send — "1",
// "t", "true", "TRUE" — and a bare `?force` with no value is the HTML form of
// "yes", as `?force=1` would be typed by hand.
// Anything else is false.
//
// Refusing a malformed value would be correct on most endpoints and is wrong on
// this one: the failure mode is a health page that will not open, and that is
// the page an operator reaches for when nothing else does.
func booleanQuery(r *http.Request, name string) bool {
	if !r.URL.Query().Has(name) {
		return false
	}
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return true
	}
	value, err := strconv.ParseBool(raw)
	return err == nil && value
}

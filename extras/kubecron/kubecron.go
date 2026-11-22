// Package kubecron is KubeCron, scheduled-job health: KubeTower's page that
// answers whether a cluster's CronJobs are running when they should, and what
// their past runs did, as an extension of the console. Whole: verdicts and
// history together, and the verdict stays on this page rather than in the
// console's CronJobs list. Behind its own per-cluster switch.
//
// # What is here
//
// Read-only routes: the cluster's CronJobs with their verdicts, one CronJob's
// calendar and runs, one run's record, log and samples (what a React page
// needs); the
// CronJob and run behind a Job or a Pod, for the object panel's tab
// (owner.go); and every CronJob's calendar with one day's runs across them,
// for the page's heatmap (heatmap.go).
//
// # Who sees what
//
// Every route reads the CronJobs through `Host.List`, which is reviewed for the
// person behind the request. A CronJob that list does not return is not shown,
// and nor is anything the collector holds about it: the detail and run routes
// answer 404 for a CronJob the person may not list, exactly as for one that
// does not exist.
package kubecron

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
)

// Key is `kubecron`. It is the page's URL segment, the `KT_EXTRAS` name and
// the key the per-cluster switch is stored under, so it is not free to change.
const Key = "kubecron"

// legacyKey is the page's earlier name, from when it was called "Scheduled
// jobs". Kept in Legacy so a switch stored under it still reads on.
const legacyKey = "scheduled"

var (
	cronJobsGVR = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}
	jobsGVR     = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
)

// KubeCron is the extension `cmd/kubetower` registers.
type KubeCron struct {
	// now is the clock, a field so a test can stand at a chosen instant.
	now func() time.Time
	// client is what reaches the collector through the port-forward.
	client *http.Client
	// maxCronJobs and budget bound the heatmap's fan-out (heatmap.go): fields
	// so a test can make them small.
	maxCronJobs int
	budget      time.Duration
	// collector is what the environment says the collector and its token are
	// (collector.go), read once at start-up. It wins over the cluster's
	// settings, which are read on every request.
	collector collectorConfig
}

// New is the constructor `cmd/kubetower` calls. It reads KT_KUBECRON_COLLECTOR,
// KT_KUBECRON_TOKEN_SECRET and KT_KUBECRON_TOKEN_KEY; none is needed.
func New() KubeCron {
	return KubeCron{now: time.Now, client: &http.Client{Timeout: collectorTimeout},
		maxCronJobs: HeatmapMaxCronJobs, budget: HeatmapBudget, collector: readConfig(os.Getenv)}
}

func (KubeCron) Key() string   { return Key }
func (KubeCron) Label() string { return "KubeCron" }

func (KubeCron) Summary() string {
	return "Whether each CronJob runs when it should, and what its past runs did: outcomes, durations, logs and resource use."
}

// Needs is drawn beside the switch. The verdicts need nothing; the history needs
// the collector, and the page says how to install it when it is not there.
func (KubeCron) Needs() string {
	return "a KubeCron collector in the cluster for run history (github.com/schmitech-fr/kubecron, mode=server)"
}

func (KubeCron) Legacy() []string { return []string{legacyKey} }

// Settings is where the collector is, per cluster, edited behind the Edit button
// of this extra's tile (extension.Configurable). Empty is
// the default: the one Service labeled as a collector, and the Secret
// kubecron-api in its namespace. The environment wins over both, and the tile
// says so (Env). Read on every request through Host.Setting, so a change
// applies with no restart.
func (KubeCron) Settings() []extension.Setting {
	return []extension.Setting{{
		Key:         SettingCollector,
		Label:       "Collector",
		Summary:     "The collector's Service, as namespace/service. Empty: the one Service labeled " + collectorLabel + ".",
		Text:        true,
		Placeholder: "the Service labeled " + collectorLabel,
		Env:         CollectorEnv,
		Check:       checkCollector,
	}, {
		Key:         SettingTokenSecret,
		Label:       "Token Secret",
		Summary:     "The Secret holding the collector's token (key " + TokenKey + "), always read in the collector's own namespace.",
		Text:        true,
		Placeholder: DefaultTokenSecret,
		Env:         TokenSecretEnv,
		Check:       checkSecretName,
	}}
}

// Configurable is asserted here, so a signature that drifts from the contract
// fails this package's build.
var _ extension.Configurable = KubeCron{}

// Routes are the reads. All are GETs: nothing here changes a cluster.
func (k KubeCron) Routes(h extension.Host) []extension.Route {
	return []extension.Route{
		{Method: http.MethodGet, Pattern: "/{$}", Handler: k.list(h)},
		{Method: http.MethodGet, Pattern: "/cronjobs/{namespace}/{name}", Handler: k.detail(h)},
		{Method: http.MethodGet, Pattern: "/runs/{id}", Handler: k.run(h)},
		// The object panel's tab on a Job or a Pod: its CronJob and runs.
		{Method: http.MethodGet, Pattern: "/jobs/{namespace}/{name}", Handler: k.owner(h, "jobs")},
		{Method: http.MethodGet, Pattern: "/pods/{namespace}/{name}", Handler: k.owner(h, "pods")},
		// The page's heatmap: every CronJob's calendar, and one day's runs.
		{Method: http.MethodGet, Pattern: "/daily", Handler: k.heatmap(h)},
		{Method: http.MethodGet, Pattern: "/days/{day}", Handler: k.day(h)},
		// The diagnosis the Settings tile draws.
		{Method: http.MethodGet, Pattern: "/status", Handler: k.status(h)},
	}
}

// --- the collector, per request ----------------------------------------------

// collectorState is the collector's standing for one request, in the four
// words the page branches on.
type collectorState struct {
	// State is "absent" (none in the cluster: the ordinary case, and the page's
	// install instructions), "unusable" (found and not readable),
	// "misconfigured" (KubeTower's own settings for it cannot be used), or "ok".
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
	// Via is how the collector was reached, "configuration" (the environment
	// or the cluster's setting) or "label", and Source says it in a sentence;
	// empty when none was.
	Via    string `json:"via,omitempty"`
	Source string `json:"source,omitempty"`
	// Configured is the Service the environment or the setting names, when
	// one does.
	Configured string `json:"configured,omitempty"`
	// Candidates is every labeled Service, when there are several and none
	// was chosen.
	Candidates []string `json:"candidates,omitempty"`
	// Checks is each step, in order: the collector, its Secret, reaching it,
	// its token.
	Checks []check `json:"checks,omitempty"`
	// The rest is set when State is "ok".
	Where          string     `json:"where,omitempty"`
	ObservedSince  *time.Time `json:"observedSince,omitempty"`
	MetricsEnabled bool       `json:"metricsEnabled,omitempty"`
	RunDays        int        `json:"runDays,omitempty"`
	LogDays        int        `json:"logDays,omitempty"`
}

// openSession finds the collector, opens a port-forward to it and agrees the
// contract. stop closes the forward and is never nil.
//
// **The tunnel is the console's** (Host.ForwardAsConsole): a person needs no
// port-forward into the collector's namespace. So every caller asks the
// person's own reviewed reads first and serves out of the collector only what
// they allow: the CronJobs Host.List returned to them (judgeVisible, the
// heatmap's list, the run's pre-check), and a stored log only after
// Host.MayReadLogs.
func (k KubeCron) openSession(r *http.Request, h extension.Host, cluster string) (*Collector, collectorState, func()) {
	noop := func() {}
	f := k.locate(r, h, cluster)
	state, ref := f.collectorState, f.Ref
	if ref == nil {
		return nil, state, noop
	}
	fail := func(step, check, detail string) (*Collector, collectorState, func()) {
		state.State, state.Detail = "unusable", detail
		state.Checks = append(state.Checks, collectorCheck(step, check, detail))
		return nil, state, noop
	}
	tunnel, err := h.ForwardAsConsole(r, cluster, ref.Namespace, ref.Service, ref.Port)
	if err != nil {
		return fail(stepReach, checkFailed, "a port-forward to the collector at "+ref.Where()+" could not be opened: "+shortError(err))
	}
	addr, stop := tunnel.Addr, tunnel.Stop
	c, avail, err := openCollector(r.Context(), forwardGetter(k.client, addr, *ref), *ref)
	if !avail.OK {
		stop()
		var status *statusError
		switch {
		case errors.As(err, &status) && status.refused():
			state.Checks = append(state.Checks, collectorCheck(stepReach, checkOK, "the collector at "+ref.Where()+" answers"))
			return fail(stepToken, checkRefused, avail.Detail)
		case errors.As(err, &status):
			// It answered, with a status this build cannot use: reached.
			state.Checks = append(state.Checks, collectorCheck(stepReach, checkOK, "the collector at "+ref.Where()+" answers"))
			state.State, state.Detail = "unusable", avail.Detail
			return nil, state, noop
		case err != nil:
			return fail(stepReach, checkFailed, avail.Detail)
		}
		// It answered, in a way this build cannot read: reached, not usable.
		state.Checks = append(state.Checks, collectorCheck(stepReach, checkOK, "the collector at "+ref.Where()+" answers"))
		state.State, state.Detail = "unusable", avail.Detail
		return nil, state, noop
	}
	state.Checks = append(state.Checks, collectorCheck(stepReach, checkOK, "the collector at "+ref.Where()+" answers, contract "+supportedAPIVersion))
	if ref.Token != "" {
		state.Checks = append(state.Checks, collectorCheck(stepToken, checkOK, "the collector accepts the token"))
	} else {
		state.Checks = append(state.Checks, collectorCheck(stepToken, checkSkipped, "no token is sent, and the collector asks for none"))
	}
	state.State, state.Detail, state.Where = "ok", avail.Detail, c.Where()
	state.MetricsEnabled, state.RunDays, state.LogDays, state.ObservedSince = c.MetricsEnabled, c.RunDays, c.LogDays, timePtr(c.ObservedSince)
	return c, state, stop
}

// locate finds the collector and its token without reaching it: no tunnel is
// opened. Its state is "ok" when one was found that can be asked.
func (k KubeCron) locate(r *http.Request, h extension.Host, cluster string) found {
	w := resolveShared(k.collector)
	if !extension.SharedConsole(r) {
		w = resolve(k.collector, h.Setting(r, cluster, SettingCollector), h.Setting(r, cluster, SettingTokenSecret))
	}
	return discover(r.Context(), hostReads{r: r, h: h, cluster: cluster}, w)
}

// Configured is what this extra reads with the console's credentials on
// cluster (extension.Configured): the collector's Service and its token's
// Secret when the environment or the cluster's setting names the collector,
// and nothing when it is found by its label (collector.go, Whose rights read
// what). From the configuration alone, as the contract asks.
func (k KubeCron) Configured(_ string, setting func(string) string) extension.Configuration {
	return configuredObjects(resolve(k.collector, setting(SettingCollector), setting(SettingTokenSecret)))
}

var _ extension.Configured = KubeCron{}

// nothingToAsk is the history of a page with no CronJob to show: the collector
// was found, or not, and never asked.
const nothingToAsk = "There is no CronJob here this console may show, so the collector was not asked."

// collectorCheck is a check with no value of its own.
func collectorCheck(step, state, detail string) check {
	return check{Step: step, State: state, Detail: detail}
}

// --- GET /status : the diagnosis ---------------------------------------------

// statusAnswer is what the Settings tile draws: the collector's standing on
// this cluster, step by step, with where each value came from.
type statusAnswer struct {
	Collector collectorState `json:"collector"`
}

// status finds and opens the collector, as every page read does, and closes it
// at once: the Settings tile's diagnosis. It reads no CronJob from the
// collector, so it shows nothing a person could not see on the page itself.
// The collector's tunnel is the console's (openSession), so a person who may
// list no CronJob here is answered before it is opened, as for a run.
func (k KubeCron) status(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		if page, err := h.List(r, cluster, extension.ListQuery{GVR: cronJobsGVR}); err != nil {
			writeListFailure(w, cluster, err)
			return
		} else if len(page.Items) == 0 {
			writeProblem(w, http.StatusNotFound, CodeCronJobNotFound, "There is no CronJob here this console may show, so its collector is not asked.", nil)
			return
		}
		_, state, stop := k.openSession(r, h, cluster)
		stop()
		writeJSON(w, http.StatusOK, statusAnswer{Collector: state})
	})
}

// --- GET / : the cluster's CronJobs ------------------------------------------

type listAnswer struct {
	CollectedAt time.Time      `json:"collectedAt"`
	Complete    bool           `json:"complete"`
	Note        string         `json:"note,omitempty"`
	Retained    availability   `json:"retained"`
	History     availability   `json:"history"`
	Collector   collectorState `json:"collector"`
	Counts      counts         `json:"counts"`
	CronJobs    []cronJobWire  `json:"cronJobs"`
}

type availability struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type counts struct {
	Total      int `json:"total"`
	Missed     int `json:"missed"`
	Unknown    int `json:"unknown"`
	Suspended  int `json:"suspended"`
	Active     int `json:"active"`
	Failing    int `json:"failing"`
	Namespaces int `json:"namespaces"`
}

func (k KubeCron) list(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		now := k.now()

		report, ok := k.judgeVisible(w, r, h, cluster, "", now)
		if !ok {
			return
		}

		var state collectorState
		if len(report.CronJobs) == 0 {
			// Nothing to ask the collector about, so no tunnel is opened:
			// where it is, and whether it can be asked, is still said.
			state = k.locate(r, h, cluster).collectorState
			report.WithHistory(nil, Availability{Detail: nothingToAsk})
		} else {
			c, opened, stop := k.openSession(r, h, cluster)
			defer stop()
			state = opened
			if c != nil {
				outcomes, avail := c.History(r.Context())
				report.WithHistory(outcomes, avail)
				report.WithFailing(r.Context(), c)
			} else {
				report.WithHistory(nil, Availability{Detail: state.Detail})
			}
		}

		out := listAnswer{
			CollectedAt: report.CollectedAt, Complete: report.Complete, Note: report.Note,
			Retained: availability(report.Retained), History: availability(report.History), Collector: state,
			Counts: counts{Total: report.Total, Missed: report.Missed, Unknown: report.Unknown, Suspended: report.Suspended,
				Active: report.Active, Failing: report.Failing, Namespaces: report.Namespaces},
			CronJobs: make([]cronJobWire, 0, len(report.CronJobs)),
		}
		for _, cj := range report.CronJobs {
			out.CronJobs = append(out.CronJobs, wireCronJob(cj, now))
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// judgeVisible reads the CronJobs and Jobs the person may list and judges them.
// field narrows the CronJobs to one name; namespace narrows both.
func (k KubeCron) judgeVisible(w http.ResponseWriter, r *http.Request, h extension.Host, cluster, namespace string, now time.Time, field ...string) (Report, bool) {
	q := extension.ListQuery{GVR: cronJobsGVR, Namespace: namespace}
	if len(field) == 1 {
		q.FieldSelector = "metadata.name=" + field[0]
	}
	page, err := h.List(r, cluster, q)
	if err != nil {
		writeListFailure(w, cluster, err)
		return Report{}, false
	}
	jobs, jobsErr := h.List(r, cluster, extension.ListQuery{GVR: jobsGVR, Namespace: namespace})
	var retained *listed
	if jobsErr == nil {
		retained = &listed{Items: jobs.Items, Complete: jobs.Complete, Note: jobs.Note}
	}
	return judgeAll(listed{Items: page.Items, Complete: page.Complete, Note: page.Note}, retained, jobsErr, now), true
}

// --- GET /cronjobs/{namespace}/{name} : one CronJob's history ----------------

type detailAnswer struct {
	CronJob   cronJobWire    `json:"cronJob"`
	Collector collectorState `json:"collector"`
	Daily     *dailyWire     `json:"daily,omitempty"`
	DailyNote string         `json:"dailyNote,omitempty"`
	Runs      *runPageWire   `json:"runs,omitempty"`
	RunsNote  string         `json:"runsNote,omitempty"`
}

func (k KubeCron) detail(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, namespace, name := r.PathValue("cluster"), r.PathValue("namespace"), r.PathValue("name")
		now := k.now()

		report, ok := k.judgeVisible(w, r, h, cluster, namespace, now, name)
		if !ok {
			return
		}
		cj, found := findCronJob(report, namespace, name)
		if !found {
			writeProblem(w, http.StatusNotFound, CodeCronJobNotFound,
				"There is no CronJob "+namespace+"/"+name+" on this cluster that this console may read.", nil)
			return
		}

		out := detailAnswer{}
		c, state, stop := k.openSession(r, h, cluster)
		defer stop()
		out.Collector = state
		if c != nil {
			outcomes, avail := c.History(r.Context())
			if o, ok := outcomes[namespace+"/"+name]; ok && avail.OK {
				cj.History = &o
			}
			if daily, avail := c.Daily(r.Context(), namespace, name, heatmapDays(c)); avail.OK {
				out.Daily = wireDaily(daily)
			} else {
				out.DailyNote = avail.Detail
			}
			before := r.URL.Query().Get("before")
			day, dayOK := parseDay(r.URL.Query().Get("day"))
			if dayOK && before == "" {
				// A day of the calendar: the collector's v1 API has no
				// day filter, and its cursor is a start time, so the page
				// starts at the next midnight and keeps that day's runs.
				before = day.AddDate(0, 0, 1).Format(time.RFC3339Nano)
			}
			if page, avail := c.Runs(r.Context(), namespace, name, DefaultRunPage, before); avail.OK {
				if dayOK {
					page = onDay(page, day)
				}
				out.Runs = wireRunPage(page)
				// The first page is the newest runs, which is what Failing is
				// about; an older page says nothing about now.
				if r.URL.Query().Get("before") == "" && !dayOK && cj.History != nil {
					cj.History.Failing = FailingNow(page)
				}
			} else {
				out.RunsNote = avail.Detail
			}
			// A day or an older page says nothing about now: Failing is read
			// from the newest runs all the same, so the head does not lose it
			// when a day is picked.
			if (r.URL.Query().Get("before") != "" || dayOK) && cj.History != nil && !cj.History.LastFailure.IsZero() {
				if newest, avail := c.Runs(r.Context(), namespace, name, FailingRuns*2, ""); avail.OK {
					cj.History.Failing = FailingNow(newest)
				}
			}
		}
		out.CronJob = wireCronJob(cj, now)
		writeJSON(w, http.StatusOK, out)
	})
}

func findCronJob(report Report, namespace, name string) (CronJob, bool) {
	for _, cj := range report.CronJobs {
		if cj.Namespace == namespace && cj.Name == name {
			return cj, true
		}
	}
	return CronJob{}, false
}

// --- GET /runs/{id} : one run ------------------------------------------------

type runAnswer struct {
	Run       runWire        `json:"run"`
	Namespace string         `json:"namespace"`
	CronJob   string         `json:"cronJob"`
	Collector collectorState `json:"collector"`
	Log       *logWire       `json:"log,omitempty"`
	// LogState says why Log is absent when the person was not asked for it:
	// "refused" when the cluster says they may not `get pods/log` in the run's
	// namespace, "unreviewed" when that could not be asked. Empty when the
	// collector was asked, and LogNote is then its own answer.
	LogState    string       `json:"logState,omitempty"`
	LogNote     string       `json:"logNote,omitempty"`
	Samples     *samplesWire `json:"samples,omitempty"`
	SamplesNote string       `json:"samplesNote,omitempty"`
	// PodExists says whether the run's pod is still in the cluster, read for
	// the person: the page then offers the live log. Absent when that
	// could not be asked, so neither answer is drawn.
	PodExists *bool `json:"podExists,omitempty"`
	// LivePod is the pod that still exists, when one does: the run may name its
	// Job rather than its pod, and the live log is a pod's.
	LivePod string `json:"livePod,omitempty"`
	// Job is the run's Job while it is still in the cluster, for the person:
	// the page links it to the console's own Job page. Empty when it is
	// gone or could not be asked, and then nothing is linked.
	Job string `json:"job,omitempty"`
}

func (k KubeCron) run(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, id := r.PathValue("cluster"), r.PathValue("id")

		// Only the collector knows which CronJob a run belongs to, so the
		// person's review of that CronJob comes after the collector is asked.
		// The collector's tunnel is the console's (openSession), so a person
		// who may list no CronJob here at all is answered before it is opened:
		// what they are refused never makes the console dig one.
		// One row answers it: whether there is any, not which.
		if page, err := h.List(r, cluster, extension.ListQuery{GVR: cronJobsGVR, Limit: 1}); err != nil {
			writeListFailure(w, cluster, err)
			return
		} else if len(page.Items) == 0 {
			writeProblem(w, http.StatusNotFound, CodeRunNotFound, "This run is not one this console may show.", nil)
			return
		}

		c, state, stop := k.openSession(r, h, cluster)
		defer stop()
		if c == nil {
			writeProblem(w, http.StatusServiceUnavailable, CodeNoCollector, state.Detail, nil)
			return
		}
		rec, namespace, name, found, avail := c.Run(r.Context(), id)
		switch {
		case !avail.OK:
			writeProblem(w, http.StatusBadGateway, CodeCollectorFailed, avail.Detail, nil)
			return
		case !found:
			writeProblem(w, http.StatusNotFound, CodeRunNotFound, avail.Detail, nil)
			return
		}

		// The run names its CronJob; the person must be able to list it, or the
		// run is answered as one that does not exist.
		report, ok := k.judgeVisible(w, r, h, cluster, namespace, k.now(), name)
		if !ok {
			return
		}
		if _, visible := findCronJob(report, namespace, name); !visible {
			writeProblem(w, http.StatusNotFound, CodeRunNotFound, "This run is not one this console may show.", nil)
			return
		}

		out := runAnswer{Run: wireRunRecord(rec), Namespace: namespace, CronJob: name, Collector: state}
		out.PodExists, out.LivePod = livePod(r, h, cluster, namespace, rec.Pod)
		out.Job = jobOf(r, h, cluster, namespace, rec.Pod)
		// The stored log is a pod's log, so it is the person's only if
		// Kubernetes would hand them the pod's: `get pods/log` in the run's
		// namespace, asked before the collector is. The
		// run itself stays theirs - they may list its CronJob - so a refusal
		// withholds the log and says why, rather than hiding the run.
		if refused, state := logRefusal(h.MayReadLogs(r, cluster, namespace), namespace); refused != "" {
			out.LogNote, out.LogState = refused, state
		} else if log, avail := c.Log(r.Context(), id, MaxLogLines); avail.OK {
			out.Log = wireLog(log)
		} else {
			out.LogNote = avail.Detail
		}
		if series, avail := c.Samples(r.Context(), id); avail.OK {
			out.Samples = wireSamples(series)
		} else {
			out.SamplesNote = avail.Detail
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// logRefusal is MayReadLogs's answer as the run says it: the sentence and the
// state when the log is withheld, two empty strings when it may be read.
//
// Anything that is not an answer about the person - an unknown cluster, a
// namespace the collector named that is not one - withholds the log too: a
// review that did not happen is not one that allowed.
func logRefusal(err error, namespace string) (string, string) {
	switch {
	case err == nil:
		return "", ""
	case errors.Is(err, extension.ErrLogsDenied):
		return "You may not read this run's log: the cluster says you may not get pods/log in namespace " +
			namespace + ", which Kubernetes asks before it shows anyone a pod's log.", "refused"
	}
	return "Whether you may read this run's log could not be asked, so it is not shown: " + shortError(err), "unreviewed"
}

// --- failures ----------------------------------------------------------------

// problem is the failure body: Code is what the page branches on, Message what
// it prints. Codes are protocol, named in web/src/data.ts too.
type problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

const (
	CodeUnknownCluster  = "unknown_cluster"
	CodeListFailed      = "list_failed"
	CodeCronJobNotFound = "cronjob_not_found"
	CodeNoCollector     = "no_collector"
	CodeCollectorFailed = "collector_failed"
	CodeRunNotFound     = "run_not_found"
	// A read refused for the person: 403, and 503 when it could not be asked.
	CodeListForbidden  = extension.CodeListForbidden
	CodeListUnreviewed = extension.CodeListUnreviewed
)

func writeListFailure(w http.ResponseWriter, cluster string, err error) {
	if errors.Is(err, extension.ErrUnknownCluster) {
		writeProblem(w, http.StatusNotFound, CodeUnknownCluster, extension.UnknownClusterText(cluster), err)
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	// A refusal for the person is an answer about them, never a 502: 403 when
	// the cluster says they may not, 503 when that could not be asked.
	if refused, ok := extension.RefusedRead(err,
		"The cluster says you may not read what this page needs here, so it shows nothing of it."); ok {
		writeProblem(w, refused.Status, refused.Code, refused.Message, err)
		return
	}
	writeProblem(w, http.StatusBadGateway, CodeListFailed,
		"The CronJobs could not be read, so whether anything is late on this cluster is unknown.", err)
}

func writeProblem(w http.ResponseWriter, status int, code, message string, err error) {
	body := problem{Code: code, Message: message}
	if err != nil {
		body.Detail = shortError(err)
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

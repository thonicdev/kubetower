// Package report answers one question about a cluster: is anything wrong with
// what Argo CD is being told to run.
//
// It reads Argo CD's own custom resources (`applications`, `applicationsets`,
// `appprojects`) and nothing else: no Argo CD API client and no second
// credential, since Argo CD publishes its read side into the cluster.
//
// Rules the page relies on:
//
//   - Four answers are kept apart: the CRDs are not served; they are served and
//     no Application was read; Applications were read; or they could not be read
//     at all. The last is usually RBAC and must never read as "nothing to
//     report".
//   - The vocabulary is Argo CD's (`Synced`, `OutOfSync`, `Healthy`, ...),
//     never renamed or re-bucketed. A word this code has not met keeps its
//     spelling and the neutral color.
//   - "Not reported" (no status written yet) is not Argo CD's `Unknown` (assessed
//     and could not name).
//   - The offenders order is the page's central judgment and lives in one
//     table, worstFirst; the headline derives from it.
//   - The namespace is discovered, never assumed (install.go).
//   - Every bound that bites is printed on the page.
//   - Read-only: nothing here writes, syncs or refreshes, since a sync button
//     would be a second write path beside the console's guarded one.
package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/textutil"
)

// The three kinds this page reads, by slug, always qualified with the API
// group; the catalog resolves each into its group and resource.
const (
	ApplicationKind    = "applications.argoproj.io"
	ApplicationSetKind = "applicationsets.argoproj.io"
	AppProjectKind     = "appprojects.argoproj.io"
)

// The bounds. Each is a number the page prints when it bites.
const (
	// listPage is a page size and objectCeiling a total bound for paging a
	// list. Nothing in this package reads them: the caller's Host.List pages,
	// and its note reaches the page as Kind.Note.
	listPage      = 300
	objectCeiling = 5000

	// offenderLimit is how many Application rows the table draws.
	offenderLimit = 20
	// conditionLimit is how many conditions are drawn on one row.
	conditionLimit = 3
	// causeLimit is how many condition types get a row in the causes panel,
	// and causeSubjectLimit how many Applications are named under one.
	causeLimit        = 10
	causeSubjectLimit = 6
	// failingSetLimit and projectRowLimit bound the two smaller panels.
	failingSetLimit = 12
	projectRowLimit = 15
	// messageLimit is how much of Argo CD's sentence reaches a table cell: a
	// `ComparisonError` can be kilobytes of rendered YAML.
	messageLimit = 200
)

// State is which of the four answers this cluster gives.
type State int

const (
	// StateAbsent: no version of Argo CD's CRDs is served here.
	StateAbsent State = iota
	// StatePending: the CRDs are served and no Application was read: a fresh
	// install, or Applications in namespaces this identity cannot list.
	StatePending
	// StatePresent: Applications were read.
	StatePresent
	// StateUnreadable: the CRDs are served and the Applications could not be
	// read. Kept apart from StatePending so a missing `list` grant never reads
	// as "nothing deployed yet".
	StateUnreadable
)

// InstallCommand is what to type on a cluster that has no Argo CD. The chart
// version is not pinned, so the command does not go stale; the namespace is
// the chart's default for a new install (an existing one is discovered).
const InstallCommand = `helm repo add argo https://argoproj.github.io/argo-helm
helm install argocd argo/argo-cd \
  --namespace argocd --create-namespace`

// SyncStates and HealthStates are Argo CD's own words, in the order the band
// draws them. Listed so the band keeps its zeroes ("OutOfSync 0") whatever the
// cluster contains; a word not listed is appended, not folded into Unknown
// (see tally).
var (
	SyncStates   = []string{"Synced", "OutOfSync", "Unknown"}
	HealthStates = []string{"Healthy", "Progressing", "Degraded", "Suspended", "Missing", "Unknown"}
)

// Unreported is what an Application whose status Argo CD has not written yet
// is counted under. Not "Unknown", which is Argo CD's word for a state it
// assessed and could not name. It is the one label on the page that is not
// Argo CD's.
const Unreported = "Not reported"

// Tally is one bucket of the band, with Argo's word for it and a stable key
// beside it.
type Tally struct {
	// Label is Argo's own word, unchanged: "OutOfSync", not "Out of sync".
	Label string
	// Key is the word lower-cased and stripped to letters and digits
	// (`outofsync`), derived so an unknown word still gives a legal
	// identifier.
	Key   string
	Count int
}

// Condition is one entry of `status.conditions`, in Argo's words.
type Condition struct {
	// Type is Argo's own condition type: "ComparisonError", "InvalidSpecError".
	Type string
	// Status is "True"/"False" where the resource writes one. Applications do
	// not; ApplicationSets do.
	Status string
	// Message is Argo CD's sentence, first line only, clipped to
	// messageLimit.
	Message string
}

// Error reports whether this condition is Argo CD saying it could not do its
// job. By suffix, Argo CD's own convention (`ComparisonError`, `SyncError`, ...
// against `...Warning`), so a new error type is never drawn as fine.
func (c Condition) Error() bool { return strings.HasSuffix(c.Type, "Error") }

// Operation is `status.operationState`: who synced this, when, and whether it
// worked.
type Operation struct {
	// Phase is Argo's word: Running, Succeeded, Failed, Error, Terminating.
	Phase string
	// By is who asked for it: `operation.initiatedBy.username`, or
	// "automated" when the sync policy did.
	By string
	// At is when it finished, or when it started when it has not.
	At time.Time
	// Finished distinguishes the two, so the page can say "at" or "since".
	Finished bool
	// Message is Argo CD's sentence about the failure, first line, clipped.
	Message string
	// Revision is what was synced, shortened the way git shortens a sha.
	Revision string
}

// Known reports whether Argo CD has recorded an operation at all; a
// never-synced Application must not read as a success.
func (o Operation) Known() bool { return o.Phase != "" }

// Failed reports whether the last operation ended badly: `Failed` (applied and
// rejected) or `Error` (could not be attempted).
func (o Operation) Failed() bool { return o.Phase == "Failed" || o.Phase == "Error" }

// SyncSinceUnrecorded is what this page says where "OutOfSync since when" would
// go.
//
// Nothing in an Application records when its sync status changed:
// `status.sync` has no timestamp, and the timestamps in reach mean something
// else (`reconciledAt` is the last comparison, `operationState.finishedAt` the
// end of a sync, `health.lastTransitionTime` a health change). Only Argo CD's
// sync-status Event records it, and Kubernetes drops Events after about an
// hour, so the row links to an event archive instead of showing a number.
const SyncSinceUnrecorded = "not recorded by Argo"

// Deployment is the last entry of `status.history`: what Argo CD actually put
// on the cluster, and when. It differs from the last Operation because only a
// successful sync appends to the history, so after a failed sync this is what
// is still running.
type Deployment struct {
	// Known is whether Argo CD has recorded any deployment. False (never
	// synced) is an answer, not a blank cell.
	Known bool
	// At is `deployedAt`, zero when unparsable: an unreadable format costs the
	// time, never the fact.
	At time.Time
	// Revision is what was deployed, shortened the way git shortens a sha.
	Revision string
	// By is who asked for it, or "automated" when the sync policy did.
	By string
	// Kept is how many entries `status.history` holds — the window
	// `revisionHistoryLimit` leaves, ten by default.
	Kept int
	// Trimmed is how many deployments Argo CD has dropped out of that window,
	// and TrimmedKnown whether it could be worked out. Argo CD numbers
	// deployments from 0 and never reuses an id, so the oldest kept id is the
	// count of the dropped ones.
	Trimmed      int
	TrimmedKnown bool
}

// Application is one Argo CD Application, reduced to what a page can draw.
type Application struct {
	Namespace string
	Name      string

	// Deleting is whether `metadata.deletionTimestamp` is set, and DeletingSince
	// when (zero when unparsable). It is separate from Health, and worstFirst's
	// first rule, because a stuck finalizer leaves an Application `Healthy` and
	// `Synced` while it is being deleted.
	Deleting      bool
	DeletingSince time.Time

	// Sync and Health are Argo CD's words off `status.sync.status` and
	// `status.health.status`, or Unreported.
	Sync   string
	Health string
	// HealthMessage is Argo CD's sentence about the health, when it wrote one.
	HealthMessage string

	// HealthSince is `status.health.lastTransitionTime`. Argo CD's controller
	// sets it only when the health status changes, so it may be drawn as
	// "Degraded since 4d". Sync has no equivalent (SyncSinceUnrecorded).
	HealthSince time.Time
	// HealthSinceStamped is whether Argo CD wrote the field at all, kept apart
	// from HealthSince being non-zero so "never stamped" and "unparsable" stay
	// different answers.
	HealthSinceStamped bool

	// ReconciledAt is `status.reconciledAt`: when Argo CD last compared this
	// Application. It dates the readings on the row and moves whether or not
	// anything changed, so it is never drawn beside a status word.
	ReconciledAt time.Time

	// Deployment is the last entry of `status.history` (see the type).
	Deployment Deployment

	// Project is `spec.project`. Empty is not restated as "default", so the
	// page never claims a field the object does not carry.
	Project string
	// RepoURL and Path are the first source, and Sources how many there are.
	RepoURL string
	Path    string
	Sources int
	// TargetRevision is what was asked for and Revision what is deployed,
	// shortened.
	TargetRevision string
	Revision       string

	Conditions []Condition
	// ConditionsNote says how many were not drawn. Empty when Conditions is all
	// of them.
	ConditionsNote string
	Operation      Operation

	// Rank is where this row sorts, an index into worstFirst.
	Rank int
	// Reason is worstFirst's word for that rank, for the row's tooltip.
	Reason string
}

// Offending reports whether this Application belongs in the table: it matches
// one of worstFirst's rules, that is it is not both Healthy and Synced.
func (a Application) Offending() bool { return a.Rank < len(worstFirst) }

// rule is one line of the offenders table's order.
type rule struct {
	// Reason is why a row matching this is where it is.
	Reason string
	Match  func(Application) bool
}

// worstFirst is the order the offenders table is read in, lower is worse. It
// is the one judgment this page makes that Argo CD does not, so it lives in
// one table. The non-obvious choices:
//
//   - Being deleted outranks everything: an object waiting on a finalizer keeps
//     whatever health and sync it had.
//   - Degraded is broken now; OutOfSync is merely behind, often on purpose.
//   - An error condition outranks a failed sync: Argo CD could not assess the
//     Application, so every other figure on its row is stale.
//   - Unknown outranks OutOfSync on both axes: "could not tell" is worse than
//     "behind".
//   - A word this console does not know is offending on both axes, so an
//     Application with a new status word is still counted in Offending. Only
//     Healthy and Synced are good news.
var worstFirst = []rule{
	{"being deleted — waiting on a finalizer", func(a Application) bool { return a.Deleting }},
	{"Degraded — broken now", func(a Application) bool { return a.Health == "Degraded" }},
	{"Missing — Argo cannot find what it deployed", func(a Application) bool { return a.Health == "Missing" }},
	{"an error condition — Argo could not assess it", func(a Application) bool { return a.hasErrorCondition() }},
	{"the last sync failed", func(a Application) bool { return a.Operation.Failed() }},
	{"health not known", func(a Application) bool { return a.Health == "Unknown" || a.Health == Unreported }},
	{"a health word this console has not met", func(a Application) bool { return !known(HealthStates, a.Health) }},
	{"sync state not known", func(a Application) bool { return a.Sync == "Unknown" || a.Sync == Unreported }},
	{"a sync word this console has not met", func(a Application) bool { return !known(SyncStates, a.Sync) }},
	{"OutOfSync — behind its revision", func(a Application) bool { return a.Sync == "OutOfSync" }},
	{"Progressing", func(a Application) bool { return a.Health == "Progressing" }},
	{"Suspended", func(a Application) bool { return a.Health == "Suspended" }},
}

// known reports whether a status word is one of Argo CD's own, or Unreported,
// which has a rule of its own and must not read as an unrecognized word.
func known(words []string, word string) bool {
	if word == Unreported {
		return true
	}
	for _, w := range words {
		if w == word {
			return true
		}
	}
	return false
}

// HealthWorstFirst and SyncWorstFirst are the status words that mean trouble, in
// the order the offenders table ranks them, derived from worstFirst through
// rankOf so the headline cannot drift from the table. The other axis is held
// at good news, so a word ranks on its own account.
func HealthWorstFirst() []string {
	return trouble(append(append([]string{}, HealthStates...), Unreported), func(word string) Application {
		return Application{Health: word, Sync: "Synced"}
	})
}

func SyncWorstFirst() []string {
	return trouble(append(append([]string{}, SyncStates...), Unreported), func(word string) Application {
		return Application{Health: "Healthy", Sync: word}
	})
}

// trouble ranks the words that match a rule and drops the ones that match none,
// which is what "Healthy" and "Synced" look like from here.
func trouble(words []string, as func(string) Application) []string {
	type ranked struct {
		word string
		rank int
	}
	kept := make([]ranked, 0, len(words))
	for _, w := range words {
		if rank, _ := rankOf(as(w)); rank < len(worstFirst) {
			kept = append(kept, ranked{word: w, rank: rank})
		}
	}
	// Stable, so two words on one rule (Unknown and Not reported) keep
	// their declared order.
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].rank < kept[j].rank })

	out := make([]string, 0, len(kept))
	for _, k := range kept {
		out = append(out, k.word)
	}
	return out
}

func (a Application) hasErrorCondition() bool {
	for _, c := range a.Conditions {
		if c.Error() {
			return true
		}
	}
	return false
}

// rankOf is the first rule this Application matches, or len(worstFirst) when it
// matches none — which is what "Healthy and Synced" looks like from here.
func rankOf(a Application) (int, string) {
	for i, r := range worstFirst {
		if r.Match(a) {
			return i, r.Reason
		}
	}
	return len(worstFirst), ""
}

// Subject is one object named under a grouped cause.
type Subject struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// Cause is one condition type with every Application carrying it: twelve
// Applications failing on one unreachable repository are one thing to fix.
type Cause struct {
	// Type is Argo CD's condition type, unchanged.
	Type string
	// Message is one of the messages, as an example; they are nearly always
	// the same sentence.
	Message string
	// Error tells a failure from a warning, so the panel can color them
	// apart.
	Error bool

	Failing     int
	Objects     []Subject
	ObjectsNote string
}

// Set is one ApplicationSet that is not working.
type Set struct {
	Namespace string
	Name      string
	// Type, Status and Message are the condition that says so.
	Type    string
	Status  string
	Message string
}

// Project is one AppProject, with how many of the Applications read name it.
// Argo CD writes no meaningful status on an AppProject, so nothing here says
// whether one is failing.
type Project struct {
	Namespace   string
	Name        string
	Description string
	// Applications and Offending are counted from the Applications this page
	// read (see Report.Notes for the bounds).
	Applications int
	Offending    int
}

// Area is one kind, read or not read. Unavailable keeps an empty answer apart
// from a failed one: a failed area's figures are not drawn.
type Area struct {
	// Name is what the panel is called: "Applications".
	Name string
	// Kind is the slug, so the caller can link to the objects themselves.
	Kind string
	// Served is whether this cluster serves the kind at all.
	Served bool
	// Unavailable is why this area is empty: not served here, or the list call
	// failed and what it said. Empty means the figures are the truth.
	Unavailable string
	// Objects is how many were read.
	Objects int
	// Refused is how many the cluster sent and this code could not decode.
	// `state` decides on it: zero Objects with refusals is not a fresh
	// install.
	Refused int
	// Note is what was left out of this area and why. Empty means everything the
	// cluster had was read.
	Note string
}

// Report is the whole page's worth of answer.
type Report struct {
	Cluster   string
	Collected time.Time
	State     State
	// Detail is the state in words, for the states that need explaining.
	Detail string

	// Install is where Argo CD was found and how it was found. Never assumed.
	Install Install

	Areas []Area

	// Sync and Health are the band, in Argo's words and with their zeroes.
	Sync   []Tally
	Health []Tally
	// Applications is how many were read; Offending how many are not both
	// Healthy and Synced.
	Applications int
	Offending    int
	// Deleting is how many carry a `metadata.deletionTimestamp`. They are in
	// Offending too, and counted apart so the headline can name them.
	Deleting int

	Offenders     []Application
	OffendersNote string

	Causes     []Cause
	CausesNote string

	Sets       []Set
	SetsNote   string
	SetTotal   int
	SetFailing int

	Projects     []Project
	ProjectsNote string
	ProjectTotal int

	// Notes are the bounds and omissions that belong to the page rather than to
	// one panel.
	Notes []string
}

// joinNotes joins an area's non-empty notes; a truncated list may also have
// unreadable objects, and neither note may hide the other.
func joinNotes(notes ...string) string {
	kept := make([]string, 0, len(notes))
	for _, n := range notes {
		if n != "" {
			kept = append(kept, n)
		}
	}
	return strings.Join(kept, " ")
}

// --- the applications ---------------------------------------------------------

// tallies is the band, both axes, built in the pass that reads the objects so
// the totals cannot disagree with the rows.
type tallies struct {
	sync   []Tally
	health []Tally
}

func readApplications(items []rawApplication) ([]Application, tallies) {
	apps := make([]Application, 0, len(items))
	syncCount := map[string]int{}
	healthCount := map[string]int{}

	for i := range items {
		app := readApplication(items[i])
		syncCount[app.Sync]++
		healthCount[app.Health]++
		apps = append(apps, app)
	}

	return apps, tallies{
		sync:   tally(SyncStates, syncCount),
		health: tally(HealthStates, healthCount),
	}
}

func readApplication(object rawApplication) Application {
	var source rawSource
	if object.Spec.Source != nil {
		source = *object.Spec.Source
	}

	app := Application{
		Namespace: object.Metadata.Namespace,
		Name:      object.Metadata.Name,

		// Presence decides; an unparsable timestamp costs the duration, never
		// the fact.
		Deleting: strings.TrimSpace(object.Metadata.DeletionTimestamp) != "",

		Sync:          word(object.Status.Sync.Status),
		Health:        word(object.Status.Health.Status),
		HealthMessage: textutil.FirstLineN(object.Status.Health.Message, messageLimit),

		Project:        object.Spec.Project,
		TargetRevision: source.TargetRevision,
		Revision:       shortRevision(object.Status.Sync.Revision),

		RepoURL: source.RepoURL,
		Path:    source.Path,
		Sources: 1,
	}

	// A multi-source Application has `spec.sources` and no `spec.source`:
	// draw its first source and the count.
	if app.RepoURL == "" {
		if sources := object.Spec.Sources; len(sources) > 0 {
			app.Sources = len(sources)
			app.RepoURL = strings.TrimSpace(sources[0].RepoURL)
			app.Path = strings.TrimSpace(sources[0].Path)
			if app.TargetRevision == "" {
				app.TargetRevision = strings.TrimSpace(sources[0].TargetRevision)
			}
		} else {
			app.Sources = 0
		}
	}

	if at, ok := timeAt(object.Metadata.DeletionTimestamp); ok {
		app.DeletingSince = at
	}

	// Presence and value read apart: an unparsable `lastTransitionTime` is
	// not the same fact as an Argo CD that never writes it.
	app.HealthSinceStamped = strings.TrimSpace(object.Status.Health.LastTransitionTime) != ""
	if at, ok := timeAt(object.Status.Health.LastTransitionTime); ok {
		app.HealthSince = at
	}
	if at, ok := timeAt(object.Status.ReconciledAt); ok {
		app.ReconciledAt = at
	}
	app.Deployment = deploymentOf(object.Status.History)

	app.Conditions, app.ConditionsNote = conditionsOf(object.Status.Conditions, conditionLimit)
	app.Operation = operationOf(object.Status.OperationState)
	app.Rank, app.Reason = rankOf(app)
	return app
}

// deploymentOf reads the last deployment out of `status.history`. Argo CD
// appends, so the last entry is the most recent. An entry with no revision is
// not a deployment and is answered as an empty history.
func deploymentOf(history []rawHistory) Deployment {
	if len(history) == 0 {
		// Never successfully deployed: Known stays false.
		return Deployment{}
	}

	last := history[len(history)-1]
	out := Deployment{
		Known:    strings.TrimSpace(last.Revision) != "",
		Revision: shortRevision(last.Revision),
		Kept:     len(history),
	}
	if !out.Known {
		return Deployment{Kept: len(history)}
	}

	if at, ok := timeAt(last.DeployedAt); ok {
		out.At = at
	} else if at, ok := timeAt(last.DeployStartedAt); ok {
		// Without `deployedAt`, the start is within seconds of it.
		out.At = at
	}

	switch {
	case strings.TrimSpace(last.InitiatedBy.Username) != "":
		out.By = strings.TrimSpace(last.InitiatedBy.Username)
	case last.InitiatedBy.Automated:
		out.By = "automated"
	}

	// How many Argo CD has dropped, from the oldest kept id. A nil id leaves
	// TrimmedKnown false.
	if first := history[0].ID; first != nil && *first >= 0 {
		out.Trimmed, out.TrimmedKnown = int(*first), true
	}
	return out
}

// word is Argo CD's word for a state, or Unreported when it has not written
// one. Trimmed, so " Healthy" is not its own bucket beside "Healthy".
func word(s string) string {
	if trimmed := strings.TrimSpace(s); trimmed != "" {
		return trimmed
	}
	return Unreported
}

// shortRevision shortens what Argo CD recorded as deployed: a 40-character
// sha to seven, as git does; anything else (a branch, a chart version) is only
// clipped, so it can still be looked up.
func shortRevision(s string) string {
	s = strings.TrimSpace(s)
	if len(s) != 40 {
		return textutil.Clip(s, 24)
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return textutil.Clip(s, 24)
		}
	}
	return s[:7]
}

// conditionsOf reads `status.conditions`, bounded, and says what it left out.
// An entry with no `type` is dropped: it says nothing.
func conditionsOf(raw []rawCondition, limit int) ([]Condition, string) {
	out := make([]Condition, 0, len(raw))
	for _, entry := range raw {
		condition := Condition{
			Type:    strings.TrimSpace(entry.Type),
			Status:  strings.TrimSpace(entry.Status),
			Message: textutil.FirstLineN(strings.TrimSpace(entry.Message), messageLimit),
		}
		if condition.Type == "" {
			continue
		}
		out = append(out, condition)
	}

	// Errors first: Argo CD writes conditions in discovery order, and an
	// error behind two warnings would fall off the row.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Error() && !out[j].Error()
	})

	if limit <= 0 || len(out) <= limit {
		return out, ""
	}
	return out[:limit], fmt.Sprintf("and %d more", len(out)-limit)
}

func operationOf(state *rawOperationState) Operation {
	if state == nil {
		// Never synced: Known is false.
		return Operation{}
	}

	op := Operation{
		Phase:    strings.TrimSpace(state.Phase),
		Message:  textutil.FirstLineN(strings.TrimSpace(state.Message), messageLimit),
		Revision: shortRevision(state.Operation.Sync.Revision),
		By:       initiatedBy(state),
	}

	if at, ok := timeAt(state.FinishedAt); ok {
		op.At, op.Finished = at, true
	} else if at, ok := timeAt(state.StartedAt); ok {
		op.At = at
	}
	return op
}

// initiatedBy is who asked for the last sync, or "automated" when the sync
// policy did: an answer, not a blank.
func initiatedBy(state *rawOperationState) string {
	if user := strings.TrimSpace(state.Operation.InitiatedBy.Username); user != "" {
		return user
	}
	if state.Operation.InitiatedBy.Automated {
		return "automated"
	}
	return ""
}

// offenders is everything not both Healthy and Synced, worst first.
func offenders(apps []Application, listNote string) ([]Application, string) {
	rows := make([]Application, 0, len(apps))
	for _, a := range apps {
		if a.Offending() {
			rows = append(rows, a)
		}
	}

	// Worst first by rank, then by name so equal rows keep a stable order
	// between readings.
	sort.SliceStable(rows, func(i, j int) bool {
		switch {
		case rows[i].Rank != rows[j].Rank:
			return rows[i].Rank < rows[j].Rank
		case rows[i].Namespace != rows[j].Namespace:
			return rows[i].Namespace < rows[j].Namespace
		default:
			return rows[i].Name < rows[j].Name
		}
	})

	if len(rows) <= offenderLimit {
		return rows, ""
	}
	note := fmt.Sprintf("The %d worst of %d Applications that are not both Healthy and Synced.", offenderLimit, len(rows))
	if listNote != "" {
		note += " Of those that were read — see the note on the Applications panel."
	}
	return rows[:offenderLimit], note
}

// causes groups the Applications by the conditions they carry.
func causes(apps []Application) ([]Cause, string) {
	grouped := map[string]*Cause{}
	var order []string

	for _, a := range apps {
		for _, c := range a.Conditions {
			cause, seen := grouped[c.Type]
			if !seen {
				cause = &Cause{Type: c.Type, Message: c.Message, Error: c.Error()}
				grouped[c.Type] = cause
				order = append(order, c.Type)
			}
			cause.Failing++
			if len(cause.Objects) < causeSubjectLimit {
				cause.Objects = append(cause.Objects, Subject{Namespace: a.Namespace, Name: a.Name})
			}
		}
	}

	out := make([]Cause, 0, len(order))
	for _, t := range order {
		cause := *grouped[t]
		if left := cause.Failing - len(cause.Objects); left > 0 {
			cause.ObjectsNote = fmt.Sprintf("and %d more", left)
		}
		out = append(out, cause)
	}

	// Errors first, then the ones the most Applications carry.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Error != out[j].Error {
			return out[i].Error
		}
		if out[i].Failing != out[j].Failing {
			return out[i].Failing > out[j].Failing
		}
		return out[i].Type < out[j].Type
	})

	if len(out) <= causeLimit {
		return out, ""
	}
	return out[:causeLimit], fmt.Sprintf("The %d most common of %d distinct conditions.", causeLimit, len(out))
}

// --- the two smaller panels ----------------------------------------------------

// failingSets picks the ApplicationSets that are not working, and counts the
// rest. The test is per condition type, because True means failure for
// `ErrorOccurred` and success for `ParametersGenerated` and
// `ResourcesUpToDate`.
func failingSets(items []rawSet) ([]Set, string, int, int) {
	rows := make([]Set, 0, len(items))
	for i := range items {
		object := items[i]
		conditions, _ := conditionsOf(object.Status.Conditions, 0)
		for _, c := range conditions {
			if !setConditionFails(c) {
				continue
			}
			rows = append(rows, Set{
				Namespace: object.Metadata.Namespace,
				Name:      object.Metadata.Name,
				Type:      c.Type,
				Status:    c.Status,
				Message:   c.Message,
			})
			break
		}
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Namespace != rows[j].Namespace {
			return rows[i].Namespace < rows[j].Namespace
		}
		return rows[i].Name < rows[j].Name
	})

	failing := len(rows)
	if len(rows) <= failingSetLimit {
		return rows, "", len(items), failing
	}
	note := fmt.Sprintf("The first %d of %d ApplicationSets with a failing condition.", failingSetLimit, failing)
	return rows[:failingSetLimit], note, len(items), failing
}

// setConditionFails decides whether one ApplicationSet condition is bad news.
func setConditionFails(c Condition) bool {
	switch c.Type {
	case "ErrorOccurred":
		return c.Status == "True"
	case "ParametersGenerated", "ResourcesUpToDate":
		return c.Status == "False"
	default:
		// An unknown type is reported only when it is an error by Argo CD's
		// suffix convention: its polarity is not guessed.
		return c.Error() && c.Status == "True"
	}
}

// projectRows lists the AppProjects with what the Applications say about them.
func projectRows(items []rawProject, apps []Application) ([]Project, string, int) {
	// Attribution is by project name: the AppProject often lives in another
	// namespace than the Application ("applications in any namespace"). A name
	// held by several AppProjects (every install has a `default`) goes to the one
	// in the Application's own namespace, or to none, since these objects cannot
	// tell which Argo CD reconciles the Application.
	homes := map[string]map[string]bool{}
	for i := range items {
		name := items[i].Metadata.Name
		if homes[name] == nil {
			homes[name] = map[string]bool{}
		}
		homes[name][items[i].Metadata.Namespace] = true
	}

	// where reports which AppProject an Application belongs to, as
	// "namespace/name", or false when that cannot be decided.
	where := func(appNamespace, project string) (string, bool) {
		if project == "" {
			// Argo CD reads no `spec.project` as "default", and so does the
			// count.
			project = "default"
		}
		in := homes[project]
		switch {
		case len(in) == 1:
			for namespace := range in {
				return namespace + "/" + project, true
			}
		case in[appNamespace]:
			return appNamespace + "/" + project, true
		}
		return "", false
	}

	total := map[string]int{}
	offending := map[string]int{}
	for _, a := range apps {
		k, ok := where(a.Namespace, a.Project)
		if !ok {
			continue
		}
		total[k]++
		if a.Offending() {
			offending[k]++
		}
	}

	rows := make([]Project, 0, len(items))
	for i := range items {
		object := items[i]
		name := object.Metadata.Name
		k := object.Metadata.Namespace + "/" + name
		rows = append(rows, Project{
			Namespace:    object.Metadata.Namespace,
			Name:         name,
			Description:  textutil.FirstLineN(object.Spec.Description, messageLimit),
			Applications: total[k],
			Offending:    offending[k],
		})
	}

	// The projects with something wrong in them first, then the busiest, then
	// by name.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Offending != rows[j].Offending {
			return rows[i].Offending > rows[j].Offending
		}
		if rows[i].Applications != rows[j].Applications {
			return rows[i].Applications > rows[j].Applications
		}
		return rows[i].Name < rows[j].Name
	})

	if len(rows) <= projectRowLimit {
		return rows, "", len(items)
	}
	return rows[:projectRowLimit], fmt.Sprintf("The %d busiest of %d AppProjects.", projectRowLimit, len(items)), len(items)
}

// --- the state -------------------------------------------------------------------

// state decides which of the four answers the areas add up to, and words it.
// The Applications area decides: listable AppProjects with unreadable
// Applications is an install nobody can see, not one with nothing deployed.
func state(areas []Area) (State, string) {
	var served int
	var applications Area
	for _, a := range areas {
		if a.Served {
			served++
		}
		if a.Kind == ApplicationKind {
			applications = a
		}
	}

	switch {
	case served == 0:
		// Build returns early on this too; kept so a kind added later cannot
		// draw a band of zeroes for a cluster with no Argo CD.
		return StateAbsent, "This cluster serves none of Argo CD's custom resource kinds, so nothing here is deployed by Argo."

	case !applications.Served:
		// Some of Argo CD's kinds but not Applications: a partial or removed
		// install, and the question cannot be answered.
		return StateUnreadable, "This cluster serves some of Argo CD's kinds but not " + ApplicationKind + ", " +
			"so there is no Application to read. That is a partial or removed installation, not an idle one."

	case applications.Unavailable != "":
		// Not StatePending: see the constant.
		return StateUnreadable, "Argo CD's Applications are served here and could not be read. " +
			"That is a permission you lack, not a pipeline with nothing in it — the reason is below."

	case applications.Objects == 0 && applications.Refused > 0:
		// `Objects` counts decoded items, so a list where every item failed
		// to decode must not fall to the fresh-install branch below.
		return StateUnreadable, fmt.Sprintf("Argo CD's Applications are served here and not one of the %d this cluster "+
			"returned could be read. That is an answer this console could not parse — a shape Argo does not "+
			"document, or one from a version it does not yet — and not an installation with nothing in it. "+
			"The reason is below.", applications.Refused)

	case applications.Objects == 0:
		return StatePending, "Argo CD is installed here and no Application was read. " +
			"A fresh install looks like this, and so does an installation whose Applications live in a namespace " +
			"you cannot list; nothing below means the cluster deploys nothing."

	default:
		return StatePresent, ""
	}
}

// notes says what this page did not read, phrased as omissions, plus every
// area's own note.
func notes(areas []Area) []string {
	out := []string{
		"Read from " + ApplicationKind + ", " + ApplicationSetKind + " and " + AppProjectKind + " only. " +
			"Argo Rollouts publishes rollouts, analysisruns and experiments under the same argoproj.io group and " +
			"none of them is on this page; they are all in the Custom Resources menu.",
		// Named field by field: these are the parts an operator would expect
		// this page to hold.
		"Two parts of an Application are not read: status.resources, which is what it manages, " +
			"and the sync result inside status.operationState. Only the last entry of " +
			"status.history is read, so what was deployed before that is not here. Open the " +
			"Application itself for any of them.",
		// Why rows say "not recorded by Argo": the limitation is Argo CD's.
		"Argo records when an Application's health last changed — status.health.lastTransitionTime, " +
			"stamped only when the health word changes — and records nothing at all about when its " +
			"sync status changed: status.sync carries a status, a revision and comparedTo, and no " +
			"timestamp. So this page says how long something has been Degraded and refuses to say how " +
			"long it has been OutOfSync. Argo does emit a Kubernetes event for both; Kubernetes drops " +
			"it after about an hour, and while it is held, the Application's own page in the console " +
			"shows it under Events.",
		"status.reconciledAt is when Argo last compared an Application against its source. It moves " +
			"every few minutes whether or not anything changed, so on this page it says how old the " +
			"readings on a row are and is never drawn as when something started.",
		"An AppProject carries no status of its own, so nothing on this page can say whether one is failing. " +
			"The figures beside a project are counted from the Applications that were read.",
	}
	for _, a := range areas {
		if a.Note != "" {
			out = append(out, a.Name+": "+a.Note)
		}
	}
	return out
}

// --- the band ---------------------------------------------------------------------

// tally counts Argo CD's words, keeping the known ones in their fixed order
// with their zeroes, and appending any other word (Unreported included, only
// when counted) rather than folding it into Unknown.
func tally(order []string, counted map[string]int) []Tally {
	out := make([]Tally, 0, len(order)+len(counted))
	seen := map[string]bool{}
	for _, label := range order {
		seen[label] = true
		out = append(out, Tally{Label: label, Key: key(label), Count: counted[label]})
	}

	extra := make([]string, 0, len(counted))
	for label := range counted {
		if !seen[label] {
			extra = append(extra, label)
		}
	}
	sort.Strings(extra)
	for _, label := range extra {
		out = append(out, Tally{Label: label, Key: key(label), Count: counted[label]})
	}
	return out
}

// key is a word reduced to a class name: "OutOfSync" becomes "outofsync".
// Everything outside a-z0-9 is dropped; an empty result is "notreported",
// the same key as `key(Unreported)`.
func key(word string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(word)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "notreported"
	}
	return b.String()
}

// --- parsing -----------------------------------------------------------------------

// timeAt reads one of Argo CD's RFC 3339 timestamps. Anything else is absent,
// never the zero time, which would render as a date. Parsed here rather than as
// `metav1.Time` so a bad value costs the time, not the Application (raw.go).
func timeAt(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

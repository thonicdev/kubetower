package api

import (
	"time"
)

// --- Helm -------------------------------------------------------------------

// HelmRelease carries its own state: a release whose stored payload could not be
// decoded is `unreadable` at the item and still listed, never dropped.
type HelmRelease struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// Revision is the current one, the highest; not necessarily the running one.
	Revision int `json:"revision,omitempty"`
	// DeployedRevision is the revision actually running, where it is not the
	// current one, and zero otherwise: after a failed upgrade the row shows both
	// the failure and what still runs.
	DeployedRevision int    `json:"deployedRevision,omitempty"`
	Chart            string `json:"chart,omitempty"`
	// ChartVersion is the chart's own version, which moves independently of
	// AppVersion; an upgrade is read off it.
	ChartVersion string `json:"chartVersion,omitempty"`
	AppVersion   string `json:"appVersion,omitempty"`
	// Deprecated is the chart's own `deprecated:` flag.
	Deprecated bool `json:"deprecated,omitempty"`
	// Description is Helm's own sentence about the last operation, including
	// what a failed upgrade said went wrong. Prose: a client must let it wrap.
	Description string `json:"description,omitempty"`
	// Note is set when the current revision is not the deployed one, so a
	// failed upgrade does not read as nothing running.
	Note      string     `json:"note,omitempty"`
	Status    string     `json:"status,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	// Age is UpdatedAt in the server's wording, as EventEntry.Age is, taken at
	// the read; the client carries it forward on its own clock. Absent with
	// UpdatedAt.
	Age    string  `json:"age,omitempty"`
	State  State   `json:"state"`
	Reason *Reason `json:"reason,omitempty"`
}

// HelmRevision is one entry of a release's history: what changed and when.
type HelmRevision struct {
	Revision  int        `json:"revision"`
	Status    string     `json:"status,omitempty"`
	Tone      string     `json:"tone,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	// ChartVersion and AppVersion move independently, as on a release.
	ChartVersion string `json:"chartVersion,omitempty"`
	AppVersion   string `json:"appVersion,omitempty"`
	Description  string `json:"description,omitempty"`
	// Mark is `current`, `deployed` or `current-and-deployed`: one field rather
	// than two booleans, since the two diverge exactly when it matters.
	Mark string `json:"mark,omitempty"`
	// Note is why a row has no chart version: its stored payload would not decode,
	// and the Secret's labels are then all there is.
	Note string `json:"note,omitempty"`
}

// HelmReleaseDetail is one release's page.
type HelmReleaseDetail struct {
	Release   HelmRelease    `json:"release"`
	Revisions []HelmRevision `json:"revisions,omitempty"`
	// HistoryMax is how many revisions are kept (`--history-max`).
	HistoryMax *int `json:"historyMax,omitempty"`
	// ValuesSupplied is whether the current revision carries user-supplied
	// values. The values are not on this answer: they often hold credentials, so
	// they arrive through RevealHelmValues, reviewed and recorded like a Secret's
	// key. False means none were supplied; PayloadNote overrides it.
	ValuesSupplied bool `json:"valuesSupplied,omitempty"`
	// NotesRendered is whether Helm rendered `NOTES.txt` for the current
	// revision. The notes may hold a generated password, so they arrive through
	// RevealHelmNotes. False means the chart ships none; PayloadNote overrides it.
	NotesRendered bool `json:"notesRendered,omitempty"`
	// PayloadNote says the current revision's stored document would not decode,
	// so neither of the two above is an answer: "none" and "unreadable" differ.
	PayloadNote string         `json:"payloadNote,omitempty"`
	Manifest    *ManifestPanel `json:"manifest,omitempty"`
}

// HelmValues is what the operator supplied to one revision of a release, as
// YAML: `helm get values` without `--all`. Only RevealHelmValues answers it.
type HelmValues struct {
	Revision int    `json:"revision"`
	Values   string `json:"values"`
}

// HelmNotes is what Helm rendered out of the chart's `NOTES.txt` for one
// revision of a release. Only RevealHelmNotes answers it.
type HelmNotes struct {
	Revision int    `json:"revision"`
	Notes    string `json:"notes"`
}

// HelmRevealQuery is one reveal of a release's values or notes, at the revision
// the page was drawn from, which the record names before the read.
type HelmRevealQuery struct {
	Cluster   string
	Namespace string
	Name      string
	Revision  int
}

// --- Overview ---------------------------------------------------------------

// CapacityCell is one resource's requested, total and used figures.
type CapacityCell struct {
	Label     string   `json:"label"`
	Requested Reading  `json:"requested"`
	Capacity  Reading  `json:"capacity"`
	Used      *Reading `json:"used,omitempty"`
	// RequestedOf and UsedOf are each line's hover, `requested / capacity` and
	// `used / capacity` in the capacity's unit (resources.Fraction). Empty where
	// either figure was not measured.
	RequestedOf string `json:"requestedOf,omitempty"`
	UsedOf      string `json:"usedOf,omitempty"`
	// Ref is the node list, for all three: the figures are sums over machines
	// against allocatable, which only a node has (the pod figure counts pod
	// slots, not pods).
	Ref  *Ref   `json:"ref,omitempty"`
	Tone string `json:"tone,omitempty"`
}

// TopEntry is one row of a leaderboard.
type TopEntry struct {
	Ref     Ref     `json:"ref"`
	Reading Reading `json:"reading"`
}

// TopBoard is one leaderboard.
type TopBoard struct {
	Label   string     `json:"label"`
	Entries []TopEntry `json:"entries"`
}

// CountEntry is one kind and how much of it is in the state it declares.
// Healthy is a pointer: a kind with no notion of health draws no fraction, and
// absent is not zero.
type CountEntry struct {
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Count   int    `json:"count"`
	Healthy *int   `json:"healthy,omitempty"`
	// Note is what the rest are ("2 not ready"); empty when all are healthy.
	Note string `json:"note,omitempty"`
	Tone string `json:"tone,omitempty"`
	Ref  *Ref   `json:"ref,omitempty"`
}

// EventEntry is one cluster event, with what an operator reads it for, such as
// how many times it fired.
type EventEntry struct {
	Text string     `json:"text"`
	Href string     `json:"href,omitempty"`
	Hint string     `json:"hint,omitempty"`
	At   *time.Time `json:"at,omitempty"`
	// Age is the server's wording, so two panels cannot spell it two ways.
	Age   string `json:"age,omitempty"`
	Scope *Ref   `json:"scope,omitempty"`
	// Object is what the event is about, unlinked where the console has no list
	// for its kind.
	Object  *Ref   `json:"object,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	Count   int    `json:"count,omitempty"`
	Tone    string `json:"tone,omitempty"`
}

// AlertEntry is a machine-level condition worth acting on, lifted out of the
// node table so it cannot be missed.
type AlertEntry struct {
	// Subject is the node; its destination opens the object, where its
	// conditions and events are.
	Subject Ref    `json:"subject"`
	Summary string `json:"summary"`
	Detail  string `json:"detail,omitempty"`
	Tone    string `json:"tone,omitempty"`
}

// TroubleEntry is one workload that is not in the state it declares.
type TroubleEntry struct {
	Kind   *Ref   `json:"kind,omitempty"`
	Scope  *Ref   `json:"scope,omitempty"`
	Object Ref    `json:"object"`
	Detail string `json:"detail"`
	// Since is when the object left the state it declares, from the object's
	// own record (per kind, in the contract), never the page's clock. Absent
	// where the kind keeps none, and then never drawn as now.
	Since *time.Time `json:"since,omitempty"`
	// Age is Since in the server's wording, as EventEntry.Age is.
	Age  string `json:"age,omitempty"`
	Tone string `json:"tone,omitempty"`
}

// RestartEntry is a container that keeps exiting, read by why it last exited
// and when.
type RestartEntry struct {
	Scope     *Ref   `json:"scope,omitempty"`
	Object    Ref    `json:"object"`
	Container string `json:"container"`
	// Init marks a container under `initContainerStatuses`: an init container
	// or a native sidecar. Always written, as PodContainer.Init is, so the reader
	// opens the right logs. A mark, not a rank: the sort stays count-first.
	Init     bool   `json:"init"`
	Restarts int    `json:"restarts"`
	Reason   string `json:"reason,omitempty"`
	// LastExitAt is when it last exited, from the kubelet's stamp on the
	// previous run, never the page's clock. Absent where there is no instant,
	// which is not the same as just now. Filled from overview.Restarting.LastExit.
	LastExitAt *time.Time `json:"lastExitAt,omitempty"`
	Age        string     `json:"age,omitempty"`
	Tone       string     `json:"tone,omitempty"`
}

// PhaseEntry is one band of the workload phase bar, as data, never markup.
type PhaseEntry struct {
	Phase string `json:"phase"`
	Count int    `json:"count"`
	Tone  string `json:"tone,omitempty"`
	Ref   *Ref   `json:"ref,omitempty"`
}

// SkippedPanel is one panel this page could not fill, with the reason. Unlike a
// failed panel envelope, it covers a panel never asked for because a
// prerequisite was missing, so it has no envelope to carry a reason.
type SkippedPanel struct {
	Panel  string `json:"panel"`
	Reason string `json:"reason"`
	// Affects is which members of Overview this failure left incomplete, by
	// property name, so a client marks the panel and an `empty` Events does not
	// read as "no warnings". Required, never nil: `[]` means it spoilt nothing
	// drawn.
	Affects []string `json:"affects"`
}

// Overview is the cluster page: each panel is its own envelope, so one missing
// source neither blanks the page nor draws as zero.
type Overview struct {
	Cluster string `json:"cluster"`
	// Version is the API server's, as it states it. Empty when discovery did not
	// answer.
	Version     string     `json:"version,omitempty"`
	CollectedAt *time.Time `json:"collectedAt,omitempty"`
	// AllWell is: there is nothing on this page to act on, machines and
	// workloads both. A server judgment, so it is never raised from a page
	// with panels missing.
	AllWell *bool `json:"allWell,omitempty"`
	// NodesSummary is the machines as one sentence ("9 of 10 nodes healthy"),
	// pointed at the node list.
	NodesSummary *Ref   `json:"nodesSummary,omitempty"`
	NodesTone    string `json:"nodesTone,omitempty"`
	// WorkloadsSummary is the same for what is running, pointed at the pod list.
	WorkloadsSummary *Ref `json:"workloadsSummary,omitempty"`

	Counts     List[CountEntry]    `json:"counts"`
	Capacity   List[CapacityCell]  `json:"capacity"`
	Top        List[TopBoard]      `json:"top"`
	Events     List[EventEntry]    `json:"events"`
	Alerts     List[AlertEntry]    `json:"alerts"`
	Trouble    List[TroubleEntry]  `json:"trouble"`
	Restarting List[RestartEntry]  `json:"restarting"`
	Phases     List[PhaseEntry]    `json:"phases"`
	History    Value[MetricsPanel] `json:"history"`

	// RestartingTotal and EventsTotal are how many rows `restarting` and
	// `events` held before the server cut them, so a client can say a list is
	// not whole. Nil where the panel did not answer.
	RestartingTotal *int `json:"restartingTotal,omitempty"`
	EventsTotal     *int `json:"eventsTotal,omitempty"`

	// NotShown is what this page did not look at (see SkippedPanel).
	NotShown []SkippedPanel `json:"notShown,omitempty"`

	// AccessNote says the access review narrowed what this page counts to the
	// namespaces the person may list. Absent when every list was whole.
	AccessNote string `json:"accessNote,omitempty"`
}

// ObjectEvent is one event about one object, as the object's own panel draws
// it: who reported it and over what span. EventEntry is the cluster page's row.
type ObjectEvent struct {
	// Type is "Normal" or "Warning", passed through: a controller can write
	// anything.
	Type    string `json:"type,omitempty"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	From    string `json:"from,omitempty"`
	// Count is how many times it has fired.
	Count int32 `json:"count,omitempty"`
	// First and Last are the span. Pointers: a zero time.Time marshals as
	// `0001-01-01T00:00:00Z`, a wrong date rather than "unknown".
	First *time.Time `json:"first,omitempty"`
	Last  *time.Time `json:"last,omitempty"`
	// About is the owned object the event is about (a controller's panel also
	// carries its ReplicaSets', Jobs' and pods' events). Absent on the object's
	// own events.
	About *EventAbout `json:"about,omitempty"`
}

// EventAbout names an owned object an event is about, in the namespace of the
// object on screen: a controller owns nothing outside it.
type EventAbout struct {
	// Kind is the plural resource name, as an address carries it: "pods".
	Kind string `json:"kind"`
	// Title is the singular kind, as a person reads it: "Pod".
	Title string `json:"title"`
	Name  string `json:"name"`
}

// --- Metrics, history, env --------------------------------------------------

// MetricsSeries is one line of the panel; a chart may be several lines.
//
// Chart groups the lines of one chart (Key does not match the icon row for
// every measurement); empty means the line is its own chart. Reason is why the
// line has no samples, set only then, so an absent line reads as the absence of
// that measurement.
type MetricsSeries struct {
	Key    string  `json:"key"`
	Chart  string  `json:"chart,omitempty"`
	Label  string  `json:"label"`
	Unit   string  `json:"unit,omitempty"`
	Data   Series  `json:"data"`
	Reason *Reason `json:"reason,omitempty"`
	// Current is the measurement now from a source with no history
	// (metrics-server), sent only on a line with no samples, so a cluster
	// without Prometheus still shows usage. Reason still says why there is no
	// series.
	Current *Reading `json:"current,omitempty"`
	// Period is the key of a `windows` entry that would show what this empty
	// line cannot: on a finished pod, the period that holds its run. The client
	// offers it as a button; nothing selects it automatically.
	Period string `json:"period,omitempty"`
	// RunTotal is a finished pod's network counter over its whole run, on an
	// empty `network_rx` or `network_tx` line where Prometheus holds a sample:
	// the increase between first and last samples, or the single value.
	RunTotal *Reading `json:"runTotal,omitempty"`
}

// MetricsChoice is one measurement the panel offers. An unavailable one carries
// a Reason and is drawn as a disabled control, never a flat zero chart.
type MetricsChoice struct {
	Key       string  `json:"key"`
	Label     string  `json:"label"`
	Available bool    `json:"available"`
	Selected  bool    `json:"selected,omitempty"`
	Reason    *Reason `json:"reason,omitempty"`
}

// MetricsWindow is one span the panel can be asked for, listed by the server
// because which are answerable depends on the source.
type MetricsWindow struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Selected bool   `json:"selected,omitempty"`
}

// MetricsPanel is the panel for one object at one zoom level.
type MetricsPanel struct {
	Series  []MetricsSeries `json:"series"`
	Caption string          `json:"caption,omitempty"`
	Metrics []MetricsChoice `json:"metrics,omitempty"`
	Windows []MetricsWindow `json:"windows,omitempty"`
	// Zoomable says the full-size overlay can honor the selected series. A
	// pointer: "cannot be zoomed" differs from "not said".
	Zoomable *bool `json:"zoomable,omitempty"`
}

// HistoryEntry is one recorded change.
type HistoryEntry struct {
	At       time.Time `json:"at"`
	Summary  string    `json:"summary"`
	Revision int       `json:"revision,omitempty"`
	Ref      *Ref      `json:"ref,omitempty"`
}

// ChangesPanel is who last wrote which part of one object, from the field
// managers' record, newest first (`internal/changes`). The `/archive` name is
// reserved and unbuilt.
type ChangesPanel struct {
	Entries []HistoryEntry `json:"entries"`
	// EntriesState is the entries half's own answer, so "nothing", "off" and
	// "unreadable" stay apart. `ok` where there are entries; `absent` where the
	// object carries no managedFields, which is common. A failed read of the
	// object itself fails the whole panel on the envelope instead.
	EntriesState State `json:"entriesState,omitempty"`
	// EntriesReason is the sentence for a state that is not ok. Empty otherwise.
	EntriesReason *Reason `json:"entriesReason,omitempty"`
	// Rollouts is what shipped and when, which a rollback is chosen from. Nil,
	// not empty, on kinds that have no revisions.
	Rollouts []Rollout `json:"rollouts,omitempty"`
	// RolloutsState is the rollout half's own answer: it reads the ReplicaSets,
	// which may be forbidden while the object reads fine, and an unreadable
	// history must not read as never shipped. Absent where the kind has no
	// revisions.
	RolloutsState State `json:"rolloutsState,omitempty"`
	// RolloutsReason says why, on any state that is not ok or empty, from the
	// route's shared classifier.
	RolloutsReason *Reason `json:"rolloutsReason,omitempty"`
}

// Rollout is one revision of a workload.
type Rollout struct {
	Revision string    `json:"revision"`
	At       time.Time `json:"at"`
	// Name is the ReplicaSet or ControllerRevision carrying it, which a rollback
	// names.
	Name     string `json:"name,omitempty"`
	Replicas int64  `json:"replicas,omitempty"`
	// Image is what the revision ran, which a rollback is chosen by.
	Image string `json:"image,omitempty"`
	// Current is marked rather than inferred: after a failed rollout the
	// highest revision is not the deployed one.
	Current bool   `json:"current,omitempty"`
	Cause   string `json:"cause,omitempty"`
}

// EnvValue is one revealed environment row. The value travels only in the body,
// never a URL, which would put it in an access log.
type EnvValue struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source,omitempty"`
}

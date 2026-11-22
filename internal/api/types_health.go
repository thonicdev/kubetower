package api

import (
	"time"
)

// --- Health -----------------------------------------------------------------

// HealthCheck is one diagnosis, with its own state: the page is never a single
// verdict.
type HealthCheck struct {
	Key     string  `json:"key"`
	Label   string  `json:"label"`
	State   State   `json:"state"`
	Reason  *Reason `json:"reason,omitempty"`
	Cluster string  `json:"cluster,omitempty"`
	// Remedy is who fixes it: renewing a session and granting IAM are different
	// people's jobs. Absent on a check that answered, and on a failure with no
	// known remedy (Reason says so instead).
	Remedy *Remedy `json:"remedy,omitempty"`
	// ReadOnly is per cluster, where the guardrail is; Capabilities.ReadOnly is
	// the profile-wide fact.
	ReadOnly bool `json:"readOnly,omitempty"`
	// Version is the API server's, where it answered: a cluster several minors
	// ahead fails on moved APIs in a way that looks like a console defect.
	Version string `json:"version,omitempty"`
	// MetricsSource is what is behind this cluster's charts, so an empty chart
	// has an explanation instead of looking broken.
	MetricsSource string `json:"metricsSource,omitempty"`
	// CheckedAt is when this answer was obtained. Absent on a check in `pending`.
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
	TookMs    *int       `json:"tookMs,omitempty"`
}

// HealthCount is one summary pill.
type HealthCount struct {
	Label   string     `json:"label"`
	Count   int        `json:"count"`
	Code    ReasonCode `json:"code,omitempty"`
	Tone    string     `json:"tone,omitempty"`
	Cluster string     `json:"cluster,omitempty"`
}

// HealthNotice is something about the machine or the installation that fails
// before any cluster is contacted.
//
// `Measured: false` says the fact could not be established at all, which is
// neither fine nor broken: a cache directory that cannot be listed must not read
// as "there is no session".
type HealthNotice struct {
	Key    string `json:"key"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	// Command is exactly what to type. Absent rather than guessed, as on a notice
	// that cannot be measured, where the fix may be a mount rather than a sign-in.
	Command string `json:"command,omitempty"`
	// Paths are the files the notice is about, named rather than counted.
	Paths    []string `json:"paths,omitempty"`
	Severity string   `json:"severity"`
	// Measured is always marshalled: the contract's `default: true` makes it
	// required in the generated TypeScript.
	Measured *bool   `json:"measured"`
	Remedy   *Remedy `json:"remedy,omitempty"`
}

// HealthReport is the whole page.
//
// A context not probed yet has no row, only a count, so `len(Checks)` is not how
// many clusters there are; the counts keep an unasked cluster from reading as a
// dead one.
type HealthReport struct {
	Checks []HealthCheck `json:"checks"`
	// Total is how many contexts the configuration declares, above len(Checks)
	// while a first round is in flight.
	Total    *int `json:"total,omitempty"`
	Probed   *int `json:"probed,omitempty"`
	Answered *int `json:"answered,omitempty"`
	// Pending is how many contexts have not been asked yet.
	Pending *int `json:"pending,omitempty"`
	// Refreshing says a round is running now, so "nothing yet" does not read as
	// given up.
	Refreshing *bool `json:"refreshing,omitempty"`
	// Headline is composed by the server so it agrees with the counts beside it.
	Headline string         `json:"headline,omitempty"`
	Tone     string         `json:"tone,omitempty"`
	Counts   []HealthCount  `json:"counts,omitempty"`
	Notices  []HealthNotice `json:"notices,omitempty"`
	// SSO is the machine's AWS SSO sessions, for the home's session card. Nil
	// under the server profile, where no user's AWS CLI exists.
	SSO *HealthSSO `json:"sso,omitempty"`
}

// HealthSSO is what the AWS CLI's SSO cache holds, reduced to what a card says.
// Measured false is a cache that could not be read, so an empty Sessions then
// does not mean none. Names, states and expiries only: the cache files hold
// live tokens.
type HealthSSO struct {
	Measured bool `json:"measured"`
	// Problem is why it could not be read, without the path. Only on !Measured.
	Problem  string             `json:"problem,omitempty"`
	Sessions []HealthSSOSession `json:"sessions"`
}

// HealthSSOSession is one session: its name, whether it is signed in, and until
// when.
type HealthSSOSession struct {
	Name string `json:"name"`
	// State is `valid`, `renewable` or `expired` (internal/health's SSO states).
	State     string     `json:"state"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	// Command is what to type to sign in again, on `expired` only.
	Command string `json:"command,omitempty"`
}

// --- Readings, series, cells ------------------------------------------------

// Reading is a measurement and whether it can be believed: an untrustworthy
// reading drawn as a bare number would look entirely fine.
//
// Value is a pointer because JSON has no NaN or infinity; build one with
// ReadingOf.
type Reading struct {
	Value       *float64 `json:"value"`
	Trustworthy bool     `json:"trustworthy"`
	Text        string   `json:"text,omitempty"`
}

// Series is a run of samples at a fixed step. Gaps are null and never omitted:
// omitting one shifts every later sample onto the wrong moment.
type Series struct {
	StartedAt   time.Time  `json:"startedAt"`
	StepSeconds float64    `json:"stepSeconds"`
	Values      []*float64 `json:"values"`
	Trustworthy *bool      `json:"trustworthy,omitempty"`
}

// Cell is one cell of the grid. State and Reason are per cell: a usage column
// with no metrics source is `absent` in every row, and must not draw as 0.
type Cell struct {
	Text string `json:"text"`
	Sort any    `json:"sort,omitempty"`
	Tone string `json:"tone,omitempty"`
	// Rank is how urgently this cell wants reading; zero makes no claim. It
	// makes a worst-first ordering one comparison per row.
	Rank int `json:"rank,omitempty"`
	// Hint is what hovering says, where the cell states a number the reader
	// would look up (a node's taint count). Separate from Ref, which may carry an
	// address on the same cell.
	Hint string `json:"hint,omitempty"`
	// Copy is what a copy control puts on the clipboard where the drawn text is
	// not it, such as a cell drawn as three hosts and `+2 more`.
	Copy    string     `json:"copy,omitempty"`
	Ref     *Ref       `json:"ref,omitempty"`
	Parts   []CellPart `json:"parts,omitempty"`
	Reading *Reading   `json:"reading,omitempty"`
	// Gauge is the bar drawn beside the reading. Nil when nothing was measured:
	// an empty track would read as 0%, an idle machine.
	Gauge  *Gauge  `json:"gauge,omitempty"`
	State  State   `json:"state,omitempty"`
	Reason *Reason `json:"reason,omitempty"`
}

// Gauge is a bar beside a cell's reading, when the reading is a fraction of a
// known whole. Percent is clamped on the server, which owns what the whole is.
//
// Tone is separate from Cell.Tone: a node at 91% memory warrants a colored
// bar, not a row marked as a fault.
type Gauge struct {
	Percent float64 `json:"percent"`
	Tone    string  `json:"tone,omitempty"`
}

// CellPart is one piece of a cell holding several clickable things, such as an
// Ingress's hosts. A piece with no Href is plain text (a `*` host is not an
// address). Separators are pieces too: the pieces concatenate to exactly
// Cell.Text.
type CellPart struct {
	Text string `json:"text"`
	Href string `json:"href,omitempty"`
}

// Column is a grid heading.
//
// The behavioral flags are facts about the kind, not the cells at hand, so a
// client reserves room from the declaration rather than from the first answer.
// Secondary, Gauged, Copyable, Prose and Role carry a `default` in the contract,
// making them required in the generated TypeScript, so they are always
// marshalled.
type Column struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Visible bool   `json:"visible"`
	Order   int    `json:"order"`
	// Width is the operator's stored width in pixels, or null for automatic,
	// which the front end computes.
	Width    *int   `json:"width"`
	Align    string `json:"align,omitempty"`
	Sortable *bool  `json:"sortable,omitempty"`
	// Secondary means hidden by default. It travels beside Visible so a client
	// can tell "the operator hid this" from "off by default", which decides what
	// a reset means.
	Secondary bool `json:"secondary"`
	// Gauged says cells of this column draw a bar when there is a measurement.
	Gauged bool `json:"gauged"`
	// Copyable says cells of this column carry a copy control when they hold
	// something worth copying.
	Copyable bool `json:"copyable"`
	// Prose says this column holds a sentence and its cells may wrap.
	Prose bool `json:"prose"`
	// Role is what this column draws: "name", "namespace", "age" or "other", so
	// a client styles a cell by what it is rather than where it sits after a
	// reorder.
	Role string `json:"role"`
	// LockedNote is why this column cannot be hidden, where it cannot.
	LockedNote string `json:"lockedNote,omitempty"`
	// Filterable says the filter control offers this column (a node, a status),
	// and the list's `filter` parameter narrows by it.
	Filterable bool `json:"filterable"`
	// FilterValues is what the filter control offers for a Filterable column:
	// the values its cells hold across the rows the search and every other
	// column's filters leave, sorted. Absent on a column that is not Filterable;
	// `[]` on one whose rows were all filtered away. A pointer, because
	// `omitempty` drops an empty slice as it does a nil one.
	FilterValues *[]string `json:"filterValues,omitempty"`
}

// ListFilter is one filter in effect on a list: a column, by key, and the value
// its cells were required to equal.
type ListFilter struct {
	Column string `json:"column"`
	Value  string `json:"value"`
}

// Row is one object, keyed by column.
type Row struct {
	ID        string          `json:"id"`
	Name      string          `json:"name,omitempty"`
	Namespace string          `json:"namespace,omitempty"`
	Href      string          `json:"href,omitempty"`
	Cells     map[string]Cell `json:"cells"`
	// Unchanged is the list's bulk verbs that would change nothing on this
	// object: one half of a toggle it is already on (`suspend` on a suspended
	// CronJob, `cordon` on a cordoned node). The selection bar offers such a
	// verb only while a ticked row would be changed by it, and sends only
	// those rows. Absent where every verb would act.
	Unchanged []string `json:"unchanged,omitempty"`
}

// BulkVerb is one bulk action, already answered permitted or not.
type BulkVerb struct {
	Verb      string `json:"verb"`
	Label     string `json:"label"`
	Permitted bool   `json:"permitted"`
	// Destructive is the answer `DetailAction.Destructive` carries for the same
	// (verb, kind). Off the wire where the verb is ordinary.
	Destructive *bool   `json:"destructive,omitempty"`
	Reason      *Reason `json:"reason,omitempty"`
}

// The four values Column.Role may take; the contract's enum is closed.
const (
	RoleName      = "name"
	RoleNamespace = "namespace"
	RoleAge       = "age"
	RoleOther     = "other"
)

// CreateAffordance is whether the list's "+" may create an object of the
// list's kind, reviewed for the person in the namespace in view, or
// cluster-wide for a cluster-scoped kind. `Permitted: false` comes with the
// Reason. It is advice only: previewCreate reviews again.
type CreateAffordance struct {
	Permitted bool `json:"permitted"`
	// Namespace is the namespace the verdict is for and the template is filled
	// with. Empty on a cluster-scoped kind, or across several namespaces.
	Namespace string `json:"namespace,omitempty"`
	// Kind is the kind as discovery spells it, for the button's name. Empty
	// where it could not be resolved.
	Kind   string  `json:"kind,omitempty"`
	Reason *Reason `json:"reason,omitempty"`
}

// BulkAffordance is which bulk verbs this list offers.
type BulkAffordance struct {
	// Scope is the identity of the table this selection belongs to; a client
	// discards its selection whenever it changes. The server names it because
	// only it knows when the underlying set changed, and a selection carried to
	// another set would act on the wrong objects.
	Scope string     `json:"scope,omitempty"`
	Verbs []BulkVerb `json:"verbs"`
}

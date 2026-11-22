package report

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/textutil"
)

// The page, ready to draw: JSON for the front end, with every sentence decided
// on this side.

// View is the whole page.
type View struct {
	Collected string `json:"collected"`
	// State is absent, pending, present or unreadable.
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`

	Where            string `json:"where,omitempty"`
	WhereUnavailable string `json:"whereUnavailable,omitempty"`

	// Install is the command, on a cluster with no Argo CD only.
	Install string `json:"install,omitempty"`

	Summary  string `json:"summary,omitempty"`
	Headline string `json:"headline,omitempty"`
	// HeadlineKey is the word's key, for its color: "degraded", "deleting".
	HeadlineKey string `json:"headlineKey,omitempty"`
	AllWell     bool   `json:"allWell"`

	Sync   []Tile `json:"sync"`
	Health []Tile `json:"health"`

	Areas []AreaView `json:"areas"`

	OffendersUnavailable string `json:"offendersUnavailable,omitempty"`
	Offenders            []Row  `json:"offenders"`
	OffendersNote        string `json:"offendersNote,omitempty"`

	Causes     []CauseRow `json:"causes"`
	CausesNote string     `json:"causesNote,omitempty"`

	SetsUnavailable string   `json:"setsUnavailable,omitempty"`
	Sets            []SetRow `json:"sets"`
	SetsNote        string   `json:"setsNote,omitempty"`
	SetTotal        int      `json:"setTotal"`
	SetFailing      int      `json:"setFailing"`

	ProjectsUnavailable string       `json:"projectsUnavailable,omitempty"`
	Projects            []ProjectRow `json:"projects"`
	ProjectsNote        string       `json:"projectsNote,omitempty"`
	ProjectTotal        int          `json:"projectTotal"`

	Notes []string `json:"notes"`
}

// Tile is one word of a band, zeroes included.
type Tile struct {
	Label string `json:"label"`
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// AreaView is one kind under "What was read".
type AreaView struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Unavailable string `json:"unavailable,omitempty"`
	Count       int    `json:"count"`
	Summary     string `json:"summary,omitempty"`
	Note        string `json:"note,omitempty"`
}

// Row is one Application that is not both Healthy and Synced, worst first.
type Row struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Project is spec.project; ProjectNamespace is where that AppProject was
	// found, when it was, so the cell can open it. ProjectMessage says why not.
	Project          string `json:"project,omitempty"`
	ProjectNamespace string `json:"projectNamespace,omitempty"`
	ProjectMessage   string `json:"projectMessage,omitempty"`

	Sync          string `json:"sync"`
	SyncKey       string `json:"syncKey"`
	Health        string `json:"health"`
	HealthKey     string `json:"healthKey"`
	HealthMessage string `json:"healthMessage,omitempty"`
	Deleting      string `json:"deleting,omitempty"`

	Since []Since `json:"since"`

	Source     string `json:"source"`
	SourceHint string `json:"sourceHint,omitempty"`
	Revision   string `json:"revision"`

	Operation        string `json:"operation"`
	OperationTone    string `json:"operationTone"`
	OperationMessage string `json:"operationMessage,omitempty"`
	Deployed         string `json:"deployed,omitempty"`
	DeployedHint     string `json:"deployedHint,omitempty"`

	Conditions     []ConditionView `json:"conditions"`
	ConditionsNote string          `json:"conditionsNote,omitempty"`

	// Reason is why this row is where it is, worstFirst's own words.
	Reason string `json:"reason"`
}

// Since is one line of the "since" cell.
type Since struct {
	Of      string `json:"of"`
	Value   string `json:"value"`
	Hint    string `json:"hint"`
	Missing bool   `json:"missing"`
}

// ConditionView is one condition, in Argo's words.
type ConditionView struct {
	Type    string `json:"type"`
	Error   bool   `json:"error"`
	Message string `json:"message,omitempty"`
}

// CauseRow is one condition type and the Applications carrying it.
type CauseRow struct {
	Type        string    `json:"type"`
	Error       bool      `json:"error"`
	Message     string    `json:"message"`
	Failing     int       `json:"failing"`
	Objects     []Subject `json:"objects"`
	ObjectsNote string    `json:"objectsNote,omitempty"`
}

// SetRow is one failing ApplicationSet.
type SetRow struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Message   string `json:"message"`
}

// ProjectRow is one AppProject and what was counted against it.
type ProjectRow struct {
	Namespace    string `json:"namespace"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Applications int    `json:"applications"`
	Offending    int    `json:"offending"`
}

// sourceLimit is how much of a repository path reaches a table cell.
const sourceLimit = 44

// Missing is the console's em dash for a field the object does not carry.
const Missing = "—"

// ViewOf builds the page from the report. `now` is a parameter so a test can
// pin a duration without pinning a clock.
func ViewOf(report Report, now time.Time) View {
	v := View{
		Collected: report.Collected.UTC().Format(time.RFC3339),
		Detail:    report.Detail,
		Notes:     report.Notes,
		Sync:      tiles(report.Sync),
		Health:    tiles(report.Health),
		Areas:     []AreaView{},
		Offenders: []Row{},
		Causes:    []CauseRow{},
		Sets:      []SetRow{},
		Projects:  []ProjectRow{},
	}
	if v.Notes == nil {
		v.Notes = []string{}
	}

	switch report.State {
	case StateAbsent:
		v.State = "absent"
		// Only here. On a cluster that *has* Argo CD, an install command is an
		// invitation to reinstall something that is working.
		v.Install = InstallCommand
	case StatePending:
		v.State = "pending"
	case StateUnreadable:
		v.State = "unreadable"
	default:
		v.State = "present"
	}

	v.Where, v.WhereUnavailable = whereFound(report.Install)
	// A short count first, with the rule in brackets.
	v.Summary = strconv.Itoa(report.Offending) + " of " + plural(report.Applications, "Application", "Applications") +
		" " + needs(report.Offending) + " attention (not both Healthy and Synced)"
	v.Headline, v.HeadlineKey = headline(report)
	// The green pill is a claim about the whole page, so it is gated on the
	// whole page: nothing offending, nothing being deleted, and no headline.
	v.AllWell = v.State == "present" && report.Offending == 0 && report.Deleting == 0 && v.Headline == ""

	for _, a := range report.Areas {
		area := AreaView{Name: a.Name, Kind: a.Kind, Unavailable: a.Unavailable, Count: a.Objects, Note: a.Note}
		if a.Unavailable == "" {
			area.Summary = plural(a.Objects, "object", "objects") + " read"
		}
		v.Areas = append(v.Areas, area)
	}

	v.OffendersUnavailable = unavailableOf(report.Areas, ApplicationKind)
	v.SetsUnavailable = unavailableOf(report.Areas, ApplicationSetKind)
	v.ProjectsUnavailable = unavailableOf(report.Areas, AppProjectKind)

	projects := indexProjects(report)
	v.OffendersNote = report.OffendersNote
	for _, a := range report.Offenders {
		v.Offenders = append(v.Offenders, rowOf(a, projects, now))
	}

	v.CausesNote = report.CausesNote
	for _, c := range report.Causes {
		objects := c.Objects
		if objects == nil {
			objects = []Subject{}
		}
		v.Causes = append(v.Causes, CauseRow{
			Type: c.Type, Error: c.Error, Message: c.Message, Failing: c.Failing,
			Objects: objects, ObjectsNote: c.ObjectsNote,
		})
	}

	v.SetsNote, v.SetTotal, v.SetFailing = report.SetsNote, report.SetTotal, report.SetFailing
	for _, s := range report.Sets {
		v.Sets = append(v.Sets, SetRow{Namespace: s.Namespace, Name: s.Name, Type: s.Type, Status: s.Status, Message: s.Message})
	}
	v.ProjectsNote, v.ProjectTotal = report.ProjectsNote, report.ProjectTotal
	for _, p := range report.Projects {
		v.Projects = append(v.Projects, ProjectRow{
			Namespace: p.Namespace, Name: p.Name, Description: p.Description,
			Applications: p.Applications, Offending: p.Offending,
		})
	}
	return v
}

func tiles(all []Tally) []Tile {
	out := make([]Tile, 0, len(all))
	for _, t := range all {
		out = append(out, Tile{Label: t.Label, Key: t.Key, Count: t.Count})
	}
	return out
}

// projectIndex records where each AppProject this page read lives, so an
// offender row can link to it; the Application's own namespace is not where
// the AppProject is once `application.namespaces` is on.
type projectIndex struct {
	at     map[string]string
	shared map[string]bool
	// read is whether every AppProject the cluster has was read, which decides
	// the wording when a name is not in the index: missing, or not read.
	read bool
}

func indexProjects(report Report) projectIndex {
	idx := projectIndex{at: map[string]string{}, shared: map[string]bool{}}
	for _, p := range report.Projects {
		if at, seen := idx.at[p.Name]; seen && at != p.Namespace {
			idx.shared[p.Name] = true
		}
		idx.at[p.Name] = p.Namespace
		idx.at[p.Namespace+"/"+p.Name] = p.Namespace
	}
	idx.read = unavailableOf(report.Areas, AppProjectKind) == "" &&
		noteOf(report.Areas, AppProjectKind) == "" &&
		report.ProjectsNote == "" &&
		len(report.Projects) == report.ProjectTotal
	return idx
}

func (idx projectIndex) resolve(appNamespace, project string) (string, bool) {
	if project == "" {
		return "", false
	}
	if !idx.shared[project] {
		namespace, found := idx.at[project]
		return namespace, found
	}
	namespace, found := idx.at[appNamespace+"/"+project]
	return namespace, found
}

func rowOf(a Application, projects projectIndex, now time.Time) Row {
	row := Row{
		Namespace:        a.Namespace,
		Name:             a.Name,
		Project:          a.Project,
		Sync:             a.Sync,
		SyncKey:          key(a.Sync),
		Health:           a.Health,
		HealthKey:        key(a.Health),
		HealthMessage:    a.HealthMessage,
		Deleting:         deletingFor(a, now),
		Since:            sinceLines(a, now),
		Source:           sourceOf(a),
		SourceHint:       sourceHint(a),
		Revision:         dash(a.Revision),
		Operation:        operationText(a.Operation, now),
		OperationTone:    operationTone(a.Operation),
		OperationMessage: a.Operation.Message,
		ConditionsNote:   a.ConditionsNote,
		Reason:           a.Reason,
		Conditions:       []ConditionView{},
	}
	row.Deployed, row.DeployedHint = deployedFor(a, now)

	switch namespace, found := projects.resolve(a.Namespace, a.Project); {
	case a.Project == "":
		// Argo reads no spec.project as "default"; the cell prints the em dash
		// rather than inventing the word the object does not carry.
	case found:
		row.ProjectNamespace = namespace
	case projects.read:
		row.ProjectMessage = "no AppProject of this name exists in this cluster"
	default:
		row.ProjectMessage = "not among the AppProjects this page read"
	}

	for _, c := range a.Conditions {
		row.Conditions = append(row.Conditions, ConditionView{Type: c.Type, Error: c.Error(), Message: c.Message})
	}
	return row
}

// deletingFor words the deletion, with how long it has been going: a deletion
// three seconds old is Kubernetes working, one three days old is a finalizer
// that will not return.
func deletingFor(a Application, now time.Time) string {
	if !a.Deleting {
		return ""
	}
	if a.DeletingSince.IsZero() {
		return "being deleted"
	}
	return "being deleted, " + shortDuration(now.Sub(a.DeletingSince))
}

// sinceLines answers "when did this start", one subject at a time, and never
// synthesizes a transition time out of data that does not record one: health
// has one (status.health.lastTransitionTime), sync has none, and reconciledAt
// is how old the readings are, labeled "as of".
func sinceLines(a Application, now time.Time) []Since {
	lines := make([]Since, 0, 3)
	switch {
	case a.HealthSinceStamped && !a.HealthSince.IsZero():
		lines = append(lines, Since{
			Of:    "health",
			Value: shortDuration(now.Sub(a.HealthSince)),
			Hint: "Argo recorded the health of this Application becoming " + a.Health + " at " +
				a.HealthSince.UTC().Format("2006-01-02 15:04:05 UTC") +
				". status.health.lastTransitionTime is written only when the health word changes.",
		})
	case a.HealthSinceStamped:
		lines = append(lines, Since{
			Of: "health", Value: "recorded, unreadable", Missing: true,
			Hint: "Argo wrote a status.health.lastTransitionTime this console could not parse as " +
				"RFC3339, so the fact that it recorded one is kept and the duration is not.",
		})
	case a.Health == Unreported:
		lines = append(lines, Since{
			Of: "health", Value: "not assessed yet", Missing: true,
			Hint: "Argo has written no status.health.status for this Application at all, so there " +
				"is no transition to time.",
		})
	default:
		lines = append(lines, Since{
			Of: "health", Value: "not recorded", Missing: true,
			Hint: "This Application carries no status.health.lastTransitionTime. Argo writes it the " +
				"first time the health word changes, and versions of Argo CD before 2.13 never wrote it.",
		})
	}

	if a.Sync != "Synced" {
		lines = append(lines, Since{
			Of: "sync", Value: SyncSinceUnrecorded, Missing: true,
			Hint: "status.sync holds a status, a revision and comparedTo, and no timestamp of any " +
				"kind - so nothing in this object says when this Application went " + a.Sync + ". " +
				"Argo emits a Kubernetes event when it changes; the Application's own page in the " +
				"console shows the events Kubernetes still holds.",
		})
	}

	if a.ReconciledAt.IsZero() {
		return append(lines, Since{
			Of: "as of", Value: "not recorded", Missing: true,
			Hint: "This Application carries no readable status.reconciledAt, so nothing here says " +
				"how old the readings on this row are.",
		})
	}
	return append(lines, Since{
		Of:    "as of",
		Value: shortDuration(now.Sub(a.ReconciledAt)) + " ago",
		Hint: "status.reconciledAt: Argo last compared this Application against its source at " +
			a.ReconciledAt.UTC().Format("2006-01-02 15:04:05 UTC") + ". It moves whether or not " +
			"anything changed, so it is how old the readings on this row are and never when " +
			"anything started.",
	})
}

// deployedFor is what Argo last actually put on the cluster, drawn only when it
// says something the operation does not: after a failed sync, what is running is
// still whatever was deployed before.
func deployedFor(a Application, now time.Time) (string, string) {
	d := a.Deployment
	if !d.Known {
		return "", ""
	}
	if a.Operation.Known() && !a.Operation.Failed() && a.Operation.Revision == d.Revision {
		return "", ""
	}
	text := "deployed"
	if !d.At.IsZero() {
		text += " " + shortDuration(now.Sub(d.At)) + " ago"
	}
	if d.Revision != "" {
		text += " → " + d.Revision
	}
	if d.By != "" {
		text += ", by " + d.By
	}
	hint := "The last entry of status.history: what Argo CD last successfully put on this cluster. " +
		"A sync that fails does not append to it, so this can be older than the operation above."
	switch {
	case d.TrimmedKnown && d.Trimmed > 0:
		hint += fmt.Sprintf(" status.history holds the last %d of the %d deployments Argo has made here; "+
			"the earlier ones have been trimmed by revisionHistoryLimit.", d.Kept, d.Kept+d.Trimmed)
	case d.TrimmedKnown:
		hint += fmt.Sprintf(" status.history holds all %d deployments Argo has made here.", d.Kept)
	default:
		hint += fmt.Sprintf(" status.history holds %d entries and does not number them, so how many "+
			"earlier deployments have been trimmed is not knowable from this object.", d.Kept)
	}
	return text, hint
}

// headline is the worst thing on the page, in one pill: being deleted, then the
// worst health word, then the worst sync word, then anything else offending.
func headline(report Report) (string, string) {
	if report.Deleting > 0 {
		return plural(report.Deleting, "Application", "Applications") + " being deleted", "deleting"
	}
	if worst := worstTile(report.Health, HealthWorstFirst(), "Healthy"); worst.Count > 0 {
		return strconv.Itoa(worst.Count) + " " + worst.Label, worst.Key
	}
	if worst := worstTile(report.Sync, SyncWorstFirst(), "Synced"); worst.Count > 0 {
		return strconv.Itoa(worst.Count) + " " + worst.Label, worst.Key
	}
	if report.Offending > 0 {
		return plural(report.Offending, "Application", "Applications") + " needing attention", "conderror"
	}
	return "", ""
}

func worstTile(all []Tally, trouble []string, good string) Tally {
	for _, want := range trouble {
		for _, t := range all {
			if t.Label == want && t.Count > 0 {
				return t
			}
		}
	}
	ranked := map[string]bool{good: true}
	for _, w := range trouble {
		ranked[w] = true
	}
	for _, t := range all {
		if !ranked[t.Label] && t.Count > 0 {
			return t
		}
	}
	return Tally{}
}

func whereFound(install Install) (string, string) {
	if !install.Found() {
		return "", install.Unavailable
	}
	return install.How + " " + strings.Join(install.Namespaces, ", ") + ".", install.Unavailable
}

func unavailableOf(areas []Area, kind string) string {
	for _, a := range areas {
		if a.Kind == kind {
			return a.Unavailable
		}
	}
	return ""
}

func noteOf(areas []Area, kind string) string {
	for _, a := range areas {
		if a.Kind == kind {
			return a.Note
		}
	}
	return ""
}

// operationText words the last sync: Argo's phase, who asked and when. "never
// synced" is a stated answer and not a blank cell.
func operationText(op Operation, now time.Time) string {
	if !op.Known() {
		return "never synced"
	}
	out := op.Phase
	if op.By != "" {
		out += ", by " + op.By
	}
	if !op.At.IsZero() {
		when := shortDuration(now.Sub(op.At)) + " ago"
		if !op.Finished {
			when = "started " + when
		}
		out += ", " + when
	}
	return out
}

func operationTone(op Operation) string {
	switch {
	case op.Failed():
		return "bad"
	case op.Phase == "Succeeded":
		return "ok"
	default:
		// Including "never synced", which is not a success.
		return "muted"
	}
}

func sourceOf(a Application) string {
	text := a.Path
	if text == "" {
		text = a.RepoURL
	}
	if text == "" {
		return Missing
	}
	text = textutil.Clip(text, sourceLimit)
	if a.Sources > 1 {
		text += " +" + strconv.Itoa(a.Sources-1)
	}
	return text
}

func sourceHint(a Application) string {
	parts := make([]string, 0, 4)
	if a.RepoURL != "" {
		parts = append(parts, a.RepoURL)
	}
	if a.Path != "" {
		parts = append(parts, a.Path)
	}
	if a.TargetRevision != "" {
		parts = append(parts, "target "+a.TargetRevision)
	}
	if a.Sources > 1 {
		parts = append(parts, strconv.Itoa(a.Sources)+" sources")
	}
	return strings.Join(parts, " · ")
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return Missing
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// shortDuration is the console's `resources.ShortDuration`: the largest unit
// that fits, and never a negative (a timestamp in the future is a clock
// disagreement, not an event from tomorrow).
func shortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func needs(n int) string {
	if n == 1 {
		return "needs"
	}
	return "need"
}

package web

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/helm"
	"github.com/thonicdev/kubetower/internal/resources"
)

func (r apiReads) HelmReleases(ctx context.Context, name string, namespaces []string) (api.HelmReleaseReading, *api.Failure) {
	// The selection travels whole: `helm.Releases` takes a slice and the
	// contract's parameter is repeatable.
	selection := make([]string, 0, len(namespaces))
	for _, namespace := range namespaces {
		if namespace != "" {
			selection = append(selection, namespace)
		}
	}
	// The scope names the selection as asked, so an empty answer says "in argocd and
	// ingress" rather than "on this cluster".
	scope := api.Scope{Cluster: name, Namespace: strings.Join(selection, ","), Resource: "helm"}
	accessNote := ""
	read := selection
	if len(selection) == 0 {
		narrowed, fail := r.helmNamespaces(ctx, name)
		if fail != nil {
			return api.HelmReleaseReading{}, fail.Within(scope)
		}
		if narrowed != nil {
			read = narrowed
			accessNote = "You may not list Secrets in every namespace, and a Helm release is stored as one, " +
				"so this shows the releases of the namespaces where you may."
		}
	}
	report := r.s.helm.Releases(ctx, name, read)
	switch report.State {
	case helm.StateUnreachable:
		return api.HelmReleaseReading{}, api.Unreachable(report.Detail).Within(scope)
	case helm.StateUnreadable:
		return api.HelmReleaseReading{}, api.Unreadable(report.Detail).Within(scope)
	case helm.StateUndecodable:
		return api.HelmReleaseReading{}, api.Fail(api.StateUnreadable, api.CodeUndecodable, report.Detail).Within(scope)
	}
	out := make([]api.HelmRelease, 0, len(report.Releases)+len(report.Undecoded))
	for _, release := range report.Releases {
		out = append(out, helmReleaseOf(release))
	}
	// A release whose payload could not be decoded is listed as unreadable, never
	// dropped: it exists.
	for _, bad := range report.Undecoded {
		reason := api.Because(api.CodeUndecodable, bad.Reason)
		out = append(out, api.HelmRelease{
			Name: bad.Secret, Namespace: bad.Namespace, State: api.StateUnreadable, Reason: &reason,
		})
	}
	// The arrangement the column store keeps for the list, under `helm`, as a
	// resource list's comes back with its rows: the same store, the same routes
	// (`/api/preferences/columns/*` with kind `helm`) and the same reading.
	arrangement := r.s.columns.Arrangement(helmKind, titlesOf(helmKind))
	return api.HelmReleaseReading{
		Releases:          out,
		AccessNote:        accessNote,
		Columns:           arrangedColumns(helmColumns, arrangement, nil, false),
		ColumnsCustomised: arrangement.Custom,
	}, nil
}

// helmKind is the name the column store files the Helm list's arrangement
// under, and the `kind` its column writes carry. Not a kind internal/resources
// lists - a release is a Secret's payload - so declaredColumns answers for it.
const helmKind = "helm"

// helmColumns is what the Helm list draws, in its default order. The client
// draws each cell; what is declared here is what the store and the column
// menu need: the titles, which one is a figure, and which ones the filter
// control offers - a status and a chart are categories, a name, a version, a
// date and a sentence are not. The namespace is the namespace menu's, as on a
// resource list.
var helmColumns = []resources.Column{
	{Title: "Name"},
	{Title: "Namespace"},
	{Title: "Revision", Numeric: true},
	{Title: "Status", Filterable: true},
	{Title: "Chart", Filterable: true},
	{Title: "Chart version"},
	{Title: "App version"},
	{Title: "Updated"},
	{Title: "Description"},
}

// declaredColumns is the column list a kind declares: the descriptor's for a
// resource kind, helmColumns for the Helm list.
func declaredColumns(kind string) []resources.Column {
	if kind == helmKind {
		return helmColumns
	}
	return resources.ColumnsOf(kind)
}

// helmNamespaces is where the release page reads for a person who chose no
// namespace and may not list Secrets across the cluster: the namespaces where
// they may. nil, and no failure, where they may list everywhere, or where the set
// cannot be established or is too large to read one namespace at a time
// (internal/helm then refuses and asks for a namespace). A person who may list
// Secrets nowhere is refused.
func (r apiReads) helmNamespaces(ctx context.Context, name string) ([]string, *api.Failure) {
	client, fail := r.client(name)
	if fail != nil {
		return nil, nil
	}
	wide := access.Attributes{Verb: "list", Version: "v1", Resource: "secrets"}
	if err := r.s.gate.Check(ctx, client.Context, wide); !errors.Is(err, access.ErrDenied) {
		return nil, nil
	}
	held := r.s.namespaceAnswer(ctx, name)
	if held.state() != namespacesListed {
		return nil, nil
	}
	allowed, complete, err := r.s.gate.ListableIn(ctx, client.Context, wide, held.names)
	if err != nil {
		return nil, stateOf(err, api.Scope{Cluster: name, Resource: "secrets", Verb: "list"})
	}
	if !complete || len(allowed) > access.NarrowedReads {
		return nil, nil
	}
	return allowed, nil
}

// helmReleaseOf copies every field of the row, including the chart version, the
// deprecation flag, Helm's sentence about the last operation and `age`.
func helmReleaseOf(release helm.Release) api.HelmRelease {
	out := api.HelmRelease{
		Name:             release.Name,
		Namespace:        release.Namespace,
		Revision:         release.Revision,
		DeployedRevision: release.DeployedRevision,
		Chart:            release.Chart,
		ChartVersion:     release.ChartVersion,
		AppVersion:       release.AppVersion,
		Deprecated:       release.Deprecated,
		Description:      release.Description,
		Status:           release.Status,
		State:            api.StateOK,
	}
	if !release.Updated.IsZero() {
		updated := release.Updated
		out.UpdatedAt = &updated
		// `internal/helm` ages every row from its answer's own collection instant; this
		// only carries it.
		out.Age = release.Age
	}
	// On the row, drawn under the status it contradicts: `failed` alone reads as a
	// release with nothing running.
	if release.DeployedRevision != 0 && release.DeployedRevision != release.Revision {
		out.Note = "Running: revision " + strconv.Itoa(release.DeployedRevision) +
			" is the one Helm marks deployed"
	}
	return out
}

// helmRevisionOf is one entry of the history. `Mark` folds `Current` and
// `Deployed` into one three-state value, since they are only read together.
func helmRevisionOf(revision helm.Revision) api.HelmRevision {
	out := api.HelmRevision{
		Revision:     revision.Revision,
		Status:       revision.Status,
		Tone:         helmStatusTone(revision.Status),
		ChartVersion: revision.ChartVersion,
		AppVersion:   revision.AppVersion,
		Description:  revision.Description,
		Note:         revision.Note,
	}
	if !revision.Updated.IsZero() {
		updated := revision.Updated
		out.UpdatedAt = &updated
	}
	switch {
	case revision.Current && revision.Deployed:
		out.Mark = "current-and-deployed"
	case revision.Current:
		out.Mark = "current"
	case revision.Deployed:
		out.Mark = "deployed"
	}
	return out
}

// helmStatusTone grades Helm's own word for a revision; the word is never
// renamed. Pending states are amber: neither worked nor failed.
func helmStatusTone(status string) string {
	switch status {
	case "deployed":
		return "ok"
	case "failed", "unknown":
		return "bad"
	case "pending-install", "pending-upgrade", "pending-rollback", "uninstalling":
		return "warn"
	case "superseded", "uninstalled":
		return "muted"
	}
	return ""
}

func (r apiReads) HelmRelease(ctx context.Context, name, namespace, release string) (api.HelmReleaseDetail, *api.Failure) {
	history := r.s.helm.History(ctx, name, namespace, release)
	scope := api.Scope{Cluster: name, Namespace: namespace, Name: release, Resource: "helm"}
	switch history.State {
	case helm.StateUnreachable:
		return api.HelmReleaseDetail{}, api.Unreachable(history.Detail).Within(scope)
	case helm.StateUnreadable:
		return api.HelmReleaseDetail{}, api.Unreadable(history.Detail).Within(scope)
	case helm.StateEmpty:
		return api.HelmReleaseDetail{}, api.Absent("no such release").Within(scope)
	case helm.StateUndecodable:
		return api.HelmReleaseDetail{}, api.Fail(api.StateUnreadable, api.CodeUndecodable, history.Detail).Within(scope)
	}
	out := api.HelmReleaseDetail{Release: helmReleaseOf(history.Release)}
	for _, revision := range history.Revisions {
		out.Revisions = append(out.Revisions, helmRevisionOf(revision))
	}
	// Sent even when zero: keeping no revisions is a real state, not "unknown".
	kept := len(history.Revisions)
	out.HistoryMax = &kept
	// Only whether there are values and notes, never their content: values carry
	// passwords and charts print generated ones in their notes. Both are reveals
	// (RevealHelmValues, RevealHelmNotes), reviewed and recorded.
	// reveals: RevealHelmValues and RevealHelmNotes.
	out.ValuesSupplied = history.Values != ""
	out.NotesRendered = history.Notes != ""
	out.PayloadNote = history.PayloadNote
	return out, nil
}

// RevealHelmValues is the one door a release's supplied values reach a view
// through: revealHelm, under the key `values`.
func (r apiReads) RevealHelmValues(ctx context.Context, q api.HelmRevealQuery) (api.HelmValues, *api.Failure) {
	var out api.HelmValues
	fail := r.revealHelm(ctx, q, "values", func(history helm.History) {
		out = api.HelmValues{Revision: q.Revision, Values: history.Values}
	})
	if fail != nil {
		return api.HelmValues{}, fail
	}
	return out, nil
}

// RevealHelmNotes is the one door a release's notes reach a view through:
// revealHelm, under the key `notes`. Charts print generated passwords in
// `NOTES.txt`, so the notes get the values' review and record. Not redacted.
func (r apiReads) RevealHelmNotes(ctx context.Context, q api.HelmRevealQuery) (api.HelmNotes, *api.Failure) {
	var out api.HelmNotes
	fail := r.revealHelm(ctx, q, "notes", func(history helm.History) {
		out = api.HelmNotes{Revision: q.Revision, Notes: history.Notes}
	})
	if fail != nil {
		return api.HelmNotes{}, fail
	}
	return out, nil
}

// revealHelm is the door both revealed parts go through, RevealEnv's door: `key`
// names the part in the record and the line; `take` copies it out of a history
// helmRevealable has accepted.
//
//   - Reviewed for the person before anything is read: `get` on the Secret Helm
//     stores the revision in (`sh.helm.release.v1.<release>.v<n>`), by name, so
//     a Role naming one release's Secrets is honored exactly.
//   - Recorded: the attempt before the read and the outcome after, on every
//     path; an attempt that cannot be written refuses the reveal.
//   - A `reveal:` line in the process log, in the env reveal's shape.
//
// The revision is the one the page was drawn from; a release that has moved on
// answers `absent` rather than a newer revision's part.
func (r apiReads) revealHelm(ctx context.Context, q api.HelmRevealQuery, key string, take func(helm.History)) *api.Failure {
	scope := api.Scope{Cluster: q.Cluster, Namespace: q.Namespace, Name: q.Name, Resource: "helm"}
	asked := api.EnvQuery{
		ObjectRef: api.ObjectRef{Cluster: q.Cluster, Kind: "secrets", Namespace: q.Namespace,
			Name: api.HelmRevisionSecret(q.Name, q.Revision)},
		Key: key,
	}
	where, located, decision, fail := r.s.mayReveal(ctx, asked, scope)
	if !located {
		r.s.log("%s", revealLine(ctx, asked, decision, fail))
		return fail
	}
	record := r.s.gate.Record(ctx, audit.Reveal, where.Cluster, revealAttributes(where, asked), decision)
	record.Key = asked.Key
	pending, unrecorded := r.s.gate.Begin(ctx, record)
	if fail != nil {
		r.s.gate.End(ctx, pending, audit.Refused, "")
		r.s.log("%s", revealLine(ctx, asked, decision, fail))
		return fail
	}
	if unrecorded != nil {
		fail := api.Fail(api.StateUnreadable, api.CodeAccessUnreviewable, unrecorded.Error()).Within(scope).Retryable()
		r.s.log("%s", revealLine(ctx, asked, decision, fail))
		return fail
	}
	history := r.s.helm.History(ctx, q.Cluster, q.Namespace, q.Name)
	fail = helmRevealable(history, q.Revision, scope)
	// One outcome and one line on every path; the reason is the failure's code,
	// never its message.
	if fail != nil {
		r.s.gate.End(ctx, pending, audit.Failed, string(fail.Reason.Code))
	} else {
		take(history)
		r.s.gate.End(ctx, pending, audit.Applied, "")
	}
	r.s.log("%s", revealLine(ctx, asked, access.Allowed, fail))
	return fail
}

// helmRevealable is what the door says about a history it has read: nil when
// the revision asked for is the current one and its document was read, or why
// not. A pure function of the history, so every answer is reachable without a
// cluster. Nil with an empty part is an answer - the chart ships no notes, no
// values were supplied - and differs from PayloadNote's `unreadable`.
func helmRevealable(history helm.History, revision int, scope api.Scope) *api.Failure {
	switch history.State {
	case helm.StateUnreachable:
		return api.Unreachable(history.Detail).Within(scope)
	case helm.StateUnreadable:
		return api.Unreadable(history.Detail).Within(scope)
	case helm.StateEmpty:
		return api.Absent("no such release").Within(scope)
	case helm.StateUndecodable:
		return api.Fail(api.StateUnreadable, api.CodeUndecodable, history.Detail).Within(scope)
	}
	if history.Release.Revision != revision {
		return api.Absent(fmt.Sprintf("revision %d is no longer the current one: the release "+
			"is at revision %d now, so reload the page to see what it carries", revision,
			history.Release.Revision)).Within(scope)
	}
	if history.PayloadNote != "" {
		return api.Fail(api.StateUnreadable, api.CodeUndecodable, history.PayloadNote).Within(scope)
	}
	return nil
}

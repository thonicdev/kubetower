// Package helm answers one question about a cluster: what is installed on it
// with Helm, at which chart version, and at which revision.
//
// It reads Helm's own storage through client-go and nothing else: every fact
// on the page is read from the release document Helm wrote, in Helm's own
// spelling, and nothing shells out (the desktop binary has no `helm`).
//
// It is a core page, not an extra: nearly every cluster has releases, and a
// release is an ordinary Secret rather than a CRD.
//
// # Where the data is
//
// decode.go carries the storage format. One revision of a large chart is
// megabytes decoded, and Helm keeps ten by default, which shapes both reads:
//
//   - The list narrows to the revisions that can be current, with the label
//     selector `owner=helm,status notin (superseded)`, an order of magnitude
//     less data. This is sound because Helm creates the new revision before it
//     marks the old one superseded (on upgrade and rollback alike), so an
//     existing release always has a non-superseded revision. Only hand-edited
//     labels defeat it, and the release's own page reads every revision.
//
//   - One release's page reads all of its revisions: the history is what an
//     operator opens it for, and each revision's chart version lives only in
//     its own document. Bounded by count and bytes, and the page says when a
//     bound bites.
//
// # Four absences, four sentences
//
// An empty answer and a failed one are different answers. This page can have
// no rows because the cluster runs no Helm release, because this console may
// not list Secrets, because the cluster could not be asked, or because release
// Secrets were found and could not be read. Drawn as one empty table, they
// would tell an operator a cluster runs nothing when it could not be read.
//
// # Read-only
//
// Nothing here writes or offers to: no rollback, no uninstall. `internal/edit`
// is the console's one guarded write door, and a Helm rollback is not a
// manifest edit.
package helm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/resources"
	"github.com/thonicdev/kubetower/textutil"
)

// What Helm writes on every release Secret. The list selector, the history
// selector and the fold must agree on these; a typo finds nothing, silently.
const (
	// ReleaseType is the Secret's `type`, also usable as a field selector on
	// Secrets: a second server-side narrowing.
	ReleaseType = "helm.sh/release.v1"

	ownerLabel   = "owner"
	ownerHelm    = "helm"
	nameLabel    = "name"
	versionLabel = "version"
	statusLabel  = "status"

	// StatusSuperseded is the one status this package reasons about by name;
	// every other status word is carried through untouched (Release.Status).
	StatusSuperseded = "superseded"
	// StatusDeployed is named only so the page can say when the newest
	// revision is not the deployed one (Release.DeployedRevision).
	StatusDeployed = "deployed"
)

// The bounds. Each one is printed on the page when it bites: a capped list
// that does not admit it is a lie the operator acts on.
const (
	// pageSize is how many Secrets one API request asks for: one page's bodies
	// in memory at a time, one round trip on an ordinary cluster.
	pageSize = 50

	// releaseLimit is how many release Secrets the list reads: a ceiling on a
	// page's cost, not a display limit.
	releaseLimit = 300

	// revisionLimit is how many revisions one release's page reads: ten times
	// Helm's default retention, and still bounded when retention is off.
	revisionLimit = 100

	// byteBudget, in bytes, is the bound that protects the process: a count
	// says nothing about size. Both are reported when either bites.
	byteBudget = 64 << 20
)

// State is which of the answers this cluster gives: four absences and one
// presence. StateUndecodable is kept apart from StateEmpty because "nothing
// is installed with Helm" is a conclusion an operator would act on.
type State int

const (
	// StateUnreachable means the cluster could not be asked. Never drawn as a
	// cluster with nothing on it.
	StateUnreachable State = iota
	// StateUnreadable means the cluster refused, as for an identity without
	// `list` on `secrets`.
	StateUnreadable
	// StateEmpty means the Secrets were listed and none is a Helm release.
	StateEmpty
	// StateUndecodable means release Secrets were found and none could be read.
	StateUndecodable
	// StatePresent means at least one release was read; some may still have
	// failed (Report.Note).
	StatePresent
)

// Release is one Helm release, folded from every revision Helm kept of it.
//
// The fields describe the current revision: the highest number, not the
// `deployed` one, matching `helm list`, so a failed last upgrade reads
// `failed` here as there. DeployedRevision then says which revision runs.
type Release struct {
	Name      string
	Namespace string
	// Revision is the current one, the highest Helm kept.
	Revision int
	// Status is Helm's own word for that revision (deployed, failed,
	// pending-upgrade, ...), never renamed or re-bucketed; a caller may choose
	// a color for it and nothing else.
	Status string
	// Chart, ChartVersion and AppVersion are the chart's own metadata. The
	// chart version and the application version are different numbers and are
	// kept apart.
	Chart        string
	ChartVersion string
	AppVersion   string
	// Deprecated is the chart's own `deprecated:` flag.
	Deprecated bool
	// Updated is `info.last_deployed` of the current revision; FirstDeployed is
	// `info.first_deployed`, the first install, which survives upgrades.
	Updated       time.Time
	FirstDeployed time.Time
	// Age is Updated in the server's wording ("4m", "3d"), measured from the
	// report's collection instant and empty when Helm recorded no time. Written
	// by the server so every age it sends shares one clock and one scale; a
	// snapshot that the client carries forward.
	Age string
	// Description is Helm's sentence about the current revision ("Upgrade
	// complete", or the failure). Prose: its cell must be allowed to wrap.
	Description string
	// Revisions is how many revisions were seen: on the list, the
	// non-superseded ones (normally one, two after a failed upgrade); on a
	// release's own page, every one Helm kept.
	Revisions int
	// DeployedRevision is the `deployed` revision when it is not the current
	// one, and zero otherwise. After a failed upgrade the row shows both the
	// failure and what is still running.
	DeployedRevision int
}

// Revision is one entry of a release's history.
type Revision struct {
	Revision     int
	Status       string
	Updated      time.Time
	ChartVersion string
	AppVersion   string
	Description  string
	// Current marks the revision the release folds onto, the highest one.
	Current bool
	// Deployed marks the revision whose status is `deployed`; it differs from
	// Current after a failed upgrade.
	Deployed bool
	// Note says why this revision has no chart version or description: its
	// Secret would not decode and only its labels were read.
	Note string
}

// Undecoded is one release Secret that could not be read. Listed by name, not
// counted, because the name is what an operator can act on.
type Undecoded struct {
	Namespace string
	Secret    string
	Reason    string
}

// Report is one cluster's answer.
type Report struct {
	Cluster   string
	State     State
	Collected time.Time
	// Detail is the sentence for the state, empty when there are rows.
	Detail string
	// Note is what happened on the way to an otherwise good answer (a bound
	// that bit, some Secrets undecoded), drawn beside the table.
	Note string
	// Scope is the namespace selection read, canonicalized by the caller;
	// empty means every namespace. It lets the empty sentence say "in argocd"
	// rather than "on this cluster".
	Scope     []string
	Releases  []Release
	Undecoded []Undecoded
}

// History is one release's own page: the release as the list draws it, and
// every revision Helm kept.
type History struct {
	Cluster   string
	Namespace string
	Name      string
	State     State
	Collected time.Time
	Detail    string
	Note      string
	// Release is the fold, present only in StatePresent.
	Release Release
	// Revisions are newest first: "what changed" is about the top.
	Revisions []Revision
	Undecoded []Undecoded

	// Notes is Helm's rendering of the chart's `NOTES.txt` at the current
	// revision. Empty means the chart ships none, a real answer.
	Notes string
	// Values is the supplied values as YAML (`helm get values` without
	// `--all`), not the merged result. Empty means none were supplied.
	Values string
	// PayloadNote says why Notes and Values are not an answer at all: the
	// current revision's document would not decode. Empty whenever they were
	// read, so "no values" is never drawn over a revision nobody could read.
	PayloadNote string
}

// Clusters is what this package needs of the cluster manager: one client, by
// context name. An interface so tests can supply a fake API server.
type Clusters interface {
	Client(context string) (*cluster.Client, error)
}

// Reader reads Helm releases. There is no cache: the only thing worth caching
// is the release documents, the largest objects in the cluster.
//
// A release is a Secret, so it is reviewed as one: `list secrets` for the
// person behind the request, in the namespaces read. Someone who may not list
// Secrets there is not shown the releases, nor the values inside them.
type Reader struct {
	clusters Clusters
	gate     *access.Gate
}

// NewReader builds a Reader. A nil gate refuses every read.
func NewReader(clusters Clusters, gate *access.Gate) *Reader {
	return &Reader{clusters: clusters, gate: gate}
}

// labelValue is the shape of a Kubernetes label value. A release name comes
// from a URL and becomes half of a label selector, so it is checked rather
// than escaped: a half-valid value would match something else and draw
// another release's history under this name.
var labelValue = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]{0,61}[A-Za-z0-9])?$`)

// Releases lists what is installed on one cluster. It never returns an error:
// every failure is one of the states above with its own sentence, written
// once, here.
func (r *Reader) Releases(ctx context.Context, name string, namespaces []string) Report {
	report := Report{Cluster: name, Collected: time.Now(), Scope: namespaces}

	client, err := r.client(name)
	if err != nil {
		report.State = StateUnreachable
		report.Detail = unreachableSentence(name, err)
		return report
	}

	// One listing per selected namespace, or one over every namespace when
	// none is selected. A cluster-wide listing filtered here would let other
	// teams' releases use up the ceiling and make the note's count depend on
	// Secrets the person cannot see. The listings run one after another and
	// any failure fails the page, so no table is drawn from part of a
	// selection.
	scopes := listScopes(namespaces)

	// No silent narrowing: the list has no way to say it holds only some
	// namespaces. Every scope is reviewed first, by name; a refused selected
	// namespace is named, and someone who may not list Secrets everywhere is
	// told to choose a namespace.
	if refusal := r.selectionRefused(ctx, client.Context, scopes); refusal != nil {
		report.State = StateUnreadable
		report.Detail = refusedSentence(name, refusal)
		return report
	}

	found := 0
	bytesRead := 0
	capped := ""
	// Keyed by namespace and release name: two namespaces may each hold a
	// release called `redis`.
	byRelease := map[string][]storedRelease{}

	// `visited` counts the Secrets the person is shown, and the ceiling is on
	// it; `bytesRead` counts everything received, for the budget that
	// protects the process. Kept separate so a Secret not shown can never
	// move the number the note prints.
	visited := 0

	visit := func(secret *corev1.Secret, shown bool) bool {
		if secret.Type != ReleaseType {
			return true
		}
		bytesRead += len(secret.Data[releaseKey])
		if bytesRead > byteBudget {
			capped = strconv.Itoa(visited) + " release Secrets, which is where " +
				textutil.Bytes(int64(byteBudget)) + " of them had been read"
			return false
		}
		if !shown {
			return true
		}
		visited++
		if visited > releaseLimit {
			capped = "the first " + strconv.Itoa(releaseLimit) + " release Secrets"
			return false
		}
		found++
		stored, err := decodeRelease(secret.Data[releaseKey])
		if err != nil {
			report.Undecoded = append(report.Undecoded, Undecoded{
				Namespace: secret.Namespace,
				Secret:    secret.Name,
				Reason:    textutil.FirstLineN(err.Error(), 200),
			})
			return true
		}
		// The labels fill what the document lacks; the document wins where
		// both exist, since it is what Helm reads back.
		fill(&stored, secret)
		key := secret.Namespace + "\x00" + stored.Name
		byRelease[key] = append(byRelease[key], stored)
		return true
	}
	for _, scope := range scopes {
		if err = r.eachSecret(ctx, client, scope, currentSelector, visit); err != nil || capped != "" {
			break
		}
	}
	if err != nil {
		if refused := (*access.Refused)(nil); errors.As(err, &refused) {
			report.State = StateUnreadable
			report.Detail = refusedSentence(name, err)
			return report
		}
		if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			report.State = StateUnreadable
			report.Detail = unreadableSentence(name, err)
			return report
		}
		report.State = StateUnreachable
		report.Detail = unreachableSentence(name, err)
		return report
	}

	for _, revisions := range byRelease {
		report.Releases = append(report.Releases, aged(fold(revisions), report.Collected))
	}
	sort.Slice(report.Releases, func(i, j int) bool {
		a, b := report.Releases[i], report.Releases[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})

	switch {
	case len(report.Releases) > 0:
		report.State = StatePresent
	case len(report.Undecoded) > 0:
		report.State = StateUndecodable
		report.Detail = undecodableSentence(len(report.Undecoded), report.Undecoded[0])
		return report
	default:
		report.State = StateEmpty
		report.Detail = emptySentence(namespaces)
		return report
	}

	report.Note = noteFor(capped, len(report.Undecoded), "release Secret")
	return report
}

// History reads every revision of one release. Unlike the list, it does not
// narrow, so a release with hand-edited labels is still drawn in full here.
func (r *Reader) History(ctx context.Context, name, namespace, release string) History {
	out := History{Cluster: name, Namespace: namespace, Name: release, Collected: time.Now()}

	if !labelValue.MatchString(release) || namespace == "" {
		// Refused before any request: this value comes from a URL and would
		// be half of a label selector.
		out.State = StateEmpty
		out.Detail = "There is no Helm release by that name here: " +
			strconv.Quote(textutil.Clip(release, 80)) + " is not a name Helm can have written."
		return out
	}

	client, err := r.client(name)
	if err != nil {
		out.State = StateUnreachable
		out.Detail = unreachableSentence(name, err)
		return out
	}

	selector := ownerLabel + "=" + ownerHelm + "," + nameLabel + "=" + release
	read := 0
	bytesRead := 0
	capped := ""
	var stored []storedRelease

	// The count cap keeps the newest revisions. Secrets arrive in name order,
	// and `...v<n>` sorts as text (v1, v10, v100, v11, ...), so stopping at
	// the hundredth would pick arbitrary revisions and fold onto an old one as
	// current. So the stream only collects (the version is on a label: no
	// decode), the byte budget stops the listing while bytes arrive, and the
	// count cap selects after sorting.
	var arrived []*corev1.Secret

	err = r.eachSecret(ctx, client, namespace, selector, func(secret *corev1.Secret, shown bool) bool {
		if secret.Type != ReleaseType || !shown {
			return true
		}
		read++
		bytesRead += len(secret.Data[releaseKey])
		if bytesRead > byteBudget {
			capped = strconv.Itoa(read-1) + " revisions, which is where " +
				textutil.Bytes(int64(byteBudget)) + " of them had been read"
			return false
		}
		arrived = append(arrived, secret)
		return true
	})
	if err != nil {
		if refused := (*access.Refused)(nil); errors.As(err, &refused) {
			out.State = StateUnreadable
			out.Detail = refusedSentence(name, err)
			return out
		}
		if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			out.State = StateUnreadable
			out.Detail = unreadableSentence(name, err)
			return out
		}
		out.State = StateUnreachable
		out.Detail = unreachableSentence(name, err)
		return out
	}

	// Newest first, by the label Helm writes. An unparseable label sorts last
	// rather than being dropped: it is still a revision.
	sort.SliceStable(arrived, func(i, j int) bool {
		return labelVersion(arrived[i]) > labelVersion(arrived[j])
	})
	if len(arrived) > revisionLimit {
		capped = "the newest " + strconv.Itoa(revisionLimit) + " of " +
			strconv.Itoa(len(arrived)) + " revisions"
		arrived = arrived[:revisionLimit]
	}

	// The payload is read for the current revision only (index 0, what `helm
	// get values` answers about by default), not once per revision.
	var payload storedPayload
	payloadRead := false

	for index, secret := range arrived {
		current := index == 0
		var one storedRelease
		var err error
		if current {
			one, payload, err = decodeCurrent(secret.Data[releaseKey])
			payloadRead = err == nil
		} else {
			one, err = decodeRelease(secret.Data[releaseKey])
		}
		if err != nil {
			out.Undecoded = append(out.Undecoded, Undecoded{
				Namespace: secret.Namespace,
				Secret:    secret.Name,
				Reason:    textutil.FirstLineN(err.Error(), 200),
			})
			// The labels still say which revision this is and what happened
			// to it: a gap that says what it is beats a renumbered history.
			if fallback, ok := fromLabels(secret); ok {
				stored = append(stored, fallback)
			}
			continue
		}
		fill(&one, secret)
		stored = append(stored, one)
	}

	// Every Secret undecoded is StateUndecodable, as on the list, even though
	// fromLabels made `stored` non-empty: StatePresent would draw a release
	// of em dashes under a heading claiming it is there.
	if len(stored) > 0 && len(out.Undecoded) == len(stored) {
		out.State = StateUndecodable
		out.Detail = undecodableSentence(len(out.Undecoded), out.Undecoded[0])
		return out
	}

	if len(stored) == 0 {
		if len(out.Undecoded) > 0 {
			out.State = StateUndecodable
			out.Detail = undecodableSentence(len(out.Undecoded), out.Undecoded[0])
			return out
		}
		out.State = StateEmpty
		out.Detail = "No Helm release called " + release + " in " + namespace +
			" on this cluster. Helm keeps one Secret per revision, and there is none here " +
			"— the release may have been uninstalled without --keep-history, or it may be in another namespace."
		return out
	}

	out.State = StatePresent
	out.Release = aged(fold(stored), out.Collected)
	out.Revisions = revisionsOf(stored, out.Release)
	out.Note = noteFor(capped, len(out.Undecoded), "revision Secret")

	// Three states: read (possibly empty), or unread, said instead and
	// neither field filled.
	if payloadRead {
		out.Notes = strings.TrimRight(payload.Info.Notes, "\n")
		out.Values, err = valuesYAML(payload.Config)
		if err != nil {
			out.PayloadNote = "The values of revision " + strconv.Itoa(out.Release.Revision) +
				" could not be rendered: " + textutil.FirstLineN(err.Error(), 160)
			out.Values = ""
		}
	} else {
		out.PayloadNote = "Revision " + strconv.Itoa(out.Release.Revision) +
			" is the current one and its stored document would not decode, so its notes and " +
			"its values could not be read. That is not the same as a chart that ships none."
	}
	return out
}

// valuesYAML renders the supplied values as YAML, comparable with the
// `-f values.yaml` that produced them, using the same converter as the
// console's manifests.
//
// `{}` and `null` come back empty: Helm writes `"config": {}` for a release
// with no overrides, and the page says "none" in words rather than drawing
// `{}`.
func valuesYAML(config json.RawMessage) (string, error) {
	trimmed := strings.TrimSpace(string(config))
	if trimmed == "" || trimmed == "null" || trimmed == "{}" {
		return "", nil
	}
	out, err := yaml.JSONToYAML(config)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(out)) == "{}" {
		return "", nil
	}
	return string(out), nil
}

func (r *Reader) client(name string) (*cluster.Client, error) {
	if r == nil || r.clusters == nil {
		return nil, errors.New("this build has no cluster manager")
	}
	client, err := r.clusters.Client(name)
	if err != nil {
		return nil, err
	}
	if client == nil || client.Clients == nil {
		return nil, errors.New("no client for this context")
	}
	return client, nil
}

// currentSelector is the list page's narrowing; the package comment says why
// Helm's write order makes it safe.
var currentSelector = ownerLabel + "=" + ownerHelm + "," +
	statusLabel + " notin (" + StatusSuperseded + ")"

// selectionRefused reviews each scope the release page will draw, and returns
// the first refusal or nil.
func (r *Reader) selectionRefused(ctx context.Context, cluster string, scopes []string) error {
	for _, ns := range scopes {
		if err := r.gate.Check(ctx, cluster, secretList(ns)); err != nil {
			return err
		}
	}
	return nil
}

// listScopes is what the release page lists: each selected namespace once, in
// order, or "" (every namespace) when nothing is selected. Once, so nothing is
// counted twice against the ceiling. A stray empty value beside real ones is
// dropped, not read as "every namespace", which would widen the selection.
func listScopes(namespaces []string) []string {
	seen := map[string]bool{}
	var scopes []string
	for _, ns := range namespaces {
		if ns == "" || seen[ns] {
			continue
		}
		seen[ns] = true
		scopes = append(scopes, ns)
	}
	if len(scopes) == 0 {
		return []string{""}
	}
	return scopes
}

// secretList is the review's question for listing Secrets in ns, or in every
// namespace when ns is empty.
func secretList(ns string) access.Attributes {
	return access.Attributes{Verb: "list", Version: "v1", Resource: "secrets", Namespace: ns}
}

// eachSecret pages through a listing, handing each Secret to `visit` until it
// returns false.
//
// Reviewed for the person behind ctx (access.Each): a person who may not list
// Secrets where it reads is refused, not narrowed. Nothing in this package
// lists narrowed, so `shown` is always true; visit keeps the argument so that
// a narrowed listing could never be drawn by accident.
//
// Paged, so the caller's bounds apply while data arrives rather than to a
// whole cluster's release Secrets already in memory.
func (r *Reader) eachSecret(ctx context.Context, client *cluster.Client, namespace string, selector string,
	visit func(secret *corev1.Secret, shown bool) bool) error {
	_, err := access.Each(ctx, r.gate, client.Context, secretList(namespace), nil,
		func(ctx context.Context, keep func(*corev1.Secret) bool) error {
			return pageSecrets(ctx, client, namespace, selector, func(secret *corev1.Secret) bool {
				return visit(secret, keep(secret))
			})
		})
	return err
}

// pageSecrets is the listing itself, called only by eachSecret inside the
// review.
func pageSecrets(ctx context.Context, client *cluster.Client, namespace, selector string,
	visit func(*corev1.Secret) bool) error {
	opts := metav1.ListOptions{
		LabelSelector: selector,
		// A field selector on `type`, which the API server indexes for
		// Secrets: a Secret someone else labeled `owner=helm` never reaches
		// the decoder.
		FieldSelector: "type=" + ReleaseType,
		Limit:         pageSize,
	}
	for {
		list, err := client.Clients.CoreV1().Secrets(namespace).List(ctx, opts)
		if err != nil {
			return err
		}
		for i := range list.Items {
			if !visit(&list.Items[i]) {
				return nil
			}
		}
		if list.Continue == "" {
			return nil
		}
		opts.Continue = list.Continue
	}
}

// fill takes from the Secret's labels what the document did not carry. The
// document is the authority (Helm reads it back); the labels, written from
// the same release, are the fallback.
func fill(stored *storedRelease, secret *corev1.Secret) {
	if stored.Name == "" {
		stored.Name = secret.Labels[nameLabel]
	}
	if stored.Namespace == "" {
		stored.Namespace = secret.Namespace
	}
	if stored.Version == 0 {
		stored.Version, _ = strconv.Atoi(secret.Labels[versionLabel])
	}
	if stored.Info.Status == "" {
		stored.Info.Status = secret.Labels[statusLabel]
	}
}

// labelVersion is the revision number off the Secret's label (what Helm's
// storage driver queries on), or -1 when it is not a number, so it sorts
// after every real revision.
func labelVersion(secret *corev1.Secret) int {
	v, err := strconv.Atoi(secret.Labels[versionLabel])
	if err != nil {
		return -1
	}
	return v
}

// fromLabels is what is left of a revision whose document would not decode:
// the facts Helm puts in the labels. It returns false when even those are
// missing, since "revision 0, no status" is not a revision.
func fromLabels(secret *corev1.Secret) (storedRelease, bool) {
	version, err := strconv.Atoi(secret.Labels[versionLabel])
	if err != nil || version <= 0 {
		return storedRelease{}, false
	}
	var out storedRelease
	out.Name = secret.Labels[nameLabel]
	out.Namespace = secret.Namespace
	out.Version = version
	out.Info.Status = secret.Labels[statusLabel]
	// The Secret's creation time is not offered as `last_deployed`: nearly
	// right is not right, so the row shows no time.
	out.undecoded = true
	return out, true
}

// fold reduces a release's revisions to the one row that describes it.
// Current is the highest revision number, as in `helm list`; where that is not
// the `deployed` one (a failed upgrade, an interrupted rollback),
// DeployedRevision carries the other half.
func fold(revisions []storedRelease) Release {
	sort.Slice(revisions, func(i, j int) bool { return revisions[i].Version > revisions[j].Version })
	current := revisions[0]

	out := Release{
		Name:          current.Name,
		Namespace:     current.Namespace,
		Revision:      current.Version,
		Status:        current.Info.Status,
		Chart:         current.Chart.Metadata.Name,
		ChartVersion:  current.Chart.Metadata.Version,
		AppVersion:    current.Chart.Metadata.AppVersion,
		Deprecated:    current.Chart.Metadata.Deprecated,
		Updated:       current.Info.LastDeployed.Time,
		FirstDeployed: current.Info.FirstDeployed.Time,
		Description:   current.Info.Description,
		Revisions:     len(revisions),
	}
	if current.Info.Status != StatusDeployed {
		for _, rev := range revisions[1:] {
			if rev.Info.Status == StatusDeployed {
				out.DeployedRevision = rev.Version
				break
			}
		}
	}
	return out
}

// aged writes Release.Age from the report's collection instant, kept out of
// fold so every age in one answer is measured from one moment. A release with
// no recorded time gets no age, since "0s" would claim it was just deployed.
// resources.ShortDuration is the shared scale the client's fallback matches,
// and it clamps clock skew to zero.
func aged(release Release, now time.Time) Release {
	if release.Updated.IsZero() {
		return release
	}
	release.Age = resources.ShortDuration(now.Sub(release.Updated))
	return release
}

// revisionsOf is the history table: newest first, marked.
func revisionsOf(stored []storedRelease, release Release) []Revision {
	sort.Slice(stored, func(i, j int) bool { return stored[i].Version > stored[j].Version })
	out := make([]Revision, 0, len(stored))
	for _, one := range stored {
		row := Revision{
			Revision:     one.Version,
			Status:       one.Info.Status,
			Updated:      one.Info.LastDeployed.Time,
			ChartVersion: one.Chart.Metadata.Version,
			AppVersion:   one.Chart.Metadata.AppVersion,
			Description:  one.Info.Description,
			Current:      one.Version == release.Revision,
			Deployed:     one.Info.Status == StatusDeployed,
		}
		if one.undecoded {
			row.Note = "this revision's Secret would not decode, so its labels are all there is"
		}
		out = append(out, row)
	}
	return out
}

// --- the sentences ----------------------------------------------------------
//
// One composer per absence, shared by the list and a release's own page, so
// one state is never worded two ways.

// requestURL matches a URL in an error message. reason strips it before
// cutting the sentence: client-go prefixes errors with the full request URL,
// selectors included, which would push the actual failure ("dial tcp ...:
// connection refused") past the cut. Same pattern as internal/health's.
var requestURL = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"]*`)

// reason is a client-go error reduced to the part an operator can act on.
func reason(err error) string {
	if err == nil {
		return ""
	}
	// `Get "": ` is what is left once the address goes; drop it too.
	stripped := strings.ReplaceAll(requestURL.ReplaceAllString(err.Error(), ""), `Get "": `, "")
	return textutil.FirstLineN(strings.TrimSpace(stripped), 200)
}

func unreachableSentence(name string, err error) string {
	return name + " could not be asked, so whether it runs any Helm release is unknown: " +
		reason(err)
}

// refusedSentence is a release page the person behind the request may not
// read, in the access review's own words.
func refusedSentence(name string, err error) string {
	sentence := "You may not list Secrets on " + name + " where this page reads, and a Helm 3 " +
		"release is stored as one, so the releases there are not shown to you. "
	if errors.Is(err, access.ErrUnreviewable) {
		sentence = "Whether you may list Secrets on " + name + " could not be asked, and a Helm 3 " +
			"release is stored as one, so the releases there are not shown. "
	}
	return sentence + capitalised(textutil.FirstLineN(err.Error(), 300)) +
		". Choosing one namespace you may list shows its releases."
}

// capitalized upper-cases a sentence's first letter: the review's words start
// in lower case, and here they follow a full stop.
func capitalised(s string) string {
	first, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(first)) + s[size:]
}

func unreadableSentence(name string, err error) string {
	return "This console may not list Secrets on " + name +
		", and a Helm 3 release is stored as one — so whether anything is installed here " +
		"cannot be established. The identity KubeTower uses needs `list` on `secrets`. " +
		reason(err)
}

func undecodableSentence(n int, first Undecoded) string {
	subject := "One release Secret was found"
	if n > 1 {
		subject = strconv.Itoa(n) + " release Secrets were found"
	}
	return subject + " and not one of them could be read. " +
		"That is not an empty cluster: something has written a Secret typed " +
		ReleaseType + " that does not hold what Helm writes. First of them, " +
		first.Namespace + "/" + first.Secret + ": " + first.Reason
}

func emptySentence(namespaces []string) string {
	where := "on this cluster"
	switch len(namespaces) {
	case 0:
	case 1:
		where = "in " + namespaces[0]
	default:
		where = "in " + strings.Join(namespaces, ", ")
	}
	return "No Helm release " + where + ". Helm 3 keeps a release as a Secret in its own " +
		"namespace, and none was found — an installation using the `configmap` or `sql` " +
		"storage driver is not read here."
}

// noteFor is what a good answer still has to admit to.
func noteFor(capped string, undecoded int, subject string) string {
	var parts []string
	if capped != "" {
		parts = append(parts, "This page read "+capped+
			" and stopped, so there may be more than is drawn here.")
	}
	if undecoded == 1 {
		parts = append(parts, "One "+subject+" could not be read and is not on this page.")
	} else if undecoded > 1 {
		parts = append(parts, fmt.Sprintf("%d %ss could not be read and are not on this page.",
			undecoded, subject))
	}
	return strings.Join(parts, " ")
}

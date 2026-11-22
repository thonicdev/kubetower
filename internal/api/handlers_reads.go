package api

import (
	"net/http"
	"strconv"
	"strings"
)

// The enveloped reads answer 200 with an envelope even when the data could not
// be obtained: the request succeeded, and the answer is that the data did not.

// absentReads is the answer when this build has no reader. Not `empty`: "no
// reader" and "nothing to read" are different answers.
func absentReads(what string) *Failure {
	return Absent("this build was assembled without " + what)
}

// listNamespaces is the selector's endpoint, and it answers four ways: `empty`
// (nothing there), `unreadable` or `forbidden` (could not be asked), `pending`
// (nobody asked yet), or `ok` with `stale_reading` (kept from before the
// cluster stopped answering). Empty and could-not-ask must never look alike:
// every cluster has `default` and `kube-system`, while a namespace-scoped
// identity or a lapsed session is ordinary. The backend establishes which and
// namespaceList maps it; this handler only refuses an unknown cluster and a
// build with no reader.
func (a *API) listNamespaces(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	if a.deps.Reads == nil {
		writeOK(w, namespaceList(NamespaceReading{}, absentReads("a namespace reader")))
		return
	}
	reading, f := a.deps.Reads.Namespaces(r.Context(), name)
	writeOK(w, namespaceList(reading, f))
}

// maxListFilters bounds the repeatable `filter` parameter; api/openapi.yaml
// declares the same number as the parameter's maxItems.
const maxListFilters = 64

func (a *API) listResources(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	kind, ok := kindOf(w, r.PathValue("kind"))
	if !ok {
		return
	}
	q := r.URL.Query()
	if len(q.Get("selector")) > 1024 || len(q.Get("search")) > 256 {
		badRequest(w, "selector or search is too long")
		return
	}
	// The filter control's choices, bounded like the search: more than this
	// was not written by the control.
	if len(q["filter"]) > maxListFilters {
		badRequest(w, "too many filters")
		return
	}
	for _, filter := range q["filter"] {
		if len(filter) > 512 {
			badRequest(w, "a filter is too long")
			return
		}
	}
	if a.deps.Reads == nil {
		writeOK(w, ValueFailed[ListPage](StateAbsent, absentReads("a resource reader").Reason))
		return
	}
	// The search term is never interpolated into a path, so a name with an
	// ampersand cannot redirect the request.
	page, f := a.deps.Reads.ListResources(r.Context(), ListQuery{
		Cluster: name,
		Kind:    kind,
		// Every value, because the parameter is repeatable: `q.Get` would narrow
		// `?namespace=a&namespace=b` to `a` silently, showing a fraction of the
		// cluster with no sign of it. Joined rather than passed as a slice, so
		// `?namespace=a,b` and the repeated form reach parseNamespaces (which
		// splits, canonicalizes and deduplicates) identically, and ListQuery keeps
		// its shape.
		Namespace: strings.Join(q["namespace"], ","),
		Selector:  q.Get("selector"),
		Search:    q.Get("search"),
		// Every value, for the namespace's reason above.
		Filters: q["filter"],
	})
	// A selector that does not parse is the request's fault, never answered
	// as a list, which would show every row.
	if f.SelectorInvalid() {
		WriteProblem(w, http.StatusBadRequest, ProblemInvalidSelector, "Bad request", f.Reason.Message)
		return
	}
	if f.Invalid() {
		badRequest(w, f.Reason.Message)
		return
	}
	writeOK(w, valueOf(page, f))
}

// getClusterScopedResource and getNamespacedResource are two forms because a
// kind is namespaced or not; the list payload's `namespaced` says which, so the
// front end does not guess.
func (a *API) getClusterScopedResource(w http.ResponseWriter, r *http.Request) {
	a.detail(w, r, "")
}

func (a *API) getNamespacedResource(w http.ResponseWriter, r *http.Request) {
	a.detail(w, r, r.PathValue("namespace"))
}

func (a *API) detail(w http.ResponseWriter, r *http.Request, namespace string) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	kind, ok := kindOf(w, r.PathValue("kind"))
	if !ok {
		return
	}
	object, ok := nameOf(w, r.PathValue("name"))
	if !ok {
		return
	}
	if a.deps.Reads == nil {
		writeOK(w, ValueFailed[DetailPage](StateAbsent, absentReads("a detail reader").Reason))
		return
	}
	page, f := a.deps.Reads.GetResource(r.Context(), ObjectRef{
		Cluster: name, Kind: kind, Namespace: namespace, Name: object,
	})
	writeOK(w, valueOf(page, f))
}

// getCreateTemplate is the document the list's "+" opens. It reviews nothing:
// reading a template is not permission to create, and previewCreate reviews
// `create` for the person. The namespace is held to a DNS label first because
// it is written into the document.
func (a *API) getCreateTemplate(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	kind, ok := kindOf(w, q.Get("kind"))
	if !ok {
		return
	}
	namespace := q.Get("namespace")
	if namespace != "" && !namespacePattern.MatchString(namespace) {
		badRequest(w, "a namespace is a DNS label: lower-case letters, digits and '-', at most 63")
		return
	}
	if a.deps.Reads == nil {
		writeOK(w, ValueFailed[CreateTemplate](StateAbsent, absentReads("a template reader").Reason))
		return
	}
	template, f := a.deps.Reads.CreateTemplate(r.Context(), CreateTemplateQuery{Cluster: name, Kind: kind, Namespace: namespace})
	writeOK(w, valueOf(template, f))
}

func (a *API) listHelmReleases(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	if a.deps.Reads == nil {
		writeOK(w, ListFailed[HelmRelease](StateAbsent, absentReads("a Helm reader").Reason))
		return
	}
	// `Query()["namespace"]`, not `Get`: the parameter is repeatable, and
	// `Get` would silently turn a multi-namespace selection into the first one.
	reading, f := a.deps.Reads.HelmReleases(r.Context(), name, r.URL.Query()["namespace"])
	writeOK(w, helmList(reading, f))
}

func (a *API) getHelmRelease(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	namespace, ok := nameOf(w, r.PathValue("namespace"))
	if !ok {
		return
	}
	release, ok := nameOf(w, r.PathValue("name"))
	if !ok {
		return
	}
	if a.deps.Reads == nil {
		writeOK(w, ValueFailed[HelmReleaseDetail](StateAbsent, absentReads("a Helm reader").Reason))
		return
	}
	detail, f := a.deps.Reads.HelmRelease(r.Context(), name, namespace, release)
	writeOK(w, valueOf(detail, f))
}

// revealHelmValues is the one door a release's supplied values reach a view
// through. `revision` is required because the audit record names it before the
// read; every name in the audit line is held to Kubernetes' rules first, as
// revealEnvValue's are.
func (a *API) revealHelmValues(w http.ResponseWriter, r *http.Request) {
	q, ok := a.helmReveal(w, r, "values")
	if !ok {
		return
	}
	if a.deps.Reads == nil {
		writeOK(w, ValueFailed[HelmValues](StateAbsent, absentReads("a Helm reader").Reason))
		return
	}
	values, f := a.deps.Reads.RevealHelmValues(r.Context(), q)
	writeOK(w, valueOf(values, f))
}

// revealHelmNotes is the same door for the rendered `NOTES.txt`, which charts
// use to print generated passwords and tokens.
func (a *API) revealHelmNotes(w http.ResponseWriter, r *http.Request) {
	q, ok := a.helmReveal(w, r, "notes")
	if !ok {
		return
	}
	if a.deps.Reads == nil {
		writeOK(w, ValueFailed[HelmNotes](StateAbsent, absentReads("a Helm reader").Reason))
		return
	}
	notes, f := a.deps.Reads.RevealHelmNotes(r.Context(), q)
	writeOK(w, valueOf(notes, f))
}

// helmReveal reads what both Helm reveals ask: the release, and the revision
// the page was drawn from. It answers 400 itself, and marks the answer
// `no-store` before anything is read.
func (a *API) helmReveal(w http.ResponseWriter, r *http.Request, key string) (HelmRevealQuery, bool) {
	name, ok := a.cluster(w, r)
	if !ok {
		return HelmRevealQuery{}, false
	}
	namespace, ok := nameOf(w, r.PathValue("namespace"))
	if !ok {
		return HelmRevealQuery{}, false
	}
	release, ok := nameOf(w, r.PathValue("name"))
	if !ok {
		return HelmRevealQuery{}, false
	}
	revision, err := strconv.Atoi(r.URL.Query().Get("revision"))
	if err != nil || revision < 1 {
		badRequest(w, "revision is required: the release's current revision, a positive whole number")
		return HelmRevealQuery{}, false
	}
	ref := ObjectRef{Cluster: name, Kind: "secrets", Namespace: namespace, Name: HelmRevisionSecret(release, revision)}
	if problem := revealNamesProblem(ref, key, ""); problem != "" {
		badRequest(w, problem)
		return HelmRevealQuery{}, false
	}
	noStore(w)
	return HelmRevealQuery{Cluster: name, Namespace: namespace, Name: release, Revision: revision}, true
}

// getOverview is nine panels read from different places, each enveloped, so a
// cluster with no metrics source shows its capacity and `absent` for usage
// rather than zero.
func (a *API) getOverview(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	if a.deps.Reads == nil {
		writeOK(w, ValueFailed[Overview](StateAbsent, absentReads("a cluster reader").Reason))
		return
	}
	overview, f := a.deps.Reads.Overview(r.Context(), name)
	writeOK(w, valueOf(overview, f))
}

func (a *API) getMetricsPanel(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	ref, ok := a.objectFromQuery(w, r, name)
	if !ok {
		return
	}
	q := r.URL.Query()
	window := q.Get("range")
	if window == "" {
		window = "1h"
	}
	if !rangePattern.MatchString(window) {
		badRequest(w, "range is a duration such as 30m, 6h or 7d")
		return
	}
	if a.deps.Reads == nil {
		writeOK(w, ValueFailed[MetricsPanel](StateAbsent, absentReads("a metrics reader").Reason))
		return
	}
	panel, f := a.deps.Reads.Metrics(r.Context(), MetricsQuery{
		ObjectRef: ref,
		Range:     window,
		Points:    queryInt(q, "points", 120, 10, 2000),
	})
	writeOK(w, valueOf(panel, f))
}

func (a *API) getChangesPanel(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	ref, ok := a.objectFromQuery(w, r, name)
	if !ok {
		return
	}
	if a.deps.Reads == nil {
		writeOK(w, ValueFailed[ChangesPanel](StateAbsent, absentReads("a changes reader").Reason))
		return
	}
	panel, f := a.deps.Reads.Changes(r.Context(), ref)
	writeOK(w, valueOf(panel, f))
}

// revealEnvValue is the one door a masked value reaches a view through. The
// key travels in the query and the value only in the body, since a URL lands
// in access logs, and the response is never cached.
func (a *API) revealEnvValue(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	ref, ok := a.objectFromQuery(w, r, name)
	if !ok {
		return
	}
	q := r.URL.Query()
	key := q.Get("key")
	if key == "" || len(key) > 253 {
		badRequest(w, "key is required, and it is the variable's name and never its value")
		return
	}
	// Every name the audit line carries, held to Kubernetes' rules first
	// (revealnames.go says why).
	if problem := revealNamesProblem(ref, key, q.Get("container")); problem != "" {
		badRequest(w, problem)
		return
	}
	noStore(w)
	if a.deps.Reads == nil {
		writeOK(w, ValueFailed[EnvValue](StateAbsent, absentReads("a detail reader").Reason))
		return
	}
	value, f := a.deps.Reads.RevealEnv(r.Context(), EnvQuery{
		ObjectRef: ref, Container: q.Get("container"), Key: key,
	})
	writeOK(w, valueOf(value, f))
}

func (a *API) getDataPanel(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	ref, ok := a.objectFromQuery(w, r, name)
	if !ok {
		return
	}
	if a.deps.Reads == nil {
		writeOK(w, ValueFailed[DataPanel](StateAbsent, absentReads("a detail reader").Reason))
		return
	}
	panel, f := a.deps.Reads.DataPanel(r.Context(), ref)
	writeOK(w, valueOf(panel, f))
}

// getManifest returns tokens, which the front end renders as spans. The API
// never returns HTML, which would ship an injection surface.
func (a *API) getManifest(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	ref, ok := a.objectFromQuery(w, r, name)
	if !ok {
		return
	}
	if a.deps.Reads == nil {
		writeOK(w, ValueFailed[ManifestPanel](StateAbsent, absentReads("a detail reader").Reason))
		return
	}
	panel, f := a.deps.Reads.Manifest(r.Context(), ref)
	writeOK(w, valueOf(panel, f))
}

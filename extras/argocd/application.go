package argocd

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/thonicdev/kubetower/extension"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// One Application, read whole, from what the Application object carries. The
// Git-to-cluster diff lives in Argo CD's repo-server, behind its own API and
// sign-in, so this page shows which resource differs, never a diff. Each
// resource is addressed as the console addresses it, so it opens in the
// console's own pages, read for the signed-in person.

// ApplicationDetail is the answer of the one-Application route.
type ApplicationDetail struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Project   string `json:"project,omitempty"`
	Deleting  bool   `json:"deleting"`

	Source      Source      `json:"source"`
	Destination Destination `json:"destination"`

	// Sync and Health are Argo's own words, as on the list.
	Sync          string       `json:"sync"`
	SyncRevision  string       `json:"syncRevision,omitempty"`
	Health        string       `json:"health"`
	HealthMessage string       `json:"healthMessage,omitempty"`
	ReconciledAt  string       `json:"reconciledAt,omitempty"`
	AutomatedSync bool         `json:"automatedSync"`
	Operation     *Operation   `json:"operation,omitempty"`
	History       []Deployment `json:"history"`
	Conditions    []Condition  `json:"conditions"`
	Resources     []Resource   `json:"resources"`
	// ResourcesNote is set when some resources could not be given a console
	// address, so the page says why those rows open nothing.
	ResourcesNote string `json:"resourcesNote,omitempty"`
	// ProjectNamespace is where the AppProject lives, so the page opens it;
	// empty when it could not be told.
	ProjectNamespace string `json:"projectNamespace,omitempty"`
	// Events are the Application's Kubernetes Events, newest first.
	Events     []Event `json:"events"`
	EventsNote string  `json:"eventsNote,omitempty"`
}

// Source is where Argo CD reads the desired state from.
type Source struct {
	RepoURL        string `json:"repoURL,omitempty"`
	Path           string `json:"path,omitempty"`
	Chart          string `json:"chart,omitempty"`
	TargetRevision string `json:"targetRevision,omitempty"`
}

// Destination is where Argo CD applies it.
type Destination struct {
	Server    string `json:"server,omitempty"`
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

// destinationOf reads `spec.destination`, absent fields empty.
func destinationOf(obj unstructured.Unstructured) Destination {
	return Destination{
		Server:    nestedString(obj, "spec", "destination", "server"),
		Name:      nestedString(obj, "spec", "destination", "name"),
		Namespace: nestedString(obj, "spec", "destination", "namespace"),
	}
}

// Operation is the last (or running) sync operation, `status.operationState`.
type Operation struct {
	Phase       string `json:"phase"`
	Message     string `json:"message,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	FinishedAt  string `json:"finishedAt,omitempty"`
	InitiatedBy string `json:"initiatedBy,omitempty"`
	// Automated is Argo's own `initiatedBy.automated`: the sync policy ran it,
	// not a person.
	Automated bool   `json:"automated"`
	Revision  string `json:"revision,omitempty"`
	// Results is what the operation did to each resource, Argo's words.
	Results []OperationResult `json:"results"`
}

// OperationResult is one row of `syncResult.resources`.
type OperationResult struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Status    string `json:"status,omitempty"`
	HookPhase string `json:"hookPhase,omitempty"`
	Message   string `json:"message,omitempty"`
}

// Deployment is one entry of `status.history`, newest first.
type Deployment struct {
	ID         int64  `json:"id"`
	Revision   string `json:"revision,omitempty"`
	DeployedAt string `json:"deployedAt,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	Source     Source `json:"source"`
}

// Condition is one of `status.conditions`. Argo names its errors `…Error`.
type Condition struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
	Error   bool   `json:"error"`
}

// Resource is one of `status.resources`: a resource the Application manages.
type Resource struct {
	Group     string `json:"group,omitempty"`
	Version   string `json:"version,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	// Status is Argo's sync word for this resource: `Synced`, or `OutOfSync`
	// when the cluster differs from Git.
	Status          string `json:"status,omitempty"`
	Health          string `json:"health,omitempty"`
	HealthMessage   string `json:"healthMessage,omitempty"`
	RequiresPruning bool   `json:"requiresPruning"`
	// Path is the kind's segment in the console's own address -
	// `/c/{cluster}/{path}/{namespace}/{name}` - or empty when it could not be
	// told (ResourcesNote says why).
	Path string `json:"path,omitempty"`
}

func (d Delivery) application(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		namespace, name := r.PathValue("namespace"), r.PathValue("name")

		crd, ok := resolve(w, r, h, cluster, ApplicationKind, "Argo CD")
		if !ok {
			return
		}
		page, err := h.List(r, cluster, extension.ListQuery{
			GVR:           crd.GVR(),
			Namespace:     namespace,
			FieldSelector: "metadata.name=" + name,
		})
		if writeRefused(w, err, func(who string) string {
			return "Argo CD is installed on this cluster, and " + who + " may not read its Applications."
		}) {
			return
		}
		if err != nil {
			writeProblem(w, http.StatusBadGateway, CodeListFailed, "The Application could not be read.", err)
			return
		}
		var found *unstructured.Unstructured
		for i := range page.Items {
			if page.Items[i].GetNamespace() == namespace && page.Items[i].GetName() == name {
				found = &page.Items[i]
			}
		}
		if found == nil {
			writeProblem(w, http.StatusNotFound, CodeApplicationNotFound,
				"There is no Application "+namespace+"/"+name+" on this cluster that this console may read.", nil)
			return
		}

		detail := applicationDetail(*found)
		detail.ResourcesNote = addressResources(r, h, cluster, detail.Resources)
		detail.ProjectNamespace = projectNamespace(r, h, cluster, namespace, detail.Project)
		detail.Events, detail.EventsNote = eventsOf(r, h, cluster, namespace, name)
		writeJSON(w, http.StatusOK, detail)
	})
}

func applicationDetail(obj unstructured.Unstructured) ApplicationDetail {
	d := ApplicationDetail{
		Namespace:     obj.GetNamespace(),
		Name:          obj.GetName(),
		Project:       nestedString(obj, "spec", "project"),
		Deleting:      obj.GetDeletionTimestamp() != nil,
		Source:        sourceOf(nestedMap(obj.Object, "spec", "source")),
		Sync:          nestedString(obj, "status", "sync", "status"),
		SyncRevision:  nestedString(obj, "status", "sync", "revision"),
		Health:        nestedString(obj, "status", "health", "status"),
		HealthMessage: nestedString(obj, "status", "health", "message"),
		ReconciledAt:  nestedString(obj, "status", "reconciledAt"),
		History:       []Deployment{},
		Conditions:    []Condition{},
		Resources:     []Resource{},
	}
	d.Destination = destinationOf(obj)
	_, d.AutomatedSync, _ = unstructured.NestedMap(obj.Object, "spec", "syncPolicy", "automated")

	if op := nestedMap(obj.Object, "status", "operationState"); op != nil {
		o := &Operation{
			Phase:       str(op, "phase"),
			Message:     str(op, "message"),
			StartedAt:   str(op, "startedAt"),
			FinishedAt:  str(op, "finishedAt"),
			InitiatedBy: str(nestedMap(op, "operation", "initiatedBy"), "username"),
			Revision:    str(nestedMap(op, "syncResult"), "revision"),
			Results:     []OperationResult{},
		}
		o.Automated, _, _ = unstructured.NestedBool(op, "operation", "initiatedBy", "automated")
		for _, raw := range slice(nestedMap(op, "syncResult"), "resources") {
			o.Results = append(o.Results, OperationResult{
				Kind:      str(raw, "kind"),
				Namespace: str(raw, "namespace"),
				Name:      str(raw, "name"),
				Status:    str(raw, "status"),
				HookPhase: str(raw, "hookPhase"),
				Message:   str(raw, "message"),
			})
		}
		d.Operation = o
	}

	for _, raw := range slice(nestedMap(obj.Object, "status"), "history") {
		id, _, _ := unstructured.NestedInt64(raw, "id")
		d.History = append(d.History, Deployment{
			ID:         id,
			Revision:   str(raw, "revision"),
			DeployedAt: str(raw, "deployedAt"),
			StartedAt:  str(raw, "deployStartedAt"),
			Source:     sourceOf(nestedMap(raw, "source")),
		})
	}
	// Argo appends; the page reads the newest first.
	sort.SliceStable(d.History, func(i, j int) bool { return d.History[i].ID > d.History[j].ID })

	for _, raw := range slice(nestedMap(obj.Object, "status"), "conditions") {
		kind := str(raw, "type")
		d.Conditions = append(d.Conditions, Condition{
			Type:    kind,
			Message: str(raw, "message"),
			Error:   strings.HasSuffix(kind, "Error"),
		})
	}

	for _, raw := range slice(nestedMap(obj.Object, "status"), "resources") {
		pruning, _, _ := unstructured.NestedBool(raw, "requiresPruning")
		d.Resources = append(d.Resources, Resource{
			Group:           str(raw, "group"),
			Version:         str(raw, "version"),
			Kind:            str(raw, "kind"),
			Namespace:       str(raw, "namespace"),
			Name:            str(raw, "name"),
			Status:          str(raw, "status"),
			Health:          str(nestedMap(raw, "health"), "status"),
			HealthMessage:   str(nestedMap(raw, "health"), "message"),
			RequiresPruning: pruning,
		})
	}
	return d
}

// addressResources gives each resource the segment the console's own address
// uses for its kind, and answers a note when some could not be given one.
//
// The plural comes from discovery, because a plural is not the kind plus `s`
// (NetworkPolicy, Endpoints). Discovery says only which kinds are served, so
// reading it with the console's credentials reveals no object.
func addressResources(r *http.Request, h extension.Host, cluster string, resources []Resource) string {
	if len(resources) == 0 {
		return ""
	}
	clients, err := h.Client(r, cluster)
	if err != nil || clients.Typed == nil {
		return "The cluster's kinds could not be read, so the resources below open nothing here."
	}
	plurals := map[string]map[string]string{} // group/version -> kind -> plural
	pages := newPages(r, h, cluster)
	unserved, unpaged, unread := 0, 0, 0
	for i := range resources {
		gv := schema.GroupVersion{Group: resources[i].Group, Version: resources[i].Version}.String()
		kinds, seen := plurals[gv]
		if !seen {
			kinds = map[string]string{}
			if list, err := clients.Typed.Discovery().ServerResourcesForGroupVersion(gv); err == nil {
				for _, res := range list.APIResources {
					if !strings.Contains(res.Name, "/") {
						kinds[res.Kind] = res.Name
					}
				}
			}
			plurals[gv] = kinds
		}
		plural, ok := kinds[resources[i].Kind]
		if !ok {
			unserved++
			continue
		}
		page := pages.page(resources[i].Group, plural)
		resources[i].Path = page.path
		switch {
		case page.unread:
			unread++
		case page.path == "":
			unpaged++
		}
	}
	var notes []string
	if unserved > 0 {
		notes = append(notes, "Some resources' kinds are not served by this cluster's API, so those rows open nothing here.")
	}
	if unpaged > 0 {
		notes = append(notes, "Some resources' kinds have no page in this console, so those rows open nothing here.")
	}
	if unread > 0 {
		notes = append(notes, "Whether this console has a page for some resources' kinds could not be read, so those rows open nothing here.")
	}
	return strings.Join(notes, " ")
}

// pages answers where the console opens a kind, once per kind per request.
//
// A kind the console types by hand always has its page, at its bare plural.
// Any other is spelled as the menu spells it (extension.KindSegment) and
// linked only when the catalog resolves that segment back to the same kind,
// so a row never links onto "unknown kind" or another kind's list.
//
// A failure is cached for the request too, as unread rather than "no page", so
// many rows behind a failing catalog cost one lookup.
type pages struct {
	r        *http.Request
	h        extension.Host
	cluster  string
	registry extension.CRDRegistry
	regErr   error
	read     bool
	known    map[schema.GroupResource]kindPage
}

// kindPage is where one kind opens: path, or "" with unread saying whether
// that is the console's answer or a failure to get one.
type kindPage struct {
	path   string
	unread bool
}

func newPages(r *http.Request, h extension.Host, cluster string) *pages {
	return &pages{r: r, h: h, cluster: cluster, known: map[schema.GroupResource]kindPage{}}
}

// page is where the console opens this group's resource on this cluster.
func (p *pages) page(group, plural string) kindPage {
	if typed, ok := extension.TypedKind(plural); ok && typed == group {
		return kindPage{path: plural}
	}
	key := schema.GroupResource{Group: group, Resource: plural}
	if known, seen := p.known[key]; seen {
		return known
	}
	if !p.read {
		p.read = true
		p.registry, p.regErr = p.h.Definitions(p.r, p.cluster)
	}
	var answer kindPage
	if p.regErr != nil || p.registry == nil {
		answer.unread = true
	} else {
		segment := extension.KindSegment(group, plural)
		crd, err := p.registry.Lookup(p.r.Context(), segment)
		switch {
		case err == nil && crd.Group == group && crd.Plural == plural:
			answer.path = segment
		case err == nil, isKindAnswer(err):
			// Another kind's page, or none: the console's own answer.
		default:
			answer.unread = true
		}
	}
	p.known[key] = answer
	return answer
}

// isKindAnswer is whether a lookup's error is the catalog's answer about the
// kind - none, not served, or two - rather than a failure to give one.
func isKindAnswer(err error) bool {
	return errors.Is(err, extension.ErrUnknownKind) || errors.Is(err, extension.ErrNotServed) || errors.Is(err, extension.ErrAmbiguousKind)
}

// writeAmbiguous answers a plural two API groups define.
func writeAmbiguous(w http.ResponseWriter, kind string, err error) {
	writeProblem(w, http.StatusConflict, CodeKindAmbiguous, "Two API groups on this cluster define the same plural, so "+kind+" does not name one kind.", err)
}

// resolve answers the catalog's three answers and its failure for one kind,
// with the product named, and reports whether the handler may go on.
func resolve(w http.ResponseWriter, r *http.Request, h extension.Host, cluster, kind, product string) (extension.CRD, bool) {
	registry, err := h.Definitions(r, cluster)
	if unknownCluster(w, cluster, err) {
		return extension.CRD{}, false
	}
	if err != nil {
		writeProblem(w, http.StatusBadGateway, CodeCatalogueUnreadable, "The custom resource catalog could not be read, so whether this cluster serves "+product+" is unknown.", err)
		return extension.CRD{}, false
	}
	crd, err := registry.Lookup(r.Context(), kind)
	switch {
	case err == nil:
		return crd, true
	case errors.Is(err, extension.ErrUnknownKind):
		writeProblem(w, http.StatusNotFound, CodeKindUnknown, "This cluster serves no "+kind+", so "+product+" is not installed on it.", err)
	case errors.Is(err, extension.ErrNotServed):
		writeProblem(w, http.StatusNotFound, CodeKindNotServed, product+"'s custom resource definition is present on this cluster and no version of it is served, so nothing can be read.", err)
	case errors.Is(err, extension.ErrAmbiguousKind):
		writeAmbiguous(w, kind, err)
	default:
		writeProblem(w, http.StatusBadGateway, CodeKindUnresolved, "Resolving "+kind+" failed.", err)
	}
	return extension.CRD{}, false
}

func sourceOf(m map[string]any) Source {
	return Source{
		RepoURL:        str(m, "repoURL"),
		Path:           str(m, "path"),
		Chart:          str(m, "chart"),
		TargetRevision: str(m, "targetRevision"),
	}
}

// The three readers below keep nestedString's rule (absent or wrongly typed
// reads as empty) for maps that are not a whole object.

func nestedMap(m map[string]any, fields ...string) map[string]any {
	if m == nil {
		return nil
	}
	v, found, err := unstructured.NestedMap(m, fields...)
	if !found || err != nil {
		return nil
	}
	return v
}

func str(m map[string]any, field string) string {
	if m == nil {
		return ""
	}
	v, found, err := unstructured.NestedString(m, field)
	if !found || err != nil {
		return ""
	}
	return v
}

func slice(m map[string]any, field string) []map[string]any {
	if m == nil {
		return nil
	}
	raw, found, err := unstructured.NestedSlice(m, field)
	if !found || err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, one := range raw {
		if item, ok := one.(map[string]any); ok {
			out = append(out, item)
		}
	}
	return out
}

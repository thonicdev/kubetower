package argocd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/extras/argocd/drift"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// One object the console is showing, as Argo CD sees it: which Application
// manages it, Argo CD's sync word for it, and what changed on it since Argo CD
// last applied it (`drift`, never a change still in git; the answer carries
// the revisions so the page can say which). Argo CD's own diff is argodiff.go.
//
// The object and the Application are read through `Host.List`, for the
// person. Discovery, read with the console's credentials, only turns the kind
// segment into a resource and reveals no object.

// TrackingAnnotation is Argo CD 3's default way to mark what it manages:
// `<app>:<group>/<Kind>:<namespace>/<name>`.
const TrackingAnnotation = "argocd.argoproj.io/tracking-id"

// InstanceLabel is the older label tracking, the Application's name alone. Helm
// sets the same label on its own releases, so a label is never trusted until
// the Application it names lists the object.
const InstanceLabel = "app.kubernetes.io/instance"

// HelmRelease is what `helm install` writes on its objects and Argo CD's
// rendering never does: beside it, the instance label is Helm's.
const HelmRelease = "meta.helm.sh/release-name"

// CodeObjectNotFound: no object of that kind and name that the person may read.
const CodeObjectNotFound = "object_not_found"

// CodeBadRequest: the address does not name an object.
const CodeBadRequest = "bad_request"

// Tracked is the answer of the resource route.
type Tracked struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	// Of is the object asked about when the answer is its owner's (a Pod
	// through its Deployment). Through is the chain from the object's owner
	// up to the answered one.
	Of      *ObjectRef  `json:"of,omitempty"`
	Through []ObjectRef `json:"through,omitempty"`

	// Managed is false when nothing on the object names an Application, or the
	// label it carries names none that lists it. Note says which.
	Managed bool `json:"managed"`
	// Via is how the object names its Application: "annotation" or "label".
	Via  string `json:"via,omitempty"`
	Mark string `json:"mark,omitempty"`
	// Copied: the tracking annotation names another object, so it was copied
	// onto this one and Argo CD does not manage it.
	Copied bool `json:"copied"`
	// Secret: the object is a Secret, answered without reading it
	// (secret.go): only which keys differ, never a value.
	Secret bool `json:"secret,omitempty"`

	// Application is the one that manages the object, nil when it could not be
	// read; ApplicationCode and ApplicationNote say why, in the route's codes.
	Application     *ApplicationDetail `json:"application,omitempty"`
	ApplicationCode string             `json:"applicationCode,omitempty"`
	ApplicationNote string             `json:"applicationNote,omitempty"`
	Subject         string             `json:"subject,omitempty"`
	// Resource is Argo's entry for this object in `status.resources`.
	Resource *Resource `json:"resource,omitempty"`
	// SyncedRevision is the revision Argo CD last applied, beside the
	// Application's `syncRevision`, the one it compares against now.
	SyncedRevision string `json:"syncedRevision,omitempty"`

	Drift Drift `json:"drift"`
	// ArgoDiff is Argo CD's own comparison, through argocd-server; nil
	// when the Application or its listing of the object was not read.
	ArgoDiff *ArgoDiff `json:"argoDiff,omitempty"`
	// Note is the one sentence the page puts above everything: not managed,
	// copied, or what the gap means against Argo's own word.
	Note string `json:"note,omitempty"`
}

// Drift is the comparison, or why there is none.
type Drift struct {
	// Compared is false when there was nothing to compare against; Note says
	// why.
	Compared bool          `json:"compared"`
	Masked   bool          `json:"masked"`
	Fields   []drift.Field `json:"fields"`
	Note     string        `json:"note,omitempty"`
}

func (d Delivery) resource(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		query := r.URL.Query()
		segment, namespace, name := query.Get("kind"), query.Get("namespace"), query.Get("name")
		if segment == "" || name == "" {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, "The address names no object: kind and name are both needed.", nil)
			return
		}

		target, err := kindOf(r, h, cluster, segment)
		if kindProblem(w, segment, err) || unknownCluster(w, cluster, err) {
			return
		}
		if err != nil {
			writeProblem(w, http.StatusBadGateway, CodeKindUnresolved, "The cluster's kinds could not be read, so "+segment+" names nothing here.", err)
			return
		}
		if !target.namespaced {
			namespace = ""
		}
		if isSecret(target) {
			writeJSON(w, http.StatusOK, trackSecret(r, h, cluster, target, namespace, name))
			return
		}

		page, err := h.List(r, cluster, extension.ListQuery{GVR: target.gvr, Namespace: namespace, FieldSelector: "metadata.name=" + name})
		if writeRefused(w, err, func(who string) string {
			return capital(who) + " may not read this " + target.kind + "."
		}) {
			return
		}
		if err != nil {
			writeProblem(w, http.StatusBadGateway, CodeListFailed, "The "+target.kind+" could not be read.", err)
			return
		}
		var object *unstructured.Unstructured
		for i := range page.Items {
			if page.Items[i].GetName() == name && page.Items[i].GetNamespace() == namespace {
				object = &page.Items[i]
			}
		}
		if object == nil {
			writeProblem(w, http.StatusNotFound, CodeObjectNotFound, "There is no "+target.kind+" "+qualified(namespace, name)+" on this cluster that this console may read.", nil)
			return
		}

		writeJSON(w, http.StatusOK, track(r, h, cluster, target, *object))
	})
}

// target is the kind behind the console's segment.
type target struct {
	gvr        schema.GroupVersionResource
	kind       string
	namespaced bool
}

// kindOf turns the console's segment (`deployments`, `rollouts.argoproj.io`)
// back into a resource exactly as the console's own page resolves it, so the
// tab and the column answer about the kind on screen.
//
// A kind the console types by hand is looked up in its one group only, so
// `pods` is never metrics.k8s.io's PodMetrics. Every other segment goes
// through the same CRDRegistry.Lookup the page makes. ErrUnknownKind when the
// page has no kind to show either.
func kindOf(r *http.Request, h extension.Host, cluster, segment string) (target, error) {
	if group, typed := extension.TypedKind(segment); typed {
		return typedKind(r, h, cluster, group, segment)
	}
	registry, err := h.Definitions(r, cluster)
	if err != nil {
		return target{}, err
	}
	crd, err := registry.Lookup(r.Context(), segment)
	switch {
	case errors.Is(err, extension.ErrNotServed):
		// The page says no version is served, so nothing on it to answer about.
		return target{}, fmt.Errorf("%w: %w", extension.ErrUnknownKind, err)
	case err != nil:
		return target{}, err
	}
	return target{gvr: crd.GVR(), kind: crd.Kind, namespaced: crd.Namespaced}, nil
}

// kindProblem answers kindOf's two answers about the segment itself - no kind,
// or two - and reports whether it wrote one. Any other error is the caller's.
func kindProblem(w http.ResponseWriter, segment string, err error) bool {
	switch {
	case errors.Is(err, extension.ErrUnknownKind):
		writeProblem(w, http.StatusNotFound, CodeKindUnknown, "This cluster serves no "+segment+".", nil)
	case errors.Is(err, extension.ErrAmbiguousKind):
		writeAmbiguous(w, segment, err)
	default:
		return false
	}
	return true
}

// typedKind is a kind the console types by hand, at the version discovery
// prefers for its group.
func typedKind(r *http.Request, h extension.Host, cluster, group, plural string) (target, error) {
	clients, err := h.Client(r, cluster)
	if err != nil {
		return target{}, err
	}
	if clients.Typed == nil {
		return target{}, errors.New("no client for the cluster's discovery")
	}
	groups, lists, err := clients.Typed.Discovery().ServerGroupsAndResources()
	// A partial answer (one aggregated API down) still serves every other
	// kind, so search it before returning the error.
	preferred := ""
	for _, g := range groups {
		if g != nil && g.Name == group {
			preferred = g.PreferredVersion.Version
		}
	}
	found := false
	var best target
	for _, list := range lists {
		gv, parseErr := schema.ParseGroupVersion(list.GroupVersion)
		if parseErr != nil || gv.Group != group {
			continue
		}
		for _, res := range list.APIResources {
			if res.Name != plural {
				continue
			}
			if !found || gv.Version == preferred {
				best, found = target{gvr: gv.WithResource(res.Name), kind: res.Kind, namespaced: res.Namespaced}, true
			}
		}
	}
	if found {
		return best, nil
	}
	if err != nil {
		return target{}, err
	}
	return target{}, extension.ErrUnknownKind
}

// mark is what the object says about its Application.
type mark struct {
	via, raw          string
	appNamespace, app string
	// The object the annotation was written for, to tell a copied one.
	group, kind, namespace, name string
}

// markOf reads the tracking annotation, or the instance label.
func markOf(object unstructured.Unstructured) (mark, bool) {
	if raw, ok := object.GetAnnotations()[TrackingAnnotation]; ok && raw != "" {
		m := mark{via: "annotation", raw: raw}
		parts := strings.SplitN(raw, ":", 3)
		m.appNamespace, m.app = appRef(parts[0])
		if len(parts) == 3 {
			gk, nn := parts[1], parts[2]
			if group, kind, ok := strings.Cut(gk, "/"); ok {
				m.group, m.kind = group, kind
			}
			if ns, n, ok := strings.Cut(nn, "/"); ok {
				m.namespace, m.name = ns, n
			} else {
				m.name = nn
			}
		}
		return m, m.app != ""
	}
	if _, helm := object.GetAnnotations()[HelmRelease]; helm {
		return mark{}, false
	}
	if raw, ok := object.GetLabels()[InstanceLabel]; ok && raw != "" {
		m := mark{via: "label", raw: raw}
		m.appNamespace, m.app = appRef(raw)
		return m, true
	}
	return mark{}, false
}

// copiedMark says the tracking annotation names another object. A
// Deployment's ReplicaSets carry its annotations, Argo CD's included: Argo CD
// manages the object the mark names, not the one it is on.
func copiedMark(m mark, t target, object unstructured.Unstructured) bool {
	return m.via == "annotation" && m.kind != "" &&
		(m.group != t.gvr.Group || m.kind != t.kind || m.name != object.GetName() || (m.namespace != "" && m.namespace != object.GetNamespace()))
}

// appRef splits `<namespace>_<name>`, Argo CD's spelling for an Application
// outside its own namespace. Neither part may hold `_`, so the split is exact.
func appRef(s string) (string, string) {
	if ns, name, ok := strings.Cut(s, "_"); ok {
		return ns, name
	}
	return "", s
}

func track(r *http.Request, h extension.Host, cluster string, t target, object unstructured.Unstructured) Tracked {
	return trackWithin(r, h, cluster, t, object, MaxOwnerHops)
}

// trackWithin is track with the owner hops left for the walk up the
// controller chain (owner.go).
func trackWithin(r *http.Request, h extension.Host, cluster string, t target, object unstructured.Unstructured, hops int) Tracked {
	out := Tracked{Kind: t.kind, Namespace: object.GetNamespace(), Name: object.GetName(), Drift: Drift{Fields: []drift.Field{}}}
	m, ok := markOf(object)
	if !ok {
		// Unmarked: Argo CD may still manage it through its owner.
		if through, found := throughOwner(r, h, cluster, t, object, hops); found {
			return through
		}
		out.Note = "Nothing on this " + t.kind + " names an Argo CD Application: it carries neither " + TrackingAnnotation + " nor " + InstanceLabel + " outside a Helm release, and none of its owners is an object an Argo CD Application lists."
		return out
	}
	out.Via, out.Mark = m.via, m.raw

	if copiedMark(m, t, object) {
		// A ReplicaSet carries its Deployment's mark: answer through the owner
		// that is Argo CD's, when there is one.
		if through, found := throughOwner(r, h, cluster, t, object, hops); found {
			return through
		}
		out.Copied = true
		out.Note = "The tracking annotation names another object (" + m.raw + "): it was copied onto this one, and Argo CD does not manage it."
		return out
	}

	application, resource := findApplication(r, h, cluster, m, t, object, &out)
	if m.via == "label" && resource == nil {
		// A label alone is Helm's as often as Argo CD's: only an Application
		// listing the object says Argo CD manages it.
		if out.ApplicationCode == "" || out.ApplicationCode == CodeApplicationNotFound {
			// A chart's Pods and ReplicaSets carry the release label; the
			// Deployment above them is what Argo CD lists.
			if through, found := throughOwner(r, h, cluster, t, object, hops); found {
				return through
			}
			out.ApplicationCode, out.ApplicationNote = "", ""
			out.Note = "The label " + InstanceLabel + "=" + m.raw + " names no Argo CD Application that lists this " + t.kind + ": it is not Argo CD's, or Argo CD tracks by annotation and this label is Helm's."
			return out
		}
	}
	out.Managed = true
	out.Application, out.Resource = application, resource
	if application != nil {
		if application.Operation != nil && application.Operation.Revision != "" {
			out.SyncedRevision = application.Operation.Revision
		} else if len(application.History) > 0 {
			out.SyncedRevision = application.History[0].Revision
		}
	}

	out.Drift = driftOf(object, m)
	out.Note = verdict(out)
	// Argo CD's own diff, only once the person's read of the Application and
	// its listing of the object stood.
	if application != nil && resource != nil {
		diff := argoDiff(r, h, cluster, application, t, object)
		out.ArgoDiff = &diff
	}
	return out
}

// findApplication reads the Application the mark names, for the person, and
// its entry for the object. A failure is written on out and is not the route's:
// the drift needs only the object.
func findApplication(r *http.Request, h extension.Host, cluster string, m mark, t target, object unstructured.Unstructured, out *Tracked) (*ApplicationDetail, *Resource) {
	registry, err := h.Definitions(r, cluster)
	if err != nil {
		out.ApplicationCode, out.ApplicationNote = CodeCatalogueUnreadable, "The custom resource catalog could not be read, so the Application was not."
		return nil, nil
	}
	crd, err := registry.Lookup(r.Context(), ApplicationKind)
	switch {
	case errors.Is(err, extension.ErrUnknownKind):
		out.ApplicationCode, out.ApplicationNote = CodeKindUnknown, "This cluster serves no "+ApplicationKind+": the object is marked, and Argo CD is not installed here."
		return nil, nil
	case err != nil:
		out.ApplicationCode, out.ApplicationNote = CodeKindUnresolved, "Resolving "+ApplicationKind+" failed."
		return nil, nil
	}

	page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR(), Namespace: m.appNamespace, FieldSelector: "metadata.name=" + m.app})
	// Sorted as writeRefused sorts them; here each is a note, since the drift
	// below stands without the Application.
	const unknownSync = ", so the sync word is not known; the drift below is read from the object alone."
	switch {
	case errors.Is(err, extension.ErrDenied):
		out.ApplicationCode = CodeListForbidden
		out.ApplicationNote = capital(whoPerson) + " may not read Argo CD's Applications" + unknownSync
		return nil, nil
	case errors.Is(err, extension.ErrUnreviewable):
		out.ApplicationCode = CodeListUnreviewed
		out.ApplicationNote = "The console could not ask the cluster whether you may read Argo CD's Applications" + unknownSync
		return nil, nil
	case apierrors.IsForbidden(err):
		out.ApplicationCode = CodeListForbidden
		out.ApplicationNote = capital(whoConsole) + " may not read Argo CD's Applications" + unknownSync
		out.Subject = subjectOf(err)
		return nil, nil
	}
	if err != nil {
		out.ApplicationCode, out.ApplicationNote = CodeListFailed, "The Application could not be read."
		return nil, nil
	}

	var candidates []ApplicationDetail
	for i := range page.Items {
		if page.Items[i].GetName() == m.app && (m.appNamespace == "" || page.Items[i].GetNamespace() == m.appNamespace) {
			candidates = append(candidates, applicationDetail(page.Items[i]))
		}
	}
	// The one whose resources list the object; failing that, the only one.
	for i := range candidates {
		if res := listed(candidates[i].Resources, t, object); res != nil {
			return &candidates[i], res
		}
	}
	switch len(candidates) {
	case 0:
		out.ApplicationCode = CodeApplicationNotFound
		out.ApplicationNote = "There is no Application " + qualified(m.appNamespace, m.app) + " on this cluster that this console may read."
		return nil, nil
	case 1:
		out.ApplicationNote = "The Application " + qualified(candidates[0].Namespace, m.app) + " does not list this " + t.kind + " among its resources: it is being created or pruned, or Argo CD has not refreshed since."
		return &candidates[0], nil
	default:
		out.ApplicationCode = CodeKindAmbiguous
		out.ApplicationNote = "Several Applications are named " + m.app + " and none lists this " + t.kind + ", so which one manages it is not known."
		return nil, nil
	}
}

func listed(resources []Resource, t target, object unstructured.Unstructured) *Resource {
	for i := range resources {
		res := resources[i]
		if res.Group == t.gvr.Group && res.Kind == t.kind && res.Name == object.GetName() && res.Namespace == object.GetNamespace() {
			return &res
		}
	}
	return nil
}

// driftOf compares the object with what Argo CD last applied to it.
func driftOf(object unstructured.Unstructured, m mark) Drift {
	out := Drift{Fields: []drift.Field{}, Masked: object.GetKind() == "Secret" && object.GroupVersionKind().Group == ""}
	raw, ok := object.GetAnnotations()[drift.LastApplied]
	if !ok || raw == "" {
		out.Note = "This object carries no " + drift.LastApplied + ": Argo CD applied it server-side (ServerSideApply=true), so what it applied is not on the object. Argo CD's own diff will show it, once this page may ask argocd-server."
		return out
	}
	var applied map[string]any
	if err := json.Unmarshal([]byte(raw), &applied); err != nil {
		out.Note = "The object's " + drift.LastApplied + " is not JSON, so it was not compared."
		return out
	}
	// Argo CD writes its mark into what it applies; a last-applied without it
	// comes from a later manual `kubectl apply`.
	if !wroteBy(applied, m) {
		out.Note = "The object's " + drift.LastApplied + " was not written by Argo CD - it lacks Argo CD's mark - so somebody applied this object by hand since, and it is not compared."
		return out
	}
	out.Compared = true
	out.Fields = drift.Compare(applied, object.Object, drift.Options{Mask: out.Masked})
	return out
}

func wroteBy(applied map[string]any, m mark) bool {
	if m.via == "annotation" {
		value, _, _ := unstructured.NestedString(applied, "metadata", "annotations", TrackingAnnotation)
		return value == m.raw
	}
	value, _, _ := unstructured.NestedString(applied, "metadata", "labels", InstanceLabel)
	return value == m.raw
}

// verdict is the sentence above the table: what the gap means against Argo's
// own word for the resource.
func verdict(t Tracked) string {
	if t.Resource == nil || !t.Drift.Compared {
		return ""
	}
	changed := len(t.Drift.Fields) > 0
	switch t.Resource.Status {
	case "OutOfSync":
		if !changed {
			return "Argo CD finds this resource different from git, and nothing changed on it since Argo CD last applied it: the difference is in git, not yet synced" + revisions(t) + "."
		}
		return "Argo CD finds this resource different from git, and the fields below changed on it since Argo CD last applied it" + revisions(t) + "."
	case "Synced":
		if changed {
			return "Argo CD calls this resource Synced, and the fields below differ from what it applied: its ignoreDifferences may exclude them, or it has not compared since they changed."
		}
	}
	return ""
}

func revisions(t Tracked) string {
	if t.Application == nil || t.Application.SyncRevision == "" || t.SyncedRevision == "" || t.SyncedRevision == t.Application.SyncRevision {
		return ""
	}
	return " (git is at " + short(t.Application.SyncRevision) + ", last applied " + short(t.SyncedRevision) + ")"
}

func short(revision string) string {
	if len(revision) > 7 {
		return revision[:7]
	}
	return revision
}

func qualified(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "/" + name
}

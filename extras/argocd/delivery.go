// Package argocd is the Delivery Center: KubeTower's Argo CD and Flux CD page,
// as an extension of the console.
//
// It never reaches into the cluster on its own: every read goes through
// `extension.Host`, the only door the core opens, and nothing here imports
// `internal/`.
package argocd

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/thonicdev/kubetower/extension"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Key is the extension's key, `argocd`. It is the page's URL segment, the
// `KT_EXTRAS` name and the key the per-cluster switch is stored under, so
// renaming it turns the page off silently unless the old key goes in `Legacy`.
const Key = "argocd"

// ApplicationKind is the qualified kind `Host.Definitions` resolves. Bare
// `applications` would be ambiguous as soon as a second group defines that
// plural.
const ApplicationKind = "applications.argoproj.io"

// Delivery is the extension `cmd/kubetower` registers.
type Delivery struct{}

// New is the constructor `cmd/kubetower` calls.
func New() Delivery { return Delivery{} }

func (Delivery) Key() string   { return Key }
func (Delivery) Label() string { return "Delivery Center" }

func (Delivery) Summary() string {
	return "Argo CD Applications and Flux CD Kustomizations and HelmReleases: sync, health, history, and what is being deleted."
}

// Needs is drawn beside the switch. It names the CRDs rather than the product,
// because they are what the page asks the cluster for.
func (Delivery) Needs() string {
	return "Argo CD, serving " + ApplicationKind + ", or Flux CD, serving " + KustomizationKind + " or " + HelmReleaseKind
}

// Legacy is empty: this page has only ever been published under one key.
func (Delivery) Legacy() []string { return nil }

// Routes are the page's reads; none writes.
func (d Delivery) Routes(h extension.Host) []extension.Route {
	return []extension.Route{
		{Method: http.MethodGet, Pattern: "/{$}", Handler: d.applications(h)},
		{Method: http.MethodGet, Pattern: "/applications/{namespace}/{name}", Handler: d.application(h)},
		{Method: http.MethodGet, Pattern: "/flux", Handler: d.flux(h)},
		{Method: http.MethodGet, Pattern: "/report", Handler: d.report(h)},
		// The page's own settings: which tool, and where each UI is.
		{Method: http.MethodGet, Pattern: "/settings", Handler: d.settings(h)},
		// One object as Argo CD sees it, for the console's object panel.
		{Method: http.MethodGet, Pattern: "/resource", Handler: d.resource(h)},
		// The Argo CD column of the console's lists: one read per list.
		{Method: http.MethodGet, Pattern: "/column", Handler: d.column(h)},
		// Ask Argo CD to compare one Application again (refresh.go).
		{Method: http.MethodPost, Pattern: "/applications/{namespace}/{name}/refresh", Handler: d.refresh(h)},
	}
}

// Application is one row.
type Application struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Sync and Health are Argo CD's own words (`Synced`, `OutOfSync`, ...),
	// untranslated so the page agrees with every other Argo CD surface.
	Sync   string `json:"sync"`
	Health string `json:"health"`
	// Deleting is `metadata.deletionTimestamp` being set. It is separate from
	// Health because a stuck finalizer leaves an Application `Healthy` and
	// `Synced` while it is being deleted.
	Deleting bool `json:"deleting"`

	// Project is `spec.project`; Destination is where Argo CD applies it.
	Project     string      `json:"project,omitempty"`
	Destination Destination `json:"destination"`

	// OutOfSyncCount is how many entries of `status.resources` are
	// `OutOfSync`; the names are on the Application's own page.
	OutOfSyncCount int `json:"outOfSyncCount"`
	// Condition is the first condition of an error type (`ComparisonError`,
	// `SyncError`, ...), usually why the sync word is `Unknown`.
	Condition *Condition `json:"condition,omitempty"`

	// Source is the first source of a multi-source Application, and Sources
	// how many there are.
	Source        Source    `json:"source"`
	Sources       int       `json:"sources"`
	Revision      string    `json:"revision,omitempty"`
	AutomatedSync bool      `json:"automatedSync"`
	HealthMessage string    `json:"healthMessage,omitempty"`
	LastSync      *LastSync `json:"lastSync,omitempty"`
}

// LastSync is `status.operationState` without its per-resource results: the
// phase, when it started and ended, and Argo CD's message when it did not
// succeed.
type LastSync struct {
	Phase      string `json:"phase"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Message    string `json:"message,omitempty"`
}

// answer is what the route returns. `Complete` and `Note` travel with the rows
// so the page can tell an incomplete list from a complete one.
type answer struct {
	Applications []Application `json:"applications"`
	Complete     bool          `json:"complete"`
	Note         string        `json:"note,omitempty"`
}

// applications lists Argo CD Applications for one cluster.
func (d Delivery) applications(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")

		crd, ok := resolve(w, r, h, cluster, ApplicationKind, "Argo CD")
		if !ok {
			return
		}

		page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR()})
		// The kind resolved, so Argo CD is installed: a refusal is an answer
		// about who may read it, never a 502.
		if writeRefused(w, err, func(who string) string {
			return "Argo CD is installed on this cluster, and " + who + " may not list its Applications."
		}) {
			return
		}
		if err != nil {
			writeProblem(w, http.StatusBadGateway, CodeListFailed, "The Applications could not be read.", err)
			return
		}

		out := answer{
			Applications: make([]Application, 0, len(page.Items)),
			Complete:     page.Complete,
			Note:         page.Note,
		}
		for i := range page.Items {
			out.Applications = append(out.Applications, applicationFrom(page.Items[i]))
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// applicationFrom reads one object. Every field is optional: a new Application
// has no `status`, and an absent or mistyped field reads as an empty word,
// never a guessed one.
func applicationFrom(obj unstructured.Unstructured) Application {
	app := Application{
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
		Deleting:  obj.GetDeletionTimestamp() != nil,
	}
	app.Sync = nestedString(obj, "status", "sync", "status")
	app.Health = nestedString(obj, "status", "health", "status")

	app.Project = nestedString(obj, "spec", "project")
	app.Destination = destinationOf(obj)
	status := nestedMap(obj.Object, "status")
	for _, raw := range slice(status, "resources") {
		if str(raw, "status") == "OutOfSync" {
			app.OutOfSyncCount++
		}
	}
	for _, raw := range slice(status, "conditions") {
		if kind := str(raw, "type"); strings.HasSuffix(kind, "Error") {
			app.Condition = &Condition{Type: kind, Message: str(raw, "message"), Error: true}
			break
		}
	}

	app.Source, app.Sources = sourceOf(nestedMap(obj.Object, "spec", "source")), 1
	if app.Source == (Source{}) {
		// A multi-source Application has `spec.sources` and no `spec.source`:
		// its first source is drawn and the count said, as the report does.
		sources, _, _ := unstructured.NestedSlice(obj.Object, "spec", "sources")
		app.Sources = len(sources)
		if first, ok := firstMap(sources); ok {
			app.Source = sourceOf(first)
		}
	}
	app.Revision = nestedString(obj, "status", "sync", "revision")
	_, app.AutomatedSync, _ = unstructured.NestedMap(obj.Object, "spec", "syncPolicy", "automated")
	app.HealthMessage = nestedString(obj, "status", "health", "message")
	if op := nestedMap(obj.Object, "status", "operationState"); op != nil {
		app.LastSync = &LastSync{
			Phase:      str(op, "phase"),
			StartedAt:  str(op, "startedAt"),
			FinishedAt: str(op, "finishedAt"),
		}
		if phase := app.LastSync.Phase; phase != "Succeeded" {
			app.LastSync.Message = str(op, "message")
		}
	}
	return app
}

func firstMap(items []any) (map[string]any, bool) {
	if len(items) == 0 {
		return nil, false
	}
	m, ok := items[0].(map[string]any)
	return m, ok
}

// nestedString reads a string field, empty when it is absent or not a string.
func nestedString(obj unstructured.Unstructured, fields ...string) string {
	value, found, err := unstructured.NestedString(obj.Object, fields...)
	if !found || err != nil {
		return ""
	}
	return value
}

// problem is the failure body. `Code` is stable and is what the page branches
// on; `Message` is the sentence it prints, which may be reworded in any
// release.
type problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
	// Subject is the account the cluster refused, on `list_forbidden` only:
	// the console's ServiceAccount and a signed-in person are granted the read
	// in two different places, and the page says which.
	Subject string `json:"subject,omitempty"`
}

// writeForbidden is the 403 every route answers when the cluster refused the
// read: an answer about the cluster's RBAC, carrying who was refused.
func writeForbidden(w http.ResponseWriter, message string, err error) {
	writeJSON(w, http.StatusForbidden, problem{
		Code:    CodeListForbidden,
		Message: message,
		Detail:  err.Error(),
		Subject: subjectOf(err),
	})
}

// The two accounts a refused read is about, as sentence names whom.
const (
	// whoConsole is the account the console reads with: its ServiceAccount in
	// a cluster, the kubeconfig's user on a desktop.
	whoConsole = "the account this console read it with"
	// whoPerson is the person the read was reviewed for.
	whoPerson = "you"
)

// writeRefused answers a read refused rather than failed, and reports whether
// it wrote. sentence says what was refused to whom.
//
// Told apart with errors.Is, as the extension contract says, and in this
// order, because the person's refusal also reads as apierrors.IsForbidden:
//
//   - extension.ErrDenied: the access review says the person may not. 403
//     list_forbidden, about them, naming no account the page could grant;
//   - extension.ErrUnreviewable: the console could not ask. 503
//     list_unreviewed, a failure to ask that a retry may change;
//   - any other 403: the cluster refused the console's own read. 403
//     list_forbidden, naming the account it refused.
//
// Anything else is the caller's: a read that failed.
func writeRefused(w http.ResponseWriter, err error, sentence func(who string) string) bool {
	if refused, ok := extension.RefusedRead(err, sentence(whoPerson)); ok {
		writeJSON(w, refused.Status, problem{Code: refused.Code, Message: refused.Message, Detail: err.Error()})
		return true
	}
	if apierrors.IsForbidden(err) {
		writeForbidden(w, sentence(whoConsole), err)
		return true
	}
	return false
}

// capital is s with its first letter upper case, for a sentence that opens
// with whom it is about.
func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// refusedUser is how the API server's authorizer names the subject it refused:
// `User "system:serviceaccount:kubetower:kubetower" cannot list resource …`.
var refusedUser = regexp.MustCompile(`User "([^"]+)" cannot`)

// subjectOf is the refused account, or empty when the sentence does not name
// one. Empty is drawn as "the account named above", never guessed.
func subjectOf(err error) string {
	if m := refusedUser.FindStringSubmatch(err.Error()); m != nil {
		return m[1]
	}
	return ""
}

// The codes are protocol, not display text: the front end in `web/` names each
// of them, so renaming one changes both halves.
const (
	// CodeKindUnknown: no CRD answers to the kind; Argo CD is not installed.
	CodeKindUnknown = "kind_unknown"
	// CodeKindNotServed: the CRD is there and every version is switched off.
	CodeKindNotServed = "kind_not_served"
	// CodeKindAmbiguous: two groups define the plural.
	CodeKindAmbiguous = "kind_ambiguous"
	// CodeCatalogueUnreadable, CodeKindUnresolved and CodeListFailed are
	// failures to ask, as opposed to the answers above.
	CodeCatalogueUnreadable = "catalogue_unreadable"
	CodeKindUnresolved      = "kind_unresolved"
	CodeListFailed          = "list_failed"
	// CodeListForbidden: the kind resolved and the cluster refused the list,
	// so Argo CD is installed and the read has to be granted - to the person
	// asking, or to the account the console reads with.
	CodeListForbidden = extension.CodeListForbidden
	// CodeListUnreviewed: the console could not ask the cluster whether the
	// person may make the read, so it made none. A failure to ask, which a
	// retry may change, and never a 502: the cluster was not read.
	CodeListUnreviewed = extension.CodeListUnreviewed
	// CodeApplicationNotFound: no readable Application of that name. Absent
	// and unreadable give the same answer, so it reveals nothing about
	// existence.
	CodeApplicationNotFound = "application_not_found"
	// CodeClusterUnknown: the cluster in the address is not a known context.
	// It is a 404, never a 502, because no API server was contacted.
	CodeClusterUnknown = "cluster_unknown"
)

// unknownCluster answers 404 when err says the cluster is not one the console
// knows, and reports whether it wrote. Call it before any 502: the gate passes
// an unknown name through so the page can say so.
func unknownCluster(w http.ResponseWriter, cluster string, err error) bool {
	if !errors.Is(err, extension.ErrUnknownCluster) {
		return false
	}
	writeProblem(w, http.StatusNotFound, CodeClusterUnknown, extension.UnknownClusterText(cluster), err)
	return true
}

func writeProblem(w http.ResponseWriter, status int, code, message string, err error) {
	body := problem{Code: code, Message: message}
	if err != nil {
		body.Detail = err.Error()
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// The status line is already written, so an encoding error has nowhere to
	// go.
	_ = json.NewEncoder(w).Encode(body)
}

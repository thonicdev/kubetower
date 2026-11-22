package argocd_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/extension"
	argocd "github.com/thonicdev/kubetower/extras/argocd"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// The routes beyond the list: Flux CD's and one Application read whole,
// against a host that serves several kinds at once.

var (
	kustomizations = extension.CRD{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Plural: "kustomizations", Namespaced: true}
	helmReleases   = extension.CRD{Group: "helm.toolkit.fluxcd.io", Version: "v2", Plural: "helmreleases", Namespaced: true}
	applications   = extension.CRD{Group: "argoproj.io", Version: "v1alpha1", Plural: "applications", Namespaced: true}
)

func TestFluxIsReadInItsOwnWordsFromBothReconcilers(t *testing.T) {
	host := newMultiHost(kustomizations, helmReleases)
	host.pages["kustomizations"] = []unstructured.Unstructured{
		fluxObject("Kustomization", "flux-system", "apps", "True", "ReconciliationSucceeded", false, "main@sha1:abc"),
		fluxObject("Kustomization", "flux-system", "infra", "False", "BuildFailed", true, ""),
	}
	host.pages["helmreleases"] = []unstructured.Unstructured{
		fluxObject("HelmRelease", "podinfo", "podinfo", "Unknown", "Progressing", false, ""),
	}

	body, status := serveAt(t, host, "/flux")
	if status != http.StatusOK {
		t.Fatalf("status %d, body %s", status, body)
	}
	var got struct {
		Objects  []argocd.FluxObject `json:"objects"`
		Complete bool                `json:"complete"`
		Note     string              `json:"note"`
	}
	decode(t, body, &got)
	if len(got.Objects) != 3 || !got.Complete || got.Note != "" {
		t.Fatalf("got %+v, want three objects, complete, no note", got)
	}
	apps := got.Objects[0]
	if apps.Kind != "Kustomization" || apps.Ready != "True" || apps.Reason != "ReconciliationSucceeded" || apps.Revision != "main@sha1:abc" {
		t.Errorf("apps = %+v, want Flux's own Ready/Reason/revision", apps)
	}
	if infra := got.Objects[1]; infra.Ready != "False" || !infra.Suspended {
		t.Errorf("infra = %+v, want Ready False and suspended", infra)
	}
	if hr := got.Objects[2]; hr.Kind != "HelmRelease" || hr.Ready != "Unknown" {
		t.Errorf("helmrelease = %+v, want Flux's Unknown kept as Unknown", hr)
	}
}

func TestFluxWithOneReconcilerListsItAndNamesTheOther(t *testing.T) {
	host := newMultiHost(kustomizations)
	host.pages["kustomizations"] = []unstructured.Unstructured{
		fluxObject("Kustomization", "flux-system", "apps", "True", "ReconciliationSucceeded", false, ""),
	}
	body, status := serveAt(t, host, "/flux")
	if status != http.StatusOK {
		t.Fatalf("status %d, body %s", status, body)
	}
	var got struct {
		Objects []argocd.FluxObject `json:"objects"`
		Note    string              `json:"note"`
	}
	decode(t, body, &got)
	if len(got.Objects) != 1 || !strings.Contains(got.Note, argocd.HelmReleaseKind) {
		t.Errorf("got %+v, want the Kustomization and a note naming the absent HelmReleases", got)
	}
}

func TestFluxAbsentIsNotInstalledAndNeverAnEmptyList(t *testing.T) {
	body, status := serveAt(t, newMultiHost(applications), "/flux")
	if status != http.StatusNotFound {
		t.Fatalf("status %d, want 404, body %s", status, body)
	}
	var got struct{ Code, Message string }
	decode(t, body, &got)
	if got.Code != "kind_unknown" || !strings.Contains(got.Message, "Flux CD is not installed") {
		t.Errorf("got %+v, want kind_unknown saying Flux CD is not installed", got)
	}
}

func TestFluxRefusedNamesWhoWasRefused(t *testing.T) {
	host := newMultiHost(kustomizations, helmReleases)
	host.listErr["kustomizations"] = forbidden("kustomize.toolkit.fluxcd.io", "kustomizations")
	body, status := serveAt(t, host, "/flux")
	if status != http.StatusForbidden {
		t.Fatalf("status %d, want 403, body %s", status, body)
	}
	var got struct{ Code, Subject string }
	decode(t, body, &got)
	if got.Code != "list_forbidden" || got.Subject != "system:serviceaccount:kubetower:kubetower" {
		t.Errorf("got %+v, want list_forbidden naming the ServiceAccount", got)
	}
}

func TestOneApplicationIsReadWholeAndEachResourceIsAddressed(t *testing.T) {
	host := newMultiHost(applications, extension.CRD{Group: "monitoring.coreos.com", Version: "v1", Plural: "servicemonitors", Kind: "ServiceMonitor", Namespaced: true})
	host.discovery = map[string][]metav1.APIResource{
		"apps/v1": {{Name: "deployments", Kind: "Deployment"}, {Name: "deployments/scale", Kind: "Scale"}},
		"v1":      {{Name: "services", Kind: "Service"}},
		"networking.k8s.io/v1": {
			{Name: "networkpolicies", Kind: "NetworkPolicy"},
		},
		"monitoring.coreos.com/v1": {{Name: "servicemonitors", Kind: "ServiceMonitor"}},
	}
	host.pages["applications"] = []unstructured.Unstructured{guestbook()}

	body, status := serveAt(t, host, "/applications/argocd/guestbook")
	if status != http.StatusOK {
		t.Fatalf("status %d, body %s", status, body)
	}
	if first := host.queries[0]; first.Namespace != "argocd" || first.FieldSelector != "metadata.name=guestbook" {
		t.Errorf("asked %+v, want the one Application by namespace and field selector", first)
	}
	var got argocd.ApplicationDetail
	decode(t, body, &got)

	if got.Sync != "OutOfSync" || got.Health != "Degraded" || got.HealthMessage == "" {
		t.Errorf("sync/health = %q/%q (%q), want Argo's words and message", got.Sync, got.Health, got.HealthMessage)
	}
	if got.Source.RepoURL != "https://github.com/argoproj/argocd-example-apps.git" || got.Destination.Namespace != "guestbook" {
		t.Errorf("source/destination = %+v / %+v", got.Source, got.Destination)
	}
	if len(got.History) != 2 || got.History[0].ID != 2 || got.History[0].Revision != "bbb" {
		t.Errorf("history = %+v, want two entries, newest first", got.History)
	}
	if got.Operation == nil || got.Operation.Phase != "Failed" || !got.Operation.Automated || len(got.Operation.Results) != 1 {
		t.Errorf("operation = %+v, want the failed automated sync and its one result", got.Operation)
	}
	if len(got.Conditions) != 1 || !got.Conditions[0].Error {
		t.Errorf("conditions = %+v, want one error condition", got.Conditions)
	}

	paths := map[string]string{}
	for _, res := range got.Resources {
		paths[res.Kind] = res.Path
	}
	for kind, want := range map[string]string{
		"Deployment":     "deployments",
		"Service":        "services",
		"NetworkPolicy":  "networkpolicies",
		"ServiceMonitor": "servicemonitors.monitoring.coreos.com",
	} {
		if paths[kind] != want {
			t.Errorf("%s path = %q, want %q (the console's own address)", kind, paths[kind], want)
		}
	}
	if got.Resources[0].Status != "OutOfSync" {
		t.Errorf("first resource status = %q, want Argo's OutOfSync carried", got.Resources[0].Status)
	}
	if got.ResourcesNote != "" {
		t.Errorf("note %q, want none when every kind was found", got.ResourcesNote)
	}
}

// The list carries what the page's tile draws: how many resources are out of
// sync, the error condition that makes a sync word Unknown, the project, and
// where Argo CD applies it. The names are the
// Application's own page.
func TestTheListCountsWhatIsOutOfSyncAndSaysWhyAndWhere(t *testing.T) {
	host := newMultiHost(applications)

	// Ten ConfigMaps out of sync and one Synced: ten counted.
	many := guestbook()
	many.SetName("many")
	resources := []any{map[string]any{"version": "v1", "kind": "ConfigMap", "namespace": "a", "name": "kept", "status": "Synced"}}
	for i := range 10 {
		resources = append(resources, map[string]any{"version": "v1", "kind": "ConfigMap", "namespace": "a", "name": fmt.Sprintf("cm-%d", i), "status": "OutOfSync"})
	}
	if err := unstructured.SetNestedSlice(many.Object, resources, "status", "resources"); err != nil {
		t.Fatal(err)
	}

	broken := application("argocd", "broken", "Unknown", "Healthy", false)
	if err := unstructured.SetNestedSlice(broken.Object, []any{
		map[string]any{"type": "SharedResourceWarning", "message": "not an error"},
		map[string]any{"type": "ComparisonError", "message": "app path does not exist"},
	}, "status", "conditions"); err != nil {
		t.Fatal(err)
	}
	// Another cluster, named: the destination is carried as Argo CD wrote it.
	remote := application("argocd", "remote", "Synced", "Healthy", false)
	if err := unstructured.SetNestedMap(remote.Object, map[string]any{"name": "prod-eu", "namespace": "shop"}, "spec", "destination"); err != nil {
		t.Fatal(err)
	}
	// Multi-source: no spec.source, the first of spec.sources and the count.
	multi := application("argocd", "multi", "Synced", "Healthy", false)
	if err := unstructured.SetNestedSlice(multi.Object, []any{
		map[string]any{"repoURL": "https://charts.example.com", "chart": "web", "targetRevision": "1.2.3"},
		map[string]any{"repoURL": "https://git.example.com/values.git", "ref": "values"},
	}, "spec", "sources"); err != nil {
		t.Fatal(err)
	}
	host.pages["applications"] = []unstructured.Unstructured{guestbook(), many, broken, remote, multi}

	body, status := serveAt(t, host, "/")
	if status != http.StatusOK {
		t.Fatalf("status %d, body %s", status, body)
	}
	var got struct {
		Applications []argocd.Application `json:"applications"`
	}
	decode(t, body, &got)
	byName := map[string]argocd.Application{}
	for _, app := range got.Applications {
		byName[app.Name] = app
	}

	g := byName["guestbook"]
	if g.OutOfSyncCount != 1 {
		t.Errorf("guestbook out of sync = %d, want its one Deployment and none of its three Synced", g.OutOfSyncCount)
	}
	if g.Condition == nil || g.Condition.Type != "SyncError" {
		t.Errorf("guestbook condition = %+v, want its SyncError", g.Condition)
	}
	if g.Project != "default" || g.Destination.Server != "https://kubernetes.default.svc" || g.Destination.Namespace != "guestbook" {
		t.Errorf("guestbook project %q, destination %+v, want default and the in-cluster guestbook", g.Project, g.Destination)
	}

	if m := byName["many"]; m.OutOfSyncCount != 10 {
		t.Errorf("many: count %d, want 10", m.OutOfSyncCount)
	}

	b := byName["broken"]
	if b.Condition == nil || b.Condition.Type != "ComparisonError" || b.Condition.Message != "app path does not exist" {
		t.Errorf("broken condition = %+v, want the first *Error condition, not the warning before it", b.Condition)
	}
	if b.OutOfSyncCount != 0 || b.Project != "" || b.Destination != (argocd.Destination{}) {
		t.Errorf("broken = %+v, want nothing counted and no project or destination invented", b)
	}

	if r := byName["remote"]; r.Destination.Name != "prod-eu" || r.Destination.Namespace != "shop" || r.Destination.Server != "" {
		t.Errorf("remote destination = %+v, want prod-eu/shop as written", r.Destination)
	}

	// The tile's detail: source, revision, policy, health's reason, last sync.
	if g.Source.RepoURL != "https://github.com/argoproj/argocd-example-apps.git" || g.Source.Path != "guestbook" || g.Sources != 1 {
		t.Errorf("guestbook source %+v (%d), want the example repository's guestbook path, one source", g.Source, g.Sources)
	}
	if g.Revision != "bbb" || !g.AutomatedSync || g.HealthMessage != "Deployment has 0 of 1 pods available" {
		t.Errorf("guestbook revision %q automated %v health message %q", g.Revision, g.AutomatedSync, g.HealthMessage)
	}
	if g.LastSync == nil || g.LastSync.Phase != "Failed" || g.LastSync.Message != "one or more objects failed to apply" || g.LastSync.StartedAt == "" {
		t.Errorf("guestbook last sync %+v, want the failed operation with its message", g.LastSync)
	}
	if b.LastSync != nil || b.AutomatedSync || b.Sources != 0 || b.Source != (argocd.Source{}) {
		t.Errorf("broken = %+v, want no sync, policy or source invented", b)
	}

	if m := byName["multi"]; m.Sources != 2 || m.Source.Chart != "web" || m.Source.TargetRevision != "1.2.3" {
		t.Errorf("multi source %+v (%d), want the first source and a count of 2", m.Source, m.Sources)
	}
	if strings.Contains(string(body), "outOfSync\"") || strings.Contains(string(body), "resourcesNote") {
		t.Errorf("the list still carries resource names: %s", body)
	}
}

// A row links its resource where the console's menu does:
// the bare plural for a kind the console types by hand, and `plural.group` for
// a Kubernetes project kind it does not - an APIService the API server serves
// itself, a Gateway its CRD serves. The bare `apiservices` is an address the
// console answers "unknown kind", and the bare `gateways` one no menu row holds.
func TestAnApplicationsRowsLinkEachKindWhereTheMenuDoes(t *testing.T) {
	host := newMultiHost(applications)
	host.discovery = map[string][]metav1.APIResource{
		"apps/v1":                      {{Name: "deployments", Kind: "Deployment"}, {Name: "controllerrevisions", Kind: "ControllerRevision"}},
		"v1":                           {{Name: "podtemplates", Kind: "PodTemplate"}},
		"apiregistration.k8s.io/v1":    {{Name: "apiservices", Kind: "APIService"}},
		"gateway.networking.k8s.io/v1": {{Name: "gateways", Kind: "Gateway"}, {Name: "httproutes", Kind: "HTTPRoute"}},
		"networking.k8s.io/v1":         {{Name: "ingresses", Kind: "Ingress"}},
		"events.k8s.io/v1":             {{Name: "events", Kind: "Event"}},
	}
	// What the console's catalog resolves, at the segment it resolves it
	// (Registry.Definition): the project kinds the menu offers. Not
	// events.k8s.io's Events, which the menu excludes and Definition answers
	// "unknown kind" for.
	for segment, crd := range map[string]extension.CRD{
		"podtemplates":                         {Version: "v1", Plural: "podtemplates", Kind: "PodTemplate", Namespaced: true},
		"controllerrevisions.apps":             {Group: "apps", Version: "v1", Plural: "controllerrevisions", Kind: "ControllerRevision", Namespaced: true},
		"apiservices.apiregistration.k8s.io":   {Group: "apiregistration.k8s.io", Version: "v1", Plural: "apiservices", Kind: "APIService"},
		"gateways.gateway.networking.k8s.io":   {Group: "gateway.networking.k8s.io", Version: "v1", Plural: "gateways", Kind: "Gateway", Namespaced: true},
		"httproutes.gateway.networking.k8s.io": {Group: "gateway.networking.k8s.io", Version: "v1", Plural: "httproutes", Kind: "HTTPRoute", Namespaced: true},
	} {
		host.crds[segment] = crd
	}
	app := guestbook()
	resource := func(group, kind, name string) map[string]any {
		return map[string]any{"group": group, "version": "v1", "kind": kind, "namespace": "guestbook", "name": name, "status": "Synced"}
	}
	if err := unstructured.SetNestedSlice(app.Object, []any{
		resource("apps", "Deployment", "guestbook-ui"),
		resource("apps", "ControllerRevision", "guestbook-ui-1"),
		resource("", "PodTemplate", "guestbook"),
		resource("apiregistration.k8s.io", "APIService", "v1beta1.metrics.k8s.io"),
		resource("gateway.networking.k8s.io", "Gateway", "edge"),
		resource("gateway.networking.k8s.io", "HTTPRoute", "guestbook"),
		resource("networking.k8s.io", "Ingress", "guestbook"),
		resource("events.k8s.io", "Event", "guestbook.1"),
	}, "status", "resources"); err != nil {
		t.Fatal(err)
	}
	host.pages["applications"] = []unstructured.Unstructured{app}

	body, status := serveAt(t, host, "/applications/argocd/guestbook")
	if status != http.StatusOK {
		t.Fatalf("status %d, body %s", status, body)
	}
	var got argocd.ApplicationDetail
	decode(t, body, &got)
	paths := map[string]string{}
	for _, res := range got.Resources {
		paths[res.Kind] = res.Path
	}
	for kind, want := range map[string]string{
		"Deployment":         "deployments",
		"Ingress":            "ingresses",
		"PodTemplate":        "podtemplates",
		"ControllerRevision": "controllerrevisions.apps",
		"APIService":         "apiservices.apiregistration.k8s.io",
		"Gateway":            "gateways.gateway.networking.k8s.io",
		"HTTPRoute":          "httproutes.gateway.networking.k8s.io",
		// No page: `events.events.k8s.io` would open on "unknown kind".
		"Event": "",
	} {
		if paths[kind] != want {
			t.Errorf("%s path = %q, want %q (the menu's href for it)", kind, paths[kind], want)
		}
	}
	if got.ResourcesNote != "Some resources' kinds have no page in this console, so those rows open nothing here." {
		t.Errorf("note %q, want the unlinked row said", got.ResourcesNote)
	}
	// Asked once per kind, and never for the kinds the console types.
	if host.lookups["podtemplates"] != 1 || host.lookups["deployments"] != 0 {
		t.Errorf("lookups %v, want one per kind the console does not type", host.lookups)
	}
}

// A row whose kind's page could not be looked up is unlinked and says so as a
// failed read, never as "no page".
func TestAnUnreadableCatalogueLeavesTheRowsUnlinkedAndSaysWhy(t *testing.T) {
	host := newMultiHost(applications)
	host.discovery = map[string][]metav1.APIResource{
		"apps/v1":                   {{Name: "deployments", Kind: "Deployment"}},
		"apiregistration.k8s.io/v1": {{Name: "apiservices", Kind: "APIService"}},
	}
	host.lookupErr = errors.New("the CRD list timed out")
	app := guestbook()
	if err := unstructured.SetNestedSlice(app.Object, []any{
		map[string]any{"group": "apps", "version": "v1", "kind": "Deployment", "namespace": "guestbook", "name": "guestbook-ui"},
		map[string]any{"group": "apiregistration.k8s.io", "version": "v1", "kind": "APIService", "name": "v1beta1.metrics.k8s.io"},
		map[string]any{"group": "apiregistration.k8s.io", "version": "v1", "kind": "APIService", "name": "v1.apps"},
	}, "status", "resources"); err != nil {
		t.Fatal(err)
	}
	host.pages["applications"] = []unstructured.Unstructured{app}

	body, status := serveAt(t, host, "/applications/argocd/guestbook")
	if status != http.StatusOK {
		t.Fatalf("status %d, body %s", status, body)
	}
	var got argocd.ApplicationDetail
	decode(t, body, &got)
	if len(got.Resources) != 3 || got.Resources[0].Path != "deployments" || got.Resources[1].Path != "" || got.Resources[2].Path != "" {
		t.Errorf("resources %+v, want the Deployment linked and the APIServices not", got.Resources)
	}
	if !strings.Contains(got.ResourcesNote, "could not be read") || strings.Contains(got.ResourcesNote, "no page") {
		t.Errorf("note %q, want a failed read said, and not as no page", got.ResourcesNote)
	}
	// Held for the request: two rows of one kind are one failed lookup.
	if n := host.lookups["apiservices.apiregistration.k8s.io"]; n != 1 {
		t.Errorf("the failing lookup was asked %d times, want once", n)
	}
}

func TestAnApplicationNobodyMayReadIsAbsent(t *testing.T) {
	body, status := serveAt(t, newMultiHost(applications), "/applications/argocd/hidden")
	if status != http.StatusNotFound {
		t.Fatalf("status %d, want 404, body %s", status, body)
	}
	var got struct{ Code string }
	decode(t, body, &got)
	if got.Code != "application_not_found" {
		t.Errorf("code %q, want application_not_found", got.Code)
	}
}

func TestARefusedApplicationIsAnAnswer(t *testing.T) {
	host := newMultiHost(applications)
	host.listErr["applications"] = forbidden("argoproj.io", "applications")
	body, status := serveAt(t, host, "/applications/argocd/guestbook")
	if status != http.StatusForbidden {
		t.Fatalf("status %d, want 403, body %s", status, body)
	}
}

// ---------------------------------------------------------------------------

type multiHost struct {
	crds      map[string]extension.CRD // qualified kind -> CRD
	pages     map[string][]unstructured.Unstructured
	listErr   map[string]error
	discovery map[string][]metav1.APIResource // group/version -> resources
	// lookups counts the catalog's lookups by segment; lookupErr answers
	// every one of a segment crds does not hold.
	lookups   map[string]int
	lookupErr error
	asked     extension.ListQuery
	// queries is every list asked, in order; asked is the last of them.
	queries []extension.ListQuery
	// kube is what the typed client holds (Secrets, Services); forward is the
	// address Forward answers for a Service, by namespace/name, and tunnels
	// every Service a tunnel was asked to.
	kube    []runtime.Object
	forward map[string]string
	tunnels []string
	// react, when set, is handed each typed client to add reactors to.
	react func(*fake.Clientset)
	// secretReads is every ConfiguredSecret asked, namespace/name#key.
	secretReads []string
}

func newMultiHost(crds ...extension.CRD) *multiHost {
	h := &multiHost{
		crds:    map[string]extension.CRD{},
		pages:   map[string][]unstructured.Unstructured{},
		listErr: map[string]error{},
	}
	for _, c := range crds {
		h.crds[c.Plural+"."+c.Group] = c
	}
	return h
}

// Client answers discovery only, as the core's does: a Secret or a Service
// read through it is refused, so the extra must take the configured doors.
func (h *multiHost) Client(*http.Request, string) (extension.Clients, error) {
	client := h.kubeClient()
	for _, resource := range []string{"secrets", "services"} {
		client.PrependReactor("*", resource, func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("the clients handed to an extension answer discovery only")
		})
	}
	return extension.Clients{Typed: client}, nil
}

// kubeClient is the cluster as the console's own credentials read it.
func (h *multiHost) kubeClient() *fake.Clientset {
	client := fake.NewSimpleClientset(h.kube...)
	for gv, resources := range h.discovery {
		client.Resources = append(client.Resources, &metav1.APIResourceList{GroupVersion: gv, APIResources: resources})
	}
	if h.react != nil {
		h.react(client)
	}
	return client
}

// ConfiguredSecret is the core's door as an extension sees it: only what the
// extra's Configured names, read with the console's credentials, a missing
// Secret or key told apart. secretReads records every read asked.
func (h *multiHost) ConfiguredSecret(r *http.Request, cluster, namespace, name, key string) (string, error) {
	h.secretReads = append(h.secretReads, namespace+"/"+name+"#"+key)
	cfg := argocd.New().Configured(cluster, func(string) string { return "" })
	if !slices.Contains(cfg.Secrets, extension.ObjectName{Namespace: namespace, Name: name}) {
		return "", fmt.Errorf("%w: %s/%s", extension.ErrNotConfigured, namespace, name)
	}
	secret, err := h.kubeClient().CoreV1().Secrets(namespace).Get(r.Context(), name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return "", fmt.Errorf("%w: %s/%s", extension.ErrNoSuchSecret, namespace, name)
	case err != nil:
		return "", err
	}
	value, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("%w: %q", extension.ErrNoSuchKey, key)
	}
	return string(value), nil
}

// ConfiguredService is the core's door for the Service the configuration
// names, as ConfiguredSecret is for the Secret.
func (h *multiHost) ConfiguredService(r *http.Request, cluster, namespace, name string) (corev1.Service, error) {
	cfg := argocd.New().Configured(cluster, func(string) string { return "" })
	if !slices.Contains(cfg.Services, extension.ObjectName{Namespace: namespace, Name: name}) {
		return corev1.Service{}, fmt.Errorf("%w: %s/%s", extension.ErrNotConfigured, namespace, name)
	}
	svc, err := h.kubeClient().CoreV1().Services(namespace).Get(r.Context(), name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return corev1.Service{}, fmt.Errorf("%w: %s/%s", extension.ErrNoSuchService, namespace, name)
	case err != nil:
		return corev1.Service{}, err
	}
	return *svc, nil
}

func (h *multiHost) Definitions(*http.Request, string) (extension.CRDRegistry, error) {
	return multiCatalogue{h}, nil
}

func (h *multiHost) List(_ *http.Request, _ string, q extension.ListQuery) (extension.Page, error) {
	h.asked = q
	h.queries = append(h.queries, q)
	if err := h.listErr[q.GVR.Resource]; err != nil {
		return extension.Page{}, err
	}
	var items []unstructured.Unstructured
	for _, item := range h.pages[q.GVR.Resource] {
		if q.Namespace != "" && item.GetNamespace() != q.Namespace {
			continue
		}
		if name, ok := strings.CutPrefix(q.FieldSelector, "metadata.name="); ok && item.GetName() != name {
			continue
		}
		items = append(items, item)
	}
	return extension.Page{Items: items, Complete: true}, nil
}

func (h *multiHost) Usage(*http.Request, string, []string) (extension.Usage, error) {
	return extension.Usage{}, nil
}

func (h *multiHost) ContainerUsage(*http.Request, string, string) (extension.LiveUsage, error) {
	return extension.LiveUsage{}, nil
}

func (h *multiHost) UsageWindow(*http.Request, string, string) (extension.Window, error) {
	return extension.Window{}, nil
}

func (h *multiHost) DNSWindow(*http.Request, string, extension.DNSSpan) (extension.DNS, error) {
	return extension.DNS{}, nil
}

// Forward is the person's tunnel, which the diff never opens: argocd-server is
// the console's backend, reached through ForwardAsConsole.
func (h *multiHost) Forward(context.Context, string, string, string, int) (extension.Tunnel, error) {
	return extension.Tunnel{}, errors.New("argocd-server is reached through the console's tunnel, never the person's")
}

// ForwardAsConsole hands the address this test serves argocd-server at, and
// notes every tunnel asked for.
func (h *multiHost) ForwardAsConsole(_ *http.Request, _, namespace, service string, _ int) (extension.Tunnel, error) {
	h.tunnels = append(h.tunnels, namespace+"/"+service)
	addr := h.forward[namespace+"/"+service]
	if addr == "" {
		return extension.Tunnel{}, errors.New("no tunnel in this test")
	}
	return extension.Tunnel{Addr: addr, Pod: "argocd-server-0", PodKnown: true, Stop: func() {}}, nil
}

// Reveal is never asked by this extra; a test that reaches it has found a
// read the extra was not meant to make.
func (h *multiHost) Reveal(*http.Request, string, string, string, string) (string, error) {
	return "", errors.New("this extra reveals nothing")
}

func (h *multiHost) Exec(*http.Request, string, extension.ExecQuery) (extension.ExecResult, error) {
	return extension.ExecResult{}, errors.New("this fake runs no command")
}

func (h *multiHost) RefreshApplication(*http.Request, string, string, string) error {
	return errors.New("RefreshApplication is not used here")
}

func (h *multiHost) ConfirmToken(*http.Request) string { return "" }

func (h *multiHost) Setting(*http.Request, string, string) string { return "" }
func (h *multiHost) MayReadLogs(*http.Request, string, string) error {
	return errors.New("not used")
}

type multiCatalogue struct{ h *multiHost }

func (c multiCatalogue) All(context.Context) ([]extension.CRD, error) {
	out := []extension.CRD{}
	for _, crd := range c.h.crds {
		out = append(out, crd)
	}
	return out, nil
}

func (c multiCatalogue) Lookup(_ context.Context, kind string) (extension.CRD, error) {
	if c.h.lookups == nil {
		c.h.lookups = map[string]int{}
	}
	c.h.lookups[kind]++
	if crd, ok := c.h.crds[kind]; ok {
		return crd, nil
	}
	if c.h.lookupErr != nil {
		return extension.CRD{}, c.h.lookupErr
	}
	return extension.CRD{}, fmt.Errorf("looking up %q: %w", kind, extension.ErrUnknownKind)
}

func forbidden(group, resource string) error {
	return apierrors.NewForbidden(schema.GroupResource{Group: group, Resource: resource}, "",
		errors.New(`User "system:serviceaccount:kubetower:kubetower" cannot list resource "`+resource+`" in API group "`+group+`" at the cluster scope`))
}

func decode(t *testing.T, body []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(body, into); err != nil {
		t.Fatalf("decoding: %v, body %s", err, body)
	}
}

func fluxObject(kind, namespace, name, ready, reason string, suspended bool, revision string) unstructured.Unstructured {
	obj := map[string]any{
		"kind":     kind,
		"metadata": map[string]any{"namespace": namespace, "name": name},
		"spec":     map[string]any{"suspend": suspended},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": ready, "reason": reason, "message": "said by Flux"},
			},
		},
	}
	if revision != "" {
		obj["status"].(map[string]any)["lastAppliedRevision"] = revision
	}
	return unstructured.Unstructured{Object: obj}
}

// guestbook is shaped after Argo CD's example guestbook on docker-desktop, with
// a failed sync and a Degraded health so every block has something in it.
func guestbook() unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"kind":     "Application",
		"metadata": map[string]any{"namespace": "argocd", "name": "guestbook"},
		"spec": map[string]any{
			"project": "default",
			"source": map[string]any{
				"repoURL":        "https://github.com/argoproj/argocd-example-apps.git",
				"path":           "guestbook",
				"targetRevision": "HEAD",
			},
			"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": "guestbook"},
			"syncPolicy":  map[string]any{"automated": map[string]any{}},
		},
		"status": map[string]any{
			"sync":   map[string]any{"status": "OutOfSync", "revision": "bbb"},
			"health": map[string]any{"status": "Degraded", "message": "Deployment has 0 of 1 pods available"},
			"history": []any{
				map[string]any{"id": int64(1), "revision": "aaa", "deployedAt": "2026-10-19T10:00:00Z"},
				map[string]any{"id": int64(2), "revision": "bbb", "deployedAt": "2026-10-20T10:00:00Z"},
			},
			"operationState": map[string]any{
				"phase":     "Failed",
				"message":   "one or more objects failed to apply",
				"startedAt": "2026-10-20T10:00:00Z",
				"operation": map[string]any{"initiatedBy": map[string]any{"automated": true}},
				"syncResult": map[string]any{
					"revision": "bbb",
					"resources": []any{
						map[string]any{"kind": "Deployment", "namespace": "guestbook", "name": "guestbook-ui", "status": "SyncFailed", "message": "admission webhook denied"},
					},
				},
			},
			"conditions": []any{
				map[string]any{"type": "SyncError", "message": "one or more objects failed to apply"},
			},
			"resources": []any{
				map[string]any{"group": "apps", "version": "v1", "kind": "Deployment", "namespace": "guestbook", "name": "guestbook-ui", "status": "OutOfSync", "health": map[string]any{"status": "Degraded"}},
				map[string]any{"version": "v1", "kind": "Service", "namespace": "guestbook", "name": "guestbook-ui", "status": "Synced", "health": map[string]any{"status": "Healthy"}},
				map[string]any{"group": "networking.k8s.io", "version": "v1", "kind": "NetworkPolicy", "namespace": "guestbook", "name": "deny", "status": "Synced"},
				map[string]any{"group": "monitoring.coreos.com", "version": "v1", "kind": "ServiceMonitor", "namespace": "guestbook", "name": "ui", "status": "Synced"},
			},
		},
	}}
}

// The whole report page through the contract.
func TestTheReportReadsTheThreeKindsAndTheServicesThroughTheContract(t *testing.T) {
	sets := extension.CRD{Group: "argoproj.io", Version: "v1alpha1", Plural: "applicationsets", Namespaced: true}
	projects := extension.CRD{Group: "argoproj.io", Version: "v1alpha1", Plural: "appprojects", Namespaced: true}
	host := newMultiHost(applications, sets, projects)
	host.pages["applications"] = []unstructured.Unstructured{guestbook()}
	host.pages["appprojects"] = []unstructured.Unstructured{{Object: map[string]any{
		"metadata": map[string]any{"namespace": "argocd", "name": "default"},
	}}}
	host.pages["services"] = []unstructured.Unstructured{{Object: map[string]any{
		"metadata": map[string]any{"namespace": "argocd", "name": "argocd-server", "labels": map[string]any{"app.kubernetes.io/name": "argocd-server"}},
	}}}

	body, status := serveAt(t, host, "/report")
	if status != http.StatusOK {
		t.Fatalf("status %d, body %s", status, body)
	}
	var got struct {
		State     string `json:"state"`
		Headline  string `json:"headline"`
		Where     string `json:"where"`
		Offenders []struct {
			Name             string `json:"name"`
			ProjectNamespace string `json:"projectNamespace"`
		} `json:"offenders"`
	}
	decode(t, body, &got)
	if got.State != "present" || got.Headline != "1 Degraded" {
		t.Errorf("state %q headline %q, want present and the worst word", got.State, got.Headline)
	}
	if len(got.Offenders) != 1 || got.Offenders[0].Name != "guestbook" || got.Offenders[0].ProjectNamespace != "argocd" {
		t.Errorf("offenders %+v", got.Offenders)
	}
	if !strings.Contains(got.Where, "argocd") {
		t.Errorf("where %q, want the installation found from its Service", got.Where)
	}
}

func TestTheReportWithNoArgoKindIsNotInstalled(t *testing.T) {
	body, status := serveAt(t, newMultiHost(kustomizations), "/report")
	if status != http.StatusNotFound || !strings.Contains(string(body), "kind_unknown") {
		t.Errorf("status %d body %s", status, body)
	}
}

func TestTheReportWithApplicationsRefusedSaysWhoWasRefused(t *testing.T) {
	host := newMultiHost(applications)
	host.listErr["applications"] = forbidden("argoproj.io", "applications")
	body, status := serveAt(t, host, "/report")
	if status != http.StatusForbidden || !strings.Contains(string(body), "list_forbidden") {
		t.Errorf("status %d body %s", status, body)
	}
}

var appProjects = extension.CRD{Group: "argoproj.io", Version: "v1alpha1", Plural: "appprojects", Namespaced: true}

// The project opens and the Events show: both read for the person, and
// an Event about another object is not the Application's.
func TestAnApplicationsProjectAndEventsAreRead(t *testing.T) {
	host := newMultiHost(applications, appProjects)
	host.pages["applications"] = []unstructured.Unstructured{guestbook()}
	project := unstructured.Unstructured{Object: map[string]any{"kind": "AppProject", "metadata": map[string]any{"namespace": "argocd", "name": "default"}}}
	host.pages["appprojects"] = []unstructured.Unstructured{project}
	event := func(name, reason, at string) unstructured.Unstructured {
		return unstructured.Unstructured{Object: map[string]any{
			"kind":           "Event",
			"metadata":       map[string]any{"namespace": "argocd", "name": name + "." + reason},
			"involvedObject": map[string]any{"kind": "Application", "name": name},
			"type":           "Normal", "reason": reason, "message": reason + " said", "count": int64(1), "lastTimestamp": at,
		}}
	}
	host.pages["events"] = []unstructured.Unstructured{
		event("guestbook", "ResourceUpdated", "2026-10-20T10:00:00Z"),
		event("guestbook", "OperationStarted", "2026-10-20T11:00:00Z"),
		event("other", "ResourceUpdated", "2026-10-20T12:00:00Z"),
	}

	body, status := serveAt(t, host, "/applications/argocd/guestbook")
	if status != http.StatusOK {
		t.Fatalf("status %d, body %s", status, body)
	}
	var got argocd.ApplicationDetail
	decode(t, body, &got)
	if got.ProjectNamespace != "argocd" {
		t.Errorf("project namespace %q, want argocd", got.ProjectNamespace)
	}
	if len(got.Events) != 2 || got.Events[0].Reason != "OperationStarted" || got.Events[1].Reason != "ResourceUpdated" {
		t.Errorf("events %+v, want the Application's two, newest first", got.Events)
	}
}

func TestAnApplicationsEventsRefusedAreASentence(t *testing.T) {
	host := newMultiHost(applications)
	host.pages["applications"] = []unstructured.Unstructured{guestbook()}
	host.listErr["events"] = forbidden("", "events")
	body, status := serveAt(t, host, "/applications/argocd/guestbook")
	if status != http.StatusOK {
		t.Fatalf("status %d, body %s, want the page all the same", status, body)
	}
	var got argocd.ApplicationDetail
	decode(t, body, &got)
	if len(got.Events) != 0 || !strings.Contains(got.EventsNote, "could not be read") || got.ProjectNamespace != "" {
		t.Errorf("got events %+v note %q project ns %q", got.Events, got.EventsNote, got.ProjectNamespace)
	}
}

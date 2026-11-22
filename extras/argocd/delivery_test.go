package argocd_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/app"
	"github.com/thonicdev/kubetower/extension"
	argocd "github.com/thonicdev/kubetower/extras/argocd"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The contract, asserted at build rather than at run. A method leaving
// extension.Extension turns this line red in the repository that removed it.
var _ extension.Extension = argocd.Delivery{}

func TestTheConsoleCanRegisterThisPage(t *testing.T) {
	if err := app.Register(argocd.New()); err != nil {
		t.Fatalf("registering the Delivery Center: %v", err)
	}
	found := false
	for _, k := range app.Keys() {
		if k == argocd.Key {
			found = true
		}
	}
	if !found {
		t.Fatalf("app.Keys() = %v, want it to contain %q", app.Keys(), argocd.Key)
	}
}

// TestTheKeyIsTheOneTheOperatorAlreadyHas guards the one value in this package
// that cannot be changed without taking a page away from somebody.
//
// The key is the URL segment, the KT_EXTRAS name and the key the per-cluster
// switch is stored under, and it has always been `argocd`. A rename here turns an installation that had
// the page on into one that has it off, with nothing erroring.
func TestTheKeyIsTheOneTheOperatorAlreadyHas(t *testing.T) {
	if argocd.Key != "argocd" {
		t.Errorf("Key = %q, want \"argocd\" - the spelling installations have persisted", argocd.Key)
	}
	if legacy := (argocd.Delivery{}).Legacy(); len(legacy) != 0 {
		t.Errorf("Legacy = %v, want none - this page has only ever had one key. "+
			"If that changed, the old spelling belongs here and not in a migration.", legacy)
	}
}

func TestTheApplicationsAreListedThroughTheContract(t *testing.T) {
	// v9test, not v1alpha1: a version no real Argo CD serves, so the only way
	// the List below can ask for it is by taking it from the catalog. With
	// v1alpha1 here, a handler that hard-coded Argo's current version would
	// pass this test while ignoring the catalog entirely.
	host := &stubHost{
		crd: extension.CRD{Group: "argoproj.io", Version: "v9test", Plural: "applications", Namespaced: true},
		page: extension.Page{
			Complete: true,
			Items: []unstructured.Unstructured{
				application("argocd", "cilium", "OutOfSync", "Healthy", false),
				application("argocd", "wedged", "Synced", "Healthy", true),
				application("argocd", "fresh", "", "", false),
			},
		},
	}

	body, status := serve(t, host)
	if status != http.StatusOK {
		t.Fatalf("status %d, body %s", status, body)
	}

	var got struct {
		Applications []argocd.Application `json:"applications"`
		Complete     bool                 `json:"complete"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding the answer: %v, body %s", err, body)
	}

	if len(got.Applications) != 3 {
		t.Fatalf("applications = %d, want 3", len(got.Applications))
	}
	if !got.Complete {
		t.Error("complete = false on a page the host said was complete")
	}

	// The catalog was asked for the qualified kind, spelled here rather than
	// through argocd.ApplicationKind: comparing the constant with itself would
	// pass whatever the constant said. The bare plural is ambiguous the moment
	// a second group defines it.
	if host.lookedUp != "applications.argoproj.io" {
		t.Errorf("Definitions was asked for %q, want \"applications.argoproj.io\"", host.lookedUp)
	}

	// The GVR the host was asked for is the one the catalog resolved, not one
	// this package spelled. A page that hard-codes the version reads nothing on
	// the day Argo serves a new one.
	if host.asked.GVR.Group != "argoproj.io" || host.asked.GVR.Version != "v9test" || host.asked.GVR.Resource != "applications" {
		t.Errorf("listed %v, want the GVR Definitions resolved (argoproj.io/v9test, applications)", host.asked.GVR)
	}

	// Deleting is its own field: a finalizer that never returns leaves
	// an Application Healthy and Synced for days, and a page that folds deletion
	// into health says exactly that.
	wedged := got.Applications[1]
	if !wedged.Deleting {
		t.Error("the Application with a deletionTimestamp did not come back as deleting")
	}
	if wedged.Health != "Healthy" || wedged.Sync != "Synced" {
		t.Errorf("wedged = %+v, want Argo's own words kept - deleting is beside them, not instead of them", wedged)
	}

	// An Application with no status yet is empty rather than invented. A row
	// reading "Unknown" because this code guessed cannot be told from one where
	// Argo really said Unknown.
	fresh := got.Applications[2]
	if fresh.Sync != "" || fresh.Health != "" {
		t.Errorf("fresh = %+v, want empty words - it has no status block at all", fresh)
	}
}

// TestAnIncompleteReadSaysSo is the half a page is most likely to drop.
func TestAnIncompleteReadSaysSo(t *testing.T) {
	host := &stubHost{
		crd:  extension.CRD{Group: "argoproj.io", Version: "v1alpha1", Plural: "applications"},
		page: extension.Page{Complete: false, Note: "the read stopped at the page cap"},
	}

	body, status := serve(t, host)
	if status != http.StatusOK {
		t.Fatalf("status %d, body %s", status, body)
	}
	var got struct {
		Complete bool   `json:"complete"`
		Note     string `json:"note"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if got.Complete {
		t.Error("complete = true on a page the host said was incomplete")
	}
	if got.Note == "" {
		t.Error("the note explaining why it is incomplete was dropped, so the page cannot draw the difference")
	}
}

// TestTheThreeCatalogueAnswersStayThree is the whole reason Definitions is on
// the contract.
func TestTheThreeCatalogueAnswersStayThree(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		status  int
		code    string
		mustSay string
	}{
		// The codes are spelled out rather than read from the constants: they
		// are protocol the front end in web/ names by string, so a constant
		// renamed in both places at once must still fail here.
		{"unknown", extension.ErrUnknownKind, http.StatusNotFound, "kind_unknown", "not installed"},
		{"not served", extension.ErrNotServed, http.StatusNotFound, "kind_not_served", "no version of it is served"},
		{"ambiguous", extension.ErrAmbiguousKind, http.StatusConflict, "kind_ambiguous", "does not name one kind"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := &stubHost{lookupErr: fmt.Errorf("looking up %q: %w", argocd.ApplicationKind, tc.err)}
			body, status := serve(t, host)
			if status != tc.status {
				t.Errorf("status %d, want %d, body %s", status, tc.status, body)
			}
			var got struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("decoding: %v", err)
			}
			// Two of the three are 404, so the status cannot tell them apart and
			// the sentence must not be what does: the page branches on this.
			if got.Code != tc.code {
				t.Errorf("code %q, want %q - three answers need three codes", got.Code, tc.code)
			}
			if !strings.Contains(got.Message, tc.mustSay) {
				t.Errorf("message %q, want it to say %q - three answers need three sentences", got.Message, tc.mustSay)
			}
		})
	}
}

// TestAFailureToReadIsNotAnEmptyCluster covers the three calls that can fail
// for a reason other than the catalog's three answers. Each must answer 502
// with its own sentence, and none may come back as 200 with no rows - which is
// an unreadable cluster drawn as one without Argo CD's Applications.
func TestAFailureToReadIsNotAnEmptyCluster(t *testing.T) {
	broken := errors.New("connection refused")
	crd := extension.CRD{Group: "argoproj.io", Version: "v9test", Plural: "applications", Namespaced: true}
	for _, tc := range []struct {
		name    string
		host    *stubHost
		code    string
		mustSay string
	}{
		{"the catalog cannot be read", &stubHost{crd: crd, definitionsErr: broken}, "catalogue_unreadable", "catalog could not be read"},
		{"the lookup fails for another reason", &stubHost{crd: crd, lookupErr: broken}, "kind_unresolved", "Resolving applications.argoproj.io failed"},
		{"the list fails", &stubHost{crd: crd, listErr: broken}, "list_failed", "Applications could not be read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, status := serve(t, tc.host)
			if status != http.StatusBadGateway {
				t.Errorf("status %d, want 502, body %s", status, body)
			}
			var got struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Detail  string `json:"detail"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("decoding: %v, body %s", err, body)
			}
			if got.Code != tc.code {
				t.Errorf("code %q, want %q", got.Code, tc.code)
			}
			if !strings.Contains(got.Message, tc.mustSay) {
				t.Errorf("message %q, want it to say %q", got.Message, tc.mustSay)
			}
			if !strings.Contains(got.Detail, "connection refused") {
				t.Errorf("detail %q, want the underlying error carried", got.Detail)
			}
		})
	}
}

// TestAnUnknownClusterIsNotFoundOnEveryRoute: the console's gate hands a
// cluster name it cannot resolve through to the page on purpose, so that the
// page can say there is no such cluster. Every route answers 404 with its own
// code and the console's sentence, never the 502 of a read that failed - the
// operator would go and inspect an API server nobody contacted. The same host
// still serves the cluster it knows, and a known cluster whose catalog cannot
// be read is still a 502 (TestAFailureToReadIsNotAnEmptyCluster).
func TestAnUnknownClusterIsNotFoundOnEveryRoute(t *testing.T) {
	host := &kubeconfigHost{stubHost: &stubHost{
		crd:  extension.CRD{Group: "argoproj.io", Version: "v9test", Plural: "applications", Namespaced: true},
		page: extension.Page{Complete: true, Items: []unstructured.Unstructured{application("argocd", "cilium", "Synced", "Healthy", false)}},
	}, known: "a-cluster"}

	for _, path := range []string{
		"/",
		"/applications/argocd/cilium",
		"/flux",
		"/report",
		// A kind the catalog resolves, and one the console types by hand,
		// which is read through Client rather than Definitions.
		"/resource?kind=applications.argoproj.io&namespace=argocd&name=cilium",
		"/resource?kind=deployments&namespace=default&name=web",
		"/column?kind=applications.argoproj.io",
		"/column?kind=deployments",
	} {
		t.Run(path, func(t *testing.T) {
			body, status := serveCluster(t, host, "nope", path)
			if status != http.StatusNotFound {
				t.Errorf("status %d, want 404, body %s", status, body)
			}
			var got struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Detail  string `json:"detail"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("decoding: %v, body %s", err, body)
			}
			// The literal, not the constant: it is protocol.
			if got.Code != "cluster_unknown" {
				t.Errorf("code %q, want cluster_unknown", got.Code)
			}
			if want := extension.UnknownClusterText("nope"); got.Message != want {
				t.Errorf("message %q, want the console's own sentence %q", got.Message, want)
			}
			if !strings.Contains(got.Detail, "is not a context in the kubeconfig") {
				t.Errorf("detail %q, want the underlying error carried", got.Detail)
			}
		})
	}

	body, status := serveCluster(t, host, "a-cluster", "/")
	if status != http.StatusOK {
		t.Fatalf("the known cluster: status %d, want 200, body %s", status, body)
	}
	var got struct {
		Applications []argocd.Application `json:"applications"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding: %v, body %s", err, body)
	}
	if len(got.Applications) != 1 || got.Applications[0].Name != "cilium" {
		t.Errorf("the known cluster: applications = %+v, want cilium", got.Applications)
	}
}

// TestARefusedListIsAnAnswerNamingWhoWasRefused: Argo CD is installed and the
// cluster said no. That is 403 with its own code and the refused account, so
// the page can say where to grant the read - never 502 with a Try again that
// cannot succeed, and never "not installed".
func TestARefusedListIsAnAnswerNamingWhoWasRefused(t *testing.T) {
	crd := extension.CRD{Group: "argoproj.io", Version: "v9test", Plural: "applications", Namespaced: true}
	refused := apierrors.NewForbidden(schema.GroupResource{Group: "argoproj.io", Resource: "applications"}, "",
		errors.New(`User "system:serviceaccount:kubetower:kubetower" cannot list resource "applications" in API group "argoproj.io" at the cluster scope`))
	// Wrapped, as a layer of the core may do on the way out.
	body, status := serve(t, &stubHost{crd: crd, listErr: fmt.Errorf("listing: %w", refused)})
	if status != http.StatusForbidden {
		t.Fatalf("status %d, want 403, body %s", status, body)
	}
	var got struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding: %v, body %s", err, body)
	}
	// The literal, not the constant: it is protocol, and data.ts spells it too.
	if got.Code != "list_forbidden" {
		t.Errorf("code %q, want list_forbidden", got.Code)
	}
	if got.Subject != "system:serviceaccount:kubetower:kubetower" {
		t.Errorf("subject %q, want the console's ServiceAccount as the cluster named it", got.Subject)
	}
	if !strings.Contains(got.Message, "Argo CD is installed") {
		t.Errorf("message %q, want it to say Argo CD is there", got.Message)
	}
	if !strings.Contains(got.Detail, "forbidden") {
		t.Errorf("detail %q, want the cluster's own sentence", got.Detail)
	}
}

// personRefusal is a refusal as the core's access review answers one through
// Host.List: errors.Is answers the contract's sentinel, and it also carries an
// API status - 403 for a denial, 503 for a review that could not be made - so
// apierrors.IsForbidden reads a denial as forbidden too. The core's own type
// is internal; this one behaves as the contract says it does.
type personRefusal struct {
	sentinel error
}

func (e personRefusal) Error() string {
	if e.sentinel == extension.ErrDenied {
		return "the cluster says you may not list applications.argoproj.io"
	}
	return "the console could not ask the cluster whether you may list applications.argoproj.io, so it did not try"
}

func (e personRefusal) Is(target error) bool { return target == e.sentinel }

func (e personRefusal) Status() metav1.Status {
	if e.sentinel == extension.ErrDenied {
		return metav1.Status{Status: metav1.StatusFailure, Code: http.StatusForbidden,
			Reason: metav1.StatusReasonForbidden, Message: e.Error()}
	}
	return metav1.Status{Status: metav1.StatusFailure, Code: http.StatusServiceUnavailable,
		Reason: metav1.StatusReasonServiceUnavailable, Message: e.Error()}
}

// TestAPersonsRefusalIsAnAnswerAboutThem: the access review refused the person
// behind the request. The Delivery Center answers it as a refusal - 403 about
// them, never the console's account and never a 502 - and a review that could
// not be made as a failure to ask (503), never a failed read (502). The
// classification is errors.Is against the contract's sentinels, which is the
// only test the contract documents.
func TestAPersonsRefusalIsAnAnswerAboutThem(t *testing.T) {
	crd := extension.CRD{Group: "argoproj.io", Version: "v9test", Plural: "applications", Namespaced: true}
	for _, tc := range []struct {
		name    string
		err     error
		status  int
		code    string
		mustSay string
	}{
		{"denied", fmt.Errorf("listing: %w", personRefusal{extension.ErrDenied}),
			http.StatusForbidden, "list_forbidden", "Argo CD is installed on this cluster, and you may not list its Applications."},
		{"unreviewable", fmt.Errorf("listing: %w", personRefusal{extension.ErrUnreviewable}),
			http.StatusServiceUnavailable, "list_unreviewed", "could not ask the cluster whether you may"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, status := serve(t, &stubHost{crd: crd, listErr: tc.err})
			if status != tc.status {
				t.Fatalf("status %d, want %d, body %s", status, tc.status, body)
			}
			var got struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Detail  string `json:"detail"`
				Subject string `json:"subject"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("decoding: %v, body %s", err, body)
			}
			if got.Code != tc.code {
				t.Errorf("code %q, want %q", got.Code, tc.code)
			}
			if !strings.Contains(got.Message, tc.mustSay) {
				t.Errorf("message %q, want it to say %q", got.Message, tc.mustSay)
			}
			if strings.Contains(got.Message, "account this console") {
				t.Errorf("message %q blames the console's account for a refusal about the person", got.Message)
			}
			if got.Subject != "" {
				t.Errorf("subject %q, want none: the person is not an account the page can name", got.Subject)
			}
			if got.Detail != tc.err.Error() {
				t.Errorf("detail %q, want the refusal's own sentence %q", got.Detail, tc.err.Error())
			}
		})
	}
}

// TestAWronglyTypedStatusIsEmptyRatherThanGuessed is the other half of the
// "every field is optional" rule: a status field that is present with the
// wrong type reads as an empty word, exactly as an absent one does, and the
// row is still returned.
func TestAWronglyTypedStatusIsEmptyRatherThanGuessed(t *testing.T) {
	odd := application("argocd", "odd", "", "", false)
	odd.Object["status"] = map[string]any{
		"sync":   map[string]any{"status": int64(42)},
		"health": "Healthy", // a string where an object belongs
	}
	host := &stubHost{
		crd:  extension.CRD{Group: "argoproj.io", Version: "v9test", Plural: "applications", Namespaced: true},
		page: extension.Page{Complete: true, Items: []unstructured.Unstructured{odd}},
	}
	body, status := serve(t, host)
	if status != http.StatusOK {
		t.Fatalf("status %d, body %s", status, body)
	}
	var got struct {
		Applications []argocd.Application `json:"applications"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding: %v, body %s", err, body)
	}
	if len(got.Applications) != 1 {
		t.Fatalf("applications = %d, want the row kept", len(got.Applications))
	}
	if a := got.Applications[0]; a.Sync != "" || a.Health != "" || a.Name != "odd" {
		t.Errorf("odd = %+v, want name kept and both words empty", a)
	}
}

// ---------------------------------------------------------------------------
// The stub host. It is here rather than in the core because the core does not
// hand one out: `internal/web.host` is the real implementation, and an extra
// does not import `internal/` (extras/boundary_test.go refuses it), which is
// the boundary working rather than a gap.
// ---------------------------------------------------------------------------

type stubHost struct {
	crd            extension.CRD
	page           extension.Page
	definitionsErr error
	lookupErr      error
	listErr        error
	settings       map[string]string

	// What the handler asked for, recorded so a test can assert on it rather
	// than on what the stub happened to return.
	lookedUp string
	asked    extension.ListQuery
}

func (h *stubHost) Client(*http.Request, string) (extension.Clients, error) {
	return extension.Clients{}, nil
}

func (h *stubHost) Definitions(*http.Request, string) (extension.CRDRegistry, error) {
	if h.definitionsErr != nil {
		return nil, h.definitionsErr
	}
	return stubCatalogue{host: h}, nil
}

func (h *stubHost) List(_ *http.Request, _ string, q extension.ListQuery) (extension.Page, error) {
	h.asked = q
	if h.listErr != nil {
		return extension.Page{}, h.listErr
	}
	return h.page, nil
}

func (h *stubHost) Usage(*http.Request, string, []string) (extension.Usage, error) {
	return extension.Usage{}, nil
}

func (h *stubHost) ContainerUsage(*http.Request, string, string) (extension.LiveUsage, error) {
	return extension.LiveUsage{}, nil
}

func (h *stubHost) UsageWindow(*http.Request, string, string) (extension.Window, error) {
	return extension.Window{}, nil
}

func (h *stubHost) DNSWindow(*http.Request, string, extension.DNSSpan) (extension.DNS, error) {
	return extension.DNS{}, nil
}

func (h *stubHost) Forward(context.Context, string, string, string, int) (extension.Tunnel, error) {
	return extension.Tunnel{Stop: func() {}}, nil
}

func (h *stubHost) ForwardAsConsole(*http.Request, string, string, string, int) (extension.Tunnel, error) {
	return extension.Tunnel{Stop: func() {}}, nil
}

// Reveal is never asked by this extra; a test that reaches it has found a
// read the extra was not meant to make.
func (h *stubHost) Reveal(*http.Request, string, string, string, string) (string, error) {
	return "", errors.New("this extra reveals nothing")
}

func (h *stubHost) Exec(*http.Request, string, extension.ExecQuery) (extension.ExecResult, error) {
	return extension.ExecResult{}, errors.New("this fake runs no command")
}

// ConfiguredSecret and ConfiguredService: the stub holds no configured object.
func (h *stubHost) ConfiguredSecret(*http.Request, string, string, string, string) (string, error) {
	return "", extension.ErrNotConfigured
}

func (h *stubHost) ConfiguredService(*http.Request, string, string, string) (corev1.Service, error) {
	return corev1.Service{}, extension.ErrNotConfigured
}

func (h *stubHost) RefreshApplication(*http.Request, string, string, string) error {
	return errors.New("RefreshApplication is not used here")
}

func (h *stubHost) ConfirmToken(*http.Request) string { return "" }

func (h *stubHost) Setting(_ *http.Request, _, key string) string { return h.settings[key] }
func (h *stubHost) MayReadLogs(*http.Request, string, string) error {
	return errors.New("not used")
}

// stubCatalogue answers only the qualified kind it holds. Asked for anything
// else - the bare plural, say - it answers what a real catalog does for a
// kind it does not know, so a handler spelling the kind wrongly is refused
// here rather than handed the CRD regardless.
type stubCatalogue struct {
	host *stubHost
}

func (c stubCatalogue) All(context.Context) ([]extension.CRD, error) {
	return []extension.CRD{c.host.crd}, nil
}

func (c stubCatalogue) Lookup(_ context.Context, kind string) (extension.CRD, error) {
	c.host.lookedUp = kind
	if c.host.lookupErr != nil {
		return extension.CRD{}, c.host.lookupErr
	}
	if kind != c.host.crd.Plural+"."+c.host.crd.Group {
		return extension.CRD{}, fmt.Errorf("looking up %q: %w", kind, extension.ErrUnknownKind)
	}
	return c.host.crd, nil
}

// kubeconfigHost knows one cluster by name, and answers any other the way the
// core's Host does for a name that is not a context in its kubeconfig:
// extension.ErrUnknownCluster, wrapped, from every method that takes one.
type kubeconfigHost struct {
	*stubHost
	known string
}

func (h *kubeconfigHost) unknown(cluster string) error {
	if cluster == h.known {
		return nil
	}
	return fmt.Errorf("%w: %q is not a context in the kubeconfig", extension.ErrUnknownCluster, cluster)
}

func (h *kubeconfigHost) Client(r *http.Request, cluster string) (extension.Clients, error) {
	if err := h.unknown(cluster); err != nil {
		return extension.Clients{}, err
	}
	return h.stubHost.Client(r, cluster)
}

func (h *kubeconfigHost) Definitions(r *http.Request, cluster string) (extension.CRDRegistry, error) {
	if err := h.unknown(cluster); err != nil {
		return nil, err
	}
	return h.stubHost.Definitions(r, cluster)
}

func (h *kubeconfigHost) List(r *http.Request, cluster string, q extension.ListQuery) (extension.Page, error) {
	if err := h.unknown(cluster); err != nil {
		return extension.Page{}, err
	}
	return h.stubHost.List(r, cluster, q)
}

func serve(t *testing.T, host extension.Host) ([]byte, int) {
	t.Helper()
	return serveAt(t, host, "/")
}

// serveAt mounts every route the way the core does - under
// /x/{cluster}/{key}, with the cluster as a path value - and asks one path.
func serveAt(t *testing.T, host extension.Host, path string) ([]byte, int) {
	t.Helper()
	return serveCluster(t, host, "a-cluster", path)
}

// serveCluster is serveAt for a cluster the test names.
func serveCluster(t *testing.T, host extension.Host, cluster, path string) ([]byte, int) {
	t.Helper()
	mux := http.NewServeMux()
	for _, route := range argocd.New().Routes(host) {
		mux.Handle(route.Method+" /x/{cluster}/"+argocd.Key+route.Pattern, route.Handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x/"+cluster+"/"+argocd.Key+path, nil))
	return rec.Body.Bytes(), rec.Code
}

func application(namespace, name, sync, health string, deleting bool) unstructured.Unstructured {
	obj := unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]any{
			"namespace": namespace,
			"name":      name,
		},
	}}
	if sync != "" || health != "" {
		status := map[string]any{}
		if sync != "" {
			status["sync"] = map[string]any{"status": sync}
		}
		if health != "" {
			status["health"] = map[string]any{"status": health}
		}
		obj.Object["status"] = status
	}
	if deleting {
		meta := obj.Object["metadata"].(map[string]any)
		meta["deletionTimestamp"] = "2026-09-21T10:00:00Z"
	}
	return obj
}

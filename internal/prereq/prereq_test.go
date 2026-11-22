package prereq

import (
	"context"
	"fmt"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/edit"
)

// Every test here runs against a fake cluster, and none of them may be made to
// run against anything else.
//
// The rule is absolute: a test of a write guard that reaches a real cluster
// proves a hole in the guard by exercising it on that cluster. This package's
// whole purpose is to write to a cluster, which is exactly where that rule is
// most load-bearing.

var applicationGVR = schema.GroupVersionResource{
	Group: "argoproj.io", Version: "v1alpha1", Resource: "applications",
}

// --- the harness ---------------------------------------------------------------

type sent struct {
	Verb      string
	Resource  string
	Namespace string
	Name      string
	DryRun    bool
}

type harness struct {
	t         *testing.T
	installer *Installer
	writes    []sent
	logged    []string
}

// fakeResolver is what `internal/web` builds out of detail.Reader, reduced to the
// one kind these tests use. `cluster` is the **resolved** context name, which is
// what the read-only check has to be made against, never the spelling that
// arrived.
type fakeResolver struct {
	dyn           *dynamicfake.FakeDynamicClient
	cluster       string
	clusterScoped bool
	unknown       bool
}

func (f fakeResolver) Locate(_ context.Context, _, kind string) (edit.Location, error) {
	if f.unknown {
		return edit.Location{}, fmt.Errorf("%q is not a kind this cluster serves", kind)
	}
	return edit.Location{
		Cluster: f.cluster, Resource: applicationGVR, Kind: "Application",
		Namespaced: !f.clusterScoped, Dynamic: f.dyn,
	}, nil
}

func newHarness(t *testing.T, resolve fakeResolver, readOnly func(string) bool) *harness {
	t.Helper()

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{applicationGVR: "ApplicationList"})
	resolve.dyn = dyn
	if resolve.cluster == "" {
		resolve.cluster = "prod"
	}

	h := &harness{t: t}
	dyn.PrependReactor("create", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create, ok := action.(k8stesting.CreateActionImpl)
		if !ok {
			return false, nil, nil
		}
		object, _ := create.GetObject().(*unstructured.Unstructured)
		name := ""
		if object != nil {
			name = object.GetName()
		}
		dryRun := false
		for _, value := range create.CreateOptions.DryRun {
			if value == metav1.DryRunAll {
				dryRun = true
			}
		}
		h.writes = append(h.writes, sent{
			Verb: action.GetVerb(), Resource: action.GetResource().Resource,
			Namespace: action.GetNamespace(), Name: name, DryRun: dryRun,
		})
		if dryRun {
			// A real API server validates and stores nothing; the fake's tracker
			// would store it and then refuse the commit as already existing.
			return true, create.GetObject(), nil
		}
		return false, nil, nil
	})

	h.installer = New(edit.New(edit.Options{
		Resolve:  resolve,
		Access:   accesstest.Allow(),
		ReadOnly: readOnly,
		Logf:     func(format string, args ...any) { h.logged = append(h.logged, fmt.Sprintf(format, args...)) },
	}))
	return h
}

// nothingSent is the assertion that matters on every refusal. A refusal that
// returns an error *after* the request left is not a guardrail, and it would pass
// a test that only looked at the error.
func (h *harness) nothingSent() {
	h.t.Helper()
	if len(h.writes) != 0 {
		h.t.Fatalf("a refused install still reached the cluster: %+v", h.writes)
	}
}

func request() Request {
	return Request{
		Cluster: "prod", Prereq: "trivy-operator",
		ArgoNamespace: "argocd", Confirm: actions.ConfirmToken,
	}
}

// --- the manifest, which needs no cluster at all --------------------------------

// The Application, field by field, because every one of them is a decision.
//
// A golden assertion and not a shape check: this is the object that lands in
// somebody's production cluster, and the field that is easy to add here is the
// field that reaches it. See Prerequisite.Application for the argument behind
// each of these.
func TestTheGeneratedApplicationIsExactlyThis(t *testing.T) {
	p, ok := Lookup("trivy-operator")
	if !ok {
		t.Fatal("the one prerequisite this console installs is not in the table")
	}

	document, err := p.YAML("argocd", "argoproj.io/v1alpha1")
	if err != nil {
		t.Fatalf("writing the Application out: %v", err)
	}

	const want = `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  annotations:
    kubetower.io/why: Created from KubeTower's Security Center to install Trivy Operator
      on this cluster through Argo CD. Delete this Application to stop Argo reconciling
      it; that alone does not uninstall the chart.
  name: trivy-operator
  namespace: argocd
spec:
  destination:
    namespace: trivy-system
    server: https://kubernetes.default.svc
  project: default
  source:
    chart: trivy-operator
    repoURL: https://aquasecurity.github.io/helm-charts
    targetRevision: 0.38.1
  syncPolicy:
    automated:
      prune: false
      selfHeal: false
    syncOptions:
    - CreateNamespace=true
    - ServerSideApply=true
`
	if document != want {
		t.Errorf("the Application has changed. Every field of it is argued for in\n"+
			"Prerequisite.Application; if this change is deliberate, the argument there has to\n"+
			"change with it.\n\n--- got ---\n%s\n--- want ---\n%s", document, want)
	}
}

// The four decisions that are easiest to reverse by accident, asserted as
// behavior rather than as text.
func TestTheApplicationsFourDecisionsHold(t *testing.T) {
	p, _ := Lookup("trivy-operator")
	object := p.Application("argocd", "argoproj.io/v1alpha1").Object

	// An automated sync, because this console has no sync button: without it the
	// Application is created OutOfSync and *nothing happens*, which is a
	// one-click install that installs nothing.
	automated, found, err := unstructured.NestedMap(object, "spec", "syncPolicy", "automated")
	if err != nil || !found {
		t.Fatal("there is no automated sync policy, so Argo would wait for a Sync this console " +
			"cannot ask for and the install would silently never happen")
	}
	// And neither of its two teeth. Automated says "make this exist"; prune says
	// "and delete whatever the chart stops shipping", selfHeal says "and undo
	// anything anybody changes by hand". Neither is a console button's decision.
	for _, key := range []string{"prune", "selfHeal"} {
		if automated[key] != false {
			t.Errorf("spec.syncPolicy.automated.%s is %v, want false", key, automated[key])
		}
	}

	options, _, _ := unstructured.NestedStringSlice(object, "spec", "syncPolicy", "syncOptions")
	for _, want := range []string{"CreateNamespace=true", "ServerSideApply=true"} {
		if !contains(options, want) {
			t.Errorf("%s is missing from syncOptions %v. Without CreateNamespace the chart has "+
				"nowhere to go; without ServerSideApply Trivy's CRDs exceed the 262144-byte "+
				"last-applied annotation and the API server answers "+
				"`metadata.annotations: Too long`, which reads as a broken chart", want, options)
		}
	}

	// No resource finalizer. Adding it would make deleting the Application delete
	// Trivy Operator and every report it wrote — a reasonable thing for an
	// operator to choose and not a thing for a console to choose for them.
	if finalizers := p.Application("argocd", "v1").GetFinalizers(); len(finalizers) != 0 {
		t.Errorf("the Application carries finalizers %v: deleting it would then take the whole "+
			"installation with it, which is not this button's decision to make", finalizers)
	}

	// `destination.server` and never `name` beside it: Argo refuses an
	// Application carrying both.
	destination, _, _ := unstructured.NestedMap(object, "spec", "destination")
	if _, both := destination["name"]; both {
		t.Error("the destination carries both server and name, which Argo CD refuses outright")
	}
}

// The chart coordinates, pinned, so a change to any of them is deliberate.
//
// The pin is the decision recorded in Prerequisite.Version: an unpinned
// targetRevision with the automated sync above means Argo replaces the operator's
// security scanner whenever a new chart is published. What it costs is that this
// line goes stale in *this repository*, which is why the value is here where a
// release can see it rather than only inside a manifest.
func TestTheChartCoordinatesArePinnedAndVerified(t *testing.T) {
	p, _ := Lookup("trivy-operator")

	// CHECKED 2026-11-13 against https://aquasecurity.github.io/helm-charts/index.yaml:
	// newest `trivy-operator` 0.38.1 (appVersion 0.36.1).
	if p.RepoURL != "https://aquasecurity.github.io/helm-charts" {
		t.Errorf("the repository is %q", p.RepoURL)
	}
	if p.Chart != "trivy-operator" {
		t.Errorf("the chart is %q", p.Chart)
	}
	if p.Version != "0.38.1" || p.AppVersion != "0.36.1" {
		t.Errorf("the pinned version is %q (app %q) — if this is a deliberate bump, re-read the index "+
			"and move the CHECKED line beside it", p.Version, p.AppVersion)
	}
	if p.Namespace != "trivy-system" {
		t.Errorf("the destination namespace is %q, and the helm command the page also prints "+
			"names trivy-system", p.Namespace)
	}
	// A version, not a range and not a tracking expression. `*`, `^` and `~` all
	// mean "and upgrade it later, on your own".
	for _, floating := range []string{"*", "^", "~", ">", "latest"} {
		if strings.Contains(p.Version, floating) {
			t.Errorf("the target revision %q floats: with the automated sync above, Argo would "+
				"upgrade the operator's scanner without anybody asking", p.Version)
		}
	}
}

// The YAML on the confirmation is the object that is sent, and not a second copy
// written out for the screen.
//
// A confirmation showing a manifest that is not the one being sent is worse than
// showing none: it is the one the reader will believe.
func TestTheConfirmationsYAMLIsTheObjectThatIsSent(t *testing.T) {
	h := newHarness(t, fakeResolver{}, nil)

	plan, err := h.installer.Plan(t.Context(), request())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	var shown map[string]any
	if err := yaml.Unmarshal([]byte(plan.YAML), &shown); err != nil {
		t.Fatalf("the manifest on screen is not YAML: %v", err)
	}
	object := plan.Application(plan.ArgoNamespace, plan.APIVersion).Object
	if fmt.Sprint(shown) != fmt.Sprint(object) {
		t.Errorf("the manifest shown is not the object sent.\n--- shown ---\n%v\n--- sent ---\n%v",
			shown, object)
	}
	if plan.APIVersion != "argoproj.io/v1alpha1" {
		t.Errorf("the apiVersion is %q; it must come from the version the cluster serves", plan.APIVersion)
	}
}

// --- the refusals ---------------------------------------------------------------

// A cluster in KT_READONLY_CLUSTERS is refused, and **nothing is sent**.
//
// The refusal comes from `edit.Writer.Locate`, which is the one implementation of
// that guardrail in this binary — not from a second check here, and not from the
// page declining to draw a button. A hidden control is a convenience; this is the
// guardrail.
func TestAReadOnlyClusterIsRefusedAndNothingIsSent(t *testing.T) {
	h := newHarness(t, fakeResolver{}, func(name string) bool { return name == "prod" })

	// Both calls, because the confirmation is a request of its own: a read-only
	// cluster must refuse *before* the operator is asked to confirm, or the page
	// invites them to.
	if _, err := h.installer.Plan(t.Context(), request()); !refusedAs(t, err, edit.RefusedReadOnly) {
		t.Errorf("the confirmation was drawn for a read-only cluster: %v", err)
	}
	if _, err := h.installer.Install(context.Background(), request()); !refusedAs(t, err, edit.RefusedReadOnly) {
		t.Errorf("the commit was not refused on a read-only cluster: %v", err)
	}
	h.nothingSent()
}

// The guardrail is applied *before* the confirmation is checked, and the order is
// this package's whole safety.
//
// `internal/actions`' rule, in its own words: checking the confirmation first
// would answer a request against a read-only cluster with "confirm to install",
// which invites the operator to confirm. Driven by sending a request that fails
// **both** checks and asserting which of the two answers comes back.
func TestTheGuardrailIsAppliedBeforeTheConfirmationIsChecked(t *testing.T) {
	h := newHarness(t, fakeResolver{}, func(string) bool { return true })

	unconfirmed := request()
	unconfirmed.Confirm = ""
	_, err := h.installer.Install(context.Background(), unconfirmed)
	if !refusedAs(t, err, edit.RefusedReadOnly) {
		t.Errorf("a request that is both unconfirmed and against a read-only cluster was "+
			"answered with %v, want the read-only refusal: the other order asks the operator "+
			"to confirm something they are not allowed to do", err)
	}
	h.nothingSent()
}

// An unconfirmed POST sends nothing. A hidden input is a courtesy; this is the
// check that a hand-made POST cannot skip.
func TestAnUnconfirmedInstallSendsNothing(t *testing.T) {
	for _, confirm := range []string{"", "yes", "true", "1", "Confirm"} {
		t.Run(fmt.Sprintf("%q", confirm), func(t *testing.T) {
			h := newHarness(t, fakeResolver{}, nil)
			req := request()
			req.Confirm = confirm
			_, err := h.installer.Install(context.Background(), req)
			if !refusedAs(t, err, edit.RefusedInput) {
				t.Fatalf("confirm=%q was accepted: %v", confirm, err)
			}
			h.nothingSent()
		})
	}
}

// No Argo namespace is a refusal and never a default.
//
// `argocd` is the chart's default and a great many installations are not in it.
// An Application in a namespace no Argo controller watches is an object that sits
// there for ever, reconciled by nobody, looking exactly like an install in
// progress — so this refuses rather than guessing, and the page says which of the
// three reasons it could not name one.
func TestAnInstallWithoutAnArgoNamespaceIsRefusedRatherThanDefaulted(t *testing.T) {
	h := newHarness(t, fakeResolver{}, nil)
	req := request()
	req.ArgoNamespace = "  "

	_, err := h.installer.Install(context.Background(), req)
	refusal, ok := edit.AsRefusal(err)
	if !ok {
		t.Fatalf("an empty Argo namespace was not refused: %v", err)
	}
	if strings.Contains(refusal.Reason, "argocd") {
		t.Errorf("the refusal offers a default namespace: %q", refusal.Reason)
	}
	if !strings.Contains(refusal.Reason, "reconciled by nobody") {
		t.Errorf("the refusal does not say why it will not guess: %q", refusal.Reason)
	}
	h.nothingSent()
}

// Only what is in the table can be installed, and the key is all a caller
// supplies.
//
// `argo-cd` is in the list on purpose: installing Argo CD itself is deliberately
// not offered, because if Argo is not there this mechanism has nothing to install
// *through*. Surrounding whitespace is not in the list — `Lookup` trims, which is
// what every other key this console reads off a form does, and a trimmed key is
// still one of these rows and not a new one.
func TestOnlyWhatIsInTheTableCanBeInstalled(t *testing.T) {
	for _, key := range []string{"", "argo-cd", "argocd", "trivy", "kube-bench", "trivy-operator/../x"} {
		t.Run(fmt.Sprintf("%q", key), func(t *testing.T) {
			h := newHarness(t, fakeResolver{}, nil)
			req := request()
			req.Prereq = key
			if _, err := h.installer.Install(context.Background(), req); !refusedAs(t, err, edit.RefusedInput) {
				t.Fatalf("%q was accepted as something this console installs: %v", key, err)
			}
			h.nothingSent()
		})
	}
}

// A build with no write path installs nothing rather than reaching a cluster
// unguarded.
func TestABuildWithNoWritePathInstallsNothing(t *testing.T) {
	if _, err := New(nil).Install(context.Background(), request()); !refusedAs(t, err, edit.RefusedInput) {
		t.Fatalf("a nil Writer did not refuse: %v", err)
	}
}

// A cluster that does not serve the Application kind is refused by the resolver,
// in the resolver's own words.
func TestAClusterServingNoApplicationKindIsRefused(t *testing.T) {
	h := newHarness(t, fakeResolver{unknown: true}, nil)
	_, err := h.installer.Install(context.Background(), request())
	refusal, ok := edit.AsRefusal(err)
	if !ok {
		t.Fatalf("an unserved kind was not refused: %v", err)
	}
	if !strings.Contains(refusal.Reason, "not a kind this cluster serves") {
		t.Errorf("the refusal reads %q", refusal.Reason)
	}
	h.nothingSent()
}

// --- the install itself ----------------------------------------------------------

// The commit is a dry run and then one create, and it says what happened without
// claiming more than it knows.
func TestAnInstallIsValidatedBeforeItIsCommitted(t *testing.T) {
	h := newHarness(t, fakeResolver{}, nil)

	done, err := h.installer.Install(context.Background(), request())
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if len(h.writes) != 2 {
		t.Fatalf("want a dry-run create and a real one, got %+v", h.writes)
	}
	if !h.writes[0].DryRun || h.writes[1].DryRun {
		t.Errorf("the validation and the commit are the wrong way round or missing: %+v", h.writes)
	}
	for _, got := range h.writes {
		if got.Resource != "applications" || got.Namespace != "argocd" || got.Name != "trivy-operator" {
			t.Errorf("a create went somewhere else: %+v", got)
		}
	}

	// **The sentence must not say the operator installed anything.** An Argo CD
	// Application exists; Argo will reconcile it, which takes minutes and can
	// fail. Those are different answers and only the first one is known here.
	if !strings.Contains(done.Message, "Argo will reconcile") {
		t.Errorf("the outcome does not say who does the installing: %q", done.Message)
	}
	if !strings.Contains(done.Message, "Nothing is installed yet") {
		t.Errorf("the outcome does not say that nothing is installed yet, which is the one "+
			"thing an operator will otherwise read into it: %q", done.Message)
	}
	for _, claim := range []string{"Trivy Operator is installed", "has been installed", "is now installed"} {
		if strings.Contains(done.Message, claim) {
			t.Errorf("the outcome claims %q, which is not something this process knows: %q",
				claim, done.Message)
		}
	}
}

// The one greppable line that says a cluster was changed.
//
// Same `write:` prefix as `internal/edit`'s and `internal/actions`', because one
// grep has to find every write this process has ever made — and it names the
// chart and the version, since "an Application was created" is not enough to
// reconstruct what arrived.
func TestAnInstallLeavesTheGreppableWriteLine(t *testing.T) {
	h := newHarness(t, fakeResolver{}, nil)
	if _, err := h.installer.Install(context.Background(), request()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	var line string
	for _, logged := range h.logged {
		if strings.HasPrefix(logged, "write:") {
			line = logged
		}
	}
	if line == "" {
		t.Fatalf("no write: line was logged, so this write is invisible to the one grep that "+
			"finds them: %v", h.logged)
	}
	for _, want := range []string{`cluster="prod"`, "applications", "namespace=argocd",
		"name=trivy-operator", "action=install", "chart=trivy-operator", "version=0.38.1"} {
		if !strings.Contains(line, want) {
			t.Errorf("the write line does not carry %q: %q", want, line)
		}
	}
}

// A cluster serving Applications cluster-scoped is refused rather than
// mis-addressed.
func TestAClusterScopedApplicationKindIsRefused(t *testing.T) {
	h := newHarness(t, fakeResolver{clusterScoped: true}, nil)
	_, err := h.installer.Install(context.Background(), request())
	refusal, ok := edit.AsRefusal(err)
	if !ok || !strings.Contains(refusal.Reason, "cluster-scoped") {
		t.Fatalf("a cluster-scoped Application kind was not refused: %v", err)
	}
	h.nothingSent()
}

// --- helpers ---------------------------------------------------------------------

func refusedAs(t *testing.T, err error, kind edit.RefusalKind) bool {
	t.Helper()
	refusal, ok := edit.AsRefusal(err)
	return ok && refusal.Kind == kind
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// Check is what a preview runs: the API server's dry run of the create, and
// nothing else. No real create, and the plan it returns is Plan's.
func TestACheckDryRunsTheCreateAndCommitsNothing(t *testing.T) {
	h := newHarness(t, fakeResolver{}, nil)

	plan, err := h.installer.Check(context.Background(), request())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(h.writes) != 1 || !h.writes[0].DryRun {
		t.Fatalf("a check sent %+v, want one dry-run create", h.writes)
	}
	if len(h.logged) != 0 {
		t.Errorf("a check wrote a `write:` line, which says a cluster was changed: %q", h.logged)
	}
	planned, err := h.installer.Plan(context.Background(), request())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.YAML != planned.YAML || plan.ArgoNamespace != "argocd" {
		t.Errorf("the check's plan is not Plan's:\n%s\n---\n%s", plan.YAML, planned.YAML)
	}
}

// A person who may not create Applications in Argo CD's namespace is refused at
// the check, by the review, and nothing reaches the cluster - dry run included.
func TestACheckIsReviewedForThePerson(t *testing.T) {
	h := newHarness(t, fakeResolver{}, nil)
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		return q.Attributes.Verb != "create", nil
	}}
	h.installer = New(edit.New(edit.Options{
		Resolve: fakeResolver{dyn: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{applicationGVR: "ApplicationList"}), cluster: "prod"},
		Access: reviews.Gate(),
		Logf:   func(format string, args ...any) { h.logged = append(h.logged, fmt.Sprintf(format, args...)) },
	}))

	_, err := h.installer.Check(context.Background(), request())
	if !refusedAs(t, err, edit.RefusedByAccess) {
		t.Fatalf("a refused person's check answered %v, want a refusal by the review", err)
	}
	asked := reviews.Asked()
	if len(asked) == 0 || asked[0].Attributes.Verb != "create" || asked[0].Attributes.Resource != "applications" ||
		asked[0].Attributes.Namespace != "argocd" {
		t.Errorf("asked %+v, want create applications in argocd", asked)
	}
}

// An Application of this name that exists is said as such, at the check and at
// the install, and never replaced.
func TestAnExistingApplicationIsSaidAsSuch(t *testing.T) {
	h := newHarness(t, fakeResolver{}, nil)
	if _, err := h.installer.Install(context.Background(), request()); err != nil {
		t.Fatalf("the first install: %v", err)
	}
	h.writes = nil
	// The dry run of the harness stores nothing and answers success, so the
	// second commit meets the first.
	_, err := h.installer.Install(context.Background(), request())
	if !refusedAs(t, err, edit.RefusedInput) || !strings.Contains(err.Error(), "already exists in argocd") {
		t.Errorf("a second install answered %v, want it to say the Application exists", err)
	}
}

package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/customresources"
	"github.com/thonicdev/kubetower/internal/edit"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/prereq"
)

// The prerequisite install behind the JSON mapping, against a fake cluster
// always: Argo CD found (and not), the plan, the Application sent, the
// refusals, and the records the act leaves.

var argoApplicationGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

func argoDefinition(context.Context) (customresources.Definition, error) {
	return customresources.Definition{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application",
		Plural: "applications", Namespaced: true, Served: true}, nil
}

func controllerPod(namespace, name string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name,
		Labels: map[string]string{"app.kubernetes.io/name": "argocd-application-controller"}}}
}

// prereqCluster is a fake cluster with Argo CD's controllers where it is told,
// an Application kind that answers creates as an API server does (a dry run
// stores nothing), and every real create kept.
type prereqCluster struct {
	t       *testing.T
	typed   *kubefake.Clientset
	dyn     *dynamicfake.FakeDynamicClient
	mu      sync.Mutex
	sent    []*unstructured.Unstructured
	dryRuns int
	logged  []string
}

func newPrereqCluster(t *testing.T, pods ...runtime.Object) *prereqCluster {
	t.Helper()
	c := &prereqCluster{t: t, typed: kubefake.NewClientset(pods...)}
	c.dyn = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{argoApplicationGVR: "ApplicationList"})
	c.dyn.PrependReactor("create", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create := action.(k8stesting.CreateActionImpl)
		c.mu.Lock()
		defer c.mu.Unlock()
		if len(create.CreateOptions.DryRun) > 0 {
			c.dryRuns++
			return true, create.GetObject(), nil
		}
		object, _ := create.GetObject().(*unstructured.Unstructured)
		c.sent = append(c.sent, object.DeepCopy())
		return false, nil, nil
	})
	return c
}

func (c *prereqCluster) client() *cluster.Client {
	return &cluster.Client{Context: "prod", Clients: c.typed, Dynamic: c.dyn}
}

type argoResolver struct {
	dyn *dynamicfake.FakeDynamicClient
}

func (r argoResolver) Locate(_ context.Context, cluster, kind string) (edit.Location, error) {
	if kind != prereq.ApplicationKind {
		return edit.Location{}, fmt.Errorf("%q is not a kind this cluster serves", kind)
	}
	return edit.Location{Cluster: cluster, Resource: argoApplicationGVR, Kind: "Application", Namespaced: true,
		Dynamic: r.dyn}, nil
}

// prereqs is apiPrereqs over the fake cluster, its writer reviewing through
// gate and auditing into sink.
func (c *prereqCluster) prereqs(gate *access.Gate, readOnly func(string) bool) apiPrereqs {
	writer := edit.New(edit.Options{Resolve: argoResolver{dyn: c.dyn}, Access: gate, ReadOnly: readOnly,
		Logf: func(format string, args ...any) { c.logged = append(c.logged, fmt.Sprintf(format, args...)) }})
	return apiPrereqs{s: &Server{writer: writer, gate: gate},
		probe: func(context.Context, string) (argoProbe, error) {
			return argoProbe{gate: gate, client: c.client(), definition: argoDefinition}, nil
		}}
}

type auditSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *auditSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *auditSink) records(t *testing.T) []audit.Record {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []audit.Record
	scanner := bufio.NewScanner(bytes.NewReader(s.buf.Bytes()))
	for scanner.Scan() {
		var r audit.Record
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			t.Fatalf("not a record: %v\n%s", err, scanner.Text())
		}
		out = append(out, r)
	}
	return out
}

// --- finding Argo CD -----------------------------------------------------------

func TestArgoIsFoundByItsControllerInOneNamespaceOnly(t *testing.T) {
	cases := []struct {
		name       string
		pods       []runtime.Object
		definition func(context.Context) (customresources.Definition, error)
		answer     func(accesstest.Question) (bool, error)
		state      string
		namespace  string
		note       string
	}{
		{name: "one namespace", pods: []runtime.Object{controllerPod("argocd", "c-0"), controllerPod("argocd", "c-1")},
			state: api.ArgoFound, namespace: "argocd"},
		{name: "two namespaces", pods: []runtime.Object{controllerPod("argocd", "c-0"), controllerPod("gitops", "c-0")},
			state: api.ArgoAmbiguous, note: "argocd, gitops"},
		{name: "the kind served, no controller", state: api.ArgoAbsent, note: "no pod is labeled"},
		{name: "the kind not served", pods: []runtime.Object{controllerPod("argocd", "c-0")},
			definition: func(context.Context) (customresources.Definition, error) {
				return customresources.Definition{}, fmt.Errorf("%w %q", customresources.ErrUnknownKind, prereq.ApplicationKind)
			}, state: api.ArgoAbsent, note: "is not served"},
		{name: "the catalog unreadable", pods: []runtime.Object{controllerPod("argocd", "c-0")},
			definition: func(context.Context) (customresources.Definition, error) {
				return customresources.Definition{}, errors.New("the server is currently unable to handle the request")
			}, state: api.ArgoUnknown, note: "could not be read"},
		{name: "narrowed to namespaces holding none", pods: []runtime.Object{controllerPod("argocd", "c-0")},
			answer: func(q accesstest.Question) (bool, error) { return q.Attributes.Namespace == "shop", nil },
			state:  api.ArgoUnknown, note: "namespaces you may list"},
		{name: "narrowed, one found where they may look", pods: []runtime.Object{controllerPod("argocd", "c-0")},
			answer: func(q accesstest.Question) (bool, error) { return q.Attributes.Namespace == "argocd", nil },
			state:  api.ArgoFound, namespace: "argocd"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := newPrereqCluster(t, c.pods...)
			reviews := &accesstest.Reviews{Answer: c.answer}
			gate := access.New(access.Options{Reviews: reviews.For, InCluster: true, Audit: audit.New(nopSink{})})
			definition := c.definition
			if definition == nil {
				definition = argoDefinition
			}
			probe := argoProbe{gate: gate, client: fake.client(), definition: definition}
			got := probe.find(identity.With(context.Background(), frank))
			if got.State != c.state || got.Namespace != c.namespace {
				t.Errorf("found %+v, want %s in %q", got.PrereqArgo, c.state, c.namespace)
			}
			if c.note != "" && !strings.Contains(got.Note, c.note) {
				t.Errorf("the note %q does not say %q", got.Note, c.note)
			}
			// Every list was asked about frank, never about the console.
			for _, q := range reviews.Asked() {
				if q.Attributes.Resource == "pods" && (q.Self || q.User != frank.Name) {
					t.Errorf("the controller's pods were reviewed as %+v, not as frank", q)
				}
			}
		})
	}
}

func TestTheApplicationIsReadByNameForThePerson(t *testing.T) {
	fake := newPrereqCluster(t, controllerPod("argocd", "c-0"))
	reviews := &accesstest.Reviews{}
	gate := access.New(access.Options{Reviews: reviews.For, InCluster: true, Audit: audit.New(nopSink{})})
	probe := argoProbe{gate: gate, client: fake.client(), definition: argoDefinition}
	ctx := identity.With(context.Background(), frank)
	found := probe.find(ctx)

	if got := probe.application(ctx, found, "trivy-operator"); got.State != api.ApplicationAbsent || got.Namespace != "argocd" {
		t.Errorf("before: %+v", got)
	}
	existing := prereqApplication("argocd")
	if err := fake.dyn.Tracker().Create(argoApplicationGVR, existing, "argocd"); err != nil {
		t.Fatal(err)
	}
	if got := probe.application(ctx, found, "trivy-operator"); got.State != api.ApplicationPresent {
		t.Errorf("after: %+v", got)
	}
	var asked bool
	for _, q := range reviews.Asked() {
		a := q.Attributes
		if a.Verb == "get" && a.Resource == "applications" && a.Group == "argoproj.io" && a.Name == "trivy-operator" &&
			a.Namespace == "argocd" && !q.Self && q.User == frank.Name {
			asked = true
		}
	}
	if !asked {
		t.Errorf("the Application was not read by name for frank: %+v", reviews.Asked())
	}
}

func prereqApplication(namespace string) *unstructured.Unstructured {
	p, _ := prereq.Lookup("trivy-operator")
	return p.Application(namespace, "argoproj.io/v1alpha1")
}

// --- what the page reads ---------------------------------------------------------

func TestThePrereqFactsSayWhyNotInOrder(t *testing.T) {
	p, _ := prereq.Lookup("trivy-operator")
	found := argoFinding{PrereqArgo: api.PrereqArgo{State: api.ArgoFound, Namespace: "argocd"}}
	absentApp := &api.PrereqApplication{State: api.ApplicationAbsent, Name: "trivy-operator", Namespace: "argocd"}
	allowed := &api.CreateAffordance{Permitted: true}
	denied := api.Because(api.CodeVerbDenied, "you may not create applications.argoproj.io in argocd")
	refused := &api.CreateAffordance{Reason: &denied}

	cases := []struct {
		name        string
		found       argoFinding
		app         *api.PrereqApplication
		writable    bool
		create      *api.CreateAffordance
		installable bool
		code        api.ReasonCode
		helm        bool
	}{
		{"installable", found, absentApp, true, allowed, true, "", false},
		{"read-only first", found, absentApp, false, allowed, false, api.CodeVerbDenied, false},
		{"Argo absent", argoFinding{PrereqArgo: api.PrereqArgo{State: api.ArgoAbsent}}, nil, true, nil, false, api.CodeNotInstalled, true},
		{"Argo ambiguous", argoFinding{PrereqArgo: api.PrereqArgo{State: api.ArgoAmbiguous}}, nil, true, nil, false, api.CodeNotSupportedHere, true},
		{"Argo unknown", argoFinding{PrereqArgo: api.PrereqArgo{State: api.ArgoUnknown}}, nil, true, nil, false, api.CodeAccessUnreviewable, true},
		{"already there", found, &api.PrereqApplication{State: api.ApplicationPresent, Name: "trivy-operator", Namespace: "argocd"},
			true, nil, false, api.CodeNotSupportedHere, false},
		{"the person may not create", found, absentApp, true, refused, false, api.CodeVerbDenied, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := prereqFacts(p, c.found, c.app, c.writable, "prod is marked read-only", c.create)
			if got.Installable != c.installable {
				t.Errorf("installable %v, want %v (%+v)", got.Installable, c.installable, got.Reason)
			}
			if c.code != "" && (got.Reason == nil || got.Reason.Code != c.code) {
				t.Errorf("reason %+v, want %s", got.Reason, c.code)
			}
			if (got.Helm != nil) != c.helm {
				t.Errorf("helm %+v, want present=%v", got.Helm, c.helm)
			}
			if got.Helm != nil && (got.Helm.Version != "0.38.1" || got.Helm.Namespace != "trivy-system" ||
				got.Helm.Repo != "https://aquasecurity.github.io/helm-charts" || got.Helm.Chart != "trivy-operator") {
				t.Errorf("helm %+v", got.Helm)
			}
			if got.Chart.Version != "0.38.1" || got.Chart.AppVersion != "0.36.1" {
				t.Errorf("chart %+v", got.Chart)
			}
		})
	}
}

// --- the plan and the install ------------------------------------------------------

// The YAML a person confirms is the object the cluster receives, byte for byte
// once parsed; the act leaves one `write:` line and the structured audit
// records, naming the person.
func TestTheInstallSendsTheObjectThePreviewShowedAndRecordsIt(t *testing.T) {
	fake := newPrereqCluster(t, controllerPod("argocd", "c-0"))
	sink := &auditSink{}
	reviews := &accesstest.Reviews{}
	gate := access.New(access.Options{Reviews: reviews.For, InCluster: true, Audit: audit.New(sink)})
	prereqs := fake.prereqs(gate, nil)
	ctx := identity.With(context.Background(), frank)

	facts, err := prereqs.PreviewPrereq(ctx, "prod", "trivy-operator")
	if err != nil {
		t.Fatalf("PreviewPrereq: %v", err)
	}
	if fake.dryRuns != 1 || len(fake.sent) != 0 {
		t.Fatalf("the preview made %d dry runs and %d creates", fake.dryRuns, len(fake.sent))
	}
	if facts.Verb != "install" || facts.Count != 1 || facts.Destructive || len(facts.Targets) != 1 ||
		facts.Targets[0].Namespace != "argocd" || facts.Targets[0].Name != "trivy-operator" {
		t.Errorf("facts %+v", facts)
	}
	if facts.Confirmation == nil || facts.Confirmation.Kind != "name" || facts.Confirmation.Expected != "trivy-operator" {
		t.Errorf("the gesture is %+v, want the Application's name typed", facts.Confirmation)
	}
	if !strings.Contains(facts.Request, "targetRevision: 0.38.1") || !strings.Contains(facts.Button, "0.38.1") {
		t.Errorf("the plan does not name the pinned version: %q %q", facts.Button, facts.Request)
	}
	if len(fake.logged) != 0 {
		t.Errorf("the preview wrote a `write:` line: %q", fake.logged)
	}

	outcome, err := prereqs.InstallPrereq(ctx, "prod", "trivy-operator", facts.Targets[0].Namespace)
	if err != nil {
		t.Fatalf("InstallPrereq: %v", err)
	}
	if len(fake.sent) != 1 {
		t.Fatalf("%d creates reached the cluster, want 1", len(fake.sent))
	}
	var shown map[string]any
	if err := yaml.Unmarshal([]byte(facts.Request), &shown); err != nil {
		t.Fatalf("the plan's request is not YAML: %v", err)
	}
	sentJSON, _ := json.Marshal(fake.sent[0].Object)
	shownJSON, _ := json.Marshal(shown)
	if !bytes.Equal(sentJSON, shownJSON) {
		t.Errorf("the object sent is not the one shown.\n--- shown ---\n%s\n--- sent ---\n%s", shownJSON, sentJSON)
	}
	if outcome.Succeeded != 1 || len(outcome.Next) != 1 ||
		outcome.Next[0].Href != "/c/prod/applications.argoproj.io/argocd/trivy-operator" ||
		!strings.Contains(outcome.Summary, "Nothing is installed yet") {
		t.Errorf("outcome %+v", outcome)
	}

	// The process log's greppable line, naming who asked.
	if len(fake.logged) != 1 || !strings.Contains(fake.logged[0], "action=install") ||
		!strings.Contains(fake.logged[0], "version=0.38.1") || !strings.Contains(fake.logged[0], frank.Name) {
		t.Errorf("the write line is %q", fake.logged)
	}
	// The structured audit record on stdout: the commit's outcome, for frank.
	var applied bool
	for _, r := range sink.records(t) {
		if r.Verb == "create" && r.Resource == "applications" && r.Namespace == "argocd" && !r.DryRun &&
			r.Outcome == "applied" && r.Who == frank.Name {
			applied = true
		}
	}
	if !applied {
		t.Errorf("no audit record of the create for frank: %s", sink.buf.String())
	}
}

func TestAnInstallRefusedForThePersonSendsNothing(t *testing.T) {
	fake := newPrereqCluster(t, controllerPod("argocd", "c-0"))
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		return q.Attributes.Verb != "create", nil
	}}
	gate := access.New(access.Options{Reviews: reviews.For, InCluster: true, Audit: audit.New(nopSink{})})
	prereqs := fake.prereqs(gate, nil)
	ctx := identity.With(context.Background(), frank)

	if _, err := prereqs.PreviewPrereq(ctx, "prod", "trivy-operator"); !errors.Is(err, api.ErrRefused) {
		t.Errorf("the preview answered %v, want ErrRefused (403)", err)
	}
	if _, err := prereqs.InstallPrereq(ctx, "prod", "trivy-operator", "argocd"); !errors.Is(err, api.ErrRefused) {
		t.Errorf("the install answered %v, want ErrRefused (403)", err)
	}
	if fake.dryRuns != 0 || len(fake.sent) != 0 {
		t.Errorf("a refused person reached the cluster: %d dry runs, %d creates", fake.dryRuns, len(fake.sent))
	}
}

func TestAReadOnlyClusterInstallsNothing(t *testing.T) {
	fake := newPrereqCluster(t, controllerPod("argocd", "c-0"))
	prereqs := fake.prereqs(accesstest.Allow(), func(name string) bool { return name == "prod" })
	if _, err := prereqs.PreviewPrereq(context.Background(), "prod", "trivy-operator"); !errors.Is(err, api.ErrReadOnly) {
		t.Errorf("the preview answered %v, want ErrReadOnly", err)
	}
	if _, err := prereqs.InstallPrereq(context.Background(), "prod", "trivy-operator", "argocd"); !errors.Is(err, api.ErrReadOnly) {
		t.Errorf("the install answered %v, want ErrReadOnly", err)
	}
	if fake.dryRuns != 0 || len(fake.sent) != 0 {
		t.Errorf("a read-only cluster was written to")
	}
}

func TestNothingIsPlannedWithoutOneArgo(t *testing.T) {
	for name, pods := range map[string][]runtime.Object{
		"absent":    nil,
		"ambiguous": {controllerPod("argocd", "c-0"), controllerPod("gitops", "c-0")},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newPrereqCluster(t, pods...)
			prereqs := fake.prereqs(accesstest.Allow(), nil)
			_, err := prereqs.PreviewPrereq(context.Background(), "prod", "trivy-operator")
			if !errors.Is(err, api.ErrInvalid) || !strings.Contains(err.Error(), name) {
				t.Errorf("answered %v, want ErrInvalid naming %s", err, name)
			}
			if fake.dryRuns != 0 || len(fake.sent) != 0 {
				t.Errorf("something reached the cluster")
			}
		})
	}
	fake := newPrereqCluster(t, controllerPod("argocd", "c-0"))
	if _, err := fake.prereqs(accesstest.Allow(), nil).PreviewPrereq(context.Background(), "prod", "cert-manager"); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("a key not in the table answered %v, want ErrNotFound", err)
	}
}

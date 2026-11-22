package web

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/edit"
	"github.com/thonicdev/kubetower/internal/identity"
)

// The list's "+" through the JSON mapping: the plan, the
// outcome's `next`, the refusals, and ListPage.create's verdict. Against a
// fake clientset, always: a test of a write path is a write.

const createdDeployment = "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n  namespace: shop\nspec:\n  replicas: 1\n"

type nopSink struct{}

func (nopSink) Write(p []byte) (int, error) { return len(p), nil }

// createActs is apiActs over a fake cluster whose dry-run create answers as an
// API server does - AlreadyExists for an object that is there, the object
// otherwise, and nothing stored - and which counts the real creates.
func createActs(t *testing.T, gate *access.Gate, seeded bool) (apiActs, *dynamicfake.FakeDynamicClient, *int) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{fakeDeploymentGVR: "DeploymentList"})
	if seeded {
		existing := fakeDeployment("shop", "web")
		if err := dyn.Tracker().Create(fakeDeploymentGVR, existing, "shop"); err != nil {
			t.Fatal(err)
		}
	}
	real := 0
	dyn.PrependReactor("create", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create := action.(k8stesting.CreateActionImpl)
		dry := len(create.CreateOptions.DryRun) > 0
		if !dry {
			real++
			return false, nil, nil
		}
		meta, _ := create.GetObject().(metav1.Object)
		if _, err := dyn.Tracker().Get(action.GetResource(), action.GetNamespace(), meta.GetName()); err == nil {
			return true, nil, apierrors.NewAlreadyExists(action.GetResource().GroupResource(), meta.GetName())
		}
		return true, create.GetObject(), nil
	})
	writer := edit.New(edit.Options{Resolve: fakeActsResolver{dyn: dyn}, Logf: func(string, ...any) {}, Access: gate})
	return apiActs{s: &Server{writer: writer}}, dyn, &real
}

func TestACreateIsPlannedFromTheDocumentAndNamesWhatItMade(t *testing.T) {
	acts, dyn, real := createActs(t, accesstest.Allow(), false)
	req := api.ManifestWrite{Document: createdDeployment}

	facts, err := acts.PreviewCreate(context.Background(), "prod", req)
	if err != nil {
		t.Fatalf("PreviewCreate: %v", err)
	}
	// The kind, name and namespace are the document's: nothing else was given.
	if len(facts.Targets) != 1 || facts.Targets[0].Kind != "Deployment" || facts.Targets[0].Namespace != "shop" ||
		facts.Targets[0].Name != "web" {
		t.Errorf("targets %+v", facts.Targets)
	}
	if facts.Verb != "create" || facts.Button != "Create this deployment" && facts.Button != "Create this Deployment" ||
		facts.Destructive || facts.Count != 1 || facts.Confirmation == nil || facts.Confirmation.Kind != "acknowledge" {
		t.Errorf("facts %+v", facts)
	}
	if !strings.HasPrefix(facts.Diff, "+") || strings.Contains(facts.Diff, "\n-") || facts.Request == "" {
		t.Errorf("diff %q request %q", facts.Diff, facts.Request)
	}
	if *real != 0 {
		t.Fatalf("the preview created %d objects", *real)
	}

	outcome, err := acts.ApplyCreate(context.Background(), "prod", req)
	if err != nil {
		t.Fatalf("ApplyCreate: %v", err)
	}
	if *real != 1 {
		t.Errorf("%d creates reached the cluster, want 1", *real)
	}
	if _, err := dyn.Tracker().Get(fakeDeploymentGVR, "shop", "web"); err != nil {
		t.Errorf("the Deployment is not in the cluster: %v", err)
	}
	if outcome.Succeeded != 1 || len(outcome.Next) != 1 || outcome.Next[0].Href != "/c/prod/deployments/shop/web" {
		t.Errorf("outcome %+v", outcome)
	}
}

func TestACreateIsRefusedAsARequestWhereItIsOne(t *testing.T) {
	// A resourceVersion, beside the document or inside it.
	acts, _, real := createActs(t, accesstest.Allow(), false)
	if _, err := acts.PreviewCreate(context.Background(), "prod",
		api.ManifestWrite{Document: createdDeployment, ResourceVersion: "7"}); !errors.Is(err, api.ErrInvalid) {
		t.Errorf("a resourceVersion beside the document answered %v, want ErrInvalid", err)
	}
	inside := strings.Replace(createdDeployment, "namespace: shop\n", "namespace: shop\n  resourceVersion: \"7\"\n", 1)
	if _, err := acts.PreviewCreate(context.Background(), "prod", api.ManifestWrite{Document: inside}); !errors.Is(err, api.ErrInvalid) {
		t.Errorf("a resourceVersion in the document answered %v, want ErrInvalid", err)
	}

	// An object that exists: a 400 saying so, never a 403 and never a replace.
	existing, _, realExisting := createActs(t, accesstest.Allow(), true)
	_, err := existing.PreviewCreate(context.Background(), "prod", api.ManifestWrite{Document: createdDeployment})
	if !errors.Is(err, api.ErrInvalid) || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("an existing object answered %v, want ErrInvalid saying it exists", err)
	}
	if _, err := existing.ApplyCreate(context.Background(), "prod", api.ManifestWrite{Document: createdDeployment}); err == nil {
		t.Error("the apply went ahead over an existing object")
	}
	if *real+*realExisting != 0 {
		t.Errorf("%d creates reached the cluster", *real+*realExisting)
	}
}

// The API server answering 400 to a manifest that does not decode into its kind
// is the caller's input, a 400 on both the preview and the apply; never the 403
// of a refusal by rights. A 403 from the API server stays a refusal.
func TestAManifestTheClusterAnswersBadRequestToIsARequestNotARefusal(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"bad request", apierrors.NewBadRequest("cannot be handled as a ConfigMap: json: cannot unmarshal bool into Go struct field"), api.ErrInvalid},
		{"forbidden", apierrors.NewForbidden(schema.GroupResource{Resource: "deployments"}, "web", errors.New("no")), api.ErrRefused},
	} {
		acts, dyn, _ := createActs(t, accesstest.Allow(), false)
		dyn.PrependReactor("create", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, tc.err
		})
		req := api.ManifestWrite{Document: createdDeployment}
		if _, err := acts.PreviewCreate(context.Background(), "prod", req); !errors.Is(err, tc.want) {
			t.Errorf("%s: preview answered %v, want %v", tc.name, err, tc.want)
		}
		if _, err := acts.ApplyCreate(context.Background(), "prod", req); !errors.Is(err, tc.want) {
			t.Errorf("%s: apply answered %v, want %v", tc.name, err, tc.want)
		}
	}
}

// A forged previewCreate - the list said no, the request was sent anyway - is
// refused by the server: `create` is reviewed for the person at the write
// door, on the desktop and under the server profile, and the refusal maps to
// the 403 the contract declares.
func TestAForgedCreateIsRefusedForThePerson(t *testing.T) {
	for _, profile := range []struct {
		name      string
		inCluster bool
	}{{"desktop", false}, {"server", true}} {
		t.Run(profile.name, func(t *testing.T) {
			reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
				return q.Attributes.Verb != "create", nil
			}}
			gate := access.New(access.Options{Reviews: reviews.For, InCluster: profile.inCluster, Audit: audit.New(nopSink{})})
			acts, _, real := createActs(t, gate, false)
			ctx := identity.With(context.Background(), frank)

			_, err := acts.PreviewCreate(ctx, "prod", api.ManifestWrite{Document: createdDeployment})
			if !errors.Is(err, api.ErrRefused) {
				t.Errorf("a person refused create answered %v, want ErrRefused (403)", err)
			}
			if _, err := acts.ApplyCreate(ctx, "prod", api.ManifestWrite{Document: createdDeployment}); !errors.Is(err, api.ErrRefused) {
				t.Errorf("the apply answered %v, want ErrRefused", err)
			}
			if *real != 0 {
				t.Errorf("%d creates reached the cluster for a refused person", *real)
			}
			if profile.inCluster {
				for _, q := range reviews.Asked() {
					if q.Attributes.Verb == "create" && (q.Self || q.User != frank.Name) {
						t.Errorf("the create was reviewed as %+v, not as frank", q)
					}
				}
			}
		})
	}
}

// ListPage.create's verdict: the write's own question, for the person and -
// under the server profile - for the console too.
func TestTheCreateVerdictIsThePersons(t *testing.T) {
	where := edit.Location{Cluster: "prod", Resource: fakeDeploymentGVR, Kind: "Deployment", Namespaced: true}
	cases := []struct {
		name      string
		inCluster bool
		answer    func(accesstest.Question) (bool, error)
		permitted bool
		reason    string
	}{
		{"desktop, allowed", false, func(accesstest.Question) (bool, error) { return true, nil }, true, ""},
		{"desktop, refused", false, func(accesstest.Question) (bool, error) { return false, nil }, false, "you may not create deployments.apps in shop"},
		{"server, the person refused", true, func(q accesstest.Question) (bool, error) { return q.Self, nil }, false, "you may not create"},
		{"server, the person allowed and the console not", true, func(q accesstest.Question) (bool, error) { return !q.Self, nil }, false, "this console may not"},
		{"server, both allowed", true, func(accesstest.Question) (bool, error) { return true, nil }, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reviews := &accesstest.Reviews{Answer: c.answer}
			gate := access.New(access.Options{Reviews: reviews.For, InCluster: c.inCluster})
			got := createVerdict(identity.With(context.Background(), frank), gate, where, "shop")
			if got.Permitted != c.permitted || got.Namespace != "shop" {
				t.Errorf("verdict %+v, want permitted=%v in shop", got, c.permitted)
			}
			if c.reason != "" && (got.Reason == nil || !strings.Contains(got.Reason.Message, c.reason)) {
				t.Errorf("reason %+v, want it to say %q", got.Reason, c.reason)
			}
			if !c.permitted && got.Reason == nil {
				t.Error("a refusal carries no reason")
			}
			asked := reviews.Asked()
			if len(asked) == 0 {
				t.Fatal("nothing was asked")
			}
			a := asked[0].Attributes
			if a.Verb != "create" || a.Resource != "deployments" || a.Group != "apps" || a.Namespace != "shop" || a.Name != "" {
				t.Errorf("asked %+v, want create deployments.apps in shop with no name", a)
			}
			if c.inCluster && (asked[0].Self || asked[0].User != frank.Name) {
				t.Errorf("the first question was %+v, want frank's", asked[0])
			}
		})
	}
}

// Before anything is asked: a read-only cluster, and a namespaced list not
// narrowed to exactly one namespace, where there is no single place a new
// object would go.
func TestTheCreateAffordanceSaysWhyBeforeAsking(t *testing.T) {
	readOnly := listPageFixture(t)
	got := readOnly.createAffordance(context.Background(), "prod", "deployments", true, []string{"shop"})
	if got == nil || got.Permitted || got.Reason == nil || !strings.Contains(got.Reason.Message, "read-only") {
		t.Errorf("a read-only cluster's create is %+v", got)
	}

	writable := serverFor(t, false)
	reviews := &accesstest.Reviews{}
	writable.gate = reviews.Gate()
	for _, namespaces := range [][]string{nil, {"shop", "web"}} {
		got := writable.createAffordance(context.Background(), "prod", "deployments", true, namespaces)
		if got == nil || got.Permitted || got.Namespace != "" || got.Reason == nil || got.Reason.Code != api.CodeNotSupportedHere {
			t.Errorf("namespaces %v: create is %+v, want refused with a reason to choose one", namespaces, got)
		}
	}
	if n := len(reviews.Asked()); n != 0 {
		t.Errorf("%d questions were asked before the answer was known", n)
	}
}

func TestACreatedObjectIsNamedByItsListsSlug(t *testing.T) {
	for _, c := range []struct {
		gvr  schema.GroupVersionResource
		want string
	}{
		{fakeDeploymentGVR, "deployments"},
		{schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, "configmaps"},
		{schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}, "ingresses"},
		{schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}, "certificates.cert-manager.io"},
		// A CRD with a built-in kind's plural keeps its group, as resolveKind
		// reads the bare plural as the built-in.
		{schema.GroupVersionResource{Group: "acme.io", Version: "v1", Resource: "services"}, "services.acme.io"},
	} {
		if got := kindSlug(c.gvr); got != c.want {
			t.Errorf("kindSlug(%v) = %q, want %q", c.gvr, got, c.want)
		}
	}
}

func TestNodesEventsAndEndpointsHaveNoPlus(t *testing.T) {
	s := &Server{}
	for _, kind := range []string{"nodes", "events", "endpoints"} {
		if got := s.createAffordance(context.Background(), "prod", kind, kind != "nodes", []string{"shop"}); got != nil {
			t.Errorf("%s offers %+v; want no affordance at all", kind, got)
		}
	}
}

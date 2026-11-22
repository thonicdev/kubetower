package edit

import (
	"strings"
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/identity"
)

// The list's "+": PreviewCreate and ApplyCreate, against
// the fake, always - a test of a write path is a write.

const newDeployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: shop
spec:
  replicas: 1
`

func webTarget() Target {
	return Target{Cluster: "prod", Kind: "Deployment", Namespace: "shop", Name: "web"}
}

// createHarness answers a dry-run create the way an API server does: refused
// with AlreadyExists when the object is there, and otherwise answered with the
// object and **not stored** - the fake's tracker would store it. Every create
// that reaches it is counted, dry run or not.
func newCreateDocHarness(t *testing.T, readOnly func(string) bool, seeds ...string) (*harness, *[]bool) {
	t.Helper()
	h := newHarness(t, readOnly)
	for _, seed := range seeds {
		o := object(t, seed)
		if err := h.dyn.Tracker().Create(deploymentGVR, o, o.GetNamespace()); err != nil {
			t.Fatalf("seeding: %v", err)
		}
	}
	var creates []bool
	h.dyn.PrependReactor("create", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create := action.(k8stesting.CreateActionImpl)
		dry := hasDryRunAll(create.CreateOptions.DryRun)
		creates = append(creates, dry)
		if !dry {
			return false, nil, nil
		}
		meta, _ := create.GetObject().(metav1.Object)
		if _, err := h.dyn.Tracker().Get(action.GetResource(), action.GetNamespace(), meta.GetName()); err == nil {
			return true, nil, apierrors.NewAlreadyExists(action.GetResource().GroupResource(), meta.GetName())
		}
		return true, create.GetObject(), nil
	})
	return h, &creates
}

func TestACreateIsPreviewedThenMade(t *testing.T) {
	h, creates := newCreateDocHarness(t, nil)
	change := CreateChange{Target: webTarget(), YAML: newDeployment}

	preview, err := h.writer.PreviewCreate(t.Context(), change)
	if err != nil {
		t.Fatalf("PreviewCreate: %v", err)
	}
	if len(*creates) != 1 || !(*creates)[0] {
		t.Fatalf("the preview sent %v; want one dry-run create", *creates)
	}
	if _, err := h.dyn.Tracker().Get(deploymentGVR, "shop", "web"); err == nil {
		t.Fatal("the preview created the object")
	}
	if preview.Title != "Deployment" || preview.Added == 0 {
		t.Errorf("preview %+v", preview)
	}
	for _, line := range preview.Diff {
		if line.Mark != MarkAdd {
			t.Errorf("a create's diff has a line that is not an addition: %q%q", line.Mark, line.Text)
		}
	}
	if !strings.Contains(preview.Request, `"replicas": 1`) {
		t.Errorf("the request does not carry the document: %s", preview.Request)
	}

	created, err := h.writer.ApplyCreate(t.Context(), change)
	if err != nil {
		t.Fatalf("ApplyCreate: %v", err)
	}
	if created.Resource != deploymentGVR || !created.Namespaced || created.Name != "web" {
		t.Errorf("created %+v", created)
	}
	// The apply ran the dry run again before it committed.
	if got := *creates; len(got) != 3 || !got[1] || got[2] {
		t.Errorf("the creates were %v; want dry run, dry run, create", got)
	}
	if _, err := h.dyn.Tracker().Get(deploymentGVR, "shop", "web"); err != nil {
		t.Errorf("the object is not in the cluster after the create: %v", err)
	}
	if len(h.logged) == 0 || !strings.Contains(h.logged[len(h.logged)-1], "action=create") {
		t.Errorf("no write line names the create: %v", h.logged)
	}
}

// The create review shows the document that will be sent. A dry run answers
// with what the API server would store - a uid, a creationTimestamp, a
// generation, managedFields, a resourceVersion - and none of those are part of
// the request: the server fills them in again, with other values, when the
// object is made.
func TestACreateReviewShowsNoFieldTheServerFillsIn(t *testing.T) {
	h, _ := newCreateDocHarness(t, nil)
	h.dyn.PrependReactor("create", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create := action.(k8stesting.CreateActionImpl)
		if !hasDryRunAll(create.CreateOptions.DryRun) {
			return false, nil, nil
		}
		stored := create.GetObject().DeepCopyObject()
		meta, _ := stored.(metav1.Object)
		meta.SetUID("0b6c-dry-run")
		meta.SetCreationTimestamp(metav1.Now())
		meta.SetGeneration(1)
		meta.SetResourceVersion("4242")
		meta.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "kubetower", Operation: metav1.ManagedFieldsOperationUpdate}})
		return true, stored, nil
	})

	preview, err := h.writer.PreviewCreate(t.Context(), CreateChange{Target: webTarget(), YAML: newDeployment})
	if err != nil {
		t.Fatalf("PreviewCreate: %v", err)
	}
	var added []string
	for _, line := range preview.Diff {
		added = append(added, line.Text)
	}
	shown := strings.Join(added, "\n")
	for _, filled := range []string{"uid:", "creationTimestamp:", "generation:", "managedFields:", "resourceVersion:"} {
		if strings.Contains(shown, filled) {
			t.Errorf("the create review shows %q, which the server fills in:\n%s", filled, shown)
		}
	}
	if !strings.Contains(shown, "replicas: 1") || !strings.Contains(shown, "name: web") {
		t.Errorf("the create review does not show the document:\n%s", shown)
	}
}

// A document copied from an existing object carries its resourceVersion; a
// create refuses it before anything is sent, reviewed or not.
func TestACreateRefusesAResourceVersion(t *testing.T) {
	h, creates := newCreateDocHarness(t, nil)
	doc := strings.Replace(newDeployment, "namespace: shop\n", "namespace: shop\n  resourceVersion: \"42\"\n", 1)
	_, err := h.writer.PreviewCreate(t.Context(), CreateChange{Target: webTarget(), YAML: doc})
	if refusal := refusalOf(t, err); refusal.Kind != RefusedInput || !strings.Contains(refusal.Reason, "resourceVersion") {
		t.Errorf("refused as %v: %s", refusal.Kind, refusal.Reason)
	}
	if len(*creates) != 0 {
		t.Errorf("a document with a resourceVersion reached the cluster: %v", *creates)
	}
}

// An object that exists is refused, as the mistake it is, and never replaced.
func TestACreateNeverReplacesAnObjectThatExists(t *testing.T) {
	existing := strings.Replace(newDeployment, "replicas: 1", "replicas: 9", 1)
	h, creates := newCreateDocHarness(t, nil, existing)
	change := CreateChange{Target: webTarget(), YAML: newDeployment}

	_, err := h.writer.PreviewCreate(t.Context(), change)
	refusal := refusalOf(t, err)
	if refusal.Kind != RefusedInput || !strings.Contains(refusal.Reason, "already exists") {
		t.Errorf("an existing object was refused as %v: %s", refusal.Kind, refusal.Reason)
	}
	if _, err := h.writer.ApplyCreate(t.Context(), change); err == nil {
		t.Error("the apply went ahead over an existing object")
	}
	for _, dry := range *creates {
		if !dry {
			t.Error("a real create was sent for an object that exists")
		}
	}
	live := h.object(deploymentGVR, "shop", "web")
	if replicas, _, _ := unstructuredInt(live.Object, "spec", "replicas"); replicas != 9 {
		t.Errorf("the existing object changed: replicas %d", replicas)
	}
}

// The kind sent is the kind addressed: a Target resolved for one kind and a
// document of another is refused, so the review cannot be asked about
// one resource while the body is another.
func TestACreateRefusesADocumentOfAnotherKind(t *testing.T) {
	h, creates := newCreateDocHarness(t, nil)
	target := webTarget()
	target.Kind = "configmaps"
	_, err := h.writer.PreviewCreate(t.Context(), CreateChange{Target: target, YAML: newDeployment})
	refusalOf(t, err)
	if len(*creates) != 0 {
		t.Errorf("a document of another kind reached the cluster: %v", *creates)
	}
}

func TestACreateOnAReadOnlyClusterIsRefused(t *testing.T) {
	h, creates := newCreateDocHarness(t, func(string) bool { return true })
	_, err := h.writer.PreviewCreate(t.Context(), CreateChange{Target: webTarget(), YAML: newDeployment})
	if refusal := refusalOf(t, err); refusal.Kind != RefusedReadOnly {
		t.Errorf("refused as %v: %s", refusal.Kind, refusal.Reason)
	}
	if len(*creates) != 0 {
		t.Errorf("a create reached a read-only cluster: %v", *creates)
	}
}

// `create` is reviewed for the person before the dry run, on the desktop and
// under the server profile - where the question names the single sign-on
// user, never the console's account - and a person refused it sends nothing.
// This is what refuses a forged previewCreate whatever the list said.
func TestACreateIsReviewedForThePerson(t *testing.T) {
	frank := identity.Who{Name: "frank@example.com", Method: identity.SSO, Groups: []string{"oidc:dev"}}
	for _, profile := range []struct {
		name      string
		inCluster bool
	}{{"desktop", false}, {"server", true}} {
		t.Run(profile.name, func(t *testing.T) {
			h, creates := newCreateDocHarness(t, nil)
			reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
				return q.Attributes.Verb != "create", nil
			}}
			gate := access.New(access.Options{Reviews: reviews.For, InCluster: profile.inCluster, Audit: audit.New(&recordSink{})})
			w := New(Options{Resolve: fakeResolver{dyn: h.dyn, cluster: "prod"}, Access: gate})
			ctx := identity.With(t.Context(), frank)

			_, err := w.PreviewCreate(ctx, CreateChange{Target: webTarget(), YAML: newDeployment})
			if refusal := refusalOf(t, err); refusal.Kind != RefusedByAccess {
				t.Errorf("a person refused create was refused as %v: %s", refusal.Kind, refusal.Reason)
			}
			if _, err := w.ApplyCreate(ctx, CreateChange{Target: webTarget(), YAML: newDeployment}); err == nil {
				t.Error("the apply created an object for a person refused create")
			}
			if len(*creates) != 0 {
				t.Errorf("a refused create reached the cluster: %v", *creates)
			}
			asked := reviews.Asked()
			if len(asked) == 0 {
				t.Fatal("nothing was reviewed")
			}
			a := asked[0].Attributes
			if a.Verb != "create" || a.Resource != "deployments" || a.Group != "apps" || a.Namespace != "shop" || a.Name != "" {
				t.Errorf("asked %+v; want create deployments.apps in shop, no name", a)
			}
			if profile.inCluster && (asked[0].Self || asked[0].User != frank.Name) {
				t.Errorf("under the server profile the review was %+v; want frank's own", asked[0])
			}
		})
	}
}

// --- templates ---------------------------------------------------------------

func TestEveryWrittenTemplateIsADocumentOfItsKind(t *testing.T) {
	type head struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}
	scoped := map[string]bool{"Namespace": true}
	versions := map[string]string{"apps": "v1", "batch": "v1", "networking.k8s.io": "v1", "": "v1", "autoscaling": "v2", "rbac.authorization.k8s.io": "v1"}
	for key := range templateBodies {
		group, kind, _ := strings.Cut(key, "/")
		where := Location{
			Kind:       kind,
			Namespaced: !scoped[kind],
		}
		where.Resource.Group, where.Resource.Version = group, versions[group]
		where.Resource.Resource = strings.ToLower(kind) + "s"
		t.Run(kind, func(t *testing.T) {
			document, generic, err := Template(where, "shop")
			if err != nil {
				t.Fatalf("Template: %v", err)
			}
			if generic {
				t.Error("a written template says it is the skeleton")
			}
			if !strings.HasSuffix(document, "\n") || strings.Contains(document, templateName) {
				t.Errorf("the document is unfinished:\n%s", document)
			}
			var got head
			if err := yaml.Unmarshal([]byte(document), &got); err != nil {
				t.Fatalf("the template is not YAML: %v\n%s", err, document)
			}
			if got.Kind != kind || got.APIVersion != APIVersionOf(where) {
				t.Errorf("apiVersion %q kind %q; want %q %q", got.APIVersion, got.Kind, APIVersionOf(where), kind)
			}
			if got.Metadata.Name == "" {
				t.Error("the template names no object")
			}
			switch {
			case where.Namespaced && got.Metadata.Namespace != "shop":
				t.Errorf("a namespaced kind's template is in %q, want shop", got.Metadata.Namespace)
			case !where.Namespaced && got.Metadata.Namespace != "":
				t.Errorf("a cluster-scoped kind's template carries namespace %q", got.Metadata.Namespace)
			}
			// It is a document a create can send: one object, no resourceVersion.
			if _, err := parseDocument(document); err != nil {
				t.Errorf("the template does not parse as a manifest: %v", err)
			}
		})
	}
	for _, want := range []string{"apps/Deployment", "apps/StatefulSet", "apps/DaemonSet", "/Service", "/ConfigMap",
		"/Secret", "batch/Job", "batch/CronJob", "/Pod", "networking.k8s.io/Ingress", "/PersistentVolumeClaim",
		"/ServiceAccount", "/Namespace"} {
		if _, ok := templateBodies[want]; !ok {
			t.Errorf("no written template for %s", want)
		}
	}
	if strings.Contains(templateBodies["/Secret"], "data:\n") && !strings.Contains(templateBodies["/Secret"], "stringData:") {
		t.Error("the Secret template carries base64 data rather than stringData")
	}
}

// A custom resource gets the skeleton, and says so.
func TestACustomResourceGetsTheSkeleton(t *testing.T) {
	where := Location{Kind: "Certificate", Namespaced: true}
	where.Resource.Group, where.Resource.Version, where.Resource.Resource = "cert-manager.io", "v1", "certificates"
	document, generic, err := Template(where, "shop")
	if err != nil {
		t.Fatal(err)
	}
	want := "apiVersion: cert-manager.io/v1\nkind: Certificate\nmetadata:\n  name: example-certificate\n  namespace: shop\n"
	if !generic || document != want {
		t.Errorf("generic=%v\n%s\nwant\n%s", generic, document, want)
	}
}

// The namespace is written into the document, so one that is not a DNS label
// is refused rather than written.
func TestATemplateRefusesANamespaceThatIsNotALabel(t *testing.T) {
	where := Location{Kind: "ConfigMap", Namespaced: true}
	where.Resource.Version = "v1"
	for _, namespace := range []string{"shop\nkind: Secret", "a: b", "UPPER"} {
		if document, _, err := Template(where, namespace); err == nil {
			t.Errorf("namespace %q was written:\n%s", namespace, document)
		}
	}
}

func unstructuredInt(object map[string]any, fields ...string) (int64, bool, error) {
	var current any = object
	for _, field := range fields {
		m, ok := current.(map[string]any)
		if !ok {
			return 0, false, nil
		}
		current = m[field]
	}
	switch v := current.(type) {
	case int64:
		return v, true, nil
	case float64:
		return int64(v), true, nil
	}
	return 0, false, nil
}

// The three templates with no generic reading to fall back on are decoded
// strictly into their types: a field of the wrong name or shape would otherwise
// reach the person only as a refusal from the cluster.
func TestTheNewTemplatesAreStrictlyTheirTypes(t *testing.T) {
	cases := map[string]struct {
		group, version, kind string
		into                 any
	}{
		"HorizontalPodAutoscaler": {"autoscaling", "v2", "HorizontalPodAutoscaler", &autoscalingv2.HorizontalPodAutoscaler{}},
		"Role":                    {"rbac.authorization.k8s.io", "v1", "Role", &rbacv1.Role{}},
		"NetworkPolicy":           {"networking.k8s.io", "v1", "NetworkPolicy", &networkingv1.NetworkPolicy{}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			where := Location{Kind: c.kind, Namespaced: true}
			where.Resource.Group, where.Resource.Version = c.group, c.version
			document, generic, err := Template(where, "shop")
			if err != nil || generic {
				t.Fatalf("Template: generic %v, err %v", generic, err)
			}
			if err := yaml.UnmarshalStrict([]byte(document), c.into); err != nil {
				t.Errorf("%v\n%s", err, document)
			}
		})
	}
}

package edit

import (
	"context"
	"fmt"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/identity"
)

// Under the server profile the console does not change RBAC in this version:
// every write to a Role, ClusterRole, RoleBinding or ClusterRoleBinding is
// refused at the door, before any review and before anything is sent. On the
// desktop the same writes go through.

type rbacKind struct {
	gvr        schema.GroupVersionResource
	kind       string
	namespaced bool
	document   string
}

var rbacKinds = []rbacKind{
	{schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}, "Role", true, `
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: reader, namespace: shop}
rules: [{apiGroups: [""], resources: [pods], verbs: [get]}]
`},
	{schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}, "ClusterRole", false, `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: reader}
rules: [{apiGroups: [""], resources: [pods], verbs: [get]}]
`},
	{schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}, "RoleBinding", true, `
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: reader, namespace: shop}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-admin}
subjects: [{kind: User, name: frank@example.com, apiGroup: rbac.authorization.k8s.io}]
`},
	{schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}, "ClusterRoleBinding", false, `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: reader}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-admin}
subjects: [{kind: User, name: frank@example.com, apiGroup: rbac.authorization.k8s.io}]
`},
}

type rbacResolver struct {
	dyn dynamic.Interface
}

func (r rbacResolver) Locate(_ context.Context, _, kind string) (Location, error) {
	for _, k := range rbacKinds {
		if strings.EqualFold(kind, k.kind) || kind == k.gvr.Resource {
			return Location{Cluster: "prod", Resource: k.gvr, Kind: k.kind, Namespaced: k.namespaced, Dynamic: r.dyn}, nil
		}
	}
	return Location{}, fmt.Errorf("%q is not a kind this cluster serves", kind)
}

// rbacHarness is a cluster holding one object of each RBAC kind, a writer on
// the profile asked for, and every request that would change something.
type rbacHarness struct {
	writer  *Writer
	reviews *accesstest.Reviews
	sink    *recordSink
	sent    *[]string
}

func newRBACHarness(t *testing.T, inCluster bool) rbacHarness {
	t.Helper()
	listKinds := map[schema.GroupVersionResource]string{}
	var seeds []runtime.Object
	for _, k := range rbacKinds {
		listKinds[k.gvr] = k.kind + "List"
		seed := object(t, k.document)
		seed.SetResourceVersion("7")
		seeds = append(seeds, seed)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, seeds...)
	sent := &[]string{}
	dyn.PrependReactor("*", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		switch action.GetVerb() {
		case "get", "list", "watch":
		default:
			*sent = append(*sent, action.GetVerb()+" "+action.GetResource().Resource)
		}
		// A dry-run create is answered, not stored, as the API server does.
		if create, ok := action.(k8stesting.CreateActionImpl); ok && hasDryRunAll(create.CreateOptions.DryRun) {
			return true, create.GetObject(), nil
		}
		return false, nil, nil
	})
	reviews := &accesstest.Reviews{}
	sink := &recordSink{}
	gate := access.New(access.Options{Reviews: reviews.For, InCluster: inCluster, Audit: audit.New(sink)})
	return rbacHarness{
		writer:  New(Options{Resolve: rbacResolver{dyn: dyn}, Access: gate}),
		reviews: reviews, sink: sink, sent: sent,
	}
}

func targetOf(k rbacKind) Target {
	namespace := ""
	if k.namespaced {
		namespace = "shop"
	}
	return Target{Cluster: "prod", Kind: k.kind, Namespace: namespace, Name: "reader"}
}

// The writes each verb family makes, through the door every page uses: a
// create from a document (the list's "+"), an apply of an edited manifest
// (the editor, a patch), and a delete (a verb, one row of a bulk delete).
func rbacWrites(h rbacHarness, k rbacKind, ctx context.Context) map[string]func() error {
	target := targetOf(k)
	created := strings.Replace(k.document, "name: reader", "name: fresh", 1)
	createTarget := target
	createTarget.Name = "fresh"
	edited := strings.Replace(k.document, "metadata: {name: reader", `metadata: {labels: {changed: "yes"}, resourceVersion: "7", name: reader`, 1)
	return map[string]func() error{
		"preview create": func() error {
			_, err := h.writer.PreviewCreate(ctx, CreateChange{Target: createTarget, YAML: created})
			return err
		},
		"create": func() error {
			_, err := h.writer.ApplyCreate(ctx, CreateChange{Target: createTarget, YAML: created})
			return err
		},
		"preview apply": func() error {
			_, err := h.writer.PreviewManifest(ctx, ManifestChange{Target: target, YAML: edited, ResourceVersion: "7"})
			return err
		},
		"apply": func() error {
			_, err := h.writer.ApplyManifest(ctx, ManifestChange{Target: target, YAML: edited, ResourceVersion: "7"})
			return err
		},
		"delete": func() error {
			where, err := h.writer.Locate(ctx, target)
			if err != nil {
				return err
			}
			_, err = Delete(ctx, where, target, metav1.DeleteOptions{})
			return err
		},
	}
}

func frankCtx(t *testing.T) context.Context {
	return identity.With(t.Context(), identity.Who{Name: "frank@example.com", Method: identity.SSO, Groups: []string{"oidc:admins"}})
}

func TestEveryRBACWriteIsRefusedUnderTheServerProfile(t *testing.T) {
	for _, k := range rbacKinds {
		for _, family := range []string{"preview create", "create", "preview apply", "apply", "delete"} {
			t.Run(k.kind+"/"+family, func(t *testing.T) {
				h := newRBACHarness(t, true)
				err := rbacWrites(h, k, frankCtx(t))[family]()
				refusal := refusalOf(t, err)
				if refusal.Kind != RefusedNotInProfile {
					t.Errorf("refused as %d, want RefusedNotInProfile: %s", refusal.Kind, refusal.Reason)
				}
				if !strings.HasPrefix(refusal.Reason, "RBAC changes are not available in the server profile in this version") {
					t.Errorf("the refusal does not say why in words: %q", refusal.Reason)
				}
				if len(*h.sent) != 0 {
					t.Errorf("an RBAC write reached the cluster under the server profile: %v", *h.sent)
				}
				// Refused before the review: the person's rights were not asked.
				for _, q := range h.reviews.Asked() {
					if q.Attributes.Group == "rbac.authorization.k8s.io" {
						t.Errorf("a review was asked for a write the profile refuses: %+v", q.Attributes)
					}
				}
			})
		}
	}
}

// A refused act is recorded: who tried, what, the profile's decision.
func TestARefusedRBACWriteIsRecorded(t *testing.T) {
	h := newRBACHarness(t, true)
	k := rbacKinds[2]
	if err := rbacWrites(h, k, frankCtx(t))["delete"](); err == nil {
		t.Fatal("a delete of a RoleBinding went through under the server profile")
	}
	records := h.sink.records(t)
	if len(records) != 2 {
		t.Fatalf("got %d records, want the attempt and the outcome: %+v", len(records), records)
	}
	for _, r := range records {
		if r.Decision != string(access.NotInProfile) || r.Verb != "delete" || r.Resource != "rolebindings" || r.Who != "frank@example.com" {
			t.Errorf("record %+v; want frank's delete of rolebindings, decision not_in_profile", r)
		}
	}
	if records[1].Outcome != audit.Refused {
		t.Errorf("outcome %q, want refused", records[1].Outcome)
	}
}

// The desktop profile is unchanged: the same writes are reviewed and sent.
func TestRBACWritesStillGoThroughOnTheDesktop(t *testing.T) {
	for _, k := range rbacKinds {
		for _, family := range []string{"create", "apply", "delete"} {
			t.Run(k.kind+"/"+family, func(t *testing.T) {
				h := newRBACHarness(t, false)
				if err := rbacWrites(h, k, t.Context())[family](); err != nil {
					t.Fatalf("an RBAC %s was refused on the desktop: %v", family, err)
				}
				if len(*h.sent) == 0 {
					t.Error("nothing reached the cluster")
				}
			})
		}
	}
}

// Under the server profile the fence is RBAC's alone: another kind's write
// still goes.
func TestTheServerProfileStillWritesOtherKinds(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{configMapGVR: "ConfigMapList"}, object(t, coreDNS))
	var sent []string
	dyn.PrependReactor("delete", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		sent = append(sent, action.GetResource().Resource)
		return false, nil, nil
	})
	gate := access.New(access.Options{Reviews: (&accesstest.Reviews{}).For, InCluster: true, Audit: audit.New(&recordSink{})})
	w := New(Options{Resolve: fakeResolver{dyn: dyn, cluster: "prod"}, Access: gate})
	target := Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"}
	ctx := frankCtx(t)
	where, err := w.Locate(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Delete(ctx, where, target, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("a ConfigMap delete was refused under the server profile: %v", err)
	}
	if len(sent) != 1 {
		t.Errorf("sent %v, want the one delete", sent)
	}
}

package changes

// A rollout history that cannot be read must not render as "no rollouts".
// `Report.Note` is filled only by `noteFor`, which speaks about managedFields and
// only when there are no changes at all, so the rollout error has to be kept.
//
// A Forbidden on `replicasets` is ordinary under tight RBAC: a role granting
// `get` on deployments and nothing on replicasets is the common shape. Dropping
// the error renders it identically to a Deployment that has never been rolled
// out — *confidently reporting the absence of the panel's own subject*
// (`docs/DEFINITION-OF-DONE.md` §2).

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

var (
	deploymentGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	replicaSetGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
)

func deployment(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{
			"name": name, "namespace": namespace, "resourceVersion": "1",
			"uid": "dep-uid",
		},
		"spec": map[string]any{
			"selector": map[string]any{"matchLabels": map[string]any{"app": name}},
		},
	}}
}

func replicaSet(namespace, name, owner, revision string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "ReplicaSet",
		"metadata": map[string]any{
			"name": name, "namespace": namespace, "resourceVersion": "1",
			"labels":      map[string]any{"app": owner},
			"annotations": map[string]any{"deployment.kubernetes.io/revision": revision},
			"ownerReferences": []any{map[string]any{
				"apiVersion": "apps/v1", "kind": "Deployment", "name": owner, "uid": "dep-uid",
			}},
		},
		"spec": map[string]any{"replicas": int64(3)},
	}}
}

// deploymentReader builds a Reader over a fake dynamic client that knows about
// Deployments and ReplicaSets. `refuse`, when set, is what a list of
// `replicasets` answers with.
func deploymentReader(t *testing.T, refuse error, objects ...runtime.Object) *Reader {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config")
	body := `apiVersion: v1
kind: Config
clusters:
- name: nowhere
  cluster:
    server: https://127.0.0.1:1
contexts:
- name: prod
  context: {cluster: nowhere, user: nobody}
users:
- name: nobody
  user: {token: none}
current-context: prod
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing a kubeconfig: %v", err)
	}
	clusters, err := cluster.NewManager(path)
	if err != nil {
		t.Fatalf("loading the kubeconfig: %v", err)
	}

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			{Group: "apps", Version: "v1", Resource: "deployments"}: "DeploymentList",
			replicaSetGVR: "ReplicaSetList",
		},
		objects...,
	)
	if refuse != nil {
		dyn.PrependReactor("list", "replicasets", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, refuse
		})
	}

	reader := NewReader(clusters, accesstest.Allow())
	// The context is taken and ignored: this fake reaches no cluster, and the
	// parameter is there because the real builder's discovery walk is
	// cancelable.
	reader.newMapper = func(context.Context, *cluster.Client) (meta.RESTMapper, error) {
		m := meta.NewDefaultRESTMapper([]schema.GroupVersion{deploymentGVK.GroupVersion()})
		m.Add(deploymentGVK, meta.RESTScopeNamespace)
		return m, nil
	}
	reader.dynamicOf = func(*cluster.Client) (dynamic.Interface, error) { return dyn, nil }
	return reader
}

func TestARolloutHistoryThatCouldNotBeReadIsNotAnAbsenceOfRollouts(t *testing.T) {
	refusal := apierrors.NewForbidden(
		schema.GroupResource{Group: "apps", Resource: "replicasets"}, "",
		errAccessDenied{},
	)
	reader := deploymentReader(t, refusal, deployment("prod", "api"))

	report, err := reader.Read(context.Background(), "prod", Request{
		Kind: "deployments", Namespace: "prod", Name: "api",
	})
	if err != nil {
		t.Fatalf("Read: %v; the managedFields half still reads, so the whole panel must not fail", err)
	}
	if !report.Revisioned {
		t.Errorf("Revisioned = false on a Deployment; the kind keeps a rollout history whether or not it could be read")
	}
	if report.RolloutsErr == nil {
		t.Fatalf("RolloutsErr = nil after a Forbidden on replicasets; this is the panel reporting the absence of its own subject")
	}
	if !apierrors.IsForbidden(report.RolloutsErr) {
		t.Errorf("RolloutsErr = %v, want the API server's own Forbidden so the edge can classify it", report.RolloutsErr)
	}
	if len(report.Rollouts) != 0 {
		t.Errorf("Rollouts = %v; nothing was read", report.Rollouts)
	}
}

// The other half of the distinction, which is what makes this a distinction
// rather than a new failure: a Deployment that has genuinely never been rolled
// out still answers with no rollouts and no error.
func TestADeploymentThatHasNeverShippedAnswersWithNoError(t *testing.T) {
	reader := deploymentReader(t, nil, deployment("prod", "api"))

	report, err := reader.Read(context.Background(), "prod", Request{
		Kind: "deployments", Namespace: "prod", Name: "api",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !report.Revisioned {
		t.Errorf("Revisioned = false on a Deployment")
	}
	if report.RolloutsErr != nil {
		t.Errorf("RolloutsErr = %v; the list succeeded and held nothing", report.RolloutsErr)
	}
	if len(report.Rollouts) != 0 {
		t.Errorf("Rollouts = %v, want none", report.Rollouts)
	}
}

func TestADeploymentThatHasShippedStillReportsItsRevisions(t *testing.T) {
	reader := deploymentReader(t, nil,
		deployment("prod", "api"),
		replicaSet("prod", "api-7d9", "api", "2"),
	)

	report, err := reader.Read(context.Background(), "prod", Request{
		Kind: "deployments", Namespace: "prod", Name: "api",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if report.RolloutsErr != nil {
		t.Fatalf("RolloutsErr = %v", report.RolloutsErr)
	}
	if len(report.Rollouts) != 1 {
		t.Fatalf("Rollouts = %v, want the one revision that exists", report.Rollouts)
	}
	if report.Rollouts[0].Revision != "2" {
		t.Errorf("revision = %q, want 2", report.Rollouts[0].Revision)
	}
}

// errAccessDenied is the cause apierrors.NewForbidden wants. Its own type so the
// message reads like the API server's rather than like a test fixture.
type errAccessDenied struct{}

func (errAccessDenied) Error() string {
	return "User \"system:serviceaccount:kubetower:console\" cannot list resource \"replicasets\" in API group \"apps\""
}

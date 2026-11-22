package web

import (
	"context"
	"fmt"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/edit"
)

// A cluster for `apiActs` to act on, and nothing else.
//
// `internal/actions` has a fuller harness and it is not importable from here, so
// this is the smallest copy that lets a test walk the whole JSON mapping —
// preview, act, the next stage — against objects it can move underneath the
// plan. It runs against a fake clientset and must never run against anything
// else: this file cordons nodes and evicts pods.

var (
	fakePodGVR        = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	fakeNodeGVR       = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	fakeCronJobGVR    = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}
	fakeDeploymentGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	fakeNamespaceGVR  = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
)

type fakeActsResolver struct{ dyn dynamic.Interface }

func (f fakeActsResolver) Locate(_ context.Context, cluster, kind string) (edit.Location, error) {
	switch strings.ToLower(kind) {
	case "pods", "pod":
		return edit.Location{Cluster: cluster, Resource: fakePodGVR, Kind: "Pod", Namespaced: true, Dynamic: f.dyn}, nil
	case "nodes", "node":
		return edit.Location{Cluster: cluster, Resource: fakeNodeGVR, Kind: "Node", Namespaced: false, Dynamic: f.dyn}, nil
	case "cronjobs", "cronjob":
		return edit.Location{Cluster: cluster, Resource: fakeCronJobGVR, Kind: "CronJob", Namespaced: true, Dynamic: f.dyn}, nil
	case "jobs", "job":
		return edit.Location{Cluster: cluster, Resource: fakeJobGVR, Kind: "Job", Namespaced: true, Dynamic: f.dyn}, nil
	case "deployments", "deployment":
		return edit.Location{Cluster: cluster, Resource: fakeDeploymentGVR, Kind: "Deployment", Namespaced: true, Dynamic: f.dyn}, nil
	case "namespaces", "namespace":
		return edit.Location{Cluster: cluster, Resource: fakeNamespaceGVR, Kind: "Namespace", Namespaced: false, Dynamic: f.dyn}, nil
	}
	return edit.Location{}, fmt.Errorf("%q is not a kind this cluster serves", kind)
}

// actsHarness is a Server with a write path over a fake cluster.
type actsHarness struct {
	dyn  *dynamicfake.FakeDynamicClient
	acts apiActs
}

func newActsHarness(t *testing.T, objects ...*unstructured.Unstructured) *actsHarness {
	t.Helper()
	seeds := make([]runtime.Object, 0, len(objects))
	for _, object := range objects {
		seeds = append(seeds, object)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			fakePodGVR: "PodList", fakeNodeGVR: "NodeList",
			fakeCronJobGVR: "CronJobList", fakeDeploymentGVR: "DeploymentList",
			fakeNamespaceGVR: "NamespaceList", fakeJobGVR: "JobList",
		}, seeds...)

	// The eviction subresource, intercepted for the reason the actions harness
	// gives: the default tracker treats a subresource create as an update of the
	// parent and would replace the pod with the Eviction.
	dyn.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create, ok := action.(k8stesting.CreateAction)
		if !ok || create.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		return true, create.GetObject(), nil
	})

	writer := edit.New(edit.Options{
		Resolve: fakeActsResolver{dyn: dyn},
		Logf:    func(string, ...any) {},
		Access:  accesstest.Allow(),
	})
	runner := actions.New(actions.Options{Writer: writer})
	return &actsHarness{dyn: dyn, acts: apiActs{s: &Server{act: runner}}}
}

// evictions counts the eviction requests that reached the cluster, which is
// the one write a drain's second stage exists to send and its first must not.
func (h *actsHarness) evictions() int {
	count := 0
	for _, action := range h.dyn.Actions() {
		if action.GetVerb() == "create" && action.GetSubresource() == "eviction" {
			count++
		}
	}
	return count
}

// bump rewrites the object's resourceVersion in place, which is what any
// other writer does to it between a preview and a click.
func (h *actsHarness) bump(t *testing.T, gvr schema.GroupVersionResource, namespace, name, version string) {
	t.Helper()
	ctx := context.Background()
	live, err := h.dyn.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading %s to move it: %v", name, err)
	}
	live.SetResourceVersion(version)
	if _, err := h.dyn.Resource(gvr).Namespace(namespace).Update(ctx, live, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("moving %s: %v", name, err)
	}
}

func fakeNode(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Node",
		"metadata":   map[string]any{"name": name, "uid": "uid-" + name, "resourceVersion": "7"},
		"spec":       map[string]any{},
	}}
}

// fakePod is a running pod on node-a, owned by a ReplicaSet unless the caller
// changes that — so a drain lists it under "evict" with no warning.
func fakePod(namespace, name string, mutate ...func(map[string]any)) *unstructured.Unstructured {
	object := map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name": name, "namespace": namespace, "uid": "uid-" + name, "resourceVersion": "100",
			"ownerReferences": []any{map[string]any{
				"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": "api", "uid": "uid-rs", "controller": true,
			}},
		},
		"spec":   map[string]any{"nodeName": "node-a"},
		"status": map[string]any{"phase": "Running"},
	}
	for _, m := range mutate {
		m(object)
	}
	return &unstructured.Unstructured{Object: object}
}

// fakeCronJob is a CronJob with a template to run, not suspended and with no
// run in flight unless the caller changes that.
func fakeCronJob(namespace, name string, mutate ...func(map[string]any)) *unstructured.Unstructured {
	object := map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "CronJob",
		"metadata": map[string]any{
			"name": name, "namespace": namespace, "uid": "uid-" + name,
			"resourceVersion": "40", "generation": int64(2),
		},
		"spec": map[string]any{
			"schedule": "0 3 * * *",
			"suspend":  false,
			"jobTemplate": map[string]any{"spec": map[string]any{
				"template": map[string]any{"spec": map[string]any{"restartPolicy": "Never"}},
			}},
		},
	}
	for _, m := range mutate {
		m(object)
	}
	return &unstructured.Unstructured{Object: object}
}

// fakeDeployment is a Deployment nothing owns and no tool manages, unless the
// caller changes that.
func fakeDeployment(namespace, name string, mutate ...func(map[string]any)) *unstructured.Unstructured {
	object := map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name": name, "namespace": namespace, "uid": "uid-" + name,
			"resourceVersion": "60", "generation": int64(5),
		},
		"spec": map[string]any{
			"replicas": int64(3),
			"template": map[string]any{"metadata": map[string]any{}, "spec": map[string]any{}},
		},
	}
	for _, m := range mutate {
		m(object)
	}
	return &unstructured.Unstructured{Object: object}
}

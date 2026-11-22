package actions

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// The few places this package touches the Kubernetes API types directly,
// kept together so the shapes that must be exact are readable in one screen.

const deletePropagationBackground = metav1.DeletePropagationBackground

func deleteOptions(uid *types.UID, policy *metav1.DeletionPropagation, force bool) metav1.DeleteOptions {
	options := metav1.DeleteOptions{PropagationPolicy: policy}
	if force {
		// `kubectl delete --grace-period=0`: only when the operator ticked
		// "Force delete" on a single delete.
		zero := int64(0)
		options.GracePeriodSeconds = &zero
	}
	if uid != nil && *uid != "" {
		// Delete this object, not whatever now holds its name.
		options.Preconditions = &metav1.Preconditions{UID: uid}
	}
	return options
}

// newEviction is the body of a pod eviction.
//
// `policy/v1`, served since 1.22 (`policy/v1beta1` was removed in 1.25).
// The name and namespace must match the pod addressed: the API server
// checks, and a mismatch is a 400 that reads as an internal error.
//
// No `deleteOptions`: the pod's own terminationGracePeriodSeconds applies,
// a graceful shutdown its authors wrote on purpose.
func newEviction(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "policy/v1",
		"kind":       "Eviction",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
	}}
}

func marshal(body map[string]any) ([]byte, error) {
	return json.Marshal(body)
}

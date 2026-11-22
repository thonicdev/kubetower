package detail

import (
	"maps"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The drawer's verbs are chosen from the toggle state read off the object:
// `spec.suspend` on a CronJob, `spec.unschedulable` on a Node, an absent field
// read as false, as the API server reads it.
func TestSwitchesOf(t *testing.T) {
	cronJob := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "CronJob"}
	node := schema.GroupVersionKind{Version: "v1", Kind: "Node"}
	object := func(spec map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	}
	cases := []struct {
		name string
		raw  *unstructured.Unstructured
		gvk  schema.GroupVersionKind
		want map[string]string
	}{
		{"suspended CronJob", object(map[string]any{"suspend": true}), cronJob, map[string]string{"spec.suspend": "true"}},
		{"active CronJob", object(map[string]any{"suspend": false}), cronJob, map[string]string{"spec.suspend": "false"}},
		{"CronJob with no suspend", object(map[string]any{}), cronJob, map[string]string{"spec.suspend": "false"}},
		{"cordoned node", object(map[string]any{"unschedulable": true}), node, map[string]string{"spec.unschedulable": "true"}},
		{"schedulable node", object(map[string]any{}), node, map[string]string{"spec.unschedulable": "false"}},
		{"suspend of the wrong type", object(map[string]any{"suspend": "yes"}), cronJob, nil},
		{"a CronJob of another group", object(map[string]any{"suspend": true}), schema.GroupVersionKind{Group: "example.com", Kind: "CronJob"}, nil},
		{"a Job", object(map[string]any{"suspend": true}), schema.GroupVersionKind{Group: "batch", Kind: "Job"}, nil},
	}
	for _, c := range cases {
		if got := switchesOf(c.raw, c.gvk); !maps.Equal(got, c.want) {
			t.Errorf("%s: switchesOf = %v, want %v", c.name, got, c.want)
		}
	}
}

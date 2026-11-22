package resources

import (
	"maps"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A row carries which half of a toggle its object is on, whatever its columns
// show: the selection bar reads it, never a cell.
func TestRowSwitches(t *testing.T) {
	yes := true
	cases := []struct {
		object metav1.Object
		want   map[string]string
	}{
		{&batchv1.CronJob{Spec: batchv1.CronJobSpec{Suspend: &yes}}, map[string]string{"spec.suspend": "true"}},
		{&batchv1.CronJob{}, map[string]string{"spec.suspend": "false"}},
		{&corev1.Node{Spec: corev1.NodeSpec{Unschedulable: true}}, map[string]string{"spec.unschedulable": "true"}},
		{&corev1.Node{}, map[string]string{"spec.unschedulable": "false"}},
		{&corev1.Pod{}, nil},
	}
	for _, c := range cases {
		if got := switchesOf(c.object); !maps.Equal(got, c.want) {
			t.Errorf("switchesOf(%T) = %v, want %v", c.object, got, c.want)
		}
	}
}

package actions

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A node holding only pods a drain spares because nothing owns them is not as
// far as a drain goes: a drain planned to include them evicts them. The first
// stage must not say "every one is left alone by a drain. That is as far as a
// drain takes this node" over a single bare pod.
func TestAFirstStageOverOnlyUnownedPodsSaysTheyCanBeIncluded(t *testing.T) {
	for _, tc := range []struct {
		name  string
		other bool
	}{
		{"a bare pod alone", false},
		{"a bare pod beside a DaemonSet's", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := []*unstructured.Unstructured{node("node-a"), pod("prod", "bare")}
			if tc.other {
				objects = append(objects, pod("kube-system", "fluent-bit-x", ownedBy("DaemonSet", "fluent-bit")))
			}
			h := newHarness(t, nil, objects...)
			result, err := h.runner.Run(context.Background(), Request{
				Target: target("nodes", "", "node-a"), Verb: Drain, Confirm: "node-a",
			})
			if err != nil {
				t.Fatalf("drain: %v", err)
			}
			if spared := result.Plan.SparedUnowned(); len(spared) != 1 || spared[0] != "prod/bare" {
				t.Fatalf("spared = %v", spared)
			}
			message := result.Message
			if strings.Contains(message, "every one is left alone") || strings.Contains(message, "as far as a drain") ||
				!strings.Contains(message, "not empty") || !strings.Contains(message, "prod/bare") ||
				!strings.Contains(message, "planned again to include the pods nothing owns") {
				t.Errorf("the first stage does not tell a pod a drain may include from one no drain evicts: %q", message)
			}
			if got := strings.Contains(message, "The other 1 pod(s) are left alone by every drain."); got != tc.other {
				t.Errorf("the DaemonSet's pod is said to be left by every drain: %v, want %v: %q", got, tc.other, message)
			}
			for _, write := range h.writes() {
				if write.Subresource == "eviction" {
					t.Fatalf("the first stage evicted: %+v", write)
				}
			}
		})
	}
}

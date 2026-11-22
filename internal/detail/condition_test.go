package detail

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A failing condition is the one fact on this panel whose value is a paragraph,
// and it has to say so.
//
// A Pod that cannot be scheduled carries a 260-character `PodScheduled`
// message. The drawer draws the facts in a `.grid`, `.grid td` is
// `white-space: nowrap` for the resource *lists*, and `white-space` inherits —
// so the sentence would be laid out on one line, 876px of it outside a 1600px
// window, and the only way to read the end would be to scroll sideways.
//
// The answer is a flag on the view model rather than a rule keyed off the label,
// which is the split `Class` and `Pill` are already on: this package says what
// the value *is*, the stylesheet decides what that looks like. The flag is what
// this test pins, because a `Fact` literal that quietly loses one field compiles
// and renders perfectly.
func TestAFailingConditionIsMarkedAsASentence(t *testing.T) {
	message := "0/1 nodes are available: 1 node(s) didn't have free ports for the " +
		"requested pod ports. no new claims to deallocate, preemption: 0/1 nodes are " +
		"available: 1 No preemption victims found for incoming pod"

	pod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "api-0", "namespace": "shop"},
		"status": map[string]any{
			"conditions": []any{
				// The failing one, which is what gets pulled onto the panel.
				map[string]any{
					"type": "PodScheduled", "status": "False",
					"reason": "Unschedulable", "message": message,
				},
				// And one that is fine, to keep the count honest: only the
				// conditions a controller is complaining through are drawn at all.
				map[string]any{"type": "Initialized", "status": "True"},
			},
		},
	}}

	facts := conditionsOf(pod, time.Now())
	if len(facts) != 1 {
		t.Fatalf("%d condition(s) drawn, want 1 — only the failing one belongs on the panel: %+v", len(facts), facts)
	}

	got := facts[0]
	if got.Label != "PodScheduled" {
		t.Errorf("the condition is labeled %q", got.Label)
	}
	if !strings.Contains(got.Value, "No preemption victims") {
		t.Errorf("the message did not survive into the fact: %q", got.Value)
	}
	if !got.Sentence {
		t.Error("the condition is not marked as a sentence, so drawer.html draws its cell " +
			"without `.message` and it inherits the list table's `white-space: nowrap`. " +
			"876px of this exact message was then outside a 1600px window, reachable only " +
			"by scrolling the drawer sideways")
	}
}

// And nothing else on the panel claims to be prose.
//
// The value of marking a sentence is that it is the exception: a name, an API
// version, a timestamp and a node all read better held on one line, which is
// what the list rule gives them for free. Marking everything would be the
// blanket rule again in the other direction, and it would be invisible — every
// cell would simply wrap.
func TestOnlyASentenceIsMarkedAsOne(t *testing.T) {
	pod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{
			"name": "api-0", "namespace": "shop", "generation": int64(3),
			"ownerReferences": []any{map[string]any{
				"apiVersion": "apps/v1", "kind": "ReplicaSet",
				"name": "api-6f9d8b7c4", "uid": "u", "controller": true,
			}},
		},
		"spec":   map[string]any{"nodeName": "ip-10-0-1-7"},
		"status": map[string]any{"phase": "Pending"},
	}}

	for _, fact := range factsOf(pod, pod.GroupVersionKind()) {
		if fact.Sentence {
			t.Errorf("%q is marked as prose, and it is a %s — the cell will wrap where one "+
				"line reads better", fact.Label, "value the cluster names rather than writes")
		}
	}
}

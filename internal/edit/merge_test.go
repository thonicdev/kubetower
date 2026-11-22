package edit

import (
	"encoding/json"
	"reflect"
	"testing"
)

// normalized is the pipeline both sides of a real comparison go through.
func normalised(t *testing.T, doc string) map[string]any {
	t.Helper()
	out, err := parseDocument(doc)
	if err != nil {
		t.Fatalf("parsing %q: %v", doc, err)
	}
	return out
}

func TestMergePatchCarriesOnlyWhatChanged(t *testing.T) {
	base := normalised(t, "a: 1\nb:\n  c: 2\n  d: 3\n")
	edit := normalised(t, "a: 1\nb:\n  c: 2\n  d: 4\n")

	patch := mergePatch(base, edit)
	want := map[string]any{"b": map[string]any{"d": json.Number("4")}}
	if !reflect.DeepEqual(patch, want) {
		t.Fatalf("want %#v, got %#v", want, patch)
	}
}

// The failure this normalization exists for. An unstructured object holds int64
// and a YAML decode holds float64; without a common shape, `8080` on one side is
// not equal to `8080` on the other and every save would patch every number in
// the document.
func TestNumbersDoNotDriftBetweenTheTwoSides(t *testing.T) {
	live := map[string]any{"spec": map[string]any{"port": int64(8080), "ratio": 0.5}}
	base, err := normalise(live)
	if err != nil {
		t.Fatalf("normalizing: %v", err)
	}
	edit := normalised(t, "spec:\n  port: 8080\n  ratio: 0.5\n")

	if patch := mergePatch(base, edit); len(patch) != 0 {
		t.Fatalf("an untouched document produced a patch: %v", patch)
	}
}

// RFC 7386 has no way to say "remove the third element", and inventing one would
// be a semantics of our own that the API server does not share.
func TestAnArrayIsReplacedWholesale(t *testing.T) {
	base := normalised(t, "spec:\n  args: [a, b, c]\n")
	edit := normalised(t, "spec:\n  args: [a, c]\n")

	patch := mergePatch(base, edit)
	args, ok := patch["spec"].(map[string]any)["args"].([]any)
	if !ok || len(args) != 2 {
		t.Fatalf("the whole array should be sent, got %v", patch)
	}
}

func TestARemovedKeyBecomesANull(t *testing.T) {
	base := normalised(t, "metadata:\n  labels:\n    a: one\n    b: two\n")
	edit := normalised(t, "metadata:\n  labels:\n    a: one\n")

	patch := mergePatch(base, edit)
	labels := patch["metadata"].(map[string]any)["labels"].(map[string]any)
	value, present := labels["b"]
	if !present || value != nil {
		t.Fatalf("want an explicit null for the removed key, got %v", patch)
	}
	if _, untouched := labels["a"]; untouched {
		t.Fatalf("a key that did not change is in the patch: %v", patch)
	}
}

func TestPathsNameWhatIsSent(t *testing.T) {
	patch := map[string]any{
		"spec":     map[string]any{"replicas": 5},
		"metadata": map[string]any{"labels": map[string]any{"team": nil}},
	}
	got := paths(patch)
	want := []string{"metadata.labels.team (removed)", "spec.replicas"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestHasPathSeesAReplacedParent(t *testing.T) {
	// `status` replaced wholesale still counts as touching status.readyReplicas.
	patch := map[string]any{"status": "whatever"}
	if !hasPath(patch, "status.readyReplicas") {
		t.Fatal("a replaced parent must count as a change to what is under it")
	}
	if hasPath(patch, "spec.replicas") {
		t.Fatal("hasPath found a path that is not in the patch")
	}
}

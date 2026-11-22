package edit

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/audit"
)

// The Secret a person with no rights in `payments` is guessing about.
const paymentsSecret = `
apiVersion: v1
kind: Secret
metadata:
  name: db
  namespace: payments
  resourceVersion: "7"
data:
  stripe_live_key: c2VjcmV0
binaryData:
  blob: AAEC
`

const paymentsImmutable = `
apiVersion: v1
kind: Secret
metadata:
  name: db
  namespace: payments
  resourceVersion: "7"
immutable: true
data:
  stripe_live_key: c2VjcmV0
`

// deniedPreview runs the data preview as a person every review refuses, against
// a cluster holding objects, and returns the refusal and whether the object was
// read.
func deniedPreview(t *testing.T, key, version string, objects ...*unstructured.Unstructured) (*Refusal, bool) {
	t.Helper()
	h := newHarness(t, nil, objects...)
	h.reviews.Answer = denyAll
	_, err := h.writer.PreviewKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "secrets", Namespace: "payments", Name: "db"},
		Key:             key,
		Value:           "x",
		ResourceVersion: version,
	})
	refusal := refusalOf(t, err)
	h.noWrites()
	return refusal, readTheObject(h)
}

func readTheObject(h *harness) bool {
	for _, action := range h.dyn.Actions() {
		if action.GetVerb() == "get" {
			return true
		}
	}
	return false
}

// TestADeniedPersonLearnsNothingFromTheDataPreview: a preview that read the
// Secret with the console's rights and answered from it before the dry run
// reviewed anybody would give five different sentences - absent, no such key,
// binary, immutable and a stale version - so a person with no rights in the
// namespace would learn which Secrets exist and what their keys are called.
// Every one of those must be the same refusal, made before the object is read.
func TestADeniedPersonLearnsNothingFromTheDataPreview(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		version string
		objects []*unstructured.Unstructured
	}{
		{"no such object", "stripe_live_key", "1", nil},
		{"the object, no such key", "aws_secret_access_key", "7", []*unstructured.Unstructured{object(t, paymentsSecret)}},
		{"the object and the key, a stale version", "stripe_live_key", "1", []*unstructured.Unstructured{object(t, paymentsSecret)}},
		{"a binary key", "blob", "7", []*unstructured.Unstructured{object(t, paymentsSecret)}},
		{"an immutable object", "stripe_live_key", "7", []*unstructured.Unstructured{object(t, paymentsImmutable)}},
	}

	var first *Refusal
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			refusal, read := deniedPreview(t, c.key, c.version, c.objects...)
			if read {
				t.Error("the object was read before the person was reviewed")
			}
			if refusal.Kind != RefusedByAccess {
				t.Errorf("want the access refusal, got %v: %s", refusal.Kind, refusal.Reason)
			}
			if first == nil {
				first = refusal
				return
			}
			if refusal.Reason != first.Reason {
				t.Errorf("a denied person can tell this case from the first one:\n  %q\n  %q", refusal.Reason, first.Reason)
			}
		})
	}
}

// TestADeniedPersonLearnsNothingFromTheManifestPreview is the manifest half:
// absent, a stale version and an immutable object answer alike, unread.
func TestADeniedPersonLearnsNothingFromTheManifestPreview(t *testing.T) {
	cases := []struct {
		name    string
		version string
		objects []*unstructured.Unstructured
	}{
		{"no such object", "7", nil},
		{"a stale version", "1", []*unstructured.Unstructured{object(t, paymentsSecret)}},
		{"an immutable object", "7", []*unstructured.Unstructured{object(t, paymentsImmutable)}},
	}

	var first string
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, nil, c.objects...)
			h.reviews.Answer = denyAll
			_, err := h.writer.PreviewManifest(t.Context(), ManifestChange{
				Target:          Target{Cluster: "prod", Kind: "secrets", Namespace: "payments", Name: "db"},
				YAML:            "apiVersion: v1\nkind: Secret\nmetadata:\n  name: db\n  namespace: payments\ndata:\n  stripe_live_key: eA==\n",
				ResourceVersion: c.version,
			})
			refusal := refusalOf(t, err)
			h.noWrites()
			if readTheObject(h) {
				t.Error("the object was read before the person was reviewed")
			}
			if refusal.Kind != RefusedByAccess {
				t.Errorf("want the access refusal, got %v: %s", refusal.Kind, refusal.Reason)
			}
			if first == "" {
				first = refusal.Reason
				return
			}
			if refusal.Reason != first {
				t.Errorf("a denied person can tell this case from the first one:\n  %q\n  %q", refusal.Reason, first)
			}
		})
	}
}

// TestAPreviewAsksPatchAndThenGetBeforeReading pins the questions: `patch`,
// the write's own, and then `get`, because a preview also shows the object (a
// diff's before side, an equality with the current value). A person allowed
// patch but not get is refused, and the object is still not read.
func TestAPreviewAsksPatchAndThenGetBeforeReading(t *testing.T) {
	h := newHarness(t, nil, object(t, paymentsSecret))
	h.reviews.Answer = func(q accesstest.Question) (bool, error) { return q.Attributes.Verb == "patch", nil }

	_, err := h.writer.PreviewKey(t.Context(), KeyChange{
		Target: Target{Cluster: "prod", Kind: "secrets", Namespace: "payments", Name: "db"},
		Key:    "stripe_live_key", Value: "x", ResourceVersion: "7",
	})
	if refusal := refusalOf(t, err); refusal.Kind != RefusedByAccess {
		t.Fatalf("patch without get was refused as %v: %s", refusal.Kind, refusal.Reason)
	}
	var verbs []string
	for _, q := range h.reviews.Asked() {
		a := q.Attributes
		if a.Resource != "secrets" || a.Namespace != "payments" || a.Name != "db" {
			t.Errorf("asked about %+v, want secrets payments/db", a)
		}
		verbs = append(verbs, a.Verb)
	}
	if strings.Join(verbs, ",") != "patch,get" {
		t.Errorf("asked %v, want patch then get", verbs)
	}
	if readTheObject(h) {
		t.Error("the object was read for a person who may not get it")
	}

	// And a person denied everything is asked the write's own question first.
	denied := newHarness(t, nil, object(t, paymentsSecret))
	denied.reviews.Answer = denyAll
	_, _ = denied.writer.PreviewManifest(t.Context(), ManifestChange{
		Target:          Target{Cluster: "prod", Kind: "secrets", Namespace: "payments", Name: "db"},
		YAML:            "apiVersion: v1\nkind: Secret\nmetadata:\n  name: db\n  namespace: payments\n",
		ResourceVersion: "7",
	})
	if asked := denied.reviews.Asked(); len(asked) != 1 || asked[0].Attributes.Verb != "patch" {
		t.Errorf("a denied manifest preview asked %+v, want patch alone", asked)
	}
}

// TestARefusedPreviewIsRecorded: the refusal comes ahead of the dry run, where
// Gate.Perform would have recorded it, so it is recorded here: a dry-run write,
// refused, naming the question that said no.
func TestARefusedPreviewIsRecorded(t *testing.T) {
	h := newHarness(t, nil, object(t, paymentsSecret))
	sink := &recordSink{}
	gate := access.New(access.Options{Reviews: (&accesstest.Reviews{Answer: denyAll}).For, Audit: audit.New(sink)})
	w := New(Options{Resolve: fakeResolver{dyn: h.dyn, cluster: "prod"}, Access: gate})

	_, err := w.PreviewKey(t.Context(), KeyChange{
		Target: Target{Cluster: "prod", Kind: "secrets", Namespace: "payments", Name: "db"},
		Key:    "stripe_live_key", Value: "x", ResourceVersion: "7",
	})
	refusalOf(t, err)
	records := sink.records(t)
	if len(records) == 0 {
		t.Fatal("a refused preview left no audit record")
	}
	last := records[len(records)-1]
	if last.Outcome != audit.Refused || !last.DryRun || last.Verb != "patch" || last.Resource != "secrets" || last.Name != "db" {
		t.Errorf("the refused preview was recorded as %+v", last)
	}
}

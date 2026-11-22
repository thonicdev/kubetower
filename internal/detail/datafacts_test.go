package detail

// What the Details tab says about a ConfigMap or a Secret, and what it does not.
//
// A Secret's keys are on Data, with Show and Edit per row; drawing them again on
// Details as a table of facts, each value *held back*, would be two tabs, one
// list, the second of which can do nothing the first can. So Details keeps what
// is *about* the object and never lists the keys; Data keeps the keys.

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/textutil"
)

func typedSecret(namespace, name, kind string, immutable bool, data map[string]any) *unstructured.Unstructured {
	object := map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": name, "namespace": namespace, "resourceVersion": "1"},
		"type":     kind,
		"data":     data,
	}
	if immutable {
		object["immutable"] = true
	}
	return &unstructured.Unstructured{Object: object}
}

func factByLabel(facts []Fact, label string) (Fact, bool) {
	for _, fact := range facts {
		if fact.Label == label {
			return fact, true
		}
	}
	return Fact{}, false
}

// A Secret's facts name its type, count its keys and say whether it is
// immutable — and carry no key name at all. The key list is the Data tab's.
func TestASecretsFactsDescribeTheObjectAndNeverListItsKeys(t *testing.T) {
	reader, _ := readerFor(t, typedSecret("shop", "db", "kubernetes.io/tls", true, map[string]any{
		"tls.crt": "Y2VydA==",
		"tls.key": "a2V5",
	}))

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "secrets", Namespace: "shop", Name: "db",
	})
	if err != nil {
		t.Fatalf("reading the secret: %v", err)
	}

	for _, key := range []string{"tls.crt", "tls.key"} {
		if _, listed := factByLabel(object.Facts, key); listed {
			t.Errorf("the key %q is a fact on the Details tab; the keys are the Data tab's and are listed there", key)
		}
	}

	if fact, ok := factByLabel(object.Facts, "Type"); !ok || fact.Value != "kubernetes.io/tls" {
		t.Errorf("the type did not arrive as a fact: %+v", object.Facts)
	}
	if fact, ok := factByLabel(object.Facts, "Keys"); !ok || fact.Value != "2" {
		t.Errorf("the number of keys did not arrive as a fact: %+v", object.Facts)
	}
	if fact, ok := factByLabel(object.Facts, "Immutable"); !ok || fact.Value != "yes" || !fact.Pill {
		t.Errorf("immutability did not arrive as a badge: %+v", object.Facts)
	}

	// And nothing on the tab is a value, masked or otherwise.
	for _, fact := range object.Facts {
		if strings.Contains(fact.Value, SecretValueMask) || fact.Value == "Y2VydA==" || fact.Value == "a2V5" {
			t.Errorf("a fact carries a value or its mask: %+v", fact)
		}
	}
}

// The same split for a ConfigMap: keys counted, not listed. A ConfigMap has no
// type, so there is no Type fact rather than an empty one; and "not immutable"
// is said, because the answer is *immutable or not* and an absent row is not an
// answer.
func TestAConfigMapsFactsCountItsKeysWithoutListingThem(t *testing.T) {
	reader, _ := readerFor(t, configMapObject("shop", "settings", map[string]any{
		"LOG_LEVEL":  "debug",
		"nginx.conf": "worker_processes 1;\n",
		"threshold":  "0.7",
	}))

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "configmaps", Namespace: "shop", Name: "settings",
	})
	if err != nil {
		t.Fatalf("reading the configmap: %v", err)
	}

	for _, key := range []string{"LOG_LEVEL", "nginx.conf", "threshold"} {
		if _, listed := factByLabel(object.Facts, key); listed {
			t.Errorf("the key %q is a fact on the Details tab", key)
		}
	}
	if _, ok := factByLabel(object.Facts, "Type"); ok {
		t.Errorf("a ConfigMap grew a Type fact: %+v", object.Facts)
	}
	if fact, ok := factByLabel(object.Facts, "Keys"); !ok || fact.Value != "3" {
		t.Errorf("the number of keys did not arrive: %+v", object.Facts)
	}
	if fact, ok := factByLabel(object.Facts, "Immutable"); !ok || fact.Value != "no" || fact.Pill {
		t.Errorf("a mutable object is not said to be one: %+v", object.Facts)
	}
}

// Nothing of this reaches a kind without a data block. A pod's facts stay as
// they are.
func TestAKindWithoutADataBlockGrowsNoDataFacts(t *testing.T) {
	pod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "api-0", "namespace": "shop", "resourceVersion": "1"},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "api", "image": "api:1"}}},
	}}
	reader, _ := readerFor(t, pod)

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "pods", Namespace: "shop", Name: "api-0",
	})
	if err != nil {
		t.Fatalf("reading the pod: %v", err)
	}
	for _, label := range []string{"Keys", "Immutable", "Type"} {
		if _, ok := factByLabel(object.Facts, label); ok {
			t.Errorf("a pod carries a %s fact: %+v", label, object.Facts)
		}
	}
}

// Secret values masked, through the call the drawer makes. `TestTrimmedMasksASecretsValues
// AndKeepsItsKeys` pins the function; this pins that `Reader.Get` hands the
// panel a document that went through it, with a value planted where a leak
// would show.
func TestASecretReadForTheDrawerCarriesNoValueInItsManifest(t *testing.T) {
	const planted = "cGxhbnRlZC1jcmVkZW50aWFs"
	reader, _ := readerFor(t, typedSecret("shop", "db", "Opaque", false, map[string]any{
		"password": planted,
	}))

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "secrets", Namespace: "shop", Name: "db",
	})
	if err != nil {
		t.Fatalf("reading the secret: %v", err)
	}
	if strings.Contains(object.YAML, planted) {
		t.Fatalf("the planted value is in the manifest the drawer draws:\n%s", object.YAML)
	}
	if !strings.Contains(object.YAML, "password: "+SecretValueMask) {
		t.Fatalf("the key is not there with its mask:\n%s", object.YAML)
	}
}

// The fourth fact: the total size.
//
// `DataSet.Total` sums the decoded sizes, and the number is worth showing — a
// ConfigMap is capped at 1 MiB by the API and hitting that is a real failure
// mode. It is the one fact that explains an `apply` the API refuses, and a
// number computed and drawn by nobody explains nothing.
//
// Asserted against `textutil.BytesCompact` of a size this test computes, rather
// than against a literal: a literal would pin the formatter's output here and
// break in this file the day the formatter changes, which is the copy of a rule
// this repository already keeps in one place.
func TestAConfigMapsFactsSayHowBigItIs(t *testing.T) {
	// 700 KiB in one key and 400 KiB in another: over the 1 MiB ceiling, which
	// is the case the fact exists for. Plain text, so decoded size is length.
	big := strings.Repeat("a", 700<<10)
	rest := strings.Repeat("b", 400<<10)
	reader, _ := readerFor(t, configMapObject("shop", "large", map[string]any{
		"one": big,
		"two": rest,
	}))

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "configmaps", Namespace: "shop", Name: "large",
	})
	if err != nil {
		t.Fatalf("reading the configmap: %v", err)
	}
	if object.Data == nil {
		t.Fatal("the configmap carries no data block")
	}

	want := textutil.BytesCompact(int64(len(big) + len(rest)))
	fact, ok := factByLabel(object.Facts, "Size")
	if !ok {
		t.Fatalf("there is no Size fact; Total() is computed and drawn by nobody: %+v", object.Facts)
	}
	if fact.Value != want {
		t.Errorf("Size = %q, want %q", fact.Value, want)
	}
	// Over the ceiling, which is the whole point of drawing it. A figure that
	// could never exceed 1 MiB would explain no refused write.
	if object.Data.Total() <= 1<<20 {
		t.Errorf("the fixture is %d bytes; this test only means something above the API's 1 MiB cap",
			object.Data.Total())
	}
}

// And a kind with no data block grows no Size fact, for the same reason it grows
// no Keys fact: a pod has no block to measure and `0 B` would read as one that
// is empty.
func TestAKindWithoutADataBlockGrowsNoSizeFact(t *testing.T) {
	pod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "api-1", "namespace": "shop", "resourceVersion": "1"},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "api", "image": "api:1"}}},
	}}
	reader, _ := readerFor(t, pod)

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "pods", Namespace: "shop", Name: "api-1",
	})
	if err != nil {
		t.Fatalf("reading the pod: %v", err)
	}
	if _, ok := factByLabel(object.Facts, "Size"); ok {
		t.Errorf("a pod carries a Size fact: %+v", object.Facts)
	}
}

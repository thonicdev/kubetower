package detail

// The annotations the Details tab may draw, and the two it may not.
//
// Every test here goes through `Reader.Get` rather than calling `annotationsOf`
// directly, because what this file guards is not a function's logic but a
// member filled from `raw.GetAnnotations()` on the way to a panel. The question
// is what the drawer receives, and the only honest way to ask it is to read an
// object the way the drawer does.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// annotated puts a metadata.annotations block on an object built by one of the
// helpers in this package.
func annotated(object *unstructured.Unstructured, annotations map[string]string) *unstructured.Unstructured {
	object.SetAnnotations(annotations)
	return object
}

// The whole reason this member needed a file of its own.
//
// `kubectl apply` stores the object it last sent — including `data` — as one
// JSON line in an annotation. A Details tab filled from `raw.GetAnnotations()`
// therefore ships every value of a Secret in clear to every client, which is
// exactly the leak `maskSecretValues` stops on the Manifest tab.
//
// Asserted on the **bytes**, and over the whole object rather than over the
// annotation map: a leak that the map does not carry and the YAML does is still
// a leak, and `detailPageOf` maps this struct's members by reference — so a
// string absent from these bytes cannot be on the wire.
func TestTheLastAppliedAnnotationNeverReachesTheDrawer(t *testing.T) {
	const planted = "cGxhbnRlZC1jcmVkZW50aWFs"
	lastApplied := `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"db","namespace":"shop"},` +
		`"data":{"password":"` + planted + `"}}`

	reader, _ := readerFor(t, annotated(
		typedSecret("shop", "db", "Opaque", false, map[string]any{"password": planted}),
		map[string]string{
			LastAppliedAnnotation:       lastApplied,
			"meta.helm.sh/release-name": "shop",
		},
	))

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "secrets", Namespace: "shop", Name: "db",
	})
	if err != nil {
		t.Fatalf("reading the secret: %v", err)
	}

	if _, carried := object.Annotations[LastAppliedAnnotation]; carried {
		t.Errorf("the last-applied annotation is on the Details tab: %v", object.Annotations)
	}

	bytes, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("marshalling the object the drawer receives: %v", err)
	}
	if strings.Contains(string(bytes), planted) {
		t.Fatalf("the planted value is in the bytes the drawer receives; "+
			"grep for %q in:\n%s", planted, bytes)
	}

	if _, present := object.Annotations[LastAppliedAnnotation]; present {
		t.Errorf("the last-applied annotation is in the map the drawer receives")
	}

	// The rest of the block is still a block. A treatment that took the whole map
	// with it would pass every assertion above and answer the operator's question
	// with nothing. On a Secret the value is masked — that is the second
	// treatment and it is what `TestASecretsAnnotationValuesAreMasked...` is
	// about; what matters here is that the key is still listed.
	if value, present := object.Annotations["meta.helm.sh/release-name"]; !present || value != SecretValueMask {
		t.Errorf("an ordinary annotation did not survive as a listed key: %v", object.Annotations)
	}
	if len(object.Annotations) != 1 {
		t.Errorf("annotations = %v, want exactly the one that is not last-applied", object.Annotations)
	}
}

// The second treatment, and the one that costs something. An annotation is free
// text: nothing distinguishes a note from a credential somebody put there, so on
// a Secret every value is the same mask the manifest uses. The **keys are
// kept** — a key withheld and a key absent are the two answers that must not
// render the same.
func TestASecretsAnnotationValuesAreMaskedAndItsKeysAreNot(t *testing.T) {
	reader, _ := readerFor(t, annotated(
		typedSecret("shop", "db", "Opaque", false, map[string]any{"password": "c2VjcmV0"}),
		map[string]string{
			"rotation/previous-password": "hunter2",
			"meta.helm.sh/release-name":  "shop",
			"marker":                     "",
		},
	))

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "secrets", Namespace: "shop", Name: "db",
	})
	if err != nil {
		t.Fatalf("reading the secret: %v", err)
	}

	for _, key := range []string{"rotation/previous-password", "meta.helm.sh/release-name"} {
		value, present := object.Annotations[key]
		if !present {
			t.Errorf("the key %q is gone; a withheld value must still say which annotation it belonged to", key)
			continue
		}
		if value != SecretValueMask {
			t.Errorf("%s = %q on a Secret, want the mask", key, value)
		}
	}
	// An annotation with no value is not a withheld one. `<masked>` here would
	// claim something was held back that never existed.
	if value, present := object.Annotations["marker"]; !present || value != "" {
		t.Errorf("an empty annotation arrived as %q (present=%v); it is empty, not withheld", value, present)
	}
	if strings.Contains(strings.Join(valuesOf(object.Annotations), " "), "hunter2") {
		t.Error("an annotation value reached the drawer on a Secret")
	}
}

// And the ordinary case, which is most objects: the annotations arrive as they
// are, and nothing claims a treatment that was not applied. A note that is
// always set is a note nobody reads.
func TestAnOrdinaryKindsAnnotationsArriveWholeAndWithNoNote(t *testing.T) {
	reader, _ := readerFor(t, annotated(
		configMapObject("shop", "settings", map[string]any{"LOG_LEVEL": "debug"}),
		map[string]string{
			"meta.helm.sh/release-name":        "shop",
			"nginx.org/mergeable-ingress-type": "master",
		},
	))

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "configmaps", Namespace: "shop", Name: "settings",
	})
	if err != nil {
		t.Fatalf("reading the configmap: %v", err)
	}

	if object.Annotations["meta.helm.sh/release-name"] != "shop" ||
		object.Annotations["nginx.org/mergeable-ingress-type"] != "master" {
		t.Errorf("the annotations did not arrive: %v", object.Annotations)
	}
}

// An object with no annotations carries none, rather than an empty map that a
// client would draw an empty heading over.
func TestAnObjectWithNoAnnotationsCarriesNone(t *testing.T) {
	reader, _ := readerFor(t, configMapObject("shop", "plain", map[string]any{"a": "1"}))

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "configmaps", Namespace: "shop", Name: "plain",
	})
	if err != nil {
		t.Fatalf("reading the configmap: %v", err)
	}
	if object.Annotations != nil {
		t.Errorf("annotations = %v, want none at all", object.Annotations)
	}
}

// The one annotation that carries the object again is the only one a Secret can
// lose entirely — and when it is the only annotation there is, the member is
// absent rather than an empty map, with the note still saying why.
func TestASecretWhoseOnlyAnnotationIsTheLastAppliedOneCarriesNone(t *testing.T) {
	reader, _ := readerFor(t, annotated(
		typedSecret("shop", "only", "Opaque", false, map[string]any{"password": "c2VjcmV0"}),
		map[string]string{LastAppliedAnnotation: `{"kind":"Secret"}`},
	))

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "secrets", Namespace: "shop", Name: "only",
	})
	if err != nil {
		t.Fatalf("reading the secret: %v", err)
	}
	if len(object.Annotations) != 0 {
		t.Errorf("annotations = %v, want none", object.Annotations)
	}
}

func valuesOf(of map[string]string) []string {
	out := make([]string, 0, len(of))
	for _, value := range of {
		out = append(out, value)
	}
	return out
}

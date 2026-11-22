package detail

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

const configMapDoc = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: coredns
  namespace: kube-system
  resourceVersion: "204"
data:
  Corefile: |
    .:53 {
        errors
        health
    }
  empty: ""
binaryData:
  bundle.gz: H4sIAAAAAAAA/w==
`

const secretDoc = `
apiVersion: v1
kind: Secret
metadata:
  name: registry
  namespace: shop
  resourceVersion: "77"
type: kubernetes.io/dockerconfigjson
data:
  password: c3VwZXItc2VjcmV0
  keystore.p12: /wD/AA==
`

func setOf(t *testing.T, doc, kind string) DataSet {
	t.Helper()
	set := dataOf(object(t, doc), kind)
	if set == nil {
		t.Fatalf("%s produced no data block", kind)
	}
	return *set
}

func entry(t *testing.T, set DataSet, key string) DataEntry {
	t.Helper()
	found, ok := set.Entry(key)
	if !ok {
		t.Fatalf("no key %q; got %v", key, keysOf(set))
	}
	return found
}

func keysOf(set DataSet) []string {
	var out []string
	for _, e := range set.Entries {
		out = append(out, e.Key)
	}
	return out
}

func TestOnlyConfigMapsAndSecretsHaveADataBlock(t *testing.T) {
	if dataOf(object(t, configMapDoc), "Pod") != nil {
		t.Fatal("a Pod was given a data panel")
	}
	if !HasData("configmap") || !HasData("Secret") || HasData("Deployment") {
		t.Fatal("HasData does not agree with itself")
	}
}

// A size is the size of the value, not the length of its base64 — a third too
// large is a number that looks plausible and is wrong on every Secret in the
// cluster.
func TestSizesAreOfTheDecodedValue(t *testing.T) {
	set := setOf(t, secretDoc, "Secret")

	password := entry(t, set, "password")
	if password.Size != len("super-secret") {
		t.Fatalf("want %d bytes, got %d", len("super-secret"), password.Size)
	}

	configMap := setOf(t, configMapDoc, "ConfigMap")
	bundle := entry(t, configMap, "bundle.gz")
	if bundle.Size != 10 {
		t.Fatalf("the binaryData size is not decoded: %d", bundle.Size)
	}
	if bundle.Field != FieldBinaryData {
		t.Fatalf("bundle.gz is filed under %q", bundle.Field)
	}
}

// The whole reason the panel is built this way. A Secret's values are not in the
// view model of an ordinary open, so no mistake in a template can put one on
// screen; only an explicit reveal can.
func TestASecretsValuesAreAbsentUntilOneIsAskedFor(t *testing.T) {
	raw := object(t, secretDoc)
	set := *dataOf(raw, "Secret")

	for _, e := range set.Entries {
		if e.Value != "" {
			t.Fatalf("the value of %q is in the panel without being asked for", e.Key)
		}
		if e.Preview != "" {
			t.Fatalf("a Secret must not have a preview: %q", e.Preview)
		}
		if !e.Secret {
			t.Fatalf("%q is not marked as a secret value", e.Key)
		}
	}

	set.revealInto(raw, "password")
	if set.Revealed != "password" {
		t.Fatalf("nothing was revealed")
	}
	revealed := entry(t, set, "password")
	if revealed.Value != "super-secret" {
		t.Fatalf("the revealed value is %q", revealed.Value)
	}
	// And only that one.
	if other := entry(t, set, "keystore.p12"); other.Value != "" {
		t.Fatalf("a second value came with it: %q", other.Value)
	}
}

// A value that is not valid UTF-8 has to say so. Printed, it is mojibake, and
// mojibake reads as a corrupted value rather than as a value this panel cannot
// show.
func TestBinaryValuesAreNamedRatherThanPrinted(t *testing.T) {
	set := setOf(t, secretDoc, "Secret")

	keystore := entry(t, set, "keystore.p12")
	if !keystore.Binary {
		t.Fatal("a value that is not UTF-8 was not marked binary")
	}
	if keystore.Editable {
		t.Fatal("a binary value is offered for editing; a text area would corrupt it")
	}

	// And it stays absent even when it is the key that was asked for.
	raw := object(t, secretDoc)
	set = *dataOf(raw, "Secret")
	set.revealInto(raw, "keystore.p12")
	if set.Revealed != "" || entry(t, set, "keystore.p12").Value != "" {
		t.Fatal("a binary value was revealed as text")
	}
}

func TestAConfigMapCarriesItsPreviewAndItsShape(t *testing.T) {
	set := setOf(t, configMapDoc, "ConfigMap")

	corefile := entry(t, set, "Corefile")
	if !strings.HasPrefix(corefile.Preview, ".:53") {
		t.Fatalf("the preview is not the first line: %q", corefile.Preview)
	}
	if corefile.Lines != 4 {
		t.Fatalf("want 4 lines, got %d", corefile.Lines)
	}
	if !corefile.Editable {
		t.Fatal("a text value of a mutable ConfigMap should be editable")
	}
	if set.ResourceVersion != "204" {
		t.Fatalf("the panel carries no precondition: %q", set.ResourceVersion)
	}

	// "present but empty" is a real and common cause, and is not the same thing
	// as absent.
	empty := entry(t, set, "empty")
	if empty.Size != 0 || empty.Binary {
		t.Fatalf("an empty value came out as %+v", empty)
	}
}

// A map has no order. An unordered list of keys reshuffles itself under the
// reader every time the panel is drawn.
func TestKeysAreSorted(t *testing.T) {
	set := setOf(t, configMapDoc, "ConfigMap")
	got := keysOf(set)
	want := []string{"Corefile", "empty", "bundle.gz"} // data first, then binaryData
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
}

func TestAnImmutableObjectOffersNoEditing(t *testing.T) {
	raw := object(t, configMapDoc)
	raw.Object["immutable"] = true
	set := *dataOf(raw, "ConfigMap")

	if !set.Immutable {
		t.Fatal("immutable: true was not read")
	}
	for _, e := range set.Entries {
		if e.Editable {
			t.Fatalf("%q is offered for editing on an immutable object", e.Key)
		}
	}
}

func TestAnEmptyBlockSaysSoRatherThanDrawingNothing(t *testing.T) {
	set := setOf(t, "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: none}\n", "ConfigMap")
	if len(set.Entries) != 0 {
		t.Fatalf("keys appeared from nowhere: %v", keysOf(set))
	}
	if set.Note == "" {
		t.Fatal("an empty panel has to say it is empty; blank reads as broken")
	}
}

// Trimmed is what the drawer shows and what internal/edit diffs against. The two
// have to be the same function, and this is the test that says what it removes.
func TestTrimmedRemovesOnlyTheNoise(t *testing.T) {
	raw := object(t, `
apiVersion: v1
kind: ConfigMap
metadata:
  name: coredns
  namespace: kube-system
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: '{"a":1}'
    team: platform
  managedFields:
    - manager: kubectl
data:
  a: b
`)
	trimmed := Trimmed(raw)

	if len(trimmed.GetManagedFields()) != 0 {
		t.Fatal("managedFields survived")
	}
	annotations := trimmed.GetAnnotations()
	if _, present := annotations["kubectl.kubernetes.io/last-applied-configuration"]; present {
		t.Fatal("the last-applied annotation survived")
	}
	if annotations["team"] != "platform" {
		t.Fatal("an ordinary annotation was removed")
	}
	if value, _, _ := unstructured.NestedString(trimmed.Object, "data", "a"); value != "b" {
		t.Fatal("the data was touched")
	}
	// A copy, not the object. Trimming in place would hand every later reader a
	// mutilated object.
	if len(raw.GetManagedFields()) == 0 {
		t.Fatal("Trimmed mutated the object it was given")
	}
}

// TestRevealedNamesTheKeyAndTheValueLivesOnTheEntry pins the shape a caller
// must not read backwards.
//
// `DataSet.Revealed` is a **key name**. Its doc comment says so — "the key whose
// Value is filled" — and `revealInto` assigns `d.Revealed = key`. The value is on
// the entry, and on no other field.
//
// Sending `Revealed` across the wire as `value` would answer a Secret asked for
// `password` with `{"key":"password","value":"password"}` — inside an `ok`
// envelope, which is the arm that means *this is the value*. A fake `RevealEnv`
// and a client test that mocks the body would both stay green over a payload
// neither had seen the server produce.
//
// This test does not reach that mapper — `internal/web` holds a concrete
// `*detail.Reader` and cannot be given a double. What it can do is make the trap
// impossible to walk into, by asserting in one place that the two fields
// mean different things and that a plausible confusion produces a visible
// difference.
func TestRevealedNamesTheKeyAndTheValueLivesOnTheEntry(t *testing.T) {
	raw := object(t, secretDoc)
	set := setOf(t, secretDoc, "Secret")
	set.revealInto(raw, "password")

	if set.Revealed != "password" {
		t.Fatalf("Revealed = %q, want the key name %q", set.Revealed, "password")
	}

	got := entry(t, set, "password")
	if got.Value != "super-secret" {
		t.Fatalf("the entry's Value = %q, want the decoded value %q", got.Value, "super-secret")
	}

	// The assertion that matters, stated as the confusion rather than as two
	// facts: anything sending `Revealed` where a value belongs sends the key.
	if set.Revealed == got.Value {
		t.Fatal("Revealed and the entry's Value are equal, so this test can no longer " +
			"tell a caller reading one for the other apart from a correct one — " +
			"pick a fixture whose key and value differ")
	}

	// And the case that decides where the not-found check may live: a revealed
	// EMPTY value is a real thing in a Secret, and is not the same answer as a
	// key that is not there. A caller testing `Revealed == ""` to mean "nothing
	// was revealed" is reading a third meaning into this field.
	blank := setOf(t, secretDoc, "Secret")
	blank.revealInto(raw, "no-such-key")
	if _, ok := blank.Entry("no-such-key"); ok {
		t.Fatal("the fixture grew a key this case depends on being absent")
	}
}

// --- a Secret's values never leave in a whole-object rendering ---------------

// The manifest panel's type comment and `api/openapi.yaml` both say Secret
// values are masked here, and this pins that they are. A contract that describes a behavior the server does not have is
// worse than one that stays silent, because a reader stops looking.
func TestTrimmedMasksASecretsValuesAndKeepsItsKeys(t *testing.T) {
	raw := object(t, `
apiVersion: v1
kind: Secret
metadata:
  name: db
  namespace: shop
type: Opaque
data:
  password: c3VwZXItc2VjcmV0
  username: YWRtaW4=
`)
	trimmed := Trimmed(raw)

	for _, key := range []string{"password", "username"} {
		value, present, _ := unstructured.NestedString(trimmed.Object, "data", key)
		if !present {
			t.Fatalf("the key %q was removed; the operator can no longer see that it exists", key)
		}
		if value != SecretValueMask {
			t.Fatalf("data.%s is %q, want the mask — that is a credential in a YAML document", key, value)
		}
	}

	// The keys are the half that must survive. A Secret rendered without them is
	// indistinguishable from an empty one: an empty answer and a failed one are
	// different answers.
	data, _, _ := unstructured.NestedStringMap(trimmed.Object, "data")
	if len(data) != 2 {
		t.Fatalf("the Secret shows %d keys, want 2: %v", len(data), data)
	}

	// A copy, never the object. Masking in place would hand the reveal door and
	// every later reader a Secret with no values in it.
	if live, _, _ := unstructured.NestedString(raw.Object, "data", "password"); live != "c3VwZXItc2VjcmV0" {
		t.Fatalf("Trimmed masked the object it was given; the live value is now %q", live)
	}
}

// The mask is scoped to `v1/Secret` by group and kind, not by a name that looks
// like one. A ConfigMap keeps its values — `TestTrimmedRemovesOnlyTheNoise` says
// so from the other side, and this says it about this code path.
func TestTrimmedDoesNotMaskSomethingMerelyCalledSecret(t *testing.T) {
	raw := object(t, `
apiVersion: vault.example.com/v1
kind: Secret
metadata:
  name: not-a-core-secret
  namespace: shop
data:
  address: aHR0cHM6Ly92YXVsdA==
`)
	value, _, _ := unstructured.NestedString(Trimmed(raw).Object, "data", "address")
	if value == SecretValueMask {
		t.Fatal("a custom resource that happens to be called Secret was masked; the rule is the core group, not the word")
	}
}

// The mask cannot collide with a real value, and that is a property rather than
// a hope: every value in a Secret's `data` is base64 as it comes off the API
// server, and the mask is not valid base64. It is what makes "did the operator
// leave the mask alone" a string comparison with no false positive in it.
func TestTheMaskIsNotValidBase64(t *testing.T) {
	if _, err := base64.StdEncoding.DecodeString(SecretValueMask); err == nil {
		t.Fatalf("%q decodes as base64, so it could be mistaken for a value a Secret really holds", SecretValueMask)
	}
}

// The operator has to be told, or `password: <masked>` reads as the value the
// object holds. A comment and not a field: the editor sends this document back
// and a YAML parser drops comments, so it cannot reach the cluster.
func TestTheRenderedManifestSaysItsValuesAreMasked(t *testing.T) {
	secret := manifestOf(object(t, `
apiVersion: v1
kind: Secret
metadata:
  name: db
  namespace: shop
data:
  password: c3VwZXItc2VjcmV0
`))
	if strings.Contains(secret, "c3VwZXItc2VjcmV0") {
		t.Fatalf("the value is in the document: %s", secret)
	}
	if !strings.Contains(secret, "masked") {
		t.Fatalf("nothing tells the operator the values were masked: %s", secret)
	}

	// And nothing is said where nothing was done. A ConfigMap that announced a
	// masking that did not happen would teach the operator to ignore the line.
	plain := manifestOf(object(t, configMapDoc))
	if strings.Contains(plain, "masked") {
		t.Fatalf("a ConfigMap claims its values are masked: %s", plain)
	}
}

// The invariant that makes the edit-path test in internal/edit faithful, pinned
// here because it is this package that could break it.
//
// That test renders the operator's document with `Trimmed`, because `manifestOf`
// is unexported. It is only a valid stand-in while the two agree — and the exact
// way unmasked Secret values could come back is somebody masking in
// `manifestOf` instead, at which
// point the drawer shows masks, the diff bases on values, and an edit to a label
// overwrites a credential. This says the rendered document *is* the trimmed
// object, comments aside.
func TestTheRenderedManifestIsExactlyTheTrimmedObject(t *testing.T) {
	raw := object(t, `
apiVersion: v1
kind: Secret
metadata:
  name: db
  namespace: shop
  labels:
    app: api
type: Opaque
data:
  password: c3VwZXItc2VjcmV0
`)

	var rendered map[string]any
	if err := yaml.Unmarshal([]byte(manifestOf(raw)), &rendered); err != nil {
		t.Fatalf("the document the drawer shows is not parseable YAML: %v", err)
	}

	want, err := yaml.Marshal(Trimmed(raw).Object)
	if err != nil {
		t.Fatalf("marshalling the diff base: %v", err)
	}
	var base map[string]any
	if err := yaml.Unmarshal(want, &base); err != nil {
		t.Fatalf("unmarshalling the diff base: %v", err)
	}

	if !reflect.DeepEqual(rendered, base) {
		t.Fatalf("the document on screen and the base of the edit diff have diverged.\n"+
			"That divergence is how a masked value gets written over a real one.\non screen: %#v\nbase:      %#v",
			rendered, base)
	}
}

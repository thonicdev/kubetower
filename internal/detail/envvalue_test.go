package detail

// Which environment values reach a drawer, and which never can.
//
// A ConfigMap's values are resolved on the drawer open, and the line is: **the
// kind decides, and nothing else.** envfrom_test.go is about the *names* a reference contributes; this
// file is about what is behind them.

import (
	"context"
	"encoding/base64"
	"fmt"
	"reflect"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"
)

// The two markers. Distinctive strings rather than plausible values, because the
// assertion is "this byte sequence is nowhere in the answer" and a value like
// "debug" would match a dozen innocent fields.
const (
	secretMarker    = "KT-MARKER-SECRET-VALUE-a6f1"
	configMapMarker = "KT-MARKER-CONFIGMAP-VALUE-3b02"
)

// podReadingBothKinds is a pod that names a Secret and a ConfigMap in both
// spellings a container has: one key at a time, and the whole object.
//
// Both spellings, because they are resolved by two different passes and a test
// covering one leaves the other free to leak.
func podReadingBothKinds() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "api-0", "namespace": "shop", "resourceVersion": "1"},
		"spec": map[string]any{
			"containers": []any{map[string]any{
				"name": "api", "image": "api:1",
				"env": []any{
					map[string]any{"name": "DSN", "valueFrom": map[string]any{
						"secretKeyRef": map[string]any{"name": "db-credentials", "key": "dsn"},
					}},
					map[string]any{"name": "TUNING", "valueFrom": map[string]any{
						"configMapKeyRef": map[string]any{"name": "api-config", "key": "tuning"},
					}},
				},
				"envFrom": []any{
					map[string]any{"secretRef": map[string]any{"name": "api-secrets"}},
					map[string]any{"configMapRef": map[string]any{"name": "api-tuning"}},
				},
			}},
		},
	}}
}

// **The most important test in this feature.**
//
// A Secret's value must not be in the structure a template is handed, by any
// route: not on the row for its own `secretKeyRef`, not on the rows an `envFrom`
// expands, not folded into a sentence explaining an absence. The check is a walk
// of every string reachable from the object rather than a list of the fields
// that exist today, because the defect this guards against is a *new* field
// filled from the wrong side of the line — and a test naming fields would pass
// on the day one was added.
//
// The ConfigMap half is asserted in the same breath on purpose: without it the
// test passes just as well when nothing is resolved at all, which is how a
// security test comes to be the thing that hides a broken feature.
func TestASecretsValueNeverReachesTheViewModel(t *testing.T) {
	reader, _ := readerFor(t, podReadingBothKinds(),
		secretObject("shop", "db-credentials", encodedData(map[string]string{"dsn": secretMarker})),
		secretObject("shop", "api-secrets", encodedData(map[string]string{"TOKEN": secretMarker})),
		configMapObject("shop", "api-config", map[string]any{"tuning": configMapMarker}),
		configMapObject("shop", "api-tuning", map[string]any{"threads": configMapMarker}),
	)

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "pods", Namespace: "shop", Name: "api-0",
	})
	if err != nil {
		t.Fatalf("reading the pod: %v", err)
	}

	for _, where := range stringsIn(reflect.ValueOf(object), "Object") {
		if strings.Contains(where.text, secretMarker) {
			t.Fatalf("a Secret's value reached the view model at %s: %q.\n"+
				"Nothing downstream can take it out again — it is on the tab that opens by "+
				"default, for a key nobody asked about", where.path, where.text)
		}
	}

	// And the other half of the line: a ConfigMap's value is there, twice, by
	// both routes. A drawer that resolved nothing would satisfy the walk above.
	var found int
	for _, where := range stringsIn(reflect.ValueOf(object), "Object") {
		if strings.Contains(where.text, configMapMarker) {
			found++
		}
	}
	if found < 2 {
		t.Fatalf("a ConfigMap's value is on %d fields, want at least two (the keyRef row and "+
			"the imported row). The test above passes trivially when nothing is resolved", found)
	}
}

// The second of the two guards, reached where the first cannot be.
//
// `dataOf` drops a Secret's values, and TestTheEnvLookupCarriesAConfigMapsValues
// AndNeverASecrets is that guard's own test. This one hands the pass a lookup
// that has *already* broken it — a DataSet marked Secret with its previews
// filled, which no cluster and no `dataOf` can produce — and asserts the pass
// refuses it anyway, on the strength of the kind written in the pod spec.
//
// Two guards over one fact are two guards of which neither is tested, unless
// each is tested somewhere the other cannot reach.
func TestALookupThatLeaksASecretValueIsRefusedByThePassItself(t *testing.T) {
	lying := func(_ context.Context, kind, name string) (*DataSet, error) {
		return &DataSet{
			Kind: kind, Secret: kind == "Secret",
			Entries: []DataEntry{{Key: "TOKEN", Field: FieldData, Preview: secretMarker}},
		}, nil
	}
	containers := []Container{{
		Name:    "api",
		Env:     []EnvVar{{Name: "DSN", Source: &EnvSource{Kind: "Secret", Name: "db", Key: "TOKEN"}, Secret: true}},
		EnvFrom: []Whole{{Kind: "Secret", Name: "api-secrets"}},
	}}
	expandEnvFrom(context.Background(), containers, lying)

	for _, row := range append(append([]EnvVar{}, containers[0].Env...), containers[0].Imported...) {
		if row.Held || strings.Contains(row.Resolved, secretMarker) || strings.Contains(row.Problem, secretMarker) {
			t.Fatalf("the pass took a value from a source it knows is a Secret: %+v", row)
		}
	}
}

// A ConfigMap value is on the row, and nobody pressed anything.
//
// A value that is not a secret needs no click to decode: it is shown directly.
func TestAConfigMapValueIsOnTheRowWithoutBeingAskedFor(t *testing.T) {
	fake := &countingLookup{sets: map[string]*DataSet{
		"ConfigMap/api-config": configMapSet(t, map[string]string{"tuning": "aggressive"}),
		"ConfigMap/api-tuning": configMapSet(t, map[string]string{"threads": "8"}),
	}}
	containers := []Container{{
		Name:    "api",
		Env:     []EnvVar{{Name: "TUNING", Source: &EnvSource{Kind: "ConfigMap", Name: "api-config", Key: "tuning"}}},
		EnvFrom: []Whole{{Kind: "ConfigMap", Name: "api-tuning"}},
	}}
	expandEnvFrom(context.Background(), containers, fake.lookup)

	single := containers[0].Env[0]
	if !single.Held || single.Resolved != "aggressive" || single.Clipped {
		t.Errorf("a configMapKeyRef was not resolved: %+v", single)
	}
	if len(containers[0].Imported) != 1 {
		t.Fatalf("%d imported rows, want 1", len(containers[0].Imported))
	}
	imported := containers[0].Imported[0]
	if !imported.Held || imported.Resolved != "8" {
		t.Errorf("an imported ConfigMap row was not resolved: %+v", imported)
	}
}

// An empty ConfigMap value is told apart from one nobody read.
//
// Both draw as no text in the cell, and they are not the same fact: one is the
// operator's own configuration, the other is this console having failed. The
// same distinction Availability makes on the Overview: an empty answer and a failed one are different answers.
func TestAConfigMapKeyHoldingNothingIsNotAKeyNobodyRead(t *testing.T) {
	fake := &countingLookup{sets: map[string]*DataSet{
		"ConfigMap/api-config": configMapSet(t, map[string]string{"blank": ""}),
	}}
	containers := []Container{{Name: "api", Env: []EnvVar{
		{Name: "BLANK", Source: &EnvSource{Kind: "ConfigMap", Name: "api-config", Key: "blank"}},
	}}}
	expandEnvFrom(context.Background(), containers, fake.lookup)

	row := containers[0].Env[0]
	if !row.Held {
		t.Fatalf("a key that was read and holds the empty string reports as unread: %+v", row)
	}
	if row.Problem != "" {
		t.Errorf("an empty value was reported as a fault: %q", row.Problem)
	}
}

// A value too long for a table cell is folded, and the row says there is more.
//
// A ConfigMap routinely holds a whole nginx.conf under one key. Pasting 40 KB
// into a two-column table is not showing it to anybody, and a silently
// half-shown value is worse than a folded one because nothing on the row
// contradicts it.
func TestALongConfigMapValueIsFoldedAndKeepsItsControl(t *testing.T) {
	long := strings.Repeat("x", envPreviewLimit*4)
	fake := &countingLookup{sets: map[string]*DataSet{
		"ConfigMap/api-config": configMapSet(t, map[string]string{
			"long":  long,
			"lines": "first line\nsecond line",
		}),
	}}
	containers := []Container{{Name: "api", Env: []EnvVar{
		{Name: "LONG", Source: &EnvSource{Kind: "ConfigMap", Name: "api-config", Key: "long"}},
		{Name: "LINES", Source: &EnvSource{Kind: "ConfigMap", Name: "api-config", Key: "lines"}},
	}}}
	expandEnvFrom(context.Background(), containers, fake.lookup)

	one := containers[0].Env[0]
	if !one.Held || !one.Clipped {
		t.Fatalf("a %d-character value was not marked as folded: %+v", len(long), one)
	}
	if n := len([]rune(one.Resolved)); n > envPreviewLimit {
		t.Errorf("the folded value is %d runes, past the %d the cell was measured for. "+
			"textutil.Clip keeps the ellipsis inside the budget for exactly this reason",
			n, envPreviewLimit)
	}

	// And the case with no ellipsis to give it away: a short first line with more
	// underneath it. Nothing in the text says it was cut, which is why Clipped is
	// a field rather than a `strings.HasSuffix`.
	two := containers[0].Env[1]
	if two.Resolved != "first line" || !two.Clipped {
		t.Errorf("a multi-line value came out as %+v; the row must keep a control for the "+
			"rest of it", two)
	}
}

// A `secretKeyRef` costs no read at all.
//
// There is nothing this pass could do with a Secret that came back except drop
// it, and a GET that produces nothing on the page is a request that should not
// have been made. It is also a real saving: `secretKeyRef` is how most pods name
// a credential, and every one of them would otherwise be a round trip on the tab
// that opens by default.
func TestASecretKeyRefIsNotFetchedAtAll(t *testing.T) {
	fake := &countingLookup{sets: map[string]*DataSet{}}
	containers := []Container{{Name: "api", Env: []EnvVar{
		{Name: "DSN", Source: &EnvSource{Kind: "Secret", Name: "db-credentials", Key: "dsn"}, Secret: true},
	}}}
	expandEnvFrom(context.Background(), containers, fake.lookup)

	if len(fake.asked) != 0 {
		t.Errorf("a secretKeyRef cost %v", fake.asked)
	}
	row := containers[0].Env[0]
	if row.Held || row.Problem != "" || row.Absence != AbsenceNone {
		t.Errorf("a Secret-backed row was given an opinion it should not have: %+v", row)
	}
}

// One budget for both spellings, and the ceiling on a drawer open does not move.
//
// The cost claim this feature is allowed to make: a pod with ten containers and
// six distinct ConfigMaps is six reads, whether they are imported, referenced a
// key at a time, or both — and a seventh object is refused exactly as a seventh
// import is. A second budget would double what opening a pod
// costs, silently, on the tab that opens by default.
func TestKeyRefsAndImportsShareOneReadBudget(t *testing.T) {
	fake := &countingLookup{sets: map[string]*DataSet{}}

	build := func(objects int) []Container {
		var containers []Container
		// Ten containers, each naming the same objects: half by import and half
		// by key reference, so both passes see every one of them.
		for c := 0; c < 10; c++ {
			container := Container{Name: fmt.Sprintf("c%d", c)}
			for i := 0; i < objects; i++ {
				name := fmt.Sprintf("config-%d", i)
				fake.sets["ConfigMap/"+name] = configMapSet(t, map[string]string{"K": "v"})
				if i%2 == 0 {
					container.EnvFrom = append(container.EnvFrom, Whole{Kind: "ConfigMap", Name: name})
					continue
				}
				container.Env = append(container.Env, EnvVar{
					Name:   fmt.Sprintf("V%d", i),
					Source: &EnvSource{Kind: "ConfigMap", Name: name, Key: "K"},
				})
			}
			containers = append(containers, container)
		}
		return containers
	}

	// Six objects, sixty references: one read each, nothing skipped.
	containers := build(maxEnvSources)
	if note := expandEnvFrom(context.Background(), containers, fake.lookup); note != "" {
		t.Errorf("six distinct objects are inside the budget and nothing should have been "+
			"skipped; the panel says %q", note)
	}
	if len(fake.asked) != maxEnvSources {
		t.Errorf("sixty references to six objects cost %d reads, want %d: %v",
			len(fake.asked), maxEnvSources, fake.asked)
	}
	for i := range containers {
		for _, row := range containers[i].Env {
			if !row.Held {
				t.Fatalf("container %d has an unresolved row from a cached read: %+v", i, row)
			}
		}
	}

	// And **eight** objects, which is the half that says the budget is shared
	// rather than merely present. Four are imported and four are referenced a key
	// at a time; two budgets of six would read all eight and skip nothing, and
	// the drawer would quietly have doubled what it costs to open a pod.
	fake.asked = nil
	note := expandEnvFrom(context.Background(), build(maxEnvSources+2), fake.lookup)
	if len(fake.asked) != maxEnvSources {
		t.Errorf("eight objects across both spellings cost %d reads, want %d. Resolving a "+
			"configMapKeyRef must spend the budget the imports already had, not a second "+
			"one of its own: %v", len(fake.asked), maxEnvSources, fake.asked)
	}
	if !strings.Contains(note, "2 more") {
		t.Errorf("the panel-wide note does not admit the two objects past the cap: %q", note)
	}
}

// The four ways a ConfigMap-backed value can be absent, and the four sentences.
//
// Rendered as one blank cell they are one answer, and each of them sends the
// operator somewhere different: to RBAC, to the manifest, back to this drawer,
// or to the ConfigMap itself. An empty answer and a failed one are different
// answers.
func TestTheFourWaysAConfigMapValueGoesMissingAreFourSentences(t *testing.T) {
	// Seven distinct ConfigMaps, so the seventh is past maxEnvSources = 6.
	fake := &countingLookup{
		sets: map[string]*DataSet{"ConfigMap/present": configMapSet(t, map[string]string{"there": "yes"})},
		fail: map[string]error{
			"ConfigMap/locked": apierrors.NewForbidden(
				schema.GroupResource{Resource: "configmaps"}, "locked", fmt.Errorf("no")),
			"ConfigMap/gone":     apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, "gone"),
			"ConfigMap/optional": apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, "optional"),
		},
	}

	env := []EnvVar{
		{Name: "A", Source: &EnvSource{Kind: "ConfigMap", Name: "locked", Key: "k"}},
		{Name: "B", Source: &EnvSource{Kind: "ConfigMap", Name: "gone", Key: "k"}},
		{Name: "C", Source: &EnvSource{Kind: "ConfigMap", Name: "optional", Key: "k", Optional: true}},
		{Name: "D", Source: &EnvSource{Kind: "ConfigMap", Name: "present", Key: "absent"}},
		{Name: "F", Source: &EnvSource{Kind: "ConfigMap", Name: "present", Key: "absent", Optional: true}},
	}
	// Two more distinct objects to spend the rest of the budget — four are read
	// above, so six in all — and then the one that is refused by it.
	for _, filler := range []string{"f1", "f2"} {
		fake.sets["ConfigMap/"+filler] = configMapSet(t, map[string]string{"k": "v"})
		env = append(env, EnvVar{Name: filler, Source: &EnvSource{Kind: "ConfigMap", Name: filler, Key: "k"}})
	}
	env = append(env, EnvVar{Name: "E", Source: &EnvSource{Kind: "ConfigMap", Name: "capped", Key: "k"}})
	fake.sets["ConfigMap/capped"] = configMapSet(t, map[string]string{"k": "v"})

	containers := []Container{{Name: "api", Env: env}}
	note := expandEnvFrom(context.Background(), containers, fake.lookup)

	got := containers[0].Env
	for _, want := range []struct {
		index   int
		phrase  string
		absence Absence
		why     string
	}{
		{0, "not allowed to read", AbsenceUnread,
			"a refused read must not draw as a ConfigMap with nothing in it"},
		{1, "there is no", AbsenceExpected,
			"a missing, required ConfigMap is drawn as the reference alone, not as a failed read"},
		{2, "there is no", AbsenceExpected,
			"a missing but optional ConfigMap is not a fault and must not read as one"},
		{3, "no such key", AbsenceUnset,
			"the object was read and does not have the key — asking again cannot answer " +
				"differently, and this one stops the container"},
		{4, "no such key", AbsenceExpected,
			"a missing key on an optional reference is a deliberate default. Thirty of these " +
				"on one healthy Argo CD pod is what a warning pill here costs"},
		{7, "this drawer stops after", AbsenceUnread,
			"a value past the read budget must say so rather than look like an empty one"},
	} {
		row := got[want.index]
		if !strings.Contains(row.Problem, want.phrase) {
			t.Errorf("%s reports %q, want something containing %q — %s",
				row.Name, row.Problem, want.phrase, want.why)
		}
		if row.Held {
			t.Errorf("%s is marked as read: %+v", row.Name, row)
		}
		if strings.Contains(row.Problem, "is optional") || strings.Contains(row.Problem, "optional:") {
			t.Errorf("%s says %q: no sentence speaks of the reference being optional", row.Name, row.Problem)
		}
		if row.Absence != want.absence {
			t.Errorf("%s has Absence=%q, want %q. It decides how loudly the row is drawn and "+
				"whether it still offers a control", row.Name, row.Absence, want.absence)
		}
	}
	// And no per-row sentence names the object or the key: the row already carries
	// both, and repeating them measured 123.5px in one cell on a pod that says it
	// thirty times. See missingKeyNote.
	for _, row := range got[3:5] {
		for _, repeated := range []string{"present", row.Source.Key} {
			if strings.Contains(row.Problem, repeated) {
				t.Errorf("%s repeats %q, which the row already draws beside it: %q",
					row.Name, repeated, row.Problem)
			}
		}
	}
	if !strings.Contains(note, "1 more") {
		t.Errorf("the panel-wide note does not count the object the cap refused: %q", note)
	}
}

// A binary value is said, never printed.
//
// `binaryData` is a perfectly ordinary thing for a `configMapKeyRef` to name — a
// DER certificate, a gzipped bundle. Printed it is mojibake that reads as a
// corrupted value rather than as one this panel cannot show, and it would break
// the layout of every row under it. The same sentence the reveal fragment gives.
func TestABinaryConfigMapValueIsDescribedRatherThanDrawn(t *testing.T) {
	// "//79" is base64 for 0xff 0xfe 0xfd, which is not valid UTF-8 in any
	// arrangement. Nul and the other control bytes would not have done: they are
	// perfectly valid UTF-8, and a fixture built from them tests the opposite of
	// what it says, by asserting that a binary value is drawn.
	set := dataOf(&unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata":   map[string]any{"name": "bundle", "namespace": "shop"},
		"binaryData": map[string]any{"ca.der": "//79"},
	}}, "ConfigMap")
	fake := &countingLookup{sets: map[string]*DataSet{"ConfigMap/bundle": set}}

	containers := []Container{{Name: "api", Env: []EnvVar{
		{Name: "CA", Source: &EnvSource{Kind: "ConfigMap", Name: "bundle", Key: "ca.der"}},
	}}}
	expandEnvFrom(context.Background(), containers, fake.lookup)

	row := containers[0].Env[0]
	if row.Held || row.Resolved != "" {
		t.Fatalf("a value that is not text was drawn anyway: %+v", row)
	}
	if !strings.Contains(row.Problem, "not text") {
		t.Errorf("a binary value reports %q", row.Problem)
	}
	if row.Absence != AbsenceUnprintable {
		t.Errorf("a binary value is marked %q; nothing is wrong with the pod and the row must "+
			"not be drawn as though something were", row.Absence)
	}
}

// And it reaches the drawer through the real reader, with the API server's own
// refusal.
//
// Rendering the pass on its own proves it works and nothing about whether
// anything calls it with a `configMapKeyRef` in front of it — the same gap
// TestOpeningAPodExpandsItsImports closes for the import half.
func TestOpeningAPodResolvesItsConfigMapKeyRefs(t *testing.T) {
	reader, dyn := readerFor(t, podReadingBothKinds(),
		secretObject("shop", "db-credentials", encodedData(map[string]string{"dsn": secretMarker})),
		secretObject("shop", "api-secrets", encodedData(map[string]string{"TOKEN": secretMarker})),
		configMapObject("shop", "api-config", map[string]any{"tuning": "aggressive"}),
		configMapObject("shop", "api-tuning", map[string]any{"threads": "8"}),
	)

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "pods", Namespace: "shop", Name: "api-0",
	})
	if err != nil {
		t.Fatalf("reading the pod: %v", err)
	}
	tuning := envNamed(t, object.Containers[0].Env, "TUNING")
	if !tuning.Held || tuning.Resolved != "aggressive" {
		t.Fatalf("a configMapKeyRef was not resolved on the way through Reader.Get: %+v", tuning)
	}

	// And the same pod on a cluster that refuses ConfigMaps: the row says which
	// of the two it is, rather than drawing like a key holding nothing.
	reader, dyn = readerFor(t, podReadingBothKinds(),
		configMapObject("shop", "api-config", map[string]any{"tuning": "aggressive"}))
	dyn.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "configmaps"}, "api-config", fmt.Errorf("nope"))
	})
	object, err = reader.Get(context.Background(), "prod", Request{
		Kind: "pods", Namespace: "shop", Name: "api-0",
	})
	if err != nil {
		t.Fatalf("reading the pod: %v", err)
	}
	tuning = envNamed(t, object.Containers[0].Env, "TUNING")
	if tuning.Held || !strings.Contains(tuning.Problem, "not allowed") {
		t.Fatalf("a refused ConfigMap read came out as %+v", tuning)
	}
}

// --- helpers -------------------------------------------------------------------

func envNamed(t *testing.T, vars []EnvVar, name string) EnvVar {
	t.Helper()
	for _, v := range vars {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("no variable named %s in %+v", name, vars)
	return EnvVar{}
}

// encodedData is a Secret's data block as the API holds it.
func encodedData(pairs map[string]string) map[string]any {
	out := map[string]any{}
	for key, value := range pairs {
		out[key] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	return out
}

type located struct{ path, text string }

// stringsIn walks every string reachable from a value, with the path it was
// found at.
//
// Reflection rather than a list of fields, because the field that leaks a Secret
// is by definition one nobody thought about: a test naming Value, Resolved and
// Problem passes on the day a fourth is added. It follows pointers, slices, maps
// and structs, which is every shape `detail.Object` is made of.
func stringsIn(v reflect.Value, path string) []located {
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.String:
		return []located{{path, v.String()}}
	case reflect.Ptr, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return stringsIn(v.Elem(), path)
	case reflect.Slice, reflect.Array:
		var out []located
		for i := 0; i < v.Len(); i++ {
			out = append(out, stringsIn(v.Index(i), fmt.Sprintf("%s[%d]", path, i))...)
		}
		return out
	case reflect.Map:
		var out []located
		for _, key := range v.MapKeys() {
			at := fmt.Sprintf("%s[%v]", path, key)
			out = append(out, stringsIn(key, at)...)
			out = append(out, stringsIn(v.MapIndex(key), at)...)
		}
		return out
	case reflect.Struct:
		var out []located
		for i := 0; i < v.NumField(); i++ {
			// An unexported field cannot be read through reflection, and nothing
			// here has one that a template could reach either.
			if !v.Type().Field(i).IsExported() {
				continue
			}
			out = append(out, stringsIn(v.Field(i), path+"."+v.Type().Field(i).Name)...)
		}
		return out
	}
	return nil
}

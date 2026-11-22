package detail

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// --- the resolving pass, with no cluster anywhere near it ----------------------

// countingLookup answers with fixed objects and records what it was asked.
//
// The sets it hands back are built by `dataOf`, the same function the real
// lookup ends in, so a fixture cannot carry a value the production path would
// have dropped — which is the property most of the tests below are about. The
// one test that needs a lookup to lie builds its own, further down.
type countingLookup struct {
	sets  map[string]*DataSet
	fail  map[string]error
	asked []string
}

func (c *countingLookup) lookup(_ context.Context, kind, name string) (*DataSet, error) {
	c.asked = append(c.asked, kind+"/"+name)
	if err, ok := c.fail[kind+"/"+name]; ok {
		return nil, err
	}
	if set, ok := c.sets[kind+"/"+name]; ok {
		return set, nil
	}
	// Nothing configured is an object that exists and holds no keys, which is a
	// real state with its own sentence on the panel — not a failure.
	return &DataSet{Kind: kind, Secret: kind == "Secret"}, nil
}

// configMapSet and secretSet build a lookup's answer the way the cluster does.
//
// Through `dataOf` rather than by hand: a hand-built DataSet would let a test
// assert something about a shape the real reader never produces, and the shape
// is the subject here. A Secret's values are base64 on the wire, so the fixture
// encodes them — anything else would be measuring a Secret that could not exist.
func configMapSet(t *testing.T, pairs map[string]string) *DataSet {
	t.Helper()
	data := map[string]any{}
	for key, value := range pairs {
		data[key] = value
	}
	set := dataOf(configMapObject("shop", "fixture", data), "ConfigMap")
	if set == nil {
		t.Fatal("dataOf produced no set for a ConfigMap")
	}
	return set
}

func secretSet(t *testing.T, pairs map[string]string) *DataSet {
	t.Helper()
	data := map[string]any{}
	for key, value := range pairs {
		data[key] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	set := dataOf(secretObject("shop", "fixture", data), "Secret")
	if set == nil {
		t.Fatal("dataOf produced no set for a Secret")
	}
	return set
}

func containerWith(name string, imports ...Whole) Container {
	return Container{Name: name, EnvFrom: imports}
}

// The names are listed, and the prefix is on them.
//
// The prefix is the part that is easy to leave out and impossible to notice: the
// variable the process actually sees is APP_TOKEN, and a panel printing TOKEN
// names something that does not exist in that container.
func TestAnImportBecomesRowsWithItsPrefixApplied(t *testing.T) {
	fake := &countingLookup{sets: map[string]*DataSet{
		// dataOf sorts by key, so these come back PASSWORD then TOKEN.
		"Secret/api-secrets": secretSet(t, map[string]string{"TOKEN": "t", "PASSWORD": "p"}),
	}}
	containers := []Container{containerWith("api", Whole{Kind: "Secret", Name: "api-secrets", Prefix: "APP_"})}

	if note := expandEnvFrom(context.Background(), containers, fake.lookup); note != "" {
		t.Errorf("nothing was skipped, so the note must be empty; got %q", note)
	}

	got := containers[0].Imported
	if len(got) != 2 {
		t.Fatalf("%d imported rows, want 2: %+v", len(got), got)
	}
	if got[0].Name != "APP_PASSWORD" || got[1].Name != "APP_TOKEN" {
		t.Errorf("the prefix was not applied: %q, %q", got[0].Name, got[1].Name)
	}
	if got[1].Source == nil || got[1].Source.Key != "TOKEN" || got[1].Source.Name != "api-secrets" {
		t.Errorf("the row does not carry the address of its own key: %+v", got[1].Source)
	}
	if !got[0].Secret {
		t.Error("a row read out of a Secret is not marked as one, so the panel cannot say what " +
			"asking for its value will do")
	}
	if containers[0].EnvFrom[0].Keys != 2 {
		t.Errorf("the import reports %d keys, want 2", containers[0].EnvFrom[0].Keys)
	}
}

// The whole safety of the feature, stated as a test.
//
// The lookup returns a DataSet rather than a list of names, so a ConfigMap's
// values do reach this pass — which makes this test necessary. The pass must put a Secret's value nowhere: not in
// Value, not in Resolved, not in a Problem sentence.
func TestExpandingAnImportNeverPutsAValueInARow(t *testing.T) {
	fake := &countingLookup{sets: map[string]*DataSet{
		"Secret/api-secrets": secretSet(t, map[string]string{
			"TOKEN":    "hunter2",
			"PASSWORD": "AKIAIOSFODNN7EXAMPLE",
		}),
	}}
	containers := []Container{containerWith("api", Whole{Kind: "Secret", Name: "api-secrets"})}
	expandEnvFrom(context.Background(), containers, fake.lookup)

	if len(containers[0].Imported) != 2 {
		t.Fatalf("%d rows, want 2", len(containers[0].Imported))
	}
	for _, row := range containers[0].Imported {
		if row.Value != "" || row.Resolved != "" || row.Held {
			t.Fatalf("an imported row from a Secret carries a value: %+v. It would be on the "+
				"tab that opens by default, for a key nobody asked about", row)
		}
	}
}

// Read once per object, however many containers import it.
//
// A pod whose app and sidecar share a ConfigMap is one GET, not two. This is the
// difference between a drawer that opens and a drawer that opens eventually.
func TestTheSameImportIsReadOnceForTheWholePod(t *testing.T) {
	fake := &countingLookup{sets: map[string]*DataSet{
		"ConfigMap/shared": configMapSet(t, map[string]string{"A": "1"}),
	}}
	shared := Whole{Kind: "ConfigMap", Name: "shared"}
	containers := []Container{
		containerWith("api", shared),
		containerWith("sidecar", shared),
		containerWith("metrics", shared),
	}
	expandEnvFrom(context.Background(), containers, fake.lookup)

	if len(fake.asked) != 1 {
		t.Errorf("three containers importing one ConfigMap cost %d reads: %v", len(fake.asked), fake.asked)
	}
	for i := range containers {
		if len(containers[i].Imported) != 1 {
			t.Errorf("container %d got %d rows from a cached read", i, len(containers[i].Imported))
		}
	}
}

// The cap bites, and the panel says so.
//
// A capped list that does not admit it is a lie the operator acts on — the same
// reason RelationsNote exists. The unread import says why *it* is bare, and the
// note counts them.
func TestTheNumberOfImportsResolvedIsCappedAndSaidSo(t *testing.T) {
	// A literal count, deliberately, and not maxEnvSources+n: written that way
	// the fixture grows with the constant and the test passes whatever the cap
	// is — including no cap at all, which is the one thing it exists to catch.
	// Nine imports, six of them read, three said to be missing.
	const declared = 9
	const want = 6

	if maxEnvSources != want {
		t.Fatalf("the cap is %d and this test is written for %d. The cap is a decision about "+
			"how much a drawer open may cost; changing it should mean coming here and saying "+
			"so, not watching a self-referential test agree with itself", maxEnvSources, want)
	}

	fake := &countingLookup{sets: map[string]*DataSet{}}
	c := Container{Name: "api"}
	for i := 0; i < declared; i++ {
		name := fmt.Sprintf("config-%d", i)
		fake.sets["ConfigMap/"+name] = configMapSet(t, map[string]string{"K": "v"})
		c.EnvFrom = append(c.EnvFrom, Whole{Kind: "ConfigMap", Name: name})
	}
	containers := []Container{c}

	note := expandEnvFrom(context.Background(), containers, fake.lookup)

	if len(fake.asked) != want {
		t.Errorf("%d reads for %d imports; the cap is %d and it is the whole point",
			len(fake.asked), declared, want)
	}
	if len(containers[0].Imported) != want {
		t.Errorf("%d rows, want %d", len(containers[0].Imported), want)
	}
	if note == "" {
		t.Fatal("three imports were silently dropped. A capped list that does not admit it is " +
			"a lie the operator acts on")
	}
	if !strings.Contains(note, "3") {
		t.Errorf("the note does not say how many were left out: %q", note)
	}
	for _, from := range containers[0].EnvFrom[want:] {
		if from.Problem == "" {
			t.Errorf("the unread import %s says nothing about why it has no rows", from.Name)
		}
	}
}

// Three answers that a list of rows renders identically, told apart.
//
// An empty answer and a failed one are different answers. "You may not read
// this", "there is no such object"
// and "it holds nothing" all produce no rows; an operator handed the same blank
// for all three goes and debugs the wrong thing.
func TestAFailedReadIsNeverDrawnAsAnEmptyOne(t *testing.T) {
	forbidden := apierrors.NewForbidden(
		schema.GroupResource{Resource: "secrets"}, "api-secrets", fmt.Errorf("no"))
	missing := apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, "tuning")

	fake := &countingLookup{
		sets: map[string]*DataSet{"ConfigMap/empty-one": configMapSet(t, nil)},
		fail: map[string]error{
			"Secret/api-secrets": forbidden,
			"ConfigMap/tuning":   missing,
			// Its own NotFound, naming it: a NotFound about another object is
			// not an answer about this one (absentObject).
			"ConfigMap/maybe": apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, "maybe"),
		},
	}
	containers := []Container{{Name: "api", EnvFrom: []Whole{
		{Kind: "Secret", Name: "api-secrets"},
		{Kind: "ConfigMap", Name: "tuning"},
		{Kind: "ConfigMap", Name: "maybe", Optional: true},
		{Kind: "ConfigMap", Name: "empty-one"},
	}}}
	expandEnvFrom(context.Background(), containers, fake.lookup)

	imports := containers[0].EnvFrom

	if !strings.Contains(imports[0].Problem, "not allowed") {
		t.Errorf("a refused read reads as %q; it must say this console could not read it, not "+
			"that there is nothing in it", imports[0].Problem)
	}
	if !strings.Contains(imports[1].Problem, "there is no") ||
		strings.Contains(imports[1].Problem, "optional") {
		t.Errorf("a missing, required ConfigMap reads as %q; it says it is missing and "+
			"nothing about the reference being optional", imports[1].Problem)
	}
	if !strings.Contains(imports[2].Problem, "there is no") ||
		strings.Contains(imports[2].Problem, "optional") {
		t.Errorf("a missing but optional ConfigMap reads as %q; the missing object is said "+
			"and nothing about the reference being optional", imports[2].Problem)
	}
	// And the one that genuinely holds nothing has no Problem at all — the panel
	// distinguishes it by that, and says it was read and is empty.
	if imports[3].Problem != "" || imports[3].Keys != 0 {
		t.Errorf("a ConfigMap that was read and holds nothing came out as %+v", imports[3])
	}
}

// An optional reference to an object that does not exist is the pod working as
// written; a required one is the reason it does not start.
//
// A NotFound read as `unread` whatever the reference said would draw an
// `optional: true` import of a ConfigMap nobody created with the long sentence
// in the warning tone, and keep a retry control on a `configMapKeyRef` row into
// it.
//
// One missing object behind every reference on purpose: envResolver reads it
// once and caches the *error*, so the kind has to be decided per reference
// after the cache. A kind cached with the read would give the second reference
// the first one's answer. And a refusal stays `unread` even on an optional
// reference - "you may not look" is not "there is nothing".
func TestAnOptionalReferenceToAMissingObjectIsNotAFault(t *testing.T) {
	missing := apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, "absent")
	forbidden := apierrors.NewForbidden(
		schema.GroupResource{Resource: "configmaps"}, "locked", fmt.Errorf("no"))
	fake := &countingLookup{fail: map[string]error{
		"ConfigMap/absent": missing,
		"ConfigMap/locked": forbidden,
	}}

	containers := []Container{{
		Name: "api",
		EnvFrom: []Whole{
			{Kind: "ConfigMap", Name: "absent", Optional: true},
			{Kind: "ConfigMap", Name: "absent"},
			{Kind: "ConfigMap", Name: "locked", Optional: true},
		},
		Env: []EnvVar{
			{Name: "OPTIONAL", Source: &EnvSource{Kind: "ConfigMap", Name: "absent", Key: "k", Optional: true}},
			{Name: "REQUIRED", Source: &EnvSource{Kind: "ConfigMap", Name: "absent", Key: "k"}},
			{Name: "REFUSED", Source: &EnvSource{Kind: "ConfigMap", Name: "locked", Key: "k", Optional: true}},
		},
	}}
	expandEnvFrom(context.Background(), containers, fake.lookup)

	if reads := len(fake.asked); reads != 2 {
		t.Errorf("%d reads (%v), want 2: one per distinct object, whatever each reference says",
			reads, fake.asked)
	}

	imports := containers[0].EnvFrom
	for _, want := range []struct {
		got     Whole
		absence Absence
		phrase  string
		why     string
	}{
		{imports[0], AbsenceExpected, "there is no",
			"an optional import of an object that does not exist starts the pod anyway"},
		{imports[1], AbsenceExpected, "there is no",
			"a required import of an object that does not exist is drawn as the reference alone, not as a failed read"},
		{imports[2], AbsenceUnread, "not allowed",
			"a refused read is not an empty one, optional or not"},
	} {
		if want.got.Absence != want.absence {
			t.Errorf("import %s (optional=%v) has Absence=%q, want %q: %s",
				want.got.Name, want.got.Optional, want.got.Absence, want.absence, want.why)
		}
		if !strings.Contains(want.got.Problem, want.phrase) {
			t.Errorf("import %s (optional=%v) says %q, want something containing %q",
				want.got.Name, want.got.Optional, want.got.Problem, want.phrase)
		}
	}

	env := containers[0].Env
	for _, want := range []struct {
		row     EnvVar
		absence Absence
		phrase  string
	}{
		{env[0], AbsenceExpected, "there is no"},
		{env[1], AbsenceExpected, "there is no"},
		{env[2], AbsenceUnread, "not allowed"},
	} {
		if want.row.Absence != want.absence {
			t.Errorf("%s has Absence=%q, want %q. It decides whether the row is a warning "+
				"with a retry or the pod working as written", want.row.Name, want.row.Absence, want.absence)
		}
		if !strings.Contains(want.row.Problem, want.phrase) {
			t.Errorf("%s says %q, want something containing %q", want.row.Name, want.row.Problem, want.phrase)
		}
		if want.row.Held {
			t.Errorf("%s is marked as read: %+v", want.row.Name, want.row)
		}
	}
}

// Only "this object does not exist" is quiet on an optional reference. Every
// other way a read can fail stays `unread`, optional or not.
//
// The mutation this pins is the obvious simplification of lookupAbsence -
// `case ref.optional:` - which reads every failed optional read as the pod
// working as written: a person refused the read, a review that could not be
// made, an API server answering 500, a timeout, expired credentials. Each of
// those says nothing about whether the object exists, and the variables may
// well be set in the container. A muted "absent (optional)" there would be the
// empty-versus-failed collapse: an empty answer and a failed one are different answers.
//
// And a NotFound that names some *other* object - a proxy's 404, a wrapped error
// from another read - is a failed read, not an absence (absentObject).
func TestAnOptionalReferenceThatFailedForAnyOtherReasonStillWarns(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"a person's refusal", &access.Refused{
			Cluster: "prod", Answer: access.Answer{Decision: access.Denied, Subject: access.Person}}},
		{"a review that could not be made", &access.Refused{
			Cluster: "prod", Answer: access.Answer{Decision: access.Unreviewable, Reason: "timeout"}}},
		{"the console's own refusal", apierrors.NewForbidden(
			schema.GroupResource{Resource: "configmaps"}, "tuning", fmt.Errorf("no"))},
		{"an internal error", apierrors.NewInternalError(fmt.Errorf("etcd is unhappy"))},
		{"a timeout", context.DeadlineExceeded},
		{"expired credentials", apierrors.NewUnauthorized("token expired")},
		{"a NotFound about another object",
			apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, "someone-else")},
		{"a NotFound with no details", &apierrors.StatusError{ErrStatus: metav1.Status{
			Status: metav1.StatusFailure, Code: 404, Reason: metav1.StatusReasonNotFound}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &countingLookup{fail: map[string]error{"ConfigMap/tuning": tc.err}}
			containers := []Container{{
				Name:    "api",
				EnvFrom: []Whole{{Kind: "ConfigMap", Name: "tuning", Optional: true}},
				Env: []EnvVar{{Name: "LEVEL",
					Source: &EnvSource{Kind: "ConfigMap", Name: "tuning", Key: "level", Optional: true}}},
			}}
			expandEnvFrom(context.Background(), containers, fake.lookup)

			imported := containers[0].EnvFrom[0]
			if imported.Absence != AbsenceUnread || imported.Problem == "" {
				t.Errorf("an optional import whose read failed (%v) came out as absence %q, problem %q; "+
					"want unread with a sentence - a failed read says nothing about whether the object exists",
					tc.err, imported.Absence, imported.Problem)
			}
			row := containers[0].Env[0]
			if row.Absence != AbsenceUnread || row.Problem == "" {
				t.Errorf("an optional reference whose read failed (%v) came out as absence %q, problem %q; "+
					"want unread, which keeps the control - asking again may answer differently",
					tc.err, row.Absence, row.Problem)
			}
			if strings.Contains(row.Problem, "there is no") {
				t.Errorf("a failed read is described as a missing object: %q", row.Problem)
			}
		})
	}
}

// A `valueFrom.secretKeyRef` whose Secret is forbidden still shows its reveal
// control, although three of the panel's four absences withdraw it. It cannot
// lose it, and the reason is one
// line up in `resolveKeyRefs`: a Secret is never read on a drawer open, so no
// read of a Secret can put an absence on the row. The refusal is the door's to
// give, on the click, where it arrives with a sentence.
//
// What this pins is the mutation nobody would call a regression: a resolver that
// starts reading Secrets "for symmetry" puts `unread` or `unset` on the row and
// produces a secret-backed variable with nothing to press and no reason
// given.
func TestASecretKeyRefKeepsItsControlWhateverTheSecretWouldAnswer(t *testing.T) {
	forbidden := apierrors.NewForbidden(
		schema.GroupResource{Resource: "secrets"}, "db-credentials", fmt.Errorf("no"))
	fake := &countingLookup{fail: map[string]error{"Secret/db-credentials": forbidden}}

	containers := []Container{{Name: "api", Env: []EnvVar{{
		Name:   "DB_PASSWORD",
		Secret: true,
		Source: &EnvSource{Kind: "Secret", Name: "db-credentials", Key: "password"},
	}}}}
	expandEnvFrom(context.Background(), containers, fake.lookup)

	for _, asked := range fake.asked {
		if strings.HasPrefix(asked, "Secret/") {
			t.Errorf("the drawer read %s to draw an environment row. A GET of a Secret that "+
				"produces nothing on the page is a request that should not have been made", asked)
		}
	}

	row := containers[0].Env[0]
	if !row.Secret || row.Source == nil {
		t.Fatalf("the row came out as %+v; without the mark and the address the panel has "+
			"nothing to offer a control for", row)
	}
	if row.Absence != AbsenceNone || row.Problem != "" {
		t.Errorf("a Secret-backed row came out carrying absence %q and problem %q. Both are a "+
			"report of a read, and no read of this Secret happened or may happen here: the "+
			"row would say the value could not be read when what is true is that nobody "+
			"asked for it yet, and the control beside it would read as a retry",
			row.Absence, row.Problem)
	}
}

// --- the real read, against a fake cluster -------------------------------------

// readerFor builds a Reader whose dynamic client is a fake and whose mapper
// knows the three kinds these tests use.
//
// The kubeconfig names one context and points it at a port nothing listens on:
// nothing here opens a socket, and anything that tried would fail loudly rather
// than reach a real cluster.
func readerFor(t *testing.T, objects ...runtime.Object) (*Reader, *dynamicfake.FakeDynamicClient) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config")
	body := `apiVersion: v1
kind: Config
clusters:
- name: nowhere
  cluster:
    server: https://127.0.0.1:1
contexts:
- name: prod
  context: {cluster: nowhere, user: nobody}
users:
- name: nobody
  user: {token: none}
current-context: prod
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing a kubeconfig: %v", err)
	}
	clusters, err := cluster.NewManager(path)
	if err != nil {
		t.Fatalf("loading the kubeconfig: %v", err)
	}

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			{Version: "v1", Resource: "pods"}:       "PodList",
			{Version: "v1", Resource: "configmaps"}: "ConfigMapList",
			{Version: "v1", Resource: "secrets"}:    "SecretList",
		},
		objects...,
	)

	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Version: "v1"}})
	for _, kind := range []string{"Pod", "ConfigMap", "Secret"} {
		mapper.Add(schema.GroupVersionKind{Version: "v1", Kind: kind}, meta.RESTScopeNamespace)
	}

	reader := NewReader(clusters, accesstest.Allow())
	reader.newMapper = func(context.Context, *cluster.Client) (meta.RESTMapper, error) { return mapper, nil }
	reader.dynamicOf = func(*cluster.Client) (dynamic.Interface, error) { return dyn, nil }
	return reader, dyn
}

func configMapObject(namespace, name string, data map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": name, "namespace": namespace, "resourceVersion": "1"},
		"data":     data,
	}}
}

func secretObject(namespace, name string, data map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": name, "namespace": namespace, "resourceVersion": "1"},
		"data":     data,
	}}
}

// The lookup hands back a Secret's names and a ConfigMap's values, and that is
// the whole of the difference between the two kinds.
//
// There is no API for "the key names only", so listing them means reading the
// object; what matters is what survives the call. `dataOf` measures a Secret's
// values and keeps none of them, and it is the *only* thing that decides that —
// this is the test of that guard, reached against a real Secret so that removing
// the `if !secret` in `entryOf` fails here whatever any other guard does.
func TestTheEnvLookupCarriesAConfigMapsValuesAndNeverASecrets(t *testing.T) {
	reader, _ := readerFor(t,
		// "c3VwZXItc2VjcmV0" is base64 for "super-secret".
		secretObject("shop", "api-secrets", map[string]any{"TOKEN": "c3VwZXItc2VjcmV0"}),
		configMapObject("shop", "api-config", map[string]any{"tuning": "aggressive"}),
	)

	set, err := reader.envLookupFor("prod", "shop")(context.Background(), "Secret", "api-secrets")
	if err != nil {
		t.Fatalf("reading a Secret: %v", err)
	}
	if len(set.Entries) != 1 || set.Entries[0].Key != "TOKEN" {
		t.Fatalf("the entries came out as %+v, want one named TOKEN", set.Entries)
	}
	for _, entry := range set.Entries {
		if entry.Preview != "" || entry.Value != "" {
			t.Fatalf("the lookup carried a Secret's value off the cluster: %+v. Nothing "+
				"downstream can put back what this line drops", entry)
		}
	}
	// And the measurement is still there, because the panel says how big a value
	// it will not show is.
	if set.Entries[0].Size != len("super-secret") {
		t.Errorf("the Secret entry was not measured: %+v", set.Entries[0])
	}

	set, err = reader.envLookupFor("prod", "shop")(context.Background(), "ConfigMap", "api-config")
	if err != nil {
		t.Fatalf("reading a ConfigMap: %v", err)
	}
	if len(set.Entries) != 1 || set.Entries[0].Key != "tuning" {
		t.Fatalf("the entries came out as %+v, want one named tuning", set.Entries)
	}
	// The ConfigMap half. A ConfigMap is not a credential store,
	// and its value travels so the row can draw it without a round trip of its
	// own — the envFrom that produced this read already paid for the object.
	if set.Entries[0].Preview != "aggressive" {
		t.Errorf("a ConfigMap value did not come back with its entry: %+v", set.Entries[0])
	}
}

// The lookup may only ever be pointed at a kind that has a data block.
//
// It takes a kind name, and a kind name is the kind of thing that ends up in a
// URL. Reading a Pod through it would be a general object reader wearing the
// name of a key lister.
func TestTheEnvLookupRefusesAKindWithNoDataBlock(t *testing.T) {
	reader, _ := readerFor(t, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "api-0", "namespace": "shop", "resourceVersion": "1"},
		"spec":     map[string]any{},
	}})

	if _, err := reader.envLookupFor("prod", "shop")(context.Background(), "Pod", "api-0"); err == nil {
		t.Fatal("a Pod was read through the key-name lookup")
	}
}

// And it reaches the drawer: opening a pod expands its imports.
//
// Rendering the pass on its own proves it works and nothing about whether
// anything calls it — the same gap the container panel's own test closes. A
// mutation deleting the call from Reader.Get passes every test above.
func TestOpeningAPodExpandsItsImports(t *testing.T) {
	pod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "api-0", "namespace": "shop", "resourceVersion": "1"},
		"spec": map[string]any{
			"containers": []any{map[string]any{
				"name": "api", "image": "api:1",
				"envFrom": []any{map[string]any{
					"prefix":    "APP_",
					"secretRef": map[string]any{"name": "api-secrets"},
				}},
			}},
		},
	}}
	reader, _ := readerFor(t, pod, secretObject("shop", "api-secrets", map[string]any{
		"TOKEN": "c3VwZXItc2VjcmV0",
	}))

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "pods", Namespace: "shop", Name: "api-0",
	})
	if err != nil {
		t.Fatalf("reading the pod: %v", err)
	}
	if len(object.Containers) != 1 {
		t.Fatalf("%d containers", len(object.Containers))
	}
	imported := object.Containers[0].Imported
	if len(imported) != 1 || imported[0].Name != "APP_TOKEN" {
		t.Fatalf("the import was not expanded: %+v", imported)
	}
	// And the value is not in the object the drawer will render.
	if imported[0].Value != "" {
		t.Fatalf("opening a pod fetched a Secret value (%q) nobody asked for", imported[0].Value)
	}
}

// The whole path: an optional import of a Secret that is
// not in the cluster, and a required one, through the real lookup.
//
// absentObject trusts the NotFound's details to name the object. This is the
// check that the client really fills them from the request, so the hardening
// does not quietly turn every real absence back into `unread`.
func TestOpeningAPodWithAnOptionalImportOfAMissingSecret(t *testing.T) {
	pod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "api-0", "namespace": "shop", "resourceVersion": "1"},
		"spec": map[string]any{
			"containers": []any{map[string]any{
				"name": "api", "image": "api:1",
				"envFrom": []any{
					map[string]any{"secretRef": map[string]any{"name": "not-created", "optional": true}},
					map[string]any{"secretRef": map[string]any{"name": "also-missing"}},
				},
			}},
		},
	}}
	reader, _ := readerFor(t, pod)

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "pods", Namespace: "shop", Name: "api-0",
	})
	if err != nil {
		t.Fatalf("reading the pod: %v", err)
	}
	if len(object.Containers) != 1 || len(object.Containers[0].EnvFrom) != 2 {
		t.Fatalf("the imports did not come through: %+v", object.Containers)
	}
	optional, required := object.Containers[0].EnvFrom[0], object.Containers[0].EnvFrom[1]
	if optional.Absence != AbsenceExpected || !strings.Contains(optional.Problem, "there is no") {
		t.Errorf("an optional import of a missing Secret came out as absence %q, problem %q; "+
			"want expected - the pod starts as written", optional.Absence, optional.Problem)
	}
	if required.Absence != AbsenceExpected || !strings.Contains(required.Problem, "there is no") {
		t.Errorf("a required import of a missing Secret came out as absence %q, problem %q; "+
			"want expected - drawn as the reference alone, not as a failed read", required.Absence, required.Problem)
	}
}

// A Secret this console may not read is said to be unreadable, not empty.
//
// The whole path, with the API server's own answer: RBAC is the commonest reason
// an expanded import is bare, and it must not read as "this Secret has nothing
// in it".
func TestAForbiddenImportIsToldApartFromAnEmptyOne(t *testing.T) {
	pod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "api-0", "namespace": "shop", "resourceVersion": "1"},
		"spec": map[string]any{
			"containers": []any{map[string]any{
				"name": "api", "image": "api:1",
				"envFrom": []any{map[string]any{
					"secretRef": map[string]any{"name": "api-secrets"},
				}},
			}},
		},
	}}
	reader, dyn := readerFor(t, pod, secretObject("shop", "api-secrets", map[string]any{"TOKEN": "eA=="}))
	dyn.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "secrets"}, "api-secrets", fmt.Errorf("nope"))
	})

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "pods", Namespace: "shop", Name: "api-0",
	})
	if err != nil {
		t.Fatalf("reading the pod: %v", err)
	}
	problem := object.Containers[0].EnvFrom[0].Problem
	if !strings.Contains(problem, "not allowed") {
		t.Fatalf("a Secret this console may not read reports %q. An empty answer and a failed "+
			"one are different answers", problem)
	}
	if len(object.Containers[0].Imported) != 0 {
		t.Fatal("a refused read still produced rows")
	}
}

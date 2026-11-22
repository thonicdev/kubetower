package edit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/identity"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"
)

// Every test in this package runs against a fake clientset, and none of them
// may be made to run against anything else.
//
// The rule is absolute: a guard test that finds a hole proves it by exercising
// it, and against a live cluster that does the damage it was looking for. A
// test of a write path is a write. The fake is where it belongs.

var (
	configMapGVR  = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	secretGVR     = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	deploymentGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	serviceGVR    = schema.GroupVersionResource{Version: "v1", Resource: "services"}
	namespaceGVR  = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
)

// --- the harness --------------------------------------------------------------

// recorded is one request that reached the cluster. The tests assert on these
// as much as on return values: "it refused" and "it refused and sent nothing"
// are different claims, and only the second one is a guardrail.
type recorded struct {
	Verb      string
	Resource  string
	Namespace string
	Name      string
	Body      string
}

type harness struct {
	t       *testing.T
	dyn     *dynamicfake.FakeDynamicClient
	writer  *Writer
	sent    []recorded
	logged  []string
	cluster string
	// reviews answers the access review every write asks. It allows
	// everything unless a test says otherwise.
	reviews *accesstest.Reviews
}

// fakeResolver is what internal/web builds out of detail.Reader, reduced to the
// five kinds these tests use. `cluster` is the *resolved* context name, which is
// what the read-only check has to be made against.
type fakeResolver struct {
	dyn     dynamic.Interface
	cluster string
}

func (f fakeResolver) Locate(_ context.Context, _, kind string) (Location, error) {
	switch strings.ToLower(kind) {
	case "configmaps", "configmap":
		return Location{Cluster: f.cluster, Resource: configMapGVR, Kind: "ConfigMap", Namespaced: true, Dynamic: f.dyn}, nil
	case "secrets", "secret":
		return Location{Cluster: f.cluster, Resource: secretGVR, Kind: "Secret", Namespaced: true, Dynamic: f.dyn}, nil
	case "deployments", "deployment":
		return Location{Cluster: f.cluster, Resource: deploymentGVR, Kind: "Deployment", Namespaced: true, Dynamic: f.dyn}, nil
	case "services", "service":
		return Location{Cluster: f.cluster, Resource: serviceGVR, Kind: "Service", Namespaced: true, Dynamic: f.dyn}, nil
	case "namespaces", "namespace":
		return Location{Cluster: f.cluster, Resource: namespaceGVR, Kind: "Namespace", Dynamic: f.dyn}, nil
	}
	return Location{}, fmt.Errorf("%q is not a kind this cluster serves", kind)
}

func newHarness(t *testing.T, readOnly func(string) bool, objects ...*unstructured.Unstructured) *harness {
	t.Helper()

	listKinds := map[schema.GroupVersionResource]string{
		configMapGVR:  "ConfigMapList",
		secretGVR:     "SecretList",
		deploymentGVR: "DeploymentList",
		serviceGVR:    "ServiceList",
		namespaceGVR:  "NamespaceList",
	}
	seeds := make([]runtime.Object, 0, len(objects))
	for _, object := range objects {
		seeds = append(seeds, object)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, seeds...)

	h := &harness{t: t, dyn: dyn, cluster: "prod"}
	dyn.PrependReactor("patch", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patch := action.(k8stesting.PatchAction)
		h.sent = append(h.sent, recorded{
			Verb:      action.GetVerb(),
			Resource:  action.GetResource().Resource,
			Namespace: action.GetNamespace(),
			Name:      patch.GetName(),
			Body:      string(patch.GetPatch()),
		})
		return false, nil, nil
	})

	h.reviews = &accesstest.Reviews{}
	h.writer = New(Options{
		Resolve:  fakeResolver{dyn: dyn, cluster: h.cluster},
		Access:   h.reviews.Gate(),
		ReadOnly: readOnly,
		Logf:     func(format string, args ...any) { h.logged = append(h.logged, fmt.Sprintf(format, args...)) },
	})
	return h
}

// noWrites is the assertion that matters on every refusal: nothing left the
// process. A refusal that returns an error *after* sending the request is not a
// guardrail, and it would pass a test that only looked at the error.
func (h *harness) noWrites() {
	h.t.Helper()
	if len(h.sent) != 0 {
		h.t.Fatalf("the cluster was written to despite the refusal: %+v", h.sent)
	}
}

func (h *harness) object(gvr schema.GroupVersionResource, namespace, name string) *unstructured.Unstructured {
	h.t.Helper()
	got, err := h.dyn.Resource(gvr).Namespace(namespace).Get(h.t.Context(), name, metav1.GetOptions{})
	if err != nil {
		h.t.Fatalf("reading back %s/%s: %v", namespace, name, err)
	}
	return got
}

func object(t *testing.T, doc string) *unstructured.Unstructured {
	t.Helper()
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(doc), &raw); err != nil {
		t.Fatalf("parsing the fixture: %v", err)
	}
	return &unstructured.Unstructured{Object: raw}
}

func refusalOf(t *testing.T, err error) *Refusal {
	t.Helper()
	if err == nil {
		t.Fatal("the write was allowed; it should have been refused")
	}
	refusal, ok := AsRefusal(err)
	if !ok {
		t.Fatalf("the error is not a refusal: %v", err)
	}
	return refusal
}

func decodeBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("the patch is not JSON: %v (%s)", err, body)
	}
	return out
}

// --- fixtures -----------------------------------------------------------------

const coreDNS = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: coredns
  namespace: kube-system
  resourceVersion: "204"
  uid: 8ac5-1
  labels:
    k8s-app: kube-dns
data:
  Corefile: |
    .:53 {
        errors
    }
  extra: "second"
binaryData:
  bundle.gz: H4sIAAAAAAAA/w==
`

const registryCreds = `
apiVersion: v1
kind: Secret
metadata:
  name: registry
  namespace: shop
  resourceVersion: "77"
type: Opaque
data:
  password: c3VwZXItc2VjcmV0
  keystore.p12: /wD/AA==
`

// --- stage one: one value ------------------------------------------------------

// The test that matters most. What leaves this process is the whole blast radius
// of the feature, and it is pinned to the literal bytes rather than described.
func TestSetKeyPatchCarriesOneKeyAndNothingElse(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))

	_, err := h.writer.SetKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		Key:             "Corefile",
		Value:           ".:53 {\n    log\n}\n",
		ResourceVersion: "204",
	})
	if err != nil {
		t.Fatalf("the write was refused: %v", err)
	}
	if len(h.sent) == 0 {
		t.Fatal("nothing was sent")
	}

	patch := decodeBody(t, h.sent[0].Body)
	if len(patch) != 2 {
		t.Fatalf("the patch has %d top-level fields, want exactly data and metadata: %s", len(patch), h.sent[0].Body)
	}

	data, ok := patch["data"].(map[string]any)
	if !ok {
		t.Fatalf("no data block in %s", h.sent[0].Body)
	}
	if len(data) != 1 {
		t.Fatalf("the patch carries %d keys, want 1: %s", len(data), h.sent[0].Body)
	}
	if data["Corefile"] != ".:53 {\n    log\n}\n" {
		t.Fatalf("the value did not survive: %q", data["Corefile"])
	}

	// The other key of the same object, the binary block, the labels, the uid:
	// none of them may be in the request. Each of these is a real possibility of
	// the shape "read the object, change one field, send it back".
	if _, present := data["extra"]; present {
		t.Fatalf("a key the operator did not edit is in the patch: %s", h.sent[0].Body)
	}
	for _, forbidden := range []string{"binaryData", "status", "kind", "apiVersion"} {
		if _, present := patch[forbidden]; present {
			t.Fatalf("%s is in the patch: %s", forbidden, h.sent[0].Body)
		}
	}

	metadata, ok := patch["metadata"].(map[string]any)
	if !ok || len(metadata) != 1 || metadata["resourceVersion"] != "204" {
		t.Fatalf("metadata must carry the precondition and nothing else, got %v", patch["metadata"])
	}
}

// The precondition has to be *in the request*, not only checked before it. The
// check closes the window a person can see; this closes the milliseconds they
// cannot.
func TestSetKeySendsTheResourceVersionAsAPrecondition(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))

	if _, err := h.writer.SetKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		Key:             "extra",
		Value:           "third",
		ResourceVersion: "204",
	}); err != nil {
		t.Fatalf("the write was refused: %v", err)
	}

	for _, sent := range h.sent {
		if !strings.Contains(sent.Body, `"resourceVersion":"204"`) {
			t.Fatalf("a request went out with no precondition: %s", sent.Body)
		}
	}
}

// Validate, then commit. Two identical requests, of which the first is the dry
// run — the fake cannot see the DryRun option, so the count and the equality are
// what pin the order.
func TestSetKeyValidatesBeforeItCommits(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))

	if _, err := h.writer.SetKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		Key:             "extra",
		Value:           "third",
		ResourceVersion: "204",
	}); err != nil {
		t.Fatalf("the write was refused: %v", err)
	}
	if len(h.sent) != 2 {
		t.Fatalf("want a dry run and a commit, got %d request(s)", len(h.sent))
	}
	if h.sent[0].Body != h.sent[1].Body {
		t.Fatalf("the committed request differs from the one that was validated:\n%s\n%s", h.sent[0].Body, h.sent[1].Body)
	}
}

// The guardrail. Refused, and — the part that makes it a guardrail — nothing
// reaches the cluster.
func TestSetKeyRefusesAReadOnlyCluster(t *testing.T) {
	marked := func(name string) bool { return name == "prod" }
	h := newHarness(t, marked, object(t, coreDNS))

	// The name off the URL is the DNS-friendly one, which would not match an
	// entry in KT_READONLY_CLUSTERS. The check has to be made on the resolved
	// context name, and this is what says so.
	_, err := h.writer.SetKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod--dns-friendly", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		Key:             "extra",
		Value:           "third",
		ResourceVersion: "204",
	})

	refusal := refusalOf(t, err)
	if refusal.Kind != RefusedReadOnly {
		t.Fatalf("want a read-only refusal, got %v: %s", refusal.Kind, refusal.Reason)
	}
	if !strings.Contains(refusal.Reason, "KT_READONLY_CLUSTERS") || !strings.Contains(refusal.Reason, "prod") {
		t.Fatalf("the refusal must name the setting and the cluster: %s", refusal.Reason)
	}
	h.noWrites()
}

func TestSetKeyRefusesAnImmutableObject(t *testing.T) {
	frozen := object(t, coreDNS)
	frozen.Object["immutable"] = true
	h := newHarness(t, nil, frozen)

	refusal := refusalOf(t, mustFail(h.writer.SetKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		Key:             "extra",
		Value:           "third",
		ResourceVersion: "204",
	})))
	if refusal.Kind != RefusedImmutable {
		t.Fatalf("want an immutable refusal, got %v: %s", refusal.Kind, refusal.Reason)
	}
	if !strings.Contains(refusal.Reason, "immutable") {
		t.Fatalf("the refusal must name why: %s", refusal.Reason)
	}
	h.noWrites()
}

// A save built on a panel that has gone stale must lose, and must be told so in
// a sentence the operator can act on.
func TestSetKeyRefusesAStaleResourceVersion(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))

	refusal := refusalOf(t, mustFail(h.writer.SetKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		Key:             "extra",
		Value:           "third",
		ResourceVersion: "199", // the object is at 204
	})))
	if refusal.Kind != RefusedConflict {
		t.Fatalf("want a conflict, got %v: %s", refusal.Kind, refusal.Reason)
	}
	if !strings.Contains(refusal.Reason, "reload") {
		t.Fatalf("a conflict has to say what to do about it: %s", refusal.Reason)
	}
	h.noWrites()
}

// A save carrying **no** resourceVersion at all is the same defect as a stale
// one, and it is the worse of the two: the check that would have caught the race
// is simply skipped, and the patch then carries the resourceVersion this call
// just read — so the API server's own precondition is satisfied by construction
// and cannot refuse either. Both guards are gone at once, and the operator
// overwrites whatever landed while the panel was open, silently.
//
// It is not hypothetical from the form's point of view: the panel's own template
// promises this in words — "the form carries the resourceVersion the panel was
// drawn from, so a save that lost a race fails with a sentence instead of
// overwriting whatever arrived first" — and a POST assembled by hand, or a
// template that loses the hidden input, meets this and nothing else.
//
// The manifest path refuses it, at manifest.go. This is that same
// sentence on the path beside it.
func TestSetKeyRefusesASaveWithNoResourceVersionAtAll(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))

	refusal := refusalOf(t, mustFail(h.writer.SetKey(t.Context(), KeyChange{
		Target: Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		Key:    "extra",
		Value:  "third",
		// No ResourceVersion.
	})))
	if refusal.Kind != RefusedInput {
		t.Fatalf("want an input refusal, got %v: %s", refusal.Kind, refusal.Reason)
	}
	if !strings.Contains(strings.ToLower(refusal.Reason), "resourceversion") {
		t.Fatalf("the refusal does not name what is missing: %s", refusal.Reason)
	}
	// The one that matters: nothing was sent, not even the dry run.
	h.noWrites()
}

// The manifest path refuses it too. Pinned here beside the key path so the two
// cannot drift apart.
func TestSaveManifestRefusesASaveWithNoResourceVersionAtAll(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))

	_, err := h.writer.PreviewManifest(t.Context(), ManifestChange{
		Target: Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		YAML:   "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: coredns\n  namespace: kube-system\n",
	})
	refusal := refusalOf(t, err)
	if refusal.Kind != RefusedInput {
		t.Fatalf("want an input refusal, got %v: %s", refusal.Kind, refusal.Reason)
	}
	h.noWrites()
}

// The other half of the same protection: the API's own 409, which is what
// happens when the object moves between the read above and the patch below.
func TestSetKeyTranslatesTheAPIConflict(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))
	h.dyn.PrependReactor("patch", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(
			schema.GroupResource{Resource: "configmaps"}, "coredns", fmt.Errorf("the object has been modified"))
	})

	refusal := refusalOf(t, mustFail(h.writer.SetKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		Key:             "extra",
		Value:           "third",
		ResourceVersion: "204",
	})))
	if refusal.Kind != RefusedConflict {
		t.Fatalf("want a conflict, got %v: %s", refusal.Kind, refusal.Reason)
	}
	if strings.Contains(refusal.Reason, "409") {
		t.Fatalf("the operator is not owed a status code: %s", refusal.Reason)
	}
}

// Adding and removing keys is out of scope, and "out of scope" has to mean the
// request is refused rather than that the UI does not offer it.
func TestSetKeyRefusesAKeyThatIsNotThere(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))

	refusal := refusalOf(t, mustFail(h.writer.SetKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		Key:             "newkey",
		Value:           "whatever",
		ResourceVersion: "204",
	})))
	if refusal.Kind != RefusedInput {
		t.Fatalf("want an input refusal, got %v: %s", refusal.Kind, refusal.Reason)
	}
	h.noWrites()
}

func TestSetKeyRefusesBinaryData(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))

	refusal := refusalOf(t, mustFail(h.writer.SetKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		Key:             "bundle.gz",
		Value:           "text",
		ResourceVersion: "204",
	})))
	if !strings.Contains(refusal.Reason, "binary") {
		t.Fatalf("the refusal must say the value is binary: %s", refusal.Reason)
	}
	h.noWrites()
}

func TestSetKeyRefusesAKindWithNoDataBlock(t *testing.T) {
	h := newHarness(t, nil, object(t, deploymentDoc))

	refusal := refusalOf(t, mustFail(h.writer.SetKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "deployments", Namespace: "shop", Name: "api"},
		Key:             "anything",
		Value:           "x",
		ResourceVersion: "310",
	})))
	if refusal.Kind != RefusedInput {
		t.Fatalf("want an input refusal, got %v: %s", refusal.Kind, refusal.Reason)
	}
	h.noWrites()
}

// --- secrets ------------------------------------------------------------------

// The operator types plaintext and the cluster stores base64, in both
// directions. Getting this wrong stores the base64 of the base64, which reads
// back as gibberish and is only noticed by whatever the credential was for.
func TestSecretValueIsBase64EncodedOnTheWayOut(t *testing.T) {
	h := newHarness(t, nil, object(t, registryCreds))

	typed := "hunter2\nwith a newline"
	if _, err := h.writer.SetKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "secrets", Namespace: "shop", Name: "registry"},
		Key:             "password",
		Value:           typed,
		ResourceVersion: "77",
	}); err != nil {
		t.Fatalf("the write was refused: %v", err)
	}

	patch := decodeBody(t, h.sent[0].Body)
	data := patch["data"].(map[string]any)
	stored, _ := data["password"].(string)
	if stored == typed {
		t.Fatalf("the value was sent in the clear: %q", stored)
	}
	decoded, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		t.Fatalf("what was sent is not base64: %q", stored)
	}
	if string(decoded) != typed {
		t.Fatalf("the round trip lost the value: %q", decoded)
	}

	// And the same thing read back off the cluster, which is the assertion that
	// would catch a double encoding.
	live := h.object(secretGVR, "shop", "registry")
	back, _, _ := unstructured.NestedString(live.Object, "data", "password")
	again, err := base64.StdEncoding.DecodeString(back)
	if err != nil || string(again) != typed {
		t.Fatalf("the stored value does not decode to what was typed: %q (%v)", back, err)
	}
}

func TestSecretBinaryValueIsRefused(t *testing.T) {
	h := newHarness(t, nil, object(t, registryCreds))

	refusal := refusalOf(t, mustFail(h.writer.SetKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "secrets", Namespace: "shop", Name: "registry"},
		Key:             "keystore.p12",
		Value:           "text",
		ResourceVersion: "77",
	})))
	if !strings.Contains(refusal.Reason, "not text") {
		t.Fatalf("the refusal must say the value is not text: %s", refusal.Reason)
	}
	h.noWrites()
}

// --- the log line --------------------------------------------------------------

// The only thing in this process that changes a cluster has to be findable
// afterwards, and findable means one grep.
func TestASuccessfulWriteIsLogged(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))

	if _, err := h.writer.SetKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		Key:             "extra",
		Value:           "third",
		ResourceVersion: "204",
	}); err != nil {
		t.Fatalf("the write was refused: %v", err)
	}
	if len(h.logged) != 1 {
		t.Fatalf("want one log line, got %d: %v", len(h.logged), h.logged)
	}
	line := h.logged[0]
	for _, want := range []string{"write:", `cluster="prod"`, "kind=configmaps", "namespace=kube-system", "name=coredns", "key=extra"} {
		if !strings.Contains(line, want) {
			t.Fatalf("the log line does not carry %q: %s", want, line)
		}
	}
}

func TestARefusedWriteIsNotLoggedAsAWrite(t *testing.T) {
	h := newHarness(t, func(string) bool { return true }, object(t, coreDNS))

	_, _ = h.writer.SetKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		Key:             "extra",
		Value:           "third",
		ResourceVersion: "204",
	})
	for _, line := range h.logged {
		if strings.HasPrefix(line, "write:") {
			t.Fatalf("a refused write was logged as a write: %s", line)
		}
	}
}

// mustFail turns the two-value return into the error, so the assertions above
// read as one line.
func mustFail(_ Applied, err error) error { return err }

// --- the confirmation, which sends nothing --------------------------------------

// Every delete or change is confirmed in a centered dialog that asks whether
// you are sure, with a recap of the change where one is possible. PreviewKey is
// the recap. The property that makes it one
// rather than a second save is here: it reaches the cluster exactly once, as a
// dry run, and the object does not move.
func TestPreviewKeyValidatesAndCommitsNothing(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))

	preview, err := h.writer.PreviewKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		Key:             "Corefile",
		Value:           ".:53 {\n    log\n}\n",
		ResourceVersion: "204",
	})
	if err != nil {
		t.Fatalf("the preview was refused: %v", err)
	}

	// One request, and it is the dry run. Two would mean the preview committed.
	if len(h.sent) != 1 {
		t.Fatalf("want one dry run, got %d request(s): %+v", len(h.sent), h.sent)
	}

	// The count is the whole assertion, and it has to be: the fake cannot see the
	// DryRun option and applies every patch it is handed, so reading the object
	// back would show the new value whether the request was a dry run or not.
	// `TestSetKeyValidatesBeforeItCommits` pins the commit path the same way, for
	// the same reason. Against a real API server the dry run stores nothing.

	if preview.ResourceVersion != "204" {
		t.Fatalf("the precondition the apply must carry is wrong: %q", preview.ResourceVersion)
	}
	if preview.Value != ".:53 {\n    log\n}\n" {
		t.Fatalf("the typed value is not carried back into the apply: %q", preview.Value)
	}
	// The fields, named rather than counted: "one field" is not an answer to
	// "what will this send".
	if len(preview.Fields) != 2 ||
		preview.Fields[0] != "data.Corefile" || preview.Fields[1] != "metadata.resourceVersion" {
		t.Fatalf("the recap does not name what is sent: %v", preview.Fields)
	}
	if !strings.Contains(preview.Validated, "dry run") {
		t.Fatalf("the recap does not say the cluster validated it: %q", preview.Validated)
	}
}

// A ConfigMap's recap is the lines. That is what the operator is checking, and a
// confirmation that asks the question without giving the answer teaches people
// to click through it.
func TestPreviewKeyShowsTheLinesOnAConfigMap(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))

	preview, err := h.writer.PreviewKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		Key:             "Corefile",
		Value:           ".:53 {\n    log\n}\n",
		ResourceVersion: "204",
	})
	if err != nil {
		t.Fatalf("the preview was refused: %v", err)
	}
	if len(preview.Diff) == 0 {
		t.Fatal("a ConfigMap's recap has no lines in it")
	}
	if preview.Added != 1 || preview.Removed != 1 {
		t.Fatalf("the change is one line for one, got +%d -%d", preview.Added, preview.Removed)
	}

	var text []string
	for _, line := range preview.Diff {
		text = append(text, string(line.Mark)+line.Text)
	}
	joined := strings.Join(text, "\n")
	if !strings.Contains(joined, "-    errors") || !strings.Contains(joined, "+    log") {
		t.Fatalf("the recap does not show the lines that move:\n%s", joined)
	}
	if preview.Unchanged {
		t.Fatal("a changed value was reported as unchanged")
	}
}

// A Secret's recap is deliberately **not** the lines.
//
// The plaintext is already on screen in the editor the dialog opens over, so
// repeating it adds nothing the operator does not have — and a centered dialog is
// the most screenshot-prone surface in this console. One copy of a credential on
// screen is the price of editing it; two is a decision nobody made.
func TestPreviewKeyOnASecretCarriesNoPlaintextRecap(t *testing.T) {
	h := newHarness(t, nil, object(t, registryCreds))

	preview, err := h.writer.PreviewKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "secrets", Namespace: "shop", Name: "registry"},
		Key:             "password",
		Value:           "hunter2",
		ResourceVersion: "77",
	})
	if err != nil {
		t.Fatalf("the preview was refused: %v", err)
	}
	if !preview.Secret {
		t.Fatal("a Secret's preview does not know it is one")
	}
	if len(preview.Diff) != 0 {
		t.Fatalf("a Secret's recap carries the plaintext lines: %+v", preview.Diff)
	}
	// The one thing that is on screen for a Secret: the sizes. `super-secret` is
	// twelve characters and `hunter2` is seven.
	if preview.BeforeBytes != 12 || preview.AfterBytes != 7 {
		t.Fatalf("the sizes are wrong: %d → %d", preview.BeforeBytes, preview.AfterBytes)
	}
	// What the API will hold, which is a third larger and is what the operator
	// sees if they go and look with kubectl.
	if preview.StoredBytes != len(base64.StdEncoding.EncodeToString([]byte("hunter2"))) {
		t.Fatalf("the stored size is not the base64's: %d", preview.StoredBytes)
	}
	if preview.BeforeLines != 1 || preview.AfterLines != 1 {
		t.Fatalf("the line counts are wrong: %d → %d", preview.BeforeLines, preview.AfterLines)
	}
}

// **A Secret's preview does not say whether a guess was right.** `Unchanged`
// compares the typed value with the stored one, so on a Secret, typing a guess
// into the editor and asking for the preview would answer "this value is
// byte-for-byte what is already there" or not - reading the Secret one guess at
// a time, without an audited reveal. On a Secret the comparison is not
// reported; a ConfigMap, whose data is not masked, keeps it.
func TestPreviewKeyOnASecretDoesNotSayWhetherTheValueIsTheStoredOne(t *testing.T) {
	h := newHarness(t, nil, object(t, registryCreds))

	preview, err := h.writer.PreviewKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "secrets", Namespace: "shop", Name: "registry"},
		Key:             "password",
		Value:           "super-secret", // what registryCreds stores
		ResourceVersion: "77",
	})
	if err != nil {
		t.Fatalf("the preview was refused: %v", err)
	}
	if preview.Unchanged {
		t.Error("a Secret's preview said the guessed value is the stored one")
	}
}

func TestPreviewKeyOnAConfigMapStillSaysAValueIsUnchanged(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))
	current := object(t, coreDNS)
	data, _, _ := unstructured.NestedStringMap(current.Object, "data")

	preview, err := h.writer.PreviewKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		Key:             "Corefile",
		Value:           data["Corefile"],
		ResourceVersion: "204",
	})
	if err != nil {
		t.Fatalf("the preview was refused: %v", err)
	}
	if !preview.Unchanged {
		t.Error("a ConfigMap value saved as it already is was not reported unchanged")
	}
}

// The guardrail applies to the preview exactly as it applies to the save. A
// dialog that can be drawn on a cluster the write will be refused on is a dialog
// that teaches the operator the marking is noise — and, worse, the preview is a
// request, so a preview that reached a read-only cluster would already have been
// a hole.
func TestPreviewKeyIsRefusedOnAReadOnlyCluster(t *testing.T) {
	h := newHarness(t, func(name string) bool { return name == "prod" }, object(t, coreDNS))

	refusal := refusalOf(t, mustFailKeyPreview(h.writer.PreviewKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		Key:             "Corefile",
		Value:           "anything",
		ResourceVersion: "204",
	})))
	if refusal.Kind != RefusedReadOnly {
		t.Fatalf("the wrong refusal: %v", refusal.Kind)
	}
	if !strings.Contains(refusal.Reason, "KT_READONLY_CLUSTERS") {
		t.Fatalf("the refusal does not name the setting: %s", refusal.Reason)
	}
	h.noWrites()
}

// A stale panel must lose at the preview too. Otherwise the operator confirms a
// diff computed against a version of the object that is already gone, and only
// finds out after pressing Apply.
func TestPreviewKeyRefusesAStaleResourceVersion(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))

	refusal := refusalOf(t, mustFailKeyPreview(h.writer.PreviewKey(t.Context(), KeyChange{
		Target:          Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		Key:             "Corefile",
		Value:           "anything",
		ResourceVersion: "199",
	})))
	if refusal.Kind != RefusedConflict {
		t.Fatalf("the wrong refusal: %v", refusal.Kind)
	}
	h.noWrites()
}

func mustFailKeyPreview(_ KeyPreview, err error) error { return err }

// The line names who asked, and never the value
// written. A person and the shared account read differently, and a write no
// session reached says it has nobody rather than leaving the fields out.
func TestAWriteLineNamesWhoAskedAndNeverTheValue(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"a person the provider vouched for",
			identity.With(t.Context(), identity.Who{Name: "alice@example.com", Method: identity.SSO}),
			`who="alice@example.com" via=sso`},
		{"the shared account",
			identity.With(t.Context(), identity.Who{Name: "admin", Method: identity.Password}),
			`who="admin" via=password`},
		{"no session reached it", t.Context(), `who=- via=unattributed`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil, object(t, coreDNS))
			if _, err := h.writer.SetKey(tc.ctx, KeyChange{
				Target:          Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
				Key:             "extra",
				Value:           "s3cr3t-value-never-logged",
				ResourceVersion: "204",
			}); err != nil {
				t.Fatalf("the write was refused: %v", err)
			}
			if len(h.logged) != 1 {
				t.Fatalf("want one log line, got %d: %v", len(h.logged), h.logged)
			}
			if !strings.HasSuffix(h.logged[0], " "+tc.want) {
				t.Errorf("the line does not end with %q: %s", tc.want, h.logged[0])
			}
			if strings.Contains(h.logged[0], "s3cr3t") {
				t.Errorf("the value reached the log: %s", h.logged[0])
			}
		})
	}
}

// A name holding a format verb stays a name: it is an argument to the line and
// never part of its format.
func TestANameWithAPercentCannotRewriteTheLine(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))
	ctx := identity.With(t.Context(), identity.Who{Name: "%s%d%v", Method: identity.SSO})
	if _, err := h.writer.SetKey(ctx, KeyChange{
		Target:          Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		Key:             "extra",
		Value:           "third",
		ResourceVersion: "204",
	}); err != nil {
		t.Fatalf("the write was refused: %v", err)
	}
	if line := h.logged[0]; !strings.HasSuffix(line, `who="%s%d%v" via=sso`) || strings.Contains(line, "%!") {
		t.Errorf("the name was read as a format: %s", line)
	}
}

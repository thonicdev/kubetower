package edit

import (
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"

	"github.com/thonicdev/kubetower/internal/detail"
	"github.com/thonicdev/kubetower/internal/identity"
)

// A Deployment carrying the two things that make the manifest editor dangerous:
// a `managedFields` block, which the drawer takes out of the document before
// showing it, and a `status`, which controllers own. An editor that sent the
// document it displayed would delete the first and fight the second.
const deploymentDoc = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: shop
  resourceVersion: "310"
  generation: 4
  uid: 91ab-77
  labels:
    app: api
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: '{"apiVersion":"apps/v1"}'
    owner: platform
    team: payments
  managedFields:
    - manager: kubectl-client-side-apply
      operation: Update
      apiVersion: apps/v1
      fieldsType: FieldsV1
      fieldsV1:
        f:spec:
          f:replicas: {}
spec:
  replicas: 3
  selector:
    matchLabels:
      app: api
  template:
    metadata:
      labels:
        app: api
    spec:
      containers:
        - name: api
          image: registry.example/api:1.4.0
          ports:
            - containerPort: 8080
status:
  replicas: 3
  readyReplicas: 3
  observedGeneration: 4
`

// shown is the document the drawer puts on screen: the object with the two
// things detail.Trimmed removes taken out. Every manifest test edits *this*,
// because that is what the operator edits.
func shown(t *testing.T, raw *unstructured.Unstructured) string {
	t.Helper()
	out, err := yaml.Marshal(detail.Trimmed(raw).Object)
	if err != nil {
		t.Fatalf("rendering the fixture: %v", err)
	}
	return string(out)
}

// edited replaces one substring of the shown document, which is what an operator
// does.
func edited(t *testing.T, doc, from, to string) string {
	t.Helper()
	if !strings.Contains(doc, from) {
		t.Fatalf("the fixture does not contain %q; the test is testing nothing", from)
	}
	return strings.Replace(doc, from, to, 1)
}

func deploymentTarget() Target {
	return Target{Cluster: "prod", Kind: "deployments", Namespace: "shop", Name: "api"}
}

// --- the failure that damages an object rather than merely failing ------------

// The whole point of the design. The document on screen has no `managedFields`
// and no last-applied annotation; sending it back as an object would delete
// both. Diffing it against the same trimmed form means they are identical on
// both sides, so they cannot appear in the patch at all — and `status`, which
// the operator did not touch, cannot either.
func TestManifestPatchOmitsWhatTheDrawerHidAndWhatWasNotTouched(t *testing.T) {
	raw := object(t, deploymentDoc)
	h := newHarness(t, nil, raw)

	change := ManifestChange{
		Target:          deploymentTarget(),
		YAML:            edited(t, shown(t, raw), "replicas: 3", "replicas: 5"),
		ResourceVersion: "310",
	}
	if _, err := h.writer.ApplyManifest(t.Context(), change); err != nil {
		t.Fatalf("the write was refused: %v", err)
	}
	if len(h.sent) == 0 {
		t.Fatal("nothing was sent")
	}

	body := h.sent[0].Body
	patch := decodeBody(t, body)

	for _, forbidden := range []string{"managedFields", "last-applied-configuration", "\"status\""} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("%s reached the cluster: %s", forbidden, body)
		}
	}

	spec, ok := patch["spec"].(map[string]any)
	if !ok || len(spec) != 1 {
		t.Fatalf("the patch should carry spec.replicas alone: %s", body)
	}
	if spec["replicas"] != float64(5) {
		t.Fatalf("want replicas 5, got %v: %s", spec["replicas"], body)
	}
	// metadata is in the patch for exactly one reason: the precondition.
	metadata, ok := patch["metadata"].(map[string]any)
	if !ok || len(metadata) != 1 || metadata["resourceVersion"] != "310" {
		t.Fatalf("metadata must carry the precondition and nothing else: %s", body)
	}
	if len(patch) != 2 {
		t.Fatalf("the patch has %d top-level fields, want spec and metadata: %s", len(patch), body)
	}
}

// The same object saved with no edit at all. It is the case that would send the
// whole document if the patch were computed from anything but a diff, and it is
// the case a nervous operator produces by opening the editor and pressing save.
func TestManifestSavedUnchangedSendsNothing(t *testing.T) {
	raw := object(t, deploymentDoc)
	h := newHarness(t, nil, raw)

	refusal := refusalOf(t, mustFailApply(h.writer.ApplyManifest(t.Context(), ManifestChange{
		Target:          deploymentTarget(),
		YAML:            shown(t, raw),
		ResourceVersion: "310",
	})))
	if refusal.Kind != RefusedNoChange {
		t.Fatalf("want a no-change refusal, got %v: %s", refusal.Kind, refusal.Reason)
	}
	h.noWrites()
}

// A key the operator deleted has to be sent as an explicit null, or the deletion
// they watched happen on screen never reaches the cluster.
func TestManifestRemovalIsSentAsANull(t *testing.T) {
	raw := object(t, deploymentDoc)
	h := newHarness(t, nil, raw)

	doc := shown(t, raw)
	doc = edited(t, doc, "    team: payments\n", "")

	if _, err := h.writer.ApplyManifest(t.Context(), ManifestChange{
		Target: deploymentTarget(), YAML: doc, ResourceVersion: "310",
	}); err != nil {
		t.Fatalf("the write was refused: %v", err)
	}
	if !strings.Contains(h.sent[0].Body, `"team":null`) {
		t.Fatalf("the removal was not expressed: %s", h.sent[0].Body)
	}

	live := h.object(deploymentGVR, "shop", "api")
	if _, present, _ := unstructured.NestedString(live.Object, "metadata", "annotations", "team"); present {
		t.Fatal("the annotation is still there; the null did not take effect")
	}
}

// --- the guardrail -------------------------------------------------------------

func TestManifestRefusesAReadOnlyCluster(t *testing.T) {
	raw := object(t, deploymentDoc)
	h := newHarness(t, func(name string) bool { return name == "prod" }, raw)

	change := ManifestChange{
		Target:          deploymentTarget(),
		YAML:            edited(t, shown(t, raw), "replicas: 3", "replicas: 5"),
		ResourceVersion: "310",
	}

	// Both halves of the flow, because the confirmation screen is a place a
	// refusal could be skipped: a preview that succeeded and an apply that
	// refused would be a UI that offers a button it will not honor, and a
	// preview that refused and an apply that did not would be far worse.
	refusal := refusalOf(t, mustFailPreview(h.writer.PreviewManifest(t.Context(), change)))
	if refusal.Kind != RefusedReadOnly {
		t.Fatalf("preview: want a read-only refusal, got %v", refusal.Kind)
	}
	refusal = refusalOf(t, mustFailApply(h.writer.ApplyManifest(t.Context(), change)))
	if refusal.Kind != RefusedReadOnly {
		t.Fatalf("apply: want a read-only refusal, got %v", refusal.Kind)
	}
	h.noWrites()
}

func TestManifestRefusesAStaleResourceVersion(t *testing.T) {
	raw := object(t, deploymentDoc)
	h := newHarness(t, nil, raw)

	refusal := refusalOf(t, mustFailApply(h.writer.ApplyManifest(t.Context(), ManifestChange{
		Target:          deploymentTarget(),
		YAML:            edited(t, shown(t, raw), "replicas: 3", "replicas: 5"),
		ResourceVersion: "290",
	})))
	if refusal.Kind != RefusedConflict {
		t.Fatalf("want a conflict, got %v: %s", refusal.Kind, refusal.Reason)
	}
	if !strings.Contains(refusal.Reason, "reload") {
		t.Fatalf("a conflict has to say what to do: %s", refusal.Reason)
	}
	h.noWrites()
}

// A confirmation screen left open while somebody else saves. The apply carries
// the version the diff was computed against, so it loses rather than applying a
// diff of an object it never saw.
func TestApplyAfterSomeoneElseSavedIsARefusal(t *testing.T) {
	raw := object(t, deploymentDoc)
	h := newHarness(t, nil, raw)

	moved := h.object(deploymentGVR, "shop", "api")
	moved.SetResourceVersion("311")
	if _, err := h.dyn.Resource(deploymentGVR).Namespace("shop").Update(t.Context(), moved, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("moving the fixture on: %v", err)
	}

	refusal := refusalOf(t, mustFailApply(h.writer.ApplyManifest(t.Context(), ManifestChange{
		Target:          deploymentTarget(),
		YAML:            edited(t, shown(t, raw), "replicas: 3", "replicas: 5"),
		ResourceVersion: "310",
	})))
	if refusal.Kind != RefusedConflict {
		t.Fatalf("want a conflict, got %v: %s", refusal.Kind, refusal.Reason)
	}
	h.noWrites()
}

// --- what the API would refuse, said first -------------------------------------

func TestManifestRefusesTheFieldsTheAPIWould(t *testing.T) {
	raw := object(t, deploymentDoc)
	doc := shown(t, raw)

	cases := []struct {
		name     string
		from, to string
		expect   string
	}{
		{"a rename", "  name: api\n", "  name: api2\n", "metadata.name"},
		{"a move", "  namespace: shop\n", "  namespace: other\n", "metadata.namespace"},
		{"the selector", "      app: api\n  template", "      app: api-v2\n  template", "spec.selector"},
		{"status", "  readyReplicas: 3", "  readyReplicas: 0", "status"},
		{"the uid", "  uid: 91ab-77", "  uid: 0000-00", "metadata.uid"},
		{"the precondition itself", `  resourceVersion: "310"`, `  resourceVersion: "999"`, "metadata.resourceVersion"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, nil, object(t, deploymentDoc))
			refusal := refusalOf(t, mustFailApply(h.writer.ApplyManifest(t.Context(), ManifestChange{
				Target:          deploymentTarget(),
				YAML:            edited(t, doc, c.from, c.to),
				ResourceVersion: "310",
			})))
			if refusal.Kind != RefusedImmutable {
				t.Fatalf("want an immutable refusal, got %v: %s", refusal.Kind, refusal.Reason)
			}
			if !strings.Contains(refusal.Reason, c.expect) {
				t.Fatalf("the refusal must name the field %q: %s", c.expect, refusal.Reason)
			}
			h.noWrites()
		})
	}
}

func TestManifestRefusesAnImmutableConfigMapsData(t *testing.T) {
	raw := object(t, coreDNS)
	raw.Object["immutable"] = true
	h := newHarness(t, nil, raw)

	doc := shown(t, raw)
	refusal := refusalOf(t, mustFailApply(h.writer.ApplyManifest(t.Context(), ManifestChange{
		Target:          Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		YAML:            edited(t, doc, "second", "third"),
		ResourceVersion: "204",
	})))
	if refusal.Kind != RefusedImmutable {
		t.Fatalf("want an immutable refusal, got %v: %s", refusal.Kind, refusal.Reason)
	}
	h.noWrites()
}

func TestManifestRefusesInvalidYAML(t *testing.T) {
	h := newHarness(t, nil, object(t, deploymentDoc))

	refusal := refusalOf(t, mustFailApply(h.writer.ApplyManifest(t.Context(), ManifestChange{
		Target:          deploymentTarget(),
		YAML:            "spec:\n  replicas: 3\n    bad: indentation\n",
		ResourceVersion: "310",
	})))
	if refusal.Kind != RefusedInput {
		t.Fatalf("want an input refusal, got %v: %s", refusal.Kind, refusal.Reason)
	}
	h.noWrites()
}

// --- the dry run ----------------------------------------------------------------

// The dry run is what turns "it was rejected" into "it was rejected because",
// and it has to *gate* the commit: a validation that fails and a request that
// goes anyway is a validation that costs a round trip and buys nothing.
func TestADryRunRefusalStopsTheCommit(t *testing.T) {
	raw := object(t, deploymentDoc)
	h := newHarness(t, nil, raw)

	refused := 0
	h.dyn.PrependReactor("patch", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		refused++
		return true, nil, apierrors.NewInvalid(
			schema.GroupKind{Group: "apps", Kind: "Deployment"}, "api",
			field.ErrorList{field.Invalid(field.NewPath("spec", "replicas"), 5, "must be less than 4")})
	})

	refusal := refusalOf(t, mustFailApply(h.writer.ApplyManifest(t.Context(), ManifestChange{
		Target:          deploymentTarget(),
		YAML:            edited(t, shown(t, raw), "replicas: 3", "replicas: 5"),
		ResourceVersion: "310",
	})))
	if refusal.Kind != RefusedByAPI {
		t.Fatalf("want the API's own refusal, got %v: %s", refusal.Kind, refusal.Reason)
	}
	if refused != 1 {
		t.Fatalf("the commit was attempted after the validation failed: %d request(s)", refused)
	}
}

// The cluster's refusal reaches the person whole. A validation answer that
// names several fields runs past any line a table cell would take, and the
// field that explains it is often the last one: cutting it leaves a sentence
// nobody can act on.
func TestTheClusterRefusalReachesThePersonWhole(t *testing.T) {
	raw := object(t, deploymentDoc)
	h := newHarness(t, nil, raw)

	var errs field.ErrorList
	for _, name := range []string{"minReplicas", "maxReplicas", "metrics", "behavior", "scaleTargetRef.kind"} {
		errs = append(errs, field.Required(field.NewPath("spec").Child(name), "a value is required for this field"))
	}
	errs = append(errs, field.Required(field.NewPath("spec", "scaleTargetRef", "name"), "the last field named"))
	h.dyn.PrependReactor("patch", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInvalid(schema.GroupKind{Group: "apps", Kind: "Deployment"}, "api", errs)
	})

	_, err := h.writer.PreviewManifest(t.Context(), ManifestChange{
		Target:          deploymentTarget(),
		YAML:            edited(t, shown(t, raw), "replicas: 3", "replicas: 5"),
		ResourceVersion: "310",
	})
	refusal := refusalOf(t, err)
	if refusal.Kind != RefusedByAPI {
		t.Fatalf("want the API's own refusal, got %v: %s", refusal.Kind, refusal.Reason)
	}
	if n := len([]rune(refusal.Reason)); n <= 300 {
		t.Fatalf("the refusal is %d runes; the test needs one over 300 to mean anything: %s", n, refusal.Reason)
	}
	if !strings.HasSuffix(refusal.Reason, "spec.scaleTargetRef.name: Required value: the last field named]") {
		t.Fatalf("the end of the cluster's answer is missing: %s", refusal.Reason)
	}
	if strings.Contains(refusal.Reason, "…") {
		t.Fatalf("the refusal was cut: %s", refusal.Reason)
	}
}

func TestManifestValidatesBeforeItCommits(t *testing.T) {
	raw := object(t, deploymentDoc)
	h := newHarness(t, nil, raw)

	if _, err := h.writer.ApplyManifest(t.Context(), ManifestChange{
		Target:          deploymentTarget(),
		YAML:            edited(t, shown(t, raw), "replicas: 3", "replicas: 5"),
		ResourceVersion: "310",
	}); err != nil {
		t.Fatalf("the write was refused: %v", err)
	}
	if len(h.sent) != 2 {
		t.Fatalf("want a dry run and a commit, got %d request(s)", len(h.sent))
	}
	if h.sent[0].Body != h.sent[1].Body {
		t.Fatalf("the committed request differs from the validated one:\n%s\n%s", h.sent[0].Body, h.sent[1].Body)
	}
}

// A preview sends nothing but the dry run, which changes nothing by definition.
func TestPreviewCommitsNothing(t *testing.T) {
	raw := object(t, deploymentDoc)
	h := newHarness(t, nil, raw)

	preview, err := h.writer.PreviewManifest(t.Context(), ManifestChange{
		Target:          deploymentTarget(),
		YAML:            edited(t, shown(t, raw), "replicas: 3", "replicas: 5"),
		ResourceVersion: "310",
	})
	if err != nil {
		t.Fatalf("the preview failed: %v", err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("a preview should send one dry run and nothing else, got %d", len(h.sent))
	}

	// The lines the operator confirms.
	var added, removed []string
	for _, line := range preview.Diff {
		switch {
		case line.Added():
			added = append(added, line.Text)
		case line.Removed():
			removed = append(removed, line.Text)
		}
	}
	if len(added) != 1 || !strings.Contains(added[0], "replicas: 5") {
		t.Fatalf("the diff does not show the new line: %v", added)
	}
	if len(removed) != 1 || !strings.Contains(removed[0], "replicas: 3") {
		t.Fatalf("the diff does not show the old line: %v", removed)
	}
	if len(preview.Fields) != 1 || preview.Fields[0] != "spec.replicas" {
		t.Fatalf("the fields shown are not the fields sent: %v", preview.Fields)
	}
	if !strings.Contains(preview.Patch, `"replicas": 5`) {
		t.Fatalf("the request shown is not the request built: %s", preview.Patch)
	}
	if strings.Contains(preview.Patch, "managedFields") {
		t.Fatalf("the confirmation shows a patch touching managedFields: %s", preview.Patch)
	}
}

// The log line for a manifest write, same shape as the key edit's, so one grep
// finds every write this process has made.
func TestAManifestWriteIsLogged(t *testing.T) {
	raw := object(t, deploymentDoc)
	h := newHarness(t, nil, raw)

	if _, err := h.writer.ApplyManifest(t.Context(), ManifestChange{
		Target:          deploymentTarget(),
		YAML:            edited(t, shown(t, raw), "replicas: 3", "replicas: 5"),
		ResourceVersion: "310",
	}); err != nil {
		t.Fatalf("the write was refused: %v", err)
	}
	if len(h.logged) != 1 {
		t.Fatalf("want one log line, got %v", h.logged)
	}
	for _, want := range []string{"write:", `cluster="prod"`, "kind=deployments", "namespace=shop", "name=api", "spec.replicas"} {
		if !strings.Contains(h.logged[0], want) {
			t.Fatalf("the log line does not carry %q: %s", want, h.logged[0])
		}
	}
}

func mustFailApply(_ Applied, err error) error   { return err }
func mustFailPreview(_ Preview, err error) error { return err }

// --- the mask must not be writable back over the values ----------------------

// A Secret with two values and a label, which is the shape that makes the mask
// dangerous: the operator has a reason to edit this object that has nothing to
// do with its credentials.
const secretDoc = `
apiVersion: v1
kind: Secret
metadata:
  name: db
  namespace: shop
  resourceVersion: "44"
  labels:
    app: api
type: Opaque
data:
  password: c3VwZXItc2VjcmV0
  username: YWRtaW4=
`

func secretTarget() Target {
	return Target{Cluster: "prod", Kind: "secrets", Namespace: "shop", Name: "db"}
}

// This is the test the whole placement of the mask exists for.
//
// `detail.Trimmed` is both the document the drawer shows and the base of this
// diff. Mask in the renderer alone and the two stop matching: the base holds the
// real values, the edited document holds masks, and an operator who came to
// rename a label writes `<masked>` over a production credential and is told the
// save succeeded.
//
// Masked in `Trimmed`, both sides carry the mask, so `data` cannot enter the
// patch at all.
func TestEditingALabelOnASecretCannotWriteTheMaskOverItsValues(t *testing.T) {
	raw := object(t, secretDoc)
	h := newHarness(t, nil, raw)

	doc := shown(t, raw)
	if !strings.Contains(doc, detail.SecretValueMask) {
		t.Fatalf("the document on screen carries no mask, so this test proves nothing:\n%s", doc)
	}
	for _, value := range []string{"c3VwZXItc2VjcmV0", "YWRtaW4="} {
		if strings.Contains(doc, value) {
			t.Fatalf("%s reached the drawer: %s", value, doc)
		}
	}

	if _, err := h.writer.ApplyManifest(t.Context(), ManifestChange{
		Target:          secretTarget(),
		YAML:            edited(t, doc, "app: api", "app: api-v2"),
		ResourceVersion: "44",
	}); err != nil {
		t.Fatalf("the write was refused: %v", err)
	}
	if len(h.sent) == 0 {
		t.Fatal("nothing was sent")
	}
	if body := h.sent[0].Body; strings.Contains(body, "\"data\"") || strings.Contains(body, detail.SecretValueMask) {
		t.Fatalf("the patch touches the Secret's values: %s", body)
	}

	// The claim that matters is about the cluster, not about the patch.
	live := h.object(secretGVR, "shop", "db")
	if password, _, _ := unstructured.NestedString(live.Object, "data", "password"); password != "c3VwZXItc2VjcmV0" {
		t.Fatalf("the stored password is now %q — an edit to a label destroyed a credential", password)
	}
	if username, _, _ := unstructured.NestedString(live.Object, "data", "username"); username != "YWRtaW4=" {
		t.Fatalf("the stored username is now %q", username)
	}
}

// The other direction, because a mask that cannot be edited through is a
// read-only manifest wearing an editor's clothes. Typing a new base64 value over
// the mask is the deliberate gesture that should write one, and it must work.
func TestTypingANewValueOverTheMaskWritesIt(t *testing.T) {
	raw := object(t, secretDoc)
	h := newHarness(t, nil, raw)

	doc := edited(t, shown(t, raw), detail.SecretValueMask, "bmV3LXNlY3JldA==")
	if _, err := h.writer.ApplyManifest(t.Context(), ManifestChange{
		Target: secretTarget(), YAML: doc, ResourceVersion: "44",
	}); err != nil {
		t.Fatalf("the write was refused: %v", err)
	}

	live := h.object(secretGVR, "shop", "db")
	password, _, _ := unstructured.NestedString(live.Object, "data", "password")
	if password != "bmV3LXNlY3JldA==" {
		t.Fatalf("the new value did not reach the cluster: data.password is %q", password)
	}
	// `data` is marshalled with its keys sorted, so the first mask in the
	// document is password's. If that ever stops being true this assertion is
	// what says so, rather than the test silently checking the wrong key.
	if username, _, _ := unstructured.NestedString(live.Object, "data", "username"); username != "YWRtaW4=" {
		t.Fatalf("the key nobody edited is now %q", username)
	}
}

// Saving a Secret with nothing changed at all. A whole-object update would
// send the masks; it must send nothing.
func TestASecretSavedUnchangedSendsNothing(t *testing.T) {
	raw := object(t, secretDoc)
	h := newHarness(t, nil, raw)

	refusal := refusalOf(t, mustFailApply(h.writer.ApplyManifest(t.Context(), ManifestChange{
		Target:          secretTarget(),
		YAML:            shown(t, raw),
		ResourceVersion: "44",
	})))
	if refusal.Kind != RefusedNoChange {
		t.Fatalf("want a no-change refusal, got %v: %s", refusal.Kind, refusal.Reason)
	}
	h.noWrites()
}

// TestAFieldPathCannotForgeAWriteLine: the field paths a manifest apply logs
// are the keys of the YAML the person sent, and a key holding a newline could
// end the real line and write a second one, attributed to somebody else, in the
// record that is supposed to name who changed a cluster.
func TestAFieldPathCannotForgeAWriteLine(t *testing.T) {
	raw := object(t, deploymentDoc)
	h := newHarness(t, nil, raw)
	forged := `    "x\nwrite: cluster=\"prod\" kind=secrets namespace=prod name=db fields=data who=\"ceo@corp.example\" via=sso\ny": v` + "\n"
	doc := edited(t, shown(t, raw), "    team: payments\n", "    team: payments\n"+forged)

	ctx := identity.With(t.Context(), identity.Who{Name: "mallory@example.com", Method: identity.SSO})
	if _, err := h.writer.ApplyManifest(ctx, ManifestChange{
		Target: deploymentTarget(), YAML: doc, ResourceVersion: "310",
	}); err != nil {
		t.Fatalf("the write was refused: %v", err)
	}
	if len(h.logged) != 1 {
		t.Fatalf("want one log line, got %d: %q", len(h.logged), h.logged)
	}
	line := h.logged[0]
	if strings.ContainsAny(line, "\n\r") {
		t.Fatalf("the line carries a raw line break, so the log shows two writes where there was one:\n%s", line)
	}
	if !strings.HasSuffix(line, ` who="mallory@example.com" via=sso`) {
		t.Errorf("the line does not end with who really asked: %s", line)
	}
	if strings.Contains(line, `who="ceo@corp.example"`) {
		t.Errorf("the forged attribution reads as a real one: %s", line)
	}
}

func TestParseDocumentRefusesAMultiDocumentFileAndNamesTheCount(t *testing.T) {
	two := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: b\n"
	_, err := parseDocument(two)
	if err == nil || !strings.Contains(err.Error(), "2 documents") {
		t.Fatalf("a two-document file must be refused with its count, got %v", err)
	}
	three := "---\n" + two + "---\nkind: Secret\n"
	if _, err := parseDocument(three); err == nil || !strings.Contains(err.Error(), "3 documents") {
		t.Fatalf("want 3 documents, got %v", err)
	}
}

func TestParseDocumentIgnoresMarkersAndCommentOnlyDocuments(t *testing.T) {
	one := "# header\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n---\n# nothing here\n---\n"
	got, err := parseDocument(one)
	if err != nil {
		t.Fatalf("one object among empty documents must parse, got %v", err)
	}
	if got["kind"] != "ConfigMap" {
		t.Fatalf("parsed %v", got)
	}
}

package edit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/identity"
)

// The access review at the write door: every request this package sends is
// asked about first, for the person behind the request, and a refused one sends
// nothing. The structural test in internal/access proves there is no other
// door; these prove this one works.

func carol() context.Context {
	return identity.With(context.Background(), identity.Who{
		Name: "carol@example.com", Method: identity.SSO, Groups: []string{"oidc:viewers"},
	})
}

func denyAll(accesstest.Question) (bool, error) { return false, nil }

func keyChange() KeyChange {
	return KeyChange{
		Target: Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"},
		Key:    "Corefile", Value: ".:53 {\n    log\n}\n", ResourceVersion: "204",
	}
}

// The question is the request: `patch` on configmaps, in the object's
// namespace, on its name, for the person - asked for the dry run and for the
// commit alike.
func TestAKeySaveIsReviewedAsThePatchItSends(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))
	if _, err := h.writer.SetKey(carol(), keyChange()); err != nil {
		t.Fatalf("an allowed save was refused: %v", err)
	}
	asked := h.reviews.Asked()
	want := access.Attributes{Verb: "patch", Version: "v1", Resource: "configmaps", Namespace: "kube-system", Name: "coredns"}
	if len(asked) == 0 {
		t.Fatal("the save was sent without asking the access review anything")
	}
	// And `get` on the same object, asked before the object is read.
	read := want
	read.Verb = "get"
	for _, q := range asked {
		if (q.Attributes != want && q.Attributes != read) || q.User != "carol@example.com" || q.Self {
			t.Errorf("the review asked %+v, want %+v (or its get) for carol", q, want)
		}
	}
}

// Denied: nothing is sent - not the dry run, not the commit - the refusal is
// the access one, and the record says so.
func TestADeniedSaveSendsNothingAndIsRecorded(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))
	h.reviews.Answer = denyAll

	refusal := refusalOf(t, mustFail(h.writer.SetKey(carol(), keyChange())))
	if refusal.Kind != RefusedByAccess {
		t.Errorf("a denied save was refused as %d, want RefusedByAccess", refusal.Kind)
	}
	if !strings.Contains(refusal.Reason, "may not patch configmaps coredns in namespace kube-system") {
		t.Errorf("the refusal does not say what was denied: %q", refusal.Reason)
	}
	h.noWrites()
	// SetKey dry-runs before it commits, and the dry run was the request
	// refused, so the commit never began - and a refused dry run writes no
	// line, because it would have changed nothing. The next test is the commit
	// refused on its own, as a hand-made post would reach it.
	if len(h.logged) != 0 {
		t.Errorf("a refused dry run wrote a line: %v", h.logged)
	}
}

// A commit refused at the review is one line, with the decision.
func TestARefusedCommitWritesOneLineWithItsDecision(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))
	where, err := h.writer.Locate(carol(), keyChange().Target)
	if err != nil {
		t.Fatal(err)
	}
	h.reviews.Answer = denyAll
	if _, _, err := Send(carol(), where, keyChange().Target, []byte(`{"data":{"Corefile":"x"}}`), false); err == nil {
		t.Fatal("a denied commit was sent")
	}
	h.noWrites()
	if len(h.logged) != 1 {
		t.Fatalf("want one line for the refused commit, got %v", h.logged)
	}
	for _, want := range []string{"write: ", "verb=patch", "resource=configmaps", "namespace=kube-system",
		"name=coredns", "decision=denied", "outcome=refused", `who="carol@example.com" via=sso`} {
		if !strings.Contains(h.logged[0], want) {
			t.Errorf("the line does not carry %q: %s", want, h.logged[0])
		}
	}
}

// A review that could not be made refuses with its own kind, never as a
// denial.
func TestAnUnreviewableSaveIsRefusedAsUnreviewable(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))
	h.reviews.Answer = func(accesstest.Question) (bool, error) { return false, errors.New("the API server did not answer") }

	refusal := refusalOf(t, mustFail(h.writer.SetKey(carol(), keyChange())))
	if refusal.Kind != RefusedUnreviewable {
		t.Errorf("an unreviewable save was refused as %d, want RefusedUnreviewable", refusal.Kind)
	}
	if !strings.Contains(refusal.Reason, "could not ask") || strings.Contains(refusal.Reason, "may not") {
		t.Errorf("the refusal reads as a denial: %q", refusal.Reason)
	}
	h.noWrites()
}

// No gate at all refuses every write: the fail-closed default.
func TestAWriterWithNoAccessReviewWritesNothing(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))
	h.writer = New(Options{Resolve: fakeResolver{dyn: h.dyn, cluster: h.cluster}})
	refusal := refusalOf(t, mustFail(h.writer.SetKey(carol(), keyChange())))
	if refusal.Kind != RefusedUnreviewable {
		t.Errorf("a Writer with no access review refused as %d", refusal.Kind)
	}
	h.noWrites()
}

// Allowed, and then the API server refuses: one line, outcome=failed, and a
// word for why rather than the API server's message - which may quote a value.
func TestAFailedCommitIsRecordedWithAWordAndNoValue(t *testing.T) {
	raw := object(t, registryCreds)
	h := newHarness(t, nil, raw)
	where, err := h.writer.Locate(carol(), Target{Cluster: "prod", Kind: "secrets", Namespace: "shop", Name: "registry"})
	if err != nil {
		t.Fatal(err)
	}
	// An admission webhook refusing, and quoting the value it refused - which
	// is what the line must never repeat.
	h.dyn.PrependReactor("patch", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewBadRequest(`admission webhook "policy" denied: value "hunter2" is weak`)
	})
	_, _, err = Send(carol(), where, Target{Cluster: "prod", Kind: "secrets", Namespace: "shop", Name: "registry"},
		[]byte(`{"data":{"password":"aHVudGVyMg=="}}`), false)
	if err == nil {
		t.Fatal("the refused patch answered no error")
	}
	if len(h.logged) != 1 || !strings.Contains(h.logged[0], "decision=allowed outcome=failed reason=") {
		t.Fatalf("want one outcome=failed line, got %v", h.logged)
	}
	for _, secret := range []string{"aHVudGVyMg==", "hunter2", "c3VwZXItc2VjcmV0"} {
		if strings.Contains(h.logged[0], secret) {
			t.Errorf("the failure line carries a value: %s", h.logged[0])
		}
	}
}

// The kind is read out of the document. A manifest naming another kind
// than the resource it goes to is refused before anything is reviewed or sent.
func TestAManifestNamingAnotherKindIsRefusedBeforeTheReview(t *testing.T) {
	raw := object(t, deploymentDoc)
	h := newHarness(t, nil, raw)
	for _, tc := range []struct{ name, from, to string }{
		{"another kind", "kind: Deployment", "kind: ClusterRoleBinding"},
		{"another group", "apiVersion: apps/v1", "apiVersion: rbac.authorization.k8s.io/v1"},
	} {
		_, err := h.writer.ApplyManifest(carol(), ManifestChange{
			Target:          deploymentTarget(),
			YAML:            edited(t, shown(t, raw), tc.from, tc.to),
			ResourceVersion: "310",
		})
		refusal := refusalOf(t, err)
		if refusal.Kind != RefusedInput || !strings.Contains(refusal.Reason, "have to agree") {
			t.Errorf("%s: refused as %d: %q", tc.name, refusal.Kind, refusal.Reason)
		}
	}
	h.noWrites()
	if n := len(h.reviews.Asked()); n != 0 {
		t.Errorf("%d reviews were asked for documents that were never going to be sent", n)
	}
}

// And the resource reviewed is the resource written: the manifest's patch goes
// to deployments.apps in shop, and so does the question.
func TestAManifestIsReviewedAsTheResourceItIsSentTo(t *testing.T) {
	raw := object(t, deploymentDoc)
	h := newHarness(t, nil, raw)
	if _, err := h.writer.ApplyManifest(carol(), ManifestChange{
		Target: deploymentTarget(), YAML: edited(t, shown(t, raw), "replicas: 3", "replicas: 5"), ResourceVersion: "310",
	}); err != nil {
		t.Fatal(err)
	}
	want := access.Attributes{Verb: "patch", Group: "apps", Version: "v1", Resource: "deployments", Namespace: "shop", Name: "api"}
	read := want
	read.Verb = "get" // asked before the object is read
	for _, q := range h.reviews.Asked() {
		if q.Attributes != want && q.Attributes != read {
			t.Errorf("reviewed %+v, want %+v or its get", q.Attributes, want)
		}
	}
	if len(h.sent) == 0 || h.sent[len(h.sent)-1].Resource != "deployments" {
		t.Errorf("the patch went to %+v", h.sent)
	}
	if !strings.Contains(h.logged[0], "decision=allowed outcome=applied") {
		t.Errorf("the success line carries no decision: %s", h.logged[0])
	}
}

// A create is reviewed without a name, which is how the API server authorizes
// one; with the name, a Role granting create on named objects would answer yes
// where the create itself is refused.
func TestACreateIsReviewedWithoutAName(t *testing.T) {
	where := Location{Cluster: "prod", Resource: deploymentGVR, Kind: "Deployment", Namespaced: true}
	got := attributesOf(where, Target{Namespace: "shop", Name: "api"}, "create", "")
	if got.Name != "" {
		t.Errorf("a create was reviewed naming %q", got.Name)
	}
	if evict := attributesOf(where, Target{Namespace: "shop", Name: "api"}, "create", "eviction"); evict.Name != "api" {
		t.Errorf("a subresource create lost its object's name: %+v", evict)
	}
}

// A body whose kind is not the endpoint's is refused before a create is sent.
func TestACreateOfAnotherKindIsRefused(t *testing.T) {
	where := Location{Cluster: "prod", Resource: deploymentGVR, Kind: "Deployment", Namespaced: true, guarded: true}
	body := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": "api", "namespace": "shop"},
	}}
	if _, err := createInterface(where, Target{Namespace: "shop", Name: "api"}, body); err == nil ||
		!strings.Contains(err.Error(), "have to agree") {
		t.Errorf("a ClusterRoleBinding body addressed to deployments was answered %v", err)
	}
}

// Delete and Evict go through the door too, reviewed as the request they send:
// `delete` on the object, `create pods/eviction` on the pod. Denied, nothing is
// sent and the refused commit is on the record.
func TestADeleteAndAnEvictionAreReviewedAndSendNothingWhenDenied(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))
	target := Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"}
	where, err := h.writer.Locate(carol(), target)
	if err != nil {
		t.Fatal(err)
	}
	h.reviews.Answer = denyAll
	if _, err := Delete(carol(), where, target, metav1.DeleteOptions{}); err == nil {
		t.Fatal("a denied delete answered no error")
	}
	if _, err := Evict(carol(), where, target, &unstructured.Unstructured{Object: map[string]any{}}); err == nil {
		t.Fatal("a denied eviction answered no error")
	}
	for _, action := range h.dyn.Actions() {
		if verb := action.GetVerb(); verb == "delete" || verb == "create" {
			t.Errorf("a denied %s reached the cluster: %+v", verb, action)
		}
	}
	asked := h.reviews.Asked()
	if len(asked) != 2 || asked[0].Attributes.Verb != "delete" || asked[1].Attributes.Subresource != "eviction" ||
		asked[0].Attributes.Name != "coredns" || asked[1].Attributes.Name != "coredns" {
		t.Errorf("the reviews asked %+v", asked)
	}
	if len(h.logged) != 2 {
		t.Errorf("want one refused line per commit, got %v", h.logged)
	}
}

// lagging is a context whose deadline has passed and whose timer has not
// fired yet: Err is still nil. On a loaded machine every timed context is this
// for a moment after its deadline.
type lagging struct {
	context.Context
	deadline time.Time
}

func (c lagging) Deadline() (time.Time, bool) { return c.deadline, true }

// An eviction past its deadline is held back even before the context's timer
// says so, and the error says nothing was sent. Reading only ctx.Err() would
// send it, and a DeadlineExceeded from the client would then read as sent and
// not answered - or, in the drain, as a refusal.
func TestAnEvictionPastItsDeadlineIsNotSentBeforeItsTimerFires(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))
	target := Target{Cluster: "prod", Kind: "configmaps", Namespace: "kube-system", Name: "coredns"}
	where, err := h.writer.Locate(carol(), target)
	if err != nil {
		t.Fatal(err)
	}
	ctx := lagging{Context: carol(), deadline: time.Now().Add(-time.Second)}
	if ctx.Err() != nil {
		t.Fatal("the lagging context is already done; the test would not test the deadline")
	}
	_, err = Evict(ctx, where, target, &unstructured.Unstructured{Object: map[string]any{}})
	if !errors.Is(err, ErrNotSent) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("an eviction past its deadline answered %v", err)
	}
	for _, action := range h.dyn.Actions() {
		if action.GetVerb() == "create" {
			t.Errorf("an eviction past its deadline reached the cluster: %+v", action)
		}
	}
}

// On the desktop a save goes through a review that could not be made - the
// kubeconfig's own identity, which the API server checks - and the line says
// `decision=unreviewable`, never a yes nobody gave.
func TestADesktopSaveThroughAnUnreviewableReviewSaysSo(t *testing.T) {
	h := newHarness(t, nil, object(t, coreDNS))
	h.reviews.Answer = func(accesstest.Question) (bool, error) {
		return false, errors.New("the authorization API is not served")
	}
	if _, err := h.writer.SetKey(context.Background(), keyChange()); err != nil {
		t.Fatalf("a desktop save was refused: %v", err)
	}
	if len(h.sent) == 0 {
		t.Fatal("the save was not sent")
	}
	if len(h.logged) != 1 || !strings.Contains(h.logged[0], "decision=unreviewable outcome=applied") {
		t.Errorf("want one line saying decision=unreviewable, got %v", h.logged)
	}
}

// The manifest apply's line, on the same desktop answer.
func TestADesktopManifestApplyThroughAnUnreviewableReviewSaysSo(t *testing.T) {
	raw := object(t, deploymentDoc)
	h := newHarness(t, nil, raw)
	h.reviews.Answer = func(accesstest.Question) (bool, error) {
		return false, errors.New("the authorization API is not served")
	}
	if _, err := h.writer.ApplyManifest(context.Background(), ManifestChange{
		Target: deploymentTarget(), YAML: edited(t, shown(t, raw), "replicas: 3", "replicas: 5"), ResourceVersion: "310",
	}); err != nil {
		t.Fatalf("a desktop apply was refused: %v", err)
	}
	if len(h.logged) != 1 || !strings.Contains(h.logged[0], "fields=") ||
		!strings.Contains(h.logged[0], "decision=unreviewable outcome=applied") {
		t.Errorf("want the apply's line with decision=unreviewable, got %v", h.logged)
	}
}

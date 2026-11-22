package actions

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/edit"
	"github.com/thonicdev/kubetower/internal/identity"
)

func dave() context.Context {
	return identity.With(context.Background(), identity.Who{Name: "dave@example.com", Method: identity.SSO, Groups: []string{"oidc:ops"}})
}

func confirmation(verb Verb, kind, name string) string {
	if Typed(verb, kind) {
		return SingleConfirm(verb, kind, name)
	}
	return ConfirmToken
}

func distinct(questions []accesstest.Question) []access.Attributes {
	seen := map[access.Attributes]bool{}
	var out []access.Attributes
	for _, q := range questions {
		if !seen[q.Attributes] {
			seen[q.Attributes] = true
			out = append(out, q.Attributes)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// The prediction a page offers a verb on is the question the verb is reviewed
// with when it runs - every verb, run for real against the fake with a
// recording review. A page that offered Evict because it predicted `delete`
// would offer a control the review then refuses.
func TestWhatAVerbIsPredictedToNeedIsWhatItIsReviewedFor(t *testing.T) {
	cases := []struct {
		verb     Verb
		object   *unstructured.Unstructured
		kind     string
		replicas *int64
	}{
		{Delete, pod("shop", "api-1", ownedBy("ReplicaSet", "api")), "pods", nil},
		{Restart, pod("shop", "api-1", ownedBy("ReplicaSet", "api")), "pods", nil},
		{Evict, pod("shop", "api-1", ownedBy("ReplicaSet", "api")), "pods", nil},
		{Restart, deployment("shop", "api"), "deployments", nil},
		{Scale, deployment("shop", "api"), "deployments", replicaCount(2)},
		{Delete, deployment("shop", "api"), "deployments", nil},
		{Cordon, node("node-a"), "nodes", nil},
		{Uncordon, node("node-a"), "nodes", nil},
		{Suspend, cronJob("shop", "nightly"), "cronjobs", nil},
		{Resume, cronJob("shop", "nightly"), "cronjobs", nil},
		{Trigger, cronJob("shop", "nightly"), "cronjobs", nil},
	}
	for _, tc := range cases {
		h := newHarness(t, nil, tc.object)
		target := edit.Target{Cluster: "prod", Kind: tc.kind, Namespace: tc.object.GetNamespace(), Name: tc.object.GetName()}
		if _, err := h.runner.Run(dave(), Request{
			Target: target, Verb: tc.verb, Confirm: confirmation(tc.verb, tc.kind, tc.object.GetName()), Replicas: tc.replicas,
		}); err != nil {
			t.Errorf("%s %s: %v", tc.verb, tc.kind, err)
			continue
		}
		where, err := fakeResolver{dyn: h.dyn, cluster: "prod"}.Locate(t.Context(), "prod", tc.kind)
		if err != nil {
			t.Fatal(err)
		}
		predicted := Needs(tc.verb, where, target)
		sort.Slice(predicted, func(i, j int) bool { return predicted[i].String() < predicted[j].String() })
		if asked := distinct(h.reviews.Asked()); !reflect.DeepEqual(asked, predicted) {
			t.Errorf("%s on %s: reviewed %v, predicted %v", tc.verb, tc.kind, asked, predicted)
		}
	}
}

// A denied delete sends nothing and says who may not.
func TestADeniedVerbSendsNothing(t *testing.T) {
	h := newHarness(t, nil, pod("shop", "api-1", ownedBy("ReplicaSet", "api")))
	h.reviews.Answer = func(accesstest.Question) (bool, error) { return false, nil }
	_, err := h.runner.Run(dave(), Request{
		Target:  edit.Target{Cluster: "prod", Kind: "pods", Namespace: "shop", Name: "api-1"},
		Verb:    Delete,
		Confirm: confirmation(Delete, "pods", "api-1"),
	})
	refusal, ok := edit.AsRefusal(err)
	if !ok || refusal.Kind != edit.RefusedByAccess {
		t.Fatalf("a denied delete answered %v", err)
	}
	h.noWrites()
	var refused int
	for _, line := range h.logged {
		if strings.HasPrefix(line, "write: ") && strings.Contains(line, "decision=denied") && strings.Contains(line, "verb=delete") {
			refused++
		}
	}
	if refused != 1 {
		t.Errorf("want one decision=denied line, got %v", h.logged)
	}
}

// A drain whose evictions are denied in one namespace records each refused
// eviction on its own line, evicts the rest, and says so.
func TestADrainRecordsEachEvictionItsOwnWay(t *testing.T) {
	h := newHarness(t, nil, node("node-a"),
		pod("shop", "api-1", ownedBy("ReplicaSet", "api")),
		pod("billing", "ledger-1", ownedBy("ReplicaSet", "ledger")))
	h.reviews.Answer = func(q accesstest.Question) (bool, error) {
		return q.Attributes.Subresource != "eviction" || q.Attributes.Namespace == "shop", nil
	}
	req := Request{Target: edit.Target{Cluster: "prod", Kind: "nodes", Name: "node-a"}, Verb: Drain,
		Confirm: confirmation(Drain, "nodes", "node-a"), Stage: StageEvict}
	result, err := h.runner.Run(dave(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Plan == nil || result.Plan.Requested != 1 || result.Plan.Failed != 1 {
		t.Fatalf("the plan says %+v; want one evicted and one refused", result.Plan)
	}
	var applied, denied int
	for _, line := range h.logged {
		switch {
		case strings.Contains(line, "action=evict") && strings.Contains(line, "outcome=applied") && strings.Contains(line, "name=api-1"):
			applied++
		case strings.Contains(line, "verb=create resource=pods/eviction namespace=billing") && strings.Contains(line, "decision=denied"):
			denied++
		}
	}
	if applied != 1 || denied != 1 {
		t.Errorf("want one applied and one denied eviction line, got %v", h.logged)
	}
	for _, write := range h.writes() {
		if write.Subresource == "eviction" && write.Namespace == "billing" {
			t.Errorf("the denied eviction was sent: %+v", write)
		}
	}
}

// On the desktop, a review of the kubeconfig's own identity that could not be
// made lets the verb through, and every line it writes says
// `decision=unreviewable` - never a yes nobody gave.
func TestADesktopVerbThroughAnUnreviewableReviewSaysSoOnItsLine(t *testing.T) {
	failing := func(accesstest.Question) (bool, error) {
		return false, errors.New("the authorization API is not served")
	}
	for _, tc := range []struct {
		name     string
		objects  []*unstructured.Unstructured
		req      Request
		lineWith string
	}{
		{"delete", []*unstructured.Unstructured{pod("shop", "api-1", ownedBy("ReplicaSet", "api"))},
			Request{Target: edit.Target{Cluster: "prod", Kind: "pods", Namespace: "shop", Name: "api-1"}, Verb: Delete,
				Confirm: confirmation(Delete, "pods", "api-1")}, "action=delete"},
		{"scale", []*unstructured.Unstructured{deployment("shop", "api")},
			Request{Target: edit.Target{Cluster: "prod", Kind: "deployments", Namespace: "shop", Name: "api"}, Verb: Scale,
				Confirm: confirmation(Scale, "deployments", "api"), Replicas: replicaCount(2)}, "action=scale"},
		{"trigger", []*unstructured.Unstructured{cronJob("shop", "nightly")},
			Request{Target: edit.Target{Cluster: "prod", Kind: "cronjobs", Namespace: "shop", Name: "nightly"}, Verb: Trigger,
				Confirm: confirmation(Trigger, "cronjobs", "nightly")}, "action=trigger"},
		{"a drain's eviction", []*unstructured.Unstructured{node("node-a"), pod("shop", "api-1", ownedBy("ReplicaSet", "api"))},
			Request{Target: edit.Target{Cluster: "prod", Kind: "nodes", Name: "node-a"}, Verb: Drain,
				Confirm: confirmation(Drain, "nodes", "node-a"), Stage: StageEvict}, "action=evict node=node-a"},
	} {
		h := newHarness(t, nil, tc.objects...)
		h.reviews.Answer = failing
		if _, err := h.runner.Run(context.Background(), tc.req); err != nil {
			t.Errorf("%s: the desktop verb was refused: %v", tc.name, err)
			continue
		}
		found := false
		for _, line := range h.logged {
			if strings.HasPrefix(line, "write: ") && strings.Contains(line, tc.lineWith) {
				found = true
				if !strings.Contains(line, "decision=unreviewable outcome=applied") {
					t.Errorf("%s: the line claims another decision: %s", tc.name, line)
				}
			}
		}
		if !found {
			t.Errorf("%s: no write line carrying %q in %v", tc.name, tc.lineWith, h.logged)
		}
	}
}

// **The plan of a write is shown only to whoever may make it.** A preview reads
// the object and shows a field of it and its version before anything is sent,
// so a person the review refuses the verb is refused its preview - and the
// object is never read for them.
func TestAPreviewIsRefusedToAPersonTheVerbIsDenied(t *testing.T) {
	h := newHarness(t, nil, deployment("shop", "api"))
	h.reviews.Answer = func(accesstest.Question) (bool, error) { return false, nil }
	replicas := int64(0)
	_, err := h.runner.Preview(dave(), Request{
		Target: edit.Target{Cluster: "prod", Kind: "deployments", Namespace: "shop", Name: "api"},
		Verb:   Scale, Replicas: &replicas,
	})
	refusal, ok := edit.AsRefusal(err)
	if !ok || refusal.Kind != edit.RefusedByAccess {
		t.Fatalf("a preview of a denied scale answered %v, want a refusal by the review", err)
	}
	for _, action := range h.dyn.Actions() {
		if action.GetVerb() == "get" {
			t.Errorf("the object was read for a person refused the verb: %s %s", action.GetVerb(), action.GetResource().Resource)
		}
	}
	// And allowed, the same preview reads and answers.
	h2 := newHarness(t, nil, deployment("shop", "api"))
	if recap, err := h2.runner.Preview(dave(), Request{
		Target: edit.Target{Cluster: "prod", Kind: "deployments", Namespace: "shop", Name: "api"},
		Verb:   Scale, Replicas: &replicas,
	}); err != nil || recap.Before != "3" {
		t.Errorf("an allowed preview answered %+v, %v", recap, err)
	}
}

// **Trigger is reviewed for reading the CronJob as well as for creating the
// Job**, as `kubectl create job --from=cronjob/x` is. A person who may
// create Jobs in a namespace and may not read its CronJobs is refused the
// preview and the act, and the CronJob is never read for them - its template is
// what the Job would be a copy of.
func TestTriggerIsRefusedToAPersonWhoMayNotReadTheCronJob(t *testing.T) {
	createJobsOnly := func(q accesstest.Question) (bool, error) {
		return q.Attributes.Verb == "create" && q.Attributes.Resource == "jobs", nil
	}
	target := edit.Target{Cluster: "prod", Kind: "cronjobs", Namespace: "shop", Name: "nightly"}
	noCronJobRead := func(h *harness, what string) {
		t.Helper()
		for _, action := range h.dyn.Actions() {
			if action.GetVerb() == "get" && action.GetResource().Resource == "cronjobs" {
				t.Errorf("%s: the CronJob was read for a person who may not get it", what)
			}
		}
	}

	h := newHarness(t, nil, cronJob("shop", "nightly"))
	h.reviews.Answer = createJobsOnly
	_, err := h.runner.Preview(dave(), Request{Target: target, Verb: Trigger})
	if refusal, ok := edit.AsRefusal(err); !ok || refusal.Kind != edit.RefusedByAccess {
		t.Errorf("the preview of a trigger without get cronjobs answered %v, want a refusal by the review", err)
	}
	noCronJobRead(h, "preview")

	h = newHarness(t, nil, cronJob("shop", "nightly"))
	h.reviews.Answer = createJobsOnly
	_, err = h.runner.Run(dave(), Request{Target: target, Verb: Trigger, Confirm: confirmation(Trigger, "cronjobs", "nightly")})
	if refusal, ok := edit.AsRefusal(err); !ok || refusal.Kind != edit.RefusedByAccess {
		t.Errorf("a trigger without get cronjobs answered %v, want a refusal by the review", err)
	}
	h.noWrites()
	noCronJobRead(h, "act")

	// The control: with both rights the same trigger creates its Job.
	h = newHarness(t, nil, cronJob("shop", "nightly"))
	if _, err := h.runner.Run(dave(), Request{Target: target, Verb: Trigger, Confirm: confirmation(Trigger, "cronjobs", "nightly")}); err != nil {
		t.Fatalf("an allowed trigger answered %v", err)
	}
	if len(h.writes()) == 0 {
		t.Error("an allowed trigger created no Job, so the refusal above proves nothing")
	}
}

// **A refusal at the bulk door and at the preview leaves a record**, as the
// single write door's does: the person, the verb, the target, the review's
// decision, outcome refused. A confirmed bulk row is a real attempt; a
// preview is marked a dry run like the other previews'.
func TestARefusedBulkRowAndARefusedPreviewAreRecorded(t *testing.T) {
	h := newHarness(t, func(string) bool { return false }, pod("prod", "api-1"), deployment("shop", "api"))
	h.reviews.Answer = func(accesstest.Question) (bool, error) { return false, nil }

	selection := targets("prod/api-1")
	result, err := h.runner.RunBulk(dave(), BulkRequest{
		Cluster: "prod", Kind: "pods", Verb: Delete,
		Targets: selection, Confirm: BulkConfirm(Delete, "pods", len(selection)),
	})
	if err != nil || result.Failed != 1 {
		t.Fatalf("a refused bulk row answered %+v, %v", result, err)
	}
	got := h.records.all(t)
	if len(got) != 2 {
		t.Fatalf("a refused bulk row left %d records, want an attempt and an outcome: %+v", len(got), got)
	}
	r := got[1]
	if r.Verb != "delete" || r.Resource != "pods" || r.Name != "api-1" || r.Decision != "denied" ||
		r.Outcome != audit.Refused || r.DryRun || r.Who != "dave@example.com" {
		t.Errorf("the bulk refusal was recorded as %+v", r)
	}

	replicas := int64(0)
	if _, err := h.runner.Preview(dave(), Request{
		Target: edit.Target{Cluster: "prod", Kind: "deployments", Namespace: "shop", Name: "api"},
		Verb:   Scale, Replicas: &replicas,
	}); err == nil {
		t.Fatal("a denied preview was answered")
	}
	got = h.records.all(t)
	if len(got) != 4 {
		t.Fatalf("a refused preview left %d new records, want an attempt and an outcome: %+v", len(got)-2, got)
	}
	p := got[3]
	if p.Verb != "patch" || p.Resource != "deployments" || p.Name != "api" || p.Decision != "denied" ||
		p.Outcome != audit.Refused || !p.DryRun {
		t.Errorf("the preview refusal was recorded as %+v", p)
	}
}

package web

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/detail"
	"github.com/thonicdev/kubetower/internal/edit"
	"github.com/thonicdev/kubetower/internal/identity"
)

// Every resource that can be deleted must be deletable.
// These hold the inventory - every kind the console lists offers Delete on its
// page, in its row menu (which reads the page's actions) and on its selection
// bar - and the server-profile path that withheld it without a word.

// deletableKinds is every kind the console's menu lists, plus the shapes a URL
// can carry for them: a singular, a capitalized one, a custom resource and a
// cluster-scoped kind.
var deletableKinds = []string{
	// workloads
	"pods", "deployments", "statefulsets", "daemonsets", "replicasets", "jobs", "cronjobs",
	"replicationcontrollers", "podtemplates", "controllerrevisions.apps",
	// network
	"services", "ingresses", "endpoints", "endpointslices", "ingressclasses", "networkpolicies",
	"servicecidrs.networking.k8s.io", "ipaddresses.networking.k8s.io",
	// configuration
	"configmaps", "secrets", "resourcequotas", "limitranges", "horizontalpodautoscalers",
	"poddisruptionbudgets", "priorityclasses", "runtimeclasses", "leases",
	"mutatingwebhookconfigurations", "validatingwebhookconfigurations",
	// access control
	"serviceaccounts", "roles", "rolebindings", "clusterroles", "clusterrolebindings",
	"certificatesigningrequests.certificates.k8s.io",
	// storage
	"persistentvolumeclaims", "persistentvolumes", "storageclasses", "volumeattachments.storage.k8s.io",
	// cluster
	"namespaces", "nodes", "customresourcedefinitions.apiextensions.k8s.io",
	// custom resources, and the spellings a hand-written link uses
	"widgets.example.com", "applications.argoproj.io", "Job", "job", "CronJob", "Node",
}

func hasVerb(verbs []string, verb actions.Verb) bool {
	for _, one := range verbs {
		if one == string(verb) {
			return true
		}
	}
	return false
}

func TestEveryKindOffersDeleteOnItsPageItsRowMenuAndItsSelectionBar(t *testing.T) {
	for _, kind := range deletableKinds {
		page := objectActions(kind, detail.Object{Kind: kind}, true, "")
		var found *api.DetailAction
		for i := range page {
			if page[i].Verb == string(actions.Delete) {
				found = &page[i]
			}
		}
		if found == nil {
			t.Errorf("%s: the page (and so the row menu, which reads it) offers %v, no Delete", kind, verbNames(page))
			continue
		}
		if !found.Permitted || found.Irreversible == nil || !*found.Irreversible {
			t.Errorf("%s: Delete is offered as %+v, want permitted and irreversible", kind, *found)
		}
		bulk := bulkAffordance("prod", kind, false)
		var bar []string
		if bulk != nil {
			for _, verb := range bulk.Verbs {
				bar = append(bar, verb.Verb)
			}
		}
		if !hasVerb(bar, actions.Delete) {
			t.Errorf("%s: the selection bar offers %v, no Delete", kind, bar)
		}
	}
}

// A Node offers Delete, last, after Drain, and
// keeps its three other verbs.
func TestANodeOffersDeleteAfterItsOtherVerbs(t *testing.T) {
	got := strings.Join(verbNames(objectActions("nodes", detail.Object{Kind: "nodes"}, true, "")), ",")
	if got != "cordon,uncordon,drain,delete" {
		t.Errorf("a node's page offers %s, want cordon,uncordon,drain,delete", got)
	}
}

// --- the server profile's shared account ------------------------------------

func sharedAccount() context.Context {
	return identity.With(context.Background(), identity.Who{Name: "admin", Method: identity.Password})
}

var jobsWhere = edit.Location{Cluster: "prod",
	Resource: schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, Kind: "Job", Namespaced: true}

// The path that could withhold Delete on a Job without a word: under the server
// profile the shared account is reviewed as the console's ServiceAccount, and
// that review's "no" - the chart's default role grants no write - must not be
// treated as the person's and withheld silently. It is drawn disabled, saying what is missing.
func TestTheConsolesOwnRefusalIsDrawnWithItsReasonUnderTheServerProfile(t *testing.T) {
	offered := objectActions("jobs", detail.Object{Kind: "jobs"}, true, "")
	target := edit.Target{Cluster: "prod", Kind: "jobs", Namespace: "batch-runs", Name: "nightly-1"}
	refused := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return false, nil }}
	gate := access.New(access.Options{Reviews: refused.For, InCluster: true})

	got := gateActions(offered, verdictsAt(sharedAccount(), gate, jobsWhere, target, []actions.Verb{actions.Delete}))
	if len(got) != 1 || got[0].Verb != string(actions.Delete) {
		t.Fatalf("the shared account was offered %v on a Job its ServiceAccount may not delete; "+
			"want Delete drawn disabled", verbNames(got))
	}
	if got[0].Permitted {
		t.Error("Delete is offered as working on a refusal")
	}
	reason := got[0].Reason
	if reason == nil || reason.Code != api.CodeVerbDenied {
		t.Fatalf("Delete carries %+v, want the console's refusal", reason)
	}
	for _, want := range []string{"ServiceAccount", "delete on jobs.batch in batch-runs", "rbac.write"} {
		if !strings.Contains(reason.Message, want) {
			t.Errorf("the reason %q does not say %q", reason.Message, want)
		}
	}

	// The selection bar draws it the same way.
	bulk := gateBulk(bulkAffordance("prod", "jobs", false),
		verdictsAt(sharedAccount(), gate, jobsWhere, edit.Target{Cluster: "prod", Kind: "jobs", Namespace: "batch-runs"},
			[]actions.Verb{actions.Delete}))
	if bulk == nil || len(bulk.Verbs) != 1 || bulk.Verbs[0].Permitted || bulk.Verbs[0].Reason == nil ||
		!strings.Contains(bulk.Verbs[0].Reason.Message, "ServiceAccount") {
		t.Errorf("the Jobs selection bar is %+v, want Delete disabled with the console's reason", bulk)
	}
}

// A refusal that is the person's own stays a withholding: a person signed in
// with single sign-on, and the desktop's kubeconfig identity.
func TestAPersonsOwnRefusalIsStillWithheld(t *testing.T) {
	offered := objectActions("jobs", detail.Object{Kind: "jobs"}, true, "")
	target := edit.Target{Cluster: "prod", Kind: "jobs", Namespace: "batch-runs", Name: "nightly-1"}
	verbs := []actions.Verb{actions.Delete}

	// The person is refused and the console is not.
	personRefused := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) { return q.Self, nil }}
	server := access.New(access.Options{Reviews: personRefused.For, InCluster: true})
	if got := gateActions(offered, verdictsAt(grace(), server, jobsWhere, target, verbs)); len(got) != 0 {
		t.Errorf("a person refused delete on jobs was offered %v", verbNames(got))
	}

	refused := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return false, nil }}
	desktop := access.New(access.Options{Reviews: refused.For, InCluster: false})
	if got := gateActions(offered, verdictsAt(context.Background(), desktop, jobsWhere, target, verbs)); len(got) != 0 {
		t.Errorf("the desktop offered %v to a kubeconfig refused delete on jobs", verbNames(got))
	}
}

// --- the act ------------------------------------------------------------------

var fakeJobGVR = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}

func fakeBatch(kind, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       kind,
		"metadata": map[string]any{"name": name, "namespace": "batch-runs",
			"uid": "uid-" + name, "resourceVersion": "7"},
	}}
}

// deleteOne previews and acts as internal/api does, the plan's precondition
// handed back to the act.
func deleteOne(t *testing.T, h *actsHarness, kind, namespace, name string) api.ActOutcome {
	t.Helper()
	ctx := context.Background()
	request := api.ActionRequest{Verb: string(actions.Delete),
		Target: api.PlanTarget{Kind: kind, Namespace: namespace, Name: name}}
	facts, err := h.acts.PreviewAction(ctx, "prod", request)
	if err != nil {
		t.Fatalf("previewing the delete of %s %s: %v", kind, name, err)
	}
	request.Precondition = facts.Precondition
	request.PreconditionUID = facts.PreconditionUID
	outcome, err := h.acts.RunAction(ctx, "prod", request, facts.Confirmation.Expected)
	if err != nil {
		t.Fatalf("deleting %s %s: %v", kind, name, err)
	}
	return outcome
}

func onlyDelete(t *testing.T, h *actsHarness) k8stesting.DeleteActionImpl {
	t.Helper()
	var deletes []k8stesting.DeleteActionImpl
	for _, action := range h.dyn.Actions() {
		if one, ok := action.(k8stesting.DeleteActionImpl); ok {
			deletes = append(deletes, one)
		}
	}
	if len(deletes) != 1 {
		t.Fatalf("%d delete request(s) reached the cluster, want 1", len(deletes))
	}
	return deletes[0]
}

func TestAJobAndACronJobDeleteReachTheAPIServer(t *testing.T) {
	for _, c := range []struct {
		kind, apiKind, name string
		gvr                 schema.GroupVersionResource
	}{
		{"jobs", "Job", "nightly-29895445", fakeJobGVR},
		{"cronjobs", "CronJob", "nightly", fakeCronJobGVR},
	} {
		h := newActsHarness(t, fakeBatch(c.apiKind, c.name))
		if outcome := deleteOne(t, h, c.kind, "batch-runs", c.name); outcome.Succeeded != 1 || outcome.Failed != 0 {
			t.Fatalf("%s: outcome %+v, want one success", c.kind, outcome)
		}
		sent := onlyDelete(t, h)
		if sent.GetResource() != c.gvr || sent.GetNamespace() != "batch-runs" || sent.GetName() != c.name {
			t.Errorf("%s: the delete went to %s %q/%q", c.kind, sent.GetResource(), sent.GetNamespace(), sent.GetName())
		}
		if _, err := h.dyn.Resource(c.gvr).Namespace("batch-runs").Get(context.Background(), c.name,
			metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Errorf("%s %s is still there after its delete: %v", c.kind, c.name, err)
		}
	}
}

// A node's delete reaches the API server too: the plan existed all along, and
// only the table withheld the verb.
func TestANodeDeleteReachesTheAPIServer(t *testing.T) {
	node := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Node",
		"metadata": map[string]any{"name": "worker-3", "uid": "uid-worker-3", "resourceVersion": "4"},
	}}
	h := newActsHarness(t, node)
	if outcome := deleteOne(t, h, "nodes", "", "worker-3"); outcome.Succeeded != 1 {
		t.Fatalf("outcome %+v, want one success", outcome)
	}
	sent := onlyDelete(t, h)
	if sent.GetResource() != fakeNodeGVR || sent.GetName() != "worker-3" {
		t.Errorf("the delete went to %s %q", sent.GetResource(), sent.GetName())
	}
}

// The sentence names the switch that grants the question: a shell is
// `rbac.exec`'s, a delete `rbac.write`'s.
func TestTheConsolesOwnRefusalNamesTheGrantThatWouldHelp(t *testing.T) {
	exec := selfReason(execAttributes("shop", "api-1"))
	if !strings.Contains(exec, "create on pods/exec in shop") || !strings.Contains(exec, "`rbac.exec`") {
		t.Errorf("a refused shell reads %q", exec)
	}
	remove := selfReason(access.Attributes{Verb: "delete", Group: "batch", Resource: "cronjobs", Namespace: "batch-runs"})
	if !strings.Contains(remove, "delete on cronjobs.batch in batch-runs") || !strings.Contains(remove, "`rbac.write`") {
		t.Errorf("a refused delete reads %q", remove)
	}
}

// The shared account's refused exec is the console's own refusal too: a pod's
// Shell is drawn disabled, naming `rbac.exec`, rather than withheld. A person
// signed in with single sign-on who may not exec still sees no Shell.
func TestTheSharedAccountsRefusedShellIsDrawnWithItsReason(t *testing.T) {
	noExec := grants(func(a access.Attributes) bool { return a.Subresource != "exec" })
	s := serverFor(t, true)
	s.gate = access.New(access.Options{Reviews: noExec.For, InCluster: true})
	pod := api.ObjectRef{Cluster: "prod", Kind: "pods", Namespace: "shop-prod", Name: "web-1"}

	offer := s.shellFor(sharedAccount(), pod)
	if offer == nil || offer.Permitted {
		t.Fatalf("the shared account's ServiceAccount may not exec: Shell is %+v, want drawn disabled", offer)
	}
	if offer.Reason == nil || offer.Reason.Code != api.CodeVerbDenied ||
		!strings.Contains(offer.Reason.Message, "ServiceAccount") || !strings.Contains(offer.Reason.Message, "`rbac.exec`") {
		t.Errorf("the reason is %+v, want the ServiceAccount's missing grant, rbac.exec", offer.Reason)
	}
	// bob may not exec in shop-prod, the console may: his own refusal, withheld.
	everyone := grants(consoleMayAll)
	s.gate = access.New(access.Options{Reviews: everyone.For, InCluster: true})
	if offer := s.shellFor(as(bob), pod); offer != nil {
		t.Errorf("bob may not exec: Shell is %+v, want withheld", offer)
	}
}

// On a list of every namespace, the shared account's refused Delete keeps the
// "choose a namespace" hint first - a namespace may answer otherwise - and
// then names the missing grant.
func TestAnAllNamespacesBarKeepsTheNamespaceHintForTheSharedAccount(t *testing.T) {
	refused := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return false, nil }}
	gate := access.New(access.Options{Reviews: refused.For, InCluster: true})
	verbs := []actions.Verb{actions.Delete}
	verdicts := acrossNamespaces(bulkScopes(true, nil), func(namespace string) map[actions.Verb]verdict {
		return verdictsAt(sharedAccount(), gate, jobsWhere, edit.Target{Cluster: "prod", Kind: "jobs", Namespace: namespace}, verbs)
	})
	bulk := gateBulk(bulkAffordance("prod", "jobs", false), acrossEveryNamespace(true, nil, verdicts))
	if bulk == nil || len(bulk.Verbs) != 1 || bulk.Verbs[0].Permitted || bulk.Verbs[0].Reason == nil {
		t.Fatalf("the Jobs bar of every namespace is %+v, want Delete disabled with a reason", bulk)
	}
	message := bulk.Verbs[0].Reason.Message
	if !strings.HasPrefix(message, everywhereReason) || !strings.Contains(message, "ServiceAccount") {
		t.Errorf("the reason reads %q; want the namespace hint first, then the missing grant", message)
	}
}

// A person signed in with single sign-on who may delete while the console's
// ServiceAccount may not keeps the console's sentence alone on a list of every
// namespace: the "choose a namespace" hint says "you may not", and they may.
func TestAnAllNamespacesBarDoesNotTellAPersonTheConsoleRefusesThatTheyMayNot(t *testing.T) {
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) { return !q.Self, nil }}
	gate := access.New(access.Options{Reviews: reviews.For, InCluster: true})
	verbs := []actions.Verb{actions.Delete}
	verdicts := acrossNamespaces(bulkScopes(true, nil), func(namespace string) map[actions.Verb]verdict {
		return verdictsAt(as(alice), gate, jobsWhere, edit.Target{Cluster: "prod", Kind: "jobs", Namespace: namespace}, verbs)
	})
	bulk := gateBulk(bulkAffordance("prod", "jobs", false), acrossEveryNamespace(true, nil, verdicts))
	if bulk == nil || len(bulk.Verbs) != 1 || bulk.Verbs[0].Permitted || bulk.Verbs[0].Reason == nil {
		t.Fatalf("alice's Jobs bar of every namespace is %+v, want Delete disabled with a reason", bulk)
	}
	message := bulk.Verbs[0].Reason.Message
	if strings.Contains(message, "you may not") || !strings.Contains(message, "this console may not") {
		t.Errorf("the reason reads %q; want the console's sentence alone", message)
	}
}

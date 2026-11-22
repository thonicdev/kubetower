package web

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/detail"
	"github.com/thonicdev/kubetower/internal/edit"
	"github.com/thonicdev/kubetower/internal/identity"
)

// A write control is offered only when the identity that will perform it may.
// The two surfaces - the object page's verbs and the selection bar's - are
// answered by the same review the write itself goes through.

var podsWhere = edit.Location{Cluster: "prod", Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Kind: "Pod", Namespaced: true}

func grace() context.Context {
	return identity.With(context.Background(), identity.Who{Name: "grace@example.com", Method: identity.SSO, Groups: []string{"oidc:viewers"}})
}

func verbNames(actions []api.DetailAction) []string {
	out := []string{}
	for _, action := range actions {
		out = append(out, action.Verb)
	}
	return out
}

// The bench's case: a read-only ServiceAccount drew Restart, Evict and
// Delete on every pod. Denied, each is withheld; granted one, that one appears.
func TestTheObjectPageWithholdsAVerbThePersonMayNotPerform(t *testing.T) {
	object := detail.Object{Kind: "pods", Relations: []detail.Relation{controlledBy("ReplicaSet")}}
	offered := objectActions("pods", object, true, "")
	target := edit.Target{Cluster: "prod", Kind: "pods", Namespace: "shop", Name: "api-1"}
	verbs := []actions.Verb{actions.Restart, actions.Evict, actions.Delete}

	none := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return false, nil }}
	gate := access.New(access.Options{Reviews: none.For, InCluster: true})
	if got := gateActions(offered, verdictsAt(grace(), gate, podsWhere, target, verbs)); len(got) != 0 {
		t.Errorf("a person with no write verbs was offered %v", verbNames(got))
	}

	// `delete pods` granted: Delete appears, and so does Restart, which on a
	// pod is the same request. Evict is its own subresource and stays away.
	deleteOnly := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		return q.Attributes.Verb == "delete" && q.Attributes.Subresource == "", nil
	}}
	gate = access.New(access.Options{Reviews: deleteOnly.For, InCluster: true})
	got := verbNames(gateActions(offered, verdictsAt(grace(), gate, podsWhere, target, verbs)))
	if strings.Join(got, ",") != "restart,delete" {
		t.Errorf("with delete pods granted the page offered %v, want restart and delete", got)
	}
}

// A review that could not be made draws the control **disabled, saying so**:
// not hidden, which would claim a no nobody gave, and not offered as working.
func TestAnUnknownAnswerIsDrawnAsUnknown(t *testing.T) {
	object := detail.Object{Kind: "pods", Relations: []detail.Relation{controlledBy("ReplicaSet")}}
	offered := objectActions("pods", object, true, "")
	target := edit.Target{Cluster: "prod", Kind: "pods", Namespace: "shop", Name: "api-1"}
	failing := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return false, errors.New("i/o timeout") }}
	gate := access.New(access.Options{Reviews: failing.For, InCluster: true})

	got := gateActions(offered, verdictsAt(grace(), gate, podsWhere, target,
		[]actions.Verb{actions.Restart, actions.Evict, actions.Delete}))
	if len(got) != len(offered) {
		t.Fatalf("an unknown answer hid controls: %v of %v", verbNames(got), verbNames(offered))
	}
	for _, action := range got {
		if action.Permitted {
			t.Errorf("%s is offered as working on an answer nobody got", action.Verb)
		}
		if action.Reason == nil || action.Reason.Code != api.CodeAccessUnreviewable ||
			!strings.Contains(action.Reason.Message, "could not ask") {
			t.Errorf("%s carries %+v, want the unreviewable sentence", action.Verb, action.Reason)
		}
	}
}

// The read-only guardrail keeps its own meaning and its own sentence, and a
// verb it already refuses is not asked about.
func TestTheGuardrailIsNotReplacedByTheReview(t *testing.T) {
	object := detail.Object{Kind: "pods", Relations: []detail.Relation{controlledBy("ReplicaSet")}}
	offered := objectActions("pods", object, false, "this cluster is marked read-only")
	reviews := &accesstest.Reviews{}
	s := &Server{gate: access.New(access.Options{Reviews: reviews.For, InCluster: true})}

	got := s.gatedActions(grace(), api.ObjectRef{Cluster: "prod", Kind: "pods", Namespace: "shop", Name: "api-1"}, offered)
	if len(got) != len(offered) {
		t.Fatalf("the guardrail's disabled verbs were changed: %v", verbNames(got))
	}
	for _, action := range got {
		if action.Permitted || action.Reason == nil || action.Reason.Code != api.CodeVerbDenied {
			t.Errorf("%s lost the guardrail's sentence: %+v", action.Verb, action.Reason)
		}
	}
	if n := len(reviews.Asked()); n != 0 {
		t.Errorf("%d reviews were asked for verbs the guardrail already refuses", n)
	}
}

// The desktop with a full-admin kubeconfig loses nothing: the review is the
// kubeconfig's own, and it says yes.
func TestAFullAdminDesktopLosesNothing(t *testing.T) {
	object := detail.Object{Kind: "pods", Relations: []detail.Relation{controlledBy("ReplicaSet")}}
	offered := objectActions("pods", object, true, "")
	admin := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) { return q.Self, nil }}
	target := edit.Target{Cluster: "prod", Kind: "pods", Namespace: "shop", Name: "api-1"}
	got := gateActions(offered, verdictsAt(context.Background(), admin.Gate(), podsWhere, target,
		[]actions.Verb{actions.Restart, actions.Evict, actions.Delete}))
	if strings.Join(verbNames(got), ",") != strings.Join(verbNames(offered), ",") {
		t.Errorf("the desktop drew %v of %v", verbNames(got), verbNames(offered))
	}
	for _, q := range admin.Asked() {
		if !q.Self {
			t.Errorf("the desktop asked a review naming %q; it is the kubeconfig's own", q.User)
		}
	}
}

// The selection bar: the same withholding, and a verb is offered only where it
// would work in every namespace the list shows.
func TestTheSelectionBarWithholdsWhatThePersonMayNotDo(t *testing.T) {
	offered := bulkAffordance("prod", "pods", false)
	if offered == nil {
		t.Fatal("precondition: pods offer bulk verbs")
	}
	shopOnly := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		return q.Attributes.Namespace == "shop" && q.Attributes.Verb == "delete", nil
	}}
	gate := access.New(access.Options{Reviews: shopOnly.For, InCluster: true})
	verbs := []actions.Verb{}
	for _, verb := range offered.Verbs {
		verbs = append(verbs, actions.Verb(verb.Verb))
	}
	combine := func(namespaces ...string) *api.BulkAffordance {
		return gateBulk(offered, acrossNamespaces(bulkScopes(true, namespaces), func(namespace string) map[actions.Verb]verdict {
			return verdictsAt(grace(), gate, podsWhere, edit.Target{Cluster: "prod", Kind: "pods", Namespace: namespace}, verbs)
		}))
	}

	shop := combine("shop")
	if shop == nil || len(shop.Verbs) != 1 || shop.Verbs[0].Verb != "delete" {
		t.Errorf("in shop the bar offered %+v, want delete alone", shop)
	}
	// shop and billing ticked together: delete would fail on billing's rows.
	// In either order: the answer is the worst of them, not the last one asked.
	for _, order := range [][]string{{"shop", "billing"}, {"billing", "shop"}} {
		if both := combine(order...); both != nil {
			t.Errorf("across %v the bar offered %+v; delete would be refused in billing", order, both.Verbs)
		}
	}
	// Every namespace at once is one question, cluster-wide: `-A`.
	if got := bulkScopes(true, nil); len(got) != 1 || got[0] != "" {
		t.Errorf("a list of every namespace asks about %q", got)
	}
	if got := bulkScopes(false, []string{"shop"}); len(got) != 1 || got[0] != "" {
		t.Errorf("a cluster-scoped kind asks about %q", got)
	}
}

// worse orders the answers the way a page must: a denial over an unknown, an
// unknown over an allowance.
func TestWorseIsTheAnswerThatStopsSooner(t *testing.T) {
	allowed, unknown, denied := verdict{decision: access.Allowed}, verdict{decision: access.Unreviewable}, verdict{decision: access.Denied}
	for _, tc := range []struct{ a, b, want verdict }{
		{verdict{}, allowed, allowed},
		{allowed, unknown, unknown},
		{unknown, allowed, unknown},
		{unknown, denied, denied},
		{denied, allowed, denied},
	} {
		if got := worse(tc.a, tc.b); got.decision != tc.want.decision {
			t.Errorf("worse(%s, %s) = %s, want %s", tc.a.decision, tc.b.decision, got.decision, tc.want.decision)
		}
	}
}

// A masked value is revealed only to a person who may `get` that object, by
// name: the console can read every Secret, and a Role granting one named
// Secret is exactly what a review by kind would over-grant.
func TestARevealIsReviewedForThePersonByName(t *testing.T) {
	secrets := edit.Location{Cluster: "prod", Resource: schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, Kind: "Secret", Namespaced: true}
	q := api.EnvQuery{ObjectRef: api.ObjectRef{Cluster: "prod", Kind: "secrets", Namespace: "shop", Name: "db"}, Key: "password"}
	scope := api.Scope{Cluster: "prod", Resource: "secrets", Namespace: "shop", Name: "db"}

	for _, tc := range []struct {
		name     string
		answer   func(accesstest.Question) (bool, error)
		decision access.Decision
		state    api.State
		code     api.ReasonCode
	}{
		{"allowed", func(accesstest.Question) (bool, error) { return true, nil }, access.Allowed, "", ""},
		{"denied", func(accesstest.Question) (bool, error) { return false, nil }, access.Denied, api.StateForbidden, api.CodeVerbDenied},
		{"unknown", func(accesstest.Question) (bool, error) { return false, errors.New("i/o timeout") },
			access.Unreviewable, api.StateUnreadable, api.CodeAccessUnreviewable},
	} {
		reviews := &accesstest.Reviews{Answer: tc.answer}
		gate := access.New(access.Options{Reviews: reviews.For, InCluster: true})
		decision, fail := revealVerdict(grace(), gate, secrets, q, scope)
		if decision != tc.decision {
			t.Errorf("%s: decision %s, want %s", tc.name, decision, tc.decision)
		}
		if tc.state == "" && fail != nil {
			t.Errorf("%s: refused %+v", tc.name, fail)
		}
		if tc.state != "" && (fail == nil || fail.State != tc.state || fail.Reason.Code != tc.code) {
			t.Errorf("%s: answered %+v, want %s/%s", tc.name, fail, tc.state, tc.code)
		}
		want := access.Attributes{Verb: "get", Version: "v1", Resource: "secrets", Namespace: "shop", Name: "db"}
		if asked := reviews.Asked(); len(asked) != 1 || asked[0].Attributes != want || asked[0].User != "grace@example.com" {
			t.Errorf("%s: the review asked %+v, want %+v for grace", tc.name, asked, want)
		}
	}
}

// A list of every namespace asks cluster-wide, and a person with a grant in
// some namespaces is denied that question. The verb is drawn disabled saying it
// depends on the namespace - not withheld as though they could do it nowhere.
// A cluster-scoped kind's denial is still a plain withholding.
func TestEveryNamespaceDrawsADenialAsDependingOnTheNamespace(t *testing.T) {
	offered := bulkAffordance("prod", "pods", false)
	denied := map[actions.Verb]verdict{}
	for _, verb := range offered.Verbs {
		denied[actions.Verb(verb.Verb)] = verdict{decision: access.Denied}
	}
	got := gateBulk(offered, acrossEveryNamespace(true, nil, denied))
	if got == nil || len(got.Verbs) != len(offered.Verbs) {
		t.Fatalf("on every namespace the denied verbs were withheld: %+v", got)
	}
	for _, verb := range got.Verbs {
		if verb.Permitted || verb.Reason == nil || verb.Reason.Message != everywhereReason {
			t.Errorf("%s is drawn %+v, want disabled with the per-namespace sentence", verb.Verb, verb.Reason)
		}
	}
	for verb := range denied {
		denied[verb] = verdict{decision: access.Denied}
	}
	if got := gateBulk(offered, acrossEveryNamespace(true, []string{"shop"}, denied)); got != nil {
		t.Errorf("in one namespace a denial is a withholding, and the bar offered %+v", got.Verbs)
	}
	for verb := range denied {
		denied[verb] = verdict{decision: access.Denied}
	}
	if got := gateBulk(offered, acrossEveryNamespace(false, nil, denied)); got != nil {
		t.Errorf("on a cluster-scoped kind a denial is a withholding, and the bar offered %+v", got.Verbs)
	}
}

// On the desktop an answer that could not be established is offered, with the
// note that the cluster decides - the write goes through there. Under the
// server profile the same answer is drawn disabled, because the write is refused.
func TestAnUnknownAnswerIsOfferedOnTheDesktopAndDisabledInACluster(t *testing.T) {
	object := detail.Object{Kind: "pods", Relations: []detail.Relation{controlledBy("ReplicaSet")}}
	offered := objectActions("pods", object, true, "")
	target := edit.Target{Cluster: "prod", Kind: "pods", Namespace: "shop", Name: "api-1"}
	verbs := []actions.Verb{actions.Restart, actions.Evict, actions.Delete}
	failing := func(accesstest.Question) (bool, error) {
		return false, errors.New("the authorization API is not served")
	}

	desktop := (&accesstest.Reviews{Answer: failing}).Gate()
	for _, action := range gateActions(offered, verdictsAt(context.Background(), desktop, podsWhere, target, verbs)) {
		if !action.Permitted || action.Reason == nil || !strings.Contains(action.Reason.Message, "the cluster decides") {
			t.Errorf("desktop: %s is drawn %+v, want offered with the note", action.Verb, action.Reason)
		}
	}
	bulk := bulkAffordance("prod", "pods", false)
	bulkVerbs := []actions.Verb{}
	for _, verb := range bulk.Verbs {
		bulkVerbs = append(bulkVerbs, actions.Verb(verb.Verb))
	}
	for _, verb := range gateBulk(bulk, verdictsAt(context.Background(), desktop, podsWhere, edit.Target{Cluster: "prod", Kind: "pods", Namespace: "shop"}, bulkVerbs)).Verbs {
		if !verb.Permitted {
			t.Errorf("desktop: the bar disabled %s", verb.Verb)
		}
	}

	server := access.New(access.Options{Reviews: (&accesstest.Reviews{Answer: failing}).For, InCluster: true})
	password := identity.With(context.Background(), identity.Who{Name: "admin", Method: identity.Password})
	for _, action := range gateActions(offered, verdictsAt(password, server, podsWhere, target, verbs)) {
		if action.Permitted {
			t.Errorf("server profile: %s is offered on an answer nobody got", action.Verb)
		}
	}
}

package web

import (
	"context"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/resources"
)

// What a selection of namespaces asks of the API server, and what it keeps.
//
// **One namespace is asked of the API server; several are asked of the whole
// cluster and filtered here.** client-go lists
// one namespace or all of them, so a selection of three is either three round
// trips or one — and the one is the request this page already makes by default,
// since "All namespaces" is where every list lands. Three concurrent listings
// would also have three ways to half-fail, and a table drawn from two namespaces
// out of the three that were asked for is an incomplete
// answer that reads as a complete one.
//
// **The selection must not reach the fetch verbatim.** `ListResources` must not hand
// `q.Namespace` to the fetch as the string the request spelled it: one name works, but two
// would arrive as the single namespace
// "argocd,longhorn-system", which no cluster has, so the page would come back empty
// and say *"there are none here"* about a perfectly valid selection — the
// reassuring wrong answer. Against a live cluster, `?namespace=argocd` gives 5
// events, `?namespace=argocd&namespace=longhorn-system` gives 8, which is 5 and 3.

// askedPath drives the **real** `ListResources` and reads back the path it put
// to the API server.
//
// The fixture's kubeconfig names a host that does not resolve, so the read fails
// — and the failure carries the URL, which is the one observable that says what
// was asked. That is deliberately not a restatement of the rule under test: a
// helper that recomputed "one or none" here would agree with the production code
// by construction and stay green against the defect, which is what
// `columnsempty_test.go` says about writing `headingRoom` out by hand.
func askedPath(t *testing.T, namespace string) string {
	t.Helper()
	server := listPageFixture(t)
	_, fail := apiReads{server}.ListResources(context.Background(),
		api.ListQuery{Cluster: "prod", Kind: "events", Namespace: namespace})
	if fail == nil {
		t.Fatalf("the fixture's cluster answered, which it cannot: no URL to read back")
	}
	message := fail.Reason.Message
	at := strings.Index(message, "/api/v1/")
	if at < 0 {
		t.Fatalf("no request path in %q", message)
	}
	end := strings.IndexByte(message[at:], '?')
	if end < 0 {
		end = len(message) - at
	}
	return message[at : at+end]
}

func TestOneNamespaceIsAskedOfTheClusterAndSeveralAreNot(t *testing.T) {
	// One: the API server does the narrowing, which is one round trip for the
	// answer the operator wants.
	if got := askedPath(t, "argocd"); got != "/api/v1/namespaces/argocd/events" {
		t.Errorf("one namespace asked for %q", got)
	}

	// Several: the whole cluster, once. Anything with a comma in it is a
	// selection being posted as a namespace name, which is the defect.
	got := askedPath(t, "argocd,longhorn-system")
	if strings.Contains(got, ",") {
		t.Errorf("a selection was asked of the API server as one namespace: %q — no cluster "+
			"has a namespace by that name, so the list comes back empty and claims "+
			"there are none there", got)
	}
	if got != "/api/v1/events" {
		t.Errorf("two namespaces asked for %q, want the cluster-wide listing", got)
	}

	// None: everything, and that is deliberately not `default`.
	if got := askedPath(t, ""); got != "/api/v1/events" {
		t.Errorf("no namespace asked for %q", got)
	}

	// The canonical form is the same question however it is spelled, which is
	// what lets a client and a server agree on one cache key.
	if a, b := askedPath(t, "argocd,"), askedPath(t, " argocd "); a != b ||
		a != "/api/v1/namespaces/argocd/events" {
		t.Errorf("`argocd,` asked for %q and ` argocd ` for %q", a, b)
	}
}

// And the half that happens after the answer: the rows kept are the rows in the
// selection, and a cluster-scoped row is in none of them.
func TestASelectionKeepsTheRowsInItAndNoOthers(t *testing.T) {
	rows := []resources.Row{
		{Name: "a", Namespace: "argocd"},
		{Name: "b", Namespace: "longhorn-system"},
		{Name: "c", Namespace: "kube-system"},
		// A Node has no namespace. `rowsInNamespaces` must not treat it as being
		// in every one: a row whose namespace is empty is not in any, so a
		// namespace filter excludes it.
		{Name: "d", Namespace: ""},
	}

	kept := func(selection []string) []string {
		out := []string{}
		for _, row := range rowsInNamespaces(rows, selection) {
			out = append(out, row.Name)
		}
		return out
	}

	if got := kept([]string{"argocd", "longhorn-system"}); strings.Join(got, ",") != "a,b" {
		t.Errorf("a selection of two kept %v, want [a b]", got)
	}
	if got := kept([]string{"argocd"}); strings.Join(got, ",") != "a" {
		t.Errorf("a selection of one kept %v", got)
	}
	// An empty selection is not "keep nothing": absent means every namespace.
	if got := kept(nil); len(got) != 4 {
		t.Errorf("no selection kept %v, want every row including the cluster-scoped one", got)
	}
	if got := kept([]string{"nosuchns"}); len(got) != 0 {
		t.Errorf("a namespace with nothing in it kept %v", got)
	}
}

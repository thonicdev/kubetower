package web

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/customresources"
)

// TestOverviewLinksSpellTheListsParameters: the overview's links to a list
// must carry `namespace` and `search`, the names the list page reads (the SPA's
// `useUrlList` and `FILTER_PARAMETERS`, and `listResources` on the API), not
// `ns` and `q`. Otherwise
// `/pods?ns=shop-demo` opens every pod in the cluster with "All
// namespaces" in the picker, and `&open=` draws the drawer over that.
//
// Asserted on the parsed query rather than on a string, so the order the
// parameters are encoded in is not part of the contract - and the retired names are
// asserted absent, because a link that carried both would pass the first half
// and still teach a reader the dead spelling.
func TestOverviewLinksSpellTheListsParameters(t *testing.T) {
	cases := map[string]struct {
		href                    string
		namespace, search, open string
	}{
		"a namespace cell": {scopeLink("in-cluster", "Pod", "shop-demo").Href, "shop-demo", "", ""},
		"a count":          {listLink("in-cluster", "Pod", "platform", "3", "").Href, "platform", "", ""},
		"one object": {
			objectLink("in-cluster", "Pod", "platform", "memory-hog-7547689c8c-sg44p", "memory-hog", "").Href,
			"platform", "memory-hog-7547689c8c-sg44p", "memory-hog-7547689c8c-sg44p",
		},
	}
	for name, c := range cases {
		parsed, err := url.Parse(c.href)
		if err != nil || c.href == "" {
			t.Fatalf("%s: %q does not parse (%v)", name, c.href, err)
		}
		query := parsed.Query()
		got := fmt.Sprintf("namespace=%q search=%q open=%q", query.Get("namespace"), query.Get("search"), query.Get("open"))
		want := fmt.Sprintf("namespace=%q search=%q open=%q", c.namespace, c.search, c.open)
		if got != want {
			t.Errorf("%s: %s reads as %s, want %s", name, c.href, got, want)
		}
		for _, dead := range []string{"ns", "q"} {
			if query.Has(dead) {
				t.Errorf("%s: %s still carries %q, which nothing reads", name, c.href, dead)
			}
		}
	}
}

// TestAKindTheClusterDoesNotHaveIsAbsentNotUnreachable covers
// `/c/in-cluster/settings`, which must not draw "The cluster did not
// answer" under a healthy cluster. The catalog answers - no CRD by that name -
// and its sentence matches none of the not-found words, so it must not fall to the
// unreachable default. A not-found is `absent`, with no retry: asking again
// gets the same answer.
func TestAKindTheClusterDoesNotHaveIsAbsentNotUnreachable(t *testing.T) {
	scope := api.Scope{Cluster: "in-cluster", Resource: "settings", Verb: "list"}
	for name, err := range map[string]error{
		"no CRD by that name":  fmt.Errorf("%w %q", customresources.ErrUnknownKind, "settings"),
		"no version serves it": fmt.Errorf("%w: %q", customresources.ErrNotServed, "widgets"),
	} {
		failure := stateOf(err, scope)
		if failure == nil {
			t.Fatalf("%s: no failure", name)
		}
		if failure.State != api.StateAbsent || failure.Reason.Code != api.CodeNoSuchKind {
			t.Errorf("%s: answered %s / %s, want absent / %s", name, failure.State, failure.Reason.Code, api.CodeNoSuchKind)
		}
		if failure.Reason.Retryable != nil && *failure.Reason.Retryable {
			t.Errorf("%s: retryable, and asking again gets the same answer", name)
		}
	}
}

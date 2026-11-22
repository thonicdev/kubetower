package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/customresources"
	"github.com/thonicdev/kubetower/internal/resources"
)

// The in-cluster menu leaves out a kind the ServiceAccount may list nowhere,
// and only that kind - and what it costs to know is bounded.

// clock is a settable clock for menuAccess.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// listableServer is a Server with the gate answering through decide, the
// console's namespace hold holding names - or failed, where names is nil - and
// the pod's own namespace "kubetower".
func listableServer(t *testing.T, inCluster bool, names []string, decide func(accesstest.Question) (bool, error)) (*Server, *accesstest.Reviews, *clock) {
	t.Helper()
	reviews := &accesstest.Reviews{Answer: decide}
	c := &clock{at: time.Now()}
	m := newMenuAccess()
	m.now, m.own = c.now, func() string { return "kubetower" }
	s := &Server{
		inCluster: inCluster,
		// The gate on the same clock, so a test can let its own held answers
		// expire and see what the menu's hold saves.
		gate:       access.New(access.Options{Reviews: reviews.For, InCluster: inCluster, Now: c.now}),
		namespaces: newNamespaceCache(),
		menuAccess: m,
	}
	now := time.Now()
	s.namespaces.held["prod"] = namespaceHold{names: names, at: now, readAt: now, failed: names == nil}
	t.Cleanup(m.renewing.Wait)
	return s, reviews, c
}

var (
	argoApps    = resources.InNamespace("argoproj.io", "v1alpha1", "applications", "")
	deployhooks = resources.ClusterScoped("demo.example.com", "v1", "deployhooks")
)

// allowIn lets a list of resource go in the namespaces named - "" is every
// namespace - and denies everything else.
func allowIn(resource string, namespaces ...string) func(accesstest.Question) (bool, error) {
	return func(q accesstest.Question) (bool, error) {
		if q.Attributes.Resource != resource {
			return false, nil
		}
		for _, ns := range namespaces {
			if q.Attributes.Namespace == ns {
				return true, nil
			}
		}
		return false, nil
	}
}

func namespaces(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("ns-%03d", i)
	}
	return out
}

func TestTheMenuKeepsEveryKindOffTheInClusterProfile(t *testing.T) {
	s, reviews, _ := listableServer(t, false, []string{"default"}, allowIn("nothing"))
	keep := s.listable(context.Background(), "prod", []resources.Question{argoApps, deployhooks})
	if !keep[0] || !keep[1] {
		t.Errorf("the desktop left a kind out: %v", keep)
	}
	if n := len(reviews.Asked()); n != 0 {
		t.Errorf("the desktop asked %d reviews for its menu; it asks none", n)
	}
}

func TestTheInClusterMenuKeepsAKindListableAcrossTheCluster(t *testing.T) {
	s, reviews, _ := listableServer(t, true, []string{"default"}, allowIn("applications", ""))
	keep := s.listable(context.Background(), "prod", []resources.Question{argoApps})
	if !keep[0] {
		t.Error("a kind the ServiceAccount may list everywhere was left out")
	}
	asked := reviews.Asked()
	if len(asked) != 1 || !asked[0].Self || asked[0].Attributes.Verb != "list" || asked[0].Attributes.Namespace != "" {
		t.Errorf("asked %+v; want one self review of list across every namespace", asked)
	}
}

func TestTheInClusterMenuLeavesOutAClusterScopedKindItMayNotList(t *testing.T) {
	s, _, _ := listableServer(t, true, []string{"default"}, allowIn("nothing"))
	if keep := s.listable(context.Background(), "prod", []resources.Question{deployhooks}); keep[0] {
		t.Error("a cluster-scoped kind refused to the ServiceAccount stayed in the menu")
	}
}

// RoleBindings are namespaced: refused across the cluster is not refused.
func TestTheInClusterMenuKeepsAKindRefusedClusterWideButAllowedInOneNamespace(t *testing.T) {
	names := []string{"argocd", "default", "kube-system", "shop", "a", "b", "c", "d", "e", "f"}
	s, _, _ := listableServer(t, true, names, allowIn("applications", "shop"))
	if keep := s.listable(context.Background(), "prod", []resources.Question{argoApps}); !keep[0] {
		t.Error("a kind the ServiceAccount may list in shop was left out of the menu")
	}
}

func TestTheInClusterMenuLeavesOutANamespacedKindRefusedInEveryNamespace(t *testing.T) {
	names := []string{"argocd", "default", "kube-system"}
	s, reviews, _ := listableServer(t, true, names, allowIn("nothing"))
	if keep := s.listable(context.Background(), "prod", []resources.Question{argoApps}); keep[0] {
		t.Error("a kind refused in every namespace stayed in the menu")
	}
	// Across the cluster, then each namespace once.
	if n := len(reviews.Asked()); n != 1+len(names) {
		t.Errorf("asked %d reviews; want %d", n, 1+len(names))
	}
}

// The pod's own namespace is asked first: the namespace a console's role is
// most often granted in, and the cheapest way out of a walk.
func TestTheInClusterMenuAsksTheConsolesOwnNamespaceFirst(t *testing.T) {
	names := append(namespaces(20), "kubetower")
	s, reviews, _ := listableServer(t, true, names, allowIn("applications", "kubetower"))
	if keep := s.listable(context.Background(), "prod", []resources.Question{argoApps}); !keep[0] {
		t.Fatal("a kind granted in the console's own namespace was left out")
	}
	asked := reviews.Asked()
	if len(asked) < 2 || asked[1].Attributes.Namespace != "kubetower" {
		t.Fatalf("asked %+v; want the console's own namespace right after the cluster-wide question", asked)
	}
	// Alone, so nothing else is asked once it allows.
	if len(asked) != 2 {
		t.Errorf("asked %d reviews; the console's own namespace held the answer", len(asked))
	}
}

// An unknown answer is not a refusal: every way of not knowing keeps the kind.
func TestTheInClusterMenuKeepsAKindItCouldNotAskAbout(t *testing.T) {
	failing := func(accesstest.Question) (bool, error) { return false, errors.New("connection refused") }
	for _, tc := range []struct {
		name   string
		names  []string
		decide func(accesstest.Question) (bool, error)
	}{
		{"the review across the cluster failed", []string{"default"}, failing},
		{"the namespaces could not be read", nil, allowIn("nothing")},
		{"the namespace reviews failed", []string{"default", "shop"}, func(q accesstest.Question) (bool, error) {
			if q.Attributes.Namespace == "" {
				return false, nil
			}
			return false, errors.New("connection refused")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := listableServer(t, true, tc.names, tc.decide)
			keep := s.listable(context.Background(), "prod", []resources.Question{argoApps, deployhooks})
			if !keep[0] {
				t.Error("the namespaced kind was left out on an answer nobody gave")
			}
			if tc.name == "the review across the cluster failed" && !keep[1] {
				t.Error("the cluster-scoped kind was left out on an answer nobody gave")
			}
		})
	}
}

// A walk the budget does not cover is not started: the kind is kept without
// a per-namespace review, rather than asked part of the way and kept anyway.
func TestTheInClusterMenuStartsNoWalkItsBudgetDoesNotCover(t *testing.T) {
	s, reviews, _ := listableServer(t, true, namespaces(narrowReviewBudget+1), allowIn("nothing"))
	if keep := s.listable(context.Background(), "prod", []resources.Question{argoApps}); !keep[0] {
		t.Error("a kind not settled within the budget was left out")
	}
	if n := len(reviews.Asked()); n != 1 {
		t.Errorf("asked %d reviews; want the cluster-wide one only", n)
	}
}

// One budget for the menu: the built-in kinds' walks and the CRDs' draw on it
// together.
func TestTheInClusterMenusBudgetIsSharedByTheBuiltInKindsAndTheCRDs(t *testing.T) {
	names := namespaces(40)
	s, reviews, _ := listableServer(t, true, names, func(q accesstest.Question) (bool, error) {
		return q.Attributes.Resource != "secrets" && q.Attributes.Resource != "applications", nil
	})
	if hidden := s.unlistedKinds(context.Background(), "prod"); !hidden["secrets"] {
		t.Fatalf("left out %v; the first walk fits the budget and should settle secrets", hidden)
	}
	before := len(reviews.Asked())
	kept, left := s.listableDefinitions(context.Background(), "prod", []customresources.Definition{
		{Group: "argoproj.io", Version: "v1alpha1", Plural: "applications", Kind: "Application", Namespaced: true, Served: true},
	})
	if len(kept) != 1 || left != 0 {
		t.Errorf("kept %v, left out %d: the second walk is past what the first left of the budget", kept, left)
	}
	if n := len(reviews.Asked()) - before; n != 1 {
		t.Errorf("the CRD asked %d reviews; want the cluster-wide one only", n)
	}
}

// A request that is over asks nothing more.
func TestTheInClusterMenuAsksNothingForARequestThatIsOver(t *testing.T) {
	s, reviews, _ := listableServer(t, true, namespaces(10), allowIn("nothing"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if keep := s.listable(ctx, "prod", []resources.Question{argoApps, deployhooks}); !keep[0] || !keep[1] {
		t.Errorf("a request that was over left kinds out: %v", keep)
	}
	if n := len(reviews.Asked()); n != 0 {
		t.Errorf("a request that was over asked %d reviews", n)
	}
}

// Held: a second draw asks nothing, and one past the renewal age is served what
// is held and renewed behind the caller.
func TestTheInClusterMenuServesItsVerdictsAndRenewsThemBehindTheCaller(t *testing.T) {
	s, reviews, c := listableServer(t, true, []string{"default"}, allowIn("nothing"))
	questions := []resources.Question{deployhooks}
	if keep := s.listable(context.Background(), "prod", questions); keep[0] {
		t.Fatal("precondition: the refused kind is left out")
	}
	first := len(reviews.Asked())
	// Past the gate's own minute, so what is saved is the menu's hold.
	c.advance(2 * access.TTL)
	if keep := s.listable(context.Background(), "prod", questions); keep[0] {
		t.Error("the held verdict was not served")
	}
	if n := len(reviews.Asked()); n != first {
		t.Errorf("a second draw asked %d more reviews; it asks none", n-first)
	}

	c.advance(menuAccessRenew)
	if keep := s.listable(context.Background(), "prod", questions); keep[0] {
		t.Error("a verdict due for renewal was not served while it is renewed")
	}
	s.menuAccess.renewing.Wait()
	// Renewed: the gate's own hold has the answer for its minute, so the
	// renewal reached the gate - and the verdict is fresh again.
	s.menuAccess.mu.Lock()
	v := s.menuAccess.slot("prod").verdicts[wideOf(deployhooks)]
	s.menuAccess.mu.Unlock()
	if !v.at.Equal(c.now()) || v.keep {
		t.Errorf("after its renewal the verdict is %+v at %s; want left out, filed now", v, c.now())
	}
}

// A scenario at scale: 300 namespaces, Secrets refused, 200
// namespaced CRDs of which 10 have no grant. What the menu asks is bounded by
// what this file says it asks - one review per kind across the cluster, and
// narrowReviewBudget per namespace - cold, and nothing at all warm.
func TestTheInClusterMenuOnAClusterOfThreeHundredNamespacesAsksWhatItSays(t *testing.T) {
	names := namespaces(300)
	ungranted := map[string]bool{}
	definitions := make([]customresources.Definition, 200)
	for i := range definitions {
		plural := fmt.Sprintf("kind%03ds", i)
		definitions[i] = customresources.Definition{
			Group: "example.io", Version: "v1", Plural: plural, Kind: fmt.Sprintf("Kind%03d", i),
			Namespaced: true, Served: true,
		}
		if i%20 == 0 {
			ungranted[plural] = true
		}
	}
	s, reviews, c := listableServer(t, true, names, func(q accesstest.Question) (bool, error) {
		return q.Attributes.Resource != "secrets" && !ungranted[q.Attributes.Resource], nil
	})
	builtIn := len(resources.Kinds())
	ceiling := builtIn + len(definitions) + narrowReviewBudget

	s.unlistedKinds(context.Background(), "prod")
	s.listableDefinitions(context.Background(), "prod", definitions)
	cold := len(reviews.Asked())
	perNamespace := 0
	for _, q := range reviews.Asked() {
		if q.Attributes.Namespace != "" {
			perNamespace++
		}
	}
	t.Logf("cold: %d reviews (%d per namespace); ceiling %d", cold, perNamespace, ceiling)
	if cold > ceiling {
		t.Errorf("a cold menu asked %d reviews; the ceiling is %d", cold, ceiling)
	}
	if perNamespace > narrowReviewBudget {
		t.Errorf("a cold menu asked %d per-namespace reviews; the budget is %d", perNamespace, narrowReviewBudget)
	}

	// Warm: a draw past the gate's own minute and before the menu's renewal.
	// What the renewal of the kinds left unsettled asks is the cluster-wide
	// question alone - the walks are still past the budget.
	c.advance(2 * access.TTL)
	s.unlistedKinds(context.Background(), "prod")
	s.listableDefinitions(context.Background(), "prod", definitions)
	s.menuAccess.renewing.Wait()
	warm := len(reviews.Asked()) - cold
	// The unsettled ones: Secrets, and the ten CRDs with no grant.
	unsettled := 1 + len(ungranted)
	t.Logf("warm: %d reviews, all behind the caller (ceiling %d: one per unsettled kind)", warm, unsettled)
	if warm > unsettled {
		t.Errorf("a warm menu asked %d reviews of the cluster; at most %d, one per kind it could not settle", warm, unsettled)
	}
}

func TestTheInClusterMenuLeavesOutTheBuiltInKindsItMayNotList(t *testing.T) {
	// Everything but secrets, which a chart with that switch off does not grant.
	s, _, _ := listableServer(t, true, []string{"default"}, func(q accesstest.Question) (bool, error) {
		return q.Attributes.Resource != "secrets", nil
	})
	hidden := s.unlistedKinds(context.Background(), "prod")
	if len(hidden) != 1 || !hidden["secrets"] {
		t.Fatalf("left out %v; want secrets alone", hidden)
	}
	_, groups := s.kindNav("prod", "", hidden)
	for _, group := range groups {
		for _, link := range group.Links {
			if strings.HasSuffix(link.Href, "/secrets") {
				t.Errorf("the menu still links %s", link.Href)
			}
		}
	}
}

func TestTheInClusterMenuCountsTheCustomKindsItLeftOut(t *testing.T) {
	s, _, _ := listableServer(t, true, []string{"default"}, allowIn("certificates", ""))
	definitions := []customresources.Definition{
		{Group: "cert-manager.io", Version: "v1", Plural: "certificates", Kind: "Certificate", Namespaced: true, Served: true},
		{Group: "argoproj.io", Version: "v1alpha1", Plural: "applications", Kind: "Application", Namespaced: true, Served: true},
	}
	kept, left := s.listableDefinitions(context.Background(), "prod", definitions)
	if len(kept) != 1 || kept[0].Plural != "certificates" || left != 1 {
		t.Errorf("kept %v and left out %d; want certificates, and one left out", kept, left)
	}
	if got := leftOutNote(1); got != "One kind this console may not list is not shown: "+
		"the identity it reads with has to be granted the read on it." {
		t.Errorf("the note for one is %q", got)
	}
	if got := leftOutNote(4); got != "4 kinds this console may not list are not shown: "+
		"the identity it reads with has to be granted the read on them." {
		t.Errorf("the note for four is %q", got)
	}
}

// The kinds left out are said in the access note, which the menu draws
// first and in the warning tone - not in the quiet line under the API
// groups where it is easily missed - and the sentence says the read has to
// be granted, in words true on the desktop as in a cluster: no chart value.
func TestTheCustomResourceHeadingSaysWhatItLeftOutInItsAccessNote(t *testing.T) {
	kept := []customresources.Definition{
		{Group: "argoproj.io", Version: "v1alpha1", Plural: "applications", Kind: "Application", Namespaced: true, Served: true},
	}
	cases := []struct {
		what        string
		served      int
		kept        []customresources.Definition
		left        int
		access      string
		note        string
		withEntries bool
	}{
		{"three of four left out", 4, kept, 3,
			"3 kinds this console may not list are not shown: the identity it reads with has to be granted the read on them.",
			"", true},
		{"every kind refused", 4, nil, 4,
			"This console may list none of the custom resource kinds this cluster serves: " +
				"the identity it reads with has to be granted the read on them.",
			"", false},
		{"nothing left out", 1, kept, 0, "", "", true},
		{"a cluster serving none", 0, nil, 0, "", "This cluster serves no custom resource kinds.", false},
	}
	for _, c := range cases {
		group := navGroupOf(customNavGroup("prod", "", c.served, c.kept, c.left))
		if group.AccessNote != c.access {
			t.Errorf("%s: the access note is %q; want %q", c.what, group.AccessNote, c.access)
		}
		if group.Note != c.note {
			t.Errorf("%s: the note is %q; want %q", c.what, group.Note, c.note)
		}
		if got := len(group.Groups) > 0; got != c.withEntries {
			t.Errorf("%s: drawn with entries %v; want %v", c.what, got, c.withEntries)
		}
		for _, chart := range []string{"rbac", "customResources", "values", "Helm", "helm"} {
			if strings.Contains(group.AccessNote, chart) {
				t.Errorf("%s: the access note names %q, which the desktop has no use for: %q", c.what, chart, group.AccessNote)
			}
		}
	}
}

// The API's menu - which the browser's menu and its jump box both draw from -
// carries no entry for a kind left out, and every other one.
func TestTheInClusterAPIMenuOffersNoKindTheServiceAccountMayNotList(t *testing.T) {
	server := navFixture(t, "prod")
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		return q.Attributes.Resource != "secrets", nil
	}}
	server.inCluster = true
	server.gate = access.New(access.Options{Reviews: reviews.For, InCluster: true})
	now := time.Now()
	server.namespaces.mu.Lock()
	server.namespaces.held["prod"] = namespaceHold{names: []string{"default"}, at: now, readAt: now}
	server.namespaces.mu.Unlock()
	t.Cleanup(server.menuAccess.renewing.Wait)

	menu, fail := apiClusters{server}.Nav(context.Background(), "prod")
	if fail != nil {
		t.Fatalf("the menu failed: %+v", fail.Reason)
	}
	var hrefs []string
	for _, group := range menu.Groups {
		for _, item := range group.Items {
			hrefs = append(hrefs, item.Href)
		}
	}
	joined := strings.Join(hrefs, " ")
	if strings.Contains(joined, "/c/prod/secrets") {
		t.Errorf("the in-cluster menu links Secrets, which the ServiceAccount may list nowhere: %s", joined)
	}
	if !strings.Contains(joined, "/c/prod/configmaps") {
		t.Errorf("the in-cluster menu lost ConfigMaps, which it may list: %s", joined)
	}
}

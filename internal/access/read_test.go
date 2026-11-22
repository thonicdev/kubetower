package access_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/identity"
)

// The read half of the choke point: what a person reads is
// reviewed as they are, through the same gate and the same held answers as a
// write.

// row is a listed object: where it lives and what it is called.
type row struct{ namespace, name string }

func rowNamespace(r *row) string { return r.namespace }

// clusterRows is what the console's own credentials read across every
// namespace - the list access.List narrows.
var clusterRows = []row{
	{"shop", "api-1"}, {"shop", "api-2"}, {"kube-system", "coredns"}, {"billing", "ledger"},
}

var listPods = access.Attributes{Verb: "list", Version: "v1", Resource: "pods"}

// readers is a cluster where alice's group may list pods in `shop` only, bob
// may read nothing, and carol may list pods everywhere. Answered the way the
// API server answers a SubjectAccessReview against such RoleBindings.
func readers(q accesstest.Question) (bool, error) {
	a := q.Attributes
	for _, g := range q.Groups {
		switch g {
		case "oidc:shop-admins":
			if a.Resource == "pods" && a.Verb == "list" && a.Namespace == "shop" {
				return true, nil
			}
		case "oidc:platform":
			if a.Resource == "pods" && a.Verb == "list" {
				return true, nil
			}
		}
	}
	return false, nil
}

func carol() context.Context {
	return identity.With(context.Background(), identity.Who{Name: "carol@example.com", Method: identity.SSO, Groups: []string{"oidc:platform"}})
}

// readCount counts the reads a door ran.
type readCount struct{ n int }

func (c *readCount) read(rows []row) func(context.Context) ([]row, error) {
	return func(context.Context) ([]row, error) {
		c.n++
		return rows, nil
	}
}

// **The acceptance criterion, both directions.** A list of every namespace
// shows each person exactly the namespaces the cluster lets them list: alice
// the rows of `shop` and nothing else, bob nothing, carol everything - and
// each answer is their own, asked in either order.
func TestAListOfEveryNamespaceShowsEachPersonTheirOwnNamespaces(t *testing.T) {
	g := inCluster(&accesstest.Reviews{Answer: readers}, nil)
	for _, tc := range []struct {
		who      string
		ctx      context.Context
		want     []row
		narrowed bool
	}{
		{"alice", alice(), []row{{"shop", "api-1"}, {"shop", "api-2"}}, true},
		{"bob", bob(), []row{}, true},
		{"carol", carol(), clusterRows, false},
		// Again, after the others: a held answer is never another person's.
		{"alice again", alice(), []row{{"shop", "api-1"}, {"shop", "api-2"}}, true},
	} {
		t.Run(tc.who, func(t *testing.T) {
			var reads readCount
			got, seen, err := access.List(tc.ctx, g, "prod", listPods, rowNamespace, reads.read(clusterRows))
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("rows = %v, want %v", got, tc.want)
			}
			if seen.Narrowed != tc.narrowed {
				t.Errorf("Narrowed = %v, want %v: a narrowed list and a whole one must say which they are", seen.Narrowed, tc.narrowed)
			}
			if reads.n != 1 {
				t.Errorf("the list was read %d times, want once", reads.n)
			}
		})
	}
}

// The questions a narrowed list asks: one for the whole list, then one per
// namespace the rows are in - and never about a namespace the rows are not in,
// which would be the console asking questions about namespaces nobody showed.
func TestANarrowedListAsksOncePerNamespaceItHolds(t *testing.T) {
	r := &accesstest.Reviews{Answer: readers}
	g := inCluster(r, nil)
	if _, _, err := access.List(alice(), g, "prod", listPods, rowNamespace, (&readCount{}).read(clusterRows)); err != nil {
		t.Fatal(err)
	}
	var asked []string
	for _, q := range r.Asked() {
		asked = append(asked, q.Attributes.Namespace)
	}
	want := map[string]bool{"": true, "shop": true, "kube-system": true, "billing": true}
	if len(asked) != len(want) {
		t.Fatalf("asked about %q, want the whole list and each of its three namespaces once", asked)
	}
	for _, ns := range asked {
		if !want[ns] {
			t.Errorf("asked about namespace %q, which no row is in", ns)
		}
	}
}

// A list narrowed for one person must not rewrite what another is served: the
// rows handed in are shared (overview's podShare hands one read to several
// callers).
func TestNarrowingDoesNotRewriteTheRowsItWasHanded(t *testing.T) {
	g := inCluster(&accesstest.Reviews{Answer: readers}, nil)
	shared := append([]row(nil), clusterRows...)
	if _, _, err := access.List(alice(), g, "prod", listPods, rowNamespace, (&readCount{}).read(shared)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(shared, clusterRows) {
		t.Errorf("the shared rows were rewritten to %v", shared)
	}
}

// One namespace asked and denied is a refusal - a denial the API server would
// give - never an empty list, and the list is not read at all.
func TestAListOfOneNamespaceTheyMayNotListIsRefusedUnread(t *testing.T) {
	g := inCluster(&accesstest.Reviews{Answer: readers}, nil)
	question := listPods
	question.Namespace = "billing"
	var reads readCount
	got, _, err := access.List(alice(), g, "prod", question, rowNamespace, reads.read(clusterRows))
	if !errors.Is(err, access.ErrDenied) {
		t.Fatalf("err = %v, rows = %v; want a denial", err, got)
	}
	if reads.n != 0 {
		t.Errorf("the list was read %d times before a refusal", reads.n)
	}
	if !strings.Contains(err.Error(), "list pods") || !strings.Contains(err.Error(), "billing") {
		t.Errorf("the refusal does not say what was refused: %v", err)
	}
}

// A cluster-scoped kind has no namespaces to narrow to: the denial is the
// answer.
func TestAClusterScopedListTheyMayNotMakeIsRefused(t *testing.T) {
	g := inCluster(&accesstest.Reviews{Answer: readers}, nil)
	nodes := access.Attributes{Verb: "list", Version: "v1", Resource: "nodes"}
	var reads readCount
	if _, _, err := access.List(alice(), g, "prod", nodes, nil, reads.read([]row{{"", "node-a"}})); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("err = %v, want a denial", err)
	}
	if reads.n != 0 {
		t.Errorf("a refused cluster-scoped list was read")
	}
}

// Under the server profile a list nobody could review is refused, with its own
// error - not narrowed to "the namespaces whose review happened to work", and
// never read as a denial.
func TestAnUnreviewableListIsRefusedUnderTheServerProfile(t *testing.T) {
	r := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return false, errors.New("the API server did not answer") }}
	g := inCluster(r, nil)
	var reads readCount
	_, _, err := access.List(alice(), g, "prod", listPods, rowNamespace, reads.read(clusterRows))
	if !errors.Is(err, access.ErrUnreviewable) || errors.Is(err, access.ErrDenied) {
		t.Fatalf("err = %v, want unreviewable and not a denial", err)
	}
	if reads.n != 0 {
		t.Errorf("an unreviewable list was read")
	}
}

// Gate.LetsThrough, kept for reads: on the desktop a review of the console's own
// credentials that could not be made lets the read through - the API server is
// about to check that same identity itself.
func TestOnTheDesktopAnUnreviewableReadGoesThrough(t *testing.T) {
	r := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return false, errors.New("no SelfSubjectAccessReview here") }}
	g := access.New(access.Options{Reviews: r.For})
	var reads readCount
	got, seen, err := access.List(context.Background(), g, "laptop", listPods, rowNamespace, reads.read(clusterRows))
	if err != nil || !reflect.DeepEqual(got, clusterRows) || seen.Filtered() {
		t.Fatalf("rows = %v, seen = %+v, err = %v; want the list as read", got, seen, err)
	}
	if _, err := g.Read(context.Background(), "laptop", access.Attributes{Verb: "get", Version: "v1", Resource: "pods", Namespace: "shop", Name: "api-1"},
		func(context.Context) error { return nil }); err != nil {
		t.Errorf("Read refused on the desktop: %v", err)
	}
}

// And a denial still refuses on the desktop: LetsThrough relaxes "could not
// ask", never "no".
func TestOnTheDesktopADeniedReadIsStillRefused(t *testing.T) {
	r := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return false, nil }}
	g := access.New(access.Options{Reviews: r.For})
	ran := false
	_, err := g.Read(context.Background(), "laptop", access.Attributes{Verb: "get", Version: "v1", Resource: "secrets", Namespace: "shop", Name: "db"},
		func(context.Context) error { ran = true; return nil })
	if !errors.Is(err, access.ErrDenied) || ran {
		t.Fatalf("err = %v, ran = %v; want a denial and no read", err, ran)
	}
}

// **An object read by name is reviewed with its name**, so a Role granting
// `get` on one named Secret opens that Secret and refuses its neighbor
// (RBAC's `resourceNames` applies to `get`;
// docs/SERVER-MODE-THREAT-MODEL.md §5.3).
func TestAnObjectReadByNameIsReviewedWithItsName(t *testing.T) {
	oneSecret := func(q accesstest.Question) (bool, error) {
		a := q.Attributes
		return a.Verb == "get" && a.Resource == "secrets" && a.Namespace == "shop" && a.Name == "db-prod", nil
	}
	r := &accesstest.Reviews{Answer: oneSecret}
	g := inCluster(r, nil)
	get := func(name string) error {
		_, err := g.Read(alice(), "prod", access.Attributes{Verb: "get", Version: "v1", Resource: "secrets", Namespace: "shop", Name: name},
			func(context.Context) error { return nil })
		return err
	}
	if err := get("db-prod"); err != nil {
		t.Errorf("the named Secret was refused: %v", err)
	}
	if err := get("db-staging"); !errors.Is(err, access.ErrDenied) {
		t.Errorf("its neighbor was not refused: %v", err)
	}
	for _, q := range r.Asked() {
		if q.Attributes.Name == "" {
			t.Errorf("a read by name was reviewed without the name: %+v", q.Attributes)
		}
	}
}

// A write handed to a read door is a programming error, refused and never
// reviewed as though it were a read.
func TestAReadDoorRefusesAWriteVerb(t *testing.T) {
	r := &accesstest.Reviews{}
	g := inCluster(r, nil)
	ran := false
	if _, err := g.Read(alice(), "prod", deletePods, func(context.Context) error { ran = true; return nil }); err == nil || ran {
		t.Fatalf("a delete went through Read: err = %v, ran = %v", err, ran)
	}
	if _, _, err := access.List(alice(), g, "prod", deletePods, rowNamespace, (&readCount{}).read(nil)); err == nil {
		t.Fatal("a delete went through List")
	}
	if len(r.Asked()) != 0 {
		t.Errorf("a write verb was reviewed through a read door: %+v", r.Asked())
	}
}

// Keep is the same filter for rows that were not read through a client - a
// ranking out of Prometheus names pods too.
func TestKeepNarrowsRowsThatCameFromElsewhere(t *testing.T) {
	g := inCluster(&accesstest.Reviews{Answer: readers}, nil)
	got, seen, err := access.Keep(alice(), g, "prod", listPods, clusterRows, rowNamespace)
	if err != nil {
		t.Fatal(err)
	}
	if want := []row{{"shop", "api-1"}, {"shop", "api-2"}}; !reflect.DeepEqual(got, want) || !seen.Narrowed {
		t.Errorf("rows = %v (narrowed %v), want %v narrowed", got, seen.Narrowed, want)
	}
	if got, seen, _ := access.Keep(carol(), g, "prod", listPods, clusterRows, rowNamespace); !reflect.DeepEqual(got, clusterRows) || seen.Filtered() {
		t.Errorf("carol, who may list pods everywhere, got %v (%+v)", got, seen)
	}
}

// Each hands the read a keep that answers per namespace, so a walk that stops
// at a byte budget still counts every row the cluster sent.
func TestEachHandsTheReadAKeepPerNamespace(t *testing.T) {
	g := inCluster(&accesstest.Reviews{Answer: readers}, nil)
	var visited, shown []row
	seen, err := access.Each(alice(), g, "prod", listPods, rowNamespace, func(_ context.Context, keep func(*row) bool) error {
		for i := range clusterRows {
			visited = append(visited, clusterRows[i])
			if keep(&clusterRows[i]) {
				shown = append(shown, clusterRows[i])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(visited) != len(clusterRows) {
		t.Errorf("visited %d rows, want all %d the cluster sent", len(visited), len(clusterRows))
	}
	if want := []row{{"shop", "api-1"}, {"shop", "api-2"}}; !reflect.DeepEqual(shown, want) || !seen.Narrowed {
		t.Errorf("shown %v (narrowed %v), want %v narrowed", shown, seen.Narrowed, want)
	}
}

// **AsConsole reviews as the console whoever's request it runs in**: a hold
// every person is served from is filled with the console's rights, never with
// the first person's.
func TestAsConsoleIsReviewedAsTheConsoleEvenInsideAPersonsRequest(t *testing.T) {
	r := &accesstest.Reviews{}
	g := inCluster(r, nil)
	if got := g.Review(access.AsConsole(bob()), "prod", listPods); got.Subject != access.Console {
		t.Fatalf("answered for %s, want the console", got.Subject)
	}
	asked := r.Asked()
	if len(asked) != 1 || !asked[0].Self {
		t.Errorf("AsConsole was not a self review: %+v", asked)
	}
	// And without it, the same context is bob's.
	if got := g.Review(bob(), "prod", listPods); got.Subject != access.Person {
		t.Errorf("bob's own request was answered for %s", got.Subject)
	}
}

// Check is the review alone: nil when it lets the request go, a refusal of the
// right kind when it does not.
func TestCheckAnswersAsReadWouldRefuse(t *testing.T) {
	g := inCluster(&accesstest.Reviews{Answer: readers}, nil)
	shop := listPods
	shop.Namespace = "shop"
	if err := g.Check(alice(), "prod", shop); err != nil {
		t.Errorf("alice may list pods in shop: %v", err)
	}
	if err := g.Check(bob(), "prod", shop); !errors.Is(err, access.ErrDenied) {
		t.Errorf("bob may not, and Check said %v", err)
	}
}

// A person's refusal carries the API server's own status, so every reader that
// already tells a 403 from an outage reads it as what it is.
func TestARefusalCarriesTheStatusTheAPIServerWouldHaveSent(t *testing.T) {
	denied := &access.Refused{Cluster: "prod", Attributes: listPods, Answer: access.Answer{Decision: access.Denied, Subject: access.Person}}
	if s := denied.Status(); s.Code != 403 || s.Reason != "Forbidden" || s.Message != denied.Error() {
		t.Errorf("a denial's status is %+v, want 403 Forbidden with its own sentence", s)
	}
	unknown := &access.Refused{Cluster: "prod", Attributes: listPods, Answer: access.Answer{Decision: access.Unreviewable, Subject: access.Person}}
	if s := unknown.Status(); s.Code != 503 {
		t.Errorf("an unreviewable refusal's status is %+v, want 503", s)
	}
}

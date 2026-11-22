package access_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/identity"
)

var deletePods = access.Attributes{Verb: "delete", Version: "v1", Resource: "pods", Namespace: "shop", Name: "api-1"}

func alice() context.Context {
	return identity.With(context.Background(), identity.Who{Name: "alice@example.com", Method: identity.SSO, Groups: []string{"oidc:shop-admins"}})
}

func bob() context.Context {
	return identity.With(context.Background(), identity.Who{Name: "bob@example.com", Method: identity.SSO, Groups: []string{"oidc:viewers"}})
}

// rbac is a two-person cluster: alice's group may delete pods in `shop` and
// nowhere else; bob may do nothing. The same shape as the bench's two
// RoleBindings, answered the way the API server would answer them.
func rbac(q accesstest.Question) (bool, error) {
	for _, g := range q.Groups {
		if g == "oidc:shop-admins" && q.Attributes.Namespace == "shop" && q.Attributes.Resource == "pods" {
			return true, nil
		}
	}
	return false, nil
}

// inCluster is a server-profile gate, with the audit log that profile cannot
// act without, written nowhere: record_test.go reads the records.
func inCluster(r *accesstest.Reviews, now func() time.Time) *access.Gate {
	return access.New(access.Options{Reviews: r.For, InCluster: true, Now: now, Audit: audit.New(io.Discard)})
}

// A person is reviewed as themselves: a SubjectAccessReview naming their user
// and groups - and `system:authenticated`, which kube-apiserver adds to every
// signed-in user - never a self review, which would be the ServiceAccount's
// rights.
func TestAPersonIsReviewedAsThemselvesWithTheirGroups(t *testing.T) {
	r := &accesstest.Reviews{Answer: rbac}
	g := inCluster(r, nil)

	if got := g.Review(alice(), "prod", deletePods); got.Decision != access.Allowed || got.Subject != access.Person {
		t.Fatalf("alice, whose group may delete pods in shop, was answered %+v", got)
	}
	asked := r.Asked()
	if len(asked) != 1 || asked[0].Self {
		t.Fatalf("the person was reviewed as the console: %+v", asked)
	}
	want := accesstest.Question{Cluster: "prod", User: "alice@example.com",
		Groups: []string{"oidc:shop-admins", "system:authenticated"}, Attributes: deletePods}
	if !reflect.DeepEqual(asked[0], want) {
		t.Errorf("the review asked %+v, want %+v", asked[0], want)
	}
}

// Both directions: the same question about two people
// gets each person's own answer, and the answer held for one is never served to
// the other.
func TestTwoPeopleGetTheirOwnAnswersInBothDirections(t *testing.T) {
	r := &accesstest.Reviews{Answer: rbac}
	g := inCluster(r, nil)

	if got := g.Review(alice(), "prod", deletePods); !got.Allowed() {
		t.Errorf("alice may delete pods in shop and was answered %s", got.Decision)
	}
	// Asked second, while alice's yes is held: a cache keyed without the
	// identity would hand it to bob here.
	if got := g.Review(bob(), "prod", deletePods); got.Decision != access.Denied {
		t.Errorf("bob may not delete pods anywhere and was answered %s: somebody else's answer was served", got.Decision)
	}
	elsewhere := deletePods
	elsewhere.Namespace = "billing"
	if got := g.Review(alice(), "prod", elsewhere); got.Decision != access.Denied {
		t.Errorf("alice may not delete pods in billing and was answered %s", got.Decision)
	}
	if n := len(r.Asked()); n != 3 {
		t.Errorf("%d reviews were made for three different questions", n)
	}
}

// The shared password account, and the desktop, are the console's own rights:
// a self review, naming nobody.
func TestTheSharedAccountAndTheDesktopAreTheConsolesOwnRights(t *testing.T) {
	password := identity.With(context.Background(), identity.Who{Name: "admin", Method: identity.Password})
	for _, tc := range []struct {
		name      string
		ctx       context.Context
		inCluster bool
	}{
		{"the password account in a cluster", password, true},
		{"the password account on a desktop", password, false},
		{"nobody, on a desktop", context.Background(), false},
	} {
		r := &accesstest.Reviews{}
		g := access.New(access.Options{Reviews: r.For, InCluster: tc.inCluster})
		got := g.Review(tc.ctx, "prod", deletePods)
		asked := r.Asked()
		if got.Subject != access.Console || len(asked) != 1 || !asked[0].Self || asked[0].User != "" {
			t.Errorf("%s: answered %+v after asking %+v; want one self review", tc.name, got, asked)
		}
	}
}

// Under the server profile a request nobody is behind is refused without
// asking: reviewing it as the console would hand the ServiceAccount's rights to
// whatever path lost its context.
func TestNobodyIsRefusedInAClusterWithoutAsking(t *testing.T) {
	r := &accesstest.Reviews{}
	g := inCluster(r, nil)
	got := g.Review(context.Background(), "prod", deletePods)
	if got.Decision != access.Unreviewable || got.Subject != access.Nobody {
		t.Errorf("an unattributed request in a cluster was answered %+v", got)
	}
	if n := len(r.Asked()); n != 0 {
		t.Errorf("%d reviews were made for a request nobody is behind", n)
	}
}

// A review that could not be made refuses, and is never a denial.
func TestAFailedReviewRefusesAndSaysItWasNotADenial(t *testing.T) {
	r := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) {
		return false, errors.New("dial tcp 10.0.0.1:443: i/o timeout")
	}}
	g := inCluster(r, nil)
	var ran bool
	answer, err := g.Perform(alice(), "prod", deletePods, func(context.Context) error { ran = true; return nil })
	if ran {
		t.Fatal("the write ran although no review could be made")
	}
	if answer.Decision != access.Unreviewable {
		t.Errorf("a failed review was answered %s", answer.Decision)
	}
	if !errors.Is(err, access.ErrUnreviewable) || errors.Is(err, access.ErrDenied) {
		t.Errorf("the refusal %v does not say the review could not be made, or says it was denied", err)
	}
	if !strings.Contains(err.Error(), "could not ask") || !strings.Contains(err.Error(), "i/o timeout") {
		t.Errorf("the sentence does not say what failed: %q", err)
	}
}

// Perform runs the write on an allowed answer and on nothing else.
func TestPerformRunsTheWriteOnlyWhenAllowed(t *testing.T) {
	r := &accesstest.Reviews{Answer: rbac}
	g := inCluster(r, nil)

	var ran int
	do := func(context.Context) error { ran++; return nil }
	if _, err := g.Perform(alice(), "prod", deletePods, do); err != nil || ran != 1 {
		t.Fatalf("an allowed write answered %v and ran %d times", err, ran)
	}
	_, err := g.Perform(bob(), "prod", deletePods, do)
	if ran != 1 {
		t.Fatal("a denied write ran")
	}
	if !errors.Is(err, access.ErrDenied) || !strings.Contains(err.Error(), "may not delete pods api-1 in namespace shop") {
		t.Errorf("the denial reads %q", err)
	}
	// The write's own failure comes back as itself, not as a refusal.
	boom := errors.New("the API server said no to the body")
	if _, err := g.Perform(alice(), "prod", deletePods, func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("the write's own error came back as %v", err)
	}
}

// The minute is a revocation delay: an answer is reused inside it and asked
// again after it; a failure is held ten seconds, not a minute.
func TestAnAnswerIsHeldAMinuteAndAFailureTenSeconds(t *testing.T) {
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	var fail atomic.Bool
	r := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		if fail.Load() {
			return false, errors.New("unreachable")
		}
		return rbac(q)
	}}
	g := inCluster(r, clock)

	g.Review(alice(), "prod", deletePods)
	now = now.Add(access.TTL - time.Second)
	g.Review(alice(), "prod", deletePods)
	if n := len(r.Asked()); n != 1 {
		t.Fatalf("%d reviews inside the minute; the answer was not held", n)
	}
	now = now.Add(2 * time.Second)
	g.Review(alice(), "prod", deletePods)
	if n := len(r.Asked()); n != 2 {
		t.Fatalf("%d reviews after the minute; a held answer outlived its revocation delay", n)
	}

	fail.Store(true)
	now = now.Add(access.TTL + time.Second)
	if got := g.Review(alice(), "prod", deletePods); got.Decision != access.Unreviewable {
		t.Fatalf("a failing review answered %s", got.Decision)
	}
	fail.Store(false)
	now = now.Add(access.FailedTTL - time.Second)
	if got := g.Review(alice(), "prod", deletePods); got.Decision != access.Unreviewable {
		t.Errorf("the failure was not held for its ten seconds: %s", got.Decision)
	}
	now = now.Add(2 * time.Second)
	if got := g.Review(alice(), "prod", deletePods); !got.Allowed() {
		t.Errorf("the failure was held past its ten seconds: %s", got.Decision)
	}
}

// A new group set is a new identity for the held answers; the same set in
// another order is not.
func TestTheGroupsArePartOfWhoIsAsking(t *testing.T) {
	r := &accesstest.Reviews{Answer: rbac}
	g := inCluster(r, nil)
	ask := func(groups ...string) access.Answer {
		ctx := identity.With(context.Background(), identity.Who{Name: "carol@example.com", Method: identity.SSO, Groups: groups})
		return g.Review(ctx, "prod", deletePods)
	}
	if got := ask("oidc:shop-admins", "oidc:oncall"); !got.Allowed() {
		t.Fatalf("carol in shop-admins was answered %s", got.Decision)
	}
	ask("oidc:oncall", "oidc:shop-admins")
	if n := len(r.Asked()); n != 1 {
		t.Errorf("the same groups in another order were asked again (%d reviews)", n)
	}
	// Removed from the group at the provider and signed in again.
	if got := ask("oidc:oncall"); got.Decision != access.Denied {
		t.Errorf("carol without shop-admins was answered %s: the held answer outlived the group", got.Decision)
	}
}

// A caller that went away mid-review does not poison the answer for everybody.
func TestACancelledCallerDoesNotDecideForEverybody(t *testing.T) {
	r := &accesstest.Reviews{Answer: rbac}
	g := inCluster(r, nil)
	ctx, cancel := context.WithCancel(alice())
	cancel()
	if got := g.Review(ctx, "prod", deletePods); !got.Allowed() {
		t.Errorf("a canceled caller was answered %+v; the review took its cancellation", got)
	}
}

// Nil and unconfigured gates refuse everything: the fail-closed default, never a
// way to run without a review.
func TestAGateWithNothingBehindItRefuses(t *testing.T) {
	var nothing *access.Gate
	for name, g := range map[string]*access.Gate{"nil": nothing, "no reviews": access.New(access.Options{})} {
		ran := false
		answer, err := g.Perform(alice(), "prod", deletePods, func(context.Context) error { ran = true; return nil })
		if ran || answer.Decision != access.Unreviewable || !errors.Is(err, access.ErrUnreviewable) {
			t.Errorf("%s: ran=%t answer=%+v err=%v", name, ran, answer, err)
		}
	}
}

// Several questions are asked together and answered in order.
func TestReviewAllAnswersInTheOrderAsked(t *testing.T) {
	r := &accesstest.Reviews{Answer: rbac}
	g := inCluster(r, nil)
	other := deletePods
	other.Namespace = "billing"
	got := g.ReviewAll(alice(), "prod", []access.Attributes{deletePods, other, deletePods})
	if got[0].Decision != access.Allowed || got[1].Decision != access.Denied || got[2].Decision != access.Allowed {
		t.Errorf("the answers came back as %s, %s, %s", got[0].Decision, got[1].Decision, got[2].Decision)
	}
}

// A failed review is said once per cluster and message, with the reason last,
// and refused every time.
func TestAFailedReviewIsSaidOnce(t *testing.T) {
	var lines []string
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	r := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return false, errors.New("connection refused") }}
	g := access.New(access.Options{Reviews: r.For, InCluster: true, Now: func() time.Time { return now },
		Logf: func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }})
	g.Review(alice(), "prod", deletePods)
	now = now.Add(access.FailedTTL)
	g.Review(alice(), "prod", deletePods)
	want := `could not ask cluster "prod" whether a request may be made, so it is refused: connection refused`
	if len(lines) != 1 || lines[0] != want {
		t.Errorf("logged %q, want exactly [%q]", lines, want)
	}
}

// The desktop: a review of the console's own credentials that could not be
// made lets the request through - the API server checks that same identity -
// and says so; a denial still refuses. Everywhere else, an unreviewable answer
// refuses.
func TestOnlyTheDesktopsOwnCredentialsGoThroughAnUnreviewableAnswer(t *testing.T) {
	failing := func(accesstest.Question) (bool, error) {
		return false, errors.New("the authorization API is not served")
	}
	password := identity.With(context.Background(), identity.Who{Name: "admin", Method: identity.Password})
	for _, tc := range []struct {
		name      string
		inCluster bool
		ctx       context.Context
		answer    func(accesstest.Question) (bool, error)
		runs      bool
	}{
		{"desktop, the kubeconfig's own identity, review unreviewable", false, context.Background(), failing, true},
		{"desktop, the shared password account, review unreviewable", false, password, failing, true},
		{"desktop, the kubeconfig's own identity, denied", false, context.Background(), func(accesstest.Question) (bool, error) { return false, nil }, false},
		{"server profile, the shared password account, review unreviewable", true, password, failing, false},
		{"server profile, a person, review unreviewable", true, alice(), failing, false},
		{"desktop, a person, review unreviewable", false, alice(), failing, false},
	} {
		r := &accesstest.Reviews{Answer: tc.answer}
		g := access.New(access.Options{Reviews: r.For, InCluster: tc.inCluster})
		ran := false
		answer, err := g.Perform(tc.ctx, "prod", deletePods, func(context.Context) error { ran = true; return nil })
		if ran != tc.runs {
			t.Errorf("%s: ran=%t, want %t (answer %+v, err %v)", tc.name, ran, tc.runs, answer, err)
		}
		if tc.runs && (err != nil || answer.Decision != access.Unreviewable) {
			t.Errorf("%s: let through with err=%v decision=%s; want no error and the decision said as unreviewable", tc.name, err, answer.Decision)
		}
	}
	var nothing *access.Gate
	if nothing.LetsThrough(access.Answer{Decision: access.Unreviewable, Subject: access.Console}) {
		t.Error("a nil gate lets a request through")
	}
	if _, err := access.New(access.Options{}).Perform(context.Background(), "prod", deletePods, func(context.Context) error { return nil }); err == nil {
		t.Error("a desktop gate with nothing behind it let a request through")
	}
}

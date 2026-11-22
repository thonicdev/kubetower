package access

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"

	"github.com/thonicdev/kubetower/internal/identity"
)

// counting answers every review allowed, counts them, and can be held until
// released so a test can put several callers in the air at once.
type counting struct {
	asked   atomic.Int64
	release chan struct{}
}

func (c *counting) wait() {
	if c.release != nil {
		<-c.release
	}
}

func (c *counting) SubjectAccessReview(_ context.Context, r *authorizationv1.SubjectAccessReview) (*authorizationv1.SubjectAccessReview, error) {
	c.asked.Add(1)
	c.wait()
	out := r.DeepCopy()
	out.Status.Allowed = true
	return out, nil
}

func (c *counting) SelfSubjectAccessReview(_ context.Context, r *authorizationv1.SelfSubjectAccessReview) (*authorizationv1.SelfSubjectAccessReview, error) {
	c.asked.Add(1)
	c.wait()
	out := r.DeepCopy()
	out.Status.Allowed = true
	return out, nil
}

func person(name string) context.Context {
	return identity.With(context.Background(), identity.Who{Name: name, Method: identity.SSO, Groups: []string{"oidc:dev"}})
}

// The held answers are bounded. The key carries the object's name, which comes
// from the request, so a person asking about made-up names must not grow the
// map for the life of the process.
func TestTheHeldAnswersNeverOutgrowTheirBound(t *testing.T) {
	c := &counting{}
	g := New(Options{Reviews: func(string) (Reviews, error) { return c, nil }, InCluster: true})
	g.limit = 100
	for i := 0; i < 1000; i++ {
		g.Review(person("mallory@example.com"), "prod", Attributes{Verb: "get", Resource: "secrets", Namespace: "shop", Name: fmt.Sprintf("made-up-%d", i)})
		if n := len(g.held); n > g.limit {
			t.Fatalf("after %d reviews %d answers are held, past the bound of %d", i+1, n, g.limit)
		}
	}
}

// Expired answers are swept as the map is used, not only when it is full.
func TestExpiredAnswersAreSwept(t *testing.T) {
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	c := &counting{}
	g := New(Options{Reviews: func(string) (Reviews, error) { return c, nil }, InCluster: true, Now: func() time.Time { return now }})
	for i := 0; i < sweepEvery-1; i++ {
		g.Review(person("a@example.com"), "prod", Attributes{Verb: "get", Resource: "pods", Name: fmt.Sprint(i)})
	}
	now = now.Add(TTL + time.Second)
	g.Review(person("a@example.com"), "prod", Attributes{Verb: "get", Resource: "pods", Name: "fresh"})
	if n := len(g.held); n != 1 {
		t.Errorf("%d answers held after every other one expired and a sweep ran; want the fresh one alone", n)
	}
}

// The same question asked many times at once is asked of the API server once.
func TestTheSameQuestionInTheAirIsAskedOnce(t *testing.T) {
	c := &counting{release: make(chan struct{})}
	g := New(Options{Reviews: func(string) (Reviews, error) { return c, nil }, InCluster: true})
	q := Attributes{Verb: "delete", Resource: "pods", Namespace: "shop", Name: "api-1"}

	var wg sync.WaitGroup
	answers := make([]Answer, 20)
	for i := range answers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			answers[i] = g.Review(person("a@example.com"), "prod", q)
		}(i)
	}
	// Every caller is waiting on the one review that is held in the air.
	deadline := time.Now().Add(5 * time.Second)
	for c.asked.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(c.release)
	wg.Wait()
	if n := c.asked.Load(); n != 1 {
		t.Errorf("twenty identical questions at once were asked %d times", n)
	}
	for i, a := range answers {
		if !a.Allowed() {
			t.Errorf("caller %d was answered %+v", i, a)
		}
	}
}

// ReviewAll asks a repeated question once and answers it in every place - by
// the sharing of a question in the air and the held answers, not by a
// de-duplication of its own.
func TestReviewAllAsksARepeatedQuestionOnce(t *testing.T) {
	c := &counting{}
	g := New(Options{Reviews: func(string) (Reviews, error) { return c, nil }, InCluster: true})
	del := Attributes{Verb: "delete", Resource: "pods", Namespace: "shop", Name: "api-1"}
	evict := Attributes{Verb: "create", Resource: "pods", Subresource: "eviction", Namespace: "shop", Name: "api-1"}
	got := g.ReviewAll(person("a@example.com"), "prod", []Attributes{del, evict, del})
	if n := c.asked.Load(); n != 2 {
		t.Errorf("three questions, two distinct, were asked %d times", n)
	}
	if len(got) != 3 || !got[2].Allowed() {
		t.Errorf("the repeated question was not answered in its place: %+v", got)
	}
}

// Two different people must never share a key, however their names are spelt.
// A separator-joined key would let a name carry the separator and collide.
func TestNoSpellingOfANameCollidesWithAnotherPerson(t *testing.T) {
	a := subjectKey(Person, "eve\x00oidc:admins", nil)
	b := subjectKey(Person, "eve", []string{"oidc:admins"})
	if a == b {
		t.Errorf("a name holding the separator shares a key with another person: %q", a)
	}
	if subjectKey(Person, "a", []string{"x", "y"}) != subjectKey(Person, "a", []string{"y", "x"}) {
		t.Error("the same groups in another order are a different key")
	}
}

// A status that says both allowed and denied is a denial, and a denial whose
// only explanation is an evaluation error carries it.
func TestADeniedStatusIsADenialWhateverElseItSays(t *testing.T) {
	if got := answerOf(authorizationv1.SubjectAccessReviewStatus{Allowed: true, Denied: true}, Person); got.Decision != Denied {
		t.Errorf("allowed+denied read as %s", got.Decision)
	}
	got := answerOf(authorizationv1.SubjectAccessReviewStatus{EvaluationError: "webhook authorizer timed out"}, Person)
	if got.Decision != Denied || got.Reason != "webhook authorizer timed out" {
		t.Errorf("a denial with only an evaluation error read as %+v", got)
	}
	if got := answerOf(authorizationv1.SubjectAccessReviewStatus{Allowed: true}, Console); got.Decision != Allowed || got.Subject != Console {
		t.Errorf("an allowance read as %+v", got)
	}
}

// A caller waiting on somebody else's review keeps its own deadline: gone, it
// is answered unreviewable at once, and the review it was waiting on still
// lands for everybody else.
func TestAWaiterOnAReviewInTheAirKeepsItsOwnDeadline(t *testing.T) {
	c := &counting{release: make(chan struct{})}
	g := New(Options{Reviews: func(string) (Reviews, error) { return c, nil }, InCluster: true})
	q := Attributes{Verb: "delete", Resource: "pods", Namespace: "shop", Name: "api-1"}

	first := make(chan Answer, 1)
	go func() { first <- g.Review(person("a@example.com"), "prod", q) }()
	deadline := time.Now().Add(5 * time.Second)
	for c.asked.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	ctx, cancel := context.WithCancel(person("a@example.com"))
	waited := make(chan Answer, 1)
	go func() { waited <- g.Review(ctx, "prod", q) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case a := <-waited:
		if a.Decision != Unreviewable {
			t.Errorf("a waiter whose context ended was answered %+v", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a waiter whose context ended was still held by somebody else's review")
	}
	close(c.release)
	if a := <-first; !a.Allowed() {
		t.Errorf("the review in the air was answered %+v", a)
	}
}

// A review that panics still lands its flight, so a caller already waiting on
// that question is not left waiting forever - and the next one asks afresh.
func TestAPanickingReviewDoesNotStrandItsWaiters(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int64
	g := New(Options{Reviews: func(string) (Reviews, error) {
		if calls.Add(1) == 1 {
			<-release
			panic("the review client blew up")
		}
		return &counting{}, nil
	}, InCluster: true})
	q := Attributes{Verb: "delete", Resource: "pods", Namespace: "shop", Name: "api-1"}

	go func() {
		defer func() { _ = recover() }()
		g.Review(person("a@example.com"), "prod", q)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	waiter := make(chan Answer, 1)
	go func() { waiter <- g.Review(person("a@example.com"), "prod", q) }()
	time.Sleep(20 * time.Millisecond)
	close(release)
	select {
	case a := <-waiter:
		if a.Decision != Unreviewable {
			t.Errorf("a waiter on a review that panicked was answered %+v", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a waiter was stranded on the flight of a review that panicked")
	}
	if a := g.Review(person("a@example.com"), "prod", q); !a.Allowed() {
		t.Errorf("the next review after a panic was answered %+v", a)
	}
}

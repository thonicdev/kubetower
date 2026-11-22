// Package accesstest answers access reviews without a cluster, for tests.
//
// Tests only. A Gate built here answers from a function rather than an API
// server, which in production would be the choke point deciding for itself.
// The package's structural test refuses an import of it from non-test files.
package accesstest

import (
	"context"
	"sync"

	authorizationv1 "k8s.io/api/authorization/v1"

	"github.com/thonicdev/kubetower/internal/access"
)

// Question is one review as it reached the fake: whose, and about what.
type Question struct {
	Cluster string
	// Self is a SelfSubjectAccessReview - the console's own rights. False is a
	// SubjectAccessReview naming User and Groups.
	Self       bool
	User       string
	Groups     []string
	Attributes access.Attributes
}

// Reviews answers every review through Answer, and remembers what it was asked.
type Reviews struct {
	// Answer decides. Nil allows everything.
	Answer func(Question) (bool, error)

	mu    sync.Mutex
	asked []Question
}

// Asked is every question so far, in order.
func (r *Reviews) Asked() []Question {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Question(nil), r.asked...)
}

// For is the lookup a Gate takes.
func (r *Reviews) For(cluster string) (access.Reviews, error) {
	return bound{r: r, cluster: cluster}, nil
}

type bound struct {
	r       *Reviews
	cluster string
}

func (b bound) decide(q Question) (bool, error) {
	b.r.mu.Lock()
	b.r.asked = append(b.r.asked, q)
	answer := b.r.Answer
	b.r.mu.Unlock()
	if answer == nil {
		return true, nil
	}
	return answer(q)
}

func (b bound) SubjectAccessReview(ctx context.Context, review *authorizationv1.SubjectAccessReview) (*authorizationv1.SubjectAccessReview, error) {
	// Refused before anything is asked, as client-go refuses it, so a test can
	// see whether the gate hands a caller's cancellation to the review.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	allowed, err := b.decide(Question{
		Cluster: b.cluster, User: review.Spec.User,
		Groups: append([]string(nil), review.Spec.Groups...), Attributes: attributesOf(review.Spec.ResourceAttributes),
	})
	if err != nil {
		return nil, err
	}
	out := review.DeepCopy()
	out.Status.Allowed = allowed
	return out, nil
}

func (b bound) SelfSubjectAccessReview(ctx context.Context, review *authorizationv1.SelfSubjectAccessReview) (*authorizationv1.SelfSubjectAccessReview, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	allowed, err := b.decide(Question{Cluster: b.cluster, Self: true, Attributes: attributesOf(review.Spec.ResourceAttributes)})
	if err != nil {
		return nil, err
	}
	out := review.DeepCopy()
	out.Status.Allowed = allowed
	return out, nil
}

func attributesOf(a *authorizationv1.ResourceAttributes) access.Attributes {
	if a == nil {
		return access.Attributes{}
	}
	return access.Attributes{
		Verb: a.Verb, Group: a.Group, Version: a.Version, Resource: a.Resource,
		Subresource: a.Subresource, Namespace: a.Namespace, Name: a.Name,
	}
}

// Gate is a Gate answering through r, on the desktop profile.
func (r *Reviews) Gate() *access.Gate {
	return access.New(access.Options{Reviews: r.For})
}

// Allow is a Gate that allows everything - what a test of some other part of
// the write path wants from the choke point.
func Allow() *access.Gate { return (&Reviews{}).Gate() }

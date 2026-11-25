// Package access is the one place this console asks a cluster whether the
// person behind a request may do what they asked.
//
// # Why it exists
//
// Under the server profile the console acts with **one ServiceAccount**, and
// that account can do far more than any one person signed in to it. So before
// anything that changes a cluster, the console asks the API server - through a
// `SubjectAccessReview` naming the person's user and groups - whether *they*
// may, and refuses when they may not. The API server evaluates its own RBAC;
// nothing here maps a group to a permission, and nothing here should.
//
// **Never impersonation.** A review names the person in its body; the request
// that follows is the ServiceAccount's own. Impersonation would need
// `impersonate` on users and groups, which with dynamic identities cannot be
// scoped by `resourceNames` and so reaches `system:masters`. A review needs
// `create` on `subjectaccessreviews` and nothing else: the ServiceAccount can
// become nobody.
//
// # One door, and a test that there is no second one
//
// Every request that can change a cluster leaves this binary inside
// Gate.Perform: the review is made, and only an `allowed` answer runs the
// closure that makes the request. **Every read a person is shown leaves it
// inside Gate.Read or List** (read.go), which ask the same question with a read
// verb. `choke_test.go` walks the tree with go/ast and fails on a write-capable
// or read-capable client call anywhere else. A behavioral test of this
// file proves the door works; only the walk proves there is no other door.
// **And transport.go holds it at runtime** for what the walk cannot see -
// another module, a closure whose question is not its request: every cluster
// client sends through Guard, which refuses a change Perform did not stamp.
//
// # Whose rights are reviewed
//
//   - **A person the identity provider vouched for** (`via=sso`): a
//     SubjectAccessReview carrying their name and groups, plus
//     `system:authenticated`, which kube-apiserver adds to every authenticated
//     user - so a RoleBinding written for "everybody signed in" answers here as
//     it answers `kubectl auth can-i --as`.
//   - **The shared password account** (`via=password`): the console's own rights,
//     through a SelfSubjectAccessReview. The account names nobody, so there is
//     nobody to review, and **every holder of the password acts with the
//     console's rights** - under the server profile the ServiceAccount's, which
//     docs/SERVER-MODE-THREAT-MODEL.md puts close to cluster-admin less
//     impersonation. That holds whatever the password's source. A password the console drew
//     is read out of the pod, so its holder already had a way in; a configured
//     `KT_PASSWORD_HASH` is not - it is a password a deployment set and a team
//     shares (several replicas require one), and each person holding it is
//     granted the ServiceAccount's rights here. The shared account is accepted
//     as such; configuring an identity provider removes it by default.
//     The audit's `via=password` is what says such a line cannot be
//     attributed.
//   - **The desktop**: the same self review, which is the kubeconfig's own
//     identity - one operator, their own credentials.
//   - **A request nobody is behind**: refused under the server profile, where it
//     can only be a path that lost its context; the console's own rights on the
//     desktop, where there is nobody else it could be.
//
// # Three answers, never two
//
// Allowed, Denied, and Unreviewable - a review that could not be made. The last
// **refuses**, like a denial, and is never reported as one: "you may not" and
// "the console could not ask" send the reader to different people, and an
// answer that collapses them is the defect this console avoids everywhere else.
//
// **One exception, on the desktop only** (Gate.LetsThrough): a review of the
// console's own credentials that could not be made lets the request through,
// because the API server is about to check that same identity itself. The
// record still says `unreviewable`.
//
// **And one answer no review gives** (NotInProfile): under the server profile a
// change to RBAC is refused before any review is asked, whoever asks, because
// the console does not yet review `bind` and `escalate`.
package access

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/safego"
	"github.com/thonicdev/kubetower/textutil"
)

// TTL and FailedTTL are how long an answer, and a failure to get one, are
// reused.
//
// **TTL is a revocation delay, not a performance number.** Removing a
// RoleBinding takes effect here when the held answer expires - up to a minute -
// and not when it is removed. Granting one appears within the same minute. A
// failure is held ten seconds so that a cluster that stopped answering is not
// asked again on every click, and short enough that it clears on its own.
const (
	TTL       = time.Minute
	FailedTTL = 10 * time.Second
	// timeout bounds one review. The answer is filed for every caller, so it
	// must not depend on one caller's patience; see Review.
	timeout = 5 * time.Second
)

// Attributes are the question: a verb on a resource, where and on what.
//
// The fields are a SubjectAccessReview's ResourceAttributes, named the same.
// **Name is part of the question** wherever the request has one, because a
// Role may grant a verb on named objects only, and a review without the name
// answers about the whole kind.
type Attributes struct {
	Verb        string
	Group       string
	Version     string
	Resource    string
	Subresource string
	Namespace   string
	Name        string
}

// String is the attributes as the audit line and the refusals write them:
// `verb=delete resource=pods namespace=shop name=api-7f`. Group-qualified and
// with the subresource where there is one.
func (a Attributes) String() string {
	resource := a.Resource
	if a.Group != "" {
		resource += "." + a.Group
	}
	if a.Subresource != "" {
		resource += "/" + a.Subresource
	}
	namespace, name := a.Namespace, a.Name
	if namespace == "" {
		namespace = "-"
	}
	if name == "" {
		name = "-"
	}
	return fmt.Sprintf("verb=%s resource=%s namespace=%s name=%s", a.Verb, resource, namespace, name)
}

func (a Attributes) resource() *authorizationv1.ResourceAttributes {
	return &authorizationv1.ResourceAttributes{
		Verb: a.Verb, Group: a.Group, Version: a.Version, Resource: a.Resource,
		Subresource: a.Subresource, Namespace: a.Namespace, Name: a.Name,
	}
}

// Decision is what the review answered.
type Decision string

const (
	Allowed Decision = "allowed"
	Denied  Decision = "denied"
	// Unreviewable is a review that could not be made. It refuses, and it is
	// not a denial.
	Unreviewable Decision = "unreviewable"
	// NotInProfile is a request the running profile does not make, so no
	// review was asked: under the server profile, a change to RBAC
	// (rbacChange). It refuses on every answer, like a denial, and is
	// nobody's denial.
	NotInProfile Decision = "not_in_profile"
	// ReadOnly is a request refused because its cluster is marked read-only
	// (KT_READONLY_CLUSTERS), so no review was asked. It refuses on every
	// answer and is nobody's denial.
	ReadOnly Decision = "read_only"
)

// Subject says whose rights an answer is about.
type Subject string

const (
	// Person is a SubjectAccessReview naming the signed-in person.
	Person Subject = "person"
	// Console is a SelfSubjectAccessReview: the console's own credentials - the
	// ServiceAccount in a cluster, the kubeconfig on a desktop.
	Console Subject = "console"
	// Nobody is a request no identity reached, under the server profile.
	Nobody Subject = "nobody"
)

// Answer is one review's outcome.
type Answer struct {
	Decision Decision
	Subject  Subject
	// Reason is the authorizer's own reason on a denial where it gave one, and
	// why no review could be made on an Unreviewable. Never a secret: it is
	// what the API server wrote about RBAC, or an error about reaching it.
	Reason string
	// Record is the id of the audit records Perform wrote for this act
	// (internal/audit), so the process log's human line can name them. Empty
	// on an answer from Review, and where no record was written.
	Record string
}

// Allowed reports whether the answer lets the request go.
func (a Answer) Allowed() bool { return a.Decision == Allowed }

// Refused is the error Perform answers when the review did not allow the
// request. errors.Is(err, ErrDenied) and errors.Is(err, ErrUnreviewable) tell
// the two apart.
type Refused struct {
	Cluster    string
	Attributes Attributes
	Answer     Answer
	// Nowhere says a list across every namespace was denied and so was the
	// same list in each namespace of the cluster: there is nothing to narrow
	// it to, and the sentence says "in any namespace".
	Nowhere bool
}

// extension.ErrDenied and extension.ErrUnreviewable are these two values, so
// an extra outside this package classifies a Refused with errors.Is through Is
// below. Replacing either with a new error breaks that, and the boundary
// module's test says so.
var (
	// ErrDenied is the cluster saying no.
	ErrDenied = errors.New("access denied")
	// ErrUnreviewable is the console being unable to ask.
	ErrUnreviewable = errors.New("access could not be reviewed")
	// ErrRBACNotInProfile is a change to RBAC under the server profile
	// (NotInProfile): refused before any review, whatever the person's rights.
	ErrRBACNotInProfile = errors.New("RBAC changes are not available in the server profile in this version")
)

func (r *Refused) Error() string {
	what := strings.TrimSpace(r.Attributes.Verb + " " + qualified(r.Attributes))
	where := ""
	switch {
	case r.Attributes.Namespace != "":
		where = " in namespace " + r.Attributes.Namespace
	case r.Nowhere:
		where = " in any namespace"
	}
	switch r.Answer.Decision {
	case NotInProfile:
		sentence := fmt.Sprintf("%s, so this console did not %s", ErrRBACNotInProfile, what)
		if r.Attributes.Name != "" {
			sentence += " " + r.Attributes.Name
		}
		return sentence + where
	case Denied:
		sentence := fmt.Sprintf("the cluster says you may not %s%s", what, where)
		if r.Attributes.Name != "" {
			sentence = fmt.Sprintf("the cluster says you may not %s %s%s", what, r.Attributes.Name, where)
		}
		if r.Answer.Subject == Console {
			sentence = strings.Replace(sentence, "you may not", "this console's own credentials may not", 1)
		}
		if r.Answer.Reason != "" {
			sentence += ": " + r.Answer.Reason
		}
		return sentence
	default:
		sentence := fmt.Sprintf("the console could not ask the cluster whether you may %s%s, so it did not try", what, where)
		if r.Answer.Reason != "" {
			sentence += ": " + r.Answer.Reason
		}
		return sentence
	}
}

// Status is the refusal as the API server would have answered it - a denial a
// 403 Forbidden, a review that could not be made a 503 - so every reader that
// already tells a 403 from an outage (`apierrors.IsForbidden`, the health
// package's Denied) reads a person's refusal as what it is, with this
// refusal's own sentence, rather than as a failure of some other kind.
func (r *Refused) Status() metav1.Status {
	if r.Answer.Decision == Denied || r.Answer.Decision == NotInProfile {
		return metav1.Status{Status: metav1.StatusFailure, Code: http.StatusForbidden,
			Reason: metav1.StatusReasonForbidden, Message: r.Error()}
	}
	return metav1.Status{Status: metav1.StatusFailure, Code: http.StatusServiceUnavailable,
		Reason: metav1.StatusReasonServiceUnavailable, Message: r.Error()}
}

func (r *Refused) Is(target error) bool {
	switch target {
	case ErrDenied:
		return r.Answer.Decision == Denied
	case ErrUnreviewable:
		return r.Answer.Decision != Denied && r.Answer.Decision != NotInProfile
	case ErrRBACNotInProfile:
		return r.Answer.Decision == NotInProfile
	}
	return false
}

func qualified(a Attributes) string {
	resource := a.Resource
	if a.Group != "" {
		resource += "." + a.Group
	}
	if a.Subresource != "" {
		resource += "/" + a.Subresource
	}
	return resource
}

// Reviews is what a review is asked through: the two create calls of the
// authorization API, for one cluster. An interface rather than a clientset so
// the choke point's own test can answer without a cluster, and so nothing else
// of the clientset is in reach here.
type Reviews interface {
	SubjectAccessReview(ctx context.Context, review *authorizationv1.SubjectAccessReview) (*authorizationv1.SubjectAccessReview, error)
	SelfSubjectAccessReview(ctx context.Context, review *authorizationv1.SelfSubjectAccessReview) (*authorizationv1.SelfSubjectAccessReview, error)
}

// Options configure a Gate.
type Options struct {
	// Reviews returns the review client for a cluster, by its context name.
	// Required: a Gate without it answers Unreviewable to everything, which is
	// the fail-closed default rather than a way to run without reviews.
	Reviews func(cluster string) (Reviews, error)
	// InCluster is the server profile. It decides one thing here: a request no
	// identity reached is refused rather than reviewed as the console.
	InCluster bool
	// Now is the clock. Nil is time.Now.
	Now func() time.Time
	// Logf says a failed review, once per cluster and message.
	Logf func(format string, args ...any)
	// Audit is where every act's records go (audit.go). Nil keeps none,
	// which the desktop may do and **the server profile may not**: an
	// InCluster gate with no log refuses every act, so a server assembled
	// without one stops acting rather than acting unrecorded.
	Audit *audit.Log
}

// Gate is the choke point. The zero value and a nil *Gate both refuse
// everything as Unreviewable.
type Gate struct {
	reviews   func(string) (Reviews, error)
	inCluster bool
	now       func() time.Time
	logf      func(string, ...any)
	audit     *audit.Log

	mu     sync.Mutex
	held   map[key]held
	logged map[string]bool
	// flying are the reviews being asked right now, so that a question asked
	// twice at once is asked of the API server once.
	flying map[key]*flight
	// limit bounds held; inserts counts insertions for the periodic sweep.
	limit   int
	inserts int
	// slots are the reviews this process may have in the air at once, every
	// person's together (ReviewsPerProcess). Nil off the server
	// profile: see New.
	slots chan struct{}
}

// flight is one review in the air; answer is set before done closes.
type flight struct {
	done   chan struct{}
	answer Answer
}

// MaxHeld bounds the held answers. **The key carries the object's name**, and
// names come from requests, so without a bound any signed-in person could grow
// this map for as long as the process lives - one uncached review per made-up
// name, and the memory for each. Past it, expired answers are swept first and
// then arbitrary ones are dropped (and asked again when next needed): a flood
// costs the flooder reviews, never the process its memory.
const MaxHeld = 10000

// sweepEvery is how many insertions pass between sweeps of expired answers, so
// a busy map is cleaned before it reaches the bound rather than only at it.
const sweepEvery = 256

// maxLogged bounds the once-per-message record of failed reviews, whose
// messages come from errors and so are not a closed set either.
const maxLogged = 1024

type key struct {
	cluster string
	subject string
	attrs   Attributes
}

type held struct {
	answer Answer
	until  time.Time
}

// New builds a Gate.
func New(opts Options) *Gate {
	g := &Gate{
		reviews: opts.Reviews, inCluster: opts.InCluster, now: opts.Now, logf: opts.Logf, audit: opts.Audit,
		held: map[key]held{}, logged: map[string]bool{}, flying: map[key]*flight{},
		limit: MaxHeld,
	}
	if g.now == nil {
		g.now = time.Now
	}
	// **The server profile only.** There the console reaches one cluster, the
	// pod's own, through one rate limiter every person shares, so a cap on the
	// whole process is a cap on that limiter's queue. The desktop reaches many
	// clusters, several of them unreachable on an ordinary day, and one gate
	// for all of them: a process-wide cap there would let five-second reviews
	// of a cluster that does not answer hold the slots of every cluster that
	// does - and the desktop has one person, whose reviews ReviewAll already
	// bounds.
	if opts.InCluster {
		g.slots = make(chan struct{}, ReviewsPerProcess)
	}
	return g
}

// Review answers one question about the identity ctx carries, from the held
// answers where it can.
func (g *Gate) Review(ctx context.Context, cluster string, attrs Attributes) Answer {
	if g == nil || g.reviews == nil {
		return Answer{Decision: Unreviewable, Subject: Nobody,
			Reason: "this build was assembled without an access review"}
	}
	subject, who, groups := g.subjectOf(ctx)
	if subject == Nobody {
		return Answer{Decision: Unreviewable, Subject: Nobody,
			Reason: "no signed-in identity reached this request, and under the server profile the console does not act for nobody"}
	}
	// **Before any review, and before the held answers**: under the server
	// profile the console does not change RBAC in this version, whoever asks.
	// A Role or a binding is where `bind` and `escalate` apply, and the console
	// has never reviewed either: the ServiceAccount could grant what the person
	// holds no right to grant. Every write leaves through Perform, which asks
	// this first, so the request is never stamped and Guard refuses it too.
	if g.inCluster && rbacChange(attrs) {
		return Answer{Decision: NotInProfile, Subject: subject,
			Reason: "the console does not change RBAC under the server profile in this version"}
	}
	k := key{cluster: cluster, subject: subjectKey(subject, who, groups), attrs: attrs}
	now := g.now()

	g.mu.Lock()
	if h, ok := g.held[k]; ok && now.Before(h.until) {
		g.mu.Unlock()
		return h.answer
	}
	// **The same question already in the air is waited for, not asked
	// again.** A page that loads four panels asks the same review four times
	// in the same millisecond; without this each is a round trip, and a
	// burst of identical requests is a burst against the authorizer.
	if f, ok := g.flying[k]; ok {
		g.mu.Unlock()
		// A waiter keeps its own deadline: a caller that goes away is not held
		// by somebody else's review. What it is told is that nothing was
		// answered for it, which refuses and files nothing.
		select {
		case <-f.done:
			return f.answer
		case <-ctx.Done():
			return Answer{Decision: Unreviewable, Subject: subject, Reason: ctx.Err().Error()}
		}
	}
	f := &flight{done: make(chan struct{})}
	g.flying[k] = f
	g.mu.Unlock()
	// **done closes whatever happens**, a panic in the review included:
	// otherwise every later caller of this question would wait on a flight
	// that never lands. The answer is Unreviewable until the review sets it.
	f.answer = Answer{Decision: Unreviewable, Subject: subject, Reason: "the review did not complete"}
	defer close(f.done)
	f.answer = g.review(ctx, k, cluster, subject, who, groups, attrs, now)
	return f.answer
}

// review asks the API server one question and files the answer. The caller
// holds the flight for k.
func (g *Gate) review(ctx context.Context, k key, cluster string, subject Subject, who string, groups []string, attrs Attributes, now time.Time) Answer {
	defer func() {
		g.mu.Lock()
		delete(g.flying, k)
		g.mu.Unlock()
	}()

	// Asked on the request's values but not its cancellation. The answer is
	// filed for every caller for a minute, so it must not depend on whether
	// this one caller stayed: a tab closed mid-load would otherwise fail the
	// review with `context canceled` and file that as the answer for everybody.
	// Only the timeout bounds it.
	reviewCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	answer, err := g.askInTurn(reviewCtx, cluster, subject, who, groups, attrs)
	if errors.Is(err, context.Canceled) {
		// Not an answer about the cluster, so not filed as one.
		return Answer{Decision: Unreviewable, Subject: subject, Reason: err.Error()}
	}
	until := now.Add(TTL)
	if err != nil {
		answer = Answer{Decision: Unreviewable, Subject: subject, Reason: firstLine(err.Error())}
		until = now.Add(FailedTTL)
		g.sayOnce(cluster, err)
	}

	g.mu.Lock()
	g.store(k, held{answer: answer, until: until}, now)
	g.mu.Unlock()
	return answer
}

// askInTurn is ask, once one of the process's review slots is free.
//
// **The wait counts against the review's own timeout**, so a review
// costs a caller at most its own timeout: one that waited its five seconds for a
// slot is answered Unreviewable, held for FailedTTL and said once, as a review
// the limiter holds that long is. What the slots change is where the queue is:
// in this process, ReviewsPerProcess deep, rather than at the rate limiter
// every person's reads also wait at.
func (g *Gate) askInTurn(ctx context.Context, cluster string, subject Subject, who string, groups []string, attrs Attributes) (Answer, error) {
	if g.slots != nil {
		select {
		case g.slots <- struct{}{}:
			defer func() { <-g.slots }()
		case <-ctx.Done():
			return Answer{}, fmt.Errorf("every one of the %d access reviews this console has in the air at "+
				"once was taken for as long as a review may wait: %w", ReviewsPerProcess, ctx.Err())
		}
	}
	return g.ask(ctx, cluster, subject, who, groups, attrs)
}

// store files one answer, keeping held within its bound. The caller holds mu.
func (g *Gate) store(k key, h held, now time.Time) {
	g.inserts++
	if g.inserts%sweepEvery == 0 || len(g.held) >= g.limit {
		for old, entry := range g.held {
			if !now.Before(entry.until) {
				delete(g.held, old)
			}
		}
	}
	// Still full of live answers: drop arbitrary ones - map order is random -
	// down to nine tenths, so the next insertions do not each pay a sweep.
	if len(g.held) >= g.limit {
		target := g.limit * 9 / 10
		for old := range g.held {
			if len(g.held) <= target {
				break
			}
			delete(g.held, old)
		}
	}
	g.held[k] = h
}

// ReviewAll answers several questions at once, concurrently: a page that offers
// four verbs asks four reviews, and four round trips in a row are four times
// the wait. The answers are in the order of the questions.
//
// The same question twice is asked of the API server once - a pod page's
// Restart and Delete are both `delete` on that pod - because Review shares a
// question already in the air, and holds it once answered.
//
// **At most ReviewsInFlight at once.** Every review is an HTTP request
// through the one per-cluster rate limiter every person's every read also uses,
// and narrowing a denied list asks one per namespace: unbounded, that is a
// thousand goroutines and a thousand requests queued at once on a 50-QPS
// bucket, starving everybody else's reads for as long as they take.
func (g *Gate) ReviewAll(ctx context.Context, cluster string, questions []Attributes) []Answer {
	answers := make([]Answer, len(questions))
	slots := make(chan struct{}, ReviewsInFlight)
	var wg sync.WaitGroup
	for i := range questions {
		wg.Add(1)
		slots <- struct{}{}
		// Through safego: a panic in one review is that question's
		// answer, unreviewable - which every caller already treats as "not
		// known", never as allowed.
		safego.Go("access: one review of a batch", func() error {
			answers[i] = g.Review(ctx, cluster, questions[i])
			return nil
		}, func(err error) {
			if err != nil {
				answers[i] = Answer{Decision: Unreviewable, Reason: err.Error()}
			}
			<-slots
			wg.Done()
		})
	}
	wg.Wait()
	return answers
}

// rbacChange is a request that would change an object of the RBAC API group
// (roles, clusterroles, rolebindings, clusterrolebindings): any verb but a
// read.
func rbacChange(attrs Attributes) bool {
	return attrs.Group == "rbac.authorization.k8s.io" && !readVerbs[attrs.Verb]
}

// ReviewsInFlight is how many reviews one ReviewAll has in the air at once.
// Eight: a page's handful of verbs still go together, and one person's denied
// list across many namespaces takes a share of the shared limiter rather than
// all of it.
const ReviewsInFlight = 8

// ReviewsPerProcess is how many reviews the whole process has in the air at
// once under the server profile, every person and every call together
// ReviewsInFlight bounds one call, so without this twenty people narrowing at
// once would be twenty times it - 160 reviews queued on the one 50-QPS bucket,
// each read of every other person waiting behind them.
//
// **Sixteen**, which is two calls' share. It does not lower what reviews can
// do: sixteen in the air at a round trip of tens of milliseconds is hundreds
// a second, past the limiter's fifty, so the limiter still sets the pace. What
// it bounds is the queue in front of a read: at most sixteen reviews ahead of
// it at the limiter, a third of a second at fifty a second, however many
// people are narrowing.
const ReviewsPerProcess = 16

// Perform reviews, and runs do only on an Allowed answer - or on the one
// answer LetsThrough names. It is **the only place a request that changes a
// cluster may be made from** - choke_test.go holds every write-capable call to
// the inside of a closure passed here.
//
// It answers the review and do's own error. A review that did not let the
// request go is a *Refused, and do is not called. The answer comes back either
// way, so the record can say which decision a request that went was made on.
//
// **do runs on a context stamped with cluster and attrs**, and it must make
// its request on that context - the one it is handed, not one captured from
// outside. Every cluster client sends through Guard (transport.go), which
// refuses a request that changes something unless that stamp is on it and
// covers it: so a closure asking about one thing and sending another, or
// sending on a context that is not the one Perform gave it, is refused before
// it leaves the process.
//
// **And it writes the two audit records around it** (audit.go): the attempt,
// after the review and before do runs, and the outcome once do has returned.
// An attempt record that cannot be written refuses the request as an
// *Unrecorded, and do is not called - a failed write of the record refuses the
// action. A request the review refused is recorded too, attempt and outcome
// `refused`, because the refused act is the one an audit reader is looking
// for. An outcome record that cannot be written is said in the process log and
// changes nothing: the request has been made.
func (g *Gate) Perform(ctx context.Context, cluster string, attrs Attributes, do func(context.Context) error) (Answer, error) {
	answer := g.Review(ctx, cluster, attrs)
	pending, unrecorded := g.Begin(ctx, g.Record(ctx, actOf(attrs), cluster, attrs, answer.Decision))
	answer.Record = pending.ID()
	if !answer.Allowed() && !g.LetsThrough(answer) {
		g.End(ctx, pending, audit.Refused, "")
		return answer, &Refused{Cluster: cluster, Attributes: attrs, Answer: answer}
	}
	if unrecorded != nil {
		return answer, unrecorded
	}
	// held is how the outcome learns that Guard held the request back: the
	// closure's error may no longer say so by the time it returns.
	held := new(atomic.Bool)
	w := new(wire)
	err := do(stamped(ctx, cluster, attrs, held, w))
	switch {
	case held.Load():
		g.End(ctx, pending, audit.Failed, Unsent)
	case err != nil && w.snapshot().NotSent():
		// **Never sent, whatever the error says**: a
		// rate limiter out of time, a connection never made. The record says
		// `held`, not `cancelled`, which would claim a request was made, and
		// not `unsent`, which is the guard's security signal.
		g.End(ctx, pending, audit.Failed, Held)
	case err != nil && w.snapshot().AnsweredTimedOut():
		// **Answered by a timeout, whatever came back after it**: a retry cut
		// by the deadline, or a 409 for a create the timed-out attempt may
		// have made. The change may still happen (ErrTimedOut).
		g.End(ctx, pending, audit.Failed, TimedOut)
	case err != nil:
		word := FailureWord(err)
		if word == "error" && w.snapshot().Unanswered() {
			// Sent, and the connection ended before an answer: whether it
			// happened is not known, which `error` does not say.
			word = Unanswered
		}
		g.End(ctx, pending, audit.Failed, word)
	default:
		g.End(ctx, pending, audit.Applied, "")
	}
	return answer, err
}

// LetsThrough is the one answer short of Allowed that does not refuse: **on the
// desktop, a review of the console's own credentials that could not be made.**
//
// On the desktop the request is sent with
// the kubeconfig's own identity, and the API server checks that identity
// itself; refusing because the console could not ask the question the API
// server is about to answer protects nothing and blocks the operator. The
// record still says `decision=unreviewable`, so the line does not claim a yes
// nobody gave.
//
// **Nowhere else.** Under the server profile the request is the
// ServiceAccount's and the review is the only thing standing between a person
// and its rights: a person the provider vouched for, and the shared password
// account, both stay refused when the review cannot be made. A denial is a
// denial on every profile, and a gate with nothing behind it (Subject Nobody)
// refuses everywhere.
func (g *Gate) LetsThrough(a Answer) bool {
	return g != nil && !g.inCluster && a.Decision == Unreviewable && a.Subject == Console
}

// RefusesTheConsole reports a denial of the console's own ServiceAccount under
// the server profile: a self review, which is what the shared password account
// is reviewed as (subjectOf), refused.
//
// It is not a person's refusal. A page withholds a verb the person may not
// perform, and the shared account's self review answers "may not" for every
// kind the chart's role does not grant - with the chart's default
// `rbac.write: false`, every write - so treated as a person's refusal, the
// drawer, the row menu and the selection bar would offer no Delete and say
// nothing. What is missing there is the console's grant, which an
// administrator can give; the page says so instead.
// On the desktop a self review is the kubeconfig's own identity, the person's,
// and its denial stays a withholding.
func (g *Gate) RefusesTheConsole(a Answer) bool {
	return g != nil && g.inCluster && a.Decision == Denied && a.Subject == Console
}

// subjectOf decides whose rights a request is reviewed as. See the package
// comment for why each case is what it is.
func (g *Gate) subjectOf(ctx context.Context) (Subject, string, []string) {
	who, ok := identity.From(ctx)
	switch {
	case actingAsConsole(ctx):
		// The console reading for itself (AsConsole), whoever's request it runs
		// inside: what it reads fills a hold every person is served from, and
		// each of them is reviewed on the way out.
		return Console, "", nil
	case ok && who.Method == identity.SSO:
		return Person, who.Name, who.Groups
	case ok:
		return Console, "", nil
	case g.inCluster:
		return Nobody, "", nil
	}
	return Console, "", nil
}

// authenticated is the group kube-apiserver adds to every authenticated user.
// A review is a statement of who is asking, and the API server's authorizer
// takes it at its word: omitted, a RoleBinding to "everybody signed in" would
// answer no here while `kubectl auth can-i --as` answers yes.
const authenticated = "system:authenticated"

func (g *Gate) ask(ctx context.Context, cluster string, subject Subject, user string, groups []string, attrs Attributes) (Answer, error) {
	client, err := g.reviews(cluster)
	if err != nil {
		return Answer{}, err
	}
	if subject == Person {
		asserted := append(append([]string(nil), groups...), authenticated)
		review, err := client.SubjectAccessReview(ctx, &authorizationv1.SubjectAccessReview{
			Spec: authorizationv1.SubjectAccessReviewSpec{
				ResourceAttributes: attrs.resource(),
				User:               user,
				Groups:             asserted,
			},
		})
		if err != nil {
			return Answer{}, err
		}
		return answerOf(review.Status, Person), nil
	}
	review, err := client.SelfSubjectAccessReview(ctx, &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: attrs.resource()},
	})
	if err != nil {
		return Answer{}, err
	}
	return answerOf(review.Status, Console), nil
}

// answerOf reads a review's status. `EvaluationError` beside `allowed: false` is
// still an answer - the authorizer said no and explained part of why - so it is
// a denial carrying that reason, not a failure to review.
func answerOf(status authorizationv1.SubjectAccessReviewStatus, subject Subject) Answer {
	if status.Allowed && !status.Denied {
		return Answer{Decision: Allowed, Subject: subject}
	}
	reason := status.Reason
	if reason == "" {
		reason = status.EvaluationError
	}
	return Answer{Decision: Denied, Subject: subject, Reason: firstLine(reason)}
}

// subjectKey is what the held answers are filed under. The groups are sorted,
// so one person's two sessions share their answers, and a different group set -
// a group removed at the provider and a new sign-in - does not.
//
// **A digest, not the list itself.** Every held answer carries its key, and an
// enterprise identity provider hands a person hundreds of groups: kept whole,
// MaxHeld answers for one such person are a hundred megabytes and more. The
// SHA-256 of the same length-prefixed spelling is a fixed 32 bytes, whatever
// the groups, and two people still never share a key.
func subjectKey(subject Subject, user string, groups []string) string {
	if subject != Person {
		return string(subject)
	}
	sorted := append([]string(nil), groups...)
	sort.Strings(sorted)
	// **Length-prefixed**, so no name or group can be spelled to collide with
	// another split of the same bytes: a separator is a character, and a
	// provider's claim may carry any character.
	h := sha256.New()
	for _, part := range append([]string{user}, sorted...) {
		fmt.Fprintf(h, ":%d:%s", len(part), part)
	}
	return "person:" + string(h.Sum(nil))
}

func (g *Gate) sayOnce(cluster string, err error) {
	if g.logf == nil {
		return
	}
	line := fmt.Sprintf("%s: %v", cluster, err)
	g.mu.Lock()
	if len(g.logged) >= maxLogged {
		g.logged = map[string]bool{}
	}
	seen := g.logged[line]
	g.logged[line] = true
	g.mu.Unlock()
	if !seen {
		g.logf("could not ask cluster %q whether a request may be made, so it is refused: %s",
			cluster, identity.Escape(firstLine(err.Error())))
	}
}

// firstLine bounds what an error puts on a line or a control's title: 300
// runes, the length internal/edit gives a refusal.
func firstLine(s string) string { return textutil.FirstLineN(s, 300) }

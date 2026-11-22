package web

// The in-cluster menu offers only the kinds the console may list. Every list
// there is made with the console's one ServiceAccount, so a kind it may list in
// no namespace could only ever open a refusal; it is left out of the menu and the
// jump box. The desktop and the container keep every kind: there the refusal is
// the person's own and says which RoleBinding is missing.
//
// The reviews go through the lists' own Gate, as the console (`AsConsole`, a
// SelfSubjectAccessReview, which needs no grant):
//
//  1. `list` across every namespace, per kind. Allowed keeps the kind, and so
//     does an answer that could not be had: unknown is not a refusal.
//  2. A cluster-scoped kind denied there is left out.
//  3. A namespaced kind denied there may still be granted per namespace, so it
//     is asked of each namespace in the console's namespace hold, the pod's own
//     first, and kept at the first that allows it; left out only when every
//     namespace says no. The walk starts only if what is left of
//     narrowReviewBudget covers every namespace; otherwise the kind is kept
//     unasked.
//
// Verdicts are held per cluster for menuAccessTTL and renewed behind the caller
// past menuAccessRenew, as the CRD catalog is, so only a first draw waits, for
// at most menuAccessWait. Per cluster and renewal: one review per kind (the same
// questions the lists ask, so the Gate's held answers serve both) and
// narrowReviewBudget per-namespace reviews, renewInFlight at a time so the shared
// rate limiter keeps serving reads. A SelfSubjectRulesReview is not used: it
// describes rules, may be incomplete, and is not the question the list is refused
// by.

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/customresources"
	"github.com/thonicdev/kubetower/internal/resources"
	"github.com/thonicdev/kubetower/safego"
)

const (
	// menuAccessTTL is how long a verdict is served at all; past it the next draw
	// asks again while it waits.
	menuAccessTTL = 5 * time.Minute
	// menuAccessRenew is the age past which a verdict is still served and renewed
	// behind the caller. It is also how late a RoleBinding change reaches the menu;
	// the page itself is reviewed on every read.
	menuAccessRenew = 3 * menuAccessTTL / 4
	// menuAccessWait bounds what a first draw waits for; an unsettled kind is kept.
	menuAccessWait = 3 * time.Second
	// menuAccessRenewBound bounds one renewal behind the caller.
	menuAccessRenewBound = 30 * time.Second
	// renewInFlight is how many reviews a renewal has in the air at once, against
	// access.ReviewsInFlight for a draw someone waits on, so it leaves the shared
	// rate limiter to people's reads.
	renewInFlight = 2
	// narrowReviewBudget is how many per-namespace reviews one cluster's menu may ask
	// per menuAccessRenew. Below the per-cluster limiter's burst of a hundred, it
	// settles a few refused kinds on a cluster of a few dozen namespaces; on a larger
	// one a refused namespaced kind is kept rather than guessed.
	narrowReviewBudget = 64
)

// menuAccess holds the menu's verdicts per cluster.
type menuAccess struct {
	mu    sync.Mutex
	slots map[string]*accessSlot
	// now is the clock; own is the pod's own namespace, "" where unknown. Fields so a
	// test can set both.
	now func() time.Time
	own func() string
	// renewing counts the renewals running behind a caller, for a test to wait on.
	renewing sync.WaitGroup
}

// accessSlot is one cluster's verdicts, keyed by the question across every
// namespace, and the per-namespace budget of the current window.
type accessSlot struct {
	verdicts map[access.Attributes]menuVerdict
	// asking is the questions being settled now, so a second draw serves what is
	// held instead of asking again.
	asking map[access.Attributes]bool
	window time.Time
	spent  int
}

type menuVerdict struct {
	keep bool
	at   time.Time
}

func newMenuAccess() *menuAccess {
	return &menuAccess{slots: map[string]*accessSlot{}, now: time.Now, own: podNamespace}
}

// podNamespace is the namespace the console's pod runs in, from the file the
// kubelet mounts beside the ServiceAccount's token; "" where there is none.
var podNamespace = sync.OnceValue(func() string {
	raw, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
})

func (m *menuAccess) slot(cluster string) *accessSlot {
	s := m.slots[cluster]
	if s == nil {
		s = &accessSlot{verdicts: map[access.Attributes]menuVerdict{}, asking: map[access.Attributes]bool{}}
		m.slots[cluster] = s
	}
	return s
}

// take reserves n per-namespace reviews from cluster's window, or answers
// false and reserves nothing.
func (m *menuAccess) take(cluster string, n int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.slot(cluster)
	if now := m.now(); now.Sub(s.window) >= menuAccessRenew {
		s.window, s.spent = now, 0
	}
	if s.spent+n > narrowReviewBudget {
		return false
	}
	s.spent += n
	return true
}

// refund gives back what a walk reserved and did not ask.
func (m *menuAccess) refund(cluster string, n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.slot(cluster); n > 0 {
		s.spent = max(0, s.spent-n)
	}
}

// wideOf is a question across every namespace: what a verdict is filed under.
func wideOf(q resources.Question) access.Attributes {
	a := q.Attrs
	a.Namespace = ""
	return a
}

// listable answers, per question, whether the kind may stay in the menu: true
// unless the console's own credentials were refused its list in every
// namespace. Every kind is kept off the in-cluster profile, and nothing is
// asked there.
func (s *Server) listable(ctx context.Context, cluster string, questions []resources.Question) []bool {
	keep := make([]bool, len(questions))
	for i := range keep {
		keep[i] = true
	}
	m := s.menuAccess
	if !s.inCluster || s.gate == nil || m == nil || len(questions) == 0 {
		return keep
	}

	var cold, stale []int
	m.mu.Lock()
	slot, now := m.slot(cluster), m.now()
	for i, q := range questions {
		key := wideOf(q)
		v, held := slot.verdicts[key]
		switch {
		case slot.asking[key]:
			// Being settled by another draw: served as held, or kept.
			if held {
				keep[i] = v.keep
			}
		case !held || now.Sub(v.at) >= menuAccessTTL:
			cold = append(cold, i)
			slot.asking[key] = true
		default:
			keep[i] = v.keep
			if now.Sub(v.at) >= menuAccessRenew {
				stale = append(stale, i)
				slot.asking[key] = true
			}
		}
	}
	m.mu.Unlock()

	if len(stale) > 0 {
		renew := pickQuestions(questions, stale)
		// Detached from the request, which is usually over by then, with its own bound.
		m.renewing.Add(1)
		// Through safego: after a panic the verdicts stay as held and stop being
		// "asked", so the next draw asks again.
		safego.Go("menu: renewing the kinds' access verdicts", func() error {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), menuAccessRenewBound)
			defer cancel()
			s.settle(ctx, cluster, renew, renewInFlight)
			return nil
		}, func(err error) {
			defer m.renewing.Done()
			if err == nil {
				return
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			slot := m.slot(cluster)
			for _, q := range renew {
				delete(slot.asking, wideOf(q))
			}
		})
	}
	if len(cold) > 0 {
		wait, cancel := context.WithTimeout(ctx, menuAccessWait)
		defer cancel()
		got := s.settle(wait, cluster, pickQuestions(questions, cold), access.ReviewsInFlight)
		for j, i := range cold {
			keep[i] = got[j]
		}
	}
	return keep
}

func pickQuestions(questions []resources.Question, at []int) []resources.Question {
	out := make([]resources.Question, len(at))
	for j, i := range at {
		out[j] = questions[i]
	}
	return out
}

// settle asks the questions (see the file's header), files the verdicts and
// answers them. A kind that could not be settled is kept and filed as due for
// renewal, so the next draw serves it and settles it behind the caller.
func (s *Server) settle(ctx context.Context, cluster string, questions []resources.Question, inFlight int) []bool {
	m := s.menuAccess
	keep := make([]bool, len(questions))
	settled := make([]bool, len(questions))
	for i := range keep {
		keep[i] = true
	}
	// The console's own rights, whoever's request this is: the ServiceAccount makes
	// every list, and each person is still shown only their own review's share.
	ctx = access.AsConsole(ctx)

	wide := make([]access.Attributes, len(questions))
	for i, q := range questions {
		wide[i] = wideOf(q)
	}
	var narrow []int
	for i, answer := range s.ask(ctx, cluster, wide, inFlight) {
		switch {
		case answer.Decision == access.Allowed:
			settled[i] = true
		case answer.Decision != access.Denied:
			// Not known: kept, and asked again.
		case !questions[i].Namespaced:
			keep[i], settled[i] = false, true
		default:
			narrow = append(narrow, i)
		}
	}

	if len(narrow) > 0 && ctx.Err() == nil {
		held := s.namespaceAnswer(ctx, cluster)
		// Not known where it could be listed, so not known that it cannot.
		if !held.failed && len(held.names) > 0 {
			own := m.own()
			names, first := ownFirst(held.names, own)
			for _, i := range narrow {
				if ctx.Err() != nil {
					break
				}
				// The whole walk, or none of it.
				if !m.take(cluster, len(names)) {
					continue
				}
				listed, refused, asked := s.walk(ctx, cluster, wide[i], names, first, inFlight)
				m.refund(cluster, len(names)-asked)
				switch {
				case listed:
					settled[i] = true
				case refused:
					keep[i], settled[i] = false, true
				}
			}
		}
	}

	now := m.now()
	m.mu.Lock()
	slot := m.slot(cluster)
	for i, key := range wide {
		at := now
		if !settled[i] {
			at = now.Add(-menuAccessRenew)
		}
		slot.verdicts[key] = menuVerdict{keep: keep[i], at: at}
		delete(slot.asking, key)
	}
	m.mu.Unlock()
	return keep
}

// walk asks question of each namespace, in batches, until one allows it; the
// first batch is first long (the console's own namespace alone, via ownFirst).
// listed: a namespace allowed it; refused: every namespace said no; neither: an
// answer nobody gave, or the context ended. asked counts the reviews.
func (s *Server) walk(ctx context.Context, cluster string, question access.Attributes, names []string, first, inFlight int) (listed, refused bool, asked int) {
	unknown := false
	for start, size := 0, first; start < len(names); start, size = start+size, inFlight {
		// A started review finishes whatever its caller does, so an ended request stops
		// here, between batches.
		if ctx.Err() != nil {
			return false, false, asked
		}
		end := min(start+size, len(names))
		batch := make([]access.Attributes, 0, end-start)
		for _, namespace := range names[start:end] {
			q := question
			q.Namespace = namespace
			batch = append(batch, q)
		}
		asked += len(batch)
		for _, answer := range s.gate.ReviewAll(ctx, cluster, batch) {
			switch answer.Decision {
			case access.Allowed:
				return true, false, asked
			case access.Denied:
			default:
				unknown = true
			}
		}
	}
	return false, !unknown, asked
}

// ask is ReviewAll, inFlight at a time, stopping where the context ends.
// Questions not asked answer as not known.
func (s *Server) ask(ctx context.Context, cluster string, questions []access.Attributes, inFlight int) []access.Answer {
	answers := make([]access.Answer, len(questions))
	for i := range answers {
		answers[i] = access.Answer{Decision: access.Unreviewable, Subject: access.Console, Reason: "not asked"}
	}
	for start := 0; start < len(questions); start += inFlight {
		if ctx.Err() != nil {
			break
		}
		end := min(start+inFlight, len(questions))
		copy(answers[start:end], s.gate.ReviewAll(ctx, cluster, questions[start:end]))
	}
	return answers
}

// ownFirst is names with own moved to the front, if present, and the first
// batch's length: one when own leads, since a ServiceAccount is most often
// granted in its own namespace.
func ownFirst(names []string, own string) ([]string, int) {
	for i, name := range names {
		if name != own || own == "" {
			continue
		}
		out := make([]string, 0, len(names))
		out = append(out, own)
		out = append(out, names[:i]...)
		return append(out, names[i+1:]...), 1
	}
	return names, access.ReviewsInFlight
}

// unlistedKinds is the built-in kinds the in-cluster menu leaves out (listable),
// by slug. Nil off that profile, and nil where every kind may stay.
func (s *Server) unlistedKinds(ctx context.Context, cluster string) map[string]bool {
	if !s.inCluster {
		return nil
	}
	kinds := resources.Kinds()
	questions := make([]resources.Question, 0, len(kinds))
	slugs := make([]string, 0, len(kinds))
	for _, d := range kinds {
		// A kind with no question is kept: this file errs towards showing.
		q, ok := d.ListQuestion()
		if !ok {
			continue
		}
		questions = append(questions, q)
		slugs = append(slugs, d.Kind)
	}
	var hidden map[string]bool
	for i, keep := range s.listable(ctx, cluster, questions) {
		if keep {
			continue
		}
		if hidden == nil {
			hidden = map[string]bool{}
		}
		hidden[slugs[i]] = true
	}
	return hidden
}

// listableDefinitions is the custom resource kinds the in-cluster menu keeps
// (listable), in their order, and how many it left out.
func (s *Server) listableDefinitions(ctx context.Context, cluster string, definitions []customresources.Definition) ([]customresources.Definition, int) {
	if !s.inCluster || len(definitions) == 0 {
		return definitions, 0
	}
	questions := make([]resources.Question, len(definitions))
	for i, d := range definitions {
		questions[i] = definitionQuestion(d)
	}
	keep := s.listable(ctx, cluster, questions)
	out := make([]customresources.Definition, 0, len(definitions))
	for i, d := range definitions {
		if keep[i] {
			out = append(out, d)
		}
	}
	return out, len(definitions) - len(out)
}

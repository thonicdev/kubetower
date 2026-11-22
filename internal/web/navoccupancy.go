package web

// Which of the menu's kinds hold an object the person asking may see, for `GET
// /api/c/{cluster}/nav/occupancy`; the kinds column hides empty kinds by default.
//
// A kind is `none` only on an answer: listed and empty, or not listable by the
// person anywhere it is held. A refusal, a cluster that did not answer, a review
// nobody could make or a bound reached is `unknown`, with its reason.
//
//  1. As the console, held and shared (occupancyHold): one metadata-only list
//     of at most one row per kind (resources.AnyObject), through Gate.Read,
//     held occupancyTTL per cluster. `none` there is `none` for everybody.
//  2. For the person, where the console found an object: their review of the
//     list across every namespace. Allowed is `some`; denied on a
//     cluster-scoped kind is `none`.
//  3. Denied across the cluster on a namespaced kind: each namespace in the
//     console's namespace hold is reviewed for the person and each allowed one
//     probed for a row as in 1. An object in a namespace they may not list
//     does not count. Past access.NarrowedNamespaces, NarrowedReads or this
//     request's budgets, `unknown`.
//
// On the desktop the person is the console, so 2 answers from 1's review.
// Cost per cluster and occupancyTTL: one review and one one-row list per kind,
// occupancyInFlight at a time; per narrowed person and request, at most
// occupancyReviews reviews and occupancyProbes lists, most of them held.

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/customresources"
	"github.com/thonicdev/kubetower/internal/resources"
	"github.com/thonicdev/kubetower/safego"
)

const (
	// occupancyTTL is how long the console's answer about a kind is served, so a
	// kind's first object appears at most this late; the list page is read live.
	occupancyTTL = 3 * time.Minute
	// occupancyFailedTTL is how long an unknown is believed: short, because a
	// refusal is what somebody is fixing.
	occupancyFailedTTL = 15 * time.Second
	// occupancyWait bounds one request. A kind not settled by then is unknown; what
	// was read is held for the next request.
	occupancyWait = 10 * time.Second
	// occupancyInFlight is how many kinds one request reads at once, as many as
	// access.ReviewsInFlight. Each kind is two round trips one after the other (a
	// review, then a one-row list), so on a cluster serving a hundred kinds this
	// number, not the network, sets how long a first opening waits: at four, a
	// local cluster's answer took about twice as long. A person's read meets at
	// most this many of the column's requests ahead of it at the shared
	// per-cluster limiter, 160 ms at its steady rate.
	occupancyInFlight = 8
	// occupancyReviews is how many per-namespace reviews one request may ask for a
	// narrowed person, every kind together; occupancyProbes how many one-row lists in
	// a namespace. The gate and the hold keep most answers for the next request.
	occupancyReviews = 256
	occupancyProbes  = 64
)

// occupancyKind is one kind the menu offers: its slug, the last segment of
// its menu entry's href, and the question its list is reviewed with.
type occupancyKind struct {
	slug     string
	question resources.Question
}

// occupancyKey is one held answer: a kind on a cluster, in a namespace or ""
// for every namespace.
type occupancyKey struct {
	cluster, slug, namespace string
}

// occupancyAnswer is the console's answer about one key.
type occupancyAnswer struct {
	state  api.Occupancy
	reason string
	at     time.Time
	// passing is an answer about this request (its context ended), never held.
	passing bool
}

func (a occupancyAnswer) ttl() time.Duration {
	if a.state == api.OccupancyUnknown {
		return occupancyFailedTTL
	}
	return occupancyTTL
}

// occupancyHold is the console's answers, shared by every person: filled as
// the console, and what each person is answered from it is their own review.
type occupancyHold struct {
	mu   sync.Mutex
	held map[occupancyKey]occupancyAnswer
	// asking is the keys being read now, so a second request waits for the first.
	asking map[occupancyKey]chan struct{}
	now    func() time.Time
	// lastSwept is when expired answers were last dropped (sweep).
	lastSwept time.Time
}

// sweep drops every held answer past its own TTL, at most once per
// occupancyFailedTTL, so keys for namespaces, kinds and contexts that are gone
// do not stay for the life of the process. Called with the lock held. An
// expired answer is never served, so no answer changes.
func (h *occupancyHold) sweep() {
	now := h.now()
	if !h.lastSwept.IsZero() && now.Sub(h.lastSwept) < occupancyFailedTTL {
		return
	}
	h.lastSwept = now
	for key, a := range h.held {
		if now.Sub(a.at) >= a.ttl() {
			delete(h.held, key)
		}
	}
}

func newOccupancyHold() *occupancyHold {
	return &occupancyHold{
		held:   map[occupancyKey]occupancyAnswer{},
		asking: map[occupancyKey]chan struct{}{},
		now:    time.Now,
	}
}

// occupancyBudget is what one request may still ask for a narrowed person.
type occupancyBudget struct {
	mu              sync.Mutex
	reviews, probes int
}

// takeReviews reserves n reviews, all or none.
func (b *occupancyBudget) takeReviews(n int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > b.reviews {
		return false
	}
	b.reviews -= n
	return true
}

func (b *occupancyBudget) takeProbe() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.probes == 0 {
		return false
	}
	b.probes--
	return true
}

// NavOccupancy answers the operation: every kind the menu offers, keyed by
// slug, for the person behind ctx.
//
// The CRD list is the slow read of the three - the whole objects, schemas
// included, decoded - so it is started at once, beside discovery, and the
// kinds discovery names are counted while it is still being read. The custom
// resource kinds join the same occupancyInFlight slots when it lands. Each
// batch keeps the bound it had when they ran one after the other: occupancyWait
// from when its kinds are known.
func (c apiClusters) NavOccupancy(ctx context.Context, name string) (api.NavOccupancy, *api.Failure) {
	s := c.s
	client, err := s.clusters.Client(name)
	if err != nil {
		return api.NavOccupancy{}, stateOf(err, api.Scope{Cluster: name, Verb: "list"})
	}

	listed := make(chan customListed, 1)
	safego.Go("occupancy: listing the custom resource kinds", func() error {
		listed <- s.occupancyDefinitions(ctx, client)
		return nil
	}, func(err error) {
		if err != nil {
			// A panic before the send: said as a list that could not be made.
			listed <- customListed{err: err}
		}
	})

	run := s.newOccupancyRun(name, client)
	served := s.occupancyServedKinds(ctx, client)
	counted := make(chan []api.KindOccupancy, 1)
	safego.Go("occupancy: the kinds discovery names", func() error {
		counted <- run.answer(ctx, served)
		return nil
	}, func(err error) {
		if err != nil {
			counted <- unknownFor(served, err)
		}
	})

	custom, note := customOccupancyKinds(<-listed, served)
	customAnswers := run.answer(ctx, custom)
	servedAnswers := <-counted

	out := make(map[string]api.KindOccupancy, len(served)+len(custom))
	for i, k := range served {
		out[k.slug] = servedAnswers[i]
	}
	for i, k := range custom {
		out[k.slug] = customAnswers[i]
	}
	return api.NavOccupancy{Kinds: out, Note: note}, nil
}

// occupancyKinds is the kinds the menu offers on this cluster, as nav builds
// them: the typed kinds discovery does not rule out, the project's own kinds it
// serves, and the third-party CRDs. note says why the CRDs are missing when they
// could not be listed. The in-cluster menu's access review is not applied: a
// refused read answers unknown on its own. NavOccupancy reads the same two
// halves side by side; this is them one after the other.
func (s *Server) occupancyKinds(ctx context.Context, name string, client *cluster.Client) ([]occupancyKind, string) {
	served := s.occupancyServedKinds(ctx, client)
	custom, note := customOccupancyKinds(s.occupancyDefinitions(ctx, client), served)
	return append(served, custom...), note
}

// occupancyServedKinds is the half discovery answers: the typed kinds it does
// not rule out and the project's own kinds it serves.
func (s *Server) occupancyServedKinds(ctx context.Context, client *cluster.Client) []occupancyKind {
	var unserved map[string]bool
	var project []customresources.Definition
	wait, cancel := context.WithTimeout(ctx, projectNavWait)
	found, err := s.custom.ProjectKinds(wait, client)
	cancel()
	if err == nil {
		// Unserved is only what discovery stated.
		unserved = make(map[string]bool, len(found.Unserved))
		for _, kind := range found.Unserved {
			unserved[kind] = true
		}
		project = found.Kinds
	}

	var out []occupancyKind
	seen := map[string]bool{}
	add := func(slug string, q resources.Question) {
		if !seen[slug] {
			seen[slug] = true
			out = append(out, occupancyKind{slug: slug, question: q})
		}
	}
	for _, d := range resources.Kinds() {
		if q, ok := d.ListQuestion(); ok && !unserved[d.Kind] {
			add(d.Kind, q)
		}
	}
	for _, d := range project {
		add(d.Slug(), definitionQuestion(d))
	}
	return out
}

// customListed is the CRD list's answer, carried from the goroutine that reads
// it.
type customListed struct {
	definitions []customresources.Definition
	err         error
}

// occupancyDefinitions lists the cluster's CRDs, within customNavDeadline.
func (s *Server) occupancyDefinitions(ctx context.Context, client *cluster.Client) customListed {
	wait, cancel := context.WithTimeout(ctx, customNavDeadline)
	defer cancel()
	definitions, err := s.custom.Definitions(wait, client)
	return customListed{definitions: definitions, err: err}
}

// customOccupancyKinds is the half the CRD list answers: the third-party kinds
// not already among served. note says why they are missing when they could not
// be listed.
func customOccupancyKinds(listed customListed, served []occupancyKind) ([]occupancyKind, string) {
	if listed.err != nil {
		return nil, "The custom resource kinds could not be listed, so whether they hold objects is not known: " +
			firstLine(listed.err.Error())
	}
	seen := make(map[string]bool, len(served))
	for _, k := range served {
		seen[k.slug] = true
	}
	var out []occupancyKind
	for _, d := range thirdParty(listed.definitions) {
		if slug := d.Slug(); !seen[slug] {
			seen[slug] = true
			out = append(out, occupancyKind{slug: slug, question: definitionQuestion(d)})
		}
	}
	return out, ""
}

// definitionQuestion is the review a custom resource kind's list across every
// namespace is asked with - the question its list page asks.
func definitionQuestion(d customresources.Definition) resources.Question {
	gvr := d.GVR()
	if d.Namespaced {
		return resources.InNamespace(gvr.Group, gvr.Version, gvr.Resource, "")
	}
	return resources.ClusterScoped(gvr.Group, gvr.Version, gvr.Resource)
}

// occupancyRun is one request's reading: the slots every batch of its kinds
// shares, so two batches side by side still read occupancyInFlight kinds at a
// time, its budget for a narrowed person, and the console's namespaces, read
// at most once and only when a narrowed person needs them.
type occupancyRun struct {
	s      *Server
	name   string
	client *cluster.Client
	slots  chan struct{}
	budget *occupancyBudget

	namesOnce sync.Once
	names     namespaceAnswer
}

func (s *Server) newOccupancyRun(name string, client *cluster.Client) *occupancyRun {
	return &occupancyRun{
		s: s, name: name, client: client,
		slots:  make(chan struct{}, occupancyInFlight),
		budget: &occupancyBudget{reviews: occupancyReviews, probes: occupancyProbes},
	}
}

// namespaces is the console's namespaces, read by the first batch that needs
// them, within that batch's bound; a second batch waits for that read.
func (r *occupancyRun) namespaces(ctx context.Context) namespaceAnswer {
	r.namesOnce.Do(func() { r.names = r.s.namespaceAnswer(ctx, r.name) })
	return r.names
}

// answer is each of kinds for the person behind ctx, in order, within
// occupancyWait from now.
func (r *occupancyRun) answer(ctx context.Context, kinds []occupancyKind) []api.KindOccupancy {
	s := r.s
	answers := make([]api.KindOccupancy, len(kinds))
	if s.gate == nil || s.occupancy == nil {
		for i := range kinds {
			answers[i] = api.KindOccupancy{State: api.OccupancyUnknown, Reason: "this console reads nothing it cannot review"}
		}
		return answers
	}
	ctx, cancel := context.WithTimeout(ctx, occupancyWait)
	defer cancel()
	names := func() namespaceAnswer { return r.namespaces(ctx) }

	var wg sync.WaitGroup
	for i := range kinds {
		wg.Add(1)
		r.slots <- struct{}{}
		// Through safego: a panic is that kind's answer, unknown, never a hidden kind.
		safego.Go("occupancy: one kind", func() error {
			answers[i] = s.kindOccupancy(ctx, r.name, r.client, kinds[i], r.budget, names)
			return nil
		}, func(err error) {
			if err != nil {
				answers[i] = api.KindOccupancy{State: api.OccupancyUnknown, Reason: firstLine(err.Error())}
			}
			<-r.slots
			wg.Done()
		})
	}
	wg.Wait()
	return answers
}

// unknownFor answers every kind unknown, for a batch whose reading panicked.
func unknownFor(kinds []occupancyKind, err error) []api.KindOccupancy {
	out := make([]api.KindOccupancy, len(kinds))
	for i := range out {
		out[i] = unknownOccupancy(err.Error())
	}
	return out
}

// occupancyOf answers each kind for the person behind ctx, occupancyInFlight
// at a time, within occupancyWait: one batch of one run.
func (s *Server) occupancyOf(ctx context.Context, name string, client *cluster.Client, kinds []occupancyKind) map[string]api.KindOccupancy {
	answers := s.newOccupancyRun(name, client).answer(ctx, kinds)
	out := make(map[string]api.KindOccupancy, len(kinds))
	for i, k := range kinds {
		out[k.slug] = answers[i]
	}
	return out
}

// kindOccupancy is one kind's answer for the person behind ctx (steps 1 to 3 in
// the file's header).
func (s *Server) kindOccupancy(ctx context.Context, name string, client *cluster.Client, k occupancyKind,
	budget *occupancyBudget, names func() namespaceAnswer) api.KindOccupancy {
	held := s.consoleOccupancy(ctx, client, k, "")
	if held.state != api.OccupancySome {
		return api.KindOccupancy{State: held.state, Reason: held.reason}
	}

	wide := k.question.Attrs
	wide.Namespace = ""
	answer := s.gate.Review(ctx, client.Context, wide)
	switch {
	case answer.Allowed() || s.gate.LetsThrough(answer):
		return api.KindOccupancy{State: api.OccupancySome}
	case answer.Decision != access.Denied:
		return unknownOccupancy("whether you may list it could not be reviewed")
	case !k.question.Namespaced:
		// Held, and not theirs to see.
		return api.KindOccupancy{State: api.OccupancyNone}
	}
	return s.narrowOccupancy(ctx, client, k, budget, names())
}

// narrowOccupancy is step 3: a person denied the list across the cluster, asked
// per namespace the console knows of.
func (s *Server) narrowOccupancy(ctx context.Context, client *cluster.Client, k occupancyKind,
	budget *occupancyBudget, held namespaceAnswer) api.KindOccupancy {
	if held.failed || len(held.names) == 0 {
		return unknownOccupancy("the namespaces you may list it in could not be read")
	}
	names := append([]string(nil), held.names...)
	sort.Strings(names)
	if len(names) > access.NarrowedNamespaces {
		return unknownOccupancy("this cluster has " + strconv.Itoa(len(names)) +
			" namespaces, more than the " + strconv.Itoa(access.NarrowedNamespaces) +
			" this console reviews you for one at a time")
	}
	if !budget.takeReviews(len(names)) {
		return unknownOccupancy("past what this console asks for one answer; it is asked again on the next")
	}
	questions := make([]access.Attributes, len(names))
	for i, ns := range names {
		questions[i] = k.question.Attrs
		questions[i].Namespace = ns
	}
	var allowed []string
	unreviewed := false
	for i, answer := range s.gate.ReviewAll(ctx, client.Context, questions) {
		switch {
		case answer.Allowed() || s.gate.LetsThrough(answer):
			allowed = append(allowed, names[i])
		case answer.Decision != access.Denied:
			unreviewed = true
		}
	}

	unread := ""
	for i, ns := range allowed {
		if i >= access.NarrowedReads {
			unread = "you may list it in more namespaces than the " + strconv.Itoa(access.NarrowedReads) +
				" this console reads one at a time"
			break
		}
		if !budget.takeProbe() {
			unread = "past what this console reads for one answer; it is asked again on the next"
			break
		}
		got := s.consoleOccupancy(ctx, client, k, ns)
		switch got.state {
		case api.OccupancySome:
			return api.KindOccupancy{State: api.OccupancySome}
		case api.OccupancyUnknown:
			unread = got.reason
		}
	}
	switch {
	case unread != "":
		return unknownOccupancy(unread)
	case unreviewed:
		return unknownOccupancy("whether you may list it in some namespaces could not be reviewed")
	}
	return api.KindOccupancy{State: api.OccupancyNone}
}

func unknownOccupancy(reason string) api.KindOccupancy {
	return api.KindOccupancy{State: api.OccupancyUnknown, Reason: firstLine(reason)}
}

// consoleOccupancy is the console's held answer about a kind in namespace -
// "" for every namespace - read when it is not held or past its TTL.
func (s *Server) consoleOccupancy(ctx context.Context, client *cluster.Client, k occupancyKind, namespace string) occupancyAnswer {
	h := s.occupancy
	key := occupancyKey{cluster: client.Context, slug: k.slug, namespace: namespace}
	for {
		h.mu.Lock()
		if a, ok := h.held[key]; ok && h.now().Sub(a.at) < a.ttl() {
			h.mu.Unlock()
			return a
		}
		if wait, ok := h.asking[key]; ok {
			h.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return occupancyAnswer{state: api.OccupancyUnknown, reason: "not read in time", passing: true}
			}
		}
		done := make(chan struct{})
		h.asking[key] = done
		h.mu.Unlock()
		return s.readOccupancy(ctx, client, k, namespace, key, done)
	}
}

// readOccupancy reads the key and files the answer, deferred so a read that
// panics still releases whoever waits on it.
func (s *Server) readOccupancy(ctx context.Context, client *cluster.Client, k occupancyKind, namespace string,
	key occupancyKey, done chan struct{}) (a occupancyAnswer) {
	h := s.occupancy
	a = occupancyAnswer{state: api.OccupancyUnknown, reason: "the read did not finish", passing: true}
	defer func() {
		h.mu.Lock()
		delete(h.asking, key)
		if !a.passing {
			a.at = h.now()
			h.held[key] = a
			h.sweep()
		}
		h.mu.Unlock()
		close(done)
	}()
	a = s.probeOccupancy(ctx, client, k, namespace)
	return a
}

// probeOccupancy reads one row of the kind as the console: the hold is every
// person's, and each person is reviewed on the way out (kindOccupancy).
func (s *Server) probeOccupancy(ctx context.Context, client *cluster.Client, k occupancyKind, namespace string) occupancyAnswer {
	attrs := k.question.Attrs
	attrs.Namespace = namespace
	found := false
	_, err := s.gate.Read(access.AsConsole(ctx), client.Context, attrs, func(ctx context.Context) error {
		var err error
		found, err = resources.AnyObject(ctx, client.Clients, k.question, namespace)
		return err
	})
	var refused *access.Refused
	switch {
	case err == nil && found:
		return occupancyAnswer{state: api.OccupancySome}
	case err == nil:
		return occupancyAnswer{state: api.OccupancyNone}
	case ctx.Err() != nil:
		return occupancyAnswer{state: api.OccupancyUnknown, reason: "not read in time", passing: true}
	case errors.As(err, &refused) && refused.Answer.Decision == access.Denied:
		return occupancyAnswer{state: api.OccupancyUnknown,
			reason: "this console may not list it: " + grantedClause + "it"}
	}
	return occupancyAnswer{state: api.OccupancyUnknown, reason: firstLine(err.Error())}
}

package access

// What a person reads goes through the same review as what they write.
//
// Under the server profile the console reads with one ServiceAccount that may
// read nearly everything (docs/SERVER-MODE-THREAT-MODEL.md §5.1). Shown
// unfiltered, a person with a RoleBinding in one namespace would see every
// namespace, Helm release Secrets included. So reads are filtered on the way
// out by the same review the writes go through.
//
//   - Read: one thing (an object by name, a list in one namespace, a log).
//     The name is part of the question for an object read by name, because
//     RBAC's `resourceNames` applies to `get`: a review without it would
//     answer about the whole kind (§5.3).
//   - List: a list that may cross namespaces. When the whole list is denied
//     for a namespaced kind, it is read once and filtered per namespace, one
//     review each. `resourceNames` does not apply to `list`, so this neither
//     under- nor over-grants.
//   - Each: List for a read that visits its rows as they arrive, handed a keep.
//   - Keep: the same filter for rows not read through a client (a ranking out
//     of Prometheus names pods).
//   - Check: the review alone, before reading anything.
//
// Visible.Narrowed is set from the question, never from the data: a count of
// what was left out would tell a person how many namespaces hold rows they
// may not see.
//
// A cache shared by every person must not be filled with one person's
// filtered answer and served to the next, so it is read as the console
// (AsConsole) and reviewed for each person on the way out.

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// readVerbs are the verbs a read door accepts. A write handed to a read door is
// a programming error, and it is refused rather than reviewed as a read.
var readVerbs = map[string]bool{"get": true, "list": true, "watch": true}

// Read reviews one read and runs do only when the answer lets it go - Allowed,
// or the one answer LetsThrough names (on the desktop, a review of the
// console's own credentials that could not be made).
//
// It answers the review and do's own error. A review that did not let the read
// go is a *Refused, and do is not called.
func (g *Gate) Read(ctx context.Context, cluster string, attrs Attributes, do func(context.Context) error) (Answer, error) {
	if !readVerbs[attrs.Verb] {
		return Answer{Decision: Unreviewable, Subject: Nobody},
			fmt.Errorf("a read was asked to review the verb %q; a read is get, list or watch", attrs.Verb)
	}
	answer := g.Review(ctx, cluster, attrs)
	if !g.lets(answer) {
		return answer, &Refused{Cluster: cluster, Attributes: attrs, Answer: answer}
	}
	return answer, do(ctx)
}

// Check is the review alone, answered as Read would refuse: nil when the
// answer lets the request go, a *Refused when it does not. For a page that
// must establish a person may read something before it reads anything - a
// selection of namespaces, or a figure about an object that is not itself
// read through a client.
func (g *Gate) Check(ctx context.Context, cluster string, attrs Attributes) error {
	answer := g.Review(ctx, cluster, attrs)
	if !g.lets(answer) {
		return &Refused{Cluster: cluster, Attributes: attrs, Answer: answer}
	}
	return nil
}

// lets is whether an answer lets a request go.
func (g *Gate) lets(a Answer) bool { return a.Allowed() || g.LetsThrough(a) }

// Visible is what a list reviewed for a person says about itself beyond its
// rows.
type Visible struct {
	// Narrowed is a list the person may not make across every namespace: it
	// holds the rows of the namespaces they may list, and only those. Set from
	// the question and never from the rows, so it says nothing about what the
	// other namespaces hold.
	Narrowed bool
	// Unreviewed is a namespace whose review could not be made; its rows were
	// withheld rather than shown on an answer nobody gave.
	Unreviewed bool
	// Asked is the question the list was reviewed with - the namespace empty
	// for "every namespace". Zero when nothing was asked.
	Asked Attributes
}

// Filtered is whether anything about the list differs from what the cluster
// holds.
func (v Visible) Filtered() bool { return v.Narrowed || v.Unreviewed }

// Merge is two lists' Visible as one page's: filtered if either was.
func (v Visible) Merge(o Visible) Visible {
	out := Visible{Narrowed: v.Narrowed || o.Narrowed, Unreviewed: v.Unreviewed || o.Unreviewed, Asked: v.Asked}
	if out.Asked == (Attributes{}) {
		out.Asked = o.Asked
	}
	return out
}

// List reviews a list for the person behind ctx and answers what they may see
// of it.
//
// namespaceOf says where a row lives; nil means the kind is cluster-scoped,
// so there is nothing to narrow to and a denial refuses the list.
//
//   - The whole list allowed (or let through): answered as read.
//   - One namespace asked, a cluster-scoped kind, or a review that could not
//     be made: refused. A list nobody could review is not narrowed to the
//     namespaces whose review happened to work.
//   - Every namespace asked and denied: read once with the console's
//     credentials and narrowed to the namespaces the person may list, the
//     reviews asked together.
//
// read runs at most once, and never before the first answer is known.
//
// A list read to be narrowed is bounded (NarrowedRows, NarrowedNamespaces):
// unbounded, a person with namespaced rights would cost the whole cluster's
// list and one review per namespace on every refresh, through the limiter
// every person shares. Past either bound they are asked for a namespace.
func List[T any](ctx context.Context, g *Gate, cluster string, attrs Attributes,
	namespaceOf func(*T) string, read func(context.Context) ([]T, error)) ([]T, Visible, error) {
	whole, err := g.across(ctx, cluster, attrs, namespaceOf != nil)
	if err != nil {
		return nil, Visible{Asked: attrs}, err
	}
	if whole {
		items, err := read(ctx)
		return items, Visible{Asked: attrs}, err
	}
	items, err := read(context.WithValue(ctx, narrowingKey{}, true))
	if err != nil {
		var tooLarge *tooLargeToNarrow
		if errors.As(err, &tooLarge) {
			return nil, Visible{Asked: attrs}, narrowRefused(cluster, attrs,
				fmt.Sprintf("this list holds more than %d rows", NarrowedRows))
		}
		return items, Visible{Asked: attrs}, err
	}
	return narrow(ctx, g, cluster, attrs, items, namespaceOf)
}

// ListIn is List for a kind whose rows must not be read where the person may
// not list it: Secrets, whose list carries every value. List narrows after
// reading the whole cluster with the console's credentials, which for Secrets
// would read their data to show a person the few they may see. ListIn asks
// first and reads after:
//
//   - Allowed, one namespace asked, or unreviewable: as List.
//   - Every namespace asked and denied: namespaces lists where rows are, from
//     a read that carries no content (a metadata-only list); each is reviewed,
//     and read runs only in the namespaces the person may list.
//
// The bounds are List's plus NarrowedReads, the most namespaces read one at
// a time. Past any of them the list is NotNarrowed.
func ListIn[T any](ctx context.Context, g *Gate, cluster string, attrs Attributes,
	namespaces func(context.Context) ([]string, error),
	read func(ctx context.Context, namespace string) ([]T, error)) ([]T, Visible, error) {
	whole, err := g.across(ctx, cluster, attrs, true)
	if err != nil {
		return nil, Visible{Asked: attrs}, err
	}
	if whole {
		items, err := read(ctx, attrs.Namespace)
		return items, Visible{Asked: attrs}, err
	}
	narrowing := context.WithValue(ctx, narrowingKey{}, true)
	tooLarge := func(err error) error {
		var over *tooLargeToNarrow
		if errors.As(err, &over) {
			return narrowRefused(cluster, attrs, fmt.Sprintf("this list holds more than %d rows", NarrowedRows))
		}
		return err
	}
	found, err := namespaces(narrowing)
	if err != nil {
		return nil, Visible{Asked: attrs}, tooLarge(err)
	}
	seen := map[string]bool{}
	var names []string
	for _, ns := range found {
		if ns != "" && !seen[ns] {
			seen[ns] = true
			names = append(names, ns)
		}
	}
	sort.Strings(names)
	if len(names) > NarrowedNamespaces {
		return nil, Visible{Asked: attrs}, narrowRefused(cluster, attrs,
			fmt.Sprintf("this list spans %d namespaces, more than the %d", len(names), NarrowedNamespaces))
	}
	questions := make([]Attributes, len(names))
	for i, ns := range names {
		questions[i] = attrs
		questions[i].Namespace = ns
	}
	visible := Visible{Narrowed: true, Asked: attrs}
	var allowed []string
	for i, answer := range g.ReviewAll(ctx, cluster, questions) {
		switch {
		case g.lets(answer):
			allowed = append(allowed, names[i])
		case answer.Decision != Denied:
			visible.Unreviewed = true
		}
	}
	if len(allowed) > NarrowedReads {
		return nil, Visible{Asked: attrs}, narrowRefused(cluster, attrs,
			fmt.Sprintf("you may list it in %d namespaces, more than the %d this console reads one at a time",
				len(allowed), NarrowedReads))
	}
	kept := make([]T, 0)
	for _, ns := range allowed {
		items, err := read(narrowing, ns)
		if err != nil {
			return nil, Visible{Asked: attrs}, tooLarge(err)
		}
		kept = append(kept, items...)
		if len(kept) > NarrowedRows {
			return nil, Visible{Asked: attrs}, narrowRefused(cluster, attrs,
				fmt.Sprintf("this list holds more than %d rows", NarrowedRows))
		}
	}
	return kept, visible, nil
}

// ListableIn reviews a list question - one denied across every namespace -
// in each of namespaces, together, and answers the ones the person may list
// it in, sorted. complete is false where a review could not be made, or there
// are more namespaces than NarrowedNamespaces and none was asked: allowed is
// then not the whole answer, and nothing may be concluded from what it lacks.
//
// namespaces is every namespace of the cluster, not those a narrowed read
// found rows in: only the whole set tells a person whose namespaces are empty
// from one who may list the kind nowhere.
//
// A denial of every one is answered as a refusal, a *Refused with Nowhere set:
// "you may not list secrets in any namespace", never an empty list.
func (g *Gate) ListableIn(ctx context.Context, cluster string, attrs Attributes,
	namespaces []string) (allowed []string, complete bool, err error) {
	names := make([]string, 0, len(namespaces))
	seen := map[string]bool{}
	for _, ns := range namespaces {
		if ns != "" && !seen[ns] {
			seen[ns] = true
			names = append(names, ns)
		}
	}
	sort.Strings(names)
	if len(names) == 0 || len(names) > NarrowedNamespaces {
		return nil, false, nil
	}
	questions := make([]Attributes, len(names))
	for i, ns := range names {
		questions[i] = attrs
		questions[i].Namespace = ns
	}
	complete = true
	// Whose denial, without the authorizer's reason: that was given about one
	// namespace, and the refusal is about all of them.
	denial := Answer{Decision: Denied}
	for i, answer := range g.ReviewAll(ctx, cluster, questions) {
		switch {
		case g.lets(answer):
			allowed = append(allowed, names[i])
		case answer.Decision != Denied:
			complete = false
		default:
			denial.Subject = answer.Subject
		}
	}
	if complete && len(allowed) == 0 {
		wide := attrs
		wide.Namespace = ""
		return nil, true, &Refused{Cluster: cluster, Attributes: wide, Answer: denial, Nowhere: true}
	}
	return allowed, complete, nil
}

// NarrowedReads is the most namespaces ListIn reads one at a time for one
// list: twenty, what List's narrowing costs at NarrowedRows in pages of 500.
const NarrowedReads = 20

// NarrowedRows is the most rows a list read only to be narrowed may gather, and
// NarrowedNamespaces the most namespaces it is narrowed across, one review
// each. Past either the list is refused with a sentence asking for a namespace.
// Ten thousand rows is twenty pages; a hundred reviews a minute is under two a
// second on a limiter of fifty.
const (
	NarrowedRows       = 10000
	NarrowedNamespaces = 100
)

type narrowingKey struct{}

// Narrowing is whether ctx is the read of a list that is about to be narrowed
// to a person's namespaces. resources.Paged reads it to stop at NarrowedRows.
func Narrowing(ctx context.Context) bool {
	narrowing, _ := ctx.Value(narrowingKey{}).(bool)
	return narrowing
}

// TooLargeToNarrow is the error a narrowing read answers once it holds more
// than NarrowedRows. List turns it into the refusal a person reads.
func TooLargeToNarrow() error { return &tooLargeToNarrow{} }

type tooLargeToNarrow struct{}

func (*tooLargeToNarrow) Error() string {
	return fmt.Sprintf("a list read to be narrowed holds more than %d rows", NarrowedRows)
}

// narrowRefused is a list the console declined to narrow.
func narrowRefused(cluster string, attrs Attributes, why string) error {
	return &NotNarrowed{Cluster: cluster, Attributes: attrs, Why: why}
}

// NotNarrowed is a list across every namespace that this console declined to
// narrow to the person's namespaces, past NarrowedRows or NarrowedNamespaces.
//
// Its own type, not a Refused: nobody said no, and every review was answered,
// so it is not the retryable "could not be made" that the list page would
// re-poll (repeating the whole read each time). It is the console's own
// ceiling, the same until the person chooses a namespace; internal/web
// answers it as `not_supported_here`, not retryable.
type NotNarrowed struct {
	Cluster    string
	Attributes Attributes
	Why        string
}

func (n *NotNarrowed) Error() string {
	return n.Why + ", too many for this console to narrow to the namespaces you may list; choose a namespace"
}

// Keep is List for rows that were not read through a client - a ranking out of
// a metrics backend names pods too, and a person is shown the ones in the
// namespaces they may list.
func Keep[T any](ctx context.Context, g *Gate, cluster string, attrs Attributes,
	items []T, namespaceOf func(*T) string) ([]T, Visible, error) {
	whole, err := g.across(ctx, cluster, attrs, namespaceOf != nil)
	if err != nil {
		return nil, Visible{Asked: attrs}, err
	}
	if whole {
		return items, Visible{Asked: attrs}, nil
	}
	return narrow(ctx, g, cluster, attrs, items, namespaceOf)
}

// Each is List for a read that visits its rows as they arrive: a walk that
// stops at a byte budget must count what the cluster sent, not what the
// person is shown, so the filter is handed to the read as keep. keep is for
// the read's own goroutine, and asks one review per namespace when first met.
//
// A narrowed walk has List's bounds: it runs under the NarrowedRows context
// and asks at most NarrowedNamespaces reviews; past that, the read's context
// is canceled and the walk is refused with NotNarrowed. Unlike List it cannot
// count namespaces before it starts, so it stops asking instead.
func Each[T any](ctx context.Context, g *Gate, cluster string, attrs Attributes,
	namespaceOf func(*T) string, read func(ctx context.Context, keep func(*T) bool) error) (Visible, error) {
	whole, err := g.across(ctx, cluster, attrs, namespaceOf != nil)
	if err != nil {
		return Visible{Asked: attrs}, err
	}
	visible := Visible{Asked: attrs}
	if whole {
		return visible, read(ctx, func(*T) bool { return true })
	}
	visible.Narrowed = true
	reading, stop := context.WithCancel(context.WithValue(ctx, narrowingKey{}, true))
	defer stop()
	answers := map[string]bool{}
	tooMany := false
	keep := func(item *T) bool {
		ns := namespaceOf(item)
		if ns == "" {
			return false
		}
		if ok, asked := answers[ns]; asked {
			return ok
		}
		if tooMany || len(answers) >= NarrowedNamespaces {
			tooMany = true
			stop()
			return false
		}
		q := attrs
		q.Namespace = ns
		answer := g.Review(ctx, cluster, q)
		ok := g.lets(answer)
		if !ok && answer.Decision != Denied {
			visible.Unreviewed = true
		}
		answers[ns] = ok
		return ok
	}
	err = read(reading, keep)
	if tooMany {
		return Visible{Asked: attrs}, narrowRefused(cluster, attrs,
			fmt.Sprintf("this list spans more than %d namespaces", NarrowedNamespaces))
	}
	var tooLarge *tooLargeToNarrow
	if errors.As(err, &tooLarge) {
		return Visible{Asked: attrs}, narrowRefused(cluster, attrs,
			fmt.Sprintf("this list holds more than %d rows", NarrowedRows))
	}
	return visible, err
}

// across asks the list's own question. whole says the rows are the person's as
// they come; an error refuses the list; neither is a list to narrow.
func (g *Gate) across(ctx context.Context, cluster string, attrs Attributes, namespaced bool) (bool, error) {
	if attrs.Verb != "list" {
		return false, fmt.Errorf("a list was asked to review the verb %q", attrs.Verb)
	}
	answer := g.Review(ctx, cluster, attrs)
	switch {
	case g.lets(answer):
		return true, nil
	case answer.Decision == Denied && attrs.Namespace == "" && namespaced:
		return false, nil
	}
	return false, &Refused{Cluster: cluster, Attributes: attrs, Answer: answer}
}

// narrow keeps the rows of the namespaces the person may list, asking once per
// namespace, together, so forty namespaces are one wait. It returns a new
// slice. A row with no namespace in a namespaced list falls under the list's
// own question, a denial, and is withheld.
func narrow[T any](ctx context.Context, g *Gate, cluster string, attrs Attributes,
	items []T, namespaceOf func(*T) string) ([]T, Visible, error) {
	visible := Visible{Narrowed: true, Asked: attrs}
	seen := map[string]bool{}
	var namespaces []string
	for i := range items {
		if ns := namespaceOf(&items[i]); ns != "" && !seen[ns] {
			seen[ns] = true
			namespaces = append(namespaces, ns)
		}
	}
	sort.Strings(namespaces)
	if len(namespaces) > NarrowedNamespaces {
		return nil, Visible{Asked: attrs}, narrowRefused(cluster, attrs,
			fmt.Sprintf("this list spans %d namespaces, more than the %d", len(namespaces), NarrowedNamespaces))
	}
	questions := make([]Attributes, len(namespaces))
	for i, ns := range namespaces {
		q := attrs
		q.Namespace = ns
		questions[i] = q
	}
	allowed := make(map[string]bool, len(namespaces))
	for i, answer := range g.ReviewAll(ctx, cluster, questions) {
		switch {
		case g.lets(answer):
			allowed[namespaces[i]] = true
		case answer.Decision != Denied:
			visible.Unreviewed = true
		}
	}
	kept := make([]T, 0, len(items))
	for i := range items {
		if allowed[namespaceOf(&items[i])] {
			kept = append(kept, items[i])
		}
	}
	return kept, visible, nil
}

// consoleKey marks a context as the console acting for itself.
type consoleKey struct{}

// AsConsole marks ctx as the console reading for itself (to fill a hold every
// person is served from, or to reach its own monitoring backend), so the gate
// reviews it as the console's own credentials on every profile.
//
// It is the one way to read with the console's rights under the server
// profile, so every use is listed with its reason in choke_test.go, which
// fails on a new one. What a person is shown from such a hold is reviewed for
// them on the way out.
func AsConsole(ctx context.Context) context.Context {
	return context.WithValue(ctx, consoleKey{}, true)
}

// actingAsConsole is whether ctx went through AsConsole.
func actingAsConsole(ctx context.Context) bool {
	marked, _ := ctx.Value(consoleKey{}).(bool)
	return marked
}

package web

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/resources"
)

// The namespace list is held per cluster for namespaceTTL: list pages are redrawn
// every few seconds and the list rarely changes. A namespace already in the URL
// is always accepted, so one the hold has not seen yet still works.

// namespaceTTL is how long a cluster's namespace list is reused. Namespaces move
// in minutes, and being a minute behind costs one missing dropdown entry.
const namespaceTTL = time.Minute

// namespaceFailureTTL is how long a failed fetch is believed, as in
// `internal/health` and `internal/metrics`: a cluster that just refused is
// usually being fixed (SSO renewed, VPN back), so it is asked again soon, but not
// on every row refresh of a page that is already failing.
const namespaceFailureTTL = 5 * time.Second

// namespaceHold is what is remembered for one cluster.
type namespaceHold struct {
	names []string
	// at is when this hold was recorded; the TTL is measured from it.
	at time.Time
	// readAt is when the names were read, which differs from at once a reading is
	// carried forward past a failure; zero when nothing was read. Kept apart because
	// a carried-forward hold stamped with the old moment would be instantly expired
	// and re-asked on every render.
	readAt time.Time
	// failed marks a fetch that did not answer, which shortens the hold (ttl): an
	// empty dropdown and one that could not be built are different answers.
	failed bool
}

// ttl is how long this hold is worth serving; a method so the asymmetry can be
// tested without an unreachable API server.
func (h namespaceHold) ttl() time.Duration {
	if h.failed {
		return namespaceFailureTTL
	}
	return namespaceTTL
}

type namespaceCache struct {
	mu   sync.Mutex
	held map[string]namespaceHold
}

func newNamespaceCache() *namespaceCache {
	return &namespaceCache{held: map[string]namespaceHold{}}
}

// The selection is a set, carried as one comma-joined `ns` parameter. A namespace
// is a DNS-1123 label, so a comma cannot occur in one; a link naming one
// namespace is unchanged and `encode` stays a single Set.

// parseNamespaces reads the `ns` parameter into a selection, trimmed, without
// empties, deduplicated and sorted, so selections compare by their joined string.
func parseNamespaces(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// namespaceAnswer is what the hold had to say, as two facts: names alone cannot
// tell an empty answer from a failed one, and a cluster always has `default` and
// `kube-system`, while "could not be asked" is ordinary (scoped RBAC, a lapsed
// session, a slow API server).
type namespaceAnswer struct {
	names []string
	// failed says the last fetch did not answer. With names, these are a previous
	// reading kept since; without, there is nothing to show and no reason to believe
	// the cluster has none.
	failed bool
	// readAt is when these names were read; zero where none were.
	readAt time.Time
}

// namespaceState is which of the four the answer is in, named so the table test
// and the note cannot disagree with the hold.
type namespaceState int

const (
	// namespacesListed: the cluster answered, with names.
	namespacesListed namespaceState = iota
	// namespacesNone: the cluster answered with nothing. It should not happen and is
	// said out loud, or it would look like namespacesUnknown.
	namespacesNone
	// namespacesUnknown: the cluster could not be asked.
	namespacesUnknown
	// namespacesStale: the cluster could not be asked and a previous reading is
	// served, missing what was created since and offering what was deleted.
	namespacesStale
)

func (a namespaceAnswer) state() namespaceState {
	switch {
	case !a.failed && len(a.names) > 0:
		return namespacesListed
	case !a.failed:
		return namespacesNone
	case len(a.names) > 0:
		return namespacesStale
	default:
		return namespacesUnknown
	}
}

// namespaceAnswer answers from the hold when it can.
func (s *Server) namespaceAnswer(ctx context.Context, cluster string) namespaceAnswer {
	if s.namespaces == nil {
		// A server assembled without a cache: reported as "could not be asked", which
		// invents nothing about the cluster.
		return namespaceAnswer{failed: true}
	}

	s.namespaces.mu.Lock()
	held, ok := s.namespaces.held[cluster]
	fresh := ok && time.Since(held.at) < held.ttl()
	s.namespaces.mu.Unlock()
	if fresh {
		// A failure served from the hold is still a failure, said on every render.
		return namespaceAnswer{names: held.names, failed: held.failed, readAt: held.readAt}
	}

	client, err := s.clusters.Client(cluster)
	if err != nil {
		return s.rememberNamespaces(cluster, nil, true)
	}
	// Its own short deadline: a slow namespace list must not hold the page.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	// Read as the console, because the hold is shared by every person; what each is
	// shown of it is their own review (mayListNamespaces).
	names, err := resources.NamespaceNames(access.AsConsole(ctx), resources.ReaderFor(s.gate, client))
	if err != nil {
		// The previous answer, if any, rather than nothing.
		if ok && len(held.names) > 0 {
			return s.rememberNamespaces(cluster, held.names, true)
		}
		return s.rememberNamespaces(cluster, nil, true)
	}
	return s.rememberNamespaces(cluster, names, false)
}

func (s *Server) rememberNamespaces(cluster string, names []string, failed bool) namespaceAnswer {
	s.namespaces.mu.Lock()
	defer s.namespaces.mu.Unlock()
	now := time.Now()
	readAt := now
	switch {
	case failed && len(names) == 0:
		// Nothing was read, so there is no moment to report.
		readAt = time.Time{}
	case failed:
		// A reading carried forward keeps the moment it was read, so an old list never
		// claims to be fresh.
		if previous, ok := s.namespaces.held[cluster]; ok && !previous.readAt.IsZero() {
			readAt = previous.readAt
		}
	}
	s.namespaces.held[cluster] = namespaceHold{names: names, at: now, readAt: readAt, failed: failed}
	return namespaceAnswer{names: names, failed: failed, readAt: readAt}
}

// rowsInNamespaces keeps the rows the selection asked for. A cluster-scoped row
// (empty Namespace) is in no namespace, so a filter excludes it. It builds a new
// slice rather than filtering in place over the fetched array.
func rowsInNamespaces(rows []resources.Row, selection []string) []resources.Row {
	if len(selection) == 0 {
		return rows
	}
	wanted := make(map[string]bool, len(selection))
	for _, name := range selection {
		wanted[name] = true
	}
	out := make([]resources.Row, 0, len(rows))
	for _, row := range rows {
		if row.Namespace != "" && wanted[row.Namespace] {
			out = append(out, row)
		}
	}
	return out
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// namespaceUnknownNote is what the page says about a selected namespace this
// cluster's list does not have, or "" when every one is accounted for. Without it
// a typo would draw "Nothing here.", a false and reassuring statement about the
// namespace's contents. What may be concluded depends on the list:
//
//   - read, and the name is not in it: no such namespace, with the caveat that
//     the list may be up to namespaceTTL old;
//   - a reading kept from before the cluster stopped answering: nothing may be
//     concluded, or an outage reads as a typo;
//   - not read, or empty: the same, and the menu's note already says why.
func namespaceUnknownNote(answer namespaceAnswer, selection []string) string {
	var missing []string
	for _, want := range selection {
		if !containsName(answer.names, want) {
			missing = append(missing, want)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	which := strings.Join(missing, ", ")

	switch answer.state() {
	case namespacesListed:
		subject := "No namespace called " + which
		if len(missing) > 1 {
			subject = "No namespaces called " + which
		}
		return subject + " on this cluster. The selector's list is re-read every minute, " +
			"so one created in the last few seconds would not be in it yet."
	case namespacesStale:
		return "Not in the namespace list read before this cluster stopped answering: " + which +
			". Whether that exists now cannot be said from here."
	default:
		// Unknown, or empty: a cluster reporting no namespaces is a failure, not proof
		// this one is gone.
		return "This cluster's namespaces could not be listed, so " + which +
			" could not be checked."
	}
}

// namespaceUnknown asks it of the per-cluster hold, so a page redrawn every few
// seconds costs one listing per namespaceTTL, not one per render.
func (s *Server) namespaceUnknown(ctx context.Context, cluster string, selection []string) string {
	if len(selection) == 0 {
		return ""
	}
	// Only for a person who may list namespaces: the answer says whether a namespace
	// exists, which `list namespaces` guards, and would let anyone probe for names.
	if s.mayListNamespaces(ctx, cluster) != nil {
		return ""
	}
	return namespaceUnknownNote(s.namespaceAnswer(ctx, cluster), selection)
}

// mayListNamespaces reviews the person behind ctx for `list namespaces` on a
// cluster. Nil when they may.
func (s *Server) mayListNamespaces(ctx context.Context, cluster string) error {
	if s.clusters != nil {
		if resolved, ok := s.clusters.Resolve(cluster); ok {
			cluster = resolved
		}
	}
	return s.gate.Check(ctx, cluster, access.Attributes{Verb: "list", Version: "v1", Resource: "namespaces"})
}

package cluster

import (
	"log"
	"strings"
)

// The KT_READONLY_CLUSTERS guardrail, resolved against the kubeconfig.
//
// An exact-string lookup of the list would fail three ways: a context name
// from a URL can be the DNS-friendly form (`…cluster/example` arriving as
// `…cluster--example`), an entry naming no context would not be an error, and
// nothing would be announced. The last one bites: a marking that silently does
// not apply is worse than none, because the operator believes the cluster is
// protected. So an entry that resolves to nothing is said out loud.
//
// Both sides are resolved, live: entries when parsed, the name asked about on
// every call, so a context that appears after a Reload still gets its marking.
// Resolution fails closed: both spellings collapse onto the context's name.

// ReadOnly is the resolved set of clusters marked read-only.
//
// The zero value and a nil pointer both mark nothing, which is what "the
// variable is not set" means.
type ReadOnly struct {
	// resolve is the Manager's, held rather than copied so a Reload is visible
	// here.
	resolve func(string) (string, bool)

	keys      map[string]bool
	marked    []string
	unmatched []string
}

// ReadOnlyMarkings parses KT_READONLY_CLUSTERS against this kubeconfig and says
// out loud what it marked and what it could not.
//
// The announcement is made here rather than by the caller, which could forget
// to print what a constructor returns.
func (m *Manager) ReadOnlyMarkings(raw string) *ReadOnly {
	return newReadOnly(raw, m.Resolve, m.Contexts, func(format string, args ...any) {
		log.Printf("[kubetower] "+format, args...)
	})
}

func newReadOnly(raw string, resolve func(string) (string, bool), contexts func() []string, logf func(string, ...any)) *ReadOnly {
	r := &ReadOnly{resolve: resolve, keys: map[string]bool{}}

	if strings.TrimSpace(raw) == "" {
		// Said even when empty, because the failure this file is about is
		// silence: a variable that never reached the process shows here.
		logf("no cluster is marked read-only (KT_READONLY_CLUSTERS is empty)")
		return r
	}

	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		canonical, ok := resolve(entry)
		if !ok {
			r.unmatched = append(r.unmatched, entry)
			r.keys[entry] = true
			continue
		}
		if canonical != entry {
			logf("KT_READONLY_CLUSTERS names %q, which is the URL form of %q", entry, canonical)
		}
		if !r.keys[canonical] {
			r.marked = append(r.marked, canonical)
		}
		r.keys[canonical] = true
	}

	if len(r.marked) > 0 {
		logf("read-only: %s", strings.Join(r.marked, ", "))
	}
	for _, entry := range r.unmatched {
		// Kept in the set, so a kubeconfig gaining that context later is
		// protected without a restart; the line says what is true now.
		logf("KT_READONLY_CLUSTERS names %q, which is not a context in this kubeconfig: "+
			"nothing is protected by that entry", entry)
	}
	if len(r.unmatched) > 0 {
		logf("the kubeconfig declares: %s", strings.Join(contexts(), ", "))
		if len(r.marked) == 0 {
			logf("KT_READONLY_CLUSTERS is set and not one entry names a context: " +
				"no cluster is marked read-only")
		}
	}
	return r
}

// Has reports whether a cluster is marked read-only.
//
// It is the `func(string) bool` guardrail handed to the write, terminal,
// forward and web packages, so it decides whether a write is refused. The name
// is resolved before comparing, because a URL segment is not always a context
// name.
func (r *ReadOnly) Has(name string) bool {
	if r == nil || len(r.keys) == 0 {
		return false
	}
	key := r.key(name)
	if key == "" {
		return false
	}
	if r.keys[key] {
		return true
	}
	// The DNS-friendly spelling of the resolved name, for an entry written in
	// URL form that named no context at boot and so was filed under that form.
	// MakeDNSFriendly is not injective (a name containing "--" could collide);
	// a collision marks one cluster too many, the direction a guardrail may fail.
	return r.keys[MakeDNSFriendly(key)]
}

// Marked is the resolved context names that are marked, for a caller that wants
// to show them. A copy, because every request reads this set.
func (r *ReadOnly) Marked() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.marked...)
}

// Unmatched is the entries that named no context, so a page can say so as well
// as the boot log, which scrolls away.
func (r *ReadOnly) Unmatched() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.unmatched...)
}

// key is the one spelling of a name both sides of the comparison are reduced to.
func (r *ReadOnly) key(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if r.resolve != nil {
		if canonical, ok := r.resolve(name); ok {
			return canonical
		}
	}
	return name
}

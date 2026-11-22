package health

// Opening the health page never waits for a network round: a context behind
// a dropped VPN holds a probe for its whole timeout, and a failure is cached
// for only a few seconds. Known answers from what is in hand and asks for the
// next round behind the answer; the page's own refresh brings the rest in.
//
// A context nobody has asked yet is counted as unprobed and never given a
// state, so it cannot be drawn as one that failed.

// Snapshot is what this process knows right now, without asking anything.
type Snapshot struct {
	// Rows are the answers there are, worst first. Contexts with no answer yet
	// are absent: an empty state would be drawn as an unclassified failure.
	Rows []Cluster
	// Total is how many contexts the kubeconfig declares.
	Total int
	// Unprobed is Total minus the rows: contexts nobody has managed to ask yet.
	Unprobed int
	// Refreshing is true when a round is in flight; the page polls faster then.
	Refreshing bool
}

// Complete reports whether every context has an answer.
func (s Snapshot) Complete() bool { return s.Total > 0 && s.Unprobed == 0 }

// Known answers from the cache and never probes, then asks for the next round in
// the background.
//
// The round skips the failure backoff the readiness warm uses: an operator
// opens this page right after `aws sso login`, and backgroundFailedMax would
// show them a stale refusal. It is still one round at a time and no more often
// than the TTL. Forcing a round and waiting for it is the "check again" button.
func (h *Health) Known() Snapshot {
	if h.clusters == nil {
		return Snapshot{}
	}
	names := h.clusters.Contexts()
	snap := Snapshot{Total: len(names)}

	h.mu.RLock()
	for _, name := range names {
		row, ok := h.cache[name]
		if !ok || row.State == "" {
			snap.Unprobed++
			continue
		}
		snap.Rows = append(snap.Rows, row)
	}
	h.mu.RUnlock()

	Sort(snap.Rows)

	// After the read and before the report, so a round started by this call is
	// reported as running.
	h.round(false)

	h.mu.RLock()
	snap.Refreshing = h.warming
	h.mu.RUnlock()

	return snap
}

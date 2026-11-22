package web

import (
	"context"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/thonicdev/kubetower/internal/kubeletstats"
)

// kubeletCache remembers what each kubelet said, per node, for usageTTL. It is
// shared by every reader of kubelet summaries (the Disk column and an extension's
// Host.Usage), so views inside the TTL cost one round of kubelet requests.
//
// It holds the raw summary, read with the console's own credentials, so the
// bytes do not depend on who asked. Per-person filtering happens after the cache,
// on every answer (usageFor keeps pods to the namespaces the person may list), and
// any reader that filters per person must keep doing so on what comes out.
//
// Entries idle for usageIdle are swept. A failed read is remembered for the TTL
// too, so a refusing kubelet is asked every ten seconds, not on every view.
type kubeletCache struct {
	mu   sync.Mutex
	held map[string]*kubeletEntry
	// now is time.Now, replaceable by a test that must step past the TTL.
	now func() time.Time
}

type kubeletEntry struct {
	summary kubeletstats.Summary
	err     error
	// tried is when the last read finished. Zero while the first is in flight.
	tried time.Time
	// fetching is a read in flight; done is closed when it comes back. A second
	// reader of the same node waits on it rather than asking again.
	fetching bool
	done     chan struct{}
}

func newKubeletCache() *kubeletCache {
	return &kubeletCache{held: map[string]*kubeletEntry{}, now: time.Now}
}

// summaries is kubeletstats.ReadNodes through the cache: the same worker cap,
// ordering and Coverage, with each node answered from a read within usageTTL
// when there is one. cluster must be the resolved context name, the cache key.
func (c *kubeletCache) summaries(ctx context.Context, cluster string, clients kubernetes.Interface,
	nodes []string) (map[string]kubeletstats.Summary, kubeletstats.Coverage) {
	return kubeletstats.ReadNodesWith(ctx, nodes, func(ctx context.Context, node string) (kubeletstats.Summary, error) {
		return c.read(ctx, cluster+"|"+node, func(ctx context.Context) (kubeletstats.Summary, error) {
			return kubeletstats.Read(ctx, clients, node)
		})
	})
}

// read answers one node: from an entry younger than usageTTL, by waiting on the
// read in flight, or by reading in the caller's goroutine and context. A read
// ended because its caller went away is not remembered: it says nothing about the
// kubelet.
func (c *kubeletCache) read(ctx context.Context, key string,
	fetch func(context.Context) (kubeletstats.Summary, error)) (kubeletstats.Summary, error) {
	for {
		c.mu.Lock()
		c.evictIdle()
		held := c.held[key]
		if held != nil && !held.fetching && !held.tried.IsZero() && c.now().Sub(held.tried) < usageTTL {
			summary, err := held.summary, held.err
			c.mu.Unlock()
			return summary, err
		}
		if held != nil && held.fetching {
			done := held.done
			c.mu.Unlock()
			select {
			case <-done:
				// Answered from the entry next turn, or read again if the awaited read was
				// abandoned.
				continue
			case <-ctx.Done():
				return kubeletstats.Summary{}, ctx.Err()
			}
		}
		if held == nil {
			held = &kubeletEntry{}
			c.held[key] = held
		}
		held.fetching, held.done = true, make(chan struct{})
		c.mu.Unlock()

		summary, err := fetch(ctx)

		c.mu.Lock()
		held.fetching = false
		close(held.done)
		if err != nil && ctx.Err() != nil {
			// Abandoned, not answered: leave the entry, so waiters ask for themselves.
			c.mu.Unlock()
			return summary, err
		}
		held.summary, held.err, held.tried = summary, err, c.now()
		c.mu.Unlock()
		return summary, err
	}
}

// evictIdle drops the entries nobody has read for usageIdle. Called with the
// lock held. An entry with a read in flight is kept whatever its age; one
// whose only read was abandoned holds nothing and goes at once.
func (c *kubeletCache) evictIdle() {
	for key, held := range c.held {
		if held.fetching || (!held.tried.IsZero() && c.now().Sub(held.tried) < usageIdle) {
			continue
		}
		delete(c.held, key)
	}
}

// kubeletSummaries is kubeletstats.ReadNodes through the server's cache, or a
// fresh read on a Server that has none.
func (s *Server) kubeletSummaries(ctx context.Context, cluster string, clients kubernetes.Interface,
	nodes []string) (map[string]kubeletstats.Summary, kubeletstats.Coverage) {
	if s == nil || s.kubelets == nil {
		return kubeletstats.ReadNodes(ctx, clients, nodes)
	}
	return s.kubelets.summaries(ctx, cluster, clients, nodes)
}

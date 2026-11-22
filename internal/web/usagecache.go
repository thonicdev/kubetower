package web

import (
	"context"
	"time"

	"github.com/thonicdev/kubetower/internal/resources"
	"github.com/thonicdev/kubetower/safego"
)

// usageTTL is how long live CPU and memory for a table are held. They are
// fetched concurrently with the rows, never before them (a Prometheus behind a
// fresh port-forward takes seconds), and held because metrics-server aggregates
// over ~15 s and a Prometheus scrape is typically 30 s: asking more often only
// repeats the same numbers.
const usageTTL = 10 * time.Second

// usageIdle is how long an entry nobody has asked about is kept. The key
// includes the namespace, which people create without limit, so abandoned keys
// must go; six TTLs, so a table left open while someone reads still finds its
// own entry.
const usageIdle = 6 * usageTTL

// usageMaxStale is how long figures nobody could renew are still shown. A failed
// fetch keeps the last figures so a blip does not blank the column, without
// re-stamping them; past this the column goes back to dashes, since a cell has no
// room to say how old a figure is.
const usageMaxStale = 6 * usageTTL

type usageEntry struct {
	usage map[string]resources.Usage
	// at is when the figures in `usage` were observed; a failed fetch does not move
	// it.
	at time.Time
	// tried is when the last attempt finished, successful or not. The TTL and the
	// idle sweep read this one: read from `at`, a failing cluster would be asked on
	// every render.
	tried time.Time
	// fetching stops a burst of renders from starting a burst of fetches.
	fetching bool
	// done is closed when the fetch in flight comes back and replaced at the start of
	// the next one. Only usageAnswer waits on it, and only while `fetching` is set.
	done chan struct{}
	// err is why the last attempt gave no figures; nil when it gave some or nothing
	// was asked. Kept so a caller can tell a missing backend from a failed one.
	err error
}

// usageFor returns the live usage for a table, or nil, and never blocks on a
// cluster: a missing or stale answer starts a fetch behind the caller and returns
// whatever is held (nil on the first call), and the table fills in on its next
// tick. nil is a valid answer: no metrics backend is the ordinary case.
func (s *Server) usageFor(ctx context.Context, cluster, kind, namespace string) map[string]resources.Usage {
	usage, _ := s.usageRead(ctx, cluster, kind, namespace)
	return usage
}

// usageRead is usageFor, plus whether its nil is a first read still on its way:
// a fetch in flight for a key that has never finished one. Once an attempt has
// come back, a nil is that attempt's answer.
func (s *Server) usageRead(ctx context.Context, cluster, kind, namespace string) (map[string]resources.Usage, bool) {
	if !hasLiveFigures(kind) {
		// No usage column (list.go declares which kinds have one), so nothing to ask.
		return nil, false
	}
	if s.usage == nil && !needsNoMetricsBackend(kind) {
		// No metrics backend. A node's Disk gauge and a claim's Used gauge come from a
		// kubelet, so those are still worth the round trip.
		return nil, false
	}

	// The namespace is in the key only where the answer depends on it.
	key := usageKey(cluster, kind, namespace)

	s.usageMu.Lock()
	s.evictIdleUsage()
	held := s.usageHeld[key]
	if held != nil && held.usage != nil && time.Since(held.at) >= usageMaxStale {
		// Nothing could renew these for usageMaxStale: dropped once, on the way out.
		held.usage = nil
	}
	if held != nil && time.Since(held.tried) < usageTTL {
		usage := held.usage
		s.usageMu.Unlock()
		return usage, false
	}
	if held != nil && held.fetching {
		// One in flight already: hand back what is held rather than start another
		// tunnel per tile.
		usage, pending := held.usage, held.usage == nil && held.tried.IsZero()
		s.usageMu.Unlock()
		return usage, pending
	}
	if held == nil {
		held = &usageEntry{}
		if s.usageHeld == nil {
			// A Server not built by `New` (a test): a nil map would panic on write.
			s.usageHeld = map[string]*usageEntry{}
		}
		s.usageHeld[key] = held
	}
	held.fetching = true
	held.done = make(chan struct{})
	stale, pending := held.usage, held.usage == nil && held.tried.IsZero()
	s.usageMu.Unlock()

	safego.Go("usage column: the fetch", func() error {
		// Detached from the caller's context, with its own bound: the caller is often a
		// refresh aborted by the next one, which would otherwise never let a fetch finish.
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()

		// Through safego.Call, so a panic is this fetch's error and `done` is still
		// closed for its waiters.
		var usage map[string]resources.Usage
		err := safego.Call("usage column: reading "+kind, func() error {
			var err error
			usage, err = s.liveUsage(fetchCtx, cluster, kind, namespace)
			return err
		})

		s.usageMu.Lock()
		defer s.usageMu.Unlock()
		held.fetching = false
		held.tried = time.Now()
		held.err = err
		close(held.done)
		// A failed fetch keeps the previous figures without claiming they are new;
		// usageMaxStale takes them away.
		if usage != nil {
			held.usage, held.at = usage, held.tried
		}
		return nil
	}, nil)

	return stale, pending
}

// usageKey is the cache's key for one question. The namespace is in it only when
// the answer depends on it: `NodeUsage` takes none, and keying nodes by the
// filter's namespace would fetch the same data once per namespace.
func usageKey(cluster, kind, namespace string) string {
	key := cluster + "|" + kind
	if kind != "nodes" {
		key += "|" + namespace
	}
	return key
}

// usageAnswer is usageRead for a reader asked once (an object's metrics panel,
// the Overview's Capacity tile), with why figures are missing. Such a reader has
// no next tick, so a first read in flight is waited for, bounded by the caller's
// request; every other case answers at once from the cache, which spares a panel
// the backend round trip the Pods list just paid.
//
// The error is the last attempt's: nil beside figures and nil where nothing was
// asked, so a nil error with no figures is not a failure. A request that gave up
// while waiting answers its context's error.
func (s *Server) usageAnswer(ctx context.Context, cluster, kind, namespace string) (map[string]resources.Usage, error) {
	usage, pending := s.usageRead(ctx, cluster, kind, namespace)
	key := usageKey(cluster, kind, namespace)
	if pending {
		var done chan struct{}
		s.usageMu.Lock()
		if held := s.usageHeld[key]; held != nil && held.fetching {
			done = held.done
		}
		s.usageMu.Unlock()
		if done != nil {
			select {
			case <-done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		// The awaited read has come back: answer from it and start nothing.
		usage, _ = s.usageRead(ctx, cluster, kind, namespace)
	}
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if held := s.usageHeld[key]; held != nil {
		return usage, held.err
	}
	return usage, nil
}

// evictIdleUsage drops the entries nobody has asked about for usageIdle. Called
// with the lock held, on the render path: the map only grows on renders, so no
// timer is needed. It reads `tried`, so a failing but watched cluster stays live,
// and an entry with a fetch in flight is kept whatever its age, or the next
// reader would start a second fetch.
func (s *Server) evictIdleUsage() {
	for key, held := range s.usageHeld {
		if held.fetching || time.Since(held.tried) < usageIdle {
			continue
		}
		delete(s.usageHeld, key)
	}
}

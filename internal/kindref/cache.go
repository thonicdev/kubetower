package kindref

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/restmapper"
)

// Building a RESTMapper walks every API group the cluster serves, dozens of
// round trips, so the mapper is cached per cluster. It is one cache for every
// caller so that all of them retry with a fresh mapper the same way; otherwise
// a newly installed CRD resolves in one panel and not in the next.

// mapperTTL bounds how long a mapper is reused. It is a ceiling, not the
// mechanism: a kind the mapper does not know forces a rebuild (Cache.Mapping).
const mapperTTL = 10 * time.Minute

// unknownTTL bounds how long "a mapper built just now did not know that kind" is
// remembered, so a kind the cluster does not serve stops costing a discovery
// walk on every refresh.
//
// It caches an absence with no event to invalidate it, so the lifetime is the
// invalidation, and it errs short: too long and a freshly applied CRD looks
// absent; too short and the console makes a walk it could have skipped. Thirty
// seconds still collapses about fifteen two-second refreshes into one walk.
//
// It gates the rebuild, never the answer: a kind that resolves against the held
// mapper never consults it, so no entry can hide a kind the cluster serves.
const unknownTTL = 30 * time.Second

// maxUnknown caps the negative cache, because `kind` comes off a URL path and a
// client can mint keys as fast as it makes requests. At the cap, expired
// entries go and then, if still full, the whole map: losing an entry costs only
// one discovery walk.
const maxUnknown = 1024

// Cache holds one RESTMapper per cluster context.
// The caller supplies a key and a build function, which keeps this package free
// of `internal/cluster`.
type Cache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]cacheEntry
	// unknownTTL is a field so a test can change it.
	unknownTTL time.Duration
	// unknown records when a freshly built mapper did not know a kind.
	unknown map[unknownKey]time.Time
}

// unknownKey is the cluster and the kind. A struct rather than a joined string:
// both are arbitrary text, so any separator could make two keys collide.
type unknownKey struct{ key, kind string }

type cacheEntry struct {
	mapper meta.RESTMapper
	built  time.Time
}

// NewCache returns an empty cache with the default TTL.
func NewCache() *Cache {
	return &Cache{
		ttl:        mapperTTL,
		entries:    map[string]cacheEntry{},
		unknownTTL: unknownTTL,
		unknown:    map[unknownKey]time.Time{},
	}
}

// DiscoveryError is a mapper that could not be built, as opposed to a kind that
// does not exist: a cluster that could not be asked has not answered "no such
// kind", and callers must keep the two apart.
type DiscoveryError struct{ Err error }

func (e *DiscoveryError) Error() string { return e.Err.Error() }
func (e *DiscoveryError) Unwrap() error { return e.Err }

// Mapper returns the cached mapper for key, building one when there is none,
// when it has aged past the TTL, or when the caller says the answer it gave was
// wrong.
func (c *Cache) Mapper(ctx context.Context, key string, stale bool, build func(context.Context) (meta.RESTMapper, error)) (meta.RESTMapper, error) {
	c.mu.Lock()
	held, ok := c.entries[key]
	ttl := c.ttl
	c.mu.Unlock()

	if ok && !stale && ttl > 0 && time.Since(held.built) < ttl {
		return held.mapper, nil
	}

	// Built outside the lock: holding it across dozens of round trips would
	// stall every cluster behind one slow one; two concurrent builds only waste
	// a walk. The context stops the walk once the request has gone away.
	mapper, err := build(ctx)
	if err != nil {
		return nil, &DiscoveryError{Err: err}
	}

	c.mu.Lock()
	c.entries[key] = cacheEntry{mapper: mapper, built: time.Now()}
	c.mu.Unlock()
	return mapper, nil
}

// Mapping resolves a kind against the cluster's cached mapper, rebuilding once
// if it does not resolve.
// That retry is what lets a newly installed CRD resolve despite the long TTL.
// It returns the mapper it resolved with, for the caller's next question.
func (c *Cache) Mapping(ctx context.Context, key, kind string, build func(context.Context) (meta.RESTMapper, error)) (meta.RESTMapper, *meta.RESTMapping, error) {
	mapper, err := c.Mapper(ctx, key, false, build)
	if err != nil {
		return nil, nil, err
	}
	mapping, err := Mapping(mapper, kind)
	if err == nil {
		return mapper, mapping, nil
	}

	if c.recentlyUnknown(key, kind) {
		// A mapper built moments ago did not know this kind either; a second
		// walk would get the same answer and replace every panel's mapper.
		return nil, nil, err
	}

	fresh, buildErr := c.Mapper(ctx, key, true, build)
	if buildErr != nil {
		// The rebuild was a best effort: report the kind that did not resolve.
		// Nothing is remembered, because the cluster was not asked.
		return nil, nil, err
	}
	mapping, retryErr := Mapping(fresh, kind)
	if retryErr != nil {
		c.rememberUnknown(key, kind)
		return nil, nil, retryErr
	}
	return fresh, mapping, nil
}

// recentlyUnknown reports whether a freshly built mapper has already said no
// about this kind, recently enough that asking again is not worth a walk.
func (c *Cache) recentlyUnknown(key, kind string) bool {
	if c.unknownTTL <= 0 {
		return false
	}
	id := unknownKey{key: key, kind: kind}
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := c.unknown[id]
	if !ok {
		return false
	}
	if time.Since(at) >= c.unknownTTL {
		delete(c.unknown, id)
		return false
	}
	return true
}

// rememberUnknown records that a mapper built just now did not know this kind.
func (c *Cache) rememberUnknown(key, kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.unknown == nil {
		c.unknown = map[unknownKey]time.Time{}
	}
	if len(c.unknown) >= maxUnknown {
		for k, at := range c.unknown {
			if time.Since(at) >= c.unknownTTL {
				delete(c.unknown, k)
			}
		}
		if len(c.unknown) >= maxUnknown {
			clear(c.unknown)
		}
	}
	c.unknown[unknownKey{key: key, kind: kind}] = time.Now()
}

// DiscoveryMapper builds a mapper from what the cluster says it serves. It is
// the `build` every caller passes.
func DiscoveryMapper(ctx context.Context, d discovery.DiscoveryInterface) (meta.RESTMapper, error) {
	// The contextual form, so the walk stops when nobody is waiting.
	// ToDiscoveryInterfaceWithContext passes client-go's own client through and
	// wraps anything else (a test fake) in an adapter that drops the context.
	groups, err := restmapper.GetAPIGroupResourcesWithContext(ctx, discovery.ToDiscoveryInterfaceWithContext(d))
	if err != nil {
		return nil, fmt.Errorf("reading the API groups this cluster serves: %w", err)
	}
	return restmapper.NewDiscoveryRESTMapper(groups), nil
}

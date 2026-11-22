package events

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

// The cache holds the aggregate of a (person, cluster, namespace, window) for
// fifteen seconds, 32 of them at most, so the page, the tab and the panel
// lines open together do not each read 50 000 Events.
const (
	// CacheTTL is how long an aggregate is served.
	CacheTTL = 15 * time.Second
	// CacheEntries is how many aggregates are held, least recently used out.
	CacheEntries = 32
)

// sessionCookie is the console's session cookie (internal/auth's default
// name). An extension is handed no identity, so the person is told apart by
// the cookie the request carries, hashed: the cache never stores the cookie
// itself, and an answer reviewed for one session is never served to another.
const sessionCookie = "kubetower_session"

// personKey names the person behind r for the cache, from the hash of the
// session cookie (or, for a client that signs in with a header, of the
// Authorization header). A request with neither has no one to key on: its
// answer is computed and never held.
func personKey(r *http.Request) (string, bool) {
	var secret string
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		secret = "cookie:" + c.Value
	} else if v := r.Header.Get("Authorization"); v != "" {
		secret = "header:" + v
	} else {
		return "", false
	}
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:16]), true
}

type cacheKey struct{ person, cluster, namespace, window string }

type cacheEntry struct {
	key cacheKey
	at  time.Time
	agg *aggregate
}

// cache is a small LRU with a time to live. The aggregate it holds is shared
// by the requests that hit it and never changed after it is built.
type cache struct {
	mu  sync.Mutex
	max int
	ttl time.Duration
	lru *list.List
	by  map[cacheKey]*list.Element
}

func newCache(max int, ttl time.Duration) *cache {
	return &cache{max: max, ttl: ttl, lru: list.New(), by: map[cacheKey]*list.Element{}}
}

// get serves the held aggregate of key when it is fresh at now, else builds
// one and holds it. A build that fails is never held.
func (c *cache) get(key cacheKey, now time.Time, build func() (*aggregate, error)) (*aggregate, error) {
	c.mu.Lock()
	if el, ok := c.by[key]; ok {
		e := el.Value.(*cacheEntry)
		if now.Sub(e.at) < c.ttl {
			c.lru.MoveToFront(el)
			c.mu.Unlock()
			return e.agg, nil
		}
		c.lru.Remove(el)
		delete(c.by, key)
	}
	c.mu.Unlock()

	agg, err := build()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.by[key]; ok {
		c.lru.Remove(el)
	}
	c.by[key] = c.lru.PushFront(&cacheEntry{key: key, at: now, agg: agg})
	for c.lru.Len() > c.max {
		old := c.lru.Back()
		c.lru.Remove(old)
		delete(c.by, old.Value.(*cacheEntry).key)
	}
	return agg, nil
}

// size is how many aggregates are held.
func (c *cache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len()
}

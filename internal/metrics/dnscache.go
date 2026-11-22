package metrics

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/thonicdev/kubetower/safego"
)

// Keeping what a DNS read came to, for the next display.
//
// CoreDNS's figures move in seconds, so what is kept is brief: 15 s for the
// 15-minute span, 30 s for the hour, two minutes for six hours. What it buys is
// that the page and the Service line, and any number of open tabs, ask
// Prometheus once for all of them.
//
//   - keyed by the cluster, the span, the namespace and the set of pods read.
//     The key holds nothing of the person: the review is all or nothing and runs
//     before the cache is looked at, so whoever reaches it is allowed all of it;
//   - at most DNSCacheEntries entries and DNSCacheBytes across them, the least
//     recently used going first. An answer larger than the whole cache is
//     served and never kept;
//   - only an answer that read, or that found nothing, is kept: a Prometheus
//     that could not be asked is asked again by the next display;
//   - concurrent displays needing the same read share it (singleflight), and at
//     most dnsReads reads run at once, taking one of the window cache's own read
//     slots too, so DNS and usage windows share one ceiling on what the console
//     asks of a Prometheus together.

const (
	// DNSCacheEntries is how many answers are kept at most.
	DNSCacheEntries = 16
	// DNSCacheBytes is how much is kept at most, counted by dnsSize.
	DNSCacheBytes = 1 << 20
	// dnsReads is how many DNS reads may run at once.
	dnsReads = 4
)

// dnsTTL is how long an answer for span is served as it is.
func dnsTTL(span DNSSpan) time.Duration {
	switch span {
	case DNSSpan1h:
		return 30 * time.Second
	case DNSSpan6h:
		return 2 * time.Minute
	}
	return 15 * time.Second
}

// DNSCache keeps what DNS reads came to. The zero value is not usable;
// NewDNSCache makes one. Safe for concurrent use.
type DNSCache struct {
	now    func() time.Time
	reads  chan struct{}
	shared chan struct{} // the window cache's read slots; nil shares none

	mu      sync.Mutex
	order   *list.List // of *dnsKept, most recently used first
	byKey   map[string]*list.Element
	held    int
	flights map[string]*dnsFlight
}

type dnsKept struct {
	key   string
	value DNSRead
	at    time.Time
	ttl   time.Duration
	size  int
}

type dnsFlight struct {
	done  chan struct{}
	value DNSRead
}

// NewDNSCache makes an empty cache. With windows set, its reads also take one
// of that cache's foreground read slots, the ceiling usage windows count
// against.
func NewDNSCache(windows *WindowCache) *DNSCache {
	c := &DNSCache{now: time.Now, order: list.New(), byKey: map[string]*list.Element{},
		flights: map[string]*dnsFlight{}, reads: make(chan struct{}, dnsReads)}
	if windows != nil {
		c.shared = windows.reads
	}
	return c
}

// Read is ReadDNS through the cache. The read runs on a context that outlives
// the request, shared with whoever else waits for it and bounded by dnsBudget,
// so a display that stops waiting does not fail the others'.
func (c *DNSCache) Read(ctx context.Context, q RangeQuerier, cluster string, span DNSSpan, namespace string,
	pods []DNSPodRef) (DNSRead, error) {
	// What needs no Prometheus, or cannot be asked, is answered before it
	// takes a slot or a key.
	if span.Duration() == 0 || namespace == "" || !validName(namespace) || q == nil {
		return ReadDNS(ctx, q, cluster, span, namespace, pods)
	}
	key := cluster + "\x00" + string(span) + "\x00" + namespace + "\x00" + podSetKey(pods)

	c.mu.Lock()
	if e, ok := c.byKey[key]; ok {
		k := e.Value.(*dnsKept)
		if c.now().Sub(k.at) < k.ttl {
			c.order.MoveToFront(e)
			c.mu.Unlock()
			return k.value, nil
		}
		c.drop(e)
	}
	f, running := c.flights[key]
	if !running {
		if err := ctx.Err(); err != nil {
			c.mu.Unlock()
			return DNSRead{State: WindowUnreadable, Detail: shortError(err)}, nil
		}
		f = c.fly(ctx, key, q, cluster, span, namespace, pods)
	}
	c.mu.Unlock()
	select {
	case <-f.done:
		return f.value, nil
	case <-ctx.Done():
		return DNSRead{State: WindowUnreadable, Detail: shortError(ctx.Err())}, nil
	}
}

// fly starts the one read of key. c.mu is held.
func (c *DNSCache) fly(ctx context.Context, key string, q RangeQuerier, cluster string, span DNSSpan,
	namespace string, pods []DNSPodRef) *dnsFlight {
	f := &dnsFlight{done: make(chan struct{})}
	c.flights[key] = f
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dnsBudget)
	safego.Go("metrics: a DNS read", func() error {
		for _, slots := range []chan struct{}{c.reads, c.shared} {
			if slots == nil {
				continue
			}
			select {
			case slots <- struct{}{}:
				defer func(s chan struct{}) { <-s }(slots)
			case <-ctx.Done():
				f.value = DNSRead{State: WindowUnreadable, Detail: "too many DNS reads were running at once"}
				return nil
			}
		}
		var err error
		f.value, err = ReadDNS(ctx, q, cluster, span, namespace, pods)
		return err
	}, func(err error) {
		cancel()
		if err != nil {
			f.value = DNSRead{State: WindowUnreadable, Detail: shortError(err)}
		}
		c.mu.Lock()
		delete(c.flights, key)
		if f.value.State == WindowRead || f.value.State == WindowEmpty {
			c.put(key, f.value, dnsTTL(span))
		}
		c.mu.Unlock()
		close(f.done)
	})
	return f
}

// put keeps value under key, letting the least recently used go until the cache
// is within its bounds. An answer larger than the whole cache is not kept.
// c.mu is held.
func (c *DNSCache) put(key string, value DNSRead, ttl time.Duration) {
	size := dnsSize(value)
	if size > DNSCacheBytes {
		return
	}
	c.byKey[key] = c.order.PushFront(&dnsKept{key: key, value: value, at: c.now(), ttl: ttl, size: size})
	c.held += size
	for c.order.Len() > DNSCacheEntries || c.held > DNSCacheBytes {
		c.drop(c.order.Back())
	}
}

// drop forgets one entry. c.mu is held.
func (c *DNSCache) drop(e *list.Element) {
	k := e.Value.(*dnsKept)
	c.order.Remove(e)
	delete(c.byKey, k.key)
	c.held -= k.size
}

// Len and Bytes say what is kept, for a test and for nobody else.
func (c *DNSCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// dnsSize is what an answer weighs: sixteen bytes a point, and a flat
// allowance for each pod, upstream and string. Never less than the heap it
// holds, which is what a bound is for.
func dnsSize(r DNSRead) int {
	points := len(r.Series.QPS) + len(r.Series.P50) + len(r.Series.P99) + len(r.Series.CacheHit)
	for _, pts := range r.Series.ByRcode {
		points += len(pts)
	}
	return 16*points + 256*len(r.Pods) + 128*len(r.Upstreams) + 512 + len(r.Detail) + len(r.Partial)
}

// podSetKey names a set of pods: their names, nodes and addresses, hashed.
func podSetKey(pods []DNSPodRef) string {
	live, _ := validPods(pods)
	h := sha256.New()
	for _, p := range live {
		h.Write([]byte(p.Name + "\x00" + p.Node + "\x00" + p.IP + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

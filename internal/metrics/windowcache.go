package metrics

import (
	"container/list"
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/thonicdev/kubetower/safego"
)

// Keeping what a window read came to, for the next display.
//
// A window is a week of figures, and a week does not move in a few minutes:
// read again for every display of a page, it costs Prometheus the same work for
// nearly the same answer. So what a read came to is kept, packed (packed.go:
// the same points, without the timestamps the window's grid already knows),
// and the listing of the containers it was read for:
//
//   - keyed by the cluster, the namespace asked (or every one) and **the
//     person's visible scope**: every namespace when they may list pods in all
//     of those the listing holds, otherwise the exact set they may. The scope is
//     worked out on every request, from the person's own reviews, before
//     anything kept is looked at, so a window read for a broader scope is never
//     handed to a narrower one; two people with the same scope share one;
//   - fresh for WindowCacheTTL. Up to twice that it is still served, and read
//     again in the background, once, while the page draws what was kept;
//     past it, it is read again before it is served;
//   - at most WindowCacheEntries entries and WindowCacheBytes across them, the
//     least recently used going first: a ceiling on memory whatever the
//     clusters' size. A window larger than the whole cache is served, never
//     kept;
//   - only a window read whole is kept for the TTL. A failed read, a partial
//     one, one with a half missing or one larger than the cache is held for
//     windowBriefTTL only, as it was answered and saying so, at most
//     windowBriefEntries of them beside the bounds above: long enough for the
//     page that asked, which polls while the read goes on, to collect it, and
//     short enough that the next display reads again, which is when the failure
//     may have passed. Without it, a read that ended while no poll was waiting
//     was handed to nobody, and the next poll started the whole week again.
//
// Concurrent displays needing the same read share it: one read per key at a
// time, foreground or background. Across keys, at most windowReads window
// reads run at once in the foreground; past it a read waits for a slot, and
// whoever waits for it is told the window is still being read. A display that
// has already gone away starts none.

const (
	// WindowCacheTTL is how long a kept window is served as it is.
	WindowCacheTTL = 10 * time.Minute
	// WindowCacheEntries is how many windows and listings are kept at most.
	WindowCacheEntries = 32
	// WindowCacheBytes is how much is kept at most, across every entry,
	// counted as packed.go counts it - never less than the heap it holds
	// (TestAKeptWindowCostsWhatItIsCounted): 32 MiB, about 33 MB. A week of a
	// container that ran all of it is about 10.9 kB packed, so a cluster of
	// 2 400 such containers is 26 MB, in one entry.
	WindowCacheBytes = 32 << 20
	// windowRefreshes is how many background reads may run at once; past it a
	// stale window is served without one, and the next display asks again.
	windowRefreshes = 2
	// windowReads is how many window reads may run at once in the foreground,
	// across every key: each is a week of Prometheus, with windowInFlight
	// batches in flight, and outlives the display that asked.
	windowReads = 8
	// windowBriefTTL is how long an answer that is not kept is still served:
	// one page wait and a few of its polls.
	windowBriefTTL = 30 * time.Second
	// windowBriefEntries is how many such answers are held at once. Outside
	// WindowCacheBytes, since one may be larger than the whole cache.
	windowBriefEntries = 2
)

// WindowCache keeps what window reads came to. The zero value is not usable;
// NewWindowCache makes one. Safe for concurrent use.
type WindowCache struct {
	now func() time.Time

	mu      sync.Mutex
	order   *list.List // of *kept, most recently used first
	byKey   map[string]*list.Element
	held    int // bytes across every entry
	flights map[string]*flight
	slots   chan struct{} // background reads
	reads   chan struct{} // foreground window reads
	brief   map[string]*kept
}

// kept is one entry: a listing ([]string) or a window (packedWindow).
type kept struct {
	key   string
	value any
	at    time.Time
	size  int
}

// flight is a read in progress, which every display needing it waits for.
type flight struct {
	done  chan struct{}
	value any
	keep  bool
}

// NewWindowCache makes an empty cache.
func NewWindowCache() *WindowCache {
	return &WindowCache{now: time.Now, order: list.New(), byKey: map[string]*list.Element{},
		flights: map[string]*flight{}, slots: make(chan struct{}, windowRefreshes),
		reads: make(chan struct{}, windowReads), brief: map[string]*kept{}}
}

// Read is ReadWindowKept through the cache. keep is asked on every call: the
// scope it answers is part of the key. The window it answers is the caller's
// own, unpacked from what is kept.
func (c *WindowCache) Read(ctx context.Context, q RangeQuerier, cluster, namespace string,
	keep func(keys []string) (map[string]bool, error)) (Window, error) {
	if w, done, err := unasked(q, namespace); done {
		return w, err
	}
	at := cluster + "\x00" + namespace
	listed, _ := c.get(ctx, "listing\x00"+at, nil, func(ctx context.Context) (any, bool, int) {
		keys, err := listContainers(ctx, q, cluster, namespace)
		if err != nil {
			w := failedWindow(err)
			w.ReadAt = c.now()
			return w, false, 0
		}
		return keys, true, listingBytes(keys)
	})
	keys, ok := listed.([]string)
	if !ok {
		return listed.(Window), nil
	}
	kept, narrowed, err := narrow(keys, keep)
	if err != nil {
		return Window{}, err
	}
	if len(kept) == 0 {
		return emptyWindow(namespace, narrowed), nil
	}
	var fresh Window
	w, led := c.get(ctx, "window\x00"+at+"\x00"+scopeOf(kept, narrowed), c.reads, func(ctx context.Context) (any, bool, int) {
		w := readKeys(ctx, q, cluster, kept)
		w.ReadAt = c.now()
		fresh = w
		if w.State != WindowRead || w.Partial != 0 || w.CPUErr != nil || w.MemoryErr != nil {
			return w, false, 0
		}
		packed, ok := packWindow(w)
		if !ok {
			return w, false, 0
		}
		return packed, true, packed.bytes
	})
	// The display whose read this was has the window as read, and needs no
	// copy: what is kept was packed from it, and packing copied every value.
	if led && fresh.State != "" {
		return fresh, nil
	}
	if packed, ok := w.(packedWindow); ok {
		return packed.unpack(), nil
	}
	return w.(Window), nil
}

// scopeOf is the visible scope a window was read for: every namespace the
// listing holds, or the sorted namespaces of what the person may see.
func scopeOf(kept []string, narrowed bool) string {
	if !narrowed {
		return "*"
	}
	var namespaces []string
	for _, k := range kept {
		ns, _, _ := strings.Cut(k, "/")
		namespaces = append(namespaces, ns)
	}
	slices.Sort(namespaces)
	return strings.Join(slices.Compact(namespaces), ",")
}

// get answers key from the cache, or by read. read says whether its answer
// may be kept, and its size in containers. limit, when set, is the slots a
// foreground read of key waits for.
//
// The read runs on a context that outlives the request: it is shared with
// whoever else is waiting for it, and bounded by windowBudget, not by the
// first request to ask.
//
// led is true when this display's own read answered, in the foreground: its
// read has ended, and what it set is the caller's to use.
func (c *WindowCache) get(ctx context.Context, key string, limit chan struct{},
	read func(context.Context) (any, bool, int)) (value any, led bool) {
	c.mu.Lock()
	if e, ok := c.byKey[key]; ok {
		k := e.Value.(*kept)
		age := c.now().Sub(k.at)
		if age < 2*WindowCacheTTL {
			c.order.MoveToFront(e)
			if age >= WindowCacheTTL {
				c.refresh(ctx, key, read)
			}
			c.mu.Unlock()
			return k.value, false
		}
	}
	if b, ok := c.brief[key]; ok {
		if c.now().Sub(b.at) < windowBriefTTL {
			c.mu.Unlock()
			return b.value, false
		}
		delete(c.brief, key)
	}
	f, running := c.flights[key]
	if !running {
		if err := ctx.Err(); err != nil {
			// Gone before asking: a read nobody waits for is not started.
			c.mu.Unlock()
			return Window{State: WindowUnreadable, Detail: shortError(err)}, false
		}
		f = c.fly(ctx, key, limit, read, false)
	}
	led = !running
	c.mu.Unlock()
	// A display that gave up stops waiting; the read goes on for whoever
	// else waits, and is kept for the next one.
	select {
	case <-f.done:
		return f.value, led
	case <-ctx.Done():
		return Window{State: WindowUnreadable, Detail: shortError(ctx.Err())}, false
	}
}

// refresh reads key again in the background, unless a read of it is already
// running or every background slot is taken. c.mu is held.
func (c *WindowCache) refresh(ctx context.Context, key string, read func(context.Context) (any, bool, int)) {
	if _, running := c.flights[key]; running {
		return
	}
	select {
	case c.slots <- struct{}{}:
	default:
		return
	}
	c.fly(ctx, key, nil, read, true)
}

// fly starts one read of key, shared by whoever waits for it, and keeps its
// answer when it may be kept, or briefly when not. A read with a limit waits
// for one of its slots first, within its budget. A background read gives its
// slot back when it ends. c.mu is held.
func (c *WindowCache) fly(ctx context.Context, key string, limit chan struct{},
	read func(context.Context) (any, bool, int), background bool) *flight {
	f := &flight{done: make(chan struct{})}
	c.flights[key] = f
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), windowBudget)
	var size int
	// Through safego: a panic in a read is that read's failure, said as an
	// unreadable window, and every display waiting for it is let go.
	safego.Go("metrics: a kept window's read", func() error {
		if limit != nil {
			select {
			case limit <- struct{}{}:
				defer func() { <-limit }()
			case <-ctx.Done():
				f.value = Window{State: WindowUnreadable, Detail: "too many usage windows were being read at once"}
				return nil
			}
		}
		f.value, f.keep, size = read(ctx)
		return nil
	}, func(err error) {
		cancel()
		if err != nil {
			f.value, f.keep = Window{State: WindowUnreadable, Detail: shortError(err)}, false
		}
		c.mu.Lock()
		delete(c.flights, key)
		if !f.keep || !c.put(key, f.value, size) {
			c.hold(key, f.value)
		}
		c.mu.Unlock()
		close(f.done)
		if background {
			<-c.slots
		}
	})
	return f
}

// put keeps value under key, and lets the least recently used go until the
// cache is within its bounds. It is false when value is larger than the whole
// cache, and not kept. c.mu is held.
func (c *WindowCache) put(key string, value any, size int) bool {
	if e, ok := c.byKey[key]; ok {
		c.held -= e.Value.(*kept).size
		c.order.Remove(e)
	}
	if size > WindowCacheBytes {
		delete(c.byKey, key)
		return false // larger than the whole cache: served, never kept
	}
	delete(c.brief, key)
	c.byKey[key] = c.order.PushFront(&kept{key: key, value: value, at: c.now(), size: size})
	c.held += size
	for c.order.Len() > WindowCacheEntries || c.held > WindowCacheBytes {
		oldest := c.order.Back()
		k := oldest.Value.(*kept)
		c.order.Remove(oldest)
		delete(c.byKey, k.key)
		c.held -= k.size
	}
	return true
}

// hold keeps an answer that is not kept for windowBriefTTL, the oldest going
// first past windowBriefEntries. c.mu is held.
func (c *WindowCache) hold(key string, value any) {
	c.brief[key] = &kept{key: key, value: value, at: c.now()}
	for len(c.brief) > windowBriefEntries {
		var oldest *kept
		for _, b := range c.brief {
			if oldest == nil || b.at.Before(oldest.at) {
				oldest = b
			}
		}
		delete(c.brief, oldest.key)
	}
}

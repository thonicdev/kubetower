package metrics

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func cachedRead(t *testing.T, c *DNSCache, q RangeQuerier, span DNSSpan, ns string, pods []DNSPodRef) DNSRead {
	t.Helper()
	got, err := c.Read(context.Background(), q, "prod", span, ns, pods)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestTheDNSCacheServesWhatItKeptUntilItsTTL(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	now := time.Now()
	c := NewDNSCache(nil)
	c.now = func() time.Time { return now }
	prom := healthyDNS()
	cachedRead(t, c, prom, DNSSpan15m, "kube-system", dnsPods)
	first := len(prom.queries())
	cachedRead(t, c, prom, DNSSpan15m, "kube-system", dnsPods)
	if len(prom.queries()) != first {
		t.Fatal("a second display inside the TTL read Prometheus again")
	}
	now = now.Add(16 * time.Second) // the 15-minute span's TTL is 15 s
	cachedRead(t, c, prom, DNSSpan15m, "kube-system", dnsPods)
	if len(prom.queries()) != 2*first {
		t.Errorf("after the TTL it asked %d queries, want a second read of %d", len(prom.queries())-first, first)
	}
	for span, ttl := range map[DNSSpan]time.Duration{DNSSpan15m: 15 * time.Second, DNSSpan1h: 30 * time.Second, DNSSpan6h: 2 * time.Minute} {
		if dnsTTL(span) != ttl {
			t.Errorf("TTL of %s = %s, want %s", span, dnsTTL(span), ttl)
		}
	}
}

func TestTheDNSCacheIsBounded(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	c := NewDNSCache(nil)
	prom := healthyDNS()
	// Twenty-four different namespaces: none of them may grow the cache past 16.
	for i := range 24 {
		cachedRead(t, c, prom, DNSSpan15m, fmt.Sprintf("dns-%d", i), dnsPods)
	}
	if c.Len() != DNSCacheEntries {
		t.Errorf("%d entries kept, want %d", c.Len(), DNSCacheEntries)
	}
	if c.held > DNSCacheBytes {
		t.Errorf("%d bytes held, ceiling %d", c.held, DNSCacheBytes)
	}
	// The least recently used went: dns-0 is read again, dns-23 is not.
	before := len(prom.queries())
	cachedRead(t, c, prom, DNSSpan15m, "dns-23", dnsPods)
	if len(prom.queries()) != before {
		t.Error("the most recent entry was dropped")
	}
	cachedRead(t, c, prom, DNSSpan15m, "dns-0", dnsPods)
	if len(prom.queries()) == before {
		t.Error("the oldest entry was kept past the bound")
	}
}

func TestAnAnswerLargerThanTheWholeCacheIsServedNeverKept(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	c := NewDNSCache(nil)
	big := make([]Point, DNSCacheBytes/16+1)
	var weighty DNSRead
	weighty.Series.QPS = big
	if dnsSize(weighty) <= DNSCacheBytes {
		t.Fatal("the fixture is not larger than the cache")
	}
	c.mu.Lock()
	c.put("k", weighty, time.Minute)
	c.mu.Unlock()
	if c.Len() != 0 {
		t.Error("an answer larger than the cache was kept")
	}
}

func TestOnlyAnAnswerThatReadOrFoundNothingIsKept(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	c := NewDNSCache(nil)
	down := &dnsProm{err: func(string) error { return fmt.Errorf("refused") }}
	if got := cachedRead(t, c, down, DNSSpan15m, "kube-system", dnsPods); got.State != WindowUnreadable {
		t.Fatalf("state %s", got.State)
	}
	if c.Len() != 0 {
		t.Error("a read that could not be made was kept")
	}
}

func TestConcurrentDisplaysShareOneRead(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	c := NewDNSCache(NewWindowCache())
	prom := healthyDNS()
	prom.delay = 20 * time.Millisecond
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := cachedRead(t, c, prom, DNSSpan1h, "kube-system", dnsPods); got.State != WindowRead {
				t.Errorf("state %s", got.State)
			}
		}()
	}
	wg.Wait()
	if n := len(prom.queries()); n != 14 {
		t.Errorf("fifty displays asked %d queries, want one read of 14", n)
	}
}

func TestADisplayThatGivesUpDoesNotFailTheRead(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	c := NewDNSCache(nil)
	prom := healthyDNS()
	prom.delay = 30 * time.Millisecond
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if got, _ := c.Read(gone, prom, "prod", DNSSpan15m, "kube-system", dnsPods); got.State != WindowUnreadable {
		t.Errorf("a display already gone read %s", got.State)
	}
	if len(prom.queries()) != 0 {
		t.Error("a read nobody waits for was started")
	}
	// One that stops waiting midway leaves the read to finish and be kept.
	mid, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	if got, _ := c.Read(mid, prom, "prod", DNSSpan15m, "kube-system", dnsPods); got.State != WindowUnreadable {
		t.Errorf("a display that stopped waiting read %s", got.State)
	}
	time.Sleep(700 * time.Millisecond)
	before := len(prom.queries())
	if got := cachedRead(t, c, prom, DNSSpan15m, "kube-system", dnsPods); got.State != WindowRead || len(prom.queries()) != before {
		t.Errorf("the read that outlived its display was not kept: %s, %d new queries", got.State, len(prom.queries())-before)
	}
}

func TestDNSReadsShareTheWindowReadCeiling(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	w := NewWindowCache()
	c := NewDNSCache(w)
	if c.shared == nil || cap(c.shared) != windowReads {
		t.Fatalf("the DNS cache does not take the window cache's %d read slots", windowReads)
	}
	// Every slot taken by usage windows: a DNS read waits, then says so.
	for range windowReads {
		w.reads <- struct{}{}
	}
	prom := healthyDNS()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	got, _ := c.Read(ctx, prom, "prod", DNSSpan15m, "kube-system", dnsPods)
	if got.State != WindowUnreadable {
		t.Errorf("state %s with no slot free", got.State)
	}
	if len(prom.queries()) != 0 {
		t.Error("a read asked Prometheus without a slot")
	}
	for range windowReads {
		<-w.reads
	}
	time.Sleep(10 * time.Millisecond)
	if got := cachedRead(t, c, prom, DNSSpan15m, "kube-system", dnsPods); got.State != WindowRead {
		t.Errorf("state %s once the slots are free", got.State)
	}
}

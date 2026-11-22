package metrics

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// What the cache of windows keeps, for whom, for how long, and how much - and
// that what it hands back is what a fresh read would have.

// clock is a time a test moves by hand.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

func cacheAt() (*WindowCache, *clock) {
	c := NewWindowCache()
	t := &clock{at: time.Unix(1_788_294_000, 0)}
	c.now = t.now
	return c, t
}

// mine keeps the keys in the given namespaces: a person narrowed to them.
func mine(namespaces ...string) func([]string) (map[string]bool, error) {
	return func(keys []string) (map[string]bool, error) {
		out := map[string]bool{}
		for _, k := range keys {
			for _, ns := range namespaces {
				out[k] = out[k] || strings.HasPrefix(k, ns+"/")
			}
		}
		return out, nil
	}
}

func cacheRead(t *testing.T, c *WindowCache, q RangeQuerier, keep func([]string) (map[string]bool, error)) Window {
	t.Helper()
	w, err := c.Read(context.Background(), q, "c", "", keep)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return w
}

// onGrid is a matrix whose samples are a step apart, as a range query's are,
// each with a value of its own; skip leaves a sample out, a gap.
func onGrid(skip func(i int) bool, rows ...promRow) string {
	series := make([]string, 0, len(rows))
	for r, row := range rows {
		var samples []string
		for i, v := range row.values {
			if skip == nil || !skip(i) {
				samples = append(samples, fmt.Sprintf(`[%d,"%.17g"]`, 1788200100+i*900, v+float64(r)/7+float64(i)/3))
			}
		}
		series = append(series, fmt.Sprintf(`{"metric":{"namespace":%q,"pod":%q,"container":%q},"values":[%s]}`,
			row.namespace, row.pod, row.container, strings.Join(samples, ",")))
	}
	return fmt.Sprintf(`{"status":"success","data":{"resultType":"matrix","result":[%s]}}`, strings.Join(series, ","))
}

// twoTeams is a cluster of two namespaces, two containers each, on the grid.
func twoTeams() *fakeProm {
	rows := func(v float64) []promRow {
		return []promRow{{"shop", "a", "app", flat(40, v)}, {"shop", "b", "app", flat(40, v)},
			{"billing", "c", "app", flat(40, v)}, {"billing", "d", "app", flat(40, v)}}
	}
	gaps := func(i int) bool { return i%7 == 3 }
	return &fakeProm{cpu: onGrid(gaps, rows(0.1)...), memory: onGrid(nil, rows(1<<26)...)}
}

// What a kept window hands back is what a fresh read hands back: every
// point, every field, bit for bit.
func TestAKeptWindowIsTheFreshOne(t *testing.T) {
	c, _ := cacheAt()
	prom := twoTeams()
	fresh, err := ReadWindowKept(context.Background(), prom, "c", "", nil)
	if err != nil || fresh.State != WindowRead {
		t.Fatalf("fresh read: %v %s %q", err, fresh.State, fresh.Detail)
	}
	first := cacheRead(t, c, prom, nil)
	asked := len(prom.asking())
	kept := cacheRead(t, c, prom, nil)
	if len(prom.asking()) != asked {
		t.Errorf("the second display asked Prometheus %d more times", len(prom.asking())-asked)
	}
	// The time of the read is the clock's, which a fresh read does not share.
	fresh.ReadAt, first.ReadAt = time.Time{}, time.Time{}
	keptAt := kept.ReadAt
	kept.ReadAt = time.Time{}
	for name, w := range map[string]Window{"the read that filled it": first, "a kept one": kept} {
		if !reflect.DeepEqual(w, fresh) {
			t.Errorf("%s differs from a fresh read:\n%+v\n%+v", name, w, fresh)
		}
	}
	// The caller's own maps: changing them changes nothing kept.
	delete(kept.CPU, "shop/a/app")
	again := cacheRead(t, c, prom, nil)
	if again.ReadAt != keptAt {
		t.Errorf("the same kept window was read at %v, then at %v", keptAt, again.ReadAt)
	}
	if again.ReadAt = (time.Time{}); !reflect.DeepEqual(again, fresh) {
		t.Error("a caller's change reached what is kept")
	}
}

// A kept window says when it was read, not when it was handed over: the page
// says its age from it. A window held briefly says the same.
func TestAKeptWindowCarriesTheTimeOfItsRead(t *testing.T) {
	c, clk := cacheAt()
	prom := twoTeams()
	readAt := clk.now()
	if w := cacheRead(t, c, prom, nil); !w.ReadAt.Equal(readAt) {
		t.Fatalf("the read that filled the cache says %v, want %v", w.ReadAt, readAt)
	}
	clk.advance(WindowCacheTTL - time.Minute)
	asked := len(prom.asking())
	w := cacheRead(t, c, prom, nil)
	if len(prom.asking()) != asked {
		t.Fatal("a fresh kept window was read again")
	}
	if !w.ReadAt.Equal(readAt) {
		t.Errorf("a window kept %v says it was read at %v, want %v", WindowCacheTTL-time.Minute, w.ReadAt, readAt)
	}

	// An answer held briefly: the CPU half failed, collected by a later poll.
	c, clk = cacheAt()
	half := twoTeams()
	half.cpuErr = errors.New("connection refused")
	failedAt := clk.now()
	if w := cacheRead(t, c, half, nil); w.CPUErr == nil {
		t.Fatalf("the CPU half did not fail: %+v", w)
	}
	clk.advance(windowBriefTTL / 2)
	asked = len(half.asking())
	if w := cacheRead(t, c, half, nil); len(half.asking()) != asked || !w.ReadAt.Equal(failedAt) {
		t.Errorf("a held answer says it was read at %v, want %v", w.ReadAt, failedAt)
	}
}

// Any series on the grid, gaps and all, unpacks to the very same points.
func TestAPackedSeriesUnpacksToTheSamePoints(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	step := WindowStep.Seconds()
	for round := range 200 {
		var points []Point
		for i := range 673 {
			if rng.Intn(5) > 0 {
				points = append(points, Point{T: 1788200100 + float64(i)*step, V: rng.ExpFloat64() * 1e3})
			}
		}
		var w packedWindow
		p, ok := w.pack(points)
		if !ok {
			t.Fatalf("round %d: a series on the grid was not packed", round)
		}
		if got := w.unpackSeries(p); !reflect.DeepEqual(got, points) && len(points) > 0 {
			t.Fatalf("round %d: unpacked %d points, not the %d packed", round, len(got), len(points))
		}
	}
	for name, points := range map[string][]Point{
		"off the grid":    {{T: 0, V: 1}, {T: 120, V: 2}},
		"going backwards": {{T: 900, V: 1}, {T: 0, V: 2}},
		"twice one time":  {{T: 0, V: 1}, {T: 0, V: 2}},
		"a fraction off":  {{T: 0, V: 1}, {T: 900.5, V: 2}},
	} {
		var w packedWindow
		if _, ok := w.pack(points); ok {
			t.Errorf("%s: packed, and would unpack to other times", name)
		}
	}
}

// Never broader than the person's rights: the scope is part of the key, and
// two people with the same scope share one read.
func TestAKeptWindowIsNeverServedToANarrowerScope(t *testing.T) {
	c, _ := cacheAt()
	prom := twoTeams()
	if everyone := cacheRead(t, c, prom, nil); len(everyone.CPU) != 4 {
		t.Fatalf("everyone sees %d containers", len(everyone.CPU))
	}
	alice := cacheRead(t, c, prom, mine("shop"))
	for k := range alice.Memory {
		if !strings.HasPrefix(k, "shop/") {
			t.Errorf("a person narrowed to shop was handed %s", k)
		}
	}
	if len(alice.Memory) != 2 {
		t.Errorf("alice sees %d containers, want shop's 2", len(alice.Memory))
	}
	asked := len(prom.asking())
	if bob := cacheRead(t, c, prom, mine("shop")); len(bob.Memory) != 2 || len(prom.asking()) != asked {
		t.Errorf("bob, with alice's scope, read again (%d queries) or saw %d", len(prom.asking())-asked, len(bob.Memory))
	}
	// Someone who may list both namespaces sees what everyone sees: the same
	// scope, the same entry.
	if all := cacheRead(t, c, prom, mine("shop", "billing")); len(all.Memory) != 4 || len(prom.asking()) != asked {
		t.Errorf("a person who may list every namespace read again, or saw %d", len(all.Memory))
	}
	if nobody := cacheRead(t, c, prom, mine()); nobody.State != WindowEmpty {
		t.Errorf("a person who may list nothing got %s", nobody.State)
	}
}

// Stale is served while one background read refreshes it; too old is read
// again before it is served.
func TestAStaleWindowIsServedAndReadAgainOnceInTheBackground(t *testing.T) {
	c, clk := cacheAt()
	prom := twoTeams()
	cacheRead(t, c, prom, nil)
	asked := len(prom.asking())

	clk.advance(WindowCacheTTL + time.Minute)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := cacheRead(t, c, prom, nil); w.State != WindowRead {
				t.Errorf("a stale window was not served: %s", w.State)
			}
		}()
	}
	wg.Wait()
	waitForFlights(t, c)
	// The listing and the window were both stale: each read once more.
	if got := len(prom.asking()) - asked; got != 3 {
		t.Errorf("%d queries for eight stale displays, want one listing and one window (3 queries)", got)
	}

	clk.advance(2*WindowCacheTTL + time.Minute)
	before := len(prom.asking())
	cacheRead(t, c, prom, nil)
	if len(prom.asking()) == before {
		t.Error("a window older than twice the TTL was served without being read again")
	}
}

func waitForFlights(t *testing.T, c *WindowCache) {
	t.Helper()
	for range 500 {
		c.mu.Lock()
		n := len(c.flights)
		c.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("a background read never ended")
}

// Concurrent displays of a window nobody has read share one read.
func TestConcurrentMissesShareOneRead(t *testing.T) {
	c, _ := cacheAt()
	prom := twoTeams()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); cacheRead(t, c, prom, nil) }()
	}
	wg.Wait()
	if got := len(prom.asking()); got != 3 {
		t.Errorf("%d queries for eight concurrent displays, want one listing and one window (3)", got)
	}
}

// A display that gives up stops waiting, and the read it started is kept for
// the next one.
func TestADisplayThatGivesUpStopsWaiting(t *testing.T) {
	c, _ := cacheAt()
	prom := twoTeams()
	if w := cacheRead(t, c, prom, mine("shop")); w.State != WindowRead {
		t.Fatalf("got %s", w.State)
	}
	prom.hold = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Window)
	go func() { w, _ := c.Read(ctx, prom, "c", "", nil); done <- w }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if w := <-done; w.State != WindowUnreadable {
		t.Errorf("a display that gave up got %s", w.State)
	}
	close(prom.hold)
	waitForFlights(t, c)
	asked := len(prom.asking())
	if w := cacheRead(t, c, prom, nil); w.State != WindowRead || len(prom.asking()) != asked {
		t.Errorf("the next display got %s after %d more queries; want the kept read", w.State, len(prom.asking())-asked)
	}
}

// A failure, a partial window, a half missing or a window off the grid is not
// kept: past the brief hold, the next display is when it may have passed.
func TestOnlyAWholeWindowIsKept(t *testing.T) {
	teams := twoTeams()
	for name, prom := range map[string]*fakeProm{
		"unreadable":   {cpu: teams.cpu, memory: teams.memory, cpuErr: errors.New("boom"), memErr: errors.New("boom")},
		"half missing": {cpu: teams.cpu, memory: teams.memory, cpuErr: errors.New("boom")},
		"no listing":   {countErr: errors.New("refused")},
		"off the grid": {cpu: matrix(promRow{"shop", "a", "app", flat(5, 1)}), memory: teams.memory},
	} {
		c, clk := cacheAt()
		cacheRead(t, c, prom, nil)
		clk.advance(windowBriefTTL)
		asked := len(prom.asking())
		cacheRead(t, c, prom, nil)
		if len(prom.asking()) == asked {
			t.Errorf("%s: kept and served again", name)
		}
	}
}

// An answer that is not kept - partial, a half missing, unreadable - is still
// served for windowBriefTTL, as it was answered, so the page polling for it
// collects the read that ended between two polls instead of starting another.
func TestAnAnswerNotKeptIsHeldBriefly(t *testing.T) {
	teams := twoTeams()
	cpu, memory := many("prod", 1200)
	for name, prom := range map[string]*fakeProm{
		"partial":      {cpu: cpu, memory: memory, failOn: `"p-0500|`},
		"half missing": {cpu: teams.cpu, memory: teams.memory, cpuErr: errors.New("boom")},
		"unreadable":   {cpu: teams.cpu, memory: teams.memory, cpuErr: errors.New("boom"), memErr: errors.New("boom")},
	} {
		c, clk := cacheAt()
		first := cacheRead(t, c, prom, nil)
		asked := len(prom.asking())
		clk.advance(windowBriefTTL - time.Second)
		again := cacheRead(t, c, prom, nil)
		if len(prom.asking()) != asked {
			t.Errorf("%s: %d more queries within the brief hold", name, len(prom.asking())-asked)
		}
		if again.State != first.State || again.Partial != first.Partial || again.Detail != first.Detail ||
			(again.CPUErr == nil) != (first.CPUErr == nil) || len(again.CPU) != len(first.CPU) {
			t.Errorf("%s: served %s partial %d %q, want it as answered: %s partial %d %q", name,
				again.State, again.Partial, again.Detail, first.State, first.Partial, first.Detail)
		}
		if name == "partial" && again.Partial != 1 {
			t.Errorf("the partial window was served as %d partial batches", again.Partial)
		}
	}
}

// A window larger than the whole cache is not kept, but held briefly too.
func TestAWindowLargerThanTheCacheIsHeldBriefly(t *testing.T) {
	c, clk := cacheAt()
	c.mu.Lock()
	if c.put("window\x00big", packedWindow{bytes: WindowCacheBytes + 1}, WindowCacheBytes+1) {
		t.Error("a window larger than the cache was kept")
	}
	c.mu.Unlock()
	f := c.fly(context.Background(), "window\x00big", nil, func(context.Context) (any, bool, int) {
		return packedWindow{read: Window{State: WindowRead}}, true, WindowCacheBytes + 1
	}, false)
	<-f.done
	read := 0
	again := func(context.Context) (any, bool, int) { read++; return Window{}, false, 0 }
	if v, _ := c.get(context.Background(), "window\x00big", nil, again); read != 0 || v.(packedWindow).read.State != WindowRead {
		t.Errorf("read again (%d) instead of serving the held window", read)
	}
	clk.advance(windowBriefTTL)
	if c.get(context.Background(), "window\x00big", nil, again); read != 1 {
		t.Errorf("past the hold, read %d times, want once", read)
	}
}

// Two displays of one key share one read, whoever asked first.
func TestTwoDisplaysOfOneKeyShareOneRead(t *testing.T) {
	c, _ := cacheAt()
	prom := twoTeams()
	prom.hold = make(chan struct{})
	done := make(chan Window, 2)
	for range 2 {
		go func() { w, _ := c.Read(context.Background(), prom, "c", "", nil); done <- w }()
	}
	time.Sleep(30 * time.Millisecond)
	close(prom.hold)
	for range 2 {
		if w := <-done; w.State != WindowRead {
			t.Errorf("got %s", w.State)
		}
	}
	if got := len(prom.asking()); got != 3 {
		t.Errorf("%d queries for two displays of one key, want one listing and one window (3)", got)
	}
}

// Across keys, at most windowReads window reads run at once in the
// foreground, whoever went away; and a display already gone starts none.
func TestForegroundReadsAreBoundedAcrossKeys(t *testing.T) {
	c, _ := cacheAt()
	prom := twoTeams()
	prom.hold = make(chan struct{})
	// Each display goes away while the reads are held. Its timeout is long
	// enough for a read to start on a loaded runner under -race: at 20 ms
	// every display could be gone before any read began, and none ran.
	const away = 300 * time.Millisecond
	for i := range 3 * windowReads {
		ctx, cancel := context.WithTimeout(context.Background(), away)
		go func() { defer cancel(); _, _ = c.Read(ctx, prom, fmt.Sprintf("c-%d", i), "", nil) }()
	}
	inFlight := func() int {
		prom.mu.Lock()
		defer prom.mu.Unlock()
		return prom.maxInFlight
	}
	for deadline := time.Now().Add(5 * time.Second); inFlight() == 0 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	// Past every display's timeout, the reads still held are the detached ones.
	time.Sleep(away + 100*time.Millisecond)
	most := inFlight()
	// Each window read here is one batch: its CPU and memory queries.
	if most == 0 || most > 2*windowReads {
		t.Errorf("%d range queries in flight at once, want 1 to %d", most, 2*windowReads)
	}
	close(prom.hold)
	waitForFlights(t, c)

	gone, cancel := context.WithCancel(context.Background())
	cancel()
	before := len(prom.asking())
	if w, _ := c.Read(gone, prom, "never", "", nil); w.State != WindowUnreadable {
		t.Errorf("a display already gone got %s", w.State)
	}
	waitForFlights(t, c)
	if len(prom.asking()) != before {
		t.Error("a display already gone started a read")
	}
}

// The bounds: entries, then bytes, the least recently used going first.
func TestTheCacheKeepsWithinItsBounds(t *testing.T) {
	c, _ := cacheAt()
	prom := twoTeams()
	for i := range WindowCacheEntries + 10 {
		if _, err := c.Read(context.Background(), prom, fmt.Sprintf("c-%d", i), "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if c.order.Len() > WindowCacheEntries || len(c.byKey) != c.order.Len() {
		t.Errorf("%d entries kept (%d indexed), past %d", c.order.Len(), len(c.byKey), WindowCacheEntries)
	}
	if _, kept := c.byKey["window\x00c-0\x00\x00*"]; kept {
		t.Error("the least recently used window is still kept")
	}
	c.mu.Lock()
	c.put("big", packedWindow{}, WindowCacheBytes-1)
	held, n := c.held, c.order.Len()
	c.put("too big", packedWindow{}, WindowCacheBytes+1)
	c.mu.Unlock()
	if held > WindowCacheBytes || n != 1 || c.byKey["too big"] != nil {
		t.Errorf("%d bytes in %d entries; a window larger than the cache was kept: %v", held, n, c.byKey["too big"] != nil)
	}
}

// What a kept window holds, measured on the heap, is never more than it is
// counted as: WindowCacheBytes bounds the heap, not a guess at it. Measured for
// containers that ran the whole week and for short-lived ones, a CronJob's
// pods, whose keys and slots outweigh their samples.
func TestAKeptWindowCostsWhatItIsCounted(t *testing.T) {
	week := int(WindowSpan/WindowStep) + 1
	for _, samples := range []int{week, 4} {
		heap, counted := keptCost(t, 2_000, samples)
		t.Logf("%d samples a series: a container costs %.0f bytes on the heap, counted %.0f; %d bytes hold %d such containers",
			samples, heap, counted, WindowCacheBytes, int(WindowCacheBytes/counted))
		if heap > counted {
			t.Errorf("%d samples a series: the heap holds %.0f bytes a container, counted %.0f", samples, heap, counted)
		}
		if counted > 1.25*heap {
			t.Errorf("%d samples a series: counted %.0f bytes for %.0f on the heap", samples, counted, heap)
		}
	}
}

// keptCost is what n containers of samples points a half cost kept: on the
// heap, and as counted, per container.
func keptCost(t *testing.T, n, samples int) (heap, counted float64) {
	step := WindowStep.Seconds()
	var packed packedWindow
	measured := measure(func() any {
		w := Window{State: WindowRead, CPU: map[string][]Point{}, Memory: map[string][]Point{}}
		for i := range n {
			// As long as a Deployment's pod in a long namespace.
			k := ContainerKey("payments-production", fmt.Sprintf("checkout-api-7d9f8b6c5d-%05d", i), "app")
			points := make([]Point, samples)
			for j := range points {
				points[j] = Point{T: 1788200100 + float64(j)*step, V: float64(i*j) / 3}
			}
			w.CPU[k], w.Memory[k] = points, points
		}
		var ok bool
		if packed, ok = packWindow(w); !ok {
			t.Fatal("a window on the grid was not packed")
		}
		return packed
	})
	return measured / float64(n), float64(packed.bytes) / float64(n)
}

// measure is the heap build's answer still holds.
func measure(build func() any) float64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	v := build()
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(v)
	return float64(after.HeapAlloc) - float64(before.HeapAlloc)
}

// What keeping costs, for a window of 2 400 containers that ran the whole
// week: packing it, on the display that read it, and unpacking it, on every
// display it is kept for.
func BenchmarkAWindowOf2400Containers(b *testing.B) {
	step := WindowStep.Seconds()
	week := int(WindowSpan/WindowStep) + 1
	w := Window{State: WindowRead, CPU: map[string][]Point{}, Memory: map[string][]Point{}}
	for i := range 2400 {
		k := ContainerKey("shop", fmt.Sprintf("api-7d9f8b6c5d-%05d", i), "app")
		points := make([]Point, week)
		for j := range points {
			points[j] = Point{T: 1788200100 + float64(j)*step, V: float64(i*j) / 3}
		}
		w.CPU[k], w.Memory[k] = points, points
	}
	b.Run("packed", func(b *testing.B) {
		for b.Loop() {
			packWindow(w)
		}
	})
	packed, _ := packWindow(w)
	b.Run("unpacked", func(b *testing.B) {
		for b.Loop() {
			packed.unpack()
		}
	})
}

package archive

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The settings page calls RefreshUsage on **every** render, and the page
// refreshes itself. Moving the retention selector while one walk is running must
// not stack concurrent traversals of the whole archive -- an `os.ReadDir` per
// day per cluster, on the same disk the log sinks are writing to.

// TestConcurrentUsageWalksCoalesceIntoOne.
//
// The assertion counts *days read*, not calls: a caller that queued behind a
// walk and then took its answer costs nothing, and a caller that queued and then
// walked again costs the whole tree. Twenty callers in flight at once must
// produce exactly one traversal.
//
// "In flight at once" is made a fact here, not left to the scheduler. Merely
// releasing twenty goroutines does not guarantee overlap: the archive holds one
// file, so a walk takes microseconds, and on a loaded runner under -race the
// goroutines can run one after another -- twenty sequential walks, which is the
// correct outcome for twenty sequential callers.
//
// So every caller is held at the archive's one test seam, usageArrived, until
// all twenty have recorded the generation they arrived at. Only then may any of
// them take the walk lock. Whichever wins walks; the other nineteen arrived
// before that walk completed, so each must find the generation moved and take
// its answer. That holds under any interleaving, GOMAXPROCS=1 included, and it
// fails at once if the generation check -- the coalescing -- is removed.
//
// The seam is a nil func field on Archive, called only when set. It is the one
// production change made for this test, and it is test-only in spirit: nothing
// outside this file sets it.
func TestConcurrentUsageWalksCoalesceIntoOne(t *testing.T) {
	a, _ := newArchive(t, on())

	sink := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	sink.Write([]byte("one line\n"))
	sink.Close()

	// Traversals are counted by the archive itself: usageGen is incremented once
	// per **completed** walk, which is exactly the quantity the finding is about.
	before := a.usageGen.Load()

	const callers = 20

	// The barrier. It is set after the setup above, so only the twenty callers
	// below pass through it. A caller that never arrives would hold the rest
	// forever; the timeout turns that into a failure instead of a hung suite.
	var arrived atomic.Int32
	all := make(chan struct{})
	a.usageArrived = func() {
		if arrived.Add(1) == callers {
			close(all)
		}
		select {
		case <-all:
		case <-time.After(30 * time.Second):
			t.Errorf("only %d of %d callers reached RefreshUsage", arrived.Load(), callers)
		}
	}

	var wg sync.WaitGroup
	results := make([]Usage, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = a.RefreshUsage()
		}()
	}
	wg.Wait()

	// The barrier proves nothing if RefreshUsage stopped calling it: the test
	// would quietly be the scheduling-dependent one again. Say so instead.
	if got := arrived.Load(); got != callers {
		t.Fatalf("the usageArrived seam ran %d times for %d callers; RefreshUsage no "+
			"longer holds callers where this test needs them", got, callers)
	}
	if n := a.usageGen.Load() - before; n != 1 {
		t.Errorf("%d callers, all waiting before any walk could complete, produced %d "+
			"traversals of the archive; they must coalesce into exactly one", callers, n)
	}
	// And every caller got a real answer rather than a zero value: coalescing
	// that handed back nothing would satisfy the count above and be worse than
	// walking twice.
	for i, got := range results {
		if got.ComputedAt.IsZero() {
			t.Fatalf("caller %d was given a figure nothing computed", i)
		}
		if got.Bytes != results[0].Bytes {
			t.Errorf("caller %d saw %d bytes and caller 0 saw %d; one walk means one answer",
				i, got.Bytes, results[0].Bytes)
		}
		if got.Approximate {
			t.Errorf("caller %d was handed an approximate figure by a method that walks", i)
		}
	}
}

// TestASequentialRefreshStillWalks is the other half, and it is the contract
// the method's own name states.
//
// The coalescing key is a generation counter and **not** a freshness window in
// seconds, for exactly this: the clock in this package is injectable and frozen
// in tests, so a time-based window would read "no time has passed" for every
// sequential call and turn RefreshUsage into a cache that never refreshes;
// TestASizeQueryDoesNotRewalkTheTree catches that too.
func TestASequentialRefreshStillWalks(t *testing.T) {
	a, _ := newArchive(t, on())

	first := a.RefreshUsage()

	sink := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	sink.Write([]byte("five hundred more bytes would be excessive\n"))
	sink.Close()

	second := a.RefreshUsage()
	if second.Bytes <= first.Bytes {
		t.Errorf("a sequential RefreshUsage served a stale figure: %d then %d",
			first.Bytes, second.Bytes)
	}
	if second.Approximate {
		t.Error("a fresh walk is marked approximate")
	}
}

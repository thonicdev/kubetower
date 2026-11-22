package access_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

// inFlight counts reviews in the air, the way the per-cluster limiter sees them.
type inFlight struct {
	mu       sync.Mutex
	now, max int
	asked    int
}

func (f *inFlight) answer(accesstest.Question) (bool, error) {
	f.mu.Lock()
	f.now++
	f.asked++
	if f.now > f.max {
		f.max = f.now
	}
	f.mu.Unlock()
	time.Sleep(2 * time.Millisecond)
	f.mu.Lock()
	f.now--
	f.mu.Unlock()
	return false, nil
}

func rowsIn(namespaces int) []row {
	rows := make([]row, 0, namespaces)
	for i := 0; i < namespaces; i++ {
		rows = append(rows, row{fmt.Sprintf("ns-%03d", i), "pod"})
	}
	return rows
}

// TestANarrowedListNeverHasMoreThanItsShareOfReviewsInFlight: a person denied
// the list across every namespace is narrowed with one review per namespace,
// each an HTTP request through the limiter every person shares. They go
// ReviewsInFlight at a time, never all at once.
func TestANarrowedListNeverHasMoreThanItsShareOfReviewsInFlight(t *testing.T) {
	counter := &inFlight{}
	g := inCluster(&accesstest.Reviews{Answer: counter.answer}, nil)

	_, seen, err := access.List(bob(), g, "prod", listPods, rowNamespace, (&readCount{}).read(rowsIn(access.NarrowedNamespaces)))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !seen.Narrowed {
		t.Fatal("the list was not narrowed")
	}
	if counter.asked != access.NarrowedNamespaces+1 {
		t.Fatalf("asked %d reviews, want the whole list and each of %d namespaces", counter.asked, access.NarrowedNamespaces)
	}
	if counter.max > access.ReviewsInFlight {
		t.Errorf("%d reviews were in the air at once, past the bound of %d", counter.max, access.ReviewsInFlight)
	}
}

// TestAListAcrossTooManyNamespacesIsRefusedRatherThanNarrowed is the second
// half: past NarrowedNamespaces the console asks no review per namespace at all,
// and says why, with what to do instead.
func TestAListAcrossTooManyNamespacesIsRefusedRatherThanNarrowed(t *testing.T) {
	counter := &inFlight{}
	g := inCluster(&accesstest.Reviews{Answer: counter.answer}, nil)

	_, _, err := access.List(bob(), g, "prod", listPods, rowNamespace, (&readCount{}).read(rowsIn(access.NarrowedNamespaces+1)))
	var refused *access.NotNarrowed
	if !errors.As(err, &refused) {
		t.Fatalf("a list across %d namespaces was not refused: %v", access.NarrowedNamespaces+1, err)
	}
	if !strings.Contains(err.Error(), "choose a namespace") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
	if counter.asked != 1 {
		t.Errorf("asked %d reviews, want only the list's own", counter.asked)
	}
}

// TestANarrowingReadStopsAtItsBound is the read: the context List hands a read
// it is about to narrow says so, and a read that honors it (resources.Paged)
// answers TooLargeToNarrow, which List refuses with the same sentence. A list
// the person may make whole carries no such bound.
func TestANarrowingReadStopsAtItsBound(t *testing.T) {
	counter := &inFlight{}
	g := inCluster(&accesstest.Reviews{Answer: counter.answer}, nil)
	var narrowing bool
	read := func(ctx context.Context) ([]row, error) {
		narrowing = access.Narrowing(ctx)
		return nil, access.TooLargeToNarrow()
	}
	_, _, err := access.List(bob(), g, "prod", listPods, rowNamespace, read)
	if !narrowing {
		t.Error("the read of a list about to be narrowed was not told so")
	}
	if err == nil || !strings.Contains(err.Error(), "choose a namespace") {
		t.Errorf("a narrowing read past its bound was not refused with a sentence: %v", err)
	}
	if counter.asked != 1 {
		t.Errorf("asked %d reviews after the read stopped, want only the list's own", counter.asked)
	}

	whole := inCluster(&accesstest.Reviews{}, nil)
	_, _, _ = access.List(carol(), whole, "prod", listPods, rowNamespace, func(ctx context.Context) ([]row, error) {
		narrowing = access.Narrowing(ctx)
		return nil, nil
	})
	if narrowing {
		t.Error("a list the person may make whole was read under the narrowing bound")
	}
}

// eachOver is a paged read for Each: it hands every row to keep, a page of ten
// at a time, and stops at the first page boundary after its context ends - the
// way a client-go listing stops when the request's context is canceled.
func eachOver(rows []row, pages *int) func(context.Context, func(*row) bool) error {
	return func(ctx context.Context, keep func(*row) bool) error {
		for i := range rows {
			if i%10 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
				*pages++
			}
			keep(&rows[i])
		}
		return nil
	}
}

// **Each is held to narrow's bound.** List refuses a list spanning more than
// NarrowedNamespaces before it asks a single review per namespace; Each, which
// asks one for every new namespace a walk meets, each through the limiter every
// person shares, asks at most NarrowedNamespaces, then stops the read and
// refuses with List's own sentence.
func TestEachAsksNoMoreReviewsThanANarrowedListMay(t *testing.T) {
	counter := &inFlight{}
	g := inCluster(&accesstest.Reviews{Answer: counter.answer}, nil)

	pages := 0
	rows := rowsIn(access.NarrowedNamespaces + 50)
	_, err := access.Each(bob(), g, "prod", listPods, rowNamespace, eachOver(rows, &pages))
	var refused *access.NotNarrowed
	if !errors.As(err, &refused) {
		t.Fatalf("a walk across %d namespaces was not refused: %v", len(rows), err)
	}
	if !strings.Contains(err.Error(), "choose a namespace") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
	if counter.asked > access.NarrowedNamespaces+1 {
		t.Errorf("asked %d reviews in one walk, past the list's own and %d namespaces", counter.asked, access.NarrowedNamespaces)
	}
	if want := (access.NarrowedNamespaces + 50 + 9) / 10; pages >= want {
		t.Errorf("the read went on for all %d pages after the walk was refused", pages)
	}
}

// And at the bound itself the walk is narrowed: the cap is "more
// than", as narrow's is.
func TestEachNarrowsAWalkUpToTheBound(t *testing.T) {
	counter := &inFlight{}
	g := inCluster(&accesstest.Reviews{Answer: counter.answer}, nil)

	pages := 0
	seen, err := access.Each(bob(), g, "prod", listPods, rowNamespace, eachOver(rowsIn(access.NarrowedNamespaces), &pages))
	if err != nil {
		t.Fatalf("a walk across exactly %d namespaces was refused: %v", access.NarrowedNamespaces, err)
	}
	if !seen.Narrowed {
		t.Error("the walk was not narrowed")
	}
	if counter.asked != access.NarrowedNamespaces+1 {
		t.Errorf("asked %d reviews, want the list's own and each of %d namespaces", counter.asked, access.NarrowedNamespaces)
	}
}

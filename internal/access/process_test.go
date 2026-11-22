package access_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/identity"
)

// person is one of many people signed in through the provider, each with a
// name of their own, so no two of them share a held answer or a flight.
func person(i int) context.Context {
	return identity.With(context.Background(), identity.Who{
		Name: fmt.Sprintf("person-%02d@example.com", i), Method: identity.SSO, Groups: []string{"oidc:viewers"}})
}

// clientQPS is the per-cluster rate limiter's steady rate (internal/cluster,
// limits.go): what turns reviews queued ahead of a read into its wait.
const clientQPS = 50

// readBudget is how long a person's read may wait behind reviews at the
// limiter while other people narrow - a third of a second is ReviewsPerProcess
// at clientQPS, and the budget is one second, a list page's own refresh
// being two.
const readBudget = time.Second

// TestManyPeopleNarrowingAtOnceStayWithinTheProcesssShare: ReviewsInFlight
// bounds one call, so twenty people narrowing at once would put twenty times it
// in the air: 160 reviews on the one 50-QPS bucket, every other person's read
// waiting behind them. The whole process has at most ReviewsPerProcess in the
// air, and a read
// arriving in the middle of the flood finds no more than that ahead of it.
func TestManyPeopleNarrowingAtOnceStayWithinTheProcesssShare(t *testing.T) {
	const people = 20
	counter := &inFlight{}
	g := inCluster(&accesstest.Reviews{Answer: counter.answer}, nil)

	var wg sync.WaitGroup
	errs := make([]error, people)
	for i := 0; i < people; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = access.List(person(i), g, "prod", listPods, rowNamespace,
				(&readCount{}).read(rowsIn(access.NarrowedNamespaces)))
		}()
	}

	// A read of somebody else's, once the flood is under way: what it finds
	// queued ahead of it at the limiter is every review in the air.
	deadline := time.Now().Add(10 * time.Second)
	for {
		counter.mu.Lock()
		asked := counter.asked
		counter.mu.Unlock()
		if asked >= people*access.ReviewsInFlight || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	counter.mu.Lock()
	ahead := counter.now
	counter.mu.Unlock()
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("person %d: List: %v", i, err)
		}
	}
	if want := people * (access.NarrowedNamespaces + 1); counter.asked != want {
		t.Fatalf("asked %d reviews, want each person's list and each of their %d namespaces (%d)",
			counter.asked, access.NarrowedNamespaces, want)
	}
	if counter.max > access.ReviewsPerProcess {
		t.Errorf("%d reviews were in the air at once across %d people, past the process's %d",
			counter.max, people, access.ReviewsPerProcess)
	}
	if ahead > access.ReviewsPerProcess {
		t.Errorf("a read arriving mid-flood found %d reviews ahead of it, past the process's %d", ahead,
			access.ReviewsPerProcess)
	}
	if wait := time.Duration(ahead) * time.Second / clientQPS; wait > readBudget {
		t.Errorf("a read arriving mid-flood waits %s behind %d reviews at %d a second, past its budget of %s",
			wait, ahead, clientQPS, readBudget)
	}
}

// held is a review that does not answer until it is let go, so a test can
// fill the process's slots and look at what waits.
type held struct {
	mu      sync.Mutex
	started int
	release chan struct{}
}

func (h *held) answer(accesstest.Question) (bool, error) {
	h.mu.Lock()
	h.started++
	h.mu.Unlock()
	<-h.release
	return true, nil
}

func (h *held) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.started
}

// A review past the process's share waits in this process, not at the
// limiter: with every slot taken, the next review is not asked of the API
// server until one is given back - and is then asked, answered and held.
func TestAReviewPastTheProcesssShareWaitsForASlot(t *testing.T) {
	h := &held{release: make(chan struct{})}
	g := inCluster(&accesstest.Reviews{Answer: h.answer}, nil)

	var wg sync.WaitGroup
	for i := 0; i < access.ReviewsPerProcess+1; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.Review(person(i), "prod", deletePods)
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.count() < access.ReviewsPerProcess && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // room for a review the cap failed to hold back
	if got := h.count(); got != access.ReviewsPerProcess {
		t.Fatalf("%d reviews reached the API server with %d slots, want exactly the slots", got, access.ReviewsPerProcess)
	}
	close(h.release)
	wg.Wait()
	if got := h.count(); got != access.ReviewsPerProcess+1 {
		t.Errorf("%d reviews were asked once the slots came back, want all %d", got, access.ReviewsPerProcess+1)
	}
}

// The desktop has no process-wide share: it reaches many clusters through one
// gate, and a cluster that does not answer must not hold the slots of those
// that do. More than ReviewsPerProcess reviews reach the API server at once.
func TestTheDesktopHasNoProcessWideShare(t *testing.T) {
	h := &held{release: make(chan struct{})}
	g := (&accesstest.Reviews{Answer: h.answer}).Gate()
	defer close(h.release)

	for i := 0; i < access.ReviewsPerProcess+4; i++ {
		go g.Review(context.Background(), fmt.Sprintf("cluster-%02d", i), deletePods)
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.count() < access.ReviewsPerProcess+4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := h.count(); got != access.ReviewsPerProcess+4 {
		t.Errorf("%d of %d desktop reviews reached their clusters; the desktop was held to the server "+
			"profile's share", got, access.ReviewsPerProcess+4)
	}
}

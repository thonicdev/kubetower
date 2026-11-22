package logs

// A stream that ends must take everything it started with it.
//
// This package is the one place in the console where a browser holds an open
// connection to a cluster through us for up to half an hour, and it is the one
// where "the tab was closed" is the *normal* way a stream ends. Without a test
// counting goroutines nothing would notice a follow that left one behind — and
// one per opened panel, over a day of clicking, is a process that never gives
// memory back.
//
// The panel is opened and abandoned rather than allowed to finish, because that
// is the case with something to leak: a stream that reaches its own end tidies
// up on the way out, and a stream the browser walked away from has to be tidied
// up by somebody noticing.
//
// **This is a guard, not a proof, and the difference is worth stating.** It
// passes with `defer stream.Close()` removed from the handler, because the
// handler is protected twice over and either half is enough: the request
// context cancels the apiserver stream when the browser goes, *and* the write
// into the ResponseWriter fails, which ends pump. What this test catches is a
// change that detaches the stream from the request — `context.WithoutCancel` is
// a pattern this codebase uses correctly in four other places and would be
// wrong here — and simultaneously stops the write error ending the loop.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

// goroutinesSettleTo waits for the count to come back down. A bare comparison
// after the fact is flaky for reasons that have nothing to do with this package
// — an http.Transport reaps its idle connections on its own schedule — so the
// assertion is "it comes back", with a deadline, rather than "it is back now".
func goroutinesSettleTo(t *testing.T, want int, slack int) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	got := runtime.NumGoroutine()
	for time.Now().Before(deadline) {
		got = runtime.NumGoroutine()
		if got <= want+slack {
			return got
		}
		runtime.GC()
		time.Sleep(20 * time.Millisecond)
	}
	return got
}

func TestAbandonedLogStreamsLeaveNoGoroutinesBehind(t *testing.T) {
	const streams = 25

	// Never stops on its own: the pod is talking and only the reader going away
	// can end this.
	cluster := fakeAPI(t, 5*time.Millisecond, 0)

	clients := newFakeClients(t, cluster.URL, 0)
	s := New(clients, accesstest.Allow(), nil, nil)
	// Long enough that the deadline is not what ends these — the point is that
	// the *client leaving* ends them.
	s.deadline = time.Minute

	front := httptest.NewServer(s.Handler())
	defer front.Close()

	// One request through the whole stack first, so that everything lazily
	// started — the transport's connection pool, the test server's own
	// bookkeeping — is already running when the baseline is taken.
	warm(t, front.URL)
	baseline := goroutinesSettleTo(t, runtime.NumGoroutine(), 0)

	var wg sync.WaitGroup
	cancels := make([]context.CancelFunc, 0, streams)
	// Its own transport, not http.DefaultTransport, and that is the difference
	// between a leak test and a flaky one. The default is shared by every test
	// in this binary, so its idle connections - and the reader and writer
	// goroutine each one keeps - are counted here as though this test had
	// leaked them. With a transport of its own, the pool this test fills is the
	// pool it can empty.
	client := &http.Client{Transport: &http.Transport{}}
	for i := 0; i < streams; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			front.URL+"/kt-api/logs?cluster=docker-desktop&namespace=n&pod=p&container=app", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("stream %d did not open: %v", i, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("stream %d answered %d", i, resp.StatusCode)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer resp.Body.Close()
			// Read until the cancel below tears the connection down.
			_, _ = io.Copy(io.Discard, resp.Body)
		}()
	}

	// They are genuinely running: the count has to have gone up, or this test
	// would pass against a handler that refused every request.
	if during := runtime.NumGoroutine(); during <= baseline {
		t.Fatalf("%d streams did not raise the goroutine count above %d (got %d); "+
			"nothing was actually held open and this test proves nothing", streams, baseline, during)
	}

	// The browser goes away. Nothing else ends these streams.
	for _, cancel := range cancels {
		cancel()
	}
	wg.Wait()
	// A canceled request returns its connection to the pool rather than
	// closing it, and an idle connection is not free: the transport keeps a
	// reader and a writer goroutine per entry until IdleConnTimeout, which is
	// ninety seconds by default and far past this test's ten-second wait. On an
	// idle machine they are torn down before anyone looks; on a loaded runner
	// they are not, and the test fails having found nothing wrong.
	//
	// Widening the slack would be the obvious fix and the wrong one - slack is
	// what stops a leak test detecting leaks. Emptying the pool is deterministic
	// and costs nothing.
	client.CloseIdleConnections()

	// Slack, not equality: the transport keeps a reaper or two of its own around
	// for a while, and this test is about tens of leaked goroutines, not about
	// two.
	if got := goroutinesSettleTo(t, baseline, 5); got > baseline+5 {
		t.Errorf("after %d abandoned streams the goroutine count settled at %d, from a baseline of %d: "+
			"a panel opened and closed leaves work behind", streams, got, baseline)
	}
}

// warm runs one complete stream so nothing lazily initialized is counted as a
// leak by the test that follows.
func warm(t *testing.T, base string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/kt-api/logs?cluster=docker-desktop&namespace=n&pod=p&container=app", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("warm-up stream: %v", err)
	}
	buf := make([]byte, 64)
	_, _ = resp.Body.Read(buf)
	cancel()
	_ = resp.Body.Close()
}

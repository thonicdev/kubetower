package health

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// What an abandoned probe costs after the page has been drawn.
//
// `probe` answers the caller from a select on its own deadline, so a cluster that
// never replies costs the page one timeout and no more. The goroutine behind the
// select is a different question: without the context
// (`ServerVersionWithContext`, client-go 0.37.0) it runs on to client-go's own
// request timeout.
//
// Fourteen contexts against a server that takes the request and never answers,
// with a 2 s deadline:
//
//	without the context: 70 goroutines after All() returned, still up 28 s
//	        later, draining at 30 s — internal/cluster/limits.go's
//	        requestTimeout. 14 of those 70 are the test server's own blocked
//	        handlers, so 56 are ours: four per context, each holding a socket.
//	with it: 55 immediately after, down to the test server's 14 within a second.
//
// So it is not a *leak* — every goroutine terminates — and this is about socket
// lifetime rather than leaks. What is not fixable here: `net/http` finishes a dial it has already
// begun so the connection can be pooled, so a TLS handshake that hangs keeps two
// goroutines per context alive for up to `TLSHandshakeTimeout` — 10 s, set by
// client-go in transport/cache.go:149. Measured at 28 goroutines for 9 s over
// fourteen contexts, and bounded by somebody else's constant rather than ours.
//
// And the goroutine itself stays whatever happens to the context, because the
// context is not the only thing that can hang: client-go runs an exec credential
// plugin with a plain `exec.Command` and no context at all (client-go 0.37.0,
// plugin/pkg/client/auth/exec/exec.go:458, called from roundTripper.RoundTrip),
// so a wedged `aws eks get-token` is a wedged RoundTrip that no cancellation
// reaches. Removing the goroutine would let one of those hold the whole round.
func TestAnAbandonedProbeDoesNotOutliveItsDeadline(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")

	// A server that accepts the request and never answers it. TCP and HTTP both
	// complete, so nothing below client-go's own 30 s request timeout applies —
	// which is what makes this the case that measures the bound rather than
	// somebody else's handshake timeout.
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	t.Cleanup(func() { close(release); server.Close() })

	const contexts = 14
	servers := map[string]string{}
	for i := 0; i < contexts; i++ {
		servers[fmt.Sprintf("ctx-%02d", i)] = server.URL
	}
	kubeconfig := filepath.Join(t.TempDir(), "config")
	writeKubeconfig(t, kubeconfig, servers)
	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}

	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{kubeconfig},
		Timeout: 300 * time.Millisecond})

	if n := probeGoroutines(); n != 0 {
		t.Fatalf("%d probe goroutines before anything was asked", n)
	}

	rows := h.All(context.Background(), true)
	if len(rows) != contexts || rows[0].State != StateTimeout {
		t.Fatalf("got %d rows, first %q (%s) — the server answered, so there is nothing "+
			"abandoned to measure", len(rows), rows[0].State, rows[0].Detail)
	}

	// Generous, and deliberately nowhere near the number that matters: with the
	// context withheld these live for client-go's requestTimeout of 30 s, so two
	// seconds separates "goes with the deadline" from "goes when somebody else's
	// constant says so" without making the test itself slow.
	deadline := time.Now().Add(2 * time.Second)
	for {
		n := probeGoroutines()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d probe goroutines are still running two seconds after the "+
				"page was answered, so the deadline bounds the caller and not the work: "+
				"each one holds a socket and a request until client-go's own 30s "+
				"requestTimeout. The context has to reach the request — see "+
				"serverGitVersion", n, contexts)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// probeGoroutines counts the goroutines still inside probe's ask.
//
// By stack frame and not by `runtime.NumGoroutine()`, because the fixture in this
// test is an in-process HTTP server whose own blocked handlers are goroutines
// too: a total would count fourteen of somebody else's and could never reach
// zero. Naming the frame also means the count cannot drift when an unrelated
// goroutine is added elsewhere in the package.
func probeGoroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), "health.(*Health).startAsk.func")
		}
		buf = make([]byte, 2*len(buf))
	}
}

// pastDeadline is the moment at issue above: the probe's deadline
// has passed and the timer that closes Done has not run yet, so Err is still
// nil. A real context cannot be held there, which is why it is a fake.
type pastDeadline struct{ context.Context }

func (pastDeadline) Deadline() (time.Time, bool) { return time.Now().Add(-time.Millisecond), true }

// TestAnErrorAfterTheDeadlineIsTheDeadline: an ask that comes back with an
// error once the probe's own deadline has passed reports a timeout, whatever
// the error says - here client-go's rate limiter, refusing an ask that started
// too late, in words classify reads as `ko`. Before the
// deadline the same error is still classified as itself.
func TestAnErrorAfterTheDeadlineIsTheDeadline(t *testing.T) {
	h := New(Options{Timeout: 300 * time.Millisecond})
	late := errors.New("client rate limiter Wait returned an error: rate: Wait(n=1) would exceed context deadline")

	got := h.answered(pastDeadline{context.Background()}, probeAnswer{err: late}, nil)
	if got.state != StateTimeout || got.abandoned {
		t.Fatalf("an error past the deadline came back state %q (%s), abandoned=%t; want %q",
			got.state, got.detail, got.abandoned, StateTimeout)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	if got := h.answered(ctx, probeAnswer{err: late}, nil); got.state != StateKO {
		t.Errorf("the same error well before the deadline came back %q; it is not a timeout of ours", got.state)
	}
	if got := h.answered(pastDeadline{context.Background()}, probeAnswer{version: "v1.31.0"}, nil); got.state != StateOK {
		t.Errorf("an answer that arrived with the deadline came back %q; an answer is an answer", got.state)
	}
}

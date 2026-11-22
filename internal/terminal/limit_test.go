package terminal

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// --- the counter -------------------------------------------------------------

func TestSessionLimitRefusesPastTheCeilingAndNamesIt(t *testing.T) {
	limit := newSessionLimit(3)

	for i := 0; i < 3; i++ {
		if err := limit.take(); err != nil {
			t.Fatalf("session %d of 3 was refused: %v", i+1, err)
		}
	}
	if limit.live() != 3 {
		t.Fatalf("three sessions are open but the counter says %d", limit.live())
	}

	err := limit.take()
	if err == nil {
		t.Fatal("a fourth session was allowed past a limit of three")
	}
	// The number has to be in the sentence. A refusal without one reaches the
	// operator as a terminal that went black, and their first guess is the
	// cluster, not a ceiling they have never been told about.
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("the refusal does not name the limit: %q", err)
	}
	if limit.live() != 3 {
		t.Errorf("a refused session was still counted: %d open", limit.live())
	}

	// And it has to be recoverable, or the first burst of clicks disables the
	// terminal for the life of the process.
	limit.give()
	if err := limit.take(); err != nil {
		t.Errorf("a slot was freed and the next session was still refused: %v", err)
	}
}

// The default is the number written here, and it is not compared against the
// constant that produces it.
//
// Asserting `got != maxSessions` would check the constant against itself:
// raise maxSessions tenfold and it would still pass, so the one assertion
// standing between the dock and a hundred and sixty pseudo-terminals would
// raise itself along with the change it exists to stop. The pattern
// is internal/actions/bulk_test.go's
// TestABulkRunNeverHasMoreThanItsShareInFlight: assert the number, and add a
// lower bound so the test cannot pass over a degenerate implementation either.
func TestSessionLimitDefaultsToTheCeilingAndCannotBeDisabled(t *testing.T) {
	const ceiling = 16

	if maxSessions > ceiling || maxSessions < 2 {
		t.Fatalf("maxSessions is %d. Above %d is what the dock was capped for — a hidden tab "+
			"is a running tab, and a stuck loop of clicks ends in forty bash processes rather "+
			"than in a sentence — and below 2 refuses the second shell an operator opens",
			maxSessions, ceiling)
	}
	for _, asked := range []int{0, -1} {
		if got := newSessionLimit(asked).limit; got != ceiling {
			t.Errorf("newSessionLimit(%d) gave a limit of %d, want the default %d — "+
				"there is no way to ask for no limit on purpose", asked, got, ceiling)
		}
	}
	// Zero is not a hypothetical. terminal.go's New passes `opts.MaxSessions`
	// straight into newSessionLimit and nothing in internal/launch sets it, so zero
	// is the value production arrives with — which makes the two cases above the
	// whole path from this constant to a refused shell.
}

// give must not run the count below zero. It is called from a defer in both
// handlers and a stray extra call would hand out a free slot for ever after.
func TestSessionLimitDoesNotGoNegative(t *testing.T) {
	limit := newSessionLimit(1)
	limit.give()
	limit.give()
	if limit.live() != 0 {
		t.Fatalf("the counter went to %d", limit.live())
	}
	if err := limit.take(); err != nil {
		t.Fatalf("the first session was refused: %v", err)
	}
	if err := limit.take(); err == nil {
		t.Fatal("give() below zero handed out an extra slot: a second session passed a limit of one")
	}
}

func TestSessionLimitIsSafeUnderConcurrentOpens(t *testing.T) {
	limit := newSessionLimit(4)

	var wg sync.WaitGroup
	var mu sync.Mutex
	taken := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if limit.take() == nil {
				mu.Lock()
				taken++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if taken != 4 {
		t.Fatalf("%d of 50 concurrent opens were allowed past a limit of 4", taken)
	}
}

// --- the handler ---------------------------------------------------------------

// The exec endpoint has to consult the same counter the shell does, and it has
// to deliver the refusal *over the socket*.
//
// Refusing before the upgrade is the obvious way to write it and it does not
// work: a WebSocket handshake that fails hands the browser neither the status
// nor the body — `onerror` fires with an event carrying neither — so the dock
// would show its generic "the shell could not be started", which names a missing
// /bin/sh and a read-only cluster as the possibilities. The operator would go
// and check both. This test is what stops that rewrite.
func TestExecRefusesPastTheCeilingOverTheSocket(t *testing.T) {
	// The addresses in this kubeconfig are dead on purpose; the loopback rewrite
	// must not turn them into something reachable. See internal/cluster/loopback.go.
	t.Setenv("KT_IN_CONTAINER", "0")
	kubeconfig := execKubeconfig(t, "somewhere")

	term, err := New(managerFor(t, kubeconfig), Options{
		Kubeconfig:  cluster.Kubeconfig{kubeconfig},
		MaxSessions: 1,
		Access:      accesstest.Allow(),
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(term.ExecHandler())
	t.Cleanup(srv.Close)
	target := "ws" + strings.TrimPrefix(srv.URL, "http") +
		"/kt-api/exec?cluster=somewhere&namespace=default&pod=api"

	// Hold the only slot, standing in for a shell somebody else left open.
	if err := term.sessions.take(); err != nil {
		t.Fatal(err)
	}

	if got := dialAndRead(t, target); !strings.Contains(got, "which is the limit") {
		t.Errorf("the refusal did not arrive over the socket. Got:\n  %q\n"+
			"A handshake refused with a status code reaches the browser as nothing at all.", got)
	} else if !strings.Contains(got, strconv.Itoa(1)) {
		t.Errorf("the refusal does not name the limit: %q", got)
	}

	// Freed, and the next call must get past the ceiling — it then fails for its
	// own reasons against an address that does not answer, which is what proves
	// the check was the ceiling and not something else refusing everything.
	term.sessions.give()
	if got := dialAndRead(t, target); strings.Contains(got, "which is the limit") {
		t.Errorf("a session was refused with the ceiling free: %q", got)
	}

	// And the handler must give its slot back when it finishes. Without the
	// defer this leaks one slot per session and the console stops opening
	// terminals while the panel is empty.
	waitFor(t, func() bool { return term.sessions.live() == 0 }, "the handler never released its slot")
}

// dialAndRead opens the socket, reads what the server says, and closes. Returns
// everything read before the connection ended.
func dialAndRead(t *testing.T, target string) string {
	t.Helper()

	conn, _, err := websocket.DefaultDialer.Dial(target, http.Header{})
	if err != nil {
		t.Fatalf("dialling %s: %v", target, err)
	}
	defer conn.Close()

	var b strings.Builder
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		_, data, err := conn.ReadMessage()
		if len(data) > 0 {
			b.Write(data)
		}
		if err != nil {
			return b.String()
		}
		// One frame is enough for the refusal; keep reading only while the
		// server has more to say within the deadline.
		if b.Len() > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		}
	}
}

func waitFor(t *testing.T, ok func() bool, complaint string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(complaint)
}

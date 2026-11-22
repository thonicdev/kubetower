package logs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

// The ceiling exists, and it binds where the streams are rather than in the
// browser. Without it, sixty concurrent follows on a running container are all
// accepted; dock.js caps its own tabs, but a
// second window, a page left open overnight and a curl are all outside that.
func TestOnlySoManyFollowsAreHeldAtOnce(t *testing.T) {
	limit := newStreamLimit(3)

	for i := 0; i < 3; i++ {
		if err := limit.take(); err != nil {
			t.Fatalf("stream %d was refused below the limit: %v", i+1, err)
		}
	}
	err := limit.take()
	if err == nil {
		t.Fatal("a fourth follow was allowed past a limit of three")
	}
	// The number, because a panel that does not start reads as a broken cluster
	// — that is always the first guess — and the operator cannot see the other
	// three from where they are standing.
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("the refusal does not name the limit: %q", err)
	}
	if !strings.Contains(err.Error(), "close") {
		t.Errorf("the refusal does not say what to do about it: %q", err)
	}

	limit.give()
	if err := limit.take(); err != nil {
		t.Errorf("a slot given back was not reusable: %v", err)
	}
	if got := limit.open(); got != 3 {
		t.Errorf("open() = %d, want 3", got)
	}
}

// give never drops below zero, so a double-give cannot mint slots.
func TestGivingBackMoreThanWasTakenDoesNotMintSlots(t *testing.T) {
	limit := newStreamLimit(1)
	limit.give()
	limit.give()

	if err := limit.take(); err != nil {
		t.Fatalf("the first take was refused: %v", err)
	}
	if err := limit.take(); err == nil {
		t.Error("a second stream was allowed past a limit of one, so give() below zero has " +
			"created capacity that does not exist")
	}
}

// The refusal has to arrive *in* the stream.
//
// An EventSource is handed neither the status nor the body of a failed
// handshake, so a 429 would reach the panel as the same blank failure a broken
// cluster produces. The header is for everything that is not a browser.
func TestARefusedStreamSaysWhyInsideTheStream(t *testing.T) {
	s := &Streamer{logf: func(string, ...any) {}, live: newStreamLimit(1)}
	if err := s.live.take(); err != nil {
		t.Fatalf("seeding the limit: %v", err)
	}

	rec := httptest.NewRecorder()
	s.refuse(rec, s.live.take())

	if rec.Code != http.StatusOK {
		t.Errorf("status %d. A non-200 is invisible to an EventSource, so the panel would show "+
			"nothing at all and the operator would blame the cluster", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Errorf("Content-Type %q — the refusal is not framed as a stream, so the panel cannot "+
			"read it", got)
	}
	if got := rec.Header().Get("X-KubeTower-Refusal"); got != "log-stream-limit" {
		t.Errorf("X-KubeTower-Refusal = %q. Without it a curl or the assistant reads a 200 and "+
			"concludes the stream was fine", got)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "event: error\n") {
		t.Errorf("the body is not an error event, so the panel will sit on a spinner:\n%q", body)
	}
	if !strings.Contains(body, "limit") {
		t.Errorf("the body does not say what happened:\n%q", body)
	}
}

// End to end: the ceiling is enforced by the handler, not only by the counter.
//
// Without this the counter can be perfect and never consulted — a mutation that
// removed the `take()` from the handler passed every other test in this file.
func TestTheHandlerRefusesOnceTheCeilingIsReached(t *testing.T) {
	const ceiling = 3

	cluster := fakeAPI(t, 5*time.Millisecond, 0)
	clients := newFakeClients(t, cluster.URL, 0)

	s := New(clients, accesstest.Allow(), nil, nil)
	s.deadline = time.Minute
	s.live = newStreamLimit(ceiling)

	front := httptest.NewServer(s.Handler())
	defer front.Close()

	client := &http.Client{}
	var cancels []context.CancelFunc
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()

	open := func() *http.Response {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			front.URL+"/kt-api/logs?cluster=docker-desktop&namespace=n&pod=p&container=app&follow=1", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("the stream did not open: %v", err)
		}
		return resp
	}

	for i := 0; i < ceiling; i++ {
		resp := open()
		if got := resp.Header.Get("X-KubeTower-Refusal"); got != "" {
			t.Fatalf("stream %d was refused below the ceiling: %q", i+1, got)
		}
	}

	// The one over. It answers 200 — an EventSource cannot see anything else —
	// and says so in the header and in the first event.
	resp := open()
	if got := resp.Header.Get("X-KubeTower-Refusal"); got != "log-stream-limit" {
		t.Errorf("the %dth follow was accepted past a ceiling of %d. dock.js caps its own tabs, "+
			"but a second window and a curl are outside that cap — this is the one that binds",
			ceiling+1, ceiling)
	}
	body := make([]byte, 128)
	n, _ := resp.Body.Read(body)
	if !strings.Contains(string(body[:n]), "event: error") {
		t.Errorf("the refused stream opened and said nothing, which is the failure this package "+
			"already documents: %q", string(body[:n]))
	}
	_ = resp.Body.Close()

	// And closing one gives its slot back. Without this half a ceiling is just a
	// slow leak: the console would refuse the sixteenth panel of the day rather
	// than the sixteenth open at once, and the only cure would be a restart.
	cancels[0]()
	deadline := time.Now().Add(5 * time.Second)
	for {
		again := open()
		refused := again.Header.Get("X-KubeTower-Refusal")
		_ = again.Body.Close()
		if refused == "" {
			return
		}
		cancels = cancels[:len(cancels)-1]
		if time.Now().After(deadline) {
			t.Fatal("a closed stream never gave its slot back, so the ceiling counts streams " +
				"ever opened rather than streams open now — a restart would be the only cure")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The ceiling is the number it says it is, and the Streamer is built with it.
//
// Every other test in this file passes its own limit to newStreamLimit, so
// `maxStreams` itself is read by no other test and could be raised tenfold with
// the suite green — sixteen becoming a hundred and sixty, and follows left
// effectively unbounded with a test suite saying otherwise.
//
// Written as a number rather than as `maxStreams`, which would be a tautology,
// and with a lower bound so it cannot pass over a degenerate ceiling of one —
// see internal/actions/bulk_test.go's TestABulkRunNeverHasMoreThanItsShareInFlight.
func TestTheStreamCeilingIsSixteenAndTheStreamerUsesIt(t *testing.T) {
	const ceiling = 16

	if maxStreams > ceiling || maxStreams < 2 {
		t.Fatalf("maxStreams is %d. Above %d is a leak — sixty concurrent follows would be "+
			"accepted, each a goroutine and an open read "+
			"against an API server entitled to run for thirty minutes — and below 2 means the "+
			"second log panel an operator opens is refused", maxStreams, ceiling)
	}
	// The default has to be reached. A ceiling nothing consults is a comment.
	if got := newStreamLimit(0).limit; got != ceiling {
		t.Errorf("newStreamLimit(0) gave %d, want %d — zero is how every caller that has no "+
			"configured limit arrives here", got, ceiling)
	}
	if got := newStreamLimit(-1).limit; got != ceiling {
		t.Errorf("newStreamLimit(-1) gave %d, want %d — there is no way to ask for no limit "+
			"on purpose", got, ceiling)
	}
	// And the Streamer the routes are built from carries it, which is the half a
	// counter test cannot see.
	if got := New(nil, accesstest.Allow(), nil, nil).live.limit; got != ceiling {
		t.Errorf("a Streamer holds a ceiling of %d, want %d", got, ceiling)
	}
}

package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

// --- a stand-in kubelet ------------------------------------------------------

// fakeLog is an apiserver that answers the pod-log endpoint over a log it holds
// in memory, honoring the two options a page-backwards depends on: tailLines
// and previous. It records every query it was asked, because "the window it asks
// for" and "which run it asks about" are the two things this feature can be
// silently wrong about and neither is visible in a compiled binary.
type fakeLog struct {
	t       *testing.T
	server  *httptest.Server
	current []string
	prev    []string

	mu      sync.Mutex
	queries []url.Values
}

func newFakeLog(t *testing.T, current, prev []string) *fakeLog {
	t.Helper()
	f := &fakeLog{t: t, current: current, prev: prev}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/log") {
			http.Error(w, "not this test's endpoint: "+r.URL.Path, http.StatusNotFound)
			return
		}
		q := r.URL.Query()
		f.mu.Lock()
		f.queries = append(f.queries, q)
		f.mu.Unlock()

		lines := f.current
		if q.Get("previous") == "true" {
			lines = f.prev
		}
		// tailLines counts from the end, which is the only thing the real API
		// offers and the whole reason earlier.go is shaped as it is.
		if raw := q.Get("tailLines"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				http.Error(w, "bad tailLines "+raw, http.StatusBadRequest)
				return
			}
			if n < len(lines) {
				lines = lines[len(lines)-n:]
			}
		}
		for _, line := range lines {
			if _, err := fmt.Fprintln(w, line); err != nil {
				return
			}
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeLog) asked() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.queries...)
}

// tails is what tailLines every request carried, in order.
func (f *fakeLog) tails() []int64 {
	var out []int64
	for _, q := range f.asked() {
		n, _ := strconv.ParseInt(q.Get("tailLines"), 10, 64)
		out = append(out, n)
	}
	return out
}

func numbered(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s %d", prefix, i)
	}
	return out
}

// streamerOver wires a Streamer to a fake log, and hands back the client
// recorder so a test can say which of the two clients was asked for.
func streamerOver(t *testing.T, f *fakeLog) (*Streamer, *fakeClients) {
	t.Helper()
	clients := newFakeClients(t, f.server.URL, 5*time.Second)
	return New(clients, accesstest.Allow(), nil, nil), clients
}

func pagingRequest(before, page int64, extra string) string {
	u := "/kt-api/logs?cluster=docker-desktop&namespace=kubetower-demo&pod=crashing-api" +
		"&container=api&before=" + strconv.FormatInt(before, 10)
	if page > 0 {
		u += "&page=" + strconv.FormatInt(page, 10)
	}
	return u + extra
}

// decodeEarlier drives the handler, which is the point: this repository has had
// defects survive because the predicate was tested and its call site was not.
func decodeEarlier(t *testing.T, s *Streamer, target string) (Earlier, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))

	var answer Earlier
	if rec.Code == http.StatusOK {
		if err := json.NewDecoder(rec.Body).Decode(&answer); err != nil {
			t.Fatalf("decoding %q: %v\nbody: %s", target, err, rec.Body.String())
		}
	}
	return answer, rec
}

// --- the window --------------------------------------------------------------

// The mechanism, end to end through the handler. A container log is append-only,
// so the last `held + page` lines end with the last `held` the panel already has,
// and the front of that window is the block before them.
func TestAPageBackwardsAsksForAWiderTailAndKeepsItsFront(t *testing.T) {
	f := newFakeLog(t, numbered("line", 3000), nil)
	s, clients := streamerOver(t, f)

	answer, rec := decodeEarlier(t, s, pagingRequest(200, 500, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("content type %q; a page-backwards is a bounded answer a fetch can read, not a stream", got)
	}

	// 3000 lines, the last 200 are held, so the 500 before them are 2300…2799.
	if len(answer.Lines) != 500 {
		t.Fatalf("got %d lines, want 500", len(answer.Lines))
	}
	if answer.Lines[0] != "line 2300" || answer.Lines[499] != "line 2799" {
		t.Errorf("the block is %q…%q, want \"line 2300\"…\"line 2799\" — the window was sliced from the wrong end",
			answer.Lines[0], answer.Lines[499])
	}
	if answer.Stop != "" {
		t.Errorf("stop %q with 2 300 lines still unread above", answer.Stop)
	}
	if got := f.tails(); len(got) != 1 || got[0] != 700 {
		t.Errorf("asked for tailLines %v, want [700] — held plus a page", got)
	}
	// A fixed read of a block already written keeps the per-request timeout; the
	// streaming client exists for a body held open for half an hour.
	if got := clients.whatWasAsked(); len(got) != 1 || got[0] != "bounded" {
		t.Errorf("a page-backwards asked for %v, want [bounded]", got)
	}
}

// The clamp. Both halves of it, because a ceiling somebody quietly raises is the
// thing these tests exist for.
func TestTheWindowIsClampedToTheRewindCeiling(t *testing.T) {
	t.Run("a page asked for beyond the per-request maximum", func(t *testing.T) {
		f := newFakeLog(t, numbered("line", maxRewind+5000), nil)
		s, _ := streamerOver(t, f)

		if _, rec := decodeEarlier(t, s, pagingRequest(100, 99999, "")); rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		if got := f.tails(); len(got) != 1 || got[0] != 100+maxEarlierPage {
			t.Errorf("asked for tailLines %v, want [%d] — page was not clamped to maxEarlierPage",
				got, 100+maxEarlierPage)
		}
	})

	t.Run("the last page before the ceiling still delivers, and says it is the last", func(t *testing.T) {
		f := newFakeLog(t, numbered("line", maxRewind+5000), nil)
		s, _ := streamerOver(t, f)

		answer, _ := decodeEarlier(t, s, pagingRequest(maxRewind-100, 500, ""))
		if len(answer.Lines) != 100 {
			t.Errorf("got %d lines, want 100 — the page was not shrunk to the ceiling", len(answer.Lines))
		}
		if answer.Stop != stopCeiling {
			t.Errorf("stop %q, want %q", answer.Stop, stopCeiling)
		}
		if !strings.Contains(answer.Note, strconv.Itoa(maxRewind)) {
			t.Errorf("the ceiling sentence does not name the ceiling: %q", answer.Note)
		}
		if got := f.tails(); len(got) != 1 || got[0] != maxRewind {
			t.Errorf("asked for tailLines %v, want [%d]", got, maxRewind)
		}
	})

	t.Run("at the ceiling nothing is asked of the cluster at all", func(t *testing.T) {
		f := newFakeLog(t, numbered("line", maxRewind+5000), nil)
		s, _ := streamerOver(t, f)

		answer, rec := decodeEarlier(t, s, pagingRequest(maxRewind, 500, ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		if len(answer.Lines) != 0 {
			t.Errorf("got %d lines past the ceiling", len(answer.Lines))
		}
		if answer.Stop != stopCeiling || answer.Note == "" {
			t.Errorf("stop %q note %q — a refusal with no sentence is the failure this is about",
				answer.Stop, answer.Note)
		}
		if got := f.asked(); len(got) != 0 {
			t.Errorf("the cluster was asked %d times past the ceiling; a refusal must cost the API server nothing", len(got))
		}
	})

	t.Run("a lying client cannot enlarge the read", func(t *testing.T) {
		f := newFakeLog(t, numbered("line", maxRewind*10), nil)
		s, _ := streamerOver(t, f)

		// before is whatever the browser claims. It bounds the window and can
		// never widen it past maxRewind.
		decodeEarlier(t, s, pagingRequest(maxRewind-1, maxEarlierPage, ""))
		for _, got := range f.tails() {
			if got > maxRewind {
				t.Errorf("asked the kubelet for %d lines, past the %d ceiling", got, maxRewind)
			}
		}
	})
}

// --- the three ways it stops -------------------------------------------------

// The one that matters most: reaching the top of what exists must be a sentence,
// never an empty page and a spinner. A panel that stops loading with no message
// is indistinguishable from one that is broken.
func TestReachingTheOldestKeptLineIsSaidInWords(t *testing.T) {
	f := newFakeLog(t, numbered("line", 300), nil)
	s, _ := streamerOver(t, f)

	t.Run("a partial page, and the boundary", func(t *testing.T) {
		answer, _ := decodeEarlier(t, s, pagingRequest(200, 500, ""))
		// 300 lines exist, 200 are held: exactly 100 remain.
		if len(answer.Lines) != 100 {
			t.Fatalf("got %d lines, want 100", len(answer.Lines))
		}
		if answer.Lines[0] != "line 0" || answer.Lines[99] != "line 99" {
			t.Errorf("the last page is %q…%q, want \"line 0\"…\"line 99\"", answer.Lines[0], answer.Lines[99])
		}
		if answer.Stop != stopBeginning {
			t.Errorf("stop %q, want %q", answer.Stop, stopBeginning)
		}
		if !strings.Contains(answer.Note, "as far back as the kubelet has kept") {
			t.Errorf("the sentence is %q", answer.Note)
		}
		// It must not claim the process started here. The kubelet rotates
		// container logs, so the oldest line the API will return is not
		// necessarily the first line written.
		for _, forbidden := range []string{"start", "began", "beginning of the log", "first line"} {
			if strings.Contains(strings.ToLower(answer.Note), forbidden) {
				t.Errorf("the sentence claims the log starts here (%q): %q", forbidden, answer.Note)
			}
		}
	})

	t.Run("nothing at all left, and it still says why", func(t *testing.T) {
		answer, rec := decodeEarlier(t, s, pagingRequest(300, 500, ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		if len(answer.Lines) != 0 {
			t.Errorf("got %d lines when the caller already held every one", len(answer.Lines))
		}
		if answer.Stop != stopBeginning || answer.Note == "" {
			t.Fatalf("an empty page with stop %q and note %q — an empty answer and a finished one are different answers",
				answer.Stop, answer.Note)
		}
	})

	t.Run("rotated away underneath the caller", func(t *testing.T) {
		// The caller holds more lines than the kubelet still has, which is what
		// rotation between two reads looks like. Not an error: everything still
		// visible is already on screen.
		answer, rec := decodeEarlier(t, s, pagingRequest(900, 500, ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		if len(answer.Lines) != 0 {
			t.Errorf("got %d lines, want none", len(answer.Lines))
		}
		if answer.Stop != stopBeginning || answer.Note == "" {
			t.Errorf("stop %q note %q", answer.Stop, answer.Note)
		}
	})
}

// The third state. Lines long enough that a page of them will not fit in memory
// leave the block adjacent to what the caller holds undeliverable — and the
// oldest part of it, delivered on its own, would prepend lines that are adjacent
// to nothing. That is a false history, so it is refused in words instead.
func TestALogTooDenseToPageThroughIsRefusedRatherThanGuessed(t *testing.T) {
	// 20 KiB a line: the 8 MiB page ceiling bites at about 410 lines, short of
	// the 500 asked for.
	fat := strings.Repeat("x", 20<<10)
	lines := make([]string, 520)
	for i := range lines {
		lines[i] = fmt.Sprintf("%05d %s", i, fat)
	}
	f := newFakeLog(t, lines, nil)
	s, _ := streamerOver(t, f)

	answer, rec := decodeEarlier(t, s, pagingRequest(10, 500, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(answer.Lines) != 0 {
		t.Errorf("delivered %d lines that are not adjacent to what the caller holds", len(answer.Lines))
	}
	if answer.Stop != stopDense {
		t.Errorf("stop %q, want %q", answer.Stop, stopDense)
	}
	if answer.Note == "" {
		t.Error("no sentence; a page that returns nothing and says nothing reads as a broken panel")
	}
}

// --- the boundary that must not be crossed -----------------------------------

// A restarted container is a different log. Reaching the oldest line of this run
// must not reach into the run before it: those lines are not the earlier part of
// this log, and stitching them on invents a history with no visible seam.
func TestPagingNeverCrossesIntoThePreviousRun(t *testing.T) {
	f := newFakeLog(t, numbered("now", 300), numbered("then", 900))
	s, _ := streamerOver(t, f)

	t.Run("the current run stops at its own oldest line", func(t *testing.T) {
		answer, _ := decodeEarlier(t, s, pagingRequest(300, 500, ""))
		if answer.Stop != stopBeginning {
			t.Fatalf("stop %q, want %q", answer.Stop, stopBeginning)
		}
		for _, line := range answer.Lines {
			if strings.HasPrefix(line, "then") {
				t.Fatalf("a line from the previous run was stitched on: %q", line)
			}
		}
		for i, q := range f.asked() {
			if q.Get("previous") == "true" {
				t.Errorf("request %d asked for the previous run without being told to: %v", i, q)
			}
		}
	})

	t.Run("and reads it when it is asked to, explicitly", func(t *testing.T) {
		f2 := newFakeLog(t, numbered("now", 300), numbered("then", 900))
		s2, _ := streamerOver(t, f2)

		answer, _ := decodeEarlier(t, s2, pagingRequest(200, 500, "&previous=1"))
		if len(answer.Lines) != 500 {
			t.Fatalf("got %d lines from the previous run, want 500", len(answer.Lines))
		}
		for _, line := range answer.Lines {
			if !strings.HasPrefix(line, "then") {
				t.Fatalf("a line from the current run leaked into the previous run's page: %q", line)
			}
		}
		for i, q := range f2.asked() {
			if q.Get("previous") != "true" {
				t.Errorf("request %d dropped the previous flag: %v", i, q)
			}
		}
	})
}

// --- the default open --------------------------------------------------------

// Paging back is opt-in. A drawer that opens on a pod must cost exactly what it
// costs without it: the same tail, the same stream, the same client.
func TestTheDefaultOpenIsStillAStreamOfTheBoundedTail(t *testing.T) {
	f := newFakeLog(t, numbered("line", 3000), nil)
	s, clients := streamerOver(t, f)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/kt-api/logs?cluster=docker-desktop&namespace=kubetower-demo&pod=crashing-api&container=api", nil))

	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("content type %q, want text/event-stream — the opening read is still a stream", got)
	}
	if !strings.Contains(rec.Body.String(), "data: line 2800") {
		t.Errorf("the default tail is not the last %d lines; body starts:\n%s",
			defaultTail, textutilFirst(rec.Body.String()))
	}
	if got := f.tails(); len(got) != 1 || got[0] != defaultTail {
		t.Errorf("asked for tailLines %v, want [%d]", got, defaultTail)
	}
	if got := clients.whatWasAsked(); len(got) != 1 || got[0] != "streaming" {
		t.Errorf("the default open asked for %v, want [streaming]", got)
	}
	for _, q := range f.asked() {
		if q.Get("follow") != "true" {
			t.Errorf("the default open stopped following: %v", q)
		}
	}
}

func textutilFirst(s string) string {
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

// --- refusals ----------------------------------------------------------------

// The parameters are read from a browser, so every shape they can arrive in has
// to be answered. A 400 with a sentence, because a fetch can read both — which is
// the whole reason this half of the route is not server-sent events.
func TestAMalformedPagingRequestIsRefusedInWords(t *testing.T) {
	f := newFakeLog(t, numbered("line", 300), nil)
	s, _ := streamerOver(t, f)
	const base = "/kt-api/logs?cluster=docker-desktop&namespace=n&pod=p&container=c"

	for _, c := range []struct {
		name, query, names string
	}{
		{"zero", "&before=0", "before"},
		{"negative", "&before=-5", "before"},
		{"not a number", "&before=lots", "before"},
		{"a float", "&before=12.5", "before"},
		{"a zero page", "&before=200&page=0", "page"},
		{"a negative page", "&before=200&page=-1", "page"},
		{"a page that is not a number", "&before=200&page=some", "page"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, base+c.query, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400; body: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), c.names) {
				t.Errorf("the refusal does not name %q: %s", c.names, rec.Body.String())
			}
		})
	}

	// An absent `before` is not a malformed one: it is the ordinary stream, and
	// it must be indistinguishable from any other stream request.
	t.Run("an empty before falls through to the stream", func(t *testing.T) {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, base+"&before=", nil))
		if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
			t.Errorf("content type %q, want text/event-stream", got)
		}
	})

	t.Run("an unknown cluster is a status, not a stop", func(t *testing.T) {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/kt-api/logs?cluster=unknown&namespace=n&pod=p&container=c&before=200", nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status %d, want 400 for an unknown cluster; body: %s", rec.Code, rec.Body.String())
		}
	})
}

// A page-backwards is a bounded read and must not hold one of the sixteen follow
// slots: fifteen open panels would otherwise stop the operator scrolling up in
// the sixteenth.
func TestAPageBackwardsTakesNoFollowSlot(t *testing.T) {
	f := newFakeLog(t, numbered("line", 3000), nil)
	s, _ := streamerOver(t, f)

	// Fill the ceiling, so any slot taken by the page would be refused.
	for i := 0; i < maxStreams; i++ {
		if err := s.live.take(); err != nil {
			t.Fatalf("filling the limit at %d: %v", i, err)
		}
	}

	answer, rec := decodeEarlier(t, s, pagingRequest(200, 500, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-KubeTower-Refusal") != "" {
		t.Errorf("the page was refused by the follow limit: %s", rec.Body.String())
	}
	if len(answer.Lines) != 500 {
		t.Errorf("got %d lines, want 500", len(answer.Lines))
	}
	if s.live.open() != maxStreams {
		t.Errorf("%d slots held after a page-backwards, want %d", s.live.open(), maxStreams)
	}
}

// The options builder on its own, because every number in it is a ceiling
// somebody could quietly raise and a test that only mirrors the arithmetic
// proves nothing about the code that runs.
func TestTheOptionsForAPageBackwardsNeverFollowAndNeverFlipTheRun(t *testing.T) {
	for _, c := range []struct {
		name   string
		req    Request
		window int64
		want   int64
	}{
		{"an ordinary window", Request{Container: "api"}, 700, 700},
		{"past the ceiling", Request{Container: "api"}, maxRewind * 3, maxRewind},
		{"nonsense", Request{Container: "api"}, 0, 1},
		{"the previous run", Request{Container: "api", Previous: true}, 700, 700},
	} {
		opts := earlierOptions(c.req, c.window)
		if opts.Follow {
			t.Errorf("%s: a page-backwards followed; it is a read of a block already written", c.name)
		}
		if opts.TailLines == nil || *opts.TailLines != c.want {
			t.Errorf("%s: tailLines %v, want %d", c.name, opts.TailLines, c.want)
		}
		if opts.Previous != c.req.Previous {
			t.Errorf("%s: previous became %v from %v — the run must never be flipped here",
				c.name, opts.Previous, c.req.Previous)
		}
		if opts.Container != c.req.Container {
			t.Errorf("%s: container became %q", c.name, opts.Container)
		}
	}
}

// The panel's toolbar re-opens the stream when the timestamps toggle moves,
// because the apiserver either prefixes the instant or it does not. A page
// fetched afterwards has to carry the same option, or half the buffer is stamped
// and half is not and the row renderer dims the first word of the wrong ones.
func TestAPageBackwardsCarriesTheTimestampsOption(t *testing.T) {
	f := newFakeLog(t, numbered("line", 3000), nil)
	s, _ := streamerOver(t, f)

	decodeEarlier(t, s, pagingRequest(200, 500, "&timestamps=1"))
	for i, q := range f.asked() {
		if q.Get("timestamps") != "true" {
			t.Errorf("request %d dropped timestamps: %v", i, q)
		}
	}
}

// Earlier is exported and callable without a handler, so it gets the same
// treatment: the answer must not depend on which door it came through.
func TestEarlierAnsweredDirectlyMatchesTheHandler(t *testing.T) {
	f := newFakeLog(t, numbered("line", 3000), nil)
	s, _ := streamerOver(t, f)

	answer, err := s.Earlier(context.Background(), Request{
		Cluster: "docker-desktop", Namespace: "n", Pod: "p", Container: "api",
	}, 200, 500)
	if err != nil {
		t.Fatalf("Earlier: %v", err)
	}
	if len(answer.Lines) != 500 || answer.Lines[0] != "line 2300" {
		t.Fatalf("got %d lines starting %q", len(answer.Lines), answer.Lines[0])
	}
}

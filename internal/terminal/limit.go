package terminal

// How many terminals this process holds open at once.
//
// The dock shows only the tabs of the cluster on screen, and a hidden tab is a
// running tab, so shells can accumulate unseen. The dock's own limit is where
// the operator reads a refusal, but it cannot bind a second window or a stale
// page; this one is counted where the pseudo-terminals are.
//
// Shells and pod execs share the count: both are a WebSocket, a goroutine and a
// process or stream on the far side, and two ceilings of eight would be one of
// sixteen with a worse failure mode.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/thonicdev/kubetower/internal/identity"
)

// maxSessions is the default, sixteen like internal/forward's tunnels: far
// more than anyone juggles deliberately, and small enough that a stuck loop of
// clicks ends in a sentence rather than forty bash processes.
const maxSessions = 16

// PerPersonSessions is one person's share of maxSessions under the server
// profile, where the sixteen are shared by everyone; without it, one person's
// sixteen shells (or tabs a reload restored) would refuse everyone else's exec.
// Not applied to the shared password account, whose name is everybody holding
// the password: a share would cut the whole team to four, so its sessions count
// against the sixteen alone.
//
// Four leaves a quarter of the console free per person. internal/launch sets
// it under --incluster only; on the desktop there is one person.
const PerPersonSessions = 4

// sessionLimit counts what is open. Not a semaphore: a caller that cannot have
// a slot is told at once, in words, rather than queued behind another shell,
// which would look like a terminal that opened and hung.
type sessionLimit struct {
	mu    sync.Mutex
	n     int
	limit int

	// perPerson is one person's share, zero for none (the desktop). held counts
	// each person's slots by identity.Who.Key (method and name); a request carrying
	// nobody counts under the empty key.
	perPerson int
	held      map[string]int
}

func newSessionLimit(limit int) *sessionLimit {
	if limit <= 0 {
		limit = maxSessions
	}
	return &sessionLimit{limit: limit, held: map[string]int{}}
}

// shared sets one person's share. Called once, before anything is served. A
// share at or above the ceiling sets none.
func (l *sessionLimit) shared(perPerson int) *sessionLimit {
	if perPerson > 0 && perPerson < l.limit {
		l.perPerson = perPerson
	}
	return l
}

// personOf is who a slot is counted against (identity.Who.Key of the person
// the front door put on the request) and whether the per-person share applies:
// not to the shared password account.
func personOf(ctx context.Context) (string, bool) {
	who, _ := identity.From(ctx)
	return who.Key(), !who.Shared()
}

// take claims a slot, or explains why it cannot. The sentence names the
// number: in a pane that just went black, the operator's first guess is
// otherwise the cluster.
func (l *sessionLimit) take() error { return l.takeFor("", true) }

// takeFor claims a slot for one person: refused at the process's ceiling and,
// under the server profile, at that person's share, each with its own sentence,
// since "the console is full" and "you have your share open" call for different
// actions.
func (l *sessionLimit) takeFor(who string, share bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n >= l.limit {
		return fmt.Errorf(
			"%d terminals are already open, which is the limit; close one in the panel below and try again",
			l.limit)
	}
	if share && l.perPerson > 0 && l.held[who] >= l.perPerson {
		return fmt.Errorf(
			"you already have %d terminals open, which is one person's share of this shared console; "+
				"close one in the panel below and try again", l.perPerson)
	}
	l.n++
	if share && l.perPerson > 0 {
		l.held[who]++
	}
	return nil
}

// give releases a slot. Both handlers call it from a defer: a leaked slot is
// lost for the life of the process.
func (l *sessionLimit) give() { l.giveFor("", true) }

// giveFor releases a slot takeFor gave the same person.
func (l *sessionLimit) giveFor(who string, share bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n > 0 {
		l.n--
	}
	if share && l.perPerson > 0 && l.held[who] > 0 {
		l.held[who]--
		if l.held[who] == 0 {
			delete(l.held, who)
		}
	}
}

// --- what one person holds, for sign-out -------------------------------------

// hold takes a slot for the person behind ctx and records the stream as theirs,
// so signing them out can close it. The returned function gives both back; every
// handler defers it.
func (t *Terminal) hold(ctx context.Context, s *stream) (func(), error) {
	who, share := personOf(ctx)
	person, _ := identity.From(ctx)
	// The check, the slot, the registration and live.Add under one lock, so that
	// Close, which sets closing under the same lock, either sees this session or is
	// seen by it.
	t.openMu.Lock()
	if t.closing {
		t.openMu.Unlock()
		return nil, errShuttingDown
	}
	if err := t.sessions.takeFor(who, share); err != nil {
		t.openMu.Unlock()
		return nil, err
	}
	if t.open == nil {
		t.open = map[string]map[*stream]string{}
	}
	if t.open[who] == nil {
		t.open[who] = map[*stream]string{}
	}
	t.open[who][s] = person.Session
	t.live.Add(1)
	t.openMu.Unlock()
	return func() {
		defer t.live.Done()
		t.openMu.Lock()
		delete(t.open[who], s)
		if len(t.open[who]) == 0 {
			delete(t.open, who)
		}
		t.openMu.Unlock()
		t.sessions.giveFor(who, share)
	}, nil
}

// errShuttingDown is hold's answer once Close has run.
var errShuttingDown = errors.New("KubeTower is shutting down, so no new session is opened")

// farewellAll says goodbye to every stream at once and closes it, each within
// farewellWait, so the whole takes one farewellWait, not one per socket.
func farewellAll(streams []*stream, state, code, message string) {
	var wg sync.WaitGroup
	for _, s := range streams {
		wg.Add(1)
		go func(s *stream) {
			defer wg.Done()
			s.farewell(state, code, message)
		}(s)
	}
	wg.Wait()
}

// Close ends every session this Terminal holds and waits, up to closeWait, for
// each to clean up. Called once the HTTP server has stopped: Shutdown does not
// wait for hijacked connections, and each session's directory (a kubeconfig, an
// AWS cache) would otherwise stay in the work root.
//
// From here on hold refuses, so a handler still in its access review cannot
// open a session Close never sees.
func (t *Terminal) Close() {
	t.openMu.Lock()
	t.closing = true
	var streams []*stream
	for _, held := range t.open {
		for s := range held {
			streams = append(streams, s)
		}
	}
	t.openMu.Unlock()
	farewellAll(streams, stateUnreachable, codeNotSupportedHere, "KubeTower is shutting down, so this session was closed")
	done := make(chan struct{})
	go func() {
		t.live.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(closeWait):
		t.openMu.Lock()
		left := 0
		for _, held := range t.open {
			left += len(held)
		}
		t.openMu.Unlock()
		t.logf("terminal: %d of %d session(s) had not finished cleaning up %s after shutdown", left, len(streams), closeWait)
	}
}

// closeWait bounds how long Close waits for the sessions it ended: a PTY that
// will not die must not hold a quitting process up for ever.
const closeWait = 3 * time.Second

// EndSessionsOf closes every shell, pod exec, node shell and assistant session
// one sign-in of the person opened, and answers how many. Keyed by method and
// name (a single sign-on account named like the shared one is not it) and by
// who.Session (what the same person opened from another, still signed-in
// browser stays open). Called at sign-out, which otherwise only stops new
// requests.
//
// Each is told why before it closes, so the pane says the person signed out
// rather than that the network went; closing the stream ends the session
// whatever it is doing. An empty name ends nothing.
func (t *Terminal) EndSessionsOf(who identity.Who) int {
	if who.Name == "" {
		return 0
	}
	key := who.Key()
	t.openMu.Lock()
	streams := make([]*stream, 0, len(t.open[key]))
	for s, session := range t.open[key] {
		if session == who.Session {
			streams = append(streams, s)
		}
	}
	t.openMu.Unlock()
	farewellAll(streams, stateForbidden, codeVerbDenied, "you logged out of the console, so this session was closed")
	return len(streams)
}

// Open is how many sessions are open now: shells, pod execs, node shells and
// assistant sessions that hold registered and have not given back.
//
// For the macOS window's quit question ("2 terminals and 1 port-forward will
// close") and its App Nap guard (internal/launch/nativewindowfacts.go). It reads
// the registry Close and EndSessionsOf read, so it counts what quitting would
// end.
func (t *Terminal) Open() int {
	t.openMu.Lock()
	defer t.openMu.Unlock()
	n := 0
	for _, held := range t.open {
		n += len(held)
	}
	return n
}

// live is how many are open, for the tests.
func (l *sessionLimit) live() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.n
}

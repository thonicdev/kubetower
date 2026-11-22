package logs

// How many followed log streams this process holds open at once.
//
// Without a ceiling, sixty follows are sixty goroutines and sixty connections
// to an API server, each entitled to thirty minutes. The dock caps its tabs,
// but a cap in the browser does not bind another window, a stale tab or curl;
// this one is counted where the streams are.
//
// Counted apart from terminals: a log stream is not a pseudo-terminal and a
// process, and a shell refused because log panels are open would be a refusal
// nobody could act on.

import (
	"context"
	"fmt"
	"sync"

	"github.com/thonicdev/kubetower/internal/identity"
)

// maxStreams is the default ceiling, sixteen like terminals and tunnels: far
// more than anyone follows deliberately, and small enough that a page reloading
// in a loop ends in a sentence. Only followed streams count.
const maxStreams = 16

// PerPersonStreams is one person's share of maxStreams under the server
// profile, where the sixteen are shared by everyone, so one browser cannot
// refuse every other person's follow. internal/web sets it under --incluster
// only. The shared password account has no share, as for
// terminal.PerPersonSessions: its name is everybody who holds the password.
const PerPersonStreams = 4

// streamLimit counts what is open. Not a semaphore: a follow queued behind
// another would look like a stream that opened and went quiet, so a caller that
// cannot have a slot is told at once, in words.
type streamLimit struct {
	mu    sync.Mutex
	n     int
	limit int

	// perPerson is one person's share, zero for none (the desktop); held counts
	// each person's follows by the name identity.From gives, "" for nobody.
	// cancels ends each person's follows when they sign out.
	perPerson int
	held      map[string]int
	cancels   map[string]map[*context.CancelFunc]string
}

func newStreamLimit(limit int) *streamLimit {
	if limit <= 0 {
		limit = maxStreams
	}
	return &streamLimit{limit: limit, held: map[string]int{},
		cancels: map[string]map[*context.CancelFunc]string{}}
}

// take claims a slot, or explains why it cannot. The sentence names the
// number and where to close one, since a panel that fails to start otherwise
// reads as a broken cluster.
func (l *streamLimit) take() error { return l.takeFor("", true) }

// takeFor claims a slot for one person, refused at the process's ceiling and,
// under the server profile, at that person's share of it.
func (l *streamLimit) takeFor(who string, share bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n >= l.limit {
		return fmt.Errorf(
			"%d log streams are already open, which is the limit; close a log panel in the dock "+
				"below and try again", l.limit)
	}
	if share && l.perPerson > 0 && l.held[who] >= l.perPerson {
		return fmt.Errorf(
			"you already follow %d log streams, which is one person's share of this shared console; "+
				"close a log panel in the dock below and try again", l.perPerson)
	}
	l.n++
	if share && l.perPerson > 0 {
		l.held[who]++
	}
	return nil
}

// give returns a slot. Every take is paired with exactly one give, including
// where the stream never starts.
func (l *streamLimit) give() { l.giveFor("", true) }

// giveFor returns a slot takeFor gave the same person.
func (l *streamLimit) giveFor(who string, share bool) {
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

// hold takes a follow slot for the person behind ctx and derives the context
// the follow runs under, canceled when that person signs out (endFor). The
// function it returns gives both back.
func (l *streamLimit) hold(ctx context.Context) (context.Context, func(), error) {
	who, _ := identity.From(ctx)
	key, share := who.Key(), !who.Shared()
	if err := l.takeFor(key, share); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	handle := &cancel
	l.mu.Lock()
	if l.cancels[key] == nil {
		l.cancels[key] = map[*context.CancelFunc]string{}
	}
	l.cancels[key][handle] = who.Session
	l.mu.Unlock()
	return ctx, func() {
		cancel()
		l.mu.Lock()
		delete(l.cancels[key], handle)
		if len(l.cancels[key]) == 0 {
			delete(l.cancels, key)
		}
		l.mu.Unlock()
		l.giveFor(key, share)
	}, nil
}

// endFor cancels every follow one sign-in of the person holds (keyed by
// method and name, and by who.Session, as terminal.EndSessionsOf) and answers
// how many.
func (l *streamLimit) endFor(who identity.Who) int {
	if who.Name == "" {
		return 0
	}
	key := who.Key()
	l.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(l.cancels[key]))
	for handle, session := range l.cancels[key] {
		if session == who.Session {
			cancels = append(cancels, *handle)
		}
	}
	l.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	return len(cancels)
}

// open is how many are held, for tests and for the refusal's log line.
func (l *streamLimit) open() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.n
}

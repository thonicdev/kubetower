package auth

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// refusalLogEvery is how often one kind of refusal reaches the log: the first
// at once, then at most one line per interval, carrying the count of those
// left unsaid in between.
//
// The handoff and the launch route answer anything on loopback, and any web
// page can reach them (a no-cors POST, a navigation): the Host check stops a
// rebound page, not a cross-site one. One line per refusal would let any site
// the operator visits fill `kubetower.log`.
const refusalLogEvery = time.Minute

// refusalLogMaxKeys bounds how many kinds, or kinds from one client, are
// tracked at once (plus one overflow key per kind). A key quiet for
// refusalLogEvery is dropped first; past the bound, clients fold into one key
// per kind, so a flood from many addresses still says one line a minute.
const refusalLogMaxKeys = 1024

// refusalLog coalesces refusal lines by kind, and by client where the line is
// one an unauthenticated request can provoke.
type refusalLog struct {
	mu    sync.Mutex
	kinds map[string]*refusalKind
}

type refusalKind struct {
	last       time.Time
	suppressed int
}

// say logs the line for kind now, or counts it when one was logged less than
// refusalLogEvery ago. The line that ends a quiet interval says how many it
// stands for.
func (l *refusalLog) say(now time.Time, kind, format string, args ...any) {
	l.sayFor(now, kind, "", format, args...)
}

// sayFor is say for one client: each client's refusals of a kind are
// coalesced on their own, so one address cannot hide another's.
func (l *refusalLog) sayFor(now time.Time, kind, client, format string, args ...any) {
	key := kind
	if client != "" {
		key = kind + "\x00" + client
	}
	l.mu.Lock()
	if l.kinds == nil {
		l.kinds = map[string]*refusalKind{}
	}
	k := l.kinds[key]
	if k == nil {
		if len(l.kinds) >= refusalLogMaxKeys {
			l.pruneLocked(now)
		}
		if len(l.kinds) >= refusalLogMaxKeys && client != "" {
			key = kind + "\x00*"
			k = l.kinds[key]
		}
	}
	if k == nil {
		k = &refusalKind{}
		l.kinds[key] = k
	}
	if !k.last.IsZero() && now.Sub(k.last) < refusalLogEvery {
		k.suppressed++
		l.mu.Unlock()
		return
	}
	unsaid := k.suppressed
	k.last, k.suppressed = now, 0
	l.mu.Unlock()

	line := fmt.Sprintf(format, args...)
	if unsaid > 0 {
		line += fmt.Sprintf(" (and %d more like it since the last such line, not logged one by one)", unsaid)
	}
	log.Print(line)
}

// pruneLocked drops the keys that have been quiet for a whole interval: their
// next refusal is logged at once whether or not they are kept. What they had
// left unsaid is dropped with them.
func (l *refusalLog) pruneLocked(now time.Time) {
	for key, k := range l.kinds {
		if now.Sub(k.last) >= refusalLogEvery {
			delete(l.kinds, key)
		}
	}
}

// refused logs a refusal an unauthenticated request can provoke - a failed or
// throttled sign-in, a single sign-on callback turned away - at most once per
// client per refusalLogEvery, so a flood of them cannot fill the log. The line
// names the client.
func (a *Auth) refused(r *http.Request, kind, format string, args ...any) {
	key, _, _, _ := a.client(r)
	args = append(args, a.clientLine(r))
	a.refusals.sayFor(a.now(), kind, key, format+" (from %s)", args...)
}

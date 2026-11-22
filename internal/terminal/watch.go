package terminal

// What ends an open pod exec that should no longer run.
//
// A shell inside a pod is authenticated and reviewed once, at the upgrade.
// Without a later check, a sign-out on another replica, an expired session or a
// removed RoleBinding would leave it running, and pongs would keep an idle one
// alive for ever. The threat model's §8.2 asks for the check inside the stream,
// so the end is a frame the pane renders, not a socket torn down.

import (
	"context"
	"fmt"
	"time"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/identity"
)

// recheckEvery is how often an open exec asks again whether it may go on: a
// quarter of access.TTL. Cheap (the gate's held answers and one session-store
// read). A sign-out ends the shell within one interval; a removed RoleBinding
// within access.TTL plus one interval, the revocation delay every request has.
const recheckEvery = access.TTL / 4

// IdleExecBound is how long an open pod exec may receive nothing from the
// person before it is ended, under the server profile only (internal/launch
// sets Options.IdleExec there).
//
// Thirty minutes, the server profile's log-follow ceiling (internal/logs.DefaultFollowMax).
// Input is a keystroke, a paste or a resize; output does not count, or a
// forgotten `top` would hold a shared console's slot for ever, and nor do
// pongs, which the browser answers on its own. On the desktop there is one
// person and no bound.
const IdleExecBound = 30 * time.Minute

// watch asks, every recheckEvery until the stream closes, whether the exec may
// go on (the sign-in still stands, the review still allows attrs, it has not
// been idle past Options.IdleExec) and ends it in a framed refusal saying which
// when one says no.
//
// Only a no ends it: a store that did not answer or a review that could not be
// made is not a no, so the shell stays and the next interval asks again. A blip
// of the API server must not end every open shell.
func (t *Terminal) watch(ctx context.Context, s *stream, cluster string, attrs access.Attributes) {
	every := t.recheckEvery
	if every <= 0 {
		every = recheckEvery
	}
	now := t.now
	if now == nil {
		now = time.Now
	}
	stands := identity.StandingOf(ctx)
	idle := t.opts.IdleExec

	ticker := time.NewTicker(every)
	defer ticker.Stop()
	heard, heardAt := s.inputs(), now()
	for {
		select {
		case <-s.closed():
			return
		case <-ticker.C:
		}
		if stands != nil {
			ok, err := stands(ctx)
			if err == nil && !ok {
				t.logf("%s", identity.Line(ctx, "exec ended on %q %s/%s: the sign-in it was opened under has ended",
					cluster, attrs.Namespace, attrs.Name))
				s.farewell(stateForbidden, codeVerbDenied,
					"your sign-in to the console has ended, so this shell was closed; sign in again to open it")
				return
			}
		}
		if answer := t.opts.Access.Review(ctx, cluster, attrs); answer.Decision == access.Denied {
			refused := &access.Refused{Cluster: cluster, Attributes: attrs, Answer: answer}
			t.logf("%s", identity.Line(ctx, "exec ended on %q %s/%s: the access review now refuses it",
				cluster, attrs.Namespace, attrs.Name))
			s.farewell(stateForbidden, codeVerbDenied, "this shell was closed: "+refused.Error())
			return
		}
		if idle > 0 {
			if n := s.inputs(); n != heard {
				heard, heardAt = n, now()
			} else if now().Sub(heardAt) >= idle {
				t.logf("%s", identity.Line(ctx, "exec ended on %q %s/%s: no input for %s",
					cluster, attrs.Namespace, attrs.Name, idle))
				s.farewell(stateUnreadable, codeNotSupportedHere, fmt.Sprintf(
					"this shell received nothing for %s, which is the limit on this shared console, so it was closed; "+
						"open it again to carry on", idle))
				return
			}
		}
	}
}

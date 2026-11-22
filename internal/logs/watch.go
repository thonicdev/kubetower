package logs

// What ends an open log follow that should no longer run.
//
// A follow is authenticated and reviewed (`get pods/log`) once, when it opens,
// and would otherwise run to its deadline after a sign-out, an expired session
// or a removed RoleBinding. The check is made inside the stream, so the end is
// an event the pane renders rather than a dropped connection.

import (
	"context"
	"errors"
	"time"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/safego"
)

// recheckEvery is how often an open follow asks again whether it may go on: a
// quarter of access.TTL, as internal/terminal's exec does.
const recheckEvery = access.TTL / 4

// revoked is why a follow was ended from inside: the cause its context is
// canceled with, so that drive can say it rather than end in silence.
type revoked struct{ message string }

func (r *revoked) Error() string { return r.message }

// watch derives the context a follow runs under, canceled with a revoked
// cause once the sign-in has ended or the review now says no. The returned
// function stops the watching.
//
// Only a no ends it: a session store or review that could not answer leaves
// the follow running, and the next interval asks again.
func (s *Streamer) watch(ctx context.Context, req Request) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(ctx)
	every := s.recheckEvery
	if every <= 0 {
		every = recheckEvery
	}
	stands := identity.StandingOf(ctx)
	// Through safego, and a panic ends the follow as a no would: without this
	// goroutine nobody re-checks it.
	safego.Go("logs: re-checking a follow", func() error {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
			if stands != nil {
				if ok, err := stands(ctx); err == nil && !ok {
					s.logf("%s", identity.Line(ctx, "log stream for %s/%s ended: the sign-in it was opened under has ended",
						req.Namespace, req.Pod))
					cancel(&revoked{message: "your sign-in to the console has ended, so this log stream was closed; " +
						"sign in again to follow it"})
					return nil
				}
			}
			name, ok := s.clusters.Resolve(req.Cluster)
			if !ok {
				continue
			}
			question := logQuestion(Request{Namespace: req.Namespace, Pod: req.Pod})
			if answer := s.gate.Review(ctx, name, question); answer.Decision == access.Denied {
				refused := &access.Refused{Cluster: name, Attributes: question, Answer: answer}
				s.logf("%s", identity.Line(ctx, "log stream for %s/%s ended: the access review now refuses it",
					req.Namespace, req.Pod))
				cancel(&revoked{message: "this log stream was closed: " + refused.Error()})
				return nil
			}
		}
	}, func(err error) {
		if err != nil {
			cancel(&revoked{message: "this log stream was closed: the console's re-check of it stopped on an internal error"})
		}
	})
	return ctx, func() { cancel(nil) }
}

// whyCut is the event a follow cut short from inside ends on, or nil for one
// that ended because its reader went: nobody is left to tell.
func whyCut(ctx context.Context) *Event {
	var r *revoked
	if errors.As(context.Cause(ctx), &r) {
		return &Event{Kind: EventRefused, Text: r.message}
	}
	return nil
}

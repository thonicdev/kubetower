package identity

import "context"

// Standing answers whether the sign-in a request was made under still stands:
// true while it does, false once it has ended - signed out, on this replica or
// another, past its cap, or derived under a mapping this process no longer
// uses. An error is that it could not be established, the session store not
// uses. An error means it could not be established (the session store not
// answering) and is not a no: a caller keeps what it holds and asks again.
//
// It exists for what outlives its request: an open pod exec or log follow is
// authenticated once, at the upgrade, and the front door never sees it again,
// so the long-lived handler asks this while it runs.
type Standing func(context.Context) (bool, error)

type standingKey struct{}

// WithStanding returns ctx carrying how to ask whether its sign-in stands.
func WithStanding(ctx context.Context, stands Standing) context.Context {
	return context.WithValue(ctx, standingKey{}, stands)
}

// StandingOf is how to ask whether ctx's sign-in still stands, or nil where
// nothing put one there: a request with no session, or a test. Nil is never a
// no; there is simply nothing to re-check.
func StandingOf(ctx context.Context) Standing {
	stands, _ := ctx.Value(standingKey{}).(Standing)
	return stands
}

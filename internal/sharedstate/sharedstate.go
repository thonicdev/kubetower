// Package sharedstate is what every replica of the console has to agree on, and
// nothing else.
//
// With two replicas behind one Service, a request lands on either, and state
// held by one pod stops working: a session signed in on pod A is unknown to pod
// B, and a confirmation plan previewed on A is confirmed on B. A third record
// is a hash of a sign-in's `state` once it has opened a session, so a second
// callback is refused on any replica; it is written only after the provider's
// token is verified, so the public start route writes nothing here.
//
// A sign-in in progress is not here: it is a sealed cookie
// (internal/auth/oidc.go), because a public route writing into a shared store
// lets anybody flood it and evict the sessions and plans.
//
// The interface is deliberately small: a key-value store with an expiry, an
// atomic set-if-absent and an atomic read-and-delete. Something that does not
// fit that shape is a different decision.
//
// Memory, the default, is a map in this process. Valkey, selected by
// KT_VALKEY_ADDR, is what several replicas share. Nothing branches on which one
// is held, and a test runs the same assertions over both.
//
// A store never holds a token: a session record is a name, groups and an
// instant, and a plan's key is taken, never read.
package sharedstate

import (
	"context"
	"errors"
	"time"
)

// Store is a key-value store with an expiry on every key.
//
// "Not there" and "could not ask" are different answers: ok false with a nil
// error means the key does not exist (never written, ended or expired); a
// non-nil error means the store did not answer. Folding the second into the
// first signs everybody out whenever the store blinks.
type Store interface {
	// Put writes value under key until expires, replacing what was there. An
	// expiry already past deletes the key instead.
	Put(ctx context.Context, key string, value []byte, expires time.Time) error

	// PutNew writes value under key until expires only if the key is absent,
	// and reports whether it wrote. Atomic, so of two replicas writing the same
	// key exactly one gets true: this makes a sign-in's `state` single use. An
	// expiry already past writes nothing and reports false.
	PutNew(ctx context.Context, key string, value []byte, expires time.Time) (created bool, err error)

	// Get reads a value.
	Get(ctx context.Context, key string) (value []byte, ok bool, err error)

	// Take reads a value and deletes it in one step, so two callers presenting
	// the same key get it once between them: a confirmation plan's token is
	// single use across replicas.
	Take(ctx context.Context, key string) (value []byte, ok bool, err error)

	// Extend moves the expiry of a key that exists, and reports false for one
	// that does not. It never creates a key, so a session just ended elsewhere
	// is not brought back.
	Extend(ctx context.Context, key string, expires time.Time) (ok bool, err error)

	// Delete forgets a key. A key that is not there is not an error, so a
	// sign-out from a session that has already ended still succeeds.
	Delete(ctx context.Context, key string) error

	// Describe says what this store is, for the start-up log: a kind and an
	// address, never a password.
	Describe() string
}

// ErrUnreachable wraps every failure of a store to answer. The front door
// answers it with a 503 naming the store rather than a sign-in page, since
// signing in needs the same store.
var ErrUnreachable = errors.New("the session store did not answer")

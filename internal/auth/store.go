package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/thonicdev/kubetower/internal/sharedstate"
)

// The session store: what makes one person's sign-out that person's alone.
//
// Nothing server-side can end a stateless cookie (docs/SERVER-MODE-THREAT-
// MODEL.md §8.1): rotating the key signs everybody out, lowering the cap ends
// sessions by age. With several people signed in through an identity
// provider that is the missing half of offboarding. So a single sign-on
// session is a record here, its id inside the same signed payload the
// password session uses; the cookie's machinery stays whole, because a store
// makes revocation possible and does not make an unsigned cookie safe (§8.2).
//
// It holds no token: after verification the ID and access tokens are thrown
// away and no refresh token is asked for. A token here would be a credential
// in memory for a session's life, and authorization is the cluster's, asked
// with a name and groups.
//
// It lives in a sharedstate.Store: in memory for one replica (a restart signs
// every SSO session out; signing in again is one click), Valkey for several
// (a sign-out through any replica ends it for all).
//
// A record expires when its cookie does (the idle window, never past the
// cap), so the store has no lifetime of its own.
//
// "Not there" and "could not ask" stay apart: an absent record is an ended
// session, an unanswering store is a 503 naming it, because "sign in again"
// would need the same store.
type sessionStore struct {
	kv sharedstate.Store
}

// sessionPrefix is where session records live in the store. A sign-in in
// progress is not here: it is a sealed cookie (oidc.go, flow).
const sessionPrefix = "session:"

// storedSession is one person's session, as of their sign-in.
//
// It is the whole record and has no token field;
// TestAStoredSessionCarriesNoToken asserts none of a verified token's bytes
// survive in it.
type storedSession struct {
	// Name is the person, from the configured username claim.
	Name string `json:"name"`
	// Groups are the provider's groups, prefixed, as the console asserts
	// them. Stale until the next sign-in: a group removed at the provider stays
	// until the session ends, a revocation delay bounded by the single sign-on
	// cap (docs/SERVER-MODE-THREAT-MODEL.md §8.3).
	Groups []string `json:"groups"`
	// Issued is the sign-in, which the cap counts from.
	Issued time.Time `json:"issued"`
	// Mapping fingerprints the configuration Name and Groups were derived
	// under (issuer, the two claims, the two prefixes; provider.mapping). A
	// record whose mapping is not the running one is no session (live): the
	// store outlives a rollout, and a changed prefix or claim must not keep a
	// person reviewed under the old name until the cap. Empty is a mismatch.
	Mapping string `json:"mapping"`
}

// sessionIDBytes is the id's entropy: 128 bits. The id only travels inside a
// signed payload, so guessing one would also need the signing key.
const sessionIDBytes = 16

func newSessionStore(kv sharedstate.Store) *sessionStore {
	return &sessionStore{kv: kv}
}

// once records key (stateKey's, a hashed `sso-state:` key) until expires, and
// reports whether this call recorded it: set-if-absent, so of two callers
// presenting the same key, on two replicas at once, exactly one gets true.
// It makes a sign-in's `state` single use on this console's own authority.
//
// Only a verified sign-in reaches it, so a flood of the public start route
// writes nothing here.
func (s *sessionStore) once(ctx context.Context, key string, expires time.Time) (bool, error) {
	return s.kv.PutNew(ctx, key, []byte{1}, expires)
}

// open records a session until expires and returns its id.
func (s *sessionStore) open(ctx context.Context, rec storedSession, expires time.Time) (string, error) {
	raw := make([]byte, sessionIDBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw)
	body, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	if err := s.kv.Put(ctx, sessionPrefix+id, body, expires); err != nil {
		return "", err
	}
	return id, nil
}

// get reads a record. A missing one (ended, expired, or lost with an
// in-memory store's restart) means not signed in. An error is the store not
// answering, a different answer.
func (s *sessionStore) get(ctx context.Context, id string) (storedSession, bool, error) {
	body, ok, err := s.kv.Get(ctx, sessionPrefix+id)
	if err != nil || !ok {
		return storedSession{}, false, err
	}
	var rec storedSession
	if err := json.Unmarshal(body, &rec); err != nil || rec.Name == "" {
		// Unreadable (another build's shape): not a person, so not a
		// session.
		return storedSession{}, false, nil
	}
	return rec, true, nil
}

// extend moves a record's expiry along with its cookie's, and reports false
// for a record already gone (ended on another replica).
func (s *sessionStore) extend(ctx context.Context, id string, expires time.Time) (bool, error) {
	return s.kv.Extend(ctx, sessionPrefix+id, expires)
}

// end forgets a record. Idempotent: a logout from an ended session must
// still answer 204.
func (s *sessionStore) end(ctx context.Context, id string) error {
	return s.kv.Delete(ctx, sessionPrefix+id)
}

// isSessionID reports whether a string has the shape open writes. Checked
// after the signature, so not a security boundary: it keeps a payload field
// of another shape from reading as a session that does not exist.
func isSessionID(id string) bool {
	if len(id) != 2*sessionIDBytes {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

package sharedstate

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"
)

// Sealed encrypts and authenticates every value on its way into a store, with a
// key only the console holds.
//
// A session record is an assertion the console makes to the API server ("alice,
// in these groups, is asking"). Whoever can write to the store (its password,
// a shell in its pod) could otherwise rewrite their own record's groups to
// system:masters, and their legitimately signed cookie, which covers only the
// record's id, would carry the forged groups to every review.
//
// So the record is sealed with AES-256-GCM under a key derived from the session
// signing key, which every replica shares and the store never sees. A rewritten
// or moved record fails to open and reads as no session; the content is
// unreadable from the store alone.
//
// The additional data is the key and the store's namespace (Namespaced), so two
// consoles sharing one Valkey and one secret under two prefixes cannot move a
// record between them.
//
// Not protected: deletion (which signs somebody out, the safe direction) and
// replay of a sealed value copied earlier under the same key, which resurrects
// a signed-out session. Replay needs write access and an earlier copy, and is
// still bounded by the session cap its cookie carries and a plan's own sealed
// lifetime.
type Sealed struct {
	inner Store
	aead  cipher.AEAD
	// namespace is the inner store's Namespace, bound into every seal.
	namespace string
}

// Namespaced is a store whose keys live under a namespace of its own (Valkey's
// key prefix). Sealed binds it into every value, so a value copied to another
// namespace does not open there.
type Namespaced interface {
	Namespace() string
}

// sealInfo separates this key from anything else derived from the session key.
// Versioned, so a change of scheme is a new label.
const sealInfo = "kubetower sharedstate v1"

// sealVersion is the first byte of every sealed value.
const sealVersion byte = 1

// Seal wraps a store. sessionKey is the cookie signing key; at least 32 bytes.
func Seal(inner Store, sessionKey []byte) (*Sealed, error) {
	if len(sessionKey) < 32 {
		return nil, fmt.Errorf("sharedstate: a sealing key needs at least 32 bytes of session key, got %d", len(sessionKey))
	}
	key, err := hkdf.Key(sha256.New, sessionKey, nil, sealInfo, 32)
	if err != nil {
		return nil, fmt.Errorf("sharedstate: deriving the sealing key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	namespace := ""
	if n, ok := inner.(Namespaced); ok {
		namespace = n.Namespace()
	}
	return &Sealed{inner: inner, aead: aead, namespace: namespace}, nil
}

// errUnsealable is a value that did not open: rewritten, moved, or sealed under
// another key. Callers see it as "not there", never as the store failing.
var errUnsealable = errors.New("a stored value did not open")

// seal binds a value to its key: the key is the additional data, so a value
// copied under another key does not open there.
func (s *Sealed) seal(key string, value []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte{sealVersion}, nonce...)
	return s.aead.Seal(out, nonce, value, s.additional(key)), nil
}

func (s *Sealed) open(key string, sealed []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < 1+n || sealed[0] != sealVersion {
		return nil, errUnsealable
	}
	plain, err := s.aead.Open(nil, sealed[1:1+n], sealed[1+n:], s.additional(key))
	if err != nil {
		return nil, errUnsealable
	}
	return plain, nil
}

func (s *Sealed) Put(ctx context.Context, key string, value []byte, expires time.Time) error {
	sealed, err := s.seal(key, value)
	if err != nil {
		return err
	}
	return s.inner.Put(ctx, key, sealed, expires)
}

// PutNew seals the value and writes it only where the key is absent. The test
// is on the key, not the seal: a value there that does not open still counts as
// taken, the safe answer for a single-use record.
func (s *Sealed) PutNew(ctx context.Context, key string, value []byte, expires time.Time) (bool, error) {
	sealed, err := s.seal(key, value)
	if err != nil {
		return false, err
	}
	return s.inner.PutNew(ctx, key, sealed, expires)
}

// Get and Take answer "not there" for a value that does not open, so a tampered
// record leads to the sign-in page rather than a 503.
func (s *Sealed) Get(ctx context.Context, key string) ([]byte, bool, error) {
	return s.opened(key)(s.inner.Get(ctx, key))
}

func (s *Sealed) Take(ctx context.Context, key string) ([]byte, bool, error) {
	return s.opened(key)(s.inner.Take(ctx, key))
}

func (s *Sealed) opened(key string) func([]byte, bool, error) ([]byte, bool, error) {
	return func(sealed []byte, ok bool, err error) ([]byte, bool, error) {
		if err != nil || !ok {
			return nil, ok, err
		}
		plain, err := s.open(key, sealed)
		if err != nil {
			return nil, false, nil
		}
		return plain, true, nil
	}
}

// Extend moves an expiry without reading the value or checking the seal: a
// rewritten record kept alive is still refused by every read.
func (s *Sealed) Extend(ctx context.Context, key string, expires time.Time) (bool, error) {
	return s.inner.Extend(ctx, key, expires)
}

func (s *Sealed) Delete(ctx context.Context, key string) error { return s.inner.Delete(ctx, key) }

func (s *Sealed) Describe() string { return s.inner.Describe() + ", values sealed" }

// additional is what a value is bound to: the namespace, a separator no key
// holds, and the key.
func (s *Sealed) additional(key string) []byte {
	return []byte(s.namespace + "\x00" + key)
}

// Package valkeystore is the sharedstate.Store several replicas share.
//
// A package of its own so that only the entry point that builds it links the
// client; internal/api and internal/auth use sharedstate.Store.
package valkeystore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/thonicdev/kubetower/internal/sharedstate"
)

// Valkey is the store several replicas share: a sharedstate.Store.
//
// Valkey: the BSD-licensed continuation of Redis's last open release, with an
// official image published without a registry account. Every command used is
// in Valkey 7.2+ and Redis 6.2+. The client is the project's own valkey-go
// (Apache-2.0); go-redis stays out of the dependency graph.
//
//   - `SET key value PX ms` writes a record with its lifetime in one command,
//     so a session never exists without an expiry.
//   - `SET ... NX PX` is PutNew: of two pods recording one sign-in `state`, one
//     succeeds.
//   - `GET` reads one.
//   - `GETDEL` is Take: one command on a single-threaded server, so a plan's
//     token presented to two pods at once is honored once. GET then DEL would
//     honor it twice.
//   - `PEXPIRE` is Extend: it creates nothing, so a sliding refresh cannot
//     resurrect a session another pod signed out.
//   - `DEL` is Delete.
//
// Every lifetime is relative: an instant (`PXAT`) is compared with Valkey's own
// clock, which may be off from the pod's. Each command carries `expires` minus
// the pod's now. The absolute instant is still checked on the pod where it
// decides anything (a plan's own expiry, a session's in the signed cookie); the
// store's timer only decides when it forgets.
//
// No TLS, Sentinel or cluster mode: the chart deploys one Valkey beside the
// console. No client-side caching: a cached session is a sign-out another pod
// made that this one has not heard of.
type Valkey struct {
	client  valkey.Client
	address string
	prefix  string
	// now is the pod's clock, which every lifetime is computed against.
	now func() time.Time
}

// Config is how to reach a Valkey.
type Config struct {
	// Address is host:port.
	Address string
	// Username is the ACL user, empty for the default one.
	Username string
	// Password authenticates. **Never logged, never serialized.**
	Password string `json:"-"`
	// Prefix is put in front of every key, so a Valkey shared with something
	// else keeps this console's keys apart. Empty means "kubetower:".
	Prefix string
	// Now is this process's clock. Nil means time.Now; a test sets one that
	// disagrees with the server's, which is the case lifetimes exist for.
	Now func() time.Time
}

// ErrCredentials is Valkey answering and refusing who this console said it
// was: a wrong KT_VALKEY_PASSWORD, or a KT_VALKEY_USERNAME with no such user or
// no permission.
//
// It is its own error because waiting will not fix it: reported as "did not
// answer yet", it would send an operator to a Valkey pod that is fine.
var ErrCredentials = errors.New("the session store refused this console's credentials")

// isCredentials is whether Valkey refused the connection's identity, read from
// the protocol's error code in the reply.
func isCredentials(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, code := range []string{"WRONGPASS", "NOAUTH", "NOPERM"} {
		if strings.Contains(msg, code) {
			return true
		}
	}
	return false
}

// opTimeout bounds each command: the request is a person waiting on a page.
const opTimeout = 3 * time.Second

// New builds the client. An unreachable Valkey is not fatal: the pods and the
// Valkey StatefulSet start together, and refusing to start would crash-loop
// every install. The client is returned either way and reconnects on first
// use; the error is for the start-up log.
func New(cfg Config) (*Valkey, error) {
	if _, _, err := net.SplitHostPort(cfg.Address); err != nil {
		return nil, fmt.Errorf("valkeystore: the Valkey address %q is not host:port: %w", cfg.Address, err)
	}
	prefix := cfg.Prefix
	if prefix == "" {
		prefix = "kubetower:"
	}
	client, err := valkey.NewClient(valkey.ClientOption{
		InitAddress: []string{cfg.Address},
		Username:    cfg.Username,
		Password:    cfg.Password,
		ClientName:  "kubetower",
		// One server, never a cluster: skips `CLUSTER SLOTS`, and makes
		// NewClient return a usable client when the first dial fails.
		ForceSingleClient: true,
		// See the type's comment: a cached session is a stale one.
		DisableCache: true,
		Dialer:       net.Dialer{Timeout: opTimeout},
	})
	if client == nil {
		return nil, fmt.Errorf("valkeystore: the Valkey client could not be built: %w", err)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	v := &Valkey{client: client, address: cfg.Address, prefix: prefix, now: now}
	if isCredentials(err) {
		return v, fmt.Errorf("%w: %s answered and refused them (%v). Check KT_VALKEY_PASSWORD, and "+
			"KT_VALKEY_USERNAME if one is set", ErrCredentials, cfg.Address, err)
	}
	if err != nil {
		return v, fmt.Errorf("%w: %s did not answer yet (%v); the console retries on every request that needs it",
			sharedstate.ErrUnreachable, cfg.Address, err)
	}
	return v, nil
}

// Ping asks the server whether it answers, for the start-up log.
func (v *Valkey) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	return v.fail(v.client.Do(ctx, v.client.B().Ping().Build()).Error())
}

// Close ends the connections.
func (v *Valkey) Close() { v.client.Close() }

func (v *Valkey) Put(ctx context.Context, key string, value []byte, expires time.Time) error {
	ttl, alive := v.lifetime(expires)
	if !alive {
		return v.Delete(ctx, key)
	}
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	cmd := v.client.B().Set().Key(v.prefix + key).Value(valkey.BinaryString(value)).PxMilliseconds(ttl).Build()
	return v.fail(v.client.Do(ctx, cmd).Error())
}

// lifetime is how long, on the pod's clock, a record expiring at `expires` has
// left, in whole milliseconds and at least one, or false when none. A
// non-positive PX is an error to Valkey, and a non-positive PEXPIRE deletes the
// key, which Extend must never do by accident.
func (v *Valkey) lifetime(expires time.Time) (int64, bool) {
	left := expires.Sub(v.now())
	if left <= 0 {
		return 0, false
	}
	ms := left.Milliseconds()
	if ms < 1 {
		ms = 1
	}
	return ms, true
}

// PutNew is `SET ... NX PX`: Valkey answers OK when it wrote and a nil reply
// when the key was already there.
func (v *Valkey) PutNew(ctx context.Context, key string, value []byte, expires time.Time) (bool, error) {
	ttl, alive := v.lifetime(expires)
	if !alive {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	cmd := v.client.B().Set().Key(v.prefix + key).Value(valkey.BinaryString(value)).Nx().PxMilliseconds(ttl).Build()
	if err := v.client.Do(ctx, cmd).Error(); err != nil {
		if valkey.IsValkeyNil(err) {
			return false, nil
		}
		return false, v.fail(err)
	}
	return true, nil
}

func (v *Valkey) Get(ctx context.Context, key string) ([]byte, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	return v.bytes(v.client.Do(ctx, v.client.B().Get().Key(v.prefix+key).Build()))
}

func (v *Valkey) Take(ctx context.Context, key string) ([]byte, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	return v.bytes(v.client.Do(ctx, v.client.B().Getdel().Key(v.prefix+key).Build()))
}

func (v *Valkey) Extend(ctx context.Context, key string, expires time.Time) (bool, error) {
	ttl, alive := v.lifetime(expires)
	if !alive {
		// Extending to an instant already past is ending it; say it was not
		// extended, as a key that is gone would.
		return false, v.Delete(ctx, key)
	}
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	cmd := v.client.B().Pexpire().Key(v.prefix + key).Milliseconds(ttl).Build()
	n, err := v.client.Do(ctx, cmd).AsInt64()
	if err != nil {
		return false, v.fail(err)
	}
	return n == 1, nil
}

func (v *Valkey) Delete(ctx context.Context, key string) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	return v.fail(v.client.Do(ctx, v.client.B().Del().Key(v.prefix+key).Build()).Error())
}

// Namespace is the prefix every key is written under, which sharedstate.Sealed
// binds into each value.
func (v *Valkey) Namespace() string { return v.prefix }

// Describe names the store for the start-up log: the kind and the address,
// and never the password.
func (v *Valkey) Describe() string {
	return "Valkey at " + v.address + ", shared by every replica"
}

// bytes reads a bulk string, with a nil reply as "not there".
func (v *Valkey) bytes(r valkey.ValkeyResult) ([]byte, bool, error) {
	b, err := r.AsBytes()
	if err != nil {
		if valkey.IsValkeyNil(err) {
			return nil, false, nil
		}
		return nil, false, v.fail(err)
	}
	return b, true, nil
}

// fail wraps an error as the store not answering. A nil reply is never passed
// here; `bytes` has already turned it into "not there".
func (v *Valkey) fail(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sharedstate.ErrUnreachable) {
		return err
	}
	if isCredentials(err) {
		// Still ErrUnreachable to the caller (a 503 either way), but the log
		// says which.
		return fmt.Errorf("%w (%w): %s: %v", sharedstate.ErrUnreachable, ErrCredentials, v.address, err)
	}
	return fmt.Errorf("%w: %s: %v", sharedstate.ErrUnreachable, v.address, err)
}

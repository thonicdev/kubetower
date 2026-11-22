package sharedstate_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/sharedstate"
	"github.com/thonicdev/kubetower/internal/sharedstate/valkeystore"
)

// The contract every Store keeps, run over each implementation.
//
// Memory runs everywhere. **Valkey runs where `KT_TEST_VALKEY_ADDR` names one**:
// CI's Go job starts one for it with `docker run --network host`, in a step of
// its own, and a developer's machine may have none. Without the address the
// test skips and says so - and **fails instead under CI** (`CI` set), because
// there a skip is that step's Valkey gone missing, and a green tick measuring
// nothing.

var ctx = context.Background()

func contract(t *testing.T, store sharedstate.Store, prefix string) {
	t.Helper()
	later := time.Now().Add(time.Hour)
	key := func(name string) string { return prefix + name }

	t.Run("a key that was never written is not there, and that is not an error", func(t *testing.T) {
		if _, ok, err := store.Get(ctx, key("never")); ok || err != nil {
			t.Errorf("Get of a key never written answered ok=%v err=%v", ok, err)
		}
	})

	t.Run("what is put is what is got", func(t *testing.T) {
		value := []byte("a\x00binary\xffvalue")
		if err := store.Put(ctx, key("one"), value, later); err != nil {
			t.Fatal(err)
		}
		got, ok, err := store.Get(ctx, key("one"))
		if err != nil || !ok || !bytes.Equal(got, value) {
			t.Errorf("Get answered %q ok=%v err=%v, want %q", got, ok, err, value)
		}
	})

	t.Run("take answers once", func(t *testing.T) {
		_ = store.Put(ctx, key("flow"), []byte("verifier"), later)
		if _, ok, err := store.Take(ctx, key("flow")); !ok || err != nil {
			t.Fatalf("the first Take answered ok=%v err=%v", ok, err)
		}
		if _, ok, _ := store.Take(ctx, key("flow")); ok {
			t.Error("a second Take found the key the first one took")
		}
		if _, ok, _ := store.Get(ctx, key("flow")); ok {
			t.Error("a Get found the key a Take took")
		}
	})

	t.Run("take answers once when two ask at once", func(t *testing.T) {
		for round := 0; round < 25; round++ {
			k := key("race-" + strconv.Itoa(round))
			_ = store.Put(ctx, k, []byte("x"), later)
			var wg sync.WaitGroup
			var mu sync.Mutex
			found := 0
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, ok, err := store.Take(ctx, k); ok && err == nil {
						mu.Lock()
						found++
						mu.Unlock()
					}
				}()
			}
			wg.Wait()
			if found != 1 {
				t.Fatalf("round %d: four concurrent Takes found the key %d times", round, found)
			}
		}
	})

	t.Run("put-new writes once, and never over a live key", func(t *testing.T) {
		if created, err := store.PutNew(ctx, key("state"), []byte("first"), later); !created || err != nil {
			t.Fatalf("the first PutNew answered created=%v err=%v", created, err)
		}
		if created, err := store.PutNew(ctx, key("state"), []byte("second"), later); created || err != nil {
			t.Errorf("a second PutNew of a live key answered created=%v err=%v; a replayed sign-in would "+
				"be recorded as new", created, err)
		}
		if got, ok, _ := store.Get(ctx, key("state")); !ok || string(got) != "first" {
			t.Errorf("after two PutNews the key holds %q ok=%v, want the first value", got, ok)
		}
		_ = store.Put(ctx, key("put"), []byte("x"), later)
		if created, _ := store.PutNew(ctx, key("put"), []byte("y"), later); created {
			t.Error("PutNew wrote over a key Put had written")
		}
	})

	t.Run("put-new writes once when two ask at once", func(t *testing.T) {
		for round := 0; round < 25; round++ {
			k := key("state-race-" + strconv.Itoa(round))
			var wg sync.WaitGroup
			var mu sync.Mutex
			created := 0
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if ok, err := store.PutNew(ctx, k, []byte("x"), later); ok && err == nil {
						mu.Lock()
						created++
						mu.Unlock()
					}
				}()
			}
			wg.Wait()
			if created != 1 {
				t.Fatalf("round %d: four concurrent PutNews created the key %d times", round, created)
			}
		}
	})

	t.Run("put-new with an expiry already past writes nothing", func(t *testing.T) {
		if created, err := store.PutNew(ctx, key("born-dead"), []byte("x"), time.Now().Add(-time.Second)); created || err != nil {
			t.Errorf("PutNew of a record born dead answered created=%v err=%v", created, err)
		}
		if _, ok, _ := store.Get(ctx, key("born-dead")); ok {
			t.Error("PutNew with an expiry in the past left a key behind")
		}
	})

	t.Run("extend never creates", func(t *testing.T) {
		if ok, err := store.Extend(ctx, key("gone"), later); ok || err != nil {
			t.Errorf("Extend of a missing key answered ok=%v err=%v", ok, err)
		}
		if _, ok, _ := store.Get(ctx, key("gone")); ok {
			t.Error("Extend brought a key into existence - a sign-out on another replica would be undone")
		}
	})

	t.Run("extend moves a live key", func(t *testing.T) {
		_ = store.Put(ctx, key("live"), []byte("x"), time.Now().Add(time.Minute))
		if ok, err := store.Extend(ctx, key("live"), later); !ok || err != nil {
			t.Errorf("Extend of a live key answered ok=%v err=%v", ok, err)
		}
	})

	t.Run("delete forgets, and deleting nothing is not an error", func(t *testing.T) {
		_ = store.Put(ctx, key("bye"), []byte("x"), later)
		if err := store.Delete(ctx, key("bye")); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := store.Get(ctx, key("bye")); ok {
			t.Error("a deleted key is still there")
		}
		if err := store.Delete(ctx, key("bye")); err != nil {
			t.Errorf("deleting a key that is not there answered %v", err)
		}
	})

	t.Run("a record born dead is not a record", func(t *testing.T) {
		_ = store.Put(ctx, key("dead"), []byte("x"), later)
		if err := store.Put(ctx, key("dead"), []byte("y"), time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := store.Get(ctx, key("dead")); ok {
			t.Error("a key written with an expiry in the past is still there")
		}
	})
}

func TestMemoryKeepsTheContract(t *testing.T) {
	contract(t, sharedstate.NewMemory(time.Now), "")
}

func TestASealedStoreKeepsTheContract(t *testing.T) {
	sealed, err := sharedstate.Seal(sharedstate.NewMemory(time.Now), bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatal(err)
	}
	contract(t, sealed, "")
}

func TestValkeyKeepsTheContract(t *testing.T) {
	addr := valkeyUnderTest(t)
	store, err := valkeystore.New(valkeystore.Config{Address: addr, Password: os.Getenv("KT_TEST_VALKEY_PASSWORD"),
		Prefix: "kubetower-test:"})
	if err != nil {
		t.Fatalf("Valkey at %s: %v", addr, err)
	}
	t.Cleanup(store.Close)
	prefix := strconv.FormatInt(time.Now().UnixNano(), 36) + ":"
	contract(t, store, prefix)

	t.Run("a key expires in the store at the instant it was written with", func(t *testing.T) {
		_ = store.Put(ctx, prefix+"short", []byte("x"), time.Now().Add(300*time.Millisecond))
		if _, ok, _ := store.Get(ctx, prefix+"short"); !ok {
			t.Fatal("the key is not there before its expiry")
		}
		if !goneWithin(store, prefix+"short", 10*time.Second) {
			t.Error("a key written to live 300 ms is still there ten seconds later")
		}
	})

	// **The pod's clock is not the server's.** Lifetimes are sent as durations,
	// so a pod an hour out still gets exactly the lifetime it asked for. Sent as
	// instants (PXAT, PEXPIREAT) the pod behind the server writes keys that are
	// born dead, and the pod ahead of it writes keys that outlive their expiry
	// by an hour - which is what a few hundred milliseconds of skew on Docker
	// Desktop did to the subtest above, 8 runs in 10.
	for _, skew := range []time.Duration{-time.Hour, time.Hour} {
		t.Run("a pod clock "+skew.String()+" off the server's", func(t *testing.T) {
			skewed, err := valkeystore.New(valkeystore.Config{Address: addr,
				Password: os.Getenv("KT_TEST_VALKEY_PASSWORD"), Prefix: "kubetower-test:",
				Now: func() time.Time { return time.Now().Add(skew) }})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(skewed.Close)
			podNow := time.Now().Add(skew)
			k := prefix + "skew" + skew.String()
			if err := skewed.Put(ctx, k, []byte("x"), podNow.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, ok, _ := skewed.Get(ctx, k); !ok {
				t.Fatal("a key written to live a second is not there at once")
			}
			if ok, err := skewed.Extend(ctx, k, podNow.Add(time.Second)); !ok || err != nil {
				t.Fatalf("Extend of a live key answered ok=%v err=%v", ok, err)
			}
			if !goneWithin(skewed, k, 10*time.Second) {
				t.Error("a key extended to live a second is still there ten seconds later")
			}
		})
	}
}

// valkeyUnderTest is the Valkey the contract runs against. Unset, a developer's
// machine skips; CI fails, because CI's Go job provides one and its absence is
// the configuration broken, not a Valkey nobody has.
func valkeyUnderTest(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("KT_TEST_VALKEY_ADDR")
	if addr != "" {
		return addr
	}
	if os.Getenv("CI") != "" {
		t.Fatal("KT_TEST_VALKEY_ADDR is unset under CI: the Valkey the Go job starts with docker run is missing, " +
			"so the shared store's contract was not run against Valkey")
	}
	t.Skip("KT_TEST_VALKEY_ADDR is unset, so no Valkey was asked: this test did not run")
	return ""
}

// goneWithin polls for a key to be forgotten. **Polled, not slept**: Docker
// Desktop's VM corrects its clock in steps - a `sleep 3` inside the Valkey
// container can measure as 0 s of server time - so a fixed sleep
// measures the VM's clock corrections as much as the store. What is under test
// is "a second, not an hour", and ten seconds of polling says that robustly.
func goneWithin(store sharedstate.Store, key string, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if _, ok, err := store.Get(ctx, key); !ok && err == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// A store that cannot be reached answers an error that says so - never "not
// there", which would sign everybody out whenever it blinked.
func TestAnUnreachableValkeyIsAFailureAndNotAnAbsence(t *testing.T) {
	// A port on loopback that nothing listens on: dialled, refused at once.
	store, err := valkeystore.New(valkeystore.Config{Address: "127.0.0.1:1"})
	if store == nil {
		t.Fatalf("an unreachable Valkey built no client at all (%v): the console would refuse to start "+
			"when its store is a second behind", err)
	}
	t.Cleanup(store.Close)
	if !errors.Is(err, sharedstate.ErrUnreachable) {
		t.Errorf("building a client for an unreachable Valkey answered %v, expected ErrUnreachable", err)
	}
	_, ok, err := store.Get(ctx, "anything")
	if ok || !errors.Is(err, sharedstate.ErrUnreachable) {
		t.Errorf("Get on an unreachable Valkey answered ok=%v err=%v, expected ErrUnreachable", ok, err)
	}
	if err := store.Put(ctx, "k", []byte("v"), time.Now().Add(time.Minute)); !errors.Is(err, sharedstate.ErrUnreachable) {
		t.Errorf("Put on an unreachable Valkey answered %v", err)
	}
}

func TestMemoryForgetsWhatHasExpired(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	m := sharedstate.NewMemory(func() time.Time { return now })
	_ = m.Put(ctx, "k", []byte("x"), now.Add(time.Minute))
	now = now.Add(time.Minute)
	if _, ok, _ := m.Get(ctx, "k"); ok {
		t.Error("a key is still read at its expiry")
	}
	if ok, _ := m.Extend(ctx, "k", now.Add(time.Hour)); ok {
		t.Error("an expired key was extended back to life")
	}
}

// --- The seal -------------------------------------------------------------------

func TestASealedValueOpensOnlyUnderItsKeyAndItsName(t *testing.T) {
	inner := sharedstate.NewMemory(time.Now)
	key := bytes.Repeat([]byte("k"), 32)
	sealed, err := sharedstate.Seal(inner, key)
	if err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	secret := []byte(`{"verifier":"pkce-verifier-that-must-not-leak"}`)
	if err := sealed.Put(ctx, "flow:a", secret, later); err != nil {
		t.Fatal(err)
	}

	// What the store holds is not what was put.
	raw, _, _ := inner.Get(ctx, "flow:a")
	if bytes.Contains(raw, []byte("pkce-verifier")) {
		t.Errorf("the store holds the value in clear: %q", raw)
	}

	// Moved under another name: does not open.
	_ = inner.Put(ctx, "flow:b", raw, later)
	if _, ok, err := sealed.Get(ctx, "flow:b"); ok || err != nil {
		t.Errorf("a value moved to another key opened there: ok=%v err=%v", ok, err)
	}

	// One byte changed: does not open, and it is "not there" rather than a
	// store failure.
	tampered := append([]byte(nil), raw...)
	tampered[len(tampered)-1] ^= 1
	_ = inner.Put(ctx, "flow:a", tampered, later)
	if _, ok, err := sealed.Get(ctx, "flow:a"); ok || err != nil {
		t.Errorf("a tampered value opened: ok=%v err=%v", ok, err)
	}

	// Written in clear by somebody holding only the store: does not open.
	_ = inner.Put(ctx, "flow:c", secret, later)
	if _, ok, _ := sealed.Get(ctx, "flow:c"); ok {
		t.Error("a value written in clear, straight into the store, was accepted as sealed")
	}

	// Sealed under another session key: does not open.
	other, _ := sharedstate.Seal(inner, bytes.Repeat([]byte("o"), 32))
	_ = other.Put(ctx, "flow:d", secret, later)
	if _, ok, _ := sealed.Get(ctx, "flow:d"); ok {
		t.Error("a value sealed under another key opened")
	}
}

func TestASealNeedsAKeyWorthTheName(t *testing.T) {
	if _, err := sharedstate.Seal(sharedstate.NewMemory(time.Now), []byte("short")); err == nil {
		t.Error("a five-byte key was accepted for sealing")
	}
}

// prefixed is one namespace of a store shared with another: what two consoles
// under two Valkey prefixes are.
type prefixed struct {
	sharedstate.Store
	ns string
}

func (p prefixed) Namespace() string { return p.ns }
func (p prefixed) Put(ctx context.Context, key string, value []byte, expires time.Time) error {
	return p.Store.Put(ctx, p.ns+key, value, expires)
}
func (p prefixed) Get(ctx context.Context, key string) ([]byte, bool, error) {
	return p.Store.Get(ctx, p.ns+key)
}

// TestAValueDoesNotOpenInAnotherNamespace: two consoles share one Valkey and one
// session secret under two prefixes. Whoever can write the store must not be
// able to copy a record of one into the other.
func TestAValueDoesNotOpenInAnotherNamespace(t *testing.T) {
	shared := sharedstate.NewMemory(time.Now)
	key := bytes.Repeat([]byte("k"), 32)
	a, err := sharedstate.Seal(prefixed{shared, "console-a:"}, key)
	if err != nil {
		t.Fatal(err)
	}
	b, err := sharedstate.Seal(prefixed{shared, "console-b:"}, key)
	if err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := a.Put(ctx, "session:x", []byte("alice"), later); err != nil {
		t.Fatal(err)
	}
	raw, ok, _ := shared.Get(ctx, "console-a:session:x")
	if !ok {
		t.Fatal("nothing was written under the first namespace")
	}
	_ = shared.Put(ctx, "console-b:session:x", raw, later)
	if _, ok, err := b.Get(ctx, "session:x"); ok || err != nil {
		t.Errorf("a record copied into another console's namespace opened there (ok=%v err=%v)", ok, err)
	}
	if got, ok, _ := a.Get(ctx, "session:x"); !ok || string(got) != "alice" {
		t.Errorf("the record no longer opens where it was written: %q %v", got, ok)
	}
}

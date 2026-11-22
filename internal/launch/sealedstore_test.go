package launch

import (
	"bytes"
	"context"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/thonicdev/kubetower/internal/sharedstate"
)

// TestTheStoreTheFrontDoorBuildsIsSealed: the shared store is readable by
// whoever holds its password or a shell in its pod, so every value this process
// writes there is sealed with the session key first (sharedstate.Sealed).
// Replacing `sharedstate.Seal(kv, key)` in sealShared with `return kv, nil`
// would leave the rest of this package green; this is the test that says it is
// not.
//
// Both branches of frontDoor, because each calls sealShared on its own: the
// shared password's, and single sign-on's under the server profile.
//
// **What it does not see**: that auth.New is handed the sealed store rather
// than kv. That is one field in each branch, checked by reading; the store
// returned here is the one the plan store uses, and it is this one.
func TestTheStoreTheFrontDoorBuildsIsSealed(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("a password for this test"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []struct {
		name      string
		inCluster bool
		env       func(t *testing.T)
	}{
		{"the shared password", false, func(t *testing.T) { t.Setenv("KT_OIDC_ISSUER", "") }},
		{"single sign-on, server profile", true, ssoEnv},
	} {
		t.Run(profile.name, func(t *testing.T) {
			stateDir(t)
			captureLog(t)
			withConsole(t, false)
			withoutDialog(t)
			forgetCredentials(t)
			t.Setenv("KT_PASSWORD_HASH", string(hash))
			t.Setenv("KT_SESSION_SECRET", "a-session-secret-of-thirty-two-bytes-or-more")
			t.Setenv("KT_USER", "admin")
			profile.env(t)

			memory := sharedstate.NewMemory(time.Now)
			_, shared, err := frontDoor(profile.inCluster, memory)
			if err != nil {
				t.Fatalf("frontDoor: %v", err)
			}
			if shared == nil {
				t.Fatal("frontDoor handed back no store for the plans although one was configured")
			}
			marker := []byte("alice@example.com in system:authenticated")
			ctx := context.Background()
			if err := shared.Put(ctx, "session:probe", marker, time.Now().Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			raw, ok, err := memory.Get(ctx, "session:probe")
			if err != nil || !ok {
				t.Fatalf("nothing reached the store underneath (ok=%v err=%v)", ok, err)
			}
			if bytes.Contains(raw, marker) {
				t.Errorf("the store underneath holds the value in plain text: %q. Whoever can read Valkey "+
					"reads every session, and whoever can write it rewrites them", raw)
			}
		})
	}
}

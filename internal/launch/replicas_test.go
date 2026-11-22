package launch

import (
	"os"
	"strings"
	"testing"
)

// Several replicas refuse, at start-up, every configuration under which pods
// would disagree with each other - one row per reason, and each row asserts the
// refusal names what is missing, because "several replicas need something" is
// not a sentence anybody can act on.
func TestSeveralReplicasRefuseWhatWouldDifferFromPodToPod(t *testing.T) {
	complete := map[string]string{
		"KT_REPLICATED":      "true",
		"KT_VALKEY_ADDR":     "127.0.0.1:1",
		"KT_VALKEY_PASSWORD": "a-valkey-password",
		"KT_SESSION_SECRET":  strings.Repeat("s", 64),
		"KT_PASSWORD_HASH":   "$2a$10$abcdefghijklmnopqrstuv",
	}
	without := func(key string) map[string]string {
		env := map[string]string{}
		for k, v := range complete {
			if k != key {
				env[k] = v
			}
		}
		return env
	}
	cases := []struct {
		name      string
		env       map[string]string
		inCluster bool
		password  bool
		refusal   string
	}{
		{"no shared store", without("KT_VALKEY_ADDR"), true, true, "KT_VALKEY_ADDR"},
		{"the desktop profile", complete, false, true, "--incluster"},
		{"no signing key", without("KT_SESSION_SECRET"), true, true, "KT_SESSION_SECRET"},
		{"a signing key too short to be one", map[string]string{"KT_REPLICATED": "true", "KT_VALKEY_ADDR": "127.0.0.1:1",
			"KT_SESSION_SECRET": "short", "KT_PASSWORD_HASH": "x"}, true, true, "KT_SESSION_SECRET"},
		{"a shared password with no hash", without("KT_PASSWORD_HASH"), true, true, "KT_PASSWORD_HASH"},
		{"a value that is neither true nor false", map[string]string{"KT_REPLICATED": "sometimes"}, true, true,
			"neither true nor false"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := readReplication(c.inCluster, c.password, func(k string) string { return c.env[k] })
			if err == nil {
				t.Fatalf("started with %s", c.name)
			}
			if !strings.Contains(err.Error(), c.refusal) {
				t.Errorf("the refusal does not name %q: %v", c.refusal, err)
			}
			if strings.Contains(err.Error(), "a-valkey-password") {
				t.Errorf("the refusal carries the Valkey password: %v", err)
			}
		})
	}

	// **Single sign-on alone is the case that looks safe**: no password is
	// offered, so nothing here seems to need a key - and without one each replica
	// seals the sign-in flow and signs the session cookie with its own, so a
	// callback landing on the other pod reads as "took too long" and every other
	// request signs the person out.
	t.Run("single sign-on with no signing key", func(t *testing.T) {
		env := without("KT_SESSION_SECRET")
		delete(env, "KT_PASSWORD_HASH")
		env["KT_OIDC_ISSUER"] = "https://idp.example/dex"
		_, err := readReplication(true, false, func(k string) string { return env[k] })
		if err == nil || !strings.Contains(err.Error(), "KT_SESSION_SECRET") {
			t.Fatalf("several replicas with single sign-on and no KT_SESSION_SECRET started (%v); each would "+
				"seal flows and sign sessions the others cannot open", err)
		}
	})

	t.Run("no hash is needed where no password is offered", func(t *testing.T) {
		got, err := readReplication(true, false, func(k string) string { return without("KT_PASSWORD_HASH")[k] })
		if err != nil {
			t.Fatalf("single sign-on alone, with no shared password, was refused: %v", err)
		}
		if !got.Several || got.Store == nil {
			t.Errorf("a complete configuration read as %+v", got)
		}
	})

	t.Run("a store that does not answer yet is not a refusal", func(t *testing.T) {
		// 127.0.0.1:1 answers nothing. The pods and the Valkey StatefulSet start
		// together, and a console that refused to start because its store was a
		// second behind would crash-loop on every install.
		got, err := readReplication(true, true, func(k string) string { return complete[k] })
		if err != nil || got.Store == nil {
			t.Fatalf("an unreachable store at start-up stopped the process: %v", err)
		}
	})
}

// The control: nothing configured is one replica and its own memory: no store,
// no refusal, nothing taken away.
func TestOneReplicaNeedsNothing(t *testing.T) {
	got, err := readReplication(false, true, func(string) string { return "" })
	if err != nil || got.Several || got.Store != nil {
		t.Errorf("an empty environment read as %+v, %v; expected one replica in its own memory", got, err)
	}
}

// TestAWrongValkeyPasswordRefusesToStart: Valkey answered and refused the
// credentials. That is not a store starting up - waiting fixes nothing - and
// logging it as "did not answer yet" while every request answers 503 would
// mislead. Needs a real Valkey, like sharedstate's contract test.
func TestAWrongValkeyPasswordRefusesToStart(t *testing.T) {
	addr := os.Getenv("KT_TEST_VALKEY_ADDR")
	if addr == "" || os.Getenv("KT_TEST_VALKEY_PASSWORD") == "" {
		t.Skip("KT_TEST_VALKEY_ADDR and KT_TEST_VALKEY_PASSWORD are unset, so no Valkey was asked: this test did not run")
	}
	env := map[string]string{
		"KT_VALKEY_ADDR":     addr,
		"KT_VALKEY_PASSWORD": "not-" + os.Getenv("KT_TEST_VALKEY_PASSWORD"),
	}
	_, err := readReplication(true, false, func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "KT_VALKEY_PASSWORD") {
		t.Fatalf("a Valkey refusing the password gave %v; the process must refuse to start, naming the variable", err)
	}

	env["KT_VALKEY_PASSWORD"] = os.Getenv("KT_TEST_VALKEY_PASSWORD")
	if _, err := readReplication(true, false, func(k string) string { return env[k] }); err != nil {
		t.Errorf("the right password was refused: %v", err)
	}
}

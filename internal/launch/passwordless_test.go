package launch

// No password on the loopback desktop. It stays for the containerized desktop,
// for a KT_LISTEN beyond loopback, and where KT_PASSWORD_HASH sets one on
// purpose.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/thonicdev/kubetower/internal/auth"
)

// aFirstRun points the state directory at an empty one, with nothing in the
// environment that would supply a password, and the profile set by the caller.
func aFirstRun(t *testing.T, listen, inContainer string) string {
	t.Helper()
	withoutDialog(t)
	dir := t.TempDir()
	t.Setenv("KT_STATE_DIR", dir)
	t.Setenv("KT_PASSWORD_HASH", "")
	t.Setenv("KT_SESSION_SECRET", "")
	t.Setenv("KT_USER", "")
	t.Setenv("KT_OIDC_ISSUER", "")
	t.Setenv("KT_LISTEN", listen)
	t.Setenv("KT_IN_CONTAINER", inContainer)
	forgetCredentials(t)
	return dir
}

// passwordRoute is the pattern POST /auth/login is registered under, or "".
func passwordRoute(front passwordForm) string {
	mux := http.NewServeMux()
	mountPasswordForm(mux, front)
	_, pattern := mux.Handler(httptest.NewRequest(http.MethodPost, auth.LoginPath, nil))
	return pattern
}

func TestAFirstRunOnTheLoopbackDesktopDrawsNoPassword(t *testing.T) {
	dir := aFirstRun(t, "127.0.0.1:5823", "false")

	var front *auth.Auth
	said := capturingStderr(t, func() {
		var err error
		if front, _, err = frontDoor(false, nil); err != nil {
			t.Fatalf("frontDoor: %v", err)
		}
	})

	if front.OffersPassword() || front.Methods().Password {
		t.Error("the loopback desktop offers a password")
	}
	if got := passwordRoute(front); got != "" {
		t.Errorf("POST %s is registered as %q on the loopback desktop; want absent", auth.LoginPath, got)
	}
	if _, err := os.Stat(filepath.Join(dir, passwordOnceFile)); err == nil {
		t.Errorf("%s was written on the loopback desktop", passwordOnceFile)
	}
	if strings.Contains(strings.ToLower(said), "password:") {
		t.Errorf("a password banner reached the terminal on the loopback desktop:\n%s", said)
	}

	raw, err := os.ReadFile(filepath.Join(dir, credentialsFile))
	if err != nil {
		t.Fatalf("no credentials file on the loopback desktop: %v", err)
	}
	var held credentials
	if err := json.Unmarshal(raw, &held); err != nil {
		t.Fatal(err)
	}
	if held.PasswordHash != "" {
		t.Error("credentials.json holds a password hash on the loopback desktop")
	}
	if len(held.SessionSecret) < 64 || held.User == "" {
		t.Errorf("credentials.json holds user %q and a %d-character session secret; want both",
			held.User, len(held.SessionSecret))
	}

	// The window still gets in: a launch code opens a session, once the
	// desktop has said it opens its own windows, as Run does.
	front.OfferLaunches()
	code, err := front.IssueLaunchCode(true)
	if err != nil {
		t.Fatalf("no launch code on the loopback desktop: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, windowURL("127.0.0.1:5823", code), nil)
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	front.LaunchSignIn(rec, req)
	if len(rec.Result().Cookies()) == 0 {
		t.Error("a launch code opened no session on the loopback desktop")
	}
}

func TestThePasswordStaysWhereSomethingAsksForIt(t *testing.T) {
	for name, c := range map[string]struct{ listen, inContainer string }{
		"the containerized desktop":           {"127.0.0.1:5823", "true"},
		"a desktop on every interface":        {"0.0.0.0:5823", "false"},
		"a desktop on a non-loopback address": {"192.0.2.20:5823", "false"},
	} {
		t.Run(name, func(t *testing.T) {
			aFirstRun(t, c.listen, c.inContainer)
			front, _, err := frontDoor(false, nil)
			if err != nil {
				t.Fatalf("frontDoor: %v", err)
			}
			if !front.OffersPassword() {
				t.Errorf("%s offers no password", name)
			}
			if got := passwordRoute(front); got != "POST "+auth.LoginPath {
				t.Errorf("POST %s is registered as %q; want present", auth.LoginPath, got)
			}
		})
	}

	t.Run("KT_PASSWORD_HASH on the loopback desktop", func(t *testing.T) {
		aFirstRun(t, "127.0.0.1:5823", "false")
		hash, err := bcrypt.GenerateFromPassword([]byte("chosen"), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("KT_PASSWORD_HASH", string(hash))
		front, _, err := frontDoor(false, nil)
		if err != nil {
			t.Fatalf("frontDoor: %v", err)
		}
		if got := passwordRoute(front); got != "POST "+auth.LoginPath {
			t.Errorf("with KT_PASSWORD_HASH set, POST %s is registered as %q; the development loop and "+
				"the smoke suite sign in with it", auth.LoginPath, got)
		}
	})
}

func TestAPasswordlessFileGetsAPasswordWhereOneIsAskedFor(t *testing.T) {
	dir := aFirstRun(t, "127.0.0.1:5823", "false")
	first, firstKey, err := localCredentialsFor(false)
	if err != nil {
		t.Fatalf("a loopback first run: %v", err)
	}
	if first.PasswordHash != "" {
		t.Fatal("the loopback first run drew a password")
	}

	// The same state directory, now started in a container.
	forgetCredentials(t)
	second, secondKey, err := localCredentialsFor(true)
	if err != nil {
		t.Fatalf("a start that asks for a password, on a passwordless file: %v", err)
	}
	if second.PasswordHash == "" {
		t.Error("no password was drawn where one is asked for")
	}
	if string(secondKey) != string(firstKey) {
		t.Error("drawing the password replaced the session secret, signing everybody out")
	}
	if _, err := os.Stat(filepath.Join(dir, passwordOnceFile)); err != nil {
		t.Errorf("the drawn password was not delivered through %s: %v", passwordOnceFile, err)
	}
}

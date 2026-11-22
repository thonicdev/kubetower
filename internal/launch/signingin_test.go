package launch

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// offers is a front door that answers OffersPassword and nothing else.
type offers bool

func (o offers) OffersPassword() bool { return bool(o) }

// TestTheInfosTabIsToldPasswordlessOnlyOnTheLoopbackDesktop. The Infos tab
// tells a person there is no password on this machine when this is true, so
// it must be false wherever a password is asked for: a real front door, built
// the way Run builds it.
func TestTheInfosTabIsToldPasswordlessOnlyOnTheLoopbackDesktop(t *testing.T) {
	t.Run("the loopback desktop", func(t *testing.T) {
		aFirstRun(t, "127.0.0.1:5823", "false")
		front, _, err := frontDoor(false, nil)
		if err != nil {
			t.Fatalf("frontDoor: %v", err)
		}
		if !signedInWithoutPassword(false, front) {
			t.Error("the loopback desktop is not described as passwordless")
		}
	})
	for name, c := range map[string]struct{ listen, inContainer string }{
		"the containerized desktop":    {"127.0.0.1:5823", "true"},
		"a desktop on every interface": {"0.0.0.0:5823", "false"},
	} {
		t.Run(name, func(t *testing.T) {
			aFirstRun(t, c.listen, c.inContainer)
			front, _, err := frontDoor(false, nil)
			if err != nil {
				t.Fatalf("frontDoor: %v", err)
			}
			if signedInWithoutPassword(false, front) {
				t.Errorf("%s asks for a password and is described as passwordless", name)
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
		if signedInWithoutPassword(false, front) {
			t.Error("a password set on purpose is described as absent")
		}
	})
	// The server profile, whatever its front door says: single sign-on takes
	// the password away there, and that is not "no password on this machine".
	if signedInWithoutPassword(true, offers(false)) {
		t.Error("the server profile is described as passwordless")
	}

	// And Run hands this to the Infos tab, rather than a value of its own.
	source, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "opts.Setup.WithoutPassword = signedInWithoutPassword(inCluster, front)") {
		t.Error("run.go no longer hands the Infos tab signedInWithoutPassword's answer")
	}
}

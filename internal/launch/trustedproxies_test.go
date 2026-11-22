package launch

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// TestPasswordSignInBehindAnUnnamedIngressIsSaidAtStart: under --incluster with
// password sign-in on and KT_TRUSTED_PROXIES empty, nothing per client bounds a
// guess and a flood refuses most first sign-ins - so the start says so, with
// the numbers; and it says nothing once the proxies are named, or on the
// desktop.
func TestPasswordSignInBehindAnUnnamedIngressIsSaidAtStart(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("a password for this test"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		inCluster bool
		proxies   string
		said      bool
	}{
		{"server profile, no proxies named", true, "", true},
		{"server profile, proxies named", true, "10.0.0.0/8", false},
		{"desktop", false, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir(t)
			logged := captureLog(t)
			withConsole(t, false)
			withoutDialog(t)
			forgetCredentials(t)
			t.Setenv("KT_OIDC_ISSUER", "")
			t.Setenv("KT_PASSWORD_HASH", string(hash))
			t.Setenv("KT_SESSION_SECRET", "a-session-secret-of-thirty-two-bytes-or-more")
			t.Setenv("KT_USER", "admin")
			t.Setenv("KT_TRUSTED_PROXIES", tc.proxies)
			if _, _, err := frontDoor(tc.inCluster, nil); err != nil {
				t.Fatalf("frontDoor: %v", err)
			}
			said := strings.Contains(logged.String(), "KT_TRUSTED_PROXIES is empty")
			if said != tc.said {
				t.Errorf("warning said=%t, want %t; the log:\n%s", said, tc.said, logged.String())
			}
			if said && !strings.Contains(logged.String(), "95% at 100 a second") {
				t.Errorf("the warning does not state the residual in numbers: %s", logged.String())
			}
		})
	}
}

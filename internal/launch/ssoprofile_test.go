package launch

import (
	"bytes"
	"errors"
	"log"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Single sign-on is the server profile's and nobody else's.
//
// **Why this is a refusal to start and not a warning.** Without --incluster the
// process registers the local shell, the node shell, the desktop terminal and the
// assistant (profileroutes_test.go walks that half). Each runs as this process,
// with its kubeconfig. Behind a password that is one person's machine; behind an
// identity provider it is every person the provider accepts, and this console
// has no allow-list of users or groups to narrow that.

// ssoEnv is a complete provider configuration: everything singleSignOn reads, so
// that the only thing a row varies is the profile.
func ssoEnv(t *testing.T) {
	t.Helper()
	t.Setenv("KT_OIDC_ISSUER", "https://idp.example/dex")
	t.Setenv("KT_OIDC_CLIENT_ID", "kubetower")
	t.Setenv("KT_OIDC_CLIENT_SECRET", "")
	t.Setenv("KT_OIDC_REDIRECT_URL", "https://kubetower.example/api/auth/oidc/callback")
	t.Setenv("KT_AUTH_LOCAL_ACCOUNT", "")
}

func TestSingleSignOnIsRefusedOutsideTheServerProfile(t *testing.T) {
	ssoEnv(t)

	// Through frontDoor, which is what main calls: the refusal happens before
	// a password is read, a state directory is touched or a route exists.
	front, _, err := frontDoor(false, nil)
	if !errors.Is(err, errSSOOutsideServerProfile) {
		// `front != nil`, never front itself: %v of an *auth.Auth prints its
		// fields, and one of them is the session key.
		t.Fatalf("frontDoor(desktop) with KT_OIDC_ISSUER set returned (a front door: %t, %v); it must refuse "+
			"to start. The desktop profile serves a local shell, a node shell and the assistant as this "+
			"process, and an identity provider would hand them to everybody it accepts", front != nil, err)
	}

	cfg, _, err := singleSignOn(true)
	if err != nil || cfg == nil {
		t.Fatalf("singleSignOn(server profile) = (%v, %v); the server profile is where single sign-on belongs", cfg, err)
	}
}

// TestADesktopWithNoIssuerStillStarts is the half that would be skipped: the
// cheapest way to pass the test above is to refuse every desktop.
func TestADesktopWithNoIssuerStillStarts(t *testing.T) {
	t.Setenv("KT_OIDC_ISSUER", "")
	cfg, _, err := singleSignOn(false)
	if err != nil || cfg != nil {
		t.Errorf("singleSignOn(desktop, no issuer) = (%v, %v), want no provider and no error", cfg, err)
	}
}

// TestMainHandsTheProfileToTheFrontDoor is the line the two tests above cannot
// see: main is not callable, so a `frontDoor(false)` written there would leave
// both green and a desktop build signing people in. Read from the source, the
// way envwiring_test.go reads it for `KT_` variables.
func TestMainHandsTheProfileToTheFrontDoor(t *testing.T) {
	raw, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatal(err)
	}
	calls := regexp.MustCompile(`frontDoor\(([^)]*)\)`).FindAllStringSubmatch(string(raw), -1)
	var fromMain []string
	for _, c := range calls {
		if !strings.HasPrefix(c[1], "inCluster bool") { // the declaration
			first, _, _ := strings.Cut(c[1], ",")
			fromMain = append(fromMain, first)
		}
	}
	if len(fromMain) != 1 || fromMain[0] != "inCluster" {
		t.Errorf("run.go calls frontDoor with %q; it must call it once, with the --incluster flag it parsed", fromMain)
	}
}

// TestSingleSignOnWithoutASharedKeyIsSaidOutLoud: without KT_SESSION_SECRET the
// key is drawn per process, and it signs the session and seals the sign-in
// flow - so a second replica opens neither. The process says so at start-up,
// and says nothing when the key is set.
func TestSingleSignOnWithoutASharedKeyIsSaidOutLoud(t *testing.T) {
	for _, secret := range []string{"", strings.Repeat("ab", 32)} {
		ssoEnv(t)
		t.Setenv("KT_SESSION_SECRET", secret)
		var out bytes.Buffer
		log.SetOutput(&out)
		front, _, err := frontDoor(true, nil)
		log.SetOutput(os.Stderr)
		if err != nil || front == nil {
			t.Fatalf("frontDoor(server profile) = (a front door: %t, %v)", front != nil, err)
		}
		warned := strings.Contains(out.String(), "KT_SESSION_SECRET is not")
		if want := secret == ""; warned != want {
			t.Errorf("KT_SESSION_SECRET set: %t: the warning that sessions and the sign-in flow do not cross "+
				"replicas was said: %t, want %t. The log:\n%s", secret != "", warned, want, out.String())
		}
	}
}

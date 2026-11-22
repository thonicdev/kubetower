package launch

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// passwordOnlyServer is a server profile with a configured KT_PASSWORD_HASH and
// no identity provider: whose rights the password carries, and the Secure
// cookie behind the ingress.
func passwordOnlyServer(t *testing.T, password string) {
	t.Helper()
	stateDir(t)
	forgetCredentials(t)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KT_OIDC_ISSUER", "")
	t.Setenv("KT_PASSWORD_HASH", string(hash))
	t.Setenv("KT_SESSION_SECRET", strings.Repeat("ab", 32))
	t.Setenv("KT_USER", "")
	t.Setenv("KT_TRUSTED_PROXIES", "")
}

// TestTheServerProfileSaysWhoseRightsThePasswordCarries: under the server
// profile with KT_PASSWORD_HASH set,
// the start-up log carries one line saying that every holder of the password
// acts with the pod's ServiceAccount's rights. The desktop, where the
// password is one operator's own kubeconfig, says nothing of the kind.
func TestTheServerProfileSaysWhoseRightsThePasswordCarries(t *testing.T) {
	for _, inCluster := range []bool{true, false} {
		passwordOnlyServer(t, "correct-horse")
		out := captureLog(t)
		if _, _, err := frontDoor(inCluster, nil); err != nil {
			t.Fatalf("frontDoor(%t): %v", inCluster, err)
		}
		line := sharedAccountRights("admin", "KT_PASSWORD_HASH")
		said := strings.Count(out.String(), line)
		if want := map[bool]int{true: 1, false: 0}[inCluster]; said != want {
			t.Errorf("server profile %t: the shared account's rights were said %d times, want %d. The log:\n%s",
				inCluster, said, want, out.String())
		}
	}
	line := sharedAccountRights("admin", "KT_PASSWORD_HASH")
	for _, words := range []string{"KT_PASSWORD_HASH", `"admin"`, "ServiceAccount's rights", "via=password"} {
		if !strings.Contains(line, words) {
			t.Errorf("the line does not name %s: %s", words, line)
		}
	}
}

// TestAPasswordOnlyServerProfileSignsInWithASecureCookie is the wiring:
// the front door frontDoor builds for the server profile sets a Secure session
// cookie at a password sign-in, and the desktop's - plain HTTP over loopback -
// does not.
func TestAPasswordOnlyServerProfileSignsInWithASecureCookie(t *testing.T) {
	for _, inCluster := range []bool{true, false} {
		passwordOnlyServer(t, "correct-horse")
		// A desktop that offers a password at all: KT_PASSWORD_HASH keeps it
		// on loopback.
		captureLog(t)
		front, _, err := frontDoor(inCluster, nil)
		if err != nil {
			t.Fatalf("frontDoor(%t): %v", inCluster, err)
		}
		form := url.Values{"user": {"admin"}, "password": {"correct-horse"}}
		req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		front.Login(rec, req)
		var session *http.Cookie
		for _, c := range rec.Result().Cookies() {
			if c.Name == "kubetower_session" {
				session = c
			}
		}
		if session == nil {
			t.Fatalf("server profile %t: the sign-in answered %d and set no session cookie", inCluster, rec.Code)
		}
		if session.Secure != inCluster {
			t.Errorf("server profile %t: the session cookie's Secure is %t", inCluster, session.Secure)
		}
	}
}

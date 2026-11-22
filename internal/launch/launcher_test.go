package launch

// The launcher secret, and a second launch handed a
// working code by the first.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/thonicdev/kubetower/internal/auth"
)

func TestTheLauncherSecretIsWrittenOnceAndOnlyByTheFirstInstance(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KT_STATE_DIR", dir)

	if _, err := launcherSecret(false); err == nil {
		t.Fatal("a second launch read a launcher secret that no instance had written")
	}
	if _, err := os.Stat(filepath.Join(dir, launcherFile)); err == nil {
		t.Fatal("a second launch wrote the launcher secret; only the first instance may")
	}

	first, err := launcherSecret(true)
	if err != nil {
		t.Fatalf("launcherSecret(create): %v", err)
	}
	if len(first) != 2*launcherSecretBytes {
		t.Errorf("the secret is %d hex characters; want %d", len(first), 2*launcherSecretBytes)
	}
	again, err := launcherSecret(true)
	if err != nil || again != first {
		t.Errorf("a second start drew another secret (%v); it must keep the first", err)
	}
	read, err := launcherSecret(false)
	if err != nil || read != first {
		t.Errorf("a second launch read %q (%v); want the first instance's", read, err)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, launcherFile))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("the launcher secret is %o; want 600", perm)
		}
	}
}

// instance is a first instance's two routes, behind the Host allowlist, on a
// real loopback listener.
func instance(t *testing.T, secret string) (*httptest.Server, *auth.Auth) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("unused"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	front, err := auth.New(auth.Config{User: "admin", PasswordHash: string(hash),
		SessionKey: []byte("a-thirty-two-byte-key-for-tests!")})
	if err != nil {
		t.Fatal(err)
	}
	front.OfferLaunches()
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+auth.LaunchPath, front.LaunchSignIn)
	mux.HandleFunc("POST "+auth.HandoffPath, front.Handoff)
	srv := httptest.NewUnstartedServer(guardHost(front.Middleware(mux)))
	// The address every proof must name is the one this instance answers on,
	// known once the listener exists and set before anything is served.
	front.AcceptHandoffs(secret, srv.Listener.Addr().String())
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, front
}

func TestASecondLaunchIsHandedACodeThatSignsIn(t *testing.T) {
	secret := strings.Repeat("ab", launcherSecretBytes)
	srv, _ := instance(t, secret)
	listen := strings.TrimPrefix(srv.URL, "http://")

	if !instanceAnswers(listen) {
		t.Fatal("instanceAnswers says nothing answers on a listening server")
	}
	code, err := handoffClient(listen, secret)(true)
	if err != nil {
		t.Fatalf("the handoff failed with the right secret: %v", err)
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(windowURL(listen, code))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/" || len(resp.Cookies()) == 0 {
		t.Errorf("the handed-off code answered %d to %q with %d cookies; want 302 to / with a session",
			resp.StatusCode, resp.Header.Get("Location"), len(resp.Cookies()))
	}

	if _, err := handoffClient(listen, strings.Repeat("cd", launcherSecretBytes))(true); err == nil {
		t.Error("the handoff gave a code to the wrong secret")
	}
}

func TestNothingAnswersWhereNothingListens(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	listen := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()
	if instanceAnswers(listen) {
		t.Error("instanceAnswers says something answers on a closed port")
	}
}

func TestTheLoopbackDesktopIsTheDesktopOnLoopbackOutsideAContainer(t *testing.T) {
	for _, c := range []struct {
		listen                 string
		inCluster, inContainer bool
		want                   bool
	}{
		{"127.0.0.1:5823", false, false, true},
		{"localhost:5823", false, false, true},
		{"[::1]:5823", false, false, true},
		{":5823", false, false, false},
		{"0.0.0.0:5823", false, false, false},
		{"192.0.2.20:5823", false, false, false},
		{"127.0.0.1:5823", false, true, false},
		{"127.0.0.1:5823", true, false, false},
	} {
		if got := loopbackDesktop(c.listen, c.inCluster, c.inContainer); got != c.want {
			t.Errorf("loopbackDesktop(%q, inCluster %v, inContainer %v) = %v; want %v",
				c.listen, c.inCluster, c.inContainer, got, c.want)
		}
	}
}

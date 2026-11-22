package launch

// A port squatter must get nothing
// from a second launch - no proof it can replay to the instance that binds the
// port after it, and no window on its own page. These run the real
// secondLaunch, the real client and the real instance routes behind the Host
// guard, on real loopback listeners; the window is a fake opener.

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/thonicdev/kubetower/internal/auth"
)

// recordingSquatter holds addr and answers every request with answer, keeping
// every LauncherHeader it was sent.
type recordingSquatter struct {
	mu      sync.Mutex
	headers []string
	srv     *httptest.Server
}

func (s *recordingSquatter) captured() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.headers...)
}

func squatOn(t *testing.T, ln net.Listener, answer http.HandlerFunc) *recordingSquatter {
	t.Helper()
	s := &recordingSquatter{}
	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.headers = append(s.headers, r.Header.Get(auth.LauncherHeader))
		s.mu.Unlock()
		answer(w, r)
	}))
	s.srv.Listener.Close()
	s.srv.Listener = ln
	s.srv.Start()
	t.Cleanup(s.srv.Close)
	return s
}

// launchFront is a front door that offers launches, as Run's on the desktop.
func launchFront(t *testing.T) *auth.Auth {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(t.Name()), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	front, err := auth.New(auth.Config{User: "admin", PasswordHash: string(hash),
		SessionKey: []byte("a-thirty-two-byte-key-for-tests!")})
	if err != nil {
		t.Fatal(err)
	}
	front.OfferLaunches()
	return front
}

// instanceOn is a first instance's two routes, behind the Host guard, on ln.
func instanceOn(t *testing.T, ln net.Listener, secret string) *auth.Auth {
	t.Helper()
	front := launchFront(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+auth.LaunchPath, front.LaunchSignIn)
	mux.HandleFunc("POST "+auth.HandoffPath, front.Handoff)
	srv := httptest.NewUnstartedServer(guardHost(front.Middleware(mux)))
	srv.Listener.Close()
	srv.Listener = ln
	front.AcceptHandoffs(secret, ln.Addr().String())
	srv.Start()
	t.Cleanup(srv.Close)
	return front
}

// The scenario: a squatter holds the port when a launch starts, keeps whatever
// the launch sends it, answers 403 and lets the port go; the real instance then
// binds it, and the squatter replays everything it kept. It must get no code
// and no session.
func TestASquatterThatReleasesThePortCannotReplayTheLaunch(t *testing.T) {
	t.Setenv("KT_STATE_DIR", t.TempDir())
	secret, err := launcherSecret(true)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	squat := squatOn(t, ln, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "refused", http.StatusForbidden)
	})

	var seen openedWindow
	if secondLaunch(true, addr, false, false, fakeOpener(t, &seen)) || seen.calls != 0 {
		t.Fatal("a launch met by a squatter opened a window")
	}
	kept := squat.captured()
	if len(kept) == 0 {
		t.Fatal("the squatter was sent nothing, so this test proves nothing")
	}
	squat.srv.Close()

	again, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("the port could not be bound again at once on this machine: %v", err)
	}
	real := instanceOn(t, again, secret)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, header := range kept {
		if strings.Contains(header, ".prove.") {
			t.Errorf("the squatter was sent a prove: %q", header)
		}
		for _, query := range []string{"", "?native=1"} {
			req, err := http.NewRequest(http.MethodPost, "http://"+addr+auth.HandoffPath+query, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set(auth.LauncherHeader, header)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var body bytes.Buffer
			_, _ = body.ReadFrom(resp.Body)
			resp.Body.Close()
			var answer struct {
				Code, Sealed string
			}
			_ = json.Unmarshal(body.Bytes(), &answer)
			if answer.Code != "" || answer.Sealed != "" {
				t.Errorf("replaying %q to the real instance was answered %d with a code: %s",
					header, resp.StatusCode, body.String())
			}
		}
	}
	if code, err := real.IssueLaunchCode(false); err != nil || code == "" {
		t.Fatalf("the real instance mints no code at all, so this test proves nothing: %v", err)
	}
}

// A squatter that answers 200 with a code of its own: without a mutual
// exchange, the second launch would open its native window on the squatter's
// page.
func TestASquatterThatAnswersWithACodeOfItsOwnGetsNoWindow(t *testing.T) {
	t.Setenv("KT_STATE_DIR", t.TempDir())
	if _, err := launcherSecret(true); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	squat := squatOn(t, ln, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"c2VjcmV0LWNvZGUtZm9yLWEtd2luZG93LXRoaXMtcHJvY2Vzcy1vcGVucw"}`))
	})
	said := captureLog(t)

	var seen openedWindow
	if secondLaunch(true, ln.Addr().String(), false, false, fakeOpener(t, &seen)) {
		t.Error("a launch met by a squatter handed off to it")
	}
	if seen.calls != 0 {
		t.Error("a window was opened on the squatter's page")
	}
	for _, header := range squat.captured() {
		if strings.Contains(header, ".prove.") {
			t.Errorf("the squatter was sent a prove: %q", header)
		}
	}
	if !strings.Contains(said.String(), "something other than this account's KubeTower holds") {
		t.Errorf("the launch did not say that something else holds the port:\n%s", said.String())
	}
}

// A busy instance is not an
// impostor. The launch says so, opens nothing, and does not go on to start a
// second server that would fail on the port.
func TestABusyInstanceEndsTheLaunchWithoutAWindowOrABlame(t *testing.T) {
	t.Setenv("KT_STATE_DIR", t.TempDir())
	if _, err := launcherSecret(true); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	squatOn(t, ln, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	})
	said := captureLog(t)

	var seen openedWindow
	if !secondLaunch(true, ln.Addr().String(), false, false, fakeOpener(t, &seen)) {
		t.Error("a launch met by a busy instance went on to start a server of its own")
	}
	if seen.calls != 0 {
		t.Error("a window was opened although no code was handed over")
	}
	if strings.Contains(said.String(), "something other than this account's KubeTower") {
		t.Errorf("a busy instance was blamed as an impostor:\n%s", said.String())
	}
	if !strings.Contains(said.String(), "busy") {
		t.Errorf("the launch did not say the instance is busy:\n%s", said.String())
	}
}

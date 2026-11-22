package terminal

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/identity"
)

// TestOnePersonCannotHoldEveryShellAndSignOutClosesTheirs: under the server
// profile the sixteen sessions are every
// person's: alice is held to her share, bob can still open a pod shell, and
// signing alice out closes hers and gives the slots back.
func TestOnePersonCannotHoldEveryShellAndSignOutClosesTheirs(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")
	kubeconfig := execKubeconfig(t, "somewhere")
	term, err := New(managerFor(t, kubeconfig), Options{
		Kubeconfig:           cluster.Kubeconfig{kubeconfig},
		Access:               accesstest.Allow(),
		MaxSessionsPerPerson: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A pod whose command runs until the session is canceled.
	term.openExec = func(*rest.Config, *url.URL) (remotecommand.Executor, error) {
		return parkingExecutor{firstRead: make(chan struct{})}, nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := identity.Who{Name: r.URL.Query().Get("who"), Method: identity.SSO}
		term.ExecHandler().ServeHTTP(w, r.WithContext(identity.With(r.Context(), who)))
	}))
	t.Cleanup(srv.Close)
	target := func(who string) string {
		return "ws" + strings.TrimPrefix(srv.URL, "http") +
			"/kt-api/exec?cluster=somewhere&namespace=default&pod=api&who=" + who
	}

	var alices []*websocket.Conn
	for i := 0; i < 2; i++ {
		conn, _, err := websocket.DefaultDialer.Dial(target("alice"), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		alices = append(alices, conn)
	}
	waitFor(t, func() bool { return term.sessions.live() == 2 }, "alice's two shells did not open")

	if got := dialAndRead(t, target("alice")); !strings.Contains(got, "one person's share") {
		t.Fatalf("alice's third shell was not refused at her share of 2: %q", got)
	}

	bob, _, err := websocket.DefaultDialer.Dial(target("bob"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bob.Close()
	waitFor(t, func() bool { return term.sessions.live() == 3 }, "bob was refused because alice holds her share")

	if closed := term.EndSessionsOf(identity.Who{Name: "alice", Method: identity.SSO}); closed != 2 {
		t.Fatalf("signing alice out closed %d sessions, want 2", closed)
	}
	for _, conn := range alices {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var said strings.Builder
		for {
			_, data, err := conn.ReadMessage()
			said.Write(data)
			if err != nil {
				break
			}
		}
		if !strings.Contains(said.String(), "you logged out") {
			t.Errorf("alice's shell closed without saying why: %q", said.String())
		}
	}
	waitFor(t, func() bool { return term.sessions.live() == 1 }, "alice's slots were not given back at sign-out")
	if term.EndSessionsOf(identity.Who{}) != 0 {
		t.Error("an empty name closed sessions: it names nobody")
	}
}

// On the desktop there is no share: Options leaves it at zero.
func TestWithoutAShareOnePersonMayHoldTheCeiling(t *testing.T) {
	limit := newSessionLimit(3)
	for i := 0; i < 3; i++ {
		if err := limit.takeFor("alice", true); err != nil {
			t.Fatalf("session %d refused with no share set: %v", i+1, err)
		}
	}
	if err := newSessionLimit(4).shared(4).takeFor("alice", true); err != nil {
		t.Fatalf("a share equal to the ceiling is no share, and refused: %v", err)
	}
}

// TestThePasswordAccountIsNotHeldToAPersonsShare is the shared account's case:
// under a password-only server profile everybody who holds the password is one
// name, so the share would cut the whole team to two here (four in production).
// Password sessions count against the ceiling alone.
func TestThePasswordAccountIsNotHeldToAPersonsShare(t *testing.T) {
	limit := newSessionLimit(8).shared(2)
	password := identity.Who{Name: "admin", Method: identity.Password}
	for i := 0; i < 5; i++ {
		if err := limit.takeFor(password.Key(), !password.Shared()); err != nil {
			t.Fatalf("password session %d refused at a person's share: %v", i+1, err)
		}
	}
	// And a single sign-on account named like it is somebody else, with a share
	// of its own (keyed by method and name).
	sso := identity.Who{Name: "admin", Method: identity.SSO}
	for i := 0; i < 2; i++ {
		if err := limit.takeFor(sso.Key(), !sso.Shared()); err != nil {
			t.Fatalf("the single sign-on admin's session %d refused: %v", i+1, err)
		}
	}
	if err := limit.takeFor(sso.Key(), !sso.Shared()); err == nil {
		t.Error("the single sign-on admin was not held to a share")
	}
}

// heldStream opens a real socket and holds it in term for who, as a handler
// does, until the test ends; it answers the stream and the browser side.
func heldStream(t *testing.T, term *Terminal, who identity.Who) (*stream, *websocket.Conn) {
	t.Helper()
	held := make(chan *stream, 1)
	finished := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: sameOrigin}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		s := term.openStream(conn, wireFramed)
		defer s.close()
		release, err := term.hold(identity.With(r.Context(), who), s)
		if err != nil {
			t.Errorf("hold: %v", err)
			return
		}
		defer release()
		held <- s
		select {
		case <-s.closed():
		case <-finished:
		}
	}))
	t.Cleanup(func() { close(finished); srv.Close() })
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return <-held, client
}

// TestSigningOutASingleSignOnAccountLeavesTheSameNamedPasswordSessions: keyed
// by name alone, the single sign-on "admin" signing out would close every shell the
// shared password account had open.
func TestSigningOutASingleSignOnAccountLeavesTheSameNamedPasswordSessions(t *testing.T) {
	term := &Terminal{sessions: newSessionLimit(4), logf: t.Logf}
	s, _ := heldStream(t, term, identity.Who{Name: "admin", Method: identity.Password})
	if n := term.EndSessionsOf(identity.Who{Name: "admin", Method: identity.SSO}); n != 0 {
		t.Errorf("signing out the single sign-on admin closed %d of the password account's sessions", n)
	}
	select {
	case <-s.closed():
		t.Error("the password account's session was closed")
	default:
	}
}

// TestSigningOutClosesOnlyWhatThatSessionOpened: alice signed in from two
// browsers; signing out of one closes the shell that browser's session opened
// and leaves the other, whose session still works.
func TestSigningOutClosesOnlyWhatThatSessionOpened(t *testing.T) {
	term := &Terminal{sessions: newSessionLimit(4), logf: t.Logf}
	laptop := identity.Who{Name: "alice", Method: identity.SSO, Session: "laptop"}
	desktop := identity.Who{Name: "alice", Method: identity.SSO, Session: "desktop"}
	fromLaptop, _ := heldStream(t, term, laptop)
	fromDesktop, _ := heldStream(t, term, desktop)

	if n := term.EndSessionsOf(laptop); n != 1 {
		t.Errorf("signing the laptop out closed %d sessions, want 1", n)
	}
	select {
	case <-fromLaptop.closed():
	case <-time.After(5 * time.Second):
		t.Error("the laptop's shell stayed open after its sign-out")
	}
	select {
	case <-fromDesktop.closed():
		t.Error("the desktop's shell was closed by the laptop's sign-out")
	default:
	}
}

// TestNoSessionOpensAfterClose: a handler still in its access review when the
// server stops must not reach hold after Close's snapshot, open a session Close
// never ends, and race its live.Add with live.Wait.
func TestNoSessionOpensAfterClose(t *testing.T) {
	term := &Terminal{sessions: newSessionLimit(4), logf: t.Logf}
	term.Close()
	if _, err := term.hold(context.Background(), &stream{gone: make(chan struct{})}); !errors.Is(err, errShuttingDown) {
		t.Errorf("hold after Close answered %v, want errShuttingDown", err)
	}
	if n := term.sessions.live(); n != 0 {
		t.Errorf("a slot was taken after Close: %d live", n)
	}
}

// TestClosingIsBoundedWhenEveryWriteIsStuck: a goodbye waits for the write
// lock, which a write to a browser that stopped reading holds for up to
// writeWait, one socket after another - sixteen of them could make a shutdown
// or a sign-out wait minutes. Here every socket's lock is held, as
// a parked write holds it, and both still return within about farewellWait.
func TestClosingIsBoundedWhenEveryWriteIsStuck(t *testing.T) {
	for _, how := range []string{"Close", "EndSessionsOf"} {
		term := &Terminal{sessions: newSessionLimit(8), logf: t.Logf}
		alice := identity.Who{Name: "alice", Method: identity.SSO, Session: "laptop"}
		var stuck []*stream
		for i := 0; i < 4; i++ {
			s, _ := heldStream(t, term, alice)
			s.writeMu.Lock()
			stuck = append(stuck, s)
		}
		start := time.Now()
		done := make(chan struct{})
		go func() {
			defer close(done)
			if how == "Close" {
				term.Close()
			} else {
				term.EndSessionsOf(alice)
			}
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s did not return with every write stuck", how)
		}
		if elapsed := time.Since(start); elapsed > 3*farewellWait {
			t.Errorf("%s took %s with four stuck writes, want about one farewellWait (%s)", how, elapsed, farewellWait)
		}
		for _, s := range stuck {
			s.writeMu.Unlock()
		}
	}
}

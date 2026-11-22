package logs

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/identity"
)

// TestOnePersonCannotHoldEveryFollowAndSignOutEndsTheirs: under the server
// profile the sixteen
// follows are every person's: one person's share is bounded, another person can
// still open one, and the first person's follows end when they sign out.
func TestOnePersonCannotHoldEveryFollowAndSignOutEndsTheirs(t *testing.T) {
	cluster := fakeAPI(t, 5*time.Millisecond, 0)
	clients := newFakeClients(t, cluster.URL, 0)

	s := New(clients, accesstest.Allow(), nil, nil)
	s.deadline = time.Minute
	s.SharePerPerson(2)

	// What the front door does, and nothing more: the person on the request.
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := identity.Who{Name: r.Header.Get("X-Test-Who"), Method: identity.SSO}
		s.Handler().ServeHTTP(w, r.WithContext(identity.With(r.Context(), who)))
	}))
	defer front.Close()

	var cancels []context.CancelFunc
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()
	open := func(who string) *http.Response {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			front.URL+"/kt-api/logs?cluster=docker-desktop&namespace=n&pod=p&container=app&follow=1", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Test-Who", who)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("the stream did not open: %v", err)
		}
		return resp
	}

	var alices []*http.Response
	for i := 0; i < 2; i++ {
		resp := open("alice")
		if got := resp.Header.Get("X-KubeTower-Refusal"); got != "" {
			t.Fatalf("alice's follow %d was refused inside her share: %q", i+1, got)
		}
		alices = append(alices, resp)
	}

	refused := open("alice")
	body := make([]byte, 256)
	n, _ := refused.Body.Read(body)
	_ = refused.Body.Close()
	if refused.Header.Get("X-KubeTower-Refusal") == "" || !strings.Contains(string(body[:n]), "one person's share") {
		t.Fatalf("alice's third follow was not refused at her share of 2: %q", string(body[:n]))
	}

	bob := open("bob")
	if got := bob.Header.Get("X-KubeTower-Refusal"); got != "" {
		t.Fatalf("bob was refused because alice holds her share: %q", got)
	}
	defer bob.Body.Close()

	if ended := s.EndStreamsOf(identity.Who{Name: "alice", Method: identity.SSO}); ended != 2 {
		t.Fatalf("signing alice out ended %d follows, want 2", ended)
	}
	for _, resp := range alices {
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(io.Discard, resp.Body)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("alice signed out and her follow kept streaming")
		}
		_ = resp.Body.Close()
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.live.open() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("%d follows held after alice signed out, want bob's 1", s.live.open())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// What the macOS window's App Nap guard reads is the same count.
	if s.Following() != 1 {
		t.Errorf("Following() = %d with bob's one follow open, want 1", s.Following())
	}
	if s.EndStreamsOf(identity.Who{}) != 0 {
		t.Error("an empty name ended follows: it names nobody")
	}
}

// And on the desktop there is no share: one person may use every slot.
func TestWithoutAShareOnePersonMayHoldTheCeiling(t *testing.T) {
	limit := newStreamLimit(3)
	for i := 0; i < 3; i++ {
		if err := limit.takeFor("alice", true); err != nil {
			t.Fatalf("follow %d refused with no share set: %v", i+1, err)
		}
	}
}

// TestSigningOutEndsOnlyThatSessionsFollows: alice follows a log from two
// browsers; signing one out ends that session's follow and leaves the other's.
func TestSigningOutEndsOnlyThatSessionsFollows(t *testing.T) {
	l := newStreamLimit(4)
	laptop := identity.Who{Name: "alice", Method: identity.SSO, Session: "laptop"}
	desktop := identity.Who{Name: "alice", Method: identity.SSO, Session: "desktop"}
	fromLaptop, releaseLaptop, err := l.hold(identity.With(context.Background(), laptop))
	if err != nil {
		t.Fatal(err)
	}
	defer releaseLaptop()
	fromDesktop, releaseDesktop, err := l.hold(identity.With(context.Background(), desktop))
	if err != nil {
		t.Fatal(err)
	}
	defer releaseDesktop()

	if n := l.endFor(laptop); n != 1 {
		t.Errorf("signing the laptop out ended %d follows, want 1", n)
	}
	if fromLaptop.Err() == nil {
		t.Error("the laptop's follow kept running after its sign-out")
	}
	if fromDesktop.Err() != nil {
		t.Error("the desktop's follow was ended by the laptop's sign-out")
	}
}

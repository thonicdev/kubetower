package terminal

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// silentAPIServer accepts every connection and answers nothing: it hijacks the
// request and holds the connection until the test ends, as an API server or a
// proxy that stopped answering mid-upgrade does. It answers how many
// connections it holds, each parking one handler goroutine of its own.
func silentAPIServer(t *testing.T) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	var held []net.Conn
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		mu.Lock()
		held = append(held, conn)
		mu.Unlock()
		<-release
	}))
	t.Cleanup(func() {
		close(release)
		mu.Lock()
		for _, c := range held {
			_ = c.Close()
		}
		mu.Unlock()
		srv.Close()
	})
	return srv, func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(held)
	}
}

// TestATunnelDialThatIsNeverAnsweredGivesUp: against a server that accepts
// the upgrade and never replies, an unbounded dial would park a goroutine and
// a connection for good, since the forwarder's own wait stops its caller
// waiting, not the dial. The dial ends at its bound and leaves nothing running.
func TestATunnelDialThatIsNeverAnsweredGivesUp(t *testing.T) {
	srv, holding := silentAPIServer(t)
	target, err := url.Parse(srv.URL + "/api/v1/namespaces/default/pods/api/portforward")
	if err != nil {
		t.Fatal(err)
	}
	client := &cluster.Client{Context: "prod", Config: &rest.Config{Host: srv.URL}}

	baseline := runtime.NumGoroutine()
	dialer, err := newBoundedForwardDialer(context.Background(), client, target, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	type result struct{ err error }
	done := make(chan result, 1)
	go func() {
		conn, _, err := dialer.Dial("portforward.k8s.io")
		if err == nil {
			_ = conn.Close()
		}
		done <- result{err}
	}()
	select {
	case r := <-done:
		if r.err == nil {
			t.Fatal("a dial nobody answered succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a dial nobody answered was still waiting after 5s, against a bound of 200ms")
	}
	// Back to the baseline, but for the handlers the silent server itself
	// parks on the connections it was handed.
	settleGoroutines(t, baseline+holding())
}

// TestEveryUpgradeRequestCarriesTheDialBound: the bound reaches the request
// the dialers make, whatever context it was built with.
func TestEveryUpgradeRequestCarriesTheDialBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var seen context.Context
	rt := boundUpgrade(50 * time.Millisecond)(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Context()
		return &http.Response{StatusCode: http.StatusSwitchingProtocols, Body: http.NoBody}, nil
	}))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.invalid/", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	deadline, ok := seen.Deadline()
	if !ok || time.Until(deadline) > 50*time.Millisecond {
		t.Fatalf("the upgrade request carried no bound of 50ms: deadline %v, set %v", deadline, ok)
	}
}

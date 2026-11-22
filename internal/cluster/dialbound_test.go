package cluster

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A cluster read is bounded by the console and not by the kernel.
//
// Against a context whose address drops packets, an unbounded pod list answers
// after 21 s, which is Windows giving up on the connect, and the overview after
// 1m45s, which is five of those in a row — while the readiness probe says "no
// answer within 10s" about the same address. The bound is
// dialTimeout in limits.go, applied once on the config every client of a
// cluster is built from; this file is what makes it a fact rather than a
// default a client rewrite can lose.
//
// A dropped-packet address is not something a test can stand up — a listener
// that never Accepts still completes the handshake in the kernel — so the dial
// under the client is the test's own, and it does what such an address does:
// nothing, until told to stop. The bound is then the only thing that can end
// the request, and the test's own clock is the measurement.

// blockedDial never connects. It returns when the context it was handed ends,
// with that context's error — which is what the real dialer does too, in its
// own words.
func blockedDial(ctx context.Context, _, _ string) (net.Conn, error) {
	<-ctx.Done()
	return nil, &net.OpError{Op: "dial", Net: "tcp", Err: ctx.Err()}
}

func TestAReadOnAClusterThatDoesNotAnswerTheConnectRefusesWithinTheBound(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")

	const bound = 150 * time.Millisecond
	// Far past the bound, so that whatever ends the read is the console's own
	// dial bound and not the caller's patience.
	const patience = 10 * time.Second

	server, _ := tlsServer(t)
	m, err := NewManager(oneClusterKubeconfig(t, "test", server.URL, certOf(t, server)))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	m.dial, m.dialBound = blockedDial, bound

	// Both clients of a cluster, because the streaming one is a copy of the
	// config made after the bound is set — and a copy is exactly where a bound
	// gets lost.
	for _, tc := range []struct {
		name string
		get  func(string) (*Client, error)
	}{
		{"the ordinary client", m.Client},
		{"the streaming client", m.StreamingClient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := tc.get("test")
			if err != nil {
				t.Fatalf("building the client: %v", err)
			}
			if client.Config.Dial == nil {
				t.Fatal("the config carries no dial of its own, so the connect is the kernel's to bound")
			}

			ctx, cancel := context.WithTimeout(context.Background(), patience)
			defer cancel()
			started := time.Now()
			_, err = client.Clients.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
			took := time.Since(started)

			if err == nil {
				t.Fatal("a list through a dial that never connects answered")
			}
			if took >= patience {
				t.Fatalf("the list took %s: the caller's context ended it, not the console's bound", took)
			}
			// Generous above the bound: the point is seconds against a minute,
			// not scheduler jitter.
			if took > bound+2*time.Second {
				t.Errorf("the list refused after %s, which is not within the %s bound", took, bound)
			}
			if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "deadline exceeded") {
				t.Errorf("the refusal does not say the connect ran out of time: %v", err)
			}
			t.Logf("%s refused after %s: %v", tc.name, took.Round(time.Millisecond), err)
		})
	}
}

// The bound is on the connect and on nothing after it.
//
// The other half of the previous test, and the reason rest.Config.Timeout was
// not the answer: that one is copied into http.Client.Timeout and cuts the body
// read too, which makes a followed log stream die at 30 s (see
// streaming_test.go). A dial bound must let a connection it opened live for as
// long as its caller reads from it.
func TestTheDialBoundDoesNotCutAStreamOnceConnected(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")

	const (
		bound = 100 * time.Millisecond
		beat  = 50 * time.Millisecond
		lines = 12 // 600 ms of writing, six times the bound
	)

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("the test server cannot stream")
			return
		}
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		for i := 0; i < lines; i++ {
			if _, err := io.WriteString(w, "line\n"); err != nil {
				return
			}
			flusher.Flush()
			select {
			case <-time.After(beat):
			case <-r.Context().Done():
				return
			}
		}
	}))
	server.StartTLS()
	t.Cleanup(server.Close)

	m, err := NewManager(oneClusterKubeconfig(t, "test", server.URL, certOf(t, server)))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	// The real dialer, under the short bound: the server is on the loopback and
	// the connect takes well under it. Anything the bound then cut would be a
	// cut on the exchange, which is the defect this test exists to refuse.
	m.dialBound = bound

	client, err := m.StreamingClient("test")
	if err != nil {
		t.Fatalf("StreamingClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	started := time.Now()
	body, err := client.Clients.CoreV1().RESTClient().Get().AbsPath("/stream").Stream(ctx)
	if err != nil {
		t.Fatalf("opening the stream: %v", err)
	}
	defer body.Close()
	n, err := io.Copy(io.Discard, body)
	took := time.Since(started)
	if err != nil {
		t.Fatalf("the stream ended in an error after %d bytes and %s: %v", n, took, err)
	}
	if want := int64(lines * len("line\n")); n != want {
		t.Fatalf("read %d bytes of %d: the stream was cut", n, want)
	}
	if took < 3*bound {
		t.Fatalf("the whole stream arrived in %s, so nothing here outlived the %s bound and nothing was proved", took, bound)
	}
	t.Logf("read %d bytes over %s through a dial bounded at %s", n, took.Round(time.Millisecond), bound)
}

// The figure itself, pinned: the probe's own ten seconds (internal/health), so
// that "this cluster does not answer" is one number wherever it is read. And
// the wire sees it — a dialer of the test's own is handed a deadline, and the
// deadline is that figure, on a Manager built the way the binary builds one.
func TestTheDefaultDialBoundIsTheProbesTenSeconds(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")

	if dialTimeout != 10*time.Second {
		t.Fatalf("dialTimeout is %s; the readiness probe says \"no answer within 10s\", and a page "+
			"refusing on a different clock from the tile beside it is the confusion this bound removes", dialTimeout)
	}

	server, _ := tlsServer(t)
	m, err := NewManager(oneClusterKubeconfig(t, "test", server.URL, certOf(t, server)))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	var (
		mu   sync.Mutex
		seen []time.Duration
	)
	real := (&net.Dialer{}).DialContext
	m.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		deadline, ok := ctx.Deadline()
		mu.Lock()
		if ok {
			seen = append(seen, time.Until(deadline))
		} else {
			seen = append(seen, -1)
		}
		mu.Unlock()
		return real(ctx, network, address)
	}

	client, err := m.Client("test")
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	if _, err := client.Clients.CoreV1().ConfigMaps("default").List(context.Background(), metav1.ListOptions{}); err != nil {
		t.Fatalf("the list through the recording dialer: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("the request reached the server without dialling through the Manager's dial")
	}
	for _, left := range seen {
		if left < 0 {
			t.Fatal("the dial was handed a context with no deadline")
		}
		if left > dialTimeout || left < dialTimeout-2*time.Second {
			t.Errorf("the dial was given %s, want about %s", left.Round(time.Millisecond), dialTimeout)
		}
	}
}

package cluster

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/client-go/rest"
)

// What this file exists for.
//
// rest.Config.Timeout is not "time to first byte". rest.HTTPClientFor copies it
// into http.Client.Timeout, which covers every read from the response body, so
// anything built from the ordinary config is cut off mid-stream at the timeout
// however much the server still has to send. A followed log stream is exactly
// that shape, and would die at 30 seconds.
//
// Written with a short timeout and a slow server rather than the real 30 s,
// because the mechanism is the subject and a test that takes half a minute to
// prove it is a test people stop running.
func TestATimeoutOnTheConfigCutsTheBodyReadAndTheStreamingCopyDoesNot(t *testing.T) {
	const (
		timeout = 200 * time.Millisecond
		beat    = 60 * time.Millisecond
		want    = 8 // 480 ms of writing, comfortably past the timeout
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("the test server cannot stream")
			return
		}
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		for i := 0; i < want; i++ {
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
	defer server.Close()

	bounded := &rest.Config{Host: server.URL, Timeout: timeout}

	// Read as much as arrives, and report what stopped it.
	read := func(cfg *rest.Config) (int, error) {
		client, err := rest.HTTPClientFor(cfg)
		if err != nil {
			t.Fatalf("HTTPClientFor: %v", err)
		}
		// A context far longer than the timeout, so that whatever ends the read
		// is the client and not the caller.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		n, err := io.Copy(io.Discard, resp.Body)
		return int(n), err
	}

	if n, err := read(bounded); err == nil {
		t.Errorf("a %s timeout read the whole %d-line stream (%d bytes); the premise of this fix is wrong",
			timeout, want, n)
	} else {
		t.Logf("the ordinary config stopped after %d bytes: %v", n, err)
	}

	streaming := StreamingConfig(bounded)
	n, err := read(streaming)
	if err != nil {
		t.Fatalf("the streaming config was cut off after %d bytes: %v", n, err)
	}
	if n != want*len("line\n") {
		t.Errorf("read %d bytes, want %d — the stream did not run to the end", n, want*len("line\n"))
	}
}

// StreamingConfig must copy. The config inside a *Client is shared by every
// caller holding that client, so clearing the timeout in place would unbound
// every list in the process — a far worse bug than a stream that times out, and
// an invisible one.
func TestStreamingConfigCopiesRatherThanClearingInPlace(t *testing.T) {
	original := &rest.Config{Host: "https://example.invalid", Timeout: requestTimeout, QPS: clientQPS}

	streaming := StreamingConfig(original)
	if streaming == original {
		t.Fatal("StreamingConfig returned the config it was given")
	}
	if original.Timeout != requestTimeout {
		t.Errorf("the original's timeout became %s; every list in the process is now unbounded", original.Timeout)
	}
	if streaming.Timeout != 0 {
		t.Errorf("the copy still has a %s timeout, which is the whole bug", streaming.Timeout)
	}
	// Everything else has to survive, or a streaming client would reach a
	// different cluster, or reach it without credentials.
	if streaming.Host != original.Host || streaming.QPS != original.QPS {
		t.Errorf("the copy lost more than the timeout: %+v", streaming)
	}
}

// The two clients a context can have, and the one difference between them. A
// cached client is keyed on which kind it is; sharing one cache entry would
// hand a follow the 30-second client and be invisible until a panel died.
func TestAContextHasABoundedClientAndAStreamingOne(t *testing.T) {
	m, err := NewManager(kubeconfigForTest(t))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	contexts := m.Contexts()
	if len(contexts) == 0 {
		t.Skip("the kubeconfig declares no contexts")
	}
	name := contexts[0]

	bounded, err := m.Client(name)
	if err != nil {
		t.Fatalf("Client(%q): %v", name, err)
	}
	if bounded.Config.Timeout != requestTimeout {
		t.Errorf("the ordinary client's timeout is %s, want %s — a list must stay bounded",
			bounded.Config.Timeout, requestTimeout)
	}

	streaming, err := m.StreamingClient(name)
	if err != nil {
		t.Fatalf("StreamingClient(%q): %v", name, err)
	}
	if streaming.Config.Timeout != 0 {
		t.Errorf("the streaming client's timeout is %s; a followed stream would die there",
			streaming.Config.Timeout)
	}
	if streaming == bounded {
		t.Fatal("StreamingClient returned the bounded client")
	}
	if streaming.Clients == bounded.Clients {
		t.Error("both share one clientset, so both share one http.Client and one timeout")
	}

	// And each is held, or every stream would re-run an EKS exec plugin.
	if again, err := m.StreamingClient(name); err != nil || again != streaming {
		t.Errorf("the streaming client was rebuilt on the second ask (%v)", err)
	}
	if again, err := m.Client(name); err != nil || again != bounded {
		t.Errorf("the bounded client was rebuilt on the second ask (%v)", err)
	}
}

func TestAnUnknownContextHasNoStreamingClientEither(t *testing.T) {
	m, err := NewManager(kubeconfigForTest(t))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := m.StreamingClient("no-such-cluster"); err == nil {
		t.Error("an unknown context produced a streaming client")
	}
}

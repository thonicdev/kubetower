package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
)

// The word "timeout" in a failure's address or in a cluster's name is not a
// timeout.
//
// stateOf's last word arm must not read `strings.Contains(lower, "timeout")`, and
// client-go puts the configured request timeout into every request URL -
// internal/cluster/manager.go sets `cfg.Timeout = 30s`, so `?timeout=30s` is in
// every address it dials. A refused connection quoting that address would come out
// `timeout`, and so would any failure naming a context called
// `prod-timeout-eks`. stateOf asks health.TimedOut, as internal/health's own
// classifier does.
//
// The refused connection is a real one: a GET to a port nothing listens on,
// so the sentence is net/http's own on whichever platform runs this, Winsock's
// wording included.
func TestTheWordTimeoutInAnAddressOrANameIsNotATimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("a port to close: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"http://"+address+"/api/v1/namespaces/default/pods?limit=500&timeout=30s", nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	response, refused := http.DefaultClient.Do(request)
	if response != nil {
		_ = response.Body.Close()
	}
	if refused == nil || !strings.Contains(refused.Error(), "timeout=30s") {
		t.Fatalf("the closed port answered %v; this case needs a refused connection quoting `?timeout=30s`", refused)
	}

	scope := api.Scope{Cluster: "prod-timeout-eks", Verb: "list"}
	for _, c := range []struct {
		name string
		err  error
		code api.ReasonCode
	}{
		// The two false positives, which must read as what they are.
		{"a refused connection whose address carries ?timeout=30s", refused, api.CodeClusterUnreachable},
		{"a failure naming a cluster called prod-timeout-eks",
			errors.New(`listing pods on "prod-timeout-eks": the cluster did not answer: EOF`), api.CodeClusterUnreachable},
		{"a lookup of a host named prod-timeout-eks",
			errors.New(`Get "https://prod-timeout-eks.example.com/api?timeout=30s": dial tcp: lookup prod-timeout-eks.example.com: no such host`),
			api.CodeClusterUnreachable},

		// And the real ones, which must stay timeouts.
		{"a read that ran out of time", errors.New(`Get "https://10.0.0.1:6443/api?timeout=30s": read tcp 10.0.0.2:51234->10.0.0.1:6443: i/o timeout`),
			api.CodeTimeout},
		{"a TLS handshake that ran out of time", errors.New(`Get "https://10.0.0.1:6443/version?timeout=30s": net/http: TLS handshake timeout`),
			api.CodeTimeout},
		{"a context whose deadline passed, wrapped", fmt.Errorf("listing pods: %w", context.DeadlineExceeded),
			api.CodeTimeout},
		{"a log stream's text for the same", errors.New("context deadline exceeded"), api.CodeTimeout},
		// A connect nobody answered: Winsock's 10060
		// by number - its sentence holds no phrase and its Timeout() is false -
		// and Linux's sentence once the type is lost.
		{"a connect Winsock timed out", &url.Error{Op: "Get", URL: "https://10.255.255.1:6443/api?timeout=30s",
			Err: &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connectex", syscall.Errno(10060))}},
			api.CodeTimeout},
		{"a connect Linux timed out, as text",
			errors.New(`Get "https://10.0.0.1:6443/api?timeout=30s": dial tcp 10.0.0.1:6443: connect: connection timed out`),
			api.CodeTimeout},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := stateOf(c.err, scope)
			if f == nil {
				t.Fatalf("stateOf returned nil for %v", c.err)
			}
			if f.State != api.StateUnreachable || f.Reason.Code != c.code {
				t.Errorf("classified as %s/%s, want %s/%s\n  from: %v", f.State, f.Reason.Code, api.StateUnreachable, c.code, c.err)
			}
			// Both are worth asking again.
			if f.Reason.Retryable == nil || !*f.Reason.Retryable {
				t.Errorf("%s is not retryable", c.name)
			}
		})
	}
}

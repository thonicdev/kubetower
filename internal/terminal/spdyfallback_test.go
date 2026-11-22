package terminal

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// refusingAPIServer refuses every request, recording the methods it was asked
// with: a WebSocket upgrade is a GET and a SPDY one a POST.
func refusingAPIServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()
		http.Error(w, "this proxy does not upgrade to WebSocket", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), methods...)
	}
}

func askedWithPost(methods []string) bool {
	for _, m := range methods {
		if m == http.MethodPost {
			return true
		}
	}
	return false
}

// TestAnExecFallsBackToSPDYWhenTheUpgradeIsRefused is the SPDY fallback for
// exec, through client-go's real executor: against a server that refuses the
// WebSocket upgrade, the stamped GET is followed by the SPDY POST. A check of
// apimachinery's UpgradeFailureError alone, which client-go v0.37 never
// returns, would never send the POST and the exec would fail.
func TestAnExecFallsBackToSPDYWhenTheUpgradeIsRefused(t *testing.T) {
	srv, methods := refusingAPIServer(t)
	target, err := url.Parse(srv.URL + "/api/v1/namespaces/default/pods/api/exec?command=sh&stdin=true&stdout=true&tty=true")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := newExecutor(&rest.Config{Host: srv.URL}, target)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: io.Discard, Tty: true})

	if got := methods(); len(got) == 0 || got[0] != http.MethodGet || !askedWithPost(got) {
		t.Errorf("the API server was asked %v: want the WebSocket GET and then the SPDY POST", got)
	}
}

// TestAPortForwardFallsBackToSPDYWhenTheUpgradeIsRefused is the same for the tunnel,
// through client-go's real dialers.
func TestAPortForwardFallsBackToSPDYWhenTheUpgradeIsRefused(t *testing.T) {
	srv, methods := refusingAPIServer(t)
	target, err := url.Parse(srv.URL + "/api/v1/namespaces/default/pods/api/portforward")
	if err != nil {
		t.Fatal(err)
	}
	dialer, err := newForwardDialer(context.Background(), &cluster.Client{Context: "prod", Config: &rest.Config{Host: srv.URL}}, target)
	if err != nil {
		t.Fatal(err)
	}
	if conn, _, err := dialer.Dial("portforward.k8s.io"); err == nil {
		_ = conn.Close()
	}

	if got := methods(); len(got) == 0 || got[0] != http.MethodGet || !askedWithPost(got) {
		t.Errorf("the API server was asked %v: want the WebSocket GET and then the SPDY POST", got)
	}
}

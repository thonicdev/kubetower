package launch

import (
	"net/http"
	"testing"
	"time"
)

// The server bounds what a client can hold without asking for anything: an
// idle keep-alive connection and the size of a request's headers. It sets no
// read or write timeout, which would cut log streams and terminals.
func TestTheServerBoundsIdleConnectionsAndHeadersButNotStreams(t *testing.T) {
	srv := newServer("127.0.0.1:0", http.NotFoundHandler())
	if srv.IdleTimeout <= 0 || srv.IdleTimeout > 5*time.Minute {
		t.Errorf("IdleTimeout = %v; want a bound of a few minutes", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes <= 0 || srv.MaxHeaderBytes >= http.DefaultMaxHeaderBytes {
		t.Errorf("MaxHeaderBytes = %d; want a bound below net/http's default of %d",
			srv.MaxHeaderBytes, http.DefaultMaxHeaderBytes)
	}
	if srv.ReadHeaderTimeout <= 0 {
		t.Errorf("ReadHeaderTimeout = %v; want one", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 0 || srv.WriteTimeout != 0 {
		t.Errorf("ReadTimeout = %v, WriteTimeout = %v; want neither, a stream would be cut",
			srv.ReadTimeout, srv.WriteTimeout)
	}
}

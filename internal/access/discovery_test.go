package access_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access"
)

// What the clients handed to an extension may send: discovery, and nothing
// one segment deeper.
func TestIsDiscovery(t *testing.T) {
	for _, c := range []struct {
		method, path string
		upgrade      bool
		want         bool
	}{
		{"GET", "/api", false, true},
		{"GET", "/api/", false, true},
		{"GET", "/apis", false, true},
		{"GET", "/api/v1", false, true},
		{"GET", "/apis/apps", false, true},
		{"GET", "/apis/apps/v1", false, true},
		{"HEAD", "/apis/apps/v1", false, true},
		{"GET", "/version", false, true},

		{"GET", "/api/v1/pods", false, false},
		{"GET", "/api/v1/namespaces", false, false},
		{"GET", "/api/v1/namespaces/shop/secrets/db", false, false},
		{"GET", "/apis/apps/v1/deployments", false, false},
		{"GET", "/apis/apps/v1/namespaces/shop/deployments", false, false},
		{"GET", "/api/v1/proxy/nodes/n1", false, false},
		{"GET", "/openapi/v2", false, false},
		{"GET", "/healthz", false, false},
		{"GET", "/", false, false},
		{"GET", "/apis/apps/v1/../../../api/v1/secrets", false, false},
		{"GET", "/apis/apps/v1%2Fdeployments", false, false},
		{"GET", "/apis//apps", false, false},
		{"GET", "/apis/apps/v1", true, false},
		{"POST", "/api/v1", false, false},
		{"DELETE", "/apis/apps/v1", false, false},
	} {
		header := http.Header{}
		if c.upgrade {
			header.Set("Connection", "Upgrade")
			header.Set("Upgrade", "websocket")
		}
		if got := access.IsDiscovery(c.method, header, c.path); got != c.want {
			t.Errorf("IsDiscovery(%s %s, upgrade=%t) = %t, want %t", c.method, c.path, c.upgrade, got, c.want)
		}
	}
}

type countingTransport struct{ sent []string }

func (c *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.sent = append(c.sent, req.Method+" "+req.URL.Path)
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}, nil
}

// Under a prefix (a proxy at /k8s/clusters/<id>), the path is read from the API
// server's own root, as Guard reads it.
func TestDiscoveryOnlyReadsThePathFromTheServersRoot(t *testing.T) {
	next := &countingTransport{}
	rt := access.DiscoveryOnly(&rest.Config{Host: "https://proxy.example/k8s/clusters/c-1"}, "prod")(next)
	ok, _ := http.NewRequest(http.MethodGet, "https://proxy.example/k8s/clusters/c-1/apis/apps/v1", nil)
	if _, err := rt.RoundTrip(ok); err != nil {
		t.Errorf("discovery under the prefix was refused: %v", err)
	}
	for _, path := range []string{"/k8s/clusters/c-1/api/v1/pods", "/apis/apps/v1", "/other/api"} {
		req, _ := http.NewRequest(http.MethodGet, "https://proxy.example"+path, nil)
		_, err := rt.RoundTrip(req)
		var refused *access.NotDiscovery
		if !errors.Is(err, access.ErrNotDiscovery) || !errors.As(err, &refused) {
			t.Errorf("%s answered %v, want a NotDiscovery refusal", path, err)
		}
	}
	if len(next.sent) != 1 {
		t.Errorf("sent %v, want the one discovery request", next.sent)
	}
}

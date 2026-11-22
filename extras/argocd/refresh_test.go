package argocd_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/extension"
	argocd "github.com/thonicdev/kubetower/extras/argocd"
)

// A refresh is a write: the route holds it to the confirmation word and hands
// it to Host.RefreshApplication, the console's reviewed door. It writes nothing
// itself.

type refreshHost struct {
	*stubHost
	err   error
	calls []string
}

func (h *refreshHost) ConfirmToken(*http.Request) string { return "confirm" }

func (h *refreshHost) RefreshApplication(_ *http.Request, cluster, namespace, name string) error {
	h.calls = append(h.calls, fmt.Sprintf("%s/%s/%s", cluster, namespace, name))
	return h.err
}

func postRefresh(t *testing.T, host extension.Host, query string) (string, int) {
	t.Helper()
	mux := http.NewServeMux()
	for _, route := range argocd.New().Routes(host) {
		mux.Handle(route.Method+" /x/{cluster}/"+argocd.Key+route.Pattern, route.Handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x/a-cluster/"+argocd.Key+"/applications/argocd/guestbook/refresh"+query, nil))
	return rec.Body.String(), rec.Code
}

func TestARefreshWithoutTheConfirmationWordWritesNothing(t *testing.T) {
	for _, query := range []string{"", "?confirm=", "?confirm=yes"} {
		host := &refreshHost{stubHost: &stubHost{}}
		body, status := postRefresh(t, host, query)
		if status != http.StatusBadRequest {
			t.Errorf("%q: status %d, body %s", query, status, body)
		}
		if len(host.calls) != 0 {
			t.Errorf("%q: the door was called without a confirmation: %v", query, host.calls)
		}
	}
}

func TestAConfirmedRefreshAsksTheDoorForThatApplicationOnly(t *testing.T) {
	host := &refreshHost{stubHost: &stubHost{}}
	body, status := postRefresh(t, host, "?confirm=confirm")
	if status != http.StatusOK {
		t.Fatalf("status %d, body %s", status, body)
	}
	if len(host.calls) != 1 || host.calls[0] != "a-cluster/argocd/guestbook" {
		t.Errorf("the door was asked %v", host.calls)
	}
}

func TestARefreshTheDoorRefusesIsSaidWithItsOwnStatus(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("%w: no", extension.ErrDenied), http.StatusForbidden, argocd.CodeRefreshDenied},
		{fmt.Errorf("%w: no", extension.ErrUnreviewable), http.StatusServiceUnavailable, argocd.CodeRefreshUnreviewed},
		{fmt.Errorf("%w: moved", extension.ErrChanged), http.StatusConflict, argocd.CodeRefreshChanged},
		{fmt.Errorf("%w: x", extension.ErrUnknownCluster), http.StatusNotFound, argocd.CodeClusterUnknown},
		{errors.New("boom"), http.StatusBadGateway, argocd.CodeRefreshFailed},
	} {
		body, status := postRefresh(t, &refreshHost{stubHost: &stubHost{}, err: c.err}, "?confirm=confirm")
		if status != c.status || !strings.Contains(body, c.code) {
			t.Errorf("%v: status %d, body %s", c.err, status, body)
		}
	}
}

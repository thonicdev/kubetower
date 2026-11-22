package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/identity"
)

// Under the server profile every request the core hands an extension carries
// the shared-console mark, so an extension can confine what it looks for to
// what the deployment names; on the desktop none does.
func TestAnExtensionIsToldItServesASharedConsoleUnderTheServerProfileOnly(t *testing.T) {
	s, _, _ := turnedOn(t, []string{fixtureKey}, fixtureKey)
	var seen, reached bool
	mux := http.NewServeMux()
	mux.Handle("GET /x/{cluster}/"+fixtureKey+"/probe", s.gateExtension(fixtureKey, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		reached, seen = true, extension.SharedConsole(r)
	})))
	request := httptest.NewRequest(http.MethodGet, "/x/prod/"+fixtureKey+"/probe", nil)
	request = request.WithContext(identity.With(request.Context(), alice))
	mux.ServeHTTP(httptest.NewRecorder(), request)
	if !reached || !seen {
		t.Errorf("under the server profile the route was reached %v and told it is shared %v", reached, seen)
	}
	if !extension.SharedConsole(s.forExtension(httptest.NewRequest(http.MethodGet, "/", nil))) {
		t.Error("a suggestion asked under the server profile is not marked shared")
	}

	desktop := serverFor(t, false)
	if extension.SharedConsole(desktop.forExtension(httptest.NewRequest(http.MethodGet, "/", nil))) {
		t.Error("the desktop marks a request as served by a shared console")
	}
}

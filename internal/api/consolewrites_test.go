package api

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/identity"
)

// theConsoleWideWrites change what every person of the console sees: the
// archive's settings, and emptying it. The switch of an extra on a cluster and
// its settings are covered by TestTheExtensionWritesFollowTheProfile, which
// registers them under the server profile behind the use gate and the audit
// record.
var theConsoleWideWrites = []struct{ method, path string }{
	{http.MethodPut, "/api/settings"},
	{http.MethodPost, "/api/settings/clear"},
}

// TestTheServerProfileRegistersNoConsoleWideWrite is the preference-write rule
// one level over: any signed-in person could change what the archive keeps and
// empty it - "the only copy" - with no review and no line naming them. Absent
// under the server profile, one replica or several; the desktop keeps both,
// which is the control.
func TestTheServerProfileRegistersNoConsoleWideWrite(t *testing.T) {
	registered := func(a *API, method, path string) bool {
		_, pattern := a.mux.Handler(httptest.NewRequest(method, path, nil))
		return pattern != ""
	}
	server, desktop := apiForProfile(true), apiForProfile(false)
	for _, write := range theConsoleWideWrites {
		if registered(server, write.method, write.path) {
			t.Errorf("%s %s is registered under the server profile, where what it writes is everybody's",
				write.method, write.path)
		}
		if !registered(desktop, write.method, write.path) {
			t.Errorf("%s %s is not registered on the desktop, which loses nothing to the rule",
				write.method, write.path)
		}
	}
	for name, a := range map[string]*API{"server profile": server, "desktop": desktop} {
		if !registered(a, http.MethodGet, "/api/settings") {
			t.Errorf("GET /api/settings is not registered on the %s; the read stays", name)
		}
	}
}

// TestAConsoleWideWriteNamesWhoMadeIt is the other half: where the writes
// remain, each leaves a line naming who made it.
func TestAConsoleWideWriteNamesWhoMadeIt(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })

	a := apiForProfile(false)
	request := httptest.NewRequest(http.MethodPut, "/api/c/prod/extensions/argocd", strings.NewReader(`{"enabled":false}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-KubeTower-Request", "1")
	request = request.WithContext(identity.With(request.Context(), identity.Who{Name: "alice@example.com", Method: identity.SSO}))
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, request)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("the switch answered %d: %s", rec.Code, rec.Body.String())
	}
	line := buf.String()
	if !strings.Contains(line, `console write: extension "argocd" on "prod" set enabled=false`) ||
		!strings.Contains(line, `who="alice@example.com" via=sso`) {
		t.Errorf("the switch left no line naming who made it: %q", line)
	}
}

// A setting is written with exactly one of value and text,
// and a text's value is never in the line the write leaves.
func TestASettingIsWrittenWithExactlyOneOfValueAndText(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })

	a := apiForProfile(false)
	put := func(body string) int {
		request := httptest.NewRequest(http.MethodPut, "/api/c/prod/extensions/kubecron/settings/collector", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-KubeTower-Request", "1")
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, request)
		return rec.Code
	}
	for body, want := range map[string]int{
		`{"value":true}`:                     http.StatusNoContent,
		`{"text":"ops/a-very-private-name"}`: http.StatusNoContent,
		`{"text":""}`:                        http.StatusNoContent,
		`{}`:                                 http.StatusBadRequest,
		`{"value":true,"text":"ops/cron"}`:   http.StatusBadRequest,
	} {
		if got := put(body); got != want {
			t.Errorf("%.40s: %d, want %d", body, got, want)
		}
	}
	if strings.Contains(buf.String(), "a-very-private-name") {
		t.Errorf("a text's value is in the log: %q", buf.String())
	}
	if !strings.Contains(buf.String(), `extension "kubecron" on "prod" set text collector`) {
		t.Errorf("the text write left no line: %q", buf.String())
	}
}

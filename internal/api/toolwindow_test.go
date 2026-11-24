package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recordingToolWindows answers with err and records what it was asked.
type recordingToolWindows struct {
	err   error
	calls *[]string
}

func (recordingToolWindows) Available() bool { return true }

func (r recordingToolWindows) OpenToolWindow(id, tool, path string, closeTunnel bool) error {
	*r.calls = append(*r.calls, fmt.Sprintf("%s %s %s %t", id, tool, path, closeTunnel))
	return r.err
}

func toolWindowAPI(tools ToolWindows, inCluster, replicated bool) *API {
	return New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		ToolWindows: tools, InCluster: inCluster, Replicated: replicated,
		AllowOrigin: func(*http.Request, string) bool { return true },
	})
}

// The route exists where the forwards do and a native window can be drawn.
func TestTheToolWindowRouteIsRegisteredOnlyOnTheDesktop(t *testing.T) {
	for _, c := range []struct {
		name                  string
		tools                 ToolWindows
		inCluster, replicated bool
		want                  bool
	}{
		{"desktop", fakeToolWindows{}, false, false, true},
		{"server profile", fakeToolWindows{}, true, false, false},
		{"several replicas", fakeToolWindows{}, false, true, false},
		{"no native window", nil, false, false, false},
	} {
		a := toolWindowAPI(c.tools, c.inCluster, c.replicated)
		_, pattern := a.mux.Handler(httptest.NewRequest(http.MethodPost, "/api/forwards/fwd-1/window", nil))
		if got := pattern == "POST /api/forwards/{id}/window"; got != c.want {
			t.Errorf("%s: registered = %v (pattern %q), want %v", c.name, got, pattern, c.want)
		}
	}
}

func TestAToolWindowRequestIsCheckedAndItsRefusalSaid(t *testing.T) {
	send := func(a *API, marked bool, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if marked {
			req.Header.Set(requestMarker, "1")
		}
		rec := httptest.NewRecorder()
		a.mux.ServeHTTP(rec, req)
		return rec
	}

	var calls []string
	a := toolWindowAPI(recordingToolWindows{calls: &calls}, false, false)
	if rec := send(a, false, "/api/forwards/fwd-1/window", `{"tool":"hubble"}`); rec.Code != http.StatusForbidden {
		t.Errorf("without %s: %d, want 403", requestMarker, rec.Code)
	}
	for _, body := range []string{
		`{}`,
		`{"tool":"grafana"}`,
		`{"tool":"hubble","path":"/` + strings.Repeat("a", 1024) + `"}`,
		`{"tool":"hubble","url":"http://127.0.0.1:8091/"}`,
	} {
		if rec := send(a, true, "/api/forwards/fwd-1/window", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, rec.Code)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("refused requests reached the windows: %v", calls)
	}

	rec := send(a, true, "/api/forwards/fwd-1/window", `{"tool":"whisker","path":"/flows","closeWithWindow":true}`)
	if rec.Code != http.StatusNoContent || len(calls) != 1 || calls[0] != "fwd-1 whisker /flows true" {
		t.Fatalf("a good request: %d, calls %v", rec.Code, calls)
	}

	for _, c := range []struct {
		err  error
		want int
	}{
		{fmt.Errorf("%w: not in the desktop window", ErrConflict), http.StatusConflict},
		{fmt.Errorf("%w: no open port-forward", ErrNotFound), http.StatusNotFound},
		{fmt.Errorf("%w: only a loopback address", ErrInvalid), http.StatusBadRequest},
	} {
		refusing := toolWindowAPI(recordingToolWindows{err: c.err, calls: &calls}, false, false)
		if rec := send(refusing, true, "/api/forwards/fwd-1/window", `{"tool":"hubble"}`); rec.Code != c.want {
			t.Errorf("%v: %d, want %d", c.err, rec.Code, c.want)
		}
	}
}

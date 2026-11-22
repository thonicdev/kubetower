package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// countingUpdates counts how often Check now reached the checker.
type countingUpdates struct{ calls *int }

func (c countingUpdates) CheckUpdates(context.Context) SettingsUpdate {
	*c.calls++
	return SettingsUpdate{State: "current", Latest: "0.8.0", Note: "This is the latest release."}
}

func updateCheckAPI(updates UpdateCheck, inCluster bool) *API {
	return New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		Updates: updates, InCluster: inCluster,
		AllowOrigin: func(*http.Request, string) bool { return true },
	})
}

// Check now exists on the desktop, where something checks,
// and nowhere else: not under the server profile, which never checks, and not
// where no checker was given.
func TestCheckNowIsRegisteredOnlyWhereSomethingChecks(t *testing.T) {
	calls := 0
	for _, c := range []struct {
		name      string
		updates   UpdateCheck
		inCluster bool
		want      bool
	}{
		{"desktop", countingUpdates{&calls}, false, true},
		{"server profile", countingUpdates{&calls}, true, false},
		{"no checker", nil, false, false},
	} {
		a := updateCheckAPI(c.updates, c.inCluster)
		_, pattern := a.mux.Handler(httptest.NewRequest(http.MethodPost, "/api/settings/update-check", nil))
		if got := pattern != ""; got != c.want {
			t.Errorf("%s: POST /api/settings/update-check registered = %v, want %v", c.name, got, c.want)
		}
	}
}

// It makes a request on the operator's behalf, so it is a mutation behind the
// guard: without the request marker it is refused before the checker is asked,
// and with it the settled answer comes back as the contract's SettingsUpdate.
func TestCheckNowIsGuardedAndAnswersWithTheSettledCheck(t *testing.T) {
	calls := 0
	a := updateCheckAPI(countingUpdates{&calls}, false)

	bare := httptest.NewRecorder()
	a.mux.ServeHTTP(bare, httptest.NewRequest(http.MethodPost, "/api/settings/update-check", nil))
	if bare.Code != http.StatusForbidden {
		t.Errorf("without %s: status %d, want 403", requestMarker, bare.Code)
	}
	if calls != 0 {
		t.Fatalf("the checker was asked %d times by a refused request", calls)
	}

	marked := httptest.NewRequest(http.MethodPost, "/api/settings/update-check", nil)
	marked.Header.Set(requestMarker, "1")
	answer := httptest.NewRecorder()
	a.mux.ServeHTTP(answer, marked)
	if answer.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", answer.Code, answer.Body)
	}
	if calls != 1 {
		t.Errorf("the checker was asked %d times, want 1", calls)
	}
	var got SettingsUpdate
	if err := json.Unmarshal(answer.Body.Bytes(), &got); err != nil {
		t.Fatalf("the answer is not a SettingsUpdate: %v", err)
	}
	if got.State != "current" || got.Latest != "0.8.0" {
		t.Errorf("answer %+v, want the checker's current 0.8.0", got)
	}
}

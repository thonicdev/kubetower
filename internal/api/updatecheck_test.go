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

// The download and install routes exist on the desktop where an installer was
// built, and nowhere else: not under the server profile, never where nothing
// installs (a container, a test build).
func TestTheInstallRoutesAreRegisteredOnlyWhereSomethingInstalls(t *testing.T) {
	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/settings/update"},
		{http.MethodPut, "/api/settings/update"},
		{http.MethodPost, "/api/settings/update/download"},
		{http.MethodPost, "/api/settings/update/install"},
	}
	for _, c := range []struct {
		name      string
		installer UpdateInstaller
		inCluster bool
		want      bool
	}{
		{"desktop", fakeInstaller{}, false, true},
		{"server profile", fakeInstaller{}, true, false},
		{"no installer", nil, false, false},
	} {
		a := New(Deps{
			Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
			Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
			Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
			Installer: c.installer, InCluster: c.inCluster,
			AllowOrigin: func(*http.Request, string) bool { return true },
		})
		for _, r := range routes {
			_, pattern := a.mux.Handler(httptest.NewRequest(r.method, r.path, nil))
			if got := pattern != ""; got != c.want {
				t.Errorf("%s: %s %s registered = %v, want %v", c.name, r.method, r.path, got, c.want)
			}
		}
	}
}

// The three writes are mutations behind the guard: a request without the
// marker reaches nothing.
type countingInstaller struct {
	fakeInstaller
	calls *int
}

func (c countingInstaller) DownloadUpdate() (UpdateInstall, error) {
	*c.calls++
	return c.fakeInstaller.DownloadUpdate()
}

func (c countingInstaller) InstallUpdate(ctx context.Context) (UpdateInstall, error) {
	*c.calls++
	return c.fakeInstaller.InstallUpdate(ctx)
}

func TestTheInstallWritesAreGuarded(t *testing.T) {
	calls := 0
	a := New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		Installer:   countingInstaller{calls: &calls},
		AllowOrigin: func(*http.Request, string) bool { return true },
	})
	for _, path := range []string{"/api/settings/update/download", "/api/settings/update/install"} {
		bare := httptest.NewRecorder()
		a.mux.ServeHTTP(bare, httptest.NewRequest(http.MethodPost, path, nil))
		if bare.Code != http.StatusForbidden {
			t.Errorf("%s without %s: status %d, want 403", path, requestMarker, bare.Code)
		}
	}
	if calls != 0 {
		t.Fatalf("the installer was reached %d times by refused requests", calls)
	}
}

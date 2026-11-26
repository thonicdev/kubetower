package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func metricsSourceMux(deps Deps) *http.ServeMux {
	mux := http.NewServeMux()
	deps.Sessions, deps.Profile, deps.Clusters = fakeSessions{}, fakeProfile{}, fakeClusters{}
	deps.MetricsSources = fakeMetricsSources{}
	deps.AllowOrigin = func(*http.Request, string) bool { return true }
	New(deps).Mount(mux)
	return mux
}

func putSource(mux *http.ServeMux, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, "/api/c/prod/metrics/source", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-KubeTower-Request", "1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The server profile reads its source and cannot set one: the deployment's
// KT_PROMETHEUS_SERVICE decides there, so the write is not registered.
func TestTheMetricsSourceIsReadOnlyWhereSettingsAreNotKept(t *testing.T) {
	for name, deps := range map[string]Deps{
		"server profile":  {InCluster: true},
		"replicated":      {Replicated: true},
		"ephemeral state": {StateEphemeral: true},
	} {
		mux := metricsSourceMux(deps)
		get := httptest.NewRecorder()
		mux.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/c/prod/metrics/source", nil))
		if get.Code != http.StatusOK {
			t.Errorf("%s: GET answered %d", name, get.Code)
		}
		if rec := putSource(mux, `{"source":"vm/vmsingle-main:8428"}`); rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
			t.Errorf("%s: PUT answered %d, want it unregistered", name, rec.Code)
		}
	}
}

func TestAMetricsSourceThatIsNotATargetIsABadRequest(t *testing.T) {
	rec := putSource(metricsSourceMux(Deps{}), `{"source":"not a target"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "namespace/service:port") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestNoMetricsLayerRegistersNoSourceRoute(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		AllowOrigin: func(*http.Request, string) bool { return true }}).Mount(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/c/prod/metrics/source", nil))
	if rec.Code == http.StatusOK {
		t.Fatalf("answered %d: %s", rec.Code, rec.Body)
	}
}

// resolvingClusters is a cluster layer that knows `a--b` names the context
// `a/b`.
type resolvingClusters struct{ fakeClusters }

func (resolvingClusters) Resolve(name string) (string, bool) {
	if name == "a--b" {
		return "a/b", true
	}
	return name, true
}

// The audit line of a metrics-source save names the context it changed, not
// the DNS-friendly form the URL carried (#156).
func TestTheSaveIsAuditedUnderTheResolvedContext(t *testing.T) {
	a := &API{deps: Deps{Clusters: resolvingClusters{}}}
	if got := a.resolvedName("a--b"); got != "a/b" {
		t.Errorf("resolved %q", got)
	}
	// A layer that cannot resolve leaves the name as given.
	plain := &API{deps: Deps{Clusters: fakeClusters{}}}
	if got := plain.resolvedName("a--b"); got != "a--b" {
		t.Errorf("unresolved %q", got)
	}
}

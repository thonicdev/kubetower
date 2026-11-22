package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A forward names a pod or a service, and exactly one of them.
//
// # Why the pair is refused rather than resolved
//
// `internal/forward.Request` carries both - "Service
// and Pod are alternatives; exactly one must be set" - and this operation
// accepts both, so the Ports panel on a Service has a
// control it can use.
//
// Accepting both arms means the two can both arrive, and a request
// naming both would leave the target to whichever field the layer below read
// first: a tunnel to something nobody chose, reported as a success. Refusing it
// at the door is one rule in one place.
func TestAForwardNamesAPodOrAServiceAndNotBoth(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	post := func(body string) int {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/forwards", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-KubeTower-Request", "1")
		mux.ServeHTTP(recorder, request)
		return recorder.Code
	}

	for _, one := range []struct {
		name, body string
		want       int
	}{{
		name: "a pod",
		body: `{"cluster":"prod","namespace":"argo","pod":"api-0","remotePort":8080}`,
		want: http.StatusCreated,
	}, {
		name: "a service",
		body: `{"cluster":"prod","namespace":"argo","service":"api","remotePort":443}`,
		want: http.StatusCreated,
	}, {
		name: "both",
		body: `{"cluster":"prod","namespace":"argo","pod":"api-0","service":"api","remotePort":8080}`,
		want: http.StatusBadRequest,
	}, {
		name: "neither",
		body: `{"cluster":"prod","namespace":"argo","remotePort":8080}`,
		want: http.StatusBadRequest,
	}} {
		t.Run(one.name, func(t *testing.T) {
			if got := post(one.body); got != one.want {
				t.Errorf("answered %d, want %d", got, one.want)
			}
		})
	}
}

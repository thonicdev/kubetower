package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/health"
)

// The server profile writes its audit records to stdout and says so at start; the
// desktop keeps none, because its stdout under `-H windowsgui` fails every
// write and a failed record refuses the act.
func TestOnlyTheServerProfileKeepsAnAuditLog(t *testing.T) {
	var said []string
	logf := func(format string, args ...any) { said = append(said, fmt.Sprintf(format, args...)) }

	if auditLog(false, logf) != nil || len(said) != 0 {
		t.Errorf("the desktop keeps an audit log, or talks about one: %v", said)
	}
	if auditLog(true, logf) == nil {
		t.Fatal("the server profile keeps no audit log, so its gate refuses every act")
	}
	if len(said) != 1 || !strings.Contains(said[0], "stdout") || !strings.Contains(said[0], "refused") {
		t.Errorf("the server profile did not say where its records go: %v", said)
	}
}

// The wiring, not only the log: the process's gate is handed it under the
// server profile - without it, that gate refuses every act - and none on the
// desktop.
func TestTheProcessGateWritesTheLogUnderTheServerProfile(t *testing.T) {
	quiet := func(string, ...any) {}
	if !processGate(nil, true, quiet).Audited() {
		t.Error("the server profile's gate keeps no audit log")
	}
	if processGate(nil, false, quiet).Audited() {
		t.Error("the desktop's gate keeps an audit log")
	}
}

// clustersFine is a health probe that has reached every cluster.
type clustersFine struct{}

func (clustersFine) Readiness(context.Context) health.Readiness {
	return health.Readiness{OK: true, Reason: "1 of 1 contexts reachable"}
}

type brokenSink struct{}

func (brokenSink) Write([]byte) (int, error) { return 0, errors.New("write /dev/stdout: broken pipe") }

// A replica whose audit log broke is
// not ready - on both probe paths - and one whose log writes stays ready.
// Liveness is not this probe and is not touched.
func TestABrokenAuditLogMakesTheReplicaNotReady(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sink  io.Writer
		ready bool
	}{
		{"a sink that writes", io.Discard, true},
		{"a sink that fails", brokenSink{}, false},
	} {
		gate := access.New(access.Options{Reviews: (&accesstest.Reviews{}).For, InCluster: true, Audit: audit.New(tc.sink)})
		_, _ = gate.Perform(context.Background(), "prod",
			access.Attributes{Verb: "delete", Version: "v1", Resource: "pods", Namespace: "shop", Name: "api-1"},
			func(context.Context) error { return nil })

		probe := newReadiness(clustersFine{}, gate, false)
		ok, reason := probe.Ready(context.Background())
		if ok != tc.ready {
			t.Errorf("%s: ready=%t (%s), want %t", tc.name, ok, reason, tc.ready)
		}
		if !tc.ready && !strings.Contains(reason, "audit log stopped writing") {
			t.Errorf("%s: the reason does not say why: %q", tc.name, reason)
		}

		rec := httptest.NewRecorder()
		healthz(probe)(rec, httptest.NewRequest(http.MethodGet, "/kt-healthz", nil))
		if (rec.Code == http.StatusOK) != tc.ready {
			t.Errorf("%s: /kt-healthz answered %d %q", tc.name, rec.Code, rec.Body.String())
		}
	}
}

// clustersDown is a health probe whose one context did not answer.
type clustersDown struct{}

func (clustersDown) Readiness(context.Context) health.Readiness {
	return health.Readiness{Reason: "none of the 1 contexts probed answered", Total: 1, Probed: 1}
}

// switchable is a health probe a test turns off and on.
type switchable struct{ up *bool }

func (p switchable) Readiness(context.Context) health.Readiness {
	if *p.up {
		return health.Readiness{OK: true, Reason: "1 of 1 contexts answered"}
	}
	return health.Readiness{Reason: "none of the 1 contexts probed answered", Total: 1, Probed: 1}
}

// TestTheServerProfileStaysReadyThroughAnAPIServerBlip. Under --incluster the
// one context is the API server every replica shares, so counting it at once
// would empty the Service on every replica together. There a failure is
// tolerated for apiServerGrace and then answered 503 (it may be this replica's
// own), and ready again once the API server answers; the desktop is unready at
// once; and a broken audit log is 503 at once under the server profile.
func TestTheServerProfileStaysReadyThroughAnAPIServerBlip(t *testing.T) {
	writes := access.New(access.Options{Reviews: (&accesstest.Reviews{}).For, InCluster: true, Audit: audit.New(io.Discard)})
	up := false
	clock := time.Now()
	probe := newReadiness(switchable{&up}, writes, true)
	probe.now = func() time.Time { return clock }

	if ok, reason := probe.Ready(context.Background()); !ok || !strings.Contains(reason, "none of the 1 contexts probed answered") {
		t.Errorf("server profile, API server just down: ready=%t %q, want ready with the reason kept", ok, reason)
	}
	clock = clock.Add(apiServerGrace - time.Second)
	if ok, _ := probe.Ready(context.Background()); !ok {
		t.Error("server profile, inside the grace: not ready")
	}
	clock = clock.Add(2 * time.Second)
	if ok, reason := probe.Ready(context.Background()); ok || !strings.Contains(reason, "longer than") {
		t.Errorf("server profile, past the grace: ready=%t %q, want not ready", ok, reason)
	}
	up = true
	if ok, _ := probe.Ready(context.Background()); !ok {
		t.Error("server profile, the API server answers again: not ready")
	}
	up = false
	if ok, _ := probe.Ready(context.Background()); !ok {
		t.Error("server profile, a new failure after recovery: not given a fresh grace")
	}

	if ok, _ := newReadiness(switchable{new(bool)}, writes, false).Ready(context.Background()); ok {
		t.Error("desktop, no context answered: ready, want not ready")
	}

	broken := access.New(access.Options{Reviews: (&accesstest.Reviews{}).For, InCluster: true, Audit: audit.New(brokenSink{})})
	_, _ = broken.Perform(context.Background(), "prod",
		access.Attributes{Verb: "delete", Version: "v1", Resource: "pods", Namespace: "shop", Name: "api-1"},
		func(context.Context) error { return nil })
	brokenProbe := newReadiness(switchable{new(bool)}, broken, true)
	if ok, reason := brokenProbe.Ready(context.Background()); ok || !strings.Contains(reason, "audit log stopped writing") {
		t.Errorf("server profile, audit log broken: ready=%t %q, want not ready at once", ok, reason)
	}
	rec := httptest.NewRecorder()
	healthz(brokenProbe)(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("server profile, audit log broken: the probe answered %d", rec.Code)
	}
}

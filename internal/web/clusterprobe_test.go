package web

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/health"
)

// An unreachable cluster's overview is one warning
// and a Refresh that works. The Refresh re-probes this cluster only, and a
// verdict the probe reached moments ago stands in for the overview read rather
// than dialling a cluster already known to be down.

// recordingProbe is the probe as the console holds it: rows already known, and
// a record of every cluster it was asked to check.
type recordingProbe struct {
	probeFixture
	mu     sync.Mutex
	asked  []string
	forced []bool
	answer health.Cluster
}

func (p *recordingProbe) Check(_ context.Context, name string, force bool) health.Cluster {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, name)
	p.forced = append(p.forced, force)
	answer := p.answer
	answer.Name = name
	return answer
}

func (p *recordingProbe) Known() health.Snapshot {
	return health.Snapshot{Rows: p.rows, Total: len(p.rows)}
}

const timedOut = "Get \"https://prod.example:6443/version\": context deadline exceeded"

func TestProbingOneClusterForcesThatClusterAndAnswersItsVerdict(t *testing.T) {
	server := navFixture(t, "prod")
	checked := time.Now().Add(-time.Second)
	probe := &recordingProbe{answer: health.Cluster{State: health.StateTimeout, Detail: timedOut, CheckedAt: checked}}
	server.health = probe

	entry, fail := apiClusters{server}.Probe(context.Background(), "prod")
	if fail != nil {
		t.Fatalf("the probe failed as a request: %+v", fail.Reason)
	}

	if len(probe.asked) != 1 || probe.asked[0] != "prod" || !probe.forced[0] {
		t.Fatalf("asked %v with force %v; want prod alone, forced - Refresh is this cluster and no other, and never the cache",
			probe.asked, probe.forced)
	}
	if entry.Name != "prod" || entry.State != api.StateUnreachable {
		t.Errorf("the entry is %s in state %q; want prod, unreachable - the verdict the probe just reached", entry.Name, entry.State)
	}
	if entry.Reason == nil || entry.Reason.Code != api.CodeTimeout || entry.Reason.Message != timedOut {
		t.Errorf("the reason is %+v; want the probe's own timeout, in its words", entry.Reason)
	}
	if entry.CheckedAt == nil || !entry.CheckedAt.Equal(checked.UTC()) {
		t.Errorf("checkedAt is %v; want when the probe established it, %v", entry.CheckedAt, checked.UTC())
	}

	// And a cluster this console does not know is not probed at all.
	if _, fail := (apiClusters{server}).Probe(context.Background(), "nowhere"); fail == nil || fail.State != api.StateAbsent {
		t.Errorf("probing an unknown cluster answered %+v; want absent", fail)
	}
	if len(probe.asked) != 1 {
		t.Errorf("an unknown cluster was probed: %v", probe.asked)
	}
}

func TestAFreshRedVerdictShortCircuitsTheOverviewRead(t *testing.T) {
	server := navFixture(t, "prod")
	server.health = &recordingProbe{probeFixture: probeFixture{rows: []health.Cluster{
		{Name: "prod", State: health.StateTimeout, Detail: timedOut, CheckedAt: time.Now().Add(-5 * time.Second)},
	}}}

	started := time.Now()
	_, fail := apiReads{server}.Overview(context.Background(), "prod")
	took := time.Since(started)

	if fail == nil {
		t.Fatal("the overview of a cluster the probe found unreachable five seconds ago answered")
	}
	if fail.State != api.StateUnreachable || fail.Reason.Code != api.CodeTimeout || fail.Reason.Message != timedOut {
		t.Errorf("the overview answered %q %+v; want the probe's own verdict, word for word", fail.State, fail.Reason)
	}
	// Nothing was dialled: prod.example does not resolve, and a read would have
	// answered with a name that did not resolve, after asking DNS.
	if took > time.Second {
		t.Errorf("the overview took %s; a fresh verdict is answered without dialling", took)
	}
}

func TestOnlyAFreshRedVerdictStandsInForARead(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		row  health.Cluster
		want bool
	}{
		{"unreachable, 5s old", health.Cluster{Name: "prod", State: health.StateTimeout, Detail: timedOut, CheckedAt: now.Add(-5 * time.Second)}, true},
		{"unreachable, 29s old", health.Cluster{Name: "prod", State: health.StateRefused, Detail: "connection refused", CheckedAt: now.Add(-29 * time.Second)}, true},
		{"unreachable, 30s old", health.Cluster{Name: "prod", State: health.StateTimeout, Detail: timedOut, CheckedAt: now.Add(-30 * time.Second)}, false},
		{"answering, 1s old", health.Cluster{Name: "prod", State: health.StateOK, CheckedAt: now.Add(-time.Second)}, false},
		{"an expired session, 1s old", health.Cluster{Name: "prod", State: health.StateSSO, Detail: "aws exited 255", CheckedAt: now.Add(-time.Second)}, false},
		{"never checked", health.Cluster{Name: "prod", State: health.StateTimeout, Detail: timedOut}, false},
	} {
		server := &Server{health: &recordingProbe{probeFixture: probeFixture{rows: []health.Cluster{tc.row}}}}
		if got := freshRefusal(server, "prod", now) != nil; got != tc.want {
			t.Errorf("%s: stands in for a read = %v, want %v", tc.name, got, tc.want)
		}
	}
	if freshRefusal(&Server{}, "prod", now) != nil {
		t.Error("a build with no probe answered from a verdict nobody reached")
	}
}

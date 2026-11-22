package web

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/overview"
)

// A person refused the node list: the capacity says so, the two other panels
// read from the machines carry the same failure (a client draws it once), and
// everything else on the page - the version, the warnings, the workloads - is
// drawn as it was read.
func TestADashboardForAPersonRefusedTheNodesRefusesOnlyTheMachines(t *testing.T) {
	refusal := &access.Refused{Cluster: "test",
		Attributes: access.Attributes{Verb: "list", Version: "v1", Resource: "nodes"},
		Answer:     access.Answer{Decision: access.Denied}}
	machines := overview.ClusterOverview{
		Cluster:      "test",
		Collected:    time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC),
		Version:      "v1.31.4",
		NodesRefused: refusal,
		Events: []overview.EventLine{{Namespace: "default", Object: "Pod/api-0", Kind: "Pod", Name: "api-0",
			Reason: "BackOff", Message: "Back-off restarting failed container", Class: overview.ClassWarn}},
	}
	workloads := workloadsFixture()
	out := overviewOf("test", machines, overview.Top{}, overview.Top{},
		overview.Availability{Detail: "metrics-server did not answer"}, false)
	overviewPanels(&out, "test", machines, &workloads, nil, nil)

	if out.Capacity.State() != api.StateForbidden || out.Capacity.Reason() == nil {
		t.Fatalf("capacity = %v, want the refusal of the node list", out.Capacity.State())
	}
	said := *out.Capacity.Reason()
	// The scope is what a client keys "said once" on: the node list's.
	if said.Scope == nil || said.Scope.Resource != "nodes" {
		t.Errorf("the refusal's scope is %+v, want the node list", said.Scope)
	}
	if out.Alerts.State() != api.StateForbidden {
		t.Errorf("alerts = %v, want the same refusal", out.Alerts.State())
	}
	if r := out.Alerts.Reason(); r == nil || r.Message != said.Message || r.Code != said.Code {
		t.Errorf("alerts carry %+v, want the capacity's own %+v, so it is said once", r, said)
	}
	if r := out.History.Reason(); out.History.State() != api.StateForbidden || r == nil || r.Message != said.Message {
		t.Errorf("history = %v %+v, want the capacity's own refusal", out.History.State(), r)
	}
	if out.NodesSummary != nil {
		t.Errorf("a node sentence %q was drawn from a list that was refused", out.NodesSummary.Text)
	}
	if out.Version != "v1.31.4" {
		t.Errorf("version = %q, want it read without the nodes", out.Version)
	}
	if out.Events.Len() != 1 || out.Counts.Len() == 0 || out.Trouble.Len() == 0 {
		t.Errorf("events %d, counts %d, trouble %d: the half that needs no node was not drawn",
			out.Events.Len(), out.Counts.Len(), out.Trouble.Len())
	}
	for _, skipped := range out.NotShown {
		if strings.Contains(skipped.Reason, "nodes") || slices.Contains(skipped.Affects, "capacity") {
			t.Errorf("notShown repeats the refusal (%+v); it is said once, on the capacity", skipped)
		}
	}
	if out.AllWell == nil || *out.AllWell {
		t.Error("a page that could not look at the machines is all well")
	}
}

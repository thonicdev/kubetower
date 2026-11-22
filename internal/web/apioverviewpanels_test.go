package web

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/overview"
)

// The panels of the dashboard, from the two `internal/overview` values the
// endpoint reads.
//
// `TestTheDashboardCarriesEveryMemberTheContractDeclares` in internal/api reads
// `api/openapi.yaml` and proves the member set on the wire is the member set the
// contract declares. It drives a **fake** backend, so on its own it passes
// against an adapter that fills none of them from anything real.
//
// This is the other half: the mapping from the two `internal/overview` values
// the endpoint actually reads. internal/web imports internal/api and not the
// other way round, so neither test can live in the other's package and neither
// is sufficient alone.
//
// # What it does not see: the wiring
//
// Every test here calls `overviewOf` and `overviewPanels` directly, so removing
// the `overviewPanels(&out, ...)` line from `apiReads.Overview` - or the
// `overview.Workloads` goroutine that feeds it - leaves this file entirely
// green. `apiReads.Overview` obtains its client from `cluster.Manager.Client`,
// whose cache is unexported, so driving the endpoint against a fake clientset
// needs a seam in `internal/cluster` that does not exist. What covers the wiring
// is `internal/api`'s `TestTheDashboardCarriesEveryMemberTheContractDeclares`
// over a fake backend - which proves the shape and not the source - and a
// browser.

// workloadsFixture is a cluster with something wrong with it, because a fixture
// where everything is healthy exercises none of the panels this file is about:
// four of the six draw nothing at all on a quiet cluster.
func workloadsFixture() overview.WorkloadsOverview {
	return overview.WorkloadsOverview{
		Cluster:   "test",
		Collected: time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC),
		Pods:      429,
		Kinds: []overview.KindSummary{
			{Kind: "Deployments", Slug: "deployments", Total: 3, Healthy: 2,
				Note: "1 not ready", Class: overview.ClassWarn},
			// A kind with nothing wrong with it, to pin that Note is empty rather
			// than carrying a sentence the server composed for every healthy tile.
			{Kind: "DaemonSets", Slug: "daemonsets", Total: 4, Healthy: 4, Class: overview.ClassOK},
		},
		Phases: []overview.Segment{
			{Label: "Running", Value: 420, Class: overview.ClassOK},
			{Label: "Failed", Value: 9, Class: overview.ClassBad},
		},
		Trouble: []overview.Trouble{
			// Dated by its Available condition, three days before the read.
			{Kind: "Deployment", Namespace: "default", Name: "api",
				Detail: "1 of 3 replicas ready", Class: overview.ClassBad,
				Since: time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC), Age: "3d"},
			// A kind with no record of when: listed, and undated.
			{Kind: "Pod", Namespace: "default", Name: "api-0",
				Detail: "CrashLoopBackOff", Class: overview.ClassBad},
		},
		Restarting: []overview.Restarting{
			{Namespace: "default", Name: "api-0", Container: "api", Restarts: 12,
				Reason: "OOMKilled", LastExit: time.Date(2026, 9, 12, 8, 56, 0, 0, time.UTC),
				Ago: "4m", AgoTitle: "2026-09-12 08:56:00 UTC",
				Class: overview.ClassBad},
			// A container the kubelet no longer holds an exit for: listed, because
			// 900 restarts is the row an operator most needs to see, and undated.
			// It is the arm a fixture of well-formed rows cannot reach, and the one
			// where an instant invented from the page's clock would read "0s".
			{Namespace: "default", Name: "worker-7", Container: "worker", Restarts: 900,
				Reason: "—", Ago: "—",
				AgoTitle: "the kubelet no longer holds this container's last exit",
				Class:    overview.ClassBad},
			// An init container, and it is last on purpose: `restarting()` sorts
			// by count and does not promote an init row, so a 6-restart init
			// container sits below a 12 and a 900. The adapter must not reorder
			// it into a rank of its own — the mark is what the panel offers
			// instead, and this fixture is what would notice a sort added here.
			{Namespace: "default", Name: "web-3", Container: "wait-for-db", Init: true,
				Restarts: 6, Reason: "Error",
				LastExit: time.Date(2026, 9, 12, 8, 30, 0, 0, time.UTC),
				Ago:      "30m", AgoTitle: "2026-09-12 08:30:00 UTC",
				Class: overview.ClassWarn},
		},
		Degraded: []overview.PanelError{
			{Panel: overview.PanelJobs, Reason: "jobs is forbidden"},
		},
	}
}

// alerting is `measured()` with the things a dashboard exists to show.
//
// `measured()` is the capacity fixture and carries no alert, no node and no
// event, so it exercises none of them.
func alerting() overview.ClusterOverview {
	out := measured()
	out.Cluster = "test"
	out.Version = "v1.29.4"
	out.Collected = time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	out.Healthy, out.Total = 2, 3
	out.Alerts = []overview.Alert{{
		Class: overview.ClassBad, Subject: "node-2", Kind: "Node", Name: "node-2",
		Summary: "under memory pressure",
		Detail:  "kubelet has observed memory pressure",
	}}
	out.Events = []overview.EventLine{{
		When: time.Date(2026, 9, 12, 8, 56, 0, 0, time.UTC), Age: "4m",
		Namespace: "default", Object: "Pod/api-0", Kind: "Pod", Name: "api-0",
		Reason: "BackOff", Message: "Back-off restarting failed container",
		Count: 340, Class: overview.ClassWarn,
	}}
	out.Degraded = []overview.PanelError{{Panel: overview.PanelEvents, Reason: "events is forbidden"}}
	return out
}

func dashboard(t *testing.T) api.Overview {
	t.Helper()
	machines, workloads := alerting(), workloadsFixture()
	out := overviewOf("test", machines, overview.Top{}, overview.Top{},
		overview.Availability{Detail: "metrics-server did not answer"}, false)
	overviewPanels(&out, "test", machines, &workloads, nil, nil)
	return out
}

func TestTheDashboardDrawsTheHalfAnOperatorOpensItFor(t *testing.T) {
	out := dashboard(t)

	// An empty answer and a failed answer are different answers, and both differ
	// from a panel this test walked over without looking at anything.
	for _, panel := range []struct {
		name  string
		state api.State
		count int
	}{
		{"counts", out.Counts.State(), out.Counts.Len()},
		{"capacity", out.Capacity.State(), out.Capacity.Len()},
		{"events", out.Events.State(), out.Events.Len()},
		{"alerts", out.Alerts.State(), out.Alerts.Len()},
		{"trouble", out.Trouble.State(), out.Trouble.Len()},
		{"restarting", out.Restarting.State(), out.Restarting.Len()},
		{"phases", out.Phases.State(), out.Phases.Len()},
	} {
		if panel.state != api.StateOK {
			t.Errorf("%s panel = %q, want %q", panel.name, panel.state, api.StateOK)
		}
		if panel.count == 0 {
			t.Errorf("%s panel is on the ok arm with nothing in it", panel.name)
		}
	}

	// The head. The badge is
	// the one an operator reads before deciding not to read the rest.
	if out.Version != "v1.29.4" {
		t.Errorf("version = %q; a control plane that will not say its version is about to fail everything else", out.Version)
	}
	if out.CollectedAt == nil {
		t.Error("no collectedAt: nothing on the page says whether it is current")
	}
	if out.AllWell == nil {
		t.Fatal("allWell is absent, so the client has to compute it from whichever panels it received")
	}
	if *out.AllWell {
		t.Error("allWell over a cluster with a node under memory pressure and a deployment at 1 of 3")
	}
	if out.NodesSummary == nil || out.NodesSummary.Text != "2 of 3 nodes healthy" {
		t.Errorf("nodesSummary = %+v, want the sentence the head carries", out.NodesSummary)
	}
	if out.NodesTone != "warn" {
		t.Errorf("nodesTone = %q, want warn on 2 of 3", out.NodesTone)
	}
	if out.WorkloadsSummary == nil || out.WorkloadsSummary.Text != "429 pods" {
		t.Errorf("workloadsSummary = %+v", out.WorkloadsSummary)
	}
}

// The six columns an event panel is read for.
//
// The `Ref` this panel carried validated as an `EventEntry` with every optional
// member absent, which is why the response-conformance test was green about a
// panel that had lost age, count and both links. A warning with no object to
// open is a sentence.
func TestAWarningEventCarriesItsAgeItsCountAndItsObject(t *testing.T) {
	out := dashboard(t)

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	var payload struct {
		Events struct {
			Items []api.EventEntry `json:"items"`
		} `json:"events"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(payload.Events.Items) != 1 {
		t.Fatalf("%d events in the payload a client reads", len(payload.Events.Items))
	}
	event := payload.Events.Items[0]

	if event.Age != "4m" {
		t.Errorf("age = %q", event.Age)
	}
	// 340 against 1. Without it, a BackOff that has fired all morning and one
	// that fired once are the same row.
	if event.Count != 340 {
		t.Errorf("count = %d, want 340", event.Count)
	}
	if event.Reason != "BackOff" {
		t.Errorf("reason = %q", event.Reason)
	}
	if event.Object == nil || event.Object.Href == "" {
		t.Errorf("object = %+v; a warning with nothing to open is a sentence", event.Object)
	}
	if event.Scope == nil || event.Scope.Text != "default" {
		t.Errorf("scope = %+v", event.Scope)
	}
	if event.At == nil {
		t.Error("no instant: the follow-up question is whether this was during the 03:00 incident")
	}
}

// A trouble row says since when, and an undated one says nothing rather than now.
//
// `overview.Trouble` carries `Since` and `Age` from the object's own record;
// this pins that the adapter carries both onto the wire, and that
// the zero time is **omitted** rather than marshalled as the year one - which a
// client would otherwise date as "2025 years".
func TestATroubleRowSaysSinceWhenAndAnUndatedOneSaysNothing(t *testing.T) {
	out := dashboard(t)

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	var payload struct {
		Trouble struct {
			Items []struct {
				Object api.Ref `json:"object"`
				Since  *string `json:"since"`
				Age    *string `json:"age"`
			} `json:"items"`
		} `json:"trouble"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(payload.Trouble.Items) != 2 {
		t.Fatalf("%d trouble rows in the payload a client reads, want 2", len(payload.Trouble.Items))
	}

	dated := payload.Trouble.Items[0]
	if dated.Object.Text != "api" {
		t.Fatalf("first row is %q, want the Deployment", dated.Object.Text)
	}
	if dated.Since == nil || *dated.Since != "2026-09-09T09:00:00Z" {
		t.Errorf("since = %v, want the Available condition's instant on the wire", dated.Since)
	}
	if dated.Age == nil || *dated.Age != "3d" {
		t.Errorf("age = %v, want the server's own wording", dated.Age)
	}

	undated := payload.Trouble.Items[1]
	if undated.Object.Text != "api-0" {
		t.Fatalf("second row is %q, want the Pod", undated.Object.Text)
	}
	if undated.Since != nil {
		t.Errorf("since = %q on a row whose kind has no record; the member must be absent, "+
			"not the zero time", *undated.Since)
	}
	if undated.Age != nil {
		t.Errorf("age = %q on an undated row; absent draws the em dash, and anything else "+
			"reads as an answer", *undated.Age)
	}
}

// A restarting row carries the exit instant onto the wire, and an undated one
// carries nothing rather than the year one.
//
// `RestartEntry.lastExitAt` is declared in `api/openapi.yaml` and drawn by
// `Overview.tsx`'s When cell; if this adapter did not set it, every row would
// take the client's `row.lastExitAt === undefined` arm and hover as "the
// kubelet no longer holds this container's last exit", a real state reserved for
// a kubelet that has forgotten, printed over a stamp the server was holding.
//
// `internal/api`'s TestTheDashboardCarriesEveryMemberTheContractDeclares cannot
// see this: it drives a **fake** backend whose `fakeReads.Overview` sets
// `LastExitAt` by hand, so it stays green whether or not anything real fills it.
func TestARestartingRowCarriesTheExitInstantOntoTheWire(t *testing.T) {
	out := dashboard(t)

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	var payload struct {
		Restarting struct {
			Items []struct {
				Object     api.Ref `json:"object"`
				LastExitAt *string `json:"lastExitAt"`
				Age        *string `json:"age"`
			} `json:"items"`
		} `json:"restarting"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(payload.Restarting.Items) != 3 {
		t.Fatalf("%d restarting rows in the payload a client reads, want 3",
			len(payload.Restarting.Items))
	}

	dated := payload.Restarting.Items[0]
	if dated.Object.Text != "api-0" {
		t.Fatalf("first row is %q, want the OOMKilled container", dated.Object.Text)
	}
	if dated.LastExitAt == nil {
		t.Fatal("lastExitAt is absent on a row whose kubelet stamped the exit; the client " +
			"then hovers it as a termination nobody has a record of, which is a different " +
			"state and the one this member exists to distinguish")
	}
	if *dated.LastExitAt != "2026-09-12T08:56:00Z" {
		t.Errorf("lastExitAt = %q, want the kubelet's own stamp", *dated.LastExitAt)
	}
	// The relative wording travels beside it and is not replaced by it: the cell
	// answers "recently or not" and the tooltip answers "during the 03:00
	// incident".
	if dated.Age == nil || *dated.Age != "4m" {
		t.Errorf("age = %v, want the server's own wording beside the instant", dated.Age)
	}

	undated := payload.Restarting.Items[1]
	if undated.Object.Text != "worker-7" {
		t.Fatalf("second row is %q, want the container with no recorded exit", undated.Object.Text)
	}
	if undated.LastExitAt != nil {
		t.Errorf("lastExitAt = %q on a row the kubelet holds no exit for; the member must be "+
			"absent, not the zero time — which marshals as the year one and dates the crash "+
			"from it", *undated.LastExitAt)
	}
}

// A restarting init container says so on the wire, and an app container says so
// too rather than saying nothing.
//
// `overview.Restarting.Init` is written by `restarting()`, and every hop
// downstream must carry it: `api/openapi.yaml` declares it, `api.RestartEntry`
// has the field, this adapter copies it and `RestartRow` draws it, or init rows
// arrive on a real dashboard indistinguishable from the app containers beside
// them.
//
// Three things are asserted and each fails a different way:
//
//   - the init row is marked;
//   - the app rows carry `init` as **false rather than absent** — an `omitempty`
//     on a bool would make "not an init container" and "this server is too old
//     to say" the same payload, which is what `PodContainer.Init` is always
//     written to avoid;
//   - the init row is still third. `restarting()` sorts by count and refuses to
//     promote an init row, because "arguably more urgent" is not a rule a reader
//     can predict from a screen. A rank quietly added at this hop would be
//     invisible to `internal/overview`'s own tests.
func TestARestartingInitContainerIsMarkedOnTheWire(t *testing.T) {
	out := dashboard(t)

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	// *bool and not bool: a member the marshaller dropped decodes as the zero
	// value, so a test reading it as a plain bool passes against a payload that
	// says nothing at all.
	var payload struct {
		Restarting struct {
			Items []struct {
				Object    api.Ref `json:"object"`
				Container string  `json:"container"`
				Init      *bool   `json:"init"`
				Restarts  int     `json:"restarts"`
			} `json:"items"`
		} `json:"restarting"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	rows := payload.Restarting.Items
	if len(rows) != 3 {
		t.Fatalf("%d restarting rows, want the fixture's 3", len(rows))
	}
	for i, row := range rows {
		if row.Init == nil {
			t.Fatalf("row %d (%s/%s) carries no `init` at all; absent and false are "+
				"different answers, and a client cannot tell which kind of container "+
				"it is looking at from an absence", i, row.Object.Text, row.Container)
		}
	}

	if *rows[0].Init || *rows[1].Init {
		t.Errorf("init = %v, %v on the two app containers, want false on both",
			*rows[0].Init, *rows[1].Init)
	}

	marked := rows[2]
	if marked.Container != "wait-for-db" {
		t.Fatalf("third row is %q, want the init container", marked.Container)
	}
	if !*marked.Init {
		t.Error("init = false on a container the kubelet files under " +
			"initContainerStatuses; the row then reads as a crashing app container, " +
			"and an operator sent to the app container's logs finds nothing wrong " +
			"with it")
	}
	// Still third, behind 900 and 12. The mark is not a promotion.
	if marked.Restarts != 6 {
		t.Errorf("restarts = %d on the init row, want 6 — and it is last because "+
			"the sort is by count and nothing here reorders it", marked.Restarts)
	}
}

// The kind tiles are a fraction, and a kind with no notion of health carries no
// fraction rather than `0 / n`.
func TestAKindTileCarriesItsHealthyCountAndItsComplaint(t *testing.T) {
	out := dashboard(t)

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	var payload struct {
		Counts struct {
			Items []api.CountEntry `json:"items"`
		} `json:"counts"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(payload.Counts.Items) != 2 {
		t.Fatalf("%d tiles, want the two kinds in the fixture", len(payload.Counts.Items))
	}

	deployments := payload.Counts.Items[0]
	if deployments.Healthy == nil || *deployments.Healthy != 2 || deployments.Count != 3 {
		t.Errorf("deployments = %+v, want 2 of 3 — `3` alone is not a fact anybody acts on", deployments)
	}
	if deployments.Note != "1 not ready" {
		t.Errorf("note = %q", deployments.Note)
	}
	if deployments.Ref == nil || deployments.Ref.Href == "" {
		t.Error("a tile that leads nowhere")
	}

	// The healthy kind. Note is empty and the page says "all healthy" itself:
	// a server sending that sentence on every healthy tile is the same sentence
	// stored n times.
	if note := payload.Counts.Items[1].Note; note != "" {
		t.Errorf("a healthy kind carries the note %q", note)
	}
}

// The workloads half failed on its own. The machine panels are still drawn, and
// the workload panels say *why* rather than being absent from the payload.
//
// The difference matters at the client: `Panel.tsx` draws a panel that is not in
// the answer at all as "Not answered", which is a fault in this product, and a
// workloads fetch that was refused is a fact about the cluster.
func TestAWorkloadsFetchThatFailedDoesNotTakeTheMachinePanels(t *testing.T) {
	machines := alerting()
	out := overviewOf("test", machines, overview.Top{}, overview.Top{},
		overview.Availability{Detail: "metrics-server did not answer"}, false)
	overviewPanels(&out, "test", machines, nil, errors.New("pods is forbidden"), nil)

	for _, panel := range []struct {
		name  string
		state api.State
	}{
		{"alerts", out.Alerts.State()},
		{"events", out.Events.State()},
		{"capacity", out.Capacity.State()},
	} {
		if panel.state != api.StateOK {
			t.Errorf("%s = %q; the machine half is drawn whatever the workloads did", panel.name, panel.state)
		}
	}

	for _, panel := range []struct {
		name   string
		state  api.State
		reason *api.Reason
	}{
		{"counts", out.Counts.State(), out.Counts.Reason()},
		{"trouble", out.Trouble.State(), out.Trouble.Reason()},
		{"restarting", out.Restarting.State(), out.Restarting.Reason()},
		{"phases", out.Phases.State(), out.Phases.Reason()},
	} {
		if panel.state != api.StateUnreadable {
			t.Errorf("%s = %q, want %q — refused is not absent and neither is empty",
				panel.name, panel.state, api.StateUnreadable)
		}
		if panel.reason == nil || panel.reason.Detail == "" {
			t.Errorf("%s carries no reason; an empty panel that never says why is the whole defect", panel.name)
		}
	}

	if out.AllWell == nil || *out.AllWell {
		t.Error("allWell over a page whose workloads could not be read")
	}
}

// `notShown` is the page-level list, and it is not the same thing as a panel
// envelope on a failure arm.
//
// The panels in it were never *asked for*, because a prerequisite was missing,
// so they have no envelope to carry a reason and the page would otherwise draw
// nothing where they would have been. Both halves contribute.
func TestNotShownNamesEveryPanelThatWasNeverAsked(t *testing.T) {
	out := dashboard(t)

	if len(out.NotShown) != 2 {
		t.Fatalf("notShown = %+v, want both halves' skipped panels", out.NotShown)
	}
	names := map[string]string{}
	for _, panel := range out.NotShown {
		names[panel.Panel] = panel.Reason
	}
	if names[overview.PanelEvents] == "" {
		t.Error("the machine half's skipped panel is not named")
	}
	if names[overview.PanelJobs] == "" {
		t.Error("the workload half's skipped panel is not named")
	}
}

// The badge is a server judgment over **both** halves, and a panel that could
// not be read is not a panel with nothing in it.
//
// `internal/overview/allwell_test.go` pins the machine half; this pins that the
// workload half reaches it, which is the half that comes from a fetch that can
// fail on its own.
func TestTheBadgeIsNotRaisedOverAPageThatCouldNotBeRead(t *testing.T) {
	quiet := measured()
	quiet.Cluster, quiet.Healthy, quiet.Total = "test", 3, 3

	well := overview.WorkloadsOverview{Cluster: "test", Pods: 12,
		Kinds: []overview.KindSummary{{Kind: "Pods", Slug: "pods", Total: 12, Healthy: 12}}}

	out := overviewOf("test", quiet, overview.Top{}, overview.Top{}, overview.Availability{}, false)
	overviewPanels(&out, "test", quiet, &well, nil, nil)
	if out.AllWell == nil || !*out.AllWell {
		t.Fatalf("allWell = %v on a cluster with nothing wrong with it", out.AllWell)
	}

	// One crash-looping container, and nothing else changed. Restarting is
	// part of the judgment, or containers the kubelet keeps killing would sit
	// under a badge saying nothing needs acting on.
	crashing := well
	crashing.Restarting = []overview.Restarting{{Namespace: "default", Name: "api-0",
		Container: "api", Restarts: 12, Class: overview.ClassBad}}
	out = overviewOf("test", quiet, overview.Top{}, overview.Top{}, overview.Availability{}, false)
	overviewPanels(&out, "test", quiet, &crashing, nil, nil)
	if out.AllWell == nil || *out.AllWell {
		t.Error("allWell beside a container the kubelet keeps killing")
	}

	// A workload panel that was skipped rather than empty.
	skipped := well
	skipped.Degraded = []overview.PanelError{{Panel: "Jobs", Reason: "jobs is forbidden"}}
	out = overviewOf("test", quiet, overview.Top{}, overview.Top{}, overview.Availability{}, false)
	overviewPanels(&out, "test", quiet, &skipped, nil, nil)
	if out.AllWell == nil || *out.AllWell {
		t.Error("allWell over a page with a panel nobody was allowed to read")
	}
}

// The capacity captions lead to the machines that make the cluster full.
//
// All three lead to the node list deliberately: they are sums
// over machines, and the node table is where the same three figures are broken
// down one machine at a time. Without it a full cluster has no way through.
func TestEachCapacityFigureLeadsToTheNodeList(t *testing.T) {
	out := dashboard(t)
	if out.Capacity.Len() != 3 {
		t.Fatalf("%d capacity cells", out.Capacity.Len())
	}

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	var payload struct {
		Capacity struct {
			Items []api.CapacityCell `json:"items"`
		} `json:"capacity"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	for _, cell := range payload.Capacity.Items {
		if cell.Ref == nil || cell.Ref.Href == "" {
			t.Errorf("%s carries no destination", cell.Label)
			continue
		}
		if cell.Ref.Text != cell.Label+" requested" {
			t.Errorf("%s caption = %q", cell.Label, cell.Ref.Text)
		}
	}
}

// The cluster's own series, as numbers, and the three answers it has.
func TestTheClusterHistoryIsNumbersAndNamesWhyThereAreNone(t *testing.T) {
	machines := alerting()

	// No backend in this build at all.
	if got := clusterHistory(machines, nil).State(); got != api.StateAbsent {
		t.Errorf("no backend: history = %q, want %q", got, api.StateAbsent)
	}

	// A backend exists and this cluster keeps no series — a cluster with
	// metrics-server and no Prometheus is a normal, working cluster and this must
	// not be an alarm.
	machines.History = overview.Availability{Detail: "no Prometheus was found"}
	if got := clusterHistory(machines, stubMetrics{}).State(); got != api.StateAbsent {
		t.Errorf("no series: history = %q, want %q", got, api.StateAbsent)
	}

	machines.History = overview.Availability{OK: true}
	machines.CPUHistory = overview.History{Title: "CPU used", Step: time.Minute,
		Points: []overview.Point{{At: time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC), Value: 3.2}}}
	// One series and not two, so the assertion below counts something rather than
	// whatever `measured()` happened to carry.
	machines.MemoryHistory = overview.History{}
	panel := clusterHistory(machines, stubMetrics{})
	if panel.State() != api.StateOK {
		t.Fatalf("history = %q with a series in hand", panel.State())
	}

	raw, err := json.Marshal(panel)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	var payload struct {
		Data api.MetricsPanel `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(payload.Data.Series) != 1 {
		t.Fatalf("%d series", len(payload.Data.Series))
	}
	if payload.Data.Series[0].Key != "cpu-used" {
		t.Errorf("key = %q; a client keys a line by this and should not case-fold it itself",
			payload.Data.Series[0].Key)
	}
	if len(payload.Data.Series[0].Data.Values) != 1 {
		t.Errorf("values = %v", payload.Data.Series[0].Data.Values)
	}
}

// A history whose lines are empty says why, on each line and in the
// caption, and never "Prometheus answered" over two charts with no samples.
func TestAnEmptyClusterHistorySaysWhyOnEachLineAndInTheCaption(t *testing.T) {
	why := "CPU used: this cluster's Prometheus has no per-container series; Memory used: no samples in this window"
	machines := alerting()
	machines.History = overview.Availability{OK: true, Detail: why}
	machines.CPUHistory = overview.History{Title: "CPU used", Step: time.Minute,
		Unavailable: "this cluster's Prometheus has no per-container series", Missing: overview.MissingCounter}
	machines.MemoryHistory = overview.History{Title: "Memory used", Step: time.Minute,
		Unavailable: "no samples in this window", Missing: overview.MissingEmpty}

	panel := clusterHistory(machines, stubMetrics{})
	if panel.State() != api.StateOK {
		t.Fatalf("history = %q; Prometheus answered, so this is a panel and not an absence", panel.State())
	}
	raw, err := json.Marshal(panel)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	var payload struct {
		Data api.MetricsPanel `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if payload.Data.Caption != why {
		t.Errorf("caption = %q, want %q", payload.Data.Caption, why)
	}
	want := map[string]api.ReasonCode{"cpu-used": api.CodeNotInstalled, "memory-used": api.CodeEmptyScope}
	if len(payload.Data.Series) != len(want) {
		t.Fatalf("%d series, want %d", len(payload.Data.Series), len(want))
	}
	for _, line := range payload.Data.Series {
		if line.Reason == nil {
			t.Errorf("%s has no samples and no reason", line.Key)
			continue
		}
		if line.Reason.Code != want[line.Key] {
			t.Errorf("%s: code = %q, want %q", line.Key, line.Reason.Code, want[line.Key])
		}
	}
}

// The Overview draws no history at
// all where there is no Prometheus to chart from, which it reads off `absent`
// with `not_installed`. So a Prometheus that is there and did not answer -
// both range queries failing, a timeout or a refusal - must not arrive in that
// shape, or the client would hide a failure as if it were an absence. It is a
// failure envelope, classified from the line's own error; a cluster with no
// Prometheus (`MissingBackend`) stays `absent`.
func TestAClusterHistoryThatFailedIsNotReportedAsNoPrometheus(t *testing.T) {
	timeout := errors.New(`Post "http://prometheus:9090/api/v1/query_range": dial tcp 10.0.0.9:9090: i/o timeout`)
	failedLine := func(title string) overview.History {
		return overview.History{Title: title, Step: time.Minute, Unavailable: timeout.Error(),
			Missing: overview.MissingFailed, Err: timeout}
	}
	machines := alerting()
	machines.History = overview.Availability{Detail: timeout.Error()}
	machines.CPUHistory, machines.MemoryHistory = failedLine("CPU used"), failedLine("Memory used")

	panel := clusterHistory(machines, stubMetrics{})
	if got := panel.State(); got == api.StateAbsent || got == api.StateOK {
		t.Fatalf("history = %q for a Prometheus that did not answer; want a failure state", got)
	}
	raw, err := json.Marshal(panel)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	var payload struct {
		Reason api.Reason `json:"reason"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if payload.Reason.Code == api.CodeNotInstalled {
		t.Errorf("code = %q: a timeout is not a missing Prometheus", payload.Reason.Code)
	}
	if !strings.Contains(payload.Reason.Message, "i/o timeout") {
		t.Errorf("message = %q, want the read's own words", payload.Reason.Message)
	}

	// One line with no Prometheus to ask and the other failed is still a
	// failure: something was asked and did not answer.
	noBackend := overview.History{Title: "Memory used", Step: time.Minute,
		Unavailable: "no Prometheus was found", Missing: overview.MissingBackend}
	machines.MemoryHistory = noBackend
	if got := clusterHistory(machines, stubMetrics{}).State(); got == api.StateAbsent {
		t.Errorf("one failed line: history = %q, want a failure state", got)
	}

	// No Prometheus at all is the absence the client hides.
	machines.CPUHistory = noBackend
	machines.CPUHistory.Title = "CPU used"
	machines.History = overview.Availability{Detail: "no Prometheus was found"}
	panel = clusterHistory(machines, stubMetrics{})
	if got := panel.State(); got != api.StateAbsent {
		t.Fatalf("no Prometheus: history = %q, want %q", got, api.StateAbsent)
	}
	raw, err = json.Marshal(panel)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if payload.Reason.Code != api.CodeNotInstalled {
		t.Errorf("no Prometheus: code = %q, want %q", payload.Reason.Code, api.CodeNotInstalled)
	}
}

// A history read that panicked reaches the page as a failure. `overview.Cluster` recovers it into both lines
// as `MissingFailed` with an `overview.Crash`, and the Overview hides only
// `absent`/`not_installed`, so the crash must not be that.
func TestAPanickedClusterHistoryIsAFailureAndNotAnAbsence(t *testing.T) {
	crash := overview.Crash{What: "reading this cluster's history", Value: "a nil map"}
	machines := alerting()
	machines.History = overview.Availability{Detail: crash.Error(), Cause: crash}
	machines.CPUHistory = overview.History{Title: "CPU used", Unavailable: crash.Error(), Missing: overview.MissingFailed, Err: crash}
	machines.MemoryHistory = overview.History{Title: "Memory used", Unavailable: crash.Error(), Missing: overview.MissingFailed, Err: crash}

	panel := clusterHistory(machines, stubMetrics{})
	if got := panel.State(); got != api.StateUnreadable {
		t.Fatalf("history = %q for a read that panicked; want %q", got, api.StateUnreadable)
	}
	if reason := panel.Reason(); reason == nil || reason.Code != api.CodeUnclassified ||
		!strings.Contains(reason.Message, "panicked") {
		t.Errorf("reason = %+v; want unclassified, naming the panic", reason)
	}
}

// stubMetrics exists only so `clusterHistory` sees a non-nil backend. It is never
// queried: this file tests the mapping from what `overview.Cluster` already
// gathered, and a backend that answered is `ClusterOverview.History.OK`.
type stubMetrics struct{}

func (stubMetrics) Query(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("stubMetrics is never queried")
}

func (stubMetrics) QueryRange(context.Context, string, string, time.Time, time.Time, time.Duration) ([]byte, error) {
	return nil, errors.New("stubMetrics is never queried")
}

// A panel whose task failed says so on the panel, not only on the page.
//
// This is the half a client cannot compute. `notShown` names *"Warning events"*,
// which is prose; the client draws a panel called *"Recent warnings"* and has no
// way to connect the two that is not a string it has to keep in step with a
// sentence in another repository. `affects` is the connection, and it is the
// server's because one read feeds more than one panel.
func TestEveryDegradablePanelNamesWhatItSpoils(t *testing.T) {
	// Every property name on the payload, from the payload, so that a member
	// renamed in `api.Overview` fails here rather than producing a mark that
	// points at nothing.
	members := map[string]bool{}
	fields := reflect.TypeOf(api.Overview{})
	for i := range fields.NumField() {
		tag, _, _ := strings.Cut(fields.Field(i).Tag.Get("json"), ",")
		if tag != "" && tag != "-" {
			members[tag] = true
		}
	}
	if len(members) < 10 {
		t.Fatalf("read %d members off api.Overview; it has more than that, so this check is not "+
			"reading the type it is about", len(members))
	}

	if len(spoiltBy) != len(overview.DegradablePanels) {
		t.Errorf("spoiltBy maps %d panels and internal/overview can degrade %d",
			len(spoiltBy), len(overview.DegradablePanels))
	}
	for _, panel := range overview.DegradablePanels {
		spoilt, ok := spoiltBy[panel]
		if !ok {
			t.Errorf("%q can be degraded and nothing says what it spoils, so a failure of it would "+
				"reach notShown and mark no panel at all", panel)
			continue
		}
		if len(spoilt) == 0 {
			t.Errorf("%q spoils nothing; every panel this page degrades is a panel it draws", panel)
		}
		for _, member := range spoilt {
			if !members[member] {
				t.Errorf("%q says it spoils %q, which is not a member of api.Overview", panel, member)
			}
		}
	}
}

// The two facts coexist, and that is the point rather than an accident.
//
// A panicking task degrades its panel instead of ending the process, and the
// panel's own envelope reads `{"state":"empty"}`. This
// does **not** move the failure into the envelope — `empty` is still what the
// events panel answers, `notShown` still carries the sentence — it adds the one
// member that lets the client say *and this one is incomplete*.
//
// Asserting the envelope is still `empty` is deliberate: a later change that
// "fixed" this by failing the envelope would pass every other test on this page
// and would draw the panel's own failure.
func TestAFailedPanelSaysWhatItSpoiltWhileItsEnvelopeStaysEmpty(t *testing.T) {
	machines := measured()
	machines.Cluster, machines.Healthy, machines.Total = "test", 3, 3
	machines.Events = nil // the feed panicked, so there is nothing in it
	machines.Degraded = []overview.PanelError{{
		Panel:  overview.PanelEvents,
		Reason: "the warning event feed panicked: boom",
	}}

	out := overviewOf("test", machines, overview.Top{}, overview.Top{}, overview.Availability{}, false)
	workloads := workloadsFixture()
	overviewPanels(&out, "test", machines, &workloads, nil, nil)

	var entry *api.SkippedPanel
	for i := range out.NotShown {
		if out.NotShown[i].Panel == overview.PanelEvents {
			entry = &out.NotShown[i]
		}
	}
	if entry == nil {
		t.Fatalf("notShown = %+v, want the warning event feed named", out.NotShown)
	}
	if len(entry.Affects) != 1 || entry.Affects[0] != "events" {
		t.Errorf("affects = %v, want [events] — without it the client cannot tell which of its "+
			"panels this sentence is about", entry.Affects)
	}

	if out.Events.State() != api.StateEmpty {
		t.Errorf("the events envelope is %q; the envelope is not changed here, and a panel "+
			"drawing its own failure is not wanted", out.Events.State())
	}

	// And on the wire, because `affects` is required by the contract and a nil
	// slice marshals to null, which is the one form a client cannot rely on.
	body, err := json.Marshal(out.NotShown)
	if err != nil {
		t.Fatalf("marshalling notShown: %v", err)
	}
	if strings.Contains(string(body), `"affects":null`) {
		t.Errorf("notShown carries a null affects: %s", body)
	}
}

// The server cuts the restarting list and the warning feed at 200 rows,
// and the page has to be told how many there were, or the last page of a cut
// list reads as the end of it. The totals come from `internal/overview`, which
// is what knows how many it dropped; this pins that the adapter carries them
// onto the wire, and leaves `restartingTotal` and `eventsTotal` off a panel that
// did not answer.
func TestTheCutListsCarryHowManyRowsThereWereOntoTheWire(t *testing.T) {
	machines, workloads := alerting(), workloadsFixture()
	machines.EventsTotal = 900
	machines.Degraded = nil // the feed was read; alerting() also names it unread
	workloads.RestartingTotal = 250
	out := overviewOf("test", machines, overview.Top{}, overview.Top{},
		overview.Availability{Detail: "metrics-server did not answer"}, false)
	overviewPanels(&out, "test", machines, &workloads, nil, nil)

	var payload struct {
		RestartingTotal *int `json:"restartingTotal"`
		EventsTotal     *int `json:"eventsTotal"`
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if payload.RestartingTotal == nil || *payload.RestartingTotal != 250 {
		t.Errorf("restartingTotal = %v, want 250: the pager cannot say the list was cut", payload.RestartingTotal)
	}
	if payload.EventsTotal == nil || *payload.EventsTotal != 900 {
		t.Errorf("eventsTotal = %v, want 900: the pager cannot say the feed was cut", payload.EventsTotal)
	}

	failed := overviewOf("test", machines, overview.Top{}, overview.Top{},
		overview.Availability{Detail: "metrics-server did not answer"}, false)
	overviewPanels(&failed, "test", machines, nil, errors.New("pods is forbidden"), nil)
	if failed.RestartingTotal != nil {
		t.Errorf("restartingTotal = %d on a restarting panel that did not answer, want it absent",
			*failed.RestartingTotal)
	}

	// The event read failed: the feed is degraded, not cut, and its envelope
	// still answers `empty`. A 0 here would read as a measured count of zero.
	unread := alerting()
	unread.Events, unread.EventsTotal = nil, 0
	unread.Degraded = []overview.PanelError{{Panel: overview.PanelEvents, Reason: "events is forbidden"}}
	blind := overviewOf("test", unread, overview.Top{}, overview.Top{},
		overview.Availability{Detail: "metrics-server did not answer"}, false)
	overviewPanels(&blind, "test", unread, &workloads, nil, nil)
	if blind.EventsTotal != nil {
		t.Errorf("eventsTotal = %d on a warning feed that was not read, want it absent", *blind.EventsTotal)
	}
}

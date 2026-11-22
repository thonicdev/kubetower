package web

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/overview"
)

// The two panels of the cluster dashboard that answered with figures rather than
// with what they knew.
//
// The requested figure and both leaderboards are computed and tested in
// internal/overview; this adapter must ask for them. The tests below are written
// against the two ways that can fail — one number printed under two labels, and an
// `ok` answer about a backend nothing had queried — because both are invisible
// in a response that validates, renders and looks calm.

// --- capacity: requested is not used -----------------------------------------

// clock is a fixed instant for the series below; the panel reads the last
// sample, so only the order matters.
var overviewClock = time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)

// measured is a cluster whose requests and whose consumption are deliberately
// different numbers. If the two ever come out equal here, they were read from
// one place.
func measured() overview.ClusterOverview {
	return overview.ClusterOverview{
		Cluster: "test",
		Total:   3,
		Healthy: 3,
		// internal/overview counts requests against allocatable and keeps them in
		// Capacity.Used; see ClusterOverview.CPU. This is the requested figure.
		CPU:    overview.Capacity{Name: "CPU", Unit: overview.UnitCores, Used: 2000, Total: 4000},
		Memory: overview.Capacity{Name: "Memory", Unit: overview.UnitBytes, Used: 3 << 30, Total: 8 << 30},
		Pods:   overview.Capacity{Name: "Pods", Unit: overview.UnitCount, Used: 12, Total: 110},

		// What is actually being consumed, which is a different backend's answer.
		History: overview.Availability{OK: true, Detail: "Prometheus answered"},
		CPUHistory: overview.History{Title: "CPU used", Unit: overview.UnitCores, Points: []overview.Point{
			{At: overviewClock.Add(-time.Minute), Value: 400},
			{At: overviewClock, Value: 500},
		}},
		MemoryHistory: overview.History{Title: "Memory used", Unit: overview.UnitBytes, Points: []overview.Point{
			{At: overviewClock, Value: 1 << 30},
		}},
	}
}

func cellNamed(t *testing.T, cells []api.CapacityCell, label string) api.CapacityCell {
	t.Helper()
	for _, cell := range cells {
		if cell.Label == label {
			return cell
		}
	}
	t.Fatalf("no %q cell among %d", label, len(cells))
	return api.CapacityCell{}
}

// Requested and Used are different quantities and must come from different
// places. Setting both from Capacity.Used would make the panel agree with
// itself perfectly on every cluster - and an over-requested cluster, which is
// the one thing the panel exists to show, would be undrawable.
func TestRequestedAndUsedAreReadFromDifferentSources(t *testing.T) {
	cells := capacityCells("prod", measured())
	if len(cells) != 3 {
		t.Fatalf("cells = %d, want 3", len(cells))
	}

	cpu := cellNamed(t, cells, "CPU")
	if cpu.Requested.Value == nil || *cpu.Requested.Value != 2000 {
		t.Fatalf("requested CPU = %v, want the 2000m the scheduler has committed", cpu.Requested.Value)
	}
	if cpu.Used == nil || cpu.Used.Value == nil {
		t.Fatalf("used CPU = %v, want the 500m actually being consumed", cpu.Used)
	}
	if *cpu.Used.Value != 500 {
		t.Errorf("used CPU = %v, want 500 — the last sample of the usage series", *cpu.Used.Value)
	}
	if *cpu.Used.Value == *cpu.Requested.Value {
		t.Error("requested and used are the same number under two labels, which is the bug")
	}
	if got, want := cpu.Used.Text, "500m"; got != want {
		t.Errorf("used text = %q, want %q — spelled by the type that owns the unit", got, want)
	}
	if cpu.Capacity.Value == nil || *cpu.Capacity.Value != 4000 {
		t.Errorf("capacity = %v, want the 4000m allocatable", cpu.Capacity.Value)
	}

	memory := cellNamed(t, cells, "Memory")
	if memory.Requested.Value == nil || *memory.Requested.Value != float64(3<<30) {
		t.Errorf("requested memory = %v, want 3 GiB", memory.Requested.Value)
	}
	if memory.Used == nil || memory.Used.Value == nil || *memory.Used.Value != float64(1<<30) {
		t.Errorf("used memory = %v, want the 1 GiB in the series", memory.Used)
	}

	// Each line's hover, both halves in the capacity's unit
	// CPU in cores even under one core, never `500m / 4.0 cores`.
	if got, want := cpu.RequestedOf, "2 / 4 cores"; got != want {
		t.Errorf("requestedOf = %q, want %q", got, want)
	}
	if got, want := cpu.UsedOf, "0.5 / 4 cores"; got != want {
		t.Errorf("usedOf = %q, want %q", got, want)
	}
	if !strings.HasPrefix(memory.RequestedOf, "3 / ") || !strings.HasSuffix(memory.RequestedOf, " GiB") {
		t.Errorf("memory requestedOf = %q, want `3 / <capacity> GiB`, one unit", memory.RequestedOf)
	}
	if !strings.HasPrefix(memory.UsedOf, "1 / ") || !strings.HasSuffix(memory.UsedOf, " GiB") {
		t.Errorf("memory usedOf = %q, want `1 / <capacity> GiB`, one unit", memory.UsedOf)
	}
}

// A pod slot is claimed rather than consumed, so the requested count is already
// the measurement. Printing it again under "used" would be the same
// two-labels-one-number reading in the one cell where it is arithmetically true.
func TestThePodsCellDoesNotRepeatItsRequestedFigureAsUsed(t *testing.T) {
	pods := cellNamed(t, capacityCells("prod", measured()), "Pods")
	if pods.Requested.Value == nil || *pods.Requested.Value != 12 {
		t.Fatalf("requested pod slots = %v, want 12", pods.Requested.Value)
	}
	if pods.Used == nil {
		t.Fatal("the cell says nothing at all about usage; it should say why there is none")
	}
	if pods.Used.Value != nil {
		t.Errorf("used pod slots = %v, want null — there is no second figure to read", *pods.Used.Value)
	}
	if pods.Used.Trustworthy {
		t.Error("a reading that was never taken is marked trustworthy")
	}
	if !strings.Contains(pods.Used.Text, "claimed") {
		t.Errorf("Text = %q, want the reason there is no usage figure", pods.Used.Text)
	}
}

// No metrics backend is the ordinary state of a working cluster. It must cost
// the usage figure and nothing else: requested comes from the Kubernetes API and
// is still a measurement.
func TestWithoutAMetricsBackendUsedIsAStateAndRequestedIsStillANumber(t *testing.T) {
	machines := measured()
	reason := "no metrics backend is configured, so there is no history to draw"
	machines.History = overview.Availability{Detail: reason}
	machines.CPUHistory = overview.History{Title: "CPU used", Unavailable: reason}
	machines.MemoryHistory = overview.History{Title: "Memory used", Unavailable: reason}

	cpu := cellNamed(t, capacityCells("prod", machines), "CPU")
	if cpu.Used == nil {
		t.Fatal("no usage member at all, so the panel cannot say why there is no figure")
	}
	if cpu.Used.Value != nil {
		t.Errorf("used = %v, want null — a zero reads as an idle cluster", *cpu.Used.Value)
	}
	if cpu.Used.Trustworthy {
		t.Error("a reading nobody took is marked trustworthy")
	}
	if cpu.Used.Text != reason {
		t.Errorf("Text = %q, want the backend's own reason", cpu.Used.Text)
	}
	if cpu.Requested.Value == nil || *cpu.Requested.Value != 2000 {
		t.Errorf("requested = %v, want 2000m — it needs no metrics backend at all", cpu.Requested.Value)
	}
	// No figure, no hover: `usedOf` is absent rather than `0 / 4 cores`.
	if cpu.UsedOf != "" {
		t.Errorf("usedOf = %q for a reading nobody took; want none", cpu.UsedOf)
	}
	if cpu.RequestedOf == "" {
		t.Error("requestedOf is empty, but requested needs no metrics backend")
	}
}

// A backend that answered and had no samples is not a backend that could not be
// asked. Both draw as no figure; only one of them is worth going to look at.
func TestAnEmptyWindowIsNotAMissingBackend(t *testing.T) {
	answered := measured()
	answered.CPUHistory = overview.History{Title: "CPU used"}
	empty := cellNamed(t, capacityCells("prod", answered), "CPU").Used

	missing := measured()
	missing.History = overview.Availability{Detail: "no metrics backend is configured"}
	missing.CPUHistory = overview.History{Title: "CPU used", Unavailable: "no metrics backend is configured"}
	absent := cellNamed(t, capacityCells("prod", missing), "CPU").Used

	if empty == nil || absent == nil {
		t.Fatal("one of the two cells carries no usage member at all")
	}
	if empty.Value != nil || absent.Value != nil {
		t.Fatal("a figure was invented for a window with no samples")
	}
	if empty.Text == absent.Text {
		t.Errorf("both read %q — an empty answer and a failed one are drawn the same", empty.Text)
	}
	if !strings.Contains(empty.Text, "no samples") {
		t.Errorf("Text = %q, want the empty window said as one", empty.Text)
	}
}

// The pod list is what requests are summed from. A cluster that refuses it has
// no requested figure — which is a state, never a zero, and it says which of the
// two reasons it is.
func TestRequestedSaysWhyRatherThanReportingNothingAsZero(t *testing.T) {
	machines := measured()
	machines.CPU = overview.Capacity{Name: "CPU", Unit: overview.UnitCores}
	machines.Degraded = []overview.PanelError{{
		Panel:  "Requested CPU and memory",
		Reason: `pods is forbidden: User "reader" cannot list resource "pods"`,
	}}

	cpu := cellNamed(t, capacityCells("prod", machines), "CPU")
	if cpu.Requested.Value != nil {
		t.Fatalf("requested = %v, want null — nothing was summed", *cpu.Requested.Value)
	}
	if cpu.Requested.Trustworthy {
		t.Error("a figure nobody could compute is marked trustworthy")
	}
	if !strings.Contains(cpu.Requested.Text, "pod list") || !strings.Contains(cpu.Requested.Text, "forbidden") {
		t.Errorf("Text = %q, want the pod list named and the cluster's own words kept", cpu.Requested.Text)
	}
	if cpu.Capacity.Value != nil {
		t.Errorf("capacity = %v, want null: overview.Cluster drops the total with the requests "+
			"so that a real allocatable cannot draw a confident 0%%", *cpu.Capacity.Value)
	}
}

// --- the leaderboards: asked, or said so -------------------------------------

// promSource is a Prometheus that answers from two canned bodies, one per
// question. Both queries go through the same method, so a single body would make
// the memory board parse a CPU answer.
type promSource struct {
	cpu    string
	memory string
	err    error
}

func (p promSource) Query(_ context.Context, _, promQL string) ([]byte, error) {
	if p.err != nil {
		return nil, p.err
	}
	if strings.Contains(promQL, "cpu") {
		return []byte(p.cpu), nil
	}
	return []byte(p.memory), nil
}

func (p promSource) QueryRange(context.Context, string, string, time.Time, time.Time, time.Duration) ([]byte, error) {
	return nil, errors.New("the leaderboard asks instant queries only")
}

func vector(namespace, pod, value string) string {
	return `{"status":"success","data":{"resultType":"vector","result":[` +
		`{"metric":{"namespace":"` + namespace + `","pod":"` + pod + `"},"value":[1755432000,"` + value + `"]}]}}`
}

const emptyVector = `{"status":"success","data":{"resultType":"vector","result":[]}}`

// noMetricsServer is a client whose REST client is nil, which is the shape of a
// cluster with no metrics-server — and of the fake client, which cannot make raw
// API requests.
func noMetricsServer() *cluster.Client {
	return &cluster.Client{Context: "test", Clients: fake.NewSimpleClientset()}
}

// boardsOf reads the panel the way a client does: through the envelope's own
// marshalling, which is the only thing that decides the arm.
func boardsOf(t *testing.T, panel api.List[api.TopBoard]) []api.TopBoard {
	t.Helper()
	raw, err := json.Marshal(panel)
	if err != nil {
		t.Fatalf("marshalling the panel: %v", err)
	}
	var out struct {
		State  api.State       `json:"state"`
		Items  *[]api.TopBoard `json:"items"`
		Reason *api.Reason     `json:"reason"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding the panel: %v", err)
	}
	if out.Items == nil {
		return nil
	}
	return *out.Items
}

// Nothing installed to ask is `absent`, and it carries what the backends said
// rather than an empty board.
func TestTheTopPanelIsAbsentWhenThereIsNoBackendToAsk(t *testing.T) {
	cpu, memory, usage, _ := overview.TopPods(context.Background(), accesstest.Allow(), noMetricsServer(), "", nil)
	if usage.OK {
		t.Fatalf("a cluster with neither backend reported usage available: %s", usage.Detail)
	}

	panel := topPanel("test", cpu, memory, usage, false)
	if got := panel.State(); got != api.StateAbsent {
		t.Fatalf("state = %q, want %q", got, api.StateAbsent)
	}
	if panel.Len() != 0 {
		t.Errorf("a failed panel carries %d boards", panel.Len())
	}
	reason := panel.Reason()
	if reason == nil || reason.Code != api.CodeNotInstalled {
		t.Fatalf("reason = %+v, want %q", reason, api.CodeNotInstalled)
	}
	if reason.Detail == "" {
		t.Error("the backend's own words were dropped, so nobody can tell which one is missing")
	}
	if boards := boardsOf(t, panel); boards != nil {
		t.Errorf("items = %v on a failure; the member must be absent entirely", boards)
	}
}

// A backend that is configured and did not answer is not a backend that is not
// there. `absent` invites installing one; this invites asking again.
func TestTheTopPanelSaysUnreachableWhenAConfiguredBackendDidNotAnswer(t *testing.T) {
	metrics := promSource{err: errors.New("dial tcp: connection refused")}
	cpu, memory, usage, _ := overview.TopPods(context.Background(), accesstest.Allow(), noMetricsServer(), "", metrics)
	if usage.OK {
		t.Fatalf("a refused connection reported as available: %s", usage.Detail)
	}

	panel := topPanel("test", cpu, memory, usage, true)
	if got := panel.State(); got != api.StateUnreachable {
		t.Fatalf("state = %q, want %q", got, api.StateUnreachable)
	}
	if got := topPanel("test", cpu, memory, usage, false).State(); got != api.StateAbsent {
		t.Errorf("with no backend configured the same failure reads %q, want %q — the two must stay apart", got, api.StateAbsent)
	}
	reason := panel.Reason()
	if reason == nil || reason.Code != api.CodeClusterUnreachable {
		t.Fatalf("reason = %+v, want %q", reason, api.CodeClusterUnreachable)
	}
	if reason.Retryable == nil || !*reason.Retryable {
		t.Error("a backend that did not answer is worth asking again, and the panel does not say so")
	}
	if !strings.Contains(reason.Detail, "connection refused") ||
		!strings.Contains(reason.Detail, "metrics-server") {
		t.Errorf("detail = %q, want both backends' own words: reporting only the second sends "+
			"the operator to look at a Prometheus when metrics-server is what is missing", reason.Detail)
	}
}

// A backend that answered and ranked nobody is `empty`, with a reason. It is the
// only case where an empty leaderboard is the truth.
func TestTheTopPanelIsEmptyWhenTheBackendAnsweredAndRankedNobody(t *testing.T) {
	metrics := promSource{cpu: emptyVector, memory: emptyVector}
	cpu, memory, usage, _ := overview.TopPods(context.Background(), accesstest.Allow(), noMetricsServer(), "", metrics)
	if !usage.OK {
		t.Fatalf("an empty answer read as a failed one: %s", usage.Detail)
	}

	panel := topPanel("test", cpu, memory, usage, true)
	if got := panel.State(); got != api.StateEmpty {
		t.Fatalf("state = %q, want %q", got, api.StateEmpty)
	}
	if boards := boardsOf(t, panel); len(boards) != 0 {
		t.Errorf("boards = %v, want none", boards)
	}
	if panel.Reason() == nil {
		t.Error("nothing says why the leaderboard is empty, which is the one thing an empty one owes")
	}
}

// The Prometheus fallback is not interchangeable with metrics-server: a cluster
// that has one and not the other is the commonest shape kube-prometheus-stack
// produces, and it is exactly where this panel could answer `ok` with nothing.
func TestTheTopPanelRanksWhatPrometheusAnswered(t *testing.T) {
	metrics := promSource{
		cpu:    vector("kube-system", "coredns-1", "0.25"),
		memory: vector("default", "api-7f", "1073741824"),
	}
	cpu, memory, usage, _ := overview.TopPods(context.Background(), accesstest.Allow(), noMetricsServer(), "", metrics)
	if !usage.OK {
		t.Fatalf("Prometheus answered and the panel says otherwise: %s", usage.Detail)
	}

	panel := topPanel("test", cpu, memory, usage, true)
	if got := panel.State(); got != api.StateOK {
		t.Fatalf("state = %q, want %q", got, api.StateOK)
	}
	boards := boardsOf(t, panel)
	if len(boards) != 2 {
		t.Fatalf("boards = %d, want one for CPU and one for memory: %+v", len(boards), boards)
	}
	if boards[0].Label != "Hottest pods, CPU" || boards[1].Label != "Hottest pods, memory" {
		t.Errorf("labels = %q / %q", boards[0].Label, boards[1].Label)
	}
	if len(boards[0].Entries) != 1 {
		t.Fatalf("CPU entries = %d, want 1", len(boards[0].Entries))
	}
	entry := boards[0].Entries[0]
	if entry.Reading.Value == nil || *entry.Reading.Value != 250 {
		t.Errorf("CPU reading = %v, want 250 — Prometheus counts cores, this panel millicores", entry.Reading.Value)
	}
	if entry.Reading.Text != "250m" {
		t.Errorf("CPU text = %q, want %q", entry.Reading.Text, "250m")
	}
	if entry.Ref.Text != "kube-system/coredns-1" {
		t.Errorf("ref text = %q, want the pod with its namespace", entry.Ref.Text)
	}
	if !strings.Contains(entry.Ref.Href, "coredns-1") {
		t.Errorf("ref href = %q, want the pod's own page", entry.Ref.Href)
	}
	if memoryEntries := boards[1].Entries; len(memoryEntries) != 1 ||
		memoryEntries[0].Reading.Value == nil || *memoryEntries[0].Reading.Value != float64(1<<30) {
		t.Errorf("memory board = %+v, want the 1 GiB Prometheus named", boards[1].Entries)
	}
}

// The one that pins the failure itself: an `ok` panel about a backend nothing
// answered for. Boards are handed in deliberately — the availability is what
// decides, and it is the only thing that can.
func TestTheTopPanelNeverAnswersOKForABackendThatDidNotAnswer(t *testing.T) {
	ranked := overview.Top{Pods: []overview.PodUsage{{Namespace: "default", Name: "api", CPUMilli: 900}}}

	for _, configured := range []bool{true, false} {
		panel := topPanel("test", ranked, ranked,
			overview.Availability{Detail: "metrics-server did not answer"}, configured)
		if panel.State() == api.StateOK || panel.State() == api.StateEmpty {
			t.Errorf("configured=%v: state = %q — the panel answered for a backend that did not",
				configured, panel.State())
		}
		if panel.Len() != 0 {
			t.Errorf("configured=%v: %d boards drawn from an unavailable backend", configured, panel.Len())
		}
	}
}

// --- the whole payload --------------------------------------------------------

// The distinctions have to survive assembly, not only the helpers: this is the
// object the endpoint returns.
func TestTheOverviewPayloadKeepsRequestedUsedAndTheAbsentLeaderboard(t *testing.T) {
	out := overviewOf("test", measured(), overview.Top{}, overview.Top{},
		overview.Availability{Detail: "metrics-server did not answer"}, false)

	if out.Cluster != "test" {
		t.Errorf("cluster = %q", out.Cluster)
	}
	if got := out.Capacity.State(); got != api.StateOK {
		t.Fatalf("capacity panel = %q, want %q", got, api.StateOK)
	}
	if got := out.Top.State(); got != api.StateAbsent {
		t.Errorf("top panel = %q, want %q — nothing was installed to ask", got, api.StateAbsent)
	}
	// `counts` is not filled here: it holds the workload kind tiles, which come
	// from the second fetch. The two node figures are in the head's
	// `nodesSummary`, and the machines have a panel of their own. `TestAKindTileCarriesItsHealthyCountAndItsComplaint`
	// is where the tiles are pinned; what is asserted here is that this function
	// leaves them to the assembly instead of half-filling them.
	if got := out.Counts.Len(); got != 0 {
		t.Errorf("overviewOf filled %d counts; the kind tiles are overviewPanels' and come from the workloads fetch", got)
	}

	var payload struct {
		Capacity struct {
			Items []api.CapacityCell `json:"items"`
		} `json:"capacity"`
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshalling the overview: %v", err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decoding the overview: %v", err)
	}
	cpu := cellNamed(t, payload.Capacity.Items, "CPU")
	if cpu.Requested.Value == nil || cpu.Used == nil || cpu.Used.Value == nil {
		t.Fatalf("the payload lost a figure: requested=%v used=%v", cpu.Requested, cpu.Used)
	}
	if *cpu.Requested.Value == *cpu.Used.Value {
		t.Errorf("requested and used are both %v in the payload a client reads", *cpu.Used.Value)
	}
}

// A capacity line's hover divides the series' sample as it is. The CPU used
// figure is the Prometheus sample times 1000 and has a fraction; rounding it
// to whole millicores first would read `0.13` where the chooser, dividing the
// same value, reads `0.12`, and `0 / 8 cores` for 0.4m.
func TestACapacityHoverDividesTheSampleAsItIs(t *testing.T) {
	for _, c := range []struct {
		unit  overview.Unit
		used  float64
		total int64
		want  string
	}{
		{overview.UnitCores, 124.6, 8000, "0.12 / 8 cores"},
		{overview.UnitCores, 1249.7, 8000, "1.2 / 8 cores"},
		{overview.UnitCores, 0.397, 8000, "<0.01 / 8 cores"},
		{overview.UnitBytes, 0.125 * (1 << 30), 1 << 30, "0.13 / 1 GiB"},
		{overview.UnitCount, 12, 110, "12 / 110"},
		{overview.UnitCores, 500, 0, ""},
	} {
		if got := resourceFraction(c.unit, c.used, c.total); got != c.want {
			t.Errorf("resourceFraction(%v, %v, %d) = %q, want %q", c.unit, c.used, c.total, got, c.want)
		}
	}
}

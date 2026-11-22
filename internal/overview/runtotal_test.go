package overview

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The empty line carries the period its sentence names, as the key of the
// button the client draws beside it: an 8 s backup, 22 hours before an
// hour-long window, is held by 24h.
func TestARunBeforeTheWindowCarriesThePeriodThatHoldsIt(t *testing.T) {
	run := &Run{Start: at("2026-10-19T00:30:00Z"), End: at("2026-10-19T00:30:08Z")}
	got := emptyPanel(WindowFor("1h")).ExplainRun(run, runNow)
	for _, line := range []History{got.CPU, got.Memory, got.NetIn, got.NetOut} {
		if line.Period != "24h" {
			t.Errorf("%q carries period %q, want 24h", line.Unavailable, line.Period)
		}
	}
}

// A gauge stepped over at 24h carries the finer period its sentence names; a
// counter at the same period, and a run inside the window, name none and carry
// none; nor does a run older than every period.
func TestOnlyASentenceNamingAPeriodCarriesOne(t *testing.T) {
	skipped := &Run{Start: at("2026-10-19T17:30:00Z"), End: at("2026-10-19T17:30:25Z")}
	got := emptyPanel(WindowFor("24h")).ExplainRun(skipped, runNow)
	if got.Memory.Period != "6h" {
		t.Errorf("the memory line names 6h and carries %q", got.Memory.Period)
	}
	if got.CPU.Period != "" {
		t.Errorf("a counter with too few samples carries a period: %q", got.CPU.Period)
	}

	inside := &Run{Start: at("2026-10-19T22:10:00Z"), End: at("2026-10-19T22:10:08Z")}
	if got := emptyPanel(WindowFor("1h")).ExplainRun(inside, runNow); got.Memory.Period != "" || got.CPU.Period != "" {
		t.Errorf("a run inside the window carries a period: %q, %q", got.Memory.Period, got.CPU.Period)
	}

	old := &Run{Start: at("2026-10-01T00:30:00Z"), End: at("2026-10-01T00:30:08Z")}
	if got := emptyPanel(WindowFor("7d")).ExplainRun(old, runNow); got.CPU.Period != "" {
		t.Errorf("a run older than every period carries %q", got.CPU.Period)
	}
}

// runProm answers a range selector with the samples given per metric, and
// records what it was asked.
type runProm struct {
	answers map[string]string
	asked   []string
	fail    error
}

func (p *runProm) Query(_ context.Context, _, promQL string) ([]byte, error) {
	p.asked = append(p.asked, promQL)
	if p.fail != nil {
		return nil, p.fail
	}
	for metric, result := range p.answers {
		if strings.HasPrefix(promQL, metric+"{") {
			return []byte(`{"status":"success","data":{"resultType":"matrix","result":[` + result + `]}}`), nil
		}
	}
	return []byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`), nil
}

func (p *runProm) QueryRange(context.Context, string, string, time.Time, time.Time, time.Duration) ([]byte, error) {
	return nil, errors.New("not asked")
}

// matrixSeries is one series of a range selector's answer: its labels and its
// samples as [time, "value"].
func matrixSeries(labels string, values ...float64) string {
	samples := make([]string, len(values))
	for i, v := range values {
		samples[i] = fmt.Sprintf(`[%d,"%g"]`, 1792369800+30*i, v)
	}
	return `{"metric":{` + labels + `},"values":[` + strings.Join(samples, ",") + `]}`
}

const eth0 = `"__name__":"x","namespace":"backup","pod":"nightly-1","interface":"eth0","job":"kubelet","instance":"10.0.0.1:10250"`

func backupRun() *Run {
	return &Run{Start: at("2026-10-19T00:30:00Z"), End: at("2026-10-19T00:30:08Z")}
}

// One sample of the run, the single-sample case (an 8 s backup scraped every
// 30 s): the figure is the counter's value.
func TestARunWithOneSampleCountsTheCountersValue(t *testing.T) {
	prom := &runProm{answers: map[string]string{
		metricNetIn:  matrixSeries(eth0, 3<<20),
		metricNetOut: matrixSeries(eth0, 1024),
	}}
	got := emptyPanel(WindowFor("1h")).CountRun(context.Background(), prom, "prod", "backup", "nightly-1", backupRun())
	if got.NetIn.RunTotal == nil || got.NetIn.RunTotal.Value != 3<<20 || got.NetIn.RunTotal.Samples != 1 {
		t.Errorf("in = %+v, want 3 MiB from one sample", got.NetIn.RunTotal)
	}
	if got.NetOut.RunTotal == nil || got.NetOut.RunTotal.Value != 1024 {
		t.Errorf("out = %+v, want 1024", got.NetOut.RunTotal)
	}
	if got.CPU.RunTotal != nil || got.Memory.RunTotal != nil {
		t.Error("a line that is not the network's carries a run total")
	}
	// Every raw sample from the start to a minute after the end, at that end.
	want := `container_network_receive_bytes_total{namespace="backup",pod="nightly-1"}[68s] @ 1792369868`
	if prom.asked[0] != want {
		t.Errorf("asked %q, want %q", prom.asked[0], want)
	}
}

// Two samples: the figure is the increase between them, not the last value,
// and not PromQL's extrapolated increase().
func TestARunWithTwoSamplesCountsTheIncrease(t *testing.T) {
	prom := &runProm{answers: map[string]string{metricNetIn: matrixSeries(eth0, 1000, 5000)}}
	got := emptyPanel(WindowFor("1h")).CountRun(context.Background(), prom, "prod", "backup", "nightly-1", backupRun())
	if got.NetIn.RunTotal == nil || got.NetIn.RunTotal.Value != 4000 || got.NetIn.RunTotal.Samples != 2 {
		t.Errorf("in = %+v, want 4000 from two samples", got.NetIn.RunTotal)
	}
	if got.NetOut.RunTotal != nil {
		t.Errorf("a counter with no sample of the run carries %+v", got.NetOut.RunTotal)
	}
}

// The reduction is the panel's own: interfaces summed, a series scraped twice
// averaged first (dedup), and a reset counted from zero.
func TestARunTotalSumsInterfacesOnceAndCountsAReset(t *testing.T) {
	twice := strings.Replace(eth0, `"job":"kubelet"`, `"job":"kubelet-2"`, 1)
	net1 := strings.Replace(eth0, `"interface":"eth0"`, `"interface":"net1"`, 1)
	prom := &runProm{answers: map[string]string{metricNetIn: matrixSeries(eth0, 100, 300) + "," +
		matrixSeries(twice, 100, 300) + "," + matrixSeries(net1, 900, 50, 80)}}
	got := emptyPanel(WindowFor("1h")).CountRun(context.Background(), prom, "prod", "backup", "nightly-1", backupRun())
	// eth0 counted 200 (twice, averaged), net1 reset and counted 50 then 30.
	if got.NetIn.RunTotal == nil || got.NetIn.RunTotal.Value != 280 {
		t.Errorf("in = %+v, want 280", got.NetIn.RunTotal)
	}
}

// Only an empty line from a backend that answered, and only of a finished pod,
// is counted; a failed query leaves no figure.
func TestOnlyAFinishedPodsEmptyNetworkLineIsCounted(t *testing.T) {
	prom := &runProm{answers: map[string]string{metricNetIn: matrixSeries(eth0, 1)}}
	panel := emptyPanel(WindowFor("1h"))
	panel.NetIn.Missing = MissingCounter
	if got := panel.CountRun(context.Background(), prom, "prod", "backup", "nightly-1", backupRun()); got.NetIn.RunTotal != nil {
		t.Error("a line the cluster cannot answer for was counted")
	}
	if got := emptyPanel(WindowFor("1h")).CountRun(context.Background(), prom, "prod", "backup", "nightly-1", nil); got.NetIn.RunTotal != nil || len(prom.asked) != 1 {
		t.Errorf("a running pod was counted, or asked for (%d queries)", len(prom.asked))
	}
	failing := &runProm{fail: errors.New("connection refused")}
	if got := emptyPanel(WindowFor("1h")).CountRun(context.Background(), failing, "prod", "backup", "nightly-1", backupRun()); got.NetIn.RunTotal != nil {
		t.Error("a failed query left a figure")
	}
}

// A counter's line draws a rate, and a rate needs two samples: a run that left
// one draws nothing at any period, so the period its sentence names is dropped
// rather than offered as a button that brings back the same empty chart. A run
// that left two keeps it, because that period draws it.
func TestARunWithOneSampleOffersNoPeriod(t *testing.T) {
	prom := &runProm{answers: map[string]string{
		metricNetIn:  matrixSeries(eth0, 3<<20),
		metricNetOut: matrixSeries(eth0, 1000, 5000),
	}}
	got := emptyPanel(WindowFor("1h")).ExplainRun(backupRun(), runNow).
		CountRun(context.Background(), prom, "prod", "backup", "nightly-1", backupRun())
	if got.NetIn.Period != "" {
		t.Errorf("a one-sample run offers period %q", got.NetIn.Period)
	}
	if got.NetOut.Period != "24h" {
		t.Errorf("a two-sample run offers period %q, want 24h", got.NetOut.Period)
	}
}

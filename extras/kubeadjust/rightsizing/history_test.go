package rightsizing

import (
	"strings"
	"testing"
)

// The usage window: what the page has to be able to say about it.
//
// The reading of the window - what is asked of Prometheus, how one answer is
// taken apart, the cap, the namespace selector, NaN and duplicate scrapes - is
// the core's, behind a typed Host method, and tested there; the test that runs
// the whole path is the extra's. What is here is the page's half: HistoryNote,
// noWindow, the coverage count, and what the engine does with a partial window.

// windowRead is the state a usage window that was actually read hands this
// package: OK, and named with its window and its step. It is written down here,
// because building it is not this package's job.
var windowRead = Availability{OK: true, Detail: "Prometheus, 7d at 15m"}

// --- the four states -------------------------------------------------------

// The page carries one sentence about the window, and it is the most important
// sentence on it: it says whether the numbers below rest on a week or on one
// instant. Four states and not two — "no Prometheus" and "Prometheus said
// nothing" are different failures, and a partial window is not the same success
// as a full one. Rendering any of them as an empty window would draw what
// nothing measured as zero.
func TestTheHistoryNoteSaysWhichModeThePageIsIn(t *testing.T) {
	cases := []struct {
		name     string
		state    Availability
		wantOK   bool
		contains []string
	}{
		// The states with no window (no Prometheus, a Prometheus with no
		// container series cluster-wide or for one namespace, a refused query)
		// are sentences the core writes, and tested there.
		{
			// 4. A full window, named with its window and its step so the reader
			// can tell what "P95" is a percentile of.
			name:     "a window covering the whole page",
			state:    HistoryNote(windowRead, 12, 12, 12),
			wantOK:   true,
			contains: []string{"Prometheus", "7d", "15m", "a window for all 12 containers", "high confidence"},
		},
		{
			// 3. A partial window, which is the ordinary state rather than an edge
			// case: a pod started four minutes ago has no series yet. It is
			// named because a container with no usable series gets no value,
			// and the page has to say why some rows have one and others not.
			name:     "a window covering part of the page",
			state:    HistoryNote(windowRead, 8, 8, 12),
			wantOK:   true,
			contains: []string{"7d", "15m", "8 of 12 containers have a window", "other 4 have none of their own", "no recommended value"},
		},
		{
			// 3b. The asymmetric partial, which is what a pod three minutes old
			// looks like: `container_memory_working_set_bytes` is a gauge and is
			// there on the first scrape, `rate(...[5m])` is not. Counting the two
			// together hid this — see windowedContainers.
			name:     "a memory window wider than the CPU one",
			state:    HistoryNote(windowRead, 7, 12, 12),
			wantOK:   true,
			contains: []string{"of 12 containers, 7 have a CPU window and 12 a memory window"},
		},
		{
			// The degenerate partial: a window whose series match nothing on the
			// page. Reported as no window, because that is what it is.
			name:     "a window matching nothing on the page",
			state:    HistoryNote(windowRead, 0, 0, 12),
			contains: []string{"none of its series matched a container on this page", "single sample per replica"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.state.OK != tc.wantOK {
				t.Errorf("OK = %v, want %v — the page will draw this state in the wrong color: %q",
					tc.state.OK, tc.wantOK, tc.state.Detail)
			}
			for _, want := range tc.contains {
				if !strings.Contains(tc.state.Detail, want) {
					t.Errorf("the History note does not say %q, so the reader cannot tell this "+
						"state from the others: %q", want, tc.state.Detail)
				}
			}
		})
	}
}

// Every state that produces no window has to say what it costs. "No history" on
// its own reads as a missing nicety rather than as the reason every number
// below is one instant.
func TestEveryStateWithoutAWindowSaysWhatThatCosts(t *testing.T) {
	// The core's seven no-window states are tested there.
	states := []Availability{
		HistoryNote(Availability{Detail: noWindow("nothing")}, 0, 0, 40),
		HistoryNote(windowRead, 0, 0, 12),
	}
	for _, state := range states {
		if state.OK {
			t.Errorf("a state with no window reports OK: %q", state.Detail)
			continue
		}
		for _, want := range []string{"no P95", "no burst ratio", "no trend"} {
			if !strings.Contains(state.Detail, want) {
				t.Errorf("this no-window note does not say %q, so the page implies its numbers "+
					"are something they are not: %q", want, state.Detail)
			}
		}
	}
}

// --- a window that covers some replicas and not others ---------------------

// The partial window is the ordinary state, not an edge case: a pod started
// four minutes ago has no series yet, and a Prometheus that dropped
// samples has none for whoever it dropped them for. Wiring a window is what
// makes this reachable at all.
//
// Once two windowed points exist, the pooled values are what mean, P95 and max
// are taken over — so a replica with no series would be invisible to the engine
// while its own bar, drawn from the same snapshot, shows it plainly. A replica
// at 950m the engine cannot see is a reduction suggested past a number on the
// same screen.
func TestAReplicaWithNoWindowIsStillVisibleToTheEngine(t *testing.T) {
	quiet := ctr("app", opts{cpuReq: 2000, cpuLim: 4000, cpuUse: 100, memUse: 1})
	busy := ctr("app", opts{cpuReq: 2000, cpuLim: 4000, cpuUse: 950, memUse: 1})
	pods := []Pod{testPod("pod-1", quiet), testPod("pod-2", busy)}

	// Only pod-1 has a window, and it is flat and quiet.
	hist := []ContainerHistory{{Pod: "pod-1", Container: "app",
		CPU: series(180, func(int) float64 { return 100 })}}

	got := Readings([]Workload{workloadOf("app", pods, "", "")}, hist)
	if len(got) != 1 || len(got[0].Containers) != 1 {
		t.Fatalf("expected one container reading, got %+v", got)
	}
	peak := got[0].Containers[0].CPU.Peak

	if peak < 950 {
		t.Errorf("peak CPU = %dm, want at least 950m — the replica with no usage window has "+
			"been left out of the pool, so the engine's peak is lower than a figure the page "+
			"draws for that same replica two lines away, and every limit suggestion and the "+
			"burst guard are sized from this number", peak)
	}

	// And the quiet replica still counts: pooling the busy one alone would make
	// the workload look uniformly busy, which is the opposite mistake.
	if mean := got[0].Containers[0].CPU.Mean; mean >= 900 {
		t.Errorf("mean CPU = %dm, want the windowed replica's 100m to dominate — one snapshot "+
			"must not be weighted as a distribution", mean)
	}
}

// The other side of the same decision, and the one that would be easy to break
// while fixing the first: a workload of two replicas with **no** window has two
// snapshots, and reading those two numbers as a distribution would conjure a
// P95, a burst ratio and a confidence label out of one instant per replica.
// That is the failure this whole file exists to stop, arrived at backwards.
func TestSnapshotsAloneAreNeverReadAsAWindow(t *testing.T) {
	c := ctr("app", opts{cpuReq: 2000, cpuLim: 4000, cpuUse: 100, memUse: 1})
	pods := []Pod{testPod("pod-1", c), testPod("pod-2", c)}

	s, ok := find(Compute([]Workload{workloadOf("app", pods, "", "")}, nil, nil),
		byResourceAction("CPU", "Reduce request"))
	if !ok {
		t.Fatal("no 'Reduce request' produced, so the caveat cannot be checked")
	}
	if !hasWarning(s, "single scrape") {
		t.Errorf("a suggestion built from one scrape per replica carries no single-scrape "+
			"caveat (%q) — two snapshots have been read as a distribution and the row now "+
			"claims a confidence it has no window for", s.Warnings)
	}
	if strings.Contains(s.Message, "confidence") {
		t.Errorf("the message claims a confidence label with no window behind it: %q", s.Message)
	}
}

// The coverage sentence's denominator is the page's, not Prometheus's. It counts
// containers the engine will actually look at — one per replica, and only those
// something measured — because a container nothing measured is skipped before
// its history is consulted, and counting it would leave the page permanently
// reporting a partial window about containers no suggestion was ever going to
// be made about.
func TestTheCoverageCountIsPerReplicaAndOnlyOfWhatWasMeasured(t *testing.T) {
	measured := ctr("app", opts{cpuReq: 500, cpuUse: 100, memUse: 1})
	unmeasured := ctr("sidecar", opts{cpuReq: 100})
	pods := []Pod{
		testPod("pod-1", measured, unmeasured),
		testPod("pod-2", measured, unmeasured),
	}
	hist := BuildHistoryMap([]ContainerHistory{{Pod: "pod-1", Container: "app",
		CPU: series(180, func(int) float64 { return 100 })}})

	cpuWindow, memWindow, total := windowedContainers([]Workload{workloadOf("app", pods, "", "")}, hist)
	if memWindow != 0 {
		t.Errorf("memWindow = %d, want 0 — the fixture gives pod-1 a CPU series and no memory "+
			"series, and counting the two together is what hid a CPU-only window", memWindow)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2 — the denominator counts one container per replica, and "+
			"only the ones something measured", total)
	}
	if cpuWindow != 1 {
		t.Errorf("cpuWindow = %d, want 1 — only pod-1 has a series", cpuWindow)
	}

	// A series of one sample is a snapshot with a timestamp on it, and
	// usageStats needs two before it treats it as a window. The two have to
	// agree, or the note says a container is windowed while the engine does not.
	oneSample := BuildHistoryMap([]ContainerHistory{{Pod: "pod-1", Container: "app",
		CPU: series(1, func(int) float64 { return 100 })}})
	if got, _, _ := windowedContainers([]Workload{workloadOf("app", pods, "", "")}, oneSample); got != 0 {
		t.Errorf("cpuWindow = %d for a one-sample series, want 0 — the note would report a "+
			"window the engine refuses to use", got)
	}
}

package rightsizing

import "fmt"

// What the page says about the usage window.
//
// Reading the window - the three PromQL queries, the seven-day range at a
// fifteen-minute step, the container cap asked for first, and the parsing
// that drops a NaN rather than reading it as zero - is the core's, behind a
// typed Host method filtered to the person's pods, because Prometheus has
// no RBAC and a raw query on the contract would let any extra read any
// series for anyone.
//
// What stays here is the part that is about this page: the sentence every state
// without a window carries, and the coverage sentence, whose denominator is the
// page's and not Prometheus's.

// noWindow is the one sentence this page must never lose. Every state that
// produces no window says what happened *and* what it costs, because "no
// history" on its own reads as a missing nicety rather than as the reason every
// number below is a single sample.
func noWindow(reason string) string {
	return reason + " — so every suggestion below rests on a single sample per replica: " +
		"there is no P95, no burst ratio and no trend"
}

// HistoryNote finishes the History sentence once the page knows how many
// containers are on it.
//
// Separated from ReadHistory because the denominator is not Prometheus's
// answer, it is the page's: a window covering 40 containers is complete on a
// page showing 40 and two thirds of a page showing 60. The partial case has to
// be named, because a replica with no series contributes only its snapshot to
// the pooled statistics — see usageStats — so a page that is quietly two thirds
// windowed is a page whose confidence suffix is right about the containers it
// covers and silent about the rest.
//
// CPU and memory are counted apart because they are routinely different: a
// gauge is there on the first scrape and `rate()` over a five-minute window is
// not, so a pod three minutes old has a memory window and no CPU one. See
// windowedContainers, where an OR across the two was found hiding exactly that.
func HistoryNote(state Availability, cpuWindow, memWindow, total int) Availability {
	if !state.OK || total == 0 {
		return state
	}
	switch {
	case cpuWindow == 0 && memWindow == 0:
		// A window whose series match nothing on the page is not a window. Said
		// as such, with what was read kept in front of it — a whole failed query
		// on one side puts "CPU only" or "memory only" in the detail, and losing
		// that would turn two distinguishable failures back into one.
		return Availability{Detail: state.Detail + " · none of its series matched a container on " +
			"this page — so every suggestion below rests on a single sample per replica: " +
			"there is no P95, no burst ratio and no trend"}
	case cpuWindow == total && memWindow == total:
		state.Detail += fmt.Sprintf(" · a window for all %d containers", total)
	case cpuWindow == memWindow:
		// With a window on the page, a replica without one of its own is
		// sized from its siblings' series, and a container with none at all
		// is not sized: one scrape beside a week is not a figure (confidence.go).
		state.Detail += fmt.Sprintf(" · %d of %d containers have a window; the other %d have none "+
			"of their own", cpuWindow, total, total-cpuWindow)
	default:
		// The asymmetric case, spelled out rather than averaged: which half of a
		// row has no series of its own is the thing the reader needs.
		state.Detail += fmt.Sprintf(" · of %d containers, %d have a CPU window and %d a memory "+
			"window; the rest have none of their own", total, cpuWindow, memWindow)
	}
	// How much a figure needs, said once: the level on each row is derived
	// from it, and a row with no value is explained by it.
	state.Detail += " · " + windowCaveat
	return state
}

// windowCaveat is what the levels on the rows mean.
const windowCaveat = "each figure says how much of the window it rests on: a week of usable series is high " +
	"confidence, a day to a week medium, and a container with under a day - or none - gets no recommended value"

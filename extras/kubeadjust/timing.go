package kubeadjust

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// timing is where one request's time went, phase by phase, sent back as a
// `Server-Timing` header: a browser's network panel draws it beside the
// request, and a `curl -D -` prints it, so a slow display is split into its
// reads and its arithmetic without a profiler. Only durations and fixed
// phase names cross: nothing about the cluster.
type timing struct {
	names []string
	durs  []time.Duration
}

// since records the phase name as the time elapsed since start, and returns
// now, the next phase's start.
func (t *timing) since(name string, start time.Time) time.Time {
	now := time.Now()
	t.add(name, now.Sub(start))
	return now
}

// add records a phase whose duration was measured elsewhere. A phase named
// twice is summed.
func (t *timing) add(name string, d time.Duration) {
	for i, n := range t.names {
		if n == name {
			t.durs[i] += d
			return
		}
	}
	t.names = append(t.names, name)
	t.durs = append(t.durs, d)
}

// header is the Server-Timing value: `usage;dur=12.3, window;dur=4.0`, in
// milliseconds, in the order the phases were recorded.
func (t *timing) header() string {
	parts := make([]string, len(t.names))
	for i, n := range t.names {
		parts[i] = n + ";dur=" + strconv.FormatFloat(float64(t.durs[i].Microseconds())/1000, 'f', 1, 64)
	}
	return strings.Join(parts, ", ")
}

// set writes the header, when anything was recorded.
func (t *timing) set(w http.ResponseWriter) {
	if len(t.names) > 0 {
		w.Header().Set("Server-Timing", t.header())
	}
}

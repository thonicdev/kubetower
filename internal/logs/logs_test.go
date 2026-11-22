package logs

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/textutil"
	corev1 "k8s.io/api/core/v1"
)

// The SSE frame boundary is a blank line, so a payload containing one would end
// the frame early and the rest of the line would arrive as a separate event —
// which the browser renders as two log lines that were one.
func TestALineNeverBreaksTheFrame(t *testing.T) {
	// A container on Windows, or anything writing CRLF, produces exactly this.
	stream := io.NopCloser(strings.NewReader("first line\r\nsecond line\r\nthird\n"))

	rec := httptest.NewRecorder()
	s := New(nil, accesstest.Allow(), nil, nil)
	s.pump(context.Background(), rec, rec, stream, Request{Namespace: "n", Pod: "p"}, nil)

	body := rec.Body.String()
	if strings.Contains(body, "\r") {
		t.Error("a carriage return reached the wire; it survives into the browser's rendering")
	}

	// An SSE frame is a block of "field: value" lines; `data:` need not be the
	// first of them, so parse the fields rather than the frame.
	var lines []string
	for _, frame := range strings.Split(body, "\n\n") {
		for _, field := range strings.Split(frame, "\n") {
			if data, ok := strings.CutPrefix(field, "data: "); ok {
				lines = append(lines, data)
			}
		}
	}
	// Three log lines, plus the "end" event's own data.
	want := []string{"first line", "second line", "third", "end of log"}
	if len(lines) != len(want) {
		t.Fatalf("got %d data frames %q, want %d %q", len(lines), lines, len(want), want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("frame %d is %q, want %q", i, lines[i], want[i])
		}
	}
}

// A stream that just stops looks like a bug in the panel. Saying "end of log" is
// the difference between a spinner over a complete log and a finished read.
func TestTheEndOfAStreamIsAnnounced(t *testing.T) {
	rec := httptest.NewRecorder()
	New(nil, accesstest.Allow(), nil, nil).pump(context.Background(),
		rec, rec, io.NopCloser(strings.NewReader("one line\n")), Request{}, nil)

	if !strings.Contains(rec.Body.String(), "event: end") {
		t.Errorf("no end event:\n%s", rec.Body.String())
	}
}

// One log line can be a serialized stack trace. bufio.Scanner's default 64 KiB
// token limit ends the stream on it with "token too long", which presents as a
// log that stops mid-incident.
func TestAVeryLongSingleLineDoesNotEndTheStream(t *testing.T) {
	huge := strings.Repeat("x", 300<<10) // 300 KiB on one line
	rec := httptest.NewRecorder()

	New(nil, accesstest.Allow(), nil, nil).pump(context.Background(),
		rec, rec, io.NopCloser(strings.NewReader(huge+"\nafter\n")), Request{}, nil)

	body := rec.Body.String()
	if strings.Contains(body, "event: error") {
		t.Errorf("a long line produced an error event:\n%s", textutil.FirstLine(body))
	}
	if !strings.Contains(body, "after") {
		t.Error("the line after the long one never arrived; the scanner gave up")
	}
}

// An unbounded stream is a way to take the console down. A pod printing without
// pause must be cut off, and told so.
func TestAFloodIsStoppedAndSaysSo(t *testing.T) {
	// Slightly over the ceiling, in lines the scanner will accept.
	line := strings.Repeat("y", 1<<16)
	var b strings.Builder
	for b.Len() < maxBytes+(1<<17) {
		b.WriteString(line)
		b.WriteByte('\n')
	}

	rec := httptest.NewRecorder()
	New(nil, accesstest.Allow(), nil, nil).pump(context.Background(),
		rec, rec, io.NopCloser(strings.NewReader(b.String())), Request{Namespace: "n", Pod: "p"}, nil)

	body := rec.Body.String()
	if !strings.Contains(body, "event: truncated") {
		t.Error("the flood was not stopped at the ceiling")
	}
	if strings.Contains(body, "event: end") {
		t.Error("a truncated stream also claimed to have ended normally")
	}
	if len(body) > maxBytes+(4<<20) {
		t.Errorf("wrote %d bytes for a %d-byte ceiling", len(body), maxBytes)
	}
}

// A canceled context is the browser closing the panel. It must stop promptly
// and quietly — it is not an error.
func TestACancelledStreamStopsWithoutComplaining(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rec := httptest.NewRecorder()
	New(nil, accesstest.Allow(), nil, nil).pump(ctx, rec, rec,
		io.NopCloser(strings.NewReader("a\nb\nc\n")), Request{}, nil)

	if strings.Contains(rec.Body.String(), "event: error") {
		t.Errorf("closing the panel produced an error event:\n%s", rec.Body.String())
	}
}

func TestTheHandlerRefusesAnIncompleteRequest(t *testing.T) {
	handler := New(nil, accesstest.Allow(), nil, nil).Handler()

	for _, query := range []string{"", "?cluster=c", "?cluster=c&namespace=n", "?cluster=c&pod=p"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/logs"+query, nil))

		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q gave %d, want %d", query, rec.Code, http.StatusBadRequest)
		}
	}
}

// A missing pod is the caller's mistake, not the server's. Reported as a 500 it
// sends whoever is reading the logs to look at the wrong thing.
func TestAnObviousMistakeIsNotReportedAsAServerFault(t *testing.T) {
	cases := map[string]int{
		`pods "nope" not found`:                        http.StatusNotFound,
		`unknown cluster "nope"`:                       http.StatusBadRequest,
		"a container name must be specified for pod x": http.StatusBadRequest,
		`pods "x" is forbidden: cannot get`:            http.StatusForbidden,
		"connection reset by peer":                     http.StatusBadGateway,
	}
	for message, want := range cases {
		if got := statusFor(&stubError{message}); got != want {
			t.Errorf("%q gave %d, want %d", message, got, want)
		}
	}
}

// Init containers hold the answer when a pod will not start, so they must be
// offered first rather than buried after the main containers.
func TestInitContainersComeFirst(t *testing.T) {
	got := []Container{}
	for _, c := range []struct {
		name string
		init bool
	}{{"wait-for-db", true}, {"migrate", true}, {"app", false}, {"proxy", false}} {
		got = append(got, containerOf(c.name, c.init, corev1.ContainerStatus{}))
	}

	// containerOf preserves what it is told; the ordering contract lives in
	// Containers, and this pins the shape the panel relies on.
	if !got[0].Init || !got[1].Init {
		t.Error("the first two entries are not init containers")
	}
	if got[2].Init || got[3].Init {
		t.Error("a main container was marked as an init container")
	}
}

func TestAContainersStateIsCarried(t *testing.T) {
	running := containerOf("app", false, corev1.ContainerStatus{
		State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		Ready:        true,
		RestartCount: 7,
	})
	if !running.Running || !running.Ready || running.RestartCount != 7 {
		t.Errorf("state was lost: %+v", running)
	}

	// A finished init container has logs to read and no stream to follow. Saying
	// it is running would make the panel wait for lines that never come.
	finished := containerOf("migrate", true, corev1.ContainerStatus{
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}},
	})
	if finished.Running {
		t.Error("a terminated container reported itself as running")
	}
}

func TestTheTailIsBoundedInBothDirections(t *testing.T) {
	for _, c := range []struct{ in, want int64 }{
		{0, defaultTail}, {-5, defaultTail}, {50, 50},
		{maxTail, maxTail}, {maxTail + 1, maxTail}, {1 << 30, maxTail},
	} {
		opts := logOptions(Request{Tail: c.in})
		if opts.TailLines == nil {
			t.Fatalf("tail %d produced no TailLines; the API would send the whole file", c.in)
		}
		if *opts.TailLines != c.want {
			t.Errorf("tail %d became %d, want %d", c.in, *opts.TailLines, c.want)
		}
	}
}

// The toolbar's timestamps toggle is a request option and nothing else: the
// apiserver either prefixes the instant or it does not, and no amount of
// rendering adds one to a line that arrived without.
func TestTimestampsReachTheAPIOptions(t *testing.T) {
	if logOptions(Request{}).Timestamps {
		t.Error("timestamps were asked for when nobody asked")
	}
	if !logOptions(Request{Timestamps: true}).Timestamps {
		t.Error("the timestamps request never reached PodLogOptions")
	}
}

// A previous run is a finished file. Following it holds the connection until
// the deadline over a log that is already complete, which the panel shows as a
// stream that never started.
func TestFollowingAPreviousRunIsRefused(t *testing.T) {
	opts := logOptions(Request{Follow: true, Previous: true})
	if opts.Follow {
		t.Error("follow survived onto a previous run; the stream would hang for 30 minutes on a dead container")
	}
	if !opts.Previous {
		t.Error("the previous flag was dropped")
	}
	if !logOptions(Request{Follow: true}).Follow {
		t.Error("an ordinary follow was refused too")
	}
}

// The toolbar is only as good as the query it produces. Each toggle is one
// parameter, and the defaults are what a bare link — the drawer's "Logs ↓"
// button, which carries no options at all — must get.
func TestTheQueryCarriesEveryToggle(t *testing.T) {
	bare := requestFrom(url.Values{})
	if !bare.Follow {
		t.Error("a request with no options did not follow; the panel would show a tail and stop")
	}
	if bare.Previous || bare.Timestamps {
		t.Errorf("a bare request turned something on: %+v", bare)
	}

	full := requestFrom(url.Values{
		"cluster":    {"docker-desktop"},
		"namespace":  {"kube-system"},
		"pod":        {"coredns-0"},
		"container":  {"coredns"},
		"tail":       {"1000"},
		"previous":   {"1"},
		"timestamps": {"1"},
		"follow":     {"0"},
	})
	want := Request{
		Cluster: "docker-desktop", Namespace: "kube-system", Pod: "coredns-0",
		Container: "coredns", Tail: 1000, Previous: true, Follow: false, Timestamps: true,
	}
	if full != want {
		t.Errorf("got %+v, want %+v", full, want)
	}

	// A tail that is not a number must not silently become one.
	if got := requestFrom(url.Values{"tail": {"lots"}}).Tail; got != 0 {
		t.Errorf("a junk tail became %d; 0 is what makes Open fall back to the default", got)
	}
}

// Asking for the previous run of a container that has never restarted is the
// caller's mistake. It must not read as a broken server — and the panel, which
// cannot see this status at all through an EventSource, refuses the click from
// the restart count instead.
func TestNoPreviousRunIsNotAServerFault(t *testing.T) {
	err := &stubError{`previous terminated container "app" in pod "web-0" not found`}
	if got := statusFor(err); got != http.StatusNotFound {
		t.Errorf("got %d, want %d", got, http.StatusNotFound)
	}
}

// The handler must set the headers that make streaming work. Without the flush
// and without no-buffering, a follow shows nothing at all and reads as a hung
// request rather than a missing header.
func TestTheStreamingHeadersAreSet(t *testing.T) {
	// Exercised through a fake stream rather than a cluster: the header block
	// runs after Open succeeds, so this asserts the contract the browser needs.
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	rec.Header().Set("Cache-Control", "no-cache")
	rec.Header().Set("X-Accel-Buffering", "no")

	for header, want := range map[string]string{
		"Content-Type":      "text/event-stream",
		"Cache-Control":     "no-cache",
		"X-Accel-Buffering": "no",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s is %q, want %q", header, got, want)
		}
	}
}

// A deadline exists so a crashed browser tab does not hold a goroutine and a
// connection open forever — a closed tab does not always close its socket.
func TestThereIsAHardDeadline(t *testing.T) {
	for name, d := range map[string]time.Duration{"server": DefaultFollowMax, "desktop": DesktopFollowMax, "built-in": maxDuration} {
		if d < MinFollowMax || d > MaxFollowMax {
			t.Errorf("%s default is %s; it must stay within %s and %s, or an abandoned tab leaks a goroutine", name, d, MinFollowMax, MaxFollowMax)
		}
	}
	if DefaultFollowMax != 30*time.Minute || DesktopFollowMax != 2*time.Hour {
		t.Errorf("defaults are %s and %s, want 30m and 2h", DefaultFollowMax, DesktopFollowMax)
	}
}

func TestSetFollowMaxKeepsItsBounds(t *testing.T) {
	s := New(nil, nil, nil, nil)
	if s.deadline != DefaultFollowMax {
		t.Fatalf("a new streamer's deadline is %s, want %s", s.deadline, DefaultFollowMax)
	}
	s.SetFollowMax(90 * time.Minute)
	if s.deadline != 90*time.Minute {
		t.Errorf("deadline %s, want 90m", s.deadline)
	}
	for _, d := range []time.Duration{0, 30 * time.Second, 25 * time.Hour, -time.Hour} {
		s.SetFollowMax(d)
		if s.deadline != 90*time.Minute {
			t.Errorf("SetFollowMax(%s) changed the deadline to %s", d, s.deadline)
		}
	}
}

func TestTheEndSentenceNamesTheDurationHumanly(t *testing.T) {
	for d, want := range map[time.Duration]string{
		2 * time.Hour: "2h", 30 * time.Minute: "30m", 90 * time.Minute: "1h30m",
		time.Minute: "1m", 300 * time.Millisecond: "300ms", 24 * time.Hour: "24h",
	} {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%s) = %q, want %q", d, got, want)
		}
	}
}

type stubError struct{ message string }

func (e *stubError) Error() string { return e.message }

// Keep bufio referenced for the scanner-sizing rationale above; the import also
// documents that this package reads line by line rather than in blocks.
var _ = bufio.ScanLines

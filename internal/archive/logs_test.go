package archive

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The point of archiving logs at all: the pod is gone, and its logs are the
// reason it went.
func TestLogsSurviveThePodAndReadBackInOrder(t *testing.T) {
	a, c := newArchive(t, on())

	sink := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	for _, line := range []string{"starting", "connected to the database", "panic: nil map"} {
		if _, err := sink.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		c.add(time.Second)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines, err := a.Log(LogQuery{Cluster: "prod-eks-1", Namespace: "prod", Pod: "api-0", Container: "api"})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("%d lines, want 3: %+v", len(lines), lines)
	}
	if lines[0].Text != "starting" || lines[2].Text != "panic: nil map" {
		t.Errorf("read back %q … %q, want them oldest first", lines[0].Text, lines[2].Text)
	}
	if lines[0].At.After(lines[2].At) {
		t.Error("the timestamps are not increasing")
	}
	if lines[2].At.Sub(lines[0].At) != 2*time.Second {
		t.Errorf("the lines are %v apart, want the 2s the clock moved", lines[2].At.Sub(lines[0].At))
	}
}

// A log stream arrives in whatever chunks the network produced. A line split
// across two writes is one line, and a write holding three is three.
func TestChunksAreReassembledIntoLines(t *testing.T) {
	a, _ := newArchive(t, on())
	sink := a.LogWriter("prod-eks-1", "prod", "api-0", "api")

	sink.Write([]byte("one\ntw"))
	sink.Write([]byte("o\nthree\nfo"))
	sink.Write([]byte("ur\n"))
	sink.Write([]byte("five without a newline"))
	sink.Close()

	lines, _ := a.Log(LogQuery{Cluster: "prod-eks-1", Namespace: "prod", Pod: "api-0", Container: "api"})
	got := make([]string, len(lines))
	for i, l := range lines {
		got[i] = l.Text
	}
	want := []string{"one", "two", "three", "four", "five without a newline"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("read back %v, want %v", got, want)
	}
}

// A carriage return would be a second line break in a file split on newlines.
func TestCarriageReturnsAreStripped(t *testing.T) {
	a, _ := newArchive(t, on())
	sink := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	sink.Write([]byte("windows line\r\n"))
	sink.Close()

	lines, _ := a.Log(LogQuery{Cluster: "prod-eks-1", Namespace: "prod", Pod: "api-0", Container: "api"})
	if len(lines) != 1 || lines[0].Text != "windows line" {
		t.Errorf("read back %+v, want one line without the carriage return", lines)
	}
}

// This is the part that could fill a disk, so it is the part with a ceiling. One
// container, one day, and then it stops and says so.
func TestOneContainerCannotExceedItsDailyCeiling(t *testing.T) {
	c := newClock()
	a := Open(Options{
		Root:                    filepath.Join(t.TempDir(), "archive"),
		Defaults:                on(),
		Now:                     c.now,
		Logf:                    t.Logf,
		MaxLogBytesPerStreamDay: 4096,
	})
	defer a.Close()

	sink := a.LogWriter("prod-eks-1", "prod", "noisy-0", "app")
	shout := strings.Repeat("x", 200)
	for i := 0; i < 500; i++ {
		sink.Write([]byte(shout + "\n"))
	}
	sink.Close()

	path := filepath.Join(a.Root(), a.day(c.now()), segment("prod-eks-1"), logsDirName, "prod", "noisy-0", "app.log")
	body := readAll(t, path)
	if len(body) == 0 {
		t.Fatal("nothing was written at all")
	}
	// The ceiling is checked after a line, so the file may overshoot by one line
	// and the marker — never by the hundred kilobytes that were offered.
	if len(body) > 4096+2*len(shout)+300 {
		t.Errorf("the file is %d bytes against a ceiling of 4096", len(body))
	}
	if !strings.Contains(body, "reached the archive's ceiling") {
		t.Error("the file stops without saying why; that reads as a bug in the archive rather than a bound")
	}

	// The next day starts again: the ceiling is per day, not for ever.
	c.add(24 * time.Hour)
	next := a.LogWriter("prod-eks-1", "prod", "noisy-0", "app")
	next.Write([]byte("a new day\n"))
	next.Close()
	tomorrow := filepath.Join(a.Root(), a.day(c.now()), segment("prod-eks-1"), logsDirName, "prod", "noisy-0", "app.log")
	if !strings.Contains(readAll(t, tomorrow), "a new day") {
		t.Error("the daily ceiling carried into the next day")
	}
}

// A stream open across midnight has to move to the new day's directory, or a
// prune would eventually delete a file that is still being written.
func TestAStreamOpenAcrossMidnightMovesToTheNewDay(t *testing.T) {
	a, c := newArchive(t, on())
	sink := a.LogWriter("prod-eks-1", "prod", "api-0", "api")

	yesterday := a.day(c.now())
	sink.Write([]byte("before midnight\n"))
	c.add(24 * time.Hour)
	today := a.day(c.now())
	sink.Write([]byte("after midnight\n"))
	sink.Close()

	if yesterday == today {
		t.Fatal("the clock did not roll a day")
	}
	rel := filepath.Join(segment("prod-eks-1"), logsDirName, "prod", "api-0", "api.log")
	if got := readAll(t, filepath.Join(a.Root(), yesterday, rel)); !strings.Contains(got, "before midnight") {
		t.Errorf("yesterday's file holds %q", got)
	}
	if got := readAll(t, filepath.Join(a.Root(), today, rel)); !strings.Contains(got, "after midnight") {
		t.Errorf("today's file holds %q", got)
	}
}

// A history tab needs an index before it can offer anything to read.
func TestStreamsListsWhatCanBeRead(t *testing.T) {
	a, c := newArchive(t, on())
	for _, s := range []struct{ ns, pod, container string }{
		{"prod", "api-0", "api"},
		{"prod", "api-0", "envoy"},
		{"staging", "db-0", "postgres"},
	} {
		w := a.LogWriter("prod-eks-1", s.ns, s.pod, s.container)
		w.Write([]byte("hello from " + s.container + "\n"))
		w.Close()
		c.add(time.Second)
	}

	all, err := a.Streams(StreamQuery{})
	if err != nil {
		t.Fatalf("Streams: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("%d streams, want 3: %+v", len(all), all)
	}
	for _, s := range all {
		if s.Bytes == 0 || len(s.Days) == 0 || s.Cluster != "prod-eks-1" {
			t.Errorf("%+v is missing its size, days or cluster", s)
		}
	}

	one, _ := a.Streams(StreamQuery{Cluster: "prod-eks-1", Namespace: "prod", Pod: "api-0"})
	if len(one) != 2 {
		t.Errorf("%d streams for one pod, want its two containers", len(one))
	}
	if none, _ := a.Streams(StreamQuery{Cluster: "tools-eks-1"}); len(none) != 0 {
		t.Errorf("%d streams for a cluster with none", len(none))
	}
}

// A read is bounded by what the caller asked for, not by how much was archived:
// the caller of a history tab is a browser.
func TestAReadIsBoundedByItsTail(t *testing.T) {
	a, _ := newArchive(t, on())
	sink := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	for i := 0; i < 5000; i++ {
		sink.Write([]byte("line\n"))
	}
	sink.Write([]byte("the last one\n"))
	sink.Close()

	lines, err := a.Log(LogQuery{Cluster: "prod-eks-1", Namespace: "prod", Pod: "api-0", Container: "api", Tail: 10})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(lines) != 10 {
		t.Fatalf("%d lines for a tail of 10", len(lines))
	}
	if lines[9].Text != "the last one" {
		t.Errorf("the last line is %q, want the newest", lines[9].Text)
	}

	if _, err := a.Log(LogQuery{Cluster: "prod-eks-1", Namespace: "prod", Pod: "api-0"}); err == nil {
		t.Error("a read with no container was accepted")
	}
}

// Turning log capture off has to stop a stream that is already open, not wait
// for its next line — which on a quiet pod could be tomorrow.
func TestTurningLogsOffStopsAnOpenStream(t *testing.T) {
	a, _ := newArchive(t, on())
	sink := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	sink.Write([]byte("while it was on\n"))

	if _, err := a.SetSettings(Settings{Enabled: true, Logs: false, RetentionDays: 7}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	if _, err := sink.Write([]byte("after it was turned off\n")); err != nil {
		t.Errorf("a closed sink returned %v; it must still accept and discard", err)
	}
	sink.Close()

	lines, _ := a.Log(LogQuery{Cluster: "prod-eks-1", Namespace: "prod", Pod: "api-0", Container: "api"})
	for _, l := range lines {
		if strings.Contains(l.Text, "after it was turned off") {
			t.Fatal("a line written after log capture was turned off reached the disk")
		}
	}
	if len(lines) != 1 {
		t.Errorf("%d lines, want the one written while it was on", len(lines))
	}

	// And a new sink discards outright.
	w := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	if _, ok := w.(discard); !ok {
		t.Errorf("LogWriter returned %T with log capture off, want a discarding sink", w)
	}
	w.Close()
}

// Events are the cheap half and the useful half. The whole-archive ceiling stops
// log capture and must leave them alone.
//
// The sink is deliberately left open and written to between the polls, because
// that is the case that happens: if the ceiling went through fail(), it would
// set the process-wide cooldown that gates event recording as well, and a
// container still talking would re-arm that cooldown on roughly every line
// while the recorder came round every two minutes. Events would be starved for
// as long as the pod kept printing, with RecordEvents saying (0, nil)
// throughout. Closing the sink before moving the clock would miss that case.
func TestTheArchiveCeilingStopsLogsAndNotEvents(t *testing.T) {
	c := newClock()
	a := Open(Options{
		Root:          filepath.Join(t.TempDir(), "archive"),
		Defaults:      Settings{Enabled: true, Logs: true, RetentionDays: 1},
		Now:           c.now,
		Logf:          t.Logf,
		MaxTotalBytes: 1024,
	})
	defer a.Close()

	day := a.day(c.now())
	logPath := filepath.Join(a.Root(), day, segment("prod-eks-1"), logsDirName, "prod", "api-0", "api.log")

	sink := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	for i := 0; i < 200; i++ {
		sink.Write([]byte(strings.Repeat("y", 100) + "\n"))
	}

	// approxBytes rather than Usage: Usage walks the tree, and on Windows a
	// file's size in its directory entry is not updated while a handle on it is
	// still open — and this sink is deliberately still open.
	if got := a.approxBytes(); got < 1024 {
		t.Fatalf("the archive is %d bytes; the ceiling was never reached", got)
	}
	st := a.Status()
	if !st.LogsSuspended {
		t.Error("Status does not say log capture is suspended; a settings page cannot say why logs stopped")
	}
	if !strings.Contains(st.LogsNote, "ceiling") {
		t.Errorf("Status().LogsNote = %q, want it to name the ceiling", st.LogsNote)
	}
	if !st.Writable {
		t.Error("Status says the archive is not writable after reaching its size ceiling; nothing is wrong with the disk, and events are still going in")
	}

	// Three polls, each a cooldown apart, with the container still printing in
	// between. Every one of them must store.
	for round := 0; round < 3; round++ {
		c.add(writeCooldown + time.Second)
		if _, err := sink.Write([]byte("the container is still talking\n")); err != nil {
			t.Fatalf("round %d: Write: %v", round, err)
		}
		e := sampleEvent("api-"+string(rune('0'+round)), 1, c.now())
		if n, err := a.RecordEvents("prod-eks-1", []Event{e}); n != 1 || err != nil {
			t.Errorf("round %d: RecordEvents = %d, %v over the ceiling; events must keep going", round, n, err)
		}
	}

	// And log capture really did stop, rather than merely being reported as
	// stopped.
	before := len(readAll(t, logPath))
	for i := 0; i < 50; i++ {
		sink.Write([]byte(strings.Repeat("z", 100) + "\n"))
	}
	if after := len(readAll(t, logPath)); after != before {
		t.Errorf("the log file grew from %d to %d bytes while capture was suspended", before, after)
	}

	// The note says "until the next prune", so a prune that makes room has to
	// lift it. The day the logs are in expires, and the sink holding it open is
	// closed by the pruner on the way.
	c.add(3 * 24 * time.Hour)
	if _, err := a.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if st := a.Status(); st.LogsSuspended {
		t.Errorf("log capture is still suspended after a prune freed the archive: %q", st.LogsNote)
	}
	sink.Close()

	next := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	next.Write([]byte("room again\n"))
	next.Close()
	fresh := filepath.Join(a.Root(), a.day(c.now()), segment("prod-eks-1"), logsDirName, "prod", "api-0", "api.log")
	if !strings.Contains(readAll(t, fresh), "room again") {
		t.Error("log capture did not resume once the prune had made room")
	}
}

// Two viewers on one container's logs is an ordinary thing for a console to be
// asked for, and the path a sink writes to is derived from the container alone.
// Two sinks would be two O_APPEND handles on one file, each seeding its byte
// count from the other's writes when it opens — so the day's ceiling would be
// enforced at some multiple of what it says, which for the one bound that
// stands between a crash loop and the volume is not a rounding error.
func TestTwoViewersOfOneContainerShareOneSink(t *testing.T) {
	c := newClock()
	a := Open(Options{
		Root:                    filepath.Join(t.TempDir(), "archive"),
		Defaults:                on(),
		Now:                     c.now,
		Logf:                    t.Logf,
		MaxLogBytesPerStreamDay: 4096,
	})
	defer a.Close()

	first := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	second := a.LogWriter("prod-eks-1", "prod", "api-0", "api")

	fr, ok := first.(*sinkRef)
	if !ok {
		t.Fatalf("LogWriter returned %T, want a handle on a shared sink", first)
	}
	sr, ok := second.(*sinkRef)
	if !ok {
		t.Fatalf("LogWriter returned %T for the second viewer", second)
	}
	if fr.sink != sr.sink {
		t.Error("two viewers of one container got two sinks; they would open the same file twice")
	}
	if n := len(a.sinks); n != 1 {
		t.Errorf("%d sinks registered for one container, want 1", n)
	}

	// The same stream teed into both handles, which is what two viewers of one
	// container produce. The file must respect the ceiling once, not once per
	// viewer.
	line := strings.Repeat("x", 200) + "\n"
	for i := 0; i < 100; i++ {
		first.Write([]byte(line))
		second.Write([]byte(line))
	}

	path := filepath.Join(a.Root(), a.day(c.now()), segment("prod-eks-1"), logsDirName, "prod", "api-0", "api.log")
	body := readAll(t, path)
	if len(body) == 0 {
		t.Fatal("nothing was written at all")
	}
	if len(body) > 4096+2*len(line)+300 {
		t.Errorf("the file is %d bytes against a per-day ceiling of 4096; the two viewers were each allowed their own allowance", len(body))
	}

	// One viewer closing leaves the other's stream alone.
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if sr.sink.closed {
		t.Fatal("the sink was closed while a second viewer still held it")
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !sr.sink.closed {
		t.Error("the sink was not closed when the last viewer let go of it")
	}
	if n := len(a.sinks); n != 0 {
		t.Errorf("%d sinks left registered after both viewers closed", n)
	}

	// A handle closed twice must not release a share it does not have — the
	// next viewer's stream would be closed under it.
	first.Close()
	again := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	first.Close()
	if again.(*sinkRef).sink.closed {
		t.Error("closing a spent handle twice closed a stream somebody else had just opened")
	}
	again.Close()
}

// Two readings of the clock either side of an open file is a line written at
// 00:00:00.001 filed under yesterday: stamped today, in a directory a reader
// looking at today never opens and the pruner removes a day early.
func TestALineIsFiledUnderTheDayItIsStampedWith(t *testing.T) {
	// A clock that rolls the day between the first reading and the second,
	// which is what a real one does at midnight for one line in a day.
	var mu sync.Mutex
	calls := 0
	midnight := time.Date(2026, 8, 17, 23, 59, 59, 900_000_000, time.UTC)
	now := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return midnight
		}
		return midnight.Add(200 * time.Millisecond)
	}

	a := Open(Options{
		Root:     filepath.Join(t.TempDir(), "archive"),
		Defaults: on(),
		Now:      now,
		Logf:     t.Logf,
	})
	defer a.Close()

	sink := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	sink.Write([]byte("the line that straddles midnight\n"))
	sink.Close()

	rel := filepath.Join(segment("prod-eks-1"), logsDirName, "prod", "api-0", "api.log")
	written, err := a.days()
	if err != nil {
		t.Fatalf("days: %v", err)
	}
	for _, day := range written {
		body := readAll(t, filepath.Join(a.Root(), day, rel))
		if body == "" {
			continue
		}
		line, ok := parseLine(strings.TrimSuffix(body, "\n"))
		if !ok {
			t.Fatalf("%s holds a line this package cannot read back: %q", day, body)
		}
		if got := line.At.UTC().Format(dayLayout); got != day {
			t.Errorf("a line stamped %s is filed under %s; a reader asking for %s will not find it",
				got, day, got)
		}
	}
}

// The tail is a ring with a head index, not a buffer shifted down one slot per
// line. This is what the wrapping has to get right for a read to still be
// `tail -n`.
func TestATailIsExactHoweverTheRingWraps(t *testing.T) {
	a, c := newArchive(t, on())
	sink := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	const lines = 250
	for i := 0; i < lines; i++ {
		sink.Write([]byte(fmt.Sprintf("line %d\n", i)))
	}
	sink.Close()
	_ = c

	for _, tail := range []int{1, 7, 100, 249, 250, 251, 1000} {
		got, err := a.Log(LogQuery{Cluster: "prod-eks-1", Namespace: "prod", Pod: "api-0", Container: "api", Tail: tail})
		if err != nil {
			t.Fatalf("tail %d: %v", tail, err)
		}
		want := tail
		if want > lines {
			want = lines
		}
		if len(got) != want {
			t.Fatalf("tail %d returned %d lines, want %d", tail, len(got), want)
		}
		for i, l := range got {
			if expect := fmt.Sprintf("line %d", lines-want+i); l.Text != expect {
				t.Fatalf("tail %d line %d is %q, want %q — the ring unwound in the wrong order", tail, i, l.Text, expect)
			}
		}
	}
}

// The cost of that ring, which is why it is a ring. A shift-left
// moves the whole buffer per line: at the default tail, one 8 MiB day file is
// about 3.4 GB through memory, and at MaxLogTail about 48 GB.
func BenchmarkLogTail(b *testing.B) {
	root := b.TempDir()
	a := Open(Options{Root: filepath.Join(root, "archive"), Defaults: on(), Logf: func(string, ...any) {}})
	defer a.Close()

	sink := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	for i := 0; i < 60000; i++ {
		sink.Write([]byte(fmt.Sprintf("line %d %s\n", i, strings.Repeat("x", 80))))
	}
	sink.Close()

	q := LogQuery{Cluster: "prod-eks-1", Namespace: "prod", Pod: "api-0", Container: "api", Tail: MaxLogTail}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := a.Log(q); err != nil {
			b.Fatal(err)
		}
	}
}

var _ io.WriteCloser = discard{}

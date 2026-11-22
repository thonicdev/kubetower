package archive

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The window, and the fact that pruning happens at all.
func TestPruneRemovesDaysOutsideTheWindowAndKeepsTheRest(t *testing.T) {
	a, c := newArchive(t, Settings{Enabled: true, Logs: true, RetentionDays: 7})

	// Twelve days of one event each, oldest first.
	c.add(-11 * 24 * time.Hour)
	var written []string
	for i := 0; i < 12; i++ {
		written = append(written, a.day(c.now()))
		if n, _ := a.RecordEvents("prod-eks-1", []Event{sampleEvent("api-0", 1, c.now())}); n != 1 {
			t.Fatalf("day %d stored nothing", i)
		}
		c.add(24 * time.Hour)
	}
	c.add(-24 * time.Hour) // back onto the last day written

	written, err := a.days()
	if err != nil {
		t.Fatalf("days: %v", err)
	}
	if got := len(written); got != 12 {
		t.Fatalf("%d day directories after writing 12", got)
	}

	res, err := a.Prune()
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	kept, err := a.days()
	if err != nil {
		t.Fatalf("days after Prune: %v", err)
	}
	// Seven days plus today: the extra directory is deliberate, so that "seven
	// days" is a floor rather than a figure that rounds against the operator.
	if len(kept) != 8 {
		t.Errorf("%d days kept, want 8 (today and the seven before it): %v", len(kept), kept)
	}
	if len(res.Days) != 4 {
		t.Errorf("Prune removed %v, want the four oldest of twelve", res.Days)
	}
	if res.Bytes <= 0 {
		t.Error("Prune reported freeing no bytes while removing four days of events")
	}
	for _, day := range res.Days {
		if _, err := os.Stat(filepath.Join(a.Root(), day)); !os.IsNotExist(err) {
			t.Errorf("%s is still on disk after being reported removed", day)
		}
	}
	if kept[len(kept)-1] != written[len(written)-1] {
		t.Errorf("the newest day kept is %s, want %s", kept[len(kept)-1], written[len(written)-1])
	}

	if st := a.Status(); st.LastPrune.IsZero() || st.LastPruneNote == "" {
		t.Errorf("Status = %+v after a prune, want it to say when and what", st)
	}
}

// The current day is never removed, whatever the retention says. A stream is
// writing into it right now.
func TestPruneNeverRemovesToday(t *testing.T) {
	a, c := newArchive(t, Settings{Enabled: true, Logs: true, RetentionDays: 1})
	if n, _ := a.RecordEvents("prod-eks-1", []Event{sampleEvent("api-0", 1, c.now())}); n != 1 {
		t.Fatal("nothing was recorded")
	}
	today := a.day(c.now())

	for i := 0; i < 3; i++ {
		if _, err := a.Prune(); err != nil {
			t.Fatalf("Prune: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(a.Root(), today)); err != nil {
		t.Errorf("today's directory was removed: %v", err)
	}
}

// The refusal that matters most in this package. A pruner that can be talked
// into operating outside its own root is how a bug becomes a data-loss
// incident, so it refuses twice over — on the shape of the name, and on where
// the path resolves to.
func TestThePrunerCannotReachOutsideItsRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "archive")

	// Something valuable next to the archive, standing in for a home directory.
	bystander := filepath.Join(parent, "not-the-archive")
	if err := os.MkdirAll(bystander, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(bystander, "hotbars.json")
	if err := os.WriteFile(keep, []byte(`{"active":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	c := newClock()
	a := Open(Options{Root: root, Defaults: on(), Now: c.now, Logf: t.Logf})
	defer a.Close()
	a.RecordEvents("prod-eks-1", []Event{sampleEvent("api-0", 1, c.now())})

	for _, name := range []string{
		"..",
		"../not-the-archive",
		"..\\not-the-archive",
		filepath.Join("..", "not-the-archive"),
		bystander,
		parent,
		".",
		"",
		SettingsFileName,
		"2026-08-17/../../not-the-archive",
		"20260817",
		"9999-99-99",
	} {
		if err := a.removeDay(name); err == nil {
			t.Errorf("removeDay(%q) was allowed", name)
		}
	}

	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("the file next to the archive is gone: %v", err)
	}
	if _, err := os.Stat(bystander); err != nil {
		t.Fatalf("the directory next to the archive is gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, SettingsFileName)); err == nil {
		// It only exists if SetSettings ran; either way it must not have been
		// removed by a day-shaped-looking name.
		t.Log("the settings file is intact")
	}

	// And a real day still goes.
	day := a.day(c.now())
	if err := a.removeDay(day); err != nil {
		t.Fatalf("removeDay(%q) refused a real day: %v", day, err)
	}
	if _, err := os.Stat(filepath.Join(root, day)); !os.IsNotExist(err) {
		t.Error("the real day was not removed")
	}
}

// inRoot is the second of the two refusals, and is used by every path this
// package creates as well as by the one that deletes.
func TestInRootRefusesAnythingOutside(t *testing.T) {
	root := filepath.Join(t.TempDir(), "archive")
	a := Open(Options{Root: root, Defaults: on(), Logf: t.Logf})
	defer a.Close()

	for _, path := range []string{
		root,
		filepath.Dir(root),
		filepath.Join(root, ".."),
		filepath.Join(root, "..", "elsewhere"),
		string(filepath.Separator),
	} {
		if err := a.inRoot(path); err == nil {
			t.Errorf("inRoot(%q) was allowed", path)
		}
	}
	for _, path := range []string{
		filepath.Join(root, "2026-08-17"),
		filepath.Join(root, "2026-08-17", "cluster", "logs", "ns", "pod", "c.log"),
	} {
		if err := a.inRoot(path); err != nil {
			t.Errorf("inRoot(%q) refused a path inside the archive: %v", path, err)
		}
	}

	// An archive with no root refuses everything rather than defaulting to the
	// working directory.
	empty := Open(Options{Defaults: on(), Logf: t.Logf})
	defer empty.Close()
	if err := empty.inRoot("2026-08-17"); err == nil {
		t.Error("an archive with no directory allowed a path")
	}
}

// Anything in the root that is not a day is left alone: the settings file, and
// a temporary file from a write that was interrupted.
func TestPruneOnlyTouchesDayDirectories(t *testing.T) {
	a, c := newArchive(t, Settings{Enabled: true, RetentionDays: 1})
	if _, err := a.SetSettings(Settings{Enabled: true, RetentionDays: 1}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	c.add(-30 * 24 * time.Hour)
	a.RecordEvents("prod-eks-1", []Event{sampleEvent("api-0", 1, c.now())})
	c.add(30 * 24 * time.Hour)

	stray := filepath.Join(a.Root(), "notes.txt")
	if err := os.WriteFile(stray, []byte("an operator left this here"), 0o600); err != nil {
		t.Fatal(err)
	}
	strayDir := filepath.Join(a.Root(), "scratch")
	if err := os.MkdirAll(strayDir, 0o700); err != nil {
		t.Fatal(err)
	}

	res, err := a.Prune()
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(res.Days) != 1 {
		t.Errorf("Prune removed %v, want the one expired day", res.Days)
	}
	for _, path := range []string{stray, strayDir, filepath.Join(a.Root(), SettingsFileName)} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was removed by the pruner: %v", path, err)
		}
	}
}

// A sink still holding a file inside a day the pruner is about to remove has to
// let go of it first. On Windows the open handle makes the removal fail
// outright; on Linux it leaves a writer appending to an unlinked inode, which is
// worse because it looks like it worked.
func TestPruneClosesASinkHoldingAnExpiredDay(t *testing.T) {
	a, c := newArchive(t, Settings{Enabled: true, Logs: true, RetentionDays: 1})

	sink := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	sink.Write([]byte("written yesterday, and the day before\n"))
	old := a.day(c.now())
	held, ok := sink.(*sinkRef)
	if !ok {
		t.Fatalf("LogWriter returned %T", sink)
	}
	if held.sink.currentDay() != old {
		t.Fatalf("the sink is writing into %q, want %q", held.sink.currentDay(), old)
	}

	c.add(3 * 24 * time.Hour)
	res, err := a.Prune()
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(res.Days) != 1 || res.Days[0] != old {
		t.Fatalf("Prune removed %v, want %s", res.Days, old)
	}
	if !held.sink.closed {
		t.Error("the sink was left holding a file in a directory the pruner removed")
	}
	if _, err := os.Stat(filepath.Join(a.Root(), old)); !os.IsNotExist(err) {
		t.Errorf("%s is still on disk: %v", old, err)
	}

	// The handle still works, because a sink that has been closed under its
	// caller accepts and discards like any other.
	if _, err := sink.Write([]byte("after the prune\n")); err != nil {
		t.Errorf("Write on a pruned sink returned %v", err)
	}
	sink.Close()
}

// Empty is one button, and it must not make the archive report itself as
// broken. Clear removes today's directory, which is the one the event recorder
// is writing into, and the two must not interleave: the recorder creating the
// cluster directory, Clear removing the day underneath it, and the recorder's
// open failing on a path with no parent — so the settings page would say "not
// writable, no such file or directory" in the same breath as the click.
func TestClearDoesNotBreakARecorderThatIsMidWrite(t *testing.T) {
	a, c := newArchive(t, on())

	cleared := make(chan struct{})
	var once sync.Once
	a.open = func(path string) (wfile, error) {
		// The directory exists and the file is about to be created: exactly
		// where Clear could get in without the event lock.
		once.Do(func() {
			go func() {
				a.Clear()
				close(cleared)
			}()
			select {
			case <-cleared:
				// Clear got through while this write was in flight, which is a
				// failure. Let the assertions below say so.
			case <-time.After(250 * time.Millisecond):
				// It is waiting on the event lock this caller holds, as it
				// should.
			}
		})
		return openFile(path)
	}

	stored, err := a.RecordEvents("prod-eks-1", []Event{sampleEvent("api-0", 1, c.now())})
	<-cleared

	if err != nil {
		t.Fatalf("RecordEvents: %v", err)
	}
	if stored != 1 {
		t.Errorf("RecordEvents stored %d while Clear ran, want 1", stored)
	}
	if st := a.Status(); !st.Writable {
		t.Errorf("the archive reports itself unwritable after an Empty: %q", st.Note)
	}
	if days, err := a.days(); err != nil || len(days) != 0 {
		t.Errorf("%v is still on disk after Clear (%v)", days, err)
	}
}

// Writes arrive from streaming goroutines while a pruner runs and a size query
// is served. Run under -race, this is the test that says so.
//
// Every turn of the pruner has an expired day waiting for it, and that is not
// decoration. With nothing to expire, Prune returns before the part that
// matters — the pass over the open sinks, asking each one which day it is
// writing into, so that none is left holding a file inside a directory about to
// be removed. With a pinned clock that pass never runs, and the read of a field
// the streaming goroutines write would be invisible to the detector: -race can
// only report a race it sees happen.
//
// The staged day is thirty-odd days back and empty, which keeps the removal
// itself uninteresting. The writers' own days are inside the window throughout —
// a sink is never writing into a day that is already expired, which in a console
// takes a stall of a day and here would only be the test outrunning itself.
func TestWritesPruningAndSizeQueriesRunTogether(t *testing.T) {
	a, c := newArchive(t, on())

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			pod := "api-" + string(rune('0'+w))
			sink := a.LogWriter("prod-eks-1", "prod", pod, "app")
			defer func() { sink.Close() }()
			for i := 0; i < 200; i++ {
				if i > 0 && i%20 == 0 {
					// Reopened from time to time, so that there is always a
					// live sink for the pruner to find rather than one it
					// closed early on. A console does the same when a viewer
					// leaves and another arrives.
					sink.Close()
					sink = a.LogWriter("prod-eks-1", "prod", pod, "app")
				}
				// Five minutes a line, so the day rolls under the open sinks a
				// few times and the field the pruner reads is genuinely being
				// written. Two and a half days in total, against a window of
				// seven.
				c.add(5 * time.Minute)
				sink.Write([]byte("a line from " + pod + "\n"))
				a.RecordEvents("prod-eks-1", []Event{sampleEvent(pod, int32(i), c.now())})
				select {
				case <-stop:
					return
				default:
				}
			}
		}(w)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			stale := c.now().UTC().AddDate(0, 0, -30-i).Format(dayLayout)
			if err := os.MkdirAll(filepath.Join(a.Root(), stale), 0o700); err != nil {
				t.Errorf("staging %s: %v", stale, err)
				return
			}
			if _, err := a.Prune(); err != nil {
				t.Errorf("Prune: %v", err)
				return
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			_ = a.Usage()
			_ = a.Status()
			_, _ = a.Streams(StreamQuery{})
			_, _ = a.Events(EventQuery{Cluster: "prod-eks-1", Limit: 20})
		}
	}()

	wg.Wait()
	close(stop)

	if u := a.RefreshUsage(); u.Bytes <= 0 {
		t.Errorf("Usage().Bytes = %d after four writers, want something", u.Bytes)
	}
}

// A size figure is a fact an operator needs before they can decide what to
// change, so it has to break down by cluster and by what the data is.
func TestUsageBreaksTheTotalDown(t *testing.T) {
	a, c := newArchive(t, on())

	for _, cluster := range []string{"prod-eks-1", "tools-eks-1"} {
		a.RecordEvents(cluster, []Event{sampleEvent("api-0", 1, c.now())})
	}
	sink := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	for i := 0; i < 100; i++ {
		sink.Write([]byte(strings.Repeat("z", 120) + "\n"))
	}
	sink.Close()

	u := a.RefreshUsage()
	if u.Bytes <= 0 || u.Files == 0 {
		t.Fatalf("Usage = %+v, want a total and a file count", u)
	}
	if u.LogBytes < 12000 {
		t.Errorf("LogBytes = %d, want the ~12 KiB just written", u.LogBytes)
	}
	if u.EventBytes <= 0 {
		t.Error("EventBytes = 0 after recording events in two clusters")
	}
	if len(u.Clusters) != 2 {
		t.Fatalf("%d clusters in the breakdown, want 2: %+v", len(u.Clusters), u.Clusters)
	}
	// Largest first, so the answer to "what is using the disk" is the first row.
	if u.Clusters[0].Cluster != "prod-eks-1" {
		t.Errorf("the breakdown leads with %q, want the cluster with the logs", u.Clusters[0].Cluster)
	}
	if u.Clusters[0].LogBytes == 0 || u.Clusters[1].LogBytes != 0 {
		t.Errorf("the log bytes are attributed to the wrong cluster: %+v", u.Clusters)
	}
	if len(u.Days) != 1 || u.Days[0].Day != a.day(c.now()) {
		t.Errorf("Days = %+v, want the one day written", u.Days)
	}
	if u.Ceiling != DefaultMaxTotalBytes {
		t.Errorf("Ceiling = %d, want the default", u.Ceiling)
	}
	if !strings.Contains(u.Human(), "iB") {
		t.Errorf("Human() = %q, want a readable size", u.Human())
	}
}

// The cached figure is topped up by writes rather than recomputed, so that a
// settings page held open cannot become back-pressure on a log stream.
func TestASizeQueryDoesNotRewalkTheTree(t *testing.T) {
	a, _ := newArchive(t, on())
	// Each sink is closed before the tree is measured. Windows updates a file's
	// size in its directory entry lazily, so a walk that runs while a handle is
	// still open reads a stale size and the comparison below would be flaky for
	// a reason that has nothing to do with the accounting.
	first := writeAndClose(a, "one line\n")

	sink := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	sink.Write([]byte(strings.Repeat("q", 500) + "\n"))
	sink.Close()

	second := a.Usage()
	if second.Bytes <= first.Bytes {
		t.Errorf("Usage did not move after 500 more bytes: %d then %d", first.Bytes, second.Bytes)
	}
	if !second.Approximate {
		t.Error("a figure adjusted by writes since the walk is not marked approximate")
	}
	if second.ComputedAt != first.ComputedAt {
		t.Error("Usage walked the tree again; it must serve the cache")
	}

	exact := a.RefreshUsage()
	if exact.Approximate {
		t.Error("a fresh walk is marked approximate")
	}
	if exact.Bytes != second.Bytes {
		t.Errorf("the cached figure was %d and the walk says %d", second.Bytes, exact.Bytes)
	}
}

// writeAndClose writes one line, closes the sink so the size on disk is final,
// and returns a freshly walked usage.
func writeAndClose(a *Archive, line string) Usage {
	sink := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	sink.Write([]byte(line))
	sink.Close()
	return a.RefreshUsage()
}

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{4 << 20, "4.0 MiB"},
		{2 << 30, "2.0 GiB"},
		{3 << 40, "3.0 TiB"},
		{-5, "0 B"},
		// A petabyte: the exponent is capped rather than panicking.
		{1 << 50, "1024.0 TiB"},
	} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

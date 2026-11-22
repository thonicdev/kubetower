package archive

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The whole reason this package deduplicates. A BackOff event on a crashing pod
// is *one* event whose count grows; recording it every time it is polled would
// claim a hundred events where there was one, and would be most of the disk.
func TestARepeatingEventIsRecordedOnceNotOncePerPoll(t *testing.T) {
	a, c := newArchive(t, on())
	first := c.now().Add(-time.Hour)

	// Twenty polls, thirty seconds apart, of a pod that is backing off: ten
	// minutes of a count climbing from 1 to 20.
	total := 0
	for i := 1; i <= 20; i++ {
		n, err := a.RecordEvents("prod-eks-1", []Event{sampleEvent("api-0", int32(i), first)})
		if err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
		total += n
		c.add(30 * time.Second)
	}

	// One record on first sight and one update per eventUpdateInterval: two over
	// ten minutes, not twenty.
	if total != 2 {
		t.Errorf("%d records written for one repeating event over twenty polls, want 2 — the count growing is not twenty events", total)
	}

	events, err := a.Events(EventQuery{Cluster: "prod-eks-1"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("the query returned %d events, want the one that happened: %+v", len(events), events)
	}
	// The count lags by at most one update interval, which is the trade: a
	// figure that is a few minutes old, against a file a hundred times larger.
	if events[0].Count != 11 {
		t.Errorf("Count = %d, want 11 — the count as of the last update, five minutes in", events[0].Count)
	}
	if events[0].Cluster != "prod-eks-1" {
		t.Errorf("Cluster = %q, want it stamped on the record", events[0].Cluster)
	}
}

// A count that has not moved is not news at all, whatever the interval says.
func TestAnUnchangedEventIsNeverRewritten(t *testing.T) {
	a, c := newArchive(t, on())
	e := sampleEvent("api-0", 3, c.now().Add(-time.Hour))

	if n, _ := a.RecordEvents("prod-eks-1", []Event{e}); n != 1 {
		t.Fatalf("the first sighting stored %d, want 1", n)
	}
	c.add(time.Hour)
	if n, _ := a.RecordEvents("prod-eks-1", []Event{e}); n != 0 {
		t.Errorf("the same event with the same count stored %d an hour later, want 0", n)
	}
}

// Two events differing only in firstTimestamp are two events: a pod that backs
// off, recovers and backs off again on Tuesday did not have Monday's event.
func TestIdentityIsTheObjectTheReasonAndTheFirstTimestamp(t *testing.T) {
	a, c := newArchive(t, on())
	base := c.now().Add(-2 * time.Hour)

	monday := sampleEvent("api-0", 4, base)
	tuesday := sampleEvent("api-0", 4, base.Add(90*time.Minute))
	other := sampleEvent("api-1", 4, base)
	reason := sampleEvent("api-0", 4, base)
	reason.Reason = "Unhealthy"

	if n, _ := a.RecordEvents("prod-eks-1", []Event{monday, tuesday, other, reason}); n != 4 {
		t.Fatalf("stored %d of 4 distinct events", n)
	}
	events, _ := a.Events(EventQuery{Cluster: "prod-eks-1"})
	if len(events) != 4 {
		t.Errorf("the query returned %d, want 4 — %+v", len(events), events)
	}
}

// The index has to survive a restart, or the first poll after every restart
// re-records everything the cluster is still reporting.
func TestTheDedupIndexIsRebuiltAfterARestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "archive")
	c := newClock()
	opts := Options{Root: root, Defaults: on(), Now: c.now, Logf: t.Logf}

	a := Open(opts)
	events := []Event{
		sampleEvent("api-0", 2, c.now().Add(-time.Hour)),
		sampleEvent("api-1", 5, c.now().Add(-time.Hour)),
	}
	if n, _ := a.RecordEvents("prod-eks-1", events); n != 2 {
		t.Fatalf("stored %d of 2", n)
	}
	a.Close()

	b := Open(opts)
	defer b.Close()
	if n, _ := b.RecordEvents("prod-eks-1", events); n != 0 {
		t.Errorf("a restarted archive re-recorded %d events it already had", n)
	}
	// A count that has grown since the restart is still news.
	c.add(eventUpdateInterval + time.Minute)
	grown := []Event{sampleEvent("api-0", 9, c.now().Add(-time.Hour-eventUpdateInterval-time.Minute))}
	if n, _ := b.RecordEvents("prod-eks-1", grown); n != 1 {
		t.Errorf("a grown count after a restart stored %d, want 1", n)
	}
}

// The filters a history tab needs, and the one that matters when something is
// wrong: show me only the warnings.
func TestEventsCanBeNarrowed(t *testing.T) {
	a, c := newArchive(t, on())
	base := c.now().Add(-time.Hour)

	warn := sampleEvent("api-0", 1, base)
	normal := sampleEvent("api-1", 1, base)
	normal.Type = "Normal"
	normal.Reason = "Scheduled"
	elsewhere := sampleEvent("db-0", 1, base)
	elsewhere.Namespace = "staging"
	node := sampleEvent("node-1", 1, base)
	node.Kind = "Node"
	node.Namespace = ""

	if n, _ := a.RecordEvents("prod-eks-1", []Event{warn, normal, elsewhere, node}); n != 4 {
		t.Fatalf("stored %d of 4", n)
	}

	for _, tc := range []struct {
		name string
		q    EventQuery
		want int
	}{
		{"everything", EventQuery{Cluster: "prod-eks-1"}, 4},
		{"one namespace", EventQuery{Cluster: "prod-eks-1", Namespace: "prod"}, 2},
		{"one kind", EventQuery{Cluster: "prod-eks-1", Kind: "Node"}, 1},
		{"one object", EventQuery{Cluster: "prod-eks-1", Object: "api-0"}, 1},
		{"warnings only", EventQuery{Cluster: "prod-eks-1", Warnings: true}, 3},
		{"another cluster", EventQuery{Cluster: "tools-eks-1"}, 0},
		{"a limit", EventQuery{Cluster: "prod-eks-1", Limit: 2}, 2},
	} {
		got, err := a.Events(tc.q)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(got) != tc.want {
			t.Errorf("%s: %d events, want %d", tc.name, len(got), tc.want)
		}
	}

	if _, err := a.Events(EventQuery{}); err == nil {
		t.Error("a query with no cluster was accepted; it would read every cluster at once")
	}
}

// Events answer newest first, because that is the order the question is asked
// in: what happened most recently to this thing.
func TestEventsComeBackNewestFirst(t *testing.T) {
	a, c := newArchive(t, on())
	base := c.now().Add(-3 * time.Hour)

	var batch []Event
	for i := 0; i < 5; i++ {
		e := sampleEvent("api-"+string(rune('0'+i)), 1, base.Add(time.Duration(i)*time.Minute))
		e.Last = base.Add(time.Duration(i) * time.Minute)
		batch = append(batch, e)
	}
	a.RecordEvents("prod-eks-1", batch)

	got, _ := a.Events(EventQuery{Cluster: "prod-eks-1"})
	if len(got) != 5 {
		t.Fatalf("%d events, want 5", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Last.After(got[i-1].Last) {
			t.Fatalf("event %d is newer than event %d; the order is wrong", i, i-1)
		}
	}
}

// A cluster name is not a path component. An EKS context is an ARN, full of
// colons and with a slash in it, and both would escape the directory it belongs
// in — the slash into a subdirectory, the colon into a filesystem Windows
// refuses outright.
func TestAnARNContextNameBecomesOneSafeDirectory(t *testing.T) {
	a, c := newArchive(t, on())
	arn := "arn:aws:eks:eu-west-1:123456789012:cluster/example-prod-eks"

	if n, _ := a.RecordEvents(arn, []Event{sampleEvent("api-0", 1, c.now())}); n != 1 {
		t.Fatalf("stored %d, want 1", n)
	}

	dir := segment(arn)
	if strings.ContainsAny(dir, `/\:`) {
		t.Errorf("the directory name %q still contains a separator or a colon", dir)
	}
	path := filepath.Join(a.Root(), a.day(c.now()), dir, eventsFileName)
	if readAll(t, path) == "" {
		t.Errorf("nothing was written to %s", path)
	}
	// And the real name is recoverable, or a settings page could only ever show
	// the mangled one.
	if got := readCluster(filepath.Join(a.Root(), a.day(c.now()), dir)); got != arn {
		t.Errorf("the cluster name reads back as %q, want %q", got, arn)
	}

	events, err := a.Events(EventQuery{Cluster: arn})
	if err != nil || len(events) != 1 {
		t.Errorf("querying by the ARN returned %d events (%v), want 1", len(events), err)
	}

	// Two contexts that sanitize to the same characters must not share a file.
	if segment("cluster/prod") == segment("cluster:prod") {
		t.Error("two different context names collapsed to the same directory")
	}
}

// One cluster cannot be allowed to fill a day's file without bound.
func TestOneClusterCannotExceedItsEventCeilingForADay(t *testing.T) {
	c := newClock()
	a := Open(Options{
		Root:                   filepath.Join(t.TempDir(), "archive"),
		Defaults:               on(),
		Now:                    c.now,
		Logf:                   t.Logf,
		MaxEventsPerClusterDay: 10,
	})
	defer a.Close()

	var batch []Event
	for i := 0; i < 50; i++ {
		batch = append(batch, sampleEvent("pod", 1, c.now().Add(-time.Duration(i)*time.Minute)))
	}
	n, _ := a.RecordEvents("prod-eks-1", batch)
	if n != 10 {
		t.Errorf("stored %d events against a ceiling of 10", n)
	}
	if extra, _ := a.RecordEvents("prod-eks-1", []Event{sampleEvent("later", 1, c.now())}); extra != 0 {
		t.Errorf("stored %d more after the ceiling, want 0", extra)
	}
	// The next day starts again.
	c.add(24 * time.Hour)
	if tomorrow, _ := a.RecordEvents("prod-eks-1", []Event{sampleEvent("tomorrow", 1, c.now())}); tomorrow != 1 {
		t.Errorf("the ceiling carried into the next day: stored %d, want 1", tomorrow)
	}
}

// A record cut short by a crash must not take the reader down with it.
func TestATornLineIsSkippedRatherThanFatal(t *testing.T) {
	a, c := newArchive(t, on())
	a.RecordEvents("prod-eks-1", []Event{sampleEvent("api-0", 1, c.now().Add(-time.Hour))})

	path := filepath.Join(a.Root(), a.day(c.now()), segment("prod-eks-1"), eventsFileName)
	body := readAll(t, path)
	if err := appendRaw(path, `{"kind":"Pod","object":"trunc`+"\n"); err != nil {
		t.Fatal(err)
	}
	if body == "" {
		t.Fatal("nothing was recorded to begin with")
	}

	events, err := a.Events(EventQuery{Cluster: "prod-eks-1"})
	if err != nil {
		t.Fatalf("Events on a file with a torn line: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("%d events, want the one intact record", len(events))
	}
}

// A page of history is five hundred rows. Ninety days of a cluster at its
// per-day ceiling is 1.8 million records, and materializing all of them to
// return five hundred is the difference between a query and a walk of the
// archive. The days are read newest first and the read stops once no older file
// can contribute — so the answer has to be the same answer.
func TestEventsAreTheNewestAndAreNotAWalkOfEverything(t *testing.T) {
	a, c := newArchive(t, on())

	// Ten days, three events each, filed on the day they happened.
	for day := 0; day < 10; day++ {
		var batch []Event
		for i := 0; i < 3; i++ {
			e := sampleEvent(fmt.Sprintf("api-d%d-%d", day, i), 1, c.now())
			e.Last = c.now()
			batch = append(batch, e)
		}
		if n, _ := a.RecordEvents("prod-eks-1", batch); n != 3 {
			t.Fatalf("day %d stored %d of 3", day, n)
		}
		c.add(24 * time.Hour)
	}
	c.add(-24 * time.Hour) // back onto the last day written

	all, err := a.Events(EventQuery{Cluster: "prod-eks-1"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(all) != 30 {
		t.Fatalf("%d events over ten days, want 30", len(all))
	}

	// A limited query answers with the newest of them, and the two answers agree
	// about which those are.
	limited, err := a.Events(EventQuery{Cluster: "prod-eks-1", Limit: 4})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(limited) != 4 {
		t.Fatalf("%d events for a limit of 4", len(limited))
	}
	for i := range limited {
		if limited[i].Object != all[i].Object {
			t.Errorf("row %d is %q with a limit and %q without; the early stop changed the answer",
				i, limited[i].Object, all[i].Object)
		}
	}

	// And an event that started days ago and is still repeating is still one
	// event, with its latest count, however the days are walked.
	first := c.now().Add(-9 * 24 * time.Hour)
	old := sampleEvent("long-runner", 4, first)
	if n, _ := a.RecordEvents("prod-eks-1", []Event{old}); n != 1 {
		t.Fatal("the repeating event was not recorded")
	}
	c.add(24*time.Hour + eventUpdateInterval)
	grown := sampleEvent("long-runner", 40, first)
	if n, _ := a.RecordEvents("prod-eks-1", []Event{grown}); n != 1 {
		t.Fatal("the grown count was not recorded on the next day")
	}
	got, _ := a.Events(EventQuery{Cluster: "prod-eks-1", Object: "long-runner"})
	if len(got) != 1 {
		t.Fatalf("%d rows for one repeating event across two days, want 1", len(got))
	}
	if got[0].Count != 40 {
		t.Errorf("Count = %d, want the 40 from the later record — reading newest first must not hand back the older row", got[0].Count)
	}
}

// The rule the early stop rests on: a record lands in the file for the day it
// was written, so nothing in that file happened after the day ended.
func TestTheReadStopsOnceOlderDaysCannotContribute(t *testing.T) {
	day := "2026-08-17"
	end := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)

	newer := func(n int) map[string]Event {
		out := map[string]Event{}
		for i := 0; i < n; i++ {
			out[fmt.Sprint(i)] = Event{Last: end.Add(time.Duration(i+1) * time.Minute)}
		}
		return out
	}
	older := func(n int) map[string]Event {
		out := map[string]Event{}
		for i := 0; i < n; i++ {
			out[fmt.Sprint(i)] = Event{Last: end.Add(-time.Duration(i+1) * time.Minute)}
		}
		return out
	}

	if enoughAlready(newer(2), day, 3) {
		t.Error("the read stopped with fewer events in hand than the limit")
	}
	if enoughAlready(older(10), day, 3) {
		t.Error("the read stopped on events older than the day it was about to open; that day can still contribute")
	}
	if !enoughAlready(newer(3), day, 3) {
		t.Error("the read carried on into a day that cannot change the answer")
	}
	if enoughAlready(newer(3), "not-a-day", 3) {
		t.Error("an unparseable day was treated as settled rather than read")
	}
}

func appendRaw(path, text string) error {
	f, err := openFile(path)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(text)); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// The per-file counts are for the day being written and no other: a week of
// days does not leave a week of entries per cluster behind.
func TestTheEventIndexKeepsOnlyTodaysFiles(t *testing.T) {
	a, c := newArchive(t, on())
	for day := 0; day < 7; day++ {
		for _, cluster := range []string{"prod-eks-1", "staging"} {
			if n, _ := a.RecordEvents(cluster, []Event{sampleEvent(fmt.Sprintf("%s-pod-%d", cluster, day), 1, c.now())}); n != 1 {
				t.Fatalf("day %d, %s: stored %d events, want 1", day, cluster, n)
			}
		}
		c.add(24 * time.Hour)
	}
	a.events.mu.Lock()
	written, loaded := len(a.events.written), len(a.events.loaded)
	a.events.mu.Unlock()
	if written != 2 || loaded != 2 {
		t.Errorf("after seven days of two clusters the index holds %d counts and %d loaded marks, want 2 of each: "+
			"today's files", written, loaded)
	}
}

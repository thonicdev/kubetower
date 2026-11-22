package archive

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Event is one thing a cluster reported about one object, in the shape this
// package stores it.
//
// Deliberately not internal/detail.Event. That type is a view model — it is
// what a drawer draws — and it carries no identity, because the drawer already
// knows which object it is looking at. An archive does: every record has to
// name its own object or it cannot be found again. Converting between the two
// is three lines in the caller, and is much cheaper than a storage format that
// changes whenever a panel does.
type Event struct {
	Cluster   string `json:"cluster,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	// Kind and Object are the involvedObject. UID pins it across a recreation:
	// two pods of the same name in a Deployment's lifetime are two objects, and
	// their events should not merge.
	Kind   string `json:"kind"`
	Object string `json:"object"`
	UID    string `json:"uid,omitempty"`

	Type    string `json:"type"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	From    string `json:"from,omitempty"`

	// Count is the cluster's own repetition count, which grows in place. First
	// and Last are its window.
	Count int32     `json:"count"`
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`

	// Seen is when KubeTower wrote this record. It is not the event's time and is
	// not shown; it exists so that a coalescing read can tell which of two
	// records for the same event is the later one, without trusting a clock in
	// the cluster.
	Seen time.Time `json:"seen"`
}

// Warning reports whether this is the kind of event worth coloring, matching
// internal/detail so a history tab and a drawer agree.
func (e Event) Warning() bool { return strings.EqualFold(e.Type, "Warning") }

// Key is the event's identity, and the whole basis of deduplication.
//
// involvedObject + reason + firstTimestamp, which is what Kubernetes itself
// treats as one event: the count grows and lastTimestamp moves, but those three
// do not. Recording a fresh row every time a BackOff event ticks would be
// wrong twice over — it would claim a hundred events where there was one, and
// it would be most of the disk.
func (e Event) Key() string {
	id := e.UID
	if id == "" {
		id = e.Namespace + "/" + e.Object
	}
	return strings.Join([]string{e.Kind, id, e.Reason, e.First.UTC().Format(time.RFC3339Nano)}, "\x00")
}

// eventIndex remembers what has already been written, so that a repeating event
// is re-recorded only when its count has actually moved and not more often than
// eventUpdateInterval.
//
// It is per (day, cluster) for loading and global for lookup: an event that
// started yesterday and is still repeating today has the same identity, and the
// point of the index is that it is recognized.
type eventIndex struct {
	mu     sync.Mutex
	loaded map[string]bool
	seen   map[string]eventState
	// written is lines already in a (day, cluster) file, for the per-day cap.
	written map[string]int
}

type eventState struct {
	count int32
	at    time.Time
}

// forgetOtherDays drops the per-file records of every day but day. A record
// is filed on the day it is written, so a file of an earlier day is never
// written again, and its count and its loaded mark would otherwise stay for as
// long as the process runs: one entry per cluster and per day. The caller
// holds the index lock.
func (i *eventIndex) forgetOtherDays(day string) {
	prefix := day + "\x00"
	for scope := range i.written {
		if !strings.HasPrefix(scope, prefix) {
			delete(i.written, scope)
		}
	}
	for scope := range i.loaded {
		if !strings.HasPrefix(scope, prefix) {
			delete(i.loaded, scope)
		}
	}
}

func (i *eventIndex) init() {
	i.loaded = map[string]bool{}
	i.seen = map[string]eventState{}
	i.written = map[string]int{}
}

// RecordEvents stores what is new about these events and skips what is not.
//
// It returns how many records it wrote, which is normally far fewer than it was
// given: on a steady cluster the second call with the same events writes
// nothing at all.
//
// The error is for a caller mistake — no cluster name — and never for the disk.
// A failure to write is not this caller's problem to handle: it is recorded in
// Status, logged once, and the events are dropped. A poller that started
// returning errors because a volume filled would end up either spamming a log
// or being written to ignore its own errors, and both are worse than the
// archive saying so in one place.
func (a *Archive) RecordEvents(cluster string, events []Event) (int, error) {
	if strings.TrimSpace(cluster) == "" {
		return 0, fmt.Errorf("a cluster name is required to record events")
	}
	if len(events) == 0 {
		return 0, nil
	}
	if !a.writeReady() {
		return 0, nil
	}

	now := a.now()
	day := a.day(now)

	// The lock is taken before the directory is created, not after. Clear holds
	// the same lock across its removals, and the two must not be able to
	// interleave between the MkdirAll here and the append below — the open
	// would fail on a path whose parent had just gone, and an ordinary Empty
	// click would leave the archive reporting itself as broken.
	a.events.mu.Lock()
	defer a.events.mu.Unlock()

	dir, err := a.clusterDir(day, cluster)
	if err != nil {
		a.fail("events for %s could not be filed: %v", cluster, err)
		return 0, nil
	}
	path := filepath.Join(dir, eventsFileName)

	scope := day + "\x00" + cluster
	if !a.events.loaded[scope] {
		a.events.forgetOtherDays(day)
		a.loadEventIndex(scope, path)
	}
	if len(a.events.seen) > maxTrackedEvents {
		// Dropping the index costs a duplicate record per live event and keeps
		// the process bounded. Rare enough to be worth a line in the log.
		a.logf("archive: the event index reached %d entries and was reset", maxTrackedEvents)
		a.events.seen = map[string]eventState{}
		a.events.loaded = map[string]bool{}
		a.events.loaded[scope] = true
	}

	room := a.maxEventsPerDay - a.events.written[scope]
	if room <= 0 {
		return 0, nil
	}

	var lines []byte
	var pending []struct {
		key   string
		state eventState
	}
	stored := 0
	for _, e := range events {
		if stored >= room {
			a.logf("archive: %s reached the ceiling of %d events for %s; the rest of the day is not recorded",
				cluster, a.maxEventsPerDay, day)
			break
		}
		if e.Kind == "" || e.Object == "" {
			continue
		}
		e.Cluster = cluster
		if e.Last.IsZero() {
			e.Last = e.First
		}
		key := e.Key()
		prev, known := a.events.seen[key]
		switch {
		case !known:
			// Never seen: always recorded.
		case e.Count <= prev.count:
			continue
		case now.Sub(prev.at) < eventUpdateInterval:
			// It is repeating, and it was recorded recently enough. The count
			// will be brought up to date at the next interval.
			continue
		}
		e.Seen = now
		raw, err := json.Marshal(e)
		if err != nil {
			continue
		}
		lines = append(lines, raw...)
		lines = append(lines, '\n')
		pending = append(pending, struct {
			key   string
			state eventState
		}{key, eventState{count: e.Count, at: now}})
		stored++
	}
	if stored == 0 {
		return 0, nil
	}

	if err := a.appendFile(path, lines); err != nil {
		a.fail("events for %s could not be written to %s: %v", cluster, path, err)
		return 0, nil
	}
	a.recovered()
	for _, p := range pending {
		a.events.seen[p.key] = p.state
	}
	a.events.written[scope] += stored
	a.pending.Add(int64(len(lines)))
	return stored, nil
}

// loadEventIndex rebuilds today's dedup state from the file itself, so a
// restart does not re-record everything the cluster is still reporting.
//
// The caller holds the index lock.
func (a *Archive) loadEventIndex(scope, path string) {
	a.events.loaded[scope] = true

	f, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			a.logf("archive: %s could not be read to rebuild the event index: %v", path, err)
		}
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	lines := 0
	for scanner.Scan() {
		lines++
		var e Event
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		key := e.Key()
		if prev, ok := a.events.seen[key]; ok && prev.count >= e.Count {
			continue
		}
		a.events.seen[key] = eventState{count: e.Count, at: e.Seen}
	}
	a.events.written[scope] = lines
}

// appendFile is the one place events are written. Append-only and one write
// call, so that a record is either in the file or not — a torn line would be
// skipped by the reader, but a torn line is also a line that cannot be repaired.
func (a *Archive) appendFile(path string, data []byte) error {
	f, err := a.open(path)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// EventQuery is a history tab's question. Cluster is required; everything else
// narrows.
type EventQuery struct {
	Cluster   string
	Namespace string
	Kind      string
	Object    string
	// Warnings restricts the answer to Warning events.
	Warnings bool
	// Since and Until are optional bounds on the event's own Last time. Zero
	// means unbounded, subject to what is still retained.
	Since time.Time
	Until time.Time
	// Limit caps the answer. Zero means DefaultEventLimit.
	Limit int
}

// DefaultEventLimit is what a query gets when it asks for no limit. Large
// enough for a day of a noisy namespace, small enough to render.
const DefaultEventLimit = 500

// Events answers a query, newest first.
//
// Records are coalesced by identity: the archive holds several rows for one
// repeating event, one per update interval, and a reader wants the event with
// its final count and not its history of counts.
//
// The days are read newest first and the read stops as soon as the answer
// cannot change. That is the difference between a page of history and every
// retained record: ninety days of a cluster at its per-day ceiling is 1.8
// million values to materialize, sort and throw away in order to return five
// hundred rows. A record is written on the day it was polled, so nothing in a
// file can have happened after the end of that day — once `limit` of the events
// already merged are newer than that, no older file can contribute.
func (a *Archive) Events(q EventQuery) ([]Event, error) {
	if strings.TrimSpace(q.Cluster) == "" {
		return nil, fmt.Errorf("a cluster name is required")
	}
	if a.root == "" {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultEventLimit
	}

	dirName := segment(q.Cluster)
	merged := map[string]Event{}

	// The upper bound is widened by a day. A record is filed under the day it
	// was *written*, and an event whose last occurrence was at 23:59 is written
	// on the next day when the poller comes round a minute later; narrowing to
	// the exact day would silently lose it.
	until := q.Until
	if !until.IsZero() {
		until = until.Add(24 * time.Hour)
	}

	// How many days existed and could not be read. Reported beside whatever was
	// read rather than instead of it: a partial history is worth having, and a
	// partial history presented as complete is not.
	unreadable := 0

	// The root above those days: an EACCES there must not read as an empty
	// list and a `(nil, nil)` answer. Carried rather than returned here so that the
	// answer keeps the same shape it has for an unreadable day: whatever was
	// read comes back beside the error.
	all, rootErr := a.days()

	days := daysBetween(all, q.Since, until)
	for i := len(days) - 1; i >= 0; i-- {
		day := days[i]
		if enoughAlready(merged, day, limit) {
			break
		}
		path := filepath.Join(a.root, day, dirName, eventsFileName)
		f, err := os.Open(path)
		if err != nil {
			// **A day that is not there and a day that will not open are
			// different answers**. The absent case is
			// ordinary — most days hold no file for most clusters — so it stays
			// silent. Anything else is counted and reported: EACCES after the
			// state volume is remounted with a different uid, EMFILE under load,
			// a day directory `Prune` is halfway through removing.
			//
			// `internal/web/history.go`'s failure branch depends on this: it
			// says "That is a failure and not an empty history" only when this
			// function returns an error, and otherwise tells the reader nothing
			// happened to the object.
			if !os.IsNotExist(err) {
				unreadable++
			}
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for scanner.Scan() {
			var e Event
			if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
				continue // a torn line, from a write cut short
			}
			if !q.matches(e) {
				continue
			}
			key := e.Key()
			if prev, ok := merged[key]; ok && !laterRecord(e, prev) {
				continue
			}
			merged[key] = e
		}
		f.Close()
	}

	out := make([]Event, 0, len(merged))
	for _, e := range merged {
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Last.Equal(out[j].Last) {
			return out[i].Object < out[j].Object
		}
		return out[i].Last.After(out[j].Last)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	if rootErr != nil {
		// Checked first because when it fires there is nothing below it to
		// count: no day was listed, so `unreadable` is 0 and reporting only
		// that would say the archive is empty, which is exactly the failure to
		// avoid.
		return out, rootErr
	}
	if unreadable > 0 {
		// The rows come back *with* the error. `internal/archive/usage.go` takes
		// the same line for the same reason — "so this is a floor, not a total".
		return out, fmt.Errorf("%d day(s) of this cluster's event archive exist and could not be read", unreadable)
	}
	return out, nil
}

// laterRecord reports whether a is the more recent of two records of the same
// event: the one written later, and on a tie the one with the higher count.
//
// It exists so that the merge does not depend on the order the files are read
// in. Overwriting whatever was there last is correct only while the days are
// read oldest first, an assumption that survives a change of iteration order
// silently and wrongly.
func laterRecord(a, b Event) bool {
	if a.Seen.Equal(b.Seen) {
		return a.Count > b.Count
	}
	return a.Seen.After(b.Seen)
}

// enoughAlready reports whether the events merged so far settle the answer
// without reading day, or anything older than it.
//
// A record lands in the file for the day it was written, so no event in that
// file happened after the day ended. If `limit` merged events are already at
// least that new, they are the answer.
func enoughAlready(merged map[string]Event, day string, limit int) bool {
	if len(merged) < limit {
		return false
	}
	start, err := time.Parse(dayLayout, day)
	if err != nil {
		return false
	}
	end := start.AddDate(0, 0, 1)
	newer := 0
	for _, e := range merged {
		if !e.Last.Before(end) {
			newer++
			if newer >= limit {
				return true
			}
		}
	}
	return false
}

func (q EventQuery) matches(e Event) bool {
	if q.Namespace != "" && e.Namespace != q.Namespace {
		return false
	}
	if q.Kind != "" && !strings.EqualFold(e.Kind, q.Kind) {
		return false
	}
	if q.Object != "" && e.Object != q.Object {
		return false
	}
	if q.Warnings && !e.Warning() {
		return false
	}
	// The bounds are on the event's own window, not on the day directory: an
	// event first seen last week and still repeating is in today's file.
	if !q.Since.IsZero() && e.Last.Before(q.Since) {
		return false
	}
	if !q.Until.IsZero() && e.First.After(q.Until) {
		return false
	}
	return true
}

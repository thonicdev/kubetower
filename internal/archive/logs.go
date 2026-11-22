package archive

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// LogLine is one archived line: when KubeTower received it, and what it said.
// The timestamp is ours, since a container's own is often absent or in any
// format.
type LogLine struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// logStamp is the on-disk prefix: RFC3339 with milliseconds, a space, then the
// line. Plain text rather than JSON, so the archive reads with `less` and `grep`.
const logStamp = "2006-01-02T15:04:05.000Z07:00"

// sinkKey identifies a sink and the file it writes to: one sink per path.
type sinkKey struct{ cluster, namespace, pod, container string }

// LogWriter opens a sink for one container's output.
//
// The returned writer is never nil and never returns an error: it is teed into
// a live stream (io.MultiWriter(browser, sink)), which must not break. When
// the archive is off, has no directory, or has just failed to write, the sink
// discards.
//
// Callers asking for the same container share one reference-counted sink:
// two independent sinks on one file would each count bytes separately and
// enforce the daily ceiling at a multiple of its value.
//
// Close it when the stream ends; an unclosed sink holds a file handle and its
// last partial line until the archive's own Close.
func (a *Archive) LogWriter(cluster, namespace, pod, container string) io.WriteCloser {
	if !a.CapturesLogs() || a.root == "" {
		return discard{}
	}
	if cluster == "" || namespace == "" || pod == "" || container == "" {
		a.logf("archive: a log sink was asked for without a full identity (%q %q/%q %q); it will discard",
			cluster, namespace, pod, container)
		return discard{}
	}
	key := sinkKey{cluster, namespace, pod, container}

	a.sinksMu.Lock()
	s := a.sinks[key]
	if s == nil {
		s = &LogSink{
			a:         a,
			key:       key,
			cluster:   cluster,
			namespace: namespace,
			pod:       pod,
			container: container,
		}
		a.sinks[key] = s
	}
	s.refs++
	a.sinksMu.Unlock()
	return &sinkRef{sink: s}
}

// sinkRef is one caller's handle on a shared sink. Closing it releases that
// caller's share only, once, however many times it is called.
type sinkRef struct {
	sink *LogSink
	once sync.Once
}

func (r *sinkRef) Write(p []byte) (int, error) { return r.sink.Write(p) }

func (r *sinkRef) Close() error {
	r.once.Do(r.sink.release)
	return nil
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
func (discard) Close() error                { return nil }

// LogSink is one container's archived output. It splits arbitrary chunks into
// lines and stamps each, holding a partial line in a buffer capped so output
// with no newline cannot grow memory.
type LogSink struct {
	a                                  *Archive
	key                                sinkKey
	cluster, namespace, pod, container string

	// refs is how many handles are still open on it. Guarded by a.sinksMu, not
	// mu: the registry owns the sharing, and mu is held across file writes.
	refs int

	mu        sync.Mutex
	day       string
	file      wfile
	written   int64
	partial   []byte
	truncated bool
	closed    bool
}

// currentDay is the day this sink is writing into, read under the lock that
// writes it; the pruner uses it to close a sink before removing its day.
func (s *LogSink) currentDay() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.day
}

// Write always reports success: on a full disk the bytes are dropped, the
// archive goes degraded, and the stream to the browser carries on.
func (s *LogSink) Write(p []byte) (int, error) {
	n := len(p)
	if n == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return n, nil
	}

	rest := p
	for {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			break
		}
		s.partial = append(s.partial, rest[:i]...)
		s.flushLine()
		rest = rest[i+1:]
	}
	s.partial = append(s.partial, rest...)
	if len(s.partial) >= maxPartialLine {
		// No newline in sight: write what there is rather than grow; the line
		// is split.
		s.flushLine()
	}
	return n, nil
}

// release gives up one handle's share of the sink. The last one out writes the
// partial line and closes the file.
func (s *LogSink) release() {
	s.a.sinksMu.Lock()
	s.refs--
	last := s.refs <= 0
	if last {
		// Only if it is still the registered sink for its key: closeSinks may
		// have replaced it with a new one.
		if cur, ok := s.a.sinks[s.key]; ok && cur == s {
			delete(s.a.sinks, s.key)
		}
	}
	s.a.sinksMu.Unlock()

	if last {
		s.close()
	}
}

// close writes the last partial line and releases the file. Idempotent, and it
// takes no registry lock — the caller has already dealt with the registry.
func (s *LogSink) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if len(s.partial) > 0 {
		s.flushLine()
	}
	s.shut()
	s.closed = true
}

// shut closes the underlying file. The caller holds the sink lock.
func (s *LogSink) shut() {
	if s.file != nil {
		if err := s.file.Close(); err != nil {
			s.a.logf("archive: closing the log file for %s/%s: %v", s.namespace, s.pod, err)
		}
		s.file = nil
	}
}

// flushLine writes s.partial as one stamped line and empties it. The caller
// holds the sink lock.
func (s *LogSink) flushLine() {
	line := s.partial
	s.partial = s.partial[:0]

	// One reading of the clock for the directory and the stamp, so a line at
	// midnight is not filed under the wrong day.
	now := s.a.now()

	// logsReady covers the whole-archive ceiling as well, which stops log
	// capture and leaves event recording alone.
	if !s.a.logsReady() {
		return
	}

	day := s.a.day(now)
	if day != s.day {
		// The day rolled: new file, and the per-day allowance starts again.
		s.shut()
		s.day = day
		s.written = 0
		s.truncated = false
	}
	if s.truncated {
		return
	}
	if s.file == nil && !s.openFile() {
		return
	}

	// A carriage return would read as a second line break.
	text := strings.ReplaceAll(string(line), "\r", "")
	stamped := now.UTC().Format(logStamp) + " " + text + "\n"

	if _, err := s.file.Write([]byte(stamped)); err != nil {
		s.a.fail("the log archive for %s/%s could not be written: %v", s.namespace, s.pod, err)
		s.shut()
		return
	}
	s.a.recovered()
	s.written += int64(len(stamped))
	s.a.pending.Add(int64(len(stamped)))

	if s.written >= s.a.maxLogPerStream {
		marker := now.UTC().Format(logStamp) +
			fmt.Sprintf(" [kubetower] this container reached the archive's ceiling of %s for one day; the rest is not kept\n",
				humanBytes(s.a.maxLogPerStream))
		if _, err := s.file.Write([]byte(marker)); err == nil {
			s.a.pending.Add(int64(len(marker)))
		}
		s.truncated = true
		s.shut()
		s.a.logf("archive: %s/%s/%s reached the %s daily ceiling", s.namespace, s.pod, s.container,
			humanBytes(s.a.maxLogPerStream))
	}
}

// openFile creates the day's file for this container. Reports whether writing
// can proceed. The caller holds the sink lock.
func (s *LogSink) openFile() bool {
	dir, err := s.a.clusterDir(s.day, s.cluster)
	if err != nil {
		s.a.fail("the log archive for %s could not be filed: %v", s.cluster, err)
		return false
	}
	dir = filepath.Join(dir, logsDirName, segment(s.namespace), segment(s.pod))
	if err := s.a.inRoot(dir); err != nil {
		s.a.fail("%v", err)
		return false
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		s.a.fail("%s could not be created: %v", dir, err)
		return false
	}
	path := filepath.Join(dir, segment(s.container)+".log")
	f, err := s.a.open(path)
	if err != nil {
		s.a.fail("%s could not be opened: %v", path, err)
		return false
	}
	s.file = f
	if info, err := os.Stat(path); err == nil {
		// Reopened: continue the day's allowance where it stood.
		s.written = info.Size()
	}
	return true
}

// closeSinks closes every sink the predicate selects, whatever their handles
// think: when log capture is turned off, and before a prune removes a day a
// sink may hold open. Handles keep a closed sink, which discards. Sinks are
// deregistered first so a later release cannot delete their replacement.
func (a *Archive) closeSinks(match func(*LogSink) bool) {
	a.sinksMu.Lock()
	var doomed []*LogSink
	for key, s := range a.sinks {
		if match(s) {
			doomed = append(doomed, s)
			delete(a.sinks, key)
		}
	}
	a.sinksMu.Unlock()

	for _, s := range doomed {
		s.close()
	}
}

// Stream is one container that has archived output.
type Stream struct {
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	// Days with data, oldest first, as YYYY-MM-DD.
	Days  []string `json:"days"`
	Bytes int64    `json:"bytes"`
	// Updated is the newest file's modification time, used to sort.
	Updated time.Time `json:"updated"`
}

// StreamQuery narrows the list of archived containers. Every field is optional;
// with none, it is everything retained.
type StreamQuery struct {
	Cluster   string
	Namespace string
	Pod       string
	Limit     int
}

// DefaultStreamLimit caps a stream listing.
const DefaultStreamLimit = 500

// Streams lists what has archived output, most recently written first.
func (a *Archive) Streams(q StreamQuery) ([]Stream, error) {
	if a.root == "" {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultStreamLimit
	}

	type key struct{ cluster, namespace, pod, container string }
	found := map[key]*Stream{}

	// An empty index would say there is no output, so a root that will not
	// list fails the call.
	all, err := a.days()
	if err != nil {
		return nil, err
	}

	for _, day := range all {
		dayDir := a.dayDir(day)
		clusters, err := os.ReadDir(dayDir)
		if err != nil {
			continue
		}
		for _, c := range clusters {
			if !c.IsDir() {
				continue
			}
			if q.Cluster != "" && c.Name() != segment(q.Cluster) {
				continue
			}
			cluster := readCluster(filepath.Join(dayDir, c.Name()))
			root := filepath.Join(dayDir, c.Name(), logsDirName)

			namespaces, err := os.ReadDir(root)
			if err != nil {
				continue
			}
			for _, ns := range namespaces {
				if !ns.IsDir() || (q.Namespace != "" && ns.Name() != segment(q.Namespace)) {
					continue
				}
				pods, err := os.ReadDir(filepath.Join(root, ns.Name()))
				if err != nil {
					continue
				}
				for _, pod := range pods {
					if !pod.IsDir() || (q.Pod != "" && pod.Name() != segment(q.Pod)) {
						continue
					}
					files, err := os.ReadDir(filepath.Join(root, ns.Name(), pod.Name()))
					if err != nil {
						continue
					}
					for _, f := range files {
						if f.IsDir() || !strings.HasSuffix(f.Name(), ".log") {
							continue
						}
						container := strings.TrimSuffix(f.Name(), ".log")
						k := key{cluster, ns.Name(), pod.Name(), container}
						s := found[k]
						if s == nil {
							s = &Stream{
								Cluster:   cluster,
								Namespace: ns.Name(),
								Pod:       pod.Name(),
								Container: container,
							}
							found[k] = s
						}
						s.Days = append(s.Days, day)
						if info, err := f.Info(); err == nil {
							s.Bytes += info.Size()
							if info.ModTime().After(s.Updated) {
								s.Updated = info.ModTime()
							}
						}
					}
				}
			}
		}
	}

	out := make([]Stream, 0, len(found))
	for _, s := range found {
		sort.Strings(s.Days)
		out = append(out, *s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Updated.Equal(out[j].Updated) {
			return out[i].Pod < out[j].Pod
		}
		return out[i].Updated.After(out[j].Updated)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// LogQuery is a request to read back one container's archived output.
type LogQuery struct {
	Cluster   string
	Namespace string
	Pod       string
	Container string
	Since     time.Time
	Until     time.Time
	// Tail is how many lines from the end. Zero means DefaultLogTail; it is
	// clamped to MaxLogTail.
	Tail int
}

// Bounds on a read.
const (
	DefaultLogTail = 1000
	MaxLogTail     = 20000
)

// Log reads back archived output, oldest line first.
//
// The day files in range are scanned through a ring of Tail lines, like
// `tail -n`, so memory is bounded by the request. The ring uses a head index:
// shifting the buffer per line would copy gigabytes for one day file.
func (a *Archive) Log(q LogQuery) ([]LogLine, error) {
	if a.root == "" {
		return nil, nil
	}
	if q.Cluster == "" || q.Namespace == "" || q.Pod == "" || q.Container == "" {
		return nil, fmt.Errorf("cluster, namespace, pod and container are all required")
	}
	tail := q.Tail
	if tail <= 0 {
		tail = DefaultLogTail
	}
	if tail > MaxLogTail {
		tail = MaxLogTail
	}

	ring := make([]LogLine, tail)
	head, filled := 0, 0
	push := func(l LogLine) {
		ring[head] = l
		head++
		if head == tail {
			head = 0
		}
		if filled < tail {
			filled++
		}
	}

	// Days that exist and will not open: counted, not swallowed.
	unreadable := 0

	// A root that will not list is returned with the lines, as an unreadable
	// day is.
	all, rootErr := a.days()

	rel := filepath.Join(segment(q.Cluster), logsDirName, segment(q.Namespace), segment(q.Pod), segment(q.Container)+".log")
	for _, day := range daysBetween(all, q.Since, q.Until) {
		f, err := os.Open(filepath.Join(a.dayDir(day), rel))
		if err != nil {
			// Absent is ordinary; unreadable is a finding.
			if !os.IsNotExist(err) {
				unreadable++
			}
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for scanner.Scan() {
			line, ok := parseLine(scanner.Text())
			if !ok {
				continue
			}
			if !q.Since.IsZero() && line.At.Before(q.Since) {
				continue
			}
			if !q.Until.IsZero() && line.At.After(q.Until) {
				continue
			}
			push(line)
		}
		f.Close()
	}

	// Unwind the ring into reading order. head is one past the newest line, so
	// the oldest of the ones kept is head-filled.
	start := head - filled
	if start < 0 {
		start += tail
	}
	out := make([]LogLine, 0, filled)
	for i := 0; i < filled; i++ {
		j := start + i
		if j >= tail {
			j -= tail
		}
		out = append(out, ring[j])
	}
	if rootErr != nil {
		// First: no day was listed, so the counter below would read 0.
		return out, rootErr
	}
	if unreadable > 0 {
		// The lines come back with the error: a partial tail is worth reading,
		// but not as the whole one.
		return out, fmt.Errorf("%d day(s) of this container's log archive exist and could not be read", unreadable)
	}
	return out, nil
}

// parseLine splits the stamp from the text. A line with no stamp (a write cut
// short by a crash) is skipped rather than shown with a zero time.
func parseLine(raw string) (LogLine, bool) {
	space := strings.IndexByte(raw, ' ')
	if space <= 0 {
		return LogLine{}, false
	}
	at, err := time.Parse(logStamp, raw[:space])
	if err != nil {
		return LogLine{}, false
	}
	return LogLine{At: at, Text: raw[space+1:]}, true
}

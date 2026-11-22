// Package archive keeps a window of what Kubernetes forgets.
//
// Events are dropped after about an hour by default, and a pod's logs go with
// the pod, which is when they are wanted. This is a retention store for both:
// seven days by default, configurable, logs off by default. Plain files rather
// than a database, so nothing ever needs a migration to start.
//
// # The layout, and why pruning is a directory removal
//
//	<KT_STATE_DIR>/archive/
//	  settings.json                       retention and the on/off switches
//	  2026-08-17/                         one directory per UTC day
//	    prod-eks-1-3f2a1c9d/              one per cluster, name sanitized + hashed
//	      cluster                         the context name, verbatim
//	      events.jsonl                    one JSON object per line, append-only
//	      logs/<namespace>/<pod>/<container>.log
//	  2026-08-16/
//	  …
//
// The day is the top level, so expiring old data is a `ReadDir` of the root and
// an `os.RemoveAll` per expired day: nothing kept is ever traversed and there is
// no index to keep in step. The cost is that a size breakdown walks the tree;
// that happens on demand and is cached, while pruning runs on a timer.
//
// # What happens when the disk fills
//
// Nothing that reaches the console. Every write path swallows its own failures:
// a log sink's Write always reports success, and RecordEvents reports what it
// stored. A failed write puts the archive into a degraded state, visible through
// Status and logged once, and suspends writing for a cooldown so a full disk is
// not a retry storm.
//
// The archive reaching its own size ceiling is a separate state, reported apart:
// log capture stops and events carry on, since events are far smaller. Sharing
// the cooldown would let a chatty container keep event recording off too.
//
// # What is in here
//
// Log lines are written to disk unredacted, since redaction would be guessing.
// Directories are 0700 and files 0600; the archive is not encrypted.
package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// EnvDir names the state directory, shared with the other state files. The
// container sets it to /var/lib/kubetower and mounts a volume there.
const EnvDir = "KT_STATE_DIR"

// Names on disk.
const (
	// DirName is the archive's own subdirectory of the state directory, so
	// pruning its root can never reach the other state files.
	DirName          = "archive"
	SettingsFileName = "settings.json"
	eventsFileName   = "events.jsonl"
	logsDirName      = "logs"
	clusterFileName  = "cluster"
)

// Retention bounds, in days; Retention() converts.
const (
	DefaultRetentionDays = 7
	MinRetentionDays     = 1
	MaxRetentionDays     = 90
)

// Ceilings, so a mistake elsewhere costs bounded disk rather than the volume.
const (
	// DefaultMaxTotalBytes is the whole archive. Reaching it stops log capture
	// only: events are far smaller.
	DefaultMaxTotalBytes = 2 << 30 // 2 GiB
	// DefaultMaxLogBytesPerStreamDay bounds one container on one day, the same
	// figure as the live stream's ceiling in internal/logs.
	DefaultMaxLogBytesPerStreamDay = 8 << 20 // 8 MiB
	// DefaultMaxEventsPerClusterDay bounds one cluster's event file per day.
	DefaultMaxEventsPerClusterDay = 20000

	// maxPartialLine bounds what a sink holds while waiting for a newline.
	maxPartialLine = 64 << 10
	// maxTrackedEvents bounds the dedup index. Past it the index is dropped and
	// rebuilt, which costs a few duplicate records and not the process.
	maxTrackedEvents = 50000
	// eventUpdateInterval is how often a repeating event's growing count is
	// re-recorded, so one repeating event cannot fill the archive.
	eventUpdateInterval = 5 * time.Minute
	// writeCooldown is how long writing is suspended after a failure, so a full
	// disk is not retried on every write.
	writeCooldown = 60 * time.Second
	// pruneInterval is the pruning schedule.
	pruneInterval = 1 * time.Hour
)

// Permissions: production log content at rest.
const (
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

// Settings is what the operator controls.
type Settings struct {
	// Enabled is the master switch. Off means nothing is written and nothing
	// already written is touched; Clear deletes.
	Enabled bool `json:"enabled"`
	// Logs is log capture, separate because it is the expensive half.
	Logs bool `json:"logs"`
	// RetentionDays is the window, clamped to [MinRetentionDays, MaxRetentionDays].
	RetentionDays int `json:"retentionDays"`
}

// Defaults is what an installation gets with no settings file and no
// environment: events kept for a week, logs off, since writing production
// output to a volume should be a deliberate choice.
func Defaults() Settings {
	return Settings{Enabled: true, Logs: false, RetentionDays: DefaultRetentionDays}
}

// Retention is the window as a duration, for display.
func (s Settings) Retention() time.Duration {
	return time.Duration(s.normalised().RetentionDays) * 24 * time.Hour
}

func (s Settings) normalised() Settings {
	if s.RetentionDays < MinRetentionDays {
		s.RetentionDays = DefaultRetentionDays
	}
	if s.RetentionDays > MaxRetentionDays {
		s.RetentionDays = MaxRetentionDays
	}
	if !s.Enabled {
		// Logs cannot be on while the archive is off.
		s.Logs = false
	}
	return s
}

// Validate reports what is wrong with settings a form has just posted, in a
// sentence that can be put on screen.
func (s Settings) Validate() error {
	if s.RetentionDays < MinRetentionDays || s.RetentionDays > MaxRetentionDays {
		return fmt.Errorf("retention must be between %d and %d days, not %d", MinRetentionDays, MaxRetentionDays, s.RetentionDays)
	}
	if s.Logs && !s.Enabled {
		return fmt.Errorf("log retention cannot be on while the archive is off")
	}
	return nil
}

// Status is everything a settings page needs to say about the archive besides
// its size.
type Status struct {
	Settings
	// Root is where it lives, or "" when the archive is inert.
	Root string
	// Writable is false when a write has failed recently. Note says why, and
	// Since is when it started.
	Writable bool
	Note     string
	Since    time.Time

	// LogsSuspended is the whole-archive size ceiling having stopped log
	// capture. Not Writable: the disk is fine and events still go in.
	// LogsNote says which ceiling and what clears it.
	LogsSuspended bool
	LogsNote      string

	LastPrune     time.Time
	LastPruneNote string
}

// Options configures an archive. Only Root is required.
type Options struct {
	// Root is the archive directory. Empty means inert: every write is a no-op
	// and every read is empty.
	Root string
	// Defaults apply when there is no settings file. Use FromEnv to derive them
	// from KT_ARCHIVE, KT_ARCHIVE_LOGS and KT_ARCHIVE_RETENTION_DAYS.
	Defaults Settings
	Logf     func(string, ...any)
	// Now is the clock. Nil means time.Now.
	Now func() time.Time

	// ForceOff keeps the archive off whatever the settings file says, and
	// SetSettings cannot turn it back on; what is there is still pruned.
	// Defaults are not enough: a settings file carried into a replicated
	// deployment would switch it on, each pod recording part of the window.
	ForceOff bool

	MaxTotalBytes           int64
	MaxLogBytesPerStreamDay int64
	MaxEventsPerClusterDay  int
}

// Dir is where the archive lives: <KT_STATE_DIR>/archive.
func Dir() string {
	return filepath.Join(stateDir(), DirName)
}

// stateDir duplicates `statefile.Dir()` and must keep answering the same
// thing, or the archive and the other state files would land in different
// directories with the volume mounted at only one. Change both together;
// nothing in the build checks it.
func stateDir() string {
	if dir := strings.TrimSpace(os.Getenv(EnvDir)); dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".kubetower")
	}
	return "kubetower-state"
}

// FromEnv reads the boot-time defaults. The settings file, when there is one,
// wins over these.
func FromEnv() Settings {
	s := Defaults()
	if v, ok := boolEnv("KT_ARCHIVE"); ok {
		s.Enabled = v
	}
	if v, ok := boolEnv("KT_ARCHIVE_LOGS"); ok {
		s.Logs = v
	}
	if raw := strings.TrimSpace(os.Getenv("KT_ARCHIVE_RETENTION_DAYS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			s.RetentionDays = n
		}
	}
	return s.normalised()
}

func boolEnv(name string) (bool, bool) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch raw {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	}
	return false, false
}

// Archive is the store. One per process.
//
// Locking is in three pieces: settings and status are read on every write and
// rarely changed (RWMutex); the dedup index is touched only by writers
// (Mutex); and the size is a cached walk plus an atomic counter, so asking for
// it never waits behind a log line.
type Archive struct {
	// forceOff is Options.ForceOff: nothing is recorded whatever the settings
	// say, and SetSettings cannot switch it back on.
	forceOff bool

	root string
	logf func(string, ...any)
	now  func() time.Time

	maxTotal        int64
	maxLogPerStream int64
	maxEventsPerDay int

	mu       sync.RWMutex
	settings Settings
	// writable and its companions are the disk-full state, guarded by mu.
	// since is when it started failing (shown); lastFail is when it last
	// failed (the cooldown counts from it).
	writable      bool
	note          string
	since         time.Time
	lastFail      time.Time
	complainedAt  time.Time
	lastPrune     time.Time
	lastPruneNote string
	// logsSuspended is the whole-archive ceiling, separate from writable: it is
	// not a disk failure, and gating on writable would let a chatty sink keep
	// re-arming the cooldown and starve event recording.
	//
	// Only flushLine sets it and only Prune and Clear clear it.
	logsSuspended bool
	logsNote      string

	events eventIndex

	// sinks is one sink per container, shared by its callers (see LogWriter).
	sinksMu sync.Mutex
	sinks   map[sinkKey]*LogSink

	usageMu   sync.RWMutex
	usage     Usage
	usageSeen bool
	// usageGen counts completed walks, so a caller that queued for usageWalk
	// can tell another walk finished meanwhile.
	usageGen atomic.Int64
	// usageWalk is the single-flight for RefreshUsage, its own lock rather than
	// usageMu so a walk never makes readers wait.
	usageWalk sync.Mutex
	// usageArrived is a test seam, nil in production. It runs after a caller
	// has recorded its arrival generation and before it queues for usageWalk.
	usageArrived func()
	// pending is bytes written since the last walk. Atomic, so a writer never
	// touches usageMu and a size query never blocks a writer.
	pending atomic.Int64

	startOnce sync.Once
	started   atomic.Bool
	closeOnce sync.Once
	stopped   chan struct{}
	done      chan struct{}

	// open is the seam for injecting a failing disk in tests.
	open func(path string) (wfile, error)

	// readDir is the reading half of that seam, used by days(), so a root that
	// will not list can be tested on every platform (Windows mode bits cannot
	// deny the owner).
	readDir func(path string) ([]os.DirEntry, error)
}

// wfile is the part of *os.File this package writes through.
type wfile interface {
	io.WriteCloser
}

// Open prepares an archive. It never fails: a root that cannot be created is a
// degraded archive that reports why, not a process that will not start.
func Open(opts Options) *Archive {
	a := &Archive{
		root:            strings.TrimSpace(opts.Root),
		logf:            opts.Logf,
		now:             opts.Now,
		maxTotal:        opts.MaxTotalBytes,
		maxLogPerStream: opts.MaxLogBytesPerStreamDay,
		maxEventsPerDay: opts.MaxEventsPerClusterDay,
		writable:        true,
		sinks:           map[sinkKey]*LogSink{},
		stopped:         make(chan struct{}),
		done:            make(chan struct{}),
		open:            openFile,
		readDir:         os.ReadDir,
	}
	if a.logf == nil {
		a.logf = func(string, ...any) {}
	}
	if a.now == nil {
		a.now = time.Now
	}
	if a.maxTotal <= 0 {
		a.maxTotal = DefaultMaxTotalBytes
	}
	if a.maxLogPerStream <= 0 {
		a.maxLogPerStream = DefaultMaxLogBytesPerStreamDay
	}
	if a.maxEventsPerDay <= 0 {
		a.maxEventsPerDay = DefaultMaxEventsPerClusterDay
	}
	if a.root != "" {
		a.root = filepath.Clean(a.root)
	}
	a.events.init()

	defaults := opts.Defaults.normalised()
	if defaults.RetentionDays == 0 {
		defaults = Defaults()
	}
	a.settings = defaults
	a.loadSettings()
	a.forceOff = opts.ForceOff
	if a.forceOff {
		if a.settings.Enabled || a.settings.Logs {
			a.logf("archive: off, because this process is one of several replicas; the settings file " +
				"that says otherwise is kept, and not obeyed")
		}
		a.settings.Enabled, a.settings.Logs = false, false
	}

	if a.root == "" {
		a.writable = false
		a.note = "no state directory, so nothing is being kept"
	} else if err := os.MkdirAll(a.root, dirPerm); err != nil {
		a.fail("the archive directory %s could not be created: %v", a.root, err)
	} else {
		a.sweepTempSettings()
	}
	return a
}

func openFile(path string) (wfile, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, filePerm)
}

// Root is the archive directory, or "" when the archive is inert.
func (a *Archive) Root() string { return a.root }

// Settings is the current configuration.
func (a *Archive) Settings() Settings {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.settings
}

// Enabled reports whether anything is being written at all.
func (a *Archive) Enabled() bool { return a.Settings().Enabled }

// CapturesLogs reports whether log capture is on, so a caller can skip setting
// up a tee rather than write into a sink that discards.
func (a *Archive) CapturesLogs() bool {
	s := a.Settings()
	return s.Enabled && s.Logs
}

// Retention is the window as a duration.
func (a *Archive) Retention() time.Duration { return a.Settings().Retention() }

// Status is settings plus the health of the store.
func (a *Archive) Status() Status {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return Status{
		Settings:      a.settings,
		Root:          a.root,
		Writable:      a.writable,
		Note:          a.note,
		Since:         a.since,
		LogsSuspended: a.logsSuspended,
		LogsNote:      a.logsNote,
		LastPrune:     a.lastPrune,
		LastPruneNote: a.lastPruneNote,
	}
}

// Writable reports whether the archive is currently taking writes, and why not
// when it is not. The event poller needs it because every write path swallows
// its failures, so a full disk reaches it as `(0, nil)`.
//
// An archive that is switched off, or has nowhere to write, is reported as
// writable: neither is a fault.
func (a *Archive) Writable() (bool, string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.settings.Enabled || a.root == "" {
		return true, ""
	}
	return a.writable, a.note
}

// SetSettings applies and persists a change.
//
// It applies in memory even when it cannot be persisted, and says so in the
// error. Turning log capture off closes every open sink.
//
// The field and the file change under one hold of a.mu, so two concurrent
// saves cannot leave memory and settings.json disagreeing, nor have one save's
// sweepTempSettings delete the other's temporary file. The cost is that
// readers of a.mu wait for one fsync, on a rare operator gesture.
func (a *Archive) SetSettings(s Settings) (Settings, error) {
	if err := s.Validate(); err != nil {
		return a.Settings(), err
	}
	s = s.normalised()
	if a.forceOff {
		// Refused here too, so no caller can turn it on.
		s.Enabled, s.Logs = false, false
	}

	a.mu.Lock()
	a.settings = s
	err := a.saveSettingsLocked()
	a.mu.Unlock()

	// Housekeeping, not the gate: logsReady already stops every sink at the
	// assignment above; this releases file handles. With a.mu released, since
	// close() flushes through flushLine, which takes a.mu. A partial line held
	// when capture goes off is dropped.
	if !s.Enabled || !s.Logs {
		a.closeSinks(func(*LogSink) bool { return true })
	}
	return s, err
}

func (a *Archive) settingsPath() string {
	if a.root == "" {
		return ""
	}
	return filepath.Join(a.root, SettingsFileName)
}

func (a *Archive) loadSettings() {
	path := a.settingsPath()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			a.logf("archive: %s could not be read (%v); using the configured defaults", path, err)
		}
		return
	}
	var file Settings
	if err := json.Unmarshal(raw, &file); err != nil {
		a.logf("archive: %s is not readable JSON (%v); using the configured defaults", path, err)
		return
	}
	a.settings = file.normalised()
}

// saveSettingsLocked writes a.settings to the file atomically. The caller
// holds a.mu for writing; it takes no argument so a.settings is the one source.
func (a *Archive) saveSettingsLocked() error {
	path := a.settingsPath()
	if path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(a.settings, "", "  ")
	if err != nil {
		return fmt.Errorf("the archive settings could not be encoded: %w", err)
	}
	if err := os.MkdirAll(a.root, dirPerm); err != nil {
		return fmt.Errorf("%s cannot be created (%w); the setting applies now but not after a restart", a.root, err)
	}
	tmp, err := os.CreateTemp(a.root, SettingsFileName+".*")
	if err != nil {
		return fmt.Errorf("nothing can be written in %s (%w); the setting applies now but not after a restart", a.root, err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("writing %s: %w", name, err)
	}
	// Sync before the rename, or a crash can leave a zero-length file that
	// silently loads as the defaults.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("writing %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("writing %s: %w", name, err)
	}
	if err := os.Chmod(name, filePerm); err != nil {
		a.logf("archive: %s could not be restricted to its owner: %v", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("%s could not be replaced (%w); the setting applies now but not after a restart", path, err)
	}
	a.sweepTempSettings()
	return nil
}

// sweepTempSettings removes the temporary files a crash between CreateTemp and
// Rename would leave behind; the pruner never touches them, and they would
// count in the size.
func (a *Archive) sweepTempSettings() {
	if a.root == "" {
		return
	}
	matches, err := filepath.Glob(filepath.Join(a.root, SettingsFileName+".*"))
	if err != nil {
		return
	}
	for _, path := range matches {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			a.logf("archive: %s is left over from an interrupted save and could not be removed: %v", path, err)
		}
	}
}

// Start prunes once and then on a schedule, until ctx is canceled or Close is
// called. Calling it twice is a no-op.
func (a *Archive) Start(ctx context.Context) {
	a.startOnce.Do(func() {
		a.started.Store(true)
		go a.loop(ctx)
	})
}

func (a *Archive) loop(ctx context.Context) {
	defer close(a.done)

	if _, err := a.Prune(); err != nil {
		a.logf("archive: the first prune did not finish: %v", err)
	}
	a.RefreshUsage()

	ticker := time.NewTicker(pruneInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopped:
			return
		case <-ticker.C:
			if _, err := a.Prune(); err != nil {
				a.logf("archive: prune: %v", err)
			}
			a.RefreshUsage()
		}
	}
}

// Close stops the pruner and closes every open log sink. Safe to call more than
// once, and safe to call on an archive that was never started.
func (a *Archive) Close() error {
	a.closeOnce.Do(func() { close(a.stopped) })
	if a.started.Load() {
		<-a.done
	}
	a.closeSinks(func(*LogSink) bool { return true })
	return nil
}

// writeReady reports whether a write should be attempted at all: the archive is
// on, there is somewhere to write, and a recent failure's cooldown has expired.
func (a *Archive) writeReady() bool {
	a.mu.RLock()
	on, root, writable, last := a.settings.Enabled, a.root, a.writable, a.lastFail
	a.mu.RUnlock()

	if !on || root == "" {
		return false
	}
	if writable {
		return true
	}
	if a.now().Sub(last) < writeCooldown {
		return false
	}
	// The cooldown has expired: let one write through; a failure resets it.
	return true
}

// fail records a write failure, logged once per cooldown rather than per line.
func (a *Archive) fail(format string, args ...any) {
	note := fmt.Sprintf(format, args...)
	now := a.now()

	a.mu.Lock()
	if a.writable {
		a.since = now
	}
	a.writable = false
	a.lastFail = now
	a.note = note
	quiet := !a.complainedAt.IsZero() && now.Sub(a.complainedAt) < writeCooldown
	if !quiet {
		a.complainedAt = now
	}
	a.mu.Unlock()

	if !quiet {
		a.logf("archive: %s", note)
	}
}

// logsReady reports whether a log line should be written: everything
// writeReady asks, plus log capture being on, plus the whole-archive size
// ceiling. The ceiling stops log capture only; RecordEvents does not call this.
//
// Settings.Logs is the enforcement: closeSinks only reaches sinks registered
// when it runs, and one registered just after capture went off would otherwise
// keep writing production logs to disk.
func (a *Archive) logsReady() bool {
	if !a.writeReady() {
		return false
	}
	a.mu.RLock()
	capturing, suspended := a.settings.Logs, a.logsSuspended
	a.mu.RUnlock()
	if !capturing || suspended {
		return false
	}
	if a.approxBytes() >= a.maxTotal {
		a.suspendLogs("the archive has reached its ceiling of %s; log capture is paused until the next prune", humanBytes(a.maxTotal))
		return false
	}
	return true
}

// suspendLogs stops log capture until the next prune. Unlike fail, the archive
// stays writable and events keep going in.
func (a *Archive) suspendLogs(format string, args ...any) {
	note := fmt.Sprintf(format, args...)

	a.mu.Lock()
	first := !a.logsSuspended
	a.logsSuspended = true
	a.logsNote = note
	a.mu.Unlock()

	if first {
		a.logf("archive: %s", note)
	}
}

// resumeLogs lifts the ceiling's suspension after a prune or a clear; if no
// room was made, the next line suspends again.
func (a *Archive) resumeLogs() {
	a.mu.Lock()
	lifted := a.logsSuspended
	a.logsSuspended = false
	a.logsNote = ""
	a.mu.Unlock()
	if lifted {
		a.logf("archive: log capture may resume")
	}
}

// recovered clears the degraded state after a write succeeds. It runs on every
// line, so it checks under the read lock first: an exclusive lock each time
// would queue every reader behind the log traffic.
func (a *Archive) recovered() {
	a.mu.RLock()
	clean := a.writable
	a.mu.RUnlock()
	if clean {
		// Nothing sets note, since, lastFail or complainedAt without also
		// clearing writable, so a writable archive has already been reset.
		return
	}

	a.mu.Lock()
	restored := !a.writable
	a.writable = true
	a.note = ""
	a.since = time.Time{}
	a.lastFail = time.Time{}
	a.complainedAt = time.Time{}
	a.mu.Unlock()
	if restored {
		a.logf("archive: writing again")
	}
}

func (a *Archive) day(t time.Time) string { return t.UTC().Format(dayLayout) }

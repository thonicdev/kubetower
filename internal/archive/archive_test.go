package archive

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is a movable now, so that retention and the write cooldown can be
// tested without a test that sleeps.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// newArchive opens an archive under the test's own temporary directory and
// nowhere else. Every test in this package goes through it, which is what makes
// "these tests cannot touch anything real" a property of the file rather than a
// habit.
func newArchive(t *testing.T, s Settings) (*Archive, *clock) {
	t.Helper()
	c := newClock()
	a := Open(Options{
		Root:     filepath.Join(t.TempDir(), "archive"),
		Defaults: s,
		Now:      c.now,
		Logf:     t.Logf,
	})
	t.Cleanup(func() { a.Close() })
	return a, c
}

func on() Settings  { return Settings{Enabled: true, Logs: true, RetentionDays: 7} }
func off() Settings { return Settings{Enabled: false, RetentionDays: 7} }

func sampleEvent(name string, count int32, first time.Time) Event {
	return Event{
		Namespace: "prod",
		Kind:      "Pod",
		Object:    name,
		UID:       "uid-" + name,
		Type:      "Warning",
		Reason:    "BackOff",
		Message:   "Back-off restarting failed container",
		From:      "kubelet",
		Count:     count,
		First:     first,
		Last:      first.Add(time.Duration(count) * time.Second),
	}
}

// The defaults are a policy, not an accident: a week, events on, logs off. Logs
// off matters most — turning it on writes production application output to a
// volume, and that is a decision to make on purpose rather than discover.
func TestTheDefaultsKeepAWeekAndDoNotCaptureLogs(t *testing.T) {
	d := Defaults()
	if !d.Enabled {
		t.Error("the archive is off by default; events cost kilobytes and are the point of it")
	}
	if d.Logs {
		t.Error("log capture is on by default; that writes production log content to disk without anybody choosing to")
	}
	if d.RetentionDays != 7 {
		t.Errorf("RetentionDays = %d, want 7", d.RetentionDays)
	}
	if got, want := d.Retention(), 7*24*time.Hour; got != want {
		t.Errorf("Retention() = %v, want %v", got, want)
	}
}

func TestSettingsAreValidatedAndClamped(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Settings
	}{
		{"zero days", Settings{Enabled: true, RetentionDays: 0}},
		{"negative days", Settings{Enabled: true, RetentionDays: -1}},
		{"a century", Settings{Enabled: true, RetentionDays: 36500}},
		{"logs on while off", Settings{Enabled: false, Logs: true, RetentionDays: 7}},
	} {
		if err := tc.in.Validate(); err == nil {
			t.Errorf("%s: Validate() accepted %+v", tc.name, tc.in)
		}
	}

	a, _ := newArchive(t, Settings{Enabled: true, Logs: true, RetentionDays: 9999})
	if got := a.Settings().RetentionDays; got != MaxRetentionDays {
		t.Errorf("RetentionDays = %d, want it clamped to %d", got, MaxRetentionDays)
	}
}

// A retention change has to survive a restart, or an operator sets it once a
// week forever and reports it as a bug in the saving.
func TestSettingsSurviveAReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "archive")
	c := newClock()
	a := Open(Options{Root: root, Defaults: Defaults(), Now: c.now, Logf: t.Logf})
	if _, err := a.SetSettings(Settings{Enabled: true, Logs: true, RetentionDays: 21}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	a.Close()

	b := Open(Options{Root: root, Defaults: Defaults(), Now: c.now, Logf: t.Logf})
	defer b.Close()
	got := b.Settings()
	if got.RetentionDays != 21 || !got.Logs {
		t.Errorf("reopened with %+v, want the 21 days and log capture that were saved", got)
	}
}

// Off must mean off in both directions: nothing new is written, and what is
// already there is left exactly as it was. Turning the archive off is not a
// delete button — Clear is.
func TestOffWritesNothingAndDeletesNothing(t *testing.T) {
	a, _ := newArchive(t, on())

	if n, _ := a.RecordEvents("prod-eks-1", []Event{sampleEvent("api-0", 1, a.now())}); n != 1 {
		t.Fatalf("stored %d events while on, want 1", n)
	}
	sink := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	sink.Write([]byte("before the switch\n"))
	sink.Close()

	before := snapshot(t, a.Root())
	if len(before) < 2 {
		t.Fatalf("only %d files were written while on: %v", len(before), before)
	}

	if _, err := a.SetSettings(off()); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}

	if n, err := a.RecordEvents("prod-eks-1", []Event{sampleEvent("api-1", 1, a.now())}); n != 0 || err != nil {
		t.Errorf("stored %d events (%v) while off, want 0 and no error", n, err)
	}
	quiet := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	if _, err := quiet.Write([]byte("after the switch\n")); err != nil {
		t.Errorf("a discarding sink returned %v; it must never fail", err)
	}
	quiet.Close()

	if res, err := a.Prune(); err != nil || len(res.Days) != 0 || res.Skipped == "" {
		t.Errorf("Prune while off removed %v (%v); it must leave everything alone and say why", res.Days, err)
	}

	after := snapshot(t, a.Root())
	for name, size := range before {
		if name == SettingsFileName {
			continue // it changed on purpose: it holds the switch
		}
		if after[name] != size {
			t.Errorf("%s is %d bytes after turning the archive off, was %d", name, after[name], size)
		}
	}
	if body := readAll(t, filepath.Join(a.Root(), a.day(a.now()), segment("prod-eks-1"), eventsFileName)); strings.Contains(body, "api-1") {
		t.Error("an event recorded while the archive was off reached the file")
	}
}

// Clear is the explicit delete, and it must not take the settings with it —
// an operator who empties the archive has not asked to have their retention
// window reset.
func TestClearRemovesTheDataAndKeepsTheSettings(t *testing.T) {
	a, _ := newArchive(t, on())
	if _, err := a.SetSettings(Settings{Enabled: true, Logs: true, RetentionDays: 14}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	a.RecordEvents("prod-eks-1", []Event{sampleEvent("api-0", 1, a.now())})

	if res, err := a.Clear(); err != nil || len(res.Days) != 1 {
		t.Fatalf("Clear removed %v (%v), want the one day that existed", res.Days, err)
	}
	if days, err := a.days(); err != nil || len(days) != 0 {
		t.Errorf("%v is still on disk after Clear (%v)", days, err)
	}
	if _, err := os.Stat(filepath.Join(a.Root(), SettingsFileName)); err != nil {
		t.Errorf("Clear took the settings file with it: %v", err)
	}
	if got := a.Settings().RetentionDays; got != 14 {
		t.Errorf("RetentionDays = %d after Clear, want the 14 that was set", got)
	}
}

// A settings file is written to a temporary name and renamed over the real one,
// which makes the replacement atomic and says nothing about whether the bytes
// reached the disk. It also leaves a file behind whenever the process does not
// get from the one to the other, and only the sweep removes them: they are
// not day-shaped, so the pruner leaves them alone by design, while RefreshUsage
// counts everything in the root towards the figure the operator is looking at.
func TestSavingSettingsLeavesNoTemporaryFileBehind(t *testing.T) {
	root := filepath.Join(t.TempDir(), "archive")
	c := newClock()
	a := Open(Options{Root: root, Defaults: Defaults(), Now: c.now, Logf: t.Logf})

	// One left behind by a previous run that was killed between the write and
	// the rename.
	orphan := filepath.Join(root, SettingsFileName+".2118547")
	if err := os.WriteFile(orphan, []byte(`{"enabled":true,"retentionDays":3}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := a.SetSettings(Settings{Enabled: true, Logs: true, RetentionDays: 21}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	leftovers, err := filepath.Glob(filepath.Join(root, SettingsFileName+".*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Errorf("%v is still in the archive root; every interrupted save adds one, for ever, to the size an operator is shown", leftovers)
	}
	if _, err := os.Stat(filepath.Join(root, SettingsFileName)); err != nil {
		t.Fatalf("the settings file itself is not there: %v", err)
	}
	a.Close()

	// And one found at startup, from a run that never got as far as a save.
	if err := os.WriteFile(orphan, []byte(`{"enabled":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b := Open(Options{Root: root, Defaults: Defaults(), Now: c.now, Logf: t.Logf})
	defer b.Close()
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("%s survived a restart: %v", orphan, err)
	}
	if got := b.Settings().RetentionDays; got != 21 {
		t.Errorf("RetentionDays = %d after the sweep, want the 21 that was saved", got)
	}
}

// failing stands in for a disk with nothing left on it.
type failing struct{ err error }

func (f failing) Write([]byte) (int, error) { return 0, f.err }
func (f failing) Close() error              { return nil }

// The behavior that matters most in this package: a full disk is a footnote,
// not an outage. Every write path swallows the failure, says so once in Status,
// and the caller — a goroutine streaming logs to a browser — never learns about
// it, because a console that stops working when its optional archive fills up
// is worse than one with no archive.
func TestAFullDiskDoesNotReachTheCaller(t *testing.T) {
	a, c := newArchive(t, on())
	noSpace := errors.New("write /var/lib/kubetower/archive: no space left on device")
	a.open = func(string) (wfile, error) { return failing{noSpace}, nil }

	// The event path first: it reports what it stored, never what it could not.
	stored, err := a.RecordEvents("prod-eks-1", []Event{sampleEvent("api-0", 1, a.now())})
	if err != nil {
		t.Fatalf("RecordEvents = %v; the disk is not the caller's problem", err)
	}
	if stored != 0 {
		t.Errorf("RecordEvents claimed %d stored on a failing disk, want 0", stored)
	}
	if st := a.Status(); st.Writable || !strings.Contains(st.Note, "no space left") {
		t.Errorf("Status = %+v after the event write failed, want it degraded and quoting the failure", st)
	}

	// And the log path, past the cooldown so that it is genuinely attempted.
	c.add(writeCooldown + time.Second)
	sink := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	n, err := sink.Write([]byte("the disk is full\n"))
	if err != nil || n != len("the disk is full\n") {
		t.Fatalf("Write = %d, %v; a sink must always report success", n, err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close = %v; it must not fail either", err)
	}

	st := a.Status()
	if st.Writable {
		t.Fatal("Status still says writable after every write failed")
	}
	if !strings.Contains(st.Note, "no space left") {
		t.Errorf("Status().Note = %q, want it to quote the failure", st.Note)
	}
	if st.Since.IsZero() {
		t.Error("Status().Since is zero; a settings page cannot say how long it has been broken")
	}

	// Within the cooldown nothing is even attempted, so a full disk is not also
	// a hot loop.
	tries := 0
	a.open = func(string) (wfile, error) { tries++; return failing{noSpace}, nil }
	for i := 0; i < 50; i++ {
		s := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
		s.Write([]byte("still full\n"))
		s.Close()
	}
	if tries != 0 {
		t.Errorf("%d attempts were made during the cooldown, want none", tries)
	}

	// Space comes back. The next write past the cooldown succeeds and the
	// archive says so.
	c.add(writeCooldown + time.Second)
	a.open = openFile
	sink = a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	sink.Write([]byte("space again\n"))
	sink.Close()
	if st := a.Status(); !st.Writable || st.Note != "" {
		t.Errorf("Status = %+v after a successful write, want it writable again", st)
	}
}

// A root that cannot exist at all — here because its parent is a file — is a
// degraded archive that says so, not a process that will not start.
func TestARootThatCannotBeCreatedIsDegradedNotFatal(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	a := Open(Options{Root: filepath.Join(blocker, "archive"), Defaults: on(), Logf: t.Logf})
	defer a.Close()

	if st := a.Status(); st.Writable {
		t.Error("Status says writable with a root that cannot be created")
	}
	if n, err := a.RecordEvents("prod-eks-1", []Event{sampleEvent("api-0", 1, time.Now())}); n != 0 || err != nil {
		t.Errorf("RecordEvents = %d, %v; it must degrade quietly", n, err)
	}
	// **Degraded is not silent**: an unreadable archive must not answer empty.
	// A silent `nil` here turns into an emptiness in every caller, and the
	// expensive one is `Prune`, reporting 0 kept, 0 removed and no error while
	// retention has quietly stopped being enforced.
	//
	// What this test is named for still holds and is asserted above: the process
	// starts, `Status` says not writable, and `RecordEvents` degrades quietly. A
	// read is the one that has to speak, because nobody is watching a write and
	// somebody is looking at the drawer.
	//
	// **It can only fail on Linux**: the trigger is a parent that is a file,
	// where `os.ReadDir` answers ENOTDIR. On Windows the same call answers
	// ERROR_PATH_NOT_FOUND, which `os.IsNotExist` reports as true, so the error
	// arm is never reached there.
	events, err := a.Events(EventQuery{Cluster: "prod-eks-1"})
	if len(events) != 0 {
		t.Errorf("Events = %v, want none from an archive that cannot be read", events)
	}
	// And the two platforms answer two different *facts* here, which is why this
	// arm is conditional rather than the assertion being weakened to fit both.
	//
	// On Linux the parent is a file, `os.ReadDir` answers ENOTDIR, and the
	// directory is **present and unlistable** - so it must not come back as an
	// empty history. On Windows the same call answers ERROR_PATH_NOT_FOUND,
	// which `os.IsNotExist` reports as true, so the directory reads as
	// **absent** - and an absent archive genuinely holds nothing, which is the
	// arm `days()` returns `nil, nil` for on purpose.
	//
	// Asserting "an error, everywhere" would be asserting that Windows can tell
	// those two apart. It cannot, through this API, and pretending otherwise is
	// how a test comes to encode an accident of one machine.
	if runtime.GOOS != "windows" && err == nil {
		t.Error("Events answered no error; an archive that could not be read must not answer an empty history")
	}
	if u := a.Usage(); u.Bytes != 0 {
		t.Errorf("Usage().Bytes = %d, want 0", u.Bytes)
	}
}

// An archive with no directory at all is inert and total: every call works and
// nothing is written anywhere. That is what a test gets, and it is why nothing
// in this package needs a temporary directory to be constructed.
func TestNoDirectoryIsInertAndTotal(t *testing.T) {
	a := Open(Options{Defaults: on(), Logf: t.Logf})
	defer a.Close()

	if n, err := a.RecordEvents("prod-eks-1", []Event{sampleEvent("api-0", 1, time.Now())}); n != 0 || err != nil {
		t.Errorf("RecordEvents = %d, %v", n, err)
	}
	w := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	if _, err := w.Write([]byte("x\n")); err != nil {
		t.Errorf("Write = %v", err)
	}
	w.Close()
	if _, err := a.Prune(); err != nil {
		t.Errorf("Prune = %v", err)
	}
	if _, err := a.Clear(); err != nil {
		t.Errorf("Clear = %v", err)
	}
	if s, err := a.Streams(StreamQuery{}); err != nil || len(s) != 0 {
		t.Errorf("Streams = %v, %v", s, err)
	}
}

// This is production log content at rest. It should not be readable by every
// account on the host that happens to reach the volume.
func TestWhatIsWrittenIsRestrictedToItsOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits do not describe access on Windows")
	}
	a, _ := newArchive(t, on())
	a.RecordEvents("prod-eks-1", []Event{sampleEvent("api-0", 1, a.now())})
	sink := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	sink.Write([]byte("a token the application logged\n"))
	sink.Close()

	filepath.Walk(a.Root(), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		mode := info.Mode().Perm()
		if info.IsDir() {
			if mode&0o077 != 0 {
				t.Errorf("directory %s is %04o, want no group or other access", path, mode)
			}
			return nil
		}
		if mode&0o077 != 0 {
			t.Errorf("file %s is %04o, want no group or other access", path, mode)
		}
		return nil
	})
}

// snapshot is every file under root, by path relative to it, with its size.
func snapshot(t *testing.T, root string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		out[rel] = info.Size()
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return out
}

func readAll(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

// TestForceOffOverridesASettingsFileThatSaysOn: a state directory that was a
// single replica's carries a settings file with the archive on. Under several
// replicas the archive is off whatever that file says - Defaults alone apply
// only where there is no file, and the file would switch it back on.
func TestForceOffOverridesASettingsFileThatSaysOn(t *testing.T) {
	root := filepath.Join(t.TempDir(), "archive")
	c := newClock()
	single := Open(Options{Root: root, Defaults: Defaults(), Now: c.now, Logf: t.Logf})
	if _, err := single.SetSettings(Settings{Enabled: true, Logs: true, RetentionDays: 21}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	single.Close()

	off := Defaults()
	off.Enabled, off.Logs = false, false
	replicated := Open(Options{Root: root, Defaults: off, ForceOff: true, Now: c.now, Logf: t.Logf})
	defer replicated.Close()
	if got := replicated.Settings(); got.Enabled || got.Logs {
		t.Errorf("a replicated archive opened as %+v from a file that said on; it must be off", got)
	}
	if got := replicated.Settings(); got.RetentionDays != 21 {
		t.Errorf("the retention is %d, want the file's 21: what is already there is still pruned on it",
			got.RetentionDays)
	}
	if got, _ := replicated.SetSettings(Settings{Enabled: true, Logs: true, RetentionDays: 7}); got.Enabled || got.Logs {
		t.Errorf("SetSettings turned a forced-off archive back on: %+v", got)
	}
}

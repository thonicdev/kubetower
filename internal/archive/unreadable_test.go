package archive

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A day that is not there and a day that will not open are different answers.
//
// A read path that `continue`s past both can return only argument errors, and
// that makes a whole branch of the drawer unreachable: `internal/web/history.go`
// carries a failure state saying *"That is a failure and not an empty history —
// nothing here says nothing happened"*, written for exactly this.
//
// The trigger is not exotic: the state volume remounted with a uid that cannot
// read it, EMFILE under load, or a day directory `Prune` is midway through
// removing.
//
// These tests make a real file unreadable rather than faking an error, so they
// exercise the same `os.Open` the production path uses.

// unreadableFile makes a path exist and refuse to open, or skips.
//
// Skipped rather than faked on Windows and as root: a chmod that does not
// actually deny is a test that passes for the wrong reason, which is worse than
// no test — the whole point here is that a silent pass is the defect.
func unreadableFile(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("mode bits do not deny the owner on Windows; this needs a POSIX filesystem")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root, which ignores the mode bits this test depends on")
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("making %s unreadable: %v", path, err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	if f, err := os.Open(path); err == nil {
		f.Close()
		t.Skip("this filesystem let the owner open a mode-000 file, so there is nothing to test")
	}
}

func TestADayThatCannotBeReadIsNotADayWithNothingInIt(t *testing.T) {
	dir := t.TempDir()
	a := Open(Options{
		Root:     dir,
		Defaults: Settings{Enabled: true, RetentionDays: 7},
		Logf:     t.Logf,
	})
	defer a.Close()

	now := time.Now().UTC()
	if _, err := a.RecordEvents("prod", []Event{{
		Cluster: "prod", Namespace: "web", Kind: "Pod", Object: "api-1",
		Reason: "BackOff", Message: "back-off restarting", Type: "Warning",
		First: now.Add(-time.Hour), Last: now,
	}}); err != nil {
		t.Fatalf("RecordEvents: %v", err)
	}

	// It reads before the file is closed to us.
	got, err := a.Events(EventQuery{Cluster: "prod"})
	if err != nil || len(got) != 1 {
		t.Fatalf("baseline: got %d events, err %v; want 1 and nil", len(got), err)
	}

	day := now.Format("2006-01-02")
	unreadableFile(t, filepath.Join(dir, day, "prod", eventsFileName))

	got, err = a.Events(EventQuery{Cluster: "prod"})
	if err == nil {
		t.Fatalf("a day that exists and cannot be opened returned %d events and no error. "+
			"The drawer then says the object has no history, which is a claim about the "+
			"cluster made from a failure to read a file", len(got))
	}
	if !strings.Contains(err.Error(), "could not be read") {
		t.Errorf("error = %q, want it to say the archive could not be read", err)
	}
}

// An absent day is the ordinary case and must stay silent, or every read of a
// cluster that reported yesterday and not today becomes a failure.
func TestADayNobodyWroteIsNotAFailure(t *testing.T) {
	dir := t.TempDir()
	a := Open(Options{
		Root:     dir,
		Defaults: Settings{Enabled: true, RetentionDays: 7},
		Logf:     t.Logf,
	})
	defer a.Close()

	now := time.Now().UTC()
	if _, err := a.RecordEvents("prod", []Event{{
		Cluster: "prod", Namespace: "web", Kind: "Pod", Object: "api-1",
		Reason: "Pulled", Message: "pulled image", Type: "Normal",
		First: now, Last: now,
	}}); err != nil {
		t.Fatalf("RecordEvents: %v", err)
	}

	// A cluster that has never written anything: every day is absent.
	got, err := a.Events(EventQuery{Cluster: "staging"})
	if err != nil {
		t.Errorf("a cluster with no records at all reported an error: %v. Absent is the "+
			"ordinary case and must not read as a fault", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d events for a cluster that wrote none", len(got))
	}
}

func TestAnUnreadableLogDayIsReportedRatherThanSkipped(t *testing.T) {
	dir := t.TempDir()
	a := Open(Options{
		Root:     dir,
		Defaults: Settings{Enabled: true, Logs: true, RetentionDays: 7},
		Logf:     t.Logf,
	})
	defer a.Close()

	w := a.LogWriter("prod", "web", "api-1", "app")
	if _, err := w.Write([]byte("listening on :8080\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	q := LogQuery{Cluster: "prod", Namespace: "web", Pod: "api-1", Container: "app"}
	if got, err := a.Log(q); err != nil || len(got) == 0 {
		t.Fatalf("baseline: got %d lines, err %v; want lines and nil", len(got), err)
	}

	day := time.Now().UTC().Format("2006-01-02")
	unreadableFile(t, filepath.Join(dir, day, "prod", logsDirName, "web", "api-1", "app.log"))

	if _, err := a.Log(q); err == nil {
		t.Error("a log day that exists and cannot be opened returned no error, so the panel " +
			"reports the container printed nothing")
	}
}

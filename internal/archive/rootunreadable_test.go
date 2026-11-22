package archive

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An archive root that exists and will not list is not an empty archive.
//
// A bare `nil` from `days()` for both would turn into an emptiness in every
// caller: `Events` answering `(nil, nil)` so the drawer says nothing happened
// to the object, `Prune` reporting nothing to remove so retention stops being
// enforced without saying so, `Clear` reporting an archive emptied that is
// still whole on disk, and `RefreshUsage` reporting 0 bytes so the size
// ceiling can never fire.
//
// The trigger is not exotic: the state volume remounted with a uid that cannot
// read it.
//
// Driven through the `readDir` seam rather than a real chmod, and
// `internal/archive/archive.go` carries the reason: a mode bit does not deny the
// owner on Windows, and `os.ReadDir` on a non-directory answers
// ERROR_PATH_NOT_FOUND there, which is indistinguishable from absent. These
// tests therefore run on every platform, which the mode-bit ones in
// unreadable_test.go deliberately do not.

// refusesToList makes the archive root answer EACCES, in the shape os.ReadDir
// itself produces: a *fs.PathError that is not os.IsNotExist.
func refusesToList(a *Archive) {
	a.readDir = func(path string) ([]os.DirEntry, error) {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
	}
}

// written puts one day's worth of real records in the archive, so that every
// assertion below is about a root that will not list rather than about one with
// nothing in it.
func written(t *testing.T) *Archive {
	t.Helper()
	dir := t.TempDir()
	a := Open(Options{
		Root:     dir,
		Defaults: Settings{Enabled: true, RetentionDays: 7},
		Logf:     func(string, ...any) {},
	})
	t.Cleanup(func() { _ = a.Close() })

	now := time.Now().UTC()
	a.RecordEvents("prod-eks-1", []Event{{
		Cluster: "prod-eks-1", Namespace: "prod", Object: "Pod/api-0",
		Reason: "Killing", Message: "stopping container", Count: 1,
		First: now, Last: now,
	}})

	if w := a.LogWriter("prod-eks-1", "prod", "api-0", "api"); w != nil {
		w.Write([]byte("a line worth keeping\n"))
		w.Close()
	}
	return a
}

func TestAnArchiveRootThatWillNotListIsNotAnEmptyArchive(t *testing.T) {
	a := written(t)
	refusesToList(a)

	events, err := a.Events(EventQuery{Cluster: "prod-eks-1"})
	if err == nil {
		t.Errorf("Events = %v, nil; a root that could not be listed must not answer an empty history", events)
	}
	if err != nil && !strings.Contains(err.Error(), "could not be listed") {
		t.Errorf("Events error = %q, want it to say the directory could not be listed", err)
	}
}

func TestALogTailOverAnUnlistableRootReportsTheFailure(t *testing.T) {
	a := written(t)
	refusesToList(a)

	lines, err := a.Log(LogQuery{Cluster: "prod-eks-1", Namespace: "prod", Pod: "api-0", Container: "api"})
	if err == nil {
		t.Errorf("Log = %v, nil; a root that could not be listed must not answer an empty window", lines)
	}
}

func TestAStreamIndexOverAnUnlistableRootReportsTheFailure(t *testing.T) {
	a := written(t)
	refusesToList(a)

	streams, err := a.Streams(StreamQuery{})
	if err == nil {
		t.Errorf("Streams = %v, nil; an index that could not be built must not read as an archive holding nothing", streams)
	}
}

// Retention that cannot run must not report that it ran. This is the reading of
// the four that is not on a page: nobody sees it, and the consequence is a disk
// that fills while the settings page says "nothing to remove".
func TestAPruneOverAnUnlistableRootIsNotANoOp(t *testing.T) {
	a := written(t)
	refusesToList(a)

	result, err := a.Prune()
	if err == nil {
		t.Fatalf("Prune = %+v, nil; a root that could not be listed must not report a clean prune", result)
	}
	if result.Skipped == "" {
		t.Errorf("PruneResult.Skipped is empty; the settings page reads this and would say nothing happened")
	}
	if got := a.Status().LastPruneNote; !strings.Contains(got, "could not be listed") {
		t.Errorf("last prune note = %q, want it to name the listing failure", got)
	}
}

// "Emptied" over a directory that was never listed is the worst of the family:
// the operator typed the day count into a confirmation to get here.
func TestAClearOverAnUnlistableRootDoesNotReportAnEmptiedArchive(t *testing.T) {
	a := written(t)
	refusesToList(a)

	result, err := a.Clear()
	if err == nil {
		t.Fatalf("Clear = %+v, nil; nothing was removed and this says it succeeded", result)
	}
	if len(result.Days) != 0 {
		t.Errorf("Clear reported %v removed over a root it could not list", result.Days)
	}
}

// The figure the size ceiling is checked against. 0 bytes with no note is an
// archive that can never stop capturing however full the disk gets.
func TestUsageOverAnUnlistableRootSaysItIsNotAFigure(t *testing.T) {
	a := written(t)
	refusesToList(a)

	u := a.RefreshUsage()
	if u.Note == "" {
		t.Fatalf("Usage = %d bytes with no note; an archive that could not be measured reads as an empty one", u.Bytes)
	}
	if strings.Contains(u.Note, "floor") {
		t.Errorf("Usage.Note = %q; \"a floor, not a total\" promises the figure is at least real, and nothing was measured", u.Note)
	}
}

// And the other half of the distinction, which is the part that makes it a
// distinction rather than a new failure: a root that is genuinely not there yet
// still answers empty, with no error.
func TestARootThatIsNotThereStillAnswersEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-created")
	a := Open(Options{Root: dir, Defaults: Settings{Enabled: true, RetentionDays: 7}, Logf: func(string, ...any) {}})
	t.Cleanup(func() { _ = a.Close() })
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("removing the root Open created: %v", err)
	}

	days, err := a.days()
	if err != nil || len(days) != 0 {
		t.Errorf("days() = %v, %v; an absent root is an empty archive and not a failure", days, err)
	}
	if events, err := a.Events(EventQuery{Cluster: "prod-eks-1"}); err != nil || len(events) != 0 {
		t.Errorf("Events = %v, %v; want an empty history and no error", events, err)
	}
}

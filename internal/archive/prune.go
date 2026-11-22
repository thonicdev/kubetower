package archive

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// PruneResult is what one prune did, for a log line and for a settings page.
type PruneResult struct {
	// Days removed, oldest first.
	Days []string
	// Bytes freed, as measured before the removal.
	Bytes int64
	// Kept is how many day directories are still there.
	Kept int
	// Cutoff is the oldest day retained. Days strictly before it are gone.
	Cutoff string
	// Skipped is why nothing happened, when nothing happened.
	Skipped string
	At      time.Time
}

// Prune removes every day outside the retention window.
//
// It lists the root and removes each expired day, never traversing what is
// kept, so its hourly cost does not grow with the archive. Only expired days
// are walked, to report the bytes freed.
//
// Safe while writes are in flight: it never touches the current day, and it
// closes any sink holding a file in a day it removes (an open handle fails the
// removal on Windows, and on Linux leaves a writer on an unlinked inode).
//
// It does nothing while the archive is off; Clear is the explicit delete.
func (a *Archive) Prune() (PruneResult, error) {
	now := a.now()
	result := PruneResult{At: now}

	a.mu.RLock()
	on, days, root := a.settings.Enabled, a.settings.normalised().RetentionDays, a.root
	a.mu.RUnlock()

	if root == "" {
		result.Skipped = "there is no archive directory"
		a.notePrune(result)
		return result, nil
	}
	if !on {
		result.Skipped = "the archive is off, so what is already there is left alone"
		a.notePrune(result)
		return result, nil
	}

	// Keep the current day and the `days` before it: one directory more than
	// the arithmetic needs, so the retention is a floor of whole 24-hour days.
	cutoff := now.UTC().AddDate(0, 0, -days).Format(dayLayout)
	result.Cutoff = cutoff
	today := a.day(now)

	existing, err := a.days()
	if err != nil {
		// Retention that cannot run must not report that it ran.
		result.Skipped = "the archive directory could not be listed, so nothing was removed and nothing is known to have been kept"
		a.notePrune(result)
		return result, err
	}
	var expired []string
	for _, day := range existing {
		if day >= cutoff || day == today {
			result.Kept++
			continue
		}
		expired = append(expired, day)
	}
	if len(expired) == 0 {
		// Still lift the size ceiling's suspension, so room made by hand is
		// used at once; if there is still none, the next line suspends again.
		a.resumeLogs()
		a.notePrune(result)
		return result, nil
	}

	// A sink writing into a day about to go lets go of its file first; it
	// matters around midnight. currentDay, not s.day: the field is written
	// under the sink's own lock.
	doomed := map[string]bool{}
	for _, day := range expired {
		doomed[day] = true
	}
	a.closeSinks(func(s *LogSink) bool { return doomed[s.currentDay()] })

	var failures []string
	for _, day := range expired {
		size := a.dirBytes(a.dayDir(day))
		if err := a.removeDay(day); err != nil {
			failures = append(failures, err.Error())
			result.Kept++
			continue
		}
		result.Days = append(result.Days, day)
		result.Bytes += size
	}

	if result.Bytes > 0 {
		// Correct the cached size now rather than at the next walk.
		a.pending.Add(-result.Bytes)
	}
	// Room has been made, so log capture may go again.
	a.resumeLogs()
	a.notePrune(result)

	if len(failures) > 0 {
		return result, fmt.Errorf("%d of %d expired days could not be removed: %s",
			len(failures), len(expired), strings.Join(failures, "; "))
	}
	return result, nil
}

// removeDay is the only path in this package that deletes anything. Two
// independent refusals guard os.RemoveAll: the name must be day-shaped, and the
// path must resolve strictly inside the archive root.
func (a *Archive) removeDay(name string) error {
	if !dayPattern.MatchString(name) {
		return fmt.Errorf("refusing to remove %q: it is not a day directory", name)
	}
	if _, err := time.Parse(dayLayout, name); err != nil {
		return fmt.Errorf("refusing to remove %q: it is not a date", name)
	}
	path := a.dayDir(name)
	if err := a.inRoot(path); err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("%s could not be removed: %w", path, err)
	}
	return nil
}

func (a *Archive) notePrune(r PruneResult) {
	note := r.Skipped
	if note == "" {
		switch {
		case len(r.Days) == 0:
			note = fmt.Sprintf("nothing to remove; %d days kept, back to %s", r.Kept, r.Cutoff)
		default:
			note = fmt.Sprintf("removed %d days (%s), keeping back to %s",
				len(r.Days), humanBytes(r.Bytes), r.Cutoff)
		}
	}
	a.mu.Lock()
	a.lastPrune = r.At
	a.lastPruneNote = note
	a.mu.Unlock()

	if len(r.Days) > 0 {
		a.logf("archive: %s", note)
	}
}

// Clear removes every day directory, whatever the retention window says, and
// leaves the settings file alone. It is separate from the on/off switch:
// turning the archive off stops collecting and destroys nothing.
func (a *Archive) Clear() (PruneResult, error) {
	result := PruneResult{At: a.now()}
	if a.root == "" {
		result.Skipped = "there is no archive directory"
		return result, nil
	}

	a.closeSinks(func(*LogSink) bool { return true })

	// The event lock is held across the removals: RecordEvents holds it from
	// creating today's directory to appending, and interleaving with a removal
	// would fail its open and mark the archive degraded.
	a.events.mu.Lock()

	// A root that will not list must not be reported as emptied.
	all, listErr := a.days()
	if listErr != nil {
		a.events.mu.Unlock()
		result.Skipped = "the archive directory could not be listed, so nothing was removed"
		return result, listErr
	}

	var failures []string
	for _, day := range all {
		size := a.dirBytes(a.dayDir(day))
		if err := a.removeDay(day); err != nil {
			failures = append(failures, err.Error())
			result.Kept++
			continue
		}
		result.Days = append(result.Days, day)
		result.Bytes += size
	}

	a.events.init()
	a.events.mu.Unlock()

	// Whatever the size ceiling had stopped can start again.
	a.resumeLogs()
	a.RefreshUsage()
	a.logf("archive: cleared %d days (%s)", len(result.Days), humanBytes(result.Bytes))

	if len(failures) > 0 {
		return result, fmt.Errorf("%d days could not be removed: %s", len(failures), strings.Join(failures, "; "))
	}
	return result, nil
}

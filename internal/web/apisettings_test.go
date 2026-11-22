package web

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/archive"
)

// The settings payload, from the stores that hold it.
//
// `internal/api`'s `TestTheSettingsPayloadCarriesEveryMemberTheContractDeclares`
// proves the member set on the wire over a **fake**; this proves the mapping
// from `Archives` and `SetupInfo`, which that test cannot reach.
//
// # Proving it fails
//
// Replacing `fillArchiveSettings`'s body with a return:
//
//	--- FAIL: TestTheGeneralTabIsReadBackFromTheArchive
//	    general.archiveEnabled is absent; the tab could not be read at all
//	--- FAIL: TestTheRetentionListAlwaysContainsTheValueItIsEditing
//	    21 is not in the list []
//	--- FAIL: TestAnArchiveWithNowhereToWriteIsAbsentAndNotZero
//	    archive = "", want "absent"
//	    no reason on an absent archive
//
// Removing the insertion from `retentionChoicesFor` - the `current < choice.Days`
// branch - leaves the value in the list, appended by the tail case, and it is the
// **order** that fails:
//
//	--- FAIL: TestTheRetentionListAlwaysContainsTheValueItIsEditing
//	    the list is not in order: [1 3 7 14 30 21]
//	    21 was appended to [1 3 7 14 30 21] rather than inserted in order
//
// The second is worth the two assertions rather than one: a 21 sitting after 30
// reads as a mistake in the page rather than as the setting, and a test that only
// looked for membership would have passed over it.

// archiveStub is the six methods `Archives` declares, with the three this file
// reads answered from fields.
type archiveStub struct {
	settings archive.Settings
	status   archive.Status
	usage    archive.Usage
	saveErr  error
	saved    *archive.Settings

	// The emptying half. `cleared` counts, because the one thing a refused
	// gesture must not do is reach the disk.
	cleared     int
	clearErr    error
	clearResult archive.PruneResult
}

func (a *archiveStub) Status() archive.Status     { return a.status }
func (a *archiveStub) Settings() archive.Settings { return a.settings }
func (a *archiveStub) SetSettings(next archive.Settings) (archive.Settings, error) {
	a.saved = &next
	if a.saveErr != nil {
		// **Applied and not written.** The store clamps and applies in memory
		// first, so the value it returns is in force whatever happened to the
		// file - which is the whole reason `applied.persisted` exists.
		a.settings = next
		return next, a.saveErr
	}
	a.settings = next
	return next, nil
}
func (a *archiveStub) Usage() archive.Usage        { return a.usage }
func (a *archiveStub) RefreshUsage() archive.Usage { return a.usage }
func (a *archiveStub) Clear() (archive.PruneResult, error) {
	a.cleared++
	if a.clearErr != nil {
		return archive.PruneResult{}, a.clearErr
	}
	return a.clearResult, nil
}
func (a *archiveStub) CapturesLogs() bool { return a.settings.Logs }
func (a *archiveStub) LogWriter(string, string, string, string) io.WriteCloser {
	return nil
}

func filledArchive() *archiveStub {
	return &archiveStub{
		// 21 is deliberately not one of the five offered. See the test below.
		settings: archive.Settings{Enabled: true, Logs: false, RetentionDays: 21},
		status:   archive.Status{Root: "/state/archive", Writable: true},
		usage: archive.Usage{
			Bytes: 4 << 20, Files: 128,
			Days: []archive.DayUsage{{Day: "2026-09-06"}, {Day: "2026-09-12"}},
		},
	}
}

func TestTheGeneralTabIsReadBackFromTheArchive(t *testing.T) {
	out := api.Settings{}
	fillArchiveSettings(&out, filledArchive())

	if out.General.ArchiveEnabled == nil {
		t.Fatal("general.archiveEnabled is absent; the tab could not be read at all")
	}
	if !*out.General.ArchiveEnabled {
		t.Error("archiveEnabled read back false over a store that says true")
	}
	if out.General.CaptureLogs == nil || *out.General.CaptureLogs {
		t.Errorf("captureLogs = %v, want false — it is the expensive half and it is off here",
			out.General.CaptureLogs)
	}
	if out.General.RetentionDays == nil || *out.General.RetentionDays != 21 {
		t.Errorf("retentionDays = %v, want 21", out.General.RetentionDays)
	}
}

// **A control that cannot represent its own current state changes it.**
//
// `KT_ARCHIVE_RETENTION_DAYS` is documented as anything from 1 to 90 and the
// dropdown offers five of those. With the value at 21 no option carried
// `selected`, the browser submitted the first, and a Save opened to tick an
// unrelated checkbox destroyed twenty days of archived events while saying
// "Saved."
func TestTheRetentionListAlwaysContainsTheValueItIsEditing(t *testing.T) {
	out := api.Settings{}
	fillArchiveSettings(&out, filledArchive())

	days := make([]int, 0, len(out.RetentionChoices))
	for _, choice := range out.RetentionChoices {
		days = append(days, choice.Days)
	}

	found, position := false, -1
	for i, day := range days {
		if day == 21 {
			found, position = true, i
		}
	}
	if !found {
		t.Fatalf("21 is not in the list %v", days)
	}

	// **Inserted rather than appended**, because a 21 sitting after 30 reads as a
	// mistake in the page rather than as the setting.
	for i := 1; i < len(days); i++ {
		if days[i] <= days[i-1] {
			t.Errorf("the list is not in order: %v", days)
			break
		}
	}
	if position == len(days)-1 {
		t.Errorf("21 was appended to %v rather than inserted in order", days)
	}
	// Every entry carries a label, because a bare number in a dropdown about
	// disk usage is a number without a unit.
	for _, choice := range out.RetentionChoices {
		if choice.Label == "" {
			t.Errorf("the %d-day choice has no label", choice.Days)
		}
	}
}

// An archive with nowhere to write is a **state**, not a zero. A page reporting
// 0 bytes over an archive that does not exist is the most reassuring possible
// way to be wrong.
func TestAnArchiveWithNowhereToWriteIsAbsentAndNotZero(t *testing.T) {
	store := filledArchive()
	store.status = archive.Status{Root: ""}

	out := api.Settings{}
	fillArchiveSettings(&out, store)

	if got := out.Archive.State(); got != api.StateAbsent {
		t.Errorf("archive = %q, want %q", got, api.StateAbsent)
	}
	if out.Archive.Reason() == nil {
		t.Error("no reason on an absent archive")
	}

	// And a build with no archive at all is the same state with a different
	// sentence — the two are told apart by what it says, not by whether it
	// answered.
	out = api.Settings{}
	fillArchiveSettings(&out, nil)
	if got := out.Archive.State(); got != api.StateAbsent {
		t.Errorf("no archive: %q, want %q", got, api.StateAbsent)
	}
	if out.General.RetentionDays != nil {
		t.Error("a build with no archive read back a retention; there is nothing to read")
	}
}

// A **refused write** stays on the ok arm: the archive is there, it holds what
// it holds, and what has stopped is the writing. Folding that into a failure
// would hide the bytes an operator is trying to look at.
func TestARefusedWriteIsReportedWithoutHidingWhatIsThere(t *testing.T) {
	store := filledArchive()
	store.status = archive.Status{
		Root: "/state/archive", Writable: false,
		Note: "the archive volume is read-only", Since: time.Now(),
	}

	out := api.Settings{}
	fillArchiveSettings(&out, store)

	if got := out.Archive.State(); got != api.StateOK {
		t.Fatalf("archive = %q; a refused write is not an absent archive", got)
	}
	facts := archiveFactsIn(t, out)
	if !facts.WritesRefused {
		t.Error("writesRefused is false over a store that refused")
	}
	if facts.Bytes == 0 {
		t.Error("the bytes were hidden by the refusal; they are what the operator came for")
	}
	if facts.Note == "" {
		t.Error("no note saying why")
	}
}

// Log capture stopping is **not** a refused write, and reporting it as one sends
// an operator looking for a full volume when what they need is a shorter window.
func TestSuspendedLogCaptureIsNotAWriteRefusal(t *testing.T) {
	store := filledArchive()
	store.status = archive.Status{
		Root: "/state/archive", Writable: true,
		LogsSuspended: true,
		LogsNote:      "the archive is past its size ceiling, so log capture has paused",
	}

	out := api.Settings{}
	fillArchiveSettings(&out, store)
	facts := archiveFactsIn(t, out)

	if facts.WritesRefused {
		t.Error("a suspended log capture was reported as a refused write. The disk is fine, " +
			"events are still going in, and only the expensive half has stopped")
	}
	if facts.Note == "" {
		t.Error("nothing says the capture has paused")
	}
}

// The write applies the fields it was given and **leaves the ones it was not**.
//
// "Stop keeping events" and "I did not mention events" are different
// instructions, and a page that sends its whole form on every save would
// otherwise turn one into the other whenever a field failed to render.
func TestASaveThatSaysNothingAboutAFieldDoesNotReassertItsZero(t *testing.T) {
	store := filledArchive()
	days := 7

	if _, err := saveGeneral(store, api.SettingsWrite{RetentionDays: &days}); err != nil {
		t.Fatalf("saveGeneral: %v", err)
	}
	if store.saved == nil {
		t.Fatal("nothing was written")
	}
	if store.saved.RetentionDays != 7 {
		t.Errorf("retention = %d, want 7", store.saved.RetentionDays)
	}
	if !store.saved.Enabled {
		t.Error("a write that said nothing about the archive switch turned it off")
	}
}

func archiveFactsIn(t *testing.T, out api.Settings) api.ArchiveFacts {
	t.Helper()
	raw, err := out.Archive.MarshalJSON()
	if err != nil {
		t.Fatalf("marshalling the archive envelope: %v", err)
	}
	var payload struct {
		Data *api.ArchiveFacts `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if payload.Data == nil {
		t.Fatalf("the envelope carries no data: %s", raw)
	}
	return *payload.Data
}

// Emptying the archive: the recap is read off the disk, and the gesture is a
// typed number.
//
// # Why the gesture is a number and not a button
//
// `settings.go`'s own clear asks the operator to type **how many days this
// archive holds**. It cannot be answered from muscle memory, because the answer
// is different on every console and on every day - which is the whole job of a
// confirmation in front of something irreversible. The other scopes of this
// operation ask for an acknowledgement, and that is right for them: losing a
// column width is not the same class of act.
//
// # What this pins
//
// `clearState` must compare its plan's `Confirmation` to the gesture, as every
// other act in the contract does through `gestureSatisfied`. An unchecked
// gesture is invisible while every scope asks for `acknowledge` - a gesture
// satisfied by silence - but once one asks for a typed number it is a
// destructive act with a dialog in front of it and no lock behind it.
func TestEmptyingTheArchiveIsPlannedFromTheDisk(t *testing.T) {
	store := filledArchive()
	settings := apiSettings{&Server{archive: store}}

	facts, err := settings.PreviewClear("archive")
	if err != nil {
		t.Fatalf("PreviewClear: %v", err)
	}

	if facts.Verb != "empty" || !facts.Destructive || facts.Irreversible == nil || !*facts.Irreversible {
		t.Errorf("the plan does not describe an irreversible destructive act: %+v", facts)
	}
	// One target, and it is the archive. Ninety of them would put ninety rows in
	// a dialog to say one thing.
	if facts.Count != 1 || len(facts.Targets) != 1 || facts.Targets[0].Kind != "archive" {
		t.Fatalf("targets = %+v, count = %d", facts.Targets, facts.Count)
	}
	// The recap is read, never inferred: the figures are the ones the walk
	// returned, so the number the dialog shows and the number it checks cannot
	// come from two readings of the disk.
	if !strings.Contains(facts.Targets[0].Note, "2 days") || !strings.Contains(facts.Targets[0].Note, "128 files") {
		t.Errorf("the note does not carry what is on disk: %q", facts.Targets[0].Note)
	}
	if facts.Confirmation == nil || facts.Confirmation.Kind != "count" || facts.Confirmation.Expected != "2" {
		t.Fatalf("confirmation = %+v, want a count gesture expecting 2", facts.Confirmation)
	}
	// And what is lost that nothing else holds, said out loud. An operator
	// agreeing to "tidy up" and one agreeing to *this* are different people.
	if len(facts.Warnings) == 0 || !strings.Contains(strings.Join(facts.Warnings, " "), "only copy") {
		t.Errorf("no warning says the archive is the only copy: %v", facts.Warnings)
	}
}

func TestEmptyingTheArchiveReportsWhatWasFreed(t *testing.T) {
	store := filledArchive()
	store.clearResult = archive.PruneResult{Bytes: 4 << 20, Days: []string{"2026-09-06", "2026-09-12"}}
	settings := apiSettings{&Server{archive: store}}

	outcome, err := settings.Clear("archive")
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if store.cleared != 1 {
		t.Fatalf("the archive was cleared %d times", store.cleared)
	}
	if outcome.Succeeded != 1 || len(outcome.Results) != 1 || !outcome.Results[0].OK {
		t.Fatalf("outcome = %+v", outcome)
	}
	if !strings.Contains(outcome.Results[0].Message, "4.0 MiB") {
		t.Errorf("the outcome does not say what was freed: %q", outcome.Results[0].Message)
	}
}

// A refused removal is an outcome and not a 500: one failed act reported as an
// error loses the sentence saying which part of the disk would not go.
func TestAnArchiveThatWouldNotEmptySaysSo(t *testing.T) {
	store := filledArchive()
	store.clearErr = errors.New("open /state/archive/2026-09-06: permission denied")
	settings := apiSettings{&Server{archive: store}}

	outcome, err := settings.Clear("archive")
	if err != nil {
		t.Fatalf("Clear returned an error rather than an outcome: %v", err)
	}
	if outcome.Failed != 1 || outcome.Succeeded != 0 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.Results[0].Reason == nil || !strings.Contains(outcome.Results[0].Reason.Message, "permission denied") {
		t.Errorf("the reason did not survive: %+v", outcome.Results[0])
	}
}

// The other scopes are still not wired, and say so rather than reporting a
// success that discarded nothing.
func TestClearingPreferencesIsStillNotWired(t *testing.T) {
	settings := apiSettings{&Server{archive: filledArchive()}}
	if _, err := settings.Clear("preferences"); !errors.Is(err, api.ErrNotInProfile) {
		t.Fatalf("err = %v, want ErrNotInProfile", err)
	}
}

// The Infos tab's three links come off `SetupInfo`: the
// repository, its issues (drawn from the repository's address by the page) and
// the documentation. Sent whether or not the update check ran.
func TestTheInfosTabIsHandedTheRepositoryAndTheDocumentation(t *testing.T) {
	s := &Server{setup: SetupInfo{
		RepositoryURL:    "https://github.com/thonicdev/kubetower",
		DocumentationURL: "https://thonicdev.github.io/kubetower-site/docs/",
	}}
	info := settingsInfoOf(s)
	if info.RepositoryURL != "https://github.com/thonicdev/kubetower" {
		t.Errorf("repositoryUrl = %q", info.RepositoryURL)
	}
	if info.DocumentationURL != "https://thonicdev.github.io/kubetower-site/docs/" {
		t.Errorf("documentationUrl = %q, want the address SetupInfo was given", info.DocumentationURL)
	}
}

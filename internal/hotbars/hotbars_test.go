package hotbars

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/thonicdev/kubetower/internal/statefile"
)

var contexts = []string{"STAGING-EKS-1", "SHOP-EKS-1", "BUILD-EKS-1", "docker-desktop"}

// The default has to be total: with no file at all, every context is on hotbar 1
// in kubeconfig order and hotbar 1 is the one on screen. A cluster that is on no
// hotbar is a cluster that cannot be reached from the rail at all.
func TestTheDefaultPutsEverythingOnTheFirstBar(t *testing.T) {
	s := New("", nil)
	layout := s.Arrangement(contexts)

	if layout.Active != 1 {
		t.Errorf("Active = %d, want 1", layout.Active)
	}
	if len(layout.Bars) != Count {
		t.Fatalf("%d bars, want %d", len(layout.Bars), Count)
	}
	if got := layout.Bars[0]; !equal(got, contexts) {
		t.Errorf("hotbar 1 = %v, want the contexts in order %v", got, contexts)
	}
	for i := 1; i < Count; i++ {
		if len(layout.Bars[i]) != 0 {
			t.Errorf("hotbar %d = %v, want it empty", i+1, layout.Bars[i])
		}
	}
	if got := layout.Current(); !equal(got, contexts) {
		t.Errorf("Current() = %v, want hotbar 1", got)
	}
}

// The two halves of "sane and total" that a stored file can break: a context
// that has vanished from the kubeconfig must not leave a hole, and one added
// since must appear rather than be invisible.
func TestVanishedContextsLeaveAndNewOnesAppear(t *testing.T) {
	s := New("", nil)
	if _, err := s.Assign("SHOP-EKS-1", 2, contexts); err != nil {
		t.Fatalf("%v", err)
	}

	// The kubeconfig now has one context fewer and one the file has never seen.
	live := []string{"STAGING-EKS-1", "SHOP-EKS-1", "mi-k3s"}
	layout := s.Arrangement(live)

	var flat []string
	for _, bar := range layout.Bars {
		flat = append(flat, bar...)
	}
	if !sameSet(flat, live) {
		t.Errorf("the arrangement holds %v, want exactly the live contexts %v", flat, live)
	}
	if layout.BarOf("mi-k3s") != 1 {
		t.Errorf("a context added since is on hotbar %d, want 1", layout.BarOf("mi-k3s"))
	}
	if layout.BarOf("SHOP-EKS-1") != 2 {
		t.Error("a context that was assigned lost its hotbar when another one vanished")
	}
}

// A context absent for one reading and back for the next keeps its hotbar. The
// stored file is rewritten while it is away, so this is the case where it would
// quietly be forgotten.
func TestAContextThatComesBackKeepsItsHotbar(t *testing.T) {
	s := New(t.TempDir(), t.Logf)
	if _, err := s.Assign("BUILD-EKS-1", 3, contexts); err != nil {
		t.Fatalf("%v", err)
	}

	without := []string{"STAGING-EKS-1", "SHOP-EKS-1", "docker-desktop"}
	if _, err := s.Move("SHOP-EKS-1", 1, without); err != nil {
		t.Fatalf("%v", err)
	}

	if bar := s.Arrangement(contexts).BarOf("BUILD-EKS-1"); bar != 3 {
		t.Errorf("BUILD-EKS-1 came back on hotbar %d, want 3", bar)
	}
}

func TestAssignMovesAContextAndRefusesABarThatDoesNotExist(t *testing.T) {
	s := New("", nil)

	layout, err := s.Assign("docker-desktop", 2, contexts)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if layout.BarOf("docker-desktop") != 2 {
		t.Errorf("docker-desktop is on hotbar %d, want 2", layout.BarOf("docker-desktop"))
	}
	if in := layout.Bars[0]; statefile.Index(in, "docker-desktop") >= 0 {
		t.Error("it is on two hotbars at once")
	}
	if layout.Active != 1 {
		t.Error("assigning changed which hotbar is on screen; it must not")
	}

	if _, err := s.Assign("docker-desktop", 4, contexts); err == nil {
		t.Error("hotbar 4 was accepted")
	}
	if _, err := s.Assign("not-a-context", 2, contexts); err == nil {
		t.Error("a name that is not a context was accepted")
	}
}

func TestMoveWalksATileAndStopsAtTheEnds(t *testing.T) {
	s := New("", nil)

	// SHOP-EKS-1 is second; up once puts it first.
	layout, err := s.Move("SHOP-EKS-1", -1, contexts)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if got := layout.Bars[0][0]; got != "SHOP-EKS-1" {
		t.Errorf("hotbar 1 starts with %q, want SHOP-EKS-1", got)
	}

	// And up again is a no-op, not an error: the button is disabled there, but a
	// double click must redraw the rail rather than produce a red box.
	layout, err = s.Move("SHOP-EKS-1", -1, contexts)
	if err != nil {
		t.Fatalf("moving past the top: %v", err)
	}
	if got := layout.Bars[0][0]; got != "SHOP-EKS-1" {
		t.Errorf("hotbar 1 starts with %q after moving past the top", got)
	}

	last := contexts[len(contexts)-1]
	layout, _ = s.Move(last, 1, contexts)
	if got := layout.Bars[0][len(layout.Bars[0])-1]; got != last {
		t.Errorf("moving past the bottom reordered the bar: %v", layout.Bars[0])
	}

	if _, err := s.Move("not-a-context", 1, contexts); err == nil {
		t.Error("a name that is not a context was accepted")
	}
}

func TestShowRemembersWhichBarIsOnScreen(t *testing.T) {
	s := New("", nil)
	layout, err := s.Show(3, contexts)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if layout.Active != 3 {
		t.Errorf("Active = %d, want 3", layout.Active)
	}
	if len(layout.Current()) != 0 {
		t.Errorf("hotbar 3 shows %v, want nothing on it", layout.Current())
	}
	if _, err := s.Show(0, contexts); err == nil {
		t.Error("hotbar 0 was accepted")
	}
}

// The whole point of the file: the arrangement outlives the process. Written by
// one store, read by the next, from the directory KT_STATE_DIR names.
func TestTheArrangementSurvivesARestart(t *testing.T) {
	dir := t.TempDir()

	first := New(dir, t.Logf)
	if _, err := first.Assign("SHOP-EKS-1", 2, contexts); err != nil {
		t.Fatalf("%v", err)
	}
	if _, err := first.Show(2, contexts); err != nil {
		t.Fatalf("%v", err)
	}
	if first.Path() != filepath.Join(dir, FileName) {
		t.Errorf("Path() = %q", first.Path())
	}

	second := New(dir, t.Logf)
	layout := second.Arrangement(contexts)
	if layout.Active != 2 {
		t.Errorf("the hotbar on screen came back as %d, want 2", layout.Active)
	}
	if layout.BarOf("SHOP-EKS-1") != 2 {
		t.Errorf("SHOP-EKS-1 came back on hotbar %d, want 2", layout.BarOf("SHOP-EKS-1"))
	}

	// And it is a file a human can read and correct, which is the reason for
	// JSON rather than anything cleverer.
	raw, err := os.ReadFile(first.Path())
	if err != nil {
		t.Fatalf("%v", err)
	}
	var file persisted
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("the arrangement on disk is not readable JSON: %v", err)
	}
	if file.Active != 2 || statefile.Index(file.Bars[1], "SHOP-EKS-1") < 0 {
		t.Errorf("the file does not describe what was arranged: %s", raw)
	}
}

// A state directory that cannot be written must not stop the rail being drawn.
// The arrangement then lives as long as the process, which is a degraded mode
// and not a broken one.
//
// And it must **say so**: on a full disk, dragging a cluster to another hotbar
// must not redraw, look saved, and be gone at the next start with nothing
// anywhere having said so. The error comes back, wrapping statefile.ErrNotSaved
// so a handler can tell "this did not happen" (a refusal) from "this happened
// and will not last".
func TestAnUnwritableDirectoryIsReportedAndNotFatal(t *testing.T) {
	// A path whose parent is a file, so creating the directory cannot work on
	// any platform.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("%v", err)
	}

	var lines int
	s := New(filepath.Join(blocked, "state"), func(string, ...any) { lines++ })
	// Whether the unreadable file at startup logged anything is platform
	// dependent — Windows reports a missing path where Linux reports "not a
	// directory". What is counted here is the writing.
	lines = 0

	layout, err := s.Assign("SHOP-EKS-1", 2, contexts)
	if err == nil {
		t.Fatal("the arrangement could not be written and Assign said nothing. That is the " +
			"failure the operator cannot see: the tile moves, the rail redraws, and the " +
			"arrangement is gone at the next start")
	}
	if !errors.Is(err, statefile.ErrNotSaved) {
		t.Errorf("the error does not wrap statefile.ErrNotSaved, so a handler cannot tell it "+
			"from a refusal and will report a drag that worked as one that did not: %v", err)
	}
	// The layout comes back applied, beside the error. A caller that drew the
	// zero layout here would blank the rail over a full disk.
	if layout.BarOf("SHOP-EKS-1") != 2 {
		t.Errorf("the layout returned with the error does not carry the change: hotbar %d",
			layout.BarOf("SHOP-EKS-1"))
	}
	if bar := s.Arrangement(contexts).BarOf("SHOP-EKS-1"); bar != 2 {
		t.Errorf("the arrangement was lost in memory too: hotbar %d", bar)
	}

	for i := 0; i < 5; i++ {
		if _, err := s.Assign("SHOP-EKS-1", 3, contexts); err == nil {
			t.Fatal("a later write to the same broken directory reported success")
		}
	}
	if lines != 1 {
		t.Errorf("%d log lines for one broken directory, want exactly 1. The condition is "+
			"standing; one line per click buries everything else in the log", lines)
	}
}

// Hiding is the other half of the same promise: it goes through setHidden rather
// than commit, and that is a second copy of the "did the write work" question.
func TestHidingAlsoReportsAFailedWrite(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("%v", err)
	}
	s := New(filepath.Join(blocked, "state"), nil)

	layout, err := s.Hide("SHOP-EKS-1", contexts)
	if !errors.Is(err, statefile.ErrNotSaved) {
		t.Fatalf("Hide over an unwritable directory returned %v", err)
	}
	if !layout.IsHidden("SHOP-EKS-1") {
		t.Error("the layout returned with the error does not show the tile hidden")
	}
	if !s.Arrangement(contexts).IsHidden("SHOP-EKS-1") {
		t.Error("the hiding was lost in memory too")
	}
}

// The complaint is about a *condition*, not a lifetime. A directory that comes
// back and goes again is worth a second line: the first one is old by then, and
// an operator who fixed a volume needs to be told when it broke a second time.
func TestASuccessfulWriteArmsTheNextComplaint(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone")

	var lines int
	s := New(gone, func(string, ...any) { lines++ })

	// Blocked: a file where the directory has to be.
	if err := os.WriteFile(gone, []byte("x"), 0o600); err != nil {
		t.Fatalf("%v", err)
	}
	if _, err := s.Assign("SHOP-EKS-1", 2, contexts); err == nil {
		t.Fatal("the write succeeded over a file")
	}
	if lines != 1 {
		t.Fatalf("%d lines for the first failure, want 1", lines)
	}

	// Unblocked.
	if err := os.Remove(gone); err != nil {
		t.Fatalf("%v", err)
	}
	if _, err := s.Assign("SHOP-EKS-1", 3, contexts); err != nil {
		t.Fatalf("the directory is writable again and the write still failed: %v", err)
	}
	if lines != 1 {
		t.Errorf("a write that worked logged something: %d lines", lines)
	}

	// Blocked again, by taking the directory away.
	if err := os.RemoveAll(gone); err != nil {
		t.Fatalf("%v", err)
	}
	if err := os.WriteFile(gone, []byte("x"), 0o600); err != nil {
		t.Fatalf("%v", err)
	}
	if _, err := s.Assign("SHOP-EKS-1", 1, contexts); err == nil {
		t.Fatal("the write succeeded over a file")
	}
	if lines != 2 {
		t.Errorf("the second outage cost %d lines in total, want 2. A complaint that never "+
			"re-arms reports the first failure of an installation's life and nothing after it",
			lines)
	}
}

// The file is replaced through a temporary one, and an interrupted save leaves
// that temporary one behind. Nothing else in this project would ever remove it:
// it is not JSON to any loader and no pruner knows about it, so one accumulates
// per interruption for the life of the installation, in the directory the
// operator was asked to mount a volume at.
func TestAStrandedTemporaryFileIsSweptUp(t *testing.T) {
	dir := t.TempDir()
	stranded := filepath.Join(dir, FileName+".9999999")
	if err := os.WriteFile(stranded, []byte("half a file"), 0o600); err != nil {
		t.Fatalf("%v", err)
	}

	s := New(dir, t.Logf)
	if _, err := s.Assign("SHOP-EKS-1", 2, contexts); err != nil {
		t.Fatalf("Assign: %v", err)
	}

	if _, err := os.Stat(stranded); !os.IsNotExist(err) {
		t.Errorf("%s is still there after a save; it is left over from an interrupted write "+
			"and nothing else will ever remove it", stranded)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
		t.Errorf("the sweep took the arrangement with it: %v", err)
	}
}

// Nonsense in the file is a default arrangement, not a crash and not an empty
// rail. The file is small enough to be hand-edited, so it will be.
func TestARubbishFileFallsBackToTheDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{{{"), 0o600); err != nil {
		t.Fatalf("%v", err)
	}
	layout := New(dir, t.Logf).Arrangement(contexts)
	if !equal(layout.Bars[0], contexts) {
		t.Errorf("hotbar 1 = %v, want every context", layout.Bars[0])
	}
	if layout.Active != 1 {
		t.Errorf("Active = %d, want 1", layout.Active)
	}
}

// A hand-edited file naming the same context twice must not draw it twice.
func TestADuplicateInTheFileIsDrawnOnce(t *testing.T) {
	dir := t.TempDir()
	raw := `{"active":1,"bars":[["SHOP-EKS-1","STAGING-EKS-1"],["SHOP-EKS-1"],[]]}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(raw), 0o600); err != nil {
		t.Fatalf("%v", err)
	}
	layout := New(dir, t.Logf).Arrangement(contexts)

	var seen int
	for _, bar := range layout.Bars {
		for _, name := range bar {
			if name == "SHOP-EKS-1" {
				seen++
			}
		}
	}
	if seen != 1 {
		t.Errorf("SHOP-EKS-1 is drawn %d times", seen)
	}
	if layout.BarOf("SHOP-EKS-1") != 1 {
		t.Error("the first placement did not win")
	}
}

// Dropping a tile beside another one, which is what a drag ends in. Expressed as
// a neighbor and not an index, so it lands where it was dropped even if the
// browser's copy of the rail is a click out of date.
func TestPlacePutsATileBesideAnother(t *testing.T) {
	s := New("", nil)

	// docker-desktop is last; dropped on the top half of SHOP-EKS-1 it becomes
	// second, in front of it.
	layout, err := s.Place("docker-desktop", "SHOP-EKS-1", false, contexts)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if got := layout.Bars[0]; !equal(got, []string{"STAGING-EKS-1", "docker-desktop", "SHOP-EKS-1", "BUILD-EKS-1"}) {
		t.Errorf("hotbar 1 = %v", got)
	}

	// And on the bottom half of the same tile, it goes behind it.
	layout, err = s.Place("docker-desktop", "SHOP-EKS-1", true, contexts)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if got := layout.Bars[0]; !equal(got, []string{"STAGING-EKS-1", "SHOP-EKS-1", "docker-desktop", "BUILD-EKS-1"}) {
		t.Errorf("hotbar 1 = %v", got)
	}

	// Dropped on itself, the rail is redrawn and nothing moves. A drag that
	// changed its mind must not be an error.
	layout, err = s.Place("docker-desktop", "docker-desktop", false, contexts)
	if err != nil {
		t.Fatalf("dropping a tile on itself: %v", err)
	}
	if got := layout.Bars[0]; !equal(got, []string{"STAGING-EKS-1", "SHOP-EKS-1", "docker-desktop", "BUILD-EKS-1"}) {
		t.Errorf("hotbar 1 = %v after a tile was dropped on itself", got)
	}

	for _, bad := range [][2]string{{"not-a-context", "SHOP-EKS-1"}, {"SHOP-EKS-1", "not-a-context"}} {
		if _, err := s.Place(bad[0], bad[1], false, contexts); err == nil {
			t.Errorf("Place(%q, %q) was accepted", bad[0], bad[1])
		}
	}
}

// Dropping a tile onto a tile on another hotbar moves it there, at that tile's
// place — the precise form of what the 1 2 3 buttons do at the end of the bar.
func TestPlaceCrossesHotbars(t *testing.T) {
	s := New("", nil)
	if _, err := s.Assign("SHOP-EKS-1", 2, contexts); err != nil {
		t.Fatalf("%v", err)
	}
	if _, err := s.Assign("BUILD-EKS-1", 2, contexts); err != nil {
		t.Fatalf("%v", err)
	}

	layout, err := s.Place("docker-desktop", "BUILD-EKS-1", false, contexts)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !equal(layout.Bars[1], []string{"SHOP-EKS-1", "docker-desktop", "BUILD-EKS-1"}) {
		t.Errorf("hotbar 2 = %v", layout.Bars[1])
	}
	if statefile.Index(layout.Bars[0], "docker-desktop") >= 0 {
		t.Error("it is on two hotbars at once")
	}
	if layout.Active != 1 {
		t.Error("a drag onto another hotbar changed which one is on screen; it must not")
	}
}

// Hiding: fourteen contexts, and the rail is the five that are worked on. The
// hidden one is off the rail, still listed as hidden, and comes back where it
// was.
func TestHidingTakesAContextOffTheRailAndRevealingBringsItBack(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, t.Logf)
	if _, err := s.Assign("SHOP-EKS-1", 2, contexts); err != nil {
		t.Fatalf("%v", err)
	}

	layout, err := s.Hide("SHOP-EKS-1", contexts)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if layout.BarOf("SHOP-EKS-1") != 0 {
		t.Errorf("a hidden context is still drawn, on hotbar %d", layout.BarOf("SHOP-EKS-1"))
	}
	if !layout.IsHidden("SHOP-EKS-1") {
		t.Errorf("the hidden context is not listed as hidden: %v", layout.Hidden)
	}
	if layout.IsHidden("docker-desktop") {
		t.Error("a context nobody hid is reported as hidden")
	}
	// Hiding twice is the same as hiding once.
	if _, err := s.Hide("SHOP-EKS-1", contexts); err != nil {
		t.Fatalf("hiding twice: %v", err)
	}
	if got := s.Arrangement(contexts).Hidden; len(got) != 1 {
		t.Errorf("Hidden = %v, want one entry", got)
	}

	// It survives a restart, and comes back on the hotbar it was sent to.
	revived := New(dir, t.Logf)
	if !revived.Arrangement(contexts).IsHidden("SHOP-EKS-1") {
		t.Error("hiding did not survive a restart")
	}
	layout, err = revived.Reveal("SHOP-EKS-1", contexts)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if layout.BarOf("SHOP-EKS-1") != 2 {
		t.Errorf("it came back on hotbar %d, want the 2 it was on", layout.BarOf("SHOP-EKS-1"))
	}
	if layout.IsHidden("SHOP-EKS-1") {
		t.Error("it is still listed as hidden after being revealed")
	}

	if _, err := s.Hide("not-a-context", contexts); err == nil {
		t.Error("a name that is not a context was accepted")
	}
	if _, err := s.Reveal("not-a-context", contexts); err == nil {
		t.Error("a name that is not a context was accepted")
	}
}

// Hiding must never make a cluster unreachable, and must never make the rail
// disagree with the page: the cluster whose page is open is drawn even when it
// is hidden — and drawing it must not un-hide it.
func TestTheClusterOnScreenIsDrawnEvenWhenHidden(t *testing.T) {
	s := New(t.TempDir(), t.Logf)
	if _, err := s.Hide("SHOP-EKS-1", contexts); err != nil {
		t.Fatalf("%v", err)
	}

	layout := s.Showing("SHOP-EKS-1", contexts)
	if layout.BarOf("SHOP-EKS-1") != 1 {
		t.Errorf("the cluster the page is about is on no hotbar: %v", layout.Bars)
	}
	if !layout.IsHidden("SHOP-EKS-1") {
		t.Error("it is drawn but no longer reported as hidden, so nothing can mark it")
	}
	if n := len(layout.Bars[0]); n != len(contexts) {
		t.Errorf("hotbar 1 has %d tiles, want all %d", n, len(contexts))
	}
	// Drawn once, not twice.
	if got := layout.Bars[0]; !sameSet(got, contexts) {
		t.Errorf("hotbar 1 = %v", got)
	}

	// And the preference is untouched: another page draws it hidden again.
	if s.Arrangement(contexts).BarOf("SHOP-EKS-1") != 0 {
		t.Error("drawing a hidden cluster beside its own page un-hid it")
	}
	if s.Showing("docker-desktop", contexts).BarOf("SHOP-EKS-1") != 0 {
		t.Error("it is drawn beside a page about another cluster")
	}
}

// Sending a hidden cluster to a hotbar — with the buttons or by dropping it —
// brings it back. Assigning something to a hotbar and having it not appear there
// is not a state worth being able to produce.
func TestAssigningAndPlacingRevealAHiddenContext(t *testing.T) {
	s := New("", nil)
	if _, err := s.Hide("SHOP-EKS-1", contexts); err != nil {
		t.Fatalf("%v", err)
	}
	layout, err := s.Assign("SHOP-EKS-1", 3, contexts)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if layout.IsHidden("SHOP-EKS-1") || layout.BarOf("SHOP-EKS-1") != 3 {
		t.Errorf("assigning left it hidden: bar %d, hidden %v", layout.BarOf("SHOP-EKS-1"), layout.Hidden)
	}

	if _, err := s.Hide("SHOP-EKS-1", contexts); err != nil {
		t.Fatalf("%v", err)
	}
	layout, err = s.Place("SHOP-EKS-1", "docker-desktop", false, contexts)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if layout.IsHidden("SHOP-EKS-1") || layout.BarOf("SHOP-EKS-1") != 1 {
		t.Errorf("placing left it hidden: bar %d, hidden %v", layout.BarOf("SHOP-EKS-1"), layout.Hidden)
	}

	// A hidden tile is not somewhere anything can be dropped: it is not drawn.
	if _, err := s.Hide("BUILD-EKS-1", contexts); err != nil {
		t.Fatalf("%v", err)
	}
	if _, err := s.Place("docker-desktop", "BUILD-EKS-1", false, contexts); err == nil {
		t.Error("a hidden tile was accepted as a drop target")
	}
	// And it cannot be walked with the arrows either, but the error says why.
	if _, err := s.Move("BUILD-EKS-1", -1, contexts); err == nil {
		t.Error("a hidden context was moved within a hotbar it is not on")
	}
}

// Hiding is KubeTower's own preference and lives in KubeTower's own file. This
// pins the shape of it, because the file is small enough to be hand-edited and
// so it will be.
func TestHidingIsWrittenToTheArrangementFile(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, t.Logf)
	if _, err := s.Hide("BUILD-EKS-1", contexts); err != nil {
		t.Fatalf("%v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("%v", err)
	}
	var file persisted
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("not readable JSON: %v", err)
	}
	if statefile.Index(file.Hidden, "BUILD-EKS-1") < 0 {
		t.Errorf("the file does not say what is hidden: %s", raw)
	}

	// A file with no hidden list says nothing is hidden, rather than failing to
	// load.
	old := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(old, []byte(`{"active":1,"bars":[["SHOP-EKS-1"],[],[]]}`), 0o600); err != nil {
		t.Fatalf("%v", err)
	}
	layout := New(filepath.Dir(old), t.Logf).Arrangement(contexts)
	if len(layout.Hidden) != 0 {
		t.Errorf("an older file was read as hiding %v", layout.Hidden)
	}
}

// The arrangement is read on every page render and written from a handler, so
// the two have to be able to happen at once. Run this with -race where cgo is
// available; without it, it still catches a lock taken the wrong way round.
func TestConcurrentReadsAndWrites(t *testing.T) {
	s := New(t.TempDir(), nil)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 50; n++ {
				layout := s.Arrangement(contexts)
				if len(layout.Bars) != Count {
					t.Errorf("a read saw %d bars", len(layout.Bars))
					return
				}
				// The other read: the one every page render does.
				if len(s.Showing(contexts[i%len(contexts)], contexts).Bars) != Count {
					t.Errorf("a render read saw the wrong number of bars")
					return
				}
				// Mutating what a read handed back must not reach the store.
				if len(layout.Bars[0]) > 0 {
					layout.Bars[0][0] = "scribbled-on"
				}
			}
		}(i)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 50; n++ {
				_, _ = s.Assign(contexts[i], (n%Count)+1, contexts)
				_, _ = s.Move(contexts[i], 1, contexts)
				_, _ = s.Show((n%Count)+1, contexts)
				_, _ = s.Place(contexts[i], contexts[(i+1)%len(contexts)], n%2 == 0, contexts)
				_, _ = s.Hide(contexts[i], contexts)
				_, _ = s.Reveal(contexts[i], contexts)
			}
		}(i)
	}
	wg.Wait()

	var flat []string
	for _, bar := range s.Arrangement(contexts).Bars {
		flat = append(flat, bar...)
	}
	if !sameSet(flat, contexts) {
		t.Errorf("after the storm the arrangement holds %v, want %v", flat, contexts)
	}
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]int{}
	for _, name := range got {
		seen[name]++
	}
	for _, name := range want {
		seen[name]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

package statefile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The one that matters, and the reason this package exists at all.
//
// `internal/archive` learned it and wrote it down — "Rename-atomic is not the
// same as durable… a crash in between leaves a zero-length settings.json" — and
// the three stores that between them handle every click an operator makes on the
// rail and on a table did not have the line. Now there is one write and one
// place to lose it from.
//
// This reads the source, which wants justifying. An fsync has no effect this
// process can observe: the bytes are in the file either way, and the failure it
// prevents needs a power cut between two syscalls. There is no seam to inject
// and no assertion to make on the result. What *can* be checked is that the call
// is there and that it is before the rename — which is the whole of the property,
// because a Sync after the Rename would be a Sync of a file that is no longer the
// one being replaced.
func TestTheTemporaryFileIsSyncedBeforeItIsRenamed(t *testing.T) {
	raw, err := os.ReadFile("statefile.go")
	if err != nil {
		t.Fatalf("reading this package's own source: %v", err)
	}
	source := string(raw)

	sync := strings.Index(source, "tmp.Sync()")
	if sync < 0 {
		t.Fatal("statefile.Write no longer fsyncs the temporary file. Without it the rename " +
			"can reach the disk before the bytes do, and a crash in between leaves a " +
			"zero-length file that every load() in this project reads as unparseable and " +
			"replaces with the defaults — the operator's arrangement silently back to new")
	}
	rename := strings.Index(source, "os.Rename(name, path)")
	if rename < 0 {
		t.Fatal("statefile.Write no longer renames into place")
	}
	if sync > rename {
		t.Error("the fsync happens after the rename, which syncs a name that no longer refers " +
			"to the file being replaced. It has to be before")
	}
}

func TestWriteReplacesTheFileAndLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thing.json")

	if err := Write(path, []byte(`{"a":1}`)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != `{"a":1}` {
		t.Fatalf("read back %q, %v", got, err)
	}
	if err := Write(path, []byte(`{"a":2}`)); err != nil {
		t.Fatalf("Write again: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != `{"a":2}` {
		t.Errorf("the second write did not replace the first: %s", got)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "thing.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the directory holds %v, want only thing.json", names)
	}
}

// A temporary file from an interrupted save is swept up by the next successful
// one. Nothing else in this project would ever remove it: it is not JSON to any
// loader, no pruner knows about it, and one accumulates per interruption for the
// life of the installation — in the directory the operator was asked to mount a
// volume at.
func TestAStrandedTemporaryFileIsSweptUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thing.json")

	stranded := filepath.Join(dir, "thing.json.4242424")
	if err := os.WriteFile(stranded, []byte("half"), 0o600); err != nil {
		t.Fatalf("%v", err)
	}
	// Another store's file, and another store's leftovers. Neither is ours to
	// remove: the sweep is per file, or one store's save would delete a second
	// store's half-written one out from under it.
	keep := filepath.Join(dir, "other.json")
	keepTmp := filepath.Join(dir, "other.json.111")
	for _, p := range []string{keep, keepTmp} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("%v", err)
		}
	}

	if err := Write(path, []byte("{}")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if _, err := os.Stat(stranded); !os.IsNotExist(err) {
		t.Errorf("%s survived the save", stranded)
	}
	for _, p := range []string{path, keep, keepTmp} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("the sweep removed %s, which is not its business: %v", p, err)
		}
	}
}

// The state directory holds what the operator calls their production clusters
// and which of them they look at. It is nobody else's business on a shared
// machine, so it is 0700 whichever preference was changed first.
func TestTheDirectoryAndTheFileAreTheOperatorsOwn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits are not what Windows enforces; the image runs on Alpine")
	}
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, "thing.json")

	if err := Write(path, []byte("{}")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if got := info.Mode().Perm(); got != DirPerm {
		t.Errorf("the state directory is %04o, want %04o", got, DirPerm)
	}
	file, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if got := file.Mode().Perm(); got != FilePerm {
		t.Errorf("the file is %04o, want %04o", got, FilePerm)
	}
}

// Every failure wraps ErrNotSaved, and that is what a handler tests for to tell
// "this happened and will not last" from "this did not happen". The two want
// different sentences on the screen.
func TestAFailedWriteWrapsErrNotSaved(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("%v", err)
	}

	err := Write(filepath.Join(blocked, "state", "thing.json"), []byte("{}"))
	if !errors.Is(err, ErrNotSaved) {
		t.Fatalf("Write over a file returned %v, which does not wrap ErrNotSaved", err)
	}
	// And it names the path, because "it did not save" without saying where is a
	// line the operator cannot act on.
	if !strings.Contains(err.Error(), blocked) {
		t.Errorf("the error does not name the path: %v", err)
	}
	if !strings.Contains(err.Error(), "in effect now") {
		t.Errorf("the error does not say the change is in effect, which is the half that "+
			"stops it reading as a refusal: %v", err)
	}
}

// One state directory, resolved one way, so that "my hotbars reset" and "my node
// shell image reset" have the same answer.
func TestDirIsTheEnvironmentThenTheHome(t *testing.T) {
	t.Setenv(EnvDir, filepath.Join("tmp", "kubetower-state"))
	if got := Dir(); got != filepath.Join("tmp", "kubetower-state") {
		t.Errorf("Dir() = %q, want KT_STATE_DIR", got)
	}

	// Whitespace is not a directory. A compose file with `KT_STATE_DIR= ` in it
	// would otherwise put the state in a directory called " ".
	t.Setenv(EnvDir, "   ")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory on this machine to fall back to")
	}
	if got, want := Dir(), filepath.Join(home, ".kubetower"); got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
}

// The complaint is about a standing condition, and it re-arms when the condition
// clears — a volume that comes back and goes again is worth a second line,
// because the first one is old by then.
func TestAComplainerSaysItOnceAndReArms(t *testing.T) {
	var lines []string
	c := NewComplainer(func(format string, args ...any) { lines = append(lines, format) })

	for i := 0; i < 5; i++ {
		c.Once("the disk is full")
	}
	if len(lines) != 1 {
		t.Fatalf("%d lines for one standing condition, want 1: %v", len(lines), lines)
	}
	c.Log("something that happened once")
	if len(lines) != 2 {
		t.Errorf("Log was swallowed by Once's flag; they are different questions: %v", lines)
	}
	c.Clear()
	c.Once("the disk is full")
	if len(lines) != 3 {
		t.Errorf("the complaint never re-armed after a write that worked, so an installation "+
			"reports its first outage and nothing after it: %v", lines)
	}
}

// A store built with no logger at all is a supported state — several are, in
// tests — and a preference store is not a thing that should be able to panic on
// a page render.
func TestAComplainerWithNoLoggerIsSilentRatherThanFatal(t *testing.T) {
	c := NewComplainer(nil)
	c.Log("x")
	c.Once("y")
	c.Clear()
	var nilComplainer *Complainer
	nilComplainer.Log("x")
	nilComplainer.Once("y")
	nilComplainer.Clear()
}

// Remove allocates. Filtering in place with `out := names[:0]` would write its
// answer over its input, so "take this out of the layout I am about to also
// read" would quietly corrupt the layout.
func TestRemoveDoesNotWriteOverItsInput(t *testing.T) {
	in := []string{"a", "b", "c"}
	out := Remove(in, "b")

	if want := []string{"a", "c"}; !equal(out, want) {
		t.Errorf("Remove = %v, want %v", out, want)
	}
	if want := []string{"a", "b", "c"}; !equal(in, want) {
		t.Errorf("the input became %v; Remove wrote over the slice it was given", in)
	}
	if got := Remove(in, "not-there"); !equal(got, in) {
		t.Errorf("removing something absent gave %v", got)
	}
}

func TestInsertClampsRatherThanPanicking(t *testing.T) {
	in := []string{"a", "b"}
	for _, c := range []struct {
		at   int
		want []string
	}{
		{-4, []string{"x", "a", "b"}},
		{0, []string{"x", "a", "b"}},
		{1, []string{"a", "x", "b"}},
		{2, []string{"a", "b", "x"}},
		{9, []string{"a", "b", "x"}},
	} {
		if got := Insert(in, c.at, "x"); !equal(got, c.want) {
			t.Errorf("Insert(at=%d) = %v, want %v", c.at, got, c.want)
		}
	}
	if !equal(in, []string{"a", "b"}) {
		t.Errorf("Insert wrote over its input: %v", in)
	}
}

func TestIndexAndContains(t *testing.T) {
	in := []string{"a", "b", "b"}
	if got := Index(in, "b"); got != 1 {
		t.Errorf("Index = %d, want the first of them", got)
	}
	if got := Index(in, "z"); got != -1 {
		t.Errorf("Index of something absent = %d, want -1", got)
	}
	if !Contains(in, "a") || Contains(in, "z") {
		t.Error("Contains disagrees with Index")
	}
	if Contains(nil, "a") {
		t.Error("an empty list contains something")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

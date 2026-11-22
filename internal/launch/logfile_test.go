package launch

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestTheLogGoesBesideTheRestOfTheState. One directory for what this console
// keeps, so "where did it go" has one answer — and so that the operator being
// asked to mount a volume at it is mounting the log too.
func TestTheLogGoesBesideTheRestOfTheState(t *testing.T) {
	dir := stateDir(t)
	previous := log.Writer()
	t.Cleanup(func() { log.SetOutput(previous) })

	path, file := logToFile()
	if path == "" {
		t.Fatal("logToFile could not open a file in a directory it had just been given")
	}
	// Closed here and not in the process: main() holds this open for its whole
	// life, which is right there and makes the temporary directory undeletable
	// on Windows here.
	t.Cleanup(func() { _ = file.Close() })
	if filepath.Dir(path) != dir {
		t.Errorf("the log went to %s, not to the state directory %s", path, dir)
	}

	log.Printf("a line that must survive")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the log: %v", err)
	}
	if !strings.Contains(string(raw), "a line that must survive") {
		t.Error("logToFile returned a path but the logger is not writing to it")
	}
	// Stamped, because a file accumulating across launches with nothing marking
	// the boundaries reads as one long run.
	if !strings.Contains(string(raw), "kubetower") {
		t.Error("the log carries no version stamp, so a reader cannot tell which build wrote it")
	}
}

// TestTheLogReachesTheTerminalAsWellAsTheFile.
//
// Two channels, not one: the terminal and the file are not alternatives,
// because on a macOS `.app` launch the flag that would decide between them is
// a hardcoded `true`. And the terminal half must not be lost: the container's
// whole debugging channel is `docker compose logs`, and a
// log that moved into a file inside the container would take it away.
//
// The order is asserted too, at the end. `io.MultiWriter` stops at the first
// writer that errors and does not continue down the list, so stderr placed first
// takes the file's copy of every later line with it the moment the pipe on the
// other end goes.
func TestTheLogReachesTheTerminalAsWellAsTheFile(t *testing.T) {
	stateDir(t)
	previous := log.Writer()
	t.Cleanup(func() { log.SetOutput(previous) })
	withConsole(t, true)

	var path string
	var file *logFile
	terminal := capturingStderr(t, func() {
		path, file = logToFile()
		if path == "" {
			t.Error("logToFile could not open a file in a directory it had just been given")
			return
		}
		log.Printf("a line the operator is watching for")
	})
	if t.Failed() {
		return
	}
	t.Cleanup(func() { _ = file.Close() })

	if !strings.Contains(terminal, "a line the operator is watching for") {
		t.Error("the log no longer reaches stderr once it has a file. In the container that is " +
			"`docker compose logs`, which is the channel this whole project is debugged " +
			"through, and it would go silent with nothing saying so")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the log: %v", err)
	}
	if !strings.Contains(string(raw), "a line the operator is watching for") {
		t.Error("the log did not reach the file although a console was reported. That is the " +
			"macOS case: a .app launched from the Finder has an ordinary stderr going nowhere, " +
			"so the file is the only copy there is")
	}

	source, err := os.ReadFile("logfile.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "io.MultiWriter(file, os.Stderr)") {
		t.Error("the tee no longer writes to the file first. io.MultiWriter stops at the first " +
			"writer that errors, so a stderr that has gone away — a broken pipe after " +
			"`kubetower | head` — silently ends the file's copy of the log as well")
	}
}

// TestNothingGatesTheLogOnThereBeingNoConsole.
//
// A source assertion, because `main` is not callable from a test and the whole
// of the property is where the call sits. The bug it keeps out: `if
// !consoleAttached { logToFile() }` reads as careful and is Windows-only
// reasoning — off Windows that flag is a hardcoded `true`, so a macOS `.app`
// and a Linux `.desktop` launch, both of which have exactly the discarded
// stderr the gate exists for, would get no log at all.
func TestNothingGatesTheLogOnThereBeingNoConsole(t *testing.T) {
	source, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)

	if !strings.Contains(text, "logToFile()") {
		t.Fatal("logToFile is gone from run.go")
	}
	if strings.Contains(text, "if !consoleAttached") {
		t.Error("run.go opens the log only when it believes there is no console. That belief " +
			"is a hardcoded true off Windows, so a .app opened from the Finder and a .desktop " +
			"entry opened from a menu — the two launches with no terminal that are not Windows " +
			"— write every diagnostic to a discarded stderr and leave no file behind")
	}
}

// TestTheLogIsSetAsideRatherThanGrowingForever.
//
// A desktop application left open for a week must not fill a disk, and the run
// *before* the one that misbehaved is routinely the interesting one — which is
// the argument for one generation rather than none or many.
func TestTheLogIsSetAsideRatherThanGrowingForever(t *testing.T) {
	dir := stateDir(t)
	previous := log.Writer()
	t.Cleanup(func() { log.SetOutput(previous) })

	path := filepath.Join(dir, logFileName)
	if err := os.WriteFile(path, make([]byte, logSizeCap+1), 0o600); err != nil {
		t.Fatal(err)
	}
	got, file := logToFile()
	if got != path {
		t.Fatalf("logToFile returned %q, want %q", got, path)
	}
	t.Cleanup(func() { _ = file.Close() })

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("an oversized log was not set aside: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("no new log was opened: %v", err)
	}
	if info.Size() > logSizeCap {
		t.Error("the log was reopened onto the oversized file rather than a fresh one")
	}
}

// TestAnUnwritableStateDirectoryDoesNotStopTheConsole. The log is how you find
// out what went wrong; it is not a thing that must go right. Refusing to start
// because a log file could not be opened trades a diagnosable console for none.
func TestAnUnwritableStateDirectoryDoesNotStopTheConsole(t *testing.T) {
	previous := log.Writer()
	t.Cleanup(func() { log.SetOutput(previous) })

	// A path whose parent is a *file*, so neither MkdirAll nor OpenFile can win.
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KT_STATE_DIR", filepath.Join(blocker, "state"))

	if path, file := logToFile(); path != "" {
		_ = file.Close()
		t.Errorf("logToFile claimed %q under a file, which cannot be a directory", path)
	}
}

// TestTheLogStaysUnderItsCapWhileItRuns. Rotation at start-up alone would leave
// a process that runs for weeks with a log bounded by nothing; the writer
// itself sets the file aside when the next line would pass the cap.
func TestTheLogStaysUnderItsCapWhileItRuns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, logFileName)
	const capBytes = 1 << 10
	file, err := openLogFile(path, capBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })

	// 100 KiB from several goroutines at once, as the log package writes.
	line := []byte(strings.Repeat("x", 99) + "\n")
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 128; i++ {
				if n, err := file.Write(line); err != nil || n != len(line) {
					t.Errorf("write: %d, %v", n, err)
					return
				}
			}
		}()
	}
	wg.Wait()

	for _, name := range []string{path, path + ".1"} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Size() > capBytes {
			t.Errorf("%s holds %d bytes, past the cap of %d", name, info.Size(), capBytes)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("the directory holds %d files, want the log and one .1", len(entries))
	}
}

// TestAFailedRotationKeepsLoggingAndTheBound. A rename that cannot happen
// starts the file again rather than letting it grow or stopping the log.
func TestAFailedRotationKeepsLoggingAndTheBound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, logFileName)
	// A non-empty directory where the previous generation goes: no rename
	// onto it can succeed.
	if err := os.MkdirAll(filepath.Join(path+".1", "held"), 0o700); err != nil {
		t.Fatal(err)
	}
	const capBytes = 1 << 10
	file, err := openLogFile(path, capBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })

	for i := 0; i < 100; i++ {
		if _, err := file.Write([]byte(strings.Repeat("y", 99) + "\n")); err != nil {
			t.Fatalf("a write failed once rotation could not rename: %v", err)
		}
	}
	if _, err := file.Write([]byte("the last line\n")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > capBytes {
		t.Errorf("the log holds %d bytes, past the cap of %d", len(raw), capBytes)
	}
	if !strings.HasSuffix(string(raw), "the last line\n") {
		t.Error("the log stopped taking lines once a rotation failed")
	}
}

// TestALineLongerThanTheCapIsClipped. The bound holds for every write, not
// only for ordinary lines.
func TestALineLongerThanTheCapIsClipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), logFileName)
	file, err := openLogFile(path, 64)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	long := []byte(strings.Repeat("z", 200))
	if n, err := file.Write(long); err != nil || n != len(long) {
		t.Fatalf("write: %d, %v", n, err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() > 64 {
		t.Errorf("a long line took the log past its cap: %v, %v", info, err)
	}
}

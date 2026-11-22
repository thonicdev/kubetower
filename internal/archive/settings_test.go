package archive

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The settings the archive holds and the settings on disk are one fact.
//
// If SetSettings assigned the field, released a.mu, and only then wrote, two
// posts arriving together could run A sets, B sets, B writes B, A writes A:
// memory would hold B and settings.json A until the next restart, where the
// file wins and silently reverts a change the operator watched the page
// accept. Log capture back **on** after it was turned off is the case that
// matters, because what that writes is unredacted production log content at
// rest.
//
// And if the gate that decides whether a log line is written did not consult
// Settings.Logs, turning capture off would rest entirely on closeSinks, which
// can only close the sinks registered when it runs — a sink registering a
// moment later would write for the life of its stream.
//
// a.settings is the single authority: the assignment and the file write happen
// under one hold of a.mu, and logsReady asks it before every line, so
// closeSinks is housekeeping (it releases the file handles) rather than the
// thing that enforces the switch.

// readSettingsFile is what the next start will read.
func readSettingsFile(t *testing.T, root string) Settings {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, SettingsFileName))
	if err != nil {
		t.Fatalf("reading the settings file: %v", err)
	}
	var s Settings
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("%s is not readable JSON: %v", SettingsFileName, err)
	}
	return s
}

// Two posts arriving together must not leave memory and disk describing
// different archives — whichever of them wins, it has to win both.
//
// This is a stress test rather than a demonstration of one interleaving,
// because there is no seam inside the write to sequence against. What makes it
// worth having is that it cannot fail at all: the assignment and the write are
// one critical section, so the last caller to hold a.mu is by construction both
// the last in memory and the last on disk.
func TestTheSettingsFileAndMemoryCannotDisagree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "archive")
	a := Open(Options{Root: root, Defaults: Defaults(), Logf: t.Logf})
	defer a.Close()

	const rounds = 50
	for round := 0; round < rounds; round++ {
		var wg sync.WaitGroup
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				if _, err := a.SetSettings(Settings{
					Enabled:       true,
					Logs:          w%2 == 0,
					RetentionDays: MinRetentionDays + w,
				}); err != nil {
					t.Errorf("SetSettings: %v", err)
				}
			}(w)
		}
		wg.Wait()

		want := a.Settings()
		got := readSettingsFile(t, root)
		if got != want {
			t.Fatalf("round %d: %s holds %+v while the archive holds %+v. "+
				"Nothing reconciles them, so the next restart comes up in the state that lost — "+
				"including log capture back on after the operator turned it off",
				round, SettingsFileName, got, want)
		}
	}
}

// The writer's subject is the field, not what a caller handed it.
//
// This is white-box on purpose: a writer taking a Settings argument could be
// called with something other than what a.settings holds. saveSettingsLocked
// takes none, which means a change that reintroduces the argument cannot
// compile without also changing this test.
func TestTheSettingsFileRecordsTheFieldAndNotACallersCopy(t *testing.T) {
	root := filepath.Join(t.TempDir(), "archive")
	a := Open(Options{Root: root, Defaults: Defaults(), Logf: t.Logf})
	defer a.Close()

	want := Settings{Enabled: true, Logs: true, RetentionDays: 19}

	a.mu.Lock()
	a.settings = want
	err := a.saveSettingsLocked()
	a.mu.Unlock()
	if err != nil {
		t.Fatalf("saveSettingsLocked: %v", err)
	}

	if got := readSettingsFile(t, root); got != want {
		t.Errorf("%s holds %+v, want the %+v the archive holds", SettingsFileName, got, want)
	}
}

// A sink the sweep did not reach must stop writing on its own.
//
// The interleaving: a log panel evaluates CapturesLogs() as true, the settings
// POST swaps the settings and closes every sink it can see, and only then does
// the panel's own registration land. Nothing sweeps again, so without the gate
// that sink would write production log content to disk for the life of the
// stream while the page said capture was off.
//
// Reproduced here by moving the switch without the sweep rather than by racing
// for a window a few instructions wide — the resulting state is the same one,
// and it is the state the gate has to refuse. A closeSinks call here would test
// closeSinks, which is not the gate.
func TestASinkTheSweepDidNotReachStopsWritingWhenCaptureIsOff(t *testing.T) {
	a, _ := newArchive(t, on())

	w := a.LogWriter("prod-eks-1", "prod", "api-0", "api")
	defer w.Close()
	if _, ok := w.(discard); ok {
		t.Fatal("LogWriter discarded with capture on, so this test would pass for the wrong reason")
	}
	w.Write([]byte("while it was on\n"))

	a.mu.Lock()
	a.settings.Logs = false
	a.mu.Unlock()

	w.Write([]byte("after capture was turned off\n"))

	lines, err := a.Log(LogQuery{Cluster: "prod-eks-1", Namespace: "prod", Pod: "api-0", Container: "api"})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	for _, l := range lines {
		if strings.Contains(l.Text, "after it was turned off") || strings.Contains(l.Text, "after capture was turned off") {
			t.Fatalf("a line written by a sink the sweep did not reach landed on disk after "+
				"log capture was turned off: %q. The settings page says capture is off and "+
				"unredacted log content is still being kept", l.Text)
		}
	}
	if len(lines) != 1 {
		t.Errorf("%d lines on disk, want the one written while capture was on", len(lines))
	}
}

// The same thing through the real API, racing for the window instead of posing
// it: many handles asked for at the instant the switch moves.
//
// Handles are kept rather than closed, and no second sweep is allowed to run
// before they are used — a later SetSettings(off) would close every escapee and
// the test would pass for the wrong reason. Its power to catch the defect is
// probabilistic; its value as a guard is not, because the gate refuses the line
// whether or not the sink was ever registered.
func TestNoLineLandsAfterTheSwitchHoweverTheCallsInterleave(t *testing.T) {
	a, _ := newArchive(t, on())
	const marker = "after capture was turned off"

	logsOff := Settings{Enabled: true, Logs: false, RetentionDays: 7}

	for round := 0; round < 100; round++ {
		if _, err := a.SetSettings(on()); err != nil {
			t.Fatalf("SetSettings: %v", err)
		}

		handles := make([]io.WriteCloser, 16)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for g := range handles {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				<-start
				handles[g] = a.LogWriter("prod-eks-1", "prod", "api-0", "api")
			}(g)
		}
		close(start)
		if _, err := a.SetSettings(logsOff); err != nil {
			t.Fatalf("SetSettings: %v", err)
		}
		wg.Wait()

		// Capture is off and every call has returned. Nothing written from here
		// may reach the disk.
		for _, h := range handles {
			h.Write([]byte(marker + "\n"))
			h.Close()
		}
	}

	day := "2026-08-17" // the pinned clock in newArchive
	path := filepath.Join(a.Root(), day, "prod-eks-1", logsDirName, "prod", "api-0", "api.log")
	if body := readAll(t, path); strings.Contains(body, marker) {
		t.Fatalf("%s holds a line written after log capture was turned off", path)
	}
}

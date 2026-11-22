//go:build windows

package terminal

// The read-only fence probe, end to end, in PowerShell.
//
// `fence_test.go` skips its real-shell tests on this platform, but sessions do
// open on Windows (pty_windows.go). So the probe's PowerShell branch
// (`fenceScript`) needs the same treatment its POSIX sibling gets, and it needs
// it more, because three things on this platform can each turn a correctly
// written override into one that protects nothing and none of them is visible
// from the source: `PATHEXT` not resolving the file, an execution policy
// refusing it, and a profile re-pointing `$env:PATH` after us.
//
// Nothing here needs kubectl, helm, or a cluster: the stand-in is a `.cmd` that
// echoes one word, exactly as fence_test.go's is a `#!/bin/sh` that does.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheProbeReadsARealPowerShellAndARealCmdOverride(t *testing.T) {
	shell := []string{windowsTestShell(t), "-NoLogo", "-NoProfile"}
	if shellFamilyOf(shell) != familyPowerShell {
		t.Fatalf("shellFamilyOf did not recognize %q as PowerShell, so the probe would run a "+
			"POSIX script through it", shell[0])
	}

	// The stand-in binaries. `--help` on a real kubectl prints a help text and
	// contacts nothing; these print one word, which is what tells "the override
	// refused" from "the call got through".
	real := t.TempDir()
	for _, name := range fenceBinaries {
		if err := os.WriteFile(filepath.Join(real, name+".cmd"),
			[]byte("@echo off\r\necho REACHED_REAL_BINARY\r\n"), 0o644); err != nil {
			t.Fatalf("stand-in %s: %v", name, err)
		}
	}

	t.Run("with the override in front", func(t *testing.T) {
		roBin := t.TempDir()
		for _, name := range fenceBinaries {
			if err := os.WriteFile(filepath.Join(roBin, shimFileName(name)),
				[]byte(shimContent(name, filepath.Join(real, name+".cmd"))), shimMode); err != nil {
				t.Fatalf("override: %v", err)
			}
		}

		// **The probe's own output is asserted and not only its verdict**.
		// `classifyFenceProbe` reports no hole in two cases — the override
		// refused, or nothing resolved at all — so a probe that resolves neither
		// binary reads as `intact`. With no `PATHEXT` in the environment,
		// PowerShell does not consider a `.cmd`, `kubectl` is "not recognized",
		// and a verdict-only assertion stays green about a fence nothing has
		// established. See fenceProbeMachineVars.
		var saw []string
		run := fenceRun(shell)
		f := measureFence(shell, roBin, real, func(argv, env []string) (string, error) {
			out, err := run(argv, env)
			saw = append(saw, out)
			return out, err
		})

		if !f.Measured {
			t.Fatalf("the probe could not run in PowerShell, so this platform's fence is never "+
				"established: %+v", f)
		}
		if !f.Intact {
			t.Fatalf("a real .cmd override in front of a real binary was not read as a fence: %+v", f)
		}
		for i, out := range saw {
			if !strings.Contains(out, fenceRefusal) {
				t.Errorf("probe %d did not make the override refuse, so `intact` here means "+
					"`nothing resolved`:\n%s", i, out)
			}
			if strings.Contains(out, "REACHED_REAL_BINARY") {
				t.Errorf("probe %d reached the real binary through the override:\n%s", i, out)
			}
		}
	})

	// An override written under the POSIX spelling is not resolved through
	// PATHEXT, so the session runs the real binary. This
	// is what that looks like to the probe, and it must be "absent" rather than
	// "intact" — a fence believed and missing is the one thing this file exists
	// to prevent.
	t.Run("with the override written the POSIX way", func(t *testing.T) {
		roBin := t.TempDir()
		for _, name := range fenceBinaries {
			if err := os.WriteFile(filepath.Join(roBin, name),
				[]byte(buildReadOnlyShim(name, filepath.Join(real, name+".cmd"))), 0o755); err != nil {
				t.Fatalf("override: %v", err)
			}
		}
		f := measureFence(shell, roBin, real, fenceRun(shell))
		if f.Intact {
			t.Fatalf("an extensionless `#!/bin/sh` override was read as a fence on Windows, which "+
				"is a missing fence reported as present: %+v", f)
		}
		if f.State() != "absent" {
			t.Fatalf("state %q; a session that reaches the real binary is an absent fence, not an "+
				"unmeasured one: %+v", f.State(), f)
		}
	})

	t.Run("with no override at all", func(t *testing.T) {
		f := measureFence(shell, t.TempDir(), real, fenceRun(shell))
		if f.State() != "absent" {
			t.Fatalf("an empty override directory with a reachable binary was read as %q: %+v",
				f.State(), f)
		}
		if !strings.Contains(f.Detail, "none was installed") {
			t.Errorf("the sentence does not name the cause: %q", f.Detail)
		}
	})

	// A session that resolves neither binary is not a hole, and not intact
	// either: nothing refused anything, so nothing was established.
	t.Run("with nothing to fence", func(t *testing.T) {
		f := measureFence(shell, t.TempDir(), t.TempDir(), fenceRun(shell))
		if f.Intact || f.State() != "unmeasured" {
			t.Fatalf("a session that resolves neither binary was reported as %q: %+v", f.State(), f)
		}
	})
}

// The probe must terminate, and on this platform that is one flag away from not
// terminating.
//
// A Claude session's argv carries `-NoExit` so the operator lands at a prompt
// when Claude leaves; the probe runs at start-up, twice, and `-NoExit` there
// would hold KubeTower's own launch open until the probe timeout. `fenceRun` uses
// `scriptArgv` for exactly that reason and this is what keeps it there.
func TestTheProbeArgvDoesNotHoldTheShellOpen(t *testing.T) {
	shell := []string{`C:\pwsh.exe`, "-NoLogo", "-NoProfile"}

	probe := strings.Join(scriptArgv(shell, "Write-Output 1"), " ")
	if strings.Contains(probe, "-NoExit") {
		t.Errorf("the fence probe's command line keeps the shell open, so every start-up waits "+
			"out the probe timeout twice: %s", probe)
	}
	if !strings.Contains(probe, "-Command") {
		t.Errorf("the fence probe does not hand PowerShell a script at all: %s", probe)
	}

	// The session's own argv is the control: it *must* carry it, or Claude
	// exiting closes the pane instead of leaving a shell on the cluster.
	session := strings.Join(claudeSessionArgv(shell, "prod", false), " ")
	if !strings.Contains(session, "-NoExit") {
		t.Errorf("a Claude session's shell does not stay open, so the pane closes when Claude "+
			"exits rather than becoming an ordinary shell: %s", session)
	}
}

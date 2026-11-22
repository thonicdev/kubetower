package terminal

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// An empty argument must not end the scan.
//
// A Windows shim loop terminating on `if "%~1"==""` would let `kubectl
// --context "" delete ns X` reach the explicitly-empty argument (`%~1` strips
// quotes), match the end-of-arguments test, and jump to the real binary
// **without examining `delete`**. The POSIX shim iterates `for arg in "$@"`
// and sees every argument including the empty ones; the Windows one must too.

// TestTheWindowsShimTerminatesOnAbsenceAndNotOnEmptiness reads the template.
//
// Its own assertion rather than only the execution below, because the execution
// can only run on Windows, which is not always the machine running the suite.
func TestTheWindowsShimTerminatesOnAbsenceAndNotOnEmptiness(t *testing.T) {
	shim := buildReadOnlyShimCmd("kubectl", `C:\bin\kubectl.exe`)

	if strings.Contains(shim, `if "%~1"=="" goto kubetower_run`) {
		t.Error(`the scan ends on "%~1"=="", which an explicitly empty argument satisfies: ` +
			`kubectl --context "" delete ns X then reaches the real binary unexamined`)
	}
	if !strings.Contains(shim, `if "%1"=="" goto kubetower_run`) {
		t.Error(`the scan does not end on "%1"=="", so either the loop never terminates or ` +
			"it terminates on the wrong thing")
	}
	// The comparison must still strip the quotes, or no verb ever matches and
	// the whole shim becomes a pass-through -- which satisfies the two
	// assertions above and is worse than the bug.
	if !strings.Contains(shim, `"%~1"=="%%V"`) {
		t.Error("the verb comparison no longer strips quotes, so the denylist matches nothing")
	}
}

// TestAnEmptyArgumentDoesNotSmuggleAVerbPastTheShim runs it.
//
// Windows only, because a `.cmd` needs cmd.exe. The shim calls a stand-in for
// the real binary so that a failure to refuse is *visible* -- a test that only
// checked the exit code could not tell "refused" from "the real kubectl is not
// installed".
func TestAnEmptyArgumentDoesNotSmuggleAVerbPastTheShim(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the .cmd shim needs cmd.exe")
	}
	dir := t.TempDir()

	// The stand-in: reaching it at all is the failure.
	real := filepath.Join(dir, "real.cmd")
	if err := os.WriteFile(real, []byte("@echo off\r\necho REACHED-THE-REAL-BINARY\r\n"), 0o700); err != nil {
		t.Fatalf("writing the stand-in: %v", err)
	}
	shim := filepath.Join(dir, "kubectl.cmd")
	if err := os.WriteFile(shim, []byte(buildReadOnlyShimCmd("kubectl", real)), 0o700); err != nil {
		t.Fatalf("writing the shim: %v", err)
	}

	cases := []struct {
		name    string
		args    []string
		refused bool
	}{
		{"the plain mutation", []string{"delete", "ns", "demo"}, true},
		{"a verb behind an empty argument", []string{"--context", "", "delete", "ns", "demo"}, true},
		{"a verb behind an empty namespace", []string{"-n", "", "delete", "pod", "api-0"}, true},
		{"two empty arguments first", []string{"", "", "apply", "-f", "x.yaml"}, true},
		// The other direction, and it is the one a heavy-handed fix breaks: a
		// read with an empty argument in it is an ordinary thing to run and must
		// still reach the real binary.
		{"a read is still a read", []string{"--context", "", "get", "pods"}, false},
		{"an ordinary read", []string{"get", "pods", "-n", "test"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := exec.Command("cmd.exe", append([]string{"/c", shim}, tc.args...)...).CombinedOutput()
			body := string(out)
			reached := strings.Contains(body, "REACHED-THE-REAL-BINARY")
			said := strings.Contains(body, "read-only")

			if tc.refused {
				if reached {
					t.Errorf("the mutation reached the real binary: %q", body)
				}
				if !said {
					t.Errorf("it was not refused with a reason: %q", body)
				}
				return
			}
			if !reached {
				t.Errorf("a read was refused, which is how the markings that matter come to "+
					"be ignored: %q", body)
			}
		})
	}
}

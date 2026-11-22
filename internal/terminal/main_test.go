package terminal

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points the temporary directory - and with it workRoot(), which New
// uses when a test gives no WorkRoot - at a directory of this run's own.
//
// New sweeps its work root (sweepSessions), and nineteen New calls in this
// package give none, so every run would otherwise sweep the developer's real
// %TEMP%\kubetower or $TMPDIR/kubetower-<uid> - the root a running KubeTower's
// open sessions are in. Here a test can only ever sweep a
// root this run made, which it removes at the end.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "kubetower-terminal-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "making the test's temporary root:", err)
		os.Exit(1)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "securing the test's temporary root:", err)
		os.Exit(1)
	}
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		_ = os.Setenv(name, root)
	}
	// workRoot prefers $XDG_RUNTIME_DIR, which would be the developer's own.
	_ = os.Unsetenv("XDG_RUNTIME_DIR")
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}

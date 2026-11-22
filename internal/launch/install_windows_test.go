//go:build windows

package launch

import (
	"strings"
	"testing"
)

// TestInstallRefusesOnWindows. The setup is the installer on Windows:
// `--install` must fail, and say where to go, rather than do nothing quietly.
func TestInstallRefusesOnWindows(t *testing.T) {
	err := runInstall()
	if err == nil {
		t.Fatal("--install succeeded on Windows, where the setup is the installer")
	}
	if !strings.Contains(err.Error(), "setup.exe") {
		t.Errorf("the refusal does not name the setup: %v", err)
	}
}

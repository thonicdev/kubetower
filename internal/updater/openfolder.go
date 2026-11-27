package updater

import (
	"os/exec"
	"runtime"

	"github.com/thonicdev/kubetower/safego"
)

// openFolder shows dir in the platform's file manager. A variable, so a test
// records the folder rather than opening a window.
var openFolder = func(dir string) error {
	name := "xdg-open"
	switch runtime.GOOS {
	case "windows":
		name = "explorer.exe"
	case "darwin":
		name = "open"
	}
	tool, err := systemTool(name)
	if err != nil {
		return err
	}
	cmd := exec.Command(tool, dir)
	if err := cmd.Start(); err != nil {
		return err
	}
	// explorer.exe exits 1 after opening a window: only a failure to start counts.
	safego.Go("open the update folder", func() error { _ = cmd.Wait(); return nil }, nil)
	return nil
}

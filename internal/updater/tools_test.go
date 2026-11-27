package updater

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// A system program is found where the system keeps it, never through PATH
// (#178): a folder put first in PATH cannot choose what an update runs.
func TestASystemToolIsNeverFoundThroughPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Windows candidates are checked below")
	}
	dir := t.TempDir()
	name := "kubetower-test-tool"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if got, err := systemTool(name); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("found %q through PATH (err %v)", got, err)
	}
	// And sh, which every Unix keeps in /bin or /usr/bin, is found there.
	got, err := systemTool("sh")
	if err != nil || !strings.HasPrefix(got, "/") || strings.HasPrefix(got, dir) {
		t.Errorf("sh: %q %v", got, err)
	}
}

func TestTheSystemToolsAreLookedForInTheSystemsFolders(t *testing.T) {
	for _, c := range []struct {
		goos, name, root string
		want             []string
	}{
		{"windows", "powershell.exe", `D:\Win`, []string{`D:\Win\System32\WindowsPowerShell\v1.0\powershell.exe`}},
		{"windows", "explorer.exe", "", []string{`C:\Windows\explorer.exe`}},
		{"windows", "explorer.exe", `Windows`, []string{`C:\Windows\explorer.exe`}},
		{"windows", "explorer.exe", `.\w`, []string{`C:\Windows\explorer.exe`}},
		{"windows", "explorer.exe", `\\srv\win`, []string{`\\srv\win\explorer.exe`}},
		{"linux", "xdg-open", "", []string{"/usr/bin/xdg-open", "/bin/xdg-open", "/usr/sbin/xdg-open", "/sbin/xdg-open"}},
		{"darwin", "open", "", []string{"/usr/bin/open", "/bin/open", "/usr/sbin/open", "/sbin/open"}},
	} {
		if got := toolCandidates(c.goos, c.name, c.root); !slices.Equal(got, c.want) {
			t.Errorf("%s %s: %v, want %v", c.goos, c.name, got, c.want)
		}
	}
}
